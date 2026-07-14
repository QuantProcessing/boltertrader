package perp

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

func TestHyperliquidPerpTestnetLifecyclePlansBoundedFillAndReduceOnlyClose(t *testing.T) {
	inst := &model.Instrument{
		ID:          model.InstrumentID{Venue: venueName, Symbol: "BTC-USDC", Kind: enums.KindPerp},
		Base:        "BTC",
		Quote:       "USDC",
		Settle:      "USDC",
		VenueSymbol: "BTC",
		SizeStep:    decimal.RequireFromString("0.001"),
	}
	book := &model.OrderBook{
		InstrumentID: inst.ID,
		Bids:         []model.BookLevel{{Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(10)}},
		Asks:         []model.BookLevel{{Price: decimal.NewFromInt(101), Quantity: decimal.NewFromInt(10)}},
		Timestamp:    time.Now(),
	}
	spec := hyperliquidPerpTestnetLifecycleSpec(t, "Hyperliquid Perp unit", "standard Perp", "HYPERLIQUID:test", inst, book, decimal.NewFromInt(100), nil)

	if !spec.CloseAfterFill || !spec.CloseQuantity.IsZero() {
		t.Fatalf("Perp lifecycle closeAfterFill=%v closeQuantity=%s, want exact observed-fill reduce-only close", spec.CloseAfterFill, spec.CloseQuantity)
	}
	if spec.FillPrice.LessThan(book.Asks[0].Price) || spec.ClosePrice.GreaterThan(book.Bids[0].Price) {
		t.Fatalf("non-crossing IOC prices fill=%s ask=%s close=%s bid=%s", spec.FillPrice, book.Asks[0].Price, spec.ClosePrice, book.Bids[0].Price)
	}
	if spec.Quantity.Mul(spec.FillPrice).GreaterThan(decimal.NewFromInt(100)) {
		t.Fatalf("fill notional=%s exceeds cap", spec.Quantity.Mul(spec.FillPrice))
	}
}

func TestHyperliquidPerpTestnetWritePathsUseFullSharedLifecycle(t *testing.T) {
	adapterSource := readHyperliquidPerpTestnetSource(t, "testnet_acceptance_test.go")
	runtimeSource := readHyperliquidPerpTestnetSource(t, "testnet_runtime_acceptance_test.go")
	for _, want := range []string{"RunAdapterOrderLifecycle", "ConfigurePerpPositionReporter", "requireHyperliquidPrivateLifecycleEvidence", "TestHyperliquidPerpTestnetHIP3WriteAcceptance"} {
		if !strings.Contains(adapterSource, want) {
			t.Fatalf("adapter write acceptance missing %q", want)
		}
	}
	for _, want := range []string{"RunRuntimeOrderLifecycle", "AttachAccountRequiredRiskWithMaxNotional", "ConfigurePerpPositionReporter", "BeforeRuntimeClose"} {
		if !strings.Contains(runtimeSource, want) && !strings.Contains(adapterSource, want) {
			t.Fatalf("runtime write acceptance missing %q", want)
		}
	}
	for name, source := range map[string]string{
		"adapter write section":       sourceFromFunction(adapterSource, "func TestHyperliquidPerpTestnetWriteAcceptance"),
		"HIP-3 adapter write section": sourceFromFunction(adapterSource, "func TestHyperliquidPerpTestnetHIP3WriteAcceptance"),
		"runtime":                     runtimeSource,
	} {
		for _, forbidden := range []string{"SkipIfTransientLiveNetworkError", ".Skip(", ".Skipf("} {
			if strings.Contains(source, forbidden) {
				t.Fatalf("%s contains forbidden post-gate skip %q", name, forbidden)
			}
		}
	}
}

func TestHyperliquidMakefileKeepsHIP3ReadAndAddsSerializedWriteLeaf(t *testing.T) {
	makefile := readHyperliquidPerpTestnetSource(t, "../../../Makefile")
	for _, want := range []string{
		"test-hyperliquid-testnet-hip3:",
		"test-hyperliquid-testnet-hip3-write:",
		"BOLTER_ENABLE_HYPERLIQUID_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestHyperliquidPerpTestnetHIP3WriteAcceptance$$'",
		"test-hyperliquid-testnet-hip3-write test-hyperliquid-testnet-runtime-hip3",
	} {
		if !strings.Contains(makefile, want) {
			t.Fatalf("Makefile missing HIP-3 acceptance contract %q", want)
		}
	}
}

func readHyperliquidPerpTestnetSource(t *testing.T, name string) string {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve Hyperliquid Perp Testnet source path")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(current), name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

func sourceFromFunction(source, signature string) string {
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

func TestSelectHyperliquidPerpTestnetQuantityHonorsVenueMinimums(t *testing.T) {
	inst := &model.Instrument{
		SizeStep:    decimal.RequireFromString("0.1"),
		MinQty:      decimal.NewFromInt(3),
		MinNotional: decimal.NewFromInt(40),
	}
	price := decimal.NewFromInt(10)

	maxNotional := decimal.NewFromInt(100)
	qty, err := selectHyperliquidPerpTestnetQuantity(inst, maxNotional, price)
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

func TestSelectHyperliquidPerpTestnetQuantityRejectsStepAboveMaxNotional(t *testing.T) {
	inst := &model.Instrument{SizeStep: decimal.NewFromInt(1)}
	maxNotional := decimal.NewFromInt(100)
	price := decimal.NewFromInt(101)

	qty, err := selectHyperliquidPerpTestnetQuantity(inst, maxNotional, price)
	if err == nil {
		t.Fatalf("select quantity returned qty=%s, want max-notional error", qty)
	}
	if !qty.IsZero() {
		t.Fatalf("failed selection qty=%s, want zero", qty)
	}
}

func TestSelectHyperliquidPerpTestnetQuantityRejectsVenueMinimumsAboveMaxNotional(t *testing.T) {
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
			qty, err := selectHyperliquidPerpTestnetQuantity(tc.inst, decimal.NewFromInt(100), decimal.NewFromInt(60))
			if err == nil {
				t.Fatalf("select quantity returned qty=%s, want max-notional error", qty)
			}
			if !qty.IsZero() {
				t.Fatalf("failed selection qty=%s, want zero", qty)
			}
		})
	}
}

func TestSelectHyperliquidPerpTestnetQuantityRejectsInvalidInputs(t *testing.T) {
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
			if qty, err := selectHyperliquidPerpTestnetQuantity(tc.inst, tc.maxNotional, tc.price); err == nil || !qty.IsZero() {
				t.Fatalf("select quantity qty=%s err=%v, want zero quantity and error", qty, err)
			}
		})
	}
}

func TestHyperliquidPerpTestnetSettlementCurrencyUsesInstrumentMetadata(t *testing.T) {
	inst := &model.Instrument{Settle: " USDT "}
	got, err := hyperliquidPerpTestnetSettlementCurrency(inst)
	if err != nil {
		t.Fatalf("settlement currency: %v", err)
	}
	if got != "USDT" {
		t.Fatalf("settlement currency=%q, want instrument settle USDT", got)
	}

	for _, invalid := range []*model.Instrument{nil, {}} {
		if got, err := hyperliquidPerpTestnetSettlementCurrency(invalid); err == nil || got != "" {
			t.Fatalf("invalid instrument settlement=%q err=%v, want empty and error", got, err)
		}
	}
}
