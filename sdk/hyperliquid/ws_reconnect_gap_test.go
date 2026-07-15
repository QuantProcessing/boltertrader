package hyperliquid

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestWaitSubscriptionAckAcceptsExactAckObservedBeforeConnectionRotation(t *testing.T) {
	client := NewWebsocketClient(context.Background())
	t.Cleanup(client.Close)

	acknowledgedConn := &websocket.Conn{}
	waiter := make(chan error, 1)
	waiter <- nil

	// The read loop resolves waiters from the exact connection before it handles
	// that connection's subsequent close frame. Once that exact ACK is queued,
	// a concurrent disconnect must be handled by the reconnect lifecycle rather
	// than retroactively turning the acknowledged subscription into a failure.
	client.Conn = nil
	if err := client.waitSubscriptionAck(acknowledgedConn, "exact-ack", waiter); err != nil {
		t.Fatalf("exact acknowledgement was lost after connection rotation: %v", err)
	}
}

func TestReconnectRequiresExactConnectionSubscriptionACK(t *testing.T) {
	for _, tt := range []struct {
		name        string
		secondMode  string
		wantRecover bool
	}{
		{name: "missing ack", secondMode: "missing"},
		{name: "rejected", secondMode: "rejected"},
		{name: "acknowledged", secondMode: "ack", wantRecover: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var connections atomic.Int32
			replaySeen := make(chan struct{}, 1)
			upgrader := websocket.Upgrader{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer conn.Close()
				connection := connections.Add(1)
				_, raw, err := conn.ReadMessage()
				if err != nil {
					return
				}
				if connection == 1 {
					if err := writeSubscriptionACK(conn, raw); err != nil {
						return
					}
					_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "rotate"), time.Now().Add(time.Second))
					return
				}
				select {
				case replaySeen <- struct{}{}:
				default:
				}
				switch tt.secondMode {
				case "ack":
					if err := writeSubscriptionACK(conn, raw); err != nil {
						return
					}
				case "rejected":
					_ = conn.WriteJSON(map[string]any{"channel": "error", "data": "subscription rejected"})
				}
				for {
					if _, _, err := conn.ReadMessage(); err != nil {
						return
					}
				}
			}))
			t.Cleanup(server.Close)

			ctx, cancel := context.WithCancel(context.Background())
			client := NewWebsocketClient(ctx).WithURL("ws" + strings.TrimPrefix(server.URL, "http"))
			client.ReconnectWait = 5 * time.Millisecond
			client.SubscriptionAckTimeout = 30 * time.Millisecond
			t.Cleanup(func() {
				cancel()
				client.Close()
			})
			recovered := make(chan struct{}, 1)
			client.SetReconnectHooks(func(error) {}, func() { recovered <- struct{}{} })
			if err := client.Connect(); err != nil {
				t.Fatalf("Connect: %v", err)
			}
			if err := client.Subscribe("orderUpdates", map[string]any{"type": "orderUpdates", "user": "0xabc"}, func(WsMessage) {}); err != nil {
				t.Fatalf("Subscribe: %v", err)
			}
			select {
			case <-replaySeen:
			case <-time.After(time.Second):
				t.Fatal("timed out waiting for replay request")
			}
			if tt.wantRecover {
				select {
				case <-recovered:
				case <-time.After(time.Second):
					t.Fatal("timed out waiting for ACK-gated recovery")
				}
				return
			}
			select {
			case <-recovered:
				t.Fatalf("recovered after %s subscription", tt.secondMode)
			case <-time.After(100 * time.Millisecond):
			}
			if tt.secondMode == "missing" && connections.Load() < 3 {
				t.Fatalf("connections=%d, want ACK timeout to drop and retry the unconfirmed socket", connections.Load())
			}
		})
	}
}

