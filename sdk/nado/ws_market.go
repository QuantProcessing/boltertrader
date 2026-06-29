package nado

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// WsMarketClient handles public market data subscriptions without authentication
// Read loop has 60s timeout since market data streams continuously
type WsMarketClient struct {
	url    string
	ctx    context.Context
	cancel context.CancelFunc

	mu          sync.Mutex
	writeMu     sync.Mutex
	conn        *websocket.Conn
	isConnected bool

	subscriptions map[string]*marketSubscription
	stopCh        chan struct{}

	loopsStarted   bool
	loopsDoneCh    chan struct{}
	loopsStartOnce sync.Once

	Logger *zap.SugaredLogger
}

type marketSubscription struct {
	params   StreamParams
	callback func([]byte)
}

func NewWsMarketClient(ctx context.Context) *WsMarketClient {
	c := &WsMarketClient{
		url:           WsSubscriptionsURL,
		subscriptions: make(map[string]*marketSubscription),
		Logger:        zap.NewNop().Sugar().Named("nado-market"),
	}
	c.ctx, c.cancel = context.WithCancel(ctx)
	return c
}

func (c *WsMarketClient) Connect() error {
	c.mu.Lock()

	// Wait for previous loops to exit
	if c.loopsDoneCh != nil {
		c.mu.Unlock()
		<-c.loopsDoneCh
		c.mu.Lock()
	}

	// Safely close old stopCh
	if c.stopCh != nil {
		select {
		case <-c.stopCh:
		default:
			close(c.stopCh)
		}
	}

	c.stopCh = make(chan struct{})
	c.loopsDoneCh = make(chan struct{})
	c.loopsStarted = false
	c.loopsStartOnce = sync.Once{}

	loopsDoneCh := c.loopsDoneCh
	c.mu.Unlock()

	// Connect with timeout
	connectCtx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
	defer cancel()
	if err := c.connect(connectCtx); err != nil {
		return err
	}

	// Start goroutines once per connection
	c.loopsStartOnce.Do(func() {
		var wg sync.WaitGroup
		wg.Add(2)

		go func() {
			defer wg.Done()
			c.pingLoop()
		}()
		go func() {
			defer wg.Done()
			c.readLoop()
		}()

		// Signal when all loops exit
		go func() {
			wg.Wait()
			close(loopsDoneCh)
		}()
	})

	// Restore subscriptions
	c.resubscribeAll()

	return nil
}

func (c *WsMarketClient) connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	conn, _, err := websocket.Dial(ctx, c.url, &websocket.DialOptions{
		CompressionMode: 1,
	})
	if err != nil {
		return err
	}
	c.conn = conn
	c.isConnected = true
	return nil
}

func (c *WsMarketClient) pingLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	c.mu.Lock()
	stopCh := c.stopCh
	c.mu.Unlock()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-stopCh:
			c.Logger.Debug("Ping loop exiting (connection lost)")
			return
		case <-ticker.C:
			if c.conn != nil {
				ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
				if err := c.conn.Ping(ctx); err != nil {
					c.Logger.Errorw("Ping error", "error", err)
				} else {
					c.Logger.Debug("Ping sent successfully")
				}
				cancel()
			}
		}
	}
}

func (c *WsMarketClient) readLoop() {
	defer func() {
		c.mu.Lock()
		if c.conn != nil {
			c.conn.Close(websocket.StatusNormalClosure, "")
			c.conn = nil
		}
		c.isConnected = false

		// Safely close stopCh
		if c.stopCh != nil {
			select {
			case <-c.stopCh:
			default:
				close(c.stopCh)
			}
			c.stopCh = nil
		}

		manualClose := c.ctx.Err() != nil
		c.mu.Unlock()

		if !manualClose {
			go c.reconnect()
		}
	}()

	for {
		select {
		case <-c.ctx.Done():
			return
		default:
			// Market data has 60s timeout (data streams continuously)
			ctx, cancel := context.WithTimeout(c.ctx, ReadTimeout)
			_, msg, err := c.conn.Read(ctx)
			cancel()

			if err != nil {
				// Context canceled is expected during normal shutdown
				if c.ctx.Err() != nil {
					c.Logger.Debug("Read loop stopping due to context cancellation")
					return
				}
				c.Logger.Errorw("Read error", "error", err)
				return
			}

			c.Logger.Debug("Received message", "msg", string(msg))
			c.handleMessage(msg)
		}
	}
}

