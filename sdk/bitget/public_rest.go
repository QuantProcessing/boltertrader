package sdk

import (
	"context"
	"fmt"
	"strconv"
)

func (c *Client) GetInstruments(ctx context.Context, category, symbol string) ([]Instrument, error) {
	var out responseEnvelope[[]Instrument]
	err := c.get(ctx, "/api/v3/market/instruments", map[string]string{
		"category": category,
		"symbol":   symbol,
	}, &out)
	if err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: get instruments failed: %s %s", out.Code, out.Msg)
	}
	return out.Data, nil
}

func (c *Client) GetTicker(ctx context.Context, category, symbol string) (*Ticker, error) {
	var out responseEnvelope[[]Ticker]
	err := c.get(ctx, "/api/v3/market/tickers", map[string]string{
		"category": category,
		"symbol":   symbol,
	}, &out)
	if err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: get ticker failed: %s %s", out.Code, out.Msg)
	}
	if len(out.Data) == 0 {
		return nil, fmt.Errorf("bitget sdk: ticker not found for %s %s", category, symbol)
	}
	return &out.Data[0], nil
}

func (c *Client) GetTickers(ctx context.Context, category string) ([]Ticker, error) {
	var out responseEnvelope[[]Ticker]
	err := c.get(ctx, "/api/v3/market/tickers", map[string]string{
		"category": category,
	}, &out)
	if err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: get tickers failed: %s %s", out.Code, out.Msg)
	}
	return out.Data, nil
}

func (c *Client) GetOrderBook(ctx context.Context, category, symbol string, limit int) (*OrderBook, error) {
	query := map[string]string{
		"category": category,
		"symbol":   symbol,
	}
	if limit > 0 {
		query["limit"] = strconv.Itoa(limit)
	}

	var out responseEnvelope[OrderBook]
	err := c.get(ctx, "/api/v3/market/orderbook", query, &out)
	if err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: get orderbook failed: %s %s", out.Code, out.Msg)
	}
	return &out.Data, nil
}

func (c *Client) GetRecentFills(ctx context.Context, category, symbol string, limit int) ([]PublicFill, error) {
	query := map[string]string{
		"category": category,
		"symbol":   symbol,
	}
	if limit > 0 {
		query["limit"] = strconv.Itoa(limit)
	}

	var out responseEnvelope[[]PublicFill]
	err := c.get(ctx, "/api/v3/market/fills", query, &out)
	if err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: get fills failed: %s %s", out.Code, out.Msg)
	}
	return out.Data, nil
}

// GetOpenInterest retrieves current open interest for a perp symbol.
// productType: "USDT-FUTURES" | "COIN-FUTURES" | "USDC-FUTURES"
// Docs: https://www.bitget.com/api-doc/contract/market/Get-Open-Interest
func (c *Client) GetOpenInterest(ctx context.Context, symbol, productType string) (*OpenInterest, error) {
	var out responseEnvelope[OpenInterest]
	err := c.get(ctx, "/api/v2/mix/market/open-interest", map[string]string{
		"symbol":      symbol,
		"productType": productType,
	}, &out)
	if err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: get open interest failed: %s %s", out.Code, out.Msg)
	}
	return &out.Data, nil
}

// GetHistoryFundRate retrieves historical funding rates.
// pageSize max 100; pageNo 1-based.
// Docs: https://www.bitget.com/api-doc/contract/market/Get-History-Funding-Rate
func (c *Client) GetHistoryFundRate(ctx context.Context, symbol, productType string, pageSize, pageNo int) ([]HistoryFundRateEntry, error) {
	query := map[string]string{
		"symbol":      symbol,
		"productType": productType,
	}
	if pageSize > 0 {
		query["pageSize"] = strconv.Itoa(pageSize)
	}
	if pageNo > 0 {
		query["pageNo"] = strconv.Itoa(pageNo)
	}

	var out responseEnvelope[[]HistoryFundRateEntry]
	err := c.get(ctx, "/api/v2/mix/market/history-fund-rate", query, &out)
	if err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: get history fund rate failed: %s %s", out.Code, out.Msg)
	}
	return out.Data, nil
}

// GetCurrentFundRate retrieves the current funding rate.
// productType: "USDT-FUTURES" | "COIN-FUTURES" | "USDC-FUTURES".
// Docs: https://www.bitget.com/api-doc/contract/market/Get-Current-Funding-Rate
func (c *Client) GetCurrentFundRate(ctx context.Context, symbol, productType string) ([]CurrentFundRateEntry, error) {
	query := map[string]string{
		"symbol":      symbol,
		"productType": productType,
	}
	var out responseEnvelope[[]CurrentFundRateEntry]
	err := c.get(ctx, "/api/v2/mix/market/current-fund-rate", query, &out)
	if err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: get current fund rate failed: %s %s", out.Code, out.Msg)
	}
	for i := range out.Data {
		out.Data[i].RequestTime = out.RequestTime
	}
	return out.Data, nil
}

func (c *Client) GetCandles(ctx context.Context, category, symbol, interval, candleType string, startTime, endTime int64, limit int) ([]Candle, error) {
	query := map[string]string{
		"category": category,
		"symbol":   symbol,
		"interval": interval,
	}
	if candleType != "" {
		query["type"] = candleType
	}
	if startTime > 0 {
		query["startTime"] = strconv.FormatInt(startTime, 10)
	}
	if endTime > 0 {
		query["endTime"] = strconv.FormatInt(endTime, 10)
	}
	if limit > 0 {
		query["limit"] = strconv.Itoa(limit)
	}

	var out responseEnvelope[[]Candle]
	err := c.get(ctx, "/api/v3/market/candles", query, &out)
	if err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: get candles failed: %s %s", out.Code, out.Msg)
	}
	return out.Data, nil
}
