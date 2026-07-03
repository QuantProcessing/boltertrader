package perp

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/testenv"
	sdkperp "github.com/QuantProcessing/boltertrader/sdk/binance/perp"
	"github.com/shopspring/decimal"
)

func TestBinanceDemoExecAcceptance(t *testing.T) {
	testenv.RequireBinanceDemoWrite(t)
	runBinanceDemoExecAcceptance(t)
}

const demoDefaultMaxNotionalUSDT = "100"

func runBinanceDemoExecAcceptance(t *testing.T) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	apiKey := os.Getenv("BINANCE_DEMO_API_KEY")
	apiSecret := os.Getenv("BINANCE_DEMO_API_SECRET")
	symbolInput := demoEnvOrDefault("BINANCE_DEMO_SYMBOL", "ETH-USDT")
	maxNotional := demoDecimalEnvOrDefault(t, "BINANCE_DEMO_MAX_NOTIONAL_USDT", demoDefaultMaxNotionalUSDT)
	configuredQty := demoDecimalEnvOrDefault(t, "BINANCE_DEMO_ORDER_QTY", "0")

	adapter, err := New(ctx, Config{
		Demo:          true,
		DemoAPIKey:    apiKey,
		DemoAPISecret: apiSecret,
	})
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Binance Demo adapter initialization")
		t.Fatalf("new Binance Demo adapter: %v", err)
	}
	defer adapter.Close()

	if err := adapter.Start(ctx); err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Binance Demo user-data stream")
		t.Fatalf("start Binance Demo adapter stream: %v", err)
	}

	execEvents := collectDemoExecEvents(adapter.Execution.Events())
	accountEvents := collectDemoAccountEvents(adapter.Account.Events())

	info, err := adapter.rest.ExchangeInfo(ctx)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Binance Demo exchangeInfo")
		t.Fatalf("exchange info: %v", err)
	}
	spec, err := demoAcceptanceSymbolSpecFromExchangeInfo(info, symbolInput)
	if err != nil {
		t.Fatalf("resolve Demo symbol: %v", err)
	}
	instID := adapter.provider.resolveVenueSymbol(spec.VenueSymbol)

	mark, err := adapter.rest.MarkPrice(ctx, spec.VenueSymbol)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Binance Demo mark price")
		t.Fatalf("mark price: %v", err)
	}
	refPrice := dec(mark.MarkPrice)
	restingPrice := floorDecimalToStep(refPrice.Mul(decimal.RequireFromString("0.95")), spec.PriceTick)
	if restingPrice.LessThanOrEqual(decimal.Zero) {
		t.Fatalf("computed non-positive resting price %s from reference %s", restingPrice, refPrice)
	}
	qty, err := selectDemoAcceptanceOrderQuantityForPriceBand(spec, configuredQty, maxNotional, restingPrice, refPrice)
	if err != nil {
		t.Fatalf("select safe Demo order quantity: %v", err)
	}

	if open, err := adapter.Execution.OpenOrders(ctx, instID); err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Binance Demo open order preflight")
		t.Fatalf("open order preflight: %v", err)
	} else if len(open) > 0 {
		t.Skipf("skipping Binance Demo acceptance: %s already has %d open order(s); clean the Demo account before running", spec.VenueSymbol, len(open))
	}
	if exposure, err := demoCurrentExposure(ctx, adapter, instID); err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Binance Demo position preflight")
		t.Fatalf("position preflight: %v", err)
	} else if !exposure.IsZero() {
		t.Skipf("skipping Binance Demo acceptance: %s already has exposure %s; start from a flat Demo account", spec.VenueSymbol, exposure)
	}

	cleanup := newDemoAcceptanceCleanupState(spec.VenueSymbol, qty)
	defer func() {
		if !cleanup.Needed() {
			return
		}
		cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancelCleanup()
		meta := cleanup.Metadata()
		if err := cleanupBinanceDemoAcceptance(cleanupCtx, adapter, instID, &meta); err != nil {
			t.Fatalf("%v\n%s", err, meta.Remediation())
		}
	}()

	restingClientID := demoClientOrderID("rest")
	cleanup.Arm(enums.SideBuy, restingClientID)
	resting, err := adapter.Execution.Submit(ctx, model.OrderRequest{
		InstrumentID: instID,
		ClientID:     restingClientID,
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTX,
		Quantity:     qty,
		Price:        restingPrice,
		PositionSide: enums.PosNet,
	})
	if err != nil {
		t.Fatalf("submit Demo resting order: %v", err)
	}
	cleanup.RecordVenueOrderID(resting.VenueOrderID)
	if resting.Status == enums.StatusFilled || !resting.FilledQty.IsZero() {
		t.Fatalf("resting place/cancel order unexpectedly filled: %+v", resting)
	}
	if err := adapter.Execution.Cancel(ctx, instID, resting.VenueOrderID); err != nil {
		t.Fatalf("cancel Demo resting order %s: %v", resting.VenueOrderID, err)
	}
	if _, err := waitForDemoOrderStatus(ctx, adapter.rest, spec.VenueSymbol, restingClientID, "CANCELED"); err != nil {
		t.Fatalf("wait for resting order cancel: %v", err)
	}

	fillClientID := demoClientOrderID("fill")
	cleanup.Arm(enums.SideBuy, fillClientID)
	filled, err := adapter.Execution.Submit(ctx, model.OrderRequest{
		InstrumentID: instID,
		ClientID:     fillClientID,
		Side:         enums.SideBuy,
		Type:         enums.TypeMarket,
		Quantity:     qty,
		PositionSide: enums.PosNet,
	})
	if err != nil {
		t.Fatalf("submit Demo fill order: %v", err)
	}
	cleanup.RecordVenueOrderID(filled.VenueOrderID)
	filledResp, err := waitForDemoOrderStatus(ctx, adapter.rest, spec.VenueSymbol, fillClientID, "FILLED")
	if err != nil {
		t.Fatalf("wait for fill order: %v", err)
	}
	filledQty := dec(filledResp.ExecutedQty)
	if filledQty.IsZero() {
		t.Fatalf("filled order reported zero executed quantity: %+v", filledResp)
	}
	if err := waitForDemoExecObservation(ctx, execEvents, fillClientID, filled.VenueOrderID); err != nil {
		t.Fatalf("adapter execution stream did not observe Demo fill: %v", err)
	}
	if err := waitForDemoAccountObservation(ctx, accountEvents, instID, filledQty); err != nil {
		t.Fatalf("adapter account stream did not observe Demo position update: %v", err)
	}
	if exposure, err := waitForDemoExposure(ctx, adapter, instID, filledQty); err != nil {
		t.Fatalf("wait for opened Demo position: %v", err)
	} else {
		cleanup.SetExposure(exposure)
	}

	meta := cleanup.Metadata()
	if err := closeBinanceDemoExposure(ctx, adapter, instID, meta.Exposure); err != nil {
		t.Fatalf("close Demo exposure: %v\n%s", err, meta.Remediation())
	}
	if err := waitForDemoFlat(ctx, adapter, instID); err != nil {
		meta := cleanup.Metadata()
		t.Fatalf("wait for flat Demo position: %v\n%s", err, meta.Remediation())
	}
	if err := waitForNoDemoOpenOrders(ctx, adapter, instID); err != nil {
		meta := cleanup.Metadata()
		t.Fatalf("wait for no Demo open orders: %v\n%s", err, meta.Remediation())
	}
	cleanup.MarkClean()
}

func demoEnvOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func demoDecimalEnvOrDefault(t *testing.T, key, fallback string) decimal.Decimal {
	t.Helper()
	value := demoEnvOrDefault(key, fallback)
	d, err := decimal.NewFromString(value)
	if err != nil {
		t.Fatalf("invalid %s=%q: %v", key, value, err)
	}
	return d
}

func demoClientOrderID(kind string) string {
	return fmt.Sprintf("btd-%s-%s", kind, strconv.FormatInt(time.Now().UnixNano(), 36))
}

func collectDemoExecEvents(events <-chan contract.ExecEnvelope) chan contract.ExecEvent {
	out := make(chan contract.ExecEvent, 64)
	go func() {
		for envelope := range events {
			select {
			case out <- envelope.Payload:
			default:
			}
		}
		close(out)
	}()
	return out
}

func collectDemoAccountEvents(events <-chan contract.AccountEnvelope) chan contract.AccountEvent {
	out := make(chan contract.AccountEvent, 64)
	go func() {
		for envelope := range events {
			select {
			case out <- envelope.Payload:
			default:
			}
		}
		close(out)
	}()
	return out
}

func waitForDemoOrderStatus(ctx context.Context, rest *sdkperp.Client, symbol, clientID string, statuses ...string) (*sdkperp.OrderResponse, error) {
	want := make(map[string]struct{}, len(statuses))
	for _, status := range statuses {
		want[strings.ToUpper(status)] = struct{}{}
	}
	var lastErr error
	var lastStatus string
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		order, err := rest.GetOrder(ctx, symbol, 0, clientID)
		if err == nil {
			lastStatus = order.Status
			if _, ok := want[strings.ToUpper(order.Status)]; ok {
				return order, nil
			}
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("timed out waiting for %s to reach %v; lastStatus=%q lastErr=%v: %w", clientID, statuses, lastStatus, lastErr, ctx.Err())
		case <-ticker.C:
		}
	}
}

func waitForDemoExecObservation(ctx context.Context, events <-chan contract.ExecEvent, clientID, venueOrderID string) error {
	timeout, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for {
		select {
		case <-timeout.Done():
			return timeout.Err()
		case event, ok := <-events:
			if !ok {
				return fmt.Errorf("execution event stream closed")
			}
			switch ev := event.(type) {
			case contract.FillEvent:
				if ev.Fill.ClientID == clientID || ev.Fill.VenueOrderID == venueOrderID {
					return nil
				}
			case contract.OrderEvent:
				if ev.Order.Request.ClientID == clientID || ev.Order.VenueOrderID == venueOrderID {
					if ev.Order.Status == enums.StatusFilled || !ev.Order.FilledQty.IsZero() {
						return nil
					}
				}
			}
		}
	}
}

