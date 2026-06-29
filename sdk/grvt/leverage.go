package grvt

import (
	"context"
	"encoding/json"
)

func (c *Client) SetLeverage(ctx context.Context, instrument string, leverage int) (*SetLeverageResponse, error) {
	req := SetLeverageRequest{SubAccountID: c.SubAccountID, Instrument: instrument, Leverage: leverage}
	resp, err := c.Post(ctx, c.TradeDataURL+"/lite/v1/set_initial_leverage", req, true)
	if err != nil {
		return nil, err
	}

	var result SetLeverageResponse
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) GetAllInitialLeverage(ctx context.Context) (*GetAllInitialLeverageResponse, error) {
	req := GetAllInitialLeverageRequest{SubAccountID: c.SubAccountID}
	resp, err := c.Post(ctx, c.TradeDataURL+"/lite/v1/get_all_initial_leverage", req, true)
	if err != nil {
		return nil, err
	}

	var result GetAllInitialLeverageResponse
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, err
	}
	return &result, nil
}
