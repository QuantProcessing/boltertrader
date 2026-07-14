package nado

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

func TestWsAccountDisconnectedUnsubscribeRetainsSubscription(t *testing.T) {
	restClient, err := newNadoTestnetClient(t).WithCredentials(wsTestPrivateKey, "default")
	require.NoError(t, err)
	client, err := NewWsAccountClient(context.Background(), restClient)
	require.NoError(t, err)
	t.Cleanup(client.Close)

	stream := StreamParams{Type: "custom"}
	require.NoError(t, client.Subscribe(stream, func([]byte) {}))
	client.mu.Lock()
	prior := client.subscriptions[stream.Type]
	client.mu.Unlock()
	require.NotNil(t, prior)

	err = client.Unsubscribe(stream)
	require.Error(t, err)
	client.mu.Lock()
	current := client.subscriptions[stream.Type]
	client.mu.Unlock()
	require.Same(t, prior, current, "a disconnected unsubscribe must remain replayable")
}

func TestWsAccountSocketCallbackCanUnsubscribeWithoutBlockingAckReader(t *testing.T) {
	var upgrader websocket.Upgrader
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer conn.Close()

		var subscribe SubscriptionRequest
		require.NoError(t, conn.ReadJSON(&subscribe))
		require.NoError(t, conn.WriteJSON(map[string]any{"id": subscribe.Id, "status": "success"}))
		require.NoError(t, conn.WriteJSON(map[string]any{"type": "custom", "value": "trigger"}))

		var unsubscribe SubscriptionRequest
		require.NoError(t, conn.ReadJSON(&unsubscribe))
		require.Equal(t, "unsubscribe", unsubscribe.Method)
		require.NoError(t, conn.WriteJSON(map[string]any{"id": unsubscribe.Id, "status": "success"}))
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)

	restClient, err := newNadoTestnetClient(t).WithCredentials(wsTestPrivateKey, "default")
	require.NoError(t, err)
	client, err := NewWsAccountClient(context.Background(), restClient)
	require.NoError(t, err)
	client.url = wsURLFromHTTP(server.URL)
	t.Cleanup(client.Close)
	require.NoError(t, client.Connect())

	stream := StreamParams{Type: "custom"}
	unsubscribed := make(chan error, 1)
	require.NoError(t, client.Subscribe(stream, func([]byte) {
		unsubscribed <- client.Unsubscribe(stream)
	}))
	select {
	case err := <-unsubscribed:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("callback Unsubscribe deadlocked the websocket acknowledgement reader")
	}
}

func TestWsAccountReplaySerializesWithPublicSubscriptionOperations(t *testing.T) {
	var upgrader websocket.Upgrader
	replayReceived := make(chan struct{})
	releaseReplay := make(chan struct{})
	newSubscribeReceived := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer conn.Close()

		var replay SubscriptionRequest
		require.NoError(t, conn.ReadJSON(&replay))
		require.Equal(t, "subscribe", replay.Method)
		require.Equal(t, "replay-a", replay.Stream.Type)
		close(replayReceived)
		<-releaseReplay
		require.NoError(t, conn.WriteJSON(map[string]any{"id": replay.Id, "status": "success"}))

		var newer SubscriptionRequest
		require.NoError(t, conn.ReadJSON(&newer))
		require.Equal(t, "subscribe", newer.Method)
		require.Equal(t, "new-b", newer.Stream.Type)
		close(newSubscribeReceived)
		require.NoError(t, conn.WriteJSON(map[string]any{"id": newer.Id, "status": "success"}))
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)

	restClient, err := newNadoTestnetClient(t).WithCredentials(wsTestPrivateKey, "default")
	require.NoError(t, err)
	client, err := NewWsAccountClient(context.Background(), restClient)
	require.NoError(t, err)
	client.url = wsURLFromHTTP(server.URL)
	t.Cleanup(client.Close)
	require.NoError(t, client.Connect())
	client.mu.Lock()
	client.subscriptions["replay-a"] = &accountSubscription{params: StreamParams{Type: "replay-a"}}
	conn := client.conn
	client.mu.Unlock()

	replayResult := make(chan error, 1)
	go func() { replayResult <- client.resubscribeAllOn(conn) }()
	select {
	case <-replayReceived:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for replay request")
	}

	newResult := make(chan error, 1)
	newStarted := make(chan struct{})
	go func() {
		close(newStarted)
		newResult <- client.Subscribe(StreamParams{Type: "new-b"}, func([]byte) {})
	}()
	<-newStarted
	time.Sleep(50 * time.Millisecond)
	client.mu.Lock()
	_, pendingTooEarly := client.subscriptions["new-b"]
	client.mu.Unlock()
	require.False(t, pendingTooEarly, "public Subscribe entered local transaction state before replay committed")
	select {
	case <-newSubscribeReceived:
		t.Fatal("public Subscribe reached the wire before replay committed")
	default:
	}

	close(releaseReplay)
	require.NoError(t, <-replayResult)
	select {
	case <-newSubscribeReceived:
	case <-time.After(time.Second):
		t.Fatal("serialized public Subscribe did not run after replay")
	}
	require.NoError(t, <-newResult)
}

