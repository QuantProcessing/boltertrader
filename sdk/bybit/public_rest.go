package sdk

import (
	"context"
	"fmt"
	"strconv"
)

// GetOpenInterest retrieves open-interest history.
// category: "linear" | "inverse". intervalTime: "5min" | "15min" | "30min" | "1h" | "4h" | "1d".
// startMillis / endMillis optional (pass 0 to omit). limit <=0 uses exchange default. cursor optional.
// Docs: https://bybit-exchange.github.io/docs/v5/market/open-interest
func (c *Client) GetOpenInterest(ctx context.Context, category, symbol, intervalTime string, startMillis, endMillis int64, limit int, cursor string) (*OpenInterestResult, error) {
	query := map[string]string{
		"category":     category,
		"symbol":       symbol,
		"intervalTime": intervalTime,
	}
	if startMillis > 0 {
		query["startTime"] = strconv.FormatInt(startMillis, 10)
	}
	if endMillis > 0 {
		query["endTime"] = strconv.FormatInt(endMillis, 10)
	}
	if limit > 0 {
		query["limit"] = strconv.Itoa(limit)
	}
	if cursor != "" {
		query["cursor"] = cursor
	}
	var resp responseEnvelope[OpenInterestResult]
	if err := c.get(ctx, "/v5/market/open-interest", query, &resp); err != nil {
		return nil, err
	}
	if resp.RetCode != 0 {
		return nil, fmt.Errorf("bybit sdk: get open interest failed: %d %s", resp.RetCode, resp.RetMsg)
	}
	return &resp.Result, nil
}

// GetFundingHistory retrieves historical funding rates.
// category: "linear" | "inverse". symbol required.
// startMillis / endMillis optional (pass 0 to omit). limit <=0 uses exchange default (200).
// Docs: https://bybit-exchange.github.io/docs/v5/market/history-fund-rate
func (c *Client) GetFundingHistory(ctx context.Context, category, symbol string, startMillis, endMillis int64, limit int) ([]FundingHistoryEntry, error) {
	query := map[string]string{
		"category": category,
		"symbol":   symbol,
	}
	if startMillis > 0 {
		query["startTime"] = strconv.FormatInt(startMillis, 10)
	}
	if endMillis > 0 {
		query["endTime"] = strconv.FormatInt(endMillis, 10)
	}
	if limit > 0 {
		query["limit"] = strconv.Itoa(limit)
	}
	var resp responseEnvelope[FundingHistoryResult]
	if err := c.get(ctx, "/v5/market/funding/history", query, &resp); err != nil {
		return nil, err
	}
	if resp.RetCode != 0 {
		return nil, fmt.Errorf("bybit sdk: get funding history failed: %d %s", resp.RetCode, resp.RetMsg)
	}
	return resp.Result.List, nil
}

func (c *Client) GetInstruments(ctx context.Context, category string) ([]Instrument, error) {
	return c.GetInstrumentsForBase(ctx, category, "")
}

func (c *Client) GetInstrumentsForBase(ctx context.Context, category, baseCoin string) ([]Instrument, error) {
	var out []Instrument
	cursor := ""
	seenCursors := make(map[string]struct{})

	for page := 1; ; page++ {
		query := map[string]string{
			"category": category,
			"limit":    strconv.Itoa(1000),
			"cursor":   cursor,
		}
		if baseCoin != "" {
			query["baseCoin"] = baseCoin
		}

		var resp responseEnvelope[InstrumentsResult]
		err := c.get(ctx, "/v5/market/instruments-info", query, &resp)
		if err != nil {
			return nil, err
		}
		if resp.RetCode != 0 {
			return nil, fmt.Errorf("bybit sdk: get instruments failed: %d %s", resp.RetCode, resp.RetMsg)
		}

		out = append(out, resp.Result.List...)
		nextCursor := resp.Result.NextPageCursor
		if category == "spot" || nextCursor == "" {
			return out, nil
		}
		cursor, err = nextPaginationCursor("get instruments", cursor, nextCursor, page, seenCursors)
		if err != nil {
			return nil, err
		}
	}
}

