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
	"errors"
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

// Support declares whether a venue capability is intentionally supported.
type Support struct {
	Supported   bool
	Reason      string
	Conformance bool
}

func Supported() Support { return Support{Supported: true, Conformance: true} }

func InventorySupported(reason string) Support {
	return Support{Supported: true, Reason: reason}
}

func Unsupported(reason string) Support {
	return Support{Supported: false, Reason: reason}
}

// CapabilityProbe exercises or inventories a capability declaration.
// Conformance-supported probes must be executable and must not return
// ErrNotSupported. Inventory-supported declarations are explicit skips until a
// deterministic probe is added. Unsupported probes must return an error
// wrapping contract.ErrNotSupported; if no probe is supplied, the subtest is
// recorded as an explicit skip with the declaration reason.
type CapabilityProbe struct {
	Support Support
	Probe   func(context.Context) error
}

type MarketCapabilities struct {
	OrderBook         CapabilityProbe
	Bars              CapabilityProbe
	SubscribeBook     CapabilityProbe
	SubscribeQuotes   CapabilityProbe
	SubscribeTrades   CapabilityProbe
	Reconnect         CapabilityProbe
	RESTOnlyStreams   CapabilityProbe
	RESTOnlyReconnect CapabilityProbe
}

type ExecutionCapabilities struct {
	Submit       CapabilityProbe
	Cancel       CapabilityProbe
	CancelAll    CapabilityProbe
	Modify       CapabilityProbe
	OpenOrders   CapabilityProbe
	OrderReports CapabilityProbe
}

type AccountCapabilities struct {
	Balances          CapabilityProbe
	Positions         CapabilityProbe
	SetLeverage       CapabilityProbe
	SetCrossMargin    CapabilityProbe
	SetIsolatedMargin CapabilityProbe
}

type PerpCapabilitySuite struct {
	Venue     string
	Market    MarketCapabilities
	Execution ExecutionCapabilities
	Account   AccountCapabilities
}

type SpotCapabilitySuite struct {
	Venue     string
	Market    MarketCapabilities
	Execution ExecutionCapabilities
	Account   AccountCapabilities
}

func RunPerpCapabilitySuite(t *testing.T, suite PerpCapabilitySuite) {
	t.Helper()
	runGroup(t, suite.Venue+"/market", []namedCapability{
		{"order_book", suite.Market.OrderBook},
		{"bars", suite.Market.Bars},
		{"subscribe_book", suite.Market.SubscribeBook},
		{"subscribe_quotes", suite.Market.SubscribeQuotes},
		{"subscribe_trades", suite.Market.SubscribeTrades},
		{"reconnect", suite.Market.Reconnect},
		{"rest_only_streams", suite.Market.RESTOnlyStreams},
		{"rest_only_reconnect", suite.Market.RESTOnlyReconnect},
	})
	runGroup(t, suite.Venue+"/execution", []namedCapability{
		{"submit", suite.Execution.Submit},
		{"cancel", suite.Execution.Cancel},
		{"cancel_all", suite.Execution.CancelAll},
		{"modify", suite.Execution.Modify},
		{"open_orders", suite.Execution.OpenOrders},
		{"order_reports", suite.Execution.OrderReports},
	})
	runGroup(t, suite.Venue+"/account", []namedCapability{
		{"balances", suite.Account.Balances},
		{"positions", suite.Account.Positions},
		{"set_leverage", suite.Account.SetLeverage},
		{"set_cross_margin", suite.Account.SetCrossMargin},
		{"set_isolated_margin", suite.Account.SetIsolatedMargin},
	})
}

func RunSpotCapabilitySuite(t *testing.T, suite SpotCapabilitySuite) {
	t.Helper()
	if isZeroCapability(suite.Account.Balances) {
		t.Fatal("spot capability suite must declare account balances support")
	}
	if suite.Account.SetLeverage.Support.Supported {
		t.Fatal("spot capability suite must not declare leverage support")
	}
	if suite.Account.SetCrossMargin.Support.Supported {
		t.Fatal("spot capability suite must not declare cross-margin mode support")
	}
	if suite.Account.SetIsolatedMargin.Support.Supported {
		t.Fatal("spot capability suite must not declare isolated-margin mode support")
	}
	runGroup(t, suite.Venue+"/market", []namedCapability{
		{"order_book", suite.Market.OrderBook},
		{"bars", suite.Market.Bars},
		{"subscribe_book", suite.Market.SubscribeBook},
		{"subscribe_quotes", suite.Market.SubscribeQuotes},
		{"subscribe_trades", suite.Market.SubscribeTrades},
		{"reconnect", suite.Market.Reconnect},
		{"rest_only_streams", suite.Market.RESTOnlyStreams},
		{"rest_only_reconnect", suite.Market.RESTOnlyReconnect},
	})
	runGroup(t, suite.Venue+"/execution", []namedCapability{
		{"submit", suite.Execution.Submit},
		{"cancel", suite.Execution.Cancel},
		{"cancel_all", suite.Execution.CancelAll},
		{"modify", suite.Execution.Modify},
		{"open_orders", suite.Execution.OpenOrders},
		{"order_reports", suite.Execution.OrderReports},
	})
	runGroup(t, suite.Venue+"/account", []namedCapability{
		{"balances", suite.Account.Balances},
		{"positions", suite.Account.Positions},
		{"set_leverage", suite.Account.SetLeverage},
		{"set_cross_margin", suite.Account.SetCrossMargin},
		{"set_isolated_margin", suite.Account.SetIsolatedMargin},
	})
}

func isZeroCapability(cap CapabilityProbe) bool {
	return cap.Support == (Support{}) && cap.Probe == nil
}

type namedCapability struct {
	name  string
	probe CapabilityProbe
}

func runGroup(t *testing.T, group string, caps []namedCapability) {
	t.Helper()
	for _, cap := range caps {
		cap := cap
		if isZeroCapability(cap.probe) {
			continue
		}
		t.Run(group+"/"+cap.name, func(t *testing.T) {
			runCapabilityProbe(t, cap.probe)
		})
	}
}

func runCapabilityProbe(t *testing.T, cap CapabilityProbe) {
	t.Helper()
	if cap.Support.Supported {
		if !cap.Support.Conformance {
			reason := cap.Support.Reason
			if reason == "" {
				reason = "supported capability declared without deterministic conformance probe"
			}
			t.Skip(reason)
		}
		if cap.Probe == nil {
			t.Fatal("supported capability must provide an executable probe")
		}
		err := cap.Probe(context.Background())
		if errors.Is(err, contract.ErrNotSupported) {
			t.Fatalf("supported capability returned ErrNotSupported: %v", err)
		}
		if err != nil {
			t.Fatalf("supported capability probe failed: %v", err)
		}
		return
	}

	reason := cap.Support.Reason
	if reason == "" {
		reason = "unsupported"
	}
	if cap.Probe == nil {
		t.Skip(reason)
	}
	err := cap.Probe(context.Background())
	if err == nil {
		t.Fatalf("unsupported capability %q returned nil", reason)
	}
	if !errors.Is(err, contract.ErrNotSupported) {
		t.Fatalf("unsupported capability must wrap ErrNotSupported, got %v", err)
	}
}
