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

func TestOKXWebsocketCallbackDispatcherPreservesGapOrdering(t *testing.T) {
	dispatcher := newOKXWebsocketCallbackDispatcher()
	t.Cleanup(dispatcher.stop)

	oldConn := &websocket.Conn{}
	newConn := &websocket.Conn{}
	dispatcher.activateConnection(0, oldConn, false)

	entered := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	var got []string
	appendEvent := func(event string) {
		mu.Lock()
		got = append(got, event)
		mu.Unlock()
	}

	if !dispatcher.enqueueData(oldConn, []okxWebsocketCallback{{
		kind: okxWebsocketCallbackData,
		run: func() {
			close(entered)
			<-release
			appendEvent("old-data")
		},
	}}) {
		t.Fatal("old data unexpectedly overflowed callback queue")
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("old callback did not start")
	}

	dispatcher.beginGap(1, func() { appendEvent("started") })
	dispatcher.activateConnection(1, newConn, true)
	if !dispatcher.enqueueData(newConn, []okxWebsocketCallback{{
		kind: okxWebsocketCallbackData,
		run:  func() { appendEvent("replacement-data") },
	}}) {
		t.Fatal("replacement data unexpectedly overflowed callback queue")
	}
	if !dispatcher.enqueueRecovered(1, newConn, func() { appendEvent("recovered") }) {
		t.Fatal("matching recovered callback was rejected")
	}
	close(release)

	want := []string{"old-data", "started", "recovered", "replacement-data"}
	deadline := time.Now().Add(time.Second)
	for {
		mu.Lock()
		complete := len(got) == len(want)
		actual := append([]string(nil), got...)
		mu.Unlock()
		if complete {
			for i := range want {
				if actual[i] != want[i] {
					t.Fatalf("callback order=%v, want %v", actual, want)
				}
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("callback order=%v, want %v", actual, want)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestOKXWebsocketCallbackDispatcherBoundsPendingData(t *testing.T) {
	dispatcher := newOKXWebsocketCallbackDispatcher()
	t.Cleanup(dispatcher.stop)
	dispatcher.limit = okxWSCallbackControlSlots + 1

	conn := &websocket.Conn{}
	dispatcher.activateConnection(1, conn, true)
	dispatcher.beginGap(1, nil)
	dispatcher.activateConnection(1, conn, true)
	if !dispatcher.enqueueData(conn, []okxWebsocketCallback{{kind: okxWebsocketCallbackData}}) {
		t.Fatal("first pending data callback was rejected")
	}
	if dispatcher.enqueueData(conn, []okxWebsocketCallback{{kind: okxWebsocketCallbackData}}) {
		t.Fatal("callback queue accepted data beyond its bounded capacity")
	}
}

func TestOKXWebsocketCallbackDispatcherResetDropsOldLifecycleQueue(t *testing.T) {
	dispatcher := newOKXWebsocketCallbackDispatcher()
	t.Cleanup(dispatcher.stop)

	conn := &websocket.Conn{}
	dispatcher.activateConnection(0, conn, false)
	entered := make(chan struct{})
	release := make(chan struct{})
	runOld := make(chan struct{}, 1)
	if !dispatcher.enqueueData(conn, []okxWebsocketCallback{
		{kind: okxWebsocketCallbackData, run: func() { close(entered); <-release }},
		{kind: okxWebsocketCallbackData, run: func() { runOld <- struct{}{} }},
	}) {
		t.Fatal("initial callbacks unexpectedly overflowed")
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first callback did not start")
	}
	dispatcher.reset()
	close(release)
	select {
	case <-runOld:
		t.Fatal("reset delivered a queued callback from the old lifecycle")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestPrivateWSFinancialWritesFailClosedUntilExactConnectionReady(t *testing.T) {
	client := NewWSClient(context.Background()).WithCredentials("key", "secret", "pass")
	t.Cleanup(func() {
		client.mu.Lock()
		client.Conn = nil
		client.mu.Unlock()
		client.Close()
	})
	conn := &websocket.Conn{}
	client.mu.Lock()
	client.Conn = conn
	client.authenticatedConn = conn
	client.mu.Unlock()

	instIDCode := int64(1)
	orderID := "order"
	tests := []struct {
		name string
		call func() error
	}{
		{
			name: "place",
			call: func() error {
				_, err := client.PlaceOrderWS(&OrderRequest{InstIdCode: &instIDCode, Sz: "1"})
				return err
			},
		},
		{
			name: "cancel",
			call: func() error {
				_, err := client.CancelOrderWS(instIDCode, &orderID, nil)
				return err
			},
		},
		{
			name: "modify",
			call: func() error {
				_, err := client.ModifyOrderWS(&ModifyOrderRequest{InstIdCode: &instIDCode})
				return err
			},
		},
		{
			name: "batch cancel",
			call: func() error {
				_, err := client.CancelOrdersWS([]CancelOrderRequest{{InstIdCode: &instIDCode, OrdId: &orderID}})
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call()
			if err == nil || !strings.Contains(err.Error(), "not ready") {
				t.Fatalf("financial write error=%v, want exact-connection not-ready failure", err)
			}
		})
	}
}

func TestPrivateWSFinancialWriteCannotOvertakeRetainedSubscriptionReplay(t *testing.T) {
	upgrader := websocket.Upgrader{}
	replaySeen := make(chan struct{})
	releaseReplay := make(chan struct{})
	serverErrors := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
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
					serverErrors <- err
					return
				}
			case "subscribe":
				var id int64
				if err := json.Unmarshal(request.ID, &id); err != nil {
					serverErrors <- err
					return
				}
				close(replaySeen)
				<-releaseReplay
				if err := conn.WriteJSON(map[string]any{
					"id":    strconv.FormatInt(id, 10),
					"event": "subscribe",
					"code":  "0",
				}); err != nil {
					serverErrors <- err
					return
				}
			case "order":
				var id string
				if err := json.Unmarshal(request.ID, &id); err != nil {
					serverErrors <- err
					return
				}
				if err := conn.WriteJSON(map[string]any{
					"id":   id,
					"code": "0",
					"data": []map[string]any{{"ordId": "1", "sCode": "0"}},
				}); err != nil {
					serverErrors <- err
				}
				return
			}
		}
	}))
	t.Cleanup(server.Close)

	client := NewWSClient(context.Background()).
		WithURL("ws"+strings.TrimPrefix(server.URL, "http")).
		WithCredentials("key", "secret", "pass")
	t.Cleanup(client.Close)
	client.mu.Lock()
	client.Subs[WsSubscribeArgs{Channel: "orders", InstType: "SWAP"}] = func([]byte) {}
	client.mu.Unlock()

	connectResult := make(chan error, 1)
	go func() { connectResult <- client.Connect() }()
	select {
	case <-replaySeen:
	case err := <-serverErrors:
		t.Fatalf("websocket server: %v", err)
	case <-time.After(time.Second):
		t.Fatal("retained subscription replay did not start")
	}

	instIDCode := int64(1)
	if _, err := client.PlaceOrderWS(&OrderRequest{InstIdCode: &instIDCode, Sz: "1"}); err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("financial write during replay error=%v, want not-ready failure", err)
	}
	close(releaseReplay)
	select {
	case err := <-connectResult:
		if err != nil {
			t.Fatalf("Connect: %v", err)
		}
	case err := <-serverErrors:
		t.Fatalf("websocket server: %v", err)
	case <-time.After(time.Second):
		t.Fatal("Connect did not publish readiness after replay ACK")
	}

	result, err := client.PlaceOrderWS(&OrderRequest{InstIdCode: &instIDCode, Sz: "1"})
	if err != nil {
		t.Fatalf("PlaceOrderWS after replay: %v", err)
	}
	if result == nil || result.OrdId != "1" {
		t.Fatalf("PlaceOrderWS result=%+v, want order 1", result)
	}
}

