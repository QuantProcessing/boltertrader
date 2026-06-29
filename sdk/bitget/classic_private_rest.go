package sdk

import (
	"context"
	"fmt"
	"strconv"
	"time"
)

func (c *Client) GetClassicSpotAssets(ctx context.Context, coin string) ([]ClassicSpotAsset, error) {
	query := map[string]string{}
	if coin != "" {
		query["coin"] = coin
	}
	var out classicResponseEnvelope[[]ClassicSpotAsset]
	if err := c.getPrivate(ctx, "/api/v2/spot/account/assets", query, &out); err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: get classic spot assets failed: %s %s", out.Code, out.errorMessage())
	}
	return out.Data, nil
}

func (c *Client) PlaceClassicSpotOrder(ctx context.Context, req *PlaceOrderRequest) (*PlaceOrderResponse, error) {
	body := map[string]string{
		"symbol":    req.Symbol,
		"size":      req.Qty,
		"side":      req.Side,
		"orderType": req.OrderType,
	}
	if req.Price != "" {
		body["price"] = req.Price
	}
	if req.TimeInForce != "" {
		body["force"] = req.TimeInForce
	}
	if req.ClientOID != "" {
		body["clientOid"] = req.ClientOID
	}
	var out classicResponseEnvelope[PlaceOrderResponse]
	if err := c.postPrivate(ctx, "/api/v2/spot/trade/place-order", body, &out); err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: place classic spot order failed: %s %s (symbol=%s side=%s type=%s size=%s)", out.Code, out.errorMessage(), req.Symbol, req.Side, req.OrderType, req.Qty)
	}
	return &out.Data, nil
}

func (c *Client) CancelClassicSpotOrder(ctx context.Context, symbol, orderID, clientOID string) (*CancelOrderResponse, error) {
	body := map[string]string{"symbol": symbol}
	if orderID != "" {
		body["orderId"] = orderID
	}
	if clientOID != "" {
		body["clientOid"] = clientOID
	}
	var out classicResponseEnvelope[CancelOrderResponse]
	if err := c.postPrivate(ctx, "/api/v2/spot/trade/cancel-order", body, &out); err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: cancel classic spot order failed: %s %s", out.Code, out.errorMessage())
	}
	return &out.Data, nil
}

func (c *Client) CancelAllClassicSpotOrders(ctx context.Context, symbol string) error {
	var out classicResponseEnvelope[map[string]string]
	if err := c.postPrivate(ctx, "/api/v2/spot/trade/cancel-symbol-order", map[string]string{"symbol": symbol}, &out); err != nil {
		return err
	}
	if out.Code != "00000" {
		return fmt.Errorf("bitget sdk: cancel all classic spot orders failed: %s %s", out.Code, out.errorMessage())
	}
	return nil
}

func (c *Client) GetClassicSpotOrder(ctx context.Context, orderID, clientOID string) (*ClassicSpotOrderRecord, error) {
	query := map[string]string{
		"orderId":   orderID,
		"clientOid": clientOID,
	}
	var out classicResponseEnvelope[[]ClassicSpotOrderRecord]
	if err := c.getPrivate(ctx, "/api/v2/spot/trade/orderInfo", query, &out); err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: get classic spot order failed: %s %s", out.Code, out.errorMessage())
	}
	if len(out.Data) == 0 {
		return nil, fmt.Errorf("bitget sdk: classic spot order not found")
	}
	return &out.Data[0], nil
}

func (c *Client) GetClassicSpotOpenOrders(ctx context.Context, symbol string) ([]ClassicSpotOrderRecord, error) {
	query := map[string]string{
		"symbol": symbol,
		"limit":  "100",
	}
	now := strconv.FormatInt(time.Now().UnixMilli(), 10)
	query["requestTime"] = now
	var out classicResponseEnvelope[[]ClassicSpotOrderRecord]
	if err := c.getPrivate(ctx, "/api/v2/spot/trade/unfilled-orders", query, &out); err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: get classic spot open orders failed: %s %s", out.Code, out.errorMessage())
	}
	return out.Data, nil
}

