package perp

import (
	"context"
	"fmt"
	"strings"
)

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
	Time          *int64 `json:"time,omitempty"`
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
	PositionSide     string
	Type             OrderType
	TimeInForce      TimeInForce
	Quantity         string
	Price            string
	NewClientOrderID string
	StopPrice        string
	ClosePosition    bool
	ActivationPrice  string
	CallbackRate     string
	WorkingType      string
	PriceProtect     bool
	ReduceOnly       bool
	NewOrderRespType string
	RecvWindow       *int64
}

func (c *Client) PlaceOrder(ctx context.Context, p PlaceOrderParams) (*OrderResponse, error) {
	if p.ClosePosition && (p.Quantity != "" || p.ReduceOnly) {
		return nil, fmt.Errorf("aster perp place order: closePosition cannot be combined with quantity or reduceOnly")
	}
	if p.ReduceOnly && p.PositionSide != "" && !strings.EqualFold(p.PositionSide, "BOTH") {
		return nil, fmt.Errorf("aster perp place order: reduceOnly is not valid with hedge positionSide %q", p.PositionSide)
	}
	params := map[string]interface{}{
		"symbol": p.Symbol,
		"side":   p.Side,
		"type":   p.Type,
	}
	addPerpStringParam(params, "positionSide", p.PositionSide)
	if p.TimeInForce != "" {
		params["timeInForce"] = string(p.TimeInForce)
	}
	addPerpStringParam(params, "quantity", p.Quantity)
	addPerpStringParam(params, "price", p.Price)
	addPerpStringParam(params, "newClientOrderId", p.NewClientOrderID)
	addPerpStringParam(params, "stopPrice", p.StopPrice)
	if p.ClosePosition {
		params["closePosition"] = "true"
	}
	addPerpStringParam(params, "activationPrice", p.ActivationPrice)
	addPerpStringParam(params, "callbackRate", p.CallbackRate)
	addPerpStringParam(params, "workingType", p.WorkingType)
	if p.PriceProtect {
		params["priceProtect"] = "true"
	}
	if p.ReduceOnly {
		params["reduceOnly"] = "true"
	}
	addPerpStringParam(params, "newOrderRespType", p.NewOrderRespType)
	addPerpInt64Param(params, "recvWindow", p.RecvWindow)

	var response OrderResponse
	if err := c.Post(ctx, "/fapi/v3/order", params, true, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

type CancelOrderParams struct {
	Symbol            string
	OrderID           *int64
	OrigClientOrderID string
	RecvWindow        *int64
}

func (c *Client) CancelOrder(ctx context.Context, p CancelOrderParams) (*OrderResponse, error) {
	params, err := perpOrderIdentityParams(p.Symbol, p.OrderID, p.OrigClientOrderID, p.RecvWindow)
	if err != nil {
		return nil, fmt.Errorf("aster perp cancel order: %w", err)
	}
	var response OrderResponse
	if err := c.Delete(ctx, "/fapi/v3/order", params, true, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

type ModifyOrderParams struct {
	OrderID           *int64
	OrigClientOrderID string
	Symbol            string
	Quantity          string
	Price             string
	RecvWindow        *int64
}

func (c *Client) ModifyOrder(ctx context.Context, p ModifyOrderParams) (*OrderResponse, error) {
	params, err := perpOrderIdentityParams(p.Symbol, p.OrderID, p.OrigClientOrderID, p.RecvWindow)
	if err != nil {
		return nil, fmt.Errorf("aster perp modify order: %w", err)
	}
	if p.Quantity == "" || p.Price == "" {
		return nil, fmt.Errorf("aster perp modify order: quantity and price are required")
	}
	params["quantity"] = p.Quantity
	params["price"] = p.Price
	var response OrderResponse
	if err := c.Put(ctx, "/fapi/v3/order", params, true, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

type CancelAllOrdersParams struct {
	Symbol     string
	RecvWindow *int64
}

type CancelAllOrdersResponse struct {
	Code StringOrNumber `json:"code"`
	Msg  string         `json:"msg"`
}

func (c *Client) CancelAllOpenOrders(ctx context.Context, p CancelAllOrdersParams) (*CancelAllOrdersResponse, error) {
	params := map[string]interface{}{"symbol": p.Symbol}
	addPerpInt64Param(params, "recvWindow", p.RecvWindow)
	var response CancelAllOrdersResponse
	if err := c.Delete(ctx, "/fapi/v3/allOpenOrders", params, true, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

type OrderQuery struct {
	Symbol            string
	OrderID           *int64
	OrigClientOrderID string
	RecvWindow        *int64
}

func (c *Client) QueryOrder(ctx context.Context, query OrderQuery) (*OrderResponse, error) {
	params, err := perpOrderIdentityParams(query.Symbol, query.OrderID, query.OrigClientOrderID, query.RecvWindow)
	if err != nil {
		return nil, fmt.Errorf("aster perp query order: %w", err)
	}
	var response OrderResponse
	if err := c.Get(ctx, "/fapi/v3/order", params, true, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

type OpenOrdersQuery struct {
	Symbol     string
	RecvWindow *int64
}

func (c *Client) OpenOrders(ctx context.Context, query OpenOrdersQuery) ([]OrderResponse, error) {
	params := make(map[string]interface{})
	addPerpStringParam(params, "symbol", query.Symbol)
	addPerpInt64Param(params, "recvWindow", query.RecvWindow)
	var response []OrderResponse
	if err := c.Get(ctx, "/fapi/v3/openOrders", params, true, &response); err != nil {
		return nil, err
	}
	return response, nil
}

type AllOrdersQuery struct {
	Symbol     string
	OrderID    *int64
	StartTime  *int64
	EndTime    *int64
	Limit      *int
	RecvWindow *int64
}

func (c *Client) AllOrders(ctx context.Context, query AllOrdersQuery) ([]OrderResponse, error) {
	params := map[string]interface{}{"symbol": query.Symbol}
	addPerpInt64Param(params, "orderId", query.OrderID)
	addPerpInt64Param(params, "startTime", query.StartTime)
	addPerpInt64Param(params, "endTime", query.EndTime)
	addPerpIntParam(params, "limit", query.Limit)
	addPerpInt64Param(params, "recvWindow", query.RecvWindow)
	var response []OrderResponse
	if err := c.Get(ctx, "/fapi/v3/allOrders", params, true, &response); err != nil {
		return nil, err
	}
	return response, nil
}

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
	Buyer           bool   `json:"buyer"`
	Maker           bool   `json:"maker"`
	Side            string `json:"side"`
	PositionSide    string `json:"positionSide"`
	RealizedPnl     string `json:"realizedPnl"`
}

type UserTradesQuery struct {
	Symbol     string
	StartTime  *int64
	EndTime    *int64
	FromID     *int64
	Limit      *int
	RecvWindow *int64
}

func (c *Client) UserTrades(ctx context.Context, query UserTradesQuery) ([]Trade, error) {
	if query.FromID != nil && (query.StartTime != nil || query.EndTime != nil) {
		return nil, fmt.Errorf("aster perp user trades: fromId cannot be combined with startTime or endTime")
	}
	params := map[string]interface{}{"symbol": query.Symbol}
	addPerpInt64Param(params, "startTime", query.StartTime)
	addPerpInt64Param(params, "endTime", query.EndTime)
	addPerpInt64Param(params, "fromId", query.FromID)
	addPerpIntParam(params, "limit", query.Limit)
	addPerpInt64Param(params, "recvWindow", query.RecvWindow)
	var response []Trade
	if err := c.Get(ctx, "/fapi/v3/userTrades", params, true, &response); err != nil {
		return nil, err
	}
	return response, nil
}

func perpOrderIdentityParams(symbol string, orderID *int64, clientOrderID string, recvWindow *int64) (map[string]interface{}, error) {
	if orderID == nil && clientOrderID == "" {
		return nil, fmt.Errorf("orderId or origClientOrderId is required")
	}
	params := map[string]interface{}{"symbol": symbol}
	addPerpInt64Param(params, "orderId", orderID)
	addPerpStringParam(params, "origClientOrderId", clientOrderID)
	addPerpInt64Param(params, "recvWindow", recvWindow)
	return params, nil
}

func addPerpStringParam(params map[string]interface{}, key, value string) {
	if value != "" {
		params[key] = value
	}
}

func addPerpInt64Param(params map[string]interface{}, key string, value *int64) {
	if value != nil {
		params[key] = *value
	}
}

func addPerpIntParam(params map[string]interface{}, key string, value *int) {
	if value != nil {
		params[key] = *value
	}
}
