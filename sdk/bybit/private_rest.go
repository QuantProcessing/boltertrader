package sdk

import (
	"context"
	"fmt"
	"strings"
)

func (c *Client) GetWalletBalance(ctx context.Context, accountType, coin string) (*WalletBalanceResult, error) {
	query := map[string]string{"accountType": accountType}
	if coin != "" {
		query["coin"] = coin
	}

	var resp responseEnvelope[WalletBalanceResult]
	err := c.getPrivate(ctx, "/v5/account/wallet-balance", query, &resp)
	if err != nil {
		return nil, err
	}
	if resp.RetCode != 0 {
		return nil, fmt.Errorf("bybit sdk: get wallet balance failed: %d %s", resp.RetCode, resp.RetMsg)
	}
	return &resp.Result, nil
}

func (c *Client) GetAccountInfo(ctx context.Context) (*AccountInfo, error) {
	var resp responseEnvelope[AccountInfo]
	err := c.getPrivate(ctx, "/v5/account/info", nil, &resp)
	if err != nil {
		return nil, err
	}
	if resp.RetCode != 0 {
		return nil, fmt.Errorf("bybit sdk: get account info failed: %d %s", resp.RetCode, resp.RetMsg)
	}
	return &resp.Result, nil
}

func (c *Client) GetAPIKeyInfo(ctx context.Context) (*APIKeyInfo, error) {
	var resp responseEnvelope[APIKeyInfo]
	err := c.getPrivate(ctx, "/v5/user/query-api", nil, &resp)
	if err != nil {
		return nil, err
	}
	if resp.RetCode != 0 {
		return nil, fmt.Errorf("bybit sdk: query api key failed: %d %s", resp.RetCode, resp.RetMsg)
	}
	return &resp.Result, nil
}

func (c *Client) GetFeeRates(ctx context.Context, category, symbol string) ([]FeeRateRecord, error) {
	var resp responseEnvelope[FeeRatesResult]
	err := c.getPrivate(ctx, "/v5/account/fee-rate", map[string]string{
		"category": category,
		"symbol":   symbol,
	}, &resp)
	if err != nil {
		return nil, err
	}
	if resp.RetCode != 0 {
		return nil, fmt.Errorf("bybit sdk: get fee rates failed: %d %s", resp.RetCode, resp.RetMsg)
	}
	return resp.Result.List, nil
}

func (c *Client) GetPositions(ctx context.Context, category, symbol, settleCoin string) ([]PositionRecord, error) {
	query := map[string]string{
		"category":   category,
		"symbol":     symbol,
		"settleCoin": settleCoin,
	}
	var resp responseEnvelope[PositionsResult]
	err := c.getPrivate(ctx, "/v5/position/list", query, &resp)
	if err != nil {
		return nil, err
	}
	if resp.RetCode != 0 {
		return nil, fmt.Errorf("bybit sdk: get positions failed: %d %s", resp.RetCode, resp.RetMsg)
	}
	return resp.Result.List, nil
}

func (c *Client) SetLeverage(ctx context.Context, req SetLeverageRequest) error {
	var resp responseEnvelope[map[string]any]
	err := c.postPrivate(ctx, "/v5/position/set-leverage", req, &resp)
	if err != nil {
		return err
	}
	if resp.RetCode == 110043 {
		return nil
	}
	if resp.RetCode != 0 {
		return fmt.Errorf("bybit sdk: set leverage failed: %d %s", resp.RetCode, resp.RetMsg)
	}
	return nil
}

func (c *Client) SwitchPositionMode(ctx context.Context, req SwitchPositionModeRequest) error {
	var resp responseEnvelope[map[string]any]
	err := c.postPrivate(ctx, "/v5/position/switch-mode", req, &resp)
	if err != nil {
		return err
	}
	if resp.RetCode == 110025 {
		return nil
	}
	if resp.RetCode != 0 {
		return fmt.Errorf("bybit sdk: switch position mode failed: %d %s", resp.RetCode, resp.RetMsg)
	}
	return nil
}

func (c *Client) BorrowSpot(ctx context.Context, req BorrowSpotRequest) (*BorrowSpotResult, error) {
	var resp responseEnvelope[BorrowSpotResult]
	err := c.postPrivate(ctx, "/v5/account/borrow", req, &resp)
	if err != nil {
		return nil, err
	}
	if resp.RetCode != 0 {
		return nil, fmt.Errorf("bybit sdk: borrow spot failed: %d %s", resp.RetCode, resp.RetMsg)
	}
	return &resp.Result, nil
}

func (c *Client) RepaySpotBorrow(ctx context.Context, req RepaySpotBorrowRequest) (*RepaySpotBorrowResult, error) {
	var resp responseEnvelope[RepaySpotBorrowResult]
	err := c.postPrivate(ctx, "/v5/account/no-convert-repay", req, &resp)
	if err != nil {
		return nil, err
	}
	if resp.RetCode != 0 {
		return nil, fmt.Errorf("bybit sdk: repay spot borrow failed: %d %s", resp.RetCode, resp.RetMsg)
	}
	return &resp.Result, nil
}

func (c *Client) GetSpotBorrowAmount(ctx context.Context, coin string) (string, error) {
	wallet, err := c.GetWalletBalance(ctx, "UNIFIED", coin)
	if err != nil {
		return "", err
	}
	for _, account := range wallet.List {
		for _, walletCoin := range account.Coin {
			if !strings.EqualFold(walletCoin.Coin, coin) {
				continue
			}
			if walletCoin.SpotBorrow != "" {
				return walletCoin.SpotBorrow, nil
			}
			if walletCoin.BorrowAmount != "" {
				return walletCoin.BorrowAmount, nil
			}
			return "0", nil
		}
	}
	return "0", nil
}

func (c *Client) SetMarginMode(ctx context.Context, req SetMarginModeRequest) (*SetMarginModeResult, error) {
	var resp responseEnvelope[SetMarginModeResult]
	err := c.postPrivate(ctx, "/v5/account/set-margin-mode", req, &resp)
	if err != nil {
		return nil, err
	}
	msg := strings.ToLower(resp.RetMsg)
	if strings.Contains(msg, "not been modified") || strings.Contains(msg, "needs to be equal to or greater than") {
		return &resp.Result, nil
	}
	if resp.RetCode != 0 {
		return nil, fmt.Errorf("bybit sdk: set margin mode failed: %d %s", resp.RetCode, resp.RetMsg)
	}
	return &resp.Result, nil
}
