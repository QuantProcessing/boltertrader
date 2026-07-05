package spot

import (
	"context"
	"fmt"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/errs"
	"github.com/QuantProcessing/boltertrader/internal/wsstream"
	sdkspot "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/spot"
	"github.com/shopspring/decimal"
)

type accountClient struct {
	rest   *sdkspot.Client
	clk    clock.Clock
	stream *wsstream.Stream[contract.AccountEnvelope]
}

func newAccountClient(rest *sdkspot.Client, clk clock.Clock) *accountClient {
	return &accountClient{
		rest:   rest,
		clk:    clk,
		stream: wsstream.New[contract.AccountEnvelope](256),
	}
}

func (c *accountClient) Balances(ctx context.Context) ([]model.AccountBalance, error) {
	bal, err := c.rest.GetBalance()
	if err != nil {
		return nil, err
	}
	now := c.clk.Now()
	out := make([]model.AccountBalance, 0, len(bal.Balances))
	for _, b := range bal.Balances {
		total := dec(b.Total)
		locked := dec(b.Hold)
		available := total.Sub(locked)
		out = append(out, model.AccountBalance{
			Currency:  b.Coin,
			Total:     total,
			Available: available,
			Locked:    locked,
			UpdatedAt: now,
		})
	}
	return out, nil
}

func (c *accountClient) Positions(ctx context.Context) ([]model.Position, error) {
	return []model.Position{}, nil
}

func (c *accountClient) SetLeverage(ctx context.Context, id model.InstrumentID, leverage decimal.Decimal) error {
	return fmt.Errorf("hyperliquid spot: cash accounts do not support leverage: %w", errs.ErrNotSupported)
}

func (c *accountClient) SetMarginMode(ctx context.Context, id model.InstrumentID, mode string) error {
	return fmt.Errorf("hyperliquid spot: cash accounts do not support margin mode: %w", errs.ErrNotSupported)
}

func (c *accountClient) Events() <-chan contract.AccountEnvelope { return c.stream.C() }

func (c *accountClient) Close() error {
	c.stream.Close()
	return nil
}
