package gate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/errs"
	"github.com/QuantProcessing/boltertrader/internal/wsstream"
	gatesdk "github.com/QuantProcessing/boltertrader/sdk/gate"
	"github.com/shopspring/decimal"
)

const (
	executionMassStatusFillLimit = 100
	gateOpenOrdersPageLimit      = 100
)

type executionClient struct {
	rest        *gatesdk.Client
	provider    *instrumentProvider
	clk         clock.Clock
	accountID   string
	scope       []enums.InstrumentKind
	stream      *wsstream.Stream[contract.ExecEnvelope]
	futuresMode *futuresPositionModeState
}

func newExecutionClient(rest *gatesdk.Client, provider *instrumentProvider, clk clock.Clock, accountIDs ...string) *executionClient {
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
	return &executionClient{rest: rest, provider: provider, clk: clk, accountID: accountID, scope: gateTradingKinds(), stream: wsstream.New[contract.ExecEnvelope](256), futuresMode: newFuturesPositionModeState()}
}

func (c *executionClient) futuresOrderPositionSide(order gatesdk.FuturesOrder) enums.PositionSide {
	positionSide, _ := c.futuresMode.orderPositionSide(order)
	return positionSide
}

func (c *executionClient) ensureFuturesPositionMode(ctx context.Context) error {
	if c.futuresMode.current() != "" {
		return nil
	}
	return c.refreshFuturesPositionMode(ctx)
}

func (c *executionClient) refreshFuturesPositionMode(ctx context.Context) error {
	if c.rest == nil {
		return fmt.Errorf("gate: futures account position mode is unavailable")
	}
	account, err := c.rest.GetFuturesAccount(ctx, gatesdk.SettleUSDT)
	if err != nil {
		return fmt.Errorf("gate: futures account position mode: %w", err)
	}
	return c.futuresMode.setAccount(account)
}

func (c *executionClient) resolveFuturesOrderPositionSide(order gatesdk.FuturesOrder) (enums.PositionSide, bool) {
	return c.futuresMode.orderPositionSide(order)
}

func (c *executionClient) validateFuturesPositionSide(req model.OrderRequest, order gatesdk.FuturesOrder) error {
	mode := c.futuresMode.current()
	if mode == "" {
		return nil
	}
	want, ok := positionSideFromGateOrder(order, mode)
	if !ok {
		return fmt.Errorf("gate: futures %s account could not resolve order position side: %w", mode, errs.ErrNotSupported)
	}
	if req.PositionSide != want {
		return fmt.Errorf("gate: futures %s account requires position side %s for side=%s reduce_only=%t, got %s: %w", mode, want, req.Side, req.ReduceOnly, req.PositionSide, errs.ErrNotSupported)
	}
	return nil
}

func (c *executionClient) AccountID() string { return c.accountID }

func (c *executionClient) withScope(scope []enums.InstrumentKind) *executionClient {
	c.scope = gateKinds(scope)
	return c
}

func (c *executionClient) instrument(id model.InstrumentID) (*model.Instrument, error) {
	inst, ok := c.provider.Instrument(id)
	if !ok {
		return nil, fmt.Errorf("gate: unknown instrument %s: %w", id, errs.ErrSymbolNotFound)
	}
	return inst, nil
}

func (c *executionClient) ValidateSubmit(req model.OrderRequest) error {
	inst, err := c.instrument(req.InstrumentID)
	if err != nil {
		return err
	}
	product, _, err := productForInstrument(inst)
	if err != nil {
		return err
	}
	switch product {
	case gatesdk.ProductSpot:
		_, err = orderRequestToGateSpot(req, inst)
	case gatesdk.ProductFuturesUSDT:
		if c.futuresMode.current() == "" {
			return fmt.Errorf("gate: futures account position mode is unavailable: %w", errs.ErrNotSupported)
		}
		var order gatesdk.FuturesOrder
		order, err = orderRequestToGateFutures(req, inst)
		if err == nil {
			err = c.validateFuturesPositionSide(req, order)
		}
	default:
		err = unsupportedProduct(product)
	}
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
	product, _, err := productForInstrument(inst)
	if err != nil {
		return nil, err
	}
	switch product {
	case gatesdk.ProductSpot:
		venueReq, err := orderRequestToGateSpot(req, inst)
		if err != nil {
			return nil, err
		}
		resp, err := c.rest.CreateSpotOrder(ctx, venueReq)
		if err != nil {
			return nil, gateCommandError("submit spot order", err)
		}
		order := orderFromGateSpotAction(resp, req, c.clk.Now())
		return &order, nil
	case gatesdk.ProductFuturesUSDT:
		if err := c.refreshFuturesPositionMode(ctx); err != nil {
			return nil, err
		}
		venueReq, err := orderRequestToGateFutures(req, inst)
		if err != nil {
			return nil, err
		}
		if err := c.validateFuturesPositionSide(req, venueReq); err != nil {
			return nil, err
		}
		resp, err := c.rest.CreateFuturesOrder(ctx, gatesdk.SettleUSDT, venueReq)
		if err != nil {
			return nil, gateCommandError("submit futures order", err)
		}
		order := orderFromGateFuturesAction(resp, req, c.clk.Now())
		return &order, nil
	default:
		return nil, unsupportedProduct(product)
	}
}

