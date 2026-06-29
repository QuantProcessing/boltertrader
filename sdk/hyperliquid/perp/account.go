package perp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/QuantProcessing/exchanges/sdk/hyperliquid"
)

// UserFills

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

// GetPerpPosition

func (c *Client) GetPerpPosition(ctx context.Context) (*PerpPosition, error) {
	return c.GetPerpPositionForDex(ctx, "")
}

func (c *Client) GetPerpPositionForDex(ctx context.Context, dex string) (*PerpPosition, error) {
	req := map[string]any{
		"type": "clearinghouseState",
		"user": c.AccountAddr,
	}
	if dex != "" {
		req["dex"] = dex
	}
	data, err := c.Post(ctx, "/info", req)
	if err != nil {
		return nil, err
	}
	var res PerpPosition
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// GetBalance (Alias for GetPerpPosition)
func (c *Client) GetBalance(ctx context.Context) (*PerpPosition, error) {
	return c.GetPerpPosition(ctx)
}

func (c *Client) GetBalanceForDex(ctx context.Context, dex string) (*PerpPosition, error) {
	return c.GetPerpPositionForDex(ctx, dex)
}

// UpdateLeverage

func (c *Client) UpdateLeverage(ctx context.Context, req UpdateLeverageRequest) error {
	if c.PrivateKey == nil {
		return hyperliquid.ErrCredentialsRequired
	}
	action := hyperliquid.UpdateLeverageAction{
		Type:     "updateLeverage",
		Asset:    req.AssetID,
		IsCross:  req.IsCross,
		Leverage: req.Leverage,
	}

	nonce := c.GetNextNonce()
	sig, err := hyperliquid.SignL1Action(c.PrivateKey, action, c.Vault, nonce, c.ExpiresAfter, true)
	if err != nil {
		return err
	}

	data, err := c.PostAction(ctx, action, sig, nonce)
	if err != nil {
		return err
	}

	res := new(hyperliquid.APIResponse[UpdateLeverageResponse])
	err = json.Unmarshal(data, res)
	if err != nil {
		return err
	}

	if res.Status != "ok" {
		return fmt.Errorf("update leverage failed: %s", res.Status)
	}

	return nil
}

// UpdateIsolatedMargin

func (c *Client) UpdateIsolatedMargin(ctx context.Context, req UpdateIsolatedMarginRequest) error {
	if c.PrivateKey == nil {
		return hyperliquid.ErrCredentialsRequired
	}
	// Convert amount to ntli (6 decimals)
	ntli := int(req.Amount * 1e6)

	action := hyperliquid.UpdateIsolatedMarginAction{
		Type:  "updateIsolatedMargin",
		Asset: req.AssetID,
		IsBuy: req.IsBuy,
		Ntli:  ntli,
	}

	nonce := c.GetNextNonce()
	sig, err := hyperliquid.SignL1Action(c.PrivateKey, action, c.Vault, nonce, c.ExpiresAfter, true)
	if err != nil {
		return err
	}

	data, err := c.PostAction(ctx, action, sig, nonce)
	if err != nil {
		return err
	}

	res := new(hyperliquid.APIResponse[UpdateIsolatedMarginResponse])
	err = json.Unmarshal(data, res)
	if err != nil {
		return err
	}

	if res.Status != "ok" {
		return fmt.Errorf("update isolated margin failed: %s", res.Status)
	}

	return nil
}
