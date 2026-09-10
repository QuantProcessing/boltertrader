package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	wire "github.com/QuantProcessing/boltertrader/api/exchange-gateway/v1"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// WatchAccountActivity exposes only wakeups. The caller must read the durable
// order identity; a raw venue notification is not canonical fill evidence.
func (c *Client) WatchAccountActivity(ctx context.Context, notify func(string)) error {
	target := "ws" + strings.TrimPrefix(c.internalOrigin, "http") + "/internal/v1/account/ws"
	header := make(http.Header)
	header.Set("Authorization", "Bearer "+c.internalToken)
	dialCtx, stop := context.WithTimeout(ctx, c.timeout)
	conn, response, err := websocket.Dial(dialCtx, target, &websocket.DialOptions{HTTPClient: c.http, HTTPHeader: header})
	stop()
	if response != nil && response.Body != nil {
		response.Body.Close()
	}
	if err != nil {
		return transportError(err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(c.limit)
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if err := wsjson.Write(streamCtx, conn, wire.Control{Operation: wire.OperationSubscribe, RequestID: "account-wakeup", Topics: []wire.TopicRequest{{Topic: wire.TopicAccountEvents}}}); err != nil {
		return transportError(err)
	}
	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-streamCtx.Done():
				return
			case <-ticker.C:
				pingCtx, done := context.WithTimeout(streamCtx, 5*time.Second)
				err := wsjson.Write(pingCtx, conn, wire.Control{Operation: wire.OperationPing, RequestID: "heartbeat"})
				done()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()
	for {
		var message wire.Message
		readCtx, done := context.WithTimeout(streamCtx, 45*time.Second)
		err := wsjson.Read(readCtx, conn, &message)
		done()
		if err != nil {
			return transportError(err)
		}
		if message.Type == wire.MessageError || message.Type == wire.MessageResyncRequired {
			return protocol("account stream gap")
		}
		if message.Type != wire.MessageDelta {
			continue
		}
		var event struct {
			AccountID string `json:"accountId"`
			Kind      string `json:"kind"`
		}
		if json.Unmarshal(message.Data, &event) == nil && event.AccountID != "" && (event.Kind == "account.order" || event.Kind == "account.fill") {
			notify(event.AccountID)
		}
	}
}
