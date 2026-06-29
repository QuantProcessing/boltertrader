package lighter

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gorilla/websocket"
)

func TestWebsocketClient_Connect(t *testing.T) {
	client := newLiveWSClient(t)
	if client.Conn == nil {
		t.Fatal("expected websocket connection")
	}
}

func TestWebsocketClient_Close(t *testing.T) {
	client := newLiveWSClient(t)
	client.Close()
	if client.Conn != nil || client.conn != nil {
		t.Fatal("expected active conn to be cleared")
	}
}

func TestWebsocketClient_Send_Disconnected(t *testing.T) {
	client := NewWebsocketClient(context.Background())
	if err := client.Send(map[string]string{"type": "ping"}); err == nil {
		t.Fatal("expected disconnected Send to fail")
	}
}

func TestWebsocketClient_Subscribe(t *testing.T) {
	client := newLiveWSClient(t)
	if err := client.Subscribe("height", nil, func([]byte) {}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if client.Subscriptions["height"] == nil {
		t.Fatal("expected subscription to be registered")
	}
}

func TestWebsocketClient_Unsubscribe(t *testing.T) {
	client := newLiveWSClient(t)
	if err := client.Subscribe("height", nil, func([]byte) {}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if err := client.Unsubscribe("height"); err != nil {
		t.Fatalf("Unsubscribe: %v", err)
	}
	if client.Subscriptions["height"] != nil {
		t.Fatal("expected subscription to be removed")
	}
}

func TestWebsocketClient_RegisterPendingRequest(t *testing.T) {
	client := NewWebsocketClient(context.Background())
	ch := client.RegisterPendingRequest("req-1")

	if ch == nil || client.PendingRequests["req-1"] != ch {
		t.Fatal("expected pending request channel to be registered")
	}
}

func TestWebsocketClient_UnregisterPendingRequest(t *testing.T) {
	client := NewWebsocketClient(context.Background())
	ch := client.RegisterPendingRequest("req-1")

	client.UnregisterPendingRequest("req-1")

	if _, ok := client.PendingRequests["req-1"]; ok {
		t.Fatal("expected pending request to be removed")
	}
	if _, ok := <-ch; ok {
		t.Fatal("expected pending request channel to be closed")
	}
}

func TestWebsocketClient_HandleMessage(t *testing.T) {
	client := NewWebsocketClient(context.Background())
	called := false
	client.Subscriptions["trade/1"] = &subscription{
		rawHandler: func(data []byte) {
			called = true
			var got map[string]any
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("raw handler payload: %v", err)
			}
			if got["channel"] != "trade:1" {
				t.Fatalf("unexpected channel: %v", got["channel"])
			}
		},
	}

	client.HandleMessage([]byte(`{"type":"update/trade","channel":"trade:1"}`))

	if !called {
		t.Fatal("expected raw subscription handler to be called")
	}
}

func TestEnvelope_Unmarshal(t *testing.T) {
	env := &Envelope{raw: []byte(`{"channel":"trade:1","value":7}`)}
	var got struct {
		Channel string `json:"channel"`
		Value   int    `json:"value"`
	}
	if err := env.Unmarshal(&got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Channel != "trade:1" || got.Value != 7 {
		t.Fatalf("unexpected decoded payload: %+v", got)
	}
}

func TestSubscriber_Dispatch(t *testing.T) {
	var got string
	sub := Subscriber{
		Channel:   "trade/1",
		Callbacks: []Callback{func(data []byte) { got = string(data) }},
	}

	sub.Dispatch([]byte(`{"ok":true}`))

	if got != `{"ok":true}` {
		t.Fatalf("unexpected callback payload: %s", got)
	}
}

func TestWebsocketClient_HandleMessageDeliversPendingTxResponse(t *testing.T) {
	client := NewWebsocketClient(context.Background())
	ch := client.RegisterPendingRequest("req-1")
	defer client.UnregisterPendingRequest("req-1")

	if err := client.handleIncomingFrame(websocket.TextMessage, []byte(`{"id":"req-1","type":"ack","code":200}`)); err != nil {
		t.Fatalf("handleIncomingFrame: %v", err)
	}

	select {
	case resp := <-ch:
		if resp.Code != 200 {
			t.Fatalf("unexpected response: %+v", resp)
		}
	default:
		t.Fatal("expected tx response to be delivered")
	}
}
