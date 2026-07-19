package factoryclient

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	binanceperp "github.com/QuantProcessing/boltertrader/sdk/binance/perp"
	binancespot "github.com/QuantProcessing/boltertrader/sdk/binance/spot"
	"github.com/shopspring/decimal"
)

type binanceSpotPrivateAPI interface {
	Connect() error
	Close()
	PlaceOrderWS(string, string, binancespot.PlaceOrderParams, string) (*binancespot.OrderResponse, error)
	CancelOrderWS(string, string, string, int64, string, string) (*binancespot.OrderResponse, error)
}

type binanceSpotAccountWS interface {
	Connect() error
	Close()
	SetReconnectHooks(func(error), func())
	SubscribeExecutionReport(func(*binancespot.ExecutionReportEvent))
	SubscribeAccountPosition(func(*binancespot.AccountPositionEvent))
}

type binancePerpPrivateAPI interface {
	Connect() error
	Close()
	PlaceOrderWS(string, string, binanceperp.PlaceOrderParams, string) (*binanceperp.OrderResponse, error)
	CancelOrderWS(string, string, binanceperp.CancelOrderParams, string) (*binanceperp.OrderResponse, error)
}

type binancePerpAccountWS interface {
	Connect() error
	Close()
	SetReconnectHooks(func(error), func())
	SubscribeOrderUpdate(func(*binanceperp.OrderUpdateEvent))
	SubscribeAccountUpdate(func(*binanceperp.AccountUpdateEvent))
}

type binanceSpotPrivateWSBackend struct {
	meta      clientMeta
	api       binanceSpotPrivateAPI
	account   binanceSpotAccountWS
	apiKey    string
	secretKey string
	lifecycle *backendLifecycle

	mu               sync.Mutex
	apiConnected     bool
	accountConnected bool
	closed           bool
	closeOnce        sync.Once
	nextRouteID      uint64

	executionSubscribed bool
	balanceSubscribed   bool
	orderRoutes         map[uint64]binanceSpotOrderRoute
	fillRoutes          map[uint64]binanceSpotFillRoute
	balanceRoutes       map[uint64]binanceSpotBalanceRoute
}

type binanceSpotOrderRoute struct {
	instrument  string
	venueSymbol string
	callbacks   streamCallbacks[exchange.OrderEvent]
}

type binanceSpotFillRoute struct {
	instrument  string
	venueSymbol string
	callbacks   streamCallbacks[exchange.FillEvent]
}

type binanceSpotBalanceRoute struct {
	callbacks streamCallbacks[exchange.BalanceEvent]
}

func newBinanceSpotPrivateWSBackend(api binanceSpotPrivateAPI, account binanceSpotAccountWS, apiKey, secretKey string) privateWSBackend {
	return newBinanceSpotPrivateWSBackendForTest(api, account, apiKey, secretKey)
}

func newBinanceSpotPrivateWSBackendForTest(api binanceSpotPrivateAPI, account binanceSpotAccountWS, apiKey, secretKey string) privateWSBackend {
	backend := &binanceSpotPrivateWSBackend{
		meta:      clientMeta{venue: exchange.VenueBinance, product: exchange.ProductSpot},
		api:       api,
		account:   account,
		apiKey:    apiKey,
		secretKey: secretKey,
		lifecycle: newBackendLifecycle(),
	}
	if backend.account != nil {
		backend.account.SetReconnectHooks(
			func(err error) { backend.lifecycle.Started(err) },
			func() { backend.lifecycle.Recovered("Binance private websocket subscriptions restored") },
		)
	}
	return backend
}

func (backend *binanceSpotPrivateWSBackend) StartOrders(ctx context.Context, instrument string, callbacks streamCallbacks[exchange.OrderEvent]) (func() error, error) {
	venueSymbol, canonical, err := binanceSpotSymbols(instrument, "WatchOrders")
	if err != nil {
		return nil, err
	}
	if err := backend.ensureAccountConnected(ctx, "WatchOrders"); err != nil {
		return nil, err
	}
	removeLifecycle := backend.lifecycle.Register("spot:orders:"+venueSymbol+":"+nextBinanceWSRequestID(), callbacks.Status, nil)
	stop, err := backend.addOrderRoute("WatchOrders", binanceSpotOrderRoute{instrument: canonical, venueSymbol: venueSymbol, callbacks: callbacks}, removeLifecycle)
	if err != nil {
		removeLifecycle()
		return nil, err
	}
	return stop, nil
}

func (backend *binanceSpotPrivateWSBackend) StartFills(ctx context.Context, instrument string, callbacks streamCallbacks[exchange.FillEvent]) (func() error, error) {
	venueSymbol, canonical, err := binanceSpotSymbols(instrument, "WatchFills")
	if err != nil {
		return nil, err
	}
	if err := backend.ensureAccountConnected(ctx, "WatchFills"); err != nil {
		return nil, err
	}
	removeLifecycle := backend.lifecycle.Register("spot:fills:"+venueSymbol+":"+nextBinanceWSRequestID(), callbacks.Status, nil)
	stop, err := backend.addFillRoute("WatchFills", binanceSpotFillRoute{instrument: canonical, venueSymbol: venueSymbol, callbacks: callbacks}, removeLifecycle)
	if err != nil {
		removeLifecycle()
		return nil, err
	}
	return stop, nil
}

func (backend *binanceSpotPrivateWSBackend) StartBalances(ctx context.Context, callbacks streamCallbacks[exchange.BalanceEvent]) (func() error, error) {
	if err := backend.ensureAccountConnected(ctx, "WatchBalances"); err != nil {
		return nil, err
	}
	removeLifecycle := backend.lifecycle.Register("spot:balances:"+nextBinanceWSRequestID(), callbacks.Status, nil)
	stop, err := backend.addBalanceRoute("WatchBalances", binanceSpotBalanceRoute{callbacks: callbacks}, removeLifecycle)
	if err != nil {
		removeLifecycle()
		return nil, err
	}
	return stop, nil
}

