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

func TestWSFailedAdditionalSubscriptionRecoversExistingOnly(t *testing.T) {
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
			existingPayload := []string{"BTC_USDT"}
			failedPayload := []string{"ETH_USDT"}
			upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer conn.Close()
				attempt := dials.Add(1)

				switch attempt {
				case 1:
					first := readGateSubscription(t, conn)
					if first.Channel != ChannelSpotOrder || wsKey(first.Channel, first.Payload.([]string)) != wsKey(ChannelSpotOrder, existingPayload) {
						t.Errorf("first subscription = %+v, want existing payload", first)
						return
					}
					if err := writeGateSubscribeACK(conn, first, true, ""); err != nil {
						return
					}
					second := readGateSubscription(t, conn)
					if second.Channel != ChannelSpotUserTrade {
						t.Errorf("second subscription = %+v, want user trades", second)
						return
					}
					if tc.rejectNew {
						if err := writeGateSubscribeACK(conn, second, false, "rejected"); err != nil {
							return
						}
					}
					_, _, _ = conn.ReadMessage()
				case 2:
					replay := readGateSubscription(t, conn)
					if replay.Channel != ChannelSpotOrder {
						t.Errorf("restored subscription = %+v, want only spot orders", replay)
						return
					}
					close(restored)
					<-allowRestoreACK
					if err := writeGateSubscribeACK(conn, replay, true, ""); err != nil {
						return
					}
					assertNoGateSubscription(t, conn)
					close(onlyExistingRestored)
					_ = conn.WriteJSON(map[string]any{
						"time":    time.Now().Unix(),
						"channel": ChannelSpotOrder,
						"event":   "update",
						"result":  []any{},
					})
					<-finishServer
				}
			}))
			defer server.Close()

			client := MustNewWSClient(ProductSpot).
				WithCredentials("key", "secret").
				WithURL("ws" + strings.TrimPrefix(server.URL, "http"))
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

			if err := client.Subscribe(context.Background(), ChannelSpotOrder, existingPayload, func(json.RawMessage) {
				select {
				case existingEvent <- struct{}{}:
				default:
				}
			}); err != nil {
				t.Fatalf("subscribe existing channel: %v", err)
			}
			err := client.Subscribe(context.Background(), ChannelSpotUserTrade, failedPayload, func(json.RawMessage) {
				t.Error("failed subscription handler was invoked")
			})
			if err == nil {
				t.Fatal("failed additional subscription unexpectedly succeeded")
			}
			waitForGateSignal(t, started, "reconnect start after additional subscription failure")
			waitForGateSignal(t, restored, "existing subscription replay")
			select {
			case <-recovered:
				t.Fatal("recovered emitted before existing subscription ACK")
			default:
			}
			close(allowRestoreACK)
			waitForGateSignal(t, recovered, "recovery after existing subscription ACK")
			waitForGateSignal(t, onlyExistingRestored, "proof that failed subscription was not replayed")
			waitForGateSignal(t, existingEvent, "event on restored existing subscription")
			close(finishServer)

			existingKey := wsKey(ChannelSpotOrder, existingPayload)
			failedKey := wsKey(ChannelSpotUserTrade, failedPayload)
			client.mu.RLock()
			_, retainedExistingSub := client.subs[existingKey]
			_, retainedExistingHandler := client.handlers[existingKey]
			_, retainedFailedSub := client.subs[failedKey]
			_, retainedFailedHandler := client.handlers[failedKey]
			client.mu.RUnlock()
			if !retainedExistingSub || !retainedExistingHandler || retainedFailedSub || retainedFailedHandler {
				t.Fatalf("state after recovery: existing sub/handler=%v/%v failed sub/handler=%v/%v",
					retainedExistingSub, retainedExistingHandler, retainedFailedSub, retainedFailedHandler)
			}
		})
	}
}

