package sdk

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestWSClientReconnectsAndResubscribes(t *testing.T) {
	var connects atomic.Int32
	subscribed := make(chan int32, 2)
	upgrader := websocket.Upgrader{}
	stream := "markPrice.BTC_USDC_PERP"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		id := connects.Add(1)
		go func() {
			defer conn.Close()
			var req wsSubscribeRequest
			if err := conn.ReadJSON(&req); err != nil {
				return
			}
			if req.Method == "SUBSCRIBE" && len(req.Params) == 1 && req.Params[0] == stream {
				subscribed <- id
			}
			if id == 1 {
				return
			}
			payload, _ := json.Marshal(map[string]any{"ok": true})
			_ = conn.WriteJSON(StreamEnvelope{Stream: stream, Data: payload})
			time.Sleep(25 * time.Millisecond)
		}()
	}))
	defer server.Close()

	client := NewWSClient()
	client.url = "ws" + strings.TrimPrefix(server.URL, "http")
	defer client.Close()

	received := make(chan struct{}, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Subscribe(ctx, stream, false, func(json.RawMessage) {
		select {
		case received <- struct{}{}:
		default:
		}
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	for i := 0; i < 2; i++ {
		select {
		case <-subscribed:
		case <-ctx.Done():
			t.Fatalf("expected reconnect resubscribe %d: %v", i+1, ctx.Err())
		}
	}

	select {
	case <-received:
	case <-ctx.Done():
		t.Fatalf("expected message after reconnect: %v", ctx.Err())
	}
}