func (c *executionClient) Cancel(ctx context.Context, id model.InstrumentID, venueOrderID string) error {
	inst, err := c.instrument(id)
	if err != nil {
		return err
	}
	if inst.ID.Kind != enums.KindSpot {
		if inst.ID.Kind == enums.KindPerp && inst.Settle == "USDT" {
			if err := c.ensureFuturesPositionMode(ctx); err != nil {
				return err
			}
			orderID, parseErr := parseGateOrderID(venueOrderID)
			if parseErr != nil {
				return parseErr
			}
			resp, err := c.rest.CancelFuturesOrder(ctx, gatesdk.SettleUSDT, orderID)
			if err == nil && resp != nil {
				c.emit(contract.OrderEvent{Order: orderFromGateFuturesRESTRecord(*resp, inst.ID, c.accountID, c.futuresOrderPositionSide(*resp))})
			}
			return gateCommandError("cancel futures order", err)
		}
		return fmt.Errorf("gate: execution client cannot cancel %s: %w", inst.ID.Kind, errs.ErrNotSupported)
	}
	resp, err := c.rest.CancelSpotOrder(ctx, venueOrderID, inst.VenueSymbol)
	if err == nil && resp != nil {
		c.emit(contract.OrderEvent{Order: orderFromGateSpotRecord(*resp, inst.ID, c.accountID)})
	}
	return gateCommandError("cancel spot order", err)
}

func gateCommandError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if !gatesdk.IsDefinitiveCommandRejection(err) {
		return err
	}
	return fmt.Errorf("gate: %s rejected: %w", operation, errors.Join(contract.ErrVenueRejected, err))
}

func (c *executionClient) CancelAll(ctx context.Context, id model.InstrumentID) error {
	_ = ctx
	_ = id
	return fmt.Errorf("gate spot: cancel-all is not phase-one supported: %w", errs.ErrNotSupported)
}

func (c *executionClient) Modify(ctx context.Context, id model.InstrumentID, venueOrderID string, newPrice, newQty decimal.Decimal) (*model.Order, error) {
	_ = ctx
	_ = id
	_ = venueOrderID
	_ = newPrice
	_ = newQty
	return nil, fmt.Errorf("gate spot: modify is not phase-one supported: %w", errs.ErrNotSupported)
}

func (c *executionClient) OpenOrders(ctx context.Context, id model.InstrumentID) ([]model.Order, error) {
	inst, err := c.instrument(id)
	if err != nil {
		return nil, err
	}
	if inst.ID.Kind != enums.KindSpot {
		if inst.ID.Kind == enums.KindPerp && inst.Settle == "USDT" {
			if err := c.ensureFuturesPositionMode(ctx); err != nil {
				return nil, err
			}
			records, err := c.rest.ListFuturesOpenOrders(ctx, gatesdk.SettleUSDT, inst.VenueSymbol)
			if err != nil {
				return nil, err
			}
			out := make([]model.Order, 0, len(records))
			for _, record := range records {
				out = append(out, orderFromGateFuturesRESTRecord(record, id, c.accountID, c.futuresOrderPositionSide(record)))
			}
			return out, nil
		}
		return nil, fmt.Errorf("gate: execution client cannot list %s open orders: %w", inst.ID.Kind, errs.ErrNotSupported)
	}
	records, err := c.rest.ListSpotOpenOrders(ctx, inst.VenueSymbol)
	if err != nil {
		return nil, err
	}
	out := make([]model.Order, 0, len(records))
	for _, record := range records {
		out = append(out, orderFromGateSpotRecord(record, id, c.accountID))
	}
	return out, nil
}

