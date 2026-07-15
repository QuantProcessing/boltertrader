package spot

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/errs"
	"github.com/QuantProcessing/boltertrader/internal/wsstream"
	sdkspot "github.com/QuantProcessing/boltertrader/sdk/binance/spot"
	"github.com/shopspring/decimal"
)

type executionClient struct {
	rest      *sdkspot.Client
	provider  *instrumentProvider
	clk       clock.Clock
	accountID string
	stream    *wsstream.Stream[contract.ExecEnvelope]
}

func newExecutionClient(rest *sdkspot.Client, provider *instrumentProvider, clk clock.Clock, accountIDs ...string) *executionClient {
	accountID := ""
	if len(accountIDs) > 0 {
		accountID = accountIDs[0]
	}
	if accountID == "" {
		accountID = AccountIDDefault
	}
	return &executionClient{
		rest:      rest,
		provider:  provider,
		clk:       clk,
		accountID: accountID,
		stream:    wsstream.New[contract.ExecEnvelope](256),
	}
}

func (c *executionClient) AccountID() string { return c.accountID }

func mapDefinitiveOrderRejection(err error) error {
	if err == nil || !sdkspot.IsDefinitiveOrderRejection(err) {
		return err
	}
	var apiErr *sdkspot.APIError
	if !errors.As(err, &apiErr) {
		return err
	}
	venueErr := errs.NewExchangeError(venueName, strconv.Itoa(apiErr.Code), apiErr.Message, contract.ErrVenueRejected)
	return errors.Join(venueErr, err)
}

func (c *executionClient) venueSymbol(id model.InstrumentID) (string, error) {
	inst, ok := c.provider.Instrument(id)
	if !ok {
		return "", fmt.Errorf("binance spot: unknown instrument %s: %w", id, errs.ErrSymbolNotFound)
	}
	return inst.VenueSymbol, nil
}

func (c *executionClient) Submit(ctx context.Context, req model.OrderRequest) (*model.Order, error) {
	if req.ReduceOnly {
		return nil, fmt.Errorf("binance spot: reduce-only orders are not supported: %w", errs.ErrNotSupported)
	}
	if req.PositionSide != enums.PosNet {
		return nil, fmt.Errorf("binance spot: position side is not supported: %w", errs.ErrNotSupported)
	}
	symbol, err := c.venueSymbol(req.InstrumentID)
	if err != nil {
		return nil, err
	}
	if req.AccountID == "" {
		req.AccountID = c.accountID
	}
	side, err := sideToBinance(req.Side)
	if err != nil {
		return nil, err
	}
	otype, err := orderTypeToBinance(req.Type, req.TIF)
	if err != nil {
		return nil, err
	}

	p := sdkspot.PlaceOrderParams{
		Symbol:           symbol,
		Side:             side,
		Type:             otype,
		Quantity:         req.Quantity.String(),
		NewClientOrderID: req.ClientID,
		NewOrderRespType: "FULL",
	}
	if !req.Price.IsZero() {
		p.Price = req.Price.String()
	}
	if !req.TriggerPrice.IsZero() {
		p.StopPrice = req.TriggerPrice.String()
	}
	if typeNeedsTIF(req.Type, otype) {
		tif, err := tifToBinance(req.TIF)
		if err != nil {
			return nil, err
		}
		p.TimeInForce = tif
	}

	resp, err := c.rest.PlaceOrder(ctx, p)
	if err != nil {
		return nil, mapDefinitiveOrderRejection(err)
	}
	if err := validateSubmitResponse(resp, symbol, req.ClientID); err != nil {
		return nil, err
	}
	order := orderFromResponse(resp, req)
	order.CreatedAt = c.clk.Now()
	order.UpdatedAt = order.CreatedAt
	for _, ev := range execEventsFromOrderResponse(resp, req) {
		c.emitRESTSynthetic(ev)
	}
	return &order, nil
}

