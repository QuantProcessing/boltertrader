package runtimeaccept

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	btruntime "github.com/QuantProcessing/boltertrader/runtime"
	"github.com/QuantProcessing/boltertrader/runtime/orderstate"
	"github.com/shopspring/decimal"
)

type OrderLifecycleSpec struct {
	Label                 string
	Venue                 string
	Environment           string
	Product               string
	AccountID             string
	InstrumentID          model.InstrumentID
	Quantity              decimal.Decimal
	CloseQuantity         decimal.Decimal
	RestingPrice          decimal.Decimal
	FillPrice             decimal.Decimal
	ClosePrice            decimal.Decimal
	PositionSide          enums.PositionSide
	CloseAfterFill        bool
	CleanExistingPosition bool
	PrivateStreamTopics   []string
	PollInterval          time.Duration
	PollRequestTimeout    time.Duration
	CleanupTimeout        time.Duration
	BeforeRuntimeClose    func(context.Context, decimal.Decimal) error
	Logf                  func(format string, args ...any)
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
	if spec.CleanExistingPosition && spec.InstrumentID.Kind != enums.KindSpot {
		if err := cleanExistingPosition(ctx, exec, spec); err != nil {
			return nil, fmt.Errorf("%s clean existing position: %w", spec.label(), err)
		}
	}
	if err := waitForNoOpenOrders(ctx, exec, spec); err != nil {
		return nil, fmt.Errorf("%s open-order preflight: %w", spec.label(), err)
	}

	resting, err := exec.Submit(ctx, model.OrderRequest{
		AccountID:    spec.AccountID,
		ClientID:     orderLifecycleClientID("rest"),
		InstrumentID: spec.InstrumentID,
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTX,
		Quantity:     spec.Quantity,
		Price:        spec.RestingPrice,
		PositionSide: spec.PositionSide,
	})
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
	restingNeedsCancel := true
	defer func() {
		if restingNeedsCancel && resting.VenueOrderID != "" {
			_ = exec.Cancel(context.Background(), spec.InstrumentID, resting.VenueOrderID)
		}
	}()
	if resting.Status == enums.StatusFilled || !resting.FilledQty.IsZero() {
		return nil, fmt.Errorf("%s resting order unexpectedly filled: %+v", spec.label(), *resting)
	}
	if err := exec.Cancel(ctx, spec.InstrumentID, resting.VenueOrderID); err != nil {
		return nil, fmt.Errorf("%s cancel resting order %s: %w", spec.label(), resting.VenueOrderID, err)
	}
	restingNeedsCancel = false
	if err := waitForNoOpenOrders(ctx, exec, spec); err != nil {
		return nil, fmt.Errorf("%s wait for resting order cancel: %w", spec.label(), err)
	}
	spec.logf("canceled_order label=%q client_id=%s venue_order_id=%s cleanup=no_open_orders", spec.label(), resting.Request.ClientID, resting.VenueOrderID)

	cleanupPerp := spec.InstrumentID.Kind != enums.KindSpot
	defer func() {
		if !cleanupPerp {
			return
		}
		cleanupErr := cleanupPerpOrderLifecycle(exec, spec)
		if cleanupErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("%s emergency Perp cleanup: %w", spec.label(), cleanupErr))
		}
	}()
	filled, filledQty, err := submitAndWaitFilled(ctx, exec, spec, "fill", enums.SideBuy, spec.FillPrice, false, spec.Quantity)
	if err != nil {
		return nil, err
	}
	result = &OrderLifecycleResult{Resting: *resting, Filled: *filled, FilledQty: filledQty}

	if spec.CloseAfterFill {
		closeQty := spec.closeQuantity(filledQty)
		closeOrder, closedQty, err := submitAndWaitFilled(
			ctx,
			exec,
			spec,
			"close",
			enums.SideSell,
			spec.ClosePrice,
			spec.InstrumentID.Kind != enums.KindSpot,
			closeQty,
		)
		if err != nil {
			return nil, err
		}
		result.Closed = *closeOrder
		result.ClosedQty = closedQty
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
	}
	return result, nil
}

