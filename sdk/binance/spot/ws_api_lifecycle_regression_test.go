package spot

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestWsAPIClientStaleReaderCannotCloseRestartedConnection(t *testing.T) {
	connections := make(chan struct{}, 2)
	serverURL := newSpotWSServer(t, func(conn *websocket.Conn, _ *http.Request) {
		defer conn.Close()
		connections <- struct{}{}
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})
	client := NewWsAPIClient(context.Background()).WithURL(serverURL + "/ws")
	defer client.Close()

	cleanupEntered := make(chan struct{})
	cleanupFinished := make(chan struct{}, 1)
	releaseCleanup := make(chan struct{})
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(releaseCleanup) }) })
	var cleanups atomic.Int64
	client.beforeReadLoopCleanup = func() {
		if cleanups.Add(1) == 1 {
			close(cleanupEntered)
			<-releaseCleanup
		}
	}
	client.afterReadLoopCleanup = func() {
		select {
		case cleanupFinished <- struct{}{}:
		default:
		}
	}
	if err := client.Connect(); err != nil {
		t.Fatalf("initial Connect: %v", err)
	}
	select {
	case <-connections:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial connection")
	}

	client.Close()
	select {
	case <-cleanupEntered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for stale read-loop cleanup")
	}
	if err := client.Connect(); err != nil {
		t.Fatalf("restart Connect: %v", err)
	}
	select {
	case <-connections:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for replacement connection")
	}
	release.Do(func() { close(releaseCleanup) })
	select {
	case <-cleanupFinished:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for stale read-loop cleanup to finish")
	}
	if !client.IsConnected() {
		t.Fatal("stale read loop closed the replacement connection")
	}
}

func TestWsAPIClientCloseDuringReconnectBackoffDoesNotReopen(t *testing.T) {
	connections := make(chan int64, 2)
	var connectionCount atomic.Int64
	serverURL := newSpotWSServer(t, func(conn *websocket.Conn, _ *http.Request) {
		defer conn.Close()
		connection := connectionCount.Add(1)
		connections <- connection
		if connection == 1 {
			return
		}
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})
	client := NewWsAPIClient(context.Background()).WithURL(serverURL + "/ws")
	client.ReconnectWait = 150 * time.Millisecond
	disconnected := make(chan struct{}, 1)
	client.SetOnDisconnect(func(error) { disconnected <- struct{}{} })
	var recovered atomic.Int64
	client.SetPostReconnect(func() { recovered.Add(1) })

	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	select {
	case <-connections:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial connection")
	}
	select {
	case <-disconnected:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for disconnect")
	}
	client.Close()
	time.Sleep(2 * client.ReconnectWait)

	if got := connectionCount.Load(); got != 1 {
		t.Fatalf("Close during backoff allowed reconnect: connections=%d", got)
	}
	if got := recovered.Load(); got != 0 {
		t.Fatalf("Close during backoff emitted postReconnect %d times", got)
	}
	if client.IsConnected() {
		t.Fatal("client reconnected after Close")
	}
}

func TestWsAPIClientStaleReconnectSleeperDoesNotPublishManualConnection(t *testing.T) {
	connections := make(chan int64, 2)
	var connectionCount atomic.Int64
	serverURL := newSpotWSServer(t, func(conn *websocket.Conn, _ *http.Request) {
		defer conn.Close()
		connection := connectionCount.Add(1)
		connections <- connection
		if connection == 1 {
			return
		}
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})
	client := NewWsAPIClient(context.Background()).WithURL(serverURL + "/ws")
	client.ReconnectWait = 200 * time.Millisecond
	defer client.Close()
	disconnected := make(chan struct{}, 1)
	client.SetOnDisconnect(func(error) { disconnected <- struct{}{} })
	var recovered atomic.Int64
	client.SetPostReconnect(func() { recovered.Add(1) })

	if err := client.Connect(); err != nil {
		t.Fatalf("initial Connect: %v", err)
	}
	select {
	case <-connections:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial connection")
	}
	select {
	case <-disconnected:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for disconnect")
	}
	if err := client.Connect(); err != nil {
		t.Fatalf("manual Connect during backoff: %v", err)
	}
	select {
	case connection := <-connections:
		if connection != 2 {
			t.Fatalf("replacement connection = %d, want 2", connection)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for manual replacement connection")
	}
	time.Sleep(2 * client.ReconnectWait)

	if got := recovered.Load(); got != 0 {
		t.Fatalf("stale reconnect sleeper published manual connection %d times", got)
	}
	if !client.IsConnected() {
		t.Fatal("manual replacement connection was lost")
	}
}

func TestWsAPIClientCloseBeforePostReconnectSuppressesCallback(t *testing.T) {
	connections := make(chan int64, 2)
	var connectionCount atomic.Int64
	serverURL := newSpotWSServer(t, func(conn *websocket.Conn, _ *http.Request) {
		defer conn.Close()
		connection := connectionCount.Add(1)
		connections <- connection
		if connection == 1 {
			return
		}
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})
	client := NewWsAPIClient(context.Background()).WithURL(serverURL + "/ws")
	client.ReconnectWait = 10 * time.Millisecond
	disconnected := make(chan struct{}, 1)
	client.SetOnDisconnect(func(error) { disconnected <- struct{}{} })
	callbackGate := make(chan struct{})
	releaseCallback := make(chan struct{})
	client.beforePostReconnect = func() {
		close(callbackGate)
		<-releaseCallback
	}
	var recovered atomic.Int64
	client.SetPostReconnect(func() { recovered.Add(1) })

	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	select {
	case <-connections:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial connection")
	}
	select {
	case <-disconnected:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for disconnect")
	}
	select {
	case <-callbackGate:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting before postReconnect")
	}
	client.Close()
	close(releaseCallback)
	time.Sleep(50 * time.Millisecond)

	if got := recovered.Load(); got != 0 {
		t.Fatalf("closed generation emitted postReconnect %d times", got)
	}
	if client.IsConnected() {
		t.Fatal("client remained connected after Close")
	}
}

func TestWsAPIClientConcurrentCloseIsIdempotent(t *testing.T) {
	client := NewWsAPIClient(context.Background())
	client.Done = make(chan struct{})
	entered := make(chan struct{})
	var entrants atomic.Int64
	client.beforeDoneClose = func() {
		if entrants.Add(1) == 2 {
			close(entered)
		}
		select {
		case <-entered:
		case <-time.After(100 * time.Millisecond):
		}
	}

	var panics atomic.Int64
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if recover() != nil {
					panics.Add(1)
				}
			}()
			client.Close()
		}()
	}
	wg.Wait()

	if got := panics.Load(); got != 0 {
		t.Fatalf("concurrent Close panicked %d times", got)
	}
}