func TestWSClientResumeDispatchHookCanCloseWithoutDeadlock(t *testing.T) {
	client := NewWSClient(context.Background())
	args := WsSubscribeArgs{Channel: "orders", InstType: "SPOT"}
	ran := make(chan struct{}, 1)
	client.Subs[args] = func([]byte) { ran <- struct{}{} }
	client.PauseDispatch()
	client.handleMessage([]byte(`{"arg":{"channel":"orders","instType":"SPOT"},"data":[{}]}`))

	done := make(chan struct{})
	go func() {
		client.ResumeDispatch(client.Close)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("ResumeDispatch deadlocked when its hook closed the client")
	}
	select {
	case <-ran:
		t.Fatal("Close delivered data buffered by the prior dispatcher lifecycle")
	default:
	}
}

func TestExplicitConnectCompletesAutomaticRecoveryWithoutDuplicateReplay(t *testing.T) {
	upgrader := websocket.Upgrader{}
	var requests atomic.Int32
	var replacementSubscriptions atomic.Int32
	allowInitialClose := make(chan struct{})
	firstReconnectRejected := make(chan struct{})
	replacementReady := make(chan struct{}, 1)
	serverErrors := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestNumber := requests.Add(1)
		if requestNumber == 2 {
			close(firstReconnectRejected)
			http.Error(w, "retry", http.StatusServiceUnavailable)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
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
					serverErrors <- err
					return
				}
			case "subscribe":
				var id int64
				if err := json.Unmarshal(request.ID, &id); err != nil {
					serverErrors <- err
					return
				}
				if requestNumber >= 3 {
					replacementSubscriptions.Add(1)
				}
				if err := conn.WriteJSON(map[string]any{
					"id":    strconv.FormatInt(id, 10),
					"event": "subscribe",
					"code":  "0",
				}); err != nil {
					serverErrors <- err
					return
				}
				if requestNumber == 1 {
					<-allowInitialClose
					_ = conn.WriteControl(
						websocket.CloseMessage,
						websocket.FormatCloseMessage(websocket.CloseNormalClosure, "rotate"),
						time.Now().Add(time.Second),
					)
					return
				}
				select {
				case replacementReady <- struct{}{}:
				default:
				}
			}
		}
	}))
	t.Cleanup(server.Close)

	client := NewWSClient(context.Background()).
		WithURL("ws"+strings.TrimPrefix(server.URL, "http")).
		WithCredentials("key", "secret", "pass")
	client.reconnectWait = 150 * time.Millisecond
	t.Cleanup(client.Close)
	client.mu.Lock()
	client.Subs[WsSubscribeArgs{Channel: "orders", InstType: "SWAP"}] = func([]byte) {}
	client.mu.Unlock()
	recovered := make(chan struct{}, 2)
	client.SetReconnectHooks(func(error) {}, func() { recovered <- struct{}{} })

	if err := client.Connect(); err != nil {
		t.Fatalf("initial Connect: %v", err)
	}
	close(allowInitialClose)
	select {
	case <-firstReconnectRejected:
	case err := <-serverErrors:
		t.Fatalf("websocket server: %v", err)
	case <-time.After(time.Second):
		t.Fatal("automatic reconnect did not enter the controlled backoff")
	}

	if err := client.Connect(); err != nil {
		t.Fatalf("explicit Connect during backoff: %v", err)
	}
	select {
	case <-replacementReady:
	case err := <-serverErrors:
		t.Fatalf("websocket server: %v", err)
	case <-time.After(time.Second):
		t.Fatal("explicit Connect did not replay the retained subscription")
	}
	select {
	case <-recovered:
	case <-time.After(time.Second):
		t.Fatal("explicit Connect did not publish the pending recovered generation")
	}
	time.Sleep(2 * client.reconnectWait)
	if got := replacementSubscriptions.Load(); got != 1 {
		t.Fatalf("replacement subscribe writes=%d, want exactly one replay", got)
	}
	select {
	case <-recovered:
		t.Fatal("automatic reconnect published a duplicate recovered generation")
	default:
	}
}