func TestWsAccountWriteFailedUnsubscribeRetainsSubscription(t *testing.T) {
	var upgrader websocket.Upgrader
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer conn.Close()
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)

	restClient, err := newNadoTestnetClient(t).WithCredentials(wsTestPrivateKey, "default")
	require.NoError(t, err)
	client, err := NewWsAccountClient(context.Background(), restClient)
	require.NoError(t, err)
	client.url = wsURLFromHTTP(server.URL)
	t.Cleanup(client.Close)
	conn, err := client.connect(context.Background())
	require.NoError(t, err)

	stream := StreamParams{Type: "custom"}
	seeded := &accountSubscription{params: stream, callback: func([]byte) {}}
	client.mu.Lock()
	client.subscriptions[stream.Type] = seeded
	prior := client.subscriptions[stream.Type]
	client.mu.Unlock()
	require.NotNil(t, prior)
	require.NoError(t, conn.CloseNow())

	err = client.Unsubscribe(stream)
	require.Error(t, err)
	client.mu.Lock()
	current := client.subscriptions[stream.Type]
	client.mu.Unlock()
	require.Same(t, prior, current, "a write-failed unsubscribe must remain replayable")
}

func TestWsAccountRejectedUnsubscribeRetainsSubscription(t *testing.T) {
	var upgrader websocket.Upgrader
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer conn.Close()

		var subscribe SubscriptionRequest
		require.NoError(t, conn.ReadJSON(&subscribe))
		require.Equal(t, "subscribe", subscribe.Method)
		require.NoError(t, conn.WriteJSON(map[string]any{"id": subscribe.Id, "status": "success"}))

		var unsubscribe SubscriptionRequest
		require.NoError(t, conn.ReadJSON(&unsubscribe))
		require.Equal(t, "unsubscribe", unsubscribe.Method)
		require.NoError(t, conn.WriteJSON(map[string]any{"id": unsubscribe.Id, "error": "rejected"}))
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)

	restClient, err := newNadoTestnetClient(t).WithCredentials(wsTestPrivateKey, "default")
	require.NoError(t, err)
	client, err := NewWsAccountClient(context.Background(), restClient)
	require.NoError(t, err)
	client.url = wsURLFromHTTP(server.URL)
	t.Cleanup(client.Close)
	require.NoError(t, client.Connect())

	stream := StreamParams{Type: "custom"}
	require.NoError(t, client.Subscribe(stream, func([]byte) {}))
	client.mu.Lock()
	prior := client.subscriptions[stream.Type]
	client.mu.Unlock()
	require.NotNil(t, prior)

	err = client.Unsubscribe(stream)
	require.ErrorContains(t, err, "rejected")
	client.mu.Lock()
	current := client.subscriptions[stream.Type]
	client.mu.Unlock()
	require.Same(t, prior, current, "a rejected unsubscribe must remain replayable")
}

