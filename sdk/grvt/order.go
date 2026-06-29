package grvt

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// Order Methods

func (c *Client) CreateOrder(ctx context.Context, req *CreateOrderRequest, instruments map[string]Instrument) (*CreateOrderResponse, error) {

	if req.Order.Signature.Nonce == 0 {
		req.Order.Signature.Nonce = uint32(time.Now().UnixNano())
	}
	if req.Order.Signature.Expiration == "" {
		req.Order.Signature.Expiration = strconv.FormatInt(time.Now().Add(1*time.Minute).UnixNano(), 10) // Default 1 min expiration
	}

	if err := SignOrder(&req.Order, c.PrivateKey, c.ChainID, instruments); err != nil {
		return nil, fmt.Errorf("failed to sign order: %w", err)
	}

	resp, err := c.Post(ctx, c.TradeDataURL+"/full/v1/create_order", req, true)
	if err != nil {
		return nil, err
	}

	var result CreateOrderResponse
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) GetOpenOrders(ctx context.Context, symbol string) ([]Order, error) {
	req := GetOpenOrdersRequest{SubAccountID: c.SubAccountID}
	if symbol != "" {
		req.Base = &[]string{symbol}
	}
	resp, err := c.Post(ctx, c.TradeDataURL+"/lite/v1/open_orders", req, true)
	if err != nil {
		return nil, err
	}

	var result GetOpenOrdersResponse
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, err
	}
	return result.Result, nil
}

func (c *Client) CancelOrder(ctx context.Context, orderID string) error {
	req := CancelOrderRequest{SubAccountID: c.SubAccountID, OrderID: &orderID}
	_, err := c.Post(ctx, c.TradeDataURL+"/full/v1/cancel_order", req, true)
	return err
}

func (c *Client) CancelAllOrders(ctx context.Context) error {
	req := CancelAllOrderRequest{SubAccountID: c.SubAccountID}
	_, err := c.Post(ctx, c.TradeDataURL+"/full/v1/cancel_all_orders", req, true)
	return err
}
