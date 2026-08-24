package factoryclient

import (
	"context"
	"strconv"
	"strings"
	"sync"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/sdk/okx"
	"github.com/shopspring/decimal"
)

type okxPrivateWebSocketClient interface {
	Connect() error
	Close()
	SetReconnectHooks(func(error), func())
	SubscribeOrdersWithError(string, *string, func(*okx.Order), func(error)) error
	SubscribeAccountWithError(func(*okx.Balance), func(error)) error
	SubscribePositionsWithError(string, func(*okx.Position), func(error)) error
	PlaceOrderWS(*okx.OrderRequest) (*okx.OrderId, error)
	CancelOrderWS(int64, *string, *string) (*okx.OrderId, error)
	Unsubscribe(okx.WsSubscribeArgs) error
}

type okxInstrumentCodeLoader func(context.Context, string, exchange.Instrument) (int64, error)
type okxSpotTradeModeLoader func(context.Context, string) (string, error)

type okxPrivateWSBackend struct {
	meta           clientMeta
	ws             okxPrivateWebSocketClient
	instrumentCode okxInstrumentCodeLoader
	spotTradeMode  okxSpotTradeModeLoader
	perpMeta       okxPerpMetaLoader

	closeOnce sync.Once

	mu             sync.Mutex
	connected      bool
	connecting     bool
	connectDone    chan struct{}
	connectErr     error
	closed         bool
	generation     uint64
	statusHandlers map[*okxWSStatusRegistration]struct{}

	orderStreams    map[string]*okxPrivateOrderStream
	accountStream   *okxPrivateAccountStream
	positionsStream *okxPrivatePositionsStream
}

type okxPrivateOrderStream struct {
	instrument string
	instType   string
	orders     map[*okxPrivateOrderRegistration]struct{}
	fills      map[*okxPrivateFillRegistration]struct{}
}

type okxPrivateOrderRegistration struct {
	callbacks streamCallbacks[exchange.OrderEvent]
	status    *okxWSStatusRegistration
}

type okxPrivateFillRegistration struct {
	callbacks streamCallbacks[exchange.FillEvent]
	status    *okxWSStatusRegistration
}

type okxPrivateAccountStream struct {
	registrations map[*okxPrivateBalanceRegistration]struct{}
}

type okxPrivateBalanceRegistration struct {
	callbacks streamCallbacks[exchange.BalanceEvent]
	status    *okxWSStatusRegistration
}

type okxPrivatePositionsStream struct {
	instType      string
	registrations map[*okxPrivatePositionRegistration]struct{}
}

type okxPrivatePositionRegistration struct {
	instrument string
	callbacks  streamCallbacks[exchange.PositionEvent]
	status     *okxWSStatusRegistration
}

func newOKXSpotPrivateWSBackend(
	meta clientMeta,
	ws *okx.WSClient,
	instrumentCode okxInstrumentCodeLoader,
	tradeMode okxSpotTradeModeLoader,
) privateWSBackend {
	return newOKXSpotPrivateWSBackendWithClient(meta, ws, instrumentCode, tradeMode)
}

func newOKXPerpPrivateWSBackend(
	clientMeta clientMeta,
	ws *okx.WSClient,
	instrumentCode okxInstrumentCodeLoader,
	perpMeta okxPerpMetaLoader,
) perpPrivateWSBackend {
	return newOKXPerpPrivateWSBackendWithClient(clientMeta, ws, instrumentCode, perpMeta)
}

func newOKXSpotPrivateWSBackendWithClient(
	meta clientMeta,
	ws okxPrivateWebSocketClient,
	instrumentCode okxInstrumentCodeLoader,
	tradeMode okxSpotTradeModeLoader,
) privateWSBackend {
	return &okxPrivateWSBackend{
		meta:           meta,
		ws:             ws,
		instrumentCode: instrumentCode,
		spotTradeMode:  tradeMode,
		statusHandlers: make(map[*okxWSStatusRegistration]struct{}),
		orderStreams:   make(map[string]*okxPrivateOrderStream),
	}
}

