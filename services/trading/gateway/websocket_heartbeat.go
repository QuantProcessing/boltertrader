package gateway

import (
	"context"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const (
	gatewayWebSocketHeartbeatInterval = 20 * time.Second
	gatewayWebSocketHeartbeatTimeout  = 10 * time.Second
)

// monitorGatewayWebSocket detects a connection that remains locally open but
// whose peer has stopped processing frames. Ping waits for the matching pong;
// on timeout CloseNow unblocks the owning read loop, which remains the single
// place responsible for publishing disconnect state and reconnecting.
func monitorGatewayWebSocket(ctx context.Context, connection *websocket.Conn, interval, timeout time.Duration, writes sync.Locker) {
	if ctx == nil || connection == nil || interval <= 0 || timeout <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(ctx, timeout)
			if writes != nil {
				writes.Lock()
			}
			err := connection.Ping(pingCtx)
			if writes != nil {
				writes.Unlock()
			}
			cancel()
			if err != nil {
				connection.CloseNow()
				return
			}
		}
	}
}
