package spot

import (
	"context"
	"fmt"
)

// Depth

type DepthResponse struct {
	LastUpdateID int64      `json:"lastUpdateId"`
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
	err := c.Get(ctx, "/api/v1/depth", params, false, &res)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// Klines

type KlineResponse []interface{} // Klines are arrays of arrays

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
	err := c.Get(ctx, "/api/v1/klines", params, false, &res)
	if err != nil {
		return nil, err
	}
	return res, nil
}

type PublicTrade struct {
	ID           int64  `json:"id"`
	Price        string `json:"price"`
	Qty          string `json:"qty"`
	QuoteQty     string `json:"quoteQty"`
	Time         int64  `json:"time"`
	IsBuyerMaker bool   `json:"isBuyerMaker"`
	IsBestMatch  bool   `json:"isBestMatch"`
}

func (c *Client) GetTrades(ctx context.Context, symbol string, limit int) ([]PublicTrade, error) {
	params := map[string]interface{}{
		"symbol": symbol,
	}
	if limit > 0 {
		params["limit"] = limit
	}
	var res []PublicTrade
	err := c.Get(ctx, "/api/v1/trades", params, false, &res)
	if err != nil {
		return nil, err
	}
	return res, nil
}

type AggTrade struct {
	ID           int64  `json:"a"`
	Price        string `json:"p"`
	Quantity     string `json:"q"`
	FirstTradeID int64  `json:"f"`
	LastTradeID  int64  `json:"l"`
	Timestamp    int64  `json:"T"`
	IsBuyerMaker bool   `json:"m"`
	IsBestMatch  bool   `json:"M"`
}

type AggTradesQuery struct {
	Symbol    string
	FromID    *int64
	StartTime int64
	EndTime   int64
	Limit     int
}

func (c *Client) GetAggTradesPaged(ctx context.Context, q AggTradesQuery) ([]AggTrade, error) {
	params := map[string]interface{}{
		"symbol": q.Symbol,
	}
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
	err := c.Get(ctx, "/api/v1/aggTrades", params, false, &res)
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
	PrevClosePrice     string `json:"prevClosePrice"`
	LastPrice          string `json:"lastPrice"`
	LastQty            string `json:"lastQty"`
	BidPrice           string `json:"bidPrice"`
	BidQty             string `json:"bidQty"`
	AskPrice           string `json:"askPrice"`
	AskQty             string `json:"askQty"`
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

	// If symbol is empty, it returns a list. But here we assume single symbol for simplicity or handle list if needed.
	// The API returns a list if symbol is omitted.
	// For type safety, let's enforce symbol for now or use a different return type.
	if symbol == "" {
		return nil, fmt.Errorf("symbol is required for Ticker")
	}

	var res TickerResponse
	err := c.Get(ctx, "/api/v1/ticker/24hr", params, false, &res)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// Book Ticker

type BookTickerResponse struct {
	Symbol   string `json:"symbol"`
	BidPrice string `json:"bidPrice"`
	BidQty   string `json:"bidQty"`
	AskPrice string `json:"askPrice"`
	AskQty   string `json:"askQty"`
}

func (c *Client) BookTicker(ctx context.Context, symbol string) (*BookTickerResponse, error) {
	params := map[string]interface{}{}
	if symbol != "" {
		params["symbol"] = symbol
	}

	if symbol == "" {
		return nil, fmt.Errorf("symbol is required for BookTicker")
	}

	var res BookTickerResponse
	err := c.Get(ctx, "/api/v1/ticker/bookTicker", params, false, &res)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// Exchange Info

func (c *Client) ExchangeInfo(ctx context.Context) (*ExchangeInfoResponse, error) {
	var res ExchangeInfoResponse
	err := c.Get(ctx, "/api/v1/exchangeInfo", nil, false, &res)
	if err != nil {
		return nil, err
	}
	return &res, nil
}