func RunRuntimeOrderLifecycle(ctx context.Context, node *btruntime.TradingNode, venueExec contract.ExecutionClient, spec OrderLifecycleSpec) (result *OrderLifecycleResult, resultErr error) {
	if node == nil || node.Exec == nil {
		return nil, fmt.Errorf("%s runtime execution engine is required", spec.label())
	}
	if err := validateOrderLifecycleSpec(spec); err != nil {
		return nil, err
	}
	spec.logAcceptanceStart("runtime")
	if err := WaitForActive(ctx, node); err != nil {
		return nil, fmt.Errorf("%s runtime did not become active before lifecycle: %w", spec.label(), err)
	}
	if venueExec != nil {
		if err := waitForNoOpenOrders(ctx, venueExec, spec); err != nil {
			return nil, fmt.Errorf("%s open-order preflight: %w", spec.label(), err)
		}
	}

	resting, err := node.Exec.Submit(ctx, model.OrderRequest{
		AccountID:    spec.AccountID,
		ClientID:     orderLifecycleClientID("rest"),
		InstrumentID: spec.InstrumentID,
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTX,
		Quantity:     spec.Quantity,
		Price:        spec.RestingPrice,
		PositionSide: spec.PositionSide,
	})
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
	restingNeedsCancel := true
	defer func() {
		if restingNeedsCancel && venueExec != nil && resting.VenueOrderID != "" {
			_ = venueExec.Cancel(context.Background(), spec.InstrumentID, resting.VenueOrderID)
		}
	}()
	if resting.Status == enums.StatusFilled || !resting.FilledQty.IsZero() {
		return nil, fmt.Errorf("%s runtime resting order unexpectedly filled: %+v", spec.label(), *resting)
	}
	if err := node.Exec.Cancel(ctx, resting.Request.ClientID); err != nil {
		return nil, fmt.Errorf("%s runtime cancel resting order %s: %w", spec.label(), resting.VenueOrderID, err)
	}
	restingNeedsCancel = false
	if err := WaitForOrderStatus(ctx, node, resting.Request.ClientID, enums.StatusCanceled); err != nil {
		return nil, fmt.Errorf("%s runtime cache did not observe resting cancel: %w", spec.label(), err)
	}
	spec.logf("runtime_canceled_order label=%q client_id=%s venue_order_id=%s cleanup=runtime_cache_canceled", spec.label(), resting.Request.ClientID, resting.VenueOrderID)
	if venueExec != nil {
		if err := waitForNoOpenOrders(ctx, venueExec, spec); err != nil {
			return nil, fmt.Errorf("%s wait for no venue open orders after runtime cancel: %w", spec.label(), err)
		}
		spec.logf("cleanup label=%q cleanup=no_open_orders", spec.label())
	}

	cleanupPerp := spec.InstrumentID.Kind != enums.KindSpot && venueExec != nil
	defer func() {
		if !cleanupPerp {
			return
		}
		cleanupErr := cleanupPerpOrderLifecycle(venueExec, spec)
		if cleanupErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("%s emergency Perp cleanup: %w", spec.label(), cleanupErr))
		}
	}()
	filled, filledQty, err := submitRuntimeAndWaitFilled(ctx, node, spec, "fill", enums.SideBuy, spec.FillPrice, false, spec.Quantity)
	if err != nil {
		return nil, err
	}
	result = &OrderLifecycleResult{Resting: *resting, Filled: *filled, FilledQty: filledQty}

	if spec.CloseAfterFill {
		closeQty := spec.closeQuantity(filledQty)
		if spec.BeforeRuntimeClose != nil {
			if err := spec.BeforeRuntimeClose(ctx, closeQty); err != nil {
				return nil, fmt.Errorf("%s runtime close readiness: %w", spec.label(), err)
			}
		}
		closeOrder, closedQty, err := submitRuntimeAndWaitFilled(
			ctx,
			node,
			spec,
			"close",
			enums.SideSell,
			spec.ClosePrice,
			spec.InstrumentID.Kind != enums.KindSpot,
			closeQty,
		)
		if err != nil {
			return nil, err
		}
		result.Closed = *closeOrder
		result.ClosedQty = closedQty
		if spec.InstrumentID.Kind != enums.KindSpot {
			if venueExec == nil {
				return nil, fmt.Errorf("%s venue execution client is required to prove flat Perp exposure", spec.label())
			}
			if err := waitForFlatPosition(ctx, venueExec, spec); err != nil {
				return nil, fmt.Errorf("%s wait for venue flat position: %w", spec.label(), err)
			}
			if err := WaitForPortfolioFlat(ctx, node, spec.InstrumentID, decimal.Zero); err != nil {
				return nil, fmt.Errorf("%s wait for runtime portfolio flat: %w", spec.label(), err)
			}
			cleanupPerp = false
		}
	}
	if venueExec != nil {
		if err := waitForNoOpenOrders(ctx, venueExec, spec); err != nil {
			return nil, fmt.Errorf("%s wait for no venue open orders after runtime lifecycle: %w", spec.label(), err)
		}
		spec.logf("cleanup label=%q cleanup=no_open_orders", spec.label())
	}
	if open := node.Cache.OpenOrders(); len(open) != 0 {
		return nil, fmt.Errorf("%s runtime cache has %d open orders after lifecycle: %+v", spec.label(), len(open), open)
	}
	spec.logf("cleanup label=%q cleanup=runtime_cache_no_open_orders", spec.label())
	return result, nil
}

