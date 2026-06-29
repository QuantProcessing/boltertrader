package grvt

import (
	"context"
	"encoding/json"
)

// Account Methods

func (c *Client) GetAccountSummary(ctx context.Context) (*GetAccountSummaryResponse, error) {
	req := &GetAccountSummaryRequest{
		SubAccountID: c.SubAccountID,
	}
	resp, err := c.Post(ctx, c.TradeDataURL+"/lite/v1/account_summary", req, true)
	if err != nil {
		return nil, err
	}

	var result GetAccountSummaryResponse
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) GetFundingAccountSummary(ctx context.Context) (*GetFundingAccountSummaryResponse, error) {
	resp, err := c.Post(ctx, c.TradeDataURL+"/lite/v1/funding_account_summary", nil, true)
	if err != nil {
		return nil, err
	}

	var result GetFundingAccountSummaryResponse
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, err
	}
	return &result, nil
}