func (c *executionClient) GenerateOrderStatusReports(ctx context.Context, query model.OrderStatusReportQuery) ([]model.OrderStatusReport, error) {
	if query.AccountID != "" && query.AccountID != c.accountID {
		return nil, nil
	}
	product, symbol, err := c.productAndSymbol(query.InstrumentID)
	if err != nil {
		return nil, err
	}
	now := c.clk.Now()
	out := make([]model.OrderStatusReport, 0)
	if product == gatesdk.ProductFuturesUSDT {
		if err := c.ensureFuturesPositionMode(ctx); err != nil {
			return nil, err
		}
		records, err := c.rest.ListFuturesOpenOrders(ctx, gatesdk.SettleUSDT, symbol)
		if err != nil {
			return nil, err
		}
		for _, record := range records {
			id := c.provider.resolveReportInstrument(query.InstrumentID, record.Contract)
			order := orderFromGateFuturesRESTRecord(record, id, c.accountID, c.futuresOrderPositionSide(record))
			if model.OrderMatchesStatusQuery(order, query) {
				out = append(out, model.OrderStatusReport{Venue: VenueName, AccountID: c.accountID, Order: order, ReportedAt: now})
			}
		}
		return out, nil
	}
	records, err := c.rest.ListSpotOpenOrders(ctx, symbol)
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		id := c.provider.resolveReportInstrument(query.InstrumentID, record.CurrencyPair)
		order := orderFromGateSpotRecord(record, id, c.accountID)
		if model.OrderMatchesStatusQuery(order, query) {
			out = append(out, model.OrderStatusReport{Venue: VenueName, AccountID: c.accountID, Order: order, ReportedAt: now})
		}
	}
	return out, nil
}

