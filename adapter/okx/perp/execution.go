package perp

import (
	"context"
	"fmt"
	"sync"

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
	rest      *okx.Client
	provider  *instrumentProvider
	clk       clock.Clock
	tdMode    string
	accountID string
	stream    *wsstream.Stream[contract.ExecEnvelope]
	algoMu    sync.Mutex
	algoIDs   map[string]struct{}
}

func newExecutionClient(rest *okx.Client, provider *instrumentProvider, clk clock.Clock, tdMode string, accountIDs ...string) *executionClient {
	normalized, err := normalizeDerivativeTdMode(tdMode)
	if err != nil {
		normalized = defaultDerivativeTdMode
	}
	accountID := ""
	if len(accountIDs) > 0 {
		accountID = accountIDs[0]
	}
	if accountID == "" {
		accountID = model.AccountIDOKXDefault
	}
	return &executionClient{
		rest:      rest,
		provider:  provider,
		clk:       clk,
		tdMode:    normalized,
		accountID: accountID,
		stream:    wsstream.New[contract.ExecEnvelope](256),
		algoIDs:   make(map[string]struct{}),
	}
}

func (c *executionClient) AccountID() string { return c.accountID }

// checkSCode inspects OKX's per-order result codes. OKX returns HTTP 200 with a
// per-order sCode that is non-"0" when the operation was rejected, so transport
// success is NOT operation success. Returns a wrapped ExchangeError on the first
// failed entry.
func checkSCode(ids []okx.OrderId) error {
	for _, id := range ids {
		if id.SCode != "" && id.SCode != "0" {
			return errs.NewExchangeError(venueName, id.SCode, id.SMsg, contract.ErrVenueRejected)
		}
	}
	return nil
}

