package bitget

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/errs"
	"github.com/QuantProcessing/boltertrader/internal/wsstream"
	bitgetsdk "github.com/QuantProcessing/boltertrader/sdk/bitget"
	"github.com/shopspring/decimal"
)

const (
	executionMassStatusFillLimit = 1000
	bitgetFillWindow             = 30 * 24 * time.Hour
	bitgetFillHistory            = 90 * 24 * time.Hour
)

type executionClient struct {
	rest       *bitgetsdk.Client
	provider   *instrumentProvider
	clk        clock.Clock
	accountID  string
	categories []string
	stream     *wsstream.Stream[contract.ExecEnvelope]
}

func newExecutionClient(rest *bitgetsdk.Client, provider *instrumentProvider, clk clock.Clock, accountIDs ...string) *executionClient {
	if clk == nil {
		clk = clock.NewRealClock()
	}
	accountID := ""
	if len(accountIDs) > 0 {
		accountID = accountIDs[0]
	}
	if accountID == "" {
		accountID = AccountIDUnified
	}
	return &executionClient{
		rest:       rest,
		provider:   provider,
		clk:        clk,
		accountID:  accountID,
		categories: bitgetCategoriesFromProvider(provider),
		stream:     wsstream.New[contract.ExecEnvelope](256),
	}
}

func (c *executionClient) AccountID() string { return c.accountID }

func (c *executionClient) instrument(id model.InstrumentID) (*model.Instrument, error) {
	inst, ok := c.provider.Instrument(id)
	if !ok {
		return nil, fmt.Errorf("bitget: unknown instrument %s: %w", id, errs.ErrSymbolNotFound)
	}
	return inst, nil
}

func (c *executionClient) ValidateSubmit(req model.OrderRequest) error {
	inst, err := c.instrument(req.InstrumentID)
	if err != nil {
		return err
	}
	_, err = orderRequestToBitget(req, inst)
	return err
}

func (c *executionClient) Submit(ctx context.Context, req model.OrderRequest) (*model.Order, error) {
	inst, err := c.instrument(req.InstrumentID)
	if err != nil {
		return nil, err
	}
	if req.AccountID == "" {
		req.AccountID = c.accountID
	}
	venueReq, err := orderRequestToBitget(req, inst)
	if err != nil {
		return nil, err
	}
	resp, err := c.rest.PlaceOrder(ctx, &venueReq)
	if err != nil {
		return nil, bitgetCommandError("submit order", err)
	}
	order := orderFromBitgetAction(resp, req, c.clk.Now())
	return &order, nil
}

func (c *executionClient) Cancel(ctx context.Context, id model.InstrumentID, venueOrderID string) error {
	inst, err := c.instrument(id)
	if err != nil {
		return err
	}
	category, err := categoryForInstrument(inst)
	if err != nil {
		return err
	}
	_, err = c.rest.CancelOrder(ctx, &bitgetsdk.CancelOrderRequest{Category: category, Symbol: inst.VenueSymbol, OrderID: venueOrderID})
	return bitgetCommandError("cancel order", err)
}

func (c *executionClient) CancelAll(ctx context.Context, id model.InstrumentID) error {
	inst, err := c.instrument(id)
	if err != nil {
		return err
	}
	category, err := categoryForInstrument(inst)
	if err != nil {
		return err
	}
	return bitgetCommandError("cancel all orders", c.rest.CancelAllOrders(ctx, &bitgetsdk.CancelAllOrdersRequest{Category: category, Symbol: inst.VenueSymbol}))
}

func (c *executionClient) Modify(ctx context.Context, id model.InstrumentID, venueOrderID string, newPrice, newQty decimal.Decimal) (*model.Order, error) {
	inst, err := c.instrument(id)
	if err != nil {
		return nil, err
	}
	category, err := categoryForInstrument(inst)
	if err != nil {
		return nil, err
	}
	resp, err := c.rest.ModifyOrder(ctx, &bitgetsdk.ModifyOrderRequest{
		Category: category,
		Symbol:   inst.VenueSymbol,
		OrderID:  venueOrderID,
		NewQty:   decimalStringOrEmpty(newQty),
		NewPrice: decimalStringOrEmpty(newPrice),
	})
	if err != nil {
		return nil, bitgetCommandError("modify order", err)
	}
	req := model.OrderRequest{AccountID: c.accountID, InstrumentID: id, Quantity: newQty, Price: newPrice}
	order := model.Order{Request: req, VenueOrderID: resp.OrderID, Status: enums.StatusNew, UpdatedAt: c.clk.Now()}
	return &order, nil
}

func bitgetCommandError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if !bitgetsdk.IsDefinitiveCommandRejection(err) {
		return err
	}
	return fmt.Errorf("bitget: %s rejected: %w", operation, errors.Join(contract.ErrVenueRejected, err))
}

func (c *executionClient) OpenOrders(ctx context.Context, id model.InstrumentID) ([]model.Order, error) {
	inst, err := c.instrument(id)
	if err != nil {
		return nil, err
	}
	category, err := categoryForInstrument(inst)
	if err != nil {
		return nil, err
	}
	records, err := c.rest.GetOpenOrders(ctx, category, inst.VenueSymbol)
	if err != nil {
		return nil, err
	}
	out := make([]model.Order, 0, len(records))
	for _, record := range records {
		if !bitgetRecordCategoryMatches(record.Category, category) {
			return nil, fmt.Errorf("bitget: open-order category mismatch requested=%s received=%s symbol=%s", category, record.Category, record.Symbol)
		}
		if normalizeVenueSymbol(record.Symbol) != normalizeVenueSymbol(inst.VenueSymbol) {
			return nil, fmt.Errorf("bitget: open-order symbol mismatch requested=%s received=%s category=%s", inst.VenueSymbol, record.Symbol, category)
		}
		order, err := orderFromBitgetRecord(record, id, c.accountID)
		if err != nil {
			return nil, fmt.Errorf("bitget: invalid open-order position semantics symbol=%s: %w", record.Symbol, err)
		}
		out = append(out, order)
	}
	return out, nil
}