func (backend *binanceSpotPrivateWSBackend) PlaceOrder(ctx context.Context, request exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error) {
	if err := request.Validate(exchange.ProductSpot); err != nil {
		return exchange.OrderAcknowledgement{}, binanceSpotInvalid("PlaceOrder", "invalid normalized order request")
	}
	symbol, instrument, err := binanceSpotSymbols(request.Instrument, "PlaceOrder")
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	side := "BUY"
	if request.Side == exchange.SideSell {
		side = "SELL"
	}
	if err := backend.ensureAPIConnected(ctx, "PlaceOrder"); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	res, err := backend.api.PlaceOrderWS(backend.apiKey, backend.secretKey, binanceSpotPlaceParams(symbol, side, request), nextBinanceWSRequestID())
	if err != nil {
		return binanceSpotCommandAck(instrument, exchange.OrderOperationPlace, "PlaceOrder", "", request.ClientOrderID, err)
	}
	return binanceSpotWSPlaceAck(instrument, request, res)
}

func (backend *binanceSpotPrivateWSBackend) CancelOrder(ctx context.Context, request exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error) {
	symbol, instrument, err := binanceSpotSymbols(request.Instrument, "CancelOrder")
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	orderID, err := binanceSpotOrderID(request.OrderID, "CancelOrder")
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	if err := backend.ensureAPIConnected(ctx, "CancelOrder"); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	res, err := backend.api.CancelOrderWS(backend.apiKey, backend.secretKey, symbol, orderID, "", nextBinanceWSRequestID())
	if err != nil {
		return binanceSpotCommandAck(instrument, exchange.OrderOperationCancel, "CancelOrder", request.OrderID, "", err)
	}
	return binanceSpotWSCancelAck(instrument, request, res)
}

func (backend *binanceSpotPrivateWSBackend) Close() error {
	if backend == nil {
		return nil
	}
	backend.closeOnce.Do(func() {
		backend.mu.Lock()
		backend.closed = true
		api := backend.api
		account := backend.account
		backend.mu.Unlock()
		if account != nil {
			account.Close()
		}
		if api != nil {
			api.Close()
		}
	})
	return nil
}

func (backend *binanceSpotPrivateWSBackend) ensureAPIConnected(ctx context.Context, operation string) error {
	return backend.ensureConnected(ctx, operation, "Binance websocket API client is not configured", func() bool {
		return backend.apiConnected
	}, func() {
		backend.apiConnected = true
	}, func() error {
		return backend.api.Connect()
	}, backend.api != nil)
}

func (backend *binanceSpotPrivateWSBackend) ensureAccountConnected(ctx context.Context, operation string) error {
	return backend.ensureConnected(ctx, operation, "Binance private account websocket client is not configured", func() bool {
		return backend.accountConnected
	}, func() {
		backend.accountConnected = true
	}, func() error {
		return backend.account.Connect()
	}, backend.account != nil)
}

func (backend *binanceSpotPrivateWSBackend) ensureConnected(ctx context.Context, operation, missing string, connected func() bool, mark func(), connect func() error, configured bool) error {
	if backend == nil || !configured {
		return websocketError(socketMeta(nil), operation, exchange.KindInvalidConfig, missing)
	}
	if ctx == nil {
		return websocketError(backend.meta, operation, exchange.KindInvalidRequest, "context is required")
	}
	if err := ctx.Err(); err != nil {
		return websocketContextError(backend.meta, operation, err)
	}
	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return websocketError(backend.meta, operation, exchange.KindSubscriptionClosed, "websocket client is closed")
	}
	if connected() {
		backend.mu.Unlock()
		return nil
	}
	if err := connect(); err != nil {
		backend.mu.Unlock()
		return websocketError(backend.meta, operation, exchange.KindTransport, "websocket connection failed")
	}
	if backend.closed {
		backend.mu.Unlock()
		return websocketError(backend.meta, operation, exchange.KindSubscriptionClosed, "websocket client is closed")
	}
	mark()
	backend.mu.Unlock()
	return nil
}

func (backend *binanceSpotPrivateWSBackend) addOrderRoute(operation string, route binanceSpotOrderRoute, removeLifecycle func()) (func() error, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.closed {
		return nil, websocketError(backend.meta, operation, exchange.KindSubscriptionClosed, "websocket client is closed")
	}
	if backend.orderRoutes == nil {
		backend.orderRoutes = make(map[uint64]binanceSpotOrderRoute)
	}
	if !backend.executionSubscribed {
		backend.account.SubscribeExecutionReport(backend.dispatchExecutionReport)
		backend.executionSubscribed = true
	}
	backend.nextRouteID++
	id := backend.nextRouteID
	backend.orderRoutes[id] = route
	var once sync.Once
	return func() error {
		once.Do(func() {
			backend.mu.Lock()
			delete(backend.orderRoutes, id)
			backend.mu.Unlock()
			if removeLifecycle != nil {
				removeLifecycle()
			}
		})
		return nil
	}, nil
}

func (backend *binanceSpotPrivateWSBackend) addFillRoute(operation string, route binanceSpotFillRoute, removeLifecycle func()) (func() error, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.closed {
		return nil, websocketError(backend.meta, operation, exchange.KindSubscriptionClosed, "websocket client is closed")
	}
	if backend.fillRoutes == nil {
		backend.fillRoutes = make(map[uint64]binanceSpotFillRoute)
	}
	if !backend.executionSubscribed {
		backend.account.SubscribeExecutionReport(backend.dispatchExecutionReport)
		backend.executionSubscribed = true
	}
	backend.nextRouteID++
	id := backend.nextRouteID
	backend.fillRoutes[id] = route
	var once sync.Once
	return func() error {
		once.Do(func() {
			backend.mu.Lock()
			delete(backend.fillRoutes, id)
			backend.mu.Unlock()
			if removeLifecycle != nil {
				removeLifecycle()
			}
		})
		return nil
	}, nil
}

func (backend *binanceSpotPrivateWSBackend) addBalanceRoute(operation string, route binanceSpotBalanceRoute, removeLifecycle func()) (func() error, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.closed {
		return nil, websocketError(backend.meta, operation, exchange.KindSubscriptionClosed, "websocket client is closed")
	}
	if backend.balanceRoutes == nil {
		backend.balanceRoutes = make(map[uint64]binanceSpotBalanceRoute)
	}
	if !backend.balanceSubscribed {
		backend.account.SubscribeAccountPosition(backend.dispatchAccountPosition)
		backend.balanceSubscribed = true
	}
	backend.nextRouteID++
	id := backend.nextRouteID
	backend.balanceRoutes[id] = route
	var once sync.Once
	return func() error {
		once.Do(func() {
			backend.mu.Lock()
			delete(backend.balanceRoutes, id)
			backend.mu.Unlock()
			if removeLifecycle != nil {
				removeLifecycle()
			}
		})
		return nil
	}, nil
}

