package sdk

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestPrivateWSFailedAdditionalSubscriptionRecoversExistingOnly(t *testing.T) {
	tests := []struct {
		name      string
		rejectNew bool
	}{
		{name: "reject", rejectNew: true},
		{name: "timeout"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var dials atomic.Int32
			restored := make(chan struct{})
			allowRestoreACK := make(chan struct{})
			onlyExistingRestored := make(chan struct{})
			existingEvent := make(chan struct{}, 1)
			finishServer := make(chan struct{})
			existingArg := WSArg{InstType: "UTA", Topic: "order"}
			failedArg := WSArg{InstType: "UTA", Topic: "fill"}
			upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer conn.Close()
				attempt := dials.Add(1)
				if !serveBitgetLogin(conn) {
					return
				}

				switch attempt {
				case 1:
					first := readBitgetSubscription(t, conn)
					if len(first.Args) != 1 || wsKey(first.Args[0]) != wsKey(existingArg) {
						t.Errorf("first subscription = %+v, want %+v", first, existingArg)
						return
					}
					if err := writeBitgetSubscribeACK(conn, first, true, "success"); err != nil {
						return
					}
					second := readBitgetSubscription(t, conn)
					if len(second.Args) != 1 || wsKey(second.Args[0]) != wsKey(failedArg) {
						t.Errorf("second subscription = %+v, want %+v", second, failedArg)
						return
					}
					if tc.rejectNew {
						if err := writeBitgetSubscribeACK(conn, second, false, "rejected"); err != nil {
							return
						}
					}
					_, _, _ = conn.ReadMessage()
				case 2:
					replay := readBitgetSubscription(t, conn)
					if len(replay.Args) != 1 || wsKey(replay.Args[0]) != wsKey(existingArg) {
						t.Errorf("restored subscription = %+v, want only %+v", replay, existingArg)
						return
					}
					close(restored)
					<-allowRestoreACK
					if err := writeBitgetSubscribeACK(conn, replay, true, "success"); err != nil {
						return
					}
					assertNoBitgetSubscription(t, conn)
					close(onlyExistingRestored)
					_ = conn.WriteJSON(map[string]any{"arg": existingArg, "action": "update", "data": []any{}})
					<-finishServer
				}
			}))
			defer server.Close()

			client := NewPrivateWSClient().WithCredentials("key", "secret", "passphrase")
			client.url = "ws" + strings.TrimPrefix(server.URL, "http")
			client.subscriptionAckTimeout = 75 * time.Millisecond
			defer client.Close()
			started := make(chan struct{})
			recovered := make(chan struct{})
			var startedOnce sync.Once
			var recoveredOnce sync.Once
			client.SetReconnectHooks(func(error) {
				startedOnce.Do(func() { close(started) })
			}, func() {
				recoveredOnce.Do(func() { close(recovered) })
			})

			if err := client.Subscribe(context.Background(), existingArg, func(json.RawMessage) {
				select {
				case existingEvent <- struct{}{}:
				default:
				}
			}); err != nil {
				t.Fatalf("subscribe existing topic: %v", err)
			}
			err := client.Subscribe(context.Background(), failedArg, func(json.RawMessage) {
				t.Error("failed subscription handler was invoked")
			})
			if err == nil {
				t.Fatal("failed additional subscription unexpectedly succeeded")
			}
			waitForBitgetSignal(t, started, "reconnect start after additional subscription failure")
			waitForBitgetSignal(t, restored, "existing subscription replay")
			select {
			case <-recovered:
				t.Fatal("recovered emitted before existing subscription ACK")
			default:
			}
			close(allowRestoreACK)
			waitForBitgetSignal(t, recovered, "recovery after existing subscription ACK")
			waitForBitgetSignal(t, onlyExistingRestored, "proof that failed subscription was not replayed")
			waitForBitgetSignal(t, existingEvent, "event on restored existing subscription")
			close(finishServer)

			client.mu.RLock()
			_, retainedExistingSub := client.subs[wsKey(existingArg)]
			_, retainedExistingHandler := client.handlers[wsKey(existingArg)]
			_, retainedFailedSub := client.subs[wsKey(failedArg)]
			_, retainedFailedHandler := client.handlers[wsKey(failedArg)]
			client.mu.RUnlock()
			if !retainedExistingSub || !retainedExistingHandler || retainedFailedSub || retainedFailedHandler {
				t.Fatalf("state after recovery: existing sub/handler=%v/%v failed sub/handler=%v/%v",
					retainedExistingSub, retainedExistingHandler, retainedFailedSub, retainedFailedHandler)
			}
		})
	}
}

