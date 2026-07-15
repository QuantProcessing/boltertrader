package runtimeaccept

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	btruntime "github.com/QuantProcessing/boltertrader/runtime"
	runtimeexec "github.com/QuantProcessing/boltertrader/runtime/exec"
	"github.com/QuantProcessing/boltertrader/runtime/lifecycle"
	"github.com/shopspring/decimal"
)

type OrderLifecycleSpec struct {
	Label          string
	Venue          string
	Environment    string
	Product        string
	AccountID      string
	InstrumentID   model.InstrumentID
	Quantity       decimal.Decimal
	CloseQuantity  decimal.Decimal
	RestingPrice   decimal.Decimal
	FillPrice      decimal.Decimal
	ClosePrice     decimal.Decimal
	PositionSide   enums.PositionSide
	CloseAfterFill bool
	// AllowVenuePriceImprovement permits a venue to clamp an order limit only in
	// the trader-favorable direction (sell up, buy down). The default remains
	// exact price preservation.
	AllowVenuePriceImprovement bool
	// Deprecated: acceptance lifecycles never modify pre-existing positions.
	// The field remains for source compatibility and is otherwise ignored.
	CleanExistingPosition bool
	// CleanupPositionLimit caps emergency position cleanup in instrument quantity
	// units. A zero value defaults to Quantity.
	CleanupPositionLimit decimal.Decimal
	PrivateStreamTopics  []string
	PollInterval         time.Duration
	PollRequestTimeout   time.Duration
	CleanupTimeout       time.Duration
	BeforeRuntimeClose   func(context.Context, decimal.Decimal) error
	Logf                 func(format string, args ...any)

	spotBalanceGuard     *spotBalanceGuardConfig
	perpPositionReporter PerpPositionReporter
}

// PerpPositionReporter supplies authoritative account-backed positions when a
// venue serves them through its account client instead of its execution client.
type PerpPositionReporter interface {
	Positions(context.Context) ([]model.Position, error)
}

type spotBalanceGuardConfig struct {
	reporter      accountStateSource
	baseCurrency  string
	sizeStep      decimal.Decimal
	minQty        decimal.Decimal
	minNotional   decimal.Decimal
	feeReserve    decimal.Decimal
	closeQuantity decimal.Decimal
	closePrice    decimal.Decimal
}

type accountStateSource interface {
	AccountState(context.Context) (model.AccountState, error)
}

type spotBalanceSnapshot struct {
	total    decimal.Decimal
	borrowed decimal.Decimal
}

type spotBalanceSession struct {
	baseline          spotBalanceSnapshot
	observedFilledQty decimal.Decimal
	plannedCloseQty   decimal.Decimal
	complete          bool
}

type lifecycleOrderTracker struct {
	orders map[string]*trackedLifecycleOrder
}

type trackedLifecycleOrder struct {
	kind                  string
	clientID              string
	venueOrderID          string
	request               model.OrderRequest
	status                enums.OrderStatus
	filledQty             decimal.Decimal
	terminal              bool
	authoritativeTerminal bool
	reconciled            bool
	canceledVenueOrderID  string
}

type ambiguousLifecycleOrderError struct {
	err error
}

const lifecycleEvidenceStableObservations = 3

func (e *ambiguousLifecycleOrderError) Error() string { return e.err.Error() }
func (e *ambiguousLifecycleOrderError) Unwrap() error { return e.err }

func newLifecycleOrderTracker() *lifecycleOrderTracker {
	return &lifecycleOrderTracker{orders: make(map[string]*trackedLifecycleOrder)}
}

func (t *lifecycleOrderTracker) add(kind string) *trackedLifecycleOrder {
	order := &trackedLifecycleOrder{kind: kind, clientID: orderLifecycleClientID(kind)}
	t.orders[order.clientID] = order
	return order
}

func (t *lifecycleOrderTracker) observe(tracked *trackedLifecycleOrder, order *model.Order) {
	if t == nil || tracked == nil || order == nil {
		return
	}
	if order.Request.ClientID != "" && order.Request.ClientID != tracked.clientID {
		return
	}
	if order.VenueOrderID != "" {
		tracked.venueOrderID = order.VenueOrderID
	}
	if order.Request.ClientID != "" {
		tracked.request = order.Request
	}
	tracked.status = order.Status
	if order.FilledQty.GreaterThan(tracked.filledQty) {
		tracked.filledQty = order.FilledQty
	}
	if definitiveLifecycleTerminal(order.Status) {
		tracked.terminal = true
		tracked.authoritativeTerminal = true
	}
}

func (t *lifecycleOrderTracker) observeFill(tracked *trackedLifecycleOrder, fill model.Fill, cumulative decimal.Decimal) {
	if t == nil || tracked == nil {
		return
	}
	if fill.ClientID != "" && fill.ClientID != tracked.clientID {
		return
	}
	if fill.VenueOrderID != "" {
		tracked.venueOrderID = fill.VenueOrderID
	}
	if cumulative.GreaterThan(tracked.filledQty) {
		tracked.filledQty = cumulative
	}

}

func (t *lifecycleOrderTracker) byKind(kind string) *trackedLifecycleOrder {
	if t == nil {
		return nil
	}
	for _, order := range t.orders {
		if order.kind == kind {
			return order
		}
	}
	return nil
}

func (t *lifecycleOrderTracker) lifecycleCreatedBuyFilledQty() decimal.Decimal {
	if t == nil {
		return decimal.Zero
	}
	total := decimal.Zero
	for _, kind := range []string{"rest", "fill"} {
		order := t.byKind(kind)
		if order == nil || order.request.Side != enums.SideBuy {
			continue
		}
		total = total.Add(order.filledQty)
	}
	return total
}

func (t *lifecycleOrderTracker) closeRetrySafe() bool {
	closeAttempt := t.byKind("close")
	if closeAttempt == nil || !closeAttempt.authoritativeTerminal || closeAttempt.filledQty.IsPositive() {
		return false
	}
	switch closeAttempt.status {
	case enums.StatusCanceled, enums.StatusRejected, enums.StatusExpired:
		return true
	default:
		return false
	}
}

func definitiveLifecycleTerminal(status enums.OrderStatus) bool {
	switch status {
	case enums.StatusFilled, enums.StatusCanceled, enums.StatusRejected, enums.StatusExpired:
		return true
	default:
		return false
	}
}

func isAmbiguousLifecycleOrderError(err error) bool {
	var target *ambiguousLifecycleOrderError
	return errors.As(err, &target)
}

func isDefinitiveSubmitError(err error) bool {
	return errors.Is(err, contract.ErrVenueRejected) || errors.Is(err, runtimeexec.ErrVenueRejected)
}

type spotBalanceInvariantError struct {
	err error
}

func (e *spotBalanceInvariantError) Error() string { return e.err.Error() }
func (e *spotBalanceInvariantError) Unwrap() error { return e.err }

// ConfigureSpotBalanceGuard adds authoritative base-balance cleanup evidence to
// a Spot acceptance lifecycle without changing the public lifecycle API.
func ConfigureSpotBalanceGuard(
	spec OrderLifecycleSpec,
	reporter accountStateSource,
	baseCurrency string,
	sizeStep, minQty, minNotional, feeReserve decimal.Decimal,
) OrderLifecycleSpec {
	spec.spotBalanceGuard = &spotBalanceGuardConfig{
		reporter:      reporter,
		baseCurrency:  strings.ToUpper(strings.TrimSpace(baseCurrency)),
		sizeStep:      sizeStep,
		minQty:        minQty,
		minNotional:   minNotional,
		feeReserve:    feeReserve,
		closeQuantity: spec.CloseQuantity,
		closePrice:    spec.ClosePrice,
	}
	return spec
}

// ConfigurePerpPositionReporter selects an account-backed position source for
// Perp lifecycle evidence. Order submit, cancel, status, and fill operations
// continue to use the lifecycle ExecutionClient.
func ConfigurePerpPositionReporter(spec OrderLifecycleSpec, reporter PerpPositionReporter) OrderLifecycleSpec {
	spec.perpPositionReporter = reporter
	return spec
}

type OrderLifecycleResult struct {
	Resting   model.Order
	Filled    model.Order
	Closed    model.Order
	FilledQty decimal.Decimal
	ClosedQty decimal.Decimal
}

