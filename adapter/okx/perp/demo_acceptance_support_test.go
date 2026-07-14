package perp

import (
	"context"
	"errors"
	"fmt"
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

type demoPerpSpec struct {
	VenueSymbol    string
	BaseCurrency   string
	QuoteCurrency  string
	SettleCurrency string
	PriceTick      decimal.Decimal
	SizeStep       decimal.Decimal
	MinQty         decimal.Decimal
	CtVal          decimal.Decimal
	CtValCcy       string
}

func demoPerpSpecFromOKX(in *okx.Instrument) (demoPerpSpec, error) {
	if in == nil {
		return demoPerpSpec{}, fmt.Errorf("missing instrument")
	}
	settle := in.SettleCcy
	if settle == "" {
		settle = in.SettCcy
	}
	spec := demoPerpSpec{
		VenueSymbol:    in.InstId,
		BaseCurrency:   in.BaseCcy,
		QuoteCurrency:  in.QuoteCcy,
		SettleCurrency: settle,
		PriceTick:      dec(in.TickSz),
		SizeStep:       dec(in.LotSz),
		MinQty:         dec(in.MinSz),
		CtVal:          dec(in.CtVal),
		CtValCcy:       in.CtValCcy,
	}
	if spec.PriceTick.IsZero() || spec.SizeStep.IsZero() || spec.MinQty.IsZero() || spec.CtVal.IsZero() {
		return demoPerpSpec{}, fmt.Errorf("incomplete OKX Perp instrument metadata: %+v", spec)
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

func validateDemoPerpAccountMode(ctx context.Context, rest *okx.Client) error {
	configs, err := rest.GetAccountConfig(ctx)
	if err != nil {
		return fmt.Errorf("load OKX Demo account config: %w", err)
	}
	if len(configs) == 0 {
		return fmt.Errorf("load OKX Demo account config: empty response")
	}
	cfg := configs[0]
	summary := fmt.Sprintf(
		"acctLv=%q(%s) posMode=%q type=%q",
		cfg.AcctLv,
		cfg.AccountLevel(),
		cfg.PosMode,
		cfg.Type,
	)
	if cfg.AccountLevel() == okx.AccountLevelSimple {
		return fmt.Errorf("%s does not support OKX SWAP demo acceptance; switch the OKX Demo account to a margin/futures-capable account mode in OKX Web/App before running this gate", summary)
	}
	return nil
}

type testHelper interface {
	Helper()
	Fatalf(format string, args ...any)
}

func selectDemoPerpQuantity(spec demoPerpSpec, maxNotional, refPrice decimal.Decimal) (decimal.Decimal, error) {
	perContract := demoPerpNotionalPerContract(spec, refPrice)
	if maxNotional.LessThanOrEqual(decimal.Zero) {
		return decimal.Zero, fmt.Errorf("max notional must be positive")
	}
	if perContract.LessThanOrEqual(decimal.Zero) {
		return decimal.Zero, fmt.Errorf("invalid per-contract notional %s", perContract)
	}
	maxQty := floorDecimalToStep(maxNotional.Div(perContract), spec.SizeStep)
	qty := ceilDecimalToStep(spec.MinQty, spec.SizeStep)
	if qty.GreaterThan(maxQty) {
		return decimal.Zero, fmt.Errorf("min contracts %s exceed max notional %s at price %s with per-contract notional %s", qty, maxNotional, refPrice, perContract)
	}
	target := maxNotional.Div(perContract).Div(decimal.NewFromInt(2))
	qty = floorDecimalToStep(maxDecimal(qty, target), spec.SizeStep)
	if qty.LessThan(spec.MinQty) {
		qty = ceilDecimalToStep(spec.MinQty, spec.SizeStep)
	}
	if qty.GreaterThan(maxQty) {
		qty = maxQty
	}
	if qty.IsZero() {
		return decimal.Zero, fmt.Errorf("selected zero contracts")
	}
	return qty, nil
}

func demoPerpNotionalPerContract(spec demoPerpSpec, price decimal.Decimal) decimal.Decimal {
	if strings.EqualFold(spec.CtValCcy, spec.QuoteCurrency) || strings.EqualFold(spec.CtValCcy, spec.SettleCurrency) {
		return spec.CtVal
	}
	return spec.CtVal.Mul(price)
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
	prefix := "btdop"
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

func demoCurrentExposure(ctx context.Context, adapter *Adapter, id model.InstrumentID) (decimal.Decimal, error) {
	positions, err := adapter.Account.Positions(ctx)
	if err != nil {
		return decimal.Zero, err
	}
	return demoExposureFromPositions(positions, id)
}

func demoExposureFromPositions(positions []model.Position, id model.InstrumentID) (decimal.Decimal, error) {
	var exposure decimal.Decimal
	nonZero := make([]string, 0, 2)
	for _, position := range positions {
		if position.InstrumentID != id || position.Quantity.IsZero() {
			continue
		}
		exposure = position.Quantity
		nonZero = append(nonZero, fmt.Sprintf("%s:%s", position.Side, position.Quantity))
	}
	if len(nonZero) > 1 {
		return decimal.Zero, fmt.Errorf("%s has multiple non-zero position legs (%s); refusing automatic cleanup", id, strings.Join(nonZero, ", "))
	}
	return exposure, nil
}

func waitForDemoExposure(ctx context.Context, adapter *Adapter, id model.InstrumentID, minAbs decimal.Decimal) (decimal.Decimal, error) {
	var lastErr error
	var last decimal.Decimal
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		exposure, err := demoCurrentExposure(ctx, adapter, id)
		if err == nil {
			last = exposure
			if exposure.Abs().GreaterThanOrEqual(minAbs.Abs()) {
				return exposure, nil
			}
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return decimal.Zero, fmt.Errorf("timed out waiting for exposure >= %s; last=%s lastErr=%v: %w", minAbs, last, lastErr, ctx.Err())
		case <-ticker.C:
		}
	}
}

func waitForDemoFlat(ctx context.Context, adapter *Adapter, id model.InstrumentID) error {
	var lastErr error
	var last decimal.Decimal
	stable := newDemoStableReads(2)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		exposure, err := demoCurrentExposure(ctx, adapter, id)
		if err == nil {
			last = exposure
			if stable.Observe(exposure.IsZero()) {
				return nil
			}
		} else {
			lastErr = err
			stable.Observe(false)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for flat position; last=%s lastErr=%v: %w", last, lastErr, ctx.Err())
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

type demoPerpCleanupState struct {
	needed         bool
	spec           demoPerpSpec
	qty            decimal.Decimal
	exposure       decimal.Decimal
	venueOrderIDs  []string
	clientOrderIDs []string
	orders         map[string]*demoTrackedOrder
	confirmedFill  decimal.Decimal
	restingFill    decimal.Decimal
	exposureSeen   bool
	closeAttempted bool
}

func newDemoPerpCleanupState(spec demoPerpSpec, qty decimal.Decimal) demoPerpCleanupState {
	return demoPerpCleanupState{
		spec:   spec,
		qty:    qty,
		orders: make(map[string]*demoTrackedOrder),
	}
}

func (s *demoPerpCleanupState) TrackOrder(role demoOrderRole, clientID string) {
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

func (s *demoPerpCleanupState) RecordVenueOrderID(clientID, venueOrderID string) {
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

func (s *demoPerpCleanupState) ObserveOrder(clientID string, order *okx.Order) error {
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

func (s demoPerpCleanupState) TrackedOrder(clientID string) (demoTrackedOrder, bool) {
	tracked, ok := s.orders[clientID]
	if !ok {
		return demoTrackedOrder{}, false
	}
	return *tracked, true
}

func (s demoPerpCleanupState) PendingOrders() []demoTrackedOrder {
	orders := make([]demoTrackedOrder, 0, len(s.orders))
	for _, tracked := range s.orders {
		if !tracked.Terminal {
			orders = append(orders, *tracked)
		}
	}
	sort.Slice(orders, func(i, j int) bool { return orders[i].ClientOrderID < orders[j].ClientOrderID })
	return orders
}

func (s demoPerpCleanupState) RestingFillQuantity() decimal.Decimal { return s.restingFill }

func (s demoPerpCleanupState) OpeningAllowed() bool {
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

func (s demoPerpCleanupState) CloseAuthorized() bool {
	return s.confirmedFill.IsPositive() && s.exposureSeen && !s.closeAttempted
}

func (s demoPerpCleanupState) CloseLimit() decimal.Decimal { return s.confirmedFill }

func (s *demoPerpCleanupState) MarkCloseAttempted() {
	s.closeAttempted = true
}

func recoverAmbiguousTrackedDemoOrder(ctx context.Context, state *demoPerpCleanupState, clientID string, lookup demoOrderLookup) error {
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

func cancelAndConfirmTrackedDemoOrder(ctx context.Context, state *demoPerpCleanupState, clientID string, cancelOrder demoOrderCancel, lookupTerminal demoOrderLookup) error {
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

func (s *demoPerpCleanupState) SetExposure(exposure decimal.Decimal) {
	s.exposure = exposure
	if !exposure.IsZero() {
		s.exposureSeen = true
	}
}

func (s *demoPerpCleanupState) MarkClean() {
	s.needed = false
	s.exposure = decimal.Zero
	s.confirmedFill = decimal.Zero
	s.restingFill = decimal.Zero
	s.exposureSeen = false
	s.closeAttempted = false
	clear(s.orders)
}

func (s demoPerpCleanupState) Remediation() string {
	return fmt.Sprintf(
		"OKX Perp Demo cleanup failed: symbol=%s quantity=%s exposure=%s venueOrderIDs=%s clientOrderIDs=%s. Manually cancel open Demo SWAP orders and flatten remaining exposure.",
		s.spec.VenueSymbol,
		s.qty,
		s.exposure,
		strings.Join(s.venueOrderIDs, ","),
		strings.Join(s.clientOrderIDs, ","),
	)
}

func recoverAmbiguousOKXPerpDemoOrder(ctx context.Context, adapter *Adapter, spec demoPerpSpec, state *demoPerpCleanupState, clientID string) error {
	recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return recoverAmbiguousTrackedDemoOrder(recoveryCtx, state, clientID, func(callCtx context.Context, venueOrderID, clientOrderID string) (*okx.Order, error) {
		return waitForDemoOrderDiscovery(callCtx, adapter.rest, spec.VenueSymbol, venueOrderID, clientOrderID)
	})
}

func cancelAndConfirmOKXPerpDemoOrder(ctx context.Context, adapter *Adapter, id model.InstrumentID, spec demoPerpSpec, state *demoPerpCleanupState, clientID string) error {
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

func confirmOKXPerpDemoOrderTerminal(ctx context.Context, adapter *Adapter, spec demoPerpSpec, state *demoPerpCleanupState, clientID string) (*okx.Order, error) {
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

func reconcilePendingOKXPerpDemoOrders(ctx context.Context, adapter *Adapter, id model.InstrumentID, spec demoPerpSpec, state *demoPerpCleanupState) error {
	for _, tracked := range state.PendingOrders() {
		if tracked.VenueOrderID != "" {
			continue
		}
		if err := recoverAmbiguousOKXPerpDemoOrder(ctx, adapter, spec, state, tracked.ClientOrderID); err != nil {
			return fmt.Errorf("recover ambiguous %s order %s: %w", tracked.Role, tracked.ClientOrderID, err)
		}
	}
	for _, tracked := range state.PendingOrders() {
		if err := cancelAndConfirmOKXPerpDemoOrder(ctx, adapter, id, spec, state, tracked.ClientOrderID); err != nil {
			return fmt.Errorf("cancel and confirm lifecycle order %s: %w", tracked.ClientOrderID, err)
		}
	}
	return nil
}

func cleanupOKXPerpDemo(ctx context.Context, adapter *Adapter, id model.InstrumentID, spec demoPerpSpec, state *demoPerpCleanupState) error {
	if err := reconcilePendingOKXPerpDemoOrders(ctx, adapter, id, spec, state); err != nil {
		return fmt.Errorf("recorded-order cleanup failed; exposure close was not attempted: %w", err)
	}
	if state.confirmedFill.IsPositive() && !state.exposureSeen && !state.closeAttempted {
		exposure, err := waitForDemoExposure(ctx, adapter, id, state.confirmedFill)
		if err != nil {
			return fmt.Errorf("authoritative fill confirmed but exposure was not observable; automatic close was not attempted: %w", err)
		}
		state.SetExposure(exposure)
	}
	closed, err := closeOKXPerpDemoExposure(ctx, adapter, id, spec, state)
	if err != nil {
		return err
	}
	if closed != nil {
		if _, err := confirmOKXPerpDemoOrderTerminal(ctx, adapter, spec, state, closed.Request.ClientID); err != nil {
			return err
		}
	}
	if err := waitForNoDemoOpenOrders(ctx, adapter, id); err != nil {
		return err
	}
	if !state.confirmedFill.IsPositive() {
		return nil
	}
	if err := waitForDemoFlat(ctx, adapter, id); err != nil {
		exposure, _ := demoCurrentExposure(ctx, adapter, id)
		state.SetExposure(exposure)
		return err
	}
	state.SetExposure(decimal.Zero)
	return nil
}

func closeOKXPerpDemoExposure(ctx context.Context, adapter *Adapter, id model.InstrumentID, spec demoPerpSpec, state *demoPerpCleanupState) (*model.Order, error) {
	if !state.CloseAuthorized() {
		return nil, nil
	}
	maxCloseQty := state.CloseLimit()
	exposure, err := demoCurrentExposure(ctx, adapter, id)
	if err != nil {
		return nil, err
	}
	closeQty, err := demoPerpCloseQuantity(exposure, maxCloseQty)
	if err != nil {
		return nil, err
	}
	if closeQty.IsZero() {
		return nil, nil
	}
	book, err := adapter.Market.OrderBook(ctx, id, 5)
	if err != nil {
		return nil, err
	}
	if len(book.Bids) == 0 {
		return nil, fmt.Errorf("cannot close long exposure: empty bid book")
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
		Quantity:     closeQty,
		Price:        price,
		PositionSide: enums.PosNet,
		ReduceOnly:   true,
	})
	if err != nil {
		recoveryErr := recoverAmbiguousOKXPerpDemoOrder(ctx, adapter, spec, state, clientID)
		if recoveryErr != nil {
			return nil, fmt.Errorf("submit single bounded close (outcome ambiguous; not retried): %w; client-ID recovery failed: %v", err, recoveryErr)
		}
		return nil, fmt.Errorf("submit single bounded close returned an error after the lifecycle order was recovered; not retried: %w", err)
	}
	state.RecordVenueOrderID(clientID, order.VenueOrderID)
	return order, nil
}

func demoPerpCloseQuantity(exposure, maxCloseQty decimal.Decimal) (decimal.Decimal, error) {
	if !maxCloseQty.IsPositive() {
		return decimal.Zero, fmt.Errorf("automatic close requires a positive authoritative fill quantity")
	}
	if exposure.IsZero() {
		return decimal.Zero, nil
	}
	if exposure.IsNegative() {
		return decimal.Zero, fmt.Errorf("refusing automatic close of unexpected short exposure %s after a buy lifecycle", exposure)
	}
	if exposure.GreaterThan(maxCloseQty) {
		return decimal.Zero, fmt.Errorf("refusing automatic close: current exposure %s exceeds authoritative lifecycle fill %s", exposure, maxCloseQty)
	}
	return exposure, nil
}

func validateOKXPerpDemoFill(resp *okx.Order, spec demoPerpSpec, maxNotional decimal.Decimal) (decimal.Decimal, error) {
	if resp == nil {
		return decimal.Zero, fmt.Errorf("missing OKX Perp Demo fill response")
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
	notional := qty.Mul(demoPerpNotionalPerContract(spec, price))
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
