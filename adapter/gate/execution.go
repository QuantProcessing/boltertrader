package gate

import (
	"context"
	"fmt"
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

const executionMassStatusFillLimit = 100

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
			return nil, err
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
			return nil, err
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
				c.emit(contract.OrderEvent{Order: orderFromGateFuturesRecord(*resp, inst.ID, c.accountID, c.futuresOrderPositionSide(*resp))})
			}
			return err
		}
		return fmt.Errorf("gate: execution client cannot cancel %s: %w", inst.ID.Kind, errs.ErrNotSupported)
	}
	resp, err := c.rest.CancelSpotOrder(ctx, venueOrderID, inst.VenueSymbol)
	if err == nil && resp != nil {
		c.emit(contract.OrderEvent{Order: orderFromGateSpotRecord(*resp, inst.ID, c.accountID)})
	}
	return err
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
				out = append(out, orderFromGateFuturesRecord(record, id, c.accountID, c.futuresOrderPositionSide(record)))
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
			order := orderFromGateFuturesRecord(record, id, c.accountID, c.futuresOrderPositionSide(record))
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
		order := orderFromGateFuturesRecord(*record, id, c.accountID, c.futuresOrderPositionSide(*record))
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
		return model.NewExecutionMassStatus(VenueName, query.AccountID, c.clk.Now()), nil
	}
	mass := model.NewExecutionMassStatus(VenueName, c.accountID, c.clk.Now())
	mass.ClientID = query.ClientID
	mass.Lookback = query.Lookback
	mass.Partial = true
	if hasKind(c.scope, enums.KindSpot) {
		groups, err := c.rest.ListAllSpotOpenOrders(ctx, 1, 100)
		if err != nil {
			return nil, err
		}
		for _, group := range groups {
			for _, record := range group.Orders {
				if record.CurrencyPair == "" {
					record.CurrencyPair = group.CurrencyPair
				}
				id := c.provider.resolveReportInstrument(model.InstrumentID{}, record.CurrencyPair)
				order := orderFromGateSpotRecord(record, id, c.accountID)
				if !model.OrderMatchesStatusQuery(order, model.OrderStatusReportQuery{AccountID: c.accountID, ClientID: query.ClientID, OpenOnly: true}) {
					continue
				}
				if err := mass.AddOrderReport(model.OrderStatusReport{Venue: VenueName, AccountID: c.accountID, Order: order, ReportedAt: c.clk.Now()}); err != nil {
					return nil, err
				}
			}
		}
	}
	if hasKind(c.scope, enums.KindPerp) {
		if err := c.ensureFuturesPositionMode(ctx); err != nil {
			return nil, err
		}
		records, err := c.rest.ListFuturesOpenOrders(ctx, gatesdk.SettleUSDT, "")
		if err != nil {
			return nil, err
		}
		for _, record := range records {
			id := c.provider.resolveReportInstrument(model.InstrumentID{}, record.Contract)
			order := orderFromGateFuturesRecord(record, id, c.accountID, c.futuresOrderPositionSide(record))
			if !model.OrderMatchesStatusQuery(order, model.OrderStatusReportQuery{AccountID: c.accountID, ClientID: query.ClientID, OpenOnly: true}) {
				continue
			}
			if err := mass.AddOrderReport(model.OrderStatusReport{Venue: VenueName, AccountID: c.accountID, Order: order, ReportedAt: c.clk.Now()}); err != nil {
				return nil, err
			}
		}
	}
	if query.IncludeFills && c.Capabilities().Reports.FillHistory {
		limitReached := false
		fillQuery := model.FillReportQuery{
			AccountID: c.accountID,
			ClientID:  query.ClientID,
			Since:     query.Since,
			Until:     query.Until,
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
		if hasKind(c.scope, enums.KindSpot) {
			fills, reached, err := c.generateSpotFillReports(ctx, fillQuery, "")
			if err != nil {
				return nil, err
			}
			if err := addReports(fills, reached); err != nil {
				return nil, err
			}
		}
		if hasKind(c.scope, enums.KindPerp) {
			fills, reached, err := c.generateFuturesFillReports(ctx, fillQuery, "")
			if err != nil {
				return nil, err
			}
			if err := addReports(fills, reached); err != nil {
				return nil, err
			}
		}
		if limitReached {
			mass.Warnings = append(mass.Warnings, model.ReportWarning{
				Code:    "FILL_REPORTS_LIMIT_REACHED",
				Message: "one or more fill-history queries reached the 100-record limit; recovered fills may be incomplete",
			})
		}
	}
	return mass, nil
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
