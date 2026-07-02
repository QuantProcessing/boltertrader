package spot

import (
	"context"
	"fmt"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
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
	stream   *wsstream.Stream[contract.AccountEvent]
}

func newAccountClient(rest *okx.Client, provider *instrumentProvider, clk clock.Clock) *accountClient {
	return &accountClient{
		rest:     rest,
		provider: provider,
		clk:      clk,
		stream:   wsstream.New[contract.AccountEvent](256),
	}
}

func (c *accountClient) Balances(ctx context.Context) ([]model.AccountBalance, error) {
	bals, err := c.rest.GetAccountBalance(ctx, nil)
	if err != nil {
		return nil, err
	}
	now := c.clk.Now()
	out := make([]model.AccountBalance, 0)
	for _, b := range bals {
		for _, d := range b.Details {
			available := firstNonZero(dec(d.AvailBal), dec(d.AvailEq), dec(d.CashBal), dec(d.Eq))
			total := firstNonZero(dec(d.Eq), dec(d.CashBal), available)
			locked := firstNonZero(dec(d.FrozenBal), total.Sub(available))
			out = append(out, model.AccountBalance{
				Currency:  d.Ccy,
				Total:     total,
				Available: available,
				Locked:    locked,
				UpdatedAt: now,
			})
		}
	}
	return out, nil
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

func (c *accountClient) Events() <-chan contract.AccountEvent { return c.stream.C() }

func (c *accountClient) emit(ev contract.AccountEvent) { c.stream.Emit(ev) }

func (c *accountClient) Close() error {
	c.stream.Close()
	return nil
}