func (backend *binanceSpotPrivateWSBackend) dispatchExecutionReport(event *binancespot.ExecutionReportEvent) {
	backend.mu.Lock()
	orderRoutes := make([]binanceSpotOrderRoute, 0, len(backend.orderRoutes))
	for _, route := range backend.orderRoutes {
		orderRoutes = append(orderRoutes, route)
	}
	fillRoutes := make([]binanceSpotFillRoute, 0, len(backend.fillRoutes))
	for _, route := range backend.fillRoutes {
		fillRoutes = append(fillRoutes, route)
	}
	backend.mu.Unlock()
	for _, route := range orderRoutes {
		order, err := binanceSpotWSOrderEvent(route.instrument, route.venueSymbol, event)
		if err != nil {
			if errors.Is(err, binanceSkipEvent) {
				continue
			}
			emitBinanceWSError(route.callbacks, err)
			continue
		}
		route.callbacks.Event(exchange.OrderEvent{Kind: exchange.EventDelta, Order: order})
	}
	for _, route := range fillRoutes {
		fill, ok, err := binanceSpotWSFillEvent(route.instrument, route.venueSymbol, event)
		if err != nil {
			emitBinanceWSError(route.callbacks, err)
			continue
		}
		if ok {
			route.callbacks.Event(exchange.FillEvent{Kind: exchange.EventDelta, Fill: fill})
		}
	}
}

func (backend *binanceSpotPrivateWSBackend) dispatchAccountPosition(event *binancespot.AccountPositionEvent) {
	backend.mu.Lock()
	routes := make([]binanceSpotBalanceRoute, 0, len(backend.balanceRoutes))
	for _, route := range backend.balanceRoutes {
		routes = append(routes, route)
	}
	backend.mu.Unlock()
	for _, route := range routes {
		balances, err := binanceSpotWSBalances(event)
		if err != nil {
			emitBinanceWSError(route.callbacks, err)
			continue
		}
		at := time.Now().UTC()
		if event != nil && event.EventTime > 0 {
			at = time.UnixMilli(event.EventTime).UTC()
		}
		route.callbacks.Event(exchange.BalanceEvent{Kind: exchange.EventDelta, Balances: balances, Time: at})
	}
}

type binancePerpPrivateWSBackend struct {
	meta      clientMeta
	api       binancePerpPrivateAPI
	account   binancePerpAccountWS
	apiKey    string
	secretKey string
	lifecycle *backendLifecycle

	mu               sync.Mutex
	apiConnected     bool
	accountConnected bool
	closed           bool
	closeOnce        sync.Once
	nextRouteID      uint64

	orderSubscribed   bool
	accountSubscribed bool
	orderRoutes       map[uint64]binancePerpOrderRoute
	fillRoutes        map[uint64]binancePerpFillRoute
	balanceRoutes     map[uint64]binancePerpBalanceRoute
	positionRoutes    map[uint64]binancePerpPositionRoute
}

type binancePerpOrderRoute struct {
	instrument string
	callbacks  streamCallbacks[exchange.OrderEvent]
}

type binancePerpFillRoute struct {
	instrument string
	callbacks  streamCallbacks[exchange.FillEvent]
}

type binancePerpBalanceRoute struct {
	callbacks streamCallbacks[exchange.BalanceEvent]
}

type binancePerpPositionRoute struct {
	instrument string
	callbacks  streamCallbacks[exchange.PositionEvent]
}

func newBinancePerpPrivateWSBackend(api binancePerpPrivateAPI, account binancePerpAccountWS, apiKey, secretKey string) perpPrivateWSBackend {
	return newBinancePerpPrivateWSBackendForTest(api, account, apiKey, secretKey)
}

func newBinancePerpPrivateWSBackendForTest(api binancePerpPrivateAPI, account binancePerpAccountWS, apiKey, secretKey string) perpPrivateWSBackend {
	backend := &binancePerpPrivateWSBackend{
		meta:      clientMeta{venue: exchange.VenueBinance, product: exchange.ProductPerp},
		api:       api,
		account:   account,
		apiKey:    apiKey,
		secretKey: secretKey,
		lifecycle: newBackendLifecycle(),
	}
	if backend.account != nil {
		backend.account.SetReconnectHooks(
			func(err error) { backend.lifecycle.Started(err) },
			func() { backend.lifecycle.Recovered("Binance private websocket subscriptions restored") },
		)
	}
	return backend
}

func (backend *binancePerpPrivateWSBackend) StartOrders(ctx context.Context, instrument string, callbacks streamCallbacks[exchange.OrderEvent]) (func() error, error) {
	canonical, native, err := binancePerpRequestSymbols(instrument)
	if err != nil {
		return nil, websocketError(backend.meta, "WatchOrders", exchange.KindInvalidRequest, err.Error())
	}
	if err := backend.ensureAccountConnected(ctx, "WatchOrders"); err != nil {
		return nil, err
	}
	removeLifecycle := backend.lifecycle.Register("perp:orders:"+native+":"+nextBinanceWSRequestID(), callbacks.Status, nil)
	stop, err := backend.addOrderRoute("WatchOrders", binancePerpOrderRoute{instrument: canonical, callbacks: callbacks}, removeLifecycle)
	if err != nil {
		removeLifecycle()
		return nil, err
	}
	return stop, nil
}

func (backend *binancePerpPrivateWSBackend) StartFills(ctx context.Context, instrument string, callbacks streamCallbacks[exchange.FillEvent]) (func() error, error) {
	canonical, native, err := binancePerpRequestSymbols(instrument)
	if err != nil {
		return nil, websocketError(backend.meta, "WatchFills", exchange.KindInvalidRequest, err.Error())
	}
	if err := backend.ensureAccountConnected(ctx, "WatchFills"); err != nil {
		return nil, err
	}
	removeLifecycle := backend.lifecycle.Register("perp:fills:"+native+":"+nextBinanceWSRequestID(), callbacks.Status, nil)
	stop, err := backend.addFillRoute("WatchFills", binancePerpFillRoute{instrument: canonical, callbacks: callbacks}, removeLifecycle)
	if err != nil {
		removeLifecycle()
		return nil, err
	}
	return stop, nil
}