func RunAdapterOrderLifecycle(ctx context.Context, exec contract.ExecutionClient, spec OrderLifecycleSpec) (result *OrderLifecycleResult, resultErr error) {
	if err := validateOrderLifecycleSpec(spec); err != nil {
		return nil, err
	}
	spec.logAcceptanceStart("adapter")
	if spec.InstrumentID.Kind != enums.KindSpot {
		if err := rejectPreExistingPositions(ctx, exec, spec); err != nil {
			return nil, fmt.Errorf("%s position preflight: %w", spec.label(), err)
		}
	}
	if err := waitForNoOpenOrders(ctx, exec, spec); err != nil {
		return nil, fmt.Errorf("%s open-order preflight: %w", spec.label(), err)
	}
	spotSession, err := startSpotBalanceSession(ctx, spec)
	if err != nil {
		return nil, fmt.Errorf("%s spot balance baseline: %w", spec.label(), err)
	}
	tracker := newLifecycleOrderTracker()
	cleanupPerp := spec.InstrumentID.Kind != enums.KindSpot
	observedFilledQty := decimal.Zero
	closeOutcomeAmbiguous := false
	defer func() {
		if lifecycleFilledQty := tracker.lifecycleCreatedBuyFilledQty(); lifecycleFilledQty.GreaterThan(observedFilledQty) {
			observedFilledQty = lifecycleFilledQty
		}
		if spotSession != nil && !spotSession.complete {
			if observedFilledQty.GreaterThan(spotSession.observedFilledQty) {
				spotSession.observedFilledQty = observedFilledQty
				if qty, err := spec.closeQuantity(observedFilledQty); err == nil {
					spotSession.plannedCloseQty = qty
				}
			}
			cleanupErr := cleanupSpotOrderLifecycle(exec, spec, spotSession, tracker)
			if cleanupErr != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("%s spot cleanup blocked: %w", spec.label(), cleanupErr))
			}
		}
		if cleanupPerp {
			cleanupErr := cleanupPerpOrderLifecycle(exec, spec, tracker, observedFilledQty, !closeOutcomeAmbiguous)
			if cleanupErr != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("%s emergency Perp cleanup: %w", spec.label(), cleanupErr))
			}
		}
	}()
	ordersComplete := false
	defer func() {
		if ordersComplete {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), spec.cleanupTimeout())
		defer cancel()
		if cleanupErr := cancelLifecycleOpenOrders(cleanupCtx, exec, spec, tracker); cleanupErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("%s exact lifecycle order cleanup: %w", spec.label(), cleanupErr))
		}
	}()

	restingTracked := tracker.add("rest")
	restingTracked.request = model.OrderRequest{
		AccountID: spec.AccountID, ClientID: restingTracked.clientID, InstrumentID: spec.InstrumentID,
		Side: enums.SideBuy, Type: enums.TypeLimit, TIF: enums.TifGTX, Quantity: spec.Quantity,
		Price: spec.RestingPrice, PositionSide: spec.PositionSide,
	}
	resting, err := exec.Submit(ctx, restingTracked.request)
	if resting != nil {
		if evidenceErr := ensureTrackedOrder(spec, "resting_submit", restingTracked, resting); evidenceErr != nil {
			return nil, evidenceErr
		}
		tracker.observe(restingTracked, resting)
	}
	if err != nil {
		return nil, fmt.Errorf("%s submit resting order: %w", spec.label(), err)
	}
	if resting == nil {
		return nil, fmt.Errorf("%s submit resting order returned nil", spec.label())
	}
	if err := ensureOrderAccount(spec, "resting_order", resting); err != nil {
		return nil, err
	}
	spec.logOrder("resting_order", resting, resting.FilledQty)
	if resting.Status == enums.StatusFilled || !resting.FilledQty.IsZero() {
		return nil, fmt.Errorf("%s resting order unexpectedly filled: %+v", spec.label(), *resting)
	}
	if definitiveLifecycleTerminal(resting.Status) {
		return nil, fmt.Errorf("%s resting order reached terminal status %s with zero fill", spec.label(), resting.Status)
	}
	if resting.Status != enums.StatusUnknown && resting.VenueOrderID != "" {
		if err := exec.Cancel(ctx, spec.InstrumentID, resting.VenueOrderID); err != nil {
			return nil, fmt.Errorf("%s cancel resting order %s: %w", spec.label(), resting.VenueOrderID, err)
		}
		restingTracked.canceledVenueOrderID = resting.VenueOrderID
	}
	if err := waitForTrackedOrdersSettled(ctx, exec, spec, tracker, []*trackedLifecycleOrder{restingTracked}, true); err != nil {
		return nil, fmt.Errorf("%s wait for resting order cancel: %w", spec.label(), err)
	}
	if restingTracked.status == enums.StatusFilled || restingTracked.filledQty.IsPositive() {
		return nil, fmt.Errorf("%s resting order unexpectedly filled during cancel: status=%s filled_qty=%s", spec.label(), restingTracked.status, restingTracked.filledQty)
	}
	spec.logf("canceled_order label=%q client_id=%s venue_order_id=%s cleanup=no_open_orders", spec.label(), resting.Request.ClientID, resting.VenueOrderID)
	if spotSession != nil {
		if err := waitForSpotRestingCancelSettlement(ctx, spec, spotSession); err != nil {
			return nil, fmt.Errorf("%s spot balance guard after resting cancel: %w", spec.label(), err)
		}
	}

	filled, filledQty, err := submitAndWaitFilled(ctx, exec, spec, tracker, "fill", enums.SideBuy, spec.FillPrice, false, spec.Quantity)
	if err != nil {
		return nil, err
	}
	observedFilledQty = filledQty
	closeQty := decimal.Zero
	if spec.CloseAfterFill {
		closeQty, err = spec.closeQuantity(filledQty)
		if err != nil {
			return nil, fmt.Errorf("%s close quantity: %w", spec.label(), err)
		}
	}
	if spec.perpPositionReporter != nil {
		if err := waitForLifecycleLongPosition(ctx, exec, spec, filledQty); err != nil {
			return nil, fmt.Errorf("%s wait for account-backed position after fill: %w", spec.label(), err)
		}
		spec.logf("position_evidence label=%q source=account quantity=%s", spec.label(), filledQty)
	}
	if spotSession != nil {
		spotSession.observedFilledQty = filledQty
		spotSession.plannedCloseQty = closeQty
		if err := waitForSpotFillSettlement(ctx, spec, spotSession); err != nil {
			return nil, fmt.Errorf("%s spot balance guard after fill: %w", spec.label(), err)
		}
	}
	result = &OrderLifecycleResult{Resting: *resting, Filled: *filled, FilledQty: filledQty}

	if spec.CloseAfterFill {
		closeOrder, closedQty, err := submitAndWaitFilled(
			ctx,
			exec,
			spec,
			tracker,
			"close",
			enums.SideSell,
			spec.ClosePrice,
			spec.InstrumentID.Kind != enums.KindSpot,
			closeQty,
		)
		if err != nil {
			closeOutcomeAmbiguous = isAmbiguousLifecycleOrderError(err) && !tracker.closeRetrySafe()
			return nil, err
		}
		result.Closed = *closeOrder
		result.ClosedQty = closedQty
		if closedQty.LessThan(closeQty) {
			return nil, fmt.Errorf("%s close order reached terminal status with partial fill %s/%s", spec.label(), closedQty, closeQty)
		}
		if err := waitForNoOpenOrders(ctx, exec, spec); err != nil {
			return nil, fmt.Errorf("%s wait for no open orders after close: %w", spec.label(), err)
		}
		spec.logf("cleanup label=%q cleanup=no_open_orders", spec.label())
		if spec.InstrumentID.Kind != enums.KindSpot {
			if err := waitForFlatPosition(ctx, exec, spec); err != nil {
				return nil, fmt.Errorf("%s wait for flat position: %w", spec.label(), err)
			}
			cleanupPerp = false
			spec.logf("cleanup label=%q cleanup=flat_position", spec.label())
		}
		if spotSession != nil {
			if err := waitForSpotFinalBalance(ctx, spec, spotSession); err != nil {
				return nil, fmt.Errorf("%s spot cleanup blocked: %w", spec.label(), err)
			}
			spec.logf("cleanup label=%q cleanup=authoritative_spot_balance", spec.label())
		}
	}
	if spotSession != nil {
		spotSession.complete = true
	}
	ordersComplete = true
	return result, nil
}

func RunRuntimeOrderLifecycle(ctx context.Context, node *btruntime.TradingNode, venueExec contract.ExecutionClient, spec OrderLifecycleSpec) (result *OrderLifecycleResult, resultErr error) {
	if node == nil || node.Exec == nil {
		return nil, fmt.Errorf("%s runtime execution engine is required", spec.label())
	}
	if venueExec == nil {
		return nil, fmt.Errorf("%s venue execution client is required for exact runtime lifecycle cleanup", spec.label())
	}
	if err := validateOrderLifecycleSpec(spec); err != nil {
		return nil, err
	}
	spec.logAcceptanceStart("runtime")
	if err := WaitForActive(ctx, node); err != nil {
		return nil, fmt.Errorf("%s runtime did not become active before lifecycle: %w", spec.label(), err)
	}
	if spec.InstrumentID.Kind != enums.KindSpot {
		if err := rejectPreExistingPositions(ctx, venueExec, spec); err != nil {
			return nil, fmt.Errorf("%s position preflight: %w", spec.label(), err)
		}
	}
	if err := waitForNoOpenOrders(ctx, venueExec, spec); err != nil {
		return nil, fmt.Errorf("%s open-order preflight: %w", spec.label(), err)
	}
	spotSession, err := startSpotBalanceSession(ctx, spec)
	if err != nil {
		return nil, fmt.Errorf("%s spot balance baseline: %w", spec.label(), err)
	}
	tracker := newLifecycleOrderTracker()
	cleanupPerp := spec.InstrumentID.Kind != enums.KindSpot
	observedFilledQty := decimal.Zero
	closeOutcomeAmbiguous := false
	defer func() {
		if lifecycleFilledQty := tracker.lifecycleCreatedBuyFilledQty(); lifecycleFilledQty.GreaterThan(observedFilledQty) {
			observedFilledQty = lifecycleFilledQty
		}
		if spotSession != nil && !spotSession.complete {
			if observedFilledQty.GreaterThan(spotSession.observedFilledQty) {
				spotSession.observedFilledQty = observedFilledQty
				if qty, err := spec.closeQuantity(observedFilledQty); err == nil {
					spotSession.plannedCloseQty = qty
				}
			}
			cleanupErr := cleanupSpotOrderLifecycle(venueExec, spec, spotSession, tracker)
			if cleanupErr != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("%s spot cleanup blocked: %w", spec.label(), cleanupErr))
			}
		}
		if cleanupPerp {
			cleanupErr := cleanupPerpOrderLifecycle(venueExec, spec, tracker, observedFilledQty, !closeOutcomeAmbiguous)
			if cleanupErr != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("%s emergency Perp cleanup: %w", spec.label(), cleanupErr))
			}
		}
	}()
	ordersComplete := false
	defer func() {
		if ordersComplete {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), spec.cleanupTimeout())
		defer cancel()
		if cleanupErr := cancelLifecycleOpenOrders(cleanupCtx, venueExec, spec, tracker); cleanupErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("%s exact runtime lifecycle order cleanup: %w", spec.label(), cleanupErr))
		}
	}()

	restingTracked := tracker.add("rest")
	restingTracked.request = model.OrderRequest{
		AccountID: spec.AccountID, ClientID: restingTracked.clientID, InstrumentID: spec.InstrumentID,
		Side: enums.SideBuy, Type: enums.TypeLimit, TIF: enums.TifGTX, Quantity: spec.Quantity,
		Price: spec.RestingPrice, PositionSide: spec.PositionSide,
	}
	resting, err := node.Exec.Submit(ctx, restingTracked.request)
	if resting != nil {
		if evidenceErr := ensureTrackedOrder(spec, "runtime_resting_submit", restingTracked, resting); evidenceErr != nil {
			return nil, evidenceErr
		}
		tracker.observe(restingTracked, resting)
	}
	if err != nil {
		return nil, fmt.Errorf("%s runtime submit resting order: %w", spec.label(), err)
	}
	if resting == nil {
		return nil, fmt.Errorf("%s runtime submit resting order returned nil", spec.label())
	}
	if err := ensureOrderAccount(spec, "runtime_resting_order", resting); err != nil {
		return nil, err
	}
	spec.logOrder("runtime_resting_order", resting, resting.FilledQty)
	if resting.Status == enums.StatusFilled || !resting.FilledQty.IsZero() {
		return nil, fmt.Errorf("%s runtime resting order unexpectedly filled: %+v", spec.label(), *resting)
	}
	if definitiveLifecycleTerminal(resting.Status) {
		return nil, fmt.Errorf("%s runtime resting order reached terminal status %s with zero fill", spec.label(), resting.Status)
	}
	if resting.Status != enums.StatusUnknown && resting.VenueOrderID != "" {
		if err := node.Exec.Cancel(ctx, resting.Request.ClientID); err != nil {
			return nil, fmt.Errorf("%s runtime cancel resting order %s: %w", spec.label(), resting.VenueOrderID, err)
		}
		restingTracked.canceledVenueOrderID = resting.VenueOrderID
		if err := WaitForOrderStatus(ctx, node, resting.Request.ClientID, enums.StatusCanceled); err != nil {
			return nil, fmt.Errorf("%s runtime cache did not observe resting cancel: %w", spec.label(), err)
		}
	}
	spec.logf("runtime_canceled_order label=%q client_id=%s venue_order_id=%s cleanup=runtime_cache_canceled", spec.label(), resting.Request.ClientID, resting.VenueOrderID)
	if err := waitForTrackedOrdersSettled(ctx, venueExec, spec, tracker, []*trackedLifecycleOrder{restingTracked}, true); err != nil {
		return nil, fmt.Errorf("%s wait for resting venue terminal evidence: %w", spec.label(), err)
	}
	reconciledResting := *resting
	reconciledResting.Request = restingTracked.request
	reconciledResting.VenueOrderID = restingTracked.venueOrderID
	reconciledResting.Status = restingTracked.status
	reconciledResting.FilledQty = restingTracked.filledQty
	node.Cache.UpsertOrder(reconciledResting)
	node.Exec.ResolveInFlight(restingTracked.clientID, restingTracked.venueOrderID, time.Now())
	if restingTracked.status == enums.StatusFilled || restingTracked.filledQty.IsPositive() {
		return nil, fmt.Errorf("%s runtime resting order unexpectedly filled during cancel: status=%s filled_qty=%s", spec.label(), restingTracked.status, restingTracked.filledQty)
	}
	spec.logf("cleanup label=%q cleanup=no_open_orders", spec.label())
	if spotSession != nil {
		if err := waitForSpotRestingCancelSettlement(ctx, spec, spotSession); err != nil {
			return nil, fmt.Errorf("%s spot balance guard after runtime resting cancel: %w", spec.label(), err)
		}
	}

	portfolioBeforeFill := node.Portfolio.NetQtyForAccount(spec.AccountID, spec.InstrumentID, spec.PositionSide)
	filled, filledQty, err := submitRuntimeAndWaitFilled(ctx, node, venueExec, spec, tracker, "fill", enums.SideBuy, spec.FillPrice, false, spec.Quantity)
	if err != nil {
		return nil, err
	}
	observedFilledQty = filledQty
	closeQty := decimal.Zero
	if spec.CloseAfterFill {
		closeQty, err = spec.closeQuantity(filledQty)
		if err != nil {
			return nil, fmt.Errorf("%s runtime close quantity: %w", spec.label(), err)
		}
		portfolioCtx, cancel := spec.pollCallContext(ctx)
		err = waitForRuntimePortfolioIncrease(portfolioCtx, node, spec, portfolioBeforeFill, closeQty)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("%s wait for runtime portfolio opening fill: %w", spec.label(), err)
		}
	}
	if spec.perpPositionReporter != nil {
		if err := waitForLifecycleLongPosition(ctx, venueExec, spec, filledQty); err != nil {
			return nil, fmt.Errorf("%s wait for account-backed position after runtime fill: %w", spec.label(), err)
		}
		spec.logf("position_evidence label=%q source=account quantity=%s", spec.label(), filledQty)
	}
	if spotSession != nil {
		spotSession.observedFilledQty = filledQty
		spotSession.plannedCloseQty = closeQty
		if err := waitForSpotFillSettlement(ctx, spec, spotSession); err != nil {
			return nil, fmt.Errorf("%s spot balance guard after runtime fill: %w", spec.label(), err)
		}
	}
	result = &OrderLifecycleResult{Resting: *resting, Filled: *filled, FilledQty: filledQty}

	if spec.CloseAfterFill {
		if spec.BeforeRuntimeClose != nil {
			if err := spec.BeforeRuntimeClose(ctx, closeQty); err != nil {
				return nil, fmt.Errorf("%s runtime close readiness: %w", spec.label(), err)
			}
		}
		closeOrder, closedQty, err := submitRuntimeAndWaitFilled(
			ctx,
			node,
			venueExec,
			spec,
			tracker,
			"close",
			enums.SideSell,
			spec.ClosePrice,
			spec.InstrumentID.Kind != enums.KindSpot,
			closeQty,
		)
		if err != nil {
			closeOutcomeAmbiguous = isAmbiguousLifecycleOrderError(err) && !tracker.closeRetrySafe()
			return nil, err
		}
		result.Closed = *closeOrder
		result.ClosedQty = closedQty
		if closedQty.LessThan(closeQty) {
			return nil, fmt.Errorf("%s runtime close order reached terminal status with partial fill %s/%s", spec.label(), closedQty, closeQty)
		}
		if spec.InstrumentID.Kind != enums.KindSpot {
			if err := waitForFlatPosition(ctx, venueExec, spec); err != nil {
				return nil, fmt.Errorf("%s wait for venue flat position: %w", spec.label(), err)
			}
			if err := WaitForPortfolioFlat(ctx, node, spec.InstrumentID, decimal.Zero); err != nil {
				return nil, fmt.Errorf("%s wait for runtime portfolio flat: %w", spec.label(), err)
			}
			cleanupPerp = false
		}
		if spotSession != nil {
			if err := waitForSpotFinalBalance(ctx, spec, spotSession); err != nil {
				return nil, fmt.Errorf("%s spot cleanup blocked: %w", spec.label(), err)
			}
			spec.logf("cleanup label=%q cleanup=authoritative_spot_balance", spec.label())
		}
	}
	if err := waitForNoOpenOrders(ctx, venueExec, spec); err != nil {
		return nil, fmt.Errorf("%s wait for no venue open orders after runtime lifecycle: %w", spec.label(), err)
	}
	spec.logf("cleanup label=%q cleanup=no_open_orders", spec.label())
	if open := node.Cache.OpenOrders(); len(open) != 0 {
		return nil, fmt.Errorf("%s runtime cache has %d open orders after lifecycle: %+v", spec.label(), len(open), open)
	}
	if spotSession != nil {
		spotSession.complete = true
	}
	ordersComplete = true
	spec.logf("cleanup label=%q cleanup=runtime_cache_no_open_orders", spec.label())
	return result, nil
}