func (c *executionClient) GenerateOrderStatusReports(ctx context.Context, query model.OrderStatusReportQuery) ([]model.OrderStatusReport, error) {
	if query.AccountID != "" && query.AccountID != c.accountID {
		return nil, nil
	}
	targets, err := c.reportTargets(query.InstrumentID)
	if err != nil {
		return nil, err
	}
	now := c.clk.Now()
	var out []model.OrderStatusReport
	for _, target := range targets {
		reports, err := c.generateOrderStatusReportsForTarget(ctx, target, query, now)
		if err != nil {
			return nil, err
		}
		out = append(out, reports...)
	}
	if query.Limit > 0 && len(out) > query.Limit {
		out = out[:query.Limit]
	}
	return out, nil
}

func (c *executionClient) generateOrderStatusReportsForTarget(ctx context.Context, target reportTarget, query model.OrderStatusReportQuery, now time.Time) ([]model.OrderStatusReport, error) {
	records, err := c.rest.GetOpenOrders(ctx, target.category, target.symbol)
	if err != nil {
		return nil, err
	}
	out := make([]model.OrderStatusReport, 0, len(records))
	for _, record := range records {
		if !bitgetRecordCategoryMatches(record.Category, target.category) {
			return nil, fmt.Errorf("bitget: order category mismatch requested=%s received=%s symbol=%s", target.category, record.Category, record.Symbol)
		}
		id, ok := c.resolveReportInstrument(target.category, query.InstrumentID, record.Symbol)
		if !ok {
			return nil, fmt.Errorf("bitget: unknown order-report instrument category=%s symbol=%s", target.category, record.Symbol)
		}
		order, err := orderFromBitgetRecord(record, id, c.accountID)
		if err != nil {
			return nil, fmt.Errorf("bitget: invalid order-report position semantics symbol=%s: %w", record.Symbol, err)
		}
		if !model.OrderMatchesStatusQuery(order, query) {
			continue
		}
		out = append(out, model.OrderStatusReport{Venue: VenueName, AccountID: c.accountID, Order: order, ReportedAt: now})
	}
	return out, nil
}

func (c *executionClient) GenerateOrderStatusReport(ctx context.Context, query model.SingleOrderStatusQuery) (*model.OrderStatusReport, error) {
	if query.AccountID != "" && query.AccountID != c.accountID {
		return nil, nil
	}
	if (query.ClientID != "" || query.VenueOrderID != "") && query.InstrumentID.Symbol != "" {
		targets, err := c.reportTargets(query.InstrumentID)
		if err != nil {
			return nil, err
		}
		target := targets[0]
		record, err := c.rest.GetOrder(ctx, target.category, target.symbol, query.VenueOrderID, query.ClientID)
		if err != nil {
			return nil, err
		}
		if !bitgetRecordCategoryMatches(record.Category, target.category) {
			return nil, fmt.Errorf("bitget: order category mismatch requested=%s received=%s symbol=%s", target.category, record.Category, record.Symbol)
		}
		id, ok := c.resolveReportInstrument(target.category, query.InstrumentID, record.Symbol)
		if !ok {
			return nil, fmt.Errorf("bitget: unknown order-report instrument category=%s symbol=%s", target.category, record.Symbol)
		}
		order, err := orderFromBitgetRecord(*record, id, c.accountID)
		if err != nil {
			return nil, fmt.Errorf("bitget: invalid order-report position semantics symbol=%s: %w", record.Symbol, err)
		}
		if !model.OrderMatchesStatusQuery(order, model.OrderStatusReportQuery{
			InstrumentID: query.InstrumentID,
			AccountID:    query.AccountID,
			ClientID:     query.ClientID,
			VenueOrderID: query.VenueOrderID,
		}) {
			return nil, nil
		}
		report := model.OrderStatusReport{Venue: VenueName, AccountID: c.accountID, Order: order, ReportedAt: c.clk.Now()}
		return &report, nil
	}
	reports, err := c.GenerateOrderStatusReports(ctx, model.OrderStatusReportQuery{
		InstrumentID: query.InstrumentID,
		AccountID:    query.AccountID,
		ClientID:     query.ClientID,
		VenueOrderID: query.VenueOrderID,
	})
	if err != nil || len(reports) == 0 {
		return nil, err
	}
	if len(reports) > 1 {
		return nil, fmt.Errorf("bitget: order identity matched %d configured categories", len(reports))
	}
	return &reports[0], nil
}

func (c *executionClient) GenerateFillReports(ctx context.Context, query model.FillReportQuery) ([]model.FillReport, error) {
	reports, incomplete, unsafeIncomplete, err := c.generateFillReports(ctx, query)
	if err != nil {
		return nil, err
	}
	if unsafeIncomplete || (query.Limit <= 0 && incomplete) {
		return nil, fmt.Errorf("bitget: fill history remained incomplete after the bounded raw-record scan")
	}
	return reports, err
}

func (c *executionClient) generateFillReports(ctx context.Context, query model.FillReportQuery) ([]model.FillReport, bool, bool, error) {
	return c.generateFillReportsWithWindowMode(ctx, query, false)
}

