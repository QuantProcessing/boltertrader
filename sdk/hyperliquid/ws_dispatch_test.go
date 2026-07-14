package hyperliquid

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestWebsocketHandlerCanSubscribeConfirmedReentrantly(t *testing.T) {
	upgrader := websocket.Upgrader{}
	stop := make(chan struct{})
	serverErrors := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		_, first, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if err := writeSubscriptionACK(conn, first); err != nil {
			serverErrors <- err
			return
		}
		if err := conn.WriteJSON(map[string]any{
			"channel": "orderUpdates",
			"data":    []any{},
		}); err != nil {
			serverErrors <- err
			return
		}

		_, second, err := conn.ReadMessage()
		if err != nil {
			serverErrors <- err
			return
		}
		if err := writeSubscriptionACK(conn, second); err != nil {
			serverErrors <- err
			return
		}
		<-stop
	}))

	client := NewWebsocketClient(context.Background()).WithURL("ws" + strings.TrimPrefix(server.URL, "http"))
	client.SubscriptionAckTimeout = 100 * time.Millisecond
	var stopOnce sync.Once
	t.Cleanup(func() {
		stopOnce.Do(func() { close(stop) })
		client.Close()
		server.Close()
	})

	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	reentrantResult := make(chan error, 1)
	firstSubscription := map[string]any{"type": "orderUpdates", "user": "0xabc"}
	secondSubscription := map[string]any{"type": "userFills", "user": "0xabc"}
	if err := client.SubscribeConfirmed("orderUpdates", firstSubscription, func(WsMessage) {
		reentrantResult <- client.SubscribeConfirmed("userFills", secondSubscription, func(WsMessage) {})
	}); err != nil {
		t.Fatalf("first SubscribeConfirmed: %v", err)
	}

	select {
	case err := <-reentrantResult:
		if err != nil {
			t.Fatalf("reentrant SubscribeConfirmed: %v", err)
		}
	case err := <-serverErrors:
		t.Fatalf("websocket server: %v", err)
	case <-time.After(time.Second):
		t.Fatal("reentrant SubscribeConfirmed did not receive its ACK")
	}
}

func TestWebsocketPostResponseBypassesBlockedUserCallback(t *testing.T) {
	upgrader := websocket.Upgrader{}
	handlerEntered := make(chan struct{})
	stop := make(chan struct{})
	serverErrors := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var request WsPostRequest
		if err := json.Unmarshal(raw, &request); err != nil {
			serverErrors <- err
			return
		}
		if err := writeHyperliquidTestData(conn, "orders", "block"); err != nil {
			serverErrors <- err
			return
		}
		<-handlerEntered
		if err := conn.WriteJSON(map[string]any{
			"channel": "post",
			"data": map[string]any{
				"id": request.ID,
				"response": map[string]any{
					"type":    "action",
					"payload": map[string]any{"status": "ok"},
				},
			},
		}); err != nil {
			serverErrors <- err
			return
		}
		<-stop
	}))

	client := NewWebsocketClient(context.Background()).WithURL("ws" + strings.TrimPrefix(server.URL, "http"))
	var stopOnce sync.Once
	var releaseOnce sync.Once
	releaseHandler := make(chan struct{})
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(releaseHandler) })
		stopOnce.Do(func() { close(stop) })
		client.Close()
		server.Close()
	})

	if err := client.Subscribe("orders", nil, func(WsMessage) {
		close(handlerEntered)
		<-releaseHandler
	}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	result, err := client.PostRequest(WsPostRequestPayload{Type: "info", Payload: map[string]any{"type": "openOrders"}})
	if err != nil {
		t.Fatalf("PostRequest: %v", err)
	}
	select {
	case response := <-result:
		if response.Error != nil {
			t.Fatalf("post response error: %v", response.Error)
		}
		if response.Response.Type != "action" {
			t.Fatalf("post response type = %q, want action", response.Response.Type)
		}
	case err := <-serverErrors:
		t.Fatalf("websocket server: %v", err)
	case <-time.After(250 * time.Millisecond):
		t.Fatal("post response was delayed behind a blocked user callback")
	}
}

