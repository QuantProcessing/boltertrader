package sdk

import (
	"context"
	"net/url"
	"strconv"
	"strings"
)

func (c *Client) ListCurrencyPairs(ctx context.Context) ([]CurrencyPair, error) {
	var out []CurrencyPair
	err := c.get(ctx, "/spot/currency_pairs", nil, &out)
	return out, err
}

func (c *Client) GetCurrencyPair(ctx context.Context, pair string) (*CurrencyPair, error) {
	var out CurrencyPair
	err := c.get(ctx, "/spot/currency_pairs/"+url.PathEscape(pair), nil, &out)
	return &out, err
}

func (c *Client) ListSpotTickers(ctx context.Context, currencyPair string) ([]Ticker, error) {
	var out []Ticker
	err := c.get(ctx, "/spot/tickers", map[string]string{"currency_pair": currencyPair}, &out)
	return out, err
}

func (c *Client) GetSpotOrderBook(ctx context.Context, currencyPair string, limit int, withID bool) (*OrderBook, error) {
	query := map[string]string{"currency_pair": currencyPair}
	if limit > 0 {
		query["limit"] = strconv.Itoa(limit)
	}
	if withID {
		query["with_id"] = "true"
	}
	var out OrderBook
	err := c.get(ctx, "/spot/order_book", query, &out)
	return &out, err
}

func (c *Client) ListSpotTrades(ctx context.Context, currencyPair string, limit int) ([]Trade, error) {
	query := map[string]string{"currency_pair": currencyPair}
	if limit > 0 {
		query["limit"] = strconv.Itoa(limit)
	}
	var out []Trade
	err := c.get(ctx, "/spot/trades", query, &out)
	return out, err
}

func (c *Client) ListSpotCandlesticks(ctx context.Context, currencyPair, interval string, limit int) ([]Candlestick, error) {
	query := map[string]string{
		"currency_pair": currencyPair,
		"interval":      interval,
	}
	if limit > 0 {
		query["limit"] = strconv.Itoa(limit)
	}
	var out []Candlestick
	err := c.get(ctx, "/spot/candlesticks", query, &out)
	return out, err
}

func (c *Client) ListFuturesContracts(ctx context.Context, settle string) ([]Contract, error) {
	var out []Contract
	err := c.get(ctx, futuresPath(settle, "contracts"), nil, &out)
	return out, err
}

func (c *Client) GetFuturesContract(ctx context.Context, settle, contract string) (*Contract, error) {
	var out Contract
	err := c.get(ctx, futuresPath(settle, "contracts", contract), nil, &out)
	return &out, err
}

func (c *Client) ListFuturesTickers(ctx context.Context, settle, contract string) ([]FuturesTicker, error) {
	var out []FuturesTicker
	err := c.get(ctx, futuresPath(settle, "tickers"), map[string]string{"contract": contract}, &out)
	return out, err
}

func (c *Client) GetFuturesOrderBook(ctx context.Context, settle, contract string, limit int, withID bool) (*FuturesOrderBook, error) {
	query := map[string]string{"contract": contract}
	if limit > 0 {
		query["limit"] = strconv.Itoa(limit)
	}
	if withID {
		query["with_id"] = "true"
	}
	var out FuturesOrderBook
	err := c.get(ctx, futuresPath(settle, "order_book"), query, &out)
	return &out, err
}

func (c *Client) ListFuturesTrades(ctx context.Context, settle, contract string, limit int) ([]FuturesTrade, error) {
	query := map[string]string{"contract": contract}
	if limit > 0 {
		query["limit"] = strconv.Itoa(limit)
	}
	var out []FuturesTrade
	err := c.get(ctx, futuresPath(settle, "trades"), query, &out)
	return out, err
}

func (c *Client) ListFuturesCandlesticks(ctx context.Context, settle, contract, interval string, limit int) ([]FuturesCandlestick, error) {
	query := map[string]string{
		"contract": contract,
		"interval": interval,
	}
	if limit > 0 {
		query["limit"] = strconv.Itoa(limit)
	}
	var out []FuturesCandlestick
	err := c.get(ctx, futuresPath(settle, "candlesticks"), query, &out)
	return out, err
}

func futuresPath(settle string, segments ...string) string {
	parts := make([]string, 0, 2+len(segments))
	parts = append(parts, "futures", strings.ToLower(settle))
	for _, segment := range segments {
		parts = append(parts, url.PathEscape(segment))
	}
	return "/" + strings.Join(parts, "/")
}
