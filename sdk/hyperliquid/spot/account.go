package spot

import (
	"context"
	"encoding/json"
)

type Balance struct {
	Balances []struct {
		Coin     string `json:"coin"`
		Token    int64  `json:"token"`
		Hold     string `json:"hold"`
		Total    string `json:"total"`
		EntryNtl string `json:"entryNtl"`
	}
}

func (c *Client) GetBalance() (*Balance, error) {
	data, err := c.Post(context.Background(), "/info", map[string]string{
		"type": "spotClearinghouseState",
		"user": c.AccountAddr,
	})
	if err != nil {
		return nil, err
	}
	var res Balance
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	return &res, nil
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
