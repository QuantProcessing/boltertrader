package spot

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	sdkspot "github.com/QuantProcessing/boltertrader/sdk/binance/spot"
	"github.com/shopspring/decimal"
)

const demoDefaultMaxNotionalUSDT = "100"

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
			return nil, fmt.Errorf("invalid PROXY=%q: %w", proxy, err)
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
	needed bool
	meta   demoAcceptanceCleanupMetadata
}

func newDemoAcceptanceCleanupState(spec demoAcceptanceSymbolSpec, qty decimal.Decimal) demoAcceptanceCleanupState {
	return demoAcceptanceCleanupState{
		meta: demoAcceptanceCleanupMetadata{
			Symbol:        spec.VenueSymbol,
			Quantity:      qty,
			BaseCurrency:  spec.BaseCurrency,
			QuoteCurrency: spec.QuoteCurrency,
		},
	}
}

func (s *demoAcceptanceCleanupState) Arm(side enums.OrderSide, clientID string) {
	s.needed = true
	s.meta.Side = side.String()
	if clientID != "" {
		s.meta.ClientOrderIDs = append(s.meta.ClientOrderIDs, clientID)
	}
}

func (s *demoAcceptanceCleanupState) RecordVenueOrderID(venueOrderID string) {
	if venueOrderID != "" {
		s.meta.VenueOrderIDs = append(s.meta.VenueOrderIDs, venueOrderID)
	}
}

func (s *demoAcceptanceCleanupState) SetBaseDelta(delta decimal.Decimal) {
	s.meta.BaseDelta = delta
}

func (s *demoAcceptanceCleanupState) MarkClean() {
	s.needed = false
	s.meta.BaseDelta = decimal.Zero
}

func (s demoAcceptanceCleanupState) Needed() bool { return s.needed }

func (s demoAcceptanceCleanupState) Metadata() demoAcceptanceCleanupMetadata { return s.meta }

func cleanupBinanceSpotDemoAcceptance(ctx context.Context, adapter *Adapter, id model.InstrumentID, spec demoAcceptanceSymbolSpec, startBaseAvailable decimal.Decimal, meta *demoAcceptanceCleanupMetadata) error {
	cancelErr := adapter.Execution.CancelAll(ctx, id)
	balances, err := demoSpotBalances(ctx, adapter)
	if err != nil {
		return err
	}
	baseDelta := balances[spec.BaseCurrency].Available.Sub(startBaseAvailable)
	meta.BaseDelta = baseDelta
	if baseDelta.GreaterThanOrEqual(spec.SizeStep) {
		if err := closeBinanceSpotDemoBaseDelta(ctx, adapter, id, spec, startBaseAvailable); err != nil {
			return err
		}
	}
	if err := waitForNoDemoOpenOrders(ctx, adapter, id); err != nil {
		return err
	}
	if cancelErr != nil {
		return fmt.Errorf("cancel all Spot Demo open orders: %w", cancelErr)
	}
	return waitForDemoSpotBaseDeltaBelowStep(ctx, adapter, spec, startBaseAvailable)
}

func closeBinanceSpotDemoBaseDelta(ctx context.Context, adapter *Adapter, id model.InstrumentID, spec demoAcceptanceSymbolSpec, startBaseAvailable decimal.Decimal) error {
	for attempt := 0; attempt < 3; attempt++ {
		balances, err := demoSpotBalances(ctx, adapter)
		if err != nil {
			return err
		}
		availableDelta := balances[spec.BaseCurrency].Available.Sub(startBaseAvailable)
		sellQty := floorDecimalToStep(availableDelta, spec.SizeStep)
		if sellQty.LessThan(spec.MinQty) {
			return nil
		}
		_, err = adapter.Execution.Submit(ctx, model.OrderRequest{
			InstrumentID: id,
			ClientID:     demoClientOrderID("close"),
			Side:         enums.SideSell,
			Type:         enums.TypeMarket,
			Quantity:     sellQty,
			PositionSide: enums.PosNet,
		})
		if err != nil {
			return err
		}
		if err := waitForDemoSpotBaseDeltaBelowStep(ctx, adapter, spec, startBaseAvailable); err == nil {
			return nil
		}
	}
	return waitForDemoSpotBaseDeltaBelowStep(ctx, adapter, spec, startBaseAvailable)
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
			lastDelta = balances[spec.BaseCurrency].Available.Sub(startBaseAvailable)
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