func (c *executionClient) generateFillReportsWithWindowMode(ctx context.Context, query model.FillReportQuery, openStart bool) ([]model.FillReport, bool, bool, error) {
	if query.AccountID != "" && query.AccountID != c.accountID {
		return nil, false, false, nil
	}
	targets, err := c.reportTargets(query.InstrumentID)
	if err != nil {
		return nil, false, false, err
	}
	limit := query.Limit
	if limit <= 0 {
		limit = executionMassStatusFillLimit
	}
	var out []model.FillReport
	limitReached := false
	unsafeIncomplete := false
	for _, target := range targets {
		reports, reached, unsafe, err := c.generateFillReportsForCategoryWithWindowMode(ctx, target.category, query, limit, openStart)
		if err != nil {
			return nil, false, false, err
		}
		out = append(out, reports...)
		limitReached = limitReached || reached
		unsafeIncomplete = unsafeIncomplete || unsafe
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Fill.Timestamp.After(out[j].Fill.Timestamp)
	})
	if len(out) > limit {
		out = out[:limit]
		limitReached = true
	}
	return out, limitReached, unsafeIncomplete, nil
}

func (c *executionClient) generateFillReportsForCategory(ctx context.Context, category string, query model.FillReportQuery, limit int) ([]model.FillReport, bool, bool, error) {
	return c.generateFillReportsForCategoryWithWindowMode(ctx, category, query, limit, false)
}

func (c *executionClient) generateFillReportsForCategoryWithWindowMode(ctx context.Context, category string, query model.FillReportQuery, limit int, openStart bool) ([]model.FillReport, bool, bool, error) {
	var windows []fillReportWindow
	var err error
	if openStart {
		windows = []fillReportWindow{{until: query.Until}}
	} else {
		windows, err = c.fillReportWindows(query.Since, query.Until)
	}
	if err != nil {
		return nil, false, false, err
	}
	now := c.clk.Now()
	seenExecIDs := make(map[string]struct{})
	var out []model.FillReport
	limitReached := false
	unsafeIncomplete := false
	rawLimit := executionMassStatusFillLimit
	if limit > rawLimit {
		rawLimit = limit
	}
	for windowIndex, window := range windows {
		records, reached, err := c.rest.GetFillsBounded(ctx, bitgetsdk.GetFillsRequest{
			Category:  category,
			OrderID:   query.VenueOrderID,
			StartTime: unixMillisString(window.since),
			EndTime:   unixMillisString(window.until),
			Limit:     strconv.Itoa(rawLimit),
		})
		if err != nil {
			return nil, false, false, err
		}
		limitReached = limitReached || reached
		for _, record := range records {
			categoryKnownFromScope := record.Category == "" && query.InstrumentID.Symbol != ""
			if !categoryKnownFromScope && !bitgetRecordCategoryMatches(record.Category, category) {
				return nil, false, false, fmt.Errorf("bitget: fill category mismatch requested=%s received=%s symbol=%s", category, record.Category, record.Symbol)
			}
			if record.ExecID == "" {
				return nil, false, false, fmt.Errorf("bitget: fill record missing execId category=%s symbol=%s", category, record.Symbol)
			}
			execKey := category + "\x00" + record.ExecID
			if _, duplicate := seenExecIDs[execKey]; duplicate {
				continue
			}
			seenExecIDs[execKey] = struct{}{}
			if query.InstrumentID.Symbol != "" {
				inst, ok := c.provider.Instrument(query.InstrumentID)
				if !ok {
					return nil, false, false, fmt.Errorf("bitget: unknown scoped fill instrument %s", query.InstrumentID)
				}
				if normalizeVenueSymbol(record.Symbol) != normalizeVenueSymbol(inst.VenueSymbol) {
					if _, known := c.provider.ResolveVenueCategorySymbol(category, record.Symbol); known {
						continue
					}
					return nil, false, false, fmt.Errorf("bitget: unknown fill instrument category=%s symbol=%s", category, record.Symbol)
				}
			}
			id, ok := c.resolveFillInstrument(category, query.InstrumentID, record.Symbol)
			if !ok {
				return nil, false, false, fmt.Errorf("bitget: unknown fill instrument category=%s symbol=%s", category, record.Symbol)
			}
			fill := fillFromBitget(record, id, c.accountID)
			if !model.FillMatchesReportQuery(fill, query) {
				continue
			}
			if !fillWithinTimeWindow(fill.Timestamp, query.Since, query.Until) {
				continue
			}
			out = append(out, model.FillReport{Venue: VenueName, AccountID: c.accountID, Fill: fill, ReportedAt: now})
		}
		if reached && len(out) < limit {
			unsafeIncomplete = true
		}
		if len(out) >= limit {
			if windowIndex < len(windows)-1 {
				limitReached = true
			}
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Fill.Timestamp.After(out[j].Fill.Timestamp)
	})
	if len(out) > limit {
		out = out[:limit]
		limitReached = true
	}
	return out, limitReached, unsafeIncomplete, nil
}

