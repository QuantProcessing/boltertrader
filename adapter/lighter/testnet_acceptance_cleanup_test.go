package lighter

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/shopspring/decimal"
)

type lighterRestingCleanupExecution interface {
	Cancel(context.Context, model.InstrumentID, string) error
	OpenOrders(context.Context, model.InstrumentID) ([]model.Order, error)
	GenerateOrderStatusReport(context.Context, model.SingleOrderStatusQuery) (*model.OrderStatusReport, error)
}

type lighterAcceptanceExactStatusExecution interface {
	lighterAcceptanceExactOrderStatus(context.Context, model.SingleOrderStatusQuery) (*model.OrderStatusReport, error)
}

type lighterAcceptanceExposureExecution interface {
	Submit(context.Context, model.OrderRequest) (*model.Order, error)
}

type lighterAcceptanceExposureAccount interface {
	AccountState(context.Context) (model.AccountState, error)
	Positions(context.Context) ([]model.Position, error)
}

type lighterAcceptanceExposureMarket interface {
	OrderBook(context.Context, model.InstrumentID, int) (*model.OrderBook, error)
}

const (
	lighterAcceptanceCleanupPollInterval     = 500 * time.Millisecond
	lighterAcceptanceSubmitVisibilityTimeout = 45 * time.Second
	lighterAcceptanceKnownOrderMinPolls      = 6
	lighterAcceptanceStableAbsentPolls       = 3
	lighterAcceptanceAmbiguousOrderMinPolls  = int(lighterAcceptanceSubmitVisibilityTimeout/lighterAcceptanceCleanupPollInterval) + 1
	lighterAcceptanceCleanupMaxPolls         = lighterAcceptanceAmbiguousOrderMinPolls + lighterAcceptanceStableAbsentPolls + 3
	lighterAcceptanceDeferredCleanupTimeout  = 2 * time.Minute
	lighterAcceptanceExposurePollInterval    = 500 * time.Millisecond
	lighterAcceptanceExposureStablePolls     = 10
)

// lighterRestingOrderCleanup owns exactly one acceptance order. Lighter can
// acknowledge PlaceOrder and then time out while waiting for the order to
// become visible, so cleanup must retain the pre-submit client id even when
// Submit returns no order and no venue id.
type lighterRestingOrderCleanup struct {
	exec         lighterRestingCleanupExecution
	instrumentID model.InstrumentID
	accountID    string
	clientID     string
	venueOrderID string
	expectedQty  decimal.Decimal

	pollInterval        time.Duration
	maxPolls            int
	minObservationPolls int
	ambiguousMinPolls   int
	stableAbsentPolls   int

	resolved bool
	fillErr  error

	unexpectedFill    bool
	confirmedFilled   decimal.Decimal
	exposureAttempted bool
	exposureErr       error
}

type lighterAcceptanceExposureBaseline struct {
	InstrumentID  model.InstrumentID
	Kind          enums.InstrumentKind
	BaseCurrency  string
	BaseTotal     decimal.Decimal
	BaseAvailable decimal.Decimal
}

type lighterAcceptanceExposureCleaner struct {
	exec    lighterAcceptanceExposureExecution
	account lighterAcceptanceExposureAccount
	market  lighterAcceptanceExposureMarket

	pollInterval time.Duration
}

var lighterAcceptanceClientSequence atomic.Uint64

func newLighterAcceptanceClientID(label string) string {
	label = strings.ToLower(strings.TrimSpace(label))
	label = strings.NewReplacer(" ", "-", "/", "-", "_", "-").Replace(label)
	label = strings.Trim(label, "-")
	if label == "" {
		label = "order"
	}
	return fmt.Sprintf("btac-lighter-%s-%d-%d", label, time.Now().UnixNano(), lighterAcceptanceClientSequence.Add(1))
}

func newLighterRestingOrderCleanup(
	exec lighterRestingCleanupExecution,
	instrumentID model.InstrumentID,
	accountID string,
	clientID string,
	expectedQty decimal.Decimal,
) *lighterRestingOrderCleanup {
	return &lighterRestingOrderCleanup{
		exec:                exec,
		instrumentID:        instrumentID,
		accountID:           accountID,
		clientID:            clientID,
		expectedQty:         expectedQty,
		pollInterval:        lighterAcceptanceCleanupPollInterval,
		maxPolls:            lighterAcceptanceCleanupMaxPolls,
		minObservationPolls: lighterAcceptanceKnownOrderMinPolls,
		ambiguousMinPolls:   lighterAcceptanceAmbiguousOrderMinPolls,
		stableAbsentPolls:   lighterAcceptanceStableAbsentPolls,
	}
}