func submitRuntimeAndWaitFilled(ctx context.Context, node *btruntime.TradingNode, venueExec contract.ExecutionClient, spec OrderLifecycleSpec, tracker *lifecycleOrderTracker, idKind string, side enums.OrderSide, price decimal.Decimal, reduceOnly bool, qty decimal.Decimal) (*model.Order, decimal.Decimal, error) {
	if err := waitForRuntimeSubmitReady(ctx, node, reduceOnly); err != nil {
		return nil, decimal.Zero, fmt.Errorf("%s runtime was not ready before %s submit: %w", spec.label(), idKind, err)
	}
	tracked := tracker.add(idKind)
	req := model.OrderRequest{
		AccountID:    spec.AccountID,
		ClientID:     tracked.clientID,
		InstrumentID: spec.InstrumentID,
		Side:         side,
		Type:         enums.TypeLimit,
		TIF:          enums.TifIOC,
		Quantity:     qty,
		Price:        price,
		PositionSide: spec.PositionSide,
		ReduceOnly:   reduceOnly,
	}
	tracked.request = req
	order, err := node.Exec.Submit(ctx, req)
	if order != nil {
		if evidenceErr := ensureTrackedOrder(spec, "runtime_"+idKind+"_submit", tracked, order); evidenceErr != nil {
			return nil, decimal.Zero, evidenceErr
		}
		tracker.observe(tracked, order)
	}
	if err != nil {
		if isDefinitiveSubmitError(err) {
			if order == nil && tracked.venueOrderID == "" {
				tracked.status = enums.StatusRejected
				tracked.terminal = true
				tracked.authoritativeTerminal = true
			}
			return nil, decimal.Zero, fmt.Errorf("%s runtime submit %s order: %w", spec.label(), idKind, err)
		}
		recovered, filledQty, recoverErr := recoverAmbiguousIOC(ctx, venueExec, spec, tracker, tracked, req)
		if recoverErr != nil {
			return nil, decimal.Zero, &ambiguousLifecycleOrderError{err: fmt.Errorf("%s runtime submit %s order: %w", spec.label(), idKind, errors.Join(err, recoverErr))}
		}
		node.Cache.UpsertOrder(*recovered)
		node.Exec.ResolveInFlight(recovered.Request.ClientID, recovered.VenueOrderID, time.Now())
		spec.logOrder("runtime_recovered_"+filledEventName(idKind), recovered, filledQty)
		return recovered, filledQty, nil
	}
	if order == nil {
		return nil, decimal.Zero, fmt.Errorf("%s runtime submit %s order returned nil", spec.label(), idKind)
	}
	if err := ensureOrderAccount(spec, "runtime_"+idKind+"_order", order); err != nil {
		return nil, decimal.Zero, err
	}
	cached, filledQty, err := waitForRuntimeFilledQty(ctx, node, spec, tracker, tracked, order.Request.ClientID, qty)
	if err != nil {
		wrapped := fmt.Errorf("%s runtime wait for %s fill: %w", spec.label(), idKind, err)
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return nil, decimal.Zero, &ambiguousLifecycleOrderError{err: wrapped}
		}
		return nil, decimal.Zero, wrapped
	}
	if err := ensureOrderAccount(spec, "runtime_cached_"+idKind+"_order", &cached); err != nil {
		return nil, decimal.Zero, err
	}
	spec.logOrder("runtime_"+filledEventName(idKind), &cached, filledQty)
	return &cached, filledQty, nil
}

func waitForRuntimeSubmitReady(ctx context.Context, node *btruntime.TradingNode, reduceOnly bool) error {
	if node == nil {
		return errors.New("runtime node is nil")
	}
	var last lifecycle.Snapshot
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		last = node.State()
		if last.Node == lifecycle.NodeRunning && last.Trading == lifecycle.TradingActive {
			return nil
		}
		if reduceOnly && last.Node == lifecycle.NodeRunning && last.Trading == lifecycle.TradingReducing {
			return nil
		}
		if last.Node == lifecycle.NodeFailed || last.Node == lifecycle.NodeStopped {
			return fmt.Errorf("runtime stopped before submit became safe; last=%+v", last)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for runtime submit readiness; last=%+v: %w", last, ctx.Err())
		case <-ticker.C:
		}
	}
}

func waitForRuntimePortfolioIncrease(ctx context.Context, node *btruntime.TradingNode, spec OrderLifecycleSpec, baseline, minimumIncrease decimal.Decimal) error {
	if node == nil || node.Portfolio == nil {
		return errors.New("runtime portfolio is required")
	}
	if !minimumIncrease.IsPositive() {
		return fmt.Errorf("minimum opening increase must be positive, got %s", minimumIncrease)
	}
	var last decimal.Decimal
	ticker := time.NewTicker(spec.interval())
	defer ticker.Stop()
	for {
		last = node.Portfolio.NetQtyForAccount(spec.AccountID, spec.InstrumentID, spec.PositionSide)
		if last.Sub(baseline).GreaterThanOrEqual(minimumIncrease) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for account=%s instrument=%s side=%s increase >= %s from baseline=%s; last=%s: %w", spec.AccountID, spec.InstrumentID, spec.PositionSide, minimumIncrease, baseline, last, ctx.Err())
		case <-ticker.C:
		}
	}
}

func submitAndWaitFilled(ctx context.Context, exec contract.ExecutionClient, spec OrderLifecycleSpec, tracker *lifecycleOrderTracker, idKind string, side enums.OrderSide, price decimal.Decimal, reduceOnly bool, qty decimal.Decimal) (*model.Order, decimal.Decimal, error) {
	tracked := tracker.add(idKind)
	req := model.OrderRequest{
		AccountID:    spec.AccountID,
		ClientID:     tracked.clientID,
		InstrumentID: spec.InstrumentID,
		Side:         side,
		Type:         enums.TypeLimit,
		TIF:          enums.TifIOC,
		Quantity:     qty,
		Price:        price,
		PositionSide: spec.PositionSide,
		ReduceOnly:   reduceOnly,
	}
	tracked.request = req
	order, err := exec.Submit(ctx, req)
	if order != nil {
		if evidenceErr := ensureTrackedOrder(spec, idKind+"_submit", tracked, order); evidenceErr != nil {
			return nil, decimal.Zero, evidenceErr
		}
		tracker.observe(tracked, order)
	}
	if err != nil {
		if isDefinitiveSubmitError(err) {
			if order == nil && tracked.venueOrderID == "" {
				tracked.status = enums.StatusRejected
				tracked.terminal = true
				tracked.authoritativeTerminal = true
			}
			return nil, decimal.Zero, fmt.Errorf("%s submit %s order: %w", spec.label(), idKind, err)
		}
		recovered, filledQty, recoverErr := recoverAmbiguousIOC(ctx, exec, spec, tracker, tracked, req)
		if recoverErr != nil {
			return nil, decimal.Zero, &ambiguousLifecycleOrderError{err: fmt.Errorf("%s submit %s order: %w", spec.label(), idKind, errors.Join(err, recoverErr))}
		}
		spec.logOrder("recovered_"+filledEventName(idKind), recovered, filledQty)
		return recovered, filledQty, nil
	}
	if order == nil {
		return nil, decimal.Zero, fmt.Errorf("%s submit %s order returned nil", spec.label(), idKind)
	}
	if err := ensureOrderAccount(spec, idKind+"_order", order); err != nil {
		return nil, decimal.Zero, err
	}
	filledQty, err := waitForFilledQty(ctx, exec, spec, tracker, tracked, *order)
	if err != nil {
		wrapped := fmt.Errorf("%s wait for %s fill: %w", spec.label(), idKind, err)
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return nil, decimal.Zero, &ambiguousLifecycleOrderError{err: wrapped}
		}
		return nil, decimal.Zero, wrapped
	}
	if filledQty.IsZero() {
		return nil, decimal.Zero, fmt.Errorf("%s %s order reported zero filled quantity: %+v", spec.label(), idKind, *order)
	}
	spec.logOrder(filledEventName(idKind), order, filledQty)
	return order, filledQty, nil
}

