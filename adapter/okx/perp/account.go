package perp

import (
	"context"
	"fmt"
	"strings"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/errs"
	"github.com/QuantProcessing/boltertrader/internal/wsstream"
	"github.com/QuantProcessing/boltertrader/sdk/okx"
	"github.com/shopspring/decimal"
)

// accountClient implements contract.AccountClient over the OKX REST + ws.
type accountClient struct {
	rest     *okx.Client
	provider *instrumentProvider
	clk      clock.Clock
	tdMode   string
	stream   *wsstream.Stream[contract.AccountEnvelope]
}

func newAccountClient(rest *okx.Client, provider *instrumentProvider, clk clock.Clock, tdMode string) *accountClient {
	normalized, err := normalizeDerivativeTdMode(tdMode)
	if err != nil {
		normalized = defaultDerivativeTdMode
	}
	return &accountClient{
		rest:     rest,
		provider: provider,
		clk:      clk,
		tdMode:   normalized,
		stream:   wsstream.New[contract.AccountEnvelope](256),
	}
}

func (c *accountClient) Balances(ctx context.Context) ([]model.AccountBalance, error) {
	bals, err := c.rest.GetAccountBalance(ctx, nil)
	if err != nil {
		return nil, err
	}
	now := c.clk.Now()
	var out []model.AccountBalance
	for _, b := range bals {
		for _, d := range b.Details {
			out = append(out, model.AccountBalance{
				Currency:  d.Ccy,
				Total:     dec(d.Eq),
				Available: dec(d.AvailBal),
				UpdatedAt: now,
			})
		}
	}
	return out, nil
}

func (c *accountClient) Positions(ctx context.Context) ([]model.Position, error) {
	instType := instTypeSwap
	positions, err := c.rest.GetPositions(ctx, &instType, nil)
	if err != nil {
		return nil, err
	}
	now := c.clk.Now()
	out := make([]model.Position, 0, len(positions))
	for i := range positions {
		evs := accountEventsFromPosition(&positions[i], c.provider)
		for _, ev := range evs {
			if pe, ok := ev.(contract.PositionEvent); ok {
				p := pe.Position
				if p.Quantity.IsZero() {
					continue
				}
				p.UpdatedAt = now
				out = append(out, p)
			}
		}
	}
	return out, nil
}

func (c *accountClient) SetLeverage(ctx context.Context, id model.InstrumentID, leverage decimal.Decimal) error {
	inst, ok := c.provider.Instrument(id)
	if !ok {
		return fmt.Errorf("okx: unknown instrument %s: %w", id, errs.ErrSymbolNotFound)
	}
	_, err := c.rest.SetLeverage(ctx, okx.SetLeverage{
		InstId:  inst.VenueSymbol,
		Lever:   int(leverage.IntPart()),
		MgnMode: c.tdMode,
	})
	return err
}

func (c *accountClient) SetMarginMode(ctx context.Context, id model.InstrumentID, mode string) error {
	// OKX margin mode is set per-order via TdMode, not via a separate account
	// call; there is no portable account-level setter here.
	switch strings.ToLower(mode) {
	case "cross", "isolated":
		return fmt.Errorf("okx: margin mode is per-order (TdMode), set it via order opts: %w", errs.ErrNotSupported)
	default:
		return fmt.Errorf("okx: invalid margin mode %q: %w", mode, errs.ErrNotSupported)
	}
}

func (c *accountClient) Events() <-chan contract.AccountEnvelope { return c.stream.C() }

// emit blocks under backpressure (never dropping balance/position updates),
// no-op after Close.
func (c *accountClient) emit(ev contract.AccountEvent) {
	c.stream.Emit(contract.NewAccountEnvelope(ev))
}

func (c *accountClient) Close() error {
	c.stream.Close()
	return nil
}
