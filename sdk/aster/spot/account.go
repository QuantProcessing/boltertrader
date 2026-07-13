package spot

import (
	"context"
)

// Account Information

type AccountResponse struct {
	FeeTier      int       `json:"feeTier"`
	CanTrade     bool      `json:"canTrade"`
	CanDeposit   bool      `json:"canDeposit"`
	CanWithdraw  bool      `json:"canWithdraw"`
	CanBurnAsset bool      `json:"canBurnAsset"`
	UpdateTime   int64     `json:"updateTime"`
	Balances     []Balance `json:"balances"`
}

type Balance struct {
	Asset  string `json:"asset"`
	Free   string `json:"free"`
	Locked string `json:"locked"`
}

func (c *Client) GetAccount(ctx context.Context) (*AccountResponse, error) {
	var res AccountResponse
	err := c.Get(ctx, "/api/v3/account", nil, true, &res)
	if err != nil {
		return nil, err
	}
	return &res, nil
}