func TestWebsocketDisconnectFailsPendingPostRequest(t *testing.T) {
	requestSeen := make(chan struct{}, 1)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var request WsPostRequest
		if err := conn.ReadJSON(&request); err != nil {
			return
		}
		requestSeen <- struct{}{}
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "drop pending post"), time.Now().Add(time.Second))
	}))

	client := NewWebsocketClient(context.Background()).WithURL("ws" + strings.TrimPrefix(server.URL, "http"))
	client.ReconnectWait = time.Hour
	t.Cleanup(func() {
		client.Close()
		server.Close()
	})
	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	result, err := client.PostRequest(WsPostRequestPayload{Type: "info", Payload: map[string]any{"type": "openOrders"}})
	if err != nil {
		t.Fatalf("PostRequest: %v", err)
	}
	select {
	case <-requestSeen:
	case <-time.After(time.Second):
		t.Fatal("server did not receive post request")
	}
	select {
	case response, ok := <-result:
		if !ok || response.Error == nil {
			t.Fatalf("pending post result=%+v ok=%v, want connection error", response, ok)
		}
	case <-time.After(time.Second):
		t.Fatal("pending post request was not failed after its socket disconnected")
	}
	client.Mu.RLock()
	remaining := len(client.PostChannels)
	client.Mu.RUnlock()
	if remaining != 0 {
		t.Fatalf("pending post channels=%d, want 0 after disconnect", remaining)
	}
}

func TestWebsocketContextCancellationCompletesPendingPostRequest(t *testing.T) {
	requestSeen := make(chan struct{}, 1)
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
		requestSeen <- struct{}{}
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))

	client := NewWebsocketClient(context.Background()).WithURL("ws" + strings.TrimPrefix(server.URL, "http"))
	t.Cleanup(func() {
		client.Close()
		server.Close()
	})
	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result, err := client.PostRequestContext(ctx, WsPostRequestPayload{Type: "info", Payload: map[string]any{"type": "openOrders"}})
	if err != nil {
		t.Fatalf("PostRequestContext: %v", err)
	}
	select {
	case <-requestSeen:
	case <-time.After(time.Second):
		t.Fatal("server did not receive post request")
	}
	cancel()
	select {
	case response, ok := <-result:
		if !ok || !errors.Is(response.Error, context.Canceled) {
			t.Fatalf("pending post result=%+v ok=%v, want context.Canceled", response, ok)
		}
	case <-time.After(time.Second):
		t.Fatal("pending post request did not observe context cancellation")
	}
	client.Mu.RLock()
	remaining := len(client.PostChannels)
	client.Mu.RUnlock()
	if remaining != 0 {
		t.Fatalf("pending post channels=%d, want 0 after context cancellation", remaining)
	}
}

func TestWebsocketRejectsSecondOpaqueSubscriptionIdentity(t *testing.T) {
	for _, channel := range []string{"orderUpdates", "user"} {
		t.Run(channel, func(t *testing.T) {
			client := NewWebsocketClient(context.Background())
			first := map[string]any{"type": channel, "user": "0xaaa"}
			second := map[string]any{"type": channel, "user": "0xbbb"}
			firstKey := subscriptionKey(first)
			client.subscriptions[channel] = map[string]func(WsMessage){firstKey: func(WsMessage) {}}
			client.subscriptionPayloads[channel] = map[string]any{firstKey: first}

			err := client.Subscribe(channel, second, func(WsMessage) {})
			if err == nil || !strings.Contains(err.Error(), "cannot multiplex") {
				t.Fatalf("second %s subscription error=%v, want cannot multiplex", channel, err)
			}
			if len(client.subscriptions[channel]) != 1 || client.subscriptions[channel][firstKey] == nil {
				t.Fatalf("second %s subscription mutated committed handler set", channel)
			}
		})
	}
}

