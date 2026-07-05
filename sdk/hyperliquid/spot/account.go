package spot

import (
	"context"
	"encoding/json"

	"github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
)

type Balance = hyperliquid.SpotClearinghouseState

func (c *Client) GetBalance() (*Balance, error) {
	return c.GetSpotClearinghouseState(context.Background(), c.AccountAddr)
}

func (c *Client) UserFills(ctx context.Context, user string) ([]UserFill, error) {
	req := map[string]string{
		"type": "userFills",
		"user": user,
	}
	data, err := c.Post(ctx, "/info", req)
	if err != nil {
		return nil, err
	}
	var res []UserFill
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	return res, nil
}