func (c *lighterRestingOrderCleanup) ObserveSubmitResult(order *model.Order) error {
	if order == nil {
		return nil
	}
	matched, terminal, err := c.observe(*order, true)
	if !matched {
		return fmt.Errorf("Lighter acceptance submit returned an order that does not match client id %q", c.clientID)
	}
	if terminal {
		c.resolved = true
	}
	return err
}

func (c *lighterRestingOrderCleanup) NeedsCleanup() bool {
	return c != nil && !c.resolved
}

func (c *lighterRestingOrderCleanup) NeedsExposureCleanup() bool {
	return c != nil && c.unexpectedFill && !c.exposureAttempted
}

func (c *lighterRestingOrderCleanup) ConfirmedFilledQty() decimal.Decimal {
	if c == nil {
		return decimal.Zero
	}
	return c.confirmedFilled
}

func (c *lighterRestingOrderCleanup) exposureCleanupLimit() (decimal.Decimal, error) {
	if c == nil || !c.unexpectedFill {
		return decimal.Zero, nil
	}
	if !c.confirmedFilled.IsPositive() {
		return decimal.Zero, fmt.Errorf("Lighter unexpected fill has no positive authoritative filled quantity; automatic exposure cleanup is not authorized")
	}
	if !c.expectedQty.IsPositive() || c.confirmedFilled.GreaterThan(c.expectedQty) {
		return decimal.Zero, fmt.Errorf(
			"Lighter authoritative fill %s exceeds owned acceptance quantity %s; automatic exposure cleanup is not authorized",
			c.confirmedFilled,
			c.expectedQty,
		)
	}
	return c.confirmedFilled, nil
}

func (c *lighterRestingOrderCleanup) CancelConfirmAndRecover(
	ctx context.Context,
	cleaner *lighterAcceptanceExposureCleaner,
	inst *model.Instrument,
	baseline lighterAcceptanceExposureBaseline,
) error {
	orderErr := c.CancelAndConfirm(ctx)
	if c.NeedsCleanup() {
		return orderErr
	}
	if c.unexpectedFill && c.exposureAttempted {
		return errors.Join(orderErr, c.exposureErr)
	}
	if !c.NeedsExposureCleanup() {
		return orderErr
	}
	limit, authorizationErr := c.exposureCleanupLimit()
	if authorizationErr != nil {
		return errors.Join(orderErr, authorizationErr)
	}
	if cleaner == nil {
		return errors.Join(orderErr, errors.New("Lighter acceptance exposure cleaner is nil"))
	}
	c.exposureAttempted = true
	c.exposureErr = cleaner.Recover(ctx, inst, baseline, limit)
	return errors.Join(orderErr, c.exposureErr)
}