func TestSubscriptionRollbackDoesNotOverwriteNewerState(t *testing.T) {
	client := NewWebsocketClient(context.Background())
	subscription := map[string]any{"type": "orders", "user": "0xabc"}
	dispatched := make(chan string, 1)
	oldMutation, _, err := client.beginSubscription("orders", subscription, func(WsMessage) { dispatched <- "old" })
	if err != nil {
		t.Fatalf("begin old subscription: %v", err)
	}
	client.commitSubscriptionPayload(oldMutation, subscription)
	staleMutation, _, err := client.beginSubscription("orders", subscription, func(WsMessage) { dispatched <- "stale" })
	if err != nil {
		t.Fatalf("begin stale subscription: %v", err)
	}
	newMutation, _, err := client.beginSubscription("orders", subscription, func(WsMessage) { dispatched <- "new" })
	if err != nil {
		t.Fatalf("begin new subscription: %v", err)
	}
	client.commitSubscriptionPayload(newMutation, subscription)

	client.rollbackSubscription(staleMutation)
	key := subscriptionKey(subscription)
	client.Mu.RLock()
	handler := client.subscriptions["orders"][key]
	revision := client.subscriptionRevisions["orders"][key]
	client.Mu.RUnlock()
	if revision != newMutation.revision {
		t.Fatalf("revision=%d, want newer revision %d", revision, newMutation.revision)
	}
	handler(WsMessage{})
	if got := <-dispatched; got != "new" {
		t.Fatalf("handler=%q, want newer handler", got)
	}
}

func TestSubscribeSendFailureRestoresCommittedState(t *testing.T) {
	client := NewWebsocketClient(context.Background())
	subscription := map[string]any{"type": "orders", "user": "0xabc"}
	dispatched := make(chan string, 1)
	committed, _, err := client.beginSubscription("orders", subscription, func(WsMessage) { dispatched <- "committed" })
	if err != nil {
		t.Fatalf("begin committed subscription: %v", err)
	}
	client.commitSubscriptionPayload(committed, subscription)

	err = client.Subscribe("orders", subscription, func(WsMessage) { dispatched <- "failed" })
	if err == nil || !strings.Contains(err.Error(), "websocket not connected") {
		t.Fatalf("Subscribe error=%v, want websocket not connected", err)
	}
	key := subscriptionKey(subscription)
	client.Mu.RLock()
	handler := client.subscriptions["orders"][key]
	payload := client.subscriptionPayloads["orders"][key]
	client.Mu.RUnlock()
	handler(WsMessage{})
	if got := <-dispatched; got != "committed" {
		t.Fatalf("handler=%q, want committed handler", got)
	}
	if subscriptionKey(payload) != key {
		t.Fatal("failed replacement changed reconnect payload")
	}
}

func TestWebsocketConnectCannotSupersedeAutomaticRecovery(t *testing.T) {
	upgrader := websocket.Upgrader{}
	var connections atomic.Int32
	secondConnected := make(chan struct{}, 1)
	stop := make(chan struct{})
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
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if err := writeSubscriptionACK(conn, raw); err != nil {
			return
		}
		if connection == 1 {
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "rotate"), time.Now().Add(time.Second))
			return
		}
		<-stop
	}))

	client := NewWebsocketClient(context.Background()).WithURL("ws" + strings.TrimPrefix(server.URL, "http"))
	client.ReconnectWait = 150 * time.Millisecond
	client.SubscriptionAckTimeout = time.Second
	var stopOnce sync.Once
	t.Cleanup(func() {
		stopOnce.Do(func() { close(stop) })
		client.Close()
		server.Close()
	})
	started := make(chan struct{}, 1)
	recovered := make(chan struct{}, 1)
	client.SetReconnectHooks(func(error) {
		started <- struct{}{}
	}, func() {
		recovered <- struct{}{}
	})

	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := client.SubscribeConfirmed("orders", map[string]any{"type": "orders", "user": "0xabc"}, func(WsMessage) {}); err != nil {
		t.Fatalf("SubscribeConfirmed: %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("automatic recovery did not start")
	}
	if err := client.Connect(); err == nil || !strings.Contains(err.Error(), "recovery in progress") {
		t.Fatalf("Connect during recovery error = %v, want recovery in progress", err)
	}
	select {
	case <-secondConnected:
		t.Fatal("Connect superseded the recovery backoff and opened a replacement socket")
	case <-time.After(50 * time.Millisecond):
	}
	select {
	case <-secondConnected:
	case <-time.After(time.Second):
		t.Fatal("automatic recovery did not open its replacement socket")
	}
	select {
	case <-recovered:
	case <-time.After(time.Second):
		t.Fatal("automatic recovery did not complete after replay ACK")
	}
}