func TestWsAccountPendingUnsubscribeWaitsForAckAndCannotDeleteNewerSubscription(t *testing.T) {
	var upgrader websocket.Upgrader
	unsubscribeReceived := make(chan SubscriptionRequest, 1)
	newSubscribeReceived := make(chan SubscriptionRequest, 1)
	releaseUnsubscribe := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer conn.Close()

		var prior SubscriptionRequest
		require.NoError(t, conn.ReadJSON(&prior))
		require.NoError(t, conn.WriteJSON(map[string]any{"id": prior.Id, "status": "success"}))

		var unsubscribe SubscriptionRequest
		require.NoError(t, conn.ReadJSON(&unsubscribe))
		require.Equal(t, "unsubscribe", unsubscribe.Method)
		unsubscribeReceived <- unsubscribe

		<-releaseUnsubscribe
		require.NoError(t, conn.WriteJSON(map[string]any{"id": unsubscribe.Id, "status": "success"}))

		var newer SubscriptionRequest
		require.NoError(t, conn.ReadJSON(&newer))
		require.Equal(t, "subscribe", newer.Method)
		newSubscribeReceived <- newer
		require.NoError(t, conn.WriteJSON(map[string]any{"id": newer.Id, "status": "success"}))
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)

	restClient, err := newNadoTestnetClient(t).WithCredentials(wsTestPrivateKey, "default")
	require.NoError(t, err)
	client, err := NewWsAccountClient(context.Background(), restClient)
	require.NoError(t, err)
	client.url = wsURLFromHTTP(server.URL)
	t.Cleanup(client.Close)
	require.NoError(t, client.Connect())

	stream := StreamParams{Type: "custom"}
	require.NoError(t, client.Subscribe(stream, func([]byte) {}))
	client.mu.Lock()
	prior := client.subscriptions[stream.Type]
	client.mu.Unlock()
	require.NotNil(t, prior)

	unsubscribeResult := make(chan error, 1)
	go func() { unsubscribeResult <- client.Unsubscribe(stream) }()
	select {
	case <-unsubscribeReceived:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for unsubscribe request")
	}
	client.mu.Lock()
	duringUnsubscribe := client.subscriptions[stream.Type]
	client.mu.Unlock()
	require.Same(t, prior, duringUnsubscribe, "subscription must remain confirmed until unsubscribe acknowledgement")
	select {
	case err := <-unsubscribeResult:
		t.Fatalf("Unsubscribe returned before acknowledgement: %v", err)
	default:
	}

	newCalls := make(chan struct{}, 1)
	newSubscribeResult := make(chan error, 1)
	newSubscribeStarted := make(chan struct{})
	go func() {
		close(newSubscribeStarted)
		newSubscribeResult <- client.Subscribe(stream, func([]byte) { newCalls <- struct{}{} })
	}()
	<-newSubscribeStarted
	select {
	case <-newSubscribeReceived:
		t.Fatal("newer subscribe reached the wire before pending unsubscribe committed")
	case <-time.After(100 * time.Millisecond):
	}
	client.mu.Lock()
	duringSerializedWait := client.subscriptions[stream.Type]
	client.mu.Unlock()
	require.Same(t, prior, duringSerializedWait, "newer subscribe must wait for the older operation boundary")

	close(releaseUnsubscribe)
	require.NoError(t, <-unsubscribeResult)
	select {
	case <-newSubscribeReceived:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for serialized newer subscribe request")
	}
	require.NoError(t, <-newSubscribeResult)
	client.mu.Lock()
	current := client.subscriptions[stream.Type]
	client.mu.Unlock()
	require.NotNil(t, current, "newer subscription must be installed after the older unsubscribe commits")
	require.NotSame(t, prior, current)
	current.callback([]byte(`{}`))
	select {
	case <-newCalls:
	case <-time.After(time.Second):
		t.Fatal("newer retained callback was not callable")
	}
}

