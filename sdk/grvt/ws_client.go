package grvt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

const (
	// Time allowed to write a message to the peer.
	WriteWait = 10 * time.Second

	// Time allowed to read the next pong message from the peer.
	PongWait = 60 * time.Second

	// Send pings to peer with this period. Must be less than PongWait.
	PingPeriod = (PongWait * 9) / 10

	ReconnectWait = 5 * time.Second
)

type WebsocketClient struct {
	client *Client

	URL     string
	Conn    *websocket.Conn
	stopCh  chan struct{}
	doneCh  chan struct{}
	Mu      sync.RWMutex
	WriteMu sync.Mutex

	Logger *zap.SugaredLogger
	Debug  bool

	auth bool
	// subs key format: "stream.selector"
	subs map[string]func([]byte) error

	// pending RPCs
	pendingRPCs map[int64]chan *WsRpcResponse
	rpcMu       sync.Mutex

	// Instruments cache for signing
	instruments   map[string]Instrument
	instrumentsMu sync.RWMutex

	nextSubID atomic.Int64 // auto-increment ID for subscribe/unsubscribe

	ctx context.Context
}

func NewMarketWebsocketClient(ctx context.Context, client *Client) *WebsocketClient {
	return &WebsocketClient{
		client: client,
		URL:    WssMarketURL,
		Logger: zap.NewNop().Sugar().Named("grvt-market"),
		auth:   false,
		subs:   make(map[string]func([]byte) error),
		stopCh: make(chan struct{}),
		ctx:    ctx,
	}
}

func NewAccountWebsocketClient(ctx context.Context, client *Client) *WebsocketClient {
	return &WebsocketClient{
		client: client,
		URL:    WssTradeURL,
		Logger: zap.NewNop().Sugar().Named("grvt-account"),
		auth:   true,
		subs:   make(map[string]func([]byte) error),
		stopCh: make(chan struct{}),
		ctx:    ctx,
	}
}

func NewAccountRpcWebsocketClient(ctx context.Context, client *Client) *WebsocketClient {
	return &WebsocketClient{
		client:      client,
		URL:         WssTradeRpcURL,
		Logger:      zap.NewNop().Sugar().Named("grvt-account-rpc"),
		auth:        true,
		subs:        make(map[string]func([]byte) error),
		pendingRPCs: make(map[int64]chan *WsRpcResponse),
		instruments: make(map[string]Instrument),
		stopCh:      make(chan struct{}),
		ctx:         ctx,
	}
}

func (c *WebsocketClient) Connect() error {
	c.Mu.Lock()
	defer c.Mu.Unlock()

	// Check if already connected
	if c.Conn != nil {
		return nil
	}
	// Check if stopCh is closed (restart) or initialize if needed
	select {
	case <-c.stopCh:
		c.stopCh = make(chan struct{})
	default:
		// open, do nothing
	}

	header := http.Header{}
	if c.auth {
		// login
		err := c.client.Login(c.ctx)
		if err != nil {
			return fmt.Errorf("auth failed: %w", err)
		}
		cookie := c.client.cookie
		if cookie == nil {
			return fmt.Errorf("auth failed: missing cookie")
		}
		header.Add("Cookie", cookie.String())
		if c.client.accountID != "" {
			header.Add("X-Grvt-Account-Id", c.client.accountID)
		}
	}

	// Connect (unlock during dial to avoid blocking)
	c.Logger.Infow("Connecting...", "endpoint", grvtWebsocketLogEndpoint(c.URL))
	// Use internal 10 second timeout
	ctx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
	defer cancel()
	conn, resp, err := websocket.DefaultDialer.DialContext(ctx, c.URL, header)
	if err != nil {
		if resp != nil {
			bodyBytes := 0
			if resp.Body != nil {
				body, _ := io.ReadAll(resp.Body)
				bodyBytes = len(body)
				_ = resp.Body.Close()
			}
			c.Logger.Errorw("Handshake failed", "status", resp.StatusCode, "response_bytes", bodyBytes)
			return fmt.Errorf(
				"failed to connect to websocket: %w (status: %s, response bytes: %d)",
				newGRVTRedactedError("websocket handshake rejected", err),
				grvtHTTPStatus(resp.StatusCode),
				bodyBytes,
			)
		}
		return fmt.Errorf("failed to connect to websocket: %w", newGRVTRedactedError("websocket transport failed", err))
	}

	// Another goroutine might have connected while we were dialing
	if c.Conn != nil {
		conn.Close()
		return nil
	}

	c.Conn = conn
	c.doneCh = make(chan struct{})

	c.Logger.Debug("Connected")

	// set initial read deadline
	conn.SetReadDeadline(time.Now().Add(PongWait))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(PongWait))
		return nil
	})

	// start loops
	go c.readLoop()
	go c.pingLoop()

	return nil
}

