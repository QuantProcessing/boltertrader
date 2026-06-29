package grvt

import (
	"context"
	"encoding/json"
)

// Market Data Methods

func (c *Client) GetInstruments(ctx context.Context) ([]Instrument, error) {
	req := map[string]interface{}{} // Empty request
	resp, err := c.Post(ctx, c.MarketDataURL+"/lite/v1/all_instruments", req, false)
	if err != nil {
		return nil, err
	}

	var result GetInstrumentsResponse
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, err
	}
	return result.Result, nil
}

func (c *Client) GetOrderBook(ctx context.Context, instrument string, limit int) (*GetOrderBookResponse, error) {
	req := GetOrderBookRequest{
		Instrument: instrument,
		Depth:      limit,
	}
	resp, err := c.Post(ctx, c.MarketDataURL+"/lite/v1/book", req, false)
	if err != nil {
		return nil, err
	}

	var result GetOrderBookResponse
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) GetMiniTicker(ctx context.Context, instrument string) (*GetMiniTickerResponse, error) {
	req := GetMiniTickerRequest{
		Instrument: instrument,
	}
	resp, err := c.Post(ctx, c.MarketDataURL+"/lite/v1/mini", req, false)
	if err != nil {
		return nil, err
	}

	var result GetMiniTickerResponse
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) GetTicker(ctx context.Context, instrument string) (*GetTickerResponse, error) {
	req := GetTickerRequest{
		Instrument: instrument,
	}
	resp, err := c.Post(ctx, c.MarketDataURL+"/lite/v1/ticker", req, false)
	if err != nil {
		return nil, err
	}

	var result GetTickerResponse
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) GetTrade(ctx context.Context, instrument string, limit int) (*GetTradeResponse, error) {
	req := GetTradeRequest{
		Instrument: instrument,
		Limit:      limit,
	}
	resp, err := c.Post(ctx, c.MarketDataURL+"/lite/v1/trade", req, false)
	if err != nil {
		return nil, err
	}

	var result GetTradeResponse
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) GetKLine(ctx context.Context, instrument string, interval string, klineType string, startTime *string, endTime *string, limit *int64, cursor *string) (*GetKLineResponse, error) {
	req := GetKLineRequest{
		Instrument: instrument,
		Interval:   interval,
		KlineType:  klineType,
		StartTime:  startTime,
		EndTime:    endTime,
		Limit:      limit,
		Cursor:     cursor,
	}
	resp, err := c.Post(ctx, c.MarketDataURL+"/lite/v1/kline", req, false)
	if err != nil {
		return nil, err
	}

	var result GetKLineResponse
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// GetHistoricalFundingRate retrieves historical funding rate data
// Renamed from GetFundingRate to clarify it queries historical data
func (c *Client) GetHistoricalFundingRate(ctx context.Context, instrument string, startTime *string, endTime *string, limit *int64, cursor *string, aggType *string) (*GetFundingRateResponse, error) {
	req := GetFundingRateRequest{
		Instrument: instrument,
		StartTime:  startTime,
		EndTime:    endTime,
		Limit:      limit,
		Cursor:     cursor,
		AggType:    aggType,
	}
	resp, err := c.Post(ctx, c.MarketDataURL+"/lite/v1/funding", req, false)
	if err != nil {
		return nil, err
	}

	var result GetFundingRateResponse
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, err
	}
	return &result, nil
}
