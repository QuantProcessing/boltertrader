// Package contracttest provides a reusable, venue-agnostic test harness that
// every adapter must pass. It exercises the core/contract interfaces — not an
// adapter's internals — so the same checks apply uniformly to Binance, OKX,
// Hyperliquid, and the simulated backtest venue.
//
// Venue-specific concerns (enum round-trip over a venue's native strings,
// golden payload translation) live in each adapter's own package test, where
// the unexported translation functions are reachable.
package contracttest

import (
	"context"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/shopspring/decimal"
)

// RunSubmitSynchrony asserts that ExecutionClient.Submit returns a usable Order
// synchronously and promptly. This is the proof that an async venue (e.g.
// Hyperliquid's chan PostResult) is fully hidden behind the interface: the call
// must not return a nil order without an error, and must complete well under
// the deadline.
func RunSubmitSynchrony(t *testing.T, exec contract.ExecutionClient, req model.OrderRequest) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan struct{})
	var got *model.Order
	var err error
	start := time.Now()
	go func() {
		got, err = exec.Submit(ctx, req)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Submit did not return synchronously within 2s")
	}
	if err != nil {
		t.Fatalf("Submit returned error: %v", err)
	}
	if got == nil {
		t.Fatal("Submit returned a nil order without an error")
	}
	if got.VenueOrderID == "" {
		t.Error("Submit returned an order with empty VenueOrderID")
	}
	t.Logf("Submit returned in %v with status %v", time.Since(start), got.Status)
}

// InstrumentExpectation is the expected resolved precision and identity forms
// for one instrument.
type InstrumentExpectation struct {
	ID          model.InstrumentID
	PriceTick   decimal.Decimal
	SizeStep    decimal.Decimal
	MinNotional decimal.Decimal
	VenueSymbol string
	HasIntCode  bool // OKX populates VenueIntCode
	HasAssetIdx bool // Hyperliquid populates AssetIndex
}

// RunInstrumentParsing asserts the provider resolves each expected instrument
// with exact decimal precision and the correct venue identity forms populated.
func RunInstrumentParsing(t *testing.T, provider model.InstrumentProvider, want []InstrumentExpectation) {
	t.Helper()
	for _, w := range want {
		inst, ok := provider.Instrument(w.ID)
		if !ok {
			t.Errorf("instrument %s not found", w.ID)
			continue
		}
		if !inst.PriceTick.Equal(w.PriceTick) {
			t.Errorf("%s PriceTick=%s, want %s", w.ID, inst.PriceTick, w.PriceTick)
		}
		if !inst.SizeStep.Equal(w.SizeStep) {
			t.Errorf("%s SizeStep=%s, want %s", w.ID, inst.SizeStep, w.SizeStep)
		}
		if !inst.MinNotional.Equal(w.MinNotional) {
			t.Errorf("%s MinNotional=%s, want %s", w.ID, inst.MinNotional, w.MinNotional)
		}
		if inst.VenueSymbol != w.VenueSymbol {
			t.Errorf("%s VenueSymbol=%q, want %q", w.ID, inst.VenueSymbol, w.VenueSymbol)
		}
		if (inst.VenueIntCode != nil) != w.HasIntCode {
			t.Errorf("%s VenueIntCode present=%v, want %v", w.ID, inst.VenueIntCode != nil, w.HasIntCode)
		}
		if (inst.AssetIndex != nil) != w.HasAssetIdx {
			t.Errorf("%s AssetIndex present=%v, want %v", w.ID, inst.AssetIndex != nil, w.HasAssetIdx)
		}
	}
}
