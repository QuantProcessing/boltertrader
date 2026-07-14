package spot

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/shopspring/decimal"
)

func TestHyperliquidSpotTestnetLifecyclePlansBoundedFillAndCleanup(t *testing.T) {
	inst := &model.Instrument{
		ID:          model.InstrumentID{Venue: venueName, Symbol: "PURR-USDC", Kind: enums.KindSpot},
		Base:        "PURR",
		Quote:       "USDC",
		VenueSymbol: "PURR/USDC",
		SizeStep:    decimal.RequireFromString("0.1"),
	}
	book := &model.OrderBook{
		InstrumentID: inst.ID,
		Bids:         []model.BookLevel{{Price: decimal.NewFromInt(10), Quantity: decimal.NewFromInt(10)}},
		Asks:         []model.BookLevel{{Price: decimal.RequireFromString("10.1"), Quantity: decimal.NewFromInt(10)}},
		Timestamp:    time.Now(),
	}
	spec := hyperliquidSpotTestnetLifecycleSpec(t, "Hyperliquid Spot unit", "HYPERLIQUID:test", inst, book, decimal.NewFromInt(100), nil)

	if !spec.CloseAfterFill {
		t.Fatal("Spot lifecycle must fill and close instead of stopping after resting cancel")
	}
	if spec.FillPrice.LessThan(book.Asks[0].Price) {
		t.Fatalf("fill price=%s does not cross ask=%s", spec.FillPrice, book.Asks[0].Price)
	}
	if spec.ClosePrice.GreaterThan(book.Bids[0].Price) {
		t.Fatalf("close price=%s does not cross bid=%s", spec.ClosePrice, book.Bids[0].Price)
	}
	if !spec.CloseQuantity.IsPositive() || !spec.CloseQuantity.LessThan(spec.Quantity) || !spec.CloseQuantity.Mod(inst.SizeStep).IsZero() {
		t.Fatalf("close quantity=%s buy quantity=%s, want step-aligned fee buffer", spec.CloseQuantity, spec.Quantity)
	}
	if spec.Quantity.Mul(spec.FillPrice).GreaterThan(decimal.NewFromInt(100)) {
		t.Fatalf("fill notional=%s exceeds cap", spec.Quantity.Mul(spec.FillPrice))
	}
}

func TestHyperliquidSpotTestnetWritePathsUseFullSharedLifecycle(t *testing.T) {
	adapterSource := readHyperliquidSpotTestnetSource(t, "testnet_acceptance_test.go")
	runtimeSource := readHyperliquidSpotTestnetSource(t, "testnet_runtime_acceptance_test.go")
	for name, source := range map[string]string{
		"adapter": sourceFromHyperliquidSpotFunction(adapterSource, "func TestHyperliquidSpotTestnetWriteAcceptance"),
		"runtime": runtimeSource,
	} {
		for _, forbidden := range []string{"SkipIfTransientLiveNetworkError", ".Skip(", ".Skipf("} {
			if strings.Contains(source, forbidden) {
				t.Fatalf("%s write path contains forbidden post-gate skip %q", name, forbidden)
			}
		}
	}
	for _, want := range []string{"RunAdapterOrderLifecycle", "ConfigureSpotBalanceGuard", "requireHyperliquidPrivateLifecycleEvidence"} {
		if !strings.Contains(adapterSource, want) {
			t.Fatalf("adapter write acceptance missing %q", want)
		}
	}
	for _, want := range []string{"RunRuntimeOrderLifecycle", "AttachAccountRequiredRiskWithMaxNotional", "ConfigureSpotBalanceGuard"} {
		if !strings.Contains(runtimeSource, want) && !strings.Contains(adapterSource, want) {
			t.Fatalf("runtime write acceptance missing %q", want)
		}
	}
	if strings.Contains(runtimeSource, "Portfolio.NetQty") {
		t.Fatal("Spot runtime acceptance must not require an absolute flat fill-derived Portfolio after replaying historical userFills snapshots")
	}
}

func sourceFromHyperliquidSpotFunction(source, signature string) string {
	start := strings.Index(source, signature)
	if start < 0 {
		return ""
	}
	rest := source[start+len(signature):]
	if next := strings.Index(rest, "\nfunc "); next >= 0 {
		return source[start : start+len(signature)+next]
	}
	return source[start:]
}

