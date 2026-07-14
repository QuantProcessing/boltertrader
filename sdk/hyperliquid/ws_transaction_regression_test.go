package hyperliquid

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/gorilla/websocket"
)

type controlledSubscriptionMarshal struct {
	payload     string
	blockCall   int32
	failCall    int32
	marshalCall atomic.Int32
	entered     chan struct{}
	release     chan struct{}
	enterOnce   sync.Once
}

func (s *controlledSubscriptionMarshal) MarshalJSON() ([]byte, error) {
	call := s.marshalCall.Add(1)
	if call == s.blockCall {
		s.enterOnce.Do(func() { close(s.entered) })
		<-s.release
	}
	if call == s.failCall {
		return nil, errors.New("forced subscription marshal failure")
	}
	return []byte(s.payload), nil
}

func TestConcurrentSameKeyFailedReplacementKeepsCommittedHandlerAndPayloadTogether(t *testing.T) {
	server := newDrainingWebsocketServer(t)
	client := NewWebsocketClient(context.Background()).WithURL(websocketURL(server.URL))
	t.Cleanup(func() {
		client.Close()
		server.Close()
	})
	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	const payload = `{"type":"orders","user":"0xabc"}`
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	first := &controlledSubscriptionMarshal{
		payload:   payload,
		blockCall: 2,
		entered:   firstEntered,
		release:   releaseFirst,
	}
	second := &controlledSubscriptionMarshal{
		payload:  payload,
		failCall: 2,
		entered:  make(chan struct{}),
		release:  make(chan struct{}),
	}
	dispatched := make(chan string, 1)
	firstResult := make(chan error, 1)
	go func() {
		firstResult <- client.Subscribe("orders", first, func(WsMessage) { dispatched <- "first" })
	}()
	select {
	case <-firstEntered:
	case <-time.After(time.Second):
		t.Fatal("first subscription did not block in its websocket write")
	}

	secondResult := make(chan error, 1)
	go func() {
		secondResult <- client.Subscribe("orders", second, func(WsMessage) { dispatched <- "second" })
	}()
	waitForSubscriptionRevision(t, client, 2, 150*time.Millisecond)
	close(releaseFirst)

	if err := <-firstResult; err != nil {
		t.Fatalf("first Subscribe: %v", err)
	}
	if err := <-secondResult; err == nil || !strings.Contains(err.Error(), "forced subscription marshal failure") {
		t.Fatalf("second Subscribe error=%v, want forced marshal failure", err)
	}

	key := subscriptionKey(map[string]any{"type": "orders", "user": "0xabc"})
	client.Mu.RLock()
	handler := client.subscriptions["orders"][key]
	_, hasPayload := client.subscriptionPayloads["orders"][key]
	client.Mu.RUnlock()
	if handler == nil || !hasPayload {
		t.Fatalf("committed state split after failed replacement: handler=%v payload=%v", handler != nil, hasPayload)
	}
	handler(WsMessage{})
	if got := <-dispatched; got != "first" {
		t.Fatalf("active handler=%q, want first", got)
	}
}

