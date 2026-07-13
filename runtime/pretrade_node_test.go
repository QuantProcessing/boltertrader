package runtime_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime"
	"github.com/QuantProcessing/boltertrader/runtime/risk"
	"github.com/QuantProcessing/boltertrader/runtime/runtimetest"
	"github.com/shopspring/decimal"
)

type nodePreTradeLease struct {
	releases atomic.Int32
}

func (l *nodePreTradeLease) Release() { l.releases.Add(1) }

type nodeVenueValidatorExec struct {
	*runtimetest.FakeExec
	calls atomic.Int32
	lease *nodePreTradeLease
}

type nodePreTradeAccount struct {
	*runtimetest.FakeAccount
}

func (a *nodePreTradeAccount) Capabilities() contract.Capabilities {
	return contract.Capabilities{
		Venue:    "TEST",
		Products: []contract.ProductCapability{{Kind: enums.KindPerp, Account: true}},
		Reports:  contract.ReportCapabilities{AccountStateSnapshots: true},
	}
}

func (e *nodeVenueValidatorExec) Capabilities() contract.Capabilities {
	return contract.Capabilities{
		Venue:    "TEST",
		Products: []contract.ProductCapability{{Kind: enums.KindPerp, Trading: true}},
		Trading:  contract.TradingCapabilities{Submit: true},
	}
}

func (e *nodeVenueValidatorExec) ValidatePreTrade(context.Context, model.OrderRequest, *model.Instrument) (contract.PreTradeLease, error) {
	e.calls.Add(1)
	return e.lease, nil
}

type nodeInstrumentProvider struct {
	inst *model.Instrument
}

func (p nodeInstrumentProvider) Instrument(id model.InstrumentID) (*model.Instrument, bool) {
	if p.inst == nil || p.inst.ID != id {
		return nil, false
	}
	cp := *p.inst
	return &cp, true
}

func (p nodeInstrumentProvider) All() []*model.Instrument {
	if p.inst == nil {
		return nil
	}
	cp := *p.inst
	return []*model.Instrument{&cp}
}

func TestWithRiskAutoRegistersExecutionVenuePreTradeValidator(t *testing.T) {
	now := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	clk := clock.NewSimulatedClock(now)
	lease := &nodePreTradeLease{}
	execution := &nodeVenueValidatorExec{FakeExec: runtimetest.NewFakeExec(), lease: lease}
	account := &nodePreTradeAccount{FakeAccount: runtimetest.NewFakeAccount()}
	node := runtime.NewNode(runtime.Clients{Execution: execution, Account: account}, clk, "node-pretrade")
	inst := &model.Instrument{
		ID:                 model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindPerp},
		Settle:             "USDT",
		ContractMultiplier: decimal.NewFromInt(1),
	}
	provider := nodeInstrumentProvider{inst: inst}
	state := model.AccountState{
		AccountID:    "node-pretrade",
		Venue:        "TEST",
		Type:         model.AccountMargin,
		BaseCurrency: "USDT",
		Balances: []model.AccountBalance{{
			AccountID: "node-pretrade",
			Currency:  "USDT",
			Total:     decimal.NewFromInt(1000),
			Free:      decimal.NewFromInt(1000),
			Available: decimal.NewFromInt(1000),
			UpdatedAt: now,
		}},
		Reported: true,
		EventID:  model.AccountStateEventID("TEST", "node-pretrade", now),
		TsEvent:  now,
		TsInit:   now,
	}
	if err := node.Cache.ApplyAccountStateAt(state, now); err != nil {
		t.Fatal(err)
	}

	riskEngine := risk.New(risk.Limits{}, node.Cache).
		WithClock(func() time.Time { return now }).
		RequireAccountState()
	runtime.WithRisk(riskEngine, provider)(node)
	req := model.OrderRequest{
		AccountID:    "node-pretrade",
		InstrumentID: inst.ID,
		ClientID:     "auto-register",
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     decimal.NewFromInt(1),
		Price:        decimal.NewFromInt(100),
		PositionSide: enums.PosNet,
	}
	got, err := riskEngine.CheckContext(context.Background(), req, inst)
	if err != nil {
		t.Fatalf("CheckContext: %v", err)
	}
	if got != lease || execution.calls.Load() != 1 {
		t.Fatalf("validator calls=%d lease=%T, want one call and exact lease", execution.calls.Load(), got)
	}
	got.Release()
	if releases := lease.releases.Load(); releases != 1 {
		t.Fatalf("lease releases=%d, want 1", releases)
	}
}
