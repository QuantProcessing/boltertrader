package spot

import (
	"context"
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

	wsURL := newSpotWSServer(t, func(conn *websocket.Conn, _ *http.Request) {
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
	client.Handler = func(message []byte) {
		if string(message) == "replacement" {
			replacementEvent <- struct{}{}
		}
	}
	t.Cleanup(client.Close)

	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	waitSpotOverflowSignal(t, firstClosed, "overflow to close the first websocket")
	waitSpotOverflowSignal(t, replacementConnected, "replacement websocket to connect")
	waitSpotOverflowSignal(t, replacementEvent, "replacement websocket event to be delivered")

	time.Sleep(25 * time.Millisecond)
	if got := connections.Load(); got != 2 {
		t.Fatalf("connections = %d, want exactly 2", got)
	}
	if !client.IsConnected() {
		t.Fatal("replacement websocket was closed by the failed source generation")
	}
}
