package perp

import (
	"context"
	"fmt"
)

// Depth

type DepthResponse struct {
	LastUpdateID int64      `json:"lastUpdateId"`
	E            int64      `json:"E"`
	T            int64      `json:"T"`
	Bids         [][]string `json:"bids"`
	Asks         [][]string `json:"asks"`
}

func (c *Client) Depth(ctx context.Context, symbol string, limit int) (*DepthResponse, error) {
	params := map[string]interface{}{
		"symbol": symbol,
	}
	if limit > 0 {
		params["limit"] = limit
	}

	var res DepthResponse
	err := c.Get(ctx, c.apiV1("depth"), params, false, &res)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// Klines

type KlineResponse []interface{}

func (c *Client) Klines(ctx context.Context, symbol, interval string, limit int, startTime, endTime int64) ([]KlineResponse, error) {
	params := map[string]interface{}{
		"symbol":   symbol,
		"interval": interval,
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

	var res []KlineResponse
	err := c.Get(ctx, c.apiV1("klines"), params, false, &res)
	if err != nil {
		return nil, err
	}
	return res, nil
}

// ContinousKlines

func (c *Client) ContinousKlines(ctx context.Context, symbol, contractType string, interval string, limit int, startTime, endTime int64) ([]KlineResponse, error) {
	params := map[string]interface{}{
		"pair":         symbol,
		"contractType": contractType,
		"interval":     interval,
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

	var res []KlineResponse
	err := c.Get(ctx, c.apiV1("continuousKlines"), params, false, &res)
	if err != nil {
		return nil, err
	}
	return res, nil
}

// Ticker

type TickerResponse struct {
	Symbol             string `json:"symbol"`
	PriceChange        string `json:"priceChange"`
	PriceChangePercent string `json:"priceChangePercent"`
	WeightedAvgPrice   string `json:"weightedAvgPrice"`
	LastPrice          string `json:"lastPrice"`
	LastQty            string `json:"lastQty"`
	OpenPrice          string `json:"openPrice"`
	HighPrice          string `json:"highPrice"`
	LowPrice           string `json:"lowPrice"`
	Volume             string `json:"volume"`
	QuoteVolume        string `json:"quoteVolume"`
	OpenTime           int64  `json:"openTime"`
	CloseTime          int64  `json:"closeTime"`
	FirstId            int64  `json:"firstId"`
	LastId             int64  `json:"lastId"`
	Count              int64  `json:"count"`
}

func (c *Client) Ticker(ctx context.Context, symbol string) (*TickerResponse, error) {
	params := map[string]interface{}{}
	if symbol != "" {
		params["symbol"] = symbol
	}

	if symbol == "" {
		return nil, fmt.Errorf("symbol is required for Ticker")
	}

	var res TickerResponse
	err := c.Get(ctx, c.apiV1("ticker/24hr"), params, false, &res)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// Mark Price

type MarkPriceResponse struct {
	Symbol               string `json:"symbol"`
	MarkPrice            string `json:"markPrice"`
	IndexPrice           string `json:"indexPrice"`
	EstimatedSettlePrice string `json:"estimatedSettlePrice"`
	LastFundingRate      string `json:"lastFundingRate"`
	NextFundingTime      int64  `json:"nextFundingTime"`
}

func (c *Client) MarkPrice(ctx context.Context, symbol string) (*MarkPriceResponse, error) {
	params := map[string]interface{}{
		"symbol": symbol,
	}
	var res MarkPriceResponse
	err := c.Get(ctx, c.apiV1("premiumIndex"), params, false, &res)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// Exchange Info

func (c *Client) ExchangeInfo(ctx context.Context) (*ExchangeInfoResponse, error) {
	var res ExchangeInfoResponse
	err := c.Get(ctx, c.apiV1("exchangeInfo"), nil, false, &res)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// AggTrades

type AggTrade struct {
	ID           int64  `json:"a"`
	Price        string `json:"p"`
	Quantity     string `json:"q"`
	FirstTradeID int64  `json:"f"`
	LastTradeID  int64  `json:"l"`
	Timestamp    int64  `json:"T"`
	IsBuyerMaker bool   `json:"m"`
}

func (c *Client) GetAggTrades(ctx context.Context, symbol string, limit int) ([]AggTrade, error) {
	params := map[string]interface{}{
		"symbol": symbol,
	}
	if limit > 0 {
		params["limit"] = limit
	}

	var res []AggTrade
	err := c.Get(ctx, c.apiV1("aggTrades"), params, false, &res)
	if err != nil {
		return nil, err
	}
	return res, nil
}

// AggTradesQuery is the full parameter set for /fapi/v1/aggTrades.
type AggTradesQuery struct {
	Symbol    string
	FromID    *int64
	StartTime int64
	EndTime   int64
	Limit     int
}

// GetAggTradesPaged is the paging-capable version of GetAggTrades.
func (c *Client) GetAggTradesPaged(ctx context.Context, q AggTradesQuery) ([]AggTrade, error) {
	params := map[string]interface{}{"symbol": q.Symbol}
	if q.FromID != nil {
		params["fromId"] = *q.FromID
	}
	if q.StartTime > 0 {
		params["startTime"] = q.StartTime
	}
	if q.EndTime > 0 {
		params["endTime"] = q.EndTime
	}
	if q.Limit > 0 {
		params["limit"] = q.Limit
	}
	var res []AggTrade
	if err := c.Get(ctx, c.apiV1("aggTrades"), params, false, &res); err != nil {
		return nil, err
	}
	return res, nil
}

// GetFundingInfo retrieves funding rate configuration information
func (c *Client) GetFundingInfo(ctx context.Context) ([]FundingInfo, error) {
	var res []FundingInfo
	err := c.Get(ctx, c.apiV1("fundingInfo"), nil, false, &res)
	if err != nil {
		return nil, err
	}
	return res, nil
}

// GetFundingRate retrieves the funding rate for a specific symbol.
func (c *Client) GetFundingRate(ctx context.Context, symbol string) (*FundingRateData, error) {
	params := map[string]interface{}{
		"symbol": symbol,
	}
	var res FundingRateData
	err := c.Get(ctx, c.apiV1("premiumIndex"), params, false, &res)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// GetAllFundingRates retrieves funding rates for all symbols.
func (c *Client) GetAllFundingRates(ctx context.Context) ([]FundingRateData, error) {
	var res []FundingRateData
	err := c.Get(ctx, c.apiV1("premiumIndex"), nil, false, &res)
	if err != nil {
		return nil, err
	}
	return res, nil
}

// OpenInterestResponse matches /fapi/v1/openInterest.
type OpenInterestResponse struct {
	Symbol       string `json:"symbol"`
	OpenInterest string `json:"openInterest"` // in base asset (contracts)
	Time         int64  `json:"time"`
}

// GetOpenInterest retrieves current open interest for a perp symbol.
// Docs: https://binance-docs.github.io/apidocs/futures/en/#open-interest
func (c *Client) GetOpenInterest(ctx context.Context, symbol string) (*OpenInterestResponse, error) {
	params := map[string]interface{}{"symbol": symbol}
	var res OpenInterestResponse
	if err := c.Get(ctx, c.apiV1("openInterest"), params, false, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// FundingRateHistoryEntry matches one element of /fapi/v1/fundingRate.
type FundingRateHistoryEntry struct {
	Symbol      string `json:"symbol"`
	FundingRate string `json:"fundingRate"`
	FundingTime int64  `json:"fundingTime"`
	MarkPrice   string `json:"markPrice"`
}

// GetFundingRateHistory retrieves historical funding rate entries for a symbol.
// startMillis/endMillis are optional; pass 0 to omit. limit <= 0 uses exchange default (100).
// Docs: https://binance-docs.github.io/apidocs/futures/en/#get-funding-rate-history
func (c *Client) GetFundingRateHistory(ctx context.Context, symbol string, startMillis, endMillis int64, limit int) ([]FundingRateHistoryEntry, error) {
	params := map[string]interface{}{"symbol": symbol}
	if startMillis > 0 {
		params["startTime"] = startMillis
	}
	if endMillis > 0 {
		params["endTime"] = endMillis
	}
	if limit > 0 {
		params["limit"] = limit
	}
	var res []FundingRateHistoryEntry
	if err := c.Get(ctx, c.apiV1("fundingRate"), params, false, &res); err != nil {
		return nil, err
	}
	return res, nil
}