func TestReconnectWaitsForEveryReplayACK(t *testing.T) {
	var connections atomic.Int32
	secondReplaySeen := make(chan struct{}, 1)
	releaseSecondACK := make(chan struct{})
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		connection := connections.Add(1)
		for i := 0; i < 2; i++ {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if connection == 2 && i == 1 {
				secondReplaySeen <- struct{}{}
				<-releaseSecondACK
			}
			if err := writeSubscriptionACK(conn, raw); err != nil {
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
	client := NewWebsocketClient(ctx).WithURL("ws" + strings.TrimPrefix(server.URL, "http"))
	client.ReconnectWait = 5 * time.Millisecond
	client.SubscriptionAckTimeout = time.Second
	t.Cleanup(func() {
		cancel()
		client.Close()
	})
	recovered := make(chan struct{}, 1)
	client.SetReconnectHooks(func(error) {}, func() { recovered <- struct{}{} })
	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	for _, sub := range []map[string]any{
		{"type": "orderUpdates", "user": "0xabc"},
		{"type": "userFills", "user": "0xabc"},
	} {
		if err := client.Subscribe(sub["type"].(string), sub, func(WsMessage) {}); err != nil {
			t.Fatalf("Subscribe(%s): %v", sub["type"], err)
		}
	}
	select {
	case <-secondReplaySeen:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for second replay payload")
	}
	select {
	case <-recovered:
		t.Fatal("recovered before every replay payload was acknowledged")
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseSecondACK)
	select {
	case <-recovered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for recovery after all replay ACKs")
	}
}

func TestStaleConnectionSubscriptionACKCannotSatisfyCurrentWaiter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := NewWebsocketClient(ctx)
	oldConn := &websocket.Conn{}
	currentConn := &websocket.Conn{}
	client.Mu.Lock()
	client.Conn = currentConn
	client.Mu.Unlock()
	subscription := map[string]any{"type": "orderUpdates", "user": "0xabc"}
	key := subscriptionKey(subscription)
	waiter, err := client.registerSubscriptionAck(currentConn, key)
	if err != nil {
		t.Fatalf("registerSubscriptionAck: %v", err)
	}
	message, err := json.Marshal(map[string]any{
		"channel": "subscriptionResponse",
		"data":    subscription,
	})
	if err != nil {
		t.Fatalf("marshal ACK: %v", err)
	}
	client.handleMessage(oldConn, message)
	select {
	case result := <-waiter:
		t.Fatalf("stale connection satisfied current waiter: %v", result)
	default:
	}
	client.handleMessage(currentConn, message)
	select {
	case result := <-waiter:
		if result != nil {
			t.Fatalf("current connection ACK result=%v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("current connection ACK did not satisfy waiter")
	}
	client.Mu.Lock()
	client.Conn = nil
	client.Mu.Unlock()
}

func writeSubscriptionACK(conn *websocket.Conn, raw []byte) error {
	var req WsSubscribeRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return err
	}
	return conn.WriteJSON(map[string]any{
		"channel": "subscriptionResponse",
		"data":    req.Subscription,
	})
}

type failOnReplaySubscription struct {
	marshalCalls atomic.Int32
}

type switchConnectionOnMarshalSubscription struct {
	client      *WebsocketClient
	replacement *websocket.Conn
	once        sync.Once
}

func (s *switchConnectionOnMarshalSubscription) MarshalJSON() ([]byte, error) {
	s.once.Do(func() {
		s.client.Mu.Lock()
		s.client.Conn = s.replacement
		s.client.Mu.Unlock()
	})
	return []byte(`{"type":"orderUpdates","user":"0xabc"}`), nil
}

func (s *failOnReplaySubscription) MarshalJSON() ([]byte, error) {
	if s.marshalCalls.Add(1) >= 3 {
		return nil, errors.New("forced replay write failure")
	}
	return []byte(`{"type":"orderUpdates","user":"0xabc"}`), nil
}

func TestReconnectDoesNotRecoverWhenAnySubscriptionReplayFails(t *testing.T) {
	var connections atomic.Int32
	secondConnected := make(chan struct{}, 1)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		connection := connections.Add(1)
		if connection == 2 {
			secondConnected <- struct{}{}
		}
		if connection == 1 {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
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
	t.Cleanup(cancel)
	client := NewWebsocketClient(ctx).WithURL("ws" + strings.TrimPrefix(server.URL, "http"))
	client.ReconnectWait = 10 * time.Millisecond
	t.Cleanup(client.Close)
	hooks, ok := any(client).(interface {
		SetReconnectHooks(func(error), func())
	})
	if !ok {
		t.Fatal("websocket does not expose reconnect hooks")
	}
	started := make(chan struct{}, 1)
	var recovered atomic.Bool
	hooks.SetReconnectHooks(func(error) {
		select {
		case started <- struct{}{}:
		default:
		}
	}, func() {
		recovered.Store(true)
	})

	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := client.Subscribe("orderUpdates", &failOnReplaySubscription{}, func(WsMessage) {}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for reconnect start")
	}
	select {
	case <-secondConnected:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for reconnect dial")
	}
	time.Sleep(100 * time.Millisecond)
	if recovered.Load() {
		t.Fatal("reconnect recovered despite a failed subscription replay")
	}
}

func TestSubscriptionReplayDoesNotSwitchToTheReplacementSocketDuringWrite(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	client := NewWebsocketClient(ctx)
	t.Cleanup(client.Close)

	writes := make(chan int32, 2)
	accepted := make(chan int32, 2)
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
		accepted <- connection
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
			writes <- connection
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
	connB := dial()

	client.Mu.Lock()
	client.Conn = connA
	client.subscriptionPayloads["orderUpdates"] = map[string]any{
		"orders": &switchConnectionOnMarshalSubscription{client: client, replacement: connB},
	}
	client.Mu.Unlock()

	if err := client.resubscribeAllOn(connA); err == nil {
		t.Fatal("subscription replay succeeded after its captured connection was replaced during write")
	}
	select {
	case connection := <-writes:
		if connection != 1 {
			t.Fatalf("subscription replay write used connection %d, want captured connection 1", connection)
		}
	case <-time.After(time.Second):
		t.Fatal("captured connection did not receive the replay write")
	}
	select {
	case connection := <-writes:
		t.Fatalf("subscription replay wrote to replacement connection %d", connection)
	case <-time.After(100 * time.Millisecond):
	}

	client.dropConnection(connA)
	if got := client.currentConnection(); got != connB {
		t.Fatal("dropping the failed captured connection also dropped the current connection")
	}
}
