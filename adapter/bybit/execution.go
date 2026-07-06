package bybit

import (
	"context"
	"fmt"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/errs"
	"github.com/QuantProcessing/boltertrader/internal/wsstream"
	bybitsdk "github.com/QuantProcessing/boltertrader/sdk/bybit"
	"github.com/shopspring/decimal"
)

type executionClient struct {
	rest     *bybitsdk.Client
	provider *instrumentProvider
	clk      clock.Clock
	stream   *wsstream.Stream[contract.ExecEnvelope]
}

func newExecutionClient(rest *bybitsdk.Client, provider *instrumentProvider, clk clock.Clock) *executionClient {
	if clk == nil {
		clk = clock.NewRealClock()
	}
	return &executionClient{
		rest:     rest,
		provider: provider,
		clk:      clk,
		stream:   wsstream.New[contract.ExecEnvelope](256),
	}
}

func (c *executionClient) instrument(id model.InstrumentID) (*model.Instrument, error) {
	inst, ok := c.provider.Instrument(id)
	if !ok {
		return nil, fmt.Errorf("bybit: unknown instrument %s: %w", id, errs.ErrSymbolNotFound)
	}
	return inst, nil
}

func (c *executionClient) Submit(ctx context.Context, req model.OrderRequest) (*model.Order, error) {
	inst, err := c.instrument(req.InstrumentID)
	if err != nil {
		return nil, err
	}
	if req.AccountID == "" {
		req.AccountID = AccountIDUnified
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
	req := model.OrderRequest{AccountID: AccountIDUnified, InstrumentID: id, Quantity: newQty, Price: newPrice}
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
		out = append(out, orderFromBybitRecord(record, id, AccountIDUnified))
	}
	return out, nil
}

