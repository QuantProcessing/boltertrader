package spot

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	sdkspot "github.com/QuantProcessing/boltertrader/sdk/binance/spot"
	"github.com/shopspring/decimal"
)

var binanceSpotDemoTerminalStatuses = []string{"FILLED", "CANCELED", "EXPIRED", "EXPIRED_IN_MATCH", "REJECTED"}

func isBinanceSpotDemoTerminalStatus(status string) bool {
	switch strings.ToUpper(status) {
	case "FILLED", "CANCELED", "EXPIRED", "EXPIRED_IN_MATCH", "REJECTED":
		return true
	default:
		return false
	}
}

const demoDefaultMaxNotionalUSDT = "100"

const (
	demoUnresolvedLookupAttempts = 4
	demoUnresolvedLookupDelay    = 250 * time.Millisecond
)

type demoAcceptanceSymbolSpec struct {
	VenueSymbol   string
	BaseCurrency  string
	QuoteCurrency string
	PriceTick     decimal.Decimal
	SizeStep      decimal.Decimal
	MinQty        decimal.Decimal
	MinNotional   decimal.Decimal
}

func normalizeDemoAcceptanceSymbol(symbol string) string {
	replacer := strings.NewReplacer("-", "", "_", "", "/", "", " ", "")
	return strings.ToUpper(replacer.Replace(symbol))
}

func demoAcceptanceSymbolSpecFromExchangeInfo(info *sdkspot.ExchangeInfoResponse, symbol string) (demoAcceptanceSymbolSpec, error) {
	if info == nil {
		return demoAcceptanceSymbolSpec{}, fmt.Errorf("missing exchange info")
	}
	want := normalizeDemoAcceptanceSymbol(symbol)
	for _, candidate := range info.Symbols {
		if normalizeDemoAcceptanceSymbol(candidate.Symbol) != want {
			continue
		}
		tick, step, minQty, minNotional := extractFilters(candidate.Filters)
		spec := demoAcceptanceSymbolSpec{
			VenueSymbol:   candidate.Symbol,
			BaseCurrency:  candidate.BaseAsset,
			QuoteCurrency: candidate.QuoteAsset,
			PriceTick:     tick,
			SizeStep:      step,
			MinQty:        minQty,
			MinNotional:   minNotional,
		}
		if spec.PriceTick.IsZero() || spec.SizeStep.IsZero() || spec.MinQty.IsZero() || spec.MinNotional.IsZero() {
			return demoAcceptanceSymbolSpec{}, fmt.Errorf("symbol %s has incomplete exchange filters: %+v", candidate.Symbol, spec)
		}
		return spec, nil
	}
	return demoAcceptanceSymbolSpec{}, fmt.Errorf("symbol %s not found in exchange info", want)
}

func selectDemoAcceptanceOrderQuantity(spec demoAcceptanceSymbolSpec, configuredQty, maxNotional, refPrice decimal.Decimal) (decimal.Decimal, error) {
	return selectDemoAcceptanceOrderQuantityForPriceBand(spec, configuredQty, maxNotional, refPrice, refPrice)
}

