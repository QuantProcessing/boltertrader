package runtimeaccept

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	btruntime "github.com/QuantProcessing/boltertrader/runtime"
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
	exec := newRuntimeLifecycleExec()
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
	AttachAccountRequiredRiskWithAcceptanceLimit(node, provider)
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

	AssertRuntimeOversizedOrderRejected(t, node, provider, id)
	if len(exec.submits) != 0 {
		t.Fatalf("venue submit calls=%d, want 0", len(exec.submits))
	}
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