func (c *WebsocketClient) readLoop() {
	defer func() {
		c.Mu.Lock()
		if c.Conn != nil {
			c.Conn.Close()
			c.Conn = nil
		}
		close(c.doneCh)
		c.Mu.Unlock()

		if !c.isStopped() {
			go c.reconnect()
		}
	}()

	for {
		if c.isStopped() {
			return
		}

		c.Mu.RLock()
		conn := c.Conn
		c.Mu.RUnlock()

		if conn == nil {
			return
		}

		_, message, err := conn.ReadMessage()
		if err != nil {
			fields := grvtWebsocketReadErrorFields(err)
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				c.Logger.Errorw("websocket unexpected close error", fields...)
			} else if !c.isStopped() {
				// Log other errors if not manually stopped
				c.Logger.Errorw("websocket read error", fields...)
			}
			return
		}

		// Reset deadline on successful read
		conn.SetReadDeadline(time.Now().Add(PongWait))
		c.handleMessage(message)
	}
}

// handleRPCResponse handles inbound JSON-RPC responses
func (c *WebsocketClient) handleRPCResponse(message []byte) {
	var resp WsRpcResponse
	if err := json.Unmarshal(message, &resp); err != nil {
		c.Logger.Errorw("failed to unmarshal rpc response", "error", err)
		return
	}

	c.rpcMu.Lock()
	ch, ok := c.pendingRPCs[int64(resp.Id)]
	if ok {
		delete(c.pendingRPCs, int64(resp.Id))
	}
	c.rpcMu.Unlock()

	if ok {
		ch <- &resp
	} else {
		fields := []any{
			"id", resp.Id,
			"result_bytes", len(resp.Result),
			"has_error", resp.Error != nil,
		}
		if resp.Error != nil {
			fields = append(fields, "error_code", resp.Error.Code)
		}
		c.Logger.Warnw("received rpc response with no pending handler", fields...)
	}
}

