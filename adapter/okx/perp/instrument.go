package perp

import (
	"context"
	"sync"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/sdk/okx"
	"github.com/shopspring/decimal"
)

// instrumentProvider is the OKX implementation of model.InstrumentProvider,
// built by parsing GetInstruments("SWAP") once and caching the result.
type instrumentProvider struct {
	mu       sync.RWMutex
	byID     map[string]*model.Instrument  // key: InstrumentID.String()
	byInstID map[string]model.InstrumentID // key: OKX InstId e.g. "BTC-USDT-SWAP"
	all      []*model.Instrument
}

func newInstrumentProvider() *instrumentProvider {
	return &instrumentProvider{
		byID:     make(map[string]*model.Instrument),
		byInstID: make(map[string]model.InstrumentID),
	}
}

// Load fetches SWAP instruments and populates the registry.
func (p *instrumentProvider) Load(ctx context.Context, client *okx.Client) error {
	insts, err := client.GetInstruments(ctx, instTypeSwap)
	if err != nil {
		return err
	}
	byID := make(map[string]*model.Instrument, len(insts))
	byInstID := make(map[string]model.InstrumentID, len(insts))
	all := make([]*model.Instrument, 0, len(insts))
	for i := range insts {
		inst := instrumentFromOKX(&insts[i])
		if inst == nil {
			continue
		}
		byID[inst.ID.String()] = inst
		byInstID[inst.VenueSymbol] = inst.ID
		all = append(all, inst)
	}
	p.mu.Lock()
	p.byID, p.byInstID, p.all = byID, byInstID, all
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

// resolveInstID maps an OKX InstId to its neutral InstrumentID, falling back to
// a best-effort neutral id so events are never dropped.
func (p *instrumentProvider) resolveInstID(instID string) model.InstrumentID {
	p.mu.RLock()
	id, ok := p.byInstID[instID]
	p.mu.RUnlock()
	if ok {
		return id
	}
	return model.InstrumentID{Venue: venueName, Symbol: instIDToNeutral(instID), Kind: enums.KindPerp}
}

// instrumentFromOKX translates one supported OKX USDT-linear SWAP instrument
// into a neutral Instrument. SDK-native integer metadata remains inside the SDK;
// adapter I/O resolves through VenueSymbol.
func instrumentFromOKX(in *okx.Instrument) *model.Instrument {
	if !isSupportedUSDTLinearSwap(okxInstrumentShapeFromInstrument(in)) {
		return nil
	}
	multiplier, err := decimal.NewFromString(in.CtVal)
	if err != nil || !multiplier.IsPositive() {
		return nil
	}
	neutral := instIDToNeutral(in.InstId)
	id := model.InstrumentID{Venue: venueName, Symbol: neutral, Kind: enums.KindPerp}

	settle := in.SettleCcy
	if settle == "" {
		settle = in.SettCcy
	}

	tick := dec(in.TickSz)
	return &model.Instrument{
		ID:                 id,
		Base:               in.BaseCcy,
		Quote:              in.QuoteCcy,
		Settle:             settle,
		VenueSymbol:        in.InstId, // "BTC-USDT-SWAP"
		AssetIndex:         nil,       // OKX is not asset-index keyed
		PriceTick:          tick,
		SizeStep:           dec(in.LotSz),
		MinQty:             dec(in.MinSz),
		MinNotional:        decimal.Zero, // OKX has no explicit min-notional filter
		PricePrecision:     int(tick.Exponent() * -1),
		ContractMultiplier: multiplier,
		PositionMode:       model.HedgeCapable, // OKX supports net or long/short mode
	}
}

func okxInstrumentShapeFromInstrument(in *okx.Instrument) *okxInstrumentShape {
	if in == nil {
		return nil
	}
	return &okxInstrumentShape{
		InstId:    in.InstId,
		InstType:  in.InstType,
		QuoteCcy:  in.QuoteCcy,
		SettCcy:   in.SettCcy,
		SettleCcy: in.SettleCcy,
	}
}