func checkAlgoSCode(ids []okx.AlgoOrderID) error {
	for _, id := range ids {
		if id.SCode != "" && id.SCode != "0" {
			return errs.NewExchangeError(venueName, id.SCode, id.SMsg, contract.ErrVenueRejected)
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
	if req.AccountID == "" {
		req.AccountID = c.accountID
	}
	instID, err := c.instID(req.InstrumentID)
	if err != nil {
		return nil, err
	}
	side, err := sideToOKX(req.Side)
	if err != nil {
		return nil, err
	}
	if isConditionalOrderType(req.Type) {
		return c.submitAlgo(ctx, req, instID, side)
	}
	ordType, err := regularOrdTypeToOKX(req.Type, req.TIF)
	if err != nil {
		return nil, err
	}

	r := &okx.OrderRequest{
		InstId:  instID,
		TdMode:  c.tdMode, // per-order margin mode is an OKX divergence
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
		return nil, errs.NewExchangeError(venueName, oid.SCode, oid.SMsg, contract.ErrVenueRejected)
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

func (c *executionClient) ValidateSubmit(req model.OrderRequest) error {
	if _, err := c.instID(req.InstrumentID); err != nil {
		return err
	}
	if _, err := sideToOKX(req.Side); err != nil {
		return err
	}
	if isConditionalOrderType(req.Type) {
		if _, err := algoOrdTypeToOKX(req.Type); err != nil {
			return err
		}
		if req.Type == enums.TypeTrailingStopMarket {
			if _, ok := callbackRatioFromBps(req.TrailingOffsetBps); !ok {
				return fmt.Errorf("okx: trailing stop requires TrailingOffsetBps: %w", errs.ErrNotSupported)
			}
		}
		return nil
	}
	_, err := regularOrdTypeToOKX(req.Type, req.TIF)
	return err
}

func (c *executionClient) submitAlgo(ctx context.Context, req model.OrderRequest, instID, side string) (*model.Order, error) {
	if req.AccountID == "" {
		req.AccountID = c.accountID
	}
	ordType, err := algoOrdTypeToOKX(req.Type)
	if err != nil {
		return nil, err
	}
	r := &okx.AlgoOrderRequest{
		InstId:  instID,
		TdMode:  c.tdMode,
		Side:    side,
		OrdType: ordType,
		Sz:      req.Quantity.String(),
	}
	if req.ClientID != "" {
		r.AlgoClOrdId = &req.ClientID
	}
	if !req.TriggerPrice.IsZero() {
		trigger := req.TriggerPrice.String()
		r.TriggerPx = &trigger
	}
	if orderPx, ok := algoOrderPx(req.Type, req.Price); ok {
		r.OrderPx = &orderPx
	}
	if req.Type == enums.TypeTrailingStopMarket {
		callback, ok := callbackRatioFromBps(req.TrailingOffsetBps)
		if !ok {
			return nil, fmt.Errorf("okx: trailing stop requires TrailingOffsetBps: %w", errs.ErrNotSupported)
		}
		r.CallbackRatio = &callback
		if !req.ActivationPrice.IsZero() {
			active := req.ActivationPrice.String()
			r.ActivePx = &active
		}
	}
	if req.PositionSide != enums.PosNet {
		ps := positionSideToOKX(req.PositionSide)
		r.PosSide = &ps
	}
	if req.ReduceOnly {
		ro := true
		r.ReduceOnly = &ro
	}

	ids, err := c.rest.PlaceAlgoOrder(ctx, r)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("okx: empty algo order response")
	}
	if err := checkAlgoSCode(ids); err != nil {
		return nil, err
	}
	oid := ids[0]
	now := c.clk.Now()
	if req.ClientID == "" {
		req.ClientID = oid.AlgoClOrdId
	}
	order := &model.Order{
		Request:      req,
		VenueOrderID: oid.AlgoId,
		Status:       enums.StatusNew,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	c.rememberAlgo(order.VenueOrderID)
	return order, nil
}

func (c *executionClient) Cancel(ctx context.Context, id model.InstrumentID, venueOrderID string) error {
	instID, err := c.instID(id)
	if err != nil {
		return err
	}
	if c.isKnownAlgo(venueOrderID) {
		ids, err := c.rest.CancelAlgoOrders(ctx, []okx.AlgoCancelRequest{{InstId: instID, AlgoId: venueOrderID}})
		if err == nil {
			c.forgetAlgo(venueOrderID)
		}
		if err != nil {
			return err
		}
		return checkAlgoSCode(ids)
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
		Request:      model.OrderRequest{AccountID: c.accountID, InstrumentID: id},
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
		out = append(out, orderFromOKX(&orders[i], c.provider, c.accountID))
	}
	return out, nil
}

// GenerateOrderStatusReports returns every pending SWAP order across all
// instruments in one call. OKX's orders-pending endpoint returns the full set
// when instId is omitted (nil), which is the venue-wide reconciliation feed.
func (c *executionClient) GenerateOrderStatusReports(ctx context.Context, query model.OrderStatusReportQuery) ([]model.OrderStatusReport, error) {
	if query.AccountID != "" && query.AccountID != c.accountID {
		return nil, nil
	}
	accountID := c.accountID
	query.AccountID = accountID
	instType := instTypeSwap
	orders, err := c.rest.GetOrders(ctx, &instType, nil)
	if err != nil {
		return nil, err
	}
	now := c.clk.Now()
	out := make([]model.OrderStatusReport, 0, len(orders))
	for i := range orders {
		o := orderFromOKX(&orders[i], c.provider, accountID)
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
	return nil, fmt.Errorf("okx: fill report history is not implemented: %w", errs.ErrNotSupported)
}

func (c *executionClient) GeneratePositionReports(ctx context.Context, query model.PositionReportQuery) ([]model.PositionReport, error) {
	if query.AccountID != "" && query.AccountID != c.accountID {
		return nil, nil
	}
	return nil, fmt.Errorf("okx: position reports are served by the account client: %w", errs.ErrNotSupported)
}

func (c *executionClient) GenerateExecutionMassStatus(ctx context.Context, query model.MassStatusQuery) (*model.ExecutionMassStatus, error) {
	if query.AccountID != "" && query.AccountID != c.accountID {
		return model.NewExecutionMassStatus(venueName, query.AccountID, c.clk.Now()), nil
	}
	accountID := c.accountID
	reports, err := c.GenerateOrderStatusReports(ctx, model.OrderStatusReportQuery{AccountID: accountID, ClientID: query.ClientID, OpenOnly: true})
	if err != nil {
		return nil, err
	}
	mass := model.NewExecutionMassStatus(venueName, accountID, c.clk.Now())
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

// emit blocks under backpressure (never dropping order/fill updates), no-op
// after Close.
func (c *executionClient) emit(ev contract.ExecEvent) { c.stream.Emit(contract.NewExecEnvelope(ev)) }

func (c *executionClient) Close() error {
	c.stream.Close()
	return nil
}

func (c *executionClient) rememberAlgo(id string) {
	if id == "" {
		return
	}
	c.algoMu.Lock()
	c.algoIDs[id] = struct{}{}
	c.algoMu.Unlock()
}

func (c *executionClient) forgetAlgo(id string) {
	c.algoMu.Lock()
	delete(c.algoIDs, id)
	c.algoMu.Unlock()
}

func (c *executionClient) isKnownAlgo(id string) bool {
	c.algoMu.Lock()
	defer c.algoMu.Unlock()
	_, ok := c.algoIDs[id]
	return ok
}
