package spot

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	hlaccount "github.com/QuantProcessing/boltertrader/adapter/hyperliquid/internal/account"
	"github.com/QuantProcessing/boltertrader/adapter/hyperliquid/internal/cloid"
	"github.com/QuantProcessing/boltertrader/adapter/hyperliquid/internal/instruments"
	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/errs"
	"github.com/QuantProcessing/boltertrader/internal/wsstream"
	sdk "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
	sdkspot "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/spot"
	"github.com/shopspring/decimal"
)

type executionClient struct {
	rest      *sdkspot.Client
	provider  *instruments.Registry
	clk       clock.Clock
	accountID string
	stream    *wsstream.Stream[contract.ExecEnvelope]
	mu        sync.RWMutex
	orders    map[string]model.Order
	ids       *cloid.Mapper
}

func newExecutionClient(rest *sdkspot.Client, provider *instruments.Registry, clk clock.Clock, accountID ...string) *executionClient {
	return &executionClient{
		rest:      rest,
		provider:  provider,
		clk:       clk,
		accountID: firstAccountID(accountID),
		stream:    wsstream.New[contract.ExecEnvelope](256),
		orders:    make(map[string]model.Order),
		ids:       cloid.NewMapper(),
	}
}

func (c *executionClient) AccountID() string { return c.accountID }

func (c *executionClient) instrument(id model.InstrumentID) (*model.Instrument, error) {
	inst, ok := c.provider.Instrument(id)
	if !ok {
		return nil, fmt.Errorf("hyperliquid spot: unknown instrument %s: %w", id, errs.ErrSymbolNotFound)
	}
	if inst.AssetIndex == nil {
		return nil, fmt.Errorf("hyperliquid spot: missing asset index for %s: %w", id, errs.ErrSymbolNotFound)
	}
	return inst, nil
}

func rejectDerivativeOrderFields(req model.OrderRequest) error {
	if req.ReduceOnly {
		return fmt.Errorf("hyperliquid spot: reduce-only orders are not supported: %w", errs.ErrNotSupported)
	}
	if req.PositionSide != enums.PosNet {
		return fmt.Errorf("hyperliquid spot: position side is not supported: %w", errs.ErrNotSupported)
	}
	return nil
}

func (c *executionClient) Submit(ctx context.Context, req model.OrderRequest) (*model.Order, error) {
	if err := rejectDerivativeOrderFields(req); err != nil {
		return nil, err
	}
	accountID, err := c.scopedAccountID(req.AccountID)
	if err != nil {
		return nil, err
	}
	req.AccountID = accountID
	hlReq, err := c.placeOrderRequest(req)
	if err != nil {
		return nil, err
	}
	status, err := c.rest.PlaceOrder(ctx, hlReq)
	if err != nil {
		return nil, err
	}
	now := c.clk.Now()
	req.PositionSide = enums.PosNet
	req.ReduceOnly = false
	order := &model.Order{Request: req, CreatedAt: now, UpdatedAt: now}
	applyOrderStatus(order, status)
	*order = c.rememberOrder(*order)
	return order, nil
}

func (c *executionClient) placeOrderRequest(req model.OrderRequest) (sdkspot.PlaceOrderRequest, error) {
	inst, err := c.instrument(req.InstrumentID)
	if err != nil {
		return sdkspot.PlaceOrderRequest{}, err
	}
	isBuy, err := sideToHL(req.Side)
	if err != nil {
		return sdkspot.PlaceOrderRequest{}, err
	}
	if req.Type != enums.TypeLimit {
		return sdkspot.PlaceOrderRequest{}, fmt.Errorf("hyperliquid spot: unsupported order type %v: %w", req.Type, errs.ErrNotSupported)
	}
	tif, err := tifToHL(req.TIF)
	if err != nil {
		return sdkspot.PlaceOrderRequest{}, err
	}
	var venueCloid *string
	if req.ClientID != "" {
		mapped := c.ids.VenueCloid(req.ClientID)
		venueCloid = &mapped
	}
	return sdkspot.PlaceOrderRequest{
		AssetID:       *inst.AssetIndex,
		IsBuy:         isBuy,
		Price:         decimalFloat64(req.Price),
		Size:          decimalFloat64(req.Quantity),
		ClientOrderID: venueCloid,
		OrderType: sdkspot.OrderType{
			Limit: &sdkspot.OrderTypeLimit{Tif: tif},
		},
	}, nil
}

