package spot

import (
	"context"
	"encoding/json"
	"fmt"
)

type OrderResponse struct {
	Symbol        string `json:"symbol"`
	OrderID       int64  `json:"orderId"`
	ClientOrderID string `json:"clientOrderId"`
	UpdateTime    *int64 `json:"updateTime,omitempty"`
	Time          *int64 `json:"time,omitempty"`
	Price         string `json:"price"`
	AvgPrice      string `json:"avgPrice"`
	OrigQty       string `json:"origQty"`
	CumQty        string `json:"cumQty"`
	ExecutedQty   string `json:"executedQty"`
	CumQuote      string `json:"cumQuote"`
	Status        string `json:"status"`
	TimeInForce   string `json:"timeInForce"`
	StopPrice     string `json:"stopPrice"`
	OrigType      string `json:"origType"`
	Type          string `json:"type"`
	Side          string `json:"side"`
}

type PlaceOrderParams struct {
	Symbol           string
	Side             string
	Type             string
	TimeInForce      string
	Quantity         string
	QuoteOrderQty    string
	Price            string
	NewClientOrderID string
	StopPrice        string
	RecvWindow       *int64
}

func (c *Client) PlaceOrder(ctx context.Context, p PlaceOrderParams) (*OrderResponse, error) {
	params := map[string]interface{}{
		"symbol": p.Symbol,
		"side":   p.Side,
		"type":   p.Type,
	}
	addStringParam(params, "timeInForce", p.TimeInForce)
	addStringParam(params, "quantity", p.Quantity)
	addStringParam(params, "quoteOrderQty", p.QuoteOrderQty)
	addStringParam(params, "price", p.Price)
	addStringParam(params, "newClientOrderId", p.NewClientOrderID)
	addStringParam(params, "stopPrice", p.StopPrice)
	addInt64Param(params, "recvWindow", p.RecvWindow)

	var response OrderResponse
	if err := c.Post(ctx, "/api/v3/order", params, true, &response); err != nil {
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
	params, err := orderIdentityParams(p.Symbol, p.OrderID, p.OrigClientOrderID, p.RecvWindow)
	if err != nil {
		return nil, fmt.Errorf("aster spot cancel order: %w", err)
	}
	var response OrderResponse
	if err := c.Delete(ctx, "/api/v3/order", params, true, &response); err != nil {
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
	params, err := orderIdentityParams(query.Symbol, query.OrderID, query.OrigClientOrderID, query.RecvWindow)
	if err != nil {
		return nil, fmt.Errorf("aster spot query order: %w", err)
	}
	var response OrderResponse
	if err := c.Get(ctx, "/api/v3/order", params, true, &response); err != nil {
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
	addStringParam(params, "symbol", query.Symbol)
	addInt64Param(params, "recvWindow", query.RecvWindow)
	var response []OrderResponse
	if err := c.Get(ctx, "/api/v3/openOrders", params, true, &response); err != nil {
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
	addInt64Param(params, "orderId", query.OrderID)
	addInt64Param(params, "startTime", query.StartTime)
	addInt64Param(params, "endTime", query.EndTime)
	addIntParam(params, "limit", query.Limit)
	addInt64Param(params, "recvWindow", query.RecvWindow)
	var response []OrderResponse
	if err := c.Get(ctx, "/api/v3/allOrders", params, true, &response); err != nil {
		return nil, err
	}
	return response, nil
}

type CancelAllOrdersParams struct {
	Symbol             string
	OrderIDs           []int64
	OrigClientOrderIDs []string
	RecvWindow         *int64
}

type CancelAllOrdersResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

func (c *Client) CancelAllOpenOrders(ctx context.Context, p CancelAllOrdersParams) (*CancelAllOrdersResponse, error) {
	params := map[string]interface{}{"symbol": p.Symbol}
	if len(p.OrderIDs) > 0 {
		value, err := json.Marshal(p.OrderIDs)
		if err != nil {
			return nil, fmt.Errorf("aster spot cancel all: encode order ids: %w", err)
		}
		params["orderIdList"] = string(value)
	}
	if len(p.OrigClientOrderIDs) > 0 {
		value, err := json.Marshal(p.OrigClientOrderIDs)
		if err != nil {
			return nil, fmt.Errorf("aster spot cancel all: encode client order ids: %w", err)
		}
		params["origClientOrderIdList"] = string(value)
	}
	addInt64Param(params, "recvWindow", p.RecvWindow)
	var response CancelAllOrdersResponse
	if err := c.Delete(ctx, "/api/v3/allOpenOrders", params, true, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

type Trade struct {
	Symbol          string `json:"symbol"`
	ID              int64  `json:"id"`
	OrderID         int64  `json:"orderId"`
	Side            string `json:"side"`
	Price           string `json:"price"`
	Qty             string `json:"qty"`
	QuoteQty        string `json:"quoteQty"`
	Commission      string `json:"commission"`
	CommissionAsset string `json:"commissionAsset"`
	Time            int64  `json:"time"`
	CounterpartyID  int64  `json:"counterpartyId"`
	CreateUpdateID  *int64 `json:"createUpdateId"`
	Maker           bool   `json:"maker"`
	Buyer           bool   `json:"buyer"`
}

type UserTradesQuery struct {
	Symbol     string
	OrderID    *int64
	StartTime  *int64
	EndTime    *int64
	FromID     *int64
	Limit      *int
	RecvWindow *int64
}

func (c *Client) UserTrades(ctx context.Context, query UserTradesQuery) ([]Trade, error) {
	if query.OrderID != nil && query.Symbol == "" {
		return nil, fmt.Errorf("aster spot user trades: symbol is required with orderId")
	}
	if query.FromID != nil && (query.StartTime != nil || query.EndTime != nil) {
		return nil, fmt.Errorf("aster spot user trades: fromId cannot be combined with startTime or endTime")
	}
	params := make(map[string]interface{})
	addStringParam(params, "symbol", query.Symbol)
	addInt64Param(params, "orderId", query.OrderID)
	addInt64Param(params, "startTime", query.StartTime)
	addInt64Param(params, "endTime", query.EndTime)
	addInt64Param(params, "fromId", query.FromID)
	addIntParam(params, "limit", query.Limit)
	addInt64Param(params, "recvWindow", query.RecvWindow)
	var response []Trade
	if err := c.Get(ctx, "/api/v3/userTrades", params, true, &response); err != nil {
		return nil, err
	}
	return response, nil
}

func orderIdentityParams(symbol string, orderID *int64, clientOrderID string, recvWindow *int64) (map[string]interface{}, error) {
	if orderID == nil && clientOrderID == "" {
		return nil, fmt.Errorf("orderId or origClientOrderId is required")
	}
	params := map[string]interface{}{"symbol": symbol}
	addInt64Param(params, "orderId", orderID)
	addStringParam(params, "origClientOrderId", clientOrderID)
	addInt64Param(params, "recvWindow", recvWindow)
	return params, nil
}

func addStringParam(params map[string]interface{}, key, value string) {
	if value != "" {
		params[key] = value
	}
}

func addInt64Param(params map[string]interface{}, key string, value *int64) {
	if value != nil {
		params[key] = *value
	}
}

func addIntParam(params map[string]interface{}, key string, value *int) {
	if value != nil {
		params[key] = *value
	}
}
