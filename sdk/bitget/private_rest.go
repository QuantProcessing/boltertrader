package sdk

import (
	"context"
	"fmt"
)

func (c *Client) PlaceOrder(ctx context.Context, req *PlaceOrderRequest) (*PlaceOrderResponse, error) {
	var out responseEnvelope[PlaceOrderResponse]
	err := c.postPrivate(ctx, "/api/v3/trade/place-order", req, &out)
	if err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: place order failed: %s %s", out.Code, out.Msg)
	}
	return &out.Data, nil
}

func (c *Client) BatchPlaceOrders(ctx context.Context, req []PlaceOrderRequest) ([]PlaceOrderResponse, error) {
	var out responseEnvelope[[]PlaceOrderResponse]
	err := c.postPrivate(ctx, "/api/v3/trade/place-batch", req, &out)
	if err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: batch place orders failed: %s %s", out.Code, out.Msg)
	}
	return out.Data, nil
}

func (c *Client) CancelOrder(ctx context.Context, req *CancelOrderRequest) (*CancelOrderResponse, error) {
	var out responseEnvelope[CancelOrderResponse]
	err := c.postPrivate(ctx, "/api/v3/trade/cancel-order", req, &out)
	if err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: cancel order failed: %s %s", out.Code, out.Msg)
	}
	return &out.Data, nil
}

func (c *Client) BatchCancelOrders(ctx context.Context, req []CancelOrderRequest) ([]CancelOrderResponse, error) {
	var out responseEnvelope[[]CancelOrderResponse]
	err := c.postPrivate(ctx, "/api/v3/trade/cancel-batch", req, &out)
	if err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: batch cancel orders failed: %s %s", out.Code, out.Msg)
	}
	return out.Data, nil
}

func (c *Client) BatchModifyOrders(ctx context.Context, req []ModifyOrderRequest) ([]CancelOrderResponse, error) {
	var out responseEnvelope[[]CancelOrderResponse]
	err := c.postPrivate(ctx, "/api/v3/trade/batch-modify-order", req, &out)
	if err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: batch modify orders failed: %s %s", out.Code, out.Msg)
	}
	return out.Data, nil
}

func (c *Client) CancelAllOrders(ctx context.Context, req *CancelAllOrdersRequest) error {
	var out responseEnvelope[any]
	err := c.postPrivate(ctx, "/api/v3/trade/cancel-symbol-order", req, &out)
	if err != nil {
		return err
	}
	if out.Code != "00000" {
		return fmt.Errorf("bitget sdk: cancel all orders failed: %s %s", out.Code, out.Msg)
	}
	return nil
}

func (c *Client) ModifyOrder(ctx context.Context, req *ModifyOrderRequest) (*CancelOrderResponse, error) {
	var out responseEnvelope[CancelOrderResponse]
	err := c.postPrivate(ctx, "/api/v3/trade/modify-order", req, &out)
	if err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: modify order failed: %s %s", out.Code, out.Msg)
	}
	return &out.Data, nil
}

func (c *Client) GetOrder(ctx context.Context, category, symbol, orderID, clientOID string) (*OrderRecord, error) {
	var out responseEnvelope[OrderRecord]
	err := c.getPrivate(ctx, "/api/v3/trade/order-info", map[string]string{
		"category":  category,
		"symbol":    symbol,
		"orderId":   orderID,
		"clientOid": clientOID,
	}, &out)
	if err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: get order failed: %s %s", out.Code, out.Msg)
	}
	return &out.Data, nil
}

func (c *Client) GetOpenOrders(ctx context.Context, category, symbol string) ([]OrderRecord, error) {
	var out responseEnvelope[OrderList]
	err := c.getPrivate(ctx, "/api/v3/trade/unfilled-orders", map[string]string{
		"category": category,
		"symbol":   symbol,
	}, &out)
	if err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: get open orders failed: %s %s", out.Code, out.Msg)
	}
	return out.Data.List, nil
}

func (c *Client) GetOrderHistory(ctx context.Context, category, symbol string) ([]OrderRecord, error) {
	var out responseEnvelope[OrderList]
	err := c.getPrivate(ctx, "/api/v3/trade/history-orders", map[string]string{
		"category": category,
		"symbol":   symbol,
	}, &out)
	if err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: get order history failed: %s %s", out.Code, out.Msg)
	}
	return out.Data.List, nil
}

func (c *Client) GetFills(ctx context.Context, req GetFillsRequest) ([]FillRecord, error) {
	var out responseEnvelope[FillList]
	err := c.getPrivate(ctx, "/api/v3/trade/fills", map[string]string{
		"category":  req.Category,
		"orderId":   req.OrderID,
		"startTime": req.StartTime,
		"endTime":   req.EndTime,
		"limit":     req.Limit,
		"cursor":    req.Cursor,
	}, &out)
	if err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: get fills failed: %s %s", out.Code, out.Msg)
	}
	return out.Data.List, nil
}