func applyOrderStatus(order *model.Order, status *sdkspot.OrderStatus) {
	if status == nil {
		order.Status = enums.StatusUnknown
		return
	}
	if status.Resting != nil {
		order.VenueOrderID = strconv.FormatInt(status.Resting.Oid, 10)
		if order.Request.ClientID == "" && status.Resting.ClientID != nil {
			order.Request.ClientID = *status.Resting.ClientID
		}
		order.Status = enums.StatusNew
		return
	}
	if status.Filled != nil {
		order.VenueOrderID = strconv.Itoa(status.Filled.Oid)
		order.FilledQty = dec(status.Filled.TotalSz)
		order.AvgFillPrice = dec(status.Filled.AvgPx)
		order.Status = enums.StatusFilled
		return
	}
	if status.Error != nil {
		order.Status = enums.StatusRejected
		order.RejectReason = *status.Error
		return
	}
	order.Status = enums.StatusUnknown
}

func (c *executionClient) Cancel(ctx context.Context, id model.InstrumentID, venueOrderID string) error {
	inst, err := c.instrument(id)
	if err != nil {
		return err
	}
	oid, err := parseVenueOrderID(venueOrderID)
	if err != nil {
		return err
	}
	_, err = c.rest.CancelOrder(ctx, sdkspot.CancelOrderRequest{AssetID: *inst.AssetIndex, OrderID: oid})
	if err != nil {
		return err
	}
	c.emitCanceled(id, venueOrderID)
	return nil
}

func (c *executionClient) CancelAll(ctx context.Context, id model.InstrumentID) error {
	orders, err := c.OpenOrders(ctx, id)
	if err != nil {
		return err
	}
	for _, order := range orders {
		if err := c.Cancel(ctx, id, order.VenueOrderID); err != nil {
			return err
		}
	}
	return nil
}

func (c *executionClient) Modify(ctx context.Context, id model.InstrumentID, venueOrderID string, newPrice, newQty decimal.Decimal) (*model.Order, error) {
	inst, err := c.instrument(id)
	if err != nil {
		return nil, err
	}
	oid, err := parseVenueOrderID(venueOrderID)
	if err != nil {
		return nil, err
	}
	if newPrice.IsZero() && newQty.IsZero() {
		return nil, fmt.Errorf("hyperliquid spot: modify requires price or quantity: %w", errs.ErrNotSupported)
	}
	current, err := c.rest.OrderStatus(ctx, c.rest.AccountAddr, oid)
	if err != nil {
		return nil, err
	}
	isBuy, err := sideToHL(sideFromHL(current.Side))
	if err != nil {
		return nil, err
	}
	price := dec(current.LimitPx)
	if !newPrice.IsZero() {
		price = newPrice
	}
	qty := dec(current.Sz)
	if !newQty.IsZero() {
		qty = newQty
	}
	req := sdkspot.ModifyOrderRequest{
		Oid: &oid,
		Order: sdkspot.PlaceOrderRequest{
			AssetID: *inst.AssetIndex,
			IsBuy:   isBuy,
			Price:   decimalFloat64(price),
			Size:    decimalFloat64(qty),
			OrderType: sdkspot.OrderType{
				Limit: &sdkspot.OrderTypeLimit{Tif: sdk.TifGtc},
			},
		},
	}
	status, err := c.rest.ModifyOrder(ctx, req)
	if err != nil {
		return nil, err
	}
	order := &model.Order{Request: model.OrderRequest{AccountID: c.accountID, InstrumentID: id, PositionSide: enums.PosNet}, VenueOrderID: venueOrderID, UpdatedAt: c.clk.Now()}
	applyOrderStatus(order, status)
	*order = c.rememberOrder(*order)
	return order, nil
}

