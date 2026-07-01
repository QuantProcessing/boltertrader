package spot

import (
	"context"
	"strings"
	"sync"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	sdkspot "github.com/QuantProcessing/boltertrader/sdk/binance/spot"
	"github.com/shopspring/decimal"
)

type instrumentProvider struct {
	mu       sync.RWMutex
	byID     map[string]*model.Instrument
	bySymbol map[string]model.InstrumentID
	all      []*model.Instrument
}

func newInstrumentProvider() *instrumentProvider {
	return &instrumentProvider{
		byID:     make(map[string]*model.Instrument),
		bySymbol: make(map[string]model.InstrumentID),
	}
}

func (p *instrumentProvider) Load(ctx context.Context, client *sdkspot.Client) error {
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

func (p *instrumentProvider) resolveVenueSymbol(sym string) model.InstrumentID {
	p.mu.RLock()
	id, ok := p.bySymbol[sym]
	p.mu.RUnlock()
	if ok {
		return id
	}
	return model.InstrumentID{Venue: venueName, Symbol: sym, Kind: enums.KindSpot}
}

func instrumentFromSymbolInfo(s *sdkspot.SymbolInfo) *model.Instrument {
	if s == nil || s.Symbol == "" || s.BaseAsset == "" || s.QuoteAsset == "" {
		return nil
	}
	if s.Status != "" && !strings.EqualFold(s.Status, "TRADING") {
		return nil
	}
	neutralSymbol := s.BaseAsset + "-" + s.QuoteAsset
	id := model.InstrumentID{Venue: venueName, Symbol: neutralSymbol, Kind: enums.KindSpot}
	tick, step, minQty, minNotional := extractFilters(s.Filters)
	return &model.Instrument{
		ID:             id,
		Base:           s.BaseAsset,
		Quote:          s.QuoteAsset,
		Settle:         s.QuoteAsset,
		VenueSymbol:    s.Symbol,
		PriceTick:      tick,
		SizeStep:       step,
		MinQty:         minQty,
		MinNotional:    minNotional,
		PricePrecision: decimalPlaces(tick),
		PositionMode:   model.NetOnly,
	}
}

func extractFilters(filters []map[string]any) (tick, step, minQty, minNotional decimal.Decimal) {
	for _, f := range filters {
		switch f["filterType"] {
		case "PRICE_FILTER":
			tick = filterValue(f, "tickSize")
		case "LOT_SIZE":
			step = filterValue(f, "stepSize")
			minQty = filterValue(f, "minQty")
		case "MIN_NOTIONAL", "NOTIONAL":
			minNotional = firstNonZero(filterValue(f, "minNotional"), filterValue(f, "notional"))
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
	case int:
		return decimal.NewFromInt(int64(v))
	case int64:
		return decimal.NewFromInt(v)
	default:
		return decimal.Zero
	}
}

func firstNonZero(values ...decimal.Decimal) decimal.Decimal {
	for _, v := range values {
		if !v.IsZero() {
			return v
		}
	}
	return decimal.Zero
}

func decimalPlaces(v decimal.Decimal) int {
	if v.IsZero() {
		return 0
	}
	return int(-v.Exponent())
}