func TestWsAccountRejectedReplacementRestoresPriorSubscriptionForReconnect(t *testing.T) {
	t.Parallel()

	var upgrader websocket.Upgrader
	var connections atomic.Int32
	allowReconnect := make(chan struct{})
	replayed := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer conn.Close()

		switch connections.Add(1) {
		case 1:
			var first SubscriptionRequest
			require.NoError(t, conn.ReadJSON(&first))
			require.Equal(t, "subscribe", first.Method)
			require.Equal(t, "custom", first.Stream.Type)
			require.NoError(t, conn.WriteJSON(map[string]any{"id": first.Id, "status": "success"}))

			var replacement SubscriptionRequest
			require.NoError(t, conn.ReadJSON(&replacement))
			require.Equal(t, "subscribe", replacement.Method)
			require.Equal(t, "custom", replacement.Stream.Type)
			require.NoError(t, conn.WriteJSON(map[string]any{"id": replacement.Id, "error": "replacement rejected"}))

			<-allowReconnect
			require.NoError(t, conn.WriteControl(
				websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, "rotate"),
				time.Now().Add(time.Second),
			))
		case 2:
			var replay SubscriptionRequest
			require.NoError(t, conn.ReadJSON(&replay))
			require.Equal(t, "subscribe", replay.Method)
			require.Equal(t, "custom", replay.Stream.Type)
			require.NoError(t, conn.WriteJSON(map[string]any{"id": replay.Id, "status": "success"}))
			require.NoError(t, conn.WriteJSON(map[string]any{"type": "custom", "value": "after-reconnect"}))
			replayed <- struct{}{}
			<-r.Context().Done()
		default:
			t.Errorf("unexpected account websocket connection")
		}
	}))
	t.Cleanup(server.Close)

	restClient, err := newNadoTestnetClient(t).WithCredentials(wsTestPrivateKey, "default")
	require.NoError(t, err)
	client, err := NewWsAccountClient(context.Background(), restClient)
	require.NoError(t, err)
	client.url = wsURLFromHTTP(server.URL)
	t.Cleanup(client.Close)
	require.NoError(t, client.Connect())

	priorCalls := make(chan struct{}, 1)
	replacementCalls := make(chan struct{}, 1)
	stream := StreamParams{Type: "custom"}
	require.NoError(t, client.Subscribe(stream, func([]byte) {
		priorCalls <- struct{}{}
	}))
	client.mu.Lock()
	prior := client.subscriptions[stream.Type]
	client.mu.Unlock()
	require.NotNil(t, prior)

	err = client.Subscribe(stream, func([]byte) {
		replacementCalls <- struct{}{}
	})
	require.ErrorContains(t, err, "replacement rejected")
	client.mu.Lock()
	current := client.subscriptions[stream.Type]
	client.mu.Unlock()
	require.Same(t, prior, current, "a rejected replacement must not become reconnect state")

	close(allowReconnect)
	select {
	case <-replayed:
	case <-time.After(4 * time.Second):
		t.Fatal("timed out waiting for prior subscription replay")
	}
	select {
	case <-priorCalls:
	case <-time.After(time.Second):
		t.Fatal("replayed account update did not use the prior callback")
	}
	select {
	case <-replacementCalls:
		t.Fatal("rejected replacement callback received a replayed account update")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestWsAccountReplacementOperationsCommitInWireOrder(t *testing.T) {
	t.Parallel()

	var upgrader websocket.Upgrader
	replacementReceived := make(chan struct{})
	newerReceived := make(chan struct{})
	rejectReplacement := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer conn.Close()

		var prior SubscriptionRequest
		require.NoError(t, conn.ReadJSON(&prior))
		require.NoError(t, conn.WriteJSON(map[string]any{"id": prior.Id, "status": "success"}))

		var replacement SubscriptionRequest
		require.NoError(t, conn.ReadJSON(&replacement))
		close(replacementReceived)
		<-rejectReplacement
		require.NoError(t, conn.WriteJSON(map[string]any{"id": replacement.Id, "error": "stale replacement rejected"}))

		var newer SubscriptionRequest
		require.NoError(t, conn.ReadJSON(&newer))
		close(newerReceived)
		require.NoError(t, conn.WriteJSON(map[string]any{"id": newer.Id, "status": "success"}))
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)

	restClient, err := newNadoTestnetClient(t).WithCredentials(wsTestPrivateKey, "default")
	require.NoError(t, err)
	client, err := NewWsAccountClient(context.Background(), restClient)
	require.NoError(t, err)
	client.url = wsURLFromHTTP(server.URL)
	t.Cleanup(client.Close)
	require.NoError(t, client.Connect())

	stream := StreamParams{Type: "custom"}
	require.NoError(t, client.Subscribe(stream, func([]byte) {}))
	replacementErr := make(chan error, 1)
	go func() {
		replacementErr <- client.Subscribe(stream, func([]byte) {})
	}()
	select {
	case <-replacementReceived:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for replacement subscription")
	}

	newerResult := make(chan error, 1)
	newerStarted := make(chan struct{})
	go func() {
		close(newerStarted)
		newerResult <- client.Subscribe(stream, func([]byte) {})
	}()
	<-newerStarted
	select {
	case <-newerReceived:
		t.Fatal("newer replacement reached the wire before the prior replacement completed")
	case <-time.After(100 * time.Millisecond):
	}

	close(rejectReplacement)
	select {
	case err := <-replacementErr:
		require.ErrorContains(t, err, "stale replacement rejected")
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for replacement rejection")
	}
	select {
	case <-newerReceived:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for serialized newer replacement")
	}
	require.NoError(t, <-newerResult)
	client.mu.Lock()
	newer := client.subscriptions[stream.Type]
	client.mu.Unlock()
	require.NotNil(t, newer)

	client.mu.Lock()
	current := client.subscriptions[stream.Type]
	client.mu.Unlock()
	require.Same(t, newer, current, "an older failure must not overwrite a newer successful subscription")
}
