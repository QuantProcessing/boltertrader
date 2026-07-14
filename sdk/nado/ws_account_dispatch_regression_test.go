package nado

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

func TestWsAccountConnectDoesNotPublishSocketBeforeSubscriptionTransaction(t *testing.T) {
	var upgrader websocket.Upgrader
	var replayCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer conn.Close()

		var replay SubscriptionRequest
		require.NoError(t, conn.ReadJSON(&replay))
		require.Equal(t, "subscribe", replay.Method)
		require.Equal(t, "retained", replay.Stream.Type)
		replayCount.Add(1)
		require.NoError(t, conn.WriteJSON(map[string]any{"id": replay.Id, "status": "success"}))
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)

	restClient, err := newNadoTestnetClient(t).WithCredentials(wsTestPrivateKey, "default")
	require.NoError(t, err)
	client, err := NewWsAccountClient(context.Background(), restClient)
	require.NoError(t, err)
	client.url = wsURLFromHTTP(server.URL)
	t.Cleanup(client.Close)
	require.NoError(t, client.Subscribe(StreamParams{Type: "retained"}, func([]byte) {}))

	client.subscriptionMu.Lock()
	connectResult := make(chan error, 1)
	go func() { connectResult <- client.Connect() }()

	// Holding the subscription transaction boundary must prevent a candidate
	// socket from becoming visible to concurrent Subscribe calls.
	time.Sleep(100 * time.Millisecond)
	client.mu.Lock()
	publishedEarly := client.conn != nil || client.isConnected
	client.mu.Unlock()
	client.subscriptionMu.Unlock()
	require.False(t, publishedEarly, "Connect published a socket before it owned the replay transaction")
	require.NoError(t, <-connectResult)
	require.Equal(t, int32(1), replayCount.Load(), "retained subscription must replay exactly once")
}

func TestWsAccountCallbackOverflowReconnectsInLifecycleOrder(t *testing.T) {
	const callbackQueueLimit = 1024

	var upgrader websocket.Upgrader
	var connections atomic.Int32
	secondConnected := make(chan struct{}, 1)
	oldSocketClosed := make(chan struct{}, 1)
	stop := make(chan struct{})
	var stopOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer conn.Close()

		connection := connections.Add(1)
		var replay SubscriptionRequest
		require.NoError(t, conn.ReadJSON(&replay))
		require.Equal(t, "subscribe", replay.Method)
		require.Equal(t, "custom", replay.Stream.Type)
		require.NoError(t, conn.WriteJSON(map[string]any{"id": replay.Id, "status": "success"}))

		if connection == 1 {
			for i := 0; i < callbackQueueLimit+100; i++ {
				if err := conn.WriteJSON(map[string]any{"type": "custom", "value": i}); err != nil {
					oldSocketClosed <- struct{}{}
					return
				}
			}
			_ = conn.SetReadDeadline(time.Now().Add(4 * time.Second))
			if _, _, err := conn.ReadMessage(); err != nil {
				oldSocketClosed <- struct{}{}
			}
			return
		}

		secondConnected <- struct{}{}
		require.NoError(t, conn.WriteJSON(map[string]any{"type": "custom", "value": "replacement"}))
		<-stop
	}))
	t.Cleanup(func() {
		stopOnce.Do(func() { close(stop) })
		server.Close()
	})

	restClient, err := newNadoTestnetClient(t).WithCredentials(wsTestPrivateKey, "default")
	require.NoError(t, err)
	client, err := NewWsAccountClient(context.Background(), restClient)
	require.NoError(t, err)
	t.Cleanup(client.Close)
	client.url = wsURLFromHTTP(server.URL)

	var eventsMu sync.Mutex
	var events []string
	appendEvent := func(event string) {
		eventsMu.Lock()
		events = append(events, event)
		eventsMu.Unlock()
	}
	firstEntered := make(chan struct{}, 1)
	releaseFirst := make(chan struct{})
	var releaseOnce sync.Once
	replacementSeen := make(chan struct{}, 1)
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseFirst) }) })

	client.SetReconnectHooks(func(err error) {
		if err == nil || !containsText(err.Error(), "callback queue overflow") {
			t.Errorf("reconnect cause = %v, want callback queue overflow", err)
		}
		appendEvent("started")
	}, func() {
		appendEvent("recovered")
	})
	require.NoError(t, client.Subscribe(StreamParams{Type: "custom"}, func(msg []byte) {
		value := accountTestMessageValue(t, msg)
		if value == "0" {
			appendEvent("old")
			firstEntered <- struct{}{}
			<-releaseFirst
			return
		}
		if value == "replacement" {
			appendEvent("replacement")
			replacementSeen <- struct{}{}
		}
	}))
	require.NoError(t, client.Connect())

	select {
	case <-firstEntered:
	case <-time.After(time.Second):
		t.Fatal("first callback did not block")
	}
	select {
	case <-secondConnected:
	case <-time.After(4 * time.Second):
		t.Fatal("callback overflow did not trigger reconnect while the handler was blocked")
	}
	select {
	case <-oldSocketClosed:
	case <-time.After(time.Second):
		t.Fatal("server did not observe the overflowing socket close")
	}
	select {
	case <-replacementSeen:
		t.Fatal("replacement data overtook the old callback and lifecycle boundary")
	default:
	}

	releaseOnce.Do(func() { close(releaseFirst) })
	select {
	case <-replacementSeen:
	case <-time.After(2 * time.Second):
		t.Fatal("replacement data was not delivered after recovery")
	}
	eventsMu.Lock()
	got := append([]string(nil), events...)
	eventsMu.Unlock()
	require.Equal(t, []string{"old", "started", "recovered", "replacement"}, got)
}

