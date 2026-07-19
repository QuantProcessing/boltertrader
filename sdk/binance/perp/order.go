package perp

import (
	"context"
	"encoding/json"
	"errors"
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
	Type             string
	TimeInForce      string // Optional
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

type AlgoOrderResponse struct {
	AlgoID                  int64  `json:"algoId"`
	ClientAlgoID            string `json:"clientAlgoId"`
	AlgoType                string `json:"algoType"`
	OrderType               string `json:"orderType"`
	Symbol                  string `json:"symbol"`
	Side                    string `json:"side"`
	PositionSide            string `json:"positionSide"`
	TimeInForce             string `json:"timeInForce"`
	Quantity                string `json:"quantity"`
	AlgoStatus              string `json:"algoStatus"`
	TriggerPrice            string `json:"triggerPrice"`
	Price                   string `json:"price"`
	WorkingType             string `json:"workingType"`
	PriceMatch              string `json:"priceMatch"`
	ClosePosition           bool   `json:"closePosition"`
	PriceProtect            bool   `json:"priceProtect"`
	ReduceOnly              bool   `json:"reduceOnly"`
	SelfTradePreventionMode string `json:"selfTradePreventionMode"`
	ActivatePrice           string `json:"activatePrice"`
	CallbackRate            string `json:"callbackRate"`
	CreateTime              int64  `json:"createTime"`
	UpdateTime              int64  `json:"updateTime"`
	TriggerTime             int64  `json:"triggerTime"`
	GoodTillDate            int64  `json:"goodTillDate"`
	ActualOrderID           string `json:"actualOrderId"`
}

type AlgoOrderCancelResponse struct {
	AlgoID       int64  `json:"algoId"`
	ClientAlgoID string `json:"clientAlgoId"`
	Code         string `json:"code"`
	Msg          string `json:"msg"`
}

type NewAlgoOrderParams struct {
	Symbol        string
	Side          string
	PositionSide  string
	Type          string
	AlgoType      string
	TimeInForce   string
	Quantity      string
	Price         string
	TriggerPrice  string
	WorkingType   string
	PriceMatch    string
	ClosePosition bool
	PriceProtect  bool
	ReduceOnly    bool
	ActivatePrice string
	CallbackRate  string
	ClientAlgoID  string
	GoodTillDate  int64
	RecvWindow    int64
}

type AlgoOrderLookupParams struct {
	AlgoID       int64
	ClientAlgoID string
	RecvWindow   int64
}

type QueryOpenAlgoOrdersParams struct {
	Symbol     string
	AlgoType   string
	AlgoID     int64
	RecvWindow int64
}

type QueryAllAlgoOrdersParams struct {
	Symbol     string
	AlgoID     int64
	StartTime  int64
	EndTime    int64
	Page       int
	Limit      int
	RecvWindow int64
}

type CancelAllOpenAlgoOrdersParams struct {
	Symbol     string
	RecvWindow int64
}

var errAlgoOrderIdentifierRequired = errors.New("either algoId or clientAlgoId must be sent")

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
		params["timeInForce"] = p.TimeInForce
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
	err := c.Post(ctx, c.apiV1("order"), params, true, &res)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

func (c *Client) NewAlgoOrder(ctx context.Context, p NewAlgoOrderParams) (*AlgoOrderResponse, error) {
	algoType := p.AlgoType
	if algoType == "" {
		algoType = "CONDITIONAL"
	}
	params := map[string]interface{}{
		"symbol":   p.Symbol,
		"side":     p.Side,
		"type":     p.Type,
		"algoType": algoType,
	}
	if p.PositionSide != "" {
		params["positionSide"] = p.PositionSide
	}
	if p.TimeInForce != "" {
		params["timeInForce"] = p.TimeInForce
	}
	if p.Quantity != "" {
		params["quantity"] = p.Quantity
	}
	if p.Price != "" {
		params["price"] = p.Price
	}
	if p.TriggerPrice != "" {
		params["triggerPrice"] = p.TriggerPrice
	}
	if p.WorkingType != "" {
		params["workingType"] = p.WorkingType
	}
	if p.PriceMatch != "" {
		params["priceMatch"] = p.PriceMatch
	}
	if p.ClosePosition {
		params["closePosition"] = "true"
	}
	if p.PriceProtect {
		params["priceProtect"] = "true"
	}
	if p.ReduceOnly {
		params["reduceOnly"] = "true"
	}
	if p.ActivatePrice != "" {
		params["activatePrice"] = p.ActivatePrice
	}
	if p.CallbackRate != "" {
		params["callbackRate"] = p.CallbackRate
	}
	if p.ClientAlgoID != "" {
		params["clientAlgoId"] = p.ClientAlgoID
	}
	if p.GoodTillDate > 0 {
		params["goodTillDate"] = p.GoodTillDate
	}
	if p.RecvWindow > 0 {
		params["recvWindow"] = p.RecvWindow
	}

	var res AlgoOrderResponse
	if err := c.Post(ctx, c.apiV1("algoOrder"), params, true, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

func (c *Client) CancelAlgoOrder(ctx context.Context, p AlgoOrderLookupParams) (*AlgoOrderCancelResponse, error) {
	if p.AlgoID == 0 && p.ClientAlgoID == "" {
		return nil, errAlgoOrderIdentifierRequired
	}
	params := map[string]interface{}{}
	if p.AlgoID != 0 {
		params["algoId"] = p.AlgoID
	}
	if p.ClientAlgoID != "" {
		params["clientAlgoId"] = p.ClientAlgoID
	}
	if p.RecvWindow > 0 {
		params["recvWindow"] = p.RecvWindow
	}

	var res AlgoOrderCancelResponse
	if err := c.Delete(ctx, c.apiV1("algoOrder"), params, true, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

func (c *Client) QueryAlgoOrder(ctx context.Context, p AlgoOrderLookupParams) (*AlgoOrderResponse, error) {
	if p.AlgoID == 0 && p.ClientAlgoID == "" {
		return nil, errAlgoOrderIdentifierRequired
	}
	params := map[string]interface{}{}
	if p.AlgoID != 0 {
		params["algoId"] = p.AlgoID
	}
	if p.ClientAlgoID != "" {
		params["clientAlgoId"] = p.ClientAlgoID
	}
	if p.RecvWindow > 0 {
		params["recvWindow"] = p.RecvWindow
	}

	var res AlgoOrderResponse
	if err := c.Get(ctx, c.apiV1("algoOrder"), params, true, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

func (c *Client) QueryOpenAlgoOrders(ctx context.Context, p QueryOpenAlgoOrdersParams) ([]AlgoOrderResponse, error) {
	params := map[string]interface{}{}
	if p.Symbol != "" {
		params["symbol"] = p.Symbol
	}
	if p.AlgoType != "" {
		params["algoType"] = p.AlgoType
	}
	if p.AlgoID != 0 {
		params["algoId"] = p.AlgoID
	}
	if p.RecvWindow > 0 {
		params["recvWindow"] = p.RecvWindow
	}

	var res []AlgoOrderResponse
	if err := c.Get(ctx, c.apiV1("openAlgoOrders"), params, true, &res); err != nil {
		return nil, err
	}
	return res, nil
}

func (c *Client) QueryAllAlgoOrders(ctx context.Context, p QueryAllAlgoOrdersParams) ([]AlgoOrderResponse, error) {
	params := map[string]interface{}{
		"symbol": p.Symbol,
	}
	if p.AlgoID != 0 {
		params["algoId"] = p.AlgoID
	}
	if p.StartTime > 0 {
		params["startTime"] = p.StartTime
	}
	if p.EndTime > 0 {
		params["endTime"] = p.EndTime
	}
	if p.Page > 0 {
		params["page"] = p.Page
	}
	if p.Limit > 0 {
		params["limit"] = p.Limit
	}
	if p.RecvWindow > 0 {
		params["recvWindow"] = p.RecvWindow
	}

	var res []AlgoOrderResponse
	if err := c.Get(ctx, c.apiV1("allAlgoOrders"), params, true, &res); err != nil {
		return nil, err
	}
	return res, nil
}

func (c *Client) CancelAllOpenAlgoOrders(ctx context.Context, p CancelAllOpenAlgoOrdersParams) (bool, error) {
	params := map[string]interface{}{
		"symbol": p.Symbol,
	}
	if p.RecvWindow > 0 {
		params["recvWindow"] = p.RecvWindow
	}
	var res struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := c.Delete(ctx, c.apiV1("algoOpenOrders"), params, true, &res); err != nil {
		return false, err
	}
	return res.Code == 200, nil
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
	err := c.Delete(ctx, c.apiV1("order"), params, true, &res)
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
	err := c.Put(ctx, c.apiV1("order"), params, true, &res)
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
	return c.Delete(ctx, c.apiV1("allOpenOrders"), params, true, &res)
}

type CancelMultipleOrdersParams struct {
	Symbol                string
	OrderIDList           []int64
	OrigClientOrderIDList []string
}

func (c *Client) CancelMultipleOrders(ctx context.Context, p CancelMultipleOrdersParams) ([]OrderResponse, error) {
	params := map[string]interface{}{
		"symbol": p.Symbol,
	}
	if len(p.OrderIDList) > 0 {
		orderIDs, err := json.Marshal(p.OrderIDList)
		if err != nil {
			return nil, err
		}
		params["orderIdList"] = string(orderIDs)
	}
	if len(p.OrigClientOrderIDList) > 0 {
		clientOrderIDs, err := json.Marshal(p.OrigClientOrderIDList)
		if err != nil {
			return nil, err
		}
		params["origClientOrderIdList"] = string(clientOrderIDs)
	}

	var res []OrderResponse
	err := c.Delete(ctx, c.apiV1("batchOrders"), params, true, &res)
	if err != nil {
		return nil, err
	}
	return res, nil
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
	err := c.Get(ctx, c.apiV1("order"), params, true, &res)
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
	err := c.Get(ctx, c.apiV1("openOrders"), params, true, &res)
	if err != nil {
		return nil, err
	}
	return res, nil
}

// All Orders

func (c *Client) AllOrders(ctx context.Context, symbol string, limit int, startTime, endTime int64, orderID int64) ([]OrderResponse, error) {
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
	if orderID > 0 {
		params["orderId"] = orderID
	}

	var res []OrderResponse
	err := c.Get(ctx, c.apiV1("allOrders"), params, true, &res)
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
	IsBuyer         bool   `json:"buyer"`
	IsMaker         bool   `json:"maker"`
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
	err := c.Get(ctx, c.apiV1("userTrades"), params, true, &res)
	if err != nil {
		return nil, err
	}
	return res, nil
}