func TestWebsocketContinuousGapDiscardsStaleRecoveryAndReplacementData(t *testing.T) {
	upgrader := websocket.Upgrader{}
	var connections atomic.Int32
	thirdReplay := make(chan struct{}, 1)
	stop := make(chan struct{})
	serverErrors := make(chan error, 1)
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
		if connection == 3 {
			thirdReplay <- struct{}{}
		}
		if err := writeSubscriptionACK(conn, raw); err != nil {
			serverErrors <- err
			return
		}

		switch connection {
		case 1:
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "rotate one"), time.Now().Add(time.Second))
		case 2:
			if err := writeHyperliquidTestData(conn, "orders", "stale"); err != nil {
				serverErrors <- err
				return
			}
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "rotate two"), time.Now().Add(time.Second))
		default:
			if err := writeHyperliquidTestData(conn, "orders", "fresh"); err != nil {
				serverErrors <- err
				return
			}
			<-stop
		}
	}))

	client := NewWebsocketClient(context.Background()).WithURL("ws" + strings.TrimPrefix(server.URL, "http"))
	client.ReconnectWait = 5 * time.Millisecond
	client.SubscriptionAckTimeout = time.Second
	var stopOnce sync.Once
	var releaseOnce sync.Once
	releaseStarted := make(chan struct{})
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(releaseStarted) })
		stopOnce.Do(func() { close(stop) })
		client.Close()
		server.Close()
	})

	var eventsMu sync.Mutex
	var events []string
	appendEvent := func(event string) {
		eventsMu.Lock()
		events = append(events, event)
		eventsMu.Unlock()
	}
	startedEntered := make(chan struct{}, 1)
	freshSeen := make(chan struct{}, 1)
	client.SetReconnectHooks(func(error) {
		appendEvent("started")
		startedEntered <- struct{}{}
		<-releaseStarted
	}, func() {
		appendEvent("recovered")
	})

	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := client.SubscribeConfirmed("orders", map[string]any{"type": "orders", "user": "0xabc"}, func(msg WsMessage) {
		var value string
		if err := json.Unmarshal(msg.Data, &value); err != nil {
			t.Errorf("decode data: %v", err)
			return
		}
		appendEvent(value)
		if value == "fresh" {
			freshSeen <- struct{}{}
		}
	}); err != nil {
		t.Fatalf("SubscribeConfirmed: %v", err)
	}

	select {
	case <-startedEntered:
	case err := <-serverErrors:
		t.Fatalf("websocket server: %v", err)
	case <-time.After(time.Second):
		t.Fatal("reconnect-started callback did not begin")
	}
	select {
	case <-thirdReplay:
	case err := <-serverErrors:
		t.Fatalf("websocket server: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("blocked reconnect-started callback prevented later dial/replay")
	}

	releaseOnce.Do(func() { close(releaseStarted) })
	select {
	case <-freshSeen:
	case err := <-serverErrors:
		t.Fatalf("websocket server: %v", err)
	case <-time.After(time.Second):
		t.Fatal("fresh replacement data was not released after recovery")
	}

	eventsMu.Lock()
	got := append([]string(nil), events...)
	eventsMu.Unlock()
	want := []string{"started", "recovered", "fresh"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("visible callback order = %v, want %v", got, want)
	}
}