func (backend *binancePerpPrivateWSBackend) StartBalances(ctx context.Context, callbacks streamCallbacks[exchange.BalanceEvent]) (func() error, error) {
	if err := backend.ensureAccountConnected(ctx, "WatchBalances"); err != nil {
		return nil, err
	}
	removeLifecycle := backend.lifecycle.Register("perp:balances:"+nextBinanceWSRequestID(), callbacks.Status, nil)
	stop, err := backend.addBalanceRoute("WatchBalances", binancePerpBalanceRoute{callbacks: callbacks}, removeLifecycle)
	if err != nil {
		removeLifecycle()
		return nil, err
	}
	return stop, nil
}

func (backend *binancePerpPrivateWSBackend) StartPositions(ctx context.Context, instrument string, callbacks streamCallbacks[exchange.PositionEvent]) (func() error, error) {
	canonical, native, err := binancePerpRequestSymbols(instrument)
	if err != nil {
		return nil, websocketError(backend.meta, "WatchPositions", exchange.KindInvalidRequest, err.Error())
	}
	if err := backend.ensureAccountConnected(ctx, "WatchPositions"); err != nil {
		return nil, err
	}
	removeLifecycle := backend.lifecycle.Register("perp:positions:"+native+":"+nextBinanceWSRequestID(), callbacks.Status, nil)
	stop, err := backend.addPositionRoute("WatchPositions", binancePerpPositionRoute{instrument: canonical, callbacks: callbacks}, removeLifecycle)
	if err != nil {
		removeLifecycle()
		return nil, err
	}
	return stop, nil
}

func (backend *binancePerpPrivateWSBackend) PlaceOrder(ctx context.Context, request exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error) {
	if err := request.Validate(exchange.ProductPerp); err != nil {
		return exchange.OrderAcknowledgement{}, binancePerpInvalidRequest("PlaceOrder", err.Error())
	}
	canonical, native, err := binancePerpRequestSymbols(request.Instrument)
	if err != nil {
		return exchange.OrderAcknowledgement{}, binancePerpInvalidRequest("PlaceOrder", err.Error())
	}
	if err := backend.ensureAPIConnected(ctx, "PlaceOrder"); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	params := binancePerpPlaceParams(native, request)
	resp, err := backend.api.PlaceOrderWS(backend.apiKey, backend.secretKey, params, nextBinanceWSRequestID())
	if err != nil {
		return binancePerpCommandErrorAck("PlaceOrder", binancePerpAck(exchange.OrderOperationPlace, canonical, "", request.ClientOrderID), err)
	}
	ack, err := binancePerpOrderAck(exchange.OrderOperationPlace, canonical, "", request.ClientOrderID, resp)
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	ack.OrderType = request.Type
	return ack, ack.Validate()
}

func (backend *binancePerpPrivateWSBackend) CancelOrder(ctx context.Context, request exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error) {
	if err := binancePerpValidateCancel(request); err != nil {
		return exchange.OrderAcknowledgement{}, binancePerpInvalidRequest("CancelOrder", err.Error())
	}
	canonical, native, _ := binancePerpRequestSymbols(request.Instrument)
	if err := backend.ensureAPIConnected(ctx, "CancelOrder"); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	params := binanceperp.CancelOrderParams{Symbol: native, OrderID: request.OrderID}
	resp, err := backend.api.CancelOrderWS(backend.apiKey, backend.secretKey, params, nextBinanceWSRequestID())
	if err != nil {
		return binancePerpCommandErrorAck("CancelOrder", binancePerpAck(exchange.OrderOperationCancel, canonical, request.OrderID, ""), err)
	}
	return binancePerpOrderAck(exchange.OrderOperationCancel, canonical, request.OrderID, "", resp)
}

func (backend *binancePerpPrivateWSBackend) Close() error {
	if backend == nil {
		return nil
	}
	backend.closeOnce.Do(func() {
		backend.mu.Lock()
		backend.closed = true
		api := backend.api
		account := backend.account
		backend.mu.Unlock()
		if account != nil {
			account.Close()
		}
		if api != nil {
			api.Close()
		}
	})
	return nil
}

func (backend *binancePerpPrivateWSBackend) ensureAPIConnected(ctx context.Context, operation string) error {
	return backend.ensureConnected(ctx, operation, "Binance perp websocket API client is not configured", func() bool {
		return backend.apiConnected
	}, func() {
		backend.apiConnected = true
	}, func() error {
		return backend.api.Connect()
	}, backend.api != nil)
}

func (backend *binancePerpPrivateWSBackend) ensureAccountConnected(ctx context.Context, operation string) error {
	return backend.ensureConnected(ctx, operation, "Binance perp private account websocket client is not configured", func() bool {
		return backend.accountConnected
	}, func() {
		backend.accountConnected = true
	}, func() error {
		return backend.account.Connect()
	}, backend.account != nil)
}

func (backend *binancePerpPrivateWSBackend) ensureConnected(ctx context.Context, operation, missing string, connected func() bool, mark func(), connect func() error, configured bool) error {
	if backend == nil || !configured {
		return websocketError(socketMeta(nil), operation, exchange.KindInvalidConfig, missing)
	}
	if ctx == nil {
		return websocketError(backend.meta, operation, exchange.KindInvalidRequest, "context is required")
	}
	if err := ctx.Err(); err != nil {
		return websocketContextError(backend.meta, operation, err)
	}
	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return websocketError(backend.meta, operation, exchange.KindSubscriptionClosed, "websocket client is closed")
	}
	if connected() {
		backend.mu.Unlock()
		return nil
	}
	if err := connect(); err != nil {
		backend.mu.Unlock()
		return websocketError(backend.meta, operation, exchange.KindTransport, "websocket connection failed")
	}
	if backend.closed {
		backend.mu.Unlock()
		return websocketError(backend.meta, operation, exchange.KindSubscriptionClosed, "websocket client is closed")
	}
	mark()
	backend.mu.Unlock()
	return nil
}