// override handleMessage to route RPC responses
func (c *WebsocketClient) handleMessage(message []byte) {
	c.Logger.Debugw("Received message", "bytes", len(message))

	// Try to detect if it's an RPC response (id field present, result/error present, jsonrpc="2.0")
	// Or standard subscription update.
	// Standard sub update: {"s":..., "s1":..., ...} (short keys)
	// RPC response: {"jsonrpc":"2.0", "id":..., "result":...}

	// We can do a quick check for "jsonrpc" or "j" (Lite)
	msgStr := string(message)
	if strings.Contains(msgStr, "\"jsonrpc\"") || strings.Contains(msgStr, "\"j\"") {
		var rpcCheck struct {
			JsonRpcFull *string `json:"jsonrpc"`
			JsonRpcLite *string `json:"j"`
			Id          *int64  `json:"i"`
			IdFull      *int64  `json:"id"`
		}
		_ = json.Unmarshal(message, &rpcCheck)

		if (rpcCheck.JsonRpcFull != nil && *rpcCheck.JsonRpcFull == "2.0") ||
			(rpcCheck.JsonRpcLite != nil && *rpcCheck.JsonRpcLite == "2.0") {
			// Only route to RPC handler if there's a pending handler for this ID.
			// Subscribe responses also have "j":"2.0" but are NOT registered in pendingRPCs.
			rpcID := rpcCheck.Id
			if rpcID == nil {
				rpcID = rpcCheck.IdFull
			}
			if rpcID != nil {
				c.rpcMu.Lock()
				_, hasPending := c.pendingRPCs[*rpcID]
				c.rpcMu.Unlock()
				if hasPending {
					c.handleRPCResponse(message)
					return
				}
			}
			// Fall through — subscribe/unsubscribe responses handled below
		}
	}

	// Legacy/Lite handling
	var msgStruct struct {
		Stream   *string `json:"s,omitempty"`  // data response
		Selector *string `json:"s1,omitempty"` // data response
		Jsonrpc  *string `json:"j,omitempty"`  // subscribe response
		Method   *string `json:"m,omitempty"`  // subscribe response
		Id       *int    `json:"i,omitempty"`  // subscribe response
	}
	if err := json.Unmarshal(message, &msgStruct); err != nil {
		c.Logger.Errorw("failed to unmarshal message", "error", err)
		return
	}

	// skip subscribe response (Lite)
	if msgStruct.Jsonrpc != nil {
		method := ""
		if msgStruct.Method != nil {
			method = *msgStruct.Method
		}
		id := 0
		if msgStruct.Id != nil {
			id = *msgStruct.Id
		}
		c.Logger.Debugw("Received subscribe response", "method", method, "id", id, "bytes", len(message))
		return
	}

	if msgStruct.Stream == nil || msgStruct.Selector == nil {
		// Possibly unknown message
		return
	}

	channel := fmt.Sprintf("%s.%s", *msgStruct.Stream, *msgStruct.Selector)
	c.Mu.RLock()
	callback, ok := c.subs[channel]
	c.Mu.RUnlock()

	if !ok {
		// try to find partial match for wildcards if needed, or just log
		c.Logger.Debugw("no callback for stream", "stream", *msgStruct.Stream)
		return
	}
	go func() {
		if err := callback(message); err != nil {
			c.Logger.Errorw("callback failed", "error_type", fmt.Sprintf("%T", err))
		}
	}()
}

func (c *WebsocketClient) SendRPC(method string, params interface{}) (*WsRpcResponse, error) {
	// Use smaller ID (uint32 range) to avoid potential JSON parsing issues with massive ints
	id := uint32(time.Now().UnixNano() % 2147483647)
	req := WsLiteRpcRequest{
		JsonRpc: "2.0",
		Method:  method,
		Params:  params,
		Id:      id,
	}

	respCh := make(chan *WsRpcResponse, 1)
	c.rpcMu.Lock()
	if c.pendingRPCs == nil {
		c.pendingRPCs = make(map[int64]chan *WsRpcResponse)
	}
	c.pendingRPCs[int64(id)] = respCh
	c.rpcMu.Unlock()

	// Ensure cleanup if timeout
	defer func() {
		c.rpcMu.Lock()
		delete(c.pendingRPCs, int64(id))
		c.rpcMu.Unlock()
	}()

	c.WriteMu.Lock()
	if c.Conn == nil {
		c.WriteMu.Unlock()
		return nil, fmt.Errorf("websocket not connected")
	}
	bytes, _ := json.Marshal(req)
	c.Logger.Infow("Sending RPC", "method", method, "id", id, "request_bytes", len(bytes))
	if err := c.Conn.WriteMessage(websocket.TextMessage, bytes); err != nil {
		c.WriteMu.Unlock()
		return nil, fmt.Errorf("write json failed: %w", err)
	}
	c.WriteMu.Unlock()

	select {
	case resp := <-respCh:
		return resp, nil
	case <-time.After(10 * time.Second): // 10s timeout
		return nil, fmt.Errorf("rpc timeout")
	case <-c.ctx.Done():
		return nil, c.ctx.Err()
	}
}

func (c *WebsocketClient) Subscribe(stream, selector string, callback func([]byte) error) error {
	channel := fmt.Sprintf("%s.%s", stream, selector)
	c.Mu.Lock()
	c.subs[channel] = callback
	c.Mu.Unlock()

	// If not connected, Connect() will be called by user or lazy load?
	// Assuming user calls Connect() explicitly.
	// We can check connection here and write if connected.
	c.Mu.RLock()
	defer c.Mu.RUnlock()
	if c.Conn == nil {
		// if not connected, just return, the subscription is stored and will be sent on connect/reconnect
		return nil
	}

	return c.sendSubscribe(stream, selector)
}

