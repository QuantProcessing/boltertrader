package perp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	sdk "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
	sdkperp "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/perp"
	"github.com/gorilla/websocket"
	"github.com/shopspring/decimal"
)

func TestHyperliquidPerpAdapterStartPreservesPrivateFillSnapshotFlag(t *testing.T) {
	const accountAddress = "0xabc"
	serverErrors := make(chan error, 1)
	subscriptions := make(chan map[string]any, 3)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			serverErrors <- fmt.Errorf("upgrade websocket: %w", err)
			return
		}
		defer conn.Close()
		for subscription := 0; subscription < 3; subscription++ {
			_ = conn.SetReadDeadline(time.Now().Add(time.Second))
			_, raw, err := conn.ReadMessage()
			if err != nil {
				serverErrors <- fmt.Errorf("read subscription %d: %w", subscription+1, err)
				return
			}
			var req sdk.WsSubscribeRequest
			if err := json.Unmarshal(raw, &req); err != nil {
				serverErrors <- fmt.Errorf("decode subscription %d: %w", subscription+1, err)
				return
			}
			encoded, _ := json.Marshal(req.Subscription)
			var fields map[string]any
			_ = json.Unmarshal(encoded, &fields)
			subscriptions <- fields
			if err := writePerpSubscriptionACK(conn, raw); err != nil {
				serverErrors <- err
				return
			}
			if subscription == 1 {
				// Hyperliquid may send the initial userFills snapshot as soon
				// as that individual subscription is acknowledged, before the
				// final account-state subscription is ready. Adapter.Start must buffer
				// rather than drop this legitimate startup snapshot.
				if err := writePerpUserFill(conn, accountAddress, 99, true); err != nil {
					serverErrors <- err
					return
				}
			}
		}
		if err := writePerpUserFill(conn, accountAddress, 100, false); err != nil {
			serverErrors <- err
			return
		}
		_ = conn.SetReadDeadline(time.Time{})
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(context.Background())
	base := sdk.NewWebsocketClient(ctx).WithURL("ws" + strings.TrimPrefix(server.URL, "http"))
	base.AccountAddr = accountAddress
	ws := sdkperp.NewWebsocketClient(base)
	provider := testProvider(t)
	clk := clock.NewRealClock()
	exec := newExecutionClient(nil, provider, clk, "HL:test")
	acct := newAccountClient(nil, provider, clk, "cross", decimal.NewFromInt(1), sdk.AccountAbstractionDefault, "HL:test")
	adapter := &Adapter{Execution: exec, Account: acct, provider: provider, ws: ws, exec: exec, acct: acct, clk: clk}
	t.Cleanup(func() {
		cancel()
		ws.Close()
		_ = exec.Close()
		_ = acct.Close()
	})

	if err := adapter.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	for index, wantType := range []string{"orderUpdates", "userFills", "clearinghouseState"} {
		select {
		case subscription := <-subscriptions:
			if subscription["type"] != wantType {
				t.Fatalf("subscription %d type=%v, want %s", index+1, subscription["type"], wantType)
			}
			if wantType == "clearinghouseState" && subscription["dex"] != "" {
				t.Fatalf("clearinghouseState dex=%v, want canonical empty dex", subscription["dex"])
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for subscription %d", index+1)
		}
	}
	for i, wantSnapshot := range []bool{true, false} {
		select {
		case env := <-exec.Events():
			fill, ok := env.Payload.(contract.FillEvent)
			if !ok {
				t.Fatalf("event %d payload=%T, want FillEvent", i+1, env.Payload)
			}
			if fill.Fill.TradeID != fmt.Sprint(99+i) || env.Source != contract.SourceAdapterStream || !env.Flags.Has(contract.EventFlagFromStream) || env.Flags.Has(contract.EventFlagFromSnapshot) != wantSnapshot {
				t.Fatalf("event %d fill=%+v meta=%+v, want trade=%d snapshot=%v", i+1, fill.Fill, env.Meta(), 99+i, wantSnapshot)
			}
		case err := <-serverErrors:
			t.Fatal(err)
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for private fill event %d", i+1)
		}
	}
}

func writePerpUserFill(conn *websocket.Conn, user string, tradeID int64, snapshot bool) error {
	return conn.WriteJSON(map[string]any{
		"channel": "userFills",
		"data": map[string]any{
			"user": user, "isSnapshot": snapshot,
			"fills": []any{map[string]any{
				"coin": "BTC", "px": "65000", "sz": "0.01", "side": "B", "time": int64(1700000000123),
				"hash": "0xhash", "oid": 555, "crossed": true, "fee": "-0.01", "feeToken": "USDC", "tid": tradeID,
			}},
		},
	})
}

func writePerpSubscriptionACK(conn *websocket.Conn, raw []byte) error {
	var req sdk.WsSubscribeRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return fmt.Errorf("decode subscription: %w", err)
	}
	return conn.WriteJSON(map[string]any{
		"channel": "subscriptionResponse",
		"data":    req.Subscription,
	})
}

