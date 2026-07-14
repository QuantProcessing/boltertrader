package okx

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestPrivateReconnectHooksRecoverAfterLoginAndAllSubscriptions(t *testing.T) {
	var connections atomic.Int32
	var sequenceMu sync.Mutex
	sequence := make([]string, 0, 5)
	appendSequence := func(value string) {
		sequenceMu.Lock()
		sequence = append(sequence, value)
		sequenceMu.Unlock()
	}

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
			if connection == 2 {
				appendSequence(request.Op)
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
	t.Cleanup(cancel)
	client := NewWSClient(ctx).
		WithURL("ws"+strings.TrimPrefix(server.URL, "http")).
		WithCredentials("key", "secret", "pass")
	t.Cleanup(client.Close)
	hooks, ok := any(client).(interface {
		SetReconnectHooks(func(error), func())
	})
	if !ok {
		t.Fatal("private websocket does not expose reconnect hooks")
	}
	recovered := make(chan struct{}, 1)
	hooks.SetReconnectHooks(func(error) {
		appendSequence("started")
	}, func() {
		appendSequence("recovered")
		recovered <- struct{}{}
	})

	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := client.Subscribe(WsSubscribeArgs{Channel: "orders", InstType: "SWAP"}, func([]byte) {}); err != nil {
		t.Fatalf("Subscribe orders: %v", err)
	}
	if err := client.Subscribe(WsSubscribeArgs{Channel: "positions", InstType: "SWAP"}, func([]byte) {}); err != nil {
		t.Fatalf("Subscribe positions: %v", err)
	}

	select {
	case <-recovered:
	case <-time.After(8 * time.Second):
		t.Fatal("timed out waiting for private reconnect recovery")
	}
	sequenceMu.Lock()
	got := append([]string(nil), sequence...)
	sequenceMu.Unlock()
	want := []string{"started", "login", "subscribe", "subscribe", "recovered"}
	if len(got) != len(want) {
		t.Fatalf("reconnect sequence=%v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("reconnect sequence=%v, want %v", got, want)
		}
	}
}

func TestPrivateReplayDoesNotSplitSubscriptionsAcrossReplacementSockets(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	client := NewWSClient(ctx)
	t.Cleanup(client.Close)

	writes := make(chan int32, 4)
	accepted := make(chan int32, 2)
	var connections atomic.Int32
	var connB *websocket.Conn
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer conn.Close()
		connection := connections.Add(1)
		accepted <- connection
		for {
			var request struct {
				ID int64  `json:"id"`
				Op string `json:"op"`
			}
			if err := conn.ReadJSON(&request); err != nil {
				return
			}
			writes <- connection
			ack := []byte(`{"id":"` + strconv.FormatInt(request.ID, 10) + `","event":"subscribe","code":"0"}`)
			if connection == 1 {
				client.mu.Lock()
				requestWaiter := client.PendingReqs[request.ID]
				client.Conn = connB
				if requestWaiter != nil {
					requestWaiter.Success <- ack
				}
				client.mu.Unlock()
				continue
			}
			client.handleMessage(ack)
		}
	}))
	t.Cleanup(server.Close)

	dial := func() *websocket.Conn {
		conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
		if err != nil {
			t.Fatalf("dial websocket: %v", err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		<-accepted
		return conn
	}
	connA := dial()
	connB = dial()

	client.mu.Lock()
	client.Conn = connA
	client.Subs[WsSubscribeArgs{Channel: "orders", InstType: "SWAP"}] = func([]byte) {}
	client.Subs[WsSubscribeArgs{Channel: "positions", InstType: "SWAP"}] = func([]byte) {}
	client.mu.Unlock()

	if err := client.replayPrivateSubscriptionsOn(connA); err == nil {
		t.Fatal("private replay succeeded after its captured connection was replaced")
	}
	select {
	case connection := <-writes:
		if connection != 1 {
			t.Fatalf("first private replay write used connection %d, want captured connection 1", connection)
		}
	case <-time.After(time.Second):
		t.Fatal("captured connection did not receive the first private replay write")
	}
	select {
	case connection := <-writes:
		t.Fatalf("private replay wrote another subscription to connection %d after replacement", connection)
	case <-time.After(100 * time.Millisecond):
	}

	client.dropConnection(connA)
	if got := client.currentConnection(); got != connB {
		t.Fatal("dropping the failed captured connection also dropped the current connection")
	}
	client.mu.Lock()
	client.IsPrivate = true
	client.recovering = true
	client.recoveryGeneration++
	generation := client.recoveryGeneration
	client.authenticatedConn = nil
	client.mu.Unlock()
	client.callbacks.beginGap(generation, nil)
	client.callbacks.activateConnection(generation, connB, true)
	if client.completeReconnect(connB) {
		t.Fatal("private reconnect recovered before the replacement socket was authenticated")
	}
	client.mu.Lock()
	client.authenticatedConn = connB
	client.mu.Unlock()
	if err := client.markConnectionReady(connB); err != nil {
		t.Fatalf("mark replacement ready: %v", err)
	}
	if !client.completeReconnect(connB) {
		t.Fatal("private reconnect did not recover on the captured authenticated socket")
	}
}
