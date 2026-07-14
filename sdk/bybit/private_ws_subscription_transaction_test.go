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
			upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer conn.Close()
				attempt := dials.Add(1)
				if !serveBybitAuth(t, conn) {
					return
				}

				switch attempt {
				case 1:
					first := readBybitSubscription(t, conn)
					if len(first.Args) != 1 || first.Args[0] != "order" {
						t.Errorf("first subscription = %+v, want order", first)
						return
					}
					if err := writeBybitSubscribeACK(conn, first, true, "OK"); err != nil {
						return
					}
					second := readBybitSubscription(t, conn)
					if len(second.Args) != 1 || second.Args[0] != "execution" {
						t.Errorf("second subscription = %+v, want execution", second)
						return
					}
					if tc.rejectNew {
						if err := writeBybitSubscribeACK(conn, second, false, "rejected"); err != nil {
							return
						}
					}
					_, _, _ = conn.ReadMessage()
				case 2:
					replay := readBybitSubscription(t, conn)
					if len(replay.Args) != 1 || replay.Args[0] != "order" {
						t.Errorf("restored subscription = %+v, want only order", replay)
						return
					}
					close(restored)
					<-allowRestoreACK
					if err := writeBybitSubscribeACK(conn, replay, true, "OK"); err != nil {
						return
					}
					assertNoBybitSubscription(t, conn)
					close(onlyExistingRestored)
					_ = conn.WriteJSON(map[string]any{"topic": "order", "data": []any{}})
					<-finishServer
				}
			}))
			defer server.Close()

			client := NewPrivateWSClient().WithCredentials("key", "secret")
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

			if err := client.Subscribe(context.Background(), "order", func(json.RawMessage) {
				select {
				case existingEvent <- struct{}{}:
				default:
				}
			}); err != nil {
				t.Fatalf("subscribe existing topic: %v", err)
			}
			err := client.Subscribe(context.Background(), "execution", func(json.RawMessage) {
				t.Error("failed subscription handler was invoked")
			})
			if err == nil {
				t.Fatal("failed additional subscription unexpectedly succeeded")
			}
			waitForSignal(t, started, "reconnect start after additional subscription failure")
			waitForSignal(t, restored, "existing subscription replay")
			select {
			case <-recovered:
				t.Fatal("recovered emitted before existing subscription ACK")
			default:
			}
			close(allowRestoreACK)
			waitForSignal(t, recovered, "recovery after existing subscription ACK")
			waitForSignal(t, onlyExistingRestored, "proof that failed subscription was not replayed")
			waitForSignal(t, existingEvent, "event on restored existing subscription")
			close(finishServer)

			client.mu.RLock()
			_, retainedExisting := client.handlers["order"]
			_, retainedFailed := client.handlers["execution"]
			client.mu.RUnlock()
			if !retainedExisting || retainedFailed {
				t.Fatalf("handlers after recovery: existing=%v failed=%v", retainedExisting, retainedFailed)
			}
		})
	}
}

