package perp

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
	sdkperp "github.com/QuantProcessing/boltertrader/sdk/aster/perp"
	"github.com/shopspring/decimal"
)

type executionClient struct {
	rest      *sdkperp.Client
	provider  *instrumentProvider
	clk       clock.Clock
	accountID string
	stream    *wsstream.Stream[contract.ExecEnvelope]
	streaming bool
}

// Aster account trade history documents a maximum page size of 1000 records.
const executionMassStatusFillLimit = 1000

func newExecutionClient(rest *sdkperp.Client, provider *instrumentProvider, clk clock.Clock, accountID string) *executionClient {
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
	return contract.Capabilities{Venue: VenueName, Products: []contract.ProductCapability{{Kind: enums.KindPerp, Trading: true}}, Reports: contract.ReportCapabilities{SingleOrderStatus: true, OpenOrders: true, FillHistory: true, PositionReports: true, OpenOnlyNotFoundAmbiguous: true}, Streaming: contract.StreamCapabilities{Execution: c.streaming}, Trading: contract.TradingCapabilities{Submit: true, Cancel: true, CancelAll: true}}
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
		return nil, fmt.Errorf("aster perp: rest client not configured: %w", errs.ErrNotSupported)
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
		return fmt.Errorf("aster perp: account id %q does not match adapter account %q", req.AccountID, c.accountID)
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
		return fmt.Errorf("aster perp: rest client not configured: %w", errs.ErrNotSupported)
	}
	orderID, err := strconv.ParseInt(venueOrderID, 10, 64)
	if err != nil {
		return fmt.Errorf("aster perp: parse order id %q: %w", venueOrderID, err)
	}
	_, err = c.rest.CancelOrder(ctx, sdkperp.CancelOrderParams{Symbol: inst.VenueSymbol, OrderID: &orderID})
	return mapAsterError(err)
}

func (c *executionClient) CancelAll(ctx context.Context, id model.InstrumentID) error {
	inst, err := c.provider.instrument(id)
	if err != nil {
		return err
	}
	if c.rest == nil {
		return fmt.Errorf("aster perp: rest client not configured: %w", errs.ErrNotSupported)
	}
	_, err = c.rest.CancelAllOpenOrders(ctx, sdkperp.CancelAllOrdersParams{Symbol: inst.VenueSymbol})
	return mapAsterError(err)
}

func (c *executionClient) Modify(ctx context.Context, id model.InstrumentID, venueOrderID string, newPrice, newQty decimal.Decimal) (*model.Order, error) {
	return nil, fmt.Errorf("aster perp: modify is not implemented in Story 5: %w", errs.ErrNotSupported)
}

