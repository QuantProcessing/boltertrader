package runtimeaccept

import (
	"context"
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
	"github.com/shopspring/decimal"
)

var runtimeAcceptanceTestMaxNotional = decimal.NewFromInt(1_000_000)

func TestRuntimeProductSupportRequiresAccountStateCapabilityForKind(t *testing.T) {
	execution := contract.Capabilities{
		Venue: "TEST",
		Products: []contract.ProductCapability{
			{Kind: enums.KindPerp, Trading: true},
		},
		Trading: contract.TradingCapabilities{Submit: true},
	}
	account := contract.Capabilities{
		Venue: "TEST",
		Products: []contract.ProductCapability{
			{Kind: enums.KindSpot, Account: true},
		},
		Reports: contract.ReportCapabilities{},
	}

	err := runtimeProductSupportReady(&execution, &account, true, enums.KindPerp)
	if err == nil || !strings.Contains(err.Error(), "account-state") {
		t.Fatalf("err=%v, want missing account-state product support", err)
	}
}

func TestRuntimeProductSupportRequiresTradingCapabilityWhenExecutionPresent(t *testing.T) {
	account := contract.Capabilities{
		Venue: "TEST",
		Products: []contract.ProductCapability{
			{Kind: enums.KindPerp, Account: true},
		},
		Reports: contract.ReportCapabilities{},
	}

	err := runtimeProductSupportReady(nil, &account, true, enums.KindPerp)
	if err == nil || !strings.Contains(err.Error(), "trading") {
		t.Fatalf("err=%v, want missing trading product support", err)
	}
}

func TestRuntimeProductSupportReadyAcceptsMatchingTradingAndAccountStateKind(t *testing.T) {
	execution := contract.Capabilities{
		Venue: "TEST",
		Products: []contract.ProductCapability{
			{Kind: enums.KindPerp, Trading: true},
		},
		Trading: contract.TradingCapabilities{Submit: true},
	}
	account := contract.Capabilities{
		Venue: "TEST",
		Products: []contract.ProductCapability{
			{Kind: enums.KindPerp, Account: true},
		},
		Reports: contract.ReportCapabilities{},
	}

	if err := runtimeProductSupportReady(&execution, &account, true, enums.KindPerp); err != nil {
		t.Fatalf("runtimeProductSupportReady: %v", err)
	}
}

func TestOversizedOrderProbePriceAccountsForContractMultiplier(t *testing.T) {
	inst := &model.Instrument{
		ContractMultiplier: decimal.RequireFromString("0.001"),
		PriceTick:          decimal.RequireFromString("0.1"),
	}
	qty := decimal.RequireFromString("0.01")
	maxNotional := decimal.NewFromInt(100)

	price := oversizedOrderProbePrice(inst, qty, maxNotional)
	gotNotional := price.Mul(qty).Mul(inst.ContractMultiplier)
	if !gotNotional.GreaterThan(maxNotional) {
		t.Fatalf("probe notional=%s, want greater than configured max %s", gotNotional, maxNotional)
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
	AttachAccountRequiredRiskWithMaxNotional(node, provider, runtimeAcceptanceTestMaxNotional)
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
	AssertOversizedOrderRejected(t, node, provider, id, runtimeAcceptanceTestMaxNotional)
	if got := exec.validateSubmitCalls.Load(); got != 1 {
		t.Fatalf("local submit validator calls=%d, want 1", got)
	}
	if len(exec.submits) != 0 {
		t.Fatalf("venue submit calls=%d, want 0", len(exec.submits))
	}
	if got := len(store.Records()); got != journalBefore {
		t.Fatalf("journal records before=%d after=%d, want unchanged", journalBefore, got)
	}
	if got := len(node.Cache.OpenOrders()); got != 0 {
		t.Fatalf("open orders=%d, want 0", got)
	}
	if got := node.Exec.InFlightCount(); got != 0 {
		t.Fatalf("in-flight intents=%d, want 0", got)
	}
}

type guardedRuntimeLifecycleExec struct {
	*runtimeLifecycleExec
	validateSubmitCalls atomic.Int32
}

func newGuardedRuntimeLifecycleExec() *guardedRuntimeLifecycleExec {
	return &guardedRuntimeLifecycleExec{runtimeLifecycleExec: newRuntimeLifecycleExec()}
}

func (e *guardedRuntimeLifecycleExec) Capabilities() contract.Capabilities {
	caps := e.runtimeLifecycleExec.Capabilities()
	caps.Products = []contract.ProductCapability{{Kind: enums.KindPerp, Trading: true}}
	return caps
}

func (e *guardedRuntimeLifecycleExec) AccountID() string { return "TEST-001" }

func (e *guardedRuntimeLifecycleExec) ValidateSubmit(model.OrderRequest) error {
	e.validateSubmitCalls.Add(1)
	return nil
}

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
