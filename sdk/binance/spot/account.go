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
	err := c.Get(ctx, "/api/v3/account", nil, true, &res)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// ================= User Data Stream =================

type ListenKeyResponse struct {
	ListenKey string `json:"listenKey"`
}

func (c *Client) StartUserDataStream(ctx context.Context) (string, error) {
	var res ListenKeyResponse
	// POST /api/v3/userDataStream (API Key required)
	// Not signed, but requires API Key (handled by header in call)
	err := c.Post(ctx, "/api/v3/userDataStream", nil, false, &res)
	if err != nil {
		return "", err
	}
	return res.ListenKey, nil
}

func (c *Client) KeepAliveUserDataStream(ctx context.Context, listenKey string) error {
	params := map[string]interface{}{
		"listenKey": listenKey,
	}
	// PUT /api/v3/userDataStream
	return c.Put(ctx, "/api/v3/userDataStream", params, false, nil)
}

func (c *Client) CloseUserDataStream(ctx context.Context, listenKey string) error {
	params := map[string]interface{}{
		"listenKey": listenKey,
	}
	// DELETE /api/v3/userDataStream
	return c.Delete(ctx, "/api/v3/userDataStream", params, false, nil)
}