func (c *executionClient) OpenOrders(ctx context.Context, id model.InstrumentID) ([]model.Order, error) {
	inst, err := c.provider.instrument(id)
	if err != nil {
		return nil, err
	}
	if c.rest == nil {
		return nil, fmt.Errorf("aster perp: rest client not configured: %w", errs.ErrNotSupported)
	}
	orders, err := c.rest.OpenOrders(ctx, sdkperp.OpenOrdersQuery{Symbol: inst.VenueSymbol})
	if err != nil {
		return nil, mapAsterError(err)
	}
	out := make([]model.Order, 0, len(orders))
	for i := range orders {
		if orders[i].Symbol != inst.VenueSymbol {
			return nil, fmt.Errorf("aster perp: open order symbol mismatch %q for %q", orders[i].Symbol, inst.VenueSymbol)
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
		return nil, fmt.Errorf("aster perp: instrument is required for open-order reports: %w", errs.ErrNotSupported)
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
	if query.AccountID != "" && query.AccountID != c.accountID {
		return nil, nil
	}
	if query.ClientID == "" && query.VenueOrderID == "" {
		reports, err := c.GenerateOrderStatusReports(ctx, model.OrderStatusReportQuery{InstrumentID: query.InstrumentID, AccountID: query.AccountID, OpenOnly: true})
		if err != nil || len(reports) == 0 {
			return nil, err
		}
		return &reports[0], nil
	}
	if query.InstrumentID == (model.InstrumentID{}) {
		return nil, fmt.Errorf("aster perp: instrument is required for order status report: %w", errs.ErrNotSupported)
	}
	inst, err := c.provider.instrument(query.InstrumentID)
	if err != nil {
		return nil, err
	}
	if c.rest == nil {
		return nil, fmt.Errorf("aster perp: rest client not configured: %w", errs.ErrNotSupported)
	}

	venueQuery := sdkperp.OrderQuery{Symbol: inst.VenueSymbol}
	if query.VenueOrderID != "" {
		orderID, err := strconv.ParseInt(query.VenueOrderID, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("aster perp: order status venue order id %q: %w", query.VenueOrderID, err)
		}
		venueQuery.OrderID = &orderID
	} else {
		venueQuery.OrigClientOrderID = query.ClientID
	}
	response, err := c.rest.QueryOrder(ctx, venueQuery)
	if err != nil {
		mapped := mapAsterError(err)
		if errors.Is(mapped, errs.ErrOrderNotFound) {
			return nil, nil
		}
		return nil, mapped
	}
	if response == nil {
		return nil, fmt.Errorf("aster perp: order status response is required")
	}
	if response.Symbol != inst.VenueSymbol {
		return nil, fmt.Errorf("aster perp: order status symbol mismatch %q for %q", response.Symbol, inst.VenueSymbol)
	}
	if response.OrderID <= 0 {
		return nil, fmt.Errorf("aster perp: order status response has invalid order id %d", response.OrderID)
	}
	if query.VenueOrderID != "" && strconv.FormatInt(response.OrderID, 10) != query.VenueOrderID {
		return nil, fmt.Errorf("aster perp: order status venue order id mismatch %d for %q", response.OrderID, query.VenueOrderID)
	}
	if query.ClientID != "" && response.ClientOrderID != query.ClientID {
		return nil, fmt.Errorf("aster perp: order status client id mismatch %q for %q", response.ClientOrderID, query.ClientID)
	}
	if err := validateOrderResponseDecimals(response); err != nil {
		return nil, err
	}
	order := orderFromResponse(response, model.OrderRequest{
		InstrumentID: query.InstrumentID,
		AccountID:    c.accountID,
		ClientID:     query.ClientID,
	}, c.accountID)
	report := model.OrderStatusReport{
		ReportID:   orderReportID(order),
		Venue:      VenueName,
		AccountID:  c.accountID,
		Order:      order,
		ReportedAt: c.clk.Now(),
	}
	if err := report.Validate(); err != nil {
		return nil, err
	}
	return &report, nil
}

func (c *executionClient) GenerateFillReports(ctx context.Context, query model.FillReportQuery) ([]model.FillReport, error) {
	reports, _, err := c.generateFillReports(ctx, query)
	return reports, err
}

func (c *executionClient) generateFillReports(ctx context.Context, query model.FillReportQuery) ([]model.FillReport, bool, error) {
	if query.AccountID != "" && query.AccountID != c.accountID {
		return nil, false, nil
	}
	if query.InstrumentID == (model.InstrumentID{}) {
		return nil, false, fmt.Errorf("aster perp: instrument is required for fill reports: %w", errs.ErrNotSupported)
	}
	inst, err := c.provider.instrument(query.InstrumentID)
	if err != nil {
		return nil, false, err
	}
	if c.rest == nil {
		return nil, false, fmt.Errorf("aster perp: rest client not configured: %w", errs.ErrNotSupported)
	}
	limit := query.Limit
	trades, err := c.rest.UserTrades(ctx, sdkperp.UserTradesQuery{
		Symbol:    inst.VenueSymbol,
		StartTime: millisPtr(query.Since),
		EndTime:   millisPtr(query.Until),
		Limit:     limitPtr(limit),
	})
	if err != nil {
		return nil, false, mapAsterError(err)
	}
	limitReached := limit > 0 && len(trades) >= limit
	now := c.clk.Now()
	out := make([]model.FillReport, 0, len(trades))
	for _, trade := range trades {
		if trade.Symbol != inst.VenueSymbol {
			return nil, false, fmt.Errorf("aster perp: fill report symbol mismatch %q for %q", trade.Symbol, inst.VenueSymbol)
		}
		if err := validateTradeDecimals(trade); err != nil {
			return nil, false, err
		}
		clientID := ""
		if query.VenueOrderID != "" && strconv.FormatInt(trade.OrderID, 10) == query.VenueOrderID {
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
			return nil, false, err
		}
		out = append(out, report)
	}
	return out, limitReached, nil
}

func (c *executionClient) GeneratePositionReports(ctx context.Context, query model.PositionReportQuery) ([]model.PositionReport, error) {
	if query.AccountID != "" && query.AccountID != c.accountID {
		return nil, nil
	}
	if c.rest == nil {
		return nil, fmt.Errorf("aster perp: rest client not configured: %w", errs.ErrNotSupported)
	}
	symbol := ""
	if query.InstrumentID != (model.InstrumentID{}) {
		inst, err := c.provider.instrument(query.InstrumentID)
		if err != nil {
			return nil, err
		}
		symbol = inst.VenueSymbol
	}
	positions, err := c.rest.GetPositionRisk(ctx, symbol)
	if err != nil {
		return nil, mapAsterError(err)
	}
	now := c.clk.Now()
	out := make([]model.PositionReport, 0, len(positions))
	for _, row := range positions {
		if side := strings.ToUpper(strings.TrimSpace(row.PositionSide)); side != "" && side != "BOTH" {
			return nil, fmt.Errorf("aster perp: position side %q requires unsupported hedge mode: %w", row.PositionSide, errs.ErrNotSupported)
		}
		if err := validatePositionRiskDecimals(row); err != nil {
			return nil, err
		}
		if dec(row.PositionAmt).IsZero() {
			continue
		}
		id, ok := c.provider.resolveKnownVenueSymbol(row.Symbol)
		if !ok {
			return nil, fmt.Errorf("aster perp: unresolved position risk symbol %q", row.Symbol)
		}
		if symbol != "" && row.Symbol != symbol {
			return nil, fmt.Errorf("aster perp: position report symbol mismatch %q for %q", row.Symbol, symbol)
		}
		pos := positionFromRisk(row, id, c.accountID, now)
		if pos.InstrumentID.Symbol == "" {
			continue
		}
		if query.InstrumentID != (model.InstrumentID{}) && pos.InstrumentID != query.InstrumentID {
			continue
		}
		if !withinTimeWindow(pos.UpdatedAt, query.Since, query.Until) {
			continue
		}
		report := model.PositionReport{ReportID: positionReportID(pos), Venue: VenueName, AccountID: c.accountID, Position: pos, ReportedAt: now}
		if err := report.Validate(); err != nil {
			return nil, err
		}
		out = append(out, report)
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
	mass.Warnings = append(mass.Warnings, model.ReportWarning{Code: "OPEN_ONLY", Message: "mass status contains authoritative open orders; missing cached orders are no longer open, but terminal reason is unknown"})
	fillLimitReached := false
	for _, inst := range sortedInstruments(c.provider) {
		orderReports, err := c.GenerateOrderStatusReports(ctx, model.OrderStatusReportQuery{
			InstrumentID: inst.ID,
			AccountID:    c.accountID,
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
			fillReports, limitReached, err := c.generateFillReports(ctx, model.FillReportQuery{
				InstrumentID: inst.ID,
				AccountID:    c.accountID,
				ClientID:     query.ClientID,
				Since:        query.Since,
				Until:        query.Until,
				Limit:        executionMassStatusFillLimit,
			})
			if err != nil {
				return nil, err
			}
			fillLimitReached = fillLimitReached || limitReached
			for _, report := range fillReports {
				if err := mass.AddFillReport(report); err != nil {
					return nil, err
				}
			}
		}
	}
	if fillLimitReached {
		mass.Partial = true
		mass.Warnings = append(mass.Warnings, model.ReportWarning{
			Code:    "FILL_REPORTS_LIMIT_REACHED",
			Message: "one or more Aster Perp account-trade queries reached the 1000-record API limit; recovered fills may be incomplete",
		})
	}
	if query.IncludePositions {
		positionReports, err := c.GeneratePositionReports(ctx, model.PositionReportQuery{AccountID: c.accountID, Since: query.Since, Until: query.Until})
		if err != nil {
			return nil, err
		}
		for _, report := range positionReports {
			if err := mass.AddPositionReport(report); err != nil {
				return nil, err
			}
		}
	}
	return mass, nil
}

func (c *executionClient) Events() <-chan contract.ExecEnvelope   { return c.stream.C() }
func (c *executionClient) emit(ev contract.ExecEvent)             { c.stream.Emit(contract.NewExecEnvelope(ev)) }
func (c *executionClient) emitEnvelope(env contract.ExecEnvelope) { c.stream.Emit(env) }
func (c *executionClient) Close() error                           { c.stream.Close(); return nil }

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

func validateTradeDecimals(t sdkperp.Trade) error {
	if t.ID == 0 || t.OrderID == 0 {
		return fmt.Errorf("aster perp: trade id and order id are required")
	}
	if sideFromAster(t.Side) == enums.SideUnknown {
		return fmt.Errorf("aster perp: trade side %q is unsupported", t.Side)
	}
	for field, raw := range map[string]string{
		"price":      t.Price,
		"qty":        t.Qty,
		"commission": t.Commission,
	} {
		value, err := parseRequiredSDKDecimal(field, raw)
		if err != nil {
			return fmt.Errorf("aster perp: trade %d: %w", t.ID, err)
		}
		if field != "commission" && !value.IsPositive() {
			return fmt.Errorf("aster perp: trade %d has non-positive %s", t.ID, field)
		}
		if field == "commission" && value.IsNegative() {
			return fmt.Errorf("aster perp: trade %d has negative commission", t.ID)
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

func positionReportID(pos model.Position) model.ReportID {
	return model.ReportID(fmt.Sprintf("%s:%s:position:%s:%s", VenueName, pos.AccountID, pos.InstrumentID.String(), pos.Side.String()))
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