func (c *WsMarketClient) reconnect() {
	c.Logger.Warn("Connection lost, attempting to reconnect...")

	backoff := time.Second
	const maxBackoff = 30 * time.Second

	attempt := 0
	for {
		select {
		case <-c.ctx.Done():
			c.Logger.Info("Reconnect cancelled due to context done")
			return
		case <-time.After(backoff):
			attempt++
			c.Logger.Infow("Reconnecting", "attempt", attempt, "backoff", backoff)
			if err := c.Connect(); err == nil {
				c.Logger.Infow("Reconnected successfully", "attempts", attempt)
				return
			} else {
				c.Logger.Warnw("Reconnect attempt failed", "attempt", attempt, "error", err)
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
			}
		}
	}
}

func (c *WsMarketClient) Close() {
	c.cancel()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		c.conn.Close(websocket.StatusNormalClosure, "")
		c.conn = nil
	}
}

func (c *WsMarketClient) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.isConnected
}

func (c *WsMarketClient) Subscribe(stream StreamParams, callback func([]byte)) error {
	c.mu.Lock()
	sub := &marketSubscription{
		params:   stream,
		callback: callback,
	}
	channel := stream.Type
	if stream.ProductId != nil {
		channel = fmt.Sprintf("%s:%d", channel, *stream.ProductId)
	}
	c.subscriptions[channel] = sub
	isConnected := c.isConnected
	c.mu.Unlock()

	if isConnected {
		return c.sendSubscribe(stream)
	}
	return nil
}

func (c *WsMarketClient) Unsubscribe(stream StreamParams) error {
	channel := stream.Type
	if stream.ProductId != nil {
		channel = fmt.Sprintf("%s:%d", channel, *stream.ProductId)
	}

	c.mu.Lock()
	delete(c.subscriptions, channel)
	c.mu.Unlock()

	req := SubscriptionRequest{
		Method: "unsubscribe",
		Stream: stream,
		Id:     time.Now().UnixNano(),
	}
	return c.writeJSON(req)
}

func (c *WsMarketClient) sendSubscribe(stream StreamParams) error {
	req := SubscriptionRequest{
		Method: "subscribe",
		Stream: stream,
		Id:     time.Now().UnixNano(),
	}
	return c.writeJSON(req)
}

func (c *WsMarketClient) resubscribeAll() {
	c.mu.Lock()
	if len(c.subscriptions) == 0 {
		c.mu.Unlock()
		return
	}

	var allParams []StreamParams
	for _, sub := range c.subscriptions {
		allParams = append(allParams, sub.params)
	}
	c.mu.Unlock()

	c.Logger.Infow("Restoring market subscriptions", "count", len(allParams))

	for _, p := range allParams {
		if err := c.sendSubscribe(p); err != nil {
			c.Logger.Errorw("Failed to restore market subscription",
				"type", p.Type,
				"error", err,
			)
		} else {
			c.Logger.Debugw("Restored market subscription", "type", p.Type)
		}
	}

	c.Logger.Info("Market subscription restoration completed")
}

func (c *WsMarketClient) writeJSON(v interface{}) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()

	if conn == nil {
		return fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
	defer cancel()
	return wsjson.Write(ctx, conn, v)
}

func (c *WsMarketClient) handleMessage(msg []byte) {
	var baseMsg struct {
		Type      *string `json:"type,omitempty"`
		ProductID *int64  `json:"product_id,omitempty"`
	}
	if err := json.Unmarshal(msg, &baseMsg); err != nil {
		return
	}

	if baseMsg.Type == nil {
		c.Logger.Debugw("Received message with no type", "msg", string(msg))
		return
	}

	channel := *baseMsg.Type
	if baseMsg.ProductID != nil {
		channel = fmt.Sprintf("%s:%d", channel, *baseMsg.ProductID)
	}

	c.mu.Lock()
	sub, ok := c.subscriptions[channel]
	c.mu.Unlock()

	if !ok {
		c.Logger.Warnw("Received message for unknown subscription", "channel", channel)
		return
	}

	// Call callback synchronously to preserve message order
	sub.callback(msg)
}

// SubscribeOrderBook subscribes to orderbook
func (c *WsMarketClient) SubscribeOrderBook(productId int64, callback func(*OrderBook)) error {
	params := StreamParams{
		Type:      "book_depth",
		ProductId: &productId,
	}
	return c.Subscribe(params, func(data []byte) {
		var res OrderBook
		if err := json.Unmarshal(data, &res); err != nil {
			c.Logger.Error("unmarshal orderbook error", zap.Error(err))
			return
		}
		if callback != nil {
			callback(&res)
		}
	})
}

