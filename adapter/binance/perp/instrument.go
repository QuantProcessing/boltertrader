package perp

import (
	"context"
	"sync"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	sdkperp "github.com/QuantProcessing/boltertrader/sdk/binance/perp"
	"github.com/shopspring/decimal"
)

// instrumentProvider is the Binance implementation of model.InstrumentProvider,
// built by parsing ExchangeInfo once and caching the result.
type instrumentProvider struct {
	mu       sync.RWMutex
	byID     map[string]*model.Instrument  // key: InstrumentID.String()
	bySymbol map[string]model.InstrumentID // key: Binance venue symbol e.g. "BTCUSDT"
	all      []*model.Instrument
}

func newInstrumentProvider() *instrumentProvider {
	return &instrumentProvider{
		byID:     make(map[string]*model.Instrument),
		bySymbol: make(map[string]model.InstrumentID),
	}
}

// Load fetches ExchangeInfo and populates the registry. Safe to call again to
// refresh.
func (p *instrumentProvider) Load(ctx context.Context, client *sdkperp.Client) error {
	info, err := client.ExchangeInfo(ctx)
	if err != nil {
		return err
	}
	byID := make(map[string]*model.Instrument, len(info.Symbols))
	bySymbol := make(map[string]model.InstrumentID, len(info.Symbols))
	all := make([]*model.Instrument, 0, len(info.Symbols))
	for i := range info.Symbols {
		inst := instrumentFromSymbolInfo(&info.Symbols[i])
		if inst == nil {
			continue
		}
		byID[inst.ID.String()] = inst
		bySymbol[inst.VenueSymbol] = inst.ID
		all = append(all, inst)
	}
	p.mu.Lock()
	p.byID, p.bySymbol, p.all = byID, bySymbol, all
	p.mu.Unlock()
	return nil
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

// resolveVenueSymbol maps a Binance venue symbol (e.g. "BTCUSDT") to its neutral
// InstrumentID. If unknown, it returns a best-effort ID carrying the raw symbol
// so events are never dropped.
func (p *instrumentProvider) resolveVenueSymbol(sym string) model.InstrumentID {
	p.mu.RLock()
	id, ok := p.bySymbol[sym]
	p.mu.RUnlock()
	if ok {
		return id
	}
	return model.InstrumentID{Venue: venueName, Symbol: sym, Kind: enums.KindPerp}
}

// instrumentFromSymbolInfo translates one Binance SymbolInfo into a neutral
// Instrument, extracting precision from the raw filter maps. Returns nil for
// non-perpetual or unparseable entries.
func instrumentFromSymbolInfo(s *sdkperp.SymbolInfo) *model.Instrument {
	if s.ContractType != "PERPETUAL" {
		return nil
	}
	neutralSymbol := s.BaseAsset + "-" + s.QuoteAsset
	id := model.InstrumentID{Venue: venueName, Symbol: neutralSymbol, Kind: enums.KindPerp}

	tick, step, minQty, minNotional := extractFilters(s.Filters)

	return &model.Instrument{
		ID:             id,
		Base:           s.BaseAsset,
		Quote:          s.QuoteAsset,
		Settle:         s.MarginAsset,
		VenueSymbol:    s.Symbol,
		VenueIntCode:   nil, // Binance has no integer instrument code
		AssetIndex:     nil, // Binance is symbol-keyed
		PriceTick:      tick,
		SizeStep:       step,
		MinQty:         minQty,
		MinNotional:    minNotional,
		PricePrecision: s.PricePrecision,
		PositionMode:   model.HedgeCapable, // Binance perp supports hedge mode
	}
}

// extractFilters walks Binance's untyped filter maps for the precision values.
// Filter values may be JSON strings ("0.10") or numbers; filterValue handles both.
func extractFilters(filters []map[string]any) (tick, step, minQty, minNotional decimal.Decimal) {
	for _, f := range filters {
		switch f["filterType"] {
		case "PRICE_FILTER":
			tick = filterValue(f, "tickSize")
		case "LOT_SIZE":
			step = filterValue(f, "stepSize")
			minQty = filterValue(f, "minQty")
		case "MIN_NOTIONAL":
			minNotional = filterValue(f, "notional")
		}
	}
	return
}

func filterValue(f map[string]any, key string) decimal.Decimal {
	switch v := f[key].(type) {
	case string:
		return dec(v)
	case float64:
		return decimal.NewFromFloat(v)
	default:
		return decimal.Zero
	}
}
