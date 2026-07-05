package perp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/adapter/hyperliquid/internal/accepttest"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/testenv"
	sdk "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
	"github.com/shopspring/decimal"
)

func TestHyperliquidPerpTestnetReadAcceptance(t *testing.T) {
	cfg := testenv.RequireHyperliquidTestnetRead(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	httpClient, err := testenv.HyperliquidTestnetHTTPClient(30 * time.Second)
	if err != nil {
		t.Fatalf("Hyperliquid Testnet HTTP client: %v", err)
	}
	adapter, err := New(ctx, Config{
		Environment: sdk.EnvironmentTestnet,
		HTTPClient:  httpClient,
	})
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Hyperliquid Perp Testnet adapter initialization")
		t.Fatalf("new Hyperliquid Perp Testnet adapter: %v", err)
	}
	defer adapter.Close()

	inst := selectPerpTestnetInstrument(t, adapter, cfg.PerpSymbol)
	book, err := adapter.Market.OrderBook(ctx, inst.ID, 5)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Hyperliquid Perp Testnet order book")
		t.Fatalf("order book: %v", err)
	}
	if len(book.Bids) == 0 || len(book.Asks) == 0 {
		t.Fatalf("empty Hyperliquid Perp Testnet book for %s", inst.VenueSymbol)
	}
	if _, err := adapter.Market.Bars(ctx, inst.ID, "1m", 5); err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Hyperliquid Perp Testnet candles")
		t.Fatalf("candles: %v", err)
	}
	market := adapter.Market.(*marketDataClient)
	if funding, err := market.FundingRate(ctx, inst.ID); err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Hyperliquid Perp Testnet funding")
		t.Fatalf("funding: %v", err)
	} else if funding.InstrumentID != inst.ID {
		t.Fatalf("funding instrument=%s, want %s", funding.InstrumentID, inst.ID)
	}
}

func TestHyperliquidPerpTestnetHIP3ReadAcceptance(t *testing.T) {
	cfg := testenv.RequireHyperliquidTestnetRead(t)
	if cfg.HIP3Symbol == "" {
		t.Skipf("skipping Hyperliquid HIP-3 Testnet acceptance: set %s to a dex-qualified symbol such as dex:coin or dex:coin-USDC", testenv.HyperliquidTestnetHIP3SymbolEnv)
	}
	dex, _, ok := strings.Cut(cfg.HIP3Symbol, ":")
	if !ok || dex == "" {
		t.Fatalf("%s must include a dex qualifier, got %q", testenv.HyperliquidTestnetHIP3SymbolEnv, cfg.HIP3Symbol)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	httpClient, err := testenv.HyperliquidTestnetHTTPClient(30 * time.Second)
	if err != nil {
		t.Fatalf("Hyperliquid Testnet HTTP client: %v", err)
	}
	adapter, err := New(ctx, Config{
		Environment: sdk.EnvironmentTestnet,
		HTTPClient:  httpClient,
		IncludeHIP3: true,
		HIP3Dexes:   []string{dex},
	})
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Hyperliquid HIP-3 Testnet adapter initialization")
		t.Fatalf("new Hyperliquid HIP-3 Testnet adapter: %v", err)
	}
	defer adapter.Close()

	inst := selectPerpTestnetInstrument(t, adapter, cfg.HIP3Symbol)
	book, err := adapter.Market.OrderBook(ctx, inst.ID, 5)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Hyperliquid HIP-3 Testnet order book")
		t.Fatalf("order book: %v", err)
	}
	if len(book.Bids) == 0 || len(book.Asks) == 0 {
		t.Fatalf("empty Hyperliquid HIP-3 Testnet book for %s", inst.VenueSymbol)
	}
}