func (c *Client) GetClassicSpotOrderHistory(ctx context.Context, symbol string) ([]ClassicSpotOrderRecord, error) {
	query := map[string]string{
		"symbol": symbol,
		"limit":  "100",
	}
	now := time.Now().UnixMilli()
	query["endTime"] = strconv.FormatInt(now, 10)
	query["startTime"] = strconv.FormatInt(now-90*24*time.Hour.Milliseconds(), 10)
	var out classicResponseEnvelope[[]ClassicSpotOrderRecord]
	if err := c.getPrivate(ctx, "/api/v2/spot/trade/history-orders", query, &out); err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: get classic spot order history failed: %s %s", out.Code, out.errorMessage())
	}
	return out.Data, nil
}

func (c *Client) PlaceClassicMixOrder(ctx context.Context, req *PlaceOrderRequest, productType, marginCoin string) (*PlaceOrderResponse, error) {
	body := map[string]string{
		"symbol":      req.Symbol,
		"productType": productType,
		"marginMode":  req.MarginMode,
		"marginCoin":  marginCoin,
		"size":        req.Qty,
		"side":        req.Side,
		"orderType":   req.OrderType,
	}
	if req.Price != "" {
		body["price"] = req.Price
	}
	if req.TimeInForce != "" {
		body["force"] = req.TimeInForce
	}
	if req.ClientOID != "" {
		body["clientOid"] = req.ClientOID
	}
	if req.TradeSide != "" {
		body["tradeSide"] = req.TradeSide
	}
	if req.ReduceOnly != "" {
		body["reduceOnly"] = req.ReduceOnly
	}
	var out classicResponseEnvelope[PlaceOrderResponse]
	if err := c.postPrivate(ctx, "/api/v2/mix/order/place-order", body, &out); err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: place classic mix order failed: %s %s", out.Code, out.errorMessage())
	}
	return &out.Data, nil
}

func (c *Client) CancelClassicMixOrder(ctx context.Context, symbol, productType, marginCoin, orderID, clientOID string) (*CancelOrderResponse, error) {
	body := map[string]string{
		"symbol":      symbol,
		"productType": productType,
		"marginCoin":  marginCoin,
	}
	if orderID != "" {
		body["orderId"] = orderID
	}
	if clientOID != "" {
		body["clientOid"] = clientOID
	}
	var out classicResponseEnvelope[CancelOrderResponse]
	if err := c.postPrivate(ctx, "/api/v2/mix/order/cancel-order", body, &out); err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: cancel classic mix order failed: %s %s", out.Code, out.errorMessage())
	}
	return &out.Data, nil
}

func (c *Client) CancelAllClassicMixOrders(ctx context.Context, productType, symbol, marginCoin string) error {
	body := map[string]string{
		"productType": productType,
		"symbol":      symbol,
		"marginCoin":  marginCoin,
	}
	var out classicResponseEnvelope[map[string]any]
	if err := c.postPrivate(ctx, "/api/v2/mix/order/cancel-all-orders", body, &out); err != nil {
		return err
	}
	if out.Code != "00000" {
		return fmt.Errorf("bitget sdk: cancel all classic mix orders failed: %s %s", out.Code, out.errorMessage())
	}
	return nil
}

func (c *Client) ModifyClassicMixOrder(ctx context.Context, req *ModifyOrderRequest, productType, marginCoin string) (*CancelOrderResponse, error) {
	body := map[string]string{
		"symbol":      req.Symbol,
		"productType": productType,
		"marginCoin":  marginCoin,
	}
	if req.OrderID != "" {
		body["orderId"] = req.OrderID
	}
	if req.ClientOID != "" {
		body["clientOid"] = req.ClientOID
	}
	if req.NewQty != "" {
		body["newSize"] = req.NewQty
	}
	if req.NewPrice != "" {
		body["newPrice"] = req.NewPrice
	}
	if req.NewClientID != "" {
		body["newClientOid"] = req.NewClientID
	}
	var out classicResponseEnvelope[CancelOrderResponse]
	if err := c.postPrivate(ctx, "/api/v2/mix/order/modify-order", body, &out); err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: modify classic mix order failed: %s %s", out.Code, out.errorMessage())
	}
	return &out.Data, nil
}

