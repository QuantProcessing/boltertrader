package runtimeaccept

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	btruntime "github.com/QuantProcessing/boltertrader/runtime"
	"github.com/QuantProcessing/boltertrader/runtime/journal"
	"github.com/QuantProcessing/boltertrader/runtime/risk"
	"github.com/shopspring/decimal"
)

func TestRuntimeProductSupportRequiresAccountStateCapabilityForKind(t *testing.T) {
	caps := []contract.Capabilities{
		{
			Venue: "TEST",
			Products: []contract.ProductCapability{
				{Kind: enums.KindPerp, Trading: true},
			},
			Trading: contract.TradingCapabilities{Submit: true},
		},
		{
			Venue: "TEST",
			Products: []contract.ProductCapability{
				{Kind: enums.KindSpot, Account: true},
			},
			Reports: contract.ReportCapabilities{AccountStateSnapshots: true},
		},
	}

	err := runtimeProductSupportReady(caps, true, enums.KindPerp)
	if err == nil || !strings.Contains(err.Error(), "account-state") {
		t.Fatalf("err=%v, want missing account-state product support", err)
	}
}

func TestRuntimeProductSupportRequiresTradingCapabilityWhenExecutionPresent(t *testing.T) {
	caps := []contract.Capabilities{
		{
			Venue: "TEST",
			Products: []contract.ProductCapability{
				{Kind: enums.KindPerp, Account: true},
			},
			Reports: contract.ReportCapabilities{AccountStateSnapshots: true},
		},
	}

	err := runtimeProductSupportReady(caps, true, enums.KindPerp)
	if err == nil || !strings.Contains(err.Error(), "trading") {
		t.Fatalf("err=%v, want missing trading product support", err)
	}
}

func TestRuntimeProductSupportReadyAcceptsMatchingTradingAndAccountStateKind(t *testing.T) {
	caps := []contract.Capabilities{
		{
			Venue: "TEST",
			Products: []contract.ProductCapability{
				{Kind: enums.KindPerp, Trading: true},
			},
			Trading: contract.TradingCapabilities{Submit: true},
		},
		{
			Venue: "TEST",
			Products: []contract.ProductCapability{
				{Kind: enums.KindPerp, Account: true},
			},
			Reports: contract.ReportCapabilities{AccountStateSnapshots: true},
		},
	}

	if err := runtimeProductSupportReady(caps, true, enums.KindPerp); err != nil {
		t.Fatalf("runtimeProductSupportReady: %v", err)
	}
}

func TestRuntimeOversizedOrderRejectedBeforeVenueHandoff(t *testing.T) {
	exec := newGuardedRuntimeLifecycleExec()
	id := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	inst := &model.Instrument{
		ID:        id,
		PriceTick: decimal.RequireFromString("0.1"),
		SizeStep:  decimal.RequireFromString("0.001"),
		MinQty:    decimal.RequireFromString("0.001"),
	}
	provider := acceptanceInstrumentProvider{inst: inst}
	store := journal.NewMemory()
	node := btruntime.NewNode(
		btruntime.Clients{Execution: exec},
		clock.NewRealClock(),
		"TEST-001",
		btruntime.WithAccountID("TEST-001"),
		btruntime.WithJournal(store),
	)
	AttachAccountRequiredRiskWithMaxNotional(node, provider, acceptanceMaxOrderNotional)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		node.Run(runCtx)
	}()
	defer func() {
		stop()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("runtime node did not stop")
		}
	}()
	if err := WaitForActive(ctx, node); err != nil {
		t.Fatal(err)
	}

	journalBefore := len(store.Records())
	qty := inst.MinQty
	price := acceptanceMaxOrderNotional.Mul(decimal.NewFromInt(2)).Div(qty)
	clientID := "offline-runtime-local-risk"
	_, err := node.Exec.Submit(ctx, model.OrderRequest{
		ClientID:     clientID,
		InstrumentID: id,
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     qty,
		Price:        price,
		PositionSide: enums.PosNet,
	})
	if !errors.Is(err, risk.ErrRiskRejected) {
		t.Fatalf("runtime oversized-order probe err=%v, want ErrRiskRejected", err)
	}
	if got := exec.validateSubmitCalls.Load(); got != 1 {
		t.Fatalf("local submit validator calls=%d, want 1", got)
	}
	if got := exec.validatePreTradeCalls.Load(); got != 0 {
		t.Fatalf("venue pre-trade validator calls=%d, want 0", got)
	}
	if len(exec.submits) != 0 {
		t.Fatalf("venue submit calls=%d, want 0", len(exec.submits))
	}
	if got := exec.submitPreparedCalls.Load(); got != 0 {
		t.Fatalf("prepared venue submit calls=%d, want 0", got)
	}
	if got := len(store.Records()); got != journalBefore {
		t.Fatalf("journal records before=%d after=%d, want unchanged", journalBefore, got)
	}
	if _, ok := node.Cache.Order(clientID); ok {
		t.Fatalf("oversized order %s reached cache", clientID)
	}
	if got := len(node.Cache.OpenOrders()); got != 0 {
		t.Fatalf("open orders=%d, want 0", got)
	}
	if got := node.Exec.InFlightCount(); got != 0 {
		t.Fatalf("in-flight intents=%d, want 0", got)
	}
}

