package hyperliquid

import (
	"context"
	"encoding/json"
	"fmt"
)

type AccountAbstraction string

const (
	AccountAbstractionUnknown         AccountAbstraction = ""
	AccountAbstractionDefault         AccountAbstraction = "default"
	AccountAbstractionUnifiedAccount  AccountAbstraction = "unifiedAccount"
	AccountAbstractionPortfolioMargin AccountAbstraction = "portfolioMargin"
)

func (a AccountAbstraction) UsesSpotClearinghouseState() bool {
	return a == AccountAbstractionUnifiedAccount || a == AccountAbstractionPortfolioMargin
}

type SpotClearinghouseState struct {
	Balances []SpotBalance `json:"balances"`
}

type SpotBalance struct {
	Coin     string `json:"coin"`
	Token    int64  `json:"token"`
	Hold     string `json:"hold"`
	Total    string `json:"total"`
	EntryNtl string `json:"entryNtl"`
}

func (c *Client) GetUserAbstraction(ctx context.Context, user string) (AccountAbstraction, error) {
	if user == "" {
		user = c.AccountAddr
	}
	if user == "" {
		return AccountAbstractionUnknown, fmt.Errorf("userAbstraction requires user address")
	}
	data, err := c.Post(ctx, "/info", map[string]string{
		"type": "userAbstraction",
		"user": user,
	})
	if err != nil {
		return AccountAbstractionUnknown, err
	}
	var mode AccountAbstraction
	if err := json.Unmarshal(data, &mode); err != nil {
		return AccountAbstractionUnknown, err
	}
	return mode, nil
}

func (c *Client) GetSpotClearinghouseState(ctx context.Context, user string) (*SpotClearinghouseState, error) {
	if user == "" {
		user = c.AccountAddr
	}
	if user == "" {
		return nil, fmt.Errorf("spotClearinghouseState requires user address")
	}
	data, err := c.Post(ctx, "/info", map[string]string{
		"type": "spotClearinghouseState",
		"user": user,
	})
	if err != nil {
		return nil, err
	}
	var res SpotClearinghouseState
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	return &res, nil
}