func (c *executionClient) OpenOrders(ctx context.Context, id model.InstrumentID) ([]model.Order, error) {
	inst, err := c.instrument(id)
	if err != nil {
		return nil, err
	}
	orders, err := c.rest.UserOpenOrders(ctx, c.rest.AccountAddr)
	if err != nil {
		return nil, err
	}
	out := make([]model.Order, 0, len(orders))
	for i := range orders {
		if orders[i].Coin != inst.VenueSymbol {
			continue
		}
		order := orderFromHL(&orders[i], id, c.accountID)
		order = c.rememberOrder(order)
		out = append(out, order)
	}
	return out, nil
}

func orderFromHL(o *sdkspot.Order, id model.InstrumentID, accountID string) model.Order {
	if o == nil {
		return model.Order{}
	}
	return model.Order{
		Request: model.OrderRequest{
			AccountID:    accountID,
			InstrumentID: id,
			ClientID:     o.Cliod,
			Side:         sideFromHL(o.Side),
			Type:         enums.TypeLimit,
			TIF:          enums.TifGTC,
			Quantity:     dec(o.OrigSz),
			Price:        dec(o.LimitPx),
			PositionSide: enums.PosNet,
		},
		VenueOrderID: strconv.FormatInt(o.Oid, 10),
		Status:       enums.StatusNew,
		CreatedAt:    parseMillis(o.Timestamp),
		UpdatedAt:    parseMillis(o.Timestamp),
	}
}

func (c *executionClient) GenerateOrderStatusReports(ctx context.Context, query model.OrderStatusReportQuery) ([]model.OrderStatusReport, error) {
	accountID, ok := c.scopedReportAccountID(query.AccountID)
	if !ok {
		return nil, nil
	}
	query.AccountID = accountID
	orders, err := c.rest.UserOpenOrders(ctx, c.rest.AccountAddr)
	if err != nil {
		return nil, err
	}
	now := c.clk.Now()
	out := make([]model.OrderStatusReport, 0, len(orders))
	for i := range orders {
		order, ok := c.orderFromOpenOrder(&orders[i])
		if !ok {
			continue
		}
		if !model.OrderMatchesStatusQuery(order, query) {
			continue
		}
		order = c.rememberOrder(order)
		out = append(out, model.OrderStatusReport{Venue: venueName, AccountID: accountID, Order: order, ReportedAt: now})
	}
	return out, nil
}

func (c *executionClient) orderFromOpenOrder(o *sdkspot.Order) (model.Order, bool) {
	if o == nil {
		return model.Order{}, false
	}
	id, ok := c.provider.ResolveVenueSymbol(o.Coin)
	if !ok {
		return model.Order{}, false
	}
	return orderFromHL(o, id, c.accountID), true
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
	if _, ok := c.scopedReportAccountID(query.AccountID); !ok {
		return nil, nil
	}
	return nil, fmt.Errorf("hyperliquid spot: fill report history is not implemented: %w", errs.ErrNotSupported)
}

func (c *executionClient) GeneratePositionReports(ctx context.Context, query model.PositionReportQuery) ([]model.PositionReport, error) {
	if _, ok := c.scopedReportAccountID(query.AccountID); !ok {
		return nil, nil
	}
	return nil, fmt.Errorf("hyperliquid spot: cash positions are balance-sourced: %w", errs.ErrNotSupported)
}

