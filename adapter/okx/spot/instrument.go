package spot

import (
	"context"
	"strings"
	"sync"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/sdk/okx"
	"github.com/shopspring/decimal"
)

type instrumentProvider struct {
	mu       sync.RWMutex
	byID     map[string]*model.Instrument
	byInstID map[string]model.InstrumentID
	all      []*model.Instrument
}

func newInstrumentProvider() *instrumentProvider {
	return &instrumentProvider{
		byID:     make(map[string]*model.Instrument),
		byInstID: make(map[string]model.InstrumentID),
	}
}

func (p *instrumentProvider) Load(ctx context.Context, client *okx.Client) error {
	insts, err := client.GetInstruments(ctx, instTypeSpot)
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

func (p *instrumentProvider) resolveInstID(instID string) model.InstrumentID {
	p.mu.RLock()
	id, ok := p.byInstID[instID]
	p.mu.RUnlock()
	if ok {
		return id
	}
	return model.InstrumentID{Venue: venueName, Symbol: instID, Kind: enums.KindSpot}
}

func instrumentFromOKX(in *okx.Instrument) *model.Instrument {
	if in == nil || in.InstType != instTypeSpot || in.InstId == "" || in.BaseCcy == "" || in.QuoteCcy == "" {
		return nil
	}
	if in.State != "" && !strings.EqualFold(in.State, "live") {
		return nil
	}
	tick := dec(in.TickSz)
	return &model.Instrument{
		ID:             model.InstrumentID{Venue: venueName, Symbol: in.InstId, Kind: enums.KindSpot},
		Base:           in.BaseCcy,
		Quote:          in.QuoteCcy,
		Settle:         in.QuoteCcy,
		VenueSymbol:    in.InstId,
		PriceTick:      tick,
		SizeStep:       dec(in.LotSz),
		MinQty:         dec(in.MinSz),
		MinNotional:    decimal.Zero,
		PricePrecision: int(tick.Exponent() * -1),
		PositionMode:   model.NetOnly,
	}
}