func TestWsAccountCloseDropsQueuedCallbacks(t *testing.T) {
	var upgrader websocket.Upgrader
	secondWritten := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer conn.Close()

		var replay SubscriptionRequest
		require.NoError(t, conn.ReadJSON(&replay))
		require.NoError(t, conn.WriteJSON(map[string]any{"id": replay.Id, "status": "success"}))
		require.NoError(t, conn.WriteJSON(map[string]any{"type": "custom", "value": "first"}))
		require.NoError(t, conn.WriteJSON(map[string]any{"type": "custom", "value": "second"}))
		close(secondWritten)
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)

	restClient, err := newNadoTestnetClient(t).WithCredentials(wsTestPrivateKey, "default")
	require.NoError(t, err)
	client, err := NewWsAccountClient(context.Background(), restClient)
	require.NoError(t, err)
	client.url = wsURLFromHTTP(server.URL)

	firstEntered := make(chan struct{}, 1)
	releaseFirst := make(chan struct{})
	var releaseOnce sync.Once
	secondSeen := make(chan struct{}, 1)
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(releaseFirst) })
		client.Close()
	})
	require.NoError(t, client.Subscribe(StreamParams{Type: "custom"}, func(msg []byte) {
		switch accountTestMessageValue(t, msg) {
		case "first":
			firstEntered <- struct{}{}
			<-releaseFirst
		case "second":
			secondSeen <- struct{}{}
		}
	}))
	require.NoError(t, client.Connect())
	select {
	case <-firstEntered:
	case <-time.After(time.Second):
		t.Fatal("first callback did not block")
	}
	select {
	case <-secondWritten:
	case <-time.After(time.Second):
		t.Fatal("server did not write the queued callback")
	}
	waitForAccountPendingCallbacks(t, client.callbackDispatcher, 1)

	closed := make(chan struct{})
	go func() {
		client.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("Close blocked on a user callback")
	}
	releaseOnce.Do(func() { close(releaseFirst) })
	select {
	case <-secondSeen:
		t.Fatal("queued callback from the closed lifecycle was delivered")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestWsAccountFailedReplacementDropsBufferedData(t *testing.T) {
	var upgrader websocket.Upgrader
	var connections atomic.Int32
	closeFirst := make(chan struct{})
	thirdConnected := make(chan struct{}, 1)
	stop := make(chan struct{})
	var closeFirstOnce sync.Once
	var stopOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer conn.Close()

		connection := connections.Add(1)
		var replay SubscriptionRequest
		require.NoError(t, conn.ReadJSON(&replay))
		require.Equal(t, "subscribe", replay.Method)
		require.Equal(t, "custom", replay.Stream.Type)
		switch connection {
		case 1:
			require.NoError(t, conn.WriteJSON(map[string]any{"id": replay.Id, "status": "success"}))
			require.NoError(t, conn.WriteJSON(map[string]any{"type": "custom", "value": "old"}))
			<-closeFirst
			require.NoError(t, conn.WriteControl(
				websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, "rotate"),
				time.Now().Add(time.Second),
			))
		case 2:
			// Data can race ahead of the replay acknowledgement. It belongs to
			// this candidate only and must disappear when replay is rejected.
			require.NoError(t, conn.WriteJSON(map[string]any{"type": "custom", "value": "failed-replacement"}))
			require.NoError(t, conn.WriteJSON(map[string]any{"id": replay.Id, "error": "replay rejected"}))
		case 3:
			require.NoError(t, conn.WriteJSON(map[string]any{"id": replay.Id, "status": "success"}))
			thirdConnected <- struct{}{}
			require.NoError(t, conn.WriteJSON(map[string]any{"type": "custom", "value": "fresh"}))
			<-stop
		default:
			t.Errorf("unexpected websocket connection %d", connection)
		}
	}))
	t.Cleanup(func() {
		closeFirstOnce.Do(func() { close(closeFirst) })
		stopOnce.Do(func() { close(stop) })
		server.Close()
	})

	restClient, err := newNadoTestnetClient(t).WithCredentials(wsTestPrivateKey, "default")
	require.NoError(t, err)
	client, err := NewWsAccountClient(context.Background(), restClient)
	require.NoError(t, err)
	client.url = wsURLFromHTTP(server.URL)
	t.Cleanup(client.Close)

	var eventsMu sync.Mutex
	var events []string
	appendEvent := func(event string) {
		eventsMu.Lock()
		events = append(events, event)
		eventsMu.Unlock()
	}
	oldEntered := make(chan struct{}, 1)
	releaseOld := make(chan struct{})
	var releaseOldOnce sync.Once
	freshSeen := make(chan struct{}, 1)
	t.Cleanup(func() { releaseOldOnce.Do(func() { close(releaseOld) }) })
	client.SetReconnectHooks(func(error) { appendEvent("started") }, func() { appendEvent("recovered") })
	require.NoError(t, client.Subscribe(StreamParams{Type: "custom"}, func(msg []byte) {
		value := accountTestMessageValue(t, msg)
		appendEvent(value)
		switch value {
		case "old":
			oldEntered <- struct{}{}
			<-releaseOld
		case "fresh":
			freshSeen <- struct{}{}
		}
	}))
	require.NoError(t, client.Connect())
	select {
	case <-oldEntered:
	case <-time.After(time.Second):
		t.Fatal("old callback did not block")
	}
	closeFirstOnce.Do(func() { close(closeFirst) })
	select {
	case <-thirdConnected:
	case <-time.After(5 * time.Second):
		t.Fatal("failed replacement prevented a later successful reconnect")
	}
	select {
	case <-freshSeen:
		t.Fatal("fresh data overtook the blocked old callback and lifecycle boundary")
	default:
	}
	releaseOldOnce.Do(func() { close(releaseOld) })
	select {
	case <-freshSeen:
	case <-time.After(time.Second):
		t.Fatal("fresh data was not delivered after successful recovery")
	}

	eventsMu.Lock()
	got := append([]string(nil), events...)
	eventsMu.Unlock()
	require.Equal(t, []string{"old", "started", "recovered", "fresh"}, got)
}

