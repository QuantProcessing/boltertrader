package instruments

import "github.com/QuantProcessing/boltertrader/core/model"

type Registry struct {
	byID          map[string]*model.Instrument
	byVenueSymbol map[string]model.InstrumentID
	all           []*model.Instrument
}

func NewRegistry(insts ...*model.Instrument) *Registry {
	r := &Registry{
		byID:          make(map[string]*model.Instrument, len(insts)),
		byVenueSymbol: make(map[string]model.InstrumentID, len(insts)),
		all:           make([]*model.Instrument, 0, len(insts)),
	}
	for _, inst := range insts {
		if inst == nil {
			continue
		}
		copied := cloneInstrument(inst)
		r.byID[copied.ID.String()] = copied
		r.byVenueSymbol[copied.VenueSymbol] = copied.ID
		r.all = append(r.all, cloneInstrument(copied))
	}
	return r
}

func (r *Registry) Instrument(id model.InstrumentID) (*model.Instrument, bool) {
	if r == nil {
		return nil, false
	}
	inst, ok := r.byID[id.String()]
	if !ok {
		return nil, false
	}
	return cloneInstrument(inst), true
}

func (r *Registry) All() []*model.Instrument {
	if r == nil {
		return nil
	}
	out := make([]*model.Instrument, 0, len(r.all))
	for _, inst := range r.all {
		out = append(out, cloneInstrument(inst))
	}
	return out
}

func (r *Registry) ResolveVenueSymbol(symbol string) (model.InstrumentID, bool) {
	if r == nil {
		return model.InstrumentID{}, false
	}
	id, ok := r.byVenueSymbol[symbol]
	return id, ok
}

func cloneInstrument(inst *model.Instrument) *model.Instrument {
	if inst == nil {
		return nil
	}
	copied := *inst
	if inst.VenueIntCode != nil {
		v := *inst.VenueIntCode
		copied.VenueIntCode = &v
	}
	if inst.AssetIndex != nil {
		v := *inst.AssetIndex
		copied.AssetIndex = &v
	}
	return &copied
}
