package spot

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

func TestHyperliquidSpotTestnetReadAcceptance(t *testing.T) {
	cfg := testenv.RequireHyperliquidTestnetRead(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	httpClient, err := testenv.HyperliquidTestnetHTTPClient(30 * time.Second)
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
		testenv.SkipIfTransientLiveNetworkError(t, err, "Hyperliquid Spot Testnet adapter initialization")
		t.Fatalf("new Hyperliquid Spot Testnet adapter: %v", err)
	}
	defer adapter.Close()

	inst := selectSpotTestnetInstrument(t, adapter, cfg.SpotSymbol)
	book, err := adapter.Market.OrderBook(ctx, inst.ID, 5)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Hyperliquid Spot Testnet order book")
		t.Fatalf("order book: %v", err)
	}
	if len(book.Bids) == 0 || len(book.Asks) == 0 {
		t.Fatalf("empty Hyperliquid Spot Testnet book for %s", inst.VenueSymbol)
	}
	if _, err := adapter.Market.Bars(ctx, inst.ID, "1m", 5); err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Hyperliquid Spot Testnet candles")
		t.Fatalf("candles: %v", err)
	}
}

func TestHyperliquidSpotTestnetWriteAcceptance(t *testing.T) {
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
		testenv.SkipIfTransientLiveNetworkError(t, err, "Hyperliquid Spot Testnet adapter initialization")
		t.Fatalf("new Hyperliquid Spot Testnet adapter: %v", err)
	}
	defer adapter.Close()

	inst := selectSpotTestnetInstrument(t, adapter, cfg.SpotSymbol)
	if open, err := adapter.Execution.OpenOrders(ctx, inst.ID); err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Hyperliquid Spot Testnet open order preflight")
		t.Fatalf("open order preflight: %v", err)
	} else if len(open) > 0 {
		t.Skipf("skipping Hyperliquid Spot Testnet write acceptance: %s already has %d open order(s); clean the testnet account first", inst.VenueSymbol, len(open))
	}
	book, err := adapter.Market.OrderBook(ctx, inst.ID, 5)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Hyperliquid Spot Testnet order book")
		t.Fatalf("order book: %v", err)
	}
	if len(book.Bids) == 0 {
		t.Fatalf("empty Hyperliquid Spot Testnet bids for %s", inst.VenueSymbol)
	}
	price := accepttest.RestingBuyPrice(inst, book.Bids[0].Price, true)
	qty := selectHyperliquidTestnetQuantity(inst, cfg.MaxNotionalUSDC, price)
	ensureSpotTestnetCash(t, ctx, adapter, inst, qty, price)

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
		TIF:          enums.TifGTC,
		Quantity:     qty,
		Price:        price,
		PositionSide: enums.PosNet,
	})
	if err != nil {
		t.Fatalf("submit Hyperliquid Spot Testnet resting order: %v", err)
	}
	venueOrderID = order.VenueOrderID
	if order.Status == enums.StatusFilled || !order.FilledQty.IsZero() {
		t.Fatalf("resting place/cancel order unexpectedly filled: %+v", order)
	}
	if err := adapter.Execution.Cancel(ctx, inst.ID, order.VenueOrderID); err != nil {
		t.Fatalf("cancel Hyperliquid Spot Testnet order %s: %v", order.VenueOrderID, err)
	}
	venueOrderID = ""
}

func selectSpotTestnetInstrument(t *testing.T, adapter *Adapter, desired string) *model.Instrument {
	t.Helper()
	all := adapter.Market.InstrumentProvider().All()
	if len(all) == 0 {
		t.Skip("Hyperliquid Spot Testnet returned no spot instruments")
	}
	if desired != "" {
		for _, inst := range all {
			if strings.EqualFold(inst.VenueSymbol, desired) || strings.EqualFold(inst.ID.Symbol, strings.ReplaceAll(desired, "/", "-")) {
				return inst
			}
		}
		t.Fatalf("configured Hyperliquid Spot Testnet symbol %q not loaded", desired)
	}
	return all[0]
}

func selectHyperliquidTestnetQuantity(inst *model.Instrument, maxNotional, price decimal.Decimal) decimal.Decimal {
	step := inst.SizeStep
	if !step.IsPositive() {
		step = decimal.NewFromInt(1)
	}
	targetNotional := maxNotional.Div(decimal.NewFromInt(4))
	if !targetNotional.IsPositive() {
		targetNotional = decimal.NewFromInt(1)
	}
	qty := targetNotional.Div(price)
	return qty.Div(step).Ceil().Mul(step)
}
