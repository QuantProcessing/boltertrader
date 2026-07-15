package bybit

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/errs"
	"github.com/QuantProcessing/boltertrader/internal/wsstream"
	bybitsdk "github.com/QuantProcessing/boltertrader/sdk/bybit"
	"github.com/shopspring/decimal"
)

const (
	executionMassStatusFillLimit         = 1000
	derivativeOrderHistoryHydrationLimit = 1000
)

const derivativeOrderHistoryWindow = 7 * 24 * time.Hour

type executionClient struct {
	rest       *bybitsdk.Client
	provider   *instrumentProvider
	clk        clock.Clock
	accountID  string
	categories []string
	stream     *wsstream.Stream[contract.ExecEnvelope]
}

func newExecutionClient(rest *bybitsdk.Client, provider *instrumentProvider, clk clock.Clock, accountIDs ...string) *executionClient {
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
		categories: []string{"spot", "linear"},
		stream:     wsstream.New[contract.ExecEnvelope](256),
	}
}

func (c *executionClient) withCategories(categories ...string) *executionClient {
	seen := make(map[string]struct{}, len(categories))
	normalized := make([]string, 0, len(categories))
	for _, category := range categories {
		category = strings.ToLower(strings.TrimSpace(category))
		if category != "spot" && category != "linear" {
			continue
		}
		if _, ok := seen[category]; ok {
			continue
		}
		seen[category] = struct{}{}
		normalized = append(normalized, category)
	}
	if len(normalized) == 0 {
		normalized = []string{"spot", "linear"}
	}
	c.categories = normalized
	return c
}

func (c *executionClient) supportsCategory(category string) bool {
	for _, supported := range c.categories {
		if supported == category {
			return true
		}
	}
	return false
}

func (c *executionClient) AccountID() string { return c.accountID }

func (c *executionClient) instrument(id model.InstrumentID) (*model.Instrument, error) {
	inst, ok := c.provider.Instrument(id)
	if !ok {
		return nil, fmt.Errorf("bybit: unknown instrument %s: %w", id, errs.ErrSymbolNotFound)
	}
	return inst, nil
}

func (c *executionClient) ValidateSubmit(req model.OrderRequest) error {
	inst, err := c.instrument(req.InstrumentID)
	if err != nil {
		return err
	}
	_, err = orderRequestToBybit(req, inst)
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
	venueReq, err := orderRequestToBybit(req, inst)
	if err != nil {
		return nil, err
	}
	resp, err := c.rest.PlaceOrder(ctx, venueReq)
	if err != nil {
		return nil, bybitCommandError("submit order", err)
	}
	order := orderFromBybitAction(resp, req, c.clk.Now())
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
	_, err = c.rest.CancelOrder(ctx, bybitsdk.CancelOrderRequest{Category: category, Symbol: inst.VenueSymbol, OrderID: venueOrderID})
	return bybitCommandError("cancel order", err)
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
	return bybitCommandError("cancel all orders", c.rest.CancelAllOrders(ctx, bybitsdk.CancelAllOrdersRequest{Category: category, Symbol: inst.VenueSymbol}))
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
	resp, err := c.rest.AmendOrder(ctx, bybitsdk.AmendOrderRequest{
		Category: category,
		Symbol:   inst.VenueSymbol,
		OrderID:  venueOrderID,
		Qty:      decimalStringOrEmpty(newQty),
		Price:    decimalStringOrEmpty(newPrice),
	})
	if err != nil {
		return nil, bybitCommandError("amend order", err)
	}
	req := model.OrderRequest{AccountID: c.accountID, InstrumentID: id, Quantity: newQty, Price: newPrice}
	order := orderFromBybitAction(resp, req, c.clk.Now())
	return &order, nil
}

func bybitCommandError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if !bybitsdk.IsDefinitiveCommandRejection(err) {
		return err
	}
	return fmt.Errorf("bybit: %s rejected: %w", operation, errors.Join(contract.ErrVenueRejected, err))
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
		order, err := orderFromBybitRecord(record, id, c.accountID)
		if err != nil {
			return nil, err
		}
		out = append(out, order)
	}
	return out, nil
}

