package perp

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

func TestWSClientPausedDispatchOverflowClosesExactConnectionAndRecovers(t *testing.T) {
	var connections atomic.Int64
	firstClosed := make(chan struct{})
	replacementConnected := make(chan struct{})
	var closeFirst sync.Once

	wsURL := newPerpWSServer(t, func(conn *websocket.Conn, _ *http.Request) {
		connection := connections.Add(1)
		defer conn.Close()
		switch connection {
		case 1:
			_ = conn.WriteMessage(websocket.TextMessage, []byte("old-1"))
			_ = conn.WriteMessage(websocket.TextMessage, []byte("old-2"))
			if _, _, err := conn.ReadMessage(); err != nil {
				closeFirst.Do(func() { close(firstClosed) })
			}
		case 2:
			close(replacementConnected)
			_ = conn.WriteMessage(websocket.TextMessage, []byte("replacement"))
			_, _, _ = conn.ReadMessage()
		}
	})

	client := NewWSClient(context.Background(), wsURL+"/ws")
	client.ReconnectWait = time.Millisecond
	client.pongInterval = time.Hour
	client.dispatcher = wsdispatch.NewBoundedDispatcher(1)
	client.PauseDispatch()
	replacementEvent := make(chan struct{}, 1)
	disconnectErr := make(chan error, 1)
	client.Handler = func(message []byte) {
		if string(message) == "replacement" {
			replacementEvent <- struct{}{}
		}
	}
	client.SetOnDisconnect(func(err error) {
		client.ResetDispatch()
		disconnectErr <- err
	})
	t.Cleanup(client.Close)

	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	waitPerpOverflowSignal(t, firstClosed, "overflow to close the first websocket")
	select {
	case err := <-disconnectErr:
		if !errors.Is(err, wsdispatch.ErrBufferFull) {
			t.Fatalf("disconnect error = %v, want wrapped %v", err, wsdispatch.ErrBufferFull)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for overflow disconnect error")
	}
	waitPerpOverflowSignal(t, replacementConnected, "replacement websocket to connect")
	waitPerpOverflowSignal(t, replacementEvent, "replacement websocket event to be delivered")

	time.Sleep(25 * time.Millisecond)
	if got := connections.Load(); got != 2 {
		t.Fatalf("connections = %d, want exactly 2", got)
	}
	if !client.IsConnected() {
		t.Fatal("replacement websocket was closed by the failed source generation")
	}
}

func waitPerpOverflowSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}