func TestOpaqueUnsubscribeFailureCannotRaceInDifferentIdentity(t *testing.T) {
	server := newDrainingWebsocketServer(t)
	client := NewWebsocketClient(context.Background()).WithURL(websocketURL(server.URL))
	t.Cleanup(func() {
		client.Close()
		server.Close()
	})
	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	first := &controlledSubscriptionMarshal{
		payload:   `{"type":"orderUpdates","user":"0xaaa"}`,
		blockCall: 3,
		failCall:  3,
		entered:   firstEntered,
		release:   releaseFirst,
	}
	firstKey := subscriptionKey(first)
	client.Mu.Lock()
	client.subscriptions["orderUpdates"] = map[string]func(WsMessage){firstKey: func(WsMessage) {}}
	client.subscriptionPayloads["orderUpdates"] = map[string]any{firstKey: first}
	client.Mu.Unlock()

	unsubscribeResult := make(chan error, 1)
	go func() { unsubscribeResult <- client.Unsubscribe("orderUpdates", first) }()
	select {
	case <-firstEntered:
	case <-time.After(time.Second):
		t.Fatal("unsubscribe did not block in its websocket write")
	}

	second := map[string]any{"type": "orderUpdates", "user": "0xbbb"}
	subscribeResult := make(chan error, 1)
	go func() { subscribeResult <- client.Subscribe("orderUpdates", second, func(WsMessage) {}) }()
	waitForSubscriptionRevision(t, client, 2, 150*time.Millisecond)
	close(releaseFirst)

	if err := <-unsubscribeResult; err == nil || !strings.Contains(err.Error(), "forced subscription marshal failure") {
		t.Fatalf("Unsubscribe error=%v, want forced marshal failure", err)
	}
	if err := <-subscribeResult; err == nil || !strings.Contains(err.Error(), "cannot multiplex") {
		t.Fatalf("second identity Subscribe error=%v, want cannot multiplex", err)
	}
	client.Mu.RLock()
	handlers := len(client.subscriptions["orderUpdates"])
	_, firstRemains := client.subscriptions["orderUpdates"][firstKey]
	client.Mu.RUnlock()
	if handlers != 1 || !firstRemains {
		t.Fatalf("opaque handlers=%d first_remains=%v, want only original identity", handlers, firstRemains)
	}
}

func TestSuccessfulUnsubscribeCannotBeOvertakenByStaleReplaySnapshot(t *testing.T) {
	methods := make(chan string, 4)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			var request WsSubscribeRequest
			if err := conn.ReadJSON(&request); err != nil {
				return
			}
			methods <- request.Method
			if request.Method == "subscribe" {
				if err := conn.WriteJSON(map[string]any{
					"channel": "subscriptionResponse",
					"data":    request.Subscription,
				}); err != nil {
					return
				}
			}
		}
	}))
	client := NewWebsocketClient(context.Background()).WithURL(websocketURL(server.URL))
	client.SubscriptionAckTimeout = time.Second
	t.Cleanup(func() {
		client.Close()
		server.Close()
	})
	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	replayEntered := make(chan struct{})
	releaseReplay := make(chan struct{})
	var replayOnce sync.Once
	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()),
		zapcore.AddSync(io.Discard),
		zap.DebugLevel,
	)
	client.Logger = zap.New(core, zap.Hooks(func(entry zapcore.Entry) error {
		if entry.Message == "resubscribing" {
			replayOnce.Do(func() {
				close(replayEntered)
				<-releaseReplay
			})
		}
		return nil
	})).Sugar()

	subscription := map[string]any{"type": "orders", "user": "0xabc"}
	key := subscriptionKey(subscription)
	client.Mu.Lock()
	client.subscriptions["orders"] = map[string]func(WsMessage){key: func(WsMessage) {}}
	client.subscriptionPayloads["orders"] = map[string]any{key: subscription}
	client.Mu.Unlock()

	replayResult := make(chan error, 1)
	go func() { replayResult <- client.resubscribeAll() }()
	select {
	case <-replayEntered:
	case <-time.After(time.Second):
		t.Fatal("replay did not reach the post-snapshot barrier")
	}

	unsubscribeResult := make(chan error, 1)
	go func() { unsubscribeResult <- client.Unsubscribe("orders", subscription) }()
	unsubscribeCompleted := false
	select {
	case err := <-unsubscribeResult:
		if err != nil {
			t.Fatalf("Unsubscribe before replay release: %v", err)
		}
		unsubscribeCompleted = true
	case <-time.After(100 * time.Millisecond):
		// A transactionally serialized implementation keeps the unsubscribe
		// behind the in-progress replay.
	}
	close(releaseReplay)
	if err := <-replayResult; err != nil {
		t.Fatalf("resubscribeAll: %v", err)
	}
	if !unsubscribeCompleted {
		select {
		case err := <-unsubscribeResult:
			if err != nil {
				t.Fatalf("Unsubscribe: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("Unsubscribe did not finish after replay")
		}
	}

	var observed []string
	deadline := time.After(time.Second)
	for len(observed) < 2 {
		select {
		case method := <-methods:
			observed = append(observed, method)
		case <-deadline:
			t.Fatalf("websocket methods=%v, want replay subscribe followed by unsubscribe", observed)
		}
	}
	if got := observed[len(observed)-1]; got != "unsubscribe" {
		t.Fatalf("websocket methods=%v, successful unsubscribe was overtaken by stale replay", observed)
	}
}

func TestPostRequestContextCanceledWhileWaitingForWriteLockDoesNotWrite(t *testing.T) {
	requestSeen := make(chan struct{}, 1)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		if _, _, err := conn.ReadMessage(); err == nil {
			requestSeen <- struct{}{}
		}
	}))
	client := NewWebsocketClient(context.Background()).WithURL(websocketURL(server.URL))
	t.Cleanup(func() {
		client.Close()
		server.Close()
	})
	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	client.WriteMu.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	type postCallResult struct {
		result chan PostResult
		err    error
	}
	callResult := make(chan postCallResult, 1)
	go func() {
		result, err := client.PostRequestContext(ctx, WsPostRequestPayload{
			Type:    "info",
			Payload: map[string]any{"type": "openOrders"},
		})
		callResult <- postCallResult{result: result, err: err}
	}()
	waitForPendingPost(t, client, time.Second)
	cancel()
	client.WriteMu.Unlock()

	call := <-callResult
	if !errors.Is(call.err, context.Canceled) || call.result != nil {
		t.Fatalf("PostRequestContext result=%v error=%v, want nil/context.Canceled", call.result, call.err)
	}
	select {
	case <-requestSeen:
		t.Fatal("canceled post request was written after waiting for WriteMu")
	case <-time.After(100 * time.Millisecond):
	}
	client.Mu.RLock()
	pending := len(client.PostChannels)
	client.Mu.RUnlock()
	if pending != 0 {
		t.Fatalf("pending post requests=%d, want 0 after canceled send", pending)
	}
}

