package spot

import (
	"context"
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
	sdkspot "github.com/QuantProcessing/boltertrader/sdk/aster/spot"
	"github.com/shopspring/decimal"
)

type executionClient struct {
	rest      *sdkspot.Client
	provider  *instrumentProvider
	clk       clock.Clock
	accountID string
	stream    *wsstream.Stream[contract.ExecEnvelope]
	streaming bool
}

func newExecutionClient(rest *sdkspot.Client, provider *instrumentProvider, clk clock.Clock, accountID string) *executionClient {
	if clk == nil {
		clk = clock.NewRealClock()
	}
	if accountID == "" {
		accountID = AccountIDDefault
	}
	return &executionClient{rest: rest, provider: provider, clk: clk, accountID: accountID, stream: wsstream.New[contract.ExecEnvelope](256)}
}

func (c *executionClient) AccountID() string { return c.accountID }

func (c *executionClient) Capabilities() contract.Capabilities {
	return contract.Capabilities{
		Venue:    VenueName,
		Products: []contract.ProductCapability{{Kind: enums.KindSpot, Trading: true}},
		Reports:  contract.ReportCapabilities{SingleOrderStatus: true, OpenOrders: true, FillHistory: true, OpenOnlyNotFoundAmbiguous: true},
		Streaming: contract.StreamCapabilities{
			Execution: c.streaming,
		},
		Trading: contract.TradingCapabilities{Submit: true, Cancel: true, CancelAll: true},
	}
}

func (c *executionClient) Submit(ctx context.Context, req model.OrderRequest) (*model.Order, error) {
	if err := c.ValidateSubmit(req); err != nil {
		return nil, err
	}
	inst, err := c.provider.instrument(req.InstrumentID)
	if err != nil {
		return nil, err
	}
	if req.AccountID == "" {
		req.AccountID = c.accountID
	}
	venueReq, err := orderRequestToAster(req, inst)
	if err != nil {
		return nil, err
	}
	if c.rest == nil {
		return nil, fmt.Errorf("aster spot: rest client not configured: %w", errs.ErrNotSupported)
	}
	resp, err := c.rest.PlaceOrder(ctx, venueReq)
	if err != nil {
		return nil, mapAsterError(err)
	}
	if err := validateOrderResponseDecimals(resp); err != nil {
		return nil, err
	}
	order := orderFromResponse(resp, req, c.accountID)
	order.CreatedAt = c.clk.Now()
	return &order, nil
}

func (c *executionClient) ValidateSubmit(req model.OrderRequest) error {
	if req.AccountID != "" && req.AccountID != c.accountID {
		return fmt.Errorf("aster spot: account id %q does not match adapter account %q", req.AccountID, c.accountID)
	}
	inst, err := c.provider.instrument(req.InstrumentID)
	if err != nil {
		return err
	}
	return validateOrderRequest(req, inst)
}

func (c *executionClient) Cancel(ctx context.Context, id model.InstrumentID, venueOrderID string) error {
	inst, err := c.provider.instrument(id)
	if err != nil {
		return err
	}
	if c.rest == nil {
		return fmt.Errorf("aster spot: rest client not configured: %w", errs.ErrNotSupported)
	}
	orderID, err := strconv.ParseInt(venueOrderID, 10, 64)
	if err != nil {
		return fmt.Errorf("aster spot: parse order id %q: %w", venueOrderID, err)
	}
	_, err = c.rest.CancelOrder(ctx, sdkspot.CancelOrderParams{Symbol: inst.VenueSymbol, OrderID: &orderID})
	return mapAsterError(err)
}

func (c *executionClient) CancelAll(ctx context.Context, id model.InstrumentID) error {
	inst, err := c.provider.instrument(id)
	if err != nil {
		return err
	}
	if c.rest == nil {
		return fmt.Errorf("aster spot: rest client not configured: %w", errs.ErrNotSupported)
	}
	_, err = c.rest.CancelAllOpenOrders(ctx, sdkspot.CancelAllOrdersParams{Symbol: inst.VenueSymbol})
	return mapAsterError(err)
}

func (c *executionClient) Modify(context.Context, model.InstrumentID, string, decimal.Decimal, decimal.Decimal) (*model.Order, error) {
	return nil, fmt.Errorf("aster spot: modify is not implemented in Story 5: %w", errs.ErrNotSupported)
}

