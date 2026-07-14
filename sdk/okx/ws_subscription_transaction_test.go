package okx

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func TestSubscriptionOperationsCommitInWireOrder(t *testing.T) {
	args := WsSubscribeArgs{Channel: "orders", InstType: "SWAP"}
	type request struct {
		ID int64  `json:"id"`
		Op string `json:"op"`
	}
	requests := make(chan request, 2)
	responses := make(chan map[string]any, 2)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		readDone := make(chan struct{})
		go func() {
			defer close(readDone)
			for {
				var req request
				if err := conn.ReadJSON(&req); err != nil {
					return
				}
				requests <- req
			}
		}()
		for {
			select {
			case response := <-responses:
				if err := conn.WriteJSON(response); err != nil {
					return
				}
			case <-readDone:
				return
			case <-r.Context().Done():
				return
			}
		}
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	client := NewWSClient(ctx).WithURL("ws" + strings.TrimPrefix(server.URL, "http"))
	t.Cleanup(client.Close)
	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	firstCalled := make(chan struct{}, 1)
	firstResult := make(chan error, 1)
	go func() {
		firstResult <- client.Subscribe(args, func([]byte) { firstCalled <- struct{}{} })
	}()
	first := <-requests
	if first.Op != "subscribe" {
		t.Fatalf("first operation=%q, want subscribe", first.Op)
	}

	secondResult := make(chan error, 1)
	secondStarted := make(chan struct{})
	go func() {
		close(secondStarted)
		secondResult <- client.Unsubscribe(args)
	}()
	<-secondStarted

	var second request
	select {
	case second = <-requests:
		t.Errorf("unsubscribe reached the wire before the successful subscribe committed")
	case <-time.After(100 * time.Millisecond):
	}
	responses <- map[string]any{
		"id":    strconv.FormatInt(first.ID, 10),
		"event": "subscribe",
		"code":  "0",
	}
	if err := <-firstResult; err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if second.ID == 0 {
		select {
		case second = <-requests:
		case <-time.After(time.Second):
			t.Fatal("serialized unsubscribe never reached the wire")
		}
	}
	if second.Op != "unsubscribe" {
		t.Fatalf("second operation=%q, want unsubscribe", second.Op)
	}
	responses <- map[string]any{
		"id":    strconv.FormatInt(second.ID, 10),
		"event": "error",
		"code":  "60012",
		"msg":   "rejected",
	}
	if err := <-secondResult; err == nil {
		t.Fatal("Unsubscribe succeeded after server rejection")
	}

	client.mu.Lock()
	handler := client.Subs[args]
	client.mu.Unlock()
	if handler == nil {
		t.Fatal("successful Subscribe was lost after a later rejected Unsubscribe")
	}
	handler(json.RawMessage(`{}`))
	select {
	case <-firstCalled:
	case <-time.After(time.Second):
		t.Fatal("retained successful handler was not callable")
	}
}

