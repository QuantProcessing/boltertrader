package spot

import (
	"context"
	"fmt"
	"strings"
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
	normalized, err := normalizeSpotTdMode(tdMode)
	if err != nil {
		normalized = defaultSpotTdMode
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
		return okx.OrderId{}, fmt.Errorf("okx spot: %s returned %d result rows, want exactly one", action, len(ids))
	}
	result := ids[0]
	if wantOrderID != "" && result.OrdId != wantOrderID {
		return okx.OrderId{}, fmt.Errorf("okx spot: %s result order id %q does not match request %q", action, result.OrdId, wantOrderID)
	}
	if wantClientID != "" && result.ClOrdId != wantClientID {
		return okx.OrderId{}, fmt.Errorf("okx spot: %s result client id %q does not match request %q", action, result.ClOrdId, wantClientID)
	}
	if err := checkSCode(ids); err != nil {
		return okx.OrderId{}, err
	}
	if result.OrdId == "" {
		return okx.OrderId{}, fmt.Errorf("okx spot: %s result missing order id", action)
	}
	return result, nil
}

func checkSingleAlgoSCode(action string, ids []okx.AlgoOrderID, wantAlgoID, wantClientID string) (okx.AlgoOrderID, error) {
	if len(ids) != 1 {
		return okx.AlgoOrderID{}, fmt.Errorf("okx spot: %s returned %d result rows, want exactly one", action, len(ids))
	}
	result := ids[0]
	if wantAlgoID != "" && result.AlgoId != wantAlgoID {
		return okx.AlgoOrderID{}, fmt.Errorf("okx spot: %s result algo id %q does not match request %q", action, result.AlgoId, wantAlgoID)
	}
	if wantClientID != "" && result.AlgoClOrdId != wantClientID {
		return okx.AlgoOrderID{}, fmt.Errorf("okx spot: %s result client id %q does not match request %q", action, result.AlgoClOrdId, wantClientID)
	}
	if err := checkAlgoSCode(ids); err != nil {
		return okx.AlgoOrderID{}, err
	}
	if result.AlgoId == "" {
		return okx.AlgoOrderID{}, fmt.Errorf("okx spot: %s result missing algo id", action)
	}
	return result, nil
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
	if req.AccountID == "" {
		req.AccountID = c.accountID
	}
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
	if isConditionalOrderType(req.Type) {
		return c.submitAlgo(ctx, req, instID, side)
	}
	ordType, err := regularOrdTypeToOKX(req.Type, req.TIF)
	if err != nil {
		return nil, err
	}

	r := &okx.OrderRequest{
		InstId:  instID,
		TdMode:  c.tdMode,
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
	oid, err := checkSingleSCode("place order", ids, "", req.ClientID)
	if err != nil {
		return nil, err
	}
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

func (c *executionClient) ValidateSubmit(req model.OrderRequest) error {
	if err := rejectDerivativeOrderFields(req); err != nil {
		return err
	}
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
				return fmt.Errorf("okx spot: trailing stop requires TrailingOffsetBps: %w", errs.ErrNotSupported)
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
			return nil, fmt.Errorf("okx spot: trailing stop requires TrailingOffsetBps: %w", errs.ErrNotSupported)
		}
		r.CallbackRatio = &callback
		if !req.ActivationPrice.IsZero() {
			active := req.ActivationPrice.String()
			r.ActivePx = &active
		}
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
	req.PositionSide = enums.PosNet
	req.ReduceOnly = false
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
	result, err := checkSingleSCode("modify order", ids, venueOrderID, "")
	if err != nil {
		return nil, err
	}
	return &model.Order{
		Request:      model.OrderRequest{AccountID: c.accountID, InstrumentID: id, PositionSide: enums.PosNet},
		VenueOrderID: result.OrdId,
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
		out = append(out, orderFromOKX(&orders[i], c.provider, c.accountID))
	}
	return out, nil
}

func (c *executionClient) GenerateOrderStatusReports(ctx context.Context, query model.OrderStatusReportQuery) ([]model.OrderStatusReport, error) {
	if query.AccountID != "" && query.AccountID != c.accountID {
		return nil, nil
	}
	accountID := c.accountID
	query.AccountID = accountID
	instType := instTypeSpot
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
	return nil, fmt.Errorf("okx spot: fill report history is not implemented: %w", errs.ErrNotSupported)
}

func (c *executionClient) GeneratePositionReports(ctx context.Context, query model.PositionReportQuery) ([]model.PositionReport, error) {
	if query.AccountID != "" && query.AccountID != c.accountID {
		return nil, nil
	}
	return nil, fmt.Errorf("okx spot: cash positions are balance-sourced: %w", errs.ErrNotSupported)
}

func (c *executionClient) GenerateExecutionMassStatus(ctx context.Context, query model.MassStatusQuery) (*model.ExecutionMassStatus, error) {
	if query.AccountID != "" && query.AccountID != c.accountID {
		return nil, fmt.Errorf("okx spot: mass status account %q does not match adapter account %q", query.AccountID, c.accountID)
	}
	accountID := c.accountID
	ids, resolver, err := c.freezeMassStatusScope(query)
	if err != nil {
		return nil, err
	}
	mass := model.NewExecutionMassStatus(venueName, accountID, c.clk.Now())
	mass.ClientID = query.ClientID
	mass.Lookback = query.Lookback
	setOptionalUnsupportedCoverage(mass, query)

	requestStartedAt := c.clk.Now()
	instType := instTypeSpot
	orders, regularErr := c.rest.GetOrders(ctx, &instType, nil)
	algos, algoErr := c.rest.GetPendingAlgoOrders(ctx, instTypeSpot, "", "", "", "")
	selected := instrumentIDSet(ids)
	if regularErr == nil {
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
	}
	if algoErr == nil {
		now := c.clk.Now()
		for i := range algos {
			order := orderFromPendingAlgo(&algos[i], resolver, accountID)
			if _, ok := selected[order.Request.InstrumentID]; !ok || !model.OrderMatchesStatusQuery(order, model.OrderStatusReportQuery{AccountID: accountID, ClientID: query.ClientID, OpenOnly: true}) {
				continue
			}
			if err := mass.AddOrderReport(model.OrderStatusReport{Venue: venueName, AccountID: accountID, Order: order, ReportedAt: now}); err != nil {
				return nil, err
			}
		}
	}
	state := model.CoverageComplete
	switch {
	case regularErr != nil && algoErr != nil:
		state = model.CoverageUnavailable
	case regularErr != nil || algoErr != nil || len(orders) >= okxPendingOrdersPageLimit || len(algos) >= okxPendingOrdersPageLimit:
		state = model.CoveragePartial
	}
	mass.OpenOrdersCoverage = model.NewSnapshotCoverage(state, accountID, query.ClientID, ids, requestStartedAt)
	appendMassStatusWarning(mass, "OPEN_ORDERS", regularErr)
	appendMassStatusWarning(mass, "ALGO_ORDERS", algoErr)
	appendPendingOrdersSaturationWarning(mass, "REGULAR", len(orders), regularErr)
	appendPendingOrdersSaturationWarning(mass, "ALGO", len(algos), algoErr)
	if err := mass.ValidateFor(query); err != nil {
		return nil, err
	}
	return mass, nil
}

func (c *executionClient) freezeMassStatusScope(query model.MassStatusQuery) ([]model.InstrumentID, frozenInstResolver, error) {
	if venue := strings.TrimSpace(query.Venue); venue != "" && venue != venueName {
		return nil, nil, fmt.Errorf("okx spot: mass status venue %q does not match %q", query.Venue, venueName)
	}
	if c.provider == nil {
		return nil, nil, fmt.Errorf("okx spot: instrument provider required for mass status")
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
			return nil, nil, fmt.Errorf("okx spot: invalid mass status instrument %s", id)
		}
		if explicitIDs {
			if instrument, ok := c.provider.byID[id.String()]; !ok || instrument == nil {
				return nil, nil, fmt.Errorf("okx spot: unknown mass status instrument %s", id)
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
	return model.InstrumentID{Venue: venueName, Symbol: instID, Kind: enums.KindSpot}
}

func setOptionalUnsupportedCoverage(mass *model.ExecutionMassStatus, query model.MassStatusQuery) {
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
			Message: fmt.Sprintf("okx spot: %s pending-order page reached %d-row cap; completeness is unproven", strings.ToLower(domain), okxPendingOrdersPageLimit),
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
	orderType := enums.TypeStopMarket
	price := firstNonZero(dec(order.OrderPx), dec(order.OrdPx))
	if strings.EqualFold(order.OrdType, "move_order_stop") {
		orderType = enums.TypeTrailingStopMarket
	} else if price.IsPositive() {
		orderType = enums.TypeStopLimit
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
			Quantity: dec(order.Sz), Price: price, TriggerPrice: dec(order.TriggerPx), PositionSide: enums.PosNet,
		},
		VenueOrderID: order.AlgoId,
		Status:       status,
		CreatedAt:    parseMillis(order.CTime),
		UpdatedAt:    parseMillis(order.UTime),
	}
}

func (c *executionClient) Events() <-chan contract.ExecEnvelope { return c.stream.C() }

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
