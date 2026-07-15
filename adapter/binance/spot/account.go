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
	sdkspot "github.com/QuantProcessing/boltertrader/sdk/binance/spot"
	"github.com/shopspring/decimal"
)

type accountClient struct {
	rest      *sdkspot.Client
	provider  *instrumentProvider
	clk       clock.Clock
	accountID string
	stream    *wsstream.Stream[contract.AccountEnvelope]
}

func newAccountClient(rest *sdkspot.Client, provider *instrumentProvider, clk clock.Clock, accountIDs ...string) *accountClient {
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
	acct, err := c.rest.GetAccount(ctx)
	if err != nil {
		return nil, err
	}
	return spotBalancesFromAccount(acct, c.accountID, c.clk.Now()), nil
}

func (c *accountClient) AccountState(ctx context.Context) (model.AccountState, error) {
	acct, err := c.rest.GetAccount(ctx)
	if err != nil {
		return model.AccountState{}, err
	}
	now := c.clk.Now()
	tsEvent := eventTimeFromMillis(acct.UpdateTime, now)
	return model.AccountState{
		AccountID: c.accountID,
		Venue:     venueName,
		Type:      model.AccountCash,
		Balances:  spotBalancesFromAccount(acct, c.accountID, now),
		Reported:  true,
		EventID:   model.AccountStateEventID(venueName, c.accountID, tsEvent),
		TsEvent:   tsEvent,
		TsInit:    now,
	}, nil
}

func spotBalancesFromAccount(acct *sdkspot.AccountResponse, accountID string, now time.Time) []model.AccountBalance {
	out := make([]model.AccountBalance, 0, len(acct.Balances))
	for _, b := range acct.Balances {
		free := dec(b.Free)
		locked := dec(b.Locked)
		out = append(out, model.AccountBalance{
			AccountID: accountID,
			Currency:  b.Asset,
			Total:     free.Add(locked),
			Free:      free,
			Locked:    locked,
			UpdatedAt: now,
		})
	}
	return out
}

func eventTimeFromMillis(ms int64, fallback time.Time) time.Time {
	if ms > 0 {
		return time.UnixMilli(ms)
	}
	return fallback
}

func (c *accountClient) Positions(ctx context.Context) ([]model.Position, error) {
	return nil, nil
}

func (c *accountClient) SetLeverage(ctx context.Context, id model.InstrumentID, leverage decimal.Decimal) error {
	return fmt.Errorf("binance spot: cash accounts do not support leverage: %w", errs.ErrNotSupported)
}

func (c *accountClient) SetMarginMode(ctx context.Context, id model.InstrumentID, mode string) error {
	return fmt.Errorf("binance spot: cash accounts do not support margin mode: %w", errs.ErrNotSupported)
}

func (c *accountClient) Events() <-chan contract.AccountEnvelope { return c.stream.C() }

func (c *accountClient) emit(ev contract.AccountEvent) {
	c.stream.Emit(contract.NewAccountEnvelope(ev))
}

func (c *accountClient) Close() error {
	c.stream.Close()
	return nil
}