func submitRuntimeAndWaitFilled(ctx context.Context, node *btruntime.TradingNode, spec OrderLifecycleSpec, idKind string, side enums.OrderSide, price decimal.Decimal, reduceOnly bool, qty decimal.Decimal) (*model.Order, decimal.Decimal, error) {
	order, err := node.Exec.Submit(ctx, model.OrderRequest{
		AccountID:    spec.AccountID,
		ClientID:     orderLifecycleClientID(idKind),
		InstrumentID: spec.InstrumentID,
		Side:         side,
		Type:         enums.TypeLimit,
		TIF:          enums.TifIOC,
		Quantity:     qty,
		Price:        price,
		PositionSide: spec.PositionSide,
		ReduceOnly:   reduceOnly,
	})
	if err != nil {
		return nil, decimal.Zero, fmt.Errorf("%s runtime submit %s order: %w", spec.label(), idKind, err)
	}
	if order == nil {
		return nil, decimal.Zero, fmt.Errorf("%s runtime submit %s order returned nil", spec.label(), idKind)
	}
	if err := ensureOrderAccount(spec, "runtime_"+idKind+"_order", order); err != nil {
		return nil, decimal.Zero, err
	}
	cached, filledQty, err := waitForRuntimeFilledQty(ctx, node, spec, order.Request.ClientID, qty)
	if err != nil {
		return nil, decimal.Zero, fmt.Errorf("%s runtime wait for %s fill: %w", spec.label(), idKind, err)
	}
	if err := ensureOrderAccount(spec, "runtime_cached_"+idKind+"_order", &cached); err != nil {
		return nil, decimal.Zero, err
	}
	spec.logOrder("runtime_"+filledEventName(idKind), &cached, filledQty)
	return &cached, filledQty, nil
}

func submitAndWaitFilled(ctx context.Context, exec contract.ExecutionClient, spec OrderLifecycleSpec, idKind string, side enums.OrderSide, price decimal.Decimal, reduceOnly bool, qty decimal.Decimal) (*model.Order, decimal.Decimal, error) {
	order, err := exec.Submit(ctx, model.OrderRequest{
		AccountID:    spec.AccountID,
		ClientID:     orderLifecycleClientID(idKind),
		InstrumentID: spec.InstrumentID,
		Side:         side,
		Type:         enums.TypeLimit,
		TIF:          enums.TifIOC,
		Quantity:     qty,
		Price:        price,
		PositionSide: spec.PositionSide,
		ReduceOnly:   reduceOnly,
	})
	if err != nil {
		return nil, decimal.Zero, fmt.Errorf("%s submit %s order: %w", spec.label(), idKind, err)
	}
	if order == nil {
		return nil, decimal.Zero, fmt.Errorf("%s submit %s order returned nil", spec.label(), idKind)
	}
	if err := ensureOrderAccount(spec, idKind+"_order", order); err != nil {
		return nil, decimal.Zero, err
	}
	filledQty, err := waitForFilledQty(ctx, exec, spec, *order)
	if err != nil {
		return nil, decimal.Zero, fmt.Errorf("%s wait for %s fill: %w", spec.label(), idKind, err)
	}
	if filledQty.IsZero() {
		return nil, decimal.Zero, fmt.Errorf("%s %s order reported zero filled quantity: %+v", spec.label(), idKind, *order)
	}
	spec.logOrder(filledEventName(idKind), order, filledQty)
	return order, filledQty, nil
}

func cleanExistingPosition(ctx context.Context, exec contract.ExecutionClient, spec OrderLifecycleSpec) error {
	reports, err := exec.GeneratePositionReports(ctx, model.PositionReportQuery{AccountID: spec.AccountID, InstrumentID: spec.InstrumentID})
	if err != nil {
		return err
	}
	total := decimal.Zero
	for _, report := range reports {
		if report.Position.InstrumentID != spec.InstrumentID {
			continue
		}
		total = total.Add(report.Position.Quantity)
	}
	if total.IsZero() {
		return nil
	}
	side := enums.SideSell
	qty := total
	if total.IsNegative() {
		side = enums.SideBuy
		qty = total.Abs()
	}
	order, _, err := submitAndWaitFilled(ctx, exec, spec, "preclean", side, spec.ClosePrice, true, qty)
	if err != nil {
		return err
	}
	spec.logOrder("preclean_order", order, qty)
	if err := waitForNoOpenOrders(ctx, exec, spec); err != nil {
		return err
	}
	if err := waitForFlatPosition(ctx, exec, spec); err != nil {
		return err
	}
	spec.logf("cleanup label=%q cleanup=preclean_flat_position", spec.label())
	return nil
}

