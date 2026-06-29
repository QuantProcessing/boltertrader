package margin

import (
	"context"
)

// PlaceOrder places a margin order
// Endpoint: POST /sapi/v1/margin/order
func (c *Client) PlaceOrder(ctx context.Context, params *PlaceOrderParams) (*OrderResponseFull, error) {
	p := map[string]interface{}{
		"symbol": params.Symbol,
		"side":   params.Side,
		"type":   params.Type,
	}

	if params.Quantity > 0 {
		p["quantity"] = params.Quantity
	}
	if params.QuoteOrderQty > 0 {
		p["quoteOrderQty"] = params.QuoteOrderQty
	}
	if params.Price > 0 {
		p["price"] = params.Price
	}
	if params.TimeInForce != "" {
		p["timeInForce"] = params.TimeInForce
	}
	if params.NewClientOrderID != "" {
		p["newClientOrderId"] = params.NewClientOrderID
	}
	if params.SideEffectType != "" {
		p["sideEffectType"] = params.SideEffectType
	}
	if params.IsIsolated {
		p["isIsolated"] = "TRUE"
	}

	// Always ask for FULL response for better debugging/info
	p["newOrderRespType"] = "FULL"

	var res OrderResponseFull
	err := c.Post(ctx, "/sapi/v1/margin/order", p, true, &res)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// GetOrder queries a margin order
// Endpoint: GET /sapi/v1/margin/order
func (c *Client) GetOrder(ctx context.Context, symbol string, orderID int64, origClientOrderID string, isIsolated bool) (*MarginOrder, error) {
	params := map[string]interface{}{
		"symbol": symbol,
	}
	if orderID > 0 {
		params["orderId"] = orderID
	}
	if origClientOrderID != "" {
		params["origClientOrderId"] = origClientOrderID
	}
	if isIsolated {
		params["isIsolated"] = "TRUE"
	}

	var res MarginOrder
	err := c.Get(ctx, "/sapi/v1/margin/order", params, true, &res)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

func (c *Client) GetOpenOrders(ctx context.Context, symbol string, isIsolated bool) ([]MarginOrder, error) {
	params := map[string]interface{}{}
	if symbol != "" {
		params["symbol"] = symbol
	}
	if isIsolated {
		params["isIsolated"] = "TRUE"
	}

	var res []MarginOrder
	err := c.Get(ctx, "/sapi/v1/margin/openOrders", params, true, &res)
	if err != nil {
		return nil, err
	}
	return res, nil
}

// CancelOrder excels a margin order
// Endpoint: DELETE /sapi/v1/margin/order
func (c *Client) CancelOrder(ctx context.Context, symbol string, orderID int64, origClientOrderID string, isIsolated bool) (*MarginOrder, error) {
	params := map[string]interface{}{
		"symbol": symbol,
	}
	if orderID > 0 {
		params["orderId"] = orderID
	}
	if origClientOrderID != "" {
		params["origClientOrderId"] = origClientOrderID
	}
	if isIsolated {
		params["isIsolated"] = "TRUE"
	}

	var res MarginOrder
	err := c.Delete(ctx, "/sapi/v1/margin/order", params, true, &res)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

func (c *Client) CancelAllOpenOrders(ctx context.Context, symbol string, isIsolated bool) ([]MarginOrder, error) {
	params := map[string]interface{}{
		"symbol": symbol,
	}
	if isIsolated {
		params["isIsolated"] = "TRUE"
	}

	var res []MarginOrder
	err := c.Delete(ctx, "/sapi/v1/margin/openOrders", params, true, &res)
	if err != nil {
		return nil, err
	}
	return res, nil
}

func (c *Client) MyTrades(ctx context.Context, symbol string, isIsolated bool, limit int, startTime int64, endTime int64, fromID int64) ([]Trade, error) {
	params := map[string]interface{}{
		"symbol": symbol,
	}
	if isIsolated {
		params["isIsolated"] = "TRUE"
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
	err := c.Get(ctx, "/sapi/v1/margin/myTrades", params, true, &res)
	if err != nil {
		return nil, err
	}
	return res, nil
}
