package spot

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/testenv"
	"github.com/QuantProcessing/boltertrader/sdk/okx"
	"github.com/shopspring/decimal"
)

type demoSpotSpec struct {
	VenueSymbol   string
	BaseCurrency  string
	QuoteCurrency string
	PriceTick     decimal.Decimal
	SizeStep      decimal.Decimal
	MinQty        decimal.Decimal
}

func demoSpotSpecFromInstrument(inst *model.Instrument) (demoSpotSpec, error) {
	if inst == nil {
		return demoSpotSpec{}, fmt.Errorf("missing instrument")
	}
	spec := demoSpotSpec{
		VenueSymbol:   inst.VenueSymbol,
		BaseCurrency:  inst.Base,
		QuoteCurrency: inst.Quote,
		PriceTick:     inst.PriceTick,
		SizeStep:      inst.SizeStep,
		MinQty:        inst.MinQty,
	}
	if spec.PriceTick.IsZero() || spec.SizeStep.IsZero() || spec.MinQty.IsZero() {
		return demoSpotSpec{}, fmt.Errorf("incomplete OKX Spot instrument filters: %+v", spec)
	}
	return spec, nil
}

func okxDemoEndpoints(t testHelper, cfg testenv.OKXDemoConfig) okx.EndpointURLs {
	t.Helper()
	if cfg.HostProfile == testenv.OKXDemoHostProfileCustom {
		return okxDemoCustomEndpoints(cfg)
	}
	endpoints, err := okx.DefaultEndpointURLs(okx.Simulated, okx.DemoHostProfile(cfg.HostProfile))
	if err != nil {
		t.Fatalf("OKX Demo endpoints: %v", err)
	}
	if cfg.RESTBaseURL != "" {
		endpoints.REST = cfg.RESTBaseURL
	}
	if cfg.WSBaseURL != "" {
		base := strings.TrimRight(cfg.WSBaseURL, "/")
		endpoints.WSPublic = base + "/ws/v5/public"
		endpoints.WSPrivate = base + "/ws/v5/private"
		endpoints.WSBusiness = base + "/ws/v5/business"
	}
	return endpoints
}

func okxDemoCustomEndpoints(cfg testenv.OKXDemoConfig) okx.EndpointURLs {
	endpoints := okx.EndpointURLs{REST: cfg.RESTBaseURL}
	if cfg.WSBaseURL != "" {
		base := strings.TrimRight(cfg.WSBaseURL, "/")
		endpoints.WSPublic = base + "/ws/v5/public"
		endpoints.WSPrivate = base + "/ws/v5/private"
		endpoints.WSBusiness = base + "/ws/v5/business"
	}
	return endpoints
}

func demoSpotTdMode(ctx context.Context, cfg testenv.OKXDemoConfig, endpoints okx.EndpointURLs, httpClient *http.Client) (string, error) {
	rest := okx.NewClient().
		WithCredentials(cfg.APIKey, cfg.APISecret, cfg.Passphrase).
		WithEnvironment(okx.Simulated).
		WithDemoHostProfile(okx.DemoHostProfile(cfg.HostProfile))
	if endpoints.REST != "" {
		rest.WithBaseURL(endpoints.REST)
	}
	if httpClient != nil {
		rest.WithHTTPClient(httpClient)
	}
	configs, err := rest.GetAccountConfig(ctx)
	if err != nil {
		return "", fmt.Errorf("load OKX Demo Spot account config: %w", err)
	}
	if len(configs) == 0 {
		return "", fmt.Errorf("load OKX Demo Spot account config: empty response")
	}
	if configs[0].AccountLevel() == okx.AccountLevelSimple {
		return defaultSpotTdMode, nil
	}
	return spotTdModeCross, nil
}

type testHelper interface {
	Helper()
	Fatalf(format string, args ...any)
}

