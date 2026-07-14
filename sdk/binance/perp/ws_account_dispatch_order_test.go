package perp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestWsAccountClientReplacementPrivateDataWaitsForRecovered(t *testing.T) {
	var listenKeyRequests atomic.Int64
	restServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		request := listenKeyRequests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"listenKey":"dispatch-key-%d"}`, request)
	}))
	defer restServer.Close()

	var connections atomic.Int64
	replacementWritten := make(chan struct{})
	wsURL := newPerpWSServer(t, func(conn *websocket.Conn, _ *http.Request) {
		defer conn.Close()
		connection := connections.Add(1)
		orderID := connection
		if err := conn.WriteMessage(websocket.TextMessage, perpOrderUpdateForDispatchTest(orderID)); err != nil {
			t.Errorf("write private data for connection %d: %v", connection, err)
			return
		}
		if connection == 2 {
			close(replacementWritten)
		}
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := NewWsAccountClientWithEndpointProfile(ctx, "api-key", "secret", EndpointProfile{
		RESTBaseURL:      restServer.URL,
		EndpointPrefix:   "/fapi",
		AccountVersion:   "v2",
		WSPrivateBaseURL: wsURL + "/ws",
	})
	client.Client.WithRateLimiter(nil)
	client.KeepAliveInt = time.Hour
	defer client.Close()

	events := make(chan string, 8)
	client.SubscribeOrderUpdate(func(event *OrderUpdateEvent) {
		if event.Order.OrderID == 1 {
			events <- "old-data"
		} else {
			events <- "replacement-data"
		}
	})
	client.SetReconnectHooks(func(error) {
		events <- "started"
	}, func() {
		events <- "recovered"
	})

	recoveryHookEntered := make(chan struct{})
	releaseRecovery := make(chan struct{})
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(releaseRecovery) }) })
	client.SetOnResubscribe(func() {
		close(recoveryHookEntered)
		<-releaseRecovery
	})

	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	waitPerpDispatchEvent(t, events, "old-data")

	recoveryDone := make(chan struct{})
	go func() {
		client.resubscribe()
		close(recoveryDone)
	}()
	waitPerpDispatchEvent(t, events, "started")
	select {
	case <-replacementWritten:
	case <-time.After(time.Second):
		t.Fatal("replacement connection did not publish private data")
	}
	select {
	case <-recoveryHookEntered:
	case <-time.After(time.Second):
		t.Fatal("recovery did not reach completion hook")
	}
	select {
	case event := <-events:
		t.Fatalf("replacement private data overtook Recovered: %q", event)
	case <-time.After(100 * time.Millisecond):
	}

	release.Do(func() { close(releaseRecovery) })
	waitPerpDispatchEvent(t, events, "recovered")
	waitPerpDispatchEvent(t, events, "replacement-data")
	select {
	case <-recoveryDone:
	case <-time.After(time.Second):
		t.Fatal("recovery worker did not finish")
	}
}

func TestWSClientCloseDropsBufferedReplacementButFinishesAcceptedOldData(t *testing.T) {
	client := NewWSClient(context.Background(), "ws://unused")

	oldStarted := make(chan struct{})
	releaseOld := make(chan struct{})
	oldDone := make(chan struct{})
	var mu sync.Mutex
	var got []string
	client.Handler = func(message []byte) {
		if string(message) == "old" {
			close(oldStarted)
			<-releaseOld
		}
		mu.Lock()
		got = append(got, string(message))
		mu.Unlock()
	}
	go func() {
		client.dispatchMessage([]byte("old"))
		close(oldDone)
	}()
	select {
	case <-oldStarted:
	case <-time.After(time.Second):
		t.Fatal("old-generation callback did not start")
	}

	client.PauseDispatch()
	client.dispatchMessage([]byte("replacement"))
	client.Close()
	close(releaseOld)
	select {
	case <-oldDone:
	case <-time.After(time.Second):
		t.Fatal("accepted old-generation callback did not finish")
	}
	client.ResumeDispatch(nil)

	mu.Lock()
	defer mu.Unlock()
	want := []string{"old"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("delivered messages = %v, want %v", got, want)
	}
}

func TestWsAccountClientFailedReplacementConnectResetsDispatch(t *testing.T) {
	restServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"listenKey":"failed-dispatch-key"}`)
	}))
	defer restServer.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := NewWsAccountClientWithEndpointProfile(ctx, "api-key", "secret", EndpointProfile{
		RESTBaseURL:      restServer.URL,
		EndpointPrefix:   "/fapi",
		AccountVersion:   "v2",
		WSPrivateBaseURL: "ws://127.0.0.1:1/ws",
	})
	client.Client.WithRateLimiter(nil)
	defer client.Close()

	ws, err := client.replaceAndConnectForRecovery(client.currentLifecycle())
	if err == nil {
		t.Fatal("replacement Connect unexpectedly succeeded")
	}
	if ws == nil {
		t.Fatal("failed replacement did not return its websocket client")
	}

	delivered := make(chan string, 1)
	ws.Handler = func(message []byte) {
		delivered <- string(message)
	}
	ws.dispatchMessage([]byte("after-failure"))
	select {
	case got := <-delivered:
		if got != "after-failure" {
			t.Fatalf("delivered message = %q, want after-failure", got)
		}
	case <-time.After(time.Second):
		t.Fatal("failed replacement left dispatch paused")
	}
}