func newOKXPerpPrivateWSBackendWithClient(
	meta clientMeta,
	ws okxPrivateWebSocketClient,
	instrumentCode okxInstrumentCodeLoader,
	perpMeta okxPerpMetaLoader,
) perpPrivateWSBackend {
	return &okxPrivateWSBackend{
		meta:           meta,
		ws:             ws,
		instrumentCode: instrumentCode,
		perpMeta:       perpMeta,
		statusHandlers: make(map[*okxWSStatusRegistration]struct{}),
		orderStreams:   make(map[string]*okxPrivateOrderStream),
	}
}

func (backend *okxPrivateWSBackend) StartOrders(
	ctx context.Context,
	instrument exchange.Instrument,
	callbacks streamCallbacks[exchange.OrderEvent],
) (func() error, error) {
	if err := backend.validateInstrument("WatchOrders", instrument); err != nil {
		return nil, err
	}
	if err := backend.ensureConnected(ctx, "WatchOrders"); err != nil {
		return nil, err
	}
	registration := &okxPrivateOrderRegistration{
		callbacks: callbacks,
		status:    backend.registerStatus(callbacks.Status),
	}
	native := instrument.Identity.NativeSymbol
	stream, first := backend.addOrderRegistration(native, registration, nil)
	if first {
		if err := backend.subscribeOrders(native, stream.instType); err != nil {
			backend.removeOrderRegistration(native, registration, nil)
			return nil, err
		}
	}
	return func() error {
		return backend.removeOrderRegistration(native, registration, nil)
	}, nil
}

func (backend *okxPrivateWSBackend) StartFills(
	ctx context.Context,
	instrument exchange.Instrument,
	callbacks streamCallbacks[exchange.FillEvent],
) (func() error, error) {
	if err := backend.validateInstrument("WatchFills", instrument); err != nil {
		return nil, err
	}
	if err := backend.ensureConnected(ctx, "WatchFills"); err != nil {
		return nil, err
	}
	registration := &okxPrivateFillRegistration{
		callbacks: callbacks,
		status:    backend.registerStatus(callbacks.Status),
	}
	native := instrument.Identity.NativeSymbol
	stream, first := backend.addOrderRegistration(native, nil, registration)
	if first {
		if err := backend.subscribeOrders(native, stream.instType); err != nil {
			backend.removeOrderRegistration(native, nil, registration)
			return nil, err
		}
	}
	return func() error {
		return backend.removeOrderRegistration(native, nil, registration)
	}, nil
}

func (backend *okxPrivateWSBackend) StartBalances(
	ctx context.Context,
	callbacks streamCallbacks[exchange.BalanceEvent],
) (func() error, error) {
	if err := backend.ensureConnected(ctx, "WatchBalances"); err != nil {
		return nil, err
	}
	registration := &okxPrivateBalanceRegistration{
		callbacks: callbacks,
		status:    backend.registerStatus(callbacks.Status),
	}
	first := backend.addBalanceRegistration(registration)
	if first {
		if err := backend.ws.SubscribeAccountWithError(
			func(balance *okx.Balance) {
				backend.emitBalance(balance)
			},
			func(err error) {
				backend.emitBalanceError(okxWSError(backend.meta, "WatchBalances", exchange.KindMalformedResponse, err.Error()))
			},
		); err != nil {
			backend.removeBalanceRegistration(registration)
			return nil, okxWSError(backend.meta, "WatchBalances", exchange.KindTransport, err.Error())
		}
	}
	return func() error {
		return backend.removeBalanceRegistration(registration)
	}, nil
}

func (backend *okxPrivateWSBackend) StartPositions(
	ctx context.Context,
	instrument exchange.Instrument,
	callbacks streamCallbacks[exchange.PositionEvent],
) (func() error, error) {
	if backend.meta.product != exchange.ProductPerp {
		return nil, okxWSError(backend.meta, "WatchPositions", exchange.KindInvalidConfig, "positions are available only for perpetual products")
	}
	if err := okxValidateSwapInstrument(instrument); err != nil {
		return nil, okxWSError(backend.meta, "WatchPositions", exchange.KindInvalidRequest, err.Error())
	}
	if err := backend.ensureConnected(ctx, "WatchPositions"); err != nil {
		return nil, err
	}
	native := instrument.Identity.NativeSymbol
	registration := &okxPrivatePositionRegistration{
		instrument: native,
		callbacks:  callbacks,
		status:     backend.registerStatus(callbacks.Status),
	}
	first := backend.addPositionRegistration(registration)
	if first {
		if err := backend.ws.SubscribePositionsWithError(
			okxSwapType,
			func(position *okx.Position) {
				backend.emitPosition(position)
			},
			func(err error) {
				backend.emitPositionError(okxWSError(backend.meta, "WatchPositions", exchange.KindMalformedResponse, err.Error()))
			},
		); err != nil {
			backend.removePositionRegistration(registration)
			return nil, okxWSError(backend.meta, "WatchPositions", exchange.KindTransport, err.Error())
		}
	}
	return func() error {
		return backend.removePositionRegistration(registration)
	}, nil
}