func TestHyperliquidPerpTestnetWriteAcceptance(t *testing.T) {
	cfg := testenv.RequireHyperliquidTestnetWrite(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	httpClient, err := testenv.HyperliquidTestnetHTTPClient(45 * time.Second)
	if err != nil {
		t.Fatalf("Hyperliquid Testnet HTTP client: %v", err)
	}
	adapter, err := New(ctx, Config{
		PrivateKey:     cfg.PrivateKey,
		AccountAddress: cfg.AccountAddress,
		VaultAddress:   cfg.VaultAddress,
		Environment:    sdk.EnvironmentTestnet,
		HTTPClient:     httpClient,
	})
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Hyperliquid Perp Testnet adapter initialization")
		t.Fatalf("new Hyperliquid Perp Testnet adapter: %v", err)
	}
	defer adapter.Close()

	inst := selectPerpTestnetInstrument(t, adapter, cfg.PerpSymbol)
	if open, err := adapter.Execution.OpenOrders(ctx, inst.ID); err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Hyperliquid Perp Testnet open order preflight")
		t.Fatalf("open order preflight: %v", err)
	} else if len(open) > 0 {
		t.Skipf("skipping Hyperliquid Perp Testnet write acceptance: %s already has %d open order(s); clean the testnet account first", inst.VenueSymbol, len(open))
	}
	book, err := adapter.Market.OrderBook(ctx, inst.ID, 5)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Hyperliquid Perp Testnet order book")
		t.Fatalf("order book: %v", err)
	}
	if len(book.Bids) == 0 {
		t.Fatalf("empty Hyperliquid Perp Testnet bids for %s", inst.VenueSymbol)
	}
	price := accepttest.RestingBuyPrice(inst, book.Bids[0].Price, false)
	qty := selectHyperliquidPerpTestnetQuantity(inst, cfg.MaxNotionalUSDC, price)

	var venueOrderID string
	defer func() {
		if venueOrderID != "" {
			_ = adapter.Execution.Cancel(context.Background(), inst.ID, venueOrderID)
		}
	}()
	order, err := adapter.Execution.Submit(ctx, model.OrderRequest{
		InstrumentID: inst.ID,
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTX,
		Quantity:     qty,
		Price:        price,
		PositionSide: enums.PosNet,
	})
	if err != nil {
		t.Fatalf("submit Hyperliquid Perp Testnet resting order: %v", err)
	}
	venueOrderID = order.VenueOrderID
	if order.Status == enums.StatusFilled || !order.FilledQty.IsZero() {
		t.Fatalf("resting place/cancel order unexpectedly filled: %+v", order)
	}
	if err := adapter.Execution.Cancel(ctx, inst.ID, order.VenueOrderID); err != nil {
		t.Fatalf("cancel Hyperliquid Perp Testnet order %s: %v", order.VenueOrderID, err)
	}
	venueOrderID = ""
}

func selectPerpTestnetInstrument(t *testing.T, adapter *Adapter, desired string) *model.Instrument {
	t.Helper()
	all := adapter.Market.InstrumentProvider().All()
	if len(all) == 0 {
		t.Skip("Hyperliquid Perp Testnet returned no perp instruments")
	}
	if desired != "" {
		for _, inst := range all {
			if matchesPerpTestnetSymbol(inst, desired) {
				return inst
			}
		}
		t.Fatalf("configured Hyperliquid Perp Testnet symbol %q not loaded", desired)
	}
	return all[0]
}

func matchesPerpTestnetSymbol(inst *model.Instrument, desired string) bool {
	if inst == nil {
		return false
	}
	desired = strings.TrimSpace(desired)
	if desired == "" {
		return false
	}
	if strings.EqualFold(inst.VenueSymbol, desired) || strings.EqualFold(inst.ID.Symbol, desired) {
		return true
	}
	withoutKind := strings.TrimSuffix(desired, "-PERP")
	if strings.EqualFold(inst.ID.Symbol, withoutKind) {
		return true
	}
	if inst.Settle != "" {
		rawFromNeutral := strings.TrimSuffix(withoutKind, "-"+inst.Settle)
		if rawFromNeutral != withoutKind && strings.EqualFold(inst.VenueSymbol, rawFromNeutral) {
			return true
		}
	}
	legacyNeutral := strings.ReplaceAll(desired, ":", "-")
	return strings.EqualFold(inst.ID.Symbol, legacyNeutral)
}

func TestMatchesPerpTestnetSymbolAcceptsHIP3RawAndNeutralForms(t *testing.T) {
	inst := &model.Instrument{
		ID:          model.InstrumentID{Venue: venueName, Symbol: "xyz:TSLA-USDC", Kind: enums.KindPerp},
		VenueSymbol: "xyz:TSLA",
		Settle:      "USDC",
	}
	for _, desired := range []string{"xyz:TSLA", "xyz:TSLA-USDC", "xyz:TSLA-USDC-PERP"} {
		if !matchesPerpTestnetSymbol(inst, desired) {
			t.Fatalf("desired %q did not match %+v", desired, inst)
		}
	}
	if matchesPerpTestnetSymbol(inst, "stocks:TSLA-USDC") {
		t.Fatalf("different HIP-3 dex must not match")
	}
}

func selectHyperliquidPerpTestnetQuantity(inst *model.Instrument, maxNotional, price decimal.Decimal) decimal.Decimal {
	step := inst.SizeStep
	if !step.IsPositive() {
		step = decimal.RequireFromString("0.0001")
	}
	targetNotional := maxNotional.Div(decimal.NewFromInt(4))
	if !targetNotional.IsPositive() {
		targetNotional = decimal.NewFromInt(1)
	}
	qty := targetNotional.Div(price)
	return qty.Div(step).Ceil().Mul(step)
}