func (c *executionClient) GenerateOrderStatusReport(ctx context.Context, query model.SingleOrderStatusQuery) (*model.OrderStatusReport, error) {
	if query.AccountID != "" && query.AccountID != c.accountID {
		return nil, nil
	}
	if query.ClientID == "" && query.VenueOrderID == "" {
		reports, err := c.GenerateOrderStatusReports(ctx, model.OrderStatusReportQuery{InstrumentID: query.InstrumentID, AccountID: query.AccountID})
		if err != nil || len(reports) == 0 {
			return nil, err
		}
		return &reports[0], nil
	}
	if query.ClientID != "" && query.VenueOrderID == "" {
		reports, err := c.GenerateOrderStatusReports(ctx, model.OrderStatusReportQuery{InstrumentID: query.InstrumentID, AccountID: query.AccountID, ClientID: query.ClientID, OpenOnly: true})
		if err != nil || len(reports) == 0 {
			return nil, err
		}
		return &reports[0], nil
	}
	product, symbol, err := c.productAndSymbol(query.InstrumentID)
	if err != nil {
		return nil, err
	}
	if product == gatesdk.ProductFuturesUSDT {
		if err := c.ensureFuturesPositionMode(ctx); err != nil {
			return nil, err
		}
		orderID, parseErr := parseGateOrderID(query.VenueOrderID)
		if parseErr != nil {
			return nil, parseErr
		}
		record, err := c.rest.GetFuturesOrder(ctx, gatesdk.SettleUSDT, orderID)
		if err != nil {
			return nil, err
		}
		id := c.provider.resolveReportInstrument(query.InstrumentID, record.Contract)
		order := orderFromGateFuturesRESTRecord(*record, id, c.accountID, c.futuresOrderPositionSide(*record))
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
	record, err := c.rest.GetSpotOrder(ctx, query.VenueOrderID, symbol)
	if err != nil {
		return nil, err
	}
	id := c.provider.resolveReportInstrument(query.InstrumentID, record.CurrencyPair)
	order := orderFromGateSpotRecord(*record, id, c.accountID)
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

func (c *executionClient) GenerateFillReports(ctx context.Context, query model.FillReportQuery) ([]model.FillReport, error) {
	if query.AccountID != "" && query.AccountID != c.accountID {
		return nil, nil
	}
	reports, _, err := c.generateFillReports(ctx, query)
	return reports, err
}

func (c *executionClient) generateFillReports(ctx context.Context, query model.FillReportQuery) ([]model.FillReport, bool, error) {
	product, symbol, err := c.productAndSymbol(query.InstrumentID)
	if err != nil {
		return nil, false, err
	}
	if product == gatesdk.ProductFuturesUSDT {
		return c.generateFuturesFillReports(ctx, query, symbol)
	}
	return c.generateSpotFillReports(ctx, query, symbol)
}

func (c *executionClient) generateSpotFillReports(ctx context.Context, query model.FillReportQuery, symbol string) ([]model.FillReport, bool, error) {
	limit := firstPositiveIntInt(query.Limit, executionMassStatusFillLimit)
	records, err := c.rest.ListSpotMyTrades(ctx, symbol, query.VenueOrderID, limit)
	if err != nil {
		return nil, false, err
	}
	limitReached := len(records) >= limit
	now := c.clk.Now()
	out := make([]model.FillReport, 0, len(records))
	for _, record := range records {
		id := query.InstrumentID
		if id.Symbol == "" {
			var ok bool
			id, ok = c.provider.ResolveVenueInstrument(record.CurrencyPair, enums.KindSpot, "")
			if !ok {
				continue
			}
		} else {
			id = c.provider.resolveReportInstrument(id, record.CurrencyPair)
		}
		fill := fillFromGateSpotTrade(record, id, c.accountID)
		if !model.FillMatchesReportQuery(fill, query) {
			continue
		}
		if !fillWithinTimeWindow(fill.Timestamp, query.Since, query.Until) {
			continue
		}
		out = append(out, model.FillReport{Venue: VenueName, AccountID: c.accountID, Fill: fill, ReportedAt: now})
	}
	return out, limitReached, nil
}

func (c *executionClient) generateFuturesFillReports(ctx context.Context, query model.FillReportQuery, symbol string) ([]model.FillReport, bool, error) {
	limit := firstPositiveIntInt(query.Limit, executionMassStatusFillLimit)
	records, err := c.rest.ListMyFuturesTrades(ctx, gatesdk.SettleUSDT, symbol, limit)
	if err != nil {
		return nil, false, err
	}
	limitReached := len(records) >= limit
	now := c.clk.Now()
	out := make([]model.FillReport, 0, len(records))
	for _, record := range records {
		id := query.InstrumentID
		if id.Symbol == "" {
			var ok bool
			id, ok = c.provider.ResolveVenueInstrument(record.Contract, enums.KindPerp, gatesdk.SettleUSDT)
			if !ok {
				continue
			}
		} else {
			id = c.provider.resolveReportInstrument(id, record.Contract)
		}
		fill := fillFromGateFuturesTrade(record, id, c.accountID)
		if !model.FillMatchesReportQuery(fill, query) {
			continue
		}
		if !fillWithinTimeWindow(fill.Timestamp, query.Since, query.Until) {
			continue
		}
		out = append(out, model.FillReport{Venue: VenueName, AccountID: c.accountID, Fill: fill, ReportedAt: now})
	}
	return out, limitReached, nil
}

func (c *executionClient) GeneratePositionReports(ctx context.Context, query model.PositionReportQuery) ([]model.PositionReport, error) {
	if query.AccountID != "" && query.AccountID != c.accountID {
		return nil, nil
	}
	if !hasKind(c.scope, enums.KindPerp) {
		return nil, fmt.Errorf("gate: position reports require USDT futures scope: %w", errs.ErrNotSupported)
	}
	records, err := c.rest.ListPositions(ctx, gatesdk.SettleUSDT, true)
	if err != nil {
		return nil, err
	}
	now := c.clk.Now()
	out := make([]model.PositionReport, 0, len(records))
	for _, record := range records {
		id := c.provider.resolveReportInstrument(query.InstrumentID, record.Contract)
		pos := positionFromGate(record, func(string) model.InstrumentID { return id }, c.accountID, now)
		if query.InstrumentID.Symbol != "" && pos.InstrumentID != query.InstrumentID {
			continue
		}
		if pos.InstrumentID.Symbol == "" || pos.Quantity.IsZero() {
			continue
		}
		out = append(out, model.PositionReport{Venue: VenueName, AccountID: c.accountID, Position: pos, ReportedAt: now})
	}
	return out, nil
}

func (c *executionClient) GenerateExecutionMassStatus(ctx context.Context, query model.MassStatusQuery) (*model.ExecutionMassStatus, error) {
	if query.AccountID != "" && query.AccountID != c.accountID {
		return nil, fmt.Errorf("gate: mass status account %q does not match execution account %q", query.AccountID, c.accountID)
	}
	frozen, selector, err := c.freezeMassStatusScope(query)
	if err != nil {
		return nil, err
	}
	selectorSet := instrumentIDSet(selector)
	openSelector := selectorForKinds(selector, enums.KindSpot, enums.KindPerp)
	fillSelector := append([]model.InstrumentID{}, openSelector...)
	positionSelector := selectorForKinds(selector, enums.KindPerp)
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
	openAttempts := 0
	openSuccesses := 0
	openFailures := 0
	openIncomplete := false
	if selectorHasKind(openSelector, enums.KindSpot) {
		openAttempts++
		groups, err := frozen.rest.ListAllSpotOpenOrders(ctx, 1, gateOpenOrdersPageLimit)
		if err != nil {
			openFailures++
			mass.Warnings = append(mass.Warnings, model.ReportWarning{Code: "OPEN_ORDERS_UNAVAILABLE", Message: err.Error()})
		} else {
			openSuccesses++
			if len(groups) >= gateOpenOrdersPageLimit {
				openIncomplete = true
				mass.Warnings = append(mass.Warnings, model.ReportWarning{Code: "OPEN_ORDERS_LIMIT_REACHED", Message: "spot open-order query reached the 100-group limit"})
			}
			for _, group := range groups {
				for _, record := range group.Orders {
					if record.CurrencyPair == "" {
						record.CurrencyPair = group.CurrencyPair
					}
					id := frozen.provider.resolveReportInstrument(model.InstrumentID{}, record.CurrencyPair)
					if _, ok := selectorSet[id.String()]; !ok {
						continue
					}
					order := orderFromGateSpotRecord(record, id, c.accountID)
					if !model.OrderMatchesStatusQuery(order, model.OrderStatusReportQuery{AccountID: c.accountID, ClientID: query.ClientID, OpenOnly: true}) {
						continue
					}
					if err := mass.AddOrderReport(model.OrderStatusReport{Venue: VenueName, AccountID: c.accountID, Order: order, ReportedAt: frozen.clk.Now()}); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	if selectorHasKind(openSelector, enums.KindPerp) {
		openAttempts++
		if err := frozen.ensureFuturesPositionMode(ctx); err != nil {
			openFailures++
			mass.Warnings = append(mass.Warnings, model.ReportWarning{Code: "OPEN_ORDERS_UNAVAILABLE", Message: err.Error()})
		} else {
			records, err := frozen.rest.ListFuturesOpenOrders(ctx, gatesdk.SettleUSDT, "")
			if err != nil {
				openFailures++
				mass.Warnings = append(mass.Warnings, model.ReportWarning{Code: "OPEN_ORDERS_UNAVAILABLE", Message: err.Error()})
			} else {
				openSuccesses++
				if len(records) >= gateOpenOrdersPageLimit {
					openIncomplete = true
					mass.Warnings = append(mass.Warnings, model.ReportWarning{Code: "OPEN_ORDERS_LIMIT_REACHED", Message: "futures open-order query reached the 100-order limit"})
				}
				for _, record := range records {
					id := frozen.provider.resolveReportInstrument(model.InstrumentID{}, record.Contract)
					if _, ok := selectorSet[id.String()]; !ok {
						continue
					}
					order := orderFromGateFuturesRESTRecord(record, id, c.accountID, frozen.futuresOrderPositionSide(record))
					if !model.OrderMatchesStatusQuery(order, model.OrderStatusReportQuery{AccountID: c.accountID, ClientID: query.ClientID, OpenOnly: true}) {
						continue
					}
					if err := mass.AddOrderReport(model.OrderStatusReport{Venue: VenueName, AccountID: c.accountID, Order: order, ReportedAt: frozen.clk.Now()}); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	mass.OpenOrdersCoverage = model.NewSnapshotCoverage(gateMassStatusCoverageState(openAttempts, openSuccesses, openFailures, openIncomplete), c.accountID, query.ClientID, openSelector, openStartedAt)
	if query.IncludeFills && frozen.Capabilities().Reports.FillHistory {
		fillThrough := query.Until
		if fillThrough.IsZero() {
			fillThrough = frozen.clk.Now()
		}
		fillFrom := query.Since
		if fillFrom.IsZero() && query.Lookback > 0 && !query.Until.IsZero() {
			fillFrom = query.Until.Add(-query.Lookback)
		}
		limitReached := false
		fillQuery := model.FillReportQuery{
			AccountID: c.accountID,
			ClientID:  query.ClientID,
			Since:     fillFrom,
			Until:     fillThrough,
			Limit:     executionMassStatusFillLimit,
		}
		addReports := func(fills []model.FillReport, reached bool) error {
			limitReached = limitReached || reached
			for _, report := range fills {
				if err := mass.AddFillReport(report); err != nil {
					return err
				}
			}
			return nil
		}
		fillAttempts := 0
		fillSuccesses := 0
		fillFailures := 0
		if selectorHasKind(fillSelector, enums.KindSpot) {
			fillAttempts++
			fills, reached, err := frozen.generateSpotFillReports(ctx, fillQuery, "")
			if err != nil {
				fillFailures++
				mass.Warnings = append(mass.Warnings, model.ReportWarning{Code: "FILL_REPORTS_UNAVAILABLE", Message: err.Error()})
			} else {
				fillSuccesses++
				fills = filterGateFillReports(fills, selectorSet)
				if err := addReports(fills, reached); err != nil {
					return nil, err
				}
			}
		}
		if selectorHasKind(fillSelector, enums.KindPerp) {
			fillAttempts++
			fills, reached, err := frozen.generateFuturesFillReports(ctx, fillQuery, "")
			if err != nil {
				fillFailures++
				mass.Warnings = append(mass.Warnings, model.ReportWarning{Code: "FILL_REPORTS_UNAVAILABLE", Message: err.Error()})
			} else {
				fillSuccesses++
				fills = filterGateFillReports(fills, selectorSet)
				if err := addReports(fills, reached); err != nil {
					return nil, err
				}
			}
		}
		if limitReached {
			mass.Warnings = append(mass.Warnings, model.ReportWarning{
				Code:    "FILL_REPORTS_LIMIT_REACHED",
				Message: "one or more fill-history queries reached the 100-record limit; recovered fills may be incomplete",
			})
		}
		fillState := gateMassStatusCoverageState(fillAttempts, fillSuccesses, fillFailures, limitReached)
		mass.FillsCoverage = model.NewFillCoverage(fillState, c.accountID, query.ClientID, fillSelector, fillFrom, fillThrough)
	} else if query.IncludeFills {
		mass.FillsCoverage = model.ReportCoverage{State: model.CoverageUnavailable}
	}
	if query.IncludePositions {
		positionsStartedAt := frozen.clk.Now()
		if len(positionSelector) == 0 {
			mass.PositionsCoverage = model.NewSnapshotCoverage(model.CoverageComplete, c.accountID, query.ClientID, positionSelector, positionsStartedAt)
		} else if !hasKind(frozen.scope, enums.KindPerp) {
			mass.PositionsCoverage = model.ReportCoverage{State: model.CoverageUnavailable}
		} else {
			positions, err := frozen.GeneratePositionReports(ctx, model.PositionReportQuery{AccountID: c.accountID})
			if err != nil {
				mass.PositionsCoverage = model.NewSnapshotCoverage(model.CoverageUnavailable, c.accountID, query.ClientID, positionSelector, positionsStartedAt)
				mass.Warnings = append(mass.Warnings, model.ReportWarning{Code: "POSITIONS_UNAVAILABLE", Message: err.Error()})
			} else {
				for _, report := range positions {
					if _, ok := selectorSet[report.Position.InstrumentID.String()]; !ok {
						continue
					}
					if err := mass.AddPositionReport(report); err != nil {
						return nil, err
					}
				}
				mass.PositionsCoverage = model.NewSnapshotCoverage(model.CoverageComplete, c.accountID, query.ClientID, positionSelector, positionsStartedAt)
			}
		}
	}
	if err := mass.ValidateFor(query); err != nil {
		return nil, err
	}
	return mass, nil
}

func (c *executionClient) freezeMassStatusScope(query model.MassStatusQuery) (*executionClient, []model.InstrumentID, error) {
	if venue := strings.TrimSpace(query.Venue); venue != "" && venue != VenueName {
		return nil, nil, fmt.Errorf("gate: mass status venue %q does not match %q", query.Venue, VenueName)
	}
	if c.provider == nil {
		return nil, nil, fmt.Errorf("gate: instrument provider required for mass status")
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
	c.provider.mu.RUnlock()
	snapshot := newInstrumentProvider()
	snapshot.LoadSnapshot(instruments)
	frozen := *c
	frozen.provider = snapshot
	frozen.scope = append([]enums.InstrumentKind(nil), c.scope...)
	selector, err := frozen.massStatusSelector(query.InstrumentIDs)
	if err != nil {
		return nil, nil, err
	}
	return &frozen, selector, nil
}

func gateMassStatusCoverageState(attempts, successes, failures int, incomplete bool) model.CoverageState {
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
			if id.Venue != VenueName || !hasKind(c.scope, id.Kind) {
				return nil, fmt.Errorf("gate: mass status instrument %s is outside execution scope", id)
			}
			if _, ok := c.provider.Instrument(id); !ok {
				return nil, fmt.Errorf("gate: unknown mass status instrument %s: %w", id, errs.ErrSymbolNotFound)
			}
		}
		return selector, nil
	}
	all := c.provider.All()
	selector := make([]model.InstrumentID, 0, len(all))
	for _, inst := range all {
		if inst != nil && inst.ID.Venue == VenueName && hasKind(c.scope, inst.ID.Kind) {
			selector = append(selector, inst.ID)
		}
	}
	return model.NormalizeInstrumentIDs(selector), nil
}

func selectorForKinds(ids []model.InstrumentID, kinds ...enums.InstrumentKind) []model.InstrumentID {
	out := make([]model.InstrumentID, 0, len(ids))
	for _, id := range ids {
		if hasKind(kinds, id.Kind) {
			out = append(out, id)
		}
	}
	return model.NormalizeInstrumentIDs(out)
}

func selectorHasKind(ids []model.InstrumentID, kind enums.InstrumentKind) bool {
	for _, id := range ids {
		if id.Kind == kind {
			return true
		}
	}
	return false
}

func instrumentIDSet(ids []model.InstrumentID) map[string]struct{} {
	out := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		out[id.String()] = struct{}{}
	}
	return out
}

func filterGateFillReports(reports []model.FillReport, selector map[string]struct{}) []model.FillReport {
	out := reports[:0]
	for _, report := range reports {
		if _, ok := selector[report.Fill.InstrumentID.String()]; ok {
			out = append(out, report)
		}
	}
	return out
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

func (c *executionClient) productAndSymbol(id model.InstrumentID) (string, string, error) {
	if id.Symbol == "" {
		return gatesdk.ProductSpot, "", nil
	}
	inst, err := c.instrument(id)
	if err != nil {
		return "", "", err
	}
	product, _, err := productForInstrument(inst)
	if err != nil {
		return "", "", err
	}
	return product, inst.VenueSymbol, nil
}

func (c *executionClient) Capabilities() contract.Capabilities {
	products := make([]contract.ProductCapability, 0, len(c.scope))
	for _, kind := range c.scope {
		products = append(products, contract.ProductCapability{Kind: kind, Trading: true})
	}
	return contract.Capabilities{
		Venue:     VenueName,
		Products:  products,
		Reports:   contract.ReportCapabilities{SingleOrderStatus: true, OpenOrders: true, OrderHistory: true, FillHistory: true, PositionReports: hasKind(c.scope, enums.KindPerp), OpenOnlyNotFoundAmbiguous: true},
		Streaming: contract.StreamCapabilities{Execution: true},
		Trading:   contract.TradingCapabilities{Submit: true, Cancel: true},
	}
}

func (c *executionClient) Events() <-chan contract.ExecEnvelope { return c.stream.C() }
func (c *executionClient) emit(ev contract.ExecEvent)           { c.stream.Emit(contract.NewExecEnvelope(ev)) }
func (c *executionClient) Close() error                         { c.stream.Close(); return nil }