func (backend *okxPrivateWSBackend) PlaceOrder(
	ctx context.Context,
	req exchange.PlaceOrderRequest,
) (exchange.OrderAcknowledgement, error) {
	const operation = "PlaceOrder"
	if err := backend.validateCommand(ctx, operation, req.Instrument); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	native, err := backend.placeRequest(ctx, operation, req)
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	row, err := backend.ws.PlaceOrderWS(native)
	if err != nil {
		return okxCommandTransportAck(backend.meta.product, exchange.OrderOperationPlace, req.Instrument, "", req.ClientOrderID, err)
	}
	rows := []okx.OrderId{}
	if row != nil {
		rows = append(rows, *row)
	}
	ack, err := okxCommandAck(backend.meta.product, operation, exchange.OrderOperationPlace, req.Instrument, "", req.ClientOrderID, rows)
	ack.OrderType = req.Type
	if err != nil {
		return ack, err
	}
	return ack, ack.Validate()
}

func (backend *okxPrivateWSBackend) CancelOrder(
	ctx context.Context,
	req exchange.CancelOrderRequest,
) (exchange.OrderAcknowledgement, error) {
	const operation = "CancelOrder"
	if err := backend.validateCommand(ctx, operation, req.Instrument); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	if err := okxValidateCancel(backend.meta.product, req); err != nil {
		return exchange.OrderAcknowledgement{}, okxWSError(backend.meta, operation, exchange.KindInvalidRequest, err.Error())
	}
	instIDCode, err := backend.instrumentCode(ctx, operation, req.Instrument)
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	row, err := backend.ws.CancelOrderWS(instIDCode, okxStringPtr(req.OrderID), nil)
	if err != nil {
		return okxCommandTransportAck(backend.meta.product, exchange.OrderOperationCancel, req.Instrument, req.OrderID, "", err)
	}
	rows := []okx.OrderId{}
	if row != nil {
		rows = append(rows, *row)
	}
	return okxCommandAck(backend.meta.product, operation, exchange.OrderOperationCancel, req.Instrument, req.OrderID, "", rows)
}

func (backend *okxPrivateWSBackend) Close() error {
	if backend == nil {
		return nil
	}
	backend.closeOnce.Do(func() {
		backend.mu.Lock()
		backend.closed = true
		backend.mu.Unlock()
		if backend.ws != nil {
			backend.ws.Close()
		}
	})
	return nil
}

func (backend *okxPrivateWSBackend) addOrderRegistration(
	instrument string,
	order *okxPrivateOrderRegistration,
	fill *okxPrivateFillRegistration,
) (*okxPrivateOrderStream, bool) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	stream := backend.orderStreams[instrument]
	first := stream == nil
	if stream == nil {
		stream = &okxPrivateOrderStream{
			instrument: instrument,
			instType:   backend.instType(),
			orders:     make(map[*okxPrivateOrderRegistration]struct{}),
			fills:      make(map[*okxPrivateFillRegistration]struct{}),
		}
		backend.orderStreams[instrument] = stream
	}
	if order != nil {
		stream.orders[order] = struct{}{}
	}
	if fill != nil {
		stream.fills[fill] = struct{}{}
	}
	return stream, first
}

func (backend *okxPrivateWSBackend) removeOrderRegistration(
	instrument string,
	order *okxPrivateOrderRegistration,
	fill *okxPrivateFillRegistration,
) error {
	var unsubscribe bool
	var instType string
	if order != nil {
		backend.unregisterStatus(order.status)
	}
	if fill != nil {
		backend.unregisterStatus(fill.status)
	}
	backend.mu.Lock()
	stream := backend.orderStreams[instrument]
	if stream != nil {
		if order != nil {
			delete(stream.orders, order)
		}
		if fill != nil {
			delete(stream.fills, fill)
		}
		if len(stream.orders) == 0 && len(stream.fills) == 0 {
			delete(backend.orderStreams, instrument)
			unsubscribe = true
			instType = stream.instType
		}
	}
	backend.mu.Unlock()
	if unsubscribe {
		return backend.unsubscribe("WatchOrders", okx.WsSubscribeArgs{Channel: "orders", InstType: instType, InstId: instrument})
	}
	return nil
}

