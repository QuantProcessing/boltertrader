package spot

import (
	"context"
	"encoding/json"
)

// Order Placement

type OrderResponse struct {
	Symbol              string `json:"symbol"`
	OrderID             int64  `json:"orderId"`
	OrderListID         int64  `json:"orderListId"`
	ClientOrderID       string `json:"clientOrderId"`
	TransactTime        int64  `json:"transactTime"`
	Price               string `json:"price"`
	OrigQty             string `json:"origQty"`
	ExecutedQty         string `json:"executedQty"`
	CummulativeQuoteQty string `json:"cummulativeQuoteQty"`
	Status              string `json:"status"`
	TimeInForce         string `json:"timeInForce"`
	Type                string `json:"type"`
	Side                string `json:"side"`
}

type PlaceOrderParams struct {
	Symbol           string
	Side             string
	Type             string
	TimeInForce      string // Optional
	Quantity         string
	Price            string // Optional
	NewClientOrderID string // Optional
	StopPrice        string // Optional
	IcebergQty       string // Optional
	NewOrderRespType string // Optional
}

func (c *Client) PlaceOrder(ctx context.Context, p PlaceOrderParams) (*OrderResponse, error) {
	params := map[string]interface{}{
		"symbol":   p.Symbol,
		"side":     p.Side,
		"type":     p.Type,
		"quantity": p.Quantity,
	}
	if p.TimeInForce != "" {
		params["timeInForce"] = p.TimeInForce
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
	if p.IcebergQty != "" {
		params["icebergQty"] = p.IcebergQty
	}
	if p.NewOrderRespType != "" {
		params["newOrderRespType"] = p.NewOrderRespType
	}

	var res OrderResponse
	err := c.Post(ctx, "/api/v1/order", params, true, &res)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// Cancel Order

type CancelOrderResponse struct {
	Symbol              string `json:"symbol"`
	OrigClientOrderID   string `json:"origClientOrderId"`
	OrderID             int64  `json:"orderId"`
	OrderListID         int64  `json:"orderListId"`
	ClientOrderID       string `json:"clientOrderId"`
	Price               string `json:"price"`
	OrigQty             string `json:"origQty"`
	ExecutedQty         string `json:"executedQty"`
	CummulativeQuoteQty string `json:"cummulativeQuoteQty"`
	Status              string `json:"status"`
	TimeInForce         string `json:"timeInForce"`
	Type                string `json:"type"`
	Side                string `json:"side"`
}

func (c *Client) CancelOrder(ctx context.Context, symbol string, orderID int64, origClientOrderID string) (*CancelOrderResponse, error) {
	params := map[string]interface{}{
		"symbol": symbol,
	}
	if orderID != 0 {
		params["orderId"] = orderID
	}
	if origClientOrderID != "" {
		params["origClientOrderId"] = origClientOrderID
	}

	var res CancelOrderResponse
	err := c.Delete(ctx, "/api/v1/order", params, true, &res)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// Modify Order (Cancel Replace)

type CancelReplaceOrderResponse struct {
	CancelResult     string
	NewOrderStatus   string
	CancelResponse   *CancelOrderResponse
	NewOrderResponse *OrderResponse
}

const cancelReplaceNewOrderKey = "new" + "Order" + "Result"

func (r *CancelReplaceOrderResponse) UnmarshalJSON(data []byte) error {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(data, &payload); err != nil {
		return err
	}

	if raw, ok := payload["cancelResult"]; ok {
		if err := json.Unmarshal(raw, &r.CancelResult); err != nil {
			return err
		}
	}
	if raw, ok := payload[cancelReplaceNewOrderKey]; ok {
		if err := json.Unmarshal(raw, &r.NewOrderStatus); err != nil {
			return err
		}
	}
	if raw, ok := payload["cancelResponse"]; ok {
		var cancelResponse CancelOrderResponse
		if err := json.Unmarshal(raw, &cancelResponse); err != nil {
			return err
		}
		r.CancelResponse = &cancelResponse
	}
	if raw, ok := payload["newOrderResponse"]; ok {
		var newOrderResponse OrderResponse
		if err := json.Unmarshal(raw, &newOrderResponse); err != nil {
			return err
		}
		r.NewOrderResponse = &newOrderResponse
	}

	return nil
}

func (r CancelReplaceOrderResponse) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{
		"cancelResult":           r.CancelResult,
		cancelReplaceNewOrderKey: r.NewOrderStatus,
		"cancelResponse":         r.CancelResponse,
		"newOrderResponse":       r.NewOrderResponse,
	})
}

type CancelReplaceOrderParams struct {
	Symbol                  string
	Side                    string
	Type                    string
	CancelReplaceMode       string // STOP_ON_FAILURE or ALLOW_FAILURE
	TimeInForce             string // Optional
	Quantity                string
	Price                   string // Optional
	CancelOrderID           int64  // Optional
	CancelOrigClientOrderID string // Optional
	NewClientOrderID        string // Optional
	StopPrice               string // Optional
	IcebergQty              string // Optional
	NewOrderRespType        string // Optional
}

func (c *Client) ModifyOrder(ctx context.Context, p CancelReplaceOrderParams) (*CancelReplaceOrderResponse, error) {
	params := map[string]interface{}{
		"symbol":            p.Symbol,
		"side":              p.Side,
		"type":              p.Type,
		"cancelReplaceMode": p.CancelReplaceMode,
		"quantity":          p.Quantity,
	}
	if p.TimeInForce != "" {
		params["timeInForce"] = p.TimeInForce
	}
	if p.Price != "" {
		params["price"] = p.Price
	}
	if p.CancelOrderID != 0 {
		params["cancelOrderId"] = p.CancelOrderID
	}
	if p.CancelOrigClientOrderID != "" {
		params["cancelOrigClientOrderId"] = p.CancelOrigClientOrderID
	}
	if p.NewClientOrderID != "" {
		params["newClientOrderId"] = p.NewClientOrderID
	}
	if p.StopPrice != "" {
		params["stopPrice"] = p.StopPrice
	}
	if p.IcebergQty != "" {
		params["icebergQty"] = p.IcebergQty
	}
	if p.NewOrderRespType != "" {
		params["newOrderRespType"] = p.NewOrderRespType
	}

	var res CancelReplaceOrderResponse
	err := c.Post(ctx, "/api/v1/order/cancelReplace", params, true, &res)
	if err != nil {
		return nil, err
	}
	return &res, nil
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
	err := c.Get(ctx, "/api/v1/order", params, true, &res)
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
	err := c.Get(ctx, "/api/v1/openOrders", params, true, &res)
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
	OrderListID     int64  `json:"orderListId"`
	Price           string `json:"price"`
	Qty             string `json:"qty"`
	QuoteQty        string `json:"quoteQty"`
	Commission      string `json:"commission"`
	CommissionAsset string `json:"commissionAsset"`
	Time            int64  `json:"time"`
	IsBuyer         bool   `json:"isBuyer"`
	IsMaker         bool   `json:"isMaker"`
	IsBestMatch     bool   `json:"isBestMatch"`
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
	err := c.Get(ctx, "/api/v1/myTrades", params, true, &res)
	if err != nil {
		return nil, err
	}
	return res, nil
}
