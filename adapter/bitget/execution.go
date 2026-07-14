package bitget

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
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
		return nil, err
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
	return err
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
	return c.rest.CancelAllOrders(ctx, &bitgetsdk.CancelAllOrdersRequest{Category: category, Symbol: inst.VenueSymbol})
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
		return nil, err
	}
	req := model.OrderRequest{AccountID: c.accountID, InstrumentID: id, Quantity: newQty, Price: newPrice}
	order := model.Order{Request: req, VenueOrderID: resp.OrderID, Status: enums.StatusNew, UpdatedAt: c.clk.Now()}
	return &order, nil
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
		records, err := c.rest.GetOpenOrders(ctx, target.category, target.symbol)
		if err != nil {
			return nil, err
		}
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
	}
	if query.Limit > 0 && len(out) > query.Limit {
		out = out[:query.Limit]
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
		reports, reached, unsafe, err := c.generateFillReportsForCategory(ctx, target.category, query, limit)
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
	windows, err := c.fillReportWindows(query.Since, query.Until)
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
		records, err := c.rest.GetCurrentPositions(ctx, target.category, target.symbol)
		if err != nil {
			return nil, err
		}
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
	}
	return out, nil
}

func (c *executionClient) GenerateExecutionMassStatus(ctx context.Context, query model.MassStatusQuery) (*model.ExecutionMassStatus, error) {
	if query.AccountID != "" && query.AccountID != c.accountID {
		return model.NewExecutionMassStatus(VenueName, query.AccountID, c.clk.Now()), nil
	}
	mass := model.NewExecutionMassStatus(VenueName, c.accountID, c.clk.Now())
	mass.ClientID = query.ClientID
	mass.Lookback = query.Lookback
	mass.Partial = true
	orders, err := c.GenerateOrderStatusReports(ctx, model.OrderStatusReportQuery{AccountID: c.accountID, ClientID: query.ClientID, OpenOnly: true})
	if err != nil {
		return nil, err
	}
	for _, report := range orders {
		if err := mass.AddOrderReport(report); err != nil {
			return nil, err
		}
	}
	if query.IncludeFills && c.Capabilities().Reports.FillHistory {
		fills, limitReached, _, err := c.generateFillReports(ctx, model.FillReportQuery{
			AccountID: c.accountID,
			ClientID:  query.ClientID,
			Since:     query.Since,
			Until:     query.Until,
			Limit:     executionMassStatusFillLimit,
		})
		if err != nil {
			return nil, err
		}
		for _, report := range fills {
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
		if err := c.addHistoricalDerivativeOrders(ctx, mass); err != nil {
			return nil, err
		}
	}
	if query.IncludePositions && c.Capabilities().Reports.PositionReports {
		positions, err := c.GeneratePositionReports(ctx, model.PositionReportQuery{AccountID: c.accountID})
		if err != nil {
			return nil, err
		}
		for _, report := range positions {
			if err := mass.AddPositionReport(report); err != nil {
				return nil, err
			}
		}
	}
	return mass, nil
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