func TestSocketHandlerCanUnsubscribeWithoutBlockingAcknowledgementReader(t *testing.T) {
	args := WsSubscribeArgs{Channel: "orders", InstType: "SWAP"}
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var subscribe struct {
			ID int64 `json:"id"`
		}
		if err := conn.ReadJSON(&subscribe); err != nil {
			return
		}
		if err := conn.WriteJSON(map[string]any{
			"id":    strconv.FormatInt(subscribe.ID, 10),
			"event": "subscribe",
			"code":  "0",
		}); err != nil {
			return
		}
		if err := conn.WriteJSON(map[string]any{
			"arg":  args,
			"data": []map[string]any{{"ordId": "trigger"}},
		}); err != nil {
			return
		}
		var unsubscribe struct {
			ID int64 `json:"id"`
		}
		if err := conn.ReadJSON(&unsubscribe); err != nil {
			return
		}
		_ = conn.WriteJSON(map[string]any{
			"id":    strconv.FormatInt(unsubscribe.ID, 10),
			"event": "unsubscribe",
			"code":  "0",
		})
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	client := NewWSClient(ctx).WithURL("ws" + strings.TrimPrefix(server.URL, "http"))
	t.Cleanup(client.Close)
	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	unsubscribed := make(chan error, 1)
	if err := client.Subscribe(args, func([]byte) {
		unsubscribed <- client.Unsubscribe(args)
	}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	select {
	case err := <-unsubscribed:
		if err != nil {
			t.Fatalf("callback Unsubscribe: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("callback Unsubscribe deadlocked the websocket acknowledgement reader")
	}
}

func TestRejectedSubscribeIsNotRetainedOrReplayed(t *testing.T) {
	args := WsSubscribeArgs{Channel: "orders", InstType: "SWAP"}
	replayed := make(chan struct{}, 1)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		var request struct {
			ID int64  `json:"id"`
			Op string `json:"op"`
		}
		if err := conn.ReadJSON(&request); err != nil {
			return
		}
		if err := conn.WriteJSON(map[string]any{
			"id":    strconv.FormatInt(request.ID, 10),
			"event": "error",
			"code":  "60012",
			"msg":   "rejected",
		}); err != nil {
			return
		}

		if err := conn.ReadJSON(&request); err == nil && request.Op == "subscribe" {
			replayed <- struct{}{}
		}
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	client := NewWSClient(ctx).WithURL("ws" + strings.TrimPrefix(server.URL, "http"))
	t.Cleanup(client.Close)
	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := client.Subscribe(args, func([]byte) {}); err == nil {
		t.Fatal("Subscribe succeeded after the server rejected it")
	}
	if err := client.replayPublicSubscriptions(client.currentConnection()); err != nil {
		t.Fatalf("replayPublicSubscriptions: %v", err)
	}

	client.mu.Lock()
	_, retained := client.Subs[args]
	client.mu.Unlock()
	if retained {
		t.Fatal("rejected subscription remained in the confirmed subscription set")
	}
	select {
	case <-replayed:
		t.Fatal("reconnect replay included a rejected subscription")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestTimedOutSubscribeIsNotRetained(t *testing.T) {
	args := WsSubscribeArgs{Channel: "orders", InstType: "SWAP"}
	requestReceived := make(chan struct{})
	holdConnection := make(chan struct{})
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
		close(requestReceived)
		<-holdConnection
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(holdConnection) })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	client := NewWSClient(ctx).WithURL("ws" + strings.TrimPrefix(server.URL, "http"))
	client.subscriptionTimeout = 25 * time.Millisecond
	t.Cleanup(client.Close)
	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := client.Subscribe(args, func([]byte) {}); err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("Subscribe error=%v, want timeout", err)
	}
	select {
	case <-requestReceived:
	default:
		t.Fatal("server did not receive the timed-out subscribe request")
	}
	client.mu.Lock()
	_, retained := client.Subs[args]
	client.mu.Unlock()
	if retained {
		t.Fatal("timed-out subscription remained in the confirmed subscription set")
	}
}

func TestReplayWaitsForPendingSubscribeCommit(t *testing.T) {
	args := WsSubscribeArgs{Channel: "orders", InstType: "SWAP"}
	firstReceived := make(chan struct{})
	releaseFirst := make(chan struct{})
	replayReceived := make(chan struct{})
	holdConnection := make(chan struct{})
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var first struct {
			ID int64  `json:"id"`
			Op string `json:"op"`
		}
		if err := conn.ReadJSON(&first); err != nil {
			return
		}
		close(firstReceived)
		<-releaseFirst
		_ = conn.WriteJSON(map[string]any{
			"id":    strconv.FormatInt(first.ID, 10),
			"event": "subscribe",
			"code":  "0",
		})
		var replay struct {
			Op string `json:"op"`
		}
		if err := conn.ReadJSON(&replay); err != nil {
			return
		}
		if replay.Op == "subscribe" {
			close(replayReceived)
		}
		<-holdConnection
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(holdConnection) })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	client := NewWSClient(ctx).WithURL("ws" + strings.TrimPrefix(server.URL, "http"))
	t.Cleanup(client.Close)
	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	subscribeResult := make(chan error, 1)
	go func() {
		subscribeResult <- client.Subscribe(args, func([]byte) {})
	}()
	select {
	case <-firstReceived:
	case <-time.After(time.Second):
		t.Fatal("server did not receive pending subscribe")
	}

	client.mu.Lock()
	_, retainedWhilePending := client.Subs[args]
	client.mu.Unlock()
	if retainedWhilePending {
		t.Fatal("pending subscription appeared in the confirmed subscription set")
	}
	replayResult := make(chan error, 1)
	go func() {
		replayResult <- client.replayPublicSubscriptions(client.currentConnection())
	}()
	select {
	case err := <-replayResult:
		t.Fatalf("replay returned before pending subscribe committed: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseFirst)
	if err := <-subscribeResult; err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	select {
	case <-replayReceived:
	case <-time.After(time.Second):
		t.Fatal("replay did not send the subscription after its commit")
	}
	if err := <-replayResult; err != nil {
		t.Fatalf("replayPublicSubscriptions: %v", err)
	}
}

func TestPendingSubscribeHandlerReceivesDataWhileAwaitingCommit(t *testing.T) {
	client := NewWSClient(context.Background())
	args := WsSubscribeArgs{Channel: "orders", InstType: "SWAP"}
	received := make(chan struct{}, 1)
	client.beginSubscribe(args, func([]byte) { received <- struct{}{} })

	client.handleMessage([]byte(`{"arg":{"channel":"orders","instType":"SWAP"},"data":[{}]}`))
	select {
	case <-received:
	case <-time.After(time.Second):
		t.Fatal("data arriving while Subscribe was committing did not reach the pending handler")
	}
}

func TestRejectedUnsubscribeRetainsConfirmedSubscription(t *testing.T) {
	args := WsSubscribeArgs{Channel: "orders", InstType: "SWAP"}
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for requestNumber := 1; requestNumber <= 2; requestNumber++ {
			var request struct {
				ID int64  `json:"id"`
				Op string `json:"op"`
			}
			if err := conn.ReadJSON(&request); err != nil {
				return
			}
			response := map[string]any{
				"id":    strconv.FormatInt(request.ID, 10),
				"event": request.Op,
				"code":  "0",
			}
			if request.Op == "unsubscribe" {
				response["event"] = "error"
				response["code"] = "60012"
				response["msg"] = "rejected"
			}
			if err := conn.WriteJSON(response); err != nil {
				return
			}
		}
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	client := NewWSClient(ctx).WithURL("ws" + strings.TrimPrefix(server.URL, "http"))
	t.Cleanup(client.Close)
	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := client.Subscribe(args, func([]byte) {}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if err := client.Unsubscribe(args); err == nil {
		t.Fatal("Unsubscribe succeeded after the server rejected it")
	}
	client.mu.Lock()
	_, retained := client.Subs[args]
	client.mu.Unlock()
	if !retained {
		t.Fatal("rejected unsubscribe removed the confirmed subscription")
	}
}

func TestConcurrentSubscribeOperationsCommitInCallOrder(t *testing.T) {
	args := WsSubscribeArgs{Channel: "orders", InstType: "SWAP"}
	firstReceived := make(chan struct{})
	secondReceived := make(chan struct{})
	releaseFirst := make(chan struct{})
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var first, second struct {
			ID int64 `json:"id"`
		}
		if err := conn.ReadJSON(&first); err != nil {
			return
		}
		close(firstReceived)
		<-releaseFirst
		if err := conn.WriteJSON(map[string]any{
			"id":    strconv.FormatInt(first.ID, 10),
			"event": "subscribe",
			"code":  "0",
		}); err != nil {
			return
		}
		if err := conn.ReadJSON(&second); err != nil {
			return
		}
		close(secondReceived)
		_ = conn.WriteJSON(map[string]any{
			"id":    strconv.FormatInt(second.ID, 10),
			"event": "subscribe",
			"code":  "0",
		})
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	client := NewWSClient(ctx).WithURL("ws" + strings.TrimPrefix(server.URL, "http"))
	t.Cleanup(client.Close)
	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	called := make(chan string, 1)
	firstResult := make(chan error, 1)
	go func() {
		firstResult <- client.Subscribe(args, func([]byte) { called <- "first" })
	}()
	select {
	case <-firstReceived:
	case <-time.After(time.Second):
		t.Fatal("server did not receive first subscribe")
	}
	secondResult := make(chan error, 1)
	go func() {
		secondResult <- client.Subscribe(args, func([]byte) { called <- "second" })
	}()
	select {
	case <-secondReceived:
		t.Fatal("second Subscribe reached the wire before the first committed")
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseFirst)
	if err := <-firstResult; err != nil {
		t.Fatalf("first Subscribe: %v", err)
	}
	select {
	case <-secondReceived:
	case <-time.After(time.Second):
		t.Fatal("serialized second Subscribe never reached the wire")
	}
	if err := <-secondResult; err != nil {
		t.Fatalf("second Subscribe: %v", err)
	}

	client.mu.Lock()
	handler := client.Subs[args]
	client.mu.Unlock()
	if handler == nil {
		t.Fatal("newer confirmed subscription was not retained")
	}
	handler(nil)
	select {
	case got := <-called:
		if got != "second" {
			t.Fatalf("retained handler=%s, want second", got)
		}
	case <-time.After(time.Second):
		t.Fatal("retained handler was not callable")
	}
}

func TestStaleSocketAcknowledgementCannotCommitSubscription(t *testing.T) {
	type request struct {
		ID int64 `json:"id"`
	}
	requests := make(chan request, 1)
	hold := make(chan struct{})
	var holdOnce sync.Once
	releaseHold := func() { holdOnce.Do(func() { close(hold) }) }
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var req request
		if err := conn.ReadJSON(&req); err == nil {
			requests <- req
		}
		<-hold
	}))
	t.Cleanup(server.Close)
	t.Cleanup(releaseHold)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	client := NewWSClient(ctx).WithURL("ws" + strings.TrimPrefix(server.URL, "http"))
	client.subscriptionTimeout = 50 * time.Millisecond
	t.Cleanup(client.Close)
	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	connA := client.currentConnection()
	t.Cleanup(func() { _ = connA.Close() })

	entered := make(chan struct{})
	releaseACK := make(chan struct{})
	var hookOnce sync.Once
	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()),
		zapcore.AddSync(io.Discard),
		zap.DebugLevel,
	)
	client.Logger = zap.New(core, zap.Hooks(func(entry zapcore.Entry) error {
		if entry.Message == "WS received msg" {
			hookOnce.Do(func() {
				close(entered)
				<-releaseACK
			})
		}
		return nil
	})).Sugar()

	result := make(chan error, 1)
	args := WsSubscribeArgs{Channel: "orders", InstType: "SWAP"}
	go func() { result <- client.Subscribe(args, func([]byte) {}) }()
	var req request
	select {
	case req = <-requests:
	case <-time.After(time.Second):
		t.Fatal("server did not receive subscription")
	}

	connB, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial replacement websocket: %v", err)
	}
	t.Cleanup(func() { _ = connB.Close() })

	ackDone := make(chan struct{})
	go func() {
		defer close(ackDone)
		client.handleSocketMessageFrom(connA, []byte(`{"id":"`+strconv.FormatInt(req.ID, 10)+`","event":"subscribe","code":"0"}`))
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("old-socket ACK did not reach post-connection-check barrier")
	}
	client.mu.Lock()
	client.Conn = connB
	client.mu.Unlock()
	close(releaseACK)
	<-ackDone

	if err := <-result; err == nil {
		t.Fatal("Subscribe committed an acknowledgement from a replaced socket")
	}
	client.mu.Lock()
	_, committed := client.Subs[args]
	client.mu.Unlock()
	if committed {
		t.Fatal("stale-socket acknowledgement entered the confirmed subscription set")
	}
	releaseHold()
}

