package margin

import (
	"context"
	"fmt"
)

// GetAccount queries the cross margin account details
// Endpoint: GET /sapi/v1/margin/account
func (c *Client) GetAccount(ctx context.Context) (*MarginAccount, error) {
	var res MarginAccount
	err := c.Get(ctx, "/sapi/v1/margin/account", nil, true, &res)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// GetIsolatedAccount queries the isolated margin account details
// Endpoint: GET /sapi/v1/margin/isolated/account
// symbols: Optional, max 5 symbols, comma separated (e.g. "BTCUSDT,ETHUSDT")
func (c *Client) GetIsolatedAccount(ctx context.Context, symbols string) (*IsolatedMarginAccount, error) {
	params := map[string]interface{}{}
	if symbols != "" {
		params["symbols"] = symbols
	}

	var res IsolatedMarginAccount
	err := c.Get(ctx, "/sapi/v1/margin/isolated/account", params, true, &res)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// Borrow borrows an asset from the margin account
// Endpoint: POST /sapi/v1/margin/loan
// isIsolated: "TRUE" or "FALSE" (default "FALSE" if empty)
// symbol: mandatory if isIsolated is "TRUE"
func (c *Client) Borrow(ctx context.Context, asset string, amount float64, isIsolated bool, symbol string) (int64, error) {
	params := map[string]interface{}{
		"asset":  asset,
		"amount": amount,
	}
	if isIsolated {
		params["isIsolated"] = "TRUE"
		if symbol == "" {
			return 0, fmt.Errorf("symbol is required for isolated margin borrow")
		}
		params["symbol"] = symbol
	}

	var res TransactionResult
	err := c.Post(ctx, "/sapi/v1/margin/loan", params, true, &res)
	if err != nil {
		return 0, err
	}
	return res.TranId, nil
}

// Repay repays a loan for the margin account
// Endpoint: POST /sapi/v1/margin/repay
// isIsolated: "TRUE" or "FALSE" (default "FALSE" if empty)
// symbol: mandatory if isIsolated is "TRUE"
func (c *Client) Repay(ctx context.Context, asset string, amount float64, isIsolated bool, symbol string) (int64, error) {
	params := map[string]interface{}{
		"asset":  asset,
		"amount": amount,
	}
	if isIsolated {
		params["isIsolated"] = "TRUE"
		if symbol == "" {
			return 0, fmt.Errorf("symbol is required for isolated margin repay")
		}
		params["symbol"] = symbol
	}

	var res TransactionResult
	err := c.Post(ctx, "/sapi/v1/margin/repay", params, true, &res)
	if err != nil {
		return 0, err
	}
	return res.TranId, nil
}