func (c *executionClient) GeneratePositionReports(ctx context.Context, query model.PositionReportQuery) ([]model.PositionReport, error) {
	if query.AccountID != "" && query.AccountID != c.accountID {
		return nil, nil
	}
	targets := c.derivativeReportTargets()
	if query.InstrumentID.Symbol != "" {
		inst, err := c.instrument(query.InstrumentID)
		if err != nil {
			return nil, err
		}
		category, err := categoryForInstrument(inst)
		if err != nil {
			return nil, err
		}
		if category == "SPOT" {
			return nil, nil
		}
		if !c.supportsCategory(category) {
			return nil, fmt.Errorf("bitget: instrument category %s is outside the configured scope", category)
		}
		targets = []reportTarget{{category: category, symbol: inst.VenueSymbol}}
	}
	now := c.clk.Now()
	out := make([]model.PositionReport, 0)
	for _, target := range targets {
		reports, err := c.generatePositionReportsForTarget(ctx, target, query, now)
		if err != nil {
			return nil, err
		}
		out = append(out, reports...)
	}
	return out, nil
}

func (c *executionClient) generatePositionReportsForTarget(ctx context.Context, target reportTarget, query model.PositionReportQuery, now time.Time) ([]model.PositionReport, error) {
	records, err := c.rest.GetCurrentPositions(ctx, target.category, target.symbol)
	if err != nil {
		return nil, err
	}
	out := make([]model.PositionReport, 0, len(records))
	for _, record := range records {
		if !bitgetRecordCategoryMatches(record.Category, target.category) {
			return nil, fmt.Errorf("bitget: position category mismatch requested=%s received=%s symbol=%s", target.category, record.Category, record.Symbol)
		}
		id, ok := c.resolveReportInstrument(target.category, query.InstrumentID, record.Symbol)
		if !ok {
			return nil, fmt.Errorf("bitget: unknown position-report instrument category=%s symbol=%s", target.category, record.Symbol)
		}
		quantity, err := bitgetAuthoritativePositionQuantity(record)
		if err != nil {
			return nil, fmt.Errorf("bitget: invalid position-report quantity category=%s symbol=%s: %w", target.category, record.Symbol, err)
		}
		pos, err := positionFromBitget(record, func(string) model.InstrumentID { return id }, c.accountID, now)
		if err != nil {
			return nil, fmt.Errorf("bitget: invalid position-report semantics category=%s symbol=%s: %w", target.category, record.Symbol, err)
		}
		pos.Quantity = quantity
		if query.InstrumentID.Symbol != "" && pos.InstrumentID != query.InstrumentID {
			continue
		}
		out = append(out, model.PositionReport{Venue: VenueName, AccountID: c.accountID, Position: pos, ReportedAt: now})
	}
	return out, nil
}

