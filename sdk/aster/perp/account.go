package perp

import (
	"context"
	"fmt"
)

// Account Information

type AccountResponse struct {
	FeeTier                     int    `json:"feeTier"`
	CanTrade                    bool   `json:"canTrade"`
	CanDeposit                  bool   `json:"canDeposit"`
	CanWithdraw                 bool   `json:"canWithdraw"`
	UpdateTime                  int64  `json:"updateTime"`
	TotalInitialMargin          string `json:"totalInitialMargin"`
	TotalMaintMargin            string `json:"totalMaintMargin"`
	TotalWalletBalance          string `json:"totalWalletBalance"`
	TotalUnrealizedProfit       string `json:"totalUnrealizedProfit"`
	TotalMarginBalance          string `json:"totalMarginBalance"`
	TotalPositionInitialMargin  string `json:"totalPositionInitialMargin"`
	TotalOpenOrderInitialMargin string `json:"totalOpenOrderInitialMargin"`
	MaxWithdrawAmount           string `json:"maxWithdrawAmount"`
	Assets                      []struct {
		Asset                  string `json:"asset"`
		WalletBalance          string `json:"walletBalance"`
		UnrealizedProfit       string `json:"unrealizedProfit"`
		MarginBalance          string `json:"marginBalance"`
		MaintMargin            string `json:"maintMargin"`
		InitialMargin          string `json:"initialMargin"`
		PositionInitialMargin  string `json:"positionInitialMargin"`
		OpenOrderInitialMargin string `json:"openOrderInitialMargin"`
		MaxWithdrawAmount      string `json:"maxWithdrawAmount"`
		CrossWalletBalance     string `json:"crossWalletBalance"`
		CrossUnPnl             string `json:"crossUnPnl"`
		AvailableBalance       string `json:"availableBalance"`
		MarginAvailable        bool   `json:"marginAvailable"`
		UpdateTime             int64  `json:"updateTime"`
	} `json:"assets"`
	Positions []struct {
		Symbol                 string `json:"symbol"`
		InitialMargin          string `json:"initialMargin"`
		MaintMargin            string `json:"maintMargin"`
		UnrealizedProfit       string `json:"unrealizedProfit"`
		PositionInitialMargin  string `json:"positionInitialMargin"`
		OpenOrderInitialMargin string `json:"openOrderInitialMargin"`
		Leverage               string `json:"leverage"`
		Isolated               bool   `json:"isolated"`
		EntryPrice             string `json:"entryPrice"`
		MaxNotional            string `json:"maxNotional"`
		PositionSide           string `json:"positionSide"`
		PositionAmt            string `json:"positionAmt"`
		UpdateTime             int64  `json:"updateTime"`
	} `json:"positions"`
}