func validateSubmitResponse(resp *sdkspot.OrderResponse, symbol, clientID string) error {
	if resp == nil {
		return fmt.Errorf("binance spot: ambiguous submit response: missing order envelope")
	}
	if resp.OrderID <= 0 {
		return fmt.Errorf("binance spot: ambiguous submit response: invalid order id %d", resp.OrderID)
	}
	if !strings.EqualFold(strings.TrimSpace(resp.Symbol), symbol) {
		return fmt.Errorf("binance spot: ambiguous submit response: symbol %q does not match %q", resp.Symbol, symbol)
	}
	if clientID != "" && resp.ClientOrderID != clientID {
		return fmt.Errorf("binance spot: ambiguous submit response: client id %q does not match %q", resp.ClientOrderID, clientID)
	}
	status := statusFromBinance(resp.Status)
	if status == enums.StatusUnknown || status == enums.StatusRejected {
		return fmt.Errorf("binance spot: ambiguous submit response: unsupported status %q", resp.Status)
	}
	return nil
}

func (c *executionClient) ValidateSubmit(req model.OrderRequest) error {
	if req.ReduceOnly {
		return fmt.Errorf("binance spot: reduce-only orders are not supported: %w", errs.ErrNotSupported)
	}
	if req.PositionSide != enums.PosNet {
		return fmt.Errorf("binance spot: position side is not supported: %w", errs.ErrNotSupported)
	}
	if _, err := c.venueSymbol(req.InstrumentID); err != nil {
		return err
	}
	if _, err := sideToBinance(req.Side); err != nil {
		return err
	}
	otype, err := orderTypeToBinance(req.Type, req.TIF)
	if err != nil {
		return err
	}
	if typeNeedsTIF(req.Type, otype) {
		_, err = tifToBinance(req.TIF)
	}
	return err
}

func (c *executionClient) Cancel(ctx context.Context, id model.InstrumentID, venueOrderID string) error {
	symbol, err := c.venueSymbol(id)
	if err != nil {
		return err
	}
	orderID, err := strconv.ParseInt(venueOrderID, 10, 64)
	if err != nil {
		return fmt.Errorf("binance spot: invalid venue order id %q: %w", venueOrderID, err)
	}
	resp, err := c.rest.CancelOrder(ctx, symbol, orderID, "")
	if err != nil {
		return mapDefinitiveOrderRejection(err)
	}
	return validateCancelResponse(resp, symbol, orderID)
}

func validateCancelResponse(resp *sdkspot.CancelOrderResponse, symbol string, orderID int64) error {
	if resp == nil {
		return fmt.Errorf("binance spot: ambiguous cancel response: missing order envelope")
	}
	if resp.OrderID <= 0 || resp.OrderID != orderID {
		return fmt.Errorf("binance spot: ambiguous cancel response: order id %d does not match %d", resp.OrderID, orderID)
	}
	if !strings.EqualFold(strings.TrimSpace(resp.Symbol), symbol) {
		return fmt.Errorf("binance spot: ambiguous cancel response: symbol %q does not match %q", resp.Symbol, symbol)
	}
	if statusFromBinance(resp.Status) != enums.StatusCanceled {
		return fmt.Errorf("binance spot: ambiguous cancel response: unsupported status %q", resp.Status)
	}
	return nil
}

func (c *executionClient) CancelAll(ctx context.Context, id model.InstrumentID) error {
	symbol, err := c.venueSymbol(id)
	if err != nil {
		return err
	}
	_, err = c.rest.CancelAllOpenOrders(ctx, symbol)
	return mapDefinitiveOrderRejection(err)
}

func (c *executionClient) Modify(ctx context.Context, id model.InstrumentID, venueOrderID string, newPrice, newQty decimal.Decimal) (*model.Order, error) {
	return nil, fmt.Errorf("binance spot: modify uses cancel-replace order incarnations: %w", contract.ErrNotSupported)
}

func (c *executionClient) OpenOrders(ctx context.Context, id model.InstrumentID) ([]model.Order, error) {
	symbol, err := c.venueSymbol(id)
	if err != nil {
		return nil, err
	}
	resps, err := c.rest.GetOpenOrders(ctx, symbol)
	if err != nil {
		return nil, err
	}
	out := make([]model.Order, 0, len(resps))
	for i := range resps {
		out = append(out, orderFromResponse(&resps[i], model.OrderRequest{AccountID: c.accountID, InstrumentID: id}))
	}
	return out, nil
}

