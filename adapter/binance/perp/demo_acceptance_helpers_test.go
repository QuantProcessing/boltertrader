package perp

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
	sdkperp "github.com/QuantProcessing/boltertrader/sdk/binance/perp"
	"github.com/shopspring/decimal"
)

func demoD(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestDemoAcceptanceNormalizeSymbol(t *testing.T) {
	cases := map[string]string{
		"BTC-USDT": "BTCUSDT",
		"eth_usdt": "ETHUSDT",
		"sol/usdt": "SOLUSDT",
		"bnbusdt":  "BNBUSDT",
	}
	for input, want := range cases {
		if got := normalizeDemoAcceptanceSymbol(input); got != want {
			t.Fatalf("normalizeDemoAcceptanceSymbol(%q)=%q, want %q", input, got, want)
		}
	}
}

func TestDemoAcceptanceSymbolSpecFromExchangeInfo(t *testing.T) {
	info := &sdkperp.ExchangeInfoResponse{Symbols: []sdkperp.SymbolInfo{{
		Symbol:       "ETHUSDT",
		ContractType: "PERPETUAL",
		Filters: []map[string]any{
			{"filterType": "PRICE_FILTER", "tickSize": "0.01"},
			{"filterType": "LOT_SIZE", "stepSize": "0.001", "minQty": "0.001"},
			{"filterType": "MIN_NOTIONAL", "notional": "5"},
		},
	}}}

	spec, err := demoAcceptanceSymbolSpecFromExchangeInfo(info, "eth-usdt")
	if err != nil {
		t.Fatalf("demoAcceptanceSymbolSpecFromExchangeInfo: %v", err)
	}
	if spec.VenueSymbol != "ETHUSDT" {
		t.Fatalf("unexpected venue symbol: %s", spec.VenueSymbol)
	}
	if !spec.PriceTick.Equal(demoD("0.01")) || !spec.SizeStep.Equal(demoD("0.001")) ||
		!spec.MinQty.Equal(demoD("0.001")) || !spec.MinNotional.Equal(demoD("5")) {
		t.Fatalf("unexpected filters: %+v", spec)
	}
}

func TestDemoAcceptanceSelectOrderQuantityChoosesMinTradableStep(t *testing.T) {
	spec := demoAcceptanceSymbolSpec{
		VenueSymbol: "ETHUSDT",
		SizeStep:    demoD("0.001"),
		MinQty:      demoD("0.001"),
		MinNotional: demoD("5"),
	}

	qty, err := selectDemoAcceptanceOrderQuantity(spec, decimal.Zero, demoD("10"), demoD("3000"))
	if err != nil {
		t.Fatalf("selectDemoAcceptanceOrderQuantity: %v", err)
	}
	if !qty.Equal(demoD("0.002")) {
		t.Fatalf("qty=%s, want 0.002", qty)
	}
}

func TestDemoAcceptanceSelectOrderQuantityUsesLowestTestPriceForMinNotional(t *testing.T) {
	spec := demoAcceptanceSymbolSpec{
		VenueSymbol: "ETHUSDT",
		SizeStep:    demoD("0.001"),
		MinQty:      demoD("0.001"),
		MinNotional: demoD("20"),
	}

	qty, err := selectDemoAcceptanceOrderQuantityForPriceBand(spec, decimal.Zero, demoD("100"), demoD("2850"), demoD("3000"))
	if err != nil {
		t.Fatalf("selectDemoAcceptanceOrderQuantityForPriceBand: %v", err)
	}
	if !qty.Equal(demoD("0.008")) {
		t.Fatalf("qty=%s, want 0.008", qty)
	}
	if qty.Mul(demoD("2850")).LessThan(spec.MinNotional) {
		t.Fatalf("qty=%s does not satisfy resting-price min notional", qty)
	}
	if qty.Mul(demoD("3000")).GreaterThan(demoD("100")) {
		t.Fatalf("qty=%s exceeds max notional at mark price", qty)
	}
}

func TestDemoAcceptanceSelectOrderQuantityRejectsOverMaxNotional(t *testing.T) {
	spec := demoAcceptanceSymbolSpec{
		VenueSymbol: "BTCUSDT",
		SizeStep:    demoD("0.001"),
		MinQty:      demoD("0.001"),
		MinNotional: demoD("5"),
	}

	if _, err := selectDemoAcceptanceOrderQuantity(spec, demoD("0.001"), demoD("10"), demoD("65000")); err == nil {
		t.Fatalf("expected over-max notional rejection")
	}
}

func TestDemoAcceptanceSelectOrderQuantityRejectsNonStepQuantity(t *testing.T) {
	spec := demoAcceptanceSymbolSpec{
		VenueSymbol: "ETHUSDT",
		SizeStep:    demoD("0.001"),
		MinQty:      demoD("0.001"),
		MinNotional: demoD("5"),
	}

	if _, err := selectDemoAcceptanceOrderQuantity(spec, demoD("0.0015"), demoD("10"), demoD("3000")); err == nil {
		t.Fatalf("expected non-step quantity rejection")
	}
}

func TestDemoAcceptanceCleanupMetadataRemediation(t *testing.T) {
	meta := demoAcceptanceCleanupMetadata{
		Symbol:         "ETHUSDT",
		Side:           "BUY",
		Quantity:       demoD("0.002"),
		VenueOrderIDs:  []string{"12345"},
		ClientOrderIDs: []string{"bolter-demo-1"},
		Exposure:       demoD("0.002"),
	}

	got := meta.Remediation()
	for _, want := range []string{"ETHUSDT", "BUY", "0.002", "12345", "bolter-demo-1"} {
		if !strings.Contains(got, want) {
			t.Fatalf("remediation %q missing %q", got, want)
		}
	}
}

func TestDemoClientOrderIDFitsBinanceLimit(t *testing.T) {
	for _, kind := range []string{"rest", "fill", "close", "rtrest", "rtfill"} {
		id := demoClientOrderID(kind)
		if len(id) >= 36 {
			t.Fatalf("client order id %q length=%d, want <36", id, len(id))
		}
		if !strings.Contains(id, kind) {
			t.Fatalf("client order id %q should include kind %q", id, kind)
		}
	}
}

func TestWaitForDemoRuntimePortfolioNetQtyObservesPortfolioFill(t *testing.T) {
	inst := model.InstrumentID{Venue: "BINANCE", Symbol: "ETH-USDT", Kind: enums.KindPerp}
	node := btruntime.NewNode(btruntime.Clients{}, clock.NewSimulatedClock(time.Now()), "pf")
	node.Portfolio.OnFill(model.Fill{
		InstrumentID: inst,
		Side:         enums.SideBuy,
		Price:        demoD("1500"),
		Quantity:     demoD("0.01"),
	}, enums.PosNet)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := waitForDemoRuntimePortfolioNetQty(ctx, node, inst, demoD("0.01")); err != nil {
		t.Fatalf("waitForDemoRuntimePortfolioNetQty: %v", err)
	}
}

func TestDemoAcceptanceDefaultMaxNotionalIs100USDT(t *testing.T) {
	t.Setenv("BINANCE_DEMO_MAX_NOTIONAL_USDT", "")

	got := demoDecimalEnvOrDefault(t, "BINANCE_DEMO_MAX_NOTIONAL_USDT", demoDefaultMaxNotionalUSDT)
	if !got.Equal(demoD("100")) {
		t.Fatalf("default Demo max notional=%s, want 100", got)
	}
}

func TestDemoAcceptanceCleanupStateArmsBeforeVenueOrderID(t *testing.T) {
	cleanup := newDemoAcceptanceCleanupState("ETHUSDT", demoD("0.002"))

	cleanup.Arm(enums.SideBuy, "bolter-demo-before-submit")

	if !cleanup.Needed() {
		t.Fatalf("cleanup should be needed before a venue order id is known")
	}
	if cleanup.Metadata().Side != "BUY" {
		t.Fatalf("side=%q, want BUY", cleanup.Metadata().Side)
	}
	if got := cleanup.Metadata().ClientOrderIDs; len(got) != 1 || got[0] != "bolter-demo-before-submit" {
		t.Fatalf("client order ids=%v, want pre-submit client id", got)
	}
	if got := cleanup.Metadata().VenueOrderIDs; len(got) != 0 {
		t.Fatalf("venue order ids=%v, want none before submit response", got)
	}

	cleanup.RecordVenueOrderID("12345")
	if got := cleanup.Metadata().VenueOrderIDs; len(got) != 1 || got[0] != "12345" {
		t.Fatalf("venue order ids=%v, want recorded venue id", got)
	}
}

func TestWaitForDemoAccountObservationRequiresMatchingPositionEvent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	events := make(chan contract.AccountEvent, 2)
	events <- contract.BalanceEvent{Balance: model.AccountBalance{Currency: "USDT"}}
	events <- contract.PositionEvent{Position: model.Position{
		InstrumentID: model.InstrumentID{Venue: "BINANCE", Symbol: "ETH-USDT", Kind: enums.KindPerp},
		Quantity:     demoD("0.002"),
	}}

	err := waitForDemoAccountObservation(ctx, events, model.InstrumentID{Venue: "BINANCE", Symbol: "ETH-USDT", Kind: enums.KindPerp}, demoD("0.002"))
	if err != nil {
		t.Fatalf("waitForDemoAccountObservation: %v", err)
	}
}

func TestWaitForDemoAccountObservationTimesOutWithoutMatchingPosition(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	events := make(chan contract.AccountEvent, 1)
	events <- contract.PositionEvent{Position: model.Position{
		InstrumentID: model.InstrumentID{Venue: "BINANCE", Symbol: "BTC-USDT", Kind: enums.KindPerp},
		Quantity:     demoD("0.002"),
	}}

	err := waitForDemoAccountObservation(ctx, events, model.InstrumentID{Venue: "BINANCE", Symbol: "ETH-USDT", Kind: enums.KindPerp}, demoD("0.002"))
	if err == nil || !strings.Contains(err.Error(), "account stream") {
		t.Fatalf("expected account stream timeout, got %v", err)
	}
}
