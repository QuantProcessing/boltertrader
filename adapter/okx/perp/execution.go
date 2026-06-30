package perp

import (
	"context"
	"fmt"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/errs"
	"github.com/QuantProcessing/boltertrader/internal/wsstream"
	"github.com/QuantProcessing/boltertrader/sdk/okx"
	"github.com/shopspring/decimal"
)

// executionClient implements contract.ExecutionClient over the OKX REST + ws.
// OKX REST PlaceOrder blocks until the venue responds, so Submit is naturally
// synchronous.
type executionClient struct {
	rest     *okx.Client
	provider *instrumentProvider
	clk      clock.Clock
	stream   *wsstream.Stream[contract.ExecEvent]
}

func newExecutionClient(rest *okx.Client, provider *instrumentProvider, clk clock.Clock) *executionClient {
	return &executionClient{
		rest:     rest,
		provider: provider,
		clk:      clk,
		stream:   wsstream.New[contract.ExecEvent](256),
	}
}

// checkSCode inspects OKX's per-order result codes. OKX returns HTTP 200 with a
// per-order sCode that is non-"0" when the operation was rejected, so transport
// success is NOT operation success. Returns a wrapped ExchangeError on the first
// failed entry.
func checkSCode(ids []okx.OrderId) error {
	for _, id := range ids {
		if id.SCode != "" && id.SCode != "0" {
			return errs.NewExchangeError(venueName, id.SCode, id.SMsg, nil)
		}
	}
	return nil
}

func (c *executionClient) instID(id model.InstrumentID) (string, error) {
	inst, ok := c.provider.Instrument(id)
	if !ok {
		return "", fmt.Errorf("okx: unknown instrument %s: %w", id, errs.ErrSymbolNotFound)
	}
	return inst.VenueSymbol, nil
}

func (c *executionClient) Submit(ctx context.Context, req model.OrderRequest) (*model.Order, error) {
	instID, err := c.instID(req.InstrumentID)
	if err != nil {
		return nil, err
	}
	side, err := sideToOKX(req.Side)
	if err != nil {
		return nil, err
	}
	ordType, err := ordTypeToOKX(req.Type, req.TIF)
	if err != nil {
		return nil, err
	}

	r := &okx.OrderRequest{
		InstId:  instID,
		TdMode:  "cross", // default; per-order margin mode is an OKX divergence
		Side:    side,
		OrdType: ordType,
		Sz:      req.Quantity.String(),
	}
	if req.ClientID != "" {
		r.ClOrdId = &req.ClientID
	}
	if !req.Price.IsZero() {
		px := req.Price.String()
		r.Px = &px
	}
	if req.PositionSide != enums.PosNet {
		ps := positionSideToOKX(req.PositionSide)
		r.PosSide = &ps
	}
	if req.ReduceOnly {
		ro := true
		r.ReduceOnly = &ro
	}

	ids, err := c.rest.PlaceOrder(ctx, r)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("okx: empty order response")
	}
	oid := ids[0]
	// OKX returns a per-order sCode; non-"0" means the order was rejected.
	if oid.SCode != "" && oid.SCode != "0" {
		return nil, errs.NewExchangeError(venueName, oid.SCode, oid.SMsg, nil)
	}

	now := c.clk.Now()
	if req.ClientID == "" {
		req.ClientID = oid.ClOrdId
	}
	return &model.Order{
		Request:      req,
		VenueOrderID: oid.OrdId,
		Status:       enums.StatusNew,
		CreatedAt:    now,
		UpdatedAt:    now,
	}, nil
}

func (c *executionClient) Cancel(ctx context.Context, id model.InstrumentID, venueOrderID string) error {
	instID, err := c.instID(id)
	if err != nil {
		return err
	}
	ids, err := c.rest.CancelOrder(ctx, instID, venueOrderID, "")
	if err != nil {
		return err
	}
	return checkSCode(ids)
}

func (c *executionClient) CancelAll(ctx context.Context, id model.InstrumentID) error {
	// OKX has no single "cancel all by instrument" REST endpoint in this SDK;
	// fetch open orders and cancel them in a batch.
	instID, err := c.instID(id)
	if err != nil {
		return err
	}
	instType := instTypeSwap
	orders, err := c.rest.GetOrders(ctx, &instType, &instID)
	if err != nil {
		return err
	}
	if len(orders) == 0 {
		return nil
	}
	reqs := make([]okx.CancelOrderRequest, 0, len(orders))
	for _, o := range orders {
		ordID := o.OrdId
		reqs = append(reqs, okx.CancelOrderRequest{InstId: instID, OrdId: &ordID})
	}
	ids, err := c.rest.CancelOrders(ctx, reqs)
	if err != nil {
		return err
	}
	return checkSCode(ids)
}

func (c *executionClient) Modify(ctx context.Context, id model.InstrumentID, venueOrderID string, newPrice, newQty decimal.Decimal) (*model.Order, error) {
	instID, err := c.instID(id)
	if err != nil {
		return nil, err
	}
	newSz := newQty.String()
	newPx := newPrice.String()
	r := &okx.ModifyOrderRequest{InstId: instID, OrdId: &venueOrderID, NewSz: &newSz, NewPx: &newPx}
	ids, err := c.rest.ModifyOrder(ctx, r)
	if err != nil {
		return nil, err
	}
	if err := checkSCode(ids); err != nil {
		return nil, err
	}
	vid := venueOrderID
	if len(ids) > 0 {
		vid = ids[0].OrdId
	}
	return &model.Order{
		Request:      model.OrderRequest{InstrumentID: id},
		VenueOrderID: vid,
		UpdatedAt:    c.clk.Now(),
	}, nil
}

func (c *executionClient) OpenOrders(ctx context.Context, id model.InstrumentID) ([]model.Order, error) {
	instID, err := c.instID(id)
	if err != nil {
		return nil, err
	}
	instType := instTypeSwap
	orders, err := c.rest.GetOrders(ctx, &instType, &instID)
	if err != nil {
		return nil, err
	}
	out := make([]model.Order, 0, len(orders))
	for i := range orders {
		out = append(out, orderFromOKX(&orders[i], c.provider))
	}
	return out, nil
}

// OrderReports returns every pending SWAP order across all instruments in one
// call. OKX's orders-pending endpoint returns the full set when instId is
// omitted (nil), which is the venue-wide reconciliation feed.
func (c *executionClient) OrderReports(ctx context.Context) ([]model.Order, error) {
	instType := instTypeSwap
	orders, err := c.rest.GetOrders(ctx, &instType, nil)
	if err != nil {
		return nil, err
	}
	out := make([]model.Order, 0, len(orders))
	for i := range orders {
		out = append(out, orderFromOKX(&orders[i], c.provider))
	}
	return out, nil
}

func (c *executionClient) Events() <-chan contract.ExecEvent { return c.stream.C() }

// emit blocks under backpressure (never dropping order/fill updates), no-op
// after Close.
func (c *executionClient) emit(ev contract.ExecEvent) { c.stream.Emit(ev) }

func (c *executionClient) Close() error {
	c.stream.Close()
	return nil
}
