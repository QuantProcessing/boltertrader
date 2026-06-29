package standx

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

// QuerySymbolInfo returns instrument details
// GET /api/query_symbol_info
func (c *Client) QuerySymbolInfo(ctx context.Context, symbol string) ([]SymbolInfo, error) {
	params := url.Values{}
	if symbol != "" {
		params.Set("symbol", symbol)
	}

	var resp []SymbolInfo
	err := c.DoPublic(ctx, http.MethodGet, "/api/query_symbol_info", params, &resp)
	return resp, err
}

// QuerySymbolMarket returns 24h market stats
// GET /api/query_symbol_market
func (c *Client) QuerySymbolMarket(ctx context.Context, symbol string) (SymbolMarket, error) {
	params := url.Values{}
	if symbol != "" {
		params.Set("symbol", symbol)
	}

	var resp SymbolMarket
	err := c.DoPublic(ctx, http.MethodGet, "/api/query_symbol_market", params, &resp)
	return resp, err
}

// QueryMarketOverview returns all current market overview rows.
// GET /api/query_market_overview
func (c *Client) QueryMarketOverview(ctx context.Context) (MarketOverview, error) {
	var resp MarketOverview
	err := c.DoPublic(ctx, http.MethodGet, "/api/query_market_overview", nil, &resp)
	return resp, err
}

// QueryDepthBook returns the order book
// GET /api/query_depth_book
func (c *Client) QueryDepthBook(ctx context.Context, symbol string, limit int) (DepthBook, error) {
	params := url.Values{}
	params.Set("symbol", symbol)

	// Note: API doc doesn't explicitly show limit param in example, but it's common.
	// However, looking at reference, Query Depth Book usually just takes symbol.
	// Reference check needed? Doc in Step 12 says "Required Parameters: symbol". No limit.

	var resp DepthBook
	err := c.DoPublic(ctx, http.MethodGet, "/api/query_depth_book", params, &resp)
	return resp, err
}

// QuerySymbolPrice returns detailed price info
// GET /api/query_symbol_price
func (c *Client) QuerySymbolPrice(ctx context.Context, symbol string) (SymbolPrice, error) {
	params := url.Values{}
	if symbol != "" {
		params.Set("symbol", symbol)
	}

	var resp SymbolPrice
	err := c.DoPublic(ctx, http.MethodGet, "/api/query_symbol_price", params, &resp)
	return resp, err
}

// QueryRecentTrades returns recent public trades
// GET /api/query_recent_trades
func (c *Client) QueryRecentTrades(ctx context.Context, symbol string, limit int) ([]RecentTrade, error) {
	params := url.Values{}
	if symbol != "" {
		params.Set("symbol", symbol)
	}
	if limit > 0 {
		params.Set("limit", strconv.Itoa(limit))
	}

	var resp []RecentTrade
	err := c.DoPublic(ctx, http.MethodGet, "/api/query_recent_trades", params, &resp)
	return resp, err
}

// QueryFundingRates returns funding rate history
// GET /api/query_funding_rates
func (c *Client) QueryFundingRates(ctx context.Context, symbol string, start, end int64) ([]FundingRate, error) {
	params := url.Values{}
	if symbol != "" {
		params.Set("symbol", symbol)
	}
	if start > 0 {
		params.Set("start_time", strconv.FormatInt(start, 10))
	}
	if end > 0 {
		params.Set("end_time", strconv.FormatInt(end, 10))
	}

	var resp []FundingRate
	err := c.DoPublic(ctx, http.MethodGet, "/api/query_funding_rates", params, &resp)
	return resp, err
}