func TestPrivateWSSubscribeWaitsForRecoveryReplay(t *testing.T) {
	var dials atomic.Int32
	closeInitial := make(chan struct{})
	firstRecoveryKey := make(chan string, 1)
	allowReplayACK := make(chan struct{})
	newKeySeen := make(chan string, 1)
	finishServer := make(chan struct{})
	existingArg := WSArg{InstType: "UTA", Topic: "order"}
	addedArg := WSArg{InstType: "UTA", Topic: "fill"}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		attempt := dials.Add(1)
		if !serveBitgetLogin(conn) {
			return
		}

		switch attempt {
		case 1:
			initial := readBitgetSubscription(t, conn)
			if err := writeBitgetSubscribeACK(conn, initial, true, "success"); err != nil {
				return
			}
			<-closeInitial
		case 2:
			replay := readBitgetSubscription(t, conn)
			if len(replay.Args) != 1 {
				return
			}
			firstRecoveryKey <- wsKey(replay.Args[0])
			if wsKey(replay.Args[0]) != wsKey(existingArg) {
				return
			}
			<-allowReplayACK
			if err := writeBitgetSubscribeACK(conn, replay, true, "success"); err != nil {
				return
			}
			added := readBitgetSubscription(t, conn)
			if len(added.Args) != 1 {
				return
			}
			newKeySeen <- wsKey(added.Args[0])
			if err := writeBitgetSubscribeACK(conn, added, true, "success"); err != nil {
				return
			}
			<-finishServer
		}
	}))
	defer server.Close()

	client := NewPrivateWSClient().WithCredentials("key", "secret", "passphrase")
	client.url = "ws" + strings.TrimPrefix(server.URL, "http")
	client.subscriptionAckTimeout = time.Second
	defer client.Close()
	started := make(chan struct{})
	recovered := make(chan struct{})
	var startedOnce sync.Once
	var recoveredOnce sync.Once
	client.SetReconnectHooks(func(error) {
		startedOnce.Do(func() { close(started) })
	}, func() {
		recoveredOnce.Do(func() { close(recovered) })
	})

	if err := client.Subscribe(context.Background(), existingArg, func(json.RawMessage) {}); err != nil {
		t.Fatalf("subscribe initial topic: %v", err)
	}
	close(closeInitial)
	waitForBitgetSignal(t, started, "recovery start")

	addedResult := make(chan error, 1)
	go func() {
		addedResult <- client.Subscribe(context.Background(), addedArg, func(json.RawMessage) {})
	}()

	select {
	case key := <-firstRecoveryKey:
		if key != wsKey(existingArg) {
			t.Fatalf("first request on recovery connection = %q, want replayed %q", key, wsKey(existingArg))
		}
	case <-time.After(4 * time.Second):
		t.Fatal("timed out waiting for first recovery subscription")
	}
	select {
	case err := <-addedResult:
		t.Fatalf("new subscription completed before replay ACK: %v", err)
	default:
	}
	close(allowReplayACK)
	waitForBitgetSignal(t, recovered, "recovery completion")
	select {
	case key := <-newKeySeen:
		if key != wsKey(addedArg) {
			t.Fatalf("post-recovery subscription = %q, want %q", key, wsKey(addedArg))
		}
	case <-time.After(4 * time.Second):
		t.Fatal("timed out waiting for post-recovery subscription")
	}
	if err := waitForBitgetResult(t, addedResult, "post-recovery subscription result"); err != nil {
		t.Fatalf("post-recovery subscription: %v", err)
	}
	close(finishServer)
}

