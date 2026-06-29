package lighter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/vmihailenco/msgpack/v5"
	"go.uber.org/zap"
)

const (
	MainnetWSURL = "wss://mainnet.zklighter.elliot.ai/stream"
)

type WSEncoding string

const (
	WSEncodingAuto    WSEncoding = "auto"
	WSEncodingJSON    WSEncoding = "json"
	WSEncodingMsgpack WSEncoding = "msgpack"
)

type WSConfig struct {
	URL               string
	ReadOnly          bool
	Encoding          WSEncoding
	KeepaliveInterval time.Duration
	ReconnectWait     time.Duration
}

type websocketConn interface {
	ReadMessage() (int, []byte, error)
	WriteJSON(v interface{}) error
	WriteControl(messageType int, data []byte, deadline time.Time) error
	SetReadDeadline(t time.Time) error
	Close() error
}

type subscription struct {
	authToken   *string
	rawHandler  func([]byte)
	dispatchers []typedDispatcher
}

type WebsocketClient struct {
	URL     string
	Conn    *websocket.Conn
	conn    websocketConn
	Mu      sync.RWMutex
	WriteMu sync.Mutex
	// Subscriptions maps channel name -> subscription (auth + handler)
	Subscriptions map[string]*subscription
	// PendingRequests maps request ID -> response channel for transaction tracking
	PendingRequests map[string]chan *TxResponse
	pendingMu       sync.RWMutex
	Logger          *zap.SugaredLogger

	// Reconnect logic
	ReconnectWait time.Duration

	// Error handling
	OnError func(error)

	ctx    context.Context
	cancel context.CancelFunc
	config WSConfig
}

func NewWebsocketClient(ctx context.Context) *WebsocketClient {
	return NewWebsocketClientWithConfig(ctx, WSConfig{})
}

func NewWebsocketClientWithConfig(ctx context.Context, cfg WSConfig) *WebsocketClient {
	// Create cancellable context
	ctx, cancel := context.WithCancel(ctx)

	if cfg.URL == "" {
		cfg.URL = MainnetWSURL
	}
	if cfg.Encoding == "" {
		cfg.Encoding = WSEncodingMsgpack
	}
	if cfg.KeepaliveInterval <= 0 {
		cfg.KeepaliveInterval = 30 * time.Second
	}
	if cfg.ReconnectWait <= 0 {
		cfg.ReconnectWait = 1 * time.Second
	}

	return &WebsocketClient{
		URL:             cfg.URL,
		Subscriptions:   make(map[string]*subscription),
		PendingRequests: make(map[string]chan *TxResponse),
		Logger:          zap.NewNop().Sugar().Named("lighter"),
		ReconnectWait:   cfg.ReconnectWait,
		OnError:         func(err error) {},
		ctx:             ctx,
		cancel:          cancel,
		config:          cfg,
	}
}

func (c *WebsocketClient) Connect() error {
	c.Mu.Lock()
	defer c.Mu.Unlock()

	if c.Conn != nil {
		return nil
	}

	// Use internal 10 second timeout
	ctx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
	defer cancel()
	urlToDial, err := c.buildURL()
	if err != nil {
		return err
	}
	dialer := *websocket.DefaultDialer
	dialer.EnableCompression = true
	conn, _, err := dialer.DialContext(ctx, urlToDial, nil)
	if err != nil {
		return err
	}

	c.Conn = conn
	c.conn = conn

	go c.readLoop()
	go c.pingLoop()

	return nil
}

func (c *WebsocketClient) Close() {
	// Cancel context to stop loops
	c.cancel()

	c.Mu.Lock()
	defer c.Mu.Unlock()

	if c.Conn != nil {
		c.Conn.Close()
		c.Conn = nil
	}
	c.conn = nil
}

func (c *WebsocketClient) readLoop() {
	defer func() {
		// Clean up connection
		c.Mu.Lock()
		if c.Conn != nil {
			c.Conn.Close()
			c.Conn = nil
		}
		c.Mu.Unlock()

		// Trigger reconnect if not manually canceled
		if c.ctx.Err() == nil {
			c.reconnect()
		}
	}()

	for {
		select {
		case <-c.ctx.Done():
			c.Logger.Debug("Read loop stopping due to context cancellation")
			return
		default:
			conn := c.activeConn()
			if conn == nil {
				return
			}
			messageType, message, err := conn.ReadMessage()
			if err != nil {
				// Check if intentionally closed
				if c.ctx.Err() != nil {
					c.Logger.Debug("Read loop stopping due to context cancellation")
					return
				}
				// Log unexpected errors
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
					c.Logger.Errorw("websocket unexpected close", "error", err)
				} else {
					c.Logger.Debugw("websocket read error", "error", err)
				}
				return
			}
			// Extend read deadline on any message received
			conn.SetReadDeadline(time.Now().Add(60 * time.Second))
			if err := c.handleIncomingFrame(messageType, message); err != nil {
				c.Logger.Errorw("failed to handle websocket frame", "error", err)
			}
		}
	}
}

