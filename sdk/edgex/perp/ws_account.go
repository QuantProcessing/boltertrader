package perp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

type EventType string

const (
	EventSnapshot          EventType = "Snapshot"
	EventAccountUpdate     EventType = "ACCOUNT_UPDATE"
	EventDepositUpdate     EventType = "DEPOSIT_UPDATE"
	EventWithdrawUpdate    EventType = "WITHDRAW_UPDATE"
	EventTransferInUpdate  EventType = "TRANSFER_IN_UPDATE"
	EventTransferOutUpdate EventType = "TRANSFER_OUT_UPDATE"
	EventOrderUpdate       EventType = "ORDER_UPDATE"
	EventOrderFillFee      EventType = "ORDER_FILL_FEE_INCOME"
	EventFundingSettle     EventType = "FUNDING_SETTLEMENT"
	EventForceWithdraw     EventType = "FORCE_WITHDRAW_UPDATE"
	EventForceTrade        EventType = "FORCE_TRADE_UPDATE"
	EventStartLiquidating  EventType = "START_LIQUIDATING"
	EventFinishLiquidating EventType = "FINISH_LIQUIDATING"
	EventUnrecognized      EventType = "UNRECOGNIZED"
)

type WSMessage struct {
	Type    string          `json:"type"`
	Content json.RawMessage `json:"content,omitempty"`
	Time    string          `json:"time,omitempty"`
}

type TradeEvent struct {
	Event   EventType        `json:"event"`
	Version int64            `json:"version"`
	Data    AccountEventData `json:"data"`
	Time    int64            `json:"time"`
	// AccountID int64            `json:"accountId"` // no sense
}

// AccountEventData 账户事件数据
type AccountEventData struct {
	Account               []AccountInfo           `json:"account"`
	Collateral            []Collateral            `json:"collateral"`
	CollateralTransaction []CollateralTransaction `json:"collateralTransaction"`
	Position              []PositionInfo          `json:"position"`
	PositionTransaction   []PositionTransaction   `json:"positionTransaction"`
	Deposit               []json.RawMessage       `json:"deposit"`
	Withdraw              []Withdraw              `json:"withdraw"`
	TransferIn            []TransferIn            `json:"transferIn"`
	TransferOut           []TransferOut           `json:"transferOut"`
	Order                 []Order                 `json:"order"`
	OrderFillTransaction  []OrderFillTransaction  `json:"orderFillTransaction"`
}

// EventHandler 事件处理函数
type EventHandler func(data AccountEventData)

// AccountState 内部状态管理
type AccountState struct {
	mu sync.RWMutex

	Accounts    map[string]AccountInfo  // Key: AccountID (usually only one for private WS, but keeping generic)
	Orders      map[string]Order        // Key: OrderId
	Positions   map[string]PositionInfo // Key: ContractId
	Collaterals map[string]Collateral   // Key: CoinId

	LastEventVersion int64 `json:"lastEventVersion"`
	LastUpdateTime   int64 `json:"lastUpdateTime"`
}

type WsAccountClient struct {
	BaseURL         string
	StarkPrivateKey string
	AccountID       string
	Conn            *websocket.Conn
	Logger          *zap.SugaredLogger

	mu sync.RWMutex

	ctx       context.Context
	isClosed  bool
	stopCh    chan struct{}
	connected atomic.Bool

	// account internal state
	state *AccountState

	eventHandlers map[EventType][]EventHandler
}

func NewWsAccountClient(ctx context.Context, starkPrivateKey, accountID string) *WsAccountClient {
	return &WsAccountClient{
		BaseURL:         WSBaseURL,
		StarkPrivateKey: starkPrivateKey,
		AccountID:       accountID,
		Logger:          zap.NewNop().Sugar().Named("edgex-account"),
		eventHandlers:   map[EventType][]EventHandler{},
		ctx:             ctx,
		state: &AccountState{
			Accounts:    make(map[string]AccountInfo),
			Orders:      make(map[string]Order),
			Positions:   make(map[string]PositionInfo),
			Collaterals: make(map[string]Collateral),
		},
	}
}

func (c *WsAccountClient) Subscribe(eventType EventType, callback EventHandler) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.eventHandlers[eventType] = append(c.eventHandlers[eventType], callback)
	c.Logger.Debugw("Subscribed to events", "event", eventType)
}

func (c *WsAccountClient) Unsubscribe(eventType EventType) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.eventHandlers, eventType)
	c.Logger.Debugw("Unsubscribed from events", "event", eventType)
}

