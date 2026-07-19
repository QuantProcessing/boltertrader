package sdk

import (
	"context"
	"net/url"
	"strconv"
	"time"
)

type FundingRateRecord struct {
	Time int64  `json:"t"`
	Rate string `json:"r"`
}

type LeverageResponse struct {
	Leverage string `json:"leverage"`
}

func (c *Client) ListSpotOrderHistory(ctx context.Context, currencyPair string, start, end time.Time, limit int) ([]Order, error) {
	query := historyQuery(start, end, limit)
	if currencyPair != "" {
		query["currency_pair"] = currencyPair
	}
	query["status"] = "finished"
	var out []Order
	err := c.getPrivate(ctx, "/spot/orders", query, &out)
	return out, err
}

func (c *Client) ListFuturesOrderHistory(ctx context.Context, settle, contract string, start, end time.Time, limit int) ([]FuturesOrder, error) {
	query := historyQuery(start, end, limit)
	if contract != "" {
		query["contract"] = contract
	}
	query["status"] = "finished"
	var out []FuturesOrder
	err := c.getPrivate(ctx, futuresPath(settle, "orders"), query, &out)
	return out, err
}

func (c *Client) ListMyFuturesTradesFiltered(ctx context.Context, settle, contract, orderID string, limit int) ([]MyFuturesTrade, error) {
	query := map[string]string{}
	if contract != "" {
		query["contract"] = contract
	}
	if orderID != "" {
		query["order"] = orderID
	}
	if limit > 0 {
		query["limit"] = strconv.Itoa(limit)
	}
	var out []MyFuturesTrade
	err := c.getPrivate(ctx, futuresPath(settle, "my_trades"), query, &out)
	return out, err
}

func (c *Client) ListSpotCandlesticksWindow(ctx context.Context, currencyPair, interval string, start, end time.Time, limit int) ([]Candlestick, error) {
	query := candleQuery(interval, start, end, limit)
	query["currency_pair"] = currencyPair
	var out []Candlestick
	err := c.get(ctx, "/spot/candlesticks", query, &out)
	return out, err
}

func (c *Client) ListFuturesCandlesticksWindow(ctx context.Context, settle, contract, interval string, start, end time.Time, limit int) ([]FuturesCandlestick, error) {
	query := candleQuery(interval, start, end, limit)
	query["contract"] = contract
	var out []FuturesCandlestick
	err := c.get(ctx, futuresPath(settle, "candlesticks"), query, &out)
	return out, err
}

func (c *Client) ListFuturesFundingRateHistory(ctx context.Context, settle, contract string, start, end time.Time, limit int) ([]FundingRateRecord, error) {
	query := map[string]string{"contract": contract}
	if !start.IsZero() {
		query["from"] = strconv.FormatInt(start.Unix(), 10)
	}
	if !end.IsZero() {
		query["to"] = strconv.FormatInt(end.Unix(), 10)
	}
	if limit > 0 {
		query["limit"] = strconv.Itoa(limit)
	}
	var out []FundingRateRecord
	err := c.get(ctx, futuresPath(settle, "funding_rate"), query, &out)
	return out, err
}

func (c *Client) SetFuturesLeverage(ctx context.Context, settle, contract string, leverage int) (*LeverageResponse, error) {
	query := map[string]string{"leverage": strconv.Itoa(leverage)}
	var out LeverageResponse
	err := c.do(ctx, "POST", futuresPath(settle, "positions", url.PathEscape(contract), "leverage"), query, nil, true, &out)
	return &out, err
}

func historyQuery(start, end time.Time, limit int) map[string]string {
	query := map[string]string{}
	if !start.IsZero() {
		query["from"] = strconv.FormatInt(start.Unix(), 10)
	}
	if !end.IsZero() {
		query["to"] = strconv.FormatInt(end.Unix(), 10)
	}
	if limit > 0 {
		query["limit"] = strconv.Itoa(limit)
	}
	return query
}

func candleQuery(interval string, start, end time.Time, limit int) map[string]string {
	query := map[string]string{
		"interval": interval,
	}
	if !start.IsZero() {
		query["from"] = strconv.FormatInt(start.Unix(), 10)
	}
	if !end.IsZero() {
		query["to"] = strconv.FormatInt(end.Unix(), 10)
	}
	if limit > 0 {
		query["limit"] = strconv.Itoa(limit)
	}
	return query
}
