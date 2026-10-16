package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/gateway/hub"
	"github.com/coder/websocket"
)

const (
	maxSubscriptions  = 64
	outboundQueue     = 1024
	maxControlBytes   = 4 << 10
	writeTimeout      = 10 * time.Second
	keepaliveInterval = 20 * time.Second
)

// control is one client → server message.
type control struct {
	Op         string `json:"op"` // sub | unsub | ping
	Channel    string `json:"ch,omitempty"`
	Instrument string `json:"inst,omitempty"`
	Interval   string `json:"interval,omitempty"`
	Account    string `json:"account,omitempty"`
}

// connection is a hub.Sink with a bounded queue. A client that falls a whole
// queue behind is disconnected rather than allowed to hold memory.
type connection struct {
	queue  chan []byte
	cancel context.CancelFunc
	once   sync.Once
}

func (conn *connection) Send(payload []byte) bool {
	select {
	case conn.queue <- payload:
		return true
	default:
		conn.once.Do(conn.cancel)
		return false
	}
}

func (server *Server) websocket(w http.ResponseWriter, r *http.Request) {
	socket, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionContextTakeover})
	if err != nil {
		return
	}
	defer socket.CloseNow()
	socket.SetReadLimit(maxControlBytes)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	conn := &connection{queue: make(chan []byte, outboundQueue), cancel: cancel}

	subscriptions := map[hub.Key]*hub.Hub{}
	defer func() {
		for key, owner := range subscriptions {
			owner.Unsubscribe(key, conn)
		}
	}()

	go func() {
		ticker := time.NewTicker(keepaliveInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case payload := <-conn.queue:
				writeCtx, stop := context.WithTimeout(ctx, writeTimeout)
				err := socket.Write(writeCtx, websocket.MessageText, payload)
				stop()
				if err != nil {
					cancel()
					return
				}
			case <-ticker.C:
				pingCtx, stop := context.WithTimeout(ctx, writeTimeout)
				err := socket.Ping(pingCtx)
				stop()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()

	reply := func(value any) {
		payload, _ := json.Marshal(value)
		conn.Send(payload)
	}
	for {
		_, data, err := socket.Read(ctx)
		if err != nil {
			return
		}
		var message control
		if json.Unmarshal(data, &message) != nil {
			reply(map[string]any{"ch": "error", "data": map[string]string{"message": "invalid control message"}})
			continue
		}
		if message.Op == "ping" {
			reply(map[string]any{"ch": "pong"})
			continue
		}
		key, owner, validErr := server.route(hub.Key{Channel: message.Channel, Instrument: message.Instrument, Interval: message.Interval, Account: message.Account})
		switch message.Op {
		case "sub":
			if validErr != nil {
				reply(map[string]any{"ch": "error", "data": map[string]any{"message": validErr.Error(), "sub": message}})
				continue
			}
			if _, exists := subscriptions[key]; exists {
				continue
			}
			if len(subscriptions) >= maxSubscriptions {
				reply(map[string]any{"ch": "error", "data": map[string]any{"message": "too many subscriptions", "sub": message}})
				continue
			}
			subscriptions[key] = owner
			owner.Subscribe(key, conn)
		case "unsub":
			if current, exists := subscriptions[key]; exists {
				current.Unsubscribe(key, conn)
				delete(subscriptions, key)
			}
		default:
			reply(map[string]any{"ch": "error", "data": map[string]string{"message": "unknown op"}})
		}
	}
}

// route normalizes and validates a topic key and returns the hub serving it.
func (server *Server) route(key hub.Key) (hub.Key, *hub.Hub, error) {
	if key.Channel == "account" {
		if key.Instrument != "" || key.Interval != "" {
			return key, nil, errInvalidTopic
		}
		return key, server.private, server.accounts.ValidKey(key)
	}
	if key.Account != "" {
		return key, nil, errInvalidTopic
	}
	if key.Channel != "candle" {
		key.Interval = ""
	}
	return key, server.public, server.feeds.ValidKey(key)
}

var errInvalidTopic = errorString("invalid topic")

type errorString string

func (e errorString) Error() string { return string(e) }