func selectDemoAcceptanceOrderQuantityForPriceBand(spec demoAcceptanceSymbolSpec, configuredQty, maxNotional, minNotionalPrice, maxNotionalPrice decimal.Decimal) (decimal.Decimal, error) {
	if spec.SizeStep.LessThanOrEqual(decimal.Zero) {
		return decimal.Zero, fmt.Errorf("symbol %s has invalid size step %s", spec.VenueSymbol, spec.SizeStep)
	}
	if maxNotional.LessThanOrEqual(decimal.Zero) {
		return decimal.Zero, fmt.Errorf("max notional must be positive")
	}
	if minNotionalPrice.LessThanOrEqual(decimal.Zero) {
		return decimal.Zero, fmt.Errorf("min-notional reference price must be positive")
	}
	if maxNotionalPrice.LessThanOrEqual(decimal.Zero) {
		return decimal.Zero, fmt.Errorf("max-notional reference price must be positive")
	}

	qty := configuredQty
	if qty.IsZero() {
		minByNotional := spec.MinNotional.Div(minNotionalPrice)
		qty = ceilDecimalToStep(maxDecimal(spec.MinQty, minByNotional), spec.SizeStep)
	} else {
		if qty.LessThan(spec.MinQty) {
			return decimal.Zero, fmt.Errorf("configured quantity %s below min quantity %s for %s", qty, spec.MinQty, spec.VenueSymbol)
		}
		if !qty.Equal(ceilDecimalToStep(qty, spec.SizeStep)) {
			return decimal.Zero, fmt.Errorf("configured quantity %s is not aligned to step %s for %s", qty, spec.SizeStep, spec.VenueSymbol)
		}
	}

	minCheckedNotional := qty.Mul(minNotionalPrice)
	if minCheckedNotional.LessThan(spec.MinNotional) {
		return decimal.Zero, fmt.Errorf("quantity %s notional %s below min notional %s for %s", qty, minCheckedNotional, spec.MinNotional, spec.VenueSymbol)
	}
	maxCheckedNotional := qty.Mul(maxNotionalPrice)
	if maxCheckedNotional.GreaterThan(maxNotional) {
		return decimal.Zero, fmt.Errorf("quantity %s notional %s exceeds max notional %s for %s", qty, maxCheckedNotional, maxNotional, spec.VenueSymbol)
	}
	return qty, nil
}

func ceilDecimalToStep(value, step decimal.Decimal) decimal.Decimal {
	if value.IsZero() || step.IsZero() {
		return value
	}
	return value.Div(step).Ceil().Mul(step)
}

func floorDecimalToStep(value, step decimal.Decimal) decimal.Decimal {
	if value.IsZero() || step.IsZero() {
		return value
	}
	return value.Div(step).Floor().Mul(step)
}

func maxDecimal(a, b decimal.Decimal) decimal.Decimal {
	if a.GreaterThan(b) {
		return a
	}
	return b
}

func demoEnvOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func demoDecimalEnvOrDefault(t testingTB, key, fallback string) decimal.Decimal {
	t.Helper()
	value := demoEnvOrDefault(key, fallback)
	d, err := decimal.NewFromString(value)
	if err != nil {
		t.Fatalf("invalid %s=%q: %v", key, value, err)
	}
	return d
}

type testingTB interface {
	Helper()
	Fatalf(format string, args ...any)
}

func demoClientOrderID(kind string) string {
	return fmt.Sprintf("btds-%s-%s", kind, strconv.FormatInt(time.Now().UnixNano(), 36))
}

func demoHTTPClient(timeout time.Duration) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	if proxy := os.Getenv("PROXY"); proxy != "" {
		parsed, err := url.Parse(proxy)
		if err != nil {
			return nil, fmt.Errorf("invalid PROXY configuration")
		}
		transport.Proxy = http.ProxyURL(parsed)
	}
	return &http.Client{Timeout: timeout, Transport: transport}, nil
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

func waitForDemoSpotOrderStatus(ctx context.Context, rest *sdkspot.Client, symbol, clientID string, statuses ...string) (*sdkspot.OrderResponse, error) {
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
	timeout, cancel := context.WithTimeout(ctx, 20*time.Second)
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

func waitForDemoSpotBalanceObservation(ctx context.Context, events <-chan contract.AccountEvent, currency string, startTotal, minDelta decimal.Decimal) error {
	timeout, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var last decimal.Decimal
	for {
		select {
		case <-timeout.Done():
			return fmt.Errorf("timed out waiting for balance stream %s delta >= %s; last=%s: %w", currency, minDelta, last, timeout.Err())
		case event, ok := <-events:
			if !ok {
				return fmt.Errorf("balance stream closed")
			}
			balance, ok := event.(contract.BalanceEvent)
			if !ok || balance.Balance.Currency != currency {
				continue
			}
			last = balance.Balance.Total
			if last.Sub(startTotal).Abs().GreaterThanOrEqual(minDelta.Abs()) {
				return nil
			}
		}
	}
}

func waitForDemoSpotBalanceDelta(ctx context.Context, adapter *Adapter, currency string, startTotal, minDelta decimal.Decimal) (decimal.Decimal, error) {
	var lastErr error
	last := startTotal
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		balances, err := demoSpotBalances(ctx, adapter)
		if err == nil {
			last = balances[currency].Total
			if last.Sub(startTotal).Abs().GreaterThanOrEqual(minDelta.Abs()) {
				return last.Sub(startTotal), nil
			}
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return decimal.Zero, fmt.Errorf("timed out waiting for %s balance delta >= %s; last=%s lastErr=%v: %w", currency, minDelta, last.Sub(startTotal), lastErr, ctx.Err())
		case <-ticker.C:
		}
	}
}