func TestWebsocketCallbackDispatcherPreservesSocketFIFO(t *testing.T) {
	upgrader := websocket.Upgrader{}
	stop := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for i := 0; i < 100; i++ {
			channel := "orders"
			if i%2 == 1 {
				channel = "fills"
			}
			if err := writeHyperliquidTestData(conn, channel, i); err != nil {
				return
			}
		}
		<-stop
	}))

	client := NewWebsocketClient(context.Background()).WithURL("ws" + strings.TrimPrefix(server.URL, "http"))
	var stopOnce sync.Once
	t.Cleanup(func() {
		stopOnce.Do(func() { close(stop) })
		client.Close()
		server.Close()
	})
	received := make(chan int, 100)
	handler := func(msg WsMessage) {
		var value int
		if err := json.Unmarshal(msg.Data, &value); err != nil {
			t.Errorf("decode FIFO data: %v", err)
			return
		}
		received <- value
	}
	for _, channel := range []string{"orders", "fills"} {
		if err := client.Subscribe(channel, nil, handler); err != nil {
			t.Fatalf("Subscribe(%s): %v", channel, err)
		}
	}
	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	for want := 0; want < 100; want++ {
		select {
		case got := <-received:
			if got != want {
				t.Fatalf("callback %d = %d, want %d", want, got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for callback %d", want)
		}
	}
}

func TestWebsocketIntentionalShutdownDropsQueuedOldData(t *testing.T) {
	for _, tt := range []struct {
		name     string
		shutdown func(*WebsocketClient)
	}{
		{name: "Disconnect", shutdown: func(client *WebsocketClient) { client.Disconnect() }},
		{name: "Close", shutdown: func(client *WebsocketClient) { client.Close() }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			upgrader := websocket.Upgrader{}
			stop := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer conn.Close()
				if err := writeHyperliquidTestData(conn, "orders", 1); err != nil {
					return
				}
				if err := writeHyperliquidTestData(conn, "orders", 2); err != nil {
					return
				}
				<-stop
			}))

			client := NewWebsocketClient(context.Background()).WithURL("ws" + strings.TrimPrefix(server.URL, "http"))
			var stopOnce sync.Once
			var releaseOnce sync.Once
			releaseFirst := make(chan struct{})
			t.Cleanup(func() {
				releaseOnce.Do(func() { close(releaseFirst) })
				stopOnce.Do(func() { close(stop) })
				client.Close()
				server.Close()
			})
			firstEntered := make(chan struct{}, 1)
			secondSeen := make(chan struct{}, 1)
			if err := client.Subscribe("orders", nil, func(msg WsMessage) {
				var value int
				if err := json.Unmarshal(msg.Data, &value); err != nil {
					t.Errorf("decode shutdown data: %v", err)
					return
				}
				if value == 1 {
					firstEntered <- struct{}{}
					<-releaseFirst
				}
				if value == 2 {
					secondSeen <- struct{}{}
				}
			}); err != nil {
				t.Fatalf("Subscribe: %v", err)
			}
			if err := client.Connect(); err != nil {
				t.Fatalf("Connect: %v", err)
			}
			select {
			case <-firstEntered:
			case <-time.After(time.Second):
				t.Fatal("first callback did not block")
			}
			waitForHyperliquidPendingCallbacks(t, client.callbackDispatcher, 1)
			assertReturnsWithin(t, tt.name, 250*time.Millisecond, func() { tt.shutdown(client) })
			releaseOnce.Do(func() { close(releaseFirst) })
			select {
			case <-secondSeen:
				t.Fatal("queued old-generation data was delivered after intentional shutdown")
			case <-time.After(100 * time.Millisecond):
			}
		})
	}
}