func (c *WsAccountClient) Connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.Conn != nil {
		return nil
	}
	// Append accountId to URL
	url := fmt.Sprintf("%s/private/ws?accountId=%s", c.BaseURL, c.AccountID)

	headers := http.Header{}
	// Authentication for Private WS
	timestamp := time.Now().UnixMilli()
	path := "/api/v1/private/ws"

	// Include accountId in params for signature generation
	params := map[string]interface{}{
		"accountId": c.AccountID,
	}

	sig, err := GenerateSignature(c.StarkPrivateKey, timestamp, "GET", path, "", params)
	if err != nil {
		return err
	}

	headers.Set("X-edgeX-Api-Signature", sig)
	headers.Set("X-edgeX-Api-Timestamp", fmt.Sprintf("%d", timestamp))

	c.Logger.Infow("Connecting to EdgeX Account WS", "url", c.BaseURL)

	// Use internal 10 second timeout
	ctx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
	defer cancel()
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, url, headers)
	if err != nil {
		return err
	}

	c.Conn = conn
	c.stopCh = make(chan struct{})
	c.connected.Store(true)

	c.Logger.Infow("Connected to Private WS")

	// Start loops
	go c.readLoop()
	go c.pingLoop()

	return nil
}

func (c *WsAccountClient) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.stopCh != nil {
		close(c.stopCh)
		c.stopCh = nil
	}
	if c.Conn != nil {
		c.Conn.Close()
	}
	c.connected.Store(false)
}

func (c *WsAccountClient) readLoop() {
	defer func() {
		c.mu.Lock()
		c.Conn = nil
		isClosed := c.isClosed
		c.mu.Unlock()

		if !isClosed {
			go c.reconnect()
		}
	}()

	for {
		select {
		case <-c.ctx.Done():
			return
		default:
		}

		c.mu.RLock()
		conn := c.Conn
		c.mu.RUnlock()

		_, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				c.Logger.Errorw("websocket unexpected close error", "error", err)
			}
			return
		}

		c.handleMessage(message)
	}
}

func (c *WsAccountClient) handleMessage(message []byte) {
	c.Logger.Debugw("Received", "msg", string(message))

	var msgStruct WSMessage
	if err := json.Unmarshal(message, &msgStruct); err != nil {
		c.Logger.Errorw("Failed to unmarshal message structure", "error", err)
		return
	}

	switch msgStruct.Type {
	case "trade-event":
		c.handleTradeEvent(msgStruct.Content)
	case "ping":
		c.handlePing(msgStruct)
	case "pong":
		c.handlePong()
	case "error":
		var errContent map[string]interface{}
		if err := json.Unmarshal(msgStruct.Content, &errContent); err == nil {
			c.Logger.Errorw("Server error", "error", errContent)
		}
	case "connected":
		c.Logger.Debugw("Connected")
	default:
		c.Logger.Debugw("Received unknown message type", "type", msgStruct.Type)
	}
}

func (c *WsAccountClient) handleTradeEvent(rawContent json.RawMessage) {
	var content TradeEvent
	if err := json.Unmarshal(rawContent, &content); err != nil {
		c.Logger.Errorw("Failed to unmarshal trade event", "error", err)
		return
	}

	c.Logger.Debugw("Received trade event", "content", content)

	// update internal state
	c.updateState(&content)
	// trigger event handlers
	c.triggerEventHandler(content.Event, &content.Data)
}

func (c *WsAccountClient) triggerEventHandler(eventType EventType, data *AccountEventData) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// Direct subscription handlers
	if handlers, ok := c.eventHandlers[eventType]; ok {
		for _, handler := range handlers {
			handler(*data)
		}
	}
}