func (backend *okxPrivateWSBackend) subscribeOrders(instrument, instType string) error {
	if err := backend.ws.SubscribeOrdersWithError(
		instType,
		&instrument,
		func(order *okx.Order) {
			backend.emitOrder(order)
		},
		func(err error) {
			backend.emitOrderError(instrument, okxWSError(backend.meta, "WatchOrders", exchange.KindMalformedResponse, err.Error()))
		},
	); err != nil {
		return okxWSError(backend.meta, "WatchOrders", exchange.KindTransport, err.Error())
	}
	return nil
}

func (backend *okxPrivateWSBackend) addBalanceRegistration(registration *okxPrivateBalanceRegistration) bool {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	first := backend.accountStream == nil
	if backend.accountStream == nil {
		backend.accountStream = &okxPrivateAccountStream{registrations: make(map[*okxPrivateBalanceRegistration]struct{})}
	}
	backend.accountStream.registrations[registration] = struct{}{}
	return first
}

func (backend *okxPrivateWSBackend) removeBalanceRegistration(registration *okxPrivateBalanceRegistration) error {
	backend.unregisterStatus(registration.status)
	var unsubscribe bool
	backend.mu.Lock()
	if backend.accountStream != nil {
		delete(backend.accountStream.registrations, registration)
		if len(backend.accountStream.registrations) == 0 {
			backend.accountStream = nil
			unsubscribe = true
		}
	}
	backend.mu.Unlock()
	if unsubscribe {
		return backend.unsubscribe("WatchBalances", okx.WsSubscribeArgs{Channel: "account"})
	}
	return nil
}

func (backend *okxPrivateWSBackend) addPositionRegistration(registration *okxPrivatePositionRegistration) bool {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	first := backend.positionsStream == nil
	if backend.positionsStream == nil {
		backend.positionsStream = &okxPrivatePositionsStream{
			instType:      okxSwapType,
			registrations: make(map[*okxPrivatePositionRegistration]struct{}),
		}
	}
	backend.positionsStream.registrations[registration] = struct{}{}
	return first
}

func (backend *okxPrivateWSBackend) removePositionRegistration(registration *okxPrivatePositionRegistration) error {
	backend.unregisterStatus(registration.status)
	var unsubscribe bool
	var instType string
	backend.mu.Lock()
	if backend.positionsStream != nil {
		delete(backend.positionsStream.registrations, registration)
		if len(backend.positionsStream.registrations) == 0 {
			instType = backend.positionsStream.instType
			backend.positionsStream = nil
			unsubscribe = true
		}
	}
	backend.mu.Unlock()
	if unsubscribe {
		return backend.unsubscribe("WatchPositions", okx.WsSubscribeArgs{Channel: "positions", InstType: instType})
	}
	return nil
}

