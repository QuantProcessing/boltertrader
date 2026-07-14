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
		return nil, err
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
	return c.rest.CancelAllOrders(ctx, bybitsdk.CancelAllOrdersRequest{Category: category, Symbol: inst.VenueSymbol})
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
		return nil, err
	}
	req := model.OrderRequest{AccountID: c.accountID, InstrumentID: id, Quantity: newQty, Price: newPrice}
	order := orderFromBybitAction(resp, req, c.clk.Now())
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
		records, err := c.rest.GetPositions(ctx, "linear", "", settle)
		if err != nil {
			return nil, err
		}
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
	orderTargets, err := c.orderReportTargets(model.OrderStatusReportQuery{ClientID: query.ClientID})
	if err != nil {
		return nil, err
	}
	for _, target := range orderTargets {
		records, err := c.rest.GetRealtimeOrders(ctx, target.category, target.symbol, target.settle, "", query.ClientID, 0)
		if err != nil {
			return nil, err
		}
		for _, record := range records {
			id, ok := c.resolveOrderInstrument(target, model.InstrumentID{}, record.Symbol)
			if !ok {
				return nil, fmt.Errorf("bybit: unknown open-order instrument category=%s settle=%s symbol=%s", target.category, target.settle, record.Symbol)
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
	if query.IncludeFills {
		limitReached := false
		for _, category := range c.categories {
			fills, reached, err := c.generateFillReportsForCategory(ctx, category, "", model.FillReportQuery{
				AccountID: c.accountID,
				ClientID:  query.ClientID,
				Since:     query.Since,
				Until:     query.Until,
				Limit:     executionMassStatusFillLimit,
			})
			if err != nil {
				return nil, err
			}
			limitReached = limitReached || reached
			for _, report := range fills {
				if err := mass.AddFillReport(report); err != nil {
					return nil, err
				}
			}
		}
		if limitReached {
			mass.Partial = true
			mass.Warnings = append(mass.Warnings, model.ReportWarning{
				Code:    "FILL_REPORTS_LIMIT_REACHED",
				Message: fmt.Sprintf("the Bybit execution-history query reached the %d-record limit; recovered fills may be incomplete", executionMassStatusFillLimit),
			})
		}
		if err := c.addHistoricalOrderReportsForDerivativeFills(ctx, mass, query); err != nil {
			return nil, err
		}
	}
	if query.IncludePositions {
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