func (backend *binancePerpPrivateWSBackend) addOrderRoute(operation string, route binancePerpOrderRoute, removeLifecycle func()) (func() error, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.closed {
		return nil, websocketError(backend.meta, operation, exchange.KindSubscriptionClosed, "websocket client is closed")
	}
	if backend.orderRoutes == nil {
		backend.orderRoutes = make(map[uint64]binancePerpOrderRoute)
	}
	if !backend.orderSubscribed {
		backend.account.SubscribeOrderUpdate(backend.dispatchOrderUpdate)
		backend.orderSubscribed = true
	}
	backend.nextRouteID++
	id := backend.nextRouteID
	backend.orderRoutes[id] = route
	var once sync.Once
	return func() error {
		once.Do(func() {
			backend.mu.Lock()
			delete(backend.orderRoutes, id)
			backend.mu.Unlock()
			if removeLifecycle != nil {
				removeLifecycle()
			}
		})
		return nil
	}, nil
}

func (backend *binancePerpPrivateWSBackend) addFillRoute(operation string, route binancePerpFillRoute, removeLifecycle func()) (func() error, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.closed {
		return nil, websocketError(backend.meta, operation, exchange.KindSubscriptionClosed, "websocket client is closed")
	}
	if backend.fillRoutes == nil {
		backend.fillRoutes = make(map[uint64]binancePerpFillRoute)
	}
	if !backend.orderSubscribed {
		backend.account.SubscribeOrderUpdate(backend.dispatchOrderUpdate)
		backend.orderSubscribed = true
	}
	backend.nextRouteID++
	id := backend.nextRouteID
	backend.fillRoutes[id] = route
	var once sync.Once
	return func() error {
		once.Do(func() {
			backend.mu.Lock()
			delete(backend.fillRoutes, id)
			backend.mu.Unlock()
			if removeLifecycle != nil {
				removeLifecycle()
			}
		})
		return nil
	}, nil
}

func (backend *binancePerpPrivateWSBackend) addBalanceRoute(operation string, route binancePerpBalanceRoute, removeLifecycle func()) (func() error, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.closed {
		return nil, websocketError(backend.meta, operation, exchange.KindSubscriptionClosed, "websocket client is closed")
	}
	if backend.balanceRoutes == nil {
		backend.balanceRoutes = make(map[uint64]binancePerpBalanceRoute)
	}
	if !backend.accountSubscribed {
		backend.account.SubscribeAccountUpdate(backend.dispatchAccountUpdate)
		backend.accountSubscribed = true
	}
	backend.nextRouteID++
	id := backend.nextRouteID
	backend.balanceRoutes[id] = route
	var once sync.Once
	return func() error {
		once.Do(func() {
			backend.mu.Lock()
			delete(backend.balanceRoutes, id)
			backend.mu.Unlock()
			if removeLifecycle != nil {
				removeLifecycle()
			}
		})
		return nil
	}, nil
}

func (backend *binancePerpPrivateWSBackend) addPositionRoute(operation string, route binancePerpPositionRoute, removeLifecycle func()) (func() error, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.closed {
		return nil, websocketError(backend.meta, operation, exchange.KindSubscriptionClosed, "websocket client is closed")
	}
	if backend.positionRoutes == nil {
		backend.positionRoutes = make(map[uint64]binancePerpPositionRoute)
	}
	if !backend.accountSubscribed {
		backend.account.SubscribeAccountUpdate(backend.dispatchAccountUpdate)
		backend.accountSubscribed = true
	}
	backend.nextRouteID++
	id := backend.nextRouteID
	backend.positionRoutes[id] = route
	var once sync.Once
	return func() error {
		once.Do(func() {
			backend.mu.Lock()
			delete(backend.positionRoutes, id)
			backend.mu.Unlock()
			if removeLifecycle != nil {
				removeLifecycle()
			}
		})
		return nil
	}, nil
}

func (backend *binancePerpPrivateWSBackend) dispatchOrderUpdate(event *binanceperp.OrderUpdateEvent) {
	backend.mu.Lock()
	orderRoutes := make([]binancePerpOrderRoute, 0, len(backend.orderRoutes))
	for _, route := range backend.orderRoutes {
		orderRoutes = append(orderRoutes, route)
	}
	fillRoutes := make([]binancePerpFillRoute, 0, len(backend.fillRoutes))
	for _, route := range backend.fillRoutes {
		fillRoutes = append(fillRoutes, route)
	}
	backend.mu.Unlock()
	for _, route := range orderRoutes {
		order, err := binancePerpWSOrderEvent(route.instrument, event)
		if err != nil {
			if errors.Is(err, binanceSkipEvent) {
				continue
			}
			emitBinanceWSError(route.callbacks, err)
			continue
		}
		route.callbacks.Event(exchange.OrderEvent{Kind: exchange.EventDelta, Order: order})
	}
	for _, route := range fillRoutes {
		fill, ok, err := binancePerpWSFillEvent(route.instrument, event)
		if err != nil {
			emitBinanceWSError(route.callbacks, err)
			continue
		}
		if ok {
			route.callbacks.Event(exchange.FillEvent{Kind: exchange.EventDelta, Fill: fill})
		}
	}
}

func (backend *binancePerpPrivateWSBackend) dispatchAccountUpdate(event *binanceperp.AccountUpdateEvent) {
	backend.mu.Lock()
	balanceRoutes := make([]binancePerpBalanceRoute, 0, len(backend.balanceRoutes))
	for _, route := range backend.balanceRoutes {
		balanceRoutes = append(balanceRoutes, route)
	}
	positionRoutes := make([]binancePerpPositionRoute, 0, len(backend.positionRoutes))
	for _, route := range backend.positionRoutes {
		positionRoutes = append(positionRoutes, route)
	}
	backend.mu.Unlock()
	for _, route := range balanceRoutes {
		balances, err := binancePerpWSBalances(event)
		if err != nil {
			emitBinanceWSError(route.callbacks, err)
			continue
		}
		route.callbacks.Event(exchange.BalanceEvent{Kind: exchange.EventDelta, Balances: balances, Time: binancePerpWSEventTime(event)})
	}
	for _, route := range positionRoutes {
		positions, err := binancePerpWSPositions(route.instrument, event)
		if err != nil {
			emitBinanceWSError(route.callbacks, err)
			continue
		}
		if len(positions) > 0 {
			route.callbacks.Event(exchange.PositionEvent{Kind: exchange.EventDelta, Positions: positions, Time: binancePerpWSEventTime(event)})
		}
	}
}