func TestWebsocketCallbackOverflowClosesExactSocketAndRecovers(t *testing.T) {
	upgrader := websocket.Upgrader{}
	var connections atomic.Int32
	secondConnected := make(chan struct{}, 1)
	oldSocketClosed := make(chan struct{}, 1)
	stop := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		connection := connections.Add(1)
		if connection == 1 {
			for i := 0; i < hyperliquidWSCallbackQueueLimit+100; i++ {
				if err := writeHyperliquidTestData(conn, "orders", i); err != nil {
					oldSocketClosed <- struct{}{}
					return
				}
			}
			_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
			if _, _, err := conn.ReadMessage(); err != nil {
				oldSocketClosed <- struct{}{}
			}
			return
		}
		secondConnected <- struct{}{}
		if err := writeHyperliquidTestData(conn, "orders", 9999); err != nil {
			return
		}
		<-stop
	}))

	client := NewWebsocketClient(context.Background()).WithURL("ws" + strings.TrimPrefix(server.URL, "http"))
	client.ReconnectWait = 5 * time.Millisecond
	var stopOnce sync.Once
	var releaseOnce sync.Once
	releaseFirst := make(chan struct{})
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(releaseFirst) })
		stopOnce.Do(func() { close(stop) })
		client.Close()
		server.Close()
	})

	firstEntered := make(chan struct{}, 1)
	newSeen := make(chan struct{}, 1)
	overflowCause := make(chan error, 1)
	var recovered atomic.Bool
	client.SetReconnectHooks(func(err error) {
		overflowCause <- err
	}, func() {
		recovered.Store(true)
	})
	if err := client.Subscribe("orders", nil, func(msg WsMessage) {
		var value int
		if err := json.Unmarshal(msg.Data, &value); err != nil {
			t.Errorf("decode order data: %v", err)
			return
		}
		if value == 0 {
			firstEntered <- struct{}{}
			<-releaseFirst
		}
		if value == 9999 {
			if !recovered.Load() {
				t.Error("replacement data became visible before recovered callback")
			}
			newSeen <- struct{}{}
		}
	}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	select {
	case <-firstEntered:
	case <-time.After(time.Second):
		t.Fatal("first callback did not block")
	}
	select {
	case <-secondConnected:
	case <-time.After(3 * time.Second):
		t.Fatal("callback overflow did not exact-close and reconnect while the handler was blocked")
	}
	select {
	case <-oldSocketClosed:
	case <-time.After(time.Second):
		t.Fatal("server did not observe the overflowing socket close")
	}

	releaseOnce.Do(func() { close(releaseFirst) })
	select {
	case err := <-overflowCause:
		if err == nil || !strings.Contains(err.Error(), "callback queue overflow") {
			t.Fatalf("reconnect-started cause = %v, want callback queue overflow", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("overflow was silent: reconnect-started callback was not delivered")
	}
	select {
	case <-newSeen:
	case <-time.After(2 * time.Second):
		t.Fatal("replacement data was not delivered after overflow recovery")
	}
}

func TestWebsocketGapPreservesOldDataLifecycleAndReplacementOrder(t *testing.T) {
	upgrader := websocket.Upgrader{}
	var connections atomic.Int32
	closeFirst := make(chan struct{})
	secondReplay := make(chan struct{}, 1)
	stop := make(chan struct{})
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
		if connection == 2 {
			secondReplay <- struct{}{}
		}
		if err := writeSubscriptionACK(conn, raw); err != nil {
			return
		}
		if connection == 1 {
			if err := writeHyperliquidTestData(conn, "orders", "old"); err != nil {
				return
			}
			<-closeFirst
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "rotate"), time.Now().Add(time.Second))
			return
		}
		if err := writeHyperliquidTestData(conn, "orders", "replacement"); err != nil {
			return
		}
		<-stop
	}))

	client := NewWebsocketClient(context.Background()).WithURL("ws" + strings.TrimPrefix(server.URL, "http"))
	client.ReconnectWait = 5 * time.Millisecond
	client.SubscriptionAckTimeout = time.Second
	var closeFirstOnce sync.Once
	var releaseOldOnce sync.Once
	var stopOnce sync.Once
	releaseOld := make(chan struct{})
	t.Cleanup(func() {
		closeFirstOnce.Do(func() { close(closeFirst) })
		releaseOldOnce.Do(func() { close(releaseOld) })
		stopOnce.Do(func() { close(stop) })
		client.Close()
		server.Close()
	})

	var eventsMu sync.Mutex
	var events []string
	appendEvent := func(event string) {
		eventsMu.Lock()
		events = append(events, event)
		eventsMu.Unlock()
	}
	oldEntered := make(chan struct{}, 1)
	replacementSeen := make(chan struct{}, 1)
	client.SetReconnectHooks(func(error) {
		appendEvent("started")
	}, func() {
		appendEvent("recovered")
	})
	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := client.SubscribeConfirmed("orders", map[string]any{"type": "orders", "user": "0xabc"}, func(msg WsMessage) {
		var value string
		if err := json.Unmarshal(msg.Data, &value); err != nil {
			t.Errorf("decode order data: %v", err)
			return
		}
		appendEvent(value)
		if value == "old" {
			oldEntered <- struct{}{}
			<-releaseOld
		}
		if value == "replacement" {
			replacementSeen <- struct{}{}
		}
	}); err != nil {
		t.Fatalf("SubscribeConfirmed: %v", err)
	}
	select {
	case <-oldEntered:
	case <-time.After(time.Second):
		t.Fatal("old data callback did not begin")
	}
	closeFirstOnce.Do(func() { close(closeFirst) })
	select {
	case <-secondReplay:
	case <-time.After(time.Second):
		t.Fatal("blocked old data callback prevented reconnect replay")
	}
	select {
	case <-replacementSeen:
		t.Fatal("replacement data overtook the blocked old callback and lifecycle boundary")
	case <-time.After(50 * time.Millisecond):
	}
	releaseOldOnce.Do(func() { close(releaseOld) })
	select {
	case <-replacementSeen:
	case <-time.After(time.Second):
		t.Fatal("replacement data was not delivered after recovery")
	}

	eventsMu.Lock()
	got := append([]string(nil), events...)
	eventsMu.Unlock()
	want := []string{"old", "started", "recovered", "replacement"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("visible callback order = %v, want %v", got, want)
	}
}

