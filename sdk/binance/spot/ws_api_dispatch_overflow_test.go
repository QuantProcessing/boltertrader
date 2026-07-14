package spot

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/internal/wsdispatch"
	"github.com/gorilla/websocket"
)

func TestWsAPIClientPausedDispatchOverflowClosesExactConnectionAndRecovers(t *testing.T) {
	var connections atomic.Int64
	firstClosed := make(chan struct{})
	replacementConnected := make(chan struct{})
	var closeFirst sync.Once

	wsURL := newSpotWSServer(t, func(conn *websocket.Conn, _ *http.Request) {
		connection := connections.Add(1)
		defer conn.Close()
		switch connection {
		case 1:
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"event":{"e":"executionReport","i":1}}`))
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"event":{"e":"executionReport","i":2}}`))
			if _, _, err := conn.ReadMessage(); err != nil {
				closeFirst.Do(func() { close(firstClosed) })
			}
		case 2:
			close(replacementConnected)
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"event":{"e":"executionReport","i":3}}`))
			_, _, _ = conn.ReadMessage()
		}
	})

	client := NewWsAPIClient(context.Background()).WithURL(wsURL + "/ws")
	client.ReconnectWait = time.Millisecond
	client.eventDispatcher = wsdispatch.NewBoundedDispatcher(1)
	client.pausePushedEvents()
	hookEntered := make(chan struct{})
	releaseHook := make(chan struct{})
	resumeDone := make(chan struct{})
	var releaseHookOnce sync.Once
	defer releaseHookOnce.Do(func() { close(releaseHook) })
	go func() {
		client.resumePushedEvents(func() {
			close(hookEntered)
			<-releaseHook
		})
		close(resumeDone)
	}()
	waitSpotOverflowSignal(t, hookEntered, "blocked recovery callback to start")
	replacementEvent := make(chan struct{}, 1)
	disconnectErr := make(chan error, 1)
	client.SetEventHandler(func(message []byte) {
		if string(message) == `{"event":{"e":"executionReport","i":3}}` {
			replacementEvent <- struct{}{}
		}
	})
	client.SetOnDisconnect(func(err error) {
		// Account recovery resets the failed generation before accepting the
		// replacement connection's pushed events.
		client.resetPushedEvents()
		disconnectErr <- err
	})
	t.Cleanup(client.Close)

	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	waitSpotOverflowSignal(t, firstClosed, "overflow to close the first websocket")
	select {
	case err := <-disconnectErr:
		if !errors.Is(err, wsdispatch.ErrBufferFull) {
			t.Fatalf("disconnect error = %v, want wrapped %v", err, wsdispatch.ErrBufferFull)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for overflow disconnect error")
	}
	waitSpotOverflowSignal(t, replacementConnected, "replacement websocket to connect")
	releaseHookOnce.Do(func() { close(releaseHook) })
	waitSpotOverflowSignal(t, resumeDone, "overflowed recovery drain to stop")
	waitSpotOverflowSignal(t, replacementEvent, "replacement websocket event to be delivered")

	if got := connections.Load(); got != 2 {
		t.Fatalf("connections = %d, want exactly 2", got)
	}
}

func waitSpotOverflowSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}
