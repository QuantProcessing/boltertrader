package standx

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
)

// QueryPositions returns user positions
func (c *Client) QueryPositions(ctx context.Context, symbol string) ([]Position, error) {
	params := url.Values{}
	if symbol != "" {
		params.Set("symbol", symbol)
	}

	var resp []Position
	endpoint := "/api/query_positions"
	if len(params) > 0 {
		endpoint += "?" + params.Encode()
	}

	err := c.DoPrivate(ctx, http.MethodGet, endpoint, nil, &resp, false, nil)
	if err != nil {
		return nil, err
	}

	// Filter out 0 positions
	var filtered []Position
	for _, p := range resp {
		// Parse Qty to float to check for non-zero
		// Assuming strconv is available or just string check if normalized
		// "0" or "0.0" or "0.00"
		q, _ := strconv.ParseFloat(p.Qty, 64)
		if q != 0 {
			filtered = append(filtered, p)
		}
	}

	return filtered, nil
}

// QueryBalances returns user balances
func (c *Client) QueryBalances(ctx context.Context) (*Balance, error) {
	var resp Balance
	err := c.DoPrivate(ctx, http.MethodGet, "/api/query_balance", nil, &resp, false, nil)
	return &resp, err
}

// QueryUserOrders returns active orders
func (c *Client) QueryUserOrders(ctx context.Context, symbol string) ([]Order, error) {
	params := url.Values{}
	if symbol != "" {
		params.Set("symbol", symbol)
	}

	endpoint := "/api/query_orders"
	if len(params) > 0 {
		endpoint += "?" + params.Encode()
	}

	var wrapper UserOrdersResponse
	err := c.DoPrivate(ctx, http.MethodGet, endpoint, nil, &wrapper, false, nil)
	if err != nil {
		return nil, err
	}

	var orders []Order
	if len(wrapper.Result) > 0 {
		if err := json.Unmarshal(wrapper.Result, &orders); err != nil {
			return nil, err
		}
	}
	return orders, nil
}

// QueryUserAllOpenOrders returns all open orders
// GET /api/query_open_orders
func (c *Client) QueryUserAllOpenOrders(ctx context.Context, symbol string) ([]Order, error) {
	params := url.Values{}
	if symbol != "" {
		params.Set("symbol", symbol)
	}

	endpoint := "/api/query_open_orders"
	if len(params) > 0 {
		endpoint += "?" + params.Encode()
	}

	var wrapper UserOrdersResponse
	err := c.DoPrivate(ctx, http.MethodGet, endpoint, nil, &wrapper, false, nil)
	if err != nil {
		return nil, err
	}

	var orders []Order
	if len(wrapper.Result) > 0 {
		if err := json.Unmarshal(wrapper.Result, &orders); err != nil {
			return nil, err
		}
	}
	return orders, nil
}

// QueryUserTrades returns user trades using /api/query_trades
func (c *Client) QueryUserTrades(ctx context.Context, symbol string, lastID int64, limit int) ([]Trade, error) {
	params := url.Values{}
	if symbol != "" {
		params.Set("symbol", symbol)
	}
	if lastID > 0 {
		params.Set("last_id", strconv.FormatInt(lastID, 10))
	}
	if limit > 0 {
		params.Set("limit", strconv.Itoa(limit))
	}

	endpoint := "/api/query_trades"
	if len(params) > 0 {
		endpoint += "?" + params.Encode()
	}

	var wrapper UserTradesResponse
	err := c.DoPrivate(ctx, http.MethodGet, endpoint, nil, &wrapper, false, nil)
	if err != nil {
		return nil, err
	}

	var trades []Trade
	if len(wrapper.Result) > 0 {
		if err := json.Unmarshal(wrapper.Result, &trades); err != nil {
			return nil, err
		}
	}
	return trades, nil
}
