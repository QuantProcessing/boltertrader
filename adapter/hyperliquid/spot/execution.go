package spot

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	hlaccount "github.com/QuantProcessing/boltertrader/adapter/hyperliquid/internal/account"
	"github.com/QuantProcessing/boltertrader/adapter/hyperliquid/internal/cloid"
	"github.com/QuantProcessing/boltertrader/adapter/hyperliquid/internal/instruments"
	"github.com/QuantProcessing/boltertrader/adapter/hyperliquid/internal/ordersemantics"
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
		if errors.Is(err, sdk.ErrOrderRejected) {
			return nil, errors.Join(contract.ErrVenueRejected, err)
		}
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
	orderType, tif, _ := ordersemantics.FromWire(current.OrderType, current.Tif, current.IsTrigger, current.TriggerPx)
	if orderType != enums.TypeLimit || tif == enums.TifUnknown || tif == enums.TifFOK {
		return nil, fmt.Errorf("hyperliquid spot: cannot safely reconstruct order semantics type=%q tif=%q for modify: %w", current.OrderType, current.Tif, errs.ErrNotSupported)
	}
	wireTif, err := tifToHL(tif)
	if err != nil {
		return nil, err
	}
	var venueCloid *string
	clientID := ""
	if current.Cliod != "" {
		value := current.Cliod
		venueCloid = &value
		clientID = c.ids.ClientID(current.Cliod, venueOrderID)
	}
	request := model.OrderRequest{
		AccountID: c.accountID, InstrumentID: id, ClientID: clientID,
		Side: sideFromHL(current.Side), Type: orderType, TIF: tif,
		Quantity: qty, Price: price, PositionSide: enums.PosNet,
	}
	req := sdkspot.ModifyOrderRequest{
		Oid: &oid,
		Order: sdkspot.PlaceOrderRequest{
			AssetID:       *inst.AssetIndex,
			IsBuy:         isBuy,
			Price:         decimalFloat64(price),
			Size:          decimalFloat64(qty),
			ClientOrderID: venueCloid,
			OrderType: sdkspot.OrderType{
				Limit: &sdkspot.OrderTypeLimit{Tif: wireTif},
			},
		},
	}
	status, err := c.rest.ModifyOrder(ctx, req)
	if err != nil {
		return nil, err
	}
	order := &model.Order{Request: request, VenueOrderID: venueOrderID, UpdatedAt: c.clk.Now()}
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
	orderType, tif, triggerPrice := ordersemantics.FromWire(o.OrderType, o.Tif, o.IsTrigger, o.TriggerPx)
	return model.Order{
		Request: model.OrderRequest{
			AccountID:    accountID,
			InstrumentID: id,
			ClientID:     o.Cliod,
			Side:         sideFromHL(o.Side),
			Type:         orderType,
			TIF:          tif,
			Quantity:     dec(o.OrigSz),
			Price:        dec(o.LimitPx),
			TriggerPrice: triggerPrice,
			PositionSide: enums.PosNet,
			ReduceOnly:   o.ReduceOnly,
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
	accountID, ok := c.scopedReportAccountID(query.AccountID)
	if !ok {
		return nil, nil
	}
	query.AccountID = accountID
	if query.ClientID == "" && query.VenueOrderID == "" {
		reports, err := c.GenerateOrderStatusReports(ctx, model.OrderStatusReportQuery{
			InstrumentID: query.InstrumentID,
			AccountID:    accountID,
		})
		if err != nil || len(reports) == 0 {
			return nil, err
		}
		return &reports[0], nil
	}

	var status *sdkspot.OrderStatusInfo
	var err error
	if query.VenueOrderID != "" {
		oid, parseErr := parseVenueOrderID(query.VenueOrderID)
		if parseErr != nil {
			return nil, parseErr
		}
		status, err = c.rest.OrderStatus(ctx, c.rest.AccountAddr, oid)
	} else {
		venueCloid := c.ids.VenueCloidForClient(query.ClientID)
		if venueCloid == "" {
			venueCloid = c.ids.VenueCloid(query.ClientID)
		}
		status, err = c.rest.OrderStatusByCloid(ctx, c.rest.AccountAddr, venueCloid)
	}
	if errors.Is(err, sdk.ErrOrderNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if status == nil || status.Oid <= 0 {
		return nil, fmt.Errorf("hyperliquid spot: exact order status returned invalid oid")
	}
	if query.ClientID != "" && query.VenueOrderID != "" {
		expectedCloid := c.ids.VenueCloidForClient(query.ClientID)
		if expectedCloid == "" {
			expectedCloid = cloid.ForClientID(query.ClientID)
		}
		if status.Cliod == "" || !strings.EqualFold(status.Cliod, expectedCloid) {
			return nil, fmt.Errorf("hyperliquid spot: exact order status client identity mismatch: client_id=%q expected_cloid=%q got_cloid=%q", query.ClientID, expectedCloid, status.Cliod)
		}
	}
	id := query.InstrumentID
	if id.Symbol == "" {
		var resolved bool
		id, resolved = c.provider.ResolveVenueSymbol(status.Coin)
		if !resolved {
			return nil, fmt.Errorf("hyperliquid spot: exact order status returned unknown coin %q", status.Coin)
		}
	} else {
		inst, instrumentErr := c.instrument(id)
		if instrumentErr != nil {
			return nil, instrumentErr
		}
		if inst.VenueSymbol != status.Coin {
			return nil, fmt.Errorf("hyperliquid spot: exact order status instrument mismatch: requested %q, got %q", inst.VenueSymbol, status.Coin)
		}
	}
	clientID := query.ClientID
	venueOrderID := strconv.FormatInt(status.Oid, 10)
	if clientID == "" {
		clientID = c.ids.ClientID(status.Cliod, venueOrderID)
	}
	order := orderFromStatusInfo(status, id, accountID, clientID)
	order = c.rememberOrder(order)
	report := model.OrderStatusReport{Venue: venueName, AccountID: accountID, Order: order, ReportedAt: c.clk.Now()}
	return &report, nil
}

func orderFromStatusInfo(status *sdkspot.OrderStatusInfo, id model.InstrumentID, accountID, clientID string) model.Order {
	if status == nil {
		return model.Order{Request: model.OrderRequest{AccountID: accountID, InstrumentID: id, ClientID: clientID, PositionSide: enums.PosNet}}
	}
	updatedAt := parseMillis(status.StatusTimestamp)
	if updatedAt.IsZero() {
		updatedAt = parseMillis(status.Timestamp)
	}
	orderType, tif, triggerPrice := ordersemantics.FromWire(status.OrderType, status.Tif, status.IsTrigger, status.TriggerPx)
	return model.Order{
		Request: model.OrderRequest{
			AccountID: accountID, InstrumentID: id, ClientID: clientID,
			Side: sideFromHL(status.Side), Type: orderType, TIF: tif,
			Quantity: dec(status.OrigSz), Price: dec(status.LimitPx), TriggerPrice: triggerPrice,
			PositionSide: enums.PosNet, ReduceOnly: status.ReduceOnly,
		},
		VenueOrderID: strconv.FormatInt(status.Oid, 10),
		Status:       statusFromHL(status.Status), FilledQty: dec(status.FilledSz), AvgFillPrice: dec(status.AvgPx),
		CreatedAt: parseMillis(status.Timestamp), UpdatedAt: updatedAt,
	}
}

func (c *executionClient) GenerateFillReports(ctx context.Context, query model.FillReportQuery) ([]model.FillReport, error) {
	accountID, ok := c.scopedReportAccountID(query.AccountID)
	if !ok {
		return nil, nil
	}
	query.AccountID = accountID
	venueOrderID := query.VenueOrderID
	if venueOrderID == "" && query.ClientID != "" {
		venueOrderID = c.ids.VenueOrderIDForClient(query.ClientID)
		if venueOrderID == "" {
			report, err := c.GenerateOrderStatusReport(ctx, model.SingleOrderStatusQuery{
				AccountID: accountID, InstrumentID: query.InstrumentID, ClientID: query.ClientID,
			})
			if err != nil {
				return nil, err
			}
			if report == nil {
				return nil, nil
			}
			venueOrderID = report.Order.VenueOrderID
		}
	}
	fills, err := c.rest.UserFills(ctx, c.rest.AccountAddr)
	if err != nil {
		return nil, err
	}
	now := c.clk.Now()
	out := make([]model.FillReport, 0)
	for i := range fills {
		venueID := strconv.FormatInt(fills[i].Oid, 10)
		if venueOrderID != "" && venueID != venueOrderID {
			continue
		}
		id, resolved := c.provider.ResolveVenueSymbol(fills[i].Coin)
		if !resolved {
			continue
		}
		liquidity := enums.LiqMaker
		if fills[i].Crossed {
			liquidity = enums.LiqTaker
		}
		fill := model.Fill{
			AccountID: accountID, InstrumentID: id, VenueOrderID: venueID,
			ClientID: c.ids.ClientID("", venueID), TradeID: strconv.FormatInt(fills[i].Tid, 10),
			Side: sideFromHL(fills[i].Side), Liquidity: liquidity, Price: dec(fills[i].Px),
			Quantity: dec(fills[i].Sz), Fee: dec(fills[i].Fee), FeeCurrency: fills[i].FeeToken,
			Timestamp: parseMillis(fills[i].Time),
		}
		if fill.ClientID == "" && query.ClientID != "" && venueID == venueOrderID {
			fill.ClientID = query.ClientID
		}
		if !model.FillMatchesReportQuery(fill, query) || (!query.Since.IsZero() && fill.Timestamp.Before(query.Since)) || (!query.Until.IsZero() && fill.Timestamp.After(query.Until)) {
			continue
		}
		out = append(out, model.FillReport{Venue: venueName, AccountID: accountID, Fill: fill, ReportedAt: now})
		if query.Limit > 0 && len(out) >= query.Limit {
			break
		}
	}
	return out, nil
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
	c.emitWithFlags(ev, 0)
}

func (c *executionClient) emitWithFlags(ev contract.ExecEvent, flags contract.EventFlags) {
	ev = c.normalizeExecEvent(ev)
	c.stream.Emit(contract.NewExecEnvelopeWithMeta(ev, contract.EventMeta{
		Source: contract.SourceAdapterStream,
		Flags:  contract.EventFlagFromStream | flags,
	}))
}

func (c *executionClient) rememberOrder(order model.Order) model.Order {
	order = c.normalizeOrderIdentity(order)
	if order.VenueOrderID == "" {
		return order
	}
	c.mu.Lock()
	if known, ok := c.orders[order.VenueOrderID]; ok {
		order.Request = ordersemantics.MergeKnownRequest(known.Request, order.Request)
	}
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