func (c *Client) GetAccountAssets(ctx context.Context) (*AccountAssets, error) {
	var out responseEnvelope[AccountAssets]
	err := c.getPrivate(ctx, "/api/v3/account/assets", nil, &out)
	if err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: get account assets failed: %s %s", out.Code, out.Msg)
	}
	return &out.Data, nil
}

func (c *Client) GetAccountInfo(ctx context.Context) (*AccountInfo, error) {
	var out responseEnvelope[AccountInfo]
	err := c.getPrivate(ctx, "/api/v3/account/info", nil, &out)
	if err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: get account info failed: %s %s", out.Code, out.Msg)
	}
	return &out.Data, nil
}

func (c *Client) GetFundingAssets(ctx context.Context, coin string) ([]FundingAsset, error) {
	var out responseEnvelope[[]FundingAsset]
	err := c.getPrivate(ctx, "/api/v3/account/funding-assets", map[string]string{"coin": coin}, &out)
	if err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: get funding assets failed: %s %s", out.Code, out.Msg)
	}
	return out.Data, nil
}

func (c *Client) GetFinancialRecords(ctx context.Context, req FinancialRecordsRequest) (*FinancialRecords, error) {
	var out responseEnvelope[FinancialRecords]
	err := c.getPrivate(ctx, "/api/v3/account/financial-records", map[string]string{
		"category":  req.Category,
		"coin":      req.Coin,
		"type":      req.Type,
		"startTime": req.StartTime,
		"endTime":   req.EndTime,
		"limit":     req.Limit,
		"cursor":    req.Cursor,
	}, &out)
	if err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: get financial records failed: %s %s", out.Code, out.Msg)
	}
	return &out.Data, nil
}

func (c *Client) GetAccountFeeRate(ctx context.Context, category, symbol string) (*AccountFeeRate, error) {
	var out responseEnvelope[AccountFeeRate]
	err := c.getPrivate(ctx, "/api/v3/account/fee-rate", map[string]string{
		"category": category,
		"symbol":   symbol,
	}, &out)
	if err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: get account fee rate failed: %s %s", out.Code, out.Msg)
	}
	return &out.Data, nil
}

func (c *Client) GetSwitchStatus(ctx context.Context) (*SwitchStatus, error) {
	var out responseEnvelope[SwitchStatus]
	err := c.getPrivate(ctx, "/api/v3/account/switch-status", nil, &out)
	if err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: get switch status failed: %s %s", out.Code, out.Msg)
	}
	return &out.Data, nil
}

func (c *Client) GetMaxTransferable(ctx context.Context, coin string) (*MaxTransferable, error) {
	var out responseEnvelope[MaxTransferable]
	err := c.getPrivate(ctx, "/api/v3/account/max-transferable", map[string]string{"coin": coin}, &out)
	if err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: get max transferable failed: %s %s", out.Code, out.Msg)
	}
	return &out.Data, nil
}

func (c *Client) GetOpenInterestLimit(ctx context.Context, category, symbol string) (*OpenInterestLimit, error) {
	var out responseEnvelope[OpenInterestLimit]
	err := c.getPrivate(ctx, "/api/v3/account/open-interest-limit", map[string]string{
		"category": category,
		"symbol":   symbol,
	}, &out)
	if err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: get open interest limit failed: %s %s", out.Code, out.Msg)
	}
	return &out.Data, nil
}

func (c *Client) GetCurrentPositions(ctx context.Context, category, symbol string) ([]PositionRecord, error) {
	var out responseEnvelope[PositionList]
	err := c.getPrivate(ctx, "/api/v3/position/current-position", map[string]string{
		"category": category,
		"symbol":   symbol,
	}, &out)
	if err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: get positions failed: %s %s", out.Code, out.Msg)
	}
	return out.Data.List, nil
}

func (c *Client) SetHoldMode(ctx context.Context, holdMode string) error {
	var out responseEnvelope[any]
	err := c.postPrivate(ctx, "/api/v3/account/set-hold-mode", map[string]string{"holdMode": holdMode}, &out)
	if err != nil {
		return err
	}
	if out.Code != "00000" {
		return fmt.Errorf("bitget sdk: set hold mode failed: %s %s", out.Code, out.Msg)
	}
	return nil
}

func (c *Client) SetLeverage(ctx context.Context, req *SetLeverageRequest) error {
	var out responseEnvelope[any]
	err := c.postPrivate(ctx, "/api/v3/account/set-leverage", req, &out)
	if err != nil {
		return err
	}
	if out.Code != "00000" {
		return fmt.Errorf("bitget sdk: set leverage failed: %s %s", out.Code, out.Msg)
	}
	return nil
}
