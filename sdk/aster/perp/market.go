package perp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	astercommon "github.com/QuantProcessing/boltertrader/sdk/aster/common"
)

// Depth

type DepthResponse struct {
	LastUpdateID    int64      `json:"lastUpdateId"`
	EventTime       int64      `json:"E"`
	TransactionTime int64      `json:"T"`
	Bids            [][]string `json:"bids"`
	Asks            [][]string `json:"asks"`
}

func (c *Client) Depth(ctx context.Context, symbol string, limit int) (*DepthResponse, error) {
	params := map[string]interface{}{
		"symbol": symbol,
	}
	if limit > 0 {
		params["limit"] = limit
	}

	var res DepthResponse
	err := c.Get(ctx, "/fapi/v3/depth", params, false, &res)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// Klines
// [0]: open time
// [1]: open price
// [2]: high price
// [3]: low price
// [4]: close price
// [5]: volume
// [6]: close time
// [7]: quote asset volume
// [8]: number of trades
// [9]: taker buy base asset volume
// [10]: taker buy quote asset volume
// [11]: ignore

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
	err := c.Get(ctx, "/fapi/v3/klines", params, false, &res)
	if err != nil {
		return nil, err
	}
	return res, nil
}

// ReferenceKlinesQuery describes the common parameters for mark and index
// price kline endpoints. Symbol is encoded as `symbol` for mark price and
// `pair` for index price, matching the official V3 API.
type ReferenceKlinesQuery struct {
	Symbol    string
	Interval  string
	Limit     int
	StartTime int64
	EndTime   int64
}

func (c *Client) MarkPriceKlines(ctx context.Context, query ReferenceKlinesQuery) ([]KlineResponse, error) {
	return c.referenceKlines(ctx, "/fapi/v3/markPriceKlines", "symbol", query)
}

func (c *Client) IndexPriceKlines(ctx context.Context, query ReferenceKlinesQuery) ([]KlineResponse, error) {
	return c.referenceKlines(ctx, "/fapi/v3/indexPriceKlines", "pair", query)
}

func (c *Client) referenceKlines(ctx context.Context, endpoint, instrumentParam string, query ReferenceKlinesQuery) ([]KlineResponse, error) {
	symbol, err := astercommon.NormalizeSymbol(c.profile, query.Symbol)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(query.Interval) == "" {
		return nil, fmt.Errorf("aster perp reference klines: interval is required")
	}
	params := map[string]interface{}{
		instrumentParam: symbol,
		"interval":      query.Interval,
	}
	if query.Limit > 0 {
		params["limit"] = query.Limit
	}
	if query.StartTime > 0 {
		params["startTime"] = query.StartTime
	}
	if query.EndTime > 0 {
		params["endTime"] = query.EndTime
	}
	var response []KlineResponse
	if err := c.Get(ctx, endpoint, params, false, &response); err != nil {
		return nil, err
	}
	return response, nil
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
	err := c.Get(ctx, "/fapi/v3/ticker/24hr", params, false, &res)
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
	InterestRate         string `json:"interestRate"`
	NextFundingTime      int64  `json:"nextFundingTime"`
	Time                 int64  `json:"time"`
}

func (c *Client) MarkPrice(ctx context.Context, symbol string) (*MarkPriceResponse, error) {
	params := map[string]interface{}{
		"symbol": symbol,
	}
	var res MarkPriceResponse
	err := c.Get(ctx, "/fapi/v3/premiumIndex", params, false, &res)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// Exchange Info

func (c *Client) ExchangeInfo(ctx context.Context) (*ExchangeInfoResponse, error) {
	var res ExchangeInfoResponse
	err := c.Get(ctx, "/fapi/v3/exchangeInfo", nil, false, &res)
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
	err := c.Get(ctx, "/fapi/v3/aggTrades", params, false, &res)
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
	err := c.Get(ctx, "/fapi/v3/premiumIndex", params, false, &res)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// GetAllFundingRates retrieves funding rates for all symbols.
func (c *Client) GetAllFundingRates(ctx context.Context) ([]FundingRateData, error) {
	var res []FundingRateData
	err := c.Get(ctx, "/fapi/v3/premiumIndex", nil, false, &res)
	if err != nil {
		return nil, err
	}
	return res, nil
}

// AggTradesQuery is the full parameter set for /fapi/v3/aggTrades.
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
	if err := c.Get(ctx, "/fapi/v3/aggTrades", params, false, &res); err != nil {
		return nil, err
	}
	return res, nil
}

// OpenInterestResponse matches the probe-backed /fapi/v3/openInterest response.
type OpenInterestResponse struct {
	Symbol       string `json:"symbol"`
	OpenInterest string `json:"openInterest"` // in base asset (contracts)
	Time         int64  `json:"time"`
}

type OpenInterestUnavailableError struct {
	cause error
}

func (e *OpenInterestUnavailableError) Error() string {
	return "aster perp: probe-backed /fapi/v3/openInterest is unavailable or incompatible"
}

func (e *OpenInterestUnavailableError) Unwrap() error { return e.cause }

// GetOpenInterest retrieves current open interest for a perp symbol.
func (c *Client) GetOpenInterest(ctx context.Context, symbol string) (*OpenInterestResponse, error) {
	normalized, err := astercommon.NormalizeSymbol(c.profile, symbol)
	if err != nil {
		return nil, err
	}
	params := map[string]interface{}{"symbol": normalized}
	var res OpenInterestResponse
	if err := c.Get(ctx, "/fapi/v3/openInterest", params, false, &res); err != nil {
		var venueErr *astercommon.VenueError
		if errors.As(err, &venueErr) && (venueErr.StatusCode() == http.StatusNotFound || venueErr.Code() == -1020) {
			return nil, &OpenInterestUnavailableError{cause: err}
		}
		return nil, err
	}
	if res.Symbol != normalized || strings.TrimSpace(res.OpenInterest) == "" || res.Time <= 0 {
		return nil, &OpenInterestUnavailableError{}
	}
	return &res, nil
}

// FundingRateHistoryEntry matches one element of /fapi/v3/fundingRate.
type FundingRateHistoryEntry struct {
	Symbol      string `json:"symbol"`
	FundingRate string `json:"fundingRate"`
	FundingTime int64  `json:"fundingTime"`
}

// GetFundingRateHistory retrieves historical funding rate entries for a symbol.
// startMillis/endMillis are optional; pass 0 to omit. limit <= 0 uses exchange default (100).
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
	if err := c.Get(ctx, "/fapi/v3/fundingRate", params, false, &res); err != nil {
		return nil, err
	}
	return res, nil
}