func TestWebsocketBlockedRecoveredCallbackDoesNotBlockNextReplay(t *testing.T) {
	upgrader := websocket.Upgrader{}
	var connections atomic.Int32
	closeSecond := make(chan struct{})
	thirdReplay := make(chan struct{}, 1)
	stop := make(chan struct{})
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
		if connection == 3 {
			thirdReplay <- struct{}{}
		}
		if err := writeSubscriptionACK(conn, raw); err != nil {
			return
		}
		switch connection {
		case 1:
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "rotate one"), time.Now().Add(time.Second))
		case 2:
			<-closeSecond
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "rotate two"), time.Now().Add(time.Second))
		default:
			if err := writeHyperliquidTestData(conn, "orders", "fresh"); err != nil {
				return
			}
			<-stop
		}
	}))

	client := NewWebsocketClient(context.Background()).WithURL("ws" + strings.TrimPrefix(server.URL, "http"))
	client.ReconnectWait = 5 * time.Millisecond
	client.SubscriptionAckTimeout = time.Second
	var closeSecondOnce sync.Once
	var releaseRecoveredOnce sync.Once
	var stopOnce sync.Once
	releaseRecovered := make(chan struct{})
	t.Cleanup(func() {
		closeSecondOnce.Do(func() { close(closeSecond) })
		releaseRecoveredOnce.Do(func() { close(releaseRecovered) })
		stopOnce.Do(func() { close(stop) })
		client.Close()
		server.Close()
	})

	var eventsMu sync.Mutex
	var events []string
	appendEvent := func(event string) {
		eventsMu.Lock()
		events = append(events, event)
		eventsMu.Unlock()
	}
	firstRecoveredEntered := make(chan struct{}, 1)
	freshSeen := make(chan struct{}, 1)
	var recoveredCalls atomic.Int32
	client.SetReconnectHooks(func(error) {
		appendEvent("started")
	}, func() {
		appendEvent("recovered")
		if recoveredCalls.Add(1) == 1 {
			firstRecoveredEntered <- struct{}{}
			<-releaseRecovered
		}
	})
	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := client.SubscribeConfirmed("orders", map[string]any{"type": "orders", "user": "0xabc"}, func(msg WsMessage) {
		var value string
		if err := json.Unmarshal(msg.Data, &value); err != nil {
			t.Errorf("decode order data: %v", err)
			return
		}
		appendEvent(value)
		if value == "fresh" {
			freshSeen <- struct{}{}
		}
	}); err != nil {
		t.Fatalf("SubscribeConfirmed: %v", err)
	}
	select {
	case <-firstRecoveredEntered:
	case <-time.After(time.Second):
		t.Fatal("first recovered callback did not begin")
	}
	closeSecondOnce.Do(func() { close(closeSecond) })
	select {
	case <-thirdReplay:
	case <-time.After(time.Second):
		t.Fatal("blocked recovered callback prevented the next dial/replay")
	}
	releaseRecoveredOnce.Do(func() { close(releaseRecovered) })
	select {
	case <-freshSeen:
	case <-time.After(time.Second):
		t.Fatal("fresh data was not released after the next recovery")
	}

	eventsMu.Lock()
	got := append([]string(nil), events...)
	eventsMu.Unlock()
	want := []string{"started", "recovered", "started", "recovered", "fresh"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("visible callback order = %v, want %v", got, want)
	}
}

