package hyperliquid

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gorilla/websocket"
)

type WebsocketClient struct {
	URL     string
	Env     Environment
	Conn    *websocket.Conn
	Mu      sync.RWMutex
	WriteMu sync.Mutex
	// subscriptions maps channel -> stable subscription payload key -> handler.
	subscriptions map[string]map[string]func(WsMessage)
	// subscriptionPayloads maps channel -> stable subscription payload key -> original payload.
	subscriptionPayloads map[string]map[string]any
	ReconnectWait        time.Duration
	Debug                bool
	Logger               *zap.SugaredLogger

	PrivateKey  *ecdsa.PrivateKey
	Vault       string
	AccountAddr string

	LastNonce    atomic.Int64
	NextPostID   atomic.Int64
	PostChannels map[int64]chan PostResult

	PingInterval time.Duration

	ctx    context.Context
	cancel context.CancelFunc
}

func NewWebsocketClient(ctx context.Context) *WebsocketClient {
	env := EnvironmentMainnet
	wsURL := wsURLForEnvironment(env)

	// Create cancellable context from parent
	ctx, cancel := context.WithCancel(ctx)

	c := &WebsocketClient{
		URL:                  wsURL,
		Env:                  env,
		subscriptions:        make(map[string]map[string]func(WsMessage)),
		subscriptionPayloads: make(map[string]map[string]any),
		ReconnectWait:        1 * time.Second,
		Logger:               zap.NewNop().Sugar().Named("hyperliquid"),
		PostChannels:         make(map[int64]chan PostResult),
		PingInterval:         50 * time.Second,
		ctx:                  ctx,
		cancel:               cancel, // Added cancel func
	}

	// Register default handler for "post" responses
	c.Subscribe("post", nil, c.handlePostResponse)

	return c
}

func (c *WebsocketClient) WithEnvironment(env Environment) *WebsocketClient {
	c.Env = normalizeEnvironment(env)
	c.URL = wsURLForEnvironment(c.Env)
	return c
}

func (c *WebsocketClient) IsMainnet() bool {
	return normalizeEnvironment(c.Env) == EnvironmentMainnet
}

func (c *WebsocketClient) SignL1Action(action any, nonce int64) (SignatureResult, error) {
	return SignL1Action(c.PrivateKey, action, c.Vault, nonce, nil, c.IsMainnet())
}

func (c *WebsocketClient) WithCredentials(privateKey string, vault *string) *WebsocketClient {
	if privateKey != "" {
		pk, err := parsePrivateKey(privateKey)
		if err == nil {
			c.PrivateKey = pk
			if c.AccountAddr == "" {
				c.AccountAddr = crypto.PubkeyToAddress(c.PrivateKey.PublicKey).Hex()
			}
		} else if c.Logger != nil {
			c.Logger.Errorw("Invalid private key", "error", err)
		}
	}
	if vault != nil {
		c.Vault = *vault
	}
	return c
}

func (c *WebsocketClient) WithURL(u string) *WebsocketClient {
	c.URL = u
	return c
}

func (c *WebsocketClient) Connect() error {
	c.Mu.Lock()
	defer c.Mu.Unlock()

	if c.Conn != nil {
		return nil
	}

	// Use internal 10 second timeout for dialing
	ctx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
	defer cancel()
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, c.URL, nil)
	if err != nil {
		return err
	}

	// Set initial read deadline
	if err := conn.SetReadDeadline(time.Now().Add(60 * time.Second)); err != nil {
		conn.Close()
		return err
	}

	c.Conn = conn

	go c.readLoop()
	go c.pingLoop()

	return nil
}