func (c *executionClient) GenerateOrderStatusReports(ctx context.Context, query model.OrderStatusReportQuery) ([]model.OrderStatusReport, error) {
	if query.AccountID != "" && query.AccountID != c.accountID {
		return nil, nil
	}
	targets, err := c.orderReportTargets(query)
	if err != nil {
		return nil, err
	}
	now := c.clk.Now()
	var out []model.OrderStatusReport
	for _, target := range targets {
		records, err := c.orderRecords(ctx, target, query)
		if err != nil {
			return nil, err
		}
		for _, record := range records {
			id, ok := c.resolveOrderInstrument(target, query.InstrumentID, record.Symbol)
			if !ok {
				return nil, fmt.Errorf("bybit: unknown order-report instrument category=%s settle=%s symbol=%s", target.category, target.settle, record.Symbol)
			}
			order, err := orderFromBybitRecord(record, id, c.accountID)
			if err != nil {
				return nil, err
			}
			if !model.OrderMatchesStatusQuery(order, query) {
				continue
			}
			out = append(out, model.OrderStatusReport{Venue: VenueName, AccountID: c.accountID, Order: order, ReportedAt: now})
		}
	}
	return out, nil
}

func (c *executionClient) GenerateOrderStatusReport(ctx context.Context, query model.SingleOrderStatusQuery) (*model.OrderStatusReport, error) {
	reports, err := c.GenerateOrderStatusReports(ctx, model.OrderStatusReportQuery{
		InstrumentID: query.InstrumentID,
		AccountID:    query.AccountID,
		ClientID:     query.ClientID,
		VenueOrderID: query.VenueOrderID,
	})
	if err != nil || len(reports) == 0 {
		return nil, err
	}
	return &reports[0], nil
}

func (c *executionClient) GenerateFillReports(ctx context.Context, query model.FillReportQuery) ([]model.FillReport, error) {
	reports, _, err := c.generateFillReports(ctx, query)
	return reports, err
}

func (c *executionClient) generateFillReports(ctx context.Context, query model.FillReportQuery) ([]model.FillReport, bool, error) {
	if query.AccountID != "" && query.AccountID != c.accountID {
		return nil, false, nil
	}
	categories, symbol, err := c.reportTargets(query.InstrumentID)
	if err != nil {
		return nil, false, err
	}
	var out []model.FillReport
	limitReached := false
	for _, category := range categories {
		reports, reached, err := c.generateFillReportsForCategory(ctx, category, symbol, query)
		if err != nil {
			return nil, false, err
		}
		out = append(out, reports...)
		limitReached = limitReached || reached
	}
	if len(categories) > 1 {
		sort.SliceStable(out, func(i, j int) bool {
			return out[i].Fill.Timestamp.After(out[j].Fill.Timestamp)
		})
		if query.Limit > 0 && len(out) > query.Limit {
			out = out[:query.Limit]
			limitReached = true
		}
	}
	return out, limitReached, nil
}

func (c *executionClient) generateFillReportsForCategory(ctx context.Context, category, symbol string, query model.FillReportQuery) ([]model.FillReport, bool, error) {
	if query.Limit <= 0 {
		records, err := c.rest.GetExecutions(ctx, category, symbol, query.VenueOrderID, query.ClientID)
		if err != nil {
			return nil, false, err
		}
		reports, err := c.fillReportsFromBybitRecords(records, category, query)
		return reports, false, err
	}

	request := bybitsdk.GetExecutionsRequest{
		Category:    category,
		Symbol:      symbol,
		OrderID:     query.VenueOrderID,
		OrderLinkID: query.ClientID,
		Limit:       query.Limit,
	}
	if !query.Since.IsZero() {
		request.StartMillis = query.Since.UnixMilli()
	}
	if !query.Until.IsZero() {
		request.EndMillis = query.Until.UnixMilli()
	}
	rawLimit := query.Limit
	for {
		request.Limit = rawLimit
		records, moreRawRecords, err := c.rest.GetExecutionsBounded(ctx, request)
		if err != nil {
			return nil, false, err
		}
		reports, err := c.fillReportsFromBybitRecords(records, category, query)
		if err != nil {
			return nil, false, err
		}
		if len(reports) >= query.Limit {
			hadExtraReports := len(reports) > query.Limit
			return reports[:query.Limit], moreRawRecords || hadExtraReports, nil
		}
		if !moreRawRecords {
			return reports, false, nil
		}
		maxInt := int(^uint(0) >> 1)
		if rawLimit > maxInt/2 {
			return nil, false, fmt.Errorf("bybit: execution history limit overflow while filtering non-trade records")
		}
		rawLimit *= 2
	}
}