func (c *executionClient) GenerateExecutionMassStatus(ctx context.Context, query model.MassStatusQuery) (*model.ExecutionMassStatus, error) {
	accountID, ok := c.scopedReportAccountID(query.AccountID)
	if !ok {
		return model.NewExecutionMassStatus(venueName, query.AccountID, c.clk.Now()), nil
	}
	query.AccountID = accountID
	mass := model.NewExecutionMassStatus(venueName, accountID, c.clk.Now())
	mass.ClientID = query.ClientID
	mass.Lookback = query.Lookback
	mass.Partial = true
	mass.Warnings = append(mass.Warnings, model.ReportWarning{Code: "OPEN_ORDERS_ONLY", Message: "adapter can generate open-order status only; absent closed orders are ambiguous"})
	reports, err := c.GenerateOrderStatusReports(ctx, model.OrderStatusReportQuery{AccountID: accountID, ClientID: query.ClientID, OpenOnly: true})
	if err != nil {
		return nil, err
	}
	for _, report := range reports {
		if err := mass.AddOrderReport(report); err != nil {
			return nil, err
		}
	}
	return mass, nil
}

func (c *executionClient) Events() <-chan contract.ExecEnvelope { return c.stream.C() }

func (c *executionClient) emit(ev contract.ExecEvent) {
	ev = c.normalizeExecEvent(ev)
	c.stream.Emit(contract.NewExecEnvelope(ev))
}

func (c *executionClient) rememberOrder(order model.Order) model.Order {
	order = c.normalizeOrderIdentity(order)
	if order.VenueOrderID == "" {
		return order
	}
	c.mu.Lock()
	c.orders[order.VenueOrderID] = order
	c.mu.Unlock()
	return order
}

func (c *executionClient) emitCanceled(id model.InstrumentID, venueOrderID string) {
	c.mu.RLock()
	order, ok := c.orders[venueOrderID]
	c.mu.RUnlock()
	if !ok {
		order = model.Order{Request: model.OrderRequest{InstrumentID: id, PositionSide: enums.PosNet}, VenueOrderID: venueOrderID}
	}
	order.Status = enums.StatusCanceled
	order.UpdatedAt = c.clk.Now()
	order = c.rememberOrder(order)
	c.stream.Emit(contract.NewExecEnvelopeWithMeta(contract.OrderEvent{Order: order}, contract.EventMeta{
		Source: contract.SourceAdapterREST,
		Flags:  contract.EventFlagSynthetic,
	}))
}

func (c *executionClient) normalizeExecEvent(ev contract.ExecEvent) contract.ExecEvent {
	switch typed := ev.(type) {
	case contract.OrderEvent:
		typed.Order = c.rememberOrder(typed.Order)
		return typed
	case contract.FillEvent:
		if c.accountID != "" {
			typed.Fill.AccountID = c.accountID
		}
		if clientID := c.ids.ClientID(typed.Fill.ClientID, typed.Fill.VenueOrderID); clientID != "" {
			typed.Fill.ClientID = clientID
		}
		return typed
	case contract.RejectEvent:
		if clientID := c.ids.ClientID(typed.ClientID, ""); clientID != "" {
			typed.ClientID = clientID
		}
		return typed
	default:
		return ev
	}
}

func (c *executionClient) normalizeOrderIdentity(order model.Order) model.Order {
	if c.accountID != "" {
		order.Request.AccountID = c.accountID
	}
	rawClientID := order.Request.ClientID
	clientID := c.ids.ClientID(rawClientID, order.VenueOrderID)
	if clientID == "" {
		clientID = rawClientID
	}
	venueCloid := rawClientID
	if mapped := c.ids.VenueCloidForClient(clientID); mapped != "" {
		venueCloid = mapped
	}
	order.Request.ClientID = clientID
	c.ids.Remember(order.Request.ClientID, venueCloid, order.VenueOrderID)
	return order
}

func (c *executionClient) scopedAccountID(accountID string) (string, error) {
	return hlaccount.ResolveScopedAccountID(accountID, c.accountID)
}

func (c *executionClient) scopedReportAccountID(accountID string) (string, bool) {
	resolved, err := c.scopedAccountID(accountID)
	if err != nil {
		return "", false
	}
	return resolved, true
}

func (c *executionClient) Close() error {
	c.stream.Close()
	return nil
}