func TestWsAccountClientDisconnectStartedHookCanCloseAfterAcceptedData(t *testing.T) {
	restServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"listenKey":"close-from-started"}`)
	}))
	defer restServer.Close()

	wsURL := newPerpWSServer(t, func(conn *websocket.Conn, _ *http.Request) {
		defer conn.Close()
		if err := conn.WriteMessage(websocket.TextMessage, perpOrderUpdateForDispatchTest(1)); err != nil {
			t.Errorf("write old private data: %v", err)
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := NewWsAccountClientWithEndpointProfile(ctx, "api-key", "secret", EndpointProfile{
		RESTBaseURL:      restServer.URL,
		EndpointPrefix:   "/fapi",
		AccountVersion:   "v2",
		WSPrivateBaseURL: wsURL + "/ws",
	})
	client.Client.WithRateLimiter(nil)
	client.KeepAliveInt = time.Hour

	events := make(chan string, 2)
	client.SubscribeOrderUpdate(func(*OrderUpdateEvent) {
		events <- "old-data"
	})
	hookReturned := make(chan struct{})
	client.SetReconnectHooks(func(error) {
		events <- "started"
		client.Close()
		close(hookReturned)
	}, nil)
	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	waitPerpDispatchEvent(t, events, "old-data")
	waitPerpDispatchEvent(t, events, "started")
	select {
	case <-hookReturned:
	case <-time.After(time.Second):
		t.Fatal("disconnect Started hook deadlocked while closing account client")
	}
}

func TestWsAccountClientManualRecoveryWaitsForAcceptedOldDataAndPausesSource(t *testing.T) {
	client := NewWsAccountClient(context.Background(), "api-key", "secret")
	ws := client.WsClient

	oldEntered := make(chan struct{})
	releaseOld := make(chan struct{})
	events := make(chan string, 3)
	ws.Handler = func(message []byte) {
		if string(message) == "old" {
			close(oldEntered)
			<-releaseOld
			events <- "old-returned"
			return
		}
		events <- string(message)
	}
	client.SetReconnectHooks(func(error) {
		events <- "started"
	}, nil)

	oldDone := make(chan struct{})
	go func() {
		ws.dispatchMessage([]byte("old"))
		close(oldDone)
	}()
	select {
	case <-oldEntered:
	case <-time.After(time.Second):
		t.Fatal("old callback did not start")
	}
	recoveryDone := make(chan struct{})
	go func() {
		client.beginRecovery(errors.New("manual recovery"))
		close(recoveryDone)
	}()
	select {
	case event := <-events:
		t.Fatalf("recovery callback overtook accepted old data: %q", event)
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseOld)
	waitPerpDispatchEvent(t, events, "old-returned")
	waitPerpDispatchEvent(t, events, "started")
	select {
	case <-oldDone:
	case <-time.After(time.Second):
		t.Fatal("old callback did not return")
	}
	select {
	case <-recoveryDone:
	case <-time.After(time.Second):
		t.Fatal("manual recovery did not finish")
	}

	ws.dispatchMessage([]byte("stale-old-socket-data"))
	ws.Close()
	ws.ResumeDispatch(nil)
	select {
	case event := <-events:
		t.Fatalf("paused old-socket data survived source close: %q", event)
	default:
	}
}

func TestWsAccountClientCloseWhileDrainingOldDataSuppressesStarted(t *testing.T) {
	client := NewWsAccountClient(context.Background(), "api-key", "secret")
	lifecycle := client.currentLifecycle()
	source := client.WsClient

	oldEntered := make(chan struct{})
	releaseOld := make(chan struct{})
	oldReturned := make(chan struct{})
	source.Handler = func([]byte) {
		close(oldEntered)
		<-releaseOld
		client.Close()
		close(oldReturned)
	}
	started := make(chan struct{}, 1)
	client.SetReconnectHooks(func(error) {
		started <- struct{}{}
	}, nil)

	go source.dispatchMessage([]byte("old"))
	select {
	case <-oldEntered:
	case <-time.After(time.Second):
		t.Fatal("old callback did not start")
	}
	recoveryReturned := make(chan struct{})
	go func() {
		client.beginRecovery(errors.New("manual recovery"))
		close(recoveryReturned)
	}()
	deadline := time.Now().Add(time.Second)
	for !source.dropDispatch.Load() {
		if time.Now().After(deadline) {
			t.Fatal("recovery did not establish the source dispatch barrier")
		}
		time.Sleep(time.Millisecond)
	}

	// Model a replacement becoming current while the already accepted source
	// callback is still finishing. Its Close must invalidate the old recovery.
	client.lifecycleMu.Lock()
	client.resetWSClientLocked(lifecycle)
	client.lifecycleMu.Unlock()
	close(releaseOld)
	select {
	case <-oldReturned:
	case <-time.After(time.Second):
		t.Fatal("old callback did not return from Close")
	}
	select {
	case <-recoveryReturned:
	case <-time.After(time.Second):
		t.Fatal("recovery barrier did not return")
	}
	select {
	case <-started:
		t.Fatal("canceled stale source published Started")
	default:
	}
	source.Close()
}

func waitPerpDispatchEvent(t *testing.T, events <-chan string, want string) {
	t.Helper()
	select {
	case got := <-events:
		if got != want {
			t.Fatalf("event = %q, want %q", got, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %q", want)
	}
}

func perpOrderUpdateForDispatchTest(orderID int64) []byte {
	return []byte(fmt.Sprintf(`{"e":"ORDER_TRADE_UPDATE","o":{"i":%d}}`, orderID))
}