// CancelAndConfirm never treats a successful Cancel call as terminal evidence.
// It keeps querying the exact client/venue order and retries cancellation while
// that order remains open. Repeated absence from the authoritative open-order
// snapshot is accepted only after a bounded observation window, which also
// covers an order that is initially invisible after an ambiguous Submit.
func (c *lighterRestingOrderCleanup) CancelAndConfirm(ctx context.Context) error {
	if c == nil || c.resolved {
		if c == nil {
			return nil
		}
		return c.fillErr
	}
	if c.exec == nil {
		return errors.Join(c.fillErr, errors.New("Lighter acceptance cleanup execution client is nil"))
	}

	maxPolls := c.maxPolls
	if maxPolls <= 0 {
		maxPolls = 1
	}
	stableAbsentTarget := c.stableAbsentPolls
	if stableAbsentTarget <= 0 {
		stableAbsentTarget = 1
	}
	minObservationPolls := c.minObservationPolls
	if minObservationPolls <= 0 {
		minObservationPolls = 1
	}
	ambiguousMinPolls := c.ambiguousMinPolls
	if ambiguousMinPolls < minObservationPolls {
		ambiguousMinPolls = minObservationPolls
	}

	stableAbsent := 0
	var lastErr error
	for poll := 1; poll <= maxPolls; poll++ {
		observedOpen := false
		statusReadSucceeded := false
		openReadSucceeded := false

		report, err := c.exactOrderStatus(ctx, model.SingleOrderStatusQuery{
			InstrumentID: c.instrumentID,
			AccountID:    c.accountID,
			ClientID:     c.clientID,
			VenueOrderID: c.venueOrderID,
		})
		if err != nil {
			lastErr = fmt.Errorf("query exact Lighter acceptance order status: %w", err)
		} else {
			statusReadSucceeded = true
			if report != nil {
				matched, terminal, observeErr := c.observe(report.Order, false)
				if observeErr != nil {
					c.rememberFillError(observeErr)
				}
				if matched && terminal {
					c.resolved = true
					return c.fillErr
				}
				observedOpen = matched
			}
		}

		open, err := c.exec.OpenOrders(ctx, c.instrumentID)
		if err != nil {
			lastErr = fmt.Errorf("query Lighter acceptance open orders: %w", err)
		} else {
			openReadSucceeded = true
			for i := range open {
				matched, terminal, observeErr := c.observe(open[i], false)
				if observeErr != nil {
					c.rememberFillError(observeErr)
				}
				if !matched {
					continue
				}
				if terminal {
					c.resolved = true
					return c.fillErr
				}
				observedOpen = true
			}
		}

		if observedOpen {
			stableAbsent = 0
			if c.venueOrderID == "" {
				lastErr = fmt.Errorf("Lighter acceptance order %q is open without a venue order id", c.clientID)
			} else if err := c.exec.Cancel(ctx, c.instrumentID, c.venueOrderID); err != nil {
				lastErr = fmt.Errorf("cancel exact Lighter acceptance order %s: %w", c.venueOrderID, err)
			}
		} else if statusReadSucceeded && openReadSucceeded {
			stableAbsent++
			requiredObservationPolls := minObservationPolls
			if c.venueOrderID == "" {
				requiredObservationPolls = ambiguousMinPolls
			}
			if poll >= requiredObservationPolls && stableAbsent >= stableAbsentTarget {
				c.resolved = true
				return c.fillErr
			}
		} else {
			stableAbsent = 0
		}

		if poll == maxPolls {
			break
		}
		if err := waitLighterCleanupPoll(ctx, c.pollInterval); err != nil {
			return errors.Join(c.fillErr, err)
		}
	}

	return errors.Join(
		c.fillErr,
		lastErr,
		fmt.Errorf("Lighter acceptance order client_id=%q venue_order_id=%q did not reach terminal/no-open evidence after %d polls", c.clientID, c.venueOrderID, maxPolls),
	)
}

func (c *lighterRestingOrderCleanup) exactOrderStatus(ctx context.Context, query model.SingleOrderStatusQuery) (*model.OrderStatusReport, error) {
	if exact, ok := c.exec.(lighterAcceptanceExactStatusExecution); ok {
		return exact.lighterAcceptanceExactOrderStatus(ctx, query)
	}
	return c.exec.GenerateOrderStatusReport(ctx, query)
}

// lighterAcceptanceExactOrderStatus supplements Lighter's open-only contract
// report with the venue's inactive-order history. This is deliberately scoped
// to acceptance tests: a resting-only test must detect a canceled order with a
// late partial fill instead of treating disappearance from OpenOrders as clean.
func (c *executionClient) lighterAcceptanceExactOrderStatus(ctx context.Context, query model.SingleOrderStatusQuery) (*model.OrderStatusReport, error) {
	report, err := c.GenerateOrderStatusReport(ctx, query)
	if err != nil || report != nil {
		return report, err
	}
	inst, ok := c.provider.Instrument(query.InstrumentID)
	if !ok || inst.AssetIndex == nil {
		return nil, fmt.Errorf("Lighter acceptance exact status: unknown instrument %s", query.InstrumentID)
	}
	inactive, err := c.rest.GetInactiveOrders(ctx, inst.AssetIndex, 100)
	if err != nil {
		return nil, err
	}
	if inactive == nil {
		return nil, errors.New("Lighter acceptance exact status: empty inactive-order response")
	}
	targetClientIndex := clientOrderIndex(query.ClientID)
	for _, raw := range inactive.Orders {
		if raw == nil {
			continue
		}
		order := c.orderFromLighter(raw)
		venueMatches := query.VenueOrderID != "" && order.VenueOrderID == query.VenueOrderID
		clientMatches := query.ClientID != "" && raw.ClientOrderIndex == targetClientIndex
		if query.VenueOrderID != "" {
			if !venueMatches {
				continue
			}
		} else if !clientMatches {
			continue
		}
		if query.ClientID != "" {
			order.Request.ClientID = query.ClientID
		}
		order.Request.AccountID = c.accountID
		order.Request.PositionSide = enums.PosNet
		order = c.rememberOrder(order)
		return &model.OrderStatusReport{
			Venue:      venueName,
			AccountID:  c.accountID,
			Order:      order,
			ReportedAt: c.clk.Now(),
		}, nil
	}
	return nil, nil
}