func TestWsAccountCompleteReconnectKeepsRecoveryOpenWhenDispatcherRejectsGeneration(t *testing.T) {
	conn := &coderws.Conn{}
	dispatcher := newAccountCallbackDispatcher()
	dispatcher.beginGap(1, nil)
	dispatcher.activateConnection(1, conn, true)
	dispatcher.stop()

	client := &WsAccountClient{
		conn:               conn,
		isConnected:        true,
		recovering:         true,
		recoveryGeneration: 1,
		callbackDispatcher: dispatcher,
		subscriptions:      make(map[string]*accountSubscription),
	}
	if client.completeReconnectOn(conn) {
		t.Fatal("reconnect reported ready after the lifecycle dispatcher rejected its generation")
	}
	client.mu.Lock()
	recovering := client.recovering
	client.mu.Unlock()
	if !recovering {
		t.Fatal("failed recovery publication cleared the reconnect owner")
	}
}

func accountTestMessageValue(t *testing.T, msg []byte) string {
	t.Helper()
	var payload struct {
		Value any `json:"value"`
	}
	require.NoError(t, json.Unmarshal(msg, &payload))
	return fmt.Sprint(payload.Value)
}

func containsText(value, fragment string) bool {
	return strings.Contains(value, fragment)
}

func waitForAccountPendingCallbacks(t *testing.T, dispatcher *accountCallbackDispatcher, minimum int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		dispatcher.mu.Lock()
		pending := dispatcher.pendingData
		dispatcher.mu.Unlock()
		if pending >= minimum {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("dispatcher did not retain at least %d pending callbacks", minimum)
}