func binanceSpotWSOrderEvent(instrument, expectedSymbol string, native *binancespot.ExecutionReportEvent) (exchange.Order, error) {
	if native == nil {
		return exchange.Order{}, binanceSpotMalformed("WatchOrders", "missing execution report")
	}
	if native.Symbol != expectedSymbol {
		return exchange.Order{}, binanceSkipEvent
	}
	if native.OrderID <= 0 {
		return exchange.Order{}, binanceSpotMalformed("WatchOrders", "order id must be positive")
	}
	side, err := binanceSpotSide(native.Side)
	if err != nil {
		return exchange.Order{}, binanceSpotMalformed("WatchOrders", err.Error())
	}
	quantity, err := binanceNonNegativeDecimal(native.Quantity)
	if err != nil {
		return exchange.Order{}, binanceSpotMalformed("WatchOrders", "order quantity must be non-negative decimal")
	}
	price, err := binanceNonNegativeDecimal(native.Price)
	if err != nil {
		return exchange.Order{}, binanceSpotMalformed("WatchOrders", "order price must be non-negative decimal")
	}
	filled, err := binanceNonNegativeDecimal(native.CumulativeFilledQuantity)
	if err != nil {
		return exchange.Order{}, binanceSpotMalformed("WatchOrders", "order filled quantity must be non-negative decimal")
	}
	orderType, policy, err := binanceSpotWSOrderShape(native.OrderType, native.TimeInForce)
	if err != nil {
		return exchange.Order{}, binanceSpotMalformed("WatchOrders", err.Error())
	}
	order := exchange.Order{
		Instrument:    instrument,
		OrderID:       strconv.FormatInt(native.OrderID, 10),
		ClientOrderID: native.ClientOrderID,
		Side:          side,
		Type:          orderType,
		Quantity:      quantity,
		LimitPrice:    price,
		LimitPolicy:   policy,
		Filled:        filled,
		Status:        native.OrderStatus,
		CreatedAt:     binanceMillis(native.CreationTime),
		UpdatedAt:     binanceMillis(firstPositiveInt64(native.TransactionTime, native.EventTime)),
	}
	if quote, err := decimal.NewFromString(native.CumulativeQuoteAssetTransactedQuantity); err == nil && filled.IsPositive() && quote.IsPositive() {
		order.AverageFillPrice = exchange.OptionalDecimal{Value: quote.Div(filled), Valid: true}
	}
	return order, nil
}

func binanceSpotWSFillEvent(instrument, expectedSymbol string, native *binancespot.ExecutionReportEvent) (exchange.Fill, bool, error) {
	if native == nil {
		return exchange.Fill{}, false, binanceSpotMalformed("WatchFills", "missing execution report")
	}
	if native.Symbol != expectedSymbol {
		return exchange.Fill{}, false, nil
	}
	if executionType := strings.TrimSpace(native.ExecutionType); executionType != "" && !strings.EqualFold(executionType, "TRADE") {
		return exchange.Fill{}, false, nil
	}
	qty, err := binancePositiveDecimal(native.LastExecutedQuantity)
	if err != nil {
		if strings.TrimSpace(native.LastExecutedQuantity) == "" || native.LastExecutedQuantity == "0" {
			return exchange.Fill{}, false, nil
		}
		return exchange.Fill{}, false, binanceSpotMalformed("WatchFills", "last fill quantity must be positive decimal")
	}
	if native.TradeID <= 0 {
		return exchange.Fill{}, false, binanceSpotMalformed("WatchFills", "trade id must be positive")
	}
	price, err := binancePositiveDecimal(native.LastExecutedPrice)
	if err != nil {
		return exchange.Fill{}, false, binanceSpotMalformed("WatchFills", "last fill price must be positive decimal")
	}
	fee, err := binanceNonNegativeDecimal(defaultZero(native.CommissionAmount))
	if err != nil {
		return exchange.Fill{}, false, binanceSpotMalformed("WatchFills", "commission must be non-negative decimal")
	}
	side, err := binanceSpotSide(native.Side)
	if err != nil {
		return exchange.Fill{}, false, binanceSpotMalformed("WatchFills", err.Error())
	}
	liquidity := exchange.LiquidityTaker
	if native.IsMaker {
		liquidity = exchange.LiquidityMaker
	}
	return exchange.Fill{
		Instrument:    instrument,
		OrderID:       strconv.FormatInt(native.OrderID, 10),
		ClientOrderID: native.ClientOrderID,
		FillID:        strconv.FormatInt(native.TradeID, 10),
		Side:          side,
		Price:         price,
		Quantity:      qty,
		Fee:           fee,
		FeeAsset:      native.CommissionAsset,
		Liquidity:     liquidity,
		Time:          binanceMillis(firstPositiveInt64(native.TransactionTime, native.EventTime)),
	}, true, nil
}

func binanceSpotWSBalances(event *binancespot.AccountPositionEvent) ([]exchange.Balance, error) {
	if event == nil {
		return nil, binanceSpotMalformed("WatchBalances", "missing account position event")
	}
	balances := make([]exchange.Balance, 0, len(event.Balances))
	for _, row := range event.Balances {
		if row.Asset == "" {
			return nil, binanceSpotMalformed("WatchBalances", "balance asset is required")
		}
		free, err := binanceNonNegativeDecimal(row.Free)
		if err != nil {
			return nil, binanceSpotMalformed("WatchBalances", "balance free must be non-negative decimal")
		}
		locked, err := binanceNonNegativeDecimal(row.Locked)
		if err != nil {
			return nil, binanceSpotMalformed("WatchBalances", "balance locked must be non-negative decimal")
		}
		balances = append(balances, exchange.Balance{Asset: row.Asset, Available: free, Locked: locked, Total: free.Add(locked)})
	}
	return balances, nil
}