func (c *WebsocketClient) sendSubscribe(stream, selector string) error {
	req := &WsRequest{
		JsonRpc: "2.0",
		Method:  "subscribe",
		Params: &WsRequestParams{
			Stream:    stream,
			Selectors: []string{selector},
		},
		Id: c.nextSubID.Add(1),
	}

	c.WriteMu.Lock()
	defer c.WriteMu.Unlock()
	if c.Conn == nil {
		return fmt.Errorf("websocket not connected")
	}

	c.Logger.Debugw("Sending subscribe request", "method", req.Method, "id", req.Id, "stream", stream)
	return c.Conn.WriteJSON(req)
}

func (c *WebsocketClient) Unsubscribe(stream, selector string) error {
	channel := fmt.Sprintf("%s.%s", stream, selector)
	c.Mu.Lock()
	delete(c.subs, channel)
	c.Mu.Unlock()

	c.Mu.RLock()
	defer c.Mu.RUnlock()
	if c.Conn == nil {
		return nil
	}

	req := &WsRequest{
		JsonRpc: "2.0",
		Method:  "unsubscribe",
		Params: &WsRequestParams{
			Stream:    stream,
			Selectors: []string{selector},
		},
		Id: c.nextSubID.Add(1),
	}

	c.WriteMu.Lock()
	defer c.WriteMu.Unlock()
	if c.Conn == nil {
		return fmt.Errorf("websocket not connected")
	}
	return c.Conn.WriteJSON(req)
}

func (c *WebsocketClient) reconnect() {
	// Exponential backoff or fixed wait loop
	attempt := 0
	for {
		if c.isStopped() {
			return
		}

		select {
		case <-c.stopCh:
			return
		case <-time.After(ReconnectWait):
		}
		c.Logger.Infow("Reconnecting...", "attempt", attempt+1)

		if err := c.Connect(); err != nil {
			c.Logger.Errorw("Reconnect failed", "error", err)
			attempt++
			continue
		}

		// Re-subscribe
		c.Mu.RLock()
		subs := make([]string, 0, len(c.subs))
		for ch := range c.subs {
			subs = append(subs, ch)
		}
		c.Mu.RUnlock()

		for _, ch := range subs {
			parts := strings.Split(ch, ".")
			if len(parts) >= 2 {
				// User feedback: selector is last part, stream is the rest (which may contain dots)
				selector := parts[len(parts)-1]
				stream := strings.Join(parts[:len(parts)-1], ".")
				if err := c.sendSubscribe(stream, selector); err != nil {
					c.Logger.Errorw("Failed to resubscribe", "stream", stream, "error", err)
				}
			}
		}

		return
	}
}

func grvtWebsocketLogEndpoint(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "configured"
	}
	return parsed.Scheme + "://" + parsed.Host
}

func grvtWebsocketReadErrorFields(err error) []any {
	fields := []any{"error_type", fmt.Sprintf("%T", err)}
	var closeErr *websocket.CloseError
	if errors.As(err, &closeErr) {
		fields = append(fields, "close_code", closeErr.Code)
	}
	return fields
}

func (c *WebsocketClient) Close() {
	close(c.stopCh)
	c.Mu.Lock()
	defer c.Mu.Unlock()

	if c.Conn != nil {
		c.Conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(1*time.Second))
		c.Conn.Close()
		c.Conn = nil
	}
}

func (c *WebsocketClient) isStopped() bool {
	select {
	case <-c.stopCh:
		return true
	default:
		return false
	}
}

func (c *WebsocketClient) pingLoop() {
	ticker := time.NewTicker(PingPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-c.stopCh:
			return
		case <-c.doneCh:
			return
		case <-ticker.C:
			c.WriteMu.Lock()
			if c.Conn != nil {
				if err := c.Conn.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(WriteWait)); err != nil {
					c.Logger.Errorw("ping failed", "error", err)
					c.Conn.Close() // Force reconnection on ping failure
					c.WriteMu.Unlock()
					return
				}
			}
			c.WriteMu.Unlock()
		}
	}
}