func TestExplicitConnectAfterCloseReplaysRetainedSubscriptions(t *testing.T) {
	type request struct {
		ID   int64             `json:"id"`
		Op   string            `json:"op"`
		Args []WsSubscribeArgs `json:"args"`
	}
	var connections atomic.Int32
	replayed := make(chan request, 1)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		connection := connections.Add(1)
		for {
			var req request
			if err := conn.ReadJSON(&req); err != nil {
				return
			}
			if connection == 1 {
				if err := conn.WriteJSON(map[string]any{
					"id": strconv.FormatInt(req.ID, 10), "event": "subscribe", "code": "0",
				}); err != nil {
					return
				}
				continue
			}
			replayed <- req
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					return
				}
			}
		}
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	client := NewWSClient(ctx).WithURL("ws" + strings.TrimPrefix(server.URL, "http"))
	t.Cleanup(client.Close)
	if err := client.Connect(); err != nil {
		t.Fatalf("first Connect: %v", err)
	}
	args := WsSubscribeArgs{Channel: "orders", InstType: "SWAP"}
	if err := client.Subscribe(args, func([]byte) {}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	client.Close()
	if err := client.Connect(); err != nil {
		t.Fatalf("Connect after Close: %v", err)
	}
	select {
	case req := <-replayed:
		if req.Op != "subscribe" || len(req.Args) != 1 || req.Args[0] != args {
			t.Fatalf("replayed request=%+v, want retained subscription", req)
		}
	case <-time.After(time.Second):
		t.Fatal("Connect after Close did not replay the retained subscription")
	}
}

