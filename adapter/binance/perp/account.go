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
	sdkperp "github.com/QuantProcessing/boltertrader/sdk/binance/perp"
	"github.com/shopspring/decimal"
)

// accountClient implements contract.AccountClient over the Binance REST +
// user-data WebSocket.
type accountClient struct {
	rest     *sdkperp.Client
	provider *instrumentProvider
	clk      clock.Clock
	stream   *wsstream.Stream[contract.AccountEnvelope]
}

func newAccountClient(rest *sdkperp.Client, provider *instrumentProvider, clk clock.Clock) *accountClient {
	return &accountClient{
		rest:     rest,
		provider: provider,
		clk:      clk,
		stream:   wsstream.New[contract.AccountEnvelope](256),
	}
}

func (c *accountClient) Balances(ctx context.Context) ([]model.AccountBalance, error) {
	resps, err := c.rest.GetBalance(ctx)
	if err != nil {
		return nil, err
	}
	now := c.clk.Now()
	out := make([]model.AccountBalance, 0, len(resps))
	for _, b := range resps {
		out = append(out, model.AccountBalance{
			Currency:  b.Asset,
			Total:     dec(b.Balance),
			Available: dec(b.AvailableBalance),
			UpdatedAt: now,
		})
	}
	return out, nil
}

func (c *accountClient) Positions(ctx context.Context) ([]model.Position, error) {
	acct, err := c.rest.GetAccount(ctx)
	if err != nil {
		return nil, err
	}
	now := c.clk.Now()
	out := make([]model.Position, 0, len(acct.Positions))
	for _, p := range acct.Positions {
		qty := dec(p.PositionAmt)
		if qty.IsZero() {
			continue // skip flat legs
		}
		out = append(out, model.Position{
			InstrumentID:  c.provider.resolveVenueSymbol(p.Symbol),
			Side:          positionSideFromBinance(p.PositionSide),
			Quantity:      qty,
			EntryPrice:    dec(p.EntryPrice),
			UnrealizedPnL: dec(p.UnrealizedProfit),
			Leverage:      dec(p.Leverage),
			UpdatedAt:     now,
		})
	}
	return out, nil
}

func (c *accountClient) SetLeverage(ctx context.Context, id model.InstrumentID, leverage decimal.Decimal) error {
	inst, ok := c.provider.Instrument(id)
	if !ok {
		return fmt.Errorf("binance: unknown instrument %s: %w", id, errs.ErrSymbolNotFound)
	}
	_, err := c.rest.ChangeLeverage(ctx, inst.VenueSymbol, int(leverage.IntPart()))
	return err
}

func (c *accountClient) SetMarginMode(ctx context.Context, id model.InstrumentID, mode string) error {
	inst, ok := c.provider.Instrument(id)
	if !ok {
		return fmt.Errorf("binance: unknown instrument %s: %w", id, errs.ErrSymbolNotFound)
	}
	var marginType string
	switch strings.ToLower(mode) {
	case "cross":
		marginType = "CROSSED"
	case "isolated":
		marginType = "ISOLATED"
	default:
		return fmt.Errorf("binance: invalid margin mode %q: %w", mode, errs.ErrNotSupported)
	}
	return c.rest.ChangeMarginType(ctx, inst.VenueSymbol, marginType)
}

func (c *accountClient) Events() <-chan contract.AccountEnvelope { return c.stream.C() }

// emit pushes a translated account event; blocks under backpressure (never
// dropping balance/position updates), no-op after Close.
func (c *accountClient) emit(ev contract.AccountEvent) {
	c.stream.Emit(contract.NewAccountEnvelope(ev))
}

func (c *accountClient) Close() error {
	c.stream.Close()
	return nil
}