func (backend *okxPrivateWSBackend) emitOrder(row *okx.Order) {
	if row == nil {
		backend.emitOrderError("", okxWSError(backend.meta, "WatchOrders", exchange.KindMalformedResponse, "order push is nil"))
		return
	}
	operation := "WatchOrders"
	streams := backend.orderStreamSnapshot(row.InstId)
	if len(streams.orders) == 0 && len(streams.fills) == 0 {
		return
	}
	if row.InstType != backend.instType() {
		backend.emitOrderError(row.InstId, okxWSError(backend.meta, operation, exchange.KindMalformedResponse, "order product mismatch"))
		return
	}
	multiplier, err := backend.orderMultiplier(context.Background(), operation, row.InstId)
	if err != nil {
		backend.emitOrderError(row.InstId, err)
		return
	}
	order, err := okxOrder(*row, multiplier)
	if err != nil {
		backend.emitOrderError(row.InstId, okxWSError(backend.meta, operation, exchange.KindMalformedResponse, err.Error()))
		return
	}
	event := exchange.OrderEvent{Kind: exchange.EventDelta, Order: order}
	for _, registration := range streams.orders {
		registration.callbacks.Event(event)
	}
	if strings.TrimSpace(row.TradeId) == "" || strings.TrimSpace(row.FillSz) == "" {
		return
	}
	fill, err := okxFill(okx.Fill{
		InstType: row.InstType,
		InstId:   row.InstId,
		TradeId:  row.TradeId,
		OrdId:    row.OrdId,
		ClOrdId:  row.ClOrdId,
		Side:     row.Side,
		PosSide:  row.PosSide,
		FillPx:   row.FillPx,
		FillSz:   row.FillSz,
		Fee:      row.Fee,
		FeeCcy:   row.FeeCcy,
		ExecType: row.ExecType,
		Ts:       row.FillTime,
	}, multiplier)
	if err != nil {
		backend.emitFillError(row.InstId, okxWSError(backend.meta, "WatchFills", exchange.KindMalformedResponse, err.Error()))
		return
	}
	fillEvent := exchange.FillEvent{Kind: exchange.EventDelta, Fill: fill}
	for _, registration := range streams.fills {
		registration.callbacks.Event(fillEvent)
	}
}

type okxPrivateOrderSnapshot struct {
	orders []*okxPrivateOrderRegistration
	fills  []*okxPrivateFillRegistration
}

func (backend *okxPrivateWSBackend) orderStreamSnapshot(instrument string) okxPrivateOrderSnapshot {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	stream := backend.orderStreams[instrument]
	if stream == nil {
		return okxPrivateOrderSnapshot{}
	}
	out := okxPrivateOrderSnapshot{
		orders: make([]*okxPrivateOrderRegistration, 0, len(stream.orders)),
		fills:  make([]*okxPrivateFillRegistration, 0, len(stream.fills)),
	}
	for registration := range stream.orders {
		out.orders = append(out.orders, registration)
	}
	for registration := range stream.fills {
		out.fills = append(out.fills, registration)
	}
	return out
}

func (backend *okxPrivateWSBackend) emitOrderError(instrument string, err error) {
	streams := backend.orderStreamSnapshot(instrument)
	for _, registration := range streams.orders {
		if registration.callbacks.Error != nil {
			registration.callbacks.Error(err)
		}
	}
	for _, registration := range streams.fills {
		if registration.callbacks.Error != nil {
			registration.callbacks.Error(err)
		}
	}
}

func (backend *okxPrivateWSBackend) emitFillError(instrument string, err error) {
	streams := backend.orderStreamSnapshot(instrument)
	for _, registration := range streams.fills {
		if registration.callbacks.Error != nil {
			registration.callbacks.Error(err)
		}
	}
}

func (backend *okxPrivateWSBackend) emitBalance(row *okx.Balance) {
	if row == nil {
		backend.emitBalanceError(okxWSError(backend.meta, "WatchBalances", exchange.KindMalformedResponse, "account push is nil"))
		return
	}
	balances, err := okxBalances(backend.meta.product, "WatchBalances", []okx.Balance{*row})
	if err != nil {
		backend.emitBalanceError(err)
		return
	}
	event := exchange.BalanceEvent{Kind: exchange.EventSnapshot, Balances: balances}
	if strings.TrimSpace(row.UTime) != "" {
		if at, err := okxOptionalMillis(row.UTime); err == nil {
			event.Time = at
		}
	}
	registrations := backend.balanceSnapshot()
	for _, registration := range registrations {
		registration.callbacks.Event(event)
	}
}

func (backend *okxPrivateWSBackend) balanceSnapshot() []*okxPrivateBalanceRegistration {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.accountStream == nil {
		return nil
	}
	out := make([]*okxPrivateBalanceRegistration, 0, len(backend.accountStream.registrations))
	for registration := range backend.accountStream.registrations {
		out = append(out, registration)
	}
	return out
}

func (backend *okxPrivateWSBackend) emitBalanceError(err error) {
	for _, registration := range backend.balanceSnapshot() {
		if registration.callbacks.Error != nil {
			registration.callbacks.Error(err)
		}
	}
}