func recoverAmbiguousIOC(ctx context.Context, exec contract.ExecutionClient, spec OrderLifecycleSpec, tracker *lifecycleOrderTracker, tracked *trackedLifecycleOrder, req model.OrderRequest) (*model.Order, decimal.Decimal, error) {
	recoveryCtx, cancel := context.WithTimeout(ctx, spec.cleanupTimeout())
	defer cancel()
	ticker := time.NewTicker(spec.interval())
	defer ticker.Stop()
	var lastOrder model.Order
	var haveOrder bool
	absentOpenObservations := 0
	canceledVenueID := ""
	var lastErr error
	for {
		statusClientID, statusVenueOrderID := exactOrderQueryIDs(tracked.clientID, tracked.venueOrderID)
		statusAbsent := false
		statusNonTerminal := false
		callCtx, callCancel := spec.pollCallContext(recoveryCtx)
		report, statusErr := exec.GenerateOrderStatusReport(callCtx, model.SingleOrderStatusQuery{
			AccountID:    spec.AccountID,
			InstrumentID: spec.InstrumentID,
			ClientID:     statusClientID,
			VenueOrderID: statusVenueOrderID,
		})
		callCancel()
		if statusErr != nil {
			lastErr = statusErr
		} else if report == nil {
			statusAbsent = true
		} else {
			if err := ensureTrackedOrderStatusReport(spec, "ambiguous_order_status_report", tracked, report); err != nil {
				return nil, decimal.Zero, err
			}
			lastOrder = report.Order
			haveOrder = true
			tracker.observe(tracked, &lastOrder)
			statusNonTerminal = !definitiveLifecycleTerminal(lastOrder.Status)
		}

		fillClientID, fillVenueOrderID := exactOrderQueryIDs(tracked.clientID, tracked.venueOrderID)
		callCtx, callCancel = spec.pollCallContext(recoveryCtx)
		fills, fillErr := exec.GenerateFillReports(callCtx, model.FillReportQuery{
			AccountID:    spec.AccountID,
			InstrumentID: spec.InstrumentID,
			ClientID:     fillClientID,
			VenueOrderID: fillVenueOrderID,
		})
		callCancel()
		fillQty := tracked.filledQty
		if fillErr != nil {
			lastErr = fillErr
		} else {
			reportedFillQty, err := sumExactLifecycleFills(spec, "ambiguous_fill_report", tracked, fills)
			if err != nil {
				return nil, decimal.Zero, err
			}
			if reportedFillQty.GreaterThan(fillQty) {
				fillQty = reportedFillQty
			}
		}
		if haveOrder && lastOrder.FilledQty.GreaterThan(fillQty) {
			fillQty = lastOrder.FilledQty
		}
		if tracked.filledQty.GreaterThan(fillQty) {
			fillQty = tracked.filledQty
		}
		if haveOrder && definitiveLifecycleTerminal(lastOrder.Status) {
			tracked.terminal = true
			if !fillQty.IsPositive() {
				return nil, decimal.Zero, fmt.Errorf("order reached terminal status %s with zero fill", lastOrder.Status)
			}
			lastOrder.FilledQty = fillQty
			return &lastOrder, fillQty, nil
		}

		callCtx, callCancel = spec.pollCallContext(recoveryCtx)
		open, openErr := exec.OpenOrders(callCtx, spec.InstrumentID)
		callCancel()
		var openMatch *model.Order
		if openErr != nil {
			lastErr = openErr
		} else {
			for i := range open {
				candidate := &open[i]
				matches, err := trackedOpenOrderMatch(tracked, candidate)
				if err != nil {
					return nil, decimal.Zero, err
				}
				if !matches {
					continue
				}
				if err := ensureTrackedOrder(spec, "ambiguous_open_order", tracked, candidate); err != nil {
					return nil, decimal.Zero, err
				}
				openMatch = candidate
				tracker.observe(tracked, candidate)
				break
			}
		}
		if tracked.venueOrderID != "" && (openMatch != nil || statusNonTerminal || canceledVenueID != tracked.venueOrderID) {
			callCtx, callCancel = spec.pollCallContext(recoveryCtx)
			cancelErr := exec.Cancel(callCtx, spec.InstrumentID, tracked.venueOrderID)
			callCancel()
			if cancelErr != nil {
				lastErr = cancelErr
			} else {
				canceledVenueID = tracked.venueOrderID
				tracked.canceledVenueOrderID = tracked.venueOrderID
			}
		}
		venueCancellationProven := tracked.venueOrderID == "" || canceledVenueID == tracked.venueOrderID
		if statusAbsent && openErr == nil && openMatch == nil && venueCancellationProven {
			absentOpenObservations++
		} else {
			absentOpenObservations = 0
		}
		if fillQty.IsPositive() && absentOpenObservations >= 2 {
			if !haveOrder {
				lastOrder = model.Order{Request: req, VenueOrderID: tracked.venueOrderID, Status: enums.StatusCanceled}
			} else {
				lastOrder.Status = enums.StatusCanceled
			}
			lastOrder.FilledQty = fillQty
			tracker.observe(tracked, &lastOrder)
			tracked.terminal = true
			tracked.authoritativeTerminal = false
			return &lastOrder, fillQty, nil
		}
		select {
		case <-recoveryCtx.Done():
			return nil, decimal.Zero, fmt.Errorf("ambiguous IOC evidence unresolved for client_id=%s venue_order_id=%s lastErr=%v: %w", tracked.clientID, tracked.venueOrderID, lastErr, recoveryCtx.Err())
		case <-ticker.C:
		}
	}
}

func rejectPreExistingPositions(ctx context.Context, exec contract.ExecutionClient, spec OrderLifecycleSpec) error {
	reports, err := nonZeroPositionReports(ctx, exec, spec)
	if err != nil {
		return err
	}
	if len(reports) == 0 {
		return nil
	}
	return fmt.Errorf("pre-existing position for %s: side=%s quantity=%s", spec.InstrumentID, reports[0].Position.Side, reports[0].Position.Quantity)
}

func cleanLifecyclePosition(ctx context.Context, exec contract.ExecutionClient, spec OrderLifecycleSpec, tracker *lifecycleOrderTracker, observedFilledQty decimal.Decimal) error {
	reports, err := waitForStableCleanupPosition(ctx, exec, spec)
	if err != nil {
		return err
	}
	if len(reports) == 0 {
		return nil
	}
	if len(reports) != 1 {
		return fmt.Errorf("position cleanup blocked: ambiguous cleanup exposure for %s: %d non-zero position reports", spec.InstrumentID, len(reports))
	}
	position := reports[0].Position
	if position.Quantity.IsNegative() || position.Side == enums.PosShort {
		return fmt.Errorf("position cleanup blocked: cleanup exposure for %s is not lifecycle-created long exposure: side=%s quantity=%s", spec.InstrumentID, position.Side, position.Quantity)
	}
	qty := position.Quantity.Abs()
	limit := spec.cleanupPositionLimit()
	if observedFilledQty.LessThan(limit) {
		limit = observedFilledQty
	}
	if qty.GreaterThan(limit) {
		return fmt.Errorf("position cleanup blocked: cleanup position limit exceeded for %s: exposure=%s limit=%s observed_filled_qty=%s", spec.InstrumentID, qty, limit, observedFilledQty)
	}
	side := enums.SideSell
	order, _, err := submitAndWaitFilled(ctx, exec, spec, tracker, "cleanup", side, spec.ClosePrice, true, qty)
	if err != nil {
		cancelErr := cancelLifecycleOpenOrders(ctx, exec, spec, tracker)
		return errors.Join(err, cancelErr)
	}
	spec.logOrder("cleanup_order", order, qty)
	if err := waitForNoOpenOrders(ctx, exec, spec); err != nil {
		return err
	}
	if err := waitForFlatPosition(ctx, exec, spec); err != nil {
		return err
	}
	spec.logf("cleanup label=%q cleanup=emergency_flat_position", spec.label())
	return nil
}

func cleanupPerpOrderLifecycle(exec contract.ExecutionClient, spec OrderLifecycleSpec, tracker *lifecycleOrderTracker, observedFilledQty decimal.Decimal, allowFlatten bool) error {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), spec.cleanupTimeout())
	defer cancel()
	cancelErr := cancelLifecycleOpenOrders(cleanupCtx, exec, spec, tracker)
	if !observedFilledQty.IsPositive() {
		return errors.Join(cancelErr, fmt.Errorf("position cleanup not armed: lifecycle fill quantity was not successfully observed"))
	}
	if !allowFlatten {
		reports, err := waitForStableCleanupPosition(cleanupCtx, exec, spec)
		if err != nil {
			return errors.Join(cancelErr, err)
		}
		if len(reports) != 0 {
			return errors.Join(cancelErr, fmt.Errorf("position cleanup blocked: close outcome ambiguous; refusing an additional sell with %d non-zero position report(s)", len(reports)))
		}
		return cancelErr
	}
	flattenErr := cleanLifecyclePosition(cleanupCtx, exec, spec, tracker, observedFilledQty)
	return errors.Join(cancelErr, flattenErr)
}

func startSpotBalanceSession(ctx context.Context, spec OrderLifecycleSpec) (*spotBalanceSession, error) {
	if spec.spotBalanceGuard == nil {
		return nil, nil
	}
	baseline, err := readSpotBalance(ctx, spec)
	if err != nil {
		return nil, err
	}
	return &spotBalanceSession{baseline: baseline}, nil
}

func cleanupSpotOrderLifecycle(exec contract.ExecutionClient, spec OrderLifecycleSpec, session *spotBalanceSession, tracker *lifecycleOrderTracker) error {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), spec.cleanupTimeout())
	defer cancel()
	cancelErr := cancelLifecycleOpenOrders(cleanupCtx, exec, spec, tracker)
	var closeErr error
	openingFilledQty := tracker.lifecycleCreatedBuyFilledQty()
	closeAttempt := tracker.byKind("close")
	closeRetrySafe := closeAttempt == nil || (closeAttempt.authoritativeTerminal && !closeAttempt.filledQty.IsPositive())
	if openingFilledQty.IsPositive() && closeRetrySafe {
		if session.observedFilledQty.LessThan(openingFilledQty) {
			session.observedFilledQty = openingFilledQty
		}
		if !session.plannedCloseQty.IsPositive() {
			session.plannedCloseQty, closeErr = spec.closeQuantity(session.observedFilledQty)
		}
		if closeErr == nil {
			closeErr = waitForSpotFillSettlement(cleanupCtx, spec, session)
		}
		if closeErr == nil {
			var closedQty decimal.Decimal
			_, closedQty, closeErr = submitAndWaitFilled(cleanupCtx, exec, spec, tracker, "cleanup", enums.SideSell, spec.ClosePrice, false, session.plannedCloseQty)
			if closeErr == nil && closedQty.LessThan(session.plannedCloseQty) {
				closeErr = fmt.Errorf("Spot cleanup close reached terminal status with partial fill %s/%s", closedQty, session.plannedCloseQty)
			}
		}
		if exactErr := cancelLifecycleOpenOrders(cleanupCtx, exec, spec, tracker); exactErr != nil {
			closeErr = errors.Join(closeErr, exactErr)
		}
	}
	balanceErr := waitForSpotFinalBalance(cleanupCtx, spec, session)
	return errors.Join(cancelErr, closeErr, balanceErr)
}

