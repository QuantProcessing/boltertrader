package sdk

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

type TradeWSClient struct {
	url       string
	apiKey    string
	secretKey string

	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.RWMutex
	writeMu sync.Mutex
	conn    *websocket.Conn
	closed  bool
	authCh  chan error

	pendingMu       sync.Mutex
	pendingRequests map[string]chan []byte
	requestTimeout  time.Duration
	reqSeq          atomic.Uint64
}

func NewTradeWSClient() *TradeWSClient {
	ctx, cancel := context.WithCancel(context.Background())
	return &TradeWSClient{
		url:             "wss://stream.bybit.com/v5/trade",
		ctx:             ctx,
		cancel:          cancel,
		pendingRequests: make(map[string]chan []byte),
		requestTimeout:  10 * time.Second,
	}
}

func (c *TradeWSClient) WithCredentials(apiKey, secretKey string) *TradeWSClient {
	c.apiKey = apiKey
	c.secretKey = secretKey
	return c
}

func (c *TradeWSClient) PlaceOrder(ctx context.Context, req PlaceOrderRequest) error {
	return c.sendTradeOp(ctx, "order.create", req)
}

func (c *TradeWSClient) BatchPlaceOrders(ctx context.Context, req BatchPlaceOrdersRequest) error {
	return c.sendTradeOp(ctx, "order.create-batch", req)
}

func (c *TradeWSClient) CancelOrder(ctx context.Context, req CancelOrderRequest) error {
	return c.sendTradeOp(ctx, "order.cancel", req)
}

func (c *TradeWSClient) BatchCancelOrders(ctx context.Context, req BatchCancelOrdersRequest) error {
	return c.sendTradeOp(ctx, "order.cancel-batch", req)
}

func (c *TradeWSClient) AmendOrder(ctx context.Context, req AmendOrderRequest) error {
	return c.sendTradeOp(ctx, "order.amend", req)
}

func (c *TradeWSClient) BatchAmendOrders(ctx context.Context, req BatchAmendOrdersRequest) error {
	return c.sendTradeOp(ctx, "order.amend-batch", req)
}

func (c *TradeWSClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.closed = true
	if c.cancel != nil {
		c.cancel()
	}
	if c.conn == nil {
		return nil
	}
	_ = c.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(5*time.Second))
	err := c.conn.Close()
	c.conn = nil
	return err
}

type tradeRequest struct {
	ReqID  string            `json:"reqId,omitempty"`
	Header map[string]string `json:"header,omitempty"`
	Op     string            `json:"op"`
	Args   []any             `json:"args"`
}

type tradeResponse struct {
	ReqID   string `json:"reqId"`
	RetCode int    `json:"retCode"`
	RetMsg  string `json:"retMsg"`
	Op      string `json:"op"`
}

func (c *TradeWSClient) Connect(ctx context.Context) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return fmt.Errorf("bybit trade ws: client closed")
	}
	if c.conn != nil {
		c.mu.Unlock()
		return nil
	}

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, c.url, nil)
	if err != nil {
		c.mu.Unlock()
		return err
	}

	c.conn = conn
	c.authCh = make(chan error, 1)
	go c.readLoop(conn)
	go c.pingLoop(conn)
	if err := c.sendAuthLocked(); err != nil {
		_ = conn.Close()
		c.conn = nil
		c.mu.Unlock()
		return err
	}
	authCh := c.authCh
	c.mu.Unlock()

	select {
	case err := <-authCh:
		return err
	case <-time.After(5 * time.Second):
		return fmt.Errorf("bybit trade ws: auth timeout")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *TradeWSClient) sendAuthLocked() error {
	expires := time.Now().Add(10 * time.Second).UnixMilli()
	signature := sign(c.secretKey, fmt.Sprintf("GET/realtime%d", expires))
	return c.writeJSONLocked(map[string]any{
		"op":   "auth",
		"args": []any{c.apiKey, expires, signature},
	})
}

