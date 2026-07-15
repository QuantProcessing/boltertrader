package perp

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/errs"
	"github.com/QuantProcessing/boltertrader/internal/wsstream"
	"github.com/QuantProcessing/boltertrader/sdk/okx"
	"github.com/shopspring/decimal"
)

// OKX requires ordType on pending-algo inventory requests. Conditional and OCO
// are the only families the venue permits in one combined selector; chase is
// included here because OKX limits it to derivative products.
var perpPendingAlgoOrderTypes = [...]string{
	string(okx.AlgoOrderTypeConditional) + "," + string(okx.AlgoOrderTypeOCO),
	string(okx.AlgoOrderTypeTrigger),
	string(okx.AlgoOrderTypeMoveOrderStop),
	string(okx.AlgoOrderTypeIceberg),
	string(okx.AlgoOrderTypeTWAP),
	string(okx.AlgoOrderTypeSmartIceberg),
	string(okx.AlgoOrderTypeChase),
}

type pendingAlgoPage struct {
	orderType string
	orders    []okx.AlgoOrder
	err       error
}

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
		accountID = AccountIDDefault
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

func checkSingleSCode(action string, ids []okx.OrderId, wantOrderID, wantClientID string) (okx.OrderId, error) {
	if len(ids) != 1 {
		return okx.OrderId{}, fmt.Errorf("okx: %s returned %d result rows, want exactly one", action, len(ids))
	}
	result := ids[0]
	if wantOrderID != "" && result.OrdId != wantOrderID {
		return okx.OrderId{}, fmt.Errorf("okx: %s result order id %q does not match request %q", action, result.OrdId, wantOrderID)
	}
	if wantClientID != "" && result.ClOrdId != wantClientID {
		return okx.OrderId{}, fmt.Errorf("okx: %s result client id %q does not match request %q", action, result.ClOrdId, wantClientID)
	}
	if err := checkSCode(ids); err != nil {
		return okx.OrderId{}, err
	}
	if result.OrdId == "" {
		return okx.OrderId{}, fmt.Errorf("okx: %s result missing order id", action)
	}
	return result, nil
}