func (c *executionClient) fillReportsFromBybitRecords(records []bybitsdk.ExecutionRecord, category string, query model.FillReportQuery) ([]model.FillReport, error) {
	now := c.clk.Now()
	out := make([]model.FillReport, 0, len(records))
	for _, record := range records {
		switch strings.ToLower(strings.TrimSpace(record.ExecType)) {
		case "trade":
		case "funding":
			continue
		default:
			return nil, fmt.Errorf("bybit: unsupported execution type %q for category=%s symbol=%s", record.ExecType, category, record.Symbol)
		}
		id, ok := c.resolveFillInstrument(category, query.InstrumentID, record.Symbol)
		if !ok {
			if query.InstrumentID.Symbol == "" && (c.provider.isDeferred(category, record.Symbol) ||
				(category == "linear" && isBybitDatedLinearSymbol(record.Symbol))) {
				continue
			}
			return nil, fmt.Errorf("bybit: unknown fill instrument category=%s symbol=%s", category, record.Symbol)
		}
		fill := fillFromBybitExecution(record, id, c.accountID)
		if !model.FillMatchesReportQuery(fill, query) {
			continue
		}
		if !query.Since.IsZero() && (fill.Timestamp.IsZero() || fill.Timestamp.Before(query.Since)) {
			continue
		}
		if !query.Until.IsZero() && (fill.Timestamp.IsZero() || fill.Timestamp.After(query.Until)) {
			continue
		}
		out = append(out, model.FillReport{Venue: VenueName, AccountID: c.accountID, Fill: fill, ReportedAt: now})
	}
	return out, nil
}

func (c *executionClient) GeneratePositionReports(ctx context.Context, query model.PositionReportQuery) ([]model.PositionReport, error) {
	if query.AccountID != "" && query.AccountID != c.accountID {
		return nil, nil
	}
	if !c.supportsCategory("linear") {
		return nil, nil
	}
	settles := []string{bybitsdk.SettleCoinUSDT, bybitsdk.SettleCoinUSDC}
	if query.InstrumentID.Symbol != "" {
		inst, ok := c.provider.Instrument(query.InstrumentID)
		if !ok {
			return nil, fmt.Errorf("bybit: unknown position-report instrument %s", query.InstrumentID)
		}
		category, err := categoryForInstrument(inst)
		if err != nil {
			return nil, err
		}
		if category != "linear" {
			return nil, nil
		}
		settles = []string{inst.Settle}
	}
	now := c.clk.Now()
	out := make([]model.PositionReport, 0)
	for _, settle := range settles {
		reports, err := c.generatePositionReportsForSettle(ctx, settle, query, now)
		if err != nil {
			return nil, err
		}
		out = append(out, reports...)
	}
	return out, nil
}

func (c *executionClient) generatePositionReportsForSettle(ctx context.Context, settle string, query model.PositionReportQuery, now time.Time) ([]model.PositionReport, error) {
	records, err := c.rest.GetPositions(ctx, "linear", "", settle)
	if err != nil {
		return nil, err
	}
	out := make([]model.PositionReport, 0, len(records))
	for _, record := range records {
		id, ok := c.provider.ResolveVenueInstrument(record.Symbol, enums.KindPerp, settle)
		if !ok {
			return nil, fmt.Errorf("bybit: unknown position-report instrument settle=%s symbol=%s", settle, record.Symbol)
		}
		pos, err := positionFromBybit(record, func(string) model.InstrumentID { return id }, c.accountID, now)
		if err != nil {
			return nil, err
		}
		if query.InstrumentID.Symbol != "" && pos.InstrumentID != query.InstrumentID {
			continue
		}
		out = append(out, model.PositionReport{Venue: VenueName, AccountID: c.accountID, Position: pos, ReportedAt: now})
	}
	return out, nil
}

