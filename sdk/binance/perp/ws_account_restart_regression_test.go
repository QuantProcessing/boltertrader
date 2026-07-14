package perp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestWsAccountClientCloseThenConnectStartsFreshLifecycle(t *testing.T) {
	var listenKeyRequests atomic.Int64
	restServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := listenKeyRequests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"listenKey":"restart-key-%d"}`, request)
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

	parentCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := NewWsAccountClientWithEndpointProfile(parentCtx, "api-key", "secret", EndpointProfile{
		RESTBaseURL:      restServer.URL,
		EndpointPrefix:   "/fapi",
		AccountVersion:   "v2",
		WSPrivateBaseURL: wsURL + "/ws",
	})
	client.Client.WithRateLimiter(nil)
	client.KeepAliveInt = time.Hour

	if err := client.Connect(); err != nil {
		t.Fatalf("first Connect: %v", err)
	}
	waitPerpAccountPath(t, paths, "/ws/restart-key-1", time.Second)
	firstWS := client.WsClient
	firstWS.Mu.RLock()
	stalePostReconnect := firstWS.postReconnect
	firstWS.Mu.RUnlock()
	if stalePostReconnect == nil {
		t.Fatal("first lifecycle did not register its reconnect callback")
	}

	client.Close()
	if err := client.Connect(); err != nil {
		t.Fatalf("Connect after Close: %v", err)
	}
	waitPerpAccountPath(t, paths, "/ws/restart-key-2", time.Second)
	secondWS := client.WsClient
	if secondWS == firstWS {
		t.Fatal("Connect after Close reused the closed WebSocket lifecycle")
	}

	// A reconnect callback already queued by the closed transport belongs to the
	// old lifecycle and must not recover or replace the newly connected one.
	stalePostReconnect()
	time.Sleep(100 * time.Millisecond)
	if got := listenKeyRequests.Load(); got != 2 {
		t.Fatalf("stale recovery callback created %d listen keys, want 2", got)
	}
	if client.WsClient != secondWS {
		t.Fatal("stale recovery callback replaced the restarted WebSocket client")
	}
	if !secondWS.IsConnected() {
		t.Fatal("stale recovery callback closed the restarted WebSocket client")
	}

	client.Close()
}
