package perp

import (
	"context"
)

// Order Placement

type OrderResponse struct {
	ClientOrderID string `json:"clientOrderId"`
	CumQty        string `json:"cumQty"`
	CumQuote      string `json:"cumQuote"`
	ExecutedQty   string `json:"executedQty"`
	OrderID       int64  `json:"orderId"`
	AvgPrice      string `json:"avgPrice"`
	OrigQty       string `json:"origQty"`
	Price         string `json:"price"`
	ReduceOnly    bool   `json:"reduceOnly"`
	Side          string `json:"side"`
	PositionSide  string `json:"positionSide"`
	Status        string `json:"status"`
	StopPrice     string `json:"stopPrice"`
	ClosePosition bool   `json:"closePosition"`
	Symbol        string `json:"symbol"`
	TimeInForce   string `json:"timeInForce"`
	Type          string `json:"type"`
	OrigType      string `json:"origType"`
	ActivatePrice string `json:"activatePrice"`
	PriceRate     string `json:"priceRate"`
	UpdateTime    int64  `json:"updateTime"`
	WorkingType   string `json:"workingType"`
	PriceProtect  bool   `json:"priceProtect"`
}

type PlaceOrderParams struct {
	Symbol           string
	Side             string
	PositionSide     string // Optional, for Hedge Mode
	Type             OrderType
	TimeInForce      TimeInForce // Optional
	Quantity         string
	Price            string // Optional
	NewClientOrderID string // Optional
	StopPrice        string // Optional
	ClosePosition    bool   // Optional
	ActivationPrice  string // Optional
	CallbackRate     string // Optional
	WorkingType      string // Optional
	PriceProtect     bool   // Optional
	ReduceOnly       bool   // Optional
}

func (c *Client) PlaceOrder(ctx context.Context, p PlaceOrderParams) (*OrderResponse, error) {
	params := map[string]interface{}{
		"symbol": p.Symbol,
		"side":   p.Side,
		"type":   p.Type,
	}
	if p.PositionSide != "" {
		params["positionSide"] = p.PositionSide
	}
	if p.TimeInForce != "" {
		params["timeInForce"] = string(p.TimeInForce)
	}
	if p.Quantity != "" {
		params["quantity"] = p.Quantity
	}
	if p.Price != "" {
		params["price"] = p.Price
	}
	if p.NewClientOrderID != "" {
		params["newClientOrderId"] = p.NewClientOrderID
	}
	if p.StopPrice != "" {
		params["stopPrice"] = p.StopPrice
	}
	if p.ClosePosition {
		params["closePosition"] = "true"
	}
	if p.ActivationPrice != "" {
		params["activationPrice"] = p.ActivationPrice
	}
	if p.CallbackRate != "" {
		params["callbackRate"] = p.CallbackRate
	}
	if p.WorkingType != "" {
		params["workingType"] = p.WorkingType
	}
	if p.PriceProtect {
		params["priceProtect"] = "true"
	}
	if p.ReduceOnly {
		params["reduceOnly"] = "true"
	}

	var res OrderResponse
	err := c.Post(ctx, "/fapi/v1/order", params, true, &res)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// Cancel Order

type CancelOrderParams struct {
	Symbol            string
	OrderID           string
	OrigClientOrderID string
}

func (c *Client) CancelOrder(ctx context.Context, p CancelOrderParams) (*OrderResponse, error) {
	params := map[string]interface{}{
		"symbol": p.Symbol,
	}
	if p.OrderID != "" {
		params["orderId"] = p.OrderID
	}
	if p.OrigClientOrderID != "" {
		params["origClientOrderId"] = p.OrigClientOrderID
	}

	var res OrderResponse
	err := c.Delete(ctx, "/fapi/v1/order", params, true, &res)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// Modify Order

type ModifyOrderParams struct {
	OrderID           int64
	OrigClientOrderID string
	Symbol            string
	Side              string // BUY or SELL
	Quantity          string
	Price             string
	PriceMatch        string // NONE, OPPONENT, OPPONENT_5, OPPONENT_10, OPPONENT_20, QUEUE, QUEUE_5, QUEUE_10, QUEUE_20
}

func (c *Client) ModifyOrder(ctx context.Context, p ModifyOrderParams) (*OrderResponse, error) {
	params := map[string]interface{}{
		"symbol": p.Symbol,
		"side":   p.Side,
	}
	if p.OrderID != 0 {
		params["orderId"] = p.OrderID
	}
	if p.OrigClientOrderID != "" {
		params["origClientOrderId"] = p.OrigClientOrderID
	}
	if p.Quantity != "" {
		params["quantity"] = p.Quantity
	}
	if p.Price != "" {
		params["price"] = p.Price
	}
	if p.PriceMatch != "" {
		params["priceMatch"] = p.PriceMatch
	}

	var res OrderResponse
	err := c.Put(ctx, "/fapi/v1/order", params, true, &res)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// Cancel All Open Orders

type CancelAllOrdersParams struct {
	Symbol string
}

func (c *Client) CancelAllOpenOrders(ctx context.Context, p CancelAllOrdersParams) error {
	params := map[string]interface{}{
		"symbol": p.Symbol,
	}
	var res struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	// Note: Response might be 200 OK with msg "success" or list of orders.
	// Binance API docs say it returns generic response or list.
	// We'll just check for error.
	return c.Delete(ctx, "/fapi/v1/allOpenOrders", params, true, &res)
}

// Get Order

func (c *Client) GetOrder(ctx context.Context, symbol string, orderID int64, origClientOrderID string) (*OrderResponse, error) {
	params := map[string]interface{}{
		"symbol": symbol,
	}
	if orderID != 0 {
		params["orderId"] = orderID
	}
	if origClientOrderID != "" {
		params["origClientOrderId"] = origClientOrderID
	}

	var res OrderResponse
	err := c.Get(ctx, "/fapi/v1/order", params, true, &res)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// Open Orders

func (c *Client) GetOpenOrders(ctx context.Context, symbol string) ([]OrderResponse, error) {
	params := map[string]interface{}{}
	if symbol != "" {
		params["symbol"] = symbol
	}

	var res []OrderResponse
	err := c.Get(ctx, "/fapi/v1/openOrders", params, true, &res)
	if err != nil {
		return nil, err
	}
	return res, nil
}

// Trade History

type Trade struct {
	Symbol          string `json:"symbol"`
	ID              int64  `json:"id"`
	OrderID         int64  `json:"orderId"`
	Price           string `json:"price"`
	Qty             string `json:"qty"`
	QuoteQty        string `json:"quoteQty"`
	Commission      string `json:"commission"`
	CommissionAsset string `json:"commissionAsset"`
	Time            int64  `json:"time"`
	IsBuyer         bool   `json:"isBuyer"`
	IsMaker         bool   `json:"isMaker"`
	PositionSide    string `json:"positionSide"`
	RealizedPnl     string `json:"realizedPnl"`
}

func (c *Client) MyTrades(ctx context.Context, symbol string, limit int, startTime, endTime int64, fromID int64) ([]Trade, error) {
	params := map[string]interface{}{
		"symbol": symbol,
	}
	if limit > 0 {
		params["limit"] = limit
	}
	if startTime > 0 {
		params["startTime"] = startTime
	}
	if endTime > 0 {
		params["endTime"] = endTime
	}
	if fromID > 0 {
		params["fromId"] = fromID
	}

	var res []Trade
	err := c.Get(ctx, "/fapi/v1/userTrades", params, true, &res)
	if err != nil {
		return nil, err
	}
	return res, nil
}