func (backend *okxPrivateWSBackend) emitPosition(row *okx.Position) {
	if row == nil {
		backend.emitPositionError(okxWSError(backend.meta, "WatchPositions", exchange.KindMalformedResponse, "position push is nil"))
		return
	}
	if row.InstType != okxSwapType {
		backend.emitPositionError(okxWSError(backend.meta, "WatchPositions", exchange.KindMalformedResponse, "position product mismatch"))
		return
	}
	meta, err := backend.orderMultiplier(context.Background(), "WatchPositions", row.InstId)
	if err != nil {
		backend.emitPositionError(err)
		return
	}
	position, err := okxPosition(*row, meta)
	if err != nil {
		backend.emitPositionError(okxWSError(backend.meta, "WatchPositions", exchange.KindMalformedResponse, err.Error()))
		return
	}
	// OKX pushes one changed position row at a time. Calling it a snapshot
	// would let an account projection delete the opposite hedge slot merely
	// because that unchanged row was absent from this message.
	event := exchange.PositionEvent{Kind: exchange.EventDelta, Positions: []exchange.Position{position}}
	if at, err := okxOptionalMillis(row.UTime); err == nil {
		event.Time = at
	}
	for _, registration := range backend.positionSnapshot(row.InstId) {
		registration.callbacks.Event(event)
	}
}

func (backend *okxPrivateWSBackend) positionSnapshot(instrument string) []*okxPrivatePositionRegistration {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.positionsStream == nil {
		return nil
	}
	out := make([]*okxPrivatePositionRegistration, 0, len(backend.positionsStream.registrations))
	for registration := range backend.positionsStream.registrations {
		if registration.instrument == instrument {
			out = append(out, registration)
		}
	}
	return out
}

func (backend *okxPrivateWSBackend) emitPositionError(err error) {
	backend.mu.Lock()
	var registrations []*okxPrivatePositionRegistration
	if backend.positionsStream != nil {
		registrations = make([]*okxPrivatePositionRegistration, 0, len(backend.positionsStream.registrations))
		for registration := range backend.positionsStream.registrations {
			registrations = append(registrations, registration)
		}
	}
	backend.mu.Unlock()
	for _, registration := range registrations {
		if registration.callbacks.Error != nil {
			registration.callbacks.Error(err)
		}
	}
}

func (backend *okxPrivateWSBackend) validateCommand(ctx context.Context, operation string, instrument exchange.Instrument) error {
	if err := backend.validateInstrument(operation, instrument); err != nil {
		return err
	}
	return backend.ensureConnected(ctx, operation)
}

func (backend *okxPrivateWSBackend) validateInstrument(operation string, instrument exchange.Instrument) error {
	if backend.meta.product == exchange.ProductSpot {
		if err := okxValidateSpotInstrument(instrument); err != nil {
			return okxWSError(backend.meta, operation, exchange.KindInvalidRequest, err.Error())
		}
		return nil
	}
	if err := okxValidateSwapInstrument(instrument); err != nil {
		return okxWSError(backend.meta, operation, exchange.KindInvalidRequest, err.Error())
	}
	return nil
}

func (backend *okxPrivateWSBackend) placeRequest(
	ctx context.Context,
	operation string,
	req exchange.PlaceOrderRequest,
) (*okx.OrderRequest, error) {
	if err := req.Validate(backend.meta.product); err != nil {
		return nil, okxWSError(backend.meta, operation, exchange.KindInvalidRequest, err.Error())
	}
	instIDCode, err := backend.instrumentCode(ctx, operation, req.Instrument)
	if err != nil {
		return nil, err
	}
	ordType, px := okxOrderRequestShape(req)
	native := &okx.OrderRequest{
		InstId:     req.Instrument.Identity.NativeSymbol,
		InstIdCode: &instIDCode,
		ClOrdId:    okxStringPtr(req.ClientOrderID),
		Side:       string(req.Side),
		OrdType:    ordType,
		Px:         px,
	}
	if backend.meta.product == exchange.ProductSpot {
		tradeMode := okxCashMode
		if backend.spotTradeMode != nil {
			loaded, err := backend.spotTradeMode(ctx, operation)
			if err != nil {
				return nil, err
			}
			tradeMode = loaded
		}
		native.TdMode = tradeMode
		native.Sz = req.Quantity.String()
		if req.Type == exchange.OrderTypeMarket {
			value := "base_ccy"
			native.TgtCcy = &value
		}
		return native, nil
	}
	if backend.perpMeta == nil {
		return nil, okxWSError(backend.meta, operation, exchange.KindInvalidConfig, "OKX perp metadata loader is not configured")
	}
	meta, err := backend.perpMeta(ctx, req.Instrument)
	if err != nil {
		return nil, err
	}
	contracts := req.Quantity.Div(meta.contractValue)
	if !contracts.Mod(meta.contractIncrement).IsZero() {
		return nil, okxWSError(backend.meta, operation, exchange.KindInvalidRequest, "quantity must align to OKX contract lot size")
	}
	native.TdMode = okxCrossMode
	native.Sz = contracts.String()
	native.ReduceOnly = &req.ReduceOnly
	return native, nil
}

