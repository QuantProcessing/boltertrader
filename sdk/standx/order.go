package standx

import (
	"context"
	"net/http"
)

// CreateOrder places a new order
// POST /api/new_order
func (c *Client) CreateOrder(ctx context.Context, req CreateOrderRequest, extraHeaders map[string]string) (*APIResponse, error) {
	var resp APIResponse
	// Authentication Required • Body Signature Required
	err := c.DoPrivate(ctx, http.MethodPost, "/api/new_order", req, &resp, true, extraHeaders)
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

// CancelOrder cancels an existing order
// POST /api/cancel_order
func (c *Client) CancelOrder(ctx context.Context, req CancelOrderRequest) (*APIResponse, error) {
	var resp APIResponse
	// Authentication Required • Body Signature Required
	err := c.DoPrivate(ctx, http.MethodPost, "/api/cancel_order", req, &resp, true, nil)
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

// CancelMultipleOrders cancels multiple orders
// POST /api/cancel_orders
func (c *Client) CancelMultipleOrders(ctx context.Context, req CancelOrdersRequest) ([]interface{}, error) {
	var resp []interface{}
	// Authentication Required • Body Signature Required
	// Response is array []
	err := c.DoPrivate(ctx, http.MethodPost, "/api/cancel_orders", req, &resp, true, nil)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// ChangeLeverage updates leverage for a symbol
// POST /api/change_leverage
func (c *Client) ChangeLeverage(ctx context.Context, req ChangeLeverageRequest) (*APIResponse, error) {
	var resp APIResponse
	// Authentication Required • Body Signature Required
	err := c.DoPrivate(ctx, http.MethodPost, "/api/change_leverage", req, &resp, true, nil)
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

// ChangeMarginMode updates margin mode for a symbol
// POST /api/change_margin_mode
func (c *Client) ChangeMarginMode(ctx context.Context, req ChangeMarginModeRequest) (*APIResponse, error) {
	var resp APIResponse
	// Authentication Required • Body Signature Required
	err := c.DoPrivate(ctx, http.MethodPost, "/api/change_margin_mode", req, &resp, true, nil)
	if err != nil {
		return nil, err
	}
	return &resp, nil
}