func demoSpotFillObservationThreshold(filledQty, sizeStep decimal.Decimal) decimal.Decimal {
	threshold := filledQty.Abs().Div(decimal.NewFromInt(2))
	halfStep := sizeStep.Abs().Div(decimal.NewFromInt(2))
	if halfStep.IsPositive() && threshold.GreaterThan(halfStep) {
		return halfStep
	}
	return threshold
}

func demoSpotBalances(ctx context.Context, adapter *Adapter) (map[string]model.AccountBalance, error) {
	balances, err := adapter.Account.Balances(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]model.AccountBalance, len(balances))
	for _, balance := range balances {
		out[balance.Currency] = balance
	}
	return out, nil
}

type demoAcceptanceCleanupMetadata struct {
	Symbol         string
	Side           string
	Quantity       decimal.Decimal
	VenueOrderIDs  []string
	ClientOrderIDs []string
	BaseCurrency   string
	QuoteCurrency  string
	BaseDelta      decimal.Decimal
}

func (m demoAcceptanceCleanupMetadata) Remediation() string {
	return fmt.Sprintf(
		"Binance Spot Demo cleanup failed: symbol=%s side=%s quantity=%s base=%s quote=%s baseDelta=%s venueOrderIDs=%s clientOrderIDs=%s. Manually cancel open Spot Demo orders and sell any unexpected base-asset test balance delta.",
		m.Symbol,
		m.Side,
		m.Quantity,
		m.BaseCurrency,
		m.QuoteCurrency,
		m.BaseDelta,
		strings.Join(m.VenueOrderIDs, ","),
		strings.Join(m.ClientOrderIDs, ","),
	)
}

type demoAcceptanceCleanupState struct {
	needed                 bool
	meta                   demoAcceptanceCleanupMetadata
	openVenueOrders        map[string]struct{}
	clientIDByVenueOrderID map[string]string
	unresolvedClientIDs    map[string]struct{}
	armedClientID          string
	confirmedFill          decimal.Decimal
	closeAttempted         bool
}

type demoAcceptanceTrackedOrder struct {
	VenueOrderID string
	ClientID     string
}

func newDemoAcceptanceCleanupState(spec demoAcceptanceSymbolSpec, qty decimal.Decimal) demoAcceptanceCleanupState {
	return demoAcceptanceCleanupState{
		meta: demoAcceptanceCleanupMetadata{
			Symbol:        spec.VenueSymbol,
			Quantity:      qty,
			BaseCurrency:  spec.BaseCurrency,
			QuoteCurrency: spec.QuoteCurrency,
		},
		openVenueOrders:        make(map[string]struct{}),
		clientIDByVenueOrderID: make(map[string]string),
		unresolvedClientIDs:    make(map[string]struct{}),
	}
}

func (s *demoAcceptanceCleanupState) Arm(side enums.OrderSide, clientID string) {
	s.needed = true
	s.meta.Side = side.String()
	if clientID != "" {
		s.meta.ClientOrderIDs = append(s.meta.ClientOrderIDs, clientID)
		s.armedClientID = clientID
		s.unresolvedClientIDs[clientID] = struct{}{}
	}
}

func (s *demoAcceptanceCleanupState) RecordVenueOrderID(venueOrderID string) {
	s.ResolveClientOrder(s.armedClientID, venueOrderID)
}

func (s *demoAcceptanceCleanupState) ResolveClientOrder(clientID, venueOrderID string) {
	if venueOrderID == "" {
		return
	}
	if _, exists := s.clientIDByVenueOrderID[venueOrderID]; !exists {
		s.meta.VenueOrderIDs = append(s.meta.VenueOrderIDs, venueOrderID)
	}
	s.openVenueOrders[venueOrderID] = struct{}{}
	s.clientIDByVenueOrderID[venueOrderID] = clientID
	delete(s.unresolvedClientIDs, clientID)
}