func (backend *okxPrivateWSBackend) orderMultiplier(
	ctx context.Context,
	operation string,
	instrument string,
) (decimal.Decimal, error) {
	if backend.meta.product == exchange.ProductSpot {
		return decimal.NewFromInt(1), nil
	}
	if backend.perpMeta == nil {
		return decimal.Zero, okxWSError(backend.meta, operation, exchange.KindInvalidConfig, "OKX perp metadata loader is not configured")
	}
	meta, err := backend.perpMetaByNative(ctx, operation, instrument)
	if err != nil {
		return decimal.Zero, err
	}
	return meta.contractValue, nil
}

func (backend *okxPrivateWSBackend) perpMetaByNative(ctx context.Context, operation, nativeSymbol string) (okxContractMeta, error) {
	if backend.meta.catalog == nil || backend.meta.catalogScope == (exchange.CatalogScope{}) {
		return okxContractMeta{}, okxWSError(backend.meta, operation, exchange.KindMetadataResolution, "instrument catalog is not configured")
	}
	snapshot, err := backend.meta.catalog.Snapshot(ctx, backend.meta.catalogScope)
	if err != nil {
		return okxContractMeta{}, withExchangeOperation(err, operation)
	}
	var instrument exchange.Instrument
	found := false
	for _, candidate := range snapshot.Instruments {
		if candidate.Identity.NativeSymbol == nativeSymbol {
			instrument = candidate
			found = true
			break
		}
	}
	if !found {
		return okxContractMeta{}, okxWSError(backend.meta, operation, exchange.KindMetadataResolution, "instrument metadata not found")
	}
	meta, err := okxContractMetaFromInstrument(instrument)
	if err != nil {
		return okxContractMeta{}, okxWSError(backend.meta, operation, exchange.KindMalformedResponse, err.Error())
	}
	return meta, nil
}

func (backend *okxPrivateWSBackend) ensureConnected(ctx context.Context, operation string) error {
	if backend == nil || backend.ws == nil {
		return okxWSError(socketMeta(nil), operation, exchange.KindInvalidConfig, "OKX private websocket client is not configured")
	}
	if ctx == nil {
		return okxWSError(backend.meta, operation, exchange.KindInvalidRequest, "context must not be nil")
	}
	if err := ctx.Err(); err != nil {
		return websocketContextError(backend.meta, operation, err)
	}
	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return okxWSError(backend.meta, operation, exchange.KindSubscriptionClosed, "websocket client is closed")
	}
	if backend.connected {
		backend.mu.Unlock()
		return nil
	}
	if backend.connecting {
		done := backend.connectDone
		backend.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
			return websocketContextError(backend.meta, operation, ctx.Err())
		}
		backend.mu.Lock()
		closed := backend.closed
		connected := backend.connected
		connectErr := backend.connectErr
		backend.mu.Unlock()
		if closed {
			return okxWSError(backend.meta, operation, exchange.KindSubscriptionClosed, "websocket client is closed")
		}
		if connected {
			return nil
		}
		if connectErr != nil {
			return okxWSError(backend.meta, operation, exchange.KindTransport, "websocket connect failed")
		}
		return okxWSError(backend.meta, operation, exchange.KindTransport, "websocket connection failed")
	}
	backend.connecting = true
	backend.connectDone = make(chan struct{})
	connectDone := backend.connectDone
	backend.mu.Unlock()

	backend.ws.SetReconnectHooks(backend.reconnectStarted, backend.reconnectRecovered)
	err := backend.ws.Connect()

	backend.mu.Lock()
	backend.connecting = false
	backend.connectErr = err
	if err == nil && !backend.closed {
		backend.connected = true
	}
	closed := backend.closed
	close(connectDone)
	backend.mu.Unlock()
	if closed {
		return okxWSError(backend.meta, operation, exchange.KindSubscriptionClosed, "websocket client is closed")
	}
	if err != nil {
		return okxWSError(backend.meta, operation, exchange.KindTransport, "websocket connect failed")
	}
	return nil
}

