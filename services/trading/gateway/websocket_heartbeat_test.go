package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestGatewayHeartbeatClosesSilentlyDeadConnection(t *testing.T) {
	accepted := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer connection.CloseNow()
		close(accepted)
		// Deliberately do not read. coder/websocket processes ping frames while
		// reading, so this models a peer whose process/socket exists but whose
		// subscription loop has stopped making progress.
		<-release
	}))
	defer server.Close()
	defer close(release)

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	connection, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.CloseNow()
	<-accepted
	go monitorGatewayWebSocket(ctx, connection, 10*time.Millisecond, 20*time.Millisecond, nil)

	var value any
	if err := wsjson.Read(ctx, connection, &value); err == nil {
		t.Fatal("silently dead connection remained readable after heartbeat timeout")
	}
}