func TestPrivateWSConcurrentSameTopicFailureRollsBackSuccessfulHandler(t *testing.T) {
	var dials atomic.Int32
	firstRequestSeen := make(chan struct{})
	restored := make(chan struct{})
	allowRestoreACK := make(chan struct{})
	firstHandlerEvent := make(chan struct{}, 1)
	secondHandlerEvent := make(chan struct{}, 1)
	finishServer := make(chan struct{})
	arg := WSArg{InstType: "UTA", Topic: "order"}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		attempt := dials.Add(1)
		if !serveBitgetLogin(conn) {
			return
		}
		switch attempt {
		case 1:
			first := readBitgetSubscription(t, conn)
			close(firstRequestSeen)
			secondResult := beginBitgetSubscriptionRead(conn)
			var second wsRequest
			select {
			case got := <-secondResult:
				if got.err != nil {
					return
				}
				second = got.req
				if err := writeBitgetSubscribeACK(conn, first, true, "success"); err != nil {
					return
				}
			case <-time.After(75 * time.Millisecond):
				if err := writeBitgetSubscribeACK(conn, first, true, "success"); err != nil {
					return
				}
				got := <-secondResult
				if got.err != nil {
					return
				}
				second = got.req
			}
			if len(second.Args) != 1 || wsKey(second.Args[0]) != wsKey(arg) {
				t.Errorf("duplicate subscription = %+v, want %+v", second, arg)
				return
			}
			if err := writeBitgetSubscribeACK(conn, second, false, "duplicate"); err != nil {
				return
			}
			_, _, _ = conn.ReadMessage()
		case 2:
			replay := readBitgetSubscription(t, conn)
			if len(replay.Args) != 1 || wsKey(replay.Args[0]) != wsKey(arg) {
				t.Errorf("restored subscription = %+v, want %+v", replay, arg)
				return
			}
			close(restored)
			<-allowRestoreACK
			if err := writeBitgetSubscribeACK(conn, replay, true, "success"); err != nil {
				return
			}
			_ = conn.WriteJSON(map[string]any{"arg": arg, "action": "update", "data": []any{}})
			<-finishServer
		}
	}))
	defer server.Close()

	client := NewPrivateWSClient().WithCredentials("key", "secret", "passphrase")
	client.url = "ws" + strings.TrimPrefix(server.URL, "http")
	client.subscriptionAckTimeout = time.Second
	defer client.Close()
	recovered := make(chan struct{})
	var recoveredOnce sync.Once
	client.SetReconnectHooks(func(error) {}, func() {
		recoveredOnce.Do(func() { close(recovered) })
	})

	firstResult := make(chan error, 1)
	go func() {
		firstResult <- client.Subscribe(context.Background(), arg, func(json.RawMessage) {
			firstHandlerEvent <- struct{}{}
		})
	}()
	waitForBitgetSignal(t, firstRequestSeen, "first same-topic subscription")
	secondResult := make(chan error, 1)
	go func() {
		secondResult <- client.Subscribe(context.Background(), arg, func(json.RawMessage) {
			secondHandlerEvent <- struct{}{}
		})
	}()
	if err := waitForBitgetResult(t, firstResult, "first same-topic subscription"); err != nil {
		t.Fatalf("first same-topic subscription: %v", err)
	}
	if err := waitForBitgetResult(t, secondResult, "rejected duplicate subscription"); err == nil {
		t.Fatal("duplicate same-topic subscription unexpectedly succeeded")
	}
	waitForBitgetSignal(t, restored, "same-topic rollback replay")
	close(allowRestoreACK)
	waitForBitgetSignal(t, recovered, "same-topic rollback recovery")
	waitForBitgetSignal(t, firstHandlerEvent, "original same-topic handler")
	select {
	case <-secondHandlerEvent:
		t.Fatal("rejected same-topic handler replaced the successful handler")
	default:
	}
	close(finishServer)
}

