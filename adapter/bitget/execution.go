package bitget

import (
	"context"
	"fmt"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/errs"
	"github.com/QuantProcessing/boltertrader/internal/wsstream"
	bitgetsdk "github.com/QuantProcessing/boltertrader/sdk/bitget"
	"github.com/shopspring/decimal"
)

type executionClient struct {
	rest      *bitgetsdk.Client
	provider  *instrumentProvider
	clk       clock.Clock
	accountID string
	stream    *wsstream.Stream[contract.ExecEnvelope]
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
	return &executionClient{rest: rest, provider: provider, clk: clk, accountID: accountID, stream: wsstream.New[contract.ExecEnvelope](256)}
}

func (c *executionClient) AccountID() string { return c.accountID }

func (c *executionClient) instrument(id model.InstrumentID) (*model.Instrument, error) {
	inst, ok := c.provider.Instrument(id)
	if !ok {
		return nil, fmt.Errorf("bitget: unknown instrument %s: %w", id, errs.ErrSymbolNotFound)
	}
	return inst, nil
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
		out = append(out, orderFromBitgetRecord(record, id, c.accountID))
	}
	return out, nil
}

func (c *executionClient) GenerateOrderStatusReports(ctx context.Context, query model.OrderStatusReportQuery) ([]model.OrderStatusReport, error) {
	if query.AccountID != "" && query.AccountID != c.accountID {
		return nil, nil
	}
	category, symbol, err := c.categoryAndSymbol(query.InstrumentID)
	if err != nil {
		return nil, err
	}
	records, err := c.rest.GetOpenOrders(ctx, category, symbol)
	if err != nil {
		return nil, err
	}
	now := c.clk.Now()
	out := make([]model.OrderStatusReport, 0, len(records))
	for _, record := range records {
		id := c.provider.resolveReportInstrument(query.InstrumentID, record.Symbol)
		order := orderFromBitgetRecord(record, id, c.accountID)
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
	if query.ClientID != "" || query.VenueOrderID != "" {
		category, symbol, err := c.categoryAndSymbol(query.InstrumentID)
		if err != nil {
			return nil, err
		}
		record, err := c.rest.GetOrder(ctx, category, symbol, query.VenueOrderID, query.ClientID)
		if err != nil {
			return nil, err
		}
		id := c.provider.resolveReportInstrument(query.InstrumentID, record.Symbol)
		order := orderFromBitgetRecord(*record, id, c.accountID)
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
	reports, err := c.GenerateOrderStatusReports(ctx, model.OrderStatusReportQuery{InstrumentID: query.InstrumentID, AccountID: query.AccountID})
	if err != nil || len(reports) == 0 {
		return nil, err
	}
	return &reports[0], nil
}

func (c *executionClient) GenerateFillReports(ctx context.Context, query model.FillReportQuery) ([]model.FillReport, error) {
	if query.AccountID != "" && query.AccountID != c.accountID {
		return nil, nil
	}
	category, _, err := c.categoryAndSymbol(query.InstrumentID)
	if err != nil {
		return nil, err
	}
	records, err := c.rest.GetFills(ctx, bitgetsdk.GetFillsRequest{Category: category, OrderID: query.VenueOrderID, Limit: "100"})
	if err != nil {
		return nil, err
	}
	now := c.clk.Now()
	out := make([]model.FillReport, 0, len(records))
	for _, record := range records {
		id := c.provider.resolveReportInstrument(query.InstrumentID, record.Symbol)
		fill := fillFromBitget(record, id, c.accountID)
		if !model.FillMatchesReportQuery(fill, query) {
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
	categories := []string{bitgetsdk.ProductTypeUSDTFutures, bitgetsdk.ProductTypeUSDCFutures}
	if query.InstrumentID.Symbol != "" {
		if inst, ok := c.provider.Instrument(query.InstrumentID); ok {
			cat, err := categoryForInstrument(inst)
			if err != nil {
				return nil, err
			}
			categories = []string{cat}
		}
	}
	now := c.clk.Now()
	out := make([]model.PositionReport, 0)
	for _, category := range categories {
		records, err := c.rest.GetCurrentPositions(ctx, category, "")
		if err != nil {
			return nil, err
		}
		for _, record := range records {
			id := c.provider.resolveReportInstrument(query.InstrumentID, record.Symbol)
			pos := positionFromBitget(record, func(string) model.InstrumentID { return id }, c.accountID, now)
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
	return mass, nil
}

func (c *executionClient) categoryAndSymbol(id model.InstrumentID) (string, string, error) {
	if id.Symbol == "" {
		return bitgetsdk.ProductTypeUSDTFutures, "", nil
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
		Reports:   contract.ReportCapabilities{OpenOrders: true, OrderHistory: true, FillHistory: true, PositionReports: true, OpenOnlyNotFoundAmbiguous: true},
		Streaming: contract.StreamCapabilities{Execution: true},
		Trading:   contract.TradingCapabilities{Submit: true, Cancel: true, CancelAll: true, Modify: true},
	}
}

func (c *executionClient) Events() <-chan contract.ExecEnvelope { return c.stream.C() }
func (c *executionClient) emit(ev contract.ExecEvent)           { c.stream.Emit(contract.NewExecEnvelope(ev)) }
func (c *executionClient) Close() error                         { c.stream.Close(); return nil }