func (c *WebsocketClient) pingLoop() {
	ticker := time.NewTicker(c.config.KeepaliveInterval)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			if err := c.sendPing(); err != nil {
				c.Logger.Errorw("websocket ping error", "error", err)
				return
			}
			c.Logger.Debugw("websocket ping sent")
		}
	}
}

func (c *WebsocketClient) reconnect() {
	time.Sleep(c.ReconnectWait)
	c.Logger.Infow("reconnecting...")
	if err := c.Connect(); err != nil {
		c.Logger.Errorw("reconnect failed", "error", err)
		go c.reconnect()
		return
	}

	// After successful reconnect, re-subscribe all existing channels
	c.resubscribeAll()
}

// resubscribeAll re-sends subscribe requests for all stored subscriptions.
// It does NOT change the in-memory Subscriptions map; it only restores
// server-side state after reconnect.
func (c *WebsocketClient) resubscribeAll() {
	c.Mu.RLock()
	subsSnapshot := make(map[string]*subscription, len(c.Subscriptions))
	for ch, sub := range c.Subscriptions {
		subsSnapshot[ch] = sub
	}
	c.Mu.RUnlock()

	for ch, sub := range subsSnapshot {
		params := map[string]string{
			"channel": ch,
			"type":    "subscribe",
		}
		if sub.authToken != nil {
			params["auth"] = *sub.authToken
		}
		if err := c.Send(params); err != nil {
			c.Logger.Errorw("failed to resubscribe channel", "channel", ch, "error", err)
		} else {
			c.Logger.Infow("resubscribed channel", "channel", ch)
		}
	}
}

func (c *WebsocketClient) HandleMessage(message []byte) {
	if err := c.handleIncomingFrame(websocket.TextMessage, message); err != nil {
		c.Logger.Errorw("failed to handle websocket message", "error", err)
	}
}

// Subscribe registers a handler for a channel.
func (c *WebsocketClient) Subscribe(channel string, authToken *string, handler func([]byte)) error {
	if err := c.registerRawSubscription(channel, authToken, handler); err != nil {
		return err
	}

	params := map[string]string{
		"channel": channel,
		"type":    "subscribe",
	}
	if authToken != nil {
		params["auth"] = *authToken
	}
	return c.Send(params)
}

func (c *WebsocketClient) Unsubscribe(channel string) error {
	c.Mu.Lock()
	defer c.Mu.Unlock()
	delete(c.Subscriptions, channel)

	// docs do not show this action
	return c.Send(map[string]string{
		"channel": channel,
		"type":    "unsubscribe",
	})
}

func (c *WebsocketClient) Send(v any) error {
	c.WriteMu.Lock()
	defer c.WriteMu.Unlock()

	conn := c.activeConn()
	if conn == nil {
		return fmt.Errorf("websocket not connected")
	}

	return conn.WriteJSON(v)
}

// RegisterPendingRequest creates a response channel for a request ID with timeout
func (c *WebsocketClient) RegisterPendingRequest(id string) chan *TxResponse {
	ch := make(chan *TxResponse, 1)
	c.pendingMu.Lock()
	c.PendingRequests[id] = ch
	c.pendingMu.Unlock()
	return ch
}

// UnregisterPendingRequest removes a pending request
func (c *WebsocketClient) UnregisterPendingRequest(id string) {
	c.pendingMu.Lock()
	if ch, ok := c.PendingRequests[id]; ok {
		close(ch)
		delete(c.PendingRequests, id)
	}
	c.pendingMu.Unlock()
}

func (c *WebsocketClient) registerRawSubscription(channel string, authToken *string, handler func([]byte)) error {
	c.Mu.Lock()
	defer c.Mu.Unlock()

	if _, ok := c.Subscriptions[channel]; ok {
		return fmt.Errorf("duplicate subscription to channel %s", channel)
	}

	c.Subscriptions[channel] = &subscription{
		authToken:  copyStringPointer(authToken),
		rawHandler: handler,
	}
	return nil
}

func (c *WebsocketClient) registerTypedSubscription(channel string, authToken *string, dispatcher typedDispatcher) error {
	c.Mu.Lock()
	defer c.Mu.Unlock()

	if _, ok := c.Subscriptions[channel]; ok {
		return fmt.Errorf("duplicate subscription to channel %s", channel)
	}

	c.Subscriptions[channel] = &subscription{
		authToken:   copyStringPointer(authToken),
		dispatchers: []typedDispatcher{dispatcher},
	}
	return nil
}