func TestCloseStopsLifecycleWorkersAndRestartUsesFreshCallbackQueue(t *testing.T) {
	client := NewWSClient(context.Background())
	t.Cleanup(client.Close)
	lifecycleCtx, lifecycleRevision := client.lifecycleForExplicitConnect()
	pingDone := make(chan struct{})
	go func() {
		client.pingLoop(lifecycleCtx, lifecycleRevision)
		close(pingDone)
	}()

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	staleRan := make(chan struct{}, 1)
	firstConn := &websocket.Conn{}
	client.callbacks.activateConnection(0, firstConn, false)
	if !client.callbacks.enqueueData(firstConn, []okxWebsocketCallback{{
		kind: okxWebsocketCallbackData,
		run: func() {
			close(firstStarted)
			<-releaseFirst
		},
	}}) {
		t.Fatal("first callback unexpectedly overflowed")
	}
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first callback did not start")
	}
	if !client.callbacks.enqueueData(firstConn, []okxWebsocketCallback{{
		kind: okxWebsocketCallbackData,
		run:  func() { staleRan <- struct{}{} },
	}}) {
		t.Fatal("stale callback unexpectedly overflowed")
	}
	client.Close()
	close(releaseFirst)

	select {
	case <-pingDone:
	case <-time.After(time.Second):
		t.Fatal("Close did not stop the lifecycle ping worker")
	}
	select {
	case <-staleRan:
		t.Fatal("Close drained a queued callback from the terminated lifecycle")
	case <-time.After(100 * time.Millisecond):
	}

	client.lifecycleForExplicitConnect()
	freshRan := make(chan struct{}, 1)
	freshConn := &websocket.Conn{}
	client.callbacks.activateConnection(0, freshConn, false)
	if !client.callbacks.enqueueData(freshConn, []okxWebsocketCallback{{
		kind: okxWebsocketCallbackData,
		run:  func() { freshRan <- struct{}{} },
	}}) {
		t.Fatal("fresh callback unexpectedly overflowed")
	}
	select {
	case <-freshRan:
	case <-time.After(time.Second):
		t.Fatal("restarted lifecycle did not start a fresh callback worker")
	}
}