func (c *WebsocketClient) readLoop() {
	defer func() {
		// If context is canceled, we are shutting down intentionally
		if c.ctx.Err() != nil {
			return
		}

		// Otherwise, it was an error/disconnect, clean up connection and reconnect
		c.Mu.Lock()
		if c.Conn != nil {
			c.Conn.Close()
			c.Conn = nil
		}
		c.Mu.Unlock()

		c.reconnect()
	}()

	for {
		select {
		case <-c.ctx.Done():
			c.Logger.Debug("Read loop stopping due to context cancellation")
			return
		default:
			_, message, err := c.Conn.ReadMessage()
			if err != nil {
				// Check if context was canceled (normal shutdown) during read
				if c.ctx.Err() != nil {
					c.Logger.Debug("Read loop stopping due to context cancellation")
					return
				}

				c.Logger.Debugw("websocket read error", "error", err)
				return
			}
			// Extend read deadline on any message received
			c.Conn.SetReadDeadline(time.Now().Add(60 * time.Second))
			c.handleMessage(message)
		}
	}
}

func (c *WebsocketClient) pingLoop() {
	ticker := time.NewTicker(c.PingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			c.WriteMu.Lock()
			if c.Conn != nil {
				// Hyperliquid requires JSON ping: {"method": "ping"}
				pingMsg := map[string]string{"method": "ping"}
				err := c.Conn.WriteJSON(pingMsg)
				if err != nil {
					c.Logger.Errorw("websocket ping error", "error", err)
					c.WriteMu.Unlock()
					return
				}
			}
			c.WriteMu.Unlock()
		}
	}
}

func (c *WebsocketClient) handleMessage(message []byte) {
	c.Logger.Debugw("received message", "msg", string(message))
	var msg WsMessage
	if err := json.Unmarshal(message, &msg); err != nil {
		c.Logger.Errorw("error unmarshaling message", "error", err)
		return
	}

	if msg.Channel == "subscriptionResponse" {
		return
	}

	if msg.Channel == "pong" {
		return
	}

	c.Mu.RLock()
	channelHandlers, ok := c.subscriptions[msg.Channel]
	handlers := make([]func(WsMessage), 0, len(channelHandlers))
	for _, handler := range channelHandlers {
		handlers = append(handlers, handler)
	}
	c.Mu.RUnlock()

	if ok {
		for _, handler := range handlers {
			if handler != nil {
				handler(msg)
			}
		}
	} else {
		c.Logger.Debugw("no handler for channel", "channel", msg.Channel)
	}
}

// Subscribe registers a handler for a specific channel name and optionally sends a subscription request.
func (c *WebsocketClient) Subscribe(channel string, subscription any, handler func(WsMessage)) error {
	key := subscriptionKey(subscription)
	c.Mu.Lock()
	if c.subscriptions[channel] == nil {
		c.subscriptions[channel] = make(map[string]func(WsMessage))
	}
	c.subscriptions[channel][key] = handler
	if subscription != nil {
		if c.subscriptionPayloads[channel] == nil {
			c.subscriptionPayloads[channel] = make(map[string]any)
		}
		c.subscriptionPayloads[channel][key] = subscription
	}
	c.Mu.Unlock()

	if subscription != nil {
		req := WsSubscribeRequest{
			Method:       "subscribe",
			Subscription: subscription,
		}
		return c.SendCommand(req)
	}
	return nil
}

// Unsubscribe removes a handler and optionally sends unsubscribe command
func (c *WebsocketClient) Unsubscribe(channel string, subscription any) error {
	key := subscriptionKey(subscription)
	c.Mu.Lock()
	if handlers := c.subscriptions[channel]; handlers != nil {
		delete(handlers, key)
		if len(handlers) == 0 {
			delete(c.subscriptions, channel)
		}
	}
	if payloads := c.subscriptionPayloads[channel]; payloads != nil {
		delete(payloads, key)
		if len(payloads) == 0 {
			delete(c.subscriptionPayloads, channel)
		}
	}
	c.Mu.Unlock()

	if subscription != nil {
		req := WsSubscribeRequest{
			Method:       "unsubscribe",
			Subscription: subscription,
		}
		return c.SendCommand(req)
	}
	return nil
}