func (c *executionClient) OpenOrders(ctx context.Context, id model.InstrumentID) ([]model.Order, error) {
	inst, err := c.provider.instrument(id)
	if err != nil {
		return nil, err
	}
	if c.rest == nil {
		return nil, fmt.Errorf("aster spot: rest client not configured: %w", errs.ErrNotSupported)
	}
	orders, err := c.rest.OpenOrders(ctx, sdkspot.OpenOrdersQuery{Symbol: inst.VenueSymbol})
	if err != nil {
		return nil, mapAsterError(err)
	}
	out := make([]model.Order, 0, len(orders))
	for i := range orders {
		if orders[i].Symbol != inst.VenueSymbol {
			return nil, fmt.Errorf("aster spot: open order symbol mismatch %q for %q", orders[i].Symbol, inst.VenueSymbol)
		}
		if err := validateOrderResponseDecimals(&orders[i]); err != nil {
			return nil, err
		}
		out = append(out, orderFromResponse(&orders[i], model.OrderRequest{InstrumentID: id, AccountID: c.accountID}, c.accountID))
	}
	return out, nil
}

func (c *executionClient) GenerateOrderStatusReports(ctx context.Context, query model.OrderStatusReportQuery) ([]model.OrderStatusReport, error) {
	if query.AccountID != "" && query.AccountID != c.accountID {
		return nil, nil
	}
	if query.ClientID != "" {
		return nil, nil
	}
	if query.InstrumentID == (model.InstrumentID{}) {
		return nil, fmt.Errorf("aster spot: instrument is required for open-order reports: %w", errs.ErrNotSupported)
	}
	orders, err := c.OpenOrders(ctx, query.InstrumentID)
	if err != nil {
		return nil, err
	}
	now := c.clk.Now()
	out := make([]model.OrderStatusReport, 0, len(orders))
	for _, order := range orders {
		if model.OrderMatchesStatusQuery(order, query) {
			if !withinOrderWindow(order, query.Since, query.Until) {
				continue
			}
			report := model.OrderStatusReport{ReportID: orderReportID(order), Venue: VenueName, AccountID: c.accountID, Order: order, ReportedAt: now}
			if err := report.Validate(); err != nil {
				return nil, err
			}
			out = append(out, report)
		}
	}
	return out, nil
}

func (c *executionClient) GenerateOrderStatusReport(ctx context.Context, query model.SingleOrderStatusQuery) (*model.OrderStatusReport, error) {
	reports, err := c.GenerateOrderStatusReports(ctx, model.OrderStatusReportQuery{InstrumentID: query.InstrumentID, AccountID: query.AccountID, ClientID: query.ClientID, VenueOrderID: query.VenueOrderID, OpenOnly: true})
	if err != nil || len(reports) == 0 {
		return nil, err
	}
	return &reports[0], nil
}

func (c *executionClient) GenerateFillReports(ctx context.Context, query model.FillReportQuery) ([]model.FillReport, error) {
	if query.AccountID != "" && query.AccountID != c.accountID {
		return nil, nil
	}
	if query.InstrumentID == (model.InstrumentID{}) {
		return nil, fmt.Errorf("aster spot: instrument is required for fill reports: %w", errs.ErrNotSupported)
	}
	inst, err := c.provider.instrument(query.InstrumentID)
	if err != nil {
		return nil, err
	}
	if c.rest == nil {
		return nil, fmt.Errorf("aster spot: rest client not configured: %w", errs.ErrNotSupported)
	}
	venueOrderID, err := parseOptionalInt64(query.VenueOrderID)
	if err != nil {
		return nil, fmt.Errorf("aster spot: fill report venue order id: %w", err)
	}
	limit := query.Limit
	trades, err := c.rest.UserTrades(ctx, sdkspot.UserTradesQuery{
		Symbol:    inst.VenueSymbol,
		OrderID:   venueOrderID,
		StartTime: millisPtr(query.Since),
		EndTime:   millisPtr(query.Until),
		Limit:     limitPtr(limit),
	})
	if err != nil {
		return nil, mapAsterError(err)
	}
	now := c.clk.Now()
	out := make([]model.FillReport, 0, len(trades))
	for _, trade := range trades {
		if trade.Symbol != inst.VenueSymbol {
			return nil, fmt.Errorf("aster spot: fill report symbol mismatch %q for %q", trade.Symbol, inst.VenueSymbol)
		}
		if err := validateTradeDecimals(trade); err != nil {
			return nil, err
		}
		clientID := ""
		if query.VenueOrderID != "" {
			clientID = query.ClientID
		}
		fill := fillFromTrade(trade, query.InstrumentID, c.accountID, clientID)
		if !model.FillMatchesReportQuery(fill, query) {
			continue
		}
		if !withinTimeWindow(fill.Timestamp, query.Since, query.Until) {
			continue
		}
		report := model.FillReport{ReportID: fillReportID(fill), Venue: VenueName, AccountID: c.accountID, Fill: fill, ReportedAt: now}
		if err := report.Validate(); err != nil {
			return nil, err
		}
		out = append(out, report)
	}
	return out, nil
}

func (c *executionClient) GeneratePositionReports(context.Context, model.PositionReportQuery) ([]model.PositionReport, error) {
	return nil, fmt.Errorf("aster spot: cash positions are balance-sourced: %w", errs.ErrNotSupported)
}

