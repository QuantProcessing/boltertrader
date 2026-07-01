package spot

import (
	"context"
	"fmt"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/errs"
	"github.com/QuantProcessing/boltertrader/internal/wsstream"
	sdkspot "github.com/QuantProcessing/boltertrader/sdk/binance/spot"
	"github.com/shopspring/decimal"
)

type accountClient struct {
	rest     *sdkspot.Client
	provider *instrumentProvider
	clk      clock.Clock
	stream   *wsstream.Stream[contract.AccountEvent]
}

func newAccountClient(rest *sdkspot.Client, provider *instrumentProvider, clk clock.Clock) *accountClient {
	return &accountClient{
		rest:     rest,
		provider: provider,
		clk:      clk,
		stream:   wsstream.New[contract.AccountEvent](256),
	}
}

func (c *accountClient) Balances(ctx context.Context) ([]model.AccountBalance, error) {
	acct, err := c.rest.GetAccount(ctx)
	if err != nil {
		return nil, err
	}
	now := c.clk.Now()
	out := make([]model.AccountBalance, 0, len(acct.Balances))
	for _, b := range acct.Balances {
		free := dec(b.Free)
		locked := dec(b.Locked)
		out = append(out, model.AccountBalance{
			Currency:  b.Asset,
			Total:     free.Add(locked),
			Available: free,
			Locked:    locked,
			UpdatedAt: now,
		})
	}
	return out, nil
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

func (c *accountClient) Events() <-chan contract.AccountEvent { return c.stream.C() }

func (c *accountClient) emit(ev contract.AccountEvent) { c.stream.Emit(ev) }

func (c *accountClient) Close() error {
	c.stream.Close()
	return nil
}