func (c *executionClient) GenerateOrderStatusReports(ctx context.Context, query model.OrderStatusReportQuery) ([]model.OrderStatusReport, error) {
	records, err := c.orderRecords(ctx, query)
	if err != nil {
		return nil, err
	}
	now := c.clk.Now()
	out := make([]model.OrderStatusReport, 0, len(records))
	for _, record := range records {
		id := c.provider.resolveReportInstrument(query.InstrumentID, record.Symbol)
		order := orderFromBybitRecord(record, id, AccountIDUnified)
		if !model.OrderMatchesStatusQuery(order, query) {
			continue
		}
		out = append(out, model.OrderStatusReport{Venue: VenueName, AccountID: AccountIDUnified, Order: order, ReportedAt: now})
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
	category, symbol, err := c.categoryAndSymbol(query.InstrumentID)
	if err != nil {
		return nil, err
	}
	records, err := c.rest.GetExecutions(ctx, category, symbol, query.VenueOrderID, query.ClientID)
	if err != nil {
		return nil, err
	}
	now := c.clk.Now()
	out := make([]model.FillReport, 0, len(records))
	for _, record := range records {
		id := c.provider.resolveReportInstrument(query.InstrumentID, record.Symbol)
		fill := fillFromBybitExecution(record, id, AccountIDUnified)
		if query.InstrumentID.Symbol != "" && fill.InstrumentID != query.InstrumentID {
			continue
		}
		out = append(out, model.FillReport{Venue: VenueName, AccountID: AccountIDUnified, Fill: fill, ReportedAt: now})
	}
	return out, nil
}

func (c *executionClient) GeneratePositionReports(ctx context.Context, query model.PositionReportQuery) ([]model.PositionReport, error) {
	settles := []string{bybitsdk.SettleCoinUSDT, bybitsdk.SettleCoinUSDC}
	if query.InstrumentID.Symbol != "" {
		if inst, ok := c.provider.Instrument(query.InstrumentID); ok && inst.Settle != "" {
			settles = []string{inst.Settle}
		}
	}
	now := c.clk.Now()
	out := make([]model.PositionReport, 0)
	for _, settle := range settles {
		records, err := c.rest.GetPositions(ctx, "linear", "", settle)
		if err != nil {
			return nil, err
		}
		for _, record := range records {
			id := c.provider.resolveReportInstrument(query.InstrumentID, record.Symbol)
			pos := positionFromBybit(record, func(string) model.InstrumentID { return id }, AccountIDUnified, now)
			if query.InstrumentID.Symbol != "" && pos.InstrumentID != query.InstrumentID {
				continue
			}
			out = append(out, model.PositionReport{Venue: VenueName, AccountID: AccountIDUnified, Position: pos, ReportedAt: now})
		}
	}
	return out, nil
}

func (c *executionClient) GenerateExecutionMassStatus(ctx context.Context, query model.MassStatusQuery) (*model.ExecutionMassStatus, error) {
	mass := model.NewExecutionMassStatus(VenueName, AccountIDUnified, c.clk.Now())
	mass.ClientID = query.ClientID
	mass.Lookback = query.Lookback
	mass.Partial = true
	for _, settle := range []string{bybitsdk.SettleCoinUSDT, bybitsdk.SettleCoinUSDC} {
		records, err := c.rest.GetRealtimeOrders(ctx, "linear", "", settle, "", query.ClientID, 0)
		if err != nil {
			return nil, err
		}
		for _, record := range records {
			id := c.provider.resolveVenueSymbol(record.Symbol)
			order := orderFromBybitRecord(record, id, AccountIDUnified)
			if !model.OrderMatchesStatusQuery(order, model.OrderStatusReportQuery{AccountID: AccountIDUnified, ClientID: query.ClientID, OpenOnly: true}) {
				continue
			}
			if err := mass.AddOrderReport(model.OrderStatusReport{Venue: VenueName, AccountID: AccountIDUnified, Order: order, ReportedAt: mass.GeneratedAt}); err != nil {
				return nil, err
			}
		}
	}
	mass.Warnings = append(mass.Warnings, model.ReportWarning{
		Code:    "bybit_spot_mass_status_symbol_scoped",
		Message: "Bybit V5 spot realtime-orders requires symbol/baseCoin; venue-wide mass status is linear-settlement scoped and spot orders remain covered by instrument-scoped OpenOrders.",
	})
	if query.IncludeFills {
		fills, err := c.GenerateFillReports(ctx, model.FillReportQuery{AccountID: AccountIDUnified, ClientID: query.ClientID})
		if err != nil {
			return nil, err
		}
		for _, report := range fills {
			if err := mass.AddFillReport(report); err != nil {
				return nil, err
			}
		}
	}
	if query.IncludePositions {
		positions, err := c.GeneratePositionReports(ctx, model.PositionReportQuery{AccountID: AccountIDUnified})
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

func (c *executionClient) orderRecords(ctx context.Context, query model.OrderStatusReportQuery) ([]bybitsdk.OrderRecord, error) {
	category, symbol, err := c.categoryAndSymbol(query.InstrumentID)
	if err != nil {
		return nil, err
	}
	if query.VenueOrderID != "" || query.ClientID != "" {
		records, err := c.rest.GetRealtimeOrders(ctx, category, symbol, "", query.VenueOrderID, query.ClientID, 0)
		if err != nil || len(records) != 0 {
			return records, err
		}
		return c.rest.GetOrderHistoryFiltered(ctx, category, symbol, query.VenueOrderID, query.ClientID)
	}
	return c.rest.GetRealtimeOrders(ctx, category, symbol, "", "", "", 0)
}

func (c *executionClient) categoryAndSymbol(id model.InstrumentID) (string, string, error) {
	if id.Symbol == "" {
		return "linear", "", nil
	}
	inst, err := c.instrument(id)
	if err != nil {
		return "", "", err
	}
	category, err := categoryForInstrument(inst)
	if err != nil {
		return "", "", err
	}
	return category, inst.VenueSymbol, nil
}

func (c *executionClient) Capabilities() contract.Capabilities {
	return contract.Capabilities{
		Venue: VenueName,
		Products: []contract.ProductCapability{
			{Kind: enums.KindSpot, Trading: true},
			{Kind: enums.KindPerp, Trading: true},
		},
		Reports: contract.ReportCapabilities{
			OpenOrders:                true,
			OrderHistory:              true,
			FillHistory:               true,
			PositionReports:           true,
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