func (c *executionClient) GenerateExecutionMassStatus(ctx context.Context, query model.MassStatusQuery) (*model.ExecutionMassStatus, error) {
	accountID := c.accountID
	if query.AccountID != "" && query.AccountID != c.accountID {
		return model.NewExecutionMassStatus(VenueName, query.AccountID, c.clk.Now()), nil
	}
	mass := model.NewExecutionMassStatus(VenueName, accountID, c.clk.Now())
	mass.ClientID = query.ClientID
	mass.Lookback = query.Lookback
	mass.Warnings = append(mass.Warnings, model.ReportWarning{Code: "OPEN_ONLY", Message: "mass status contains authoritative open orders; missing cached orders are no longer open, but terminal reason is unknown"})
	for _, inst := range sortedInstruments(c.provider) {
		orderReports, err := c.GenerateOrderStatusReports(ctx, model.OrderStatusReportQuery{
			InstrumentID: inst.ID,
			AccountID:    accountID,
			ClientID:     query.ClientID,
			OpenOnly:     true,
			Since:        query.Since,
			Until:        query.Until,
		})
		if err != nil {
			return nil, err
		}
		for _, report := range orderReports {
			if err := mass.AddOrderReport(report); err != nil {
				return nil, err
			}
		}
		if query.IncludeFills {
			fillReports, err := c.GenerateFillReports(ctx, model.FillReportQuery{
				InstrumentID: inst.ID,
				AccountID:    accountID,
				ClientID:     query.ClientID,
				Since:        query.Since,
				Until:        query.Until,
			})
			if err != nil {
				return nil, err
			}
			for _, report := range fillReports {
				if err := mass.AddFillReport(report); err != nil {
					return nil, err
				}
			}
		}
	}
	return mass, nil
}

func (c *executionClient) Events() <-chan contract.ExecEnvelope   { return c.stream.C() }
func (c *executionClient) emit(ev contract.ExecEvent)             { c.stream.Emit(contract.NewExecEnvelope(ev)) }
func (c *executionClient) emitEnvelope(env contract.ExecEnvelope) { c.stream.Emit(env) }
func (c *executionClient) Close() error                           { c.stream.Close(); return nil }

func parseOptionalInt64(raw string) (*int64, error) {
	if raw == "" {
		return nil, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return nil, err
	}
	return &value, nil
}

func millisPtr(ts time.Time) *int64 {
	if ts.IsZero() {
		return nil
	}
	value := ts.UnixMilli()
	return &value
}

func limitPtr(limit int) *int {
	if limit <= 0 {
		return nil
	}
	return &limit
}

func validateTradeDecimals(t sdkspot.Trade) error {
	if t.ID == 0 || t.OrderID == 0 {
		return fmt.Errorf("aster spot: trade id and order id are required")
	}
	if sideFromAster(t.Side) == enums.SideUnknown {
		return fmt.Errorf("aster spot: trade side %q is unsupported", t.Side)
	}
	for field, raw := range map[string]string{
		"price":      t.Price,
		"qty":        t.Qty,
		"commission": t.Commission,
	} {
		value, err := parseRequiredSDKDecimal(field, raw)
		if err != nil {
			return fmt.Errorf("aster spot: trade %d: %w", t.ID, err)
		}
		if field != "commission" && !value.IsPositive() {
			return fmt.Errorf("aster spot: trade %d has non-positive %s", t.ID, field)
		}
		if field == "commission" && value.IsNegative() {
			return fmt.Errorf("aster spot: trade %d has negative commission", t.ID)
		}
	}
	return nil
}

func sortedInstruments(provider *instrumentProvider) []*model.Instrument {
	insts := provider.All()
	sort.Slice(insts, func(i, j int) bool { return insts[i].ID.String() < insts[j].ID.String() })
	return insts
}

func orderReportID(order model.Order) model.ReportID {
	return model.ReportID(fmt.Sprintf("%s:%s:order:%s:%s", VenueName, order.Request.AccountID, order.Request.InstrumentID.String(), order.VenueOrderID))
}

func fillReportID(fill model.Fill) model.ReportID {
	return model.ReportID(fmt.Sprintf("%s:%s:fill:%s:%s:%s", VenueName, fill.AccountID, fill.InstrumentID.String(), fill.VenueOrderID, fill.TradeID))
}

func withinOrderWindow(order model.Order, since, until time.Time) bool {
	ts := order.UpdatedAt
	if ts.IsZero() {
		ts = order.CreatedAt
	}
	return withinTimeWindow(ts, since, until)
}

func withinTimeWindow(ts, since, until time.Time) bool {
	if !since.IsZero() && (ts.IsZero() || ts.Before(since)) {
		return false
	}
	if !until.IsZero() && (ts.IsZero() || ts.After(until)) {
		return false
	}
	return true
}