func waitForSpotRestingCancelSettlement(ctx context.Context, spec OrderLifecycleSpec, session *spotBalanceSession) error {
	ctx, cancel := context.WithTimeout(ctx, spec.cleanupTimeout())
	defer cancel()
	stable := 0
	var lastErr error
	ticker := time.NewTicker(spec.interval())
	defer ticker.Stop()
	for {
		snapshot, err := readSpotBalance(ctx, spec)
		if err != nil {
			if isSpotBalanceInvariant(err) {
				return err
			}
			lastErr = err
			stable = 0
		} else {
			delta, err := validateSpotBalanceDelta(session.baseline, snapshot, decimal.Zero)
			if err != nil {
				return err
			}
			if !delta.IsZero() {
				return newSpotBalanceInvariant("base delta after resting cancel is %s, want 0", delta)
			}
			stable++
			if stable >= 2 {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out proving unchanged balance after resting cancel; lastErr=%v: %w", lastErr, ctx.Err())
		case <-ticker.C:
		}
	}
}

func waitForSpotFillSettlement(ctx context.Context, spec OrderLifecycleSpec, session *spotBalanceSession) error {
	ctx, cancel := context.WithTimeout(ctx, spec.cleanupTimeout())
	defer cancel()
	stable := 0
	var lastDelta decimal.Decimal
	var lastErr error
	ticker := time.NewTicker(spec.interval())
	defer ticker.Stop()
	for {
		snapshot, err := readSpotBalance(ctx, spec)
		if err != nil {
			if isSpotBalanceInvariant(err) {
				return err
			}
			lastErr = err
			stable = 0
		} else {
			delta, err := validateSpotBalanceDelta(session.baseline, snapshot, session.observedFilledQty)
			if err != nil {
				return err
			}
			lastDelta = delta
			guardedCloseQty := session.plannedCloseQty
			if delta.GreaterThanOrEqual(guardedCloseQty) {
				stable++
				if stable >= 2 {
					return nil
				}
			} else {
				stable = 0
				lastErr = fmt.Errorf("settled base delta %s is below guarded close quantity %s", delta, guardedCloseQty)
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out proving Spot fill settlement; lastDelta=%s lastErr=%v: %w", lastDelta, lastErr, ctx.Err())
		case <-ticker.C:
		}
	}
}

func waitForSpotFinalBalance(ctx context.Context, spec OrderLifecycleSpec, session *spotBalanceSession) error {
	ctx, cancel := context.WithTimeout(ctx, spec.cleanupTimeout())
	defer cancel()
	stable := 0
	var lastDelta decimal.Decimal
	var lastErr error
	ticker := time.NewTicker(spec.interval())
	defer ticker.Stop()
	for {
		snapshot, err := readSpotBalance(ctx, spec)
		if err != nil {
			if isSpotBalanceInvariant(err) {
				return err
			}
			lastErr = err
			stable = 0
		} else {
			delta, err := validateSpotBalanceDelta(session.baseline, snapshot, session.observedFilledQty)
			if err != nil {
				return err
			}
			lastDelta = delta
			guard := spec.spotBalanceGuard
			closeQty := session.plannedCloseQty
			actualReserve := session.observedFilledQty.Sub(closeQty)
			if actualReserve.IsNegative() {
				actualReserve = decimal.Zero
			}
			feeReserve := guard.feeReserve
			if actualReserve.LessThan(feeReserve) {
				feeReserve = actualReserve
			}
			maxResidual := feeReserve.Add(guard.sizeStep)
			switch {
			case delta.GreaterThan(maxResidual):
				stable = 0
				lastErr = fmt.Errorf("residual base delta %s exceeds fee reserve plus step %s", delta, maxResidual)
			case spotResidualSellable(delta, guard):
				stable = 0
				lastErr = fmt.Errorf("residual base delta %s remains sellable at close price %s", delta, guard.closePrice)
			default:
				stable++
				if stable >= 2 {
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out proving safe Spot residual; lastDelta=%s lastErr=%v: %w", lastDelta, lastErr, ctx.Err())
		case <-ticker.C:
		}
	}
}

func readSpotBalance(ctx context.Context, spec OrderLifecycleSpec) (spotBalanceSnapshot, error) {
	guard := spec.spotBalanceGuard
	callCtx, cancel := spec.pollCallContext(ctx)
	defer cancel()
	state, err := guard.reporter.AccountState(callCtx)
	if err != nil {
		return spotBalanceSnapshot{}, err
	}
	if err := state.Validate(); err != nil {
		return spotBalanceSnapshot{}, newSpotBalanceInvariant("invalid account state: %v", err)
	}
	if !state.Reported {
		return spotBalanceSnapshot{}, newSpotBalanceInvariant("account state is not authoritative (reported=false)")
	}
	if state.EventID == "" {
		return spotBalanceSnapshot{}, newSpotBalanceInvariant("account state event id is required")
	}
	if state.TsEvent.IsZero() {
		return spotBalanceSnapshot{}, newSpotBalanceInvariant("account state event timestamp is required")
	}
	if state.TsInit.IsZero() {
		return spotBalanceSnapshot{}, newSpotBalanceInvariant("account state init timestamp is required")
	}
	if state.AccountID != spec.AccountID {
		return spotBalanceSnapshot{}, newSpotBalanceInvariant("account mismatch: state account_id=%q, want %q", state.AccountID, spec.AccountID)
	}
	venue := spec.Venue
	if venue == "" {
		venue = spec.InstrumentID.Venue
	}
	if !strings.EqualFold(strings.TrimSpace(state.Venue), strings.TrimSpace(venue)) {
		return spotBalanceSnapshot{}, newSpotBalanceInvariant("venue mismatch: state venue=%q, want %q", state.Venue, venue)
	}
	var match *model.AccountBalance
	for i := range state.Balances {
		balance := &state.Balances[i]
		if !strings.EqualFold(strings.TrimSpace(balance.Currency), guard.baseCurrency) {
			continue
		}
		if match != nil {
			return spotBalanceSnapshot{}, newSpotBalanceInvariant("base balance mismatch: multiple %s balances reported", guard.baseCurrency)
		}
		match = balance
	}
	if match == nil {
		// Authoritative venue snapshots commonly omit zero balances. Treat an
		// absent target asset as zero only after the account, venue, and reported
		// invariants above have succeeded.
		return spotBalanceSnapshot{}, nil
	}
	if match.AccountID != spec.AccountID {
		return spotBalanceSnapshot{}, newSpotBalanceInvariant("account mismatch: %s balance account_id=%q, want %q", guard.baseCurrency, match.AccountID, spec.AccountID)
	}
	if match.Borrowed.IsNegative() {
		return spotBalanceSnapshot{}, newSpotBalanceInvariant("%s borrowed balance is negative: %s", guard.baseCurrency, match.Borrowed)
	}
	return spotBalanceSnapshot{total: match.Total, borrowed: match.Borrowed}, nil
}

func validateSpotBalanceDelta(baseline, current spotBalanceSnapshot, observedFilledQty decimal.Decimal) (decimal.Decimal, error) {
	delta := current.total.Sub(baseline.total)
	if delta.IsNegative() {
		return delta, newSpotBalanceInvariant("negative base delta %s would consume pre-existing inventory", delta)
	}
	if delta.GreaterThan(observedFilledQty) {
		return delta, newSpotBalanceInvariant("base delta %s exceeds observed fill %s", delta, observedFilledQty)
	}
	if current.borrowed.GreaterThan(baseline.borrowed) {
		return delta, newSpotBalanceInvariant("borrowed balance increased from %s to %s", baseline.borrowed, current.borrowed)
	}
	return delta, nil
}

func spotResidualSellable(delta decimal.Decimal, guard *spotBalanceGuardConfig) bool {
	if !delta.IsPositive() {
		return false
	}
	qty := delta.Div(guard.sizeStep).Floor().Mul(guard.sizeStep)
	if qty.LessThan(guard.minQty) {
		return false
	}
	return !guard.minNotional.IsPositive() || qty.Mul(guard.closePrice).GreaterThanOrEqual(guard.minNotional)
}

func newSpotBalanceInvariant(format string, args ...any) error {
	return &spotBalanceInvariantError{err: fmt.Errorf(format, args...)}
}

func isSpotBalanceInvariant(err error) bool {
	var invariant *spotBalanceInvariantError
	return errors.As(err, &invariant)
}

func cancelLifecycleOpenOrders(ctx context.Context, exec contract.ExecutionClient, spec OrderLifecycleSpec, tracker *lifecycleOrderTracker) error {
	if tracker == nil || len(tracker.orders) == 0 {
		return nil
	}
	orders := make([]*trackedLifecycleOrder, 0, len(tracker.orders))
	for _, order := range tracker.orders {
		orders = append(orders, order)
	}
	return waitForTrackedOrdersSettled(ctx, exec, spec, tracker, orders, true)
}

func waitForTrackedOrdersSettled(ctx context.Context, exec contract.ExecutionClient, spec OrderLifecycleSpec, tracker *lifecycleOrderTracker, orders []*trackedLifecycleOrder, cancelOpen bool) error {
	if exec == nil {
		return fmt.Errorf("execution client is required for exact lifecycle order reconciliation")
	}
	absentObservations := make(map[string]int, len(orders))
	fillStableObservations := make(map[string]int, len(orders))
	lastFillQty := make(map[string]decimal.Decimal, len(orders))
	fillQtyObserved := make(map[string]bool, len(orders))
	ticker := time.NewTicker(spec.interval())
	defer ticker.Stop()
	var lastErr error
	for {
		pending := false
		for _, tracked := range orders {
			if tracked != nil && !tracked.reconciled {
				pending = true
				break
			}
		}
		if !pending {
			return nil
		}
		attemptedVenueIDs := make(map[string]string, len(orders))
		canceledVenueIDs := make(map[string]string, len(orders))
		callCtx, cancel := spec.pollCallContext(ctx)
		open, openErr := exec.OpenOrders(callCtx, spec.InstrumentID)
		cancel()
		if openErr != nil {
			lastErr = openErr
		}
		allSettled := true
		for _, tracked := range orders {
			if tracked == nil || tracked.reconciled {
				continue
			}
			cancelExact := func() {
				if !cancelOpen || tracked.venueOrderID == "" || attemptedVenueIDs[tracked.clientID] == tracked.venueOrderID || canceledVenueIDs[tracked.clientID] == tracked.venueOrderID || tracked.canceledVenueOrderID == tracked.venueOrderID {
					return
				}
				attemptedVenueIDs[tracked.clientID] = tracked.venueOrderID
				callCtx, callCancel := spec.pollCallContext(ctx)
				cancelErr := exec.Cancel(callCtx, spec.InstrumentID, tracked.venueOrderID)
				callCancel()
				if cancelErr != nil {
					lastErr = cancelErr
					return
				}
				canceledVenueIDs[tracked.clientID] = tracked.venueOrderID
				tracked.canceledVenueOrderID = tracked.venueOrderID
			}
			// Once an exact venue identity is known, attempt bounded cancellation
			// before parsing any potentially malformed status/fill evidence.
			// Evidence errors must never strand a known live order.
			cancelExact()
			var openMatch *model.Order
			if openErr == nil {
				for i := range open {
					candidate := &open[i]
					matches, err := trackedOpenOrderMatch(tracked, candidate)
					if err != nil {
						return err
					}
					if !matches {
						continue
					}
					if err := bindTrackedOrderIdentity(spec, "cleanup_open_order", tracked, candidate); err != nil {
						return err
					}
					openMatch = candidate
					if err := ensureTrackedOrderSemantics(spec, "cleanup_open_order", tracked, candidate); err != nil {
						// Exact identity is sufficient to cancel safely. Preserve the
						// caller's request semantics and continue cleanup even when the
						// venue echoed malformed semantic fields.
						lastErr = err
					} else {
						candidate.Request = tracked.request
						tracker.observe(tracked, candidate)
					}
					break
				}
			}
			// A previously acknowledged cancel is enough to avoid blind duplicate
			// requests. Exact open-order evidence proves the venue still considers
			// the order live, so permit one bounded retry in this reconciliation
			// poll. The per-poll maps suppress further retries until fresh evidence.
			if openMatch != nil && canceledVenueIDs[tracked.clientID] == "" {
				tracked.canceledVenueOrderID = ""
			}
			cancelExact()

			statusClientID, statusVenueOrderID := exactOrderQueryIDs(tracked.clientID, tracked.venueOrderID)
			callCtx, cancel = spec.pollCallContext(ctx)
			report, statusErr := exec.GenerateOrderStatusReport(callCtx, model.SingleOrderStatusQuery{
				AccountID:    spec.AccountID,
				InstrumentID: spec.InstrumentID,
				ClientID:     statusClientID,
				VenueOrderID: statusVenueOrderID,
			})
			cancel()
			if statusErr != nil {
				lastErr = statusErr
			} else if report != nil {
				if err := ensureOrderStatusReportAccount(spec, "cleanup_order_status", report); err != nil {
					return err
				}
				if err := bindTrackedOrderIdentity(spec, "cleanup_order_status_order", tracked, &report.Order); err != nil {
					return err
				}
				// A client-ID query can reveal the venue identity for the first time.
				// Bind and cancel that exact order before rejecting malformed status
				// quantities, otherwise cleanup would lose its only safe cancel key.
				cancelExact()
				if err := report.Validate(); err != nil {
					return fmt.Errorf("%s cleanup_order_status invalid status report: %w", spec.label(), err)
				}
				if err := validateTrackedOrderStatusFill(spec, "cleanup_order_status", tracked, report); err != nil {
					return err
				}
				if err := ensureTrackedOrderSemantics(spec, "cleanup_order_status_order", tracked, &report.Order); err != nil {
					lastErr = err
					tracked.status = report.Order.Status
					if report.Order.FilledQty.GreaterThan(tracked.filledQty) {
						tracked.filledQty = report.Order.FilledQty
					}
					if definitiveLifecycleTerminal(report.Order.Status) {
						tracked.terminal = true
						tracked.authoritativeTerminal = true
					}
				} else {
					report.Order.Request = tracked.request
					tracker.observe(tracked, &report.Order)
				}
			}

			fillClientID, fillVenueOrderID := exactOrderQueryIDs(tracked.clientID, tracked.venueOrderID)
			callCtx, cancel = spec.pollCallContext(ctx)
			fills, fillErr := exec.GenerateFillReports(callCtx, model.FillReportQuery{
				AccountID:    spec.AccountID,
				InstrumentID: spec.InstrumentID,
				ClientID:     fillClientID,
				VenueOrderID: fillVenueOrderID,
			})
			cancel()
			if fillErr != nil {
				lastErr = fillErr
				fillStableObservations[tracked.clientID] = 0
			} else {
				if _, err := sumExactLifecycleFills(spec, "cleanup_fill_report", tracked, fills); err != nil {
					return err
				}
				effectiveFillQty := tracked.filledQty
				if fillQtyObserved[tracked.clientID] && effectiveFillQty.Equal(lastFillQty[tracked.clientID]) {
					fillStableObservations[tracked.clientID]++
				} else {
					fillStableObservations[tracked.clientID] = 1
					fillQtyObserved[tracked.clientID] = true
					lastFillQty[tracked.clientID] = effectiveFillQty
				}
			}

			if tracked.terminal {
				if fillStableObservations[tracked.clientID] >= lifecycleEvidenceStableObservations {
					tracked.reconciled = true
					continue
				}
				allSettled = false
				continue
			}
			if openMatch != nil || (report != nil && !definitiveLifecycleTerminal(report.Order.Status)) {
				absentObservations[tracked.clientID] = 0
				allSettled = false
				cancelExact()
				continue
			}
			cancelExact()
			venueCancellationProven := !cancelOpen || tracked.venueOrderID == "" ||
				canceledVenueIDs[tracked.clientID] == tracked.venueOrderID ||
				tracked.canceledVenueOrderID == tracked.venueOrderID
			if openErr == nil && statusErr == nil && report == nil && venueCancellationProven && fillStableObservations[tracked.clientID] >= lifecycleEvidenceStableObservations {
				absentObservations[tracked.clientID]++
			} else {
				absentObservations[tracked.clientID] = 0
			}
			if absentObservations[tracked.clientID] >= lifecycleEvidenceStableObservations {
				tracked.status = enums.StatusCanceled
				tracked.terminal = true
				tracked.authoritativeTerminal = false
				tracked.reconciled = true
				continue
			}
			allSettled = false
		}
		if allSettled {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out reconciling exact lifecycle orders; lastErr=%v: %w", lastErr, ctx.Err())
		case <-ticker.C:
		}
	}
}

func validateOrderLifecycleSpec(spec OrderLifecycleSpec) error {
	if spec.InstrumentID.Symbol == "" {
		return fmt.Errorf("order lifecycle instrument id is required")
	}
	if strings.TrimSpace(spec.AccountID) == "" {
		return fmt.Errorf("order lifecycle account id is required")
	}
	for name, value := range map[string]decimal.Decimal{
		"quantity":     spec.Quantity,
		"restingPrice": spec.RestingPrice,
		"fillPrice":    spec.FillPrice,
		"closePrice":   spec.ClosePrice,
	} {
		if !value.IsPositive() {
			return fmt.Errorf("order lifecycle %s must be positive, got %s", name, value)
		}
	}
	if !spec.CloseQuantity.IsZero() && !spec.CloseQuantity.IsPositive() {
		return fmt.Errorf("order lifecycle closeQuantity must be positive when set, got %s", spec.CloseQuantity)
	}
	if !spec.CleanupPositionLimit.IsZero() && !spec.CleanupPositionLimit.IsPositive() {
		return fmt.Errorf("order lifecycle cleanupPositionLimit must be positive when set, got %s", spec.CleanupPositionLimit)
	}
	if guard := spec.spotBalanceGuard; guard != nil {
		if spec.InstrumentID.Kind != enums.KindSpot {
			return fmt.Errorf("order lifecycle Spot balance guard requires a Spot instrument")
		}
		if isNilSpotBalanceReporter(guard.reporter) {
			return fmt.Errorf("order lifecycle Spot balance guard account reporter is required")
		}
		if guard.baseCurrency == "" {
			return fmt.Errorf("order lifecycle Spot balance guard base currency is required")
		}
		if !guard.sizeStep.IsPositive() {
			return fmt.Errorf("order lifecycle Spot balance guard size step must be positive, got %s", guard.sizeStep)
		}
		if !guard.minQty.IsPositive() {
			return fmt.Errorf("order lifecycle Spot balance guard min quantity must be positive, got %s", guard.minQty)
		}
		if guard.minNotional.IsNegative() {
			return fmt.Errorf("order lifecycle Spot balance guard min notional must not be negative, got %s", guard.minNotional)
		}
		if guard.feeReserve.IsNegative() {
			return fmt.Errorf("order lifecycle Spot balance guard fee reserve must not be negative, got %s", guard.feeReserve)
		}
		if !spec.CloseAfterFill || !spec.CloseQuantity.IsPositive() {
			return fmt.Errorf("order lifecycle Spot balance guard requires an explicit close quantity")
		}
		if spec.CloseQuantity.GreaterThan(spec.Quantity) {
			return fmt.Errorf("order lifecycle Spot close quantity %s exceeds buy quantity %s", spec.CloseQuantity, spec.Quantity)
		}
		if !guard.closeQuantity.Equal(spec.CloseQuantity) || !guard.closePrice.Equal(spec.ClosePrice) {
			return fmt.Errorf("order lifecycle Spot close metadata changed after balance guard configuration")
		}
	}
	if spec.perpPositionReporter != nil {
		if spec.InstrumentID.Kind == enums.KindSpot {
			return fmt.Errorf("order lifecycle Perp position reporter requires a non-Spot instrument")
		}
		if isNilPerpPositionReporter(spec.perpPositionReporter) {
			return fmt.Errorf("order lifecycle Perp position reporter is required")
		}
	}
	return nil
}

func isNilSpotBalanceReporter(reporter accountStateSource) bool {
	if reporter == nil {
		return true
	}
	value := reflect.ValueOf(reporter)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func isNilPerpPositionReporter(reporter PerpPositionReporter) bool {
	if reporter == nil {
		return true
	}
	value := reflect.ValueOf(reporter)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func (s OrderLifecycleSpec) closeQuantity(filledQty decimal.Decimal) (decimal.Decimal, error) {
	if !filledQty.IsPositive() {
		return decimal.Zero, fmt.Errorf("observed fill quantity must be positive, got %s", filledQty)
	}
	if s.spotBalanceGuard == nil {
		if s.CloseQuantity.IsPositive() && s.CloseQuantity.LessThan(filledQty) {
			return s.CloseQuantity, nil
		}
		return filledQty, nil
	}
	guard := s.spotBalanceGuard
	qty := guard.closeQuantity
	if !filledQty.Equal(s.Quantity) {
		ratio := guard.closeQuantity.Div(s.Quantity)
		qty = filledQty.Mul(ratio).Div(guard.sizeStep).Floor().Mul(guard.sizeStep)
	}
	if !qty.IsPositive() || qty.LessThan(guard.minQty) {
		return decimal.Zero, fmt.Errorf("scaled Spot close quantity %s is below minimum quantity %s", qty, guard.minQty)
	}
	if guard.minNotional.IsPositive() && qty.Mul(guard.closePrice).LessThan(guard.minNotional) {
		return decimal.Zero, fmt.Errorf("scaled Spot close notional %s is below minimum notional %s", qty.Mul(guard.closePrice), guard.minNotional)
	}
	return qty, nil
}

func (s OrderLifecycleSpec) cleanupPositionLimit() decimal.Decimal {
	if s.CleanupPositionLimit.IsPositive() {
		return s.CleanupPositionLimit
	}
	return s.Quantity
}

func (s OrderLifecycleSpec) label() string {
	if s.Label != "" {
		return s.Label
	}
	return s.InstrumentID.String()
}

func (s OrderLifecycleSpec) logAcceptanceStart(path string) {
	venue := s.Venue
	if venue == "" {
		venue = s.InstrumentID.Venue
	}
	s.logf(
		"acceptance_start path=%s label=%q venue=%s environment=%s product=%s instrument=%s account_id=%s private_stream_topics=%s",
		path,
		s.label(),
		venue,
		s.Environment,
		s.Product,
		s.InstrumentID.String(),
		s.AccountID,
		strings.Join(s.PrivateStreamTopics, ","),
	)
}

func (s OrderLifecycleSpec) logOrder(event string, order *model.Order, filledQty decimal.Decimal) {
	if order == nil {
		return
	}
	s.logf(
		"%s label=%q client_id=%s venue_order_id=%s side=%s tif=%s qty=%s filled_qty=%s price=%s",
		event,
		s.label(),
		order.Request.ClientID,
		order.VenueOrderID,
		order.Request.Side,
		order.Request.TIF,
		order.Request.Quantity,
		filledQty,
		order.Request.Price,
	)
}

func (s OrderLifecycleSpec) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
}

func ensureOrderAccount(spec OrderLifecycleSpec, evidence string, order *model.Order) error {
	if order == nil {
		return fmt.Errorf("%s %s returned nil order", spec.label(), evidence)
	}
	if order.Request.AccountID != spec.AccountID {
		return fmt.Errorf("%s %s account_id=%q, want %q", spec.label(), evidence, order.Request.AccountID, spec.AccountID)
	}
	return nil
}

func ensureOrderStatusReportAccount(spec OrderLifecycleSpec, evidence string, report *model.OrderStatusReport) error {
	if report == nil {
		return fmt.Errorf("%s %s returned nil report", spec.label(), evidence)
	}
	if report.AccountID != "" && report.AccountID != spec.AccountID {
		return fmt.Errorf("%s %s report account_id=%q, want %q", spec.label(), evidence, report.AccountID, spec.AccountID)
	}
	return ensureOrderAccount(spec, evidence+"_order", &report.Order)
}

func ensureTrackedOrderStatusReport(spec OrderLifecycleSpec, evidence string, tracked *trackedLifecycleOrder, report *model.OrderStatusReport) error {
	if err := ensureOrderStatusReportAccount(spec, evidence, report); err != nil {
		return err
	}
	if err := bindTrackedOrderIdentity(spec, evidence+"_order", tracked, &report.Order); err != nil {
		return err
	}
	if err := report.Validate(); err != nil {
		return fmt.Errorf("%s %s invalid status report: %w", spec.label(), evidence, err)
	}
	if err := validateTrackedOrderStatusFill(spec, evidence, tracked, report); err != nil {
		return err
	}
	if err := ensureTrackedOrderSemantics(spec, evidence+"_order", tracked, &report.Order); err != nil {
		return err
	}
	report.Order.Request = tracked.request
	return nil
}

func validateTrackedOrderStatusFill(spec OrderLifecycleSpec, evidence string, tracked *trackedLifecycleOrder, report *model.OrderStatusReport) error {
	if tracked == nil || report == nil || !tracked.request.Quantity.IsPositive() {
		return nil
	}
	if report.Order.FilledQty.GreaterThan(tracked.request.Quantity) {
		return fmt.Errorf("%s %s filled quantity %s exceeds tracked order quantity %s", spec.label(), evidence, report.Order.FilledQty, tracked.request.Quantity)
	}
	return nil
}

func ensureTrackedOrder(spec OrderLifecycleSpec, evidence string, tracked *trackedLifecycleOrder, order *model.Order) error {
	if err := bindTrackedOrderIdentity(spec, evidence, tracked, order); err != nil {
		return err
	}
	if err := ensureTrackedOrderSemantics(spec, evidence, tracked, order); err != nil {
		return err
	}
	// Exact venue evidence proves status/fill state, but lifecycle request
	// semantics remain those of the caller. Some venue status schemas omit TIF,
	// reduce-only, and trigger fields, so replacing the request would silently
	// turn an IOC reduce-only close into a GTC opening order in runtime state.
	order.Request = tracked.request
	return nil
}

// bindTrackedOrderIdentity deliberately runs before semantic validation. A
// submit response with the exact account/instrument/client identity and a venue
// order ID may still contain malformed side/quantity/price fields. We must
// retain that exact venue identity so deferred cleanup can cancel the order,
// while never binding an order whose identity itself conflicts.
func bindTrackedOrderIdentity(spec OrderLifecycleSpec, evidence string, tracked *trackedLifecycleOrder, order *model.Order) error {
	if tracked == nil {
		return fmt.Errorf("%s %s missing lifecycle identity", spec.label(), evidence)
	}
	if err := ensureOrderAccount(spec, evidence, order); err != nil {
		return err
	}
	if order.Request.InstrumentID != spec.InstrumentID {
		return fmt.Errorf("%s %s instrument=%s, want %s", spec.label(), evidence, order.Request.InstrumentID, spec.InstrumentID)
	}
	if tracked.venueOrderID != "" {
		if order.VenueOrderID == "" || order.VenueOrderID != tracked.venueOrderID {
			return fmt.Errorf("%s %s order identity venue_order_id=%q, want %q", spec.label(), evidence, order.VenueOrderID, tracked.venueOrderID)
		}
		if order.Request.ClientID != "" && order.Request.ClientID != tracked.clientID {
			return fmt.Errorf("%s %s order identity client_id=%q, want %q", spec.label(), evidence, order.Request.ClientID, tracked.clientID)
		}
	} else if order.Request.ClientID == "" || order.Request.ClientID != tracked.clientID {
		return fmt.Errorf("%s %s order identity client_id=%q, want exact %q before venue id is known", spec.label(), evidence, order.Request.ClientID, tracked.clientID)
	}
	if tracked.venueOrderID == "" && order.VenueOrderID != "" {
		tracked.venueOrderID = order.VenueOrderID
	}
	return nil
}

func ensureTrackedOrderSemantics(spec OrderLifecycleSpec, evidence string, tracked *trackedLifecycleOrder, order *model.Order) error {
	if order.Request.Side != enums.SideUnknown && tracked.request.Side != enums.SideUnknown && order.Request.Side != tracked.request.Side {
		return fmt.Errorf("%s %s order side=%s, want %s", spec.label(), evidence, order.Request.Side, tracked.request.Side)
	}
	if order.Request.Quantity.IsPositive() && tracked.request.Quantity.IsPositive() && !order.Request.Quantity.Equal(tracked.request.Quantity) {
		return fmt.Errorf("%s %s order quantity=%s, want %s", spec.label(), evidence, order.Request.Quantity, tracked.request.Quantity)
	}
	if order.Request.Price.IsPositive() && tracked.request.Price.IsPositive() &&
		!trackedOrderPriceCompatible(spec, tracked.request.Side, order.Request.Price, tracked.request.Price) {
		return fmt.Errorf("%s %s order price=%s, want %s", spec.label(), evidence, order.Request.Price, tracked.request.Price)
	}
	return nil
}

func trackedOrderPriceCompatible(spec OrderLifecycleSpec, side enums.OrderSide, venuePrice, requestedPrice decimal.Decimal) bool {
	if venuePrice.Equal(requestedPrice) {
		return true
	}
	if !spec.AllowVenuePriceImprovement {
		return false
	}
	switch side {
	case enums.SideSell:
		return venuePrice.GreaterThan(requestedPrice)
	case enums.SideBuy:
		return venuePrice.LessThan(requestedPrice)
	default:
		return false
	}
}

func trackedOpenOrderMatch(tracked *trackedLifecycleOrder, order *model.Order) (bool, error) {
	if tracked == nil || order == nil {
		return false, nil
	}
	clientID := order.Request.ClientID
	venueOrderID := order.VenueOrderID
	if tracked.venueOrderID != "" {
		if venueOrderID == tracked.venueOrderID {
			if clientID != "" && clientID != tracked.clientID {
				return false, fmt.Errorf("open order identity conflict for venue_order_id=%q: client_id=%q, want %q", venueOrderID, clientID, tracked.clientID)
			}
			return true, nil
		}
		if clientID == tracked.clientID {
			return false, fmt.Errorf("open order identity conflict for client_id=%q: venue_order_id=%q, want %q", clientID, venueOrderID, tracked.venueOrderID)
		}
		return false, nil
	}
	if clientID == "" || clientID != tracked.clientID {
		return false, nil
	}
	return true, nil
}

func ensureFillReportAccount(spec OrderLifecycleSpec, evidence string, report model.FillReport) error {
	if report.AccountID != "" && report.AccountID != spec.AccountID {
		return fmt.Errorf("%s %s report account_id=%q, want %q", spec.label(), evidence, report.AccountID, spec.AccountID)
	}
	if report.Fill.AccountID != spec.AccountID {
		return fmt.Errorf("%s %s fill account_id=%q, want %q", spec.label(), evidence, report.Fill.AccountID, spec.AccountID)
	}
	return nil
}

func sumExactLifecycleFills(spec OrderLifecycleSpec, evidence string, tracked *trackedLifecycleOrder, reports []model.FillReport) (decimal.Decimal, error) {
	if tracked == nil {
		return decimal.Zero, fmt.Errorf("%s %s missing lifecycle identity", spec.label(), evidence)
	}
	expectedVenueOrderID := tracked.venueOrderID
	seen := make(map[string]model.Fill, len(reports))
	total := decimal.Zero
	for _, report := range reports {
		if err := ensureFillReportAccount(spec, evidence, report); err != nil {
			return decimal.Zero, err
		}
		if err := report.Validate(); err != nil {
			return decimal.Zero, fmt.Errorf("%s %s invalid fill: %w", spec.label(), evidence, err)
		}
		fill := report.Fill
		if fill.InstrumentID != spec.InstrumentID {
			return decimal.Zero, fmt.Errorf("%s %s fill instrument=%s, want %s", spec.label(), evidence, fill.InstrumentID, spec.InstrumentID)
		}
		if fill.Side != enums.SideUnknown && tracked.request.Side != enums.SideUnknown && fill.Side != tracked.request.Side {
			return decimal.Zero, fmt.Errorf("%s %s fill side=%s, want %s", spec.label(), evidence, fill.Side, tracked.request.Side)
		}
		if expectedVenueOrderID != "" {
			if fill.VenueOrderID == "" || fill.VenueOrderID != expectedVenueOrderID {
				return decimal.Zero, fmt.Errorf("%s %s fill identity venue_order_id=%q, want %q", spec.label(), evidence, fill.VenueOrderID, expectedVenueOrderID)
			}
			if fill.ClientID != "" && fill.ClientID != tracked.clientID {
				return decimal.Zero, fmt.Errorf("%s %s fill identity client_id=%q, want %q", spec.label(), evidence, fill.ClientID, tracked.clientID)
			}
		} else {
			if fill.ClientID == "" || fill.ClientID != tracked.clientID {
				return decimal.Zero, fmt.Errorf("%s %s fill identity client_id=%q, want exact %q before venue id is known", spec.label(), evidence, fill.ClientID, tracked.clientID)
			}
			if fill.VenueOrderID != "" {
				expectedVenueOrderID = fill.VenueOrderID
			}
		}
		key, err := lifecycleFillReportKey(report)
		if err != nil {
			return decimal.Zero, fmt.Errorf("%s %s: %w", spec.label(), evidence, err)
		}
		if previous, exists := seen[key]; exists {
			if !reflect.DeepEqual(previous, fill) {
				return decimal.Zero, fmt.Errorf("%s %s conflicting duplicate fill identity %q", spec.label(), evidence, key)
			}
			continue
		}
		seen[key] = fill
		total = total.Add(fill.Quantity)
	}
	if tracked.request.Quantity.IsPositive() && total.GreaterThan(tracked.request.Quantity) {
		return decimal.Zero, fmt.Errorf("%s %s aggregate fill quantity %s exceeds tracked order quantity %s", spec.label(), evidence, total, tracked.request.Quantity)
	}
	if expectedVenueOrderID != "" {
		tracked.venueOrderID = expectedVenueOrderID
	}
	if total.GreaterThan(tracked.filledQty) {
		tracked.filledQty = total
	}
	return total, nil
}

func lifecycleFillReportKey(report model.FillReport) (string, error) {
	fill := report.Fill
	if fill.TradeID != "" {
		return strings.Join([]string{"trade", fill.InstrumentID.String(), fill.VenueOrderID, fill.TradeID}, "\x00"), nil
	}
	if report.ReportID != "" {
		return "report:" + string(report.ReportID), nil
	}
	return "", fmt.Errorf("fill report requires stable ReportID or TradeID")
}

func ensurePositionReportAccount(spec OrderLifecycleSpec, evidence string, report model.PositionReport) error {
	if report.AccountID != "" && report.AccountID != spec.AccountID {
		return fmt.Errorf("%s %s report account_id=%q, want %q", spec.label(), evidence, report.AccountID, spec.AccountID)
	}
	if report.Position.AccountID != spec.AccountID {
		return fmt.Errorf("%s %s position account_id=%q, want %q", spec.label(), evidence, report.Position.AccountID, spec.AccountID)
	}
	return nil
}

func filledEventName(kind string) string {
	switch kind {
	case "fill":
		return "filled_order"
	case "close":
		return "closed_order"
	default:
		return kind + "_order"
	}
}

func orderLifecycleClientID(kind string) string {
	return "btac" + kind + strconv.FormatInt(time.Now().UnixNano(), 36)
}

func exactOrderQueryIDs(clientID, venueOrderID string) (string, string) {
	if venueOrderID != "" {
		return "", venueOrderID
	}
	return clientID, ""
}

func waitForNoOpenOrders(ctx context.Context, exec contract.ExecutionClient, spec OrderLifecycleSpec) error {
	interval := spec.interval()
	var lastLen int
	var lastErr error
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		callCtx, cancel := spec.pollCallContext(ctx)
		open, err := exec.OpenOrders(callCtx, spec.InstrumentID)
		cancel()
		if err == nil {
			lastLen = len(open)
			for _, order := range open {
				if err := ensureOrderAccount(spec, "open_order", &order); err != nil {
					return err
				}
			}
			if len(open) == 0 {
				return nil
			}
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for no open orders; lastLen=%d lastErr=%v: %w", lastLen, lastErr, ctx.Err())
		case <-ticker.C:
		}
	}
}

func waitForFilledQty(ctx context.Context, exec contract.ExecutionClient, spec OrderLifecycleSpec, tracker *lifecycleOrderTracker, tracked *trackedLifecycleOrder, order model.Order) (decimal.Decimal, error) {
	interval := spec.interval()
	var lastStatus enums.OrderStatus
	var lastErr error
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := ensureTrackedOrder(spec, "wait_order", tracked, &order); err != nil {
			return decimal.Zero, err
		}
		tracker.observe(tracked, &order)
		if definitiveLifecycleTerminal(order.Status) {
			actualFilledQty := order.FilledQty
			if tracked.filledQty.GreaterThan(actualFilledQty) {
				actualFilledQty = tracked.filledQty
			}
			if actualFilledQty.IsPositive() {
				return actualFilledQty, nil
			}
			return decimal.Zero, fmt.Errorf("order reached terminal status %s with zero fill", order.Status)
		}
		if order.Status != enums.StatusUnknown && order.FilledQty.GreaterThanOrEqual(order.Request.Quantity) {
			return order.FilledQty, nil
		}
		statusClientID, statusVenueOrderID := exactOrderQueryIDs(tracked.clientID, tracked.venueOrderID)
		callCtx, cancel := spec.pollCallContext(ctx)
		report, err := exec.GenerateOrderStatusReport(callCtx, model.SingleOrderStatusQuery{
			AccountID:    spec.AccountID,
			InstrumentID: spec.InstrumentID,
			ClientID:     statusClientID,
			VenueOrderID: statusVenueOrderID,
		})
		cancel()
		if err == nil && report != nil {
			if err := ensureTrackedOrderStatusReport(spec, "order_status_report", tracked, report); err != nil {
				return decimal.Zero, err
			}
			tracker.observe(tracked, &report.Order)
			lastStatus = report.Order.Status
			if definitiveLifecycleTerminal(report.Order.Status) {
				actualFilledQty := report.Order.FilledQty
				if tracked.filledQty.GreaterThan(actualFilledQty) {
					actualFilledQty = tracked.filledQty
				}
				if actualFilledQty.IsPositive() {
					return actualFilledQty, nil
				}
				return decimal.Zero, fmt.Errorf("order reached terminal status %s with zero fill", report.Order.Status)
			}
			if report.Order.Status != enums.StatusUnknown && report.Order.FilledQty.GreaterThanOrEqual(order.Request.Quantity) {
				return report.Order.FilledQty, nil
			}
		} else if err != nil {
			lastErr = err
		}
		fillClientID, fillVenueOrderID := exactOrderQueryIDs(tracked.clientID, tracked.venueOrderID)
		callCtx, cancel = spec.pollCallContext(ctx)
		fills, err := exec.GenerateFillReports(callCtx, model.FillReportQuery{
			AccountID:    spec.AccountID,
			InstrumentID: spec.InstrumentID,
			ClientID:     fillClientID,
			VenueOrderID: fillVenueOrderID,
		})
		cancel()
		if err == nil {
			total, sumErr := sumExactLifecycleFills(spec, "fill_report", tracked, fills)
			if sumErr != nil {
				return decimal.Zero, sumErr
			}
			if total.GreaterThanOrEqual(order.Request.Quantity) {
				return total, nil
			}
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return decimal.Zero, fmt.Errorf("timed out waiting for order %s/%s filled; lastStatus=%s lastErr=%v: %w", order.Request.ClientID, order.VenueOrderID, lastStatus, lastErr, ctx.Err())
		case <-ticker.C:
		}
	}
}

func waitForRuntimeFilledQty(ctx context.Context, node *btruntime.TradingNode, spec OrderLifecycleSpec, tracker *lifecycleOrderTracker, tracked *trackedLifecycleOrder, clientID string, expected decimal.Decimal) (model.Order, decimal.Decimal, error) {
	interval := spec.interval()
	var last model.Order
	var seen bool
	terminalZeroObservations := 0
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if order, ok := node.Cache.Order(clientID); ok {
			if err := ensureTrackedOrder(spec, "runtime_cached_order", tracked, &order); err != nil {
				return model.Order{}, decimal.Zero, err
			}
			last = order
			seen = true
			tracker.observe(tracked, &order)
			if definitiveLifecycleTerminal(order.Status) {
				actualFilledQty := order.FilledQty
				if tracked.filledQty.GreaterThan(actualFilledQty) {
					actualFilledQty = tracked.filledQty
					order.FilledQty = actualFilledQty
				}
				if actualFilledQty.IsPositive() {
					return order, actualFilledQty, nil
				}
				// Order and fill topics are independent streams. A venue may publish
				// terminal order state before the matching incremental FillEvent, so
				// require repeated stable zero-fill observations before failing.
				terminalZeroObservations++
				if terminalZeroObservations >= lifecycleEvidenceStableObservations {
					return model.Order{}, decimal.Zero, fmt.Errorf("runtime order reached terminal status %s with zero fill", order.Status)
				}
			} else {
				terminalZeroObservations = 0
			}
			if order.Status != enums.StatusUnknown && order.FilledQty.GreaterThanOrEqual(expected) {
				return order, order.FilledQty, nil
			}
		}
		select {
		case <-ctx.Done():
			if !seen {
				return model.Order{}, decimal.Zero, fmt.Errorf("runtime cache missing order %s: %w", clientID, ctx.Err())
			}
			return model.Order{}, decimal.Zero, fmt.Errorf("timed out waiting for runtime order %s filled quantity; lastStatus=%s lastFilledQty=%s: %w", clientID, last.Status, last.FilledQty, ctx.Err())
		case <-ticker.C:
		}
	}
}

func waitForFlatPosition(ctx context.Context, exec contract.ExecutionClient, spec OrderLifecycleSpec) error {
	interval := spec.interval()
	var lastGross decimal.Decimal
	var lastReports int
	var lastErr error
	stableFlat := 0
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		callCtx, cancel := spec.pollCallContext(ctx)
		reports, err := nonZeroPositionReports(callCtx, exec, spec)
		cancel()
		if err == nil {
			lastGross = decimal.Zero
			lastReports = len(reports)
			for _, report := range reports {
				lastGross = lastGross.Add(report.Position.Quantity.Abs())
			}
			if len(reports) == 0 {
				stableFlat++
				if stableFlat >= lifecycleEvidenceStableObservations {
					return nil
				}
			} else {
				stableFlat = 0
			}
		} else {
			lastErr = err
			stableFlat = 0
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for flat position; lastGross=%s lastReports=%d lastErr=%v: %w", lastGross, lastReports, lastErr, ctx.Err())
		case <-ticker.C:
		}
	}
}

func waitForStableCleanupPosition(ctx context.Context, exec contract.ExecutionClient, spec OrderLifecycleSpec) ([]model.PositionReport, error) {
	interval := spec.interval()
	stableFlat := 0
	var lastErr error
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		callCtx, cancel := spec.pollCallContext(ctx)
		reports, err := nonZeroPositionReports(callCtx, exec, spec)
		cancel()
		if err == nil {
			if len(reports) != 0 {
				return reports, nil
			}
			stableFlat++
			if stableFlat >= lifecycleEvidenceStableObservations {
				return nil, nil
			}
		} else {
			lastErr = err
			stableFlat = 0
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("timed out waiting for stable cleanup position evidence; lastErr=%v: %w", lastErr, ctx.Err())
		case <-ticker.C:
		}
	}
}

