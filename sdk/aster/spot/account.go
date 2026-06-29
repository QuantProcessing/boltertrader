package spot

import (
	"context"
)

// Account Information

type AccountResponse struct {
	MakerCommission  int64  `json:"makerCommission"`
	TakerCommission  int64  `json:"takerCommission"`
	BuyerCommission  int64  `json:"buyerCommission"`
	SellerCommission int64  `json:"sellerCommission"`
	CanTrade         bool   `json:"canTrade"`
	CanWithdraw      bool   `json:"canWithdraw"`
	CanDeposit       bool   `json:"canDeposit"`
	UpdateTime       int64  `json:"updateTime"`
	AccountType      string `json:"accountType"`
	Balances         []struct {
		Asset  string `json:"asset"`
		Free   string `json:"free"`
		Locked string `json:"locked"`
	} `json:"balances"`
}

func (c *Client) GetAccount(ctx context.Context) (*AccountResponse, error) {
	var res AccountResponse
	err := c.Get(ctx, "/api/v1/account", nil, true, &res)
	if err != nil {
		return nil, err
	}
	return &res, nil
}