func TestPrivateWSSubscriptionErrorDoesNotCompletePendingTradeRequest(t *testing.T) {
	clientConn, peer := bitgetPrivateWSPair(t)
	client := NewPrivateWSClient().WithCredentials("key", "secret", "passphrase")
	client.subscriptionAckTimeout = time.Second
	client.requestTimeout = time.Second
	client.mu.Lock()
	client.conn = clientConn
	client.authenticated = true
	client.mu.Unlock()
	defer client.Close()
	go client.readLoop(clientConn, make(chan error, 1))

	type tradeResult struct {
		payload []byte
		err     error
	}
	tradeDone := make(chan tradeResult, 1)
	go func() {
		payload, err := client.sendRequest("trade-1", map[string]any{
			"id": "trade-1",
			"op": "trade",
		})
		tradeDone <- tradeResult{payload: payload, err: err}
	}()

	var tradeRequest map[string]any
	if err := peer.ReadJSON(&tradeRequest); err != nil {
		t.Fatalf("read trade request: %v", err)
	}
	if tradeRequest["id"] != "trade-1" {
		t.Fatalf("trade request = %+v", tradeRequest)
	}

	arg := WSArg{InstType: "UTA", Topic: "order"}
	subscribeDone := make(chan error, 1)
	go func() {
		subscribeDone <- client.subscribeOnConn(context.Background(), clientConn, arg)
	}()
	var subscribeRequest wsRequest
	if err := peer.ReadJSON(&subscribeRequest); err != nil {
		t.Fatalf("read subscription request: %v", err)
	}
	if len(subscribeRequest.Args) != 1 || wsKey(subscribeRequest.Args[0]) != wsKey(arg) {
		t.Fatalf("subscription request = %+v", subscribeRequest)
	}
	if err := peer.WriteJSON(map[string]any{
		"event": "error",
		"code":  "30001",
		"msg":   "subscription rejected",
		"arg":   arg,
	}); err != nil {
		t.Fatalf("write subscription error: %v", err)
	}

	select {
	case result := <-tradeDone:
		t.Fatalf("subscription error completed pending trade request: payload=%s err=%v", result.payload, result.err)
	case <-time.After(100 * time.Millisecond):
	}
	select {
	case err := <-subscribeDone:
		if err == nil || !strings.Contains(err.Error(), "subscription rejected") {
			t.Fatalf("subscription error = %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("subscription error did not reach its exact waiter")
	}

	if err := peer.WriteJSON(map[string]any{
		"id":    "trade-1",
		"event": "trade",
		"code":  "0",
		"msg":   "success",
	}); err != nil {
		t.Fatalf("write trade response: %v", err)
	}
	select {
	case result := <-tradeDone:
		if result.err != nil {
			t.Fatalf("trade request failed: %v", result.err)
		}
		if !strings.Contains(string(result.payload), `"id":"trade-1"`) {
			t.Fatalf("trade response = %s", result.payload)
		}
	case <-time.After(time.Second):
		t.Fatal("trade response did not reach pending request")
	}
}

func serveBitgetLogin(conn *websocket.Conn) bool {
	var login wsLoginRequest
	if err := conn.ReadJSON(&login); err != nil {
		return false
	}
	return conn.WriteJSON(map[string]any{"event": "login", "code": "0", "msg": "success"}) == nil
}

func readBitgetSubscription(t *testing.T, conn *websocket.Conn) wsRequest {
	t.Helper()
	var req wsRequest
	if err := conn.ReadJSON(&req); err != nil {
		t.Errorf("read subscription: %v", err)
	}
	return req
}

type bitgetSubscriptionReadResult struct {
	req wsRequest
	err error
}

func beginBitgetSubscriptionRead(conn *websocket.Conn) <-chan bitgetSubscriptionReadResult {
	resultCh := make(chan bitgetSubscriptionReadResult, 1)
	go func() {
		var req wsRequest
		err := conn.ReadJSON(&req)
		resultCh <- bitgetSubscriptionReadResult{req: req, err: err}
	}()
	return resultCh
}

func assertNoBitgetSubscription(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	reqCh := make(chan wsRequest, 1)
	go func() {
		var req wsRequest
		if conn.ReadJSON(&req) == nil {
			reqCh <- req
		}
	}()
	select {
	case req := <-reqCh:
		t.Errorf("unexpected restored subscription: %+v", req)
	case <-time.After(100 * time.Millisecond):
	}
}

func waitForBitgetSignal(t *testing.T, ch <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(4 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func waitForBitgetResult(t *testing.T, ch <-chan error, description string) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(4 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
		return nil
	}
}
