package perp

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
	sdkperp "github.com/QuantProcessing/boltertrader/sdk/binance/perp"
	"github.com/shopspring/decimal"
)

// executionClient implements contract.ExecutionClient over the Binance REST +
// user-data WebSocket. Submit is synchronous: Binance's REST PlaceOrder blocks
// until the venue acknowledges, so no async bridging is needed (unlike
// Hyperliquid).
type executionClient struct {
	rest     *sdkperp.Client
	provider *instrumentProvider
	clk      clock.Clock
	stream   *wsstream.Stream[contract.ExecEvent]
}

func newExecutionClient(rest *sdkperp.Client, provider *instrumentProvider, clk clock.Clock) *executionClient {
	return &executionClient{
		rest:     rest,
		provider: provider,
		clk:      clk,
		stream:   wsstream.New[contract.ExecEvent](256),
	}
}

func (c *executionClient) venueSymbol(id model.InstrumentID) (string, error) {
	inst, ok := c.provider.Instrument(id)
	if !ok {
		return "", fmt.Errorf("binance: unknown instrument %s: %w", id, errs.ErrSymbolNotFound)
	}
	return inst.VenueSymbol, nil
}

func (c *executionClient) Submit(ctx context.Context, req model.OrderRequest) (*model.Order, error) {
	symbol, err := c.venueSymbol(req.InstrumentID)
	if err != nil {
		return nil, err
	}
	side, err := sideToBinance(req.Side)
	if err != nil {
		return nil, err
	}
	otype, err := orderTypeToBinance(req.Type)
	if err != nil {
		return nil, err
	}

	p := sdkperp.PlaceOrderParams{
		Symbol:           symbol,
		Side:             side,
		Type:             otype,
		Quantity:         req.Quantity.String(),
		NewClientOrderID: req.ClientID,
		ReduceOnly:       req.ReduceOnly,
	}
	if req.PositionSide != enums.PosNet {
		p.PositionSide = positionSideToBinance(req.PositionSide)
	}
	if !req.Price.IsZero() {
		p.Price = req.Price.String()
	}
	if !req.TriggerPrice.IsZero() {
		p.StopPrice = req.TriggerPrice.String()
	}
	// TIF only applies to limit-family orders.
	if typeNeedsTIF(req.Type) {
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

func (c *executionClient) Cancel(ctx context.Context, id model.InstrumentID, venueOrderID string) error {
	symbol, err := c.venueSymbol(id)
	if err != nil {
		return err
	}
	_, err = c.rest.CancelOrder(ctx, sdkperp.CancelOrderParams{Symbol: symbol, OrderID: venueOrderID})
	return err
}

func (c *executionClient) CancelAll(ctx context.Context, id model.InstrumentID) error {
	symbol, err := c.venueSymbol(id)
	if err != nil {
		return err
	}
	return c.rest.CancelAllOpenOrders(ctx, sdkperp.CancelAllOrdersParams{Symbol: symbol})
}

// Modify amends a resting order's price and/or quantity. Binance's amend
// endpoint requires the order side, which the venue-neutral Modify signature
// does not carry, so the resting order is fetched first to recover it. A zero
// newPrice or newQty is left unchanged (read back from the existing order),
// because Binance's amend requires both fields on every call.
func (c *executionClient) Modify(ctx context.Context, id model.InstrumentID, venueOrderID string, newPrice, newQty decimal.Decimal) (*model.Order, error) {
	symbol, err := c.venueSymbol(id)
	if err != nil {
		return nil, err
	}
	orderID, err := strconv.ParseInt(venueOrderID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("binance: invalid venue order id %q: %w", venueOrderID, err)
	}

	// The amend request needs the side; recover it (and any field left at zero)
	// from the resting order.
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

	resp, err := c.rest.ModifyOrder(ctx, sdkperp.ModifyOrderParams{
		Symbol:   symbol,
		Side:     existing.Side,
		OrderID:  orderID,
		Quantity: qty.String(),
		Price:    price.String(),
	})
	if err != nil {
		return nil, err
	}
	order := orderFromResponse(resp, model.OrderRequest{InstrumentID: id})
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

// OrderReports returns every open order across all instruments in one call.
// Binance's openOrders endpoint returns the full account-wide set when the
// symbol is omitted; each row's symbol is resolved back to an InstrumentID so
// reconciliation can rebuild orders the cache has never seen.
func (c *executionClient) OrderReports(ctx context.Context) ([]model.Order, error) {
	resps, err := c.rest.GetOpenOrders(ctx, "")
	if err != nil {
		return nil, err
	}
	out := make([]model.Order, 0, len(resps))
	for i := range resps {
		id := c.provider.resolveVenueSymbol(resps[i].Symbol)
		out = append(out, orderFromResponse(&resps[i], model.OrderRequest{InstrumentID: id}))
	}
	return out, nil
}

func (c *executionClient) Events() <-chan contract.ExecEvent { return c.stream.C() }

// emit pushes a translated execution event to the stream. It blocks under
// backpressure (never silently dropping fills/order updates) and is a no-op
// after Close.
func (c *executionClient) emit(ev contract.ExecEvent) { c.stream.Emit(ev) }

func (c *executionClient) Close() error {
	c.stream.Close()
	return nil
}

// orderFromResponse maps a Binance REST OrderResponse onto a domain Order,
// preserving the originating request where available.
func orderFromResponse(r *sdkperp.OrderResponse, req model.OrderRequest) model.Order {
	if req.ClientID == "" {
		req.ClientID = r.ClientOrderID
	}
	if req.Side == enums.SideUnknown {
		req.Side = sideFromBinance(r.Side)
	}
	return model.Order{
		Request:      req,
		VenueOrderID: itoa(r.OrderID),
		Status:       statusFromBinance(r.Status),
		FilledQty:    dec(r.ExecutedQty),
		AvgFillPrice: dec(r.AvgPrice),
	}
}