func checkSingleAlgoSCode(action string, ids []okx.AlgoOrderID, wantAlgoID, wantClientID string) (okx.AlgoOrderID, error) {
	if len(ids) != 1 {
		return okx.AlgoOrderID{}, fmt.Errorf("okx: %s returned %d result rows, want exactly one", action, len(ids))
	}
	result := ids[0]
	if wantAlgoID != "" && result.AlgoId != wantAlgoID {
		return okx.AlgoOrderID{}, fmt.Errorf("okx: %s result algo id %q does not match request %q", action, result.AlgoId, wantAlgoID)
	}
	if wantClientID != "" && result.AlgoClOrdId != wantClientID {
		return okx.AlgoOrderID{}, fmt.Errorf("okx: %s result client id %q does not match request %q", action, result.AlgoClOrdId, wantClientID)
	}
	if err := checkAlgoSCode(ids); err != nil {
		return okx.AlgoOrderID{}, err
	}
	if result.AlgoId == "" {
		return okx.AlgoOrderID{}, fmt.Errorf("okx: %s result missing algo id", action)
	}
	return result, nil
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
	oid, err := checkSingleSCode("place order", ids, "", req.ClientID)
	if err != nil {
		return nil, err
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
	oid, err := checkSingleAlgoSCode("place algo order", ids, "", req.ClientID)
	if err != nil {
		return nil, err
	}
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
		if err != nil {
			return err
		}
		if _, err := checkSingleAlgoSCode("cancel algo order", ids, venueOrderID, ""); err != nil {
			return err
		}
		c.forgetAlgo(venueOrderID)
		return nil
	}
	ids, err := c.rest.CancelOrder(ctx, instID, venueOrderID, "")
	if err != nil {
		return err
	}
	_, err = checkSingleSCode("cancel order", ids, venueOrderID, "")
	return err
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
	result, err := checkSingleSCode("modify order", ids, venueOrderID, "")
	if err != nil {
		return nil, err
	}
	return &model.Order{
		Request:      model.OrderRequest{AccountID: c.accountID, InstrumentID: id},
		VenueOrderID: result.OrdId,
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
		return nil, fmt.Errorf("okx: mass status account %q does not match adapter account %q", query.AccountID, c.accountID)
	}
	accountID := c.accountID
	ids, resolver, err := c.freezeMassStatusScope(query)
	if err != nil {
		return nil, err
	}
	mass := model.NewExecutionMassStatus(venueName, accountID, c.clk.Now())
	mass.ClientID = query.ClientID
	mass.Lookback = query.Lookback
	requestStartedAt := c.clk.Now()
	setOptionalUnsupportedCoverage(mass, query, accountID, ids, requestStartedAt)

	// Read parent algos before regular orders. If an algo triggers between
	// requests, this ordering can duplicate evidence but cannot miss both the
	// disappearing parent and its newly created regular child.
	algoPages := c.fetchPendingAlgoPages(ctx, instTypeSwap)
	instType := instTypeSwap
	orders, regularErr := c.rest.GetOrders(ctx, &instType, nil)
	selected := instrumentIDSet(ids)
	successfulSources := 0
	incomplete := false
	for _, page := range algoPages {
		if page.err != nil {
			incomplete = true
			appendMassStatusWarning(mass, pendingAlgoWarningCode(page.orderType), page.err)
			continue
		}
		successfulSources++
		if len(page.orders) >= okxPendingOrdersPageLimit {
			incomplete = true
		}
		appendPendingOrdersSaturationWarning(mass, pendingAlgoWarningCode(page.orderType), len(page.orders), nil)
		now := c.clk.Now()
		for i := range page.orders {
			order := orderFromPendingAlgo(&page.orders[i], resolver, accountID)
			if _, ok := selected[order.Request.InstrumentID]; !ok || !model.OrderMatchesStatusQuery(order, model.OrderStatusReportQuery{AccountID: accountID, ClientID: query.ClientID, OpenOnly: true}) {
				continue
			}
			if err := mass.AddOrderReport(model.OrderStatusReport{Venue: venueName, AccountID: accountID, Order: order, ReportedAt: now}); err != nil {
				return nil, err
			}
		}
	}
	if regularErr == nil {
		successfulSources++
		if len(orders) >= okxPendingOrdersPageLimit {
			incomplete = true
		}
		now := c.clk.Now()
		for i := range orders {
			order := orderFromOKX(&orders[i], resolver, accountID)
			if _, ok := selected[order.Request.InstrumentID]; !ok || !model.OrderMatchesStatusQuery(order, model.OrderStatusReportQuery{AccountID: accountID, ClientID: query.ClientID, OpenOnly: true}) {
				continue
			}
			if err := mass.AddOrderReport(model.OrderStatusReport{Venue: venueName, AccountID: accountID, Order: order, ReportedAt: now}); err != nil {
				return nil, err
			}
		}
	} else {
		incomplete = true
	}
	state := model.CoverageComplete
	switch {
	case successfulSources == 0:
		state = model.CoverageUnavailable
	case incomplete:
		state = model.CoveragePartial
	}
	mass.OpenOrdersCoverage = model.NewSnapshotCoverage(state, accountID, query.ClientID, ids, requestStartedAt)
	appendMassStatusWarning(mass, "OPEN_ORDERS", regularErr)
	appendPendingOrdersSaturationWarning(mass, "REGULAR", len(orders), regularErr)
	if err := mass.ValidateFor(query); err != nil {
		return nil, err
	}
	return mass, nil
}

func (c *executionClient) fetchPendingAlgoPages(ctx context.Context, instType string) []pendingAlgoPage {
	pages := make([]pendingAlgoPage, 0, len(perpPendingAlgoOrderTypes))
	for _, orderType := range perpPendingAlgoOrderTypes {
		orders, err := c.rest.GetPendingAlgoOrders(ctx, instType, "", orderType, "", "")
		pages = append(pages, pendingAlgoPage{orderType: orderType, orders: orders, err: err})
	}
	return pages
}

func pendingAlgoWarningCode(orderType string) string {
	token := strings.NewReplacer(",", "_", "-", "_").Replace(strings.ToUpper(orderType))
	return "ALGO_" + token
}