func readHyperliquidSpotTestnetSource(t *testing.T, name string) string {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve Hyperliquid Spot Testnet source path")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(current), name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

func TestSelectHyperliquidTestnetQuantityHonorsVenueMinimums(t *testing.T) {
	inst := &model.Instrument{
		SizeStep:    decimal.RequireFromString("0.1"),
		MinQty:      decimal.NewFromInt(3),
		MinNotional: decimal.NewFromInt(40),
	}
	price := decimal.NewFromInt(10)

	maxNotional := decimal.NewFromInt(100)
	qty, err := selectHyperliquidTestnetQuantity(inst, maxNotional, price)
	if err != nil {
		t.Fatalf("select quantity: %v", err)
	}
	if qty.LessThan(inst.MinQty) {
		t.Fatalf("qty=%s below min quantity %s", qty, inst.MinQty)
	}
	if qty.Mul(price).LessThan(inst.MinNotional) {
		t.Fatalf("qty=%s notional=%s below min notional %s", qty, qty.Mul(price), inst.MinNotional)
	}
	if !qty.Mod(inst.SizeStep).IsZero() {
		t.Fatalf("qty=%s is not aligned to step %s", qty, inst.SizeStep)
	}
	if qty.Mul(price).GreaterThan(maxNotional) {
		t.Fatalf("qty=%s notional=%s exceeds max notional %s", qty, qty.Mul(price), maxNotional)
	}
}

func TestSelectHyperliquidTestnetQuantityRejectsStepAboveMaxNotional(t *testing.T) {
	inst := &model.Instrument{SizeStep: decimal.NewFromInt(1)}
	maxNotional := decimal.NewFromInt(100)
	price := decimal.NewFromInt(101)

	qty, err := selectHyperliquidTestnetQuantity(inst, maxNotional, price)
	if err == nil {
		t.Fatalf("select quantity returned qty=%s, want max-notional error", qty)
	}
	if !qty.IsZero() {
		t.Fatalf("failed selection qty=%s, want zero", qty)
	}
}

func TestSelectHyperliquidTestnetQuantityRejectsVenueMinimumsAboveMaxNotional(t *testing.T) {
	for _, tc := range []struct {
		name string
		inst *model.Instrument
	}{
		{
			name: "minimum quantity",
			inst: &model.Instrument{SizeStep: decimal.NewFromInt(1), MinQty: decimal.NewFromInt(2)},
		},
		{
			name: "minimum notional after step rounding",
			inst: &model.Instrument{SizeStep: decimal.NewFromInt(1), MinNotional: decimal.NewFromInt(95)},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			qty, err := selectHyperliquidTestnetQuantity(tc.inst, decimal.NewFromInt(100), decimal.NewFromInt(60))
			if err == nil {
				t.Fatalf("select quantity returned qty=%s, want max-notional error", qty)
			}
			if !qty.IsZero() {
				t.Fatalf("failed selection qty=%s, want zero", qty)
			}
		})
	}
}

func TestSelectHyperliquidTestnetQuantityRejectsInvalidInputs(t *testing.T) {
	validInst := &model.Instrument{SizeStep: decimal.RequireFromString("0.1")}
	for _, tc := range []struct {
		name        string
		inst        *model.Instrument
		maxNotional decimal.Decimal
		price       decimal.Decimal
	}{
		{name: "nil instrument", maxNotional: decimal.NewFromInt(100), price: decimal.NewFromInt(10)},
		{name: "zero max notional", inst: validInst, price: decimal.NewFromInt(10)},
		{name: "negative max notional", inst: validInst, maxNotional: decimal.NewFromInt(-1), price: decimal.NewFromInt(10)},
		{name: "zero price", inst: validInst, maxNotional: decimal.NewFromInt(100)},
		{name: "negative price", inst: validInst, maxNotional: decimal.NewFromInt(100), price: decimal.NewFromInt(-1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if qty, err := selectHyperliquidTestnetQuantity(tc.inst, tc.maxNotional, tc.price); err == nil || !qty.IsZero() {
				t.Fatalf("select quantity qty=%s err=%v, want zero quantity and error", qty, err)
			}
		})
	}
}