func TestPrivateConnectSerializesAuthenticationBeforeSubscribe(t *testing.T) {
	type request struct {
		ID int64  `json:"id"`
		Op string `json:"op"`
	}
	requests := make(chan request, 4)
	responses := make(chan any, 4)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		readDone := make(chan struct{})
		go func() {
			defer close(readDone)
			for {
				var req request
				if err := conn.ReadJSON(&req); err != nil {
					return
				}
				requests <- req
			}
		}()
		for {
			select {
			case response := <-responses:
				if err := conn.WriteJSON(response); err != nil {
					return
				}
			case <-readDone:
				return
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
	connectResult := make(chan error, 1)
	go func() { connectResult <- client.Connect() }()
	select {
	case req := <-requests:
		if req.Op != "login" {
			t.Fatalf("first private request=%+v, want login", req)
		}
	case <-time.After(time.Second):
		t.Fatal("private Connect did not send login")
	}

	args := WsSubscribeArgs{Channel: "orders", InstType: "SWAP"}
	subscribeResult := make(chan error, 1)
	go func() { subscribeResult <- client.Subscribe(args, func([]byte) {}) }()
	select {
	case req := <-requests:
		t.Fatalf("request %+v reached unauthenticated private socket", req)
	case <-time.After(100 * time.Millisecond):
	}
	responses <- map[string]any{"event": "login", "code": "0"}
	if err := <-connectResult; err != nil {
		t.Fatalf("Connect: %v", err)
	}
	var subscribe request
	select {
	case subscribe = <-requests:
		if subscribe.Op != "subscribe" {
			t.Fatalf("post-login request=%+v, want subscribe", subscribe)
		}
	case <-time.After(time.Second):
		t.Fatal("Subscribe did not reach socket after login")
	}
	responses <- map[string]any{
		"id": strconv.FormatInt(subscribe.ID, 10), "event": "subscribe", "code": "0",
	}
	if err := <-subscribeResult; err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
}

func TestCloseStopsInFlightReconnectAndLaterConnectRestartsLifecycle(t *testing.T) {
	dropFirst := make(chan struct{})
	reconnectStarted := make(chan struct{})
	releaseReconnect := make(chan struct{})
	holdConnections := make(chan struct{})
	laterConnected := make(chan struct{}, 1)
	var requests atomic.Int32
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := requests.Add(1)
		switch request {
		case 1:
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			<-dropFirst
			_ = conn.WriteControl(
				websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, "rotate"),
				time.Now().Add(time.Second),
			)
			_ = conn.Close()
		case 2:
			close(reconnectStarted)
			<-releaseReconnect
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer conn.Close()
			<-holdConnections
		default:
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			laterConnected <- struct{}{}
			defer conn.Close()
			<-holdConnections
		}
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(holdConnections) })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	client := NewWSClient(ctx).WithURL("ws" + strings.TrimPrefix(server.URL, "http"))
	client.reconnectWait = 10 * time.Millisecond
	t.Cleanup(client.Close)
	if err := client.Connect(); err != nil {
		t.Fatalf("initial Connect: %v", err)
	}
	close(dropFirst)

	select {
	case <-reconnectStarted:
	case <-time.After(time.Second):
		t.Fatal("automatic reconnect did not enter the blocked dial")
	}
	if conn := client.currentConnection(); conn != nil {
		t.Fatal("expected the dropped connection to be cleared before Close")
	}

	client.Close()
	close(releaseReconnect)
	time.Sleep(100 * time.Millisecond)
	if conn := client.currentConnection(); conn != nil {
		t.Fatal("Close allowed an already in-flight automatic reconnect to install a connection")
	}

	if err := client.Connect(); err != nil {
		t.Fatalf("explicit Connect after Close: %v", err)
	}
	select {
	case <-laterConnected:
	case <-time.After(time.Second):
		t.Fatal("explicit Connect after Close did not start a new websocket lifecycle")
	}

	// Prove the server observed a fresh HTTP upgrade rather than Connect merely
	// returning a socket installed by the cancelled automatic reconnect.
	if got := requests.Load(); got < 3 {
		t.Fatalf("websocket upgrade requests=%d, want at least 3", got)
	}
}
