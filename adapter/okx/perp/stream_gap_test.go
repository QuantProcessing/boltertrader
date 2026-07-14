package perp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/gorilla/websocket"
)

func TestOKXPerpPrivateReconnectEmitsGapEnvelopes(t *testing.T) {
	var connections atomic.Int32
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer conn.Close()
		connection := connections.Add(1)
		subscriptions := 0
		for {
			var request struct {
				ID json.RawMessage `json:"id"`
				Op string          `json:"op"`
			}
			if err := conn.ReadJSON(&request); err != nil {
				return
			}
			switch request.Op {
			case "login":
				if err := conn.WriteJSON(map[string]any{"event": "login", "code": "0"}); err != nil {
					return
				}
			case "subscribe":
				subscriptions++
				var id int64
				if err := json.Unmarshal(request.ID, &id); err != nil {
					t.Errorf("decode subscription id: %v", err)
					return
				}
				if err := conn.WriteJSON(map[string]any{"id": strconv.FormatInt(id, 10), "event": "subscribe", "code": "0"}); err != nil {
					return
				}
				if connection == 1 && subscriptions == 2 {
					_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "rotate"), time.Now().Add(time.Second))
					return
				}
			}
		}
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(context.Background())
	provider := newInstrumentProvider()
	exec := newExecutionClient(nil, provider, clock.NewRealClock(), defaultDerivativeTdMode, "OKX:test")
	acct := newAccountClient(nil, provider, clock.NewRealClock(), defaultDerivativeTdMode, "OKX:test")
	adapter := &Adapter{
		Execution:    exec,
		Account:      acct,
		provider:     provider,
		exec:         exec,
		acct:         acct,
		apiKey:       "key",
		apiSecret:    "secret",
		passphrase:   "pass",
		wsPrivateURL: "ws" + strings.TrimPrefix(server.URL, "http"),
		wsCtx:        ctx,
		cancel:       cancel,
	}
	t.Cleanup(func() {
		cancel()
		if adapter.wsPrivate != nil {
			adapter.wsPrivate.Close()
		}
		_ = exec.Close()
		_ = acct.Close()
	})
	if err := adapter.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	assertOKXPerpGapEnvelope(t, exec.Events(), contract.StreamGapStarted, time.Second)
	assertOKXPerpGapEnvelope(t, exec.Events(), contract.StreamGapRecovered, 8*time.Second)
	if got := connections.Load(); got != 2 {
		t.Fatalf("websocket connections=%d, want 2", got)
	}
}

func assertOKXPerpGapEnvelope(t *testing.T, events <-chan contract.ExecEnvelope, phase contract.StreamGapPhase, timeout time.Duration) {
	t.Helper()
	select {
	case envelope := <-events:
		event, ok := envelope.Payload.(contract.StreamGapEvent)
		if !ok {
			t.Fatalf("payload=%T, want StreamGapEvent", envelope.Payload)
		}
		if event.Venue != venueName || event.AccountID != "OKX:test" || event.StreamID != "okx:perp:private" || event.Generation != 1 || event.Phase != phase {
			t.Fatalf("gap event=%+v", event)
		}
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for %s gap envelope", phase)
	}
}