func (c *WebsocketClient) handleIncomingFrame(messageType int, payload []byte) error {
	env, normalized, err := c.decodeEnvelope(messageType, payload)
	if err != nil {
		return err
	}

	c.Logger.Debugw("Received message", "type", env.Type, "channel", env.Channel)

	var txResp TxResponse
	if err := json.Unmarshal(normalized, &txResp); err == nil && txResp.ID != "" {
		c.pendingMu.RLock()
		if ch, ok := c.PendingRequests[txResp.ID]; ok {
			select {
			case ch <- &txResp:
				c.Logger.Debugw("delivered tx response", "id", txResp.ID, "code", txResp.Code)
			default:
				c.Logger.Warnw("tx response channel blocked", "id", txResp.ID)
			}
		}
		c.pendingMu.RUnlock()
		return nil
	}

	if env.Type == "ping" {
		if err := c.Send(map[string]string{"type": "pong"}); err != nil {
			c.Logger.Debugw("failed to send pong", "error", err)
		}
		return nil
	}

	return c.dispatchEnvelope(env, normalized)
}

func (c *WebsocketClient) decodeEnvelope(messageType int, payload []byte) (*Envelope, []byte, error) {
	var decoded map[string]interface{}
	var normalized []byte
	var err error

	switch messageType {
	case websocket.TextMessage:
		normalized = payload
		err = json.Unmarshal(payload, &decoded)
	case websocket.BinaryMessage:
		decoded, err = decodeMsgpackPayload(payload)
		if err == nil {
			normalized, err = json.Marshal(decoded)
		}
	default:
		return nil, nil, fmt.Errorf("unsupported websocket message type %d", messageType)
	}
	if err != nil {
		return nil, nil, err
	}

	env := &Envelope{
		Type:          asString(decoded["type"]),
		Channel:       asString(decoded["channel"]),
		Timestamp:     asInt64(decoded["timestamp"]),
		LastUpdatedAt: asInt64(decoded["last_updated_at"]),
		raw:           normalized,
	}
	return env, normalized, nil
}

func decodeMsgpackPayload(payload []byte) (map[string]any, error) {
	dec := msgpack.NewDecoder(bytes.NewReader(payload))
	dec.UseLooseInterfaceDecoding(true)
	dec.SetMapDecoder(func(d *msgpack.Decoder) (interface{}, error) {
		return d.DecodeUntypedMap()
	})

	var raw any
	if err := dec.Decode(&raw); err != nil {
		return nil, err
	}

	normalized := normalizeMsgpackValue(raw)
	msg, ok := normalized.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("unexpected msgpack root type %T", normalized)
	}
	return msg, nil
}

func normalizeMsgpackValue(v any) any {
	switch x := v.(type) {
	case map[interface{}]interface{}:
		out := make(map[string]any, len(x))
		for k, value := range x {
			out[fmt.Sprint(k)] = normalizeMsgpackValue(value)
		}
		return out
	case map[string]interface{}:
		out := make(map[string]any, len(x))
		for k, value := range x {
			out[k] = normalizeMsgpackValue(value)
		}
		return out
	case []interface{}:
		out := make([]any, len(x))
		for i, value := range x {
			out[i] = normalizeMsgpackValue(value)
		}
		return out
	case []byte:
		return string(x)
	default:
		return v
	}
}

func (c *WebsocketClient) dispatchEnvelope(env *Envelope, normalized []byte) error {
	channel := strings.ReplaceAll(env.Channel, ":", "/")

	c.Mu.RLock()
	sub, ok := c.Subscriptions[channel]
	c.Mu.RUnlock()
	if !ok {
		return nil
	}

	for _, dispatcher := range sub.dispatchers {
		if err := dispatcher(env); err != nil {
			return err
		}
	}
	if sub.rawHandler != nil {
		sub.rawHandler(normalized)
	}
	return nil
}

func copyStringPointer(src *string) *string {
	if src == nil {
		return nil
	}
	s := *src
	return &s
}

func asString(v interface{}) string {
	s, _ := v.(string)
	return s
}

func asInt64(v interface{}) int64 {
	switch x := v.(type) {
	case nil:
		return 0
	case int:
		return int64(x)
	case int8:
		return int64(x)
	case int16:
		return int64(x)
	case int32:
		return int64(x)
	case int64:
		return x
	case uint:
		return int64(x)
	case uint8:
		return int64(x)
	case uint16:
		return int64(x)
	case uint32:
		return int64(x)
	case uint64:
		return int64(x)
	case float64:
		return int64(x)
	case json.Number:
		n, _ := x.Int64()
		return n
	default:
		return 0
	}
}

func (c *WebsocketClient) activeConn() websocketConn {
	if c.conn != nil {
		return c.conn
	}
	if c.Conn != nil {
		return c.Conn
	}
	return nil
}

func (c *WebsocketClient) buildURL() (string, error) {
	rawURL := c.URL
	if rawURL == "" {
		rawURL = c.config.URL
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	q := u.Query()
	if c.config.ReadOnly {
		q.Set("readonly", "true")
	}
	if c.config.Encoding != "" && c.config.Encoding != WSEncodingAuto {
		q.Set("encoding", string(c.config.Encoding))
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (c *WebsocketClient) sendPing() error {
	c.WriteMu.Lock()
	defer c.WriteMu.Unlock()

	conn := c.activeConn()
	if conn == nil {
		return nil
	}
	return conn.WriteControl(websocket.PingMessage, []byte("ping"), time.Now().Add(10*time.Second))
}
