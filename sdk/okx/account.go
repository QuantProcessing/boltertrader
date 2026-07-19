package okx

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
)

// GetAccountBalance retrieves the account balance.
// ccy: optional, comma-separated list of currencies (e.g. "BTC,ETH")
func (c *Client) GetAccountBalance(ctx context.Context, ccy *string) ([]Balance, error) {
	params := url.Values{}
	if ccy != nil {
		params.Add("ccy", *ccy)
	}

	path := "/api/v5/account/balance"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}

	return Request[Balance](c, ctx, MethodGet, path, nil, true)
}

// GetPositions retrieves the account positions.
// instId: optional, instrument ID
// posType: optional, position type
func (c *Client) GetPositions(ctx context.Context, instType, instId *string) ([]Position, error) {
	params := url.Values{}
	if instType != nil {
		params.Add("instType", *instType)
	}
	if instId != nil {
		params.Add("instId", *instId)
	}

	path := "/api/v5/account/positions"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}

	return Request[Position](c, ctx, MethodGet, path, nil, true)
}

// GetAccountConfig retrieves the account configuration.
func (c *Client) GetAccountConfig(ctx context.Context) ([]AccountConfig, error) {
	return Request[AccountConfig](c, ctx, MethodGet, "/api/v5/account/config", nil, true)
}

// SetPositionMode sets the position mode.
// posMode: long_short_mode or net_mode(for perp and option)
func (c *Client) SetPositionMode(ctx context.Context, posMode string) ([]PositionMode, error) {
	return Request[PositionMode](c, ctx, MethodPost, "/api/v5/account/set-position-mode", PositionMode{PosMode: posMode}, true)
}

// SetLeverage sets the leverage.
// instId: instrument ID
// lever: leverage
// mgnMode: isolated or cross
func (c *Client) SetLeverage(ctx context.Context, params SetLeverage) ([]SetLeverage, error) {
	return Request[SetLeverage](c, ctx, MethodPost, "/api/v5/account/set-leverage", params, true)
}

func (params *SetLeverage) UnmarshalJSON(data []byte) error {
	var response struct {
		Lever   json.RawMessage `json:"lever"`
		MgnMode string          `json:"mgnMode"`
		InstId  string          `json:"instId"`
		PosSide string          `json:"posSide"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return err
	}
	leverage, err := okxLeverageInt(response.Lever)
	if err != nil {
		return err
	}
	*params = SetLeverage{
		Lever:   leverage,
		MgnMode: response.MgnMode,
		InstId:  response.InstId,
		PosSide: response.PosSide,
	}
	return nil
}

func okxLeverageInt(raw json.RawMessage) (int, error) {
	var numeric int
	if err := json.Unmarshal(raw, &numeric); err == nil {
		return numeric, nil
	}
	var encoded string
	if err := json.Unmarshal(raw, &encoded); err != nil {
		return 0, fmt.Errorf("invalid leverage response: %w", err)
	}
	value, err := strconv.Atoi(encoded)
	if err != nil {
		return 0, fmt.Errorf("invalid leverage response: %w", err)
	}
	return value, nil
}

// GetTradeFee retrieves the trade fee rates.
// instType: SPOT, MARGIN, SWAP, FUTURES, OPTION
// instId: instrument ID
func (c *Client) GetTradeFee(ctx context.Context, instType string, instId *string) ([]TradeFee, error) {
	params := url.Values{}
	params.Add("instType", instType)
	if instId != nil {
		params.Add("instId", *instId)
	}

	path := "/api/v5/account/trade-fee?" + params.Encode()
	return Request[TradeFee](c, ctx, MethodGet, path, nil, true)
}
