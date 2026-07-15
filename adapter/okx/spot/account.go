package spot

import (
	"context"
	"fmt"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/errs"
	"github.com/QuantProcessing/boltertrader/internal/wsstream"
	"github.com/QuantProcessing/boltertrader/sdk/okx"
	"github.com/shopspring/decimal"
)

type accountClient struct {
	rest      *okx.Client
	provider  *instrumentProvider
	clk       clock.Clock
	accountID string
	stream    *wsstream.Stream[contract.AccountEnvelope]
}

func newAccountClient(rest *okx.Client, provider *instrumentProvider, clk clock.Clock, accountIDs ...string) *accountClient {
	accountID := ""
	if len(accountIDs) > 0 {
		accountID = accountIDs[0]
	}
	if accountID == "" {
		accountID = AccountIDDefault
	}
	return &accountClient{
		rest:      rest,
		provider:  provider,
		clk:       clk,
		accountID: accountID,
		stream:    wsstream.New[contract.AccountEnvelope](256),
	}
}

func (c *accountClient) AccountID() string { return c.accountID }

func (c *accountClient) Balances(ctx context.Context) ([]model.AccountBalance, error) {
	bals, err := c.rest.GetAccountBalance(ctx, nil)
	if err != nil {
		return nil, err
	}
	return spotBalancesFromOKX(bals, c.accountID, c.clk.Now()), nil
}

func (c *accountClient) AccountState(ctx context.Context) (model.AccountState, error) {
	bals, err := c.rest.GetAccountBalance(ctx, nil)
	if err != nil {
		return model.AccountState{}, err
	}
	now := c.clk.Now()
	balances := spotBalancesFromOKX(bals, c.accountID, now)
	tsEvent := latestBalanceTime(bals, now)
	return model.AccountState{
		AccountID: c.accountID,
		Venue:     venueName,
		Type:      model.AccountCash,
		Balances:  balances,
		Reported:  true,
		EventID:   model.AccountStateEventID(venueName, c.accountID, tsEvent),
		TsEvent:   tsEvent,
		TsInit:    now,
	}, nil
}

func spotBalancesFromOKX(bals []okx.Balance, accountID string, now time.Time) []model.AccountBalance {
	out := make([]model.AccountBalance, 0)
	for _, b := range bals {
		for _, d := range b.Details {
			available := firstNonZero(dec(d.AvailBal), dec(d.AvailEq), dec(d.CashBal), dec(d.Eq))
			total := firstNonZero(dec(d.Eq), dec(d.CashBal), available)
			locked := firstNonZero(dec(d.FrozenBal), total.Sub(available))
			out = append(out, model.AccountBalance{
				AccountID: accountID,
				Currency:  d.Ccy,
				Total:     total,
				Free:      available,
				Locked:    locked,
				UpdatedAt: firstNonZeroTime(parseMillis(d.UTime), parseMillis(b.UTime), now),
			})
		}
	}
	return out
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