func waitForDemoAccountObservation(ctx context.Context, events <-chan contract.AccountEvent, id model.InstrumentID, minAbs decimal.Decimal) error {
	timeout, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	lastQty := decimal.Zero
	for {
		select {
		case <-timeout.Done():
			return fmt.Errorf("timed out waiting for account stream position update for %s >= %s; lastQty=%s: %w", id, minAbs, lastQty, timeout.Err())
		case event, ok := <-events:
			if !ok {
				return fmt.Errorf("account stream closed")
			}
			position, ok := event.(contract.PositionEvent)
			if !ok || position.Position.InstrumentID != id {
				continue
			}
			lastQty = position.Position.Quantity
			if lastQty.Abs().GreaterThanOrEqual(minAbs) {
				return nil
			}
		}
	}
}

func waitForDemoExposure(ctx context.Context, adapter *Adapter, id model.InstrumentID, minAbs decimal.Decimal) (decimal.Decimal, error) {
	var lastErr error
	var lastExposure decimal.Decimal
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		exposure, err := demoCurrentExposure(ctx, adapter, id)
		if err == nil && exposure.Abs().GreaterThanOrEqual(minAbs) {
			return exposure, nil
		}
		if err != nil {
			lastErr = err
		} else {
			lastExposure = exposure
		}
		select {
		case <-ctx.Done():
			return decimal.Zero, fmt.Errorf("timed out waiting for exposure >= %s; lastExposure=%s lastErr=%v: %w", minAbs, lastExposure, lastErr, ctx.Err())
		case <-ticker.C:
		}
	}
}

func waitForDemoFlat(ctx context.Context, adapter *Adapter, id model.InstrumentID) error {
	var lastErr error
	var lastExposure decimal.Decimal
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		exposure, err := demoCurrentExposure(ctx, adapter, id)
		if err == nil && exposure.IsZero() {
			return nil
		}
		if err != nil {
			lastErr = err
		} else {
			lastExposure = exposure
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for flat position; lastExposure=%s lastErr=%v: %w", lastExposure, lastErr, ctx.Err())
		case <-ticker.C:
		}
	}
}

func waitForNoDemoOpenOrders(ctx context.Context, adapter *Adapter, id model.InstrumentID) error {
	var lastErr error
	var lastOpen int
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		open, err := adapter.Execution.OpenOrders(ctx, id)
		if err == nil && len(open) == 0 {
			return nil
		}
		if err != nil {
			lastErr = err
		} else {
			lastOpen = len(open)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for no open orders; lastOpen=%d lastErr=%v: %w", lastOpen, lastErr, ctx.Err())
		case <-ticker.C:
		}
	}
}

func demoCurrentExposure(ctx context.Context, adapter *Adapter, id model.InstrumentID) (decimal.Decimal, error) {
	positions, err := adapter.Account.Positions(ctx)
	if err != nil {
		return decimal.Zero, err
	}
	exposure := decimal.Zero
	for _, position := range positions {
		if position.InstrumentID == id {
			exposure = exposure.Add(position.Quantity)
		}
	}
	return exposure, nil
}

func cleanupBinanceDemoAcceptance(ctx context.Context, adapter *Adapter, id model.InstrumentID, meta *demoAcceptanceCleanupMetadata) error {
	cancelErr := adapter.Execution.CancelAll(ctx, id)
	exposure, err := demoCurrentExposure(ctx, adapter, id)
	if err != nil {
		return err
	}
	meta.Exposure = exposure
	if !exposure.IsZero() {
		if err := closeBinanceDemoExposure(ctx, adapter, id, exposure); err != nil {
			return err
		}
	}
	if err := waitForNoDemoOpenOrders(ctx, adapter, id); err != nil {
		return err
	}
	if cancelErr != nil {
		return fmt.Errorf("cancel all Demo open orders: %w", cancelErr)
	}
	return waitForDemoFlat(ctx, adapter, id)
}

func closeBinanceDemoExposure(ctx context.Context, adapter *Adapter, id model.InstrumentID, exposure decimal.Decimal) error {
	if exposure.IsZero() {
		return nil
	}
	side := enums.SideSell
	if exposure.IsNegative() {
		side = enums.SideBuy
	}
	_, err := adapter.Execution.Submit(ctx, model.OrderRequest{
		InstrumentID: id,
		ClientID:     demoClientOrderID("close"),
		Side:         side,
		Type:         enums.TypeMarket,
		Quantity:     exposure.Abs(),
		PositionSide: enums.PosNet,
		ReduceOnly:   true,
	})
	return err
}