func (c *TradeWSClient) sendTradeOp(ctx context.Context, op string, arg any) error {
	if err := c.Connect(ctx); err != nil {
		return err
	}

	reqID := fmt.Sprintf("req-%d", c.reqSeq.Add(1))
	req := tradeRequest{
		ReqID: reqID,
		Header: map[string]string{
			"X-BAPI-TIMESTAMP":   buildTimestamp(),
			"X-BAPI-RECV-WINDOW": defaultRecvWindow,
		},
		Op:   op,
		Args: []any{arg},
	}

	payload, err := c.sendRequest(ctx, reqID, req)
	if err != nil {
		return err
	}

	var resp tradeResponse
	if err := json.Unmarshal(payload, &resp); err != nil {
		return err
	}
	if resp.RetCode != 0 {
		return fmt.Errorf("bybit trade ws: %s failed: %d %s", op, resp.RetCode, resp.RetMsg)
	}
	return nil
}

func (c *TradeWSClient) sendRequest(ctx context.Context, reqID string, req tradeRequest) ([]byte, error) {
	ch := make(chan []byte, 1)

	c.pendingMu.Lock()
	c.pendingRequests[reqID] = ch
	c.pendingMu.Unlock()
	defer func() {
		c.pendingMu.Lock()
		delete(c.pendingRequests, reqID)
		c.pendingMu.Unlock()
	}()

	if err := c.writeJSON(req); err != nil {
		return nil, err
	}

	select {
	case payload := <-ch:
		if payload == nil {
			return nil, fmt.Errorf("bybit trade ws: connection closed")
		}
		return payload, nil
	case <-time.After(c.requestTimeout):
		return nil, fmt.Errorf("bybit trade ws: request timeout")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *TradeWSClient) writeJSON(v any) error {
	c.mu.RLock()
	conn := c.conn
	c.mu.RUnlock()
	if conn == nil {
		return fmt.Errorf("bybit trade ws: not connected")
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return conn.WriteJSON(v)
}

func (c *TradeWSClient) writeJSONLocked(v any) error {
	if c.conn == nil {
		return fmt.Errorf("bybit trade ws: not connected")
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.conn.WriteJSON(v)
}

func (c *TradeWSClient) pingLoop(conn *websocket.Conn) {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		c.mu.RLock()
		active := c.conn == conn
		c.mu.RUnlock()
		if !active {
			return
		}

		c.writeMu.Lock()
		err := conn.WriteJSON(map[string]string{"op": "ping"})
		c.writeMu.Unlock()
		if err != nil {
			return
		}
	}
}

func (c *TradeWSClient) readLoop(conn *websocket.Conn) {
	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			c.mu.RLock()
			authCh := c.authCh
			c.mu.RUnlock()
			if authCh != nil {
				select {
				case authCh <- err:
				default:
				}
			}
			c.pendingMu.Lock()
			for id, ch := range c.pendingRequests {
				close(ch)
				delete(c.pendingRequests, id)
			}
			c.pendingMu.Unlock()
			c.mu.Lock()
			if c.conn == conn {
				c.conn = nil
			}
			c.mu.Unlock()
			return
		}

		var resp tradeResponse
		if err := json.Unmarshal(payload, &resp); err != nil {
			continue
		}

		if resp.Op == "auth" {
			c.mu.RLock()
			authCh := c.authCh
			c.mu.RUnlock()
			if authCh != nil {
				if resp.RetCode == 0 {
					authCh <- nil
				} else {
					authCh <- fmt.Errorf("bybit trade ws: auth failed: %d %s", resp.RetCode, resp.RetMsg)
				}
			}
			continue
		}

		if resp.ReqID == "" {
			continue
		}

		c.pendingMu.Lock()
		ch := c.pendingRequests[resp.ReqID]
		c.pendingMu.Unlock()
		if ch != nil {
			ch <- payload
		}
	}
}