func (c *lighterRestingOrderCleanup) observe(order model.Order, trustSubmitResult bool) (bool, bool, error) {
	clientMatches := order.Request.ClientID != "" && order.Request.ClientID == c.clientID
	venueMatches := c.venueOrderID != "" && order.VenueOrderID == c.venueOrderID
	if trustSubmitResult && order.Request.ClientID == "" {
		clientMatches = true
	}
	if !clientMatches && !venueMatches {
		return false, false, nil
	}
	if order.VenueOrderID != "" {
		if c.venueOrderID != "" && c.venueOrderID != order.VenueOrderID {
			return true, false, fmt.Errorf("Lighter acceptance order %q changed venue order id from %q to %q", c.clientID, c.venueOrderID, order.VenueOrderID)
		}
		c.venueOrderID = order.VenueOrderID
	}

	terminal := lighterAcceptanceOrderTerminal(order.Status)
	if order.FilledQty.IsPositive() || order.Status == enums.StatusPartiallyFilled || order.Status == enums.StatusFilled {
		c.unexpectedFill = true
		if order.FilledQty.GreaterThan(c.confirmedFilled) {
			c.confirmedFilled = order.FilledQty
		}
		err := fmt.Errorf(
			"Lighter resting-only acceptance order %q has unexpected fill qty=%s expected_qty=%s status=%s; bounded exposure cleanup is required",
			c.clientID,
			order.FilledQty,
			c.expectedQty,
			order.Status,
		)
		c.rememberFillError(err)
		return true, terminal, err
	}
	return true, terminal, nil
}

func newLighterAcceptanceExposureCleaner(
	exec lighterAcceptanceExposureExecution,
	account lighterAcceptanceExposureAccount,
	market lighterAcceptanceExposureMarket,
) *lighterAcceptanceExposureCleaner {
	return &lighterAcceptanceExposureCleaner{
		exec:         exec,
		account:      account,
		market:       market,
		pollInterval: lighterAcceptanceExposurePollInterval,
	}
}

func (c *lighterAcceptanceExposureCleaner) CaptureBaseline(ctx context.Context, inst *model.Instrument) (lighterAcceptanceExposureBaseline, error) {
	if c == nil || c.account == nil {
		return lighterAcceptanceExposureBaseline{}, errors.New("Lighter acceptance exposure account client is nil")
	}
	if inst == nil {
		return lighterAcceptanceExposureBaseline{}, errors.New("Lighter acceptance exposure instrument is nil")
	}
	baseline := lighterAcceptanceExposureBaseline{InstrumentID: inst.ID, Kind: inst.ID.Kind, BaseCurrency: inst.Base}
	switch inst.ID.Kind {
	case enums.KindPerp:
		positions, err := c.account.Positions(ctx)
		if err != nil {
			return lighterAcceptanceExposureBaseline{}, fmt.Errorf("capture Lighter Perp exposure baseline: %w", err)
		}
		qty, err := lighterAcceptancePositionQty(positions, inst.ID)
		if err != nil {
			return lighterAcceptanceExposureBaseline{}, err
		}
		if !qty.IsZero() {
			return lighterAcceptanceExposureBaseline{}, fmt.Errorf("Lighter Perp exposure baseline for %s is %s, want flat", inst.ID, qty)
		}
	case enums.KindSpot:
		if strings.TrimSpace(inst.Base) == "" {
			return lighterAcceptanceExposureBaseline{}, fmt.Errorf("Lighter Spot instrument %s has no base currency", inst.ID)
		}
		state, err := c.account.AccountState(ctx)
		if err != nil {
			return lighterAcceptanceExposureBaseline{}, fmt.Errorf("capture Lighter Spot balance baseline: %w", err)
		}
		balance := lighterAcceptanceBalance(state, inst.Base)
		baseline.BaseTotal = balance.Total
		baseline.BaseAvailable = balance.FreeOrAvailable()
	default:
		return lighterAcceptanceExposureBaseline{}, fmt.Errorf("unsupported Lighter acceptance exposure kind %s", inst.ID.Kind)
	}
	return baseline, nil
}

