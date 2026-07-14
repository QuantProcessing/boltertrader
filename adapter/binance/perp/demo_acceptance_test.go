package perp

import (
	"context"
	"errors"
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

const (
	demoUnresolvedLookupAttempts = 4
	demoUnresolvedLookupDelay    = 250 * time.Millisecond
)

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
		t.Fatalf("new Binance Demo adapter: %v", err)
	}
	defer adapter.Close()

	if err := adapter.Start(ctx); err != nil {
		t.Fatalf("start Binance Demo adapter stream: %v", err)
	}

	execEvents := collectDemoExecEvents(adapter.Execution.Events())
	accountEvents := collectDemoAccountEvents(adapter.Account.Events())

	info, err := adapter.rest.ExchangeInfo(ctx)
	if err != nil {
		t.Fatalf("exchange info: %v", err)
	}
	spec, err := demoAcceptanceSymbolSpecFromExchangeInfo(info, symbolInput)
	if err != nil {
		t.Fatalf("resolve Demo symbol: %v", err)
	}
	instID := adapter.provider.resolveVenueSymbol(spec.VenueSymbol)

	mark, err := adapter.rest.MarkPrice(ctx, spec.VenueSymbol)
	if err != nil {
		t.Fatalf("mark price: %v", err)
	}
	refPrice := dec(mark.MarkPrice)
	restingPrice := floorDecimalToStep(refPrice.Mul(decimal.RequireFromString("0.95")), spec.PriceTick)
	if restingPrice.LessThanOrEqual(decimal.Zero) {
		t.Fatalf("computed non-positive resting price %s from reference %s", restingPrice, refPrice)
	}
	fillPrice := ceilDecimalToStep(refPrice.Mul(decimal.RequireFromString("1.01")), spec.PriceTick)
	if fillPrice.LessThanOrEqual(decimal.Zero) {
		t.Fatalf("computed non-positive IOC fill price %s from reference %s", fillPrice, refPrice)
	}
	qty, err := selectDemoAcceptanceOrderQuantityForPriceBand(spec, configuredQty, maxNotional, restingPrice, fillPrice)
	if err != nil {
		t.Fatalf("select safe Demo order quantity: %v", err)
	}

	if open, err := adapter.Execution.OpenOrders(ctx, instID); err != nil {
		t.Fatalf("open order preflight: %v", err)
	} else if len(open) > 0 {
		t.Fatalf("Binance Demo acceptance requires a clean account: %s already has %d open order(s); clean the Demo account before running", spec.VenueSymbol, len(open))
	}
	if exposure, err := demoCurrentExposure(ctx, adapter, instID); err != nil {
		t.Fatalf("position preflight: %v", err)
	} else if !exposure.IsZero() {
		t.Fatalf("Binance Demo acceptance requires a flat account: %s already has exposure %s; flatten the Demo account before running", spec.VenueSymbol, exposure)
	}

	cleanup := newDemoAcceptanceCleanupState(spec.VenueSymbol, qty)
	defer func() {
		if !cleanup.Needed() {
			return
		}
		cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancelCleanup()
		if err := cleanupBinanceDemoAcceptance(cleanupCtx, adapter, instID, spec, maxNotional, &cleanup); err != nil {
			t.Fatalf("%v\n%s", err, cleanup.Metadata().Remediation())
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
		t.Fatalf("submit Demo resting order (outcome ambiguous; only a known venue order can be canceled): %v\n%s", err, cleanup.Metadata().Remediation())
	}
	cleanup.RecordVenueOrderID(resting.VenueOrderID)
	if resting.Status == enums.StatusFilled || !resting.FilledQty.IsZero() {
		cleanup.ConfirmFill(resting.FilledQty)
		t.Fatalf("resting place/cancel order unexpectedly filled: %+v\n%s", resting, cleanup.Metadata().Remediation())
	}
	if err := adapter.Execution.Cancel(ctx, instID, resting.VenueOrderID); err != nil {
		t.Fatalf("cancel Demo resting order %s: %v\n%s", resting.VenueOrderID, err, cleanup.Metadata().Remediation())
	}
	restingFinal, err := waitForDemoOrderStatus(ctx, adapter.rest, spec.VenueSymbol, restingClientID, "CANCELED")
	if err != nil {
		t.Fatalf("wait for resting order cancel: %v\n%s", err, cleanup.Metadata().Remediation())
	}
	cleanup.MarkOrderTerminal(resting.VenueOrderID)
	if partialQty := dec(restingFinal.ExecutedQty); partialQty.IsPositive() {
		cleanup.ConfirmFill(partialQty)
		t.Fatalf("resting order cancellation reported unexpected executed quantity %s\n%s", partialQty, cleanup.Metadata().Remediation())
	}
	if err := waitForNoDemoOpenOrders(ctx, adapter, instID); err != nil {
		t.Fatalf("resting cancellation did not produce authoritative no-open state: %v\n%s", err, cleanup.Metadata().Remediation())
	}

	fillClientID := demoClientOrderID("fill")
	cleanup.Arm(enums.SideBuy, fillClientID)
	filled, err := adapter.Execution.Submit(ctx, demoFillOrderRequest(instID, fillClientID, qty, fillPrice))
	if err != nil {
		t.Fatalf("submit Demo fill order (outcome ambiguous; no automatic close attempted): %v\n%s", err, cleanup.Metadata().Remediation())
	}
	cleanup.RecordVenueOrderID(filled.VenueOrderID)
	filledResp, err := waitForDemoOrderStatus(ctx, adapter.rest, spec.VenueSymbol, fillClientID, binanceDemoTerminalStatuses...)
	if err != nil {
		t.Fatalf("wait for authoritative fill order terminal status: %v\n%s", err, cleanup.Metadata().Remediation())
	}
	filledQty, err := validateBinanceDemoFill(filledResp, maxNotional)
	cleanup.MarkOrderTerminal(filled.VenueOrderID)
	cleanup.ConfirmFill(filledQty)
	if err != nil {
		t.Fatalf("validate bounded Demo fill: %v\n%s", err, cleanup.Metadata().Remediation())
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

	closeClientID := demoClientOrderID("close")
	closed, err := closeBinanceDemoExposure(ctx, adapter, instID, spec, &cleanup, closeClientID)
	if err != nil {
		t.Fatalf("close Demo exposure (not retried because outcome may be ambiguous): %v\n%s", err, cleanup.Metadata().Remediation())
	}
	if closed != nil {
		cleanup.RecordVenueOrderID(closed.VenueOrderID)
	}
	if err := waitForDemoFlat(ctx, adapter, instID); err != nil {
		meta := cleanup.Metadata()
		t.Fatalf("wait for flat Demo position: %v\n%s", err, meta.Remediation())
	}
	if err := waitForNoDemoOpenOrders(ctx, adapter, instID); err != nil {
		meta := cleanup.Metadata()
		t.Fatalf("wait for no Demo open orders: %v\n%s", err, meta.Remediation())
	}
	if closed != nil {
		cleanup.MarkOrderTerminal(closed.VenueOrderID)
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
	return demoExposureFromPositions(positions, id)
}

func cleanupBinanceDemoAcceptance(ctx context.Context, adapter *Adapter, id model.InstrumentID, spec demoAcceptanceSymbolSpec, maxNotional decimal.Decimal, state *demoAcceptanceCleanupState) error {
	initialInspectErr := inspectRecordedDemoOrders(ctx, adapter, spec, state.ResolvedOpenOrders(), maxNotional, false, state)
	resolveErr := resolveUnresolvedDemoOrdersWithRetry(ctx, state, maxNotional, demoUnresolvedLookupAttempts, demoUnresolvedLookupDelay, func(tracked demoAcceptanceTrackedOrder) (*sdkperp.OrderResponse, error) {
		return lookupRecordedDemoOrder(ctx, adapter, spec, tracked)
	})
	remaining := state.TrackedOpenOrders()
	cancelErr := cancelRecordedDemoOrders(
		state,
		func(venueOrderID string) error { return adapter.Execution.Cancel(ctx, id, venueOrderID) },
		func() error { return waitForNoDemoOpenOrders(ctx, adapter, id) },
	)
	inspectErr := inspectRecordedDemoOrders(ctx, adapter, spec, remaining, maxNotional, true, state)
	if len(state.UnresolvedClientOrders()) == 0 {
		resolveErr = nil
	}
	if err := errors.Join(initialInspectErr, resolveErr, cancelErr, inspectErr); err != nil {
		return fmt.Errorf("recorded-order cleanup failed; exposure close was not attempted: %w", err)
	}
	if !state.CloseAuthorized() {
		return nil
	}
	exposure, err := waitForDemoExposure(ctx, adapter, id, state.CloseLimit())
	if err != nil {
		return fmt.Errorf("authoritative fill confirmed but exposure was not observable for bounded cleanup: %w", err)
	}
	state.SetExposure(exposure)
	closeClientID := demoClientOrderID("cleanup-close")
	closed, err := closeBinanceDemoExposure(ctx, adapter, id, spec, state, closeClientID)
	if err != nil {
		return err
	}
	if closed != nil {
		state.RecordVenueOrderID(closed.VenueOrderID)
	}
	if err := waitForNoDemoOpenOrders(ctx, adapter, id); err != nil {
		return err
	}
	if closed != nil {
		state.MarkOrderTerminal(closed.VenueOrderID)
	}
	if err := waitForDemoFlat(ctx, adapter, id); err != nil {
		exposure, _ := demoCurrentExposure(ctx, adapter, id)
		state.SetExposure(exposure)
		return err
	}
	state.SetExposure(decimal.Zero)
	return nil
}

func inspectRecordedDemoOrders(ctx context.Context, adapter *Adapter, spec demoAcceptanceSymbolSpec, orders []demoAcceptanceTrackedOrder, maxNotional decimal.Decimal, requireTerminal bool, state *demoAcceptanceCleanupState) error {
	return inspectRecordedDemoOrdersWithLookup(orders, maxNotional, requireTerminal, state, func(tracked demoAcceptanceTrackedOrder) (*sdkperp.OrderResponse, error) {
		return lookupRecordedDemoOrder(ctx, adapter, spec, tracked)
	})
}

func lookupRecordedDemoOrder(ctx context.Context, adapter *Adapter, spec demoAcceptanceSymbolSpec, tracked demoAcceptanceTrackedOrder) (*sdkperp.OrderResponse, error) {
	orderID := int64(0)
	if tracked.VenueOrderID != "" {
		parsed, err := strconv.ParseInt(tracked.VenueOrderID, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid venue order id %s: %w", tracked.VenueOrderID, err)
		}
		orderID = parsed
	}
	if orderID == 0 && tracked.ClientID == "" {
		return nil, fmt.Errorf("recorded Demo order has neither venue nor client id")
	}
	return adapter.rest.GetOrder(ctx, spec.VenueSymbol, orderID, tracked.ClientID)
}

func resolveUnresolvedDemoOrdersWithRetry(ctx context.Context, state *demoAcceptanceCleanupState, maxNotional decimal.Decimal, attempts int, retryDelay time.Duration, lookup func(demoAcceptanceTrackedOrder) (*sdkperp.OrderResponse, error)) error {
	if attempts < 1 {
		return fmt.Errorf("unresolved Demo order lookup attempts must be positive")
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		unresolved := state.UnresolvedClientOrders()
		if len(unresolved) == 0 {
			return nil
		}
		err := inspectRecordedDemoOrdersWithLookup(unresolved, maxNotional, false, state, lookup)
		if err != nil {
			lastErr = err
		}
		if len(state.UnresolvedClientOrders()) == 0 {
			return err
		} else {
			if err == nil {
				lastErr = fmt.Errorf("client-only Demo order lookup returned no venue identity")
			}
		}
		if attempt == attempts {
			break
		}
		if retryDelay > 0 {
			timer := time.NewTimer(retryDelay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return fmt.Errorf("retry unresolved Demo order lookup: %w", ctx.Err())
			case <-timer.C:
			}
		} else {
			select {
			case <-ctx.Done():
				return fmt.Errorf("retry unresolved Demo order lookup: %w", ctx.Err())
			default:
			}
		}
	}
	return fmt.Errorf("resolve client-only Demo orders after %d attempts: %w", attempts, lastErr)
}

func inspectRecordedDemoOrdersWithLookup(orders []demoAcceptanceTrackedOrder, maxNotional decimal.Decimal, requireTerminal bool, state *demoAcceptanceCleanupState, lookup func(demoAcceptanceTrackedOrder) (*sdkperp.OrderResponse, error)) error {
	var inspectErrs []error
	for _, tracked := range orders {
		resp, err := lookup(tracked)
		if err != nil {
			inspectErrs = append(inspectErrs, fmt.Errorf("inspect recorded Demo order venueID=%s clientID=%s: %w", tracked.VenueOrderID, tracked.ClientID, err))
			continue
		}
		if resp == nil {
			inspectErrs = append(inspectErrs, fmt.Errorf("inspect recorded Demo order venueID=%s clientID=%s returned nil response", tracked.VenueOrderID, tracked.ClientID))
			continue
		}
		venueOrderID := tracked.VenueOrderID
		if resp.OrderID != 0 {
			resolvedVenueOrderID := strconv.FormatInt(resp.OrderID, 10)
			if venueOrderID != "" && venueOrderID != resolvedVenueOrderID {
				inspectErrs = append(inspectErrs, fmt.Errorf("recorded Demo order clientID=%s resolved to venue id %s, want %s", tracked.ClientID, resolvedVenueOrderID, venueOrderID))
				continue
			}
			venueOrderID = resolvedVenueOrderID
			state.ResolveClientOrder(tracked.ClientID, venueOrderID)
		}
		if !isBinanceDemoTerminalStatus(resp.Status) {
			if requireTerminal {
				inspectErrs = append(inspectErrs, fmt.Errorf("recorded Demo order venueID=%s clientID=%s remained non-terminal with status %s after no-open confirmation", venueOrderID, tracked.ClientID, resp.Status))
			}
			continue
		}
		executedQty, err := decimal.NewFromString(resp.ExecutedQty)
		if err != nil {
			inspectErrs = append(inspectErrs, fmt.Errorf("recorded Demo order venueID=%s clientID=%s has invalid executed quantity %q: %w", venueOrderID, tracked.ClientID, resp.ExecutedQty, err))
			continue
		}
		if executedQty.IsPositive() {
			confirmedQty, err := validateBinanceDemoFill(resp, maxNotional)
			state.ConfirmFill(confirmedQty)
			if err != nil {
				inspectErrs = append(inspectErrs, fmt.Errorf("validate recorded Demo order venueID=%s clientID=%s fill: %w", venueOrderID, tracked.ClientID, err))
				continue
			}
		}
		if venueOrderID != "" {
			state.MarkOrderTerminal(venueOrderID)
		} else {
			state.MarkClientOrderTerminal(tracked.ClientID)
		}
	}
	return errors.Join(inspectErrs...)
}

func cancelRecordedDemoOrders(state *demoAcceptanceCleanupState, cancel func(string) error, confirmNoOpen func() error) error {
	venueOrderIDs := state.CancellableVenueOrderIDs()
	var cancelErrs []error
	for _, venueOrderID := range venueOrderIDs {
		if err := cancel(venueOrderID); err != nil {
			cancelErrs = append(cancelErrs, fmt.Errorf("cancel recorded Demo order %s: %w", venueOrderID, err))
		}
	}
	cancelErr := errors.Join(cancelErrs...)
	if err := confirmNoOpen(); err != nil {
		return errors.Join(cancelErr, fmt.Errorf("confirm no Demo open orders after recorded-order cancellation: %w", err))
	}
	for _, venueOrderID := range venueOrderIDs {
		state.MarkOrderTerminal(venueOrderID)
	}
	return nil
}

func closeBinanceDemoExposure(ctx context.Context, adapter *Adapter, id model.InstrumentID, spec demoAcceptanceSymbolSpec, state *demoAcceptanceCleanupState, clientID string) (*model.Order, error) {
	maxCloseQty := state.CloseLimit()
	if !maxCloseQty.IsPositive() {
		return nil, fmt.Errorf("automatic close requires a positive authoritative fill quantity")
	}
	exposure, err := demoCurrentExposure(ctx, adapter, id)
	if err != nil {
		return nil, err
	}
	if exposure.IsZero() {
		return nil, nil
	}
	if exposure.IsNegative() {
		return nil, fmt.Errorf("refusing automatic close of unexpected short exposure %s after a buy lifecycle", exposure)
	}
	if exposure.GreaterThan(maxCloseQty) {
		return nil, fmt.Errorf("refusing automatic close: current exposure %s exceeds authoritative lifecycle fill %s", exposure, maxCloseQty)
	}
	book, err := adapter.Market.OrderBook(ctx, id, 5)
	if err != nil {
		return nil, err
	}
	if len(book.Bids) == 0 {
		return nil, fmt.Errorf("cannot close long exposure: empty bid book")
	}
	price := floorDecimalToStep(book.Bids[0].Price.Mul(decimal.RequireFromString("0.99")), spec.PriceTick)
	state.Arm(enums.SideSell, clientID)
	state.BeginCloseAttempt()
	order, err := adapter.Execution.Submit(ctx, model.OrderRequest{
		InstrumentID: id,
		ClientID:     clientID,
		Side:         enums.SideSell,
		Type:         enums.TypeLimit,
		TIF:          enums.TifIOC,
		Quantity:     exposure,
		Price:        price,
		PositionSide: enums.PosNet,
		ReduceOnly:   true,
	})
	if err != nil {
		return nil, fmt.Errorf("submit single bounded close (outcome ambiguous; not retried): %w", err)
	}
	return order, nil
}
