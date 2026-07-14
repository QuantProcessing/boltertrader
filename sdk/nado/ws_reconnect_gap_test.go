package nado

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

func TestAccountReconnectHooksRecoverAfterAuthenticationAndSubscriptions(t *testing.T) {
	restClient := newWsTestnetRESTClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("type"); got != "contracts" {
			t.Errorf("query type=%q, want contracts", got)
		}
		_, _ = io.WriteString(w, nadoFixtureBody(t, "contracts.json"))
	}))
	restClient, err := restClient.WithCredentials(wsTestPrivateKey, "arb")
	if err != nil {
		t.Fatalf("WithCredentials: %v", err)
	}

	var connections atomic.Int32
	var sequenceMu sync.Mutex
	sequence := make([]string, 0, 4)
	appendSequence := func(value string) {
		sequenceMu.Lock()
		sequence = append(sequence, value)
		sequenceMu.Unlock()
	}
	var upgrader websocket.Upgrader
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer conn.Close()
		connection := connections.Add(1)
		for range 2 {
			var request map[string]any
			if err := conn.ReadJSON(&request); err != nil {
				return
			}
			method, _ := request["method"].(string)
			if connection == 2 {
				appendSequence(method)
			}
			switch method {
			case "authenticate":
				if err := conn.WriteJSON(map[string]any{"id": float64(AuthRequestID)}); err != nil {
					return
				}
			case "subscribe":
				if err := conn.WriteJSON(map[string]any{"id": request["id"], "status": "success"}); err != nil {
					return
				}
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

	client, err := NewWsAccountClient(context.Background(), restClient)
	if err != nil {
		t.Fatalf("NewWsAccountClient: %v", err)
	}
	client.url = wsURLFromHTTP(server.URL)
	t.Cleanup(client.Close)
	hooks, ok := any(client).(interface {
		SetReconnectHooks(func(error), func())
	})
	if !ok {
		t.Fatal("account websocket does not expose reconnect hooks")
	}
	recovered := make(chan struct{}, 1)
	hooks.SetReconnectHooks(func(error) {
		appendSequence("started")
	}, func() {
		appendSequence("recovered")
		recovered <- struct{}{}
	})
	productID := int64(2)
	if err := client.SubscribeOrders(&productID, nil); err != nil {
		t.Fatalf("SubscribeOrders: %v", err)
	}
	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	select {
	case <-recovered:
	case <-time.After(4 * time.Second):
		t.Fatal("timed out waiting for account reconnect recovery")
	}
	sequenceMu.Lock()
	got := append([]string(nil), sequence...)
	sequenceMu.Unlock()
	want := []string{"started", "authenticate", "subscribe", "recovered"}
	if len(got) != len(want) {
		t.Fatalf("reconnect sequence=%v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("reconnect sequence=%v, want %v", got, want)
		}
	}
}

func TestAccountReplayRejectsAStaleCapturedConnectionAndRequiresAuthentication(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	client := &WsAccountClient{
		ctx:           ctx,
		cancel:        cancel,
		subscriptions: make(map[string]*accountSubscription),
		subWaiters:    make(map[int64]chan error),
		Logger:        zap.NewNop().Sugar(),
	}

	writes := make(chan int32, 4)
	accepted := make(chan int32, 2)
	var connections atomic.Int32
	var connB *coderws.Conn
	var upgrader websocket.Upgrader
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
				ID int64 `json:"id"`
			}
			if err := conn.ReadJSON(&request); err != nil {
				return
			}
			writes <- connection
			if connection == 1 {
				client.mu.Lock()
				waiter := client.subWaiters[request.ID]
				client.conn = connB
				if waiter != nil {
					waiter <- nil
				}
				client.mu.Unlock()
				continue
			}
			ack, err := json.Marshal(map[string]any{"id": request.ID, "status": "success"})
			if err != nil {
				t.Errorf("marshal acknowledgement: %v", err)
				return
			}
			client.handleMessage(ack)
		}
	}))
	t.Cleanup(server.Close)

	dial := func() *coderws.Conn {
		conn, _, err := coderws.Dial(ctx, wsURLFromHTTP(server.URL), nil)
		if err != nil {
			t.Fatalf("dial websocket: %v", err)
		}
		t.Cleanup(func() { _ = conn.Close(coderws.StatusNormalClosure, "") })
		<-accepted
		return conn
	}
	connA := dial()
	connB = dial()

	client.mu.Lock()
	client.conn = connA
	client.isConnected = true
	client.isAuthenticated = true
	client.subscriptions["custom-a"] = &accountSubscription{params: StreamParams{Type: "custom-a"}}
	client.subscriptions["custom-b"] = &accountSubscription{params: StreamParams{Type: "custom-b"}}
	client.mu.Unlock()

	if err := client.resubscribeAllOn(connA); err == nil {
		t.Fatal("account replay succeeded after its captured connection was replaced")
	}
	select {
	case connection := <-writes:
		if connection != 1 {
			t.Fatalf("first account replay write used connection %d, want captured connection 1", connection)
		}
	case <-time.After(time.Second):
		t.Fatal("captured connection did not receive the first account replay write")
	}
	select {
	case connection := <-writes:
		t.Fatalf("account replay wrote another subscription to connection %d after replacement", connection)
	case <-time.After(100 * time.Millisecond):
	}

	client.dropConnection(connA)
	client.mu.Lock()
	current := client.conn
	client.subscriptions = map[string]*accountSubscription{
		"orders": {params: StreamParams{Type: "order_update"}},
	}
	client.recovering = true
	client.isAuthenticated = false
	client.mu.Unlock()
	if current != connB {
		t.Fatal("dropping the failed captured connection also dropped the current connection")
	}
	if client.completeReconnectOn(connB) {
		t.Fatal("account reconnect recovered before private subscription authentication")
	}
	client.mu.Lock()
	client.isAuthenticated = true
	client.authenticatedConn = connB
	client.mu.Unlock()
	if !client.completeReconnectOn(connB) {
		t.Fatal("account reconnect did not recover on the captured authenticated connection")
	}
}