func (c *executionClient) GenerateExecutionMassStatus(ctx context.Context, query model.MassStatusQuery) (*model.ExecutionMassStatus, error) {
	if query.AccountID != "" && query.AccountID != c.accountID {
		return nil, fmt.Errorf("bitget: mass status account %q does not match execution account %q", query.AccountID, c.accountID)
	}
	frozen, selector, err := c.freezeMassStatusScope(query)
	if err != nil {
		return nil, err
	}
	selectorSet := bitgetInstrumentIDSet(selector)
	openSelector := append([]model.InstrumentID{}, selector...)
	fillSelector := append([]model.InstrumentID{}, selector...)
	positionSelector := bitgetSelectorForKind(selector, enums.KindPerp)
	mass := model.NewExecutionMassStatus(VenueName, c.accountID, frozen.clk.Now())
	mass.ClientID = query.ClientID
	mass.Lookback = query.Lookback
	mass.FillsCoverage = model.ReportCoverage{State: model.CoverageNotRequested}
	mass.PositionsCoverage = model.ReportCoverage{State: model.CoverageNotRequested}
	if frozen.rest == nil {
		mass.OpenOrdersCoverage = model.ReportCoverage{State: model.CoverageUnavailable}
		if query.IncludeFills {
			mass.FillsCoverage = model.ReportCoverage{State: model.CoverageUnavailable}
		}
		if query.IncludePositions {
			mass.PositionsCoverage = model.ReportCoverage{State: model.CoverageUnavailable}
		}
		if err := mass.ValidateFor(query); err != nil {
			return nil, err
		}
		return mass, nil
	}
	openStartedAt := frozen.clk.Now()
	openTargets, err := frozen.massStatusReportTargets(openSelector)
	if err != nil {
		return nil, err
	}
	openSuccesses := 0
	openFailures := 0
	for _, target := range openTargets {
		orders, err := frozen.generateOrderStatusReportsForTarget(ctx, target, model.OrderStatusReportQuery{AccountID: c.accountID, ClientID: query.ClientID, OpenOnly: true}, frozen.clk.Now())
		if err != nil {
			openFailures++
			mass.Warnings = append(mass.Warnings, model.ReportWarning{Code: "OPEN_ORDERS_UNAVAILABLE", Message: fmt.Sprintf("category %s: %v", target.category, err)})
			continue
		}
		openSuccesses++
		for _, report := range orders {
			if _, ok := selectorSet[report.Order.Request.InstrumentID.String()]; !ok {
				continue
			}
			if err := mass.AddOrderReport(report); err != nil {
				return nil, err
			}
		}
	}
	mass.OpenOrdersCoverage = model.NewSnapshotCoverage(bitgetMassStatusCoverageState(len(openTargets), openSuccesses, openFailures, false), c.accountID, query.ClientID, openSelector, openStartedAt)
	if query.IncludeFills && frozen.Capabilities().Reports.FillHistory {
		fillThrough := query.Until
		if fillThrough.IsZero() {
			fillThrough = frozen.clk.Now()
		}
		fillFrom := query.Since
		if fillFrom.IsZero() && query.Lookback > 0 && !query.Until.IsZero() {
			fillFrom = query.Until.Add(-query.Lookback)
		}
		fillQuery := model.FillReportQuery{
			AccountID: c.accountID,
			ClientID:  query.ClientID,
			Since:     fillFrom,
			Until:     fillThrough,
			Limit:     executionMassStatusFillLimit,
		}
		fillTargets, err := frozen.massStatusReportTargets(fillSelector)
		if err != nil {
			return nil, err
		}
		fillSuccesses := 0
		fillFailures := 0
		limitReached := false
		unsafeIncomplete := false
		fills := make([]model.FillReport, 0)
		openStart := query.Since.IsZero() && query.Until.IsZero()
		for _, target := range fillTargets {
			reports, reached, unsafe, err := frozen.generateFillReportsForCategoryWithWindowMode(ctx, target.category, fillQuery, executionMassStatusFillLimit, openStart)
			if err != nil {
				fillFailures++
				mass.Warnings = append(mass.Warnings, model.ReportWarning{Code: "FILL_REPORTS_UNAVAILABLE", Message: fmt.Sprintf("category %s: %v", target.category, err)})
				continue
			}
			fillSuccesses++
			limitReached = limitReached || reached
			unsafeIncomplete = unsafeIncomplete || unsafe
			fills = append(fills, reports...)
		}
		sort.SliceStable(fills, func(i, j int) bool {
			return fills[i].Fill.Timestamp.After(fills[j].Fill.Timestamp)
		})
		if len(fills) > executionMassStatusFillLimit {
			fills = fills[:executionMassStatusFillLimit]
			limitReached = true
		}
		for _, report := range fills {
			if _, ok := selectorSet[report.Fill.InstrumentID.String()]; !ok {
				continue
			}
			if err := mass.AddFillReport(report); err != nil {
				return nil, err
			}
		}
		if limitReached {
			mass.Warnings = append(mass.Warnings, model.ReportWarning{
				Code:    "FILL_REPORTS_LIMIT_REACHED",
				Message: fmt.Sprintf("fill-history recovery reached the global %d-record limit; recovered fills may be incomplete", executionMassStatusFillLimit),
			})
		}
		if fillSuccesses > 0 {
			if err := frozen.addHistoricalDerivativeOrders(ctx, mass); err != nil {
				return nil, err
			}
		}
		fillState := bitgetMassStatusCoverageState(len(fillTargets), fillSuccesses, fillFailures, limitReached || unsafeIncomplete)
		mass.FillsCoverage = model.NewFillCoverage(fillState, c.accountID, query.ClientID, fillSelector, fillFrom, fillThrough)
	} else if query.IncludeFills {
		mass.FillsCoverage = model.ReportCoverage{State: model.CoverageUnavailable}
	}
	if query.IncludePositions {
		positionsStartedAt := frozen.clk.Now()
		if len(positionSelector) == 0 {
			mass.PositionsCoverage = model.NewSnapshotCoverage(model.CoverageComplete, c.accountID, query.ClientID, positionSelector, positionsStartedAt)
		} else if !frozen.Capabilities().Reports.PositionReports {
			mass.PositionsCoverage = model.ReportCoverage{State: model.CoverageUnavailable}
		} else {
			positionTargets, err := frozen.massStatusReportTargets(positionSelector)
			if err != nil {
				return nil, err
			}
			positionSuccesses := 0
			positionFailures := 0
			for _, target := range positionTargets {
				positions, err := frozen.generatePositionReportsForTarget(ctx, target, model.PositionReportQuery{AccountID: c.accountID}, frozen.clk.Now())
				if err != nil {
					positionFailures++
					mass.Warnings = append(mass.Warnings, model.ReportWarning{Code: "POSITIONS_UNAVAILABLE", Message: fmt.Sprintf("category %s: %v", target.category, err)})
					continue
				}
				positionSuccesses++
				for _, report := range positions {
					if _, ok := selectorSet[report.Position.InstrumentID.String()]; !ok {
						continue
					}
					if err := mass.AddPositionReport(report); err != nil {
						return nil, err
					}
				}
			}
			mass.PositionsCoverage = model.NewSnapshotCoverage(bitgetMassStatusCoverageState(len(positionTargets), positionSuccesses, positionFailures, false), c.accountID, query.ClientID, positionSelector, positionsStartedAt)
		}
	}
	if err := mass.ValidateFor(query); err != nil {
		return nil, err
	}
	return mass, nil
}

func (c *executionClient) freezeMassStatusScope(query model.MassStatusQuery) (*executionClient, []model.InstrumentID, error) {
	if venue := strings.TrimSpace(query.Venue); venue != "" && venue != VenueName {
		return nil, nil, fmt.Errorf("bitget: mass status venue %q does not match %q", query.Venue, VenueName)
	}
	if c.provider == nil {
		return nil, nil, fmt.Errorf("bitget: instrument provider required for mass status")
	}
	c.provider.mu.RLock()
	instruments := make([]*model.Instrument, 0, len(c.provider.all))
	for _, inst := range c.provider.all {
		if inst == nil {
			continue
		}
		clone := *inst
		if inst.AssetIndex != nil {
			assetIndex := *inst.AssetIndex
			clone.AssetIndex = &assetIndex
		}
		instruments = append(instruments, &clone)
	}
	categories := make([]string, 0, len(c.provider.categoryScope))
	for category := range c.provider.categoryScope {
		categories = append(categories, category)
	}
	c.provider.mu.RUnlock()
	sort.Strings(categories)
	snapshot := newInstrumentProvider()
	snapshot.loadSnapshot(instruments, categories)
	frozen := *c
	frozen.provider = snapshot
	frozen.categories = append([]string(nil), c.categories...)
	selector, err := frozen.massStatusSelector(query.InstrumentIDs)
	if err != nil {
		return nil, nil, err
	}
	return &frozen, selector, nil
}