func (c *WsAccountClient) updateState(event *TradeEvent) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()

	c.state.LastEventVersion = event.Version
	c.state.LastUpdateTime = event.Time

	data := event.Data

	// Handle Snapshot: Clear and Rebuild
	if event.Event == EventSnapshot {
		// Clear existing
		c.state.Accounts = make(map[string]AccountInfo)
		c.state.Orders = make(map[string]Order)
		c.state.Positions = make(map[string]PositionInfo)
		c.state.Collaterals = make(map[string]Collateral)

		// Rebuild
		for _, v := range data.Account {
			c.state.Accounts[v.Id] = v
		}
		for _, v := range data.Order {
			c.state.Orders[v.Id] = v
		}
		for _, v := range data.Position {
			c.state.Positions[v.ContractId] = v
		}
		for _, v := range data.Collateral {
			c.state.Collaterals[v.CoinId] = v
		}
		return
	}

	// Handle Incremental Updates

	// 1. Account Info
	for _, v := range data.Account {
		c.state.Accounts[v.Id] = v
	}

	// 2. Orders
	// Filter for Open Orders only: PENDING, OPEN, UNTRIGGERED
	for _, v := range data.Order {
		switch v.Status {
		case OrderStatusPending, OrderStatusOpen, OrderStatusUntriggered:
			c.state.Orders[v.Id] = v
		default:
			// Remove from state as it's no longer an "Open Order"
			delete(c.state.Orders, v.Id)
		}
	}

	// 3. Positions
	// Remove if size is 0
	for _, v := range data.Position {
		// Simple string check for "0" or result of float parsing
		// Assuming "0" or "0.000..."
		isZero := false
		if v.OpenSize == "0" {
			isZero = true
		} else {
			// Try parsing to be safe
			val, err := strconv.ParseFloat(v.OpenSize, 64)
			if err == nil && val == 0 {
				isZero = true
			}
		}

		if isZero {
			delete(c.state.Positions, v.ContractId)
		} else {
			c.state.Positions[v.ContractId] = v
		}
	}

	// 4. Collateral
	for _, v := range data.Collateral {
		c.state.Collaterals[v.CoinId] = v
	}
}

func (c *WsAccountClient) handlePing(_ WSMessage) {
	c.Logger.Debugw("Received ping")
	pong := map[string]interface{}{
		"type": "pong",
		"time": fmt.Sprintf("%d", time.Now().UnixMilli()),
	}
	c.mu.Lock()
	if c.Conn != nil {
		c.Conn.WriteJSON(pong)
	}
	c.mu.Unlock()
}

func (c *WsAccountClient) handlePong() {
	c.Logger.Debugw("Received pong")
}

func (c *WsAccountClient) pingLoop() {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()

	for {
		c.mu.Lock()
		stopCh := c.stopCh
		c.mu.Unlock()

		if stopCh == nil {
			return
		}

		select {
		case <-stopCh:
			return
		case <-ticker.C:
			if !c.connected.Load() {
				continue
			}
			// Client ping (optional but good for keepalive)
			ping := map[string]interface{}{
				"type": "ping",
				"time": fmt.Sprintf("%d", time.Now().UnixMilli()),
			}
			c.mu.Lock()
			if c.Conn != nil {
				c.Conn.WriteJSON(ping)
			}
			c.mu.Unlock()
		}
	}
}

func (c *WsAccountClient) reconnect() {
	c.Logger.Infow("Connection lost. Reconnecting...")

	backoff := reconnectInterval
	for i := 0; i < maxReconnectAttempts; i++ {
		c.mu.Lock()
		stopCh := c.stopCh
		c.mu.Unlock()

		if stopCh == nil {
			return
		}

		// Reconnect logic
		err := c.Connect()
		if err == nil {
			c.Logger.Infow("Reconnected successfully")
			return
		}

		c.Logger.Errorw("Reconnection attempt failed", "attempt", i+1, "error", err)

		// Interruptible sleep
		select {
		case <-stopCh:
			return
		case <-time.After(backoff):
		}

		backoff *= 2
		if backoff > maxReconnectInterval {
			backoff = maxReconnectInterval
		}
	}

	c.Logger.Error("Max reconnection attempts reached. Giving up.")
}

func (c *WsAccountClient) SubscribeOrderUpdate(handler func(orders []Order)) {
	c.Subscribe(EventOrderUpdate, func(data AccountEventData) {
		orders := data.Order
		handler(orders)
	})
}

func (c *WsAccountClient) SubscribeOrderFillUpdate(handler func(fills []OrderFillTransaction)) {
	c.Subscribe(EventOrderFillFee, func(data AccountEventData) {
		fills := data.OrderFillTransaction
		if len(fills) > 0 {
			handler(fills)
		}
	})
}

func (c *WsAccountClient) SubscribePositionUpdate(handler func(positions []PositionInfo)) {
	c.Subscribe(EventAccountUpdate, func(data AccountEventData) {
		positions := data.Position
		handler(positions)
	})
	c.Subscribe(EventOrderUpdate, func(data AccountEventData) {
		positions := data.Position
		if len(positions) > 0 {
			handler(positions)
		}
	})
}

func (c *WsAccountClient) SubscribeBalanceUpdate(handler func(balance []Collateral)) {
	c.Subscribe(EventAccountUpdate, func(data AccountEventData) {
		balance := data.Collateral
		handler(balance)
	})
}