func (c *lighterAcceptanceExposureCleaner) Recover(
	ctx context.Context,
	inst *model.Instrument,
	baseline lighterAcceptanceExposureBaseline,
	confirmedFilled decimal.Decimal,
) error {
	if c == nil || c.exec == nil || c.account == nil || c.market == nil {
		return errors.New("Lighter acceptance exposure cleaner is incompletely configured")
	}
	if inst == nil || baseline.InstrumentID != inst.ID || baseline.Kind != inst.ID.Kind {
		return fmt.Errorf("Lighter acceptance exposure baseline does not match the cleanup instrument")
	}
	if !confirmedFilled.IsPositive() {
		return fmt.Errorf("Lighter automatic exposure cleanup requires a positive authoritative fill")
	}
	switch inst.ID.Kind {
	case enums.KindPerp:
		return c.recoverPerp(ctx, inst, confirmedFilled)
	case enums.KindSpot:
		return c.recoverSpot(ctx, inst, baseline, confirmedFilled)
	default:
		return fmt.Errorf("unsupported Lighter acceptance exposure kind %s", inst.ID.Kind)
	}
}

func (c *lighterAcceptanceExposureCleaner) recoverPerp(ctx context.Context, inst *model.Instrument, confirmedFilled decimal.Decimal) error {
	positions, err := c.account.Positions(ctx)
	if err != nil {
		return fmt.Errorf("read Lighter Perp exposure for cleanup: %w", err)
	}
	exposure, err := lighterAcceptancePositionQty(positions, inst.ID)
	if err != nil {
		return err
	}
	if exposure.IsZero() {
		return nil
	}
	if exposure.IsNegative() {
		return fmt.Errorf("refusing Lighter automatic Perp cleanup of unexpected short exposure %s after a buy fill", exposure)
	}
	if exposure.GreaterThan(confirmedFilled) {
		return fmt.Errorf("refusing Lighter automatic Perp cleanup: exposure %s exceeds authoritative own fill %s", exposure, confirmedFilled)
	}
	if inst.SizeStep.IsPositive() && !exposure.Mod(inst.SizeStep).IsZero() {
		return fmt.Errorf("refusing Lighter automatic Perp cleanup: exposure %s is not aligned to size step %s", exposure, inst.SizeStep)
	}
	price, err := c.closeSellPrice(ctx, inst)
	if err != nil {
		return err
	}
	_, err = c.exec.Submit(ctx, model.OrderRequest{
		AccountID:    model.AccountIDLighterDefault,
		InstrumentID: inst.ID,
		ClientID:     newLighterAcceptanceClientID("cleanup-perp"),
		Side:         enums.SideSell,
		Type:         enums.TypeLimit,
		TIF:          enums.TifIOC,
		Quantity:     exposure,
		Price:        price,
		PositionSide: enums.PosNet,
		ReduceOnly:   true,
	})
	if err != nil {
		verificationErr := c.waitForPerpFlat(ctx, inst.ID, confirmedFilled)
		return errors.Join(
			fmt.Errorf("submit single bounded Lighter Perp cleanup (ambiguous outcome; not retried): %w", err),
			verificationErr,
		)
	}
	return c.waitForPerpFlat(ctx, inst.ID, confirmedFilled)
}