func TestDeprecatedRuntimeOversizedOrderProbePreservesAPIButStaysLocal(t *testing.T) {
	exec := newGuardedRuntimeLifecycleExec()
	id := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	inst := &model.Instrument{
		ID:        id,
		PriceTick: decimal.RequireFromString("0.1"),
		SizeStep:  decimal.RequireFromString("0.001"),
		MinQty:    decimal.RequireFromString("0.001"),
	}
	provider := acceptanceInstrumentProvider{inst: inst}
	node := btruntime.NewNode(
		btruntime.Clients{Execution: exec},
		clock.NewRealClock(),
		"TEST-001",
		btruntime.WithAccountID("TEST-001"),
	)

	AssertRuntimeOversizedOrderRejected(t, node, provider, id)

	if got := exec.validateSubmitCalls.Load(); got != 0 {
		t.Fatalf("local submit validator calls=%d, want 0", got)
	}
	if got := exec.validatePreTradeCalls.Load(); got != 0 {
		t.Fatalf("venue pre-trade validator calls=%d, want 0", got)
	}
	if got := exec.submitPreparedCalls.Load(); got != 0 {
		t.Fatalf("prepared venue submit calls=%d, want 0", got)
	}
	if len(exec.submits) != 0 {
		t.Fatalf("venue submit calls=%d, want 0", len(exec.submits))
	}
}

type guardedRuntimeLifecycleExec struct {
	*runtimeLifecycleExec
	validateSubmitCalls   atomic.Int32
	validatePreTradeCalls atomic.Int32
	submitPreparedCalls   atomic.Int32
}

func newGuardedRuntimeLifecycleExec() *guardedRuntimeLifecycleExec {
	return &guardedRuntimeLifecycleExec{runtimeLifecycleExec: newRuntimeLifecycleExec()}
}

func (e *guardedRuntimeLifecycleExec) Capabilities() contract.Capabilities {
	caps := e.runtimeLifecycleExec.Capabilities()
	caps.Products = []contract.ProductCapability{{Kind: enums.KindPerp, Trading: true}}
	return caps
}

func (e *guardedRuntimeLifecycleExec) ValidateSubmit(model.OrderRequest) error {
	e.validateSubmitCalls.Add(1)
	return nil
}

func (e *guardedRuntimeLifecycleExec) ValidatePreTrade(context.Context, model.OrderRequest, *model.Instrument) (contract.PreTradeLease, error) {
	e.validatePreTradeCalls.Add(1)
	return offlineAcceptanceLease{}, nil
}

func (e *guardedRuntimeLifecycleExec) SubmitPrepared(ctx context.Context, req model.OrderRequest) (*model.Order, error) {
	e.submitPreparedCalls.Add(1)
	return e.runtimeLifecycleExec.Submit(ctx, req)
}

type offlineAcceptanceLease struct{}

func (offlineAcceptanceLease) Release() {}

type acceptanceInstrumentProvider struct{ inst *model.Instrument }

func (p acceptanceInstrumentProvider) Instrument(id model.InstrumentID) (*model.Instrument, bool) {
	if p.inst == nil || p.inst.ID != id {
		return nil, false
	}
	return p.inst, true
}

func (p acceptanceInstrumentProvider) All() []*model.Instrument {
	if p.inst == nil {
		return nil
	}
	return []*model.Instrument{p.inst}
}