func binanceSpotWSPlaceAck(instrument string, req exchange.PlaceOrderRequest, res *binancespot.OrderResponse) (exchange.OrderAcknowledgement, error) {
	if res == nil {
		return exchange.OrderAcknowledgement{}, binanceSpotMalformed("PlaceOrder", "missing order response")
	}
	if res.OrderID <= 0 {
		return exchange.OrderAcknowledgement{}, binanceSpotMalformed("PlaceOrder", "response order id must be positive")
	}
	if res.Symbol != strings.ReplaceAll(instrument, "-", "") {
		return exchange.OrderAcknowledgement{}, binanceSpotMalformed("PlaceOrder", "response symbol does not match requested instrument")
	}
	if req.ClientOrderID != "" && res.ClientOrderID != req.ClientOrderID {
		return exchange.OrderAcknowledgement{}, binanceSpotMalformed("PlaceOrder", "response client order id does not match request")
	}
	state, err := binanceSpotAckState(res.Status)
	if err != nil {
		return exchange.OrderAcknowledgement{}, binanceSpotMalformed("PlaceOrder", err.Error())
	}
	ack := exchange.OrderAcknowledgement{
		Venue:         binanceSpotVenue,
		Product:       binanceSpotProduct,
		Operation:     exchange.OrderOperationPlace,
		State:         state,
		Instrument:    instrument,
		OrderType:     req.Type,
		OrderID:       strconv.FormatInt(res.OrderID, 10),
		ClientOrderID: res.ClientOrderID,
	}
	filled, err := binanceNonNegativeDecimal(defaultZero(res.ExecutedQty))
	if err != nil {
		return exchange.OrderAcknowledgement{}, binanceSpotMalformed("PlaceOrder", "invalid executed quantity")
	}
	ack.FilledQuantity = filled
	if filled.IsPositive() {
		quote, err := binanceNonNegativeDecimal(res.CummulativeQuoteQty)
		if err != nil {
			return exchange.OrderAcknowledgement{}, binanceSpotMalformed("PlaceOrder", "invalid cumulative quote quantity")
		}
		if quote.IsPositive() {
			ack.AverageFillPrice = exchange.OptionalDecimal{Value: quote.Div(filled), Valid: true}
		}
	}
	return ack, ack.Validate()
}

func binanceSpotWSCancelAck(instrument string, req exchange.CancelOrderRequest, res *binancespot.OrderResponse) (exchange.OrderAcknowledgement, error) {
	if res == nil {
		return exchange.OrderAcknowledgement{}, binanceSpotMalformed("CancelOrder", "missing order response")
	}
	if res.OrderID <= 0 {
		return exchange.OrderAcknowledgement{}, binanceSpotMalformed("CancelOrder", "response order id must be positive")
	}
	if res.Symbol != strings.ReplaceAll(instrument, "-", "") {
		return exchange.OrderAcknowledgement{}, binanceSpotMalformed("CancelOrder", "response symbol does not match requested instrument")
	}
	orderID := strconv.FormatInt(res.OrderID, 10)
	if req.OrderID != "" && orderID != req.OrderID {
		return exchange.OrderAcknowledgement{}, binanceSpotMalformed("CancelOrder", "response order id does not match request")
	}
	ack := exchange.OrderAcknowledgement{
		Venue:         binanceSpotVenue,
		Product:       binanceSpotProduct,
		Operation:     exchange.OrderOperationCancel,
		State:         exchange.AckCanceled,
		Instrument:    instrument,
		OrderID:       orderID,
		ClientOrderID: res.ClientOrderID,
	}
	return ack, ack.Validate()
}

func binancePerpWSOrderEvent(expectedInstrument string, native *binanceperp.OrderUpdateEvent) (exchange.Order, error) {
	if native == nil {
		return exchange.Order{}, binancePerpMalformed("WatchOrders", "missing order update")
	}
	resp := binanceperp.OrderResponse{
		Symbol:        native.Order.Symbol,
		OrderID:       native.Order.OrderID,
		ClientOrderID: native.Order.ClientOrderID,
		Side:          native.Order.Side,
		Type:          native.Order.OrderType,
		TimeInForce:   native.Order.TimeInForce,
		OrigQty:       native.Order.OriginalQty,
		Price:         native.Order.OriginalPrice,
		ExecutedQty:   native.Order.AccumulatedFilledQty,
		AvgPrice:      native.Order.AveragePrice,
		Status:        native.Order.OrderStatus,
		PositionSide:  native.Order.PositionSide,
		ReduceOnly:    native.Order.IsReduceOnly,
		ClosePosition: native.Order.ClosePosition,
		UpdateTime:    firstPositiveInt64(native.TransactionTime, native.EventTime, native.Order.TradeTime),
	}
	order, err := binancePerpOrder(resp)
	if err != nil {
		return exchange.Order{}, binancePerpMalformed("WatchOrders", err.Error())
	}
	if order.Instrument != expectedInstrument {
		return exchange.Order{}, binanceSkipEvent
	}
	return order, nil
}

func binancePerpWSFillEvent(expectedInstrument string, native *binanceperp.OrderUpdateEvent) (exchange.Fill, bool, error) {
	if native == nil {
		return exchange.Fill{}, false, binancePerpMalformed("WatchFills", "missing order update")
	}
	canonical, _, err := binancePerpNativeSymbols(native.Order.Symbol)
	if err != nil {
		return exchange.Fill{}, false, binancePerpMalformed("WatchFills", err.Error())
	}
	if canonical != expectedInstrument {
		return exchange.Fill{}, false, nil
	}
	qty, err := binancePositiveDecimal(native.Order.LastFilledQty)
	if err != nil {
		if strings.TrimSpace(native.Order.LastFilledQty) == "" || native.Order.LastFilledQty == "0" {
			return exchange.Fill{}, false, nil
		}
		return exchange.Fill{}, false, binancePerpMalformed("WatchFills", "last fill quantity must be positive decimal")
	}
	if native.Order.TradeID <= 0 {
		return exchange.Fill{}, false, binancePerpMalformed("WatchFills", "trade id must be positive")
	}
	price, err := binancePositiveDecimal(native.Order.LastFilledPrice)
	if err != nil {
		return exchange.Fill{}, false, binancePerpMalformed("WatchFills", "last fill price must be positive decimal")
	}
	fee, err := binanceNonNegativeDecimal(defaultZero(native.Order.Commission))
	if err != nil {
		return exchange.Fill{}, false, binancePerpMalformed("WatchFills", "commission must be non-negative decimal")
	}
	side, err := binancePerpExchangeSide(native.Order.Side)
	if err != nil {
		return exchange.Fill{}, false, binancePerpMalformed("WatchFills", err.Error())
	}
	liquidity := exchange.LiquidityTaker
	if native.Order.IsMaker {
		liquidity = exchange.LiquidityMaker
	}
	return exchange.Fill{
		Instrument:    canonical,
		OrderID:       strconv.FormatInt(native.Order.OrderID, 10),
		ClientOrderID: native.Order.ClientOrderID,
		FillID:        strconv.FormatInt(native.Order.TradeID, 10),
		Side:          side,
		Price:         price,
		Quantity:      qty,
		Fee:           fee,
		FeeAsset:      native.Order.CommissionAsset,
		Liquidity:     liquidity,
		Time:          binanceMillis(firstPositiveInt64(native.Order.TradeTime, native.TransactionTime, native.EventTime)),
	}, true, nil
}

