package perp

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/errs"
	astercommon "github.com/QuantProcessing/boltertrader/sdk/aster/common"
	sdkperp "github.com/QuantProcessing/boltertrader/sdk/aster/perp"
	"github.com/shopspring/decimal"
)

type instrumentProvider struct {
	mu       sync.RWMutex
	byID     map[string]*model.Instrument
	bySymbol map[string]model.InstrumentID
	all      []*model.Instrument
}

func newInstrumentProvider() *instrumentProvider {
	return &instrumentProvider{byID: map[string]*model.Instrument{}, bySymbol: map[string]model.InstrumentID{}}
}

func (p *instrumentProvider) Load(ctx context.Context, client *sdkperp.Client, profile astercommon.Profile) error {
	info, err := client.ExchangeInfo(ctx)
	if err != nil {
		return err
	}
	return p.loadExchangeInfo(info, profile)
}

func (p *instrumentProvider) loadExchangeInfo(info *sdkperp.ExchangeInfoResponse, profile astercommon.Profile) error {
	if profile.Product() != astercommon.ProductPerp {
		return fmt.Errorf("aster perp: profile product is %q", profile.Product())
	}
	insts := make([]*model.Instrument, 0, len(info.Symbols))
	for i := range info.Symbols {
		inst, err := instrumentFromSymbolInfo(&info.Symbols[i], profile)
		if err != nil {
			return err
		}
		if inst != nil {
			insts = append(insts, inst)
		}
	}
	if len(insts) == 0 {
		return fmt.Errorf("aster perp: no supported instruments discovered: %w", errs.ErrSymbolNotFound)
	}
	p.LoadSnapshot(insts)
	return nil
}

func (p *instrumentProvider) LoadSnapshot(insts []*model.Instrument) {
	byID := make(map[string]*model.Instrument, len(insts))
	bySymbol := make(map[string]model.InstrumentID, len(insts))
	all := make([]*model.Instrument, 0, len(insts))
	for _, inst := range insts {
		if inst == nil {
			continue
		}
		clone := *inst
		byID[clone.ID.String()] = &clone
		if clone.VenueSymbol != "" {
			bySymbol[clone.VenueSymbol] = clone.ID
		}
		all = append(all, &clone)
	}
	p.mu.Lock()
	p.byID, p.bySymbol, p.all = byID, bySymbol, all
	p.mu.Unlock()
}

func (p *instrumentProvider) Instrument(id model.InstrumentID) (*model.Instrument, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	inst, ok := p.byID[id.String()]
	return inst, ok
}

func (p *instrumentProvider) All() []*model.Instrument {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]*model.Instrument, len(p.all))
	copy(out, p.all)
	return out
}

func (p *instrumentProvider) resolveKnownVenueSymbol(sym string) (model.InstrumentID, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	id, ok := p.bySymbol[sym]
	return id, ok
}

func (p *instrumentProvider) instrument(id model.InstrumentID) (*model.Instrument, error) {
	inst, ok := p.Instrument(id)
	if !ok {
		return nil, fmt.Errorf("aster perp: unknown instrument %s: %w", id, errs.ErrSymbolNotFound)
	}
	return inst, nil
}

func instrumentFromSymbolInfo(s *sdkperp.SymbolInfo, profile astercommon.Profile) (*model.Instrument, error) {
	if s == nil || s.Symbol == "" || s.BaseAsset == "" || s.QuoteAsset == "" || !strings.EqualFold(s.Status, "TRADING") || !strings.EqualFold(s.ContractType, "PERPETUAL") {
		return nil, nil
	}
	symbol, err := astercommon.NormalizeSymbol(profile, s.Symbol)
	if err != nil {
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(s.Symbol)), "TEST") {
			return nil, nil
		}
		return nil, err
	}
	settle := s.MarginAsset
	if !strings.EqualFold(settle, "USDT") {
		return nil, fmt.Errorf("aster perp: instrument %s settlement %q is not supported: %w", s.Symbol, settle, errs.ErrNotSupported)
	}
	tick, step, minQty, minNotional, err := extractFilters(s.Filters)
	if err != nil {
		return nil, fmt.Errorf("aster perp: instrument %s filters: %w", s.Symbol, err)
	}
	if !tick.IsPositive() || !step.IsPositive() {
		return nil, fmt.Errorf("aster perp: instrument %s has non-positive tick/step: %w", s.Symbol, errs.ErrInvalidPrecision)
	}
	return &model.Instrument{
		ID:                 model.InstrumentID{Venue: VenueName, Symbol: s.BaseAsset + "-" + s.QuoteAsset, Kind: enums.KindPerp},
		Base:               s.BaseAsset,
		Quote:              s.QuoteAsset,
		Settle:             settle,
		VenueSymbol:        symbol,
		PriceTick:          tick,
		SizeStep:           step,
		MinQty:             minQty,
		MinNotional:        minNotional,
		PricePrecision:     decimalPlaces(tick),
		ContractMultiplier: decimal.NewFromInt(1),
		PositionMode:       model.NetOnly,
	}, nil
}

func extractFilters(filters []sdkperp.SymbolFilter) (tick, step, minQty, minNotional decimal.Decimal, err error) {
	for _, f := range filters {
		switch f.FilterType {
		case "PRICE_FILTER":
			if tick, err = parseRequiredDecimal(f.TickSize, "tickSize"); err != nil {
				return
			}
		case "LOT_SIZE":
			if step, err = parseRequiredDecimal(f.StepSize, "stepSize"); err != nil {
				return
			}
			if strings.TrimSpace(f.MinQty) != "" {
				if minQty, err = parseRequiredDecimal(f.MinQty, "minQty"); err != nil {
					return
				}
			}
		case "MIN_NOTIONAL", "NOTIONAL":
			if strings.TrimSpace(f.Notional) != "" {
				if minNotional, err = parseRequiredDecimal(f.Notional, "notional"); err != nil {
					return
				}
			}
		}
	}
	return
}

func parseRequiredDecimal(raw, field string) (decimal.Decimal, error) {
	if strings.TrimSpace(raw) == "" {
		return decimal.Zero, fmt.Errorf("%s required: %w", field, errs.ErrInvalidPrecision)
	}
	v, err := decimal.NewFromString(raw)
	if err != nil {
		return decimal.Zero, fmt.Errorf("%s malformed %q: %w", field, raw, errs.ErrInvalidPrecision)
	}
	if !v.IsPositive() {
		return decimal.Zero, fmt.Errorf("%s non-positive %s: %w", field, raw, errs.ErrInvalidPrecision)
	}
	return v, nil
}

func decimalPlaces(v decimal.Decimal) int {
	if v.IsZero() {
		return 0
	}
	return int(-v.Exponent())
}
