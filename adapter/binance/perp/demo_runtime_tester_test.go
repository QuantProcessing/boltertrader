package perp

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	btruntime "github.com/QuantProcessing/boltertrader/runtime"
	"github.com/shopspring/decimal"
)

func newBinanceDemoRuntimeAcceptanceFixture(t *testing.T, ctx context.Context) (*Adapter, demoAcceptanceSymbolSpec, model.InstrumentID, decimal.Decimal, decimal.Decimal, decimal.Decimal, decimal.Decimal) {
	t.Helper()

	adapter, err := New(ctx, Config{
		Demo:          true,
		DemoAPIKey:    os.Getenv("BINANCE_DEMO_API_KEY"),
		DemoAPISecret: os.Getenv("BINANCE_DEMO_API_SECRET"),
	})
	if err != nil {
		t.Fatalf("new Binance Demo runtime adapter: %v", err)
	}

	symbolInput := demoEnvOrDefault("BINANCE_DEMO_SYMBOL", "ETH-USDT")
	maxNotional := demoDecimalEnvOrDefault(t, "BINANCE_DEMO_MAX_NOTIONAL_USDT", demoDefaultMaxNotionalUSDT)
	configuredQty := demoDecimalEnvOrDefault(t, "BINANCE_DEMO_ORDER_QTY", "0")

	info, err := adapter.rest.ExchangeInfo(ctx)
	if err != nil {
		_ = adapter.Close()
		t.Fatalf("exchange info: %v", err)
	}
	spec, err := demoAcceptanceSymbolSpecFromExchangeInfo(info, symbolInput)
	if err != nil {
		_ = adapter.Close()
		t.Fatalf("resolve Demo runtime symbol: %v", err)
	}
	instID := adapter.provider.resolveVenueSymbol(spec.VenueSymbol)

	mark, err := adapter.rest.MarkPrice(ctx, spec.VenueSymbol)
	if err != nil {
		_ = adapter.Close()
		t.Fatalf("mark price: %v", err)
	}
	refPrice := dec(mark.MarkPrice)
	restingPrice := floorDecimalToStep(refPrice.Mul(decimal.RequireFromString("0.95")), spec.PriceTick)
	if restingPrice.LessThanOrEqual(decimal.Zero) {
		_ = adapter.Close()
		t.Fatalf("computed non-positive resting price %s from reference %s", restingPrice, refPrice)
	}
	fillPrice := ceilDecimalToStep(refPrice.Mul(decimal.RequireFromString("1.01")), spec.PriceTick)
	if fillPrice.LessThanOrEqual(decimal.Zero) {
		_ = adapter.Close()
		t.Fatalf("computed non-positive fill price %s from reference %s", fillPrice, refPrice)
	}
	qty, err := selectDemoAcceptanceOrderQuantityForPriceBand(spec, configuredQty, maxNotional, restingPrice, fillPrice)
	if err != nil {
		_ = adapter.Close()
		t.Fatalf("select safe Demo runtime order quantity: %v", err)
	}

	if open, err := adapter.Execution.OpenOrders(ctx, instID); err != nil {
		_ = adapter.Close()
		t.Fatalf("open order preflight: %v", err)
	} else if len(open) > 0 {
		_ = adapter.Close()
		t.Fatalf("Binance Demo runtime acceptance requires a clean account: %s already has %d open order(s); clean the Demo account before running", spec.VenueSymbol, len(open))
	}
	if exposure, err := demoCurrentExposure(ctx, adapter, instID); err != nil {
		_ = adapter.Close()
		t.Fatalf("position preflight: %v", err)
	} else if !exposure.IsZero() {
		_ = adapter.Close()
		t.Fatalf("Binance Demo runtime acceptance requires a flat account: %s already has exposure %s; flatten the Demo account before running", spec.VenueSymbol, exposure)
	}

	return adapter, spec, instID, qty, restingPrice, fillPrice, maxNotional
}

func waitForDemoRuntimePosition(ctx context.Context, node *btruntime.TradingNode, id model.InstrumentID, minAbs decimal.Decimal) error {
	var last decimal.Decimal
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		if position, ok := node.Cache.Position(id, enums.PosNet); ok {
			last = position.Quantity
			if position.Quantity.Abs().GreaterThanOrEqual(minAbs.Abs()) {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for runtime position >= %s; last=%s: %w", minAbs.Abs(), last, ctx.Err())
		case <-ticker.C:
		}
	}
}

func waitForDemoRuntimePortfolioNetQty(ctx context.Context, node *btruntime.TradingNode, id model.InstrumentID, minAbs decimal.Decimal) error {
	var last decimal.Decimal
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		last = node.Portfolio.NetQty(id, enums.PosNet)
		if last.Abs().GreaterThanOrEqual(minAbs.Abs()) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for runtime portfolio net qty >= %s; last=%s: %w", minAbs.Abs(), last, ctx.Err())
		case <-ticker.C:
		}
	}
}

func waitForDemoRuntimePortfolioFlat(ctx context.Context, node *btruntime.TradingNode, id model.InstrumentID) error {
	var last decimal.Decimal
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		last = node.Portfolio.NetQty(id, enums.PosNet)
		if last.IsZero() {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for runtime portfolio flat; last=%s: %w", last, ctx.Err())
		case <-ticker.C:
		}
	}
}
