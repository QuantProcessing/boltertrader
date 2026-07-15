package runtime_test

import "github.com/QuantProcessing/boltertrader/core/model"

type nodeInstrumentProvider struct {
	inst *model.Instrument
}

func (p nodeInstrumentProvider) Instrument(id model.InstrumentID) (*model.Instrument, bool) {
	if p.inst == nil || p.inst.ID != id {
		return nil, false
	}
	cloned := *p.inst
	return &cloned, true
}

func (p nodeInstrumentProvider) All() []*model.Instrument {
	if p.inst == nil {
		return nil
	}
	cloned := *p.inst
	return []*model.Instrument{&cloned}
}