func (c *executionClient) GenerateExecutionMassStatus(ctx context.Context, query model.MassStatusQuery) (*model.ExecutionMassStatus, error) {
	if query.AccountID != "" && query.AccountID != c.accountID {
		return nil, fmt.Errorf("bybit: mass status account %q does not match execution account %q", query.AccountID, c.accountID)
	}
	frozen, selector, err := c.freezeMassStatusScope(query)
	if err != nil {
		return nil, err
	}
	selectorSet := bybitInstrumentIDSet(selector)
	openSelector := append([]model.InstrumentID{}, selector...)
	fillSelector := append([]model.InstrumentID{}, selector...)
	positionSelector := bybitSelectorForKind(selector, enums.KindPerp)
	mass := model.NewExecutionMassStatus(VenueName, c.accountID, c.clk.Now())
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
	orderTargets, err := frozen.massStatusOrderTargets(openSelector, query.ClientID != "")
	if err != nil {
		return nil, err
	}
	openSuccesses := 0
	openFailures := 0
	for _, target := range orderTargets {
		records, err := frozen.rest.GetRealtimeOrders(ctx, target.category, target.symbol, target.settle, "", query.ClientID, 0)
		if err != nil {
			openFailures++
			mass.Warnings = append(mass.Warnings, model.ReportWarning{Code: "OPEN_ORDERS_UNAVAILABLE", Message: err.Error()})
			continue
		}
		openSuccesses++
		for _, record := range records {
			id, ok := frozen.resolveOrderInstrument(target, model.InstrumentID{}, record.Symbol)
			if !ok {
				return nil, fmt.Errorf("bybit: unknown open-order instrument category=%s settle=%s symbol=%s", target.category, target.settle, record.Symbol)
			}
			if _, ok := selectorSet[id.String()]; !ok {
				continue
			}
			order, err := orderFromBybitRecord(record, id, c.accountID)
			if err != nil {
				return nil, err
			}
			if !model.OrderMatchesStatusQuery(order, model.OrderStatusReportQuery{AccountID: c.accountID, ClientID: query.ClientID, OpenOnly: true}) {
				continue
			}
			if err := mass.AddOrderReport(model.OrderStatusReport{Venue: VenueName, AccountID: c.accountID, Order: order, ReportedAt: mass.GeneratedAt}); err != nil {
				return nil, err
			}
		}
	}
	mass.OpenOrdersCoverage = model.NewSnapshotCoverage(bybitMassStatusCoverageState(len(orderTargets), openSuccesses, openFailures, false), c.accountID, query.ClientID, openSelector, openStartedAt)
	if query.IncludeFills {
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
		fillCategories := bybitSelectorCategories(fillSelector)
		fillSuccesses := 0
		fillFailures := 0
		limitReached := false
		fills := make([]model.FillReport, 0)
		for _, category := range fillCategories {
			reports, reached, err := frozen.generateFillReportsForCategory(ctx, category, "", fillQuery)
			if err != nil {
				fillFailures++
				mass.Warnings = append(mass.Warnings, model.ReportWarning{Code: "FILL_REPORTS_UNAVAILABLE", Message: err.Error()})
				continue
			}
			fillSuccesses++
			limitReached = limitReached || reached
			for _, report := range reports {
				if _, ok := selectorSet[report.Fill.InstrumentID.String()]; !ok {
					continue
				}
				fills = append(fills, report)
			}
		}
		sort.SliceStable(fills, func(i, j int) bool {
			return fills[i].Fill.Timestamp.After(fills[j].Fill.Timestamp)
		})
		if len(fills) > executionMassStatusFillLimit {
			fills = fills[:executionMassStatusFillLimit]
			limitReached = true
		}
		for _, report := range fills {
			if err := mass.AddFillReport(report); err != nil {
				return nil, err
			}
		}
		if fillSuccesses > 0 {
			normalizedQuery := query
			normalizedQuery.Since = fillFrom
			normalizedQuery.Until = fillThrough
			if err := frozen.addHistoricalOrderReportsForDerivativeFills(ctx, mass, normalizedQuery); err != nil {
				return nil, err
			}
		}
		if limitReached {
			mass.Warnings = append(mass.Warnings, model.ReportWarning{
				Code:    "FILL_REPORTS_LIMIT_REACHED",
				Message: fmt.Sprintf("the Bybit execution-history query reached the %d-record limit; recovered fills may be incomplete", executionMassStatusFillLimit),
			})
		}
		fillState := bybitMassStatusCoverageState(len(fillCategories), fillSuccesses, fillFailures, limitReached)
		mass.FillsCoverage = model.NewFillCoverage(fillState, c.accountID, query.ClientID, fillSelector, fillFrom, fillThrough)
	}
	if query.IncludePositions {
		positionsStartedAt := frozen.clk.Now()
		if len(positionSelector) == 0 {
			mass.PositionsCoverage = model.NewSnapshotCoverage(model.CoverageComplete, c.accountID, query.ClientID, positionSelector, positionsStartedAt)
		} else if !frozen.supportsCategory("linear") {
			mass.PositionsCoverage = model.ReportCoverage{State: model.CoverageUnavailable}
		} else {
			positionSettles, err := frozen.massStatusPositionSettles(positionSelector)
			if err != nil {
				return nil, err
			}
			positionSuccesses := 0
			positionFailures := 0
			for _, settle := range positionSettles {
				positions, err := frozen.generatePositionReportsForSettle(ctx, settle, model.PositionReportQuery{AccountID: c.accountID}, frozen.clk.Now())
				if err != nil {
					positionFailures++
					mass.Warnings = append(mass.Warnings, model.ReportWarning{Code: "POSITIONS_UNAVAILABLE", Message: fmt.Sprintf("settle %s: %v", settle, err)})
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
			mass.PositionsCoverage = model.NewSnapshotCoverage(bybitMassStatusCoverageState(len(positionSettles), positionSuccesses, positionFailures, false), c.accountID, query.ClientID, positionSelector, positionsStartedAt)
		}
	}
	if err := mass.ValidateFor(query); err != nil {
		return nil, err
	}
	return mass, nil
}

func (c *executionClient) freezeMassStatusScope(query model.MassStatusQuery) (*executionClient, []model.InstrumentID, error) {
	if venue := strings.TrimSpace(query.Venue); venue != "" && venue != VenueName {
		return nil, nil, fmt.Errorf("bybit: mass status venue %q does not match %q", query.Venue, VenueName)
	}
	if c.provider == nil {
		return nil, nil, fmt.Errorf("bybit: instrument provider required for mass status")
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
	deferred := make(map[deferredInstrumentKey]struct{}, len(c.provider.deferred))
	for key := range c.provider.deferred {
		deferred[key] = struct{}{}
	}
	c.provider.mu.RUnlock()
	snapshot := newInstrumentProvider()
	snapshot.loadSnapshot(instruments, deferred)
	frozen := *c
	frozen.provider = snapshot
	frozen.categories = append([]string(nil), c.categories...)
	selector, err := frozen.massStatusSelector(query.InstrumentIDs)
	if err != nil {
		return nil, nil, err
	}
	return &frozen, selector, nil
}

func bybitMassStatusCoverageState(attempts, successes, failures int, incomplete bool) model.CoverageState {
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

func (c *executionClient) massStatusSelector(requested []model.InstrumentID) ([]model.InstrumentID, error) {
	if requested != nil {
		selector := model.NormalizeInstrumentIDs(requested)
		for _, id := range selector {
			inst, ok := c.provider.Instrument(id)
			if !ok {
				return nil, fmt.Errorf("bybit: unknown mass status instrument %s: %w", id, errs.ErrSymbolNotFound)
			}
			category, err := categoryForInstrument(inst)
			if err != nil || id.Venue != VenueName || !c.supportsCategory(category) {
				return nil, fmt.Errorf("bybit: mass status instrument %s is outside execution scope", id)
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

func (c *executionClient) massStatusOrderTargets(selector []model.InstrumentID, exactClient bool) ([]orderReportTarget, error) {
	seen := make(map[string]struct{})
	targets := make([]orderReportTarget, 0)
	for _, id := range selector {
		inst, err := c.instrument(id)
		if err != nil {
			return nil, err
		}
		category, err := categoryForInstrument(inst)
		if err != nil {
			return nil, err
		}
		settle := ""
		if category == "linear" && !exactClient {
			settle = inst.Settle
		}
		key := category + "\x00" + settle
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		targets = append(targets, orderReportTarget{category: category, settle: settle})
	}
	return targets, nil
}

func bybitSelectorForKind(ids []model.InstrumentID, kind enums.InstrumentKind) []model.InstrumentID {
	out := make([]model.InstrumentID, 0, len(ids))
	for _, id := range ids {
		if id.Kind == kind {
			out = append(out, id)
		}
	}
	return model.NormalizeInstrumentIDs(out)
}

func bybitSelectorCategories(ids []model.InstrumentID) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0, 2)
	for _, id := range ids {
		category := ""
		switch id.Kind {
		case enums.KindSpot:
			category = "spot"
		case enums.KindPerp:
			category = "linear"
		}
		if category == "" {
			continue
		}
		if _, ok := seen[category]; ok {
			continue
		}
		seen[category] = struct{}{}
		out = append(out, category)
	}
	return out
}

func (c *executionClient) massStatusPositionSettles(ids []model.InstrumentID) ([]string, error) {
	seen := make(map[string]struct{})
	settles := make([]string, 0, 2)
	for _, id := range ids {
		inst, err := c.instrument(id)
		if err != nil {
			return nil, err
		}
		settle := strings.ToUpper(strings.TrimSpace(inst.Settle))
		if settle == "" {
			return nil, fmt.Errorf("bybit: position instrument %s has no settlement asset", id)
		}
		if _, ok := seen[settle]; ok {
			continue
		}
		seen[settle] = struct{}{}
		settles = append(settles, settle)
	}
	sort.Strings(settles)
	return settles, nil
}

func bybitInstrumentIDSet(ids []model.InstrumentID) map[string]struct{} {
	out := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		out[id.String()] = struct{}{}
	}
	return out
}

type derivativeFillOrderIdentity struct {
	venueOrderID string
	clientID     string
}

func (c *executionClient) addHistoricalOrderReportsForDerivativeFills(ctx context.Context, mass *model.ExecutionMassStatus, query model.MassStatusQuery) error {
	groups := make(map[model.InstrumentID]map[derivativeFillOrderIdentity]struct{})
	for _, reports := range mass.FillReports {
		for _, report := range reports {
			fill := report.Fill
			if fill.InstrumentID.Kind != enums.KindPerp {
				continue
			}
			identity := derivativeFillOrderIdentity{venueOrderID: fill.VenueOrderID, clientID: fill.ClientID}
			if identity.venueOrderID == "" && identity.clientID == "" {
				continue
			}
			if massHasOrderMatchingDerivativeFill(mass, fill.InstrumentID, identity) {
				continue
			}
			if groups[fill.InstrumentID] == nil {
				groups[fill.InstrumentID] = make(map[derivativeFillOrderIdentity]struct{})
			}
			groups[fill.InstrumentID][identity] = struct{}{}
		}
	}
	if len(groups) == 0 {
		return nil
	}

	ids := make([]model.InstrumentID, 0, len(groups))
	for id := range groups {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })

	windowEnd := query.Until
	if windowEnd.IsZero() {
		windowEnd = mass.GeneratedAt
	}
	if windowEnd.IsZero() {
		windowEnd = c.clk.Now()
	}
	windowStart := windowEnd.Add(-derivativeOrderHistoryWindow)

	for _, id := range ids {
		inst, err := c.instrument(id)
		if err != nil {
			return err
		}
		category, err := categoryForInstrument(inst)
		if err != nil {
			return err
		}
		if category != "linear" {
			continue
		}
		target := orderReportTarget{category: category, symbol: inst.VenueSymbol, settle: inst.Settle}
		identities := groups[id]
		var semanticErr error
		saturated, err := c.rest.ScanOrderHistory(ctx, bybitsdk.GetOrderHistoryRequest{
			Category:    target.category,
			Symbol:      target.symbol,
			SettleCoin:  target.settle,
			StartMillis: windowStart.UnixMilli(),
			EndMillis:   windowEnd.UnixMilli(),
		}, derivativeOrderHistoryHydrationLimit, func(records []bybitsdk.OrderRecord) (bool, error) {
			for _, record := range records {
				if !bybitOrderRecordMatchesDerivativeFill(record, identities) {
					continue
				}
				resolvedID, ok := c.resolveOrderInstrument(target, id, record.Symbol)
				if !ok || resolvedID != id {
					semanticErr = fmt.Errorf("bybit: historical order identity matched fill for unexpected instrument category=%s settle=%s symbol=%s", target.category, target.settle, record.Symbol)
					return false, semanticErr
				}
				order, convertErr := orderFromBybitRecord(record, resolvedID, c.accountID)
				if convertErr != nil {
					semanticErr = convertErr
					return false, semanticErr
				}
				if !orderMatchesAnyDerivativeFillIdentity(order, identities) {
					continue
				}
				report := model.OrderStatusReport{Venue: VenueName, AccountID: c.accountID, Order: order, ReportedAt: mass.GeneratedAt}
				if _, exists := mass.OrderReports[report.Key()]; exists {
					continue
				}
				if addErr := mass.AddOrderReport(report); addErr != nil {
					semanticErr = addErr
					return false, semanticErr
				}
				for identity := range identities {
					if derivativeFillIdentityMatchesOrder(identity, order) {
						delete(identities, identity)
					}
				}
			}
			return len(identities) == 0, nil
		})
		if semanticErr != nil {
			return semanticErr
		}
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			mass.Warnings = append(mass.Warnings, model.ReportWarning{
				Code:    "DERIVATIVE_ORDER_HISTORY_HYDRATION_UNAVAILABLE",
				Message: fmt.Sprintf("Bybit derivative order-history hydration was unavailable for %s; exact-order fallback remains required: %v", id, err),
			})
			continue
		}
		if saturated && len(identities) > 0 {
			mass.Warnings = append(mass.Warnings, model.ReportWarning{
				Code:    "DERIVATIVE_ORDER_HISTORY_HYDRATION_LIMIT_REACHED",
				Message: fmt.Sprintf("Bybit derivative order-history hydration reached the %d-record bound for %s; exact-order fallback remains required", derivativeOrderHistoryHydrationLimit, id),
			})
		}
	}
	return nil
}

func massHasOrderMatchingDerivativeFill(mass *model.ExecutionMassStatus, id model.InstrumentID, identity derivativeFillOrderIdentity) bool {
	for _, report := range mass.OrderReports {
		if report.Order.Request.InstrumentID == id && derivativeFillIdentityMatchesOrder(identity, report.Order) {
			return true
		}
	}
	return false
}

func bybitOrderRecordMatchesDerivativeFill(record bybitsdk.OrderRecord, identities map[derivativeFillOrderIdentity]struct{}) bool {
	for identity := range identities {
		if derivativeFillIdentityMatchesAliases(identity, record.OrderID, record.OrderLinkID) {
			return true
		}
	}
	return false
}

func orderMatchesAnyDerivativeFillIdentity(order model.Order, identities map[derivativeFillOrderIdentity]struct{}) bool {
	for identity := range identities {
		if derivativeFillIdentityMatchesOrder(identity, order) {
			return true
		}
	}
	return false
}

func derivativeFillIdentityMatchesOrder(identity derivativeFillOrderIdentity, order model.Order) bool {
	return derivativeFillIdentityMatchesAliases(identity, order.VenueOrderID, order.Request.ClientID)
}

func derivativeFillIdentityMatchesAliases(identity derivativeFillOrderIdentity, venueOrderID, clientID string) bool {
	if identity.venueOrderID != "" && identity.venueOrderID != venueOrderID {
		return false
	}
	if identity.clientID != "" && identity.clientID != clientID {
		return false
	}
	return identity.venueOrderID != "" || identity.clientID != ""
}

func (c *executionClient) resolveFillInstrument(category string, scoped model.InstrumentID, venueSymbol string) (model.InstrumentID, bool) {
	if scoped.Symbol != "" {
		inst, ok := c.provider.Instrument(scoped)
		if !ok || inst.VenueSymbol != venueSymbol {
			return model.InstrumentID{}, false
		}
		return scoped, true
	}
	switch category {
	case "spot":
		return c.provider.ResolveVenueInstrument(venueSymbol, enums.KindSpot, "")
	case "linear":
		return c.provider.ResolveVenueInstrument(venueSymbol, enums.KindPerp, "")
	default:
		return model.InstrumentID{}, false
	}
}

type orderReportTarget struct {
	category string
	symbol   string
	settle   string
}

func (c *executionClient) orderRecords(ctx context.Context, target orderReportTarget, query model.OrderStatusReportQuery) ([]bybitsdk.OrderRecord, error) {
	if query.VenueOrderID != "" || query.ClientID != "" {
		records, err := c.rest.GetRealtimeOrders(ctx, target.category, target.symbol, target.settle, query.VenueOrderID, query.ClientID, 0)
		if err != nil || len(records) != 0 {
			return records, err
		}
		return c.rest.GetOrderHistoryFilteredScoped(ctx, target.category, target.symbol, target.settle, query.VenueOrderID, query.ClientID)
	}
	return c.rest.GetRealtimeOrders(ctx, target.category, target.symbol, target.settle, "", "", 0)
}

func (c *executionClient) orderReportTargets(query model.OrderStatusReportQuery) ([]orderReportTarget, error) {
	id := query.InstrumentID
	if id.Symbol == "" {
		exactIdentity := query.VenueOrderID != "" || query.ClientID != ""
		var targets []orderReportTarget
		for _, category := range c.categories {
			switch category {
			case "spot":
				targets = append(targets, orderReportTarget{category: category})
			case "linear":
				if exactIdentity {
					targets = append(targets, orderReportTarget{category: category})
				} else {
					targets = append(targets,
						orderReportTarget{category: category, settle: bybitsdk.SettleCoinUSDT},
						orderReportTarget{category: category, settle: bybitsdk.SettleCoinUSDC},
					)
				}
			}
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
		return nil, fmt.Errorf("bybit: instrument category %s is outside the configured scope", category)
	}
	settle := ""
	if category == "linear" {
		settle = inst.Settle
	}
	return []orderReportTarget{{category: category, symbol: inst.VenueSymbol, settle: settle}}, nil
}

func (c *executionClient) resolveOrderInstrument(target orderReportTarget, scoped model.InstrumentID, venueSymbol string) (model.InstrumentID, bool) {
	if scoped.Symbol != "" {
		inst, ok := c.provider.Instrument(scoped)
		if !ok || inst.VenueSymbol != venueSymbol {
			return model.InstrumentID{}, false
		}
		return scoped, true
	}
	if target.category == "spot" {
		return c.provider.ResolveVenueInstrument(venueSymbol, enums.KindSpot, "")
	}
	if target.category == "linear" {
		return c.provider.ResolveVenueInstrument(venueSymbol, enums.KindPerp, target.settle)
	}
	return model.InstrumentID{}, false
}

func (c *executionClient) reportTargets(id model.InstrumentID) ([]string, string, error) {
	if id.Symbol == "" {
		return append([]string(nil), c.categories...), "", nil
	}
	inst, err := c.instrument(id)
	if err != nil {
		return nil, "", err
	}
	category, err := categoryForInstrument(inst)
	if err != nil {
		return nil, "", err
	}
	if !c.supportsCategory(category) {
		return nil, "", fmt.Errorf("bybit: instrument category %s is outside the configured scope", category)
	}
	return []string{category}, inst.VenueSymbol, nil
}

func (c *executionClient) Capabilities() contract.Capabilities {
	products := make([]contract.ProductCapability, 0, len(c.categories))
	for _, category := range c.categories {
		switch category {
		case "spot":
			products = append(products, contract.ProductCapability{Kind: enums.KindSpot, Trading: true})
		case "linear":
			products = append(products, contract.ProductCapability{Kind: enums.KindPerp, Trading: true})
		}
	}
	return contract.Capabilities{
		Venue:    VenueName,
		Products: products,
		Reports: contract.ReportCapabilities{
			SingleOrderStatus:         true,
			OpenOrders:                true,
			OrderHistory:              true,
			FillHistory:               true,
			PositionReports:           c.supportsCategory("linear"),
			OpenOnlyNotFoundAmbiguous: true,
		},
		Streaming: contract.StreamCapabilities{Execution: true},
		Trading:   contract.TradingCapabilities{Submit: true, Cancel: true, CancelAll: true, Modify: true},
	}
}

func (c *executionClient) Events() <-chan contract.ExecEnvelope { return c.stream.C() }

func (c *executionClient) emit(ev contract.ExecEvent) {
	c.stream.Emit(contract.NewExecEnvelope(ev))
}

func (c *executionClient) Close() error {
	c.stream.Close()
	return nil
}

func decimalStringOrEmpty(value decimal.Decimal) string {
	if value.IsZero() {
		return ""
	}
	return value.String()
}