func (c *executionClient) massStatusSelector(requested []model.InstrumentID) ([]model.InstrumentID, error) {
	if requested != nil {
		selector := model.NormalizeInstrumentIDs(requested)
		for _, id := range selector {
			inst, ok := c.provider.Instrument(id)
			if !ok {
				return nil, fmt.Errorf("bitget: unknown mass status instrument %s: %w", id, errs.ErrSymbolNotFound)
			}
			category, err := categoryForInstrument(inst)
			if err != nil || id.Venue != VenueName || !c.supportsCategory(category) {
				return nil, fmt.Errorf("bitget: mass status instrument %s is outside execution scope", id)
			}
		}
		return selector, nil
	}
	all := c.provider.All()
	selector := make([]model.InstrumentID, 0, len(all))
	for _, inst := range all {
		if inst == nil || inst.ID.Venue != VenueName {
			continue
		}
		category, err := categoryForInstrument(inst)
		if err == nil && c.supportsCategory(category) {
			selector = append(selector, inst.ID)
		}
	}
	return model.NormalizeInstrumentIDs(selector), nil
}

func (c *executionClient) massStatusReportTargets(selector []model.InstrumentID) ([]reportTarget, error) {
	seen := make(map[string]struct{})
	targets := make([]reportTarget, 0, len(c.categories))
	for _, id := range selector {
		inst, err := c.instrument(id)
		if err != nil {
			return nil, err
		}
		category, err := categoryForInstrument(inst)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[category]; ok {
			continue
		}
		seen[category] = struct{}{}
		targets = append(targets, reportTarget{category: category})
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].category < targets[j].category })
	return targets, nil
}

func bitgetMassStatusCoverageState(attempts, successes, failures int, incomplete bool) model.CoverageState {
	switch {
	case attempts == 0:
		return model.CoverageComplete
	case successes == 0 && failures > 0:
		return model.CoverageUnavailable
	case failures > 0 || incomplete:
		return model.CoveragePartial
	default:
		return model.CoverageComplete
	}
}

func bitgetSelectorForKind(ids []model.InstrumentID, kind enums.InstrumentKind) []model.InstrumentID {
	out := make([]model.InstrumentID, 0, len(ids))
	for _, id := range ids {
		if id.Kind == kind {
			out = append(out, id)
		}
	}
	return model.NormalizeInstrumentIDs(out)
}

func bitgetInstrumentIDSet(ids []model.InstrumentID) map[string]struct{} {
	out := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		out[id.String()] = struct{}{}
	}
	return out
}

type historicalFillOrderIdentity struct {
	venueOrderID string
	clientID     string
}

type derivativeOrderHistoryGroup struct {
	instrumentID model.InstrumentID
	identities   map[historicalFillOrderIdentity]struct{}
}

// addHistoricalDerivativeOrders batches terminal order recovery by instrument
// and retained history window. Any identity not found in the bounded scan is
// deliberately left absent so the runtime can use its exact-order fallback.
func (c *executionClient) addHistoricalDerivativeOrders(ctx context.Context, mass *model.ExecutionMassStatus) error {
	groups := make(map[model.InstrumentID]*derivativeOrderHistoryGroup)
	for _, reports := range mass.FillReports {
		for _, report := range reports {
			fill := report.Fill
			if fill.InstrumentID.Kind == enums.KindSpot || massHasStrictFillOrder(mass, fill) {
				continue
			}
			identity := historicalFillOrderIdentity{venueOrderID: fill.VenueOrderID, clientID: fill.ClientID}
			group := groups[fill.InstrumentID]
			if group == nil {
				group = &derivativeOrderHistoryGroup{
					instrumentID: fill.InstrumentID,
					identities:   make(map[historicalFillOrderIdentity]struct{}),
				}
				groups[fill.InstrumentID] = group
			}
			group.identities[identity] = struct{}{}
		}
	}

	orderedGroups := make([]*derivativeOrderHistoryGroup, 0, len(groups))
	for _, group := range groups {
		orderedGroups = append(orderedGroups, group)
	}
	sort.Slice(orderedGroups, func(i, j int) bool {
		return orderedGroups[i].instrumentID.String() < orderedGroups[j].instrumentID.String()
	})
	for _, group := range orderedGroups {
		if err := c.addHistoricalDerivativeOrdersForInstrument(ctx, mass, group); err != nil {
			return err
		}
	}
	return nil
}