func (c *lighterAcceptanceExposureCleaner) recoverSpot(
	ctx context.Context,
	inst *model.Instrument,
	baseline lighterAcceptanceExposureBaseline,
	confirmedFilled decimal.Decimal,
) error {
	closeQty, err := c.waitForOwnedSpotDelta(ctx, inst, baseline, confirmedFilled)
	if err != nil || closeQty.IsZero() {
		return err
	}
	price, err := c.closeSellPrice(ctx, inst)
	if err != nil {
		return err
	}
	if inst.MinQty.IsPositive() && closeQty.LessThan(inst.MinQty) {
		return fmt.Errorf("Lighter owned Spot delta %s is below minimum quantity %s", closeQty, inst.MinQty)
	}
	if inst.MinNotional.IsPositive() && closeQty.Mul(price).LessThan(inst.MinNotional) {
		return fmt.Errorf("Lighter owned Spot delta notional %s is below minimum %s", closeQty.Mul(price), inst.MinNotional)
	}
	_, err = c.exec.Submit(ctx, model.OrderRequest{
		AccountID:    model.AccountIDLighterDefault,
		InstrumentID: inst.ID,
		ClientID:     newLighterAcceptanceClientID("cleanup-spot"),
		Side:         enums.SideSell,
		Type:         enums.TypeLimit,
		TIF:          enums.TifIOC,
		Quantity:     closeQty,
		Price:        price,
		PositionSide: enums.PosNet,
	})
	if err != nil {
		verificationErr := c.waitForSpotBaseline(ctx, inst, baseline)
		return errors.Join(
			fmt.Errorf("submit single bounded Lighter Spot cleanup (ambiguous outcome; not retried): %w", err),
			verificationErr,
		)
	}
	return c.waitForSpotBaseline(ctx, inst, baseline)
}

func (c *lighterAcceptanceExposureCleaner) closeSellPrice(ctx context.Context, inst *model.Instrument) (decimal.Decimal, error) {
	book, err := c.market.OrderBook(ctx, inst.ID, 5)
	if err != nil {
		return decimal.Zero, fmt.Errorf("read Lighter cleanup order book: %w", err)
	}
	if book == nil || len(book.Bids) == 0 {
		return decimal.Zero, errors.New("Lighter cleanup order book has no bids")
	}
	price := floorLighterAcceptanceDecimal(book.Bids[0].Price.Mul(decimal.RequireFromString("0.99")), inst.PriceTick)
	if !price.IsPositive() {
		return decimal.Zero, fmt.Errorf("Lighter cleanup sell price is not positive")
	}
	return price, nil
}

func (c *lighterAcceptanceExposureCleaner) waitForOwnedSpotDelta(
	ctx context.Context,
	inst *model.Instrument,
	baseline lighterAcceptanceExposureBaseline,
	confirmedFilled decimal.Decimal,
) (decimal.Decimal, error) {
	for observation := 1; observation <= lighterAcceptanceExposureStablePolls; observation++ {
		state, err := c.account.AccountState(ctx)
		if err != nil {
			return decimal.Zero, fmt.Errorf("read Lighter Spot exposure for cleanup: %w", err)
		}
		current := lighterAcceptanceBalance(state, baseline.BaseCurrency)
		currentAvailable := current.FreeOrAvailable()
		if current.Total.LessThan(baseline.BaseTotal) {
			return decimal.Zero, fmt.Errorf("refusing Lighter Spot cleanup: current %s total %s is below pre-submit baseline %s", baseline.BaseCurrency, current.Total, baseline.BaseTotal)
		}
		totalDelta := current.Total.Sub(baseline.BaseTotal)
		availableDelta := currentAvailable.Sub(baseline.BaseAvailable)
		if totalDelta.IsZero() && !availableDelta.IsNegative() {
			return decimal.Zero, nil
		}
		if totalDelta.IsPositive() && availableDelta.IsPositive() {
			owned := minLighterAcceptanceDecimal(confirmedFilled, totalDelta, availableDelta)
			owned = floorLighterAcceptanceDecimal(owned, inst.SizeStep)
			if owned.IsPositive() {
				return owned, nil
			}
			if inst.SizeStep.IsPositive() && totalDelta.LessThan(inst.SizeStep) {
				return decimal.Zero, nil
			}
		}
		if observation == lighterAcceptanceExposureStablePolls {
			return decimal.Zero, fmt.Errorf(
				"Lighter Spot exposure ownership is not safely tradable: fill=%s total_delta=%s available_delta=%s step=%s",
				confirmedFilled,
				totalDelta,
				availableDelta,
				inst.SizeStep,
			)
		}
		if err := waitLighterCleanupPoll(ctx, c.pollInterval); err != nil {
			return decimal.Zero, err
		}
	}
	return decimal.Zero, nil
}

