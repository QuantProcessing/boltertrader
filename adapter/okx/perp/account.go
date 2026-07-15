package perp

import (
	"context"
	"fmt"
	"strings"
	"time"

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
	rest      *okx.Client
	provider  *instrumentProvider
	clk       clock.Clock
	tdMode    string
	accountID string
	stream    *wsstream.Stream[contract.AccountEnvelope]
}

func newAccountClient(rest *okx.Client, provider *instrumentProvider, clk clock.Clock, tdMode string, accountIDs ...string) *accountClient {
	normalized, err := normalizeDerivativeTdMode(tdMode)
	if err != nil {
		normalized = defaultDerivativeTdMode
	}
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
		tdMode:    normalized,
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
	return perpBalancesFromOKX(bals, c.accountID, c.clk.Now()), nil
}

func (c *accountClient) AccountState(ctx context.Context) (model.AccountState, error) {
	bals, err := c.rest.GetAccountBalance(ctx, nil)
	if err != nil {
		return model.AccountState{}, err
	}
	instType := instTypeSwap
	positions, err := c.rest.GetPositions(ctx, &instType, nil)
	if err != nil {
		return model.AccountState{}, err
	}
	configs, err := c.rest.GetAccountConfig(ctx)
	if err != nil {
		return model.AccountState{}, err
	}
	if _, err := firstAccountConfig(configs); err != nil {
		return model.AccountState{}, err
	}
	now := c.clk.Now()
	tsEvent := latestAccountTime(bals, positions, now)
	return model.AccountState{
		AccountID:    c.accountID,
		Venue:        venueName,
		Type:         model.AccountMargin,
		BaseCurrency: usdtSettlement,
		Balances:     perpBalancesFromOKX(bals, c.accountID, now),
		Margins:      c.marginBalancesFromOKX(bals, positions, now),
		Reported:     true,
		EventID:      model.AccountStateEventID(venueName, c.accountID, tsEvent),
		TsEvent:      tsEvent,
		TsInit:       now,
	}, nil
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
		evs := accountEventsFromPosition(&positions[i], c.provider, c.accountID)
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

func perpBalancesFromOKX(bals []okx.Balance, accountID string, now time.Time) []model.AccountBalance {
	out := make([]model.AccountBalance, 0)
	for _, b := range bals {
		for _, d := range b.Details {
			available := firstNonZeroDecimal(dec(d.AvailBal), dec(d.AvailEq))
			total := firstNonZeroDecimal(dec(d.Eq), dec(d.CashBal), available)
			out = append(out, model.AccountBalance{
				AccountID: accountID,
				Currency:  d.Ccy,
				Total:     total,
				Free:      available,
				Locked:    firstNonZeroDecimal(dec(d.Imr), dec(d.FrozenBal)),
				Borrowed:  dec(d.Liab),
				Interest:  dec(d.Interest),
				UpdatedAt: firstNonZeroTime(parseMillis(d.UTime), parseMillis(b.UTime), now),
			})
		}
	}
	return out
}

func (c *accountClient) marginBalancesFromOKX(bals []okx.Balance, positions []okx.Position, now time.Time) []model.MarginBalance {
	out := make([]model.MarginBalance, 0)
	for _, b := range bals {
		for _, d := range b.Details {
			out = append(out, model.MarginBalance{
				Currency:    d.Ccy,
				Initial:     dec(d.Imr),
				Maintenance: dec(d.Mmr),
				UpdatedAt:   firstNonZeroTime(parseMillis(d.UTime), parseMillis(b.UTime), now),
			})
		}
	}
	for _, p := range positions {
		if !isSupportedUSDTLinearSwapInstID(p.InstId) {
			continue
		}
		id := c.provider.resolveInstID(p.InstId)
		ccy := settleCurrencyForPosition(p, c.provider)
		idCopy := id
		out = append(out, model.MarginBalance{
			Currency:     ccy,
			InstrumentID: &idCopy,
			Initial:      dec(p.Imr),
			Maintenance:  dec(p.Mmr),
			UpdatedAt:    firstNonZeroTime(parseMillis(p.UTime), now),
		})
	}
	return out
}

func firstAccountConfig(configs []okx.AccountConfig) (okx.AccountConfig, error) {
	if len(configs) == 0 {
		return okx.AccountConfig{}, fmt.Errorf("okx: account config response was empty")
	}
	return configs[0], nil
}

func settleCurrencyForPosition(p okx.Position, provider *instrumentProvider) string {
	if p.Ccy != "" {
		return string(p.Ccy)
	}
	id := provider.resolveInstID(p.InstId)
	if inst, ok := provider.Instrument(id); ok && inst.Settle != "" {
		return inst.Settle
	}
	neutral := instIDToNeutral(p.InstId)
	_, quote, ok := strings.Cut(neutral, "-")
	if !ok {
		return ""
	}
	return strings.ToUpper(quote)
}

func latestAccountTime(bals []okx.Balance, positions []okx.Position, fallback time.Time) time.Time {
	latest := time.Time{}
	for _, b := range bals {
		latest = maxTime(latest, parseMillis(b.UTime))
		for _, d := range b.Details {
			latest = maxTime(latest, parseMillis(d.UTime))
		}
	}
	for _, p := range positions {
		latest = maxTime(latest, parseMillis(p.UTime))
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

func firstNonZeroDecimal(values ...decimal.Decimal) decimal.Decimal {
	for _, v := range values {
		if !v.IsZero() {
			return v
		}
	}
	return decimal.Zero
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