func TestWSSubscribeWaitsForRecoveryReplay(t *testing.T) {
	var dials atomic.Int32
	closeInitial := make(chan struct{})
	firstRecoveryChannel := make(chan string, 1)
	allowReplayACK := make(chan struct{})
	newChannelSeen := make(chan string, 1)
	finishServer := make(chan struct{})
	existingPayload := []string{"BTC_USDT"}
	addedPayload := []string{"ETH_USDT"}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		attempt := dials.Add(1)

		switch attempt {
		case 1:
			initial := readGateSubscription(t, conn)
			if err := writeGateSubscribeACK(conn, initial, true, ""); err != nil {
				return
			}
			<-closeInitial
		case 2:
			replay := readGateSubscription(t, conn)
			firstRecoveryChannel <- replay.Channel
			if replay.Channel != ChannelSpotOrder {
				return
			}
			<-allowReplayACK
			if err := writeGateSubscribeACK(conn, replay, true, ""); err != nil {
				return
			}
			added := readGateSubscription(t, conn)
			newChannelSeen <- added.Channel
			if err := writeGateSubscribeACK(conn, added, true, ""); err != nil {
				return
			}
			<-finishServer
		}
	}))
	defer server.Close()

	client := MustNewWSClient(ProductSpot).
		WithCredentials("key", "secret").
		WithURL("ws" + strings.TrimPrefix(server.URL, "http"))
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

	if err := client.Subscribe(context.Background(), ChannelSpotOrder, existingPayload, func(json.RawMessage) {}); err != nil {
		t.Fatalf("subscribe initial channel: %v", err)
	}
	close(closeInitial)
	waitForGateSignal(t, started, "recovery start")

	addedResult := make(chan error, 1)
	go func() {
		addedResult <- client.Subscribe(context.Background(), ChannelSpotUserTrade, addedPayload, func(json.RawMessage) {})
	}()

	select {
	case channel := <-firstRecoveryChannel:
		if channel != ChannelSpotOrder {
			t.Fatalf("first request on recovery connection = %q, want %q", channel, ChannelSpotOrder)
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
	waitForGateSignal(t, recovered, "recovery completion")
	select {
	case channel := <-newChannelSeen:
		if channel != ChannelSpotUserTrade {
			t.Fatalf("post-recovery subscription = %q, want %q", channel, ChannelSpotUserTrade)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("timed out waiting for post-recovery subscription")
	}
	if err := waitForGateResult(t, addedResult, "post-recovery subscription result"); err != nil {
		t.Fatalf("post-recovery subscription: %v", err)
	}
	close(finishServer)
}

func TestWSConcurrentSameKeyFailureRollsBackSuccessfulHandler(t *testing.T) {
	var dials atomic.Int32
	firstRequestSeen := make(chan struct{})
	restored := make(chan struct{})
	allowRestoreACK := make(chan struct{})
	firstHandlerEvent := make(chan struct{}, 1)
	secondHandlerEvent := make(chan struct{}, 1)
	finishServer := make(chan struct{})
	payload := []string{"BTC_USDT"}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		attempt := dials.Add(1)
		switch attempt {
		case 1:
			first := readGateSubscription(t, conn)
			close(firstRequestSeen)
			secondResult := beginGateSubscriptionRead(conn)
			var second wsRequest
			select {
			case got := <-secondResult:
				if got.err != nil {
					return
				}
				second = got.req
				if err := writeGateSubscribeACK(conn, first, true, ""); err != nil {
					return
				}
			case <-time.After(75 * time.Millisecond):
				if err := writeGateSubscribeACK(conn, first, true, ""); err != nil {
					return
				}
				got := <-secondResult
				if got.err != nil {
					return
				}
				second = got.req
			}
			if second.Channel != ChannelSpotOrder {
				t.Errorf("duplicate subscription = %+v, want spot orders", second)
				return
			}
			if err := writeGateSubscribeACK(conn, second, false, "duplicate"); err != nil {
				return
			}
			_, _, _ = conn.ReadMessage()
		case 2:
			replay := readGateSubscription(t, conn)
			if replay.Channel != ChannelSpotOrder {
				t.Errorf("restored subscription = %+v, want spot orders", replay)
				return
			}
			close(restored)
			<-allowRestoreACK
			if err := writeGateSubscribeACK(conn, replay, true, ""); err != nil {
				return
			}
			_ = conn.WriteJSON(map[string]any{
				"time":    time.Now().Unix(),
				"channel": ChannelSpotOrder,
				"event":   "update",
				"result":  []any{},
			})
			<-finishServer
		}
	}))
	defer server.Close()

	client := MustNewWSClient(ProductSpot).
		WithCredentials("key", "secret").
		WithURL("ws" + strings.TrimPrefix(server.URL, "http"))
	client.subscriptionAckTimeout = time.Second
	defer client.Close()
	recovered := make(chan struct{})
	var recoveredOnce sync.Once
	client.SetReconnectHooks(func(error) {}, func() {
		recoveredOnce.Do(func() { close(recovered) })
	})

	firstResult := make(chan error, 1)
	go func() {
		firstResult <- client.Subscribe(context.Background(), ChannelSpotOrder, payload, func(json.RawMessage) {
			firstHandlerEvent <- struct{}{}
		})
	}()
	waitForGateSignal(t, firstRequestSeen, "first same-key subscription")
	secondResult := make(chan error, 1)
	go func() {
		secondResult <- client.Subscribe(context.Background(), ChannelSpotOrder, payload, func(json.RawMessage) {
			secondHandlerEvent <- struct{}{}
		})
	}()
	if err := waitForGateResult(t, firstResult, "first same-key subscription"); err != nil {
		t.Fatalf("first same-key subscription: %v", err)
	}
	if err := waitForGateResult(t, secondResult, "rejected duplicate subscription"); err == nil {
		t.Fatal("duplicate same-key subscription unexpectedly succeeded")
	}
	waitForGateSignal(t, restored, "same-key rollback replay")
	close(allowRestoreACK)
	waitForGateSignal(t, recovered, "same-key rollback recovery")
	waitForGateSignal(t, firstHandlerEvent, "original same-key handler")
	select {
	case <-secondHandlerEvent:
		t.Fatal("rejected same-key handler replaced the successful handler")
	default:
	}
	close(finishServer)
}

