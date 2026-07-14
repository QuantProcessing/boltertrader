package perp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestWsAccountClientRecoveryReplaysTriggerArrivingBeforeCompletion(t *testing.T) {
	var listenKeyRequests atomic.Int64
	restServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/fapi/v1/listenKey" {
			t.Errorf("unexpected REST request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}
		request := listenKeyRequests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"listenKey":"key-%d"}`, request)
	}))
	defer restServer.Close()

	paths := make(chan string, 4)
	wsURL := newPerpWSServer(t, func(conn *websocket.Conn, r *http.Request) {
		defer conn.Close()
		paths <- r.URL.Path
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	profile := EndpointProfile{
		RESTBaseURL:      restServer.URL,
		EndpointPrefix:   "/fapi",
		AccountVersion:   "v2",
		WSPrivateBaseURL: wsURL + "/ws",
	}
	client := NewWsAccountClientWithEndpointProfile(ctx, "api-key", "secret", profile)
	client.Client.WithRateLimiter(nil)
	client.KeepAliveInt = time.Hour
	defer client.Close()

	firstHookEntered := make(chan struct{})
	releaseFirstHook := make(chan struct{})
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(releaseFirstHook) }) })
	var resubscribeHooks atomic.Int64
	client.SetOnResubscribe(func() {
		if resubscribeHooks.Add(1) == 1 {
			close(firstHookEntered)
			<-releaseFirstHook
		}
	})
	recovered := make(chan struct{}, 2)
	client.SetReconnectHooks(nil, func() { recovered <- struct{}{} })

	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	waitPerpAccountPath(t, paths, "/ws/key-1", time.Second)

	recoveryDone := make(chan struct{})
	go func() {
		client.resubscribe()
		close(recoveryDone)
	}()
	waitPerpAccountPath(t, paths, "/ws/key-2", time.Second)
	select {
	case <-firstHookEntered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first recovery completion window")
	}

	// A listenKeyExpired/keepalive trigger in this window used to be dropped
	// because the single recovery worker was still running.
	client.resubscribe()
	release.Do(func() { close(releaseFirstHook) })

	waitPerpAccountPath(t, paths, "/ws/key-3", 2*time.Second)
	select {
	case <-recovered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for coalesced recovery completion")
	}
	select {
	case <-recoveryDone:
	case <-time.After(time.Second):
		t.Fatal("recovery worker did not finish")
	}
	select {
	case <-recovered:
		t.Fatal("superseded recovery emitted a recovered callback")
	default:
	}
	if got := listenKeyRequests.Load(); got != 3 {
		t.Fatalf("listen-key creates = %d, want initial plus two recovery generations", got)
	}
	if got := resubscribeHooks.Load(); got != 2 {
		t.Fatalf("resubscribe hooks = %d, want one per connected recovery generation", got)
	}
}

func TestWsAccountClientPrivateMessageLogRedactsPayload(t *testing.T) {
	const (
		balanceSecret = "SENTINEL_BINANCE_PRIVATE_BALANCE_7f2d"
		orderSecret   = "SENTINEL_BINANCE_PRIVATE_CLIENT_ORDER_981c"
	)
	core, observed := observer.New(zap.DebugLevel)
	ws := NewWSClient(context.Background(), "wss://example.invalid/ws")
	ws.Logger = zap.New(core).Sugar()
	client := NewWsAccountClient(context.Background(), "api-key", "secret")
	defer client.Close()

	payload := []byte(`{"e":"ORDER_TRADE_UPDATE","a":{"B":[{"a":"USDT","wb":"` + balanceSecret + `"}]},"o":{"c":"` + orderSecret + `"}}`)
	client.handleWSMessage(ws, payload)

	var logged strings.Builder
	for _, entry := range observed.All() {
		logged.WriteString(entry.Message)
		logged.WriteString(fmt.Sprint(entry.ContextMap()))
	}
	text := logged.String()
	for _, secret := range []string{balanceSecret, orderSecret} {
		if strings.Contains(text, secret) {
			t.Fatalf("private account WebSocket log leaked %q: %s", secret, text)
		}
	}
	for _, safeMetadata := range []string{"ORDER_TRADE_UPDATE", "bytes"} {
		if !strings.Contains(text, safeMetadata) {
			t.Fatalf("private account WebSocket log omitted safe metadata %q: %s", safeMetadata, text)
		}
	}
}

func TestWsAccountClientRecoveryCallbacksRemainGenerationOrdered(t *testing.T) {
	var listenKeyRequests atomic.Int64
	restServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := listenKeyRequests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"listenKey":"ordered-key-%d"}`, request)
	}))
	defer restServer.Close()

	paths := make(chan string, 3)
	wsURL := newPerpWSServer(t, func(conn *websocket.Conn, r *http.Request) {
		defer conn.Close()
		paths <- r.URL.Path
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

	events := make(chan string, 4)
	recoveredEntered := make(chan struct{})
	releaseRecovered := make(chan struct{})
	var releaseRecoveredOnce sync.Once
	t.Cleanup(func() { releaseRecoveredOnce.Do(func() { close(releaseRecovered) }) })
	client.SetReconnectHooks(func(error) {
		events <- "started"
	}, func() {
		close(recoveredEntered)
		<-releaseRecovered
		events <- "recovered"
	})
	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	waitPerpAccountPath(t, paths, "/ws/ordered-key-1", time.Second)

	recoveryDone := make(chan struct{})
	go func() {
		client.resubscribe()
		close(recoveryDone)
	}()
	waitPerpAccountPhase(t, events, "started", time.Second)
	waitPerpAccountPath(t, paths, "/ws/ordered-key-2", time.Second)
	select {
	case <-recoveredEntered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for recovered callback entry")
	}

	newGapStarted := make(chan struct{})
	go func() {
		client.beginRecovery(errors.New("new disconnect during recovered callback"))
		close(newGapStarted)
	}()
	select {
	case event := <-events:
		t.Fatalf("new generation callback overtook blocked recovered callback: %q", event)
	case <-time.After(50 * time.Millisecond):
	}
	releaseRecoveredOnce.Do(func() { close(releaseRecovered) })
	waitPerpAccountPhase(t, events, "recovered", time.Second)
	waitPerpAccountPhase(t, events, "started", time.Second)
	select {
	case <-newGapStarted:
	case <-time.After(time.Second):
		t.Fatal("new generation start did not finish")
	}
	select {
	case <-recoveryDone:
	case <-time.After(time.Second):
		t.Fatal("old recovery worker did not finish")
	}
	client.mu.Lock()
	recovering := client.recovering
	client.mu.Unlock()
	if !recovering {
		t.Fatal("newer recovery generation was cleared by the older recovered callback")
	}
}

func TestWsAccountClientUnexpectedDisconnectDoesNotWaitForStaleHandshakeRecovery(t *testing.T) {
	var listenKeyRequests atomic.Int64
	restServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := listenKeyRequests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"listenKey":"fresh-key-%d"}`, request)
	}))
	defer restServer.Close()

	var firstStaleHandshake atomic.Bool
	releaseInitial := make(chan struct{})
	paths := make(chan string, 4)
	upgrader := websocket.Upgrader{}
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ws/fresh-key-1" && firstStaleHandshake.Swap(true) {
			paths <- r.URL.Path + "#rejected"
			http.Error(w, "stale listen key rejected", http.StatusUnauthorized)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer conn.Close()
		paths <- r.URL.Path
		if r.URL.Path == "/ws/fresh-key-1" {
			<-releaseInitial
			return
		}
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer wsServer.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := NewWsAccountClientWithEndpointProfile(ctx, "api-key", "secret", EndpointProfile{
		RESTBaseURL:      restServer.URL,
		EndpointPrefix:   "/fapi",
		AccountVersion:   "v2",
		WSPrivateBaseURL: "ws" + strings.TrimPrefix(wsServer.URL, "http") + "/ws",
	})
	client.Client.WithRateLimiter(nil)
	client.KeepAliveInt = time.Hour
	client.WsClient.ReconnectWait = 10 * time.Millisecond
	defer client.Close()

	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	waitPerpAccountPath(t, paths, "/ws/fresh-key-1", time.Second)
	close(releaseInitial)

	deadline := time.After(2 * time.Second)
	for {
		select {
		case path := <-paths:
			if path == "/ws/fresh-key-2" {
				if got := listenKeyRequests.Load(); got != 2 {
					t.Fatalf("listen-key creates = %d, want initial plus immediate fresh recovery", got)
				}
				return
			}
		case <-deadline:
			t.Fatalf("fresh listen-key recovery never started while stale handshakes failed; requests=%d", listenKeyRequests.Load())
		}
	}
}
