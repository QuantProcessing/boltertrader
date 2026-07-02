package spot

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
		return "", fmt.Errorf("okx spot: unknown instrument %s: %w", id, errs.ErrSymbolNotFound)
	}
	return inst.VenueSymbol, nil
}

func rejectDerivativeOrderFields(req model.OrderRequest) error {
	if req.ReduceOnly {
		return fmt.Errorf("okx spot: reduce-only orders are not supported: %w", errs.ErrNotSupported)
	}
	if req.PositionSide != enums.PosNet {
		return fmt.Errorf("okx spot: position side is not supported: %w", errs.ErrNotSupported)
	}
	return nil
}

func (c *executionClient) Submit(ctx context.Context, req model.OrderRequest) (*model.Order, error) {
	if err := rejectDerivativeOrderFields(req); err != nil {
		return nil, err
	}
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
		TdMode:  spotTdMode,
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

	ids, err := c.rest.PlaceOrder(ctx, r)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("okx spot: empty order response")
	}
	if err := checkSCode(ids); err != nil {
		return nil, err
	}
	oid := ids[0]
	now := c.clk.Now()
	if req.ClientID == "" {
		req.ClientID = oid.ClOrdId
	}
	req.PositionSide = enums.PosNet
	req.ReduceOnly = false
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
	instID, err := c.instID(id)
	if err != nil {
		return err
	}
	instType := instTypeSpot
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
	r := &okx.ModifyOrderRequest{InstId: instID, OrdId: &venueOrderID}
	if !newQty.IsZero() {
		newSz := newQty.String()
		r.NewSz = &newSz
	}
	if !newPrice.IsZero() {
		newPx := newPrice.String()
		r.NewPx = &newPx
	}
	if r.NewSz == nil && r.NewPx == nil {
		return nil, fmt.Errorf("okx spot: modify requires price or quantity: %w", errs.ErrNotSupported)
	}
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
		Request:      model.OrderRequest{InstrumentID: id, PositionSide: enums.PosNet},
		VenueOrderID: vid,
		UpdatedAt:    c.clk.Now(),
	}, nil
}

func (c *executionClient) OpenOrders(ctx context.Context, id model.InstrumentID) ([]model.Order, error) {
	instID, err := c.instID(id)
	if err != nil {
		return nil, err
	}
	instType := instTypeSpot
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

func (c *executionClient) OrderReports(ctx context.Context) ([]model.Order, error) {
	instType := instTypeSpot
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

func (c *executionClient) emit(ev contract.ExecEvent) { c.stream.Emit(ev) }

func (c *executionClient) Close() error {
	c.stream.Close()
	return nil
}