func (c *executionClient) GenerateOrderStatusReports(ctx context.Context, query model.OrderStatusReportQuery) ([]model.OrderStatusReport, error) {
	if query.AccountID != "" && query.AccountID != c.accountID {
		return nil, nil
	}
	accountID := c.accountID
	query.AccountID = accountID
	resps, err := c.rest.GetOpenOrders(ctx, "")
	if err != nil {
		return nil, err
	}
	now := c.clk.Now()
	out := make([]model.OrderStatusReport, 0, len(resps))
	for i := range resps {
		id := c.provider.resolveVenueSymbol(resps[i].Symbol)
		o := orderFromResponse(&resps[i], model.OrderRequest{AccountID: accountID, InstrumentID: id})
		if !model.OrderMatchesStatusQuery(o, query) {
			continue
		}
		out = append(out, model.OrderStatusReport{Venue: venueName, AccountID: accountID, Order: o, ReportedAt: now})
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
	if query.AccountID != "" && query.AccountID != c.accountID {
		return nil, nil
	}
	return nil, fmt.Errorf("binance spot: fill report history is not implemented: %w", errs.ErrNotSupported)
}

func (c *executionClient) GeneratePositionReports(ctx context.Context, query model.PositionReportQuery) ([]model.PositionReport, error) {
	if query.AccountID != "" && query.AccountID != c.accountID {
		return nil, nil
	}
	return nil, fmt.Errorf("binance spot: position reports are not served by execution client: %w", errs.ErrNotSupported)
}

func (c *executionClient) GenerateExecutionMassStatus(ctx context.Context, query model.MassStatusQuery) (*model.ExecutionMassStatus, error) {
	if query.AccountID != "" && query.AccountID != c.accountID {
		return nil, fmt.Errorf("binance spot: mass status account %q does not match adapter account %q", query.AccountID, c.accountID)
	}
	accountID := c.accountID
	ids, resolver, err := c.freezeMassStatusInstrumentScope(query)
	if err != nil {
		return nil, err
	}
	mass := model.NewExecutionMassStatus(venueName, accountID, c.clk.Now())
	mass.ClientID = query.ClientID
	mass.Lookback = query.Lookback
	if query.IncludeFills {
		mass.FillsCoverage = model.ReportCoverage{State: model.CoverageUnavailable}
	} else {
		mass.FillsCoverage = model.ReportCoverage{State: model.CoverageNotRequested}
	}
	if query.IncludePositions {
		mass.PositionsCoverage = model.ReportCoverage{State: model.CoverageUnavailable}
	} else {
		mass.PositionsCoverage = model.ReportCoverage{State: model.CoverageNotRequested}
	}

	requestStartedAt := c.clk.Now()
	resps, err := c.rest.GetOpenOrders(ctx, "")
	if err != nil {
		mass.OpenOrdersCoverage = model.NewSnapshotCoverage(model.CoverageUnavailable, accountID, query.ClientID, ids, requestStartedAt)
		mass.Warnings = append(mass.Warnings, model.ReportWarning{Code: "OPEN_ORDERS_UNAVAILABLE", Message: err.Error()})
		if validateErr := mass.ValidateFor(query); validateErr != nil {
			return nil, validateErr
		}
		return mass, nil
	}
	mass.OpenOrdersCoverage = model.NewSnapshotCoverage(model.CoverageComplete, accountID, query.ClientID, ids, requestStartedAt)
	selected := instrumentIDSet(ids)
	now := c.clk.Now()
	statusQuery := model.OrderStatusReportQuery{AccountID: accountID, ClientID: query.ClientID, OpenOnly: true}
	for i := range resps {
		id := resolver.resolve(resps[i].Symbol)
		if _, ok := selected[id]; !ok {
			continue
		}
		order := orderFromResponse(&resps[i], model.OrderRequest{AccountID: accountID, InstrumentID: id})
		if !model.OrderMatchesStatusQuery(order, statusQuery) {
			continue
		}
		report := model.OrderStatusReport{Venue: venueName, AccountID: accountID, Order: order, ReportedAt: now}
		if err := mass.AddOrderReport(report); err != nil {
			return nil, err
		}
	}
	if err := mass.ValidateFor(query); err != nil {
		return nil, err
	}
	return mass, nil
}

func (c *executionClient) freezeMassStatusInstrumentScope(query model.MassStatusQuery) ([]model.InstrumentID, frozenVenueSymbolResolver, error) {
	if venue := strings.TrimSpace(query.Venue); venue != "" && venue != venueName {
		return nil, nil, fmt.Errorf("binance spot: mass status venue %q does not match %q", query.Venue, venueName)
	}
	if c.provider == nil {
		return nil, nil, fmt.Errorf("binance spot: instrument provider required for mass status")
	}
	c.provider.mu.RLock()
	defer c.provider.mu.RUnlock()
	var ids []model.InstrumentID
	explicitIDs := query.InstrumentIDs != nil
	if explicitIDs {
		ids = model.NormalizeInstrumentIDs(query.InstrumentIDs)
	} else {
		ids = make([]model.InstrumentID, 0, len(c.provider.all))
		for _, instrument := range c.provider.all {
			if instrument != nil {
				ids = append(ids, instrument.ID)
			}
		}
		ids = model.NormalizeInstrumentIDs(ids)
	}
	for _, id := range ids {
		if id.Venue != venueName || id.Kind != enums.KindSpot || id.Symbol == "" {
			return nil, nil, fmt.Errorf("binance spot: invalid mass status instrument %s", id)
		}
		if explicitIDs {
			if instrument, ok := c.provider.byID[id.String()]; !ok || instrument == nil {
				return nil, nil, fmt.Errorf("binance spot: unknown mass status instrument %s", id)
			}
		}
	}
	resolver := make(frozenVenueSymbolResolver, len(c.provider.bySymbol))
	for symbol, id := range c.provider.bySymbol {
		resolver[symbol] = id
	}
	return ids, resolver, nil
}

type frozenVenueSymbolResolver map[string]model.InstrumentID

func (r frozenVenueSymbolResolver) resolve(symbol string) model.InstrumentID {
	if id, ok := r[symbol]; ok {
		return id
	}
	return model.InstrumentID{Venue: venueName, Symbol: symbol, Kind: enums.KindSpot}
}

func instrumentIDSet(ids []model.InstrumentID) map[model.InstrumentID]struct{} {
	set := make(map[model.InstrumentID]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return set
}

func (c *executionClient) Events() <-chan contract.ExecEnvelope { return c.stream.C() }

func (c *executionClient) emit(ev contract.ExecEvent) { c.stream.Emit(contract.NewExecEnvelope(ev)) }

func (c *executionClient) emitRESTSynthetic(ev contract.ExecEvent) {
	c.stream.Emit(contract.NewExecEnvelopeWithMeta(ev, contract.EventMeta{
		Source: contract.SourceAdapterREST,
		Flags:  contract.EventFlagSynthetic,
	}))
}

func (c *executionClient) Close() error {
	c.stream.Close()
	return nil
}

func orderFromResponse(r *sdkspot.OrderResponse, req model.OrderRequest) model.Order {
	if req.AccountID == "" {
		req.AccountID = AccountIDDefault
	}
	if req.ClientID == "" {
		req.ClientID = r.ClientOrderID
	}
	if req.Side == enums.SideUnknown {
		req.Side = sideFromBinance(r.Side)
	}
	if req.Type == enums.TypeUnknown {
		req.Type = orderTypeFromBinance(r.Type)
	}
	if req.TIF == enums.TifUnknown {
		req.TIF = tifFromBinance(r.TimeInForce)
	}
	if req.Quantity.IsZero() {
		req.Quantity = dec(r.OrigQty)
	}
	if req.Price.IsZero() {
		req.Price = dec(r.Price)
	}
	req.PositionSide = enums.PosNet
	req.ReduceOnly = false
	return model.Order{
		Request:      req,
		VenueOrderID: itoa(r.OrderID),
		Status:       statusFromBinance(r.Status),
		FilledQty:    dec(r.ExecutedQty),
		AvgFillPrice: avgFillPrice(dec(r.ExecutedQty), dec(r.CummulativeQuoteQty)),
	}
}

func avgFillPrice(executedQty, cumulativeQuoteQty decimal.Decimal) decimal.Decimal {
	if executedQty.IsZero() {
		return decimal.Zero
	}
	return cumulativeQuoteQty.Div(executedQty)
}