func selectDemoSpotQuantity(spec demoSpotSpec, maxNotional, refPrice decimal.Decimal) (decimal.Decimal, error) {
	if maxNotional.LessThanOrEqual(decimal.Zero) {
		return decimal.Zero, fmt.Errorf("max notional must be positive")
	}
	if refPrice.LessThanOrEqual(decimal.Zero) {
		return decimal.Zero, fmt.Errorf("reference price must be positive")
	}
	maxQty := floorDecimalToStep(maxNotional.Div(refPrice), spec.SizeStep)
	qty := maxDecimal(spec.MinQty, spec.SizeStep)
	if qty.LessThan(spec.MinQty) {
		qty = ceilDecimalToStep(spec.MinQty, spec.SizeStep)
	}
	if qty.GreaterThan(maxQty) {
		return decimal.Zero, fmt.Errorf("min quantity %s exceeds max notional %s at price %s", qty, maxNotional, refPrice)
	}
	target := maxNotional.Div(refPrice).Div(decimal.NewFromInt(2))
	qty = floorDecimalToStep(maxDecimal(qty, target), spec.SizeStep)
	if qty.LessThan(spec.MinQty) {
		qty = ceilDecimalToStep(spec.MinQty, spec.SizeStep)
	}
	if qty.GreaterThan(maxQty) {
		qty = maxQty
	}
	if qty.LessThan(spec.MinQty) || qty.IsZero() {
		return decimal.Zero, fmt.Errorf("selected quantity %s below min %s", qty, spec.MinQty)
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

func demoClientOrderID(kind string) string {
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	prefix := "btdos"
	kind = demoClientOrderIDKind(kind, 32-len(prefix)-len(suffix))
	return prefix + kind + suffix
}

func demoClientOrderIDKind(kind string, maxLen int) string {
	var b strings.Builder
	for _, r := range strings.ToLower(kind) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
		if maxLen > 0 && b.Len() >= maxLen {
			break
		}
	}
	if b.Len() == 0 {
		return "x"
	}
	return b.String()
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

func waitForDemoOrderStatus(ctx context.Context, rest *okx.Client, instID, clientID string, statuses ...string) (*okx.Order, error) {
	want := make(map[string]struct{}, len(statuses))
	for _, status := range statuses {
		want[strings.ToLower(status)] = struct{}{}
	}
	var lastErr error
	var lastStatus string
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		orders, err := rest.GetOrder(ctx, instID, "", clientID)
		if err == nil && len(orders) > 0 {
			order := orders[0]
			lastStatus = string(order.State)
			if _, ok := want[strings.ToLower(string(order.State))]; ok {
				return &order, nil
			}
		} else if err != nil {
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
	timeout, cancel := context.WithTimeout(ctx, 25*time.Second)
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

func demoSpotBalances(ctx context.Context, adapter *Adapter) (map[string]model.AccountBalance, error) {
	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	balances, err := adapter.Account.Balances(callCtx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]model.AccountBalance, len(balances))
	for _, balance := range balances {
		out[balance.Currency] = balance
	}
	return out, nil
}

func waitForDemoSpotBaseDelta(ctx context.Context, adapter *Adapter, currency string, startTotal, minDelta decimal.Decimal) (decimal.Decimal, error) {
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

type demoSpotCleanupState struct {
	needed         bool
	spec           demoSpotSpec
	qty            decimal.Decimal
	baseDelta      decimal.Decimal
	venueOrderIDs  []string
	clientOrderIDs []string
}

func newDemoSpotCleanupState(spec demoSpotSpec, qty decimal.Decimal) demoSpotCleanupState {
	return demoSpotCleanupState{spec: spec, qty: qty}
}

func (s *demoSpotCleanupState) Arm(clientID string) {
	s.needed = true
	if clientID != "" {
		s.clientOrderIDs = append(s.clientOrderIDs, clientID)
	}
}

func (s *demoSpotCleanupState) RecordVenueOrderID(id string) {
	if id != "" {
		s.venueOrderIDs = append(s.venueOrderIDs, id)
	}
}

func (s *demoSpotCleanupState) SetBaseDelta(delta decimal.Decimal) { s.baseDelta = delta }

func (s *demoSpotCleanupState) MarkClean() {
	s.needed = false
	s.baseDelta = decimal.Zero
}

func (s demoSpotCleanupState) Remediation() string {
	return fmt.Sprintf(
		"OKX Spot Demo cleanup failed: symbol=%s quantity=%s base=%s quote=%s baseDelta=%s venueOrderIDs=%s clientOrderIDs=%s. Manually cancel open Demo Spot orders and sell unexpected base-asset test balance delta.",
		s.spec.VenueSymbol,
		s.qty,
		s.spec.BaseCurrency,
		s.spec.QuoteCurrency,
		s.baseDelta,
		strings.Join(s.venueOrderIDs, ","),
		strings.Join(s.clientOrderIDs, ","),
	)
}

func cleanupOKXSpotDemo(ctx context.Context, adapter *Adapter, id model.InstrumentID, spec demoSpotSpec, startBaseAvailable decimal.Decimal, state *demoSpotCleanupState) error {
	cancelErr := adapter.Execution.CancelAll(ctx, id)
	if err := closeOKXSpotDemoBaseDelta(ctx, adapter, id, spec, startBaseAvailable); err != nil {
		return err
	}
	if err := waitForNoDemoOpenOrders(ctx, adapter, id); err != nil {
		return err
	}
	if cancelErr != nil {
		return fmt.Errorf("cancel all OKX Spot Demo open orders: %w", cancelErr)
	}
	return waitForDemoSpotBaseDeltaBelowStep(ctx, adapter, spec, startBaseAvailable, state)
}

func closeOKXSpotDemoBaseDelta(ctx context.Context, adapter *Adapter, id model.InstrumentID, spec demoSpotSpec, startBaseAvailable decimal.Decimal) error {
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
		book, err := adapter.Market.OrderBook(ctx, id, 5)
		if err != nil {
			return err
		}
		if len(book.Bids) == 0 {
			return fmt.Errorf("cannot close base delta: empty bid book")
		}
		price := floorDecimalToStep(book.Bids[0].Price.Mul(decimal.RequireFromString("0.99")), spec.PriceTick)
		_, err = adapter.Execution.Submit(ctx, model.OrderRequest{
			InstrumentID: id,
			ClientID:     demoClientOrderID("close"),
			Side:         enums.SideSell,
			Type:         enums.TypeLimit,
			TIF:          enums.TifIOC,
			Quantity:     sellQty,
			Price:        price,
			PositionSide: enums.PosNet,
		})
		if err != nil {
			return err
		}
	}
	return nil
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

func waitForDemoSpotBaseDeltaBelowStep(ctx context.Context, adapter *Adapter, spec demoSpotSpec, startBaseAvailable decimal.Decimal, state *demoSpotCleanupState) error {
	var lastErr error
	var lastDelta decimal.Decimal
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		balances, err := demoSpotBalances(ctx, adapter)
		if err == nil {
			lastDelta = balances[spec.BaseCurrency].Available.Sub(startBaseAvailable)
			state.SetBaseDelta(lastDelta)
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