func cleanupPerpOrderLifecycle(exec contract.ExecutionClient, spec OrderLifecycleSpec) error {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), spec.cleanupTimeout())
	defer cancel()
	cancelErr := exec.CancelAll(cleanupCtx, spec.InstrumentID)
	flattenErr := cleanExistingPosition(cleanupCtx, exec, spec)
	return errors.Join(cancelErr, flattenErr)
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
	return nil
}

func (s OrderLifecycleSpec) closeQuantity(filledQty decimal.Decimal) decimal.Decimal {
	if s.CloseQuantity.IsPositive() {
		return s.CloseQuantity
	}
	return filledQty
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

func ensureFillReportAccount(spec OrderLifecycleSpec, evidence string, report model.FillReport) error {
	if report.AccountID != "" && report.AccountID != spec.AccountID {
		return fmt.Errorf("%s %s report account_id=%q, want %q", spec.label(), evidence, report.AccountID, spec.AccountID)
	}
	if report.Fill.AccountID != spec.AccountID {
		return fmt.Errorf("%s %s fill account_id=%q, want %q", spec.label(), evidence, report.Fill.AccountID, spec.AccountID)
	}
	return nil
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

func waitForFilledQty(ctx context.Context, exec contract.ExecutionClient, spec OrderLifecycleSpec, order model.Order) (decimal.Decimal, error) {
	interval := spec.interval()
	clientID := order.Request.ClientID
	if order.VenueOrderID != "" {
		clientID = ""
	}
	var lastStatus enums.OrderStatus
	var lastErr error
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if order.FilledQty.GreaterThanOrEqual(order.Request.Quantity) {
			return order.FilledQty, nil
		}
		if orderstate.IsTerminal(order.Status) && order.Status != enums.StatusFilled {
			return decimal.Zero, fmt.Errorf("order reached terminal status %s with partial fill %s/%s", order.Status, order.FilledQty, order.Request.Quantity)
		}
		callCtx, cancel := spec.pollCallContext(ctx)
		report, err := exec.GenerateOrderStatusReport(callCtx, model.SingleOrderStatusQuery{
			AccountID:    spec.AccountID,
			InstrumentID: spec.InstrumentID,
			ClientID:     clientID,
			VenueOrderID: order.VenueOrderID,
		})
		cancel()
		if err == nil && report != nil {
			if err := ensureOrderStatusReportAccount(spec, "order_status_report", report); err != nil {
				return decimal.Zero, err
			}
			lastStatus = report.Order.Status
			if report.Order.FilledQty.GreaterThanOrEqual(order.Request.Quantity) {
				return report.Order.FilledQty, nil
			}
			if orderstate.IsTerminal(report.Order.Status) && report.Order.Status != enums.StatusFilled {
				return decimal.Zero, fmt.Errorf("order reached terminal status %s with partial fill %s/%s", report.Order.Status, report.Order.FilledQty, order.Request.Quantity)
			}
		} else if err != nil {
			lastErr = err
		}
		callCtx, cancel = spec.pollCallContext(ctx)
		fills, err := exec.GenerateFillReports(callCtx, model.FillReportQuery{
			AccountID:    spec.AccountID,
			InstrumentID: spec.InstrumentID,
			ClientID:     clientID,
			VenueOrderID: order.VenueOrderID,
		})
		cancel()
		if err == nil {
			total := decimal.Zero
			for _, report := range fills {
				if err := ensureFillReportAccount(spec, "fill_report", report); err != nil {
					return decimal.Zero, err
				}
				total = total.Add(report.Fill.Quantity)
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

func waitForRuntimeFilledQty(ctx context.Context, node *btruntime.TradingNode, spec OrderLifecycleSpec, clientID string, expected decimal.Decimal) (model.Order, decimal.Decimal, error) {
	interval := spec.interval()
	var last model.Order
	var seen bool
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if order, ok := node.Cache.Order(clientID); ok {
			last = order
			seen = true
			if order.FilledQty.GreaterThanOrEqual(expected) {
				return order, order.FilledQty, nil
			}
			if orderstate.IsTerminal(order.Status) && order.Status != enums.StatusFilled {
				return model.Order{}, decimal.Zero, fmt.Errorf("runtime order reached terminal status %s with partial fill %s/%s", order.Status, order.FilledQty, expected)
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
	var last decimal.Decimal
	var lastErr error
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		callCtx, cancel := spec.pollCallContext(ctx)
		reports, err := exec.GeneratePositionReports(callCtx, model.PositionReportQuery{AccountID: spec.AccountID, InstrumentID: spec.InstrumentID})
		cancel()
		if err == nil {
			last = decimal.Zero
			for _, report := range reports {
				if err := ensurePositionReportAccount(spec, "position_report", report); err != nil {
					return err
				}
				last = last.Add(report.Position.Quantity)
			}
			if last.IsZero() {
				return nil
			}
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for flat position; last=%s lastErr=%v: %w", last, lastErr, ctx.Err())
		case <-ticker.C:
		}
	}
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