func TestPrivateWSSubscribeWaitsForRecoveryReplay(t *testing.T) {
	var dials atomic.Int32
	closeInitial := make(chan struct{})
	firstRecoveryTopic := make(chan string, 1)
	allowReplayACK := make(chan struct{})
	newTopicSeen := make(chan string, 1)
	finishServer := make(chan struct{})
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		attempt := dials.Add(1)
		if !serveBybitAuth(t, conn) {
			return
		}

		switch attempt {
		case 1:
			initial := readBybitSubscription(t, conn)
			if err := writeBybitSubscribeACK(conn, initial, true, "OK"); err != nil {
				return
			}
			<-closeInitial
		case 2:
			replay := readBybitSubscription(t, conn)
			if len(replay.Args) != 1 {
				return
			}
			firstRecoveryTopic <- replay.Args[0]
			if replay.Args[0] != "order" {
				return
			}
			<-allowReplayACK
			if err := writeBybitSubscribeACK(conn, replay, true, "OK"); err != nil {
				return
			}
			added := readBybitSubscription(t, conn)
			if len(added.Args) != 1 {
				return
			}
			newTopicSeen <- added.Args[0]
			if err := writeBybitSubscribeACK(conn, added, true, "OK"); err != nil {
				return
			}
			<-finishServer
		}
	}))
	defer server.Close()

	client := NewPrivateWSClient().WithCredentials("key", "secret")
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

	if err := client.Subscribe(context.Background(), "order", func(json.RawMessage) {}); err != nil {
		t.Fatalf("subscribe initial topic: %v", err)
	}
	close(closeInitial)
	waitForSignal(t, started, "recovery start")

	addedResult := make(chan error, 1)
	go func() {
		addedResult <- client.Subscribe(context.Background(), "execution", func(json.RawMessage) {})
	}()

	select {
	case topic := <-firstRecoveryTopic:
		if topic != "order" {
			t.Fatalf("first request on recovery connection = %q, want replayed order", topic)
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
	waitForSignal(t, recovered, "recovery completion")
	select {
	case topic := <-newTopicSeen:
		if topic != "execution" {
			t.Fatalf("post-recovery subscription = %q, want execution", topic)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("timed out waiting for post-recovery subscription")
	}
	if err := waitForResult(t, addedResult, "post-recovery subscription result"); err != nil {
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
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		attempt := dials.Add(1)
		if !serveBybitAuth(t, conn) {
			return
		}
		switch attempt {
		case 1:
			first := readBybitSubscription(t, conn)
			close(firstRequestSeen)
			secondResult := beginBybitSubscriptionRead(conn)
			var second wsCommandRequest
			select {
			case got := <-secondResult:
				if got.err != nil {
					return
				}
				second = got.req
				if err := writeBybitSubscribeACK(conn, first, true, "OK"); err != nil {
					return
				}
			case <-time.After(75 * time.Millisecond):
				if err := writeBybitSubscribeACK(conn, first, true, "OK"); err != nil {
					return
				}
				got := <-secondResult
				if got.err != nil {
					return
				}
				second = got.req
			}
			if len(second.Args) != 1 || second.Args[0] != "order" {
				t.Errorf("duplicate subscription = %+v, want order", second)
				return
			}
			if err := writeBybitSubscribeACK(conn, second, false, "duplicate"); err != nil {
				return
			}
			_, _, _ = conn.ReadMessage()
		case 2:
			replay := readBybitSubscription(t, conn)
			if len(replay.Args) != 1 || replay.Args[0] != "order" {
				t.Errorf("restored subscription = %+v, want order", replay)
				return
			}
			close(restored)
			<-allowRestoreACK
			if err := writeBybitSubscribeACK(conn, replay, true, "OK"); err != nil {
				return
			}
			_ = conn.WriteJSON(map[string]any{"topic": "order", "data": []any{}})
			<-finishServer
		}
	}))
	defer server.Close()

	client := NewPrivateWSClient().WithCredentials("key", "secret")
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
		firstResult <- client.Subscribe(context.Background(), "order", func(json.RawMessage) {
			firstHandlerEvent <- struct{}{}
		})
	}()
	waitForSignal(t, firstRequestSeen, "first same-topic subscription")
	secondResult := make(chan error, 1)
	go func() {
		secondResult <- client.Subscribe(context.Background(), "order", func(json.RawMessage) {
			secondHandlerEvent <- struct{}{}
		})
	}()
	if err := waitForResult(t, firstResult, "first same-topic subscription"); err != nil {
		t.Fatalf("first same-topic subscription: %v", err)
	}
	if err := waitForResult(t, secondResult, "rejected duplicate subscription"); err == nil {
		t.Fatal("duplicate same-topic subscription unexpectedly succeeded")
	}
	waitForSignal(t, restored, "same-topic rollback replay")
	close(allowRestoreACK)
	waitForSignal(t, recovered, "same-topic rollback recovery")
	waitForSignal(t, firstHandlerEvent, "original same-topic handler")
	select {
	case <-secondHandlerEvent:
		t.Fatal("rejected same-topic handler replaced the successful handler")
	default:
	}
	close(finishServer)
}

func serveBybitAuth(t *testing.T, conn *websocket.Conn) bool {
	t.Helper()
	var auth wsAuthRequest
	if err := conn.ReadJSON(&auth); err != nil {
		return false
	}
	if err := conn.WriteJSON(map[string]any{"op": "auth", "success": true, "ret_msg": "OK"}); err != nil {
		return false
	}
	return true
}

func readBybitSubscription(t *testing.T, conn *websocket.Conn) wsCommandRequest {
	t.Helper()
	var req wsCommandRequest
	if err := conn.ReadJSON(&req); err != nil {
		t.Errorf("read subscription: %v", err)
	}
	return req
}

type bybitSubscriptionReadResult struct {
	req wsCommandRequest
	err error
}

func beginBybitSubscriptionRead(conn *websocket.Conn) <-chan bybitSubscriptionReadResult {
	resultCh := make(chan bybitSubscriptionReadResult, 1)
	go func() {
		var req wsCommandRequest
		err := conn.ReadJSON(&req)
		resultCh <- bybitSubscriptionReadResult{req: req, err: err}
	}()
	return resultCh
}

func assertNoBybitSubscription(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	reqCh := make(chan wsCommandRequest, 1)
	go func() {
		var req wsCommandRequest
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

func waitForSignal(t *testing.T, ch <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(4 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func waitForResult(t *testing.T, ch <-chan error, description string) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(4 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
		return nil
	}
}