func (c *Client) GetTicker(ctx context.Context, category, symbol string) (*Ticker, error) {
	var resp responseEnvelope[TickersResult]
	err := c.get(ctx, "/v5/market/tickers", map[string]string{
		"category": category,
		"symbol":   symbol,
	}, &resp)
	if err != nil {
		return nil, err
	}
	if resp.RetCode != 0 {
		return nil, fmt.Errorf("bybit sdk: get ticker failed: %d %s", resp.RetCode, resp.RetMsg)
	}
	if len(resp.Result.List) == 0 {
		return nil, fmt.Errorf("bybit sdk: ticker not found for %s %s", category, symbol)
	}
	if resp.Result.List[0].Time == "" && resp.Time > 0 {
		resp.Result.List[0].Time = strconv.FormatInt(resp.Time, 10)
	}
	return &resp.Result.List[0], nil
}

func (c *Client) GetTickers(ctx context.Context, category string) ([]Ticker, error) {
	var resp responseEnvelope[TickersResult]
	err := c.get(ctx, "/v5/market/tickers", map[string]string{
		"category": category,
	}, &resp)
	if err != nil {
		return nil, err
	}
	if resp.RetCode != 0 {
		return nil, fmt.Errorf("bybit sdk: get tickers failed: %d %s", resp.RetCode, resp.RetMsg)
	}
	if resp.Time > 0 {
		ts := strconv.FormatInt(resp.Time, 10)
		for i := range resp.Result.List {
			if resp.Result.List[i].Time == "" {
				resp.Result.List[i].Time = ts
			}
		}
	}
	return resp.Result.List, nil
}

func (c *Client) GetOrderBook(ctx context.Context, category, symbol string, limit int) (*OrderBook, error) {
	query := map[string]string{
		"category": category,
		"symbol":   symbol,
	}
	if limit > 0 {
		query["limit"] = strconv.Itoa(limit)
	}

	var resp responseEnvelope[OrderBook]
	err := c.get(ctx, "/v5/market/orderbook", query, &resp)
	if err != nil {
		return nil, err
	}
	if resp.RetCode != 0 {
		return nil, fmt.Errorf("bybit sdk: get orderbook failed: %d %s", resp.RetCode, resp.RetMsg)
	}
	return &resp.Result, nil
}

func (c *Client) GetRecentTrades(ctx context.Context, category, symbol string, limit int) ([]PublicTrade, error) {
	query := map[string]string{
		"category": category,
		"symbol":   symbol,
	}
	if limit > 0 {
		query["limit"] = strconv.Itoa(limit)
	}

	var resp responseEnvelope[PublicTradesResult]
	err := c.get(ctx, "/v5/market/recent-trade", query, &resp)
	if err != nil {
		return nil, err
	}
	if resp.RetCode != 0 {
		return nil, fmt.Errorf("bybit sdk: get recent trades failed: %d %s", resp.RetCode, resp.RetMsg)
	}
	return resp.Result.List, nil
}

func (c *Client) GetKlines(ctx context.Context, category, symbol, interval string, start, end int64, limit int) ([]Candle, error) {
	query := map[string]string{
		"category": category,
		"symbol":   symbol,
		"interval": interval,
	}
	if start > 0 {
		query["start"] = strconv.FormatInt(start, 10)
	}
	if end > 0 {
		query["end"] = strconv.FormatInt(end, 10)
	}
	if limit > 0 {
		query["limit"] = strconv.Itoa(limit)
	}

	var resp responseEnvelope[KlinesResult]
	err := c.get(ctx, "/v5/market/kline", query, &resp)
	if err != nil {
		return nil, err
	}
	if resp.RetCode != 0 {
		return nil, fmt.Errorf("bybit sdk: get klines failed: %d %s", resp.RetCode, resp.RetMsg)
	}
	return resp.Result.List, nil
}