func (c *Client) GetClassicMixOrder(ctx context.Context, symbol, productType, orderID, clientOID string) (*ClassicMixOrderRecord, error) {
	query := map[string]string{
		"symbol":      symbol,
		"productType": productType,
		"orderId":     orderID,
		"clientOid":   clientOID,
	}
	var out classicResponseEnvelope[ClassicMixOrderRecord]
	if err := c.getPrivate(ctx, "/api/v2/mix/order/detail", query, &out); err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: get classic mix order failed: %s %s", out.Code, out.errorMessage())
	}
	return &out.Data, nil
}

func (c *Client) GetClassicMixOpenOrders(ctx context.Context, productType, symbol string) ([]ClassicMixOrderRecord, error) {
	query := map[string]string{
		"productType": productType,
		"symbol":      symbol,
		"limit":       "100",
	}
	var out classicResponseEnvelope[ClassicMixOrderList]
	if err := c.getPrivate(ctx, "/api/v2/mix/order/orders-pending", query, &out); err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: get classic mix open orders failed: %s %s", out.Code, out.errorMessage())
	}
	return out.Data.EntrustedList, nil
}

func (c *Client) GetClassicMixOrderHistory(ctx context.Context, productType, symbol string) ([]ClassicMixOrderRecord, error) {
	query := map[string]string{
		"productType": productType,
		"symbol":      symbol,
		"limit":       "100",
	}
	now := time.Now().UnixMilli()
	query["endTime"] = strconv.FormatInt(now, 10)
	query["startTime"] = strconv.FormatInt(now-90*24*time.Hour.Milliseconds(), 10)
	var out classicResponseEnvelope[ClassicMixOrderList]
	if err := c.getPrivate(ctx, "/api/v2/mix/order/orders-history", query, &out); err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: get classic mix order history failed: %s %s", out.Code, out.errorMessage())
	}
	return out.Data.EntrustedList, nil
}

func (c *Client) GetClassicMixAccount(ctx context.Context, symbol, productType, marginCoin string) (*ClassicMixAccount, error) {
	var out classicResponseEnvelope[ClassicMixAccount]
	if err := c.getPrivate(ctx, "/api/v2/mix/account/account", map[string]string{
		"symbol":      symbol,
		"productType": productType,
		"marginCoin":  marginCoin,
	}, &out); err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: get classic mix account failed: %s %s", out.Code, out.errorMessage())
	}
	return &out.Data, nil
}

func (c *Client) GetClassicMixPositions(ctx context.Context, productType, marginCoin string) ([]ClassicMixPositionRecord, error) {
	var out classicResponseEnvelope[[]ClassicMixPositionRecord]
	if err := c.getPrivate(ctx, "/api/v2/mix/position/all-position", map[string]string{
		"productType": productType,
		"marginCoin":  marginCoin,
	}, &out); err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: get classic mix positions failed: %s %s", out.Code, out.errorMessage())
	}
	return out.Data, nil
}

func (c *Client) SetClassicMixLeverage(ctx context.Context, symbol, productType, marginCoin, leverage string) error {
	var out classicResponseEnvelope[map[string]any]
	if err := c.postPrivate(ctx, "/api/v2/mix/account/set-leverage", map[string]string{
		"symbol":      symbol,
		"productType": productType,
		"marginCoin":  marginCoin,
		"leverage":    leverage,
	}, &out); err != nil {
		return err
	}
	if out.Code != "00000" {
		return fmt.Errorf("bitget sdk: set classic mix leverage failed: %s %s", out.Code, out.errorMessage())
	}
	return nil
}
