package perp

import (
	"context"
	"fmt"
	"sync"

	hlaccount "github.com/QuantProcessing/boltertrader/adapter/hyperliquid/internal/account"
	"github.com/QuantProcessing/boltertrader/adapter/hyperliquid/internal/instruments"
	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/errs"
	"github.com/QuantProcessing/boltertrader/internal/wsstream"
	sdk "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
	sdkperp "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/perp"
	"github.com/shopspring/decimal"
)

type accountClient struct {
	rest               *sdkperp.Client
	provider           *instruments.Registry
	clk                clock.Clock
	marginModeLeverage decimal.Decimal
	accountMode        sdk.AccountAbstraction
	accountID          string
	stream             *wsstream.Stream[contract.AccountEnvelope]

	mu          sync.RWMutex
	marginModes map[string]string
	defaultMode string
}

func newAccountClient(rest *sdkperp.Client, provider *instruments.Registry, clk clock.Clock, mode string, marginModeLeverage decimal.Decimal, accountMode sdk.AccountAbstraction, accountID ...string) *accountClient {
	normalized, err := normalizeMarginMode(mode)
	if err != nil {
		normalized = "cross"
	}
	if !marginModeLeverage.IsPositive() {
		marginModeLeverage = decimal.NewFromInt(1)
	}
	return &accountClient{
		rest:               rest,
		provider:           provider,
		clk:                clk,
		marginModeLeverage: marginModeLeverage,
		accountMode:        accountMode,
		accountID:          firstAccountID(accountID),
		stream:             wsstream.New[contract.AccountEnvelope](256),
		marginModes:        make(map[string]string),
		defaultMode:        normalized,
	}
}

func (c *accountClient) instrument(id model.InstrumentID) (*model.Instrument, error) {
	inst, ok := c.provider.Instrument(id)
	if !ok {
		return nil, fmt.Errorf("hyperliquid perp: unknown instrument %s: %w", id, errs.ErrSymbolNotFound)
	}
	if inst.AssetIndex == nil {
		return nil, fmt.Errorf("hyperliquid perp: missing asset index for %s: %w", id, errs.ErrSymbolNotFound)
	}
	return inst, nil
}

func (c *accountClient) Balances(ctx context.Context) ([]model.AccountBalance, error) {
	if c.accountMode.UsesSpotClearinghouseState() {
		return c.spotClearinghouseBalances(ctx)
	}
	state, err := c.rest.GetBalance(ctx)
	if err != nil {
		return nil, err
	}
	balance, ok := balanceFromPerpPosition(state, c.clk, c.accountID)
	if !ok {
		return []model.AccountBalance{}, nil
	}
	return []model.AccountBalance{balance}, nil
}

func (c *accountClient) AccountState(ctx context.Context) (model.AccountState, error) {
	mode := c.accountMode
	if mode == sdk.AccountAbstractionUnknown {
		resolved, err := c.rest.GetUserAbstraction(ctx, c.rest.AccountAddr)
		if err != nil {
			return model.AccountState{}, err
		}
		mode = resolved
	}
	perpState, err := c.rest.GetBalance(ctx)
	if err != nil {
		return model.AccountState{}, err
	}
	spotState, err := c.rest.GetSpotClearinghouseState(ctx, c.rest.AccountAddr)
	if err != nil {
		return model.AccountState{}, err
	}
	return hlaccount.BuildAccountState(hlaccount.StateInput{
		AccountID:         c.accountID,
		AccountMode:       mode,
		Perp:              perpState,
		Spot:              spotState,
		ProductScope:      []enums.InstrumentKind{enums.KindSpot, enums.KindPerp},
		Now:               c.clk.Now(),
		AccountModeSource: "userAbstraction",
		Details: map[string]string{
			"account_address": c.rest.AccountAddr,
			"adapter":         "perp",
		},
	})
}

func (c *accountClient) spotClearinghouseBalances(ctx context.Context) ([]model.AccountBalance, error) {
	state, err := c.rest.GetSpotClearinghouseState(ctx, c.rest.AccountAddr)
	if err != nil {
		return nil, err
	}
	now := c.clk.Now()
	out := make([]model.AccountBalance, 0, len(state.Balances))
	for _, b := range state.Balances {
		total := dec(b.Total)
		locked := dec(b.Hold)
		out = append(out, model.AccountBalance{
			AccountID: c.accountID,
			Currency:  b.Coin,
			Total:     total,
			Available: total.Sub(locked),
			Locked:    locked,
			UpdatedAt: now,
		})
	}
	return out, nil
}

func (c *accountClient) Positions(ctx context.Context) ([]model.Position, error) {
	state, err := c.rest.GetBalance(ctx)
	if err != nil {
		return nil, err
	}
	now := c.clk.Now()
	out := make([]model.Position, 0, len(state.AssetPositions))
	for _, pos := range positionsFromPerpPosition(state, c.provider, now, c.accountID) {
		if pos.Quantity.IsZero() {
			continue
		}
		out = append(out, pos)
	}
	return out, nil
}

func (c *accountClient) SetLeverage(ctx context.Context, id model.InstrumentID, leverage decimal.Decimal) error {
	inst, err := c.instrument(id)
	if err != nil {
		return err
	}
	lev, err := leverageInt(leverage)
	if err != nil {
		return err
	}
	mode := c.marginMode(id)
	return c.rest.UpdateLeverage(ctx, sdkperp.UpdateLeverageRequest{
		AssetID:  *inst.AssetIndex,
		IsCross:  isCrossMarginMode(mode),
		Leverage: lev,
	})
}

func (c *accountClient) SetMarginMode(ctx context.Context, id model.InstrumentID, mode string) error {
	normalized, err := normalizeMarginMode(mode)
	if err != nil {
		return err
	}
	inst, err := c.instrument(id)
	if err != nil {
		return err
	}
	lev, err := leverageInt(c.marginModeLeverage)
	if err != nil {
		return err
	}
	if err := c.rest.UpdateLeverage(ctx, sdkperp.UpdateLeverageRequest{
		AssetID:  *inst.AssetIndex,
		IsCross:  isCrossMarginMode(normalized),
		Leverage: lev,
	}); err != nil {
		return err
	}
	c.setMarginMode(id, normalized)
	return nil
}

func (c *accountClient) marginMode(id model.InstrumentID) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if mode, ok := c.marginModes[id.String()]; ok {
		return mode
	}
	return c.defaultMode
}

func (c *accountClient) setMarginMode(id model.InstrumentID, mode string) {
	c.mu.Lock()
	c.marginModes[id.String()] = mode
	c.mu.Unlock()
}

func (c *accountClient) Events() <-chan contract.AccountEnvelope { return c.stream.C() }

func (c *accountClient) emit(ev contract.AccountEvent) {
	c.stream.Emit(contract.NewAccountEnvelope(ev))
}

func (c *accountClient) Close() error {
	c.stream.Close()
	return nil
}

func firstAccountID(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	return ids[0]
}