func waitForLifecycleLongPosition(ctx context.Context, exec contract.ExecutionClient, spec OrderLifecycleSpec, expected decimal.Decimal) error {
	interval := spec.interval()
	var lastQty decimal.Decimal
	var lastReports int
	var lastErr error
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		callCtx, cancel := spec.pollCallContext(ctx)
		reports, err := nonZeroPositionReports(callCtx, exec, spec)
		cancel()
		if err == nil {
			lastReports = len(reports)
			switch len(reports) {
			case 0:
				lastQty = decimal.Zero
			case 1:
				position := reports[0].Position
				lastQty = position.Quantity
				if position.Quantity.IsNegative() || position.Side == enums.PosShort {
					return fmt.Errorf("observed non-lifecycle short position: side=%s quantity=%s", position.Side, position.Quantity)
				}
				if position.Quantity.GreaterThan(expected) {
					return fmt.Errorf("account-backed position %s exceeds observed lifecycle fill %s", position.Quantity, expected)
				}
				if position.Quantity.Equal(expected) {
					return nil
				}
			default:
				return fmt.Errorf("ambiguous account-backed position evidence: %d non-zero reports", len(reports))
			}
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for lifecycle long position %s; lastQty=%s lastReports=%d lastErr=%v: %w", expected, lastQty, lastReports, lastErr, ctx.Err())
		case <-ticker.C:
		}
	}
}