func (c *lighterAcceptanceExposureCleaner) waitForPerpFlat(ctx context.Context, id model.InstrumentID, confirmedFilled decimal.Decimal) error {
	for observation := 1; observation <= lighterAcceptanceExposureStablePolls; observation++ {
		positions, err := c.account.Positions(ctx)
		if err != nil {
			return fmt.Errorf("confirm Lighter Perp cleanup: %w", err)
		}
		exposure, err := lighterAcceptancePositionQty(positions, id)
		if err != nil {
			return err
		}
		if exposure.IsZero() {
			return nil
		}
		if exposure.IsNegative() || exposure.GreaterThan(confirmedFilled) {
			return fmt.Errorf("Lighter Perp cleanup produced unowned exposure %s (own fill limit %s)", exposure, confirmedFilled)
		}
		if observation == lighterAcceptanceExposureStablePolls {
			return fmt.Errorf("Lighter Perp cleanup left residual exposure %s after one bounded close", exposure)
		}
		if err := waitLighterCleanupPoll(ctx, c.pollInterval); err != nil {
			return err
		}
	}
	return nil
}

func (c *lighterAcceptanceExposureCleaner) waitForSpotBaseline(ctx context.Context, inst *model.Instrument, baseline lighterAcceptanceExposureBaseline) error {
	for observation := 1; observation <= lighterAcceptanceExposureStablePolls; observation++ {
		state, err := c.account.AccountState(ctx)
		if err != nil {
			return fmt.Errorf("confirm Lighter Spot cleanup: %w", err)
		}
		current := lighterAcceptanceBalance(state, baseline.BaseCurrency)
		currentAvailable := current.FreeOrAvailable()
		if current.Total.LessThan(baseline.BaseTotal) {
			return fmt.Errorf("Lighter Spot cleanup consumed pre-existing %s: total=%s baseline=%s", baseline.BaseCurrency, current.Total, baseline.BaseTotal)
		}
		residual := current.Total.Sub(baseline.BaseTotal)
		if !currentAvailable.LessThan(baseline.BaseAvailable) && (residual.IsZero() || inst.SizeStep.IsPositive() && residual.LessThan(inst.SizeStep)) {
			return nil
		}
		if observation == lighterAcceptanceExposureStablePolls {
			return fmt.Errorf(
				"Lighter Spot cleanup did not restore safe baseline: total=%s available=%s baseline_total=%s baseline_available=%s",
				current.Total,
				currentAvailable,
				baseline.BaseTotal,
				baseline.BaseAvailable,
			)
		}
		if err := waitLighterCleanupPoll(ctx, c.pollInterval); err != nil {
			return err
		}
	}
	return nil
}

func lighterAcceptancePositionQty(positions []model.Position, id model.InstrumentID) (decimal.Decimal, error) {
	qty := decimal.Zero
	found := false
	for _, position := range positions {
		if position.InstrumentID != id || position.Quantity.IsZero() {
			continue
		}
		if found {
			return decimal.Zero, fmt.Errorf("multiple Lighter position records found for %s; automatic cleanup is not authorized", id)
		}
		found = true
		qty = position.Quantity
	}
	return qty, nil
}

func lighterAcceptanceBalance(state model.AccountState, currency string) model.AccountBalance {
	for _, balance := range state.Balances {
		if strings.EqualFold(strings.TrimSpace(balance.Currency), strings.TrimSpace(currency)) {
			return balance
		}
	}
	return model.AccountBalance{AccountID: model.AccountIDLighterDefault, Currency: currency}
}

func minLighterAcceptanceDecimal(values ...decimal.Decimal) decimal.Decimal {
	if len(values) == 0 {
		return decimal.Zero
	}
	out := values[0]
	for _, value := range values[1:] {
		if value.LessThan(out) {
			out = value
		}
	}
	return out
}

func floorLighterAcceptanceDecimal(value, step decimal.Decimal) decimal.Decimal {
	if value.IsZero() || !step.IsPositive() {
		return value
	}
	return value.Div(step).Floor().Mul(step)
}

func (c *lighterRestingOrderCleanup) rememberFillError(err error) {
	if err != nil && c.fillErr == nil {
		c.fillErr = err
	}
}

func lighterAcceptanceOrderTerminal(status enums.OrderStatus) bool {
	switch status {
	case enums.StatusFilled, enums.StatusCanceled, enums.StatusRejected, enums.StatusExpired:
		return true
	default:
		return false
	}
}

func waitLighterCleanupPoll(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
