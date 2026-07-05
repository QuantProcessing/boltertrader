package spot

import (
	"context"
	"fmt"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/errs"
	"github.com/QuantProcessing/boltertrader/internal/wsstream"
	"github.com/QuantProcessing/boltertrader/sdk/okx"
	"github.com/shopspring/decimal"
)

type accountClient struct {
	rest     *okx.Client
	provider *instrumentProvider
	clk      clock.Clock
	stream   *wsstream.Stream[contract.AccountEnvelope]
}

func newAccountClient(rest *okx.Client, provider *instrumentProvider, clk clock.Clock) *accountClient {
	return &accountClient{
		rest:     rest,
		provider: provider,
		clk:      clk,
		stream:   wsstream.New[contract.AccountEnvelope](256),
	}
}

func (c *accountClient) Balances(ctx context.Context) ([]model.AccountBalance, error) {
	bals, err := c.rest.GetAccountBalance(ctx, nil)
	if err != nil {
		return nil, err
	}
	return spotBalancesFromOKX(bals, c.clk.Now()), nil
}

func (c *accountClient) AccountState(ctx context.Context) (model.AccountState, error) {
	bals, err := c.rest.GetAccountBalance(ctx, nil)
	if err != nil {
		return model.AccountState{}, err
	}
	configs, err := c.rest.GetAccountConfig(ctx)
	if err != nil {
		return model.AccountState{}, err
	}
	cfg, err := firstAccountConfig(configs)
	if err != nil {
		return model.AccountState{}, err
	}
	now := c.clk.Now()
	balances := spotBalancesFromOKX(bals, now)
	return model.AccountState{
		AccountID: model.AccountIDOKXSpot,
		Venue:     venueName,
		Type:      model.AccountCash,
		Balances:  balances,
		ModeInfo:  okxSpotModeInfo(cfg, now),
		Reported:  true,
		TsEvent:   latestBalanceTime(bals, now),
		TsInit:    now,
	}, nil
}

func spotBalancesFromOKX(bals []okx.Balance, now time.Time) []model.AccountBalance {
	out := make([]model.AccountBalance, 0)
	for _, b := range bals {
		for _, d := range b.Details {
			available := firstNonZero(dec(d.AvailBal), dec(d.AvailEq), dec(d.CashBal), dec(d.Eq))
			total := firstNonZero(dec(d.Eq), dec(d.CashBal), available)
			locked := firstNonZero(dec(d.FrozenBal), total.Sub(available))
			out = append(out, model.AccountBalance{
				Currency:  d.Ccy,
				Total:     total,
				Free:      available,
				Available: available,
				Locked:    locked,
				UpdatedAt: firstNonZeroTime(parseMillis(d.UTime), parseMillis(b.UTime), now),
			})
		}
	}
	return out
}

func okxSpotModeInfo(cfg okx.AccountConfig, now time.Time) model.AccountModeInfo {
	return model.AccountModeInfo{
		Venue:        venueName,
		AccountID:    model.AccountIDOKXSpot,
		AccountMode:  okxAccountModeLabel(cfg),
		MarginMode:   defaultSpotTdMode,
		PositionMode: firstNonEmpty(cfg.PosMode, "net_mode"),
		ProductScope: []enums.InstrumentKind{enums.KindSpot},
		Verified:     true,
		VerifiedAt:   now,
		Source:       "GET /api/v5/account/balance + GET /api/v5/account/config",
		Details: map[string]string{
			"acctLv":           cfg.AcctLv,
			"mgnIsoMode":       cfg.MgnIsoMode,
			"spotOffsetType":   cfg.SpotOffsetType,
			"enableSpotBorrow": fmt.Sprintf("%t", cfg.EnableSpotBorrow),
		},
	}
}

func firstAccountConfig(configs []okx.AccountConfig) (okx.AccountConfig, error) {
	if len(configs) == 0 {
		return okx.AccountConfig{}, fmt.Errorf("okx spot: account config response was empty")
	}
	return configs[0], nil
}

func latestBalanceTime(bals []okx.Balance, fallback time.Time) time.Time {
	latest := time.Time{}
	for _, b := range bals {
		latest = maxTime(latest, parseMillis(b.UTime))
		for _, d := range b.Details {
			latest = maxTime(latest, parseMillis(d.UTime))
		}
	}
	return firstNonZeroTime(latest, fallback)
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

func firstNonZeroTime(values ...time.Time) time.Time {
	for _, v := range values {
		if !v.IsZero() {
			return v
		}
	}
	return time.Time{}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func okxAccountModeLabel(cfg okx.AccountConfig) string {
	if level := string(cfg.AccountLevel()); level != "" {
		return level
	}
	if cfg.AcctLv != "" {
		return "acctLv:" + cfg.AcctLv
	}
	return "unknown"
}

func (c *accountClient) Positions(ctx context.Context) ([]model.Position, error) {
	return []model.Position{}, nil
}

func (c *accountClient) SetLeverage(ctx context.Context, id model.InstrumentID, leverage decimal.Decimal) error {
	return fmt.Errorf("okx spot: cash accounts do not support leverage: %w", errs.ErrNotSupported)
}

func (c *accountClient) SetMarginMode(ctx context.Context, id model.InstrumentID, mode string) error {
	return fmt.Errorf("okx spot: cash accounts do not support margin mode: %w", errs.ErrNotSupported)
}

func (c *accountClient) Events() <-chan contract.AccountEnvelope { return c.stream.C() }

func (c *accountClient) emit(ev contract.AccountEvent) {
	c.stream.Emit(contract.NewAccountEnvelope(ev))
}

func (c *accountClient) Close() error {
	c.stream.Close()
	return nil
}