func nonZeroPositionReports(ctx context.Context, exec contract.ExecutionClient, spec OrderLifecycleSpec) ([]model.PositionReport, error) {
	var reports []model.PositionReport
	if spec.perpPositionReporter != nil {
		positions, err := spec.perpPositionReporter.Positions(ctx)
		if err != nil {
			return nil, err
		}
		reports = make([]model.PositionReport, 0, len(positions))
		for _, position := range positions {
			reports = append(reports, model.PositionReport{AccountID: position.AccountID, Position: position})
		}
	} else {
		if exec == nil {
			return nil, fmt.Errorf("execution client is required for position reports")
		}
		var err error
		reports, err = exec.GeneratePositionReports(ctx, model.PositionReportQuery{AccountID: spec.AccountID, InstrumentID: spec.InstrumentID})
		if err != nil {
			return nil, err
		}
	}
	nonZero := make([]model.PositionReport, 0, len(reports))
	for _, report := range reports {
		if report.Position.InstrumentID != spec.InstrumentID {
			continue
		}
		if err := ensurePositionReportAccount(spec, "position_report", report); err != nil {
			return nil, err
		}
		if !report.Position.Quantity.IsZero() {
			nonZero = append(nonZero, report)
		}
	}
	return nonZero, nil
}

func (s OrderLifecycleSpec) interval() time.Duration {
	if s.PollInterval > 0 {
		return s.PollInterval
	}
	return 500 * time.Millisecond
}

func (s OrderLifecycleSpec) pollCallContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := s.PollRequestTimeout
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	return context.WithTimeout(ctx, timeout)
}

func (s OrderLifecycleSpec) cleanupTimeout() time.Duration {
	if s.CleanupTimeout > 0 {
		return s.CleanupTimeout
	}
	return 45 * time.Second
}