func TestWebsocketDisconnectAndCloseRemainBoundedWithoutOvertakingBlockedCallback(t *testing.T) {
	upgrader := websocket.Upgrader{}
	var connections atomic.Int32
	stop := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		connection := connections.Add(1)
		if err := writeHyperliquidTestData(conn, "orders", connection); err != nil {
			return
		}
		<-stop
	}))

	client := NewWebsocketClient(context.Background()).WithURL("ws" + strings.TrimPrefix(server.URL, "http"))
	var stopOnce sync.Once
	var releaseFirstOnce sync.Once
	var releaseSecondOnce sync.Once
	releaseFirst := make(chan struct{})
	releaseSecond := make(chan struct{})
	t.Cleanup(func() {
		releaseFirstOnce.Do(func() { close(releaseFirst) })
		releaseSecondOnce.Do(func() { close(releaseSecond) })
		stopOnce.Do(func() { close(stop) })
		client.Close()
		server.Close()
	})

	firstEntered := make(chan struct{}, 1)
	secondEntered := make(chan struct{}, 1)
	if err := client.Subscribe("orders", nil, func(msg WsMessage) {
		var value int32
		if err := json.Unmarshal(msg.Data, &value); err != nil {
			t.Errorf("decode order data: %v", err)
			return
		}
		if value == 1 {
			firstEntered <- struct{}{}
			<-releaseFirst
		}
		if value == 2 {
			secondEntered <- struct{}{}
			<-releaseSecond
		}
	}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if err := client.Connect(); err != nil {
		t.Fatalf("first Connect: %v", err)
	}
	select {
	case <-firstEntered:
	case <-time.After(time.Second):
		t.Fatal("first callback did not block")
	}

	assertReturnsWithin(t, "Disconnect", 250*time.Millisecond, client.Disconnect)
	if err := client.Connect(); err != nil {
		t.Fatalf("second Connect: %v", err)
	}
	select {
	case <-secondEntered:
		t.Fatal("fresh callback overtook the old in-flight callback after Disconnect")
	case <-time.After(50 * time.Millisecond):
	}
	releaseFirstOnce.Do(func() { close(releaseFirst) })
	select {
	case <-secondEntered:
	case <-time.After(time.Second):
		t.Fatal("fresh callback did not run after the old callback returned")
	}
	assertReturnsWithin(t, "Close", 250*time.Millisecond, client.Close)
	// Deliberately release after Close: shutdown must not wait for user code.
	releaseSecondOnce.Do(func() { close(releaseSecond) })
}

func TestWebsocketDisconnectResetsDispatcherBeforeReleasingClientState(t *testing.T) {
	client := NewWebsocketClient(context.Background())
	t.Cleanup(client.Close)
	dispatcher := client.callbackDispatcher
	fakeConnection := &websocket.Conn{}
	client.Mu.Lock()
	client.wantConnected = true
	client.Mu.Unlock()
	dispatcher.activateConnection(0, fakeConnection, false)

	dispatcher.mu.Lock()
	disconnectDone := make(chan struct{})
	go func() {
		client.Disconnect()
		close(disconnectDone)
	}()
	observedReleasedState := make(chan struct{}, 1)
	stopObserver := make(chan struct{})
	go func() {
		for {
			select {
			case <-stopObserver:
				return
			default:
			}
			client.Mu.Lock()
			disconnected := !client.wantConnected
			client.Mu.Unlock()
			if disconnected {
				observedReleasedState <- struct{}{}
				return
			}
			runtime.Gosched()
		}
	}()

	prematureRelease := false
	select {
	case <-observedReleasedState:
		prematureRelease = true
	case <-time.After(50 * time.Millisecond):
	}
	dispatcher.mu.Unlock()
	select {
	case <-disconnectDone:
	case <-time.After(time.Second):
		close(stopObserver)
		t.Fatal("Disconnect did not complete after dispatcher lock release")
	}
	close(stopObserver)
	if prematureRelease {
		t.Fatal("Disconnect exposed detached client state before resetting the callback dispatcher")
	}
}

func writeHyperliquidTestData(conn *websocket.Conn, channel string, data any) error {
	return conn.WriteJSON(map[string]any{"channel": channel, "data": data})
}

func assertReturnsWithin(t *testing.T, name string, timeout time.Duration, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		fn()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		t.Fatalf("%s did not return within %s", name, timeout)
	}
}

func waitForHyperliquidPendingCallbacks(t *testing.T, dispatcher *websocketCallbackDispatcher, minimum int) {
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
