package spot

import (
	"context"
	"fmt"
	"strconv"

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
	rest     *sdkspot.Client
	provider *instrumentProvider
	clk      clock.Clock
	stream   *wsstream.Stream[contract.ExecEnvelope]
}

func newExecutionClient(rest *sdkspot.Client, provider *instrumentProvider, clk clock.Clock) *executionClient {
	return &executionClient{
		rest:     rest,
		provider: provider,
		clk:      clk,
		stream:   wsstream.New[contract.ExecEnvelope](256),
	}
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
		return nil, err
	}
	order := orderFromResponse(resp, req)
	order.CreatedAt = c.clk.Now()
	order.UpdatedAt = order.CreatedAt
	return &order, nil
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
	_, err = c.rest.CancelOrder(ctx, symbol, orderID, "")
	return err
}

func (c *executionClient) CancelAll(ctx context.Context, id model.InstrumentID) error {
	symbol, err := c.venueSymbol(id)
	if err != nil {
		return err
	}
	_, err = c.rest.CancelAllOpenOrders(ctx, symbol)
	return err
}

func (c *executionClient) Modify(ctx context.Context, id model.InstrumentID, venueOrderID string, newPrice, newQty decimal.Decimal) (*model.Order, error) {
	symbol, err := c.venueSymbol(id)
	if err != nil {
		return nil, err
	}
	orderID, err := strconv.ParseInt(venueOrderID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("binance spot: invalid venue order id %q: %w", venueOrderID, err)
	}
	existing, err := c.rest.GetOrder(ctx, symbol, orderID, "")
	if err != nil {
		return nil, err
	}
	qty := newQty
	if qty.IsZero() {
		qty = dec(existing.OrigQty)
	}
	price := newPrice
	if price.IsZero() {
		price = dec(existing.Price)
	}
	resp, err := c.rest.ModifyOrder(ctx, sdkspot.CancelReplaceOrderParams{
		Symbol:            symbol,
		Side:              existing.Side,
		Type:              existing.Type,
		CancelReplaceMode: "STOP_ON_FAILURE",
		TimeInForce:       existing.TimeInForce,
		Quantity:          qty.String(),
		Price:             price.String(),
		CancelOrderID:     orderID,
	})
	if err != nil {
		return nil, err
	}
	if resp.NewOrderResponse == nil {
		return nil, fmt.Errorf("binance spot: cancelReplace response missing new order response")
	}
	order := orderFromResponse(resp.NewOrderResponse, model.OrderRequest{InstrumentID: id})
	order.UpdatedAt = c.clk.Now()
	return &order, nil
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
		out = append(out, orderFromResponse(&resps[i], model.OrderRequest{InstrumentID: id}))
	}
	return out, nil
}

func (c *executionClient) GenerateOrderStatusReports(ctx context.Context, query model.OrderStatusReportQuery) ([]model.OrderStatusReport, error) {
	resps, err := c.rest.GetOpenOrders(ctx, "")
	if err != nil {
		return nil, err
	}
	now := c.clk.Now()
	out := make([]model.OrderStatusReport, 0, len(resps))
	for i := range resps {
		id := c.provider.resolveVenueSymbol(resps[i].Symbol)
		o := orderFromResponse(&resps[i], model.OrderRequest{InstrumentID: id})
		if !model.OrderMatchesStatusQuery(o, query) {
			continue
		}
		out = append(out, model.OrderStatusReport{Venue: venueName, AccountID: query.AccountID, Order: o, ReportedAt: now})
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
	return nil, fmt.Errorf("binance spot: fill report history is not implemented: %w", errs.ErrNotSupported)
}

func (c *executionClient) GeneratePositionReports(ctx context.Context, query model.PositionReportQuery) ([]model.PositionReport, error) {
	return nil, fmt.Errorf("binance spot: position reports are not served by execution client: %w", errs.ErrNotSupported)
}

func (c *executionClient) GenerateExecutionMassStatus(ctx context.Context, query model.MassStatusQuery) (*model.ExecutionMassStatus, error) {
	reports, err := c.GenerateOrderStatusReports(ctx, model.OrderStatusReportQuery{AccountID: query.AccountID, ClientID: query.ClientID, OpenOnly: true})
	if err != nil {
		return nil, err
	}
	mass := model.NewExecutionMassStatus(venueName, query.AccountID, c.clk.Now())
	mass.ClientID = query.ClientID
	mass.Lookback = query.Lookback
	mass.Partial = true
	mass.Warnings = append(mass.Warnings, model.ReportWarning{Code: "OPEN_ORDERS_ONLY", Message: "adapter can generate open-order status only; absent closed orders are ambiguous"})
	for _, report := range reports {
		if err := mass.AddOrderReport(report); err != nil {
			return nil, err
		}
	}
	return mass, nil
}

func (c *executionClient) Events() <-chan contract.ExecEnvelope { return c.stream.C() }

func (c *executionClient) emit(ev contract.ExecEvent) { c.stream.Emit(contract.NewExecEnvelope(ev)) }

func (c *executionClient) Close() error {
	c.stream.Close()
	return nil
}

func orderFromResponse(r *sdkspot.OrderResponse, req model.OrderRequest) model.Order {
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