// SendCommand sends a raw JSON command
func (c *WebsocketClient) SendCommand(cmd any) error {
	c.WriteMu.Lock()
	defer c.WriteMu.Unlock()

	if c.Conn == nil {
		return fmt.Errorf("websocket not connected")
	}

	c.Logger.Debugw("sending command", "cmd", cmd)
	return c.Conn.WriteJSON(cmd)
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
}

func (c *WebsocketClient) reconnect() {
	time.Sleep(c.ReconnectWait)
	c.Logger.Infow("reconnecting...")
	if err := c.Connect(); err != nil {
		c.Logger.Errorw("reconnect failed", "error", err)
		go c.reconnect()
	} else {
		c.resubscribeAll()
	}
}

func (c *WebsocketClient) resubscribeAll() {
	c.Mu.RLock()
	type replay struct {
		channel      string
		subscription any
	}
	var payloads []replay
	for channel, byKey := range c.subscriptionPayloads {
		for _, subscription := range byKey {
			payloads = append(payloads, replay{channel: channel, subscription: subscription})
		}
	}
	c.Mu.RUnlock()

	for _, payload := range payloads {
		c.Logger.Infow("resubscribing", "channel", payload.channel)
		req := WsSubscribeRequest{
			Method:       "subscribe",
			Subscription: payload.subscription,
		}
		if err := c.SendCommand(req); err != nil {
			c.Logger.Errorw("resubscribe failed", "channel", payload.channel, "error", err)
		}
	}
}

func subscriptionKey(subscription any) string {
	if subscription == nil {
		return "__default__"
	}
	data, err := json.Marshal(subscription)
	if err != nil {
		return fmt.Sprintf("%#v", subscription)
	}
	return string(data)
}

func (c *WebsocketClient) GetNextNonce() int64 {
	for {
		last := c.LastNonce.Load()
		candidate := time.Now().UnixMilli()

		if candidate <= last {
			candidate = last + 1
		}
		if c.LastNonce.CompareAndSwap(last, candidate) {
			return candidate
		}
	}
}

func (c *WebsocketClient) PostAction(action any, sig SignatureResult, nonce int64) (chan PostResult, error) {
	payload := map[string]any{
		"action":    action,
		"nonce":     nonce,
		"signature": sig,
	}
	if c.Vault != "" {
		if actionMap, ok := action.(map[string]any); ok {
			if actionMap["type"] == "usdClassTransfer" {
				actionMap["vaultAddress"] = c.Vault
			} else {
				payload["vaultAddress"] = nil
			}
		} else {
			payload["vaultAddress"] = c.Vault
		}
	}

	return c.PostRequest(WsPostRequestPayload{
		Type:    "action",
		Payload: payload,
	})
}

func (c *WebsocketClient) PostRequest(payload WsPostRequestPayload) (chan PostResult, error) {
	id := c.NextPostID.Add(1)
	ch := make(chan PostResult, 1)

	c.Mu.Lock()
	c.PostChannels[id] = ch
	c.Mu.Unlock()

	req := WsPostRequest{
		Method:  "post",
		ID:      id,
		Request: payload,
	}

	if err := c.SendCommand(req); err != nil {
		c.Mu.Lock()
		delete(c.PostChannels, id)
		c.Mu.Unlock()
		return nil, err
	}

	return ch, nil
}

func (c *WebsocketClient) handlePostResponse(msg WsMessage) {
	var data WsPostResponse
	if err := json.Unmarshal(msg.Data, &data); err != nil {
		c.Logger.Errorw("error unmarshaling post response", "error", err)
		return
	}

	c.Mu.Lock()
	ch, ok := c.PostChannels[data.ID]
	if ok {
		delete(c.PostChannels, data.ID)
	}
	c.Mu.Unlock()

	if ok {
		ch <- PostResult{Response: data.Response}
		close(ch)
	}
}
