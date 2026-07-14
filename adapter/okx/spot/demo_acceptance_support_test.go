package spot

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
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

func waitForDemoOrderStatus(ctx context.Context, rest *okx.Client, instID, venueOrderID, clientID string, statuses ...string) (*okx.Order, error) {
	want := make(map[string]struct{}, len(statuses))
	for _, status := range statuses {
		want[strings.ToLower(status)] = struct{}{}
	}
	var lastErr error
	var lastStatus string
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		orders, err := rest.GetOrder(ctx, instID, venueOrderID, clientID)
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

func waitForDemoOrderDiscovery(ctx context.Context, rest *okx.Client, instID, venueOrderID, clientID string) (*okx.Order, error) {
	var lastErr error
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		orders, err := rest.GetOrder(ctx, instID, venueOrderID, clientID)
		if err == nil && len(orders) > 0 {
			order := orders[0]
			return &order, nil
		}
		if err != nil {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("timed out discovering venueOrderID=%q clientOrderID=%q; lastErr=%v: %w", venueOrderID, clientID, lastErr, ctx.Err())
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

type demoOrderRole uint8

const (
	demoOrderRoleResting demoOrderRole = iota + 1
	demoOrderRoleOpening
	demoOrderRoleClose
)

func (r demoOrderRole) String() string {
	switch r {
	case demoOrderRoleResting:
		return "resting"
	case demoOrderRoleOpening:
		return "opening"
	case demoOrderRoleClose:
		return "close"
	default:
		return fmt.Sprintf("role(%d)", r)
	}
}

type demoTrackedOrder struct {
	Role             demoOrderRole
	ClientOrderID    string
	VenueOrderID     string
	ProcessedFillQty decimal.Decimal
	Terminal         bool
}

type demoOrderLookup func(context.Context, string, string) (*okx.Order, error)
type demoOrderCancel func(context.Context, string) error

type demoSpotCleanupState struct {
	needed         bool
	spec           demoSpotSpec
	qty            decimal.Decimal
	baseDelta      decimal.Decimal
	venueOrderIDs  []string
	clientOrderIDs []string
	orders         map[string]*demoTrackedOrder
	confirmedFill  decimal.Decimal
	restingFill    decimal.Decimal
	inventorySeen  bool
	closeAttempted bool
}

func newDemoSpotCleanupState(spec demoSpotSpec, qty decimal.Decimal) demoSpotCleanupState {
	return demoSpotCleanupState{
		spec:   spec,
		qty:    qty,
		orders: make(map[string]*demoTrackedOrder),
	}
}

func (s *demoSpotCleanupState) TrackOrder(role demoOrderRole, clientID string) {
	s.needed = true
	if clientID == "" {
		return
	}
	if existing, ok := s.orders[clientID]; ok {
		if existing.Role == role {
			return
		}
		return
	}
	s.clientOrderIDs = append(s.clientOrderIDs, clientID)
	s.orders[clientID] = &demoTrackedOrder{Role: role, ClientOrderID: clientID}
}

func (s *demoSpotCleanupState) RecordVenueOrderID(clientID, venueOrderID string) {
	if venueOrderID == "" {
		return
	}
	tracked, ok := s.orders[clientID]
	if !ok {
		return
	}
	if tracked.VenueOrderID == venueOrderID {
		return
	}
	if tracked.VenueOrderID != "" {
		return
	}
	tracked.VenueOrderID = venueOrderID
	s.venueOrderIDs = append(s.venueOrderIDs, venueOrderID)
}

func (s *demoSpotCleanupState) ObserveOrder(clientID string, order *okx.Order) error {
	if order == nil {
		return fmt.Errorf("missing authoritative order observation for %q", clientID)
	}
	if clientID == "" {
		clientID = order.ClOrdId
	}
	tracked, ok := s.orders[clientID]
	if !ok {
		return fmt.Errorf("refusing untracked OKX Demo order observation clientOrderID=%q venueOrderID=%q", clientID, order.OrdId)
	}
	if order.ClOrdId != "" && order.ClOrdId != clientID {
		return fmt.Errorf("order client identity mismatch: tracked=%q observed=%q", clientID, order.ClOrdId)
	}
	if tracked.VenueOrderID != "" && order.OrdId != "" && tracked.VenueOrderID != order.OrdId {
		return fmt.Errorf("order venue identity mismatch: tracked=%q observed=%q", tracked.VenueOrderID, order.OrdId)
	}
	s.RecordVenueOrderID(clientID, order.OrdId)
	fillQty := decimal.Zero
	if order.AccFillSz != "" {
		parsed, err := decimal.NewFromString(order.AccFillSz)
		if err != nil || parsed.IsNegative() {
			return fmt.Errorf("invalid accumulated fill quantity %q for %s", order.AccFillSz, clientID)
		}
		fillQty = parsed
	}
	if fillQty.LessThan(tracked.ProcessedFillQty) {
		return fmt.Errorf("accumulated fill regressed for %s: observed=%s processed=%s", clientID, fillQty, tracked.ProcessedFillQty)
	}
	delta := fillQty.Sub(tracked.ProcessedFillQty)
	if delta.IsPositive() && tracked.Role != demoOrderRoleClose {
		s.confirmedFill = s.confirmedFill.Add(delta)
		if tracked.Role == demoOrderRoleResting {
			s.restingFill = s.restingFill.Add(delta)
		}
	}
	tracked.ProcessedFillQty = fillQty
	tracked.Terminal = isDemoOrderTerminal(order.State)
	return nil
}

func isDemoOrderTerminal(status okx.OrderStatus) bool {
	return status == okx.OrderStatusFilled || status == okx.OrderStatusCanceled || status == okx.OrderStatusMmpCanceled
}

func (s demoSpotCleanupState) TrackedOrder(clientID string) (demoTrackedOrder, bool) {
	tracked, ok := s.orders[clientID]
	if !ok {
		return demoTrackedOrder{}, false
	}
	return *tracked, true
}

func (s demoSpotCleanupState) PendingOrders() []demoTrackedOrder {
	orders := make([]demoTrackedOrder, 0, len(s.orders))
	for _, tracked := range s.orders {
		if !tracked.Terminal {
			orders = append(orders, *tracked)
		}
	}
	sort.Slice(orders, func(i, j int) bool { return orders[i].ClientOrderID < orders[j].ClientOrderID })
	return orders
}

func (s demoSpotCleanupState) RestingFillQuantity() decimal.Decimal { return s.restingFill }

func (s demoSpotCleanupState) OpeningAllowed() bool {
	found := false
	for _, tracked := range s.orders {
		if tracked.Role != demoOrderRoleResting {
			continue
		}
		found = true
		if !tracked.Terminal || tracked.ProcessedFillQty.IsPositive() {
			return false
		}
	}
	return found
}

func (s demoSpotCleanupState) CloseAuthorized() bool {
	return s.confirmedFill.IsPositive() && s.inventorySeen && !s.closeAttempted
}

func (s demoSpotCleanupState) CloseLimit() decimal.Decimal { return s.confirmedFill }

func (s *demoSpotCleanupState) MarkCloseAttempted() {
	s.closeAttempted = true
}

func recoverAmbiguousTrackedDemoOrder(ctx context.Context, state *demoSpotCleanupState, clientID string, lookup demoOrderLookup) error {
	tracked, ok := state.TrackedOrder(clientID)
	if !ok {
		return fmt.Errorf("cannot recover untracked client order %q", clientID)
	}
	order, err := lookup(ctx, tracked.VenueOrderID, tracked.ClientOrderID)
	if err != nil {
		return err
	}
	return state.ObserveOrder(clientID, order)
}

func cancelAndConfirmTrackedDemoOrder(ctx context.Context, state *demoSpotCleanupState, clientID string, cancelOrder demoOrderCancel, lookupTerminal demoOrderLookup) error {
	tracked, ok := state.TrackedOrder(clientID)
	if !ok {
		return fmt.Errorf("cannot cancel untracked client order %q", clientID)
	}
	if tracked.Terminal {
		return nil
	}
	if tracked.VenueOrderID == "" {
		return fmt.Errorf("cannot cancel %s before venue identity is confirmed", clientID)
	}
	cancelErr := cancelOrder(ctx, tracked.VenueOrderID)
	order, lookupErr := lookupTerminal(ctx, tracked.VenueOrderID, tracked.ClientOrderID)
	if lookupErr != nil {
		return errors.Join(cancelErr, lookupErr)
	}
	if err := state.ObserveOrder(clientID, order); err != nil {
		return errors.Join(cancelErr, err)
	}
	confirmed, _ := state.TrackedOrder(clientID)
	if !confirmed.Terminal {
		return errors.Join(cancelErr, fmt.Errorf("order %s did not reach an authoritative terminal state", clientID))
	}
	return nil
}

func (s *demoSpotCleanupState) SetBaseDelta(delta decimal.Decimal) {
	s.baseDelta = delta
	if !delta.IsZero() {
		s.inventorySeen = true
	}
}

func (s *demoSpotCleanupState) MarkClean() {
	s.needed = false
	s.baseDelta = decimal.Zero
	s.confirmedFill = decimal.Zero
	s.restingFill = decimal.Zero
	s.inventorySeen = false
	s.closeAttempted = false
	clear(s.orders)
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

func recoverAmbiguousOKXSpotDemoOrder(ctx context.Context, adapter *Adapter, spec demoSpotSpec, state *demoSpotCleanupState, clientID string) error {
	recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return recoverAmbiguousTrackedDemoOrder(recoveryCtx, state, clientID, func(callCtx context.Context, venueOrderID, clientOrderID string) (*okx.Order, error) {
		return waitForDemoOrderDiscovery(callCtx, adapter.rest, spec.VenueSymbol, venueOrderID, clientOrderID)
	})
}

func cancelAndConfirmOKXSpotDemoOrder(ctx context.Context, adapter *Adapter, id model.InstrumentID, spec demoSpotSpec, state *demoSpotCleanupState, clientID string) error {
	return cancelAndConfirmTrackedDemoOrder(
		ctx,
		state,
		clientID,
		func(callCtx context.Context, venueOrderID string) error {
			return adapter.Execution.Cancel(callCtx, id, venueOrderID)
		},
		func(callCtx context.Context, venueOrderID, clientOrderID string) (*okx.Order, error) {
			return waitForDemoOrderStatus(callCtx, adapter.rest, spec.VenueSymbol, venueOrderID, clientOrderID, "filled", "canceled", "mmp_canceled")
		},
	)
}

func confirmOKXSpotDemoOrderTerminal(ctx context.Context, adapter *Adapter, spec demoSpotSpec, state *demoSpotCleanupState, clientID string) (*okx.Order, error) {
	tracked, ok := state.TrackedOrder(clientID)
	if !ok {
		return nil, fmt.Errorf("cannot confirm untracked order %q", clientID)
	}
	order, err := waitForDemoOrderStatus(ctx, adapter.rest, spec.VenueSymbol, tracked.VenueOrderID, tracked.ClientOrderID, "filled", "canceled", "mmp_canceled")
	if err != nil {
		return nil, err
	}
	if err := state.ObserveOrder(clientID, order); err != nil {
		return nil, err
	}
	return order, nil
}

func reconcilePendingOKXSpotDemoOrders(ctx context.Context, adapter *Adapter, id model.InstrumentID, spec demoSpotSpec, state *demoSpotCleanupState) error {
	for _, tracked := range state.PendingOrders() {
		if tracked.VenueOrderID != "" {
			continue
		}
		if err := recoverAmbiguousOKXSpotDemoOrder(ctx, adapter, spec, state, tracked.ClientOrderID); err != nil {
			return fmt.Errorf("recover ambiguous %s order %s: %w", tracked.Role, tracked.ClientOrderID, err)
		}
	}
	for _, tracked := range state.PendingOrders() {
		if err := cancelAndConfirmOKXSpotDemoOrder(ctx, adapter, id, spec, state, tracked.ClientOrderID); err != nil {
			return fmt.Errorf("cancel and confirm lifecycle order %s: %w", tracked.ClientOrderID, err)
		}
	}
	return nil
}

func cleanupOKXSpotDemo(ctx context.Context, adapter *Adapter, id model.InstrumentID, spec demoSpotSpec, startBaseAvailable, startBaseTotal decimal.Decimal, state *demoSpotCleanupState) error {
	if err := reconcilePendingOKXSpotDemoOrders(ctx, adapter, id, spec, state); err != nil {
		return fmt.Errorf("recorded-order cleanup failed; inventory close was not attempted: %w", err)
	}
	if state.confirmedFill.IsPositive() && !state.inventorySeen && !state.closeAttempted {
		minObserved := state.confirmedFill.Div(decimal.NewFromInt(2))
		delta, err := waitForDemoSpotBaseDelta(ctx, adapter, spec.BaseCurrency, startBaseTotal, minObserved)
		if err != nil {
			return fmt.Errorf("authoritative fill confirmed but inventory was not observable; automatic close was not attempted: %w", err)
		}
		state.SetBaseDelta(delta)
	}
	closed, err := closeOKXSpotDemoBaseDelta(ctx, adapter, id, spec, startBaseAvailable, state)
	if err != nil {
		return err
	}
	if closed != nil {
		if _, err := confirmOKXSpotDemoOrderTerminal(ctx, adapter, spec, state, closed.Request.ClientID); err != nil {
			return err
		}
	}
	if err := waitForNoDemoOpenOrders(ctx, adapter, id); err != nil {
		return err
	}
	if !state.confirmedFill.IsPositive() {
		return nil
	}
	return waitForDemoSpotBaseDeltaBelowStep(ctx, adapter, spec, startBaseAvailable, state)
}

func closeOKXSpotDemoBaseDelta(ctx context.Context, adapter *Adapter, id model.InstrumentID, spec demoSpotSpec, startBaseAvailable decimal.Decimal, state *demoSpotCleanupState) (*model.Order, error) {
	if !state.CloseAuthorized() {
		return nil, nil
	}
	maxCloseQty := state.CloseLimit()
	balances, err := demoSpotBalances(ctx, adapter)
	if err != nil {
		return nil, err
	}
	availableDelta := balances[spec.BaseCurrency].Available.Sub(startBaseAvailable)
	sellQty, err := demoSpotCloseQuantity(availableDelta, maxCloseQty, spec)
	if err != nil {
		return nil, err
	}
	if sellQty.IsZero() {
		return nil, nil
	}
	book, err := adapter.Market.OrderBook(ctx, id, 5)
	if err != nil {
		return nil, err
	}
	if len(book.Bids) == 0 {
		return nil, fmt.Errorf("cannot close base delta: empty bid book")
	}
	price := floorDecimalToStep(book.Bids[0].Price.Mul(decimal.RequireFromString("0.99")), spec.PriceTick)
	clientID := demoClientOrderID("close")
	state.TrackOrder(demoOrderRoleClose, clientID)
	state.MarkCloseAttempted()
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
		recoveryErr := recoverAmbiguousOKXSpotDemoOrder(ctx, adapter, spec, state, clientID)
		if recoveryErr != nil {
			return nil, fmt.Errorf("submit single bounded inventory close (outcome ambiguous; not retried): %w; client-ID recovery failed: %v", err, recoveryErr)
		}
		return nil, fmt.Errorf("submit single bounded inventory close returned an error after the lifecycle order was recovered; not retried: %w", err)
	}
	state.RecordVenueOrderID(clientID, order.VenueOrderID)
	return order, nil
}

func demoSpotCloseQuantity(availableDelta, maxCloseQty decimal.Decimal, spec demoSpotSpec) (decimal.Decimal, error) {
	if !maxCloseQty.IsPositive() {
		return decimal.Zero, fmt.Errorf("automatic close requires a positive authoritative fill quantity")
	}
	if availableDelta.IsNegative() {
		return decimal.Zero, fmt.Errorf("refusing automatic close of negative %s inventory delta %s", spec.BaseCurrency, availableDelta)
	}
	if availableDelta.GreaterThan(maxCloseQty) {
		return decimal.Zero, fmt.Errorf("refusing automatic close: available delta %s exceeds authoritative lifecycle fill %s", availableDelta, maxCloseQty)
	}
	sellQty := floorDecimalToStep(availableDelta, spec.SizeStep)
	if sellQty.LessThan(spec.MinQty) {
		return decimal.Zero, nil
	}
	return sellQty, nil
}

func validateOKXSpotDemoFill(resp *okx.Order, maxNotional decimal.Decimal) (decimal.Decimal, error) {
	if resp == nil {
		return decimal.Zero, fmt.Errorf("missing OKX Spot Demo fill response")
	}
	if !maxNotional.IsPositive() {
		return decimal.Zero, fmt.Errorf("max notional must be positive")
	}
	qty, err := decimal.NewFromString(resp.AccFillSz)
	if err != nil || !qty.IsPositive() {
		return decimal.Zero, fmt.Errorf("invalid accumulated fill quantity %q", resp.AccFillSz)
	}
	priceText := resp.AvgPx
	if priceText == "" {
		priceText = resp.FillPx
	}
	price, err := decimal.NewFromString(priceText)
	if err != nil || !price.IsPositive() {
		return qty, fmt.Errorf("invalid average fill price %q", priceText)
	}
	notional := qty.Mul(price)
	if notional.GreaterThan(maxNotional) {
		return qty, fmt.Errorf("actual filled notional %s exceeds configured OKX Demo cap %s", notional, maxNotional)
	}
	return qty, nil
}

func waitForNoDemoOpenOrders(ctx context.Context, adapter *Adapter, id model.InstrumentID) error {
	var lastErr error
	var lastOpen int
	stable := newDemoStableReads(2)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		open, err := adapter.Execution.OpenOrders(ctx, id)
		if err != nil {
			lastErr = err
			stable.Observe(false)
		} else {
			lastOpen = len(open)
			if stable.Observe(len(open) == 0) {
				return nil
			}
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
	stable := newDemoStableReads(2)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		balances, err := demoSpotBalances(ctx, adapter)
		if err == nil {
			lastDelta = balances[spec.BaseCurrency].Available.Sub(startBaseAvailable)
			state.SetBaseDelta(lastDelta)
			if stable.Observe(lastDelta.Abs().LessThan(spec.SizeStep)) {
				return nil
			}
		} else {
			lastErr = err
			stable.Observe(false)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for %s base delta below step %s; lastDelta=%s lastErr=%v: %w", spec.BaseCurrency, spec.SizeStep, lastDelta, lastErr, ctx.Err())
		case <-ticker.C:
		}
	}
}

type demoStableReads struct {
	required    int
	consecutive int
}

func newDemoStableReads(required int) *demoStableReads {
	if required < 1 {
		required = 1
	}
	return &demoStableReads{required: required}
}

func (s *demoStableReads) Observe(stable bool) bool {
	if !stable {
		s.consecutive = 0
		return false
	}
	s.consecutive++
	return s.consecutive >= s.required
}