func (c *executionClient) addHistoricalDerivativeOrdersForInstrument(
	ctx context.Context,
	mass *model.ExecutionMassStatus,
	group *derivativeOrderHistoryGroup,
) error {
	inst, err := c.instrument(group.instrumentID)
	if err != nil {
		return err
	}
	category, err := categoryForInstrument(inst)
	if err != nil {
		return err
	}
	if category == "SPOT" {
		return nil
	}
	now := time.UnixMilli(c.clk.Now().UnixMilli())
	windows, err := c.fillReportWindows(now.Add(-bitgetFillHistory), now)
	if err != nil {
		return err
	}
	limitReached := false
	for _, window := range windows {
		records, saturated, err := c.rest.GetOrderHistoryBounded(ctx, bitgetsdk.GetOrderHistoryRequest{
			Category:  category,
			Symbol:    inst.VenueSymbol,
			StartTime: unixMillisString(window.since),
			EndTime:   unixMillisString(window.until),
			Limit:     strconv.Itoa(executionMassStatusFillLimit),
		})
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			mass.Warnings = append(mass.Warnings, model.ReportWarning{
				Code:    "ORDER_HISTORY_PREFETCH_FAILED",
				Message: fmt.Sprintf("historical-order batch recovery failed for %s; exact-order fallback remains required: %v", group.instrumentID, err),
			})
			return nil
		}
		limitReached = limitReached || saturated
		for _, record := range records {
			identity, wanted := matchingHistoricalFillIdentity(record, group.identities)
			if !wanted {
				continue
			}
			if !bitgetRecordCategoryMatches(record.Category, category) {
				return fmt.Errorf("bitget: historical order category mismatch requested=%s received=%s symbol=%s", category, record.Category, record.Symbol)
			}
			if normalizeVenueSymbol(record.Symbol) != normalizeVenueSymbol(inst.VenueSymbol) {
				return fmt.Errorf("bitget: historical order symbol mismatch requested=%s received=%s category=%s", inst.VenueSymbol, record.Symbol, category)
			}
			order, err := orderFromBitgetRecord(record, group.instrumentID, c.accountID)
			if err != nil {
				return fmt.Errorf("bitget: invalid historical order position semantics symbol=%s: %w", record.Symbol, err)
			}
			if !historicalIdentityMatchesOrder(identity, order) || massHasConflictingOrderIdentity(mass, order) {
				continue
			}
			if err := mass.AddOrderReport(model.OrderStatusReport{
				Venue:      VenueName,
				AccountID:  c.accountID,
				Order:      order,
				ReportedAt: c.clk.Now(),
			}); err != nil {
				return err
			}
			deleteHistoricalFillIdentitiesForOrder(group.identities, order)
		}
		if len(group.identities) == 0 {
			break
		}
	}
	if limitReached && len(group.identities) > 0 {
		mass.Warnings = append(mass.Warnings, model.ReportWarning{
			Code:    "ORDER_HISTORY_PREFETCH_LIMIT_REACHED",
			Message: fmt.Sprintf("historical-order batch recovery reached the bounded record limit for %s; exact-order fallback remains required", group.instrumentID),
		})
	}
	return nil
}

func matchingHistoricalFillIdentity(record bitgetsdk.OrderRecord, identities map[historicalFillOrderIdentity]struct{}) (historicalFillOrderIdentity, bool) {
	candidates := [...]historicalFillOrderIdentity{
		{venueOrderID: record.OrderID, clientID: record.ClientOID},
		{venueOrderID: record.OrderID},
		{clientID: record.ClientOID},
	}
	for _, candidate := range candidates {
		if _, ok := identities[candidate]; ok {
			return candidate, true
		}
	}
	return historicalFillOrderIdentity{}, false
}

func historicalIdentityMatchesOrder(identity historicalFillOrderIdentity, order model.Order) bool {
	if identity.venueOrderID != "" && order.VenueOrderID != identity.venueOrderID {
		return false
	}
	if identity.clientID != "" && order.Request.ClientID != identity.clientID {
		return false
	}
	return identity.venueOrderID != "" || identity.clientID != ""
}

func deleteHistoricalFillIdentitiesForOrder(identities map[historicalFillOrderIdentity]struct{}, order model.Order) {
	for identity := range identities {
		if historicalIdentityMatchesOrder(identity, order) {
			delete(identities, identity)
		}
	}
}

func massHasStrictFillOrder(mass *model.ExecutionMassStatus, fill model.Fill) bool {
	for _, report := range mass.OrderReports {
		if model.OrderMatchesStatusQuery(report.Order, model.OrderStatusReportQuery{
			InstrumentID: fill.InstrumentID,
			AccountID:    fill.AccountID,
			ClientID:     fill.ClientID,
			VenueOrderID: fill.VenueOrderID,
		}) {
			return true
		}
	}
	return false
}

func massHasConflictingOrderIdentity(mass *model.ExecutionMassStatus, candidate model.Order) bool {
	if _, exists := mass.OrderReports[candidate.VenueOrderID]; candidate.VenueOrderID != "" && exists {
		return true
	}
	for _, report := range mass.OrderReports {
		existing := report.Order
		if candidate.VenueOrderID != "" && existing.VenueOrderID == candidate.VenueOrderID {
			return true
		}
		if candidate.Request.ClientID != "" && existing.Request.ClientID == candidate.Request.ClientID {
			return true
		}
	}
	return false
}

func (c *executionClient) resolveFillInstrument(category string, scoped model.InstrumentID, venueSymbol string) (model.InstrumentID, bool) {
	return c.resolveReportInstrument(category, scoped, venueSymbol)
}

func (c *executionClient) resolveReportInstrument(category string, scoped model.InstrumentID, venueSymbol string) (model.InstrumentID, bool) {
	if scoped.Symbol != "" {
		inst, ok := c.provider.Instrument(scoped)
		if !ok || normalizeVenueSymbol(inst.VenueSymbol) != normalizeVenueSymbol(venueSymbol) {
			return model.InstrumentID{}, false
		}
		actualCategory, err := categoryForInstrument(inst)
		if err != nil || actualCategory != category {
			return model.InstrumentID{}, false
		}
		return scoped, true
	}
	return c.provider.ResolveVenueCategorySymbol(category, venueSymbol)
}

type fillReportWindow struct {
	since time.Time
	until time.Time
}