func binancePerpWSBalances(event *binanceperp.AccountUpdateEvent) ([]exchange.Balance, error) {
	if event == nil {
		return nil, binancePerpMalformed("WatchBalances", "missing account update")
	}
	balances := make([]exchange.Balance, 0, len(event.UpdateData.Balances))
	for _, row := range event.UpdateData.Balances {
		if row.Asset == "" {
			return nil, binancePerpMalformed("WatchBalances", "balance asset is required")
		}
		total, err := binanceNonNegativeDecimal(row.WalletBalance)
		if err != nil {
			return nil, binancePerpMalformed("WatchBalances", "wallet balance must be non-negative decimal")
		}
		available, err := binanceNonNegativeDecimal(row.CrossWalletBalance)
		if err != nil {
			return nil, binancePerpMalformed("WatchBalances", "cross wallet balance must be non-negative decimal")
		}
		locked := total.Sub(available)
		if locked.IsNegative() {
			locked = decimal.Zero
		}
		balances = append(balances, exchange.Balance{Asset: row.Asset, Available: available, Locked: locked, Total: total})
	}
	return balances, nil
}

func binancePerpWSPositions(expectedInstrument string, event *binanceperp.AccountUpdateEvent) ([]exchange.Position, error) {
	if event == nil {
		return nil, binancePerpMalformed("WatchPositions", "missing account update")
	}
	positions := make([]exchange.Position, 0, len(event.UpdateData.Positions))
	for _, row := range event.UpdateData.Positions {
		canonical, _, err := binancePerpNativeSymbols(row.Symbol)
		if err != nil {
			return nil, binancePerpMalformed("WatchPositions", err.Error())
		}
		if canonical != expectedInstrument {
			continue
		}
		if row.PositionSide != "" && row.PositionSide != "BOTH" {
			return nil, binancePerpMalformed("WatchPositions", "hedge position side is not supported")
		}
		amount, err := decimal.NewFromString(row.PositionAmount)
		if err != nil {
			return nil, binancePerpMalformed("WatchPositions", "position amount must be decimal")
		}
		side := exchange.SideBuy
		quantity := amount
		if amount.IsNegative() {
			side = exchange.SideSell
			quantity = amount.Neg()
		}
		entry, err := binanceNonNegativeDecimal(row.EntryPrice)
		if err != nil {
			return nil, binancePerpMalformed("WatchPositions", "entry price must be non-negative decimal")
		}
		pnl, err := decimal.NewFromString(row.UnrealizedPnL)
		if err != nil {
			return nil, binancePerpMalformed("WatchPositions", "unrealized pnl must be decimal")
		}
		margin := exchange.OptionalDecimal{}
		if strings.TrimSpace(row.IsolatedWallet) != "" {
			value, err := binanceNonNegativeDecimal(row.IsolatedWallet)
			if err != nil {
				return nil, binancePerpMalformed("WatchPositions", "isolated wallet must be non-negative decimal")
			}
			margin = exchange.OptionalDecimal{Value: value, Valid: true}
		}
		positions = append(positions, exchange.Position{
			Instrument:    canonical,
			Side:          side,
			Quantity:      quantity,
			EntryPrice:    entry,
			UnrealizedPnL: pnl,
			MarginUsed:    margin,
		})
	}
	return positions, nil
}

func binancePerpWSEventTime(event *binanceperp.AccountUpdateEvent) time.Time {
	if event == nil {
		return time.Now().UTC()
	}
	return binanceMillis(firstPositiveInt64(event.TransactionTime, event.EventTime))
}

func binanceSpotWSOrderShape(nativeType, tif string) (exchange.OrderType, exchange.LimitPolicy, error) {
	switch nativeType {
	case "MARKET":
		return exchange.OrderTypeMarket, "", nil
	case "LIMIT_MAKER":
		return exchange.OrderTypeLimit, exchange.LimitPolicyPostOnly, nil
	case "LIMIT", "":
		if tif == "IOC" {
			return exchange.OrderTypeLimit, exchange.LimitPolicyIOC, nil
		}
		return exchange.OrderTypeLimit, exchange.LimitPolicyResting, nil
	default:
		return "", "", fmt.Errorf("unsupported order type")
	}
}

func binanceNonNegativeDecimal(raw string) (decimal.Decimal, error) {
	if strings.TrimSpace(raw) == "" {
		return decimal.Zero, errors.New("decimal is empty")
	}
	value, err := decimal.NewFromString(raw)
	if err != nil || value.IsNegative() {
		return decimal.Zero, errors.New("decimal must be non-negative")
	}
	return value, nil
}

func binancePositiveDecimal(raw string) (decimal.Decimal, error) {
	value, err := binanceNonNegativeDecimal(raw)
	if err != nil || !value.IsPositive() {
		return decimal.Zero, errors.New("decimal must be positive")
	}
	return value, nil
}

func defaultZero(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return "0"
	}
	return raw
}

func binanceMillis(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

func firstPositiveInt64(values ...int64) int64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func nextBinanceWSRequestID() string {
	return fmt.Sprintf("exchange-%d", websocketSubscriptionSequence.Add(1))
}

var binanceSkipEvent = errors.New("skip foreign Binance private websocket event")

var _ privateWSBackend = (*binanceSpotPrivateWSBackend)(nil)
var _ perpPrivateWSBackend = (*binancePerpPrivateWSBackend)(nil)