func (c *Client) GetAccount(ctx context.Context) (*AccountResponse, error) {
	var res AccountResponse
	err := c.Get(ctx, "/fapi/v2/account", nil, true, &res)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// Balance

type BalanceResponse struct {
	AccountAlias       string `json:"accountAlias"`
	Asset              string `json:"asset"`
	Balance            string `json:"balance"`
	CrossWalletBalance string `json:"crossWalletBalance"`
	CrossUnPnl         string `json:"crossUnPnl"`
	AvailableBalance   string `json:"availableBalance"`
	MaxWithdrawAmount  string `json:"maxWithdrawAmount"`
}

func (c *Client) GetBalance(ctx context.Context) ([]BalanceResponse, error) {
	var res []BalanceResponse
	err := c.Get(ctx, "/fapi/v2/balance", nil, true, &res)
	if err != nil {
		return nil, err
	}
	return res, nil
}

// Position Risk

type PositionRiskResponse struct {
	EntryPrice       string `json:"entryPrice"`
	MarginType       string `json:"marginType"`
	IsAutoAddMargin  string `json:"isAutoAddMargin"`
	IsolatedMargin   string `json:"isolatedMargin"`
	Leverage         string `json:"leverage"`
	LiquidationPrice string `json:"liquidationPrice"`
	MarkPrice        string `json:"markPrice"`
	MaxNotionalValue string `json:"maxNotionalValue"`
	PositionAmt      string `json:"positionAmt"`
	Symbol           string `json:"symbol"`
	UnRealizedProfit string `json:"unRealizedProfit"`
	PositionSide     string `json:"positionSide"`
	Notional         string `json:"notional"`
	IsolatedWallet   string `json:"isolatedWallet"`
	UpdateTime       int64  `json:"updateTime"`
}

func (c *Client) GetPositionRisk(ctx context.Context, symbol string) ([]PositionRiskResponse, error) {
	params := map[string]interface{}{}
	if symbol != "" {
		params["symbol"] = symbol
	}
	var res []PositionRiskResponse
	err := c.Get(ctx, "/fapi/v2/positionRisk", params, true, &res)
	if err != nil {
		return nil, err
	}
	return res, nil
}

// Leverage

type LeverageResponse struct {
	Leverage         int    `json:"leverage"`
	MaxNotionalValue string `json:"maxNotionalValue"`
	Symbol           string `json:"symbol"`
}

func (c *Client) ChangeLeverage(ctx context.Context, symbol string, leverage int) (*LeverageResponse, error) {
	params := map[string]interface{}{
		"symbol":   symbol,
		"leverage": leverage,
	}
	var res LeverageResponse
	err := c.Post(ctx, "/fapi/v1/leverage", params, true, &res)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// Margin Type

type MarginTypeResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

func (c *Client) ChangeMarginType(ctx context.Context, symbol string, marginType string) error {
	params := map[string]interface{}{
		"symbol":     symbol,
		"marginType": marginType, // ISOLATED, CROSSED
	}
	var res MarginTypeResponse
	err := c.Post(ctx, "/fapi/v1/marginType", params, true, &res)
	if err != nil {
		return err
	}
	return nil
}

// Position Mode

type PositionModeResponse struct {
	DualSidePosition bool `json:"dualSidePosition"` // true: Hedge Mode, false: One-way Mode
}

func (c *Client) GetPositionMode(ctx context.Context) (*PositionModeResponse, error) {
	var res PositionModeResponse
	err := c.Get(ctx, "/fapi/v1/positionSide/dual", nil, true, &res)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

func (c *Client) ChangePositionMode(ctx context.Context, dualSidePosition bool) error {
	params := map[string]interface{}{
		"dualSidePosition": fmt.Sprintf("%v", dualSidePosition),
	}
	var res struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	err := c.Post(ctx, "/fapi/v1/positionSide/dual", params, true, &res)
	if err != nil {
		return err
	}
	return nil
}

// Multi-Assets Mode

type MultiAssetsModeResponse struct {
	MultiAssetsMargin bool `json:"multiAssetsMargin"`
}

func (c *Client) GetMultiAssetsMode(ctx context.Context) (*MultiAssetsModeResponse, error) {
	var res MultiAssetsModeResponse
	err := c.Get(ctx, "/fapi/v1/multiAssetsMargin", nil, true, &res)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

func (c *Client) ChangeMultiAssetsMode(ctx context.Context, multiAssetsMargin bool) error {
	params := map[string]interface{}{
		"multiAssetsMargin": fmt.Sprintf("%v", multiAssetsMargin),
	}
	var res struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	err := c.Post(ctx, "/fapi/v1/multiAssetsMargin", params, true, &res)
	if err != nil {
		return err
	}
	return nil
}

func (c *Client) GetFeeRate(ctx context.Context, symbol string) (*FeeRateResponse, error) {
	params := map[string]interface{}{
		"symbol": symbol,
	}
	var res FeeRateResponse
	err := c.Get(ctx, "/fapi/v1/commissionRate", params, true, &res)
	if err != nil {
		return nil, err
	}
	return &res, nil
}