func TestHyperliquidPerpStartRollsBackThirdSubscriptionRejectionAndRetriesCleanly(t *testing.T) {
	const accountAddress = "0xAbC"
	var connections atomic.Int32
	secondReady := make(chan struct{})
	releaseEvent := make(chan struct{})
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		connection := connections.Add(1)
		for subscription := 0; subscription < 3; subscription++ {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if connection == 1 && subscription == 2 {
				_ = writePerpUserFill(conn, strings.ToLower(accountAddress), 801, false)
				_ = conn.WriteJSON(map[string]any{"channel": "error", "data": "subscription rejected"})
				for {
					if _, _, err := conn.ReadMessage(); err != nil {
						return
					}
				}
			}
			if err := writePerpSubscriptionACK(conn, raw); err != nil {
				return
			}
		}
		close(secondReady)
		<-releaseEvent
		_ = writePerpUserFill(conn, strings.ToLower(accountAddress), 802, false)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(context.Background())
	base := sdk.NewWebsocketClient(ctx).WithURL("ws" + strings.TrimPrefix(server.URL, "http"))
	base.AccountAddr = accountAddress
	base.SubscriptionAckTimeout = 50 * time.Millisecond
	base.ReconnectWait = 5 * time.Millisecond
	ws := sdkperp.NewWebsocketClient(base)
	provider := testProvider(t)
	clk := clock.NewRealClock()
	exec := newExecutionClient(nil, provider, clk, "HL:test")
	acct := newAccountClient(nil, provider, clk, "cross", decimal.NewFromInt(1), sdk.AccountAbstractionDefault, "HL:test")
	adapter := &Adapter{Execution: exec, Account: acct, provider: provider, ws: ws, exec: exec, acct: acct, clk: clk}
	t.Cleanup(func() {
		cancel()
		ws.Close()
		_ = exec.Close()
		_ = acct.Close()
	})

	if err := adapter.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "subscription rejected") {
		t.Fatalf("first Start err=%v, want third-subscription rejection", err)
	}
	time.Sleep(50 * time.Millisecond)
	if got := connections.Load(); got != 1 {
		t.Fatalf("connections=%d, failed Start must not reconnect", got)
	}
	select {
	case event := <-exec.Events():
		t.Fatalf("failed Start emitted private event: %+v", event)
	default:
	}

	if err := adapter.Start(context.Background()); err != nil {
		t.Fatalf("retry Start: %v", err)
	}
	select {
	case <-secondReady:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for retry subscriptions")
	}
	close(releaseEvent)
	select {
	case env := <-exec.Events():
		fill, ok := env.Payload.(contract.FillEvent)
		if !ok || fill.Fill.TradeID != "802" {
			t.Fatalf("retry event=%+v, want mixed-case-address fill 802", env)
		}
	case <-time.After(time.Second):
		t.Fatal("retry Start did not activate private handlers")
	}
}

func TestHyperliquidPerpPrivateReconnectEmitsOneGapPairForStandardAndHIP3(t *testing.T) {
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
		for subscription := 0; subscription < 3; subscription++ {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if err := writePerpSubscriptionACK(conn, raw); err != nil {
				return
			}
		}
		if connection == 1 {
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "rotate"), time.Now().Add(time.Second))
			return
		}
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(context.Background())
	base := sdk.NewWebsocketClient(ctx).WithURL("ws" + strings.TrimPrefix(server.URL, "http"))
	base.AccountAddr = "0xabc"
	base.ReconnectWait = 10 * time.Millisecond
	ws := sdkperp.NewWebsocketClient(base)
	provider := testProvider(t)
	clk := clock.NewRealClock()
	exec := newExecutionClient(nil, provider, clk, "HL:test")
	acct := newAccountClient(nil, provider, clk, "cross", decimal.NewFromInt(1), sdk.AccountAbstractionDefault, "HL:test")
	adapter := &Adapter{
		Execution: exec,
		Account:   acct,
		provider:  provider,
		ws:        ws,
		exec:      exec,
		acct:      acct,
		clk:       clk,
	}
	t.Cleanup(func() {
		cancel()
		ws.Close()
		_ = exec.Close()
		_ = acct.Close()
	})

	if err := adapter.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	assertHyperliquidPerpGapEnvelope(t, exec.Events(), contract.StreamGapStarted)
	assertHyperliquidPerpGapEnvelope(t, exec.Events(), contract.StreamGapRecovered)
	if got := connections.Load(); got != 2 {
		t.Fatalf("websocket connections=%d, want 2", got)
	}
	select {
	case event := <-exec.Events():
		t.Fatalf("duplicate private gap envelope: %+v", event)
	case <-time.After(50 * time.Millisecond):
	}
}

func assertHyperliquidPerpGapEnvelope(t *testing.T, events <-chan contract.ExecEnvelope, phase contract.StreamGapPhase) {
	t.Helper()
	select {
	case envelope := <-events:
		event, ok := envelope.Payload.(contract.StreamGapEvent)
		if !ok {
			t.Fatalf("payload=%T, want StreamGapEvent", envelope.Payload)
		}
		if event.Venue != venueName || event.AccountID != "HL:test" || event.StreamID != "hyperliquid:perp:private" || event.Generation != 1 || event.Phase != phase {
			t.Fatalf("gap event=%+v", event)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s gap envelope", phase)
	}
}