func readGateSubscription(t *testing.T, conn *websocket.Conn) wsRequest {
	t.Helper()
	var raw struct {
		ID      uint64   `json:"id"`
		Time    int64    `json:"time"`
		Channel string   `json:"channel"`
		Event   string   `json:"event"`
		Payload []string `json:"payload"`
		Auth    *WSAuth  `json:"auth"`
	}
	if err := conn.ReadJSON(&raw); err != nil {
		t.Errorf("read subscription: %v", err)
	}
	return wsRequest{ID: raw.ID, Time: raw.Time, Channel: raw.Channel, Event: raw.Event, Payload: raw.Payload, Auth: raw.Auth}
}

type gateSubscriptionReadResult struct {
	req wsRequest
	err error
}

func beginGateSubscriptionRead(conn *websocket.Conn) <-chan gateSubscriptionReadResult {
	resultCh := make(chan gateSubscriptionReadResult, 1)
	go func() {
		var raw struct {
			ID      uint64   `json:"id"`
			Time    int64    `json:"time"`
			Channel string   `json:"channel"`
			Event   string   `json:"event"`
			Payload []string `json:"payload"`
			Auth    *WSAuth  `json:"auth"`
		}
		err := conn.ReadJSON(&raw)
		resultCh <- gateSubscriptionReadResult{
			req: wsRequest{ID: raw.ID, Time: raw.Time, Channel: raw.Channel, Event: raw.Event, Payload: raw.Payload, Auth: raw.Auth},
			err: err,
		}
	}()
	return resultCh
}

func assertNoGateSubscription(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	resultCh := beginGateSubscriptionRead(conn)
	select {
	case got := <-resultCh:
		if got.err == nil {
			t.Errorf("unexpected restored subscription: %+v", got.req)
		}
	case <-time.After(100 * time.Millisecond):
	}
}

func waitForGateSignal(t *testing.T, ch <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(4 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func waitForGateResult(t *testing.T, ch <-chan error, description string) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(4 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
		return nil
	}
}