func (backend *okxPrivateWSBackend) reconnectStarted(err error) {
	reason := "OKX private websocket disconnected"
	if err != nil && strings.TrimSpace(err.Error()) != "" {
		reason = err.Error()
	}
	backend.mu.Lock()
	backend.generation++
	generation := backend.generation
	backend.mu.Unlock()
	backend.emitStatus(backendStatus{
		State:      exchange.SubscriptionGap,
		Phase:      exchange.GapStarted,
		Generation: generation,
		Reason:     reason,
	})
}

func (backend *okxPrivateWSBackend) reconnectRecovered() {
	backend.mu.Lock()
	generation := backend.generation
	backend.mu.Unlock()
	backend.emitStatus(backendStatus{
		State:      exchange.SubscriptionActive,
		Phase:      exchange.GapRecovered,
		Generation: generation,
		Reason:     "OKX private websocket subscriptions restored",
	})
}

func (backend *okxPrivateWSBackend) registerStatus(status func(backendStatus)) *okxWSStatusRegistration {
	registration := &okxWSStatusRegistration{status: status}
	backend.mu.Lock()
	backend.statusHandlers[registration] = struct{}{}
	backend.mu.Unlock()
	return registration
}

func (backend *okxPrivateWSBackend) unregisterStatus(registration *okxWSStatusRegistration) {
	if registration == nil {
		return
	}
	backend.mu.Lock()
	delete(backend.statusHandlers, registration)
	backend.mu.Unlock()
}

func (backend *okxPrivateWSBackend) emitStatus(status backendStatus) {
	backend.mu.Lock()
	handlers := make([]func(backendStatus), 0, len(backend.statusHandlers))
	for registration := range backend.statusHandlers {
		if registration.status != nil {
			handlers = append(handlers, registration.status)
		}
	}
	backend.mu.Unlock()
	for _, handler := range handlers {
		handler(status)
	}
}

func (backend *okxPrivateWSBackend) unsubscribe(operation string, args okx.WsSubscribeArgs) error {
	if err := backend.ws.Unsubscribe(args); err != nil {
		return okxWSError(backend.meta, operation, exchange.KindTransport, err.Error())
	}
	return nil
}

func (backend *okxPrivateWSBackend) instType() string {
	if backend.meta.product == exchange.ProductPerp {
		return okxSwapType
	}
	return okxSpotType
}

func okxSpotInstrumentCodeLoader(meta clientMeta) okxInstrumentCodeLoader {
	return okxCatalogInstrumentCodeLoader(meta, exchange.ProductSpot)
}

func okxPerpInstrumentCodeLoader(meta clientMeta) okxInstrumentCodeLoader {
	return okxCatalogInstrumentCodeLoader(meta, exchange.ProductPerp)
}

func okxCatalogInstrumentCodeLoader(meta clientMeta, product exchange.Product) okxInstrumentCodeLoader {
	return func(_ context.Context, operation string, instrument exchange.Instrument) (int64, error) {
		if product == exchange.ProductSpot {
			if err := okxValidateSpotInstrument(instrument); err != nil {
				return 0, okxInvalid(product, operation, err.Error())
			}
		} else if err := okxValidateSwapInstrument(instrument); err != nil {
			return 0, okxInvalid(product, operation, err.Error())
		}
		if strings.TrimSpace(instrument.Identity.NativeID) == "" {
			return 0, okxMalformed(product, operation, "OKX instrument id code is missing")
		}
		code, err := strconv.ParseInt(instrument.Identity.NativeID, 10, 64)
		if err != nil || code <= 0 || strconv.FormatInt(code, 10) != instrument.Identity.NativeID {
			return 0, okxMalformed(product, operation, "OKX instrument id code is invalid")
		}
		return code, nil
	}
}

var _ privateWSBackend = (*okxPrivateWSBackend)(nil)
var _ perpPrivateWSBackend = (*okxPrivateWSBackend)(nil)