func (s *demoAcceptanceCleanupState) MarkOrderTerminal(venueOrderID string) {
	delete(s.unresolvedClientIDs, s.clientIDByVenueOrderID[venueOrderID])
	delete(s.openVenueOrders, venueOrderID)
}

func (s *demoAcceptanceCleanupState) MarkClientOrderTerminal(clientID string) {
	delete(s.unresolvedClientIDs, clientID)
}

func (s *demoAcceptanceCleanupState) ConfirmFill(qty decimal.Decimal) {
	if qty.IsPositive() {
		if qty.GreaterThan(s.confirmedFill) {
			s.confirmedFill = qty
		}
		s.needed = true
	}
}

func (s demoAcceptanceCleanupState) CloseAuthorized() bool {
	return s.confirmedFill.IsPositive() && !s.closeAttempted
}

func (s demoAcceptanceCleanupState) CloseLimit() decimal.Decimal {
	return s.confirmedFill
}

func (s *demoAcceptanceCleanupState) BeginCloseAttempt() {
	s.closeAttempted = true
}

func (s demoAcceptanceCleanupState) CancellableVenueOrderIDs() []string {
	ids := make([]string, 0, len(s.openVenueOrders))
	for id := range s.openVenueOrders {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (s demoAcceptanceCleanupState) TrackedOpenOrders() []demoAcceptanceTrackedOrder {
	orders := s.ResolvedOpenOrders()
	orders = append(orders, s.UnresolvedClientOrders()...)
	return orders
}

func (s demoAcceptanceCleanupState) ResolvedOpenOrders() []demoAcceptanceTrackedOrder {
	venueOrderIDs := s.CancellableVenueOrderIDs()
	orders := make([]demoAcceptanceTrackedOrder, 0, len(venueOrderIDs))
	for _, venueOrderID := range venueOrderIDs {
		orders = append(orders, demoAcceptanceTrackedOrder{
			VenueOrderID: venueOrderID,
			ClientID:     s.clientIDByVenueOrderID[venueOrderID],
		})
	}
	return orders
}

func (s demoAcceptanceCleanupState) UnresolvedClientOrders() []demoAcceptanceTrackedOrder {
	clientIDs := make([]string, 0, len(s.unresolvedClientIDs))
	for clientID := range s.unresolvedClientIDs {
		clientIDs = append(clientIDs, clientID)
	}
	sort.Strings(clientIDs)
	orders := make([]demoAcceptanceTrackedOrder, 0, len(clientIDs))
	for _, clientID := range clientIDs {
		orders = append(orders, demoAcceptanceTrackedOrder{ClientID: clientID})
	}
	return orders
}

func (s *demoAcceptanceCleanupState) SetBaseDelta(delta decimal.Decimal) {
	s.meta.BaseDelta = delta
}

func (s *demoAcceptanceCleanupState) MarkClean() {
	s.needed = false
	s.meta.BaseDelta = decimal.Zero
	s.confirmedFill = decimal.Zero
	s.closeAttempted = false
	s.armedClientID = ""
	clear(s.openVenueOrders)
	clear(s.clientIDByVenueOrderID)
	clear(s.unresolvedClientIDs)
}

func (s demoAcceptanceCleanupState) Needed() bool { return s.needed }

func (s demoAcceptanceCleanupState) Metadata() demoAcceptanceCleanupMetadata { return s.meta }

func cleanupBinanceSpotDemoAcceptance(ctx context.Context, adapter *Adapter, id model.InstrumentID, spec demoAcceptanceSymbolSpec, startBaseAvailable, startBaseTotal, maxNotional decimal.Decimal, state *demoAcceptanceCleanupState) error {
	initialInspectErr := inspectRecordedSpotDemoOrders(ctx, adapter, spec, state.ResolvedOpenOrders(), maxNotional, false, state)
	resolveErr := resolveUnresolvedSpotDemoOrdersWithRetry(ctx, state, maxNotional, demoUnresolvedLookupAttempts, demoUnresolvedLookupDelay, func(tracked demoAcceptanceTrackedOrder) (*sdkspot.OrderResponse, error) {
		return lookupRecordedSpotDemoOrder(ctx, adapter, spec, tracked)
	})
	remaining := state.TrackedOpenOrders()
	cancelErr := cancelRecordedSpotDemoOrders(
		state,
		func(venueOrderID string) error { return adapter.Execution.Cancel(ctx, id, venueOrderID) },
		func() error { return waitForNoDemoOpenOrders(ctx, adapter, id) },
	)
	inspectErr := inspectRecordedSpotDemoOrders(ctx, adapter, spec, remaining, maxNotional, true, state)
	if len(state.UnresolvedClientOrders()) == 0 {
		resolveErr = nil
	}
	if err := errors.Join(initialInspectErr, resolveErr, cancelErr, inspectErr); err != nil {
		return fmt.Errorf("recorded-order cleanup failed; inventory close was not attempted: %w", err)
	}
	if !state.CloseAuthorized() {
		return nil
	}
	observationThreshold := demoSpotFillObservationThreshold(state.CloseLimit(), spec.SizeStep)
	baseDelta, err := waitForDemoSpotBalanceDelta(ctx, adapter, spec.BaseCurrency, startBaseTotal, observationThreshold)
	if err != nil {
		return fmt.Errorf("authoritative fill confirmed but inventory delta was not observable for bounded cleanup: %w", err)
	}
	state.SetBaseDelta(baseDelta)
	closeClientID := demoClientOrderID("cleanup-close")
	closed, err := closeBinanceSpotDemoBaseDelta(ctx, adapter, id, spec, startBaseAvailable, state, closeClientID)
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
	return waitForDemoSpotBaseDeltaBelowStep(ctx, adapter, spec, startBaseAvailable)
}

func inspectRecordedSpotDemoOrders(ctx context.Context, adapter *Adapter, spec demoAcceptanceSymbolSpec, orders []demoAcceptanceTrackedOrder, maxNotional decimal.Decimal, requireTerminal bool, state *demoAcceptanceCleanupState) error {
	return inspectRecordedSpotDemoOrdersWithLookup(orders, maxNotional, requireTerminal, state, func(tracked demoAcceptanceTrackedOrder) (*sdkspot.OrderResponse, error) {
		return lookupRecordedSpotDemoOrder(ctx, adapter, spec, tracked)
	})
}

func lookupRecordedSpotDemoOrder(ctx context.Context, adapter *Adapter, spec demoAcceptanceSymbolSpec, tracked demoAcceptanceTrackedOrder) (*sdkspot.OrderResponse, error) {
	orderID := int64(0)
	if tracked.VenueOrderID != "" {
		parsed, err := strconv.ParseInt(tracked.VenueOrderID, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid venue order id %s: %w", tracked.VenueOrderID, err)
		}
		orderID = parsed
	}
	if orderID == 0 && tracked.ClientID == "" {
		return nil, fmt.Errorf("recorded Spot Demo order has neither venue nor client id")
	}
	return adapter.rest.GetOrder(ctx, spec.VenueSymbol, orderID, tracked.ClientID)
}

func resolveUnresolvedSpotDemoOrdersWithRetry(ctx context.Context, state *demoAcceptanceCleanupState, maxNotional decimal.Decimal, attempts int, retryDelay time.Duration, lookup func(demoAcceptanceTrackedOrder) (*sdkspot.OrderResponse, error)) error {
	if attempts < 1 {
		return fmt.Errorf("unresolved Spot Demo order lookup attempts must be positive")
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		unresolved := state.UnresolvedClientOrders()
		if len(unresolved) == 0 {
			return nil
		}
		err := inspectRecordedSpotDemoOrdersWithLookup(unresolved, maxNotional, false, state, lookup)
		if err != nil {
			lastErr = err
		}
		if len(state.UnresolvedClientOrders()) == 0 {
			return err
		} else {
			if err == nil {
				lastErr = fmt.Errorf("client-only Spot Demo order lookup returned no venue identity")
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
				return fmt.Errorf("retry unresolved Spot Demo order lookup: %w", ctx.Err())
			case <-timer.C:
			}
		} else {
			select {
			case <-ctx.Done():
				return fmt.Errorf("retry unresolved Spot Demo order lookup: %w", ctx.Err())
			default:
			}
		}
	}
	return fmt.Errorf("resolve client-only Spot Demo orders after %d attempts: %w", attempts, lastErr)
}

func inspectRecordedSpotDemoOrdersWithLookup(orders []demoAcceptanceTrackedOrder, maxNotional decimal.Decimal, requireTerminal bool, state *demoAcceptanceCleanupState, lookup func(demoAcceptanceTrackedOrder) (*sdkspot.OrderResponse, error)) error {
	var inspectErrs []error
	for _, tracked := range orders {
		resp, err := lookup(tracked)
		if err != nil {
			inspectErrs = append(inspectErrs, fmt.Errorf("inspect recorded Spot Demo order venueID=%s clientID=%s: %w", tracked.VenueOrderID, tracked.ClientID, err))
			continue
		}
		if resp == nil {
			inspectErrs = append(inspectErrs, fmt.Errorf("inspect recorded Spot Demo order venueID=%s clientID=%s returned nil response", tracked.VenueOrderID, tracked.ClientID))
			continue
		}
		venueOrderID := tracked.VenueOrderID
		if resp.OrderID != 0 {
			resolvedVenueOrderID := strconv.FormatInt(resp.OrderID, 10)
			if venueOrderID != "" && venueOrderID != resolvedVenueOrderID {
				inspectErrs = append(inspectErrs, fmt.Errorf("recorded Spot Demo order clientID=%s resolved to venue id %s, want %s", tracked.ClientID, resolvedVenueOrderID, venueOrderID))
				continue
			}
			venueOrderID = resolvedVenueOrderID
			state.ResolveClientOrder(tracked.ClientID, venueOrderID)
		}
		if !isBinanceSpotDemoTerminalStatus(resp.Status) {
			if requireTerminal {
				inspectErrs = append(inspectErrs, fmt.Errorf("recorded Spot Demo order venueID=%s clientID=%s remained non-terminal with status %s after no-open confirmation", venueOrderID, tracked.ClientID, resp.Status))
			}
			continue
		}
		executedQty, err := decimal.NewFromString(resp.ExecutedQty)
		if err != nil {
			inspectErrs = append(inspectErrs, fmt.Errorf("recorded Spot Demo order venueID=%s clientID=%s has invalid executed quantity %q: %w", venueOrderID, tracked.ClientID, resp.ExecutedQty, err))
			continue
		}
		if executedQty.IsPositive() {
			confirmedQty, err := validateBinanceSpotDemoFill(resp, maxNotional)
			state.ConfirmFill(confirmedQty)
			if err != nil {
				inspectErrs = append(inspectErrs, fmt.Errorf("validate recorded Spot Demo order venueID=%s clientID=%s fill: %w", venueOrderID, tracked.ClientID, err))
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

func cancelRecordedSpotDemoOrders(state *demoAcceptanceCleanupState, cancel func(string) error, confirmNoOpen func() error) error {
	venueOrderIDs := state.CancellableVenueOrderIDs()
	var cancelErrs []error
	for _, venueOrderID := range venueOrderIDs {
		if err := cancel(venueOrderID); err != nil {
			cancelErrs = append(cancelErrs, fmt.Errorf("cancel recorded Spot Demo order %s: %w", venueOrderID, err))
		}
	}
	cancelErr := errors.Join(cancelErrs...)
	if err := confirmNoOpen(); err != nil {
		return errors.Join(cancelErr, fmt.Errorf("confirm no Spot Demo open orders after recorded-order cancellation: %w", err))
	}
	for _, venueOrderID := range venueOrderIDs {
		state.MarkOrderTerminal(venueOrderID)
	}
	return nil
}

func closeBinanceSpotDemoBaseDelta(ctx context.Context, adapter *Adapter, id model.InstrumentID, spec demoAcceptanceSymbolSpec, startBaseAvailable decimal.Decimal, state *demoAcceptanceCleanupState, clientID string) (*model.Order, error) {
	maxCloseQty := state.CloseLimit()
	if !maxCloseQty.IsPositive() {
		return nil, fmt.Errorf("automatic close requires a positive authoritative fill quantity")
	}
	balances, err := demoSpotBalances(ctx, adapter)
	if err != nil {
		return nil, err
	}
	availableDelta := balances[spec.BaseCurrency].Free.Sub(startBaseAvailable)
	if availableDelta.IsNegative() {
		return nil, fmt.Errorf("refusing automatic close of negative %s inventory delta %s", spec.BaseCurrency, availableDelta)
	}
	if availableDelta.GreaterThan(maxCloseQty) {
		return nil, fmt.Errorf("refusing automatic close: available delta %s exceeds authoritative lifecycle fill %s", availableDelta, maxCloseQty)
	}
	sellQty := floorDecimalToStep(availableDelta, spec.SizeStep)
	if sellQty.LessThan(spec.MinQty) {
		return nil, nil
	}
	ticker, err := adapter.rest.BookTicker(ctx, spec.VenueSymbol)
	if err != nil {
		return nil, err
	}
	price := floorDecimalToStep(dec(ticker.BidPrice).Mul(decimal.RequireFromString("0.99")), spec.PriceTick)
	state.Arm(enums.SideSell, clientID)
	state.BeginCloseAttempt()
	order, err := adapter.Execution.Submit(ctx, model.OrderRequest{
		InstrumentID: id,
		ClientID:     clientID,
		Side:         enums.SideSell,
		Type:         enums.TypeLimit,
		TIF:          enums.TifIOC,
		Quantity:     sellQty,
		Price:        price,
		PositionSide: enums.PosNet,
	})
	if err != nil {
		return nil, fmt.Errorf("submit single bounded inventory close (outcome ambiguous; not retried): %w", err)
	}
	return order, nil
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

func waitForDemoSpotBaseDeltaBelowStep(ctx context.Context, adapter *Adapter, spec demoAcceptanceSymbolSpec, startBaseAvailable decimal.Decimal) error {
	var lastErr error
	var lastDelta decimal.Decimal
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		balances, err := demoSpotBalances(ctx, adapter)
		if err == nil {
			lastDelta = balances[spec.BaseCurrency].Free.Sub(startBaseAvailable)
			if lastDelta.Abs().LessThan(spec.SizeStep) {
				return nil
			}
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for %s base delta below step %s; lastDelta=%s lastErr=%v: %w", spec.BaseCurrency, spec.SizeStep, lastDelta, lastErr, ctx.Err())
		case <-ticker.C:
		}
	}
}

func validateBinanceSpotDemoFill(resp *sdkspot.OrderResponse, maxNotional decimal.Decimal) (decimal.Decimal, error) {
	if resp == nil {
		return decimal.Zero, fmt.Errorf("missing Binance Spot Demo fill response")
	}
	if maxNotional.LessThanOrEqual(decimal.Zero) {
		return decimal.Zero, fmt.Errorf("max notional must be positive")
	}
	qty, err := decimal.NewFromString(resp.ExecutedQty)
	if err != nil || !qty.IsPositive() {
		return decimal.Zero, fmt.Errorf("invalid executed quantity %q", resp.ExecutedQty)
	}
	cumQuote, err := decimal.NewFromString(resp.CummulativeQuoteQty)
	if err != nil || !cumQuote.IsPositive() {
		return qty, fmt.Errorf("invalid cumulative quote %q", resp.CummulativeQuoteQty)
	}
	if cumQuote.GreaterThan(maxNotional) {
		return qty, fmt.Errorf("executed cumulative quote %s exceeds configured Demo cap %s", cumQuote, maxNotional)
	}
	return qty, nil
}

func demoFillOrderRequest(id model.InstrumentID, clientID string, qty, maxPrice decimal.Decimal) model.OrderRequest {
	return model.OrderRequest{
		InstrumentID: id,
		ClientID:     clientID,
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifIOC,
		Quantity:     qty,
		Price:        maxPrice,
		PositionSide: enums.PosNet,
	}
}
