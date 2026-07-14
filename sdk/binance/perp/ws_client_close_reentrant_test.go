package perp

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestWSClientReadLoopHandlerCanCloseClient(t *testing.T) {
	wsURL := newPerpWSServer(t, func(conn *websocket.Conn, _ *http.Request) {
		defer conn.Close()
		if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"event":"close"}`)); err != nil {
			t.Errorf("write close event: %v", err)
			return
		}
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})

	client := NewWSClient(context.Background(), wsURL+"/ws")
	callbackReturned := make(chan struct{})
	client.Handler = func([]byte) {
		client.Close()
		close(callbackReturned)
	}
	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	select {
	case <-callbackReturned:
	case <-time.After(time.Second):
		t.Fatal("read-loop handler deadlocked while closing its WSClient")
	}
}