func TestSuccessfulUnsubscribeReleasesRevisionMetadata(t *testing.T) {
	server := newDrainingWebsocketServer(t)
	client := NewWebsocketClient(context.Background()).WithURL(websocketURL(server.URL))
	t.Cleanup(func() {
		client.Close()
		server.Close()
	})
	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	subscription := map[string]any{"type": "orders", "user": "0xabc"}
	if err := client.Subscribe("orders", subscription, func(WsMessage) {}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if err := client.Unsubscribe("orders", subscription); err != nil {
		t.Fatalf("Unsubscribe: %v", err)
	}
	client.Mu.RLock()
	revisions, channelRemains := client.subscriptionRevisions["orders"]
	client.Mu.RUnlock()
	if channelRemains || len(revisions) != 0 {
		t.Fatalf("successful unsubscribe retained revision metadata: %+v", revisions)
	}
}

func TestConcurrentConnectWaitsForRetainedSubscriptionReplay(t *testing.T) {
	replaySeen := make(chan struct{})
	releaseReplay := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseReplay) }) }
	t.Cleanup(release)

	var replayRequests atomic.Int32
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			var request WsSubscribeRequest
			if err := conn.ReadJSON(&request); err != nil {
				return
			}
			if request.Method != "subscribe" {
				continue
			}
			if replayRequests.Add(1) == 1 {
				close(replaySeen)
				<-releaseReplay
			}
			if err := conn.WriteJSON(map[string]any{
				"channel": "subscriptionResponse",
				"data":    request.Subscription,
			}); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)

	client := NewWebsocketClient(context.Background()).WithURL(websocketURL(server.URL))
	client.SubscriptionAckTimeout = time.Second
	t.Cleanup(client.Close)

	subscription := map[string]any{"type": "l2Book", "coin": "BTC"}
	key := subscriptionKey(subscription)
	client.Mu.Lock()
	client.subscriptions["l2Book"] = map[string]func(WsMessage){key: func(WsMessage) {}}
	client.subscriptionPayloads["l2Book"] = map[string]any{key: subscription}
	client.Mu.Unlock()

	firstResult := make(chan error, 1)
	go func() { firstResult <- client.Connect() }()
	select {
	case <-replaySeen:
	case <-time.After(time.Second):
		t.Fatal("first Connect did not begin retained subscription replay")
	}

	secondResult := make(chan error, 1)
	go func() { secondResult <- client.Connect() }()
	select {
	case err := <-secondResult:
		t.Fatalf("concurrent Connect returned before replay was ready: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	release()
	if err := <-firstResult; err != nil {
		t.Fatalf("first Connect: %v", err)
	}
	if err := <-secondResult; err != nil {
		t.Fatalf("second Connect: %v", err)
	}
	if got := replayRequests.Load(); got != 1 {
		t.Fatalf("retained replay requests=%d, want exactly 1", got)
	}
}

func TestConnectDoesNotPublishSocketInsidePendingSubscriptionTransaction(t *testing.T) {
	accepted := make(chan struct{}, 1)
	var subscriptionRequests atomic.Int32
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		accepted <- struct{}{}
		for {
			var request WsSubscribeRequest
			if err := conn.ReadJSON(&request); err != nil {
				return
			}
			if request.Method != "subscribe" {
				continue
			}
			subscriptionRequests.Add(1)
			if err := conn.WriteJSON(map[string]any{
				"channel": "subscriptionResponse",
				"data":    request.Subscription,
			}); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)

	client := NewWebsocketClient(context.Background()).WithURL(websocketURL(server.URL))
	client.SubscriptionAckTimeout = time.Second
	t.Cleanup(client.Close)

	releaseSubscription := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseSubscription) }) }
	t.Cleanup(release)
	payload := &controlledSubscriptionMarshal{
		payload:   `{"type":"l2Book","coin":"ETH"}`,
		blockCall: 1,
		entered:   make(chan struct{}),
		release:   releaseSubscription,
	}
	subscribeResult := make(chan error, 1)
	go func() {
		subscribeResult <- client.Subscribe("l2Book", payload, func(WsMessage) {})
	}()
	select {
	case <-payload.entered:
	case <-time.After(time.Second):
		t.Fatal("subscription transaction did not reach its blocking payload")
	}

	connectResult := make(chan error, 1)
	go func() { connectResult <- client.Connect() }()
	select {
	case <-accepted:
		release()
		t.Fatal("Connect published a socket while an earlier subscription transaction was pending")
	case <-time.After(100 * time.Millisecond):
	}

	release()
	if err := <-subscribeResult; err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("Subscribe error=%v, want not connected before the later Connect", err)
	}
	if err := <-connectResult; err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if got := subscriptionRequests.Load(); got != 0 {
		t.Fatalf("subscription requests=%d, pending Subscribe was replayed or duplicated", got)
	}
}

func newDrainingWebsocketServer(t *testing.T) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
}

func websocketURL(httpURL string) string {
	return "ws" + strings.TrimPrefix(httpURL, "http")
}

func waitForSubscriptionRevision(t *testing.T, client *WebsocketClient, want uint64, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		client.Mu.RLock()
		got := client.nextSubscriptionRev
		client.Mu.RUnlock()
		if got >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
}

func waitForPendingPost(t *testing.T, client *WebsocketClient, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		client.Mu.RLock()
		pending := len(client.PostChannels)
		client.Mu.RUnlock()
		if pending != 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("post request did not register before waiting for WriteMu")
}