func (c *executionClient) fillReportWindows(since, until time.Time) ([]fillReportWindow, error) {
	if since.IsZero() && until.IsZero() {
		return []fillReportWindow{{}}, nil
	}
	now := time.UnixMilli(c.clk.Now().UnixMilli())
	if until.IsZero() || until.After(now) {
		until = now
	}
	historyFloor := now.Add(-bitgetFillHistory)
	if since.IsZero() {
		since = historyFloor
	}
	if since.Before(historyFloor) {
		return nil, fmt.Errorf("bitget: fill history starts at %s, before the supported 90-day floor %s", since.UTC().Format(time.RFC3339), historyFloor.UTC().Format(time.RFC3339))
	}
	if until.Before(historyFloor) {
		return nil, fmt.Errorf("bitget: fill history ends at %s, before the supported 90-day floor %s", until.UTC().Format(time.RFC3339), historyFloor.UTC().Format(time.RFC3339))
	}
	if since.After(until) {
		return nil, fmt.Errorf("bitget: fill history start %s is after end %s", since.UTC().Format(time.RFC3339), until.UTC().Format(time.RFC3339))
	}
	if until.Sub(since) > bitgetFillHistory {
		return nil, fmt.Errorf("bitget: fill history window %s exceeds the supported 90 days", until.Sub(since))
	}
	if since.Equal(until) {
		return []fillReportWindow{{since: since, until: until}}, nil
	}
	windows := make([]fillReportWindow, 0, 3)
	windowEnd := until
	for {
		windowStart := windowEnd.Add(-bitgetFillWindow)
		if windowStart.Before(since) {
			windowStart = since
		}
		windows = append(windows, fillReportWindow{since: windowStart, until: windowEnd})
		if windowStart.Equal(since) {
			break
		}
		windowEnd = windowStart
	}
	return windows, nil
}

func unixMillisString(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return strconv.FormatInt(value.UnixMilli(), 10)
}

func fillWithinTimeWindow(timestamp, since, until time.Time) bool {
	if !since.IsZero() && (timestamp.IsZero() || timestamp.Before(since)) {
		return false
	}
	if !until.IsZero() && (timestamp.IsZero() || timestamp.After(until)) {
		return false
	}
	return true
}

type reportTarget struct {
	category string
	symbol   string
}

func (c *executionClient) reportTargets(id model.InstrumentID) ([]reportTarget, error) {
	if id.Symbol == "" {
		targets := make([]reportTarget, 0, len(c.categories))
		for _, category := range c.categories {
			targets = append(targets, reportTarget{category: category})
		}
		return targets, nil
	}
	inst, err := c.instrument(id)
	if err != nil {
		return nil, err
	}
	category, err := categoryForInstrument(inst)
	if err != nil {
		return nil, err
	}
	if !c.supportsCategory(category) {
		return nil, fmt.Errorf("bitget: instrument category %s is outside the configured scope", category)
	}
	return []reportTarget{{category: category, symbol: inst.VenueSymbol}}, nil
}

func (c *executionClient) derivativeReportTargets() []reportTarget {
	var targets []reportTarget
	for _, category := range c.categories {
		switch category {
		case bitgetsdk.ProductTypeUSDTFutures, bitgetsdk.ProductTypeUSDCFutures:
			targets = append(targets, reportTarget{category: category})
		}
	}
	return targets
}

func (c *executionClient) supportsCategory(category string) bool {
	normalized, ok := normalizeBitgetCategory(category)
	if !ok {
		return false
	}
	for _, configured := range c.categories {
		if configured == normalized {
			return true
		}
	}
	return false
}

func bitgetRecordCategoryMatches(recordCategory, requestedCategory string) bool {
	recordNormalized, recordOK := normalizeBitgetCategory(recordCategory)
	requestedNormalized, requestedOK := normalizeBitgetCategory(requestedCategory)
	return recordOK && requestedOK && recordNormalized == requestedNormalized
}

func (c *executionClient) Capabilities() contract.Capabilities {
	products := make([]contract.ProductCapability, 0, 2)
	spot := c.supportsCategory("SPOT")
	perp := c.supportsCategory(bitgetsdk.ProductTypeUSDTFutures) || c.supportsCategory(bitgetsdk.ProductTypeUSDCFutures)
	if spot {
		products = append(products, contract.ProductCapability{Kind: enums.KindSpot, Trading: true})
	}
	if perp {
		products = append(products, contract.ProductCapability{Kind: enums.KindPerp, Trading: true})
	}
	hasScope := len(products) > 0
	return contract.Capabilities{
		Venue:    VenueName,
		Products: products,
		Reports: contract.ReportCapabilities{
			SingleOrderStatus:         hasScope,
			OpenOrders:                hasScope,
			FillHistory:               hasScope,
			PositionReports:           perp,
			OpenOnlyNotFoundAmbiguous: hasScope,
		},
		Streaming: contract.StreamCapabilities{Execution: hasScope},
		Trading:   contract.TradingCapabilities{Submit: hasScope, Cancel: hasScope, CancelAll: hasScope, Modify: hasScope},
	}
}

func bitgetCategoriesFromProvider(provider *instrumentProvider) []string {
	if provider == nil {
		return nil
	}
	seen := make(map[string]struct{})
	var categories []string
	for _, inst := range provider.All() {
		category, err := categoryForInstrument(inst)
		if err != nil {
			continue
		}
		if _, duplicate := seen[category]; duplicate {
			continue
		}
		seen[category] = struct{}{}
		categories = append(categories, category)
	}
	return categories
}

func (c *executionClient) Events() <-chan contract.ExecEnvelope { return c.stream.C() }
func (c *executionClient) emit(ev contract.ExecEvent)           { c.stream.Emit(contract.NewExecEnvelope(ev)) }
func (c *executionClient) Close() error                         { c.stream.Close(); return nil }
