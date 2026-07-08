package sdk

import (
	"context"
	"net/url"
	"strconv"
)

func (c *Client) ListSpotAccounts(ctx context.Context, currency string) ([]SpotAccount, error) {
	query := map[string]string{"currency": currency}
	var out []SpotAccount
	err := c.getPrivate(ctx, "/spot/accounts", query, &out)
	return out, err
}

func (c *Client) CreateSpotOrder(ctx context.Context, order Order) (*Order, error) {
	var out Order
	err := c.postPrivate(ctx, "/spot/orders", order, &out)
	return &out, err
}

func (c *Client) ListSpotOpenOrders(ctx context.Context, currencyPair string) ([]Order, error) {
	query := map[string]string{
		"currency_pair": currencyPair,
		"status":        "open",
	}
	var out []Order
	err := c.getPrivate(ctx, "/spot/orders", query, &out)
	return out, err
}

func (c *Client) ListAllSpotOpenOrders(ctx context.Context, page, limit int) ([]OpenOrders, error) {
	query := map[string]string{"account": "spot"}
	if page > 0 {
		query["page"] = strconv.Itoa(page)
	}
	if limit > 0 {
		query["limit"] = strconv.Itoa(limit)
	}
	var out []OpenOrders
	err := c.getPrivate(ctx, "/spot/open_orders", query, &out)
	return out, err
}

func (c *Client) GetSpotOrder(ctx context.Context, orderID, currencyPair string) (*Order, error) {
	var out Order
	err := c.getPrivate(ctx, "/spot/orders/"+url.PathEscape(orderID), map[string]string{"currency_pair": currencyPair}, &out)
	return &out, err
}

func (c *Client) CancelSpotOrder(ctx context.Context, orderID, currencyPair string) (*Order, error) {
	var out Order
	err := c.deletePrivate(ctx, "/spot/orders/"+url.PathEscape(orderID), map[string]string{"currency_pair": currencyPair}, &out)
	return &out, err
}

func (c *Client) ListSpotMyTrades(ctx context.Context, currencyPair, orderID string, limit int) ([]SpotUserTrade, error) {
	query := map[string]string{
		"currency_pair": currencyPair,
		"order_id":      orderID,
	}
	if limit > 0 {
		query["limit"] = strconv.Itoa(limit)
	}
	var out []SpotUserTrade
	err := c.getPrivate(ctx, "/spot/my_trades", query, &out)
	return out, err
}

func (c *Client) GetFuturesAccount(ctx context.Context, settle string) (*FuturesAccount, error) {
	var out FuturesAccount
	err := c.getPrivate(ctx, futuresPath(settle, "accounts"), nil, &out)
	return &out, err
}

func (c *Client) ListPositions(ctx context.Context, settle string, holding bool) ([]Position, error) {
	query := map[string]string{}
	if holding {
		query["holding"] = "true"
	}
	var out []Position
	err := c.getPrivate(ctx, futuresPath(settle, "positions"), query, &out)
	return out, err
}

func (c *Client) CreateFuturesOrder(ctx context.Context, settle string, order FuturesOrder) (*FuturesOrder, error) {
	var out FuturesOrder
	err := c.postPrivate(ctx, futuresPath(settle, "orders"), order, &out)
	return &out, err
}

func (c *Client) ListFuturesOpenOrders(ctx context.Context, settle, contract string) ([]FuturesOrder, error) {
	query := map[string]string{
		"status":   "open",
		"contract": contract,
	}
	var out []FuturesOrder
	err := c.getPrivate(ctx, futuresPath(settle, "orders"), query, &out)
	return out, err
}

func (c *Client) GetFuturesOrder(ctx context.Context, settle string, orderID int64) (*FuturesOrder, error) {
	var out FuturesOrder
	err := c.getPrivate(ctx, futuresPath(settle, "orders", strconv.FormatInt(orderID, 10)), nil, &out)
	return &out, err
}

func (c *Client) CancelFuturesOrder(ctx context.Context, settle string, orderID int64) (*FuturesOrder, error) {
	var out FuturesOrder
	err := c.deletePrivate(ctx, futuresPath(settle, "orders", strconv.FormatInt(orderID, 10)), nil, &out)
	return &out, err
}

func (c *Client) ListMyFuturesTrades(ctx context.Context, settle, contract string, limit int) ([]MyFuturesTrade, error) {
	query := map[string]string{"contract": contract}
	if limit > 0 {
		query["limit"] = strconv.Itoa(limit)
	}
	var out []MyFuturesTrade
	err := c.getPrivate(ctx, futuresPath(settle, "my_trades"), query, &out)
	return out, err
}
