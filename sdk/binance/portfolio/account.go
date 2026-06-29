package portfolio

import "context"

func (c *Client) GetBalances(ctx context.Context) ([]Balance, error) {
	var out []Balance
	if err := c.get(ctx, "/papi/v1/balance", nil, true, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) GetAccount(ctx context.Context) (*Account, error) {
	var out Account
	if err := c.get(ctx, "/papi/v1/account", nil, true, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