func (c *executionClient) freezeMassStatusScope(query model.MassStatusQuery) ([]model.InstrumentID, frozenInstResolver, error) {
	if venue := strings.TrimSpace(query.Venue); venue != "" && venue != venueName {
		return nil, nil, fmt.Errorf("okx: mass status venue %q does not match %q", query.Venue, venueName)
	}
	if c.provider == nil {
		return nil, nil, fmt.Errorf("okx: instrument provider required for mass status")
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
		if id.Venue != venueName || id.Kind != enums.KindPerp || id.Symbol == "" {
			return nil, nil, fmt.Errorf("okx: invalid mass status instrument %s", id)
		}
		if explicitIDs {
			if instrument, ok := c.provider.byID[id.String()]; !ok || instrument == nil {
				return nil, nil, fmt.Errorf("okx: unknown mass status instrument %s", id)
			}
		}
	}
	resolver := make(frozenInstResolver, len(c.provider.byInstID))
	for instID, id := range c.provider.byInstID {
		resolver[instID] = id
	}
	return ids, resolver, nil
}

type frozenInstResolver map[string]model.InstrumentID

func (r frozenInstResolver) resolveInstID(instID string) model.InstrumentID {
	if id, ok := r[instID]; ok {
		return id
	}
	return model.InstrumentID{Venue: venueName, Symbol: instIDToNeutral(instID), Kind: enums.KindPerp}
}

func setOptionalUnsupportedCoverage(
	mass *model.ExecutionMassStatus,
	query model.MassStatusQuery,
	accountID string,
	ids []model.InstrumentID,
	requestStartedAt time.Time,
) {
	if query.IncludeFills {
		mass.FillsCoverage = model.ReportCoverage{State: model.CoverageUnavailable}
	} else {
		mass.FillsCoverage = model.ReportCoverage{State: model.CoverageNotRequested}
	}
	if query.IncludePositions {
		mass.PositionsCoverage = model.NewSnapshotCoverage(model.CoverageUnavailable, accountID, query.ClientID, ids, requestStartedAt)
	} else {
		mass.PositionsCoverage = model.ReportCoverage{State: model.CoverageNotRequested}
	}
}

func appendMassStatusWarning(mass *model.ExecutionMassStatus, code string, err error) {
	if err != nil {
		mass.Warnings = append(mass.Warnings, model.ReportWarning{Code: code + "_UNAVAILABLE", Message: err.Error()})
	}
}

const okxPendingOrdersPageLimit = 100

func appendPendingOrdersSaturationWarning(mass *model.ExecutionMassStatus, domain string, count int, err error) {
	if err == nil && count >= okxPendingOrdersPageLimit {
		mass.Warnings = append(mass.Warnings, model.ReportWarning{
			Code:    domain + "_ORDERS_SATURATED",
			Message: fmt.Sprintf("okx: %s pending-order page reached %d-row cap; completeness is unproven", strings.ToLower(domain), okxPendingOrdersPageLimit),
		})
	}
}

func instrumentIDSet(ids []model.InstrumentID) map[model.InstrumentID]struct{} {
	set := make(map[model.InstrumentID]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return set
}

func orderFromPendingAlgo(order *okx.AlgoOrder, resolver instResolver, accountID string) model.Order {
	if order == nil {
		return model.Order{}
	}
	orderType := enums.TypeUnknown
	price := dec(order.OrderPx)
	if price.IsZero() {
		price = dec(order.OrdPx)
	}
	switch strings.ToLower(order.OrdType) {
	case string(okx.AlgoOrderTypeMoveOrderStop):
		orderType = enums.TypeTrailingStopMarket
	case string(okx.AlgoOrderTypeTrigger):
		orderType = enums.TypeStopMarket
		if price.IsPositive() {
			orderType = enums.TypeStopLimit
		}
	}
	clientID := order.AlgoClOrdId
	if clientID == "" {
		clientID = order.ClOrdId
	}
	status := statusFromOKX(order.State)
	if status == enums.StatusUnknown {
		status = enums.StatusNew
	}
	return model.Order{
		Request: model.OrderRequest{
			AccountID: accountID, InstrumentID: resolver.resolveInstID(order.InstId), ClientID: clientID,
			Side: sideFromOKX(string(order.Side)), Type: orderType, TIF: enums.TifUnknown,
			Quantity: dec(order.Sz), Price: price, TriggerPrice: dec(order.TriggerPx),
			PositionSide: positionSideFromOKX(string(order.PosSide)), ReduceOnly: strings.EqualFold(order.ReduceOnly, "true"),
		},
		VenueOrderID: order.AlgoId,
		Status:       status,
		CreatedAt:    parseMillis(order.CTime),
		UpdatedAt:    parseMillis(order.UTime),
	}
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