func (c *WsMarketClient) SubscribeTrades(productId int64, callback func(*Trade)) error {
	params := StreamParams{
		Type:      "trade",
		ProductId: &productId,
	}
	return c.Subscribe(params, func(data []byte) {
		var res Trade
		if err := json.Unmarshal(data, &res); err != nil {
			c.Logger.Error("unmarshal trade error", zap.Error(err))
			return
		}
		if callback != nil {
			callback(&res)
		}
	})
}

func (c *WsMarketClient) SubscribeTicker(productId int64, callback func(*Ticker)) error {
	params := StreamParams{
		Type:      "best_bid_offer",
		ProductId: &productId,
	}
	return c.Subscribe(params, func(data []byte) {
		var res Ticker
		if err := json.Unmarshal(data, &res); err != nil {
			c.Logger.Error("unmarshal ticker error", zap.Error(err))
			return
		}
		if callback != nil {
			callback(&res)
		}
	})
}

func (c *WsMarketClient) SubscribeLiquidation(productId *int64, callback func(*Liquidation)) error {
	params := StreamParams{
		Type:      "liquidation",
		ProductId: productId,
	}
	return c.Subscribe(params, func(data []byte) {
		var res Liquidation
		if err := json.Unmarshal(data, &res); err != nil {
			c.Logger.Error("unmarshal liquidation error", zap.Error(err))
			return
		}
		if callback != nil {
			callback(&res)
		}
	})
}

func (c *WsMarketClient) SubscribeLatestCandlestick(productId int64, granularity int32, callback func(*Candlestick)) error {
	params := StreamParams{
		Type:        "latest_candlestick",
		ProductId:   &productId,
		Granularity: granularity,
	}
	return c.Subscribe(params, func(data []byte) {
		var res Candlestick
		if err := json.Unmarshal(data, &res); err != nil {
			c.Logger.Error("unmarshal candlestick error", zap.Error(err))
			return
		}
		if callback != nil {
			callback(&res)
		}
	})
}

func (c *WsMarketClient) SubscribeFundingPayment(productId int64, callback func(*FundingPayment)) error {
	params := StreamParams{
		Type:      "funding_payment",
		ProductId: &productId,
	}
	return c.Subscribe(params, func(data []byte) {
		var res FundingPayment
		if err := json.Unmarshal(data, &res); err != nil {
			c.Logger.Error("unmarshal funding_payment error", zap.Error(err))
			return
		}
		if callback != nil {
			callback(&res)
		}
	})
}

func (c *WsMarketClient) SubscribeFundingRate(productId *int64, callback func(*FundingRate)) error {
	params := StreamParams{
		Type:      "funding_rate",
		ProductId: productId,
	}
	return c.Subscribe(params, func(data []byte) {
		var res FundingRate
		if err := json.Unmarshal(data, &res); err != nil {
			c.Logger.Error("unmarshal funding_rate error", zap.Error(err))
			return
		}
		if callback != nil {
			callback(&res)
		}
	})
}

// Unsubscribe methods

func (c *WsMarketClient) UnsubscribeFundingRate(productId *int64) error {
	params := StreamParams{
		Type:      "funding_rate",
		ProductId: productId,
	}
	return c.Unsubscribe(params)
}

func (c *WsMarketClient) UnsubscribeOrderBook(productId int64) error {
	params := StreamParams{
		Type:      "book_depth",
		ProductId: &productId,
	}
	return c.Unsubscribe(params)
}

func (c *WsMarketClient) UnsubscribeTrades(productId int64) error {
	params := StreamParams{
		Type:      "trade",
		ProductId: &productId,
	}
	return c.Unsubscribe(params)
}

func (c *WsMarketClient) UnsubscribeTicker(productId int64) error {
	params := StreamParams{
		Type:      "best_bid_offer",
		ProductId: &productId,
	}
	return c.Unsubscribe(params)
}

func (c *WsMarketClient) UnsubscribeLatestCandlestick(productId int64, granularity int32) error {
	params := StreamParams{
		Type:        "latest_candlestick",
		ProductId:   &productId,
		Granularity: granularity,
	}
	return c.Unsubscribe(params)
}
