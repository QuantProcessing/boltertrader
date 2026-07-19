package factoryclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	hyperliquid "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
	hyperliquidperp "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/perp"
	hyperliquidspot "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/spot"
)

type hyperliquidPrivateHub[T any] struct {
	mu         sync.Mutex
	sinks      map[string]func(T)
	errorSinks map[string]func(error)
	starting   bool
	started    bool
	ready      chan struct{}
	startErr   error
	stopNative func() error
	closed     bool
}

func newHyperliquidPrivateHub[T any]() *hyperliquidPrivateHub[T] {
	return &hyperliquidPrivateHub[T]{
		sinks:      make(map[string]func(T)),
		errorSinks: make(map[string]func(error)),
	}
}

func (hub *hyperliquidPrivateHub[T]) add(
	ctx context.Context,
	key string,
	event func(T),
	onError func(error),
	start func() (func() error, error),
) (func() error, error) {
	hub.mu.Lock()
	if hub.closed {
		hub.mu.Unlock()
		return nil, errors.New("private websocket hub is closed")
	}
	if _, exists := hub.sinks[key]; exists {
		hub.mu.Unlock()
		return nil, fmt.Errorf("private websocket sink %q already exists", key)
	}
	hub.sinks[key] = event
	hub.errorSinks[key] = onError
	if hub.started {
		hub.mu.Unlock()
		return hub.stop(key), nil
	}
	if hub.starting {
		ready := hub.ready
		hub.mu.Unlock()
		select {
		case <-ready:
		case <-ctx.Done():
			_ = hub.stop(key)()
			return nil, ctx.Err()
		}
		hub.mu.Lock()
		started, err := hub.started, hub.startErr
		hub.mu.Unlock()
		if err != nil || !started {
			_ = hub.stop(key)()
			if err == nil {
				err = errors.New("private websocket subscription did not start")
			}
			return nil, err
		}
		return hub.stop(key), nil
	}
	hub.starting = true
	hub.ready = make(chan struct{})
	ready := hub.ready
	hub.mu.Unlock()

	stopNative, err := start()

	hub.mu.Lock()
	hub.starting = false
	closeAfterStart := hub.closed && err == nil
	if closeAfterStart {
		err = errors.New("private websocket hub closed during startup")
	}
	hub.startErr = err
	if err == nil && !hub.closed {
		hub.started = true
		hub.stopNative = stopNative
	}
	close(ready)
	hub.mu.Unlock()
	if closeAfterStart && stopNative != nil {
		_ = stopNative()
	}
	if err != nil {
		_ = hub.stop(key)()
		return nil, err
	}
	return hub.stop(key), nil
}

func (hub *hyperliquidPrivateHub[T]) publish(value T) {
	hub.mu.Lock()
	sinks := make([]func(T), 0, len(hub.sinks))
	for _, sink := range hub.sinks {
		sinks = append(sinks, sink)
	}
	hub.mu.Unlock()
	for _, sink := range sinks {
		if sink != nil {
			sink(value)
		}
	}
}

func (hub *hyperliquidPrivateHub[T]) broadcastError(err error) {
	if err == nil {
		return
	}
	hub.mu.Lock()
	sinks := make([]func(error), 0, len(hub.errorSinks))
	for _, sink := range hub.errorSinks {
		sinks = append(sinks, sink)
	}
	hub.mu.Unlock()
	for _, sink := range sinks {
		if sink != nil {
			sink(err)
		}
	}
}

func (hub *hyperliquidPrivateHub[T]) stop(key string) func() error {
	var once sync.Once
	var stopErr error
	return func() error {
		once.Do(func() {
			hub.mu.Lock()
			delete(hub.sinks, key)
			delete(hub.errorSinks, key)
			if len(hub.sinks) == 0 && hub.started {
				stopNative := hub.stopNative
				hub.started = false
				hub.stopNative = nil
				hub.mu.Unlock()
				if stopNative != nil {
					stopErr = stopNative()
				}
				return
			}
			hub.mu.Unlock()
		})
		return stopErr
	}
}

func (hub *hyperliquidPrivateHub[T]) close() error {
	hub.mu.Lock()
	hub.closed = true
	hub.sinks = make(map[string]func(T))
	hub.errorSinks = make(map[string]func(error))
	stopNative := hub.stopNative
	hub.stopNative = nil
	hub.started = false
	hub.mu.Unlock()
	if stopNative != nil {
		return stopNative()
	}
	return nil
}

type hyperliquidPrivateFill struct {
	fill hyperliquid.WsUserFill
	kind exchange.EventKind
}

type hyperliquidPrivateState struct {
	spot *hyperliquid.SpotClearinghouseState
	perp *hyperliquidperp.PerpPosition
}

type hyperliquidPrivateWSBackend struct {
	meta clientMeta
	base *hyperliquid.WebsocketClient
	user string

	resolve func(context.Context, string, string) (hyperliquidMarketMeta, error)
	mid     func(context.Context, string) (float64, error)

	spot *hyperliquidspot.WebsocketClient
	perp *hyperliquidperp.WebsocketClient

	lifecycle *backendLifecycle
	cancel    context.CancelFunc
	orders    *hyperliquidPrivateHub[hyperliquid.WsOrderUpdate]
	fills     *hyperliquidPrivateHub[hyperliquidPrivateFill]
	state     *hyperliquidPrivateHub[hyperliquidPrivateState]

	connectMu sync.Mutex
	sinkSeq   atomic.Uint64
	connected bool
	closed    bool
	closeOnce sync.Once
	closeErr  error
}

func (backend *hyperliquidPrivateWSBackend) nextSinkKey(prefix string) string {
	return fmt.Sprintf("%s:%d", prefix, backend.sinkSeq.Add(1))
}

func newHyperliquidSpotPrivateWSBackend(
	rest *hyperliquidSpotClient,
	privateKey string,
	settings Settings,
) *hyperliquidPrivateWSBackend {
	ctx, cancel := context.WithCancel(context.Background())
	base := hyperliquid.NewWebsocketClient(ctx).WithCredentials(privateKey, nil)
	if settings.AccountAddress != "" {
		base.AccountAddr = settings.AccountAddress
	}
	hlConfigureWSBase(base, settings)
	backend := &hyperliquidPrivateWSBackend{
		meta:      rest.meta,
		base:      base,
		user:      base.AccountAddr,
		resolve:   rest.spotMeta,
		mid:       rest.hyperliquidSpotMid,
		spot:      hyperliquidspot.NewWebsocketClient(base),
		lifecycle: newBackendLifecycle(),
		cancel:    cancel,
		orders:    newHyperliquidPrivateHub[hyperliquid.WsOrderUpdate](),
		fills:     newHyperliquidPrivateHub[hyperliquidPrivateFill](),
		state:     newHyperliquidPrivateHub[hyperliquidPrivateState](),
	}
	backend.installReconnectHooks()
	return backend
}

func newHyperliquidPerpPrivateWSBackend(
	rest *hyperliquidPerpClient,
	privateKey string,
	settings Settings,
) *hyperliquidPrivateWSBackend {
	ctx, cancel := context.WithCancel(context.Background())
	base := hyperliquid.NewWebsocketClient(ctx).WithCredentials(privateKey, nil)
	if settings.AccountAddress != "" {
		base.AccountAddr = settings.AccountAddress
	}
	hlConfigureWSBase(base, settings)
	backend := &hyperliquidPrivateWSBackend{
		meta:      rest.meta,
		base:      base,
		user:      base.AccountAddr,
		resolve:   rest.perpMeta,
		mid:       rest.hyperliquidPerpMid,
		perp:      hyperliquidperp.NewWebsocketClient(base),
		lifecycle: newBackendLifecycle(),
		cancel:    cancel,
		orders:    newHyperliquidPrivateHub[hyperliquid.WsOrderUpdate](),
		fills:     newHyperliquidPrivateHub[hyperliquidPrivateFill](),
		state:     newHyperliquidPrivateHub[hyperliquidPrivateState](),
	}
	backend.installReconnectHooks()
	return backend
}

func hlConfigureWSBase(base *hyperliquid.WebsocketClient, settings Settings) {
	if settings.Environment == "testnet" {
		base.WithEnvironment(hyperliquid.EnvironmentTestnet)
	} else {
		base.WithEnvironment(hyperliquid.EnvironmentMainnet)
	}
	if settings.WebSocketEndpoint != "" {
		base.WithURL(settings.WebSocketEndpoint)
	}
}

func (backend *hyperliquidPrivateWSBackend) installReconnectHooks() {
	backend.base.SetReconnectHooks(
		backend.lifecycle.Started,
		func() { backend.lifecycle.Recovered("private subscriptions confirmed") },
	)
}

func (backend *hyperliquidPrivateWSBackend) ensureConnected(operation string) error {
	if backend.user == "" {
		return exchange.NewError(exchange.KindAuthentication, exchange.ErrorDetails{
			Venue:       backend.meta.venue,
			Product:     backend.meta.product,
			Operation:   operation,
			SafeMessage: "Hyperliquid credentials required",
		})
	}
	backend.connectMu.Lock()
	defer backend.connectMu.Unlock()
	if backend.closed {
		return websocketError(backend.meta, operation, exchange.KindSubscriptionClosed, "websocket backend is closed")
	}
	if backend.connected {
		return nil
	}
	if err := backend.base.Connect(); err != nil {
		return websocketError(backend.meta, operation, exchange.KindTransport, fmt.Sprintf("websocket connect failed: %v", err))
	}
	backend.connected = true
	return nil
}

func (backend *hyperliquidPrivateWSBackend) StartOrders(
	ctx context.Context,
	instrument string,
	callbacks streamCallbacks[exchange.OrderEvent],
) (func() error, error) {
	const operation = "WatchOrders"
	meta, err := backend.resolve(ctx, operation, instrument)
	if err != nil {
		return nil, err
	}
	removeLifecycle := backend.lifecycle.Register("orders:"+instrument, callbacks.Status, nil)
	stop, err := backend.orders.add(
		ctx,
		backend.nextSinkKey("orders"),
		func(row hyperliquid.WsOrderUpdate) {
			if row.Order.Coin != meta.nativeCoin {
				return
			}
			event, err := hyperliquidPrivateOrderEvent(meta, row)
			if err != nil {
				callbacks.Error(hyperliquidWebSocketMalformed(backend.meta, operation, err.Error()))
				return
			}
			callbacks.Event(event)
		},
		callbacks.Error,
		func() (func() error, error) {
			if err := backend.ensureConnected(operation); err != nil {
				return nil, err
			}
			handler := func(rows []hyperliquid.WsOrderUpdate) {
				for _, row := range rows {
					backend.orders.publish(row)
				}
			}
			onDecodeError := func(err error) {
				backend.orders.broadcastError(hyperliquidWebSocketMalformed(backend.meta, operation, err.Error()))
			}
			var err error
			if backend.meta.product == exchange.ProductSpot {
				err = backend.spot.SubscribeOrderUpdatesConfirmedWithErrors(backend.user, handler, onDecodeError)
			} else {
				err = backend.perp.SubscribeOrderUpdatesConfirmedWithErrors(backend.user, handler, onDecodeError)
			}
			if err != nil {
				return nil, websocketError(backend.meta, operation, exchange.KindTransport, "websocket subscription was not confirmed")
			}
			return backend.nativeStop(
				"orderUpdates",
				map[string]string{"type": "orderUpdates", "user": backend.user},
				operation,
			), nil
		},
	)
	if err != nil {
		removeLifecycle()
		return nil, hyperliquidPrivateStartError(backend.meta, operation, err)
	}
	return hyperliquidPrivateStop(removeLifecycle, stop), nil
}

func (backend *hyperliquidPrivateWSBackend) StartFills(
	ctx context.Context,
	instrument string,
	callbacks streamCallbacks[exchange.FillEvent],
) (func() error, error) {
	const operation = "WatchFills"
	meta, err := backend.resolve(ctx, operation, instrument)
	if err != nil {
		return nil, err
	}
	removeLifecycle := backend.lifecycle.Register("fills:"+instrument, callbacks.Status, nil)
	stop, err := backend.fills.add(
		ctx,
		backend.nextSinkKey("fills"),
		func(row hyperliquidPrivateFill) {
			if row.fill.Coin != meta.nativeCoin {
				return
			}
			fill, err := hlFill(meta, row.fill.Coin, row.fill.Side, row.fill.Px, row.fill.Sz, row.fill.Fee, row.fill.FeeToken, row.fill.Oid, row.fill.Tid, row.fill.Hash, row.fill.Crossed, row.fill.Time)
			if err != nil {
				callbacks.Error(hyperliquidWebSocketMalformed(backend.meta, operation, err.Error()))
				return
			}
			callbacks.Event(exchange.FillEvent{Kind: row.kind, Fill: fill})
		},
		callbacks.Error,
		func() (func() error, error) {
			if err := backend.ensureConnected(operation); err != nil {
				return nil, err
			}
			handler := func(rows hyperliquid.WsUserFills) {
				kind := exchange.EventDelta
				if rows.IsSnapshot {
					kind = exchange.EventSnapshot
				}
				for _, row := range rows.Fills {
					backend.fills.publish(hyperliquidPrivateFill{fill: row, kind: kind})
				}
			}
			onDecodeError := func(err error) {
				backend.fills.broadcastError(hyperliquidWebSocketMalformed(backend.meta, operation, err.Error()))
			}
			var err error
			if backend.meta.product == exchange.ProductSpot {
				err = backend.spot.SubscribeUserFillsConfirmedWithErrors(backend.user, handler, onDecodeError)
			} else {
				err = backend.perp.SubscribeUserFillsConfirmedWithErrors(backend.user, handler, onDecodeError)
			}
			if err != nil {
				return nil, websocketError(backend.meta, operation, exchange.KindTransport, "websocket subscription was not confirmed")
			}
			return backend.nativeStop(
				"userFills",
				map[string]any{"type": "userFills", "user": backend.user, "aggregateByTime": false},
				operation,
			), nil
		},
	)
	if err != nil {
		removeLifecycle()
		return nil, hyperliquidPrivateStartError(backend.meta, operation, err)
	}
	return hyperliquidPrivateStop(removeLifecycle, stop), nil
}

func (backend *hyperliquidPrivateWSBackend) StartBalances(
	ctx context.Context,
	callbacks streamCallbacks[exchange.BalanceEvent],
) (func() error, error) {
	const operation = "WatchBalances"
	removeLifecycle := backend.lifecycle.Register("balances", callbacks.Status, nil)
	stop, err := backend.state.add(
		ctx,
		backend.nextSinkKey("balances"),
		func(state hyperliquidPrivateState) {
			if state.spot != nil {
				balances, err := hlSpotBalances(operation, state.spot)
				if err != nil {
					callbacks.Error(err)
					return
				}
				callbacks.Event(exchange.BalanceEvent{Kind: exchange.EventSnapshot, Balances: balances, Time: time.Now().UTC()})
				return
			}
			if state.perp != nil {
				event, err := hyperliquidPerpBalanceEvent(*state.perp)
				if err != nil {
					callbacks.Error(err)
					return
				}
				callbacks.Event(event)
			}
		},
		callbacks.Error,
		func() (func() error, error) {
			return backend.startNativeState(operation)
		},
	)
	if err != nil {
		removeLifecycle()
		return nil, hyperliquidPrivateStartError(backend.meta, operation, err)
	}
	return hyperliquidPrivateStop(removeLifecycle, stop), nil
}

func (backend *hyperliquidPrivateWSBackend) StartPositions(
	ctx context.Context,
	instrument string,
	callbacks streamCallbacks[exchange.PositionEvent],
) (func() error, error) {
	const operation = "WatchPositions"
	if backend.meta.product != exchange.ProductPerp || backend.perp == nil {
		return nil, websocketError(backend.meta, operation, exchange.KindInvalidConfig, "perp private websocket backend is not configured")
	}
	meta, err := backend.resolve(ctx, operation, instrument)
	if err != nil {
		return nil, err
	}
	removeLifecycle := backend.lifecycle.Register("positions:"+instrument, callbacks.Status, nil)
	stop, err := backend.state.add(
		ctx,
		backend.nextSinkKey("positions"),
		func(state hyperliquidPrivateState) {
			if state.perp == nil {
				return
			}
			event, err := hyperliquidPerpPositionEvent(meta, *state.perp)
			if err != nil {
				callbacks.Error(err)
				return
			}
			callbacks.Event(event)
		},
		callbacks.Error,
		func() (func() error, error) {
			return backend.startNativeState(operation)
		},
	)
	if err != nil {
		removeLifecycle()
		return nil, hyperliquidPrivateStartError(backend.meta, operation, err)
	}
	return hyperliquidPrivateStop(removeLifecycle, stop), nil
}

func (backend *hyperliquidPrivateWSBackend) startNativeState(operation string) (func() error, error) {
	if err := backend.ensureConnected(operation); err != nil {
		return nil, err
	}
	onDecodeError := func(err error) {
		backend.state.broadcastError(hyperliquidWebSocketMalformed(backend.meta, operation, err.Error()))
	}
	if backend.meta.product == exchange.ProductSpot {
		err := backend.spot.SubscribeSpotStateConfirmedWithErrors(
			backend.user,
			false,
			func(state hyperliquid.SpotClearinghouseState) {
				backend.state.publish(hyperliquidPrivateState{spot: &state})
			},
			onDecodeError,
		)
		if err != nil {
			return nil, websocketError(backend.meta, operation, exchange.KindTransport, "websocket subscription was not confirmed")
		}
		return backend.nativeStop(
			"spotState",
			map[string]any{"type": "spotState", "user": backend.user, "isPortfolioMargin": false},
			operation,
		), nil
	}
	err := backend.perp.SubscribeClearinghouseStateConfirmedWithErrors(
		backend.user,
		"",
		func(state hyperliquidperp.PerpPosition) {
			backend.state.publish(hyperliquidPrivateState{perp: &state})
		},
		onDecodeError,
	)
	if err != nil {
		return nil, websocketError(backend.meta, operation, exchange.KindTransport, "websocket subscription was not confirmed")
	}
	return backend.nativeStop(
		"clearinghouseState",
		map[string]string{"type": "clearinghouseState", "user": backend.user, "dex": ""},
		operation,
	), nil
}

func (backend *hyperliquidPrivateWSBackend) PlaceOrder(
	ctx context.Context,
	request exchange.PlaceOrderRequest,
) (exchange.OrderAcknowledgement, error) {
	const operation = "PlaceOrder"
	meta, err := backend.resolve(ctx, operation, request.Instrument)
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	request, err = hlNormalizePlace(backend.meta.product, request, meta)
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	if err := backend.ensureConnected(operation); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	nativeClientID := hlNativeClientOrderID(request.ClientOrderID)
	var mid float64
	if request.Type == exchange.OrderTypeMarket {
		mid, err = backend.mid(ctx, meta.nativeCoin)
		if err != nil {
			return exchange.OrderAcknowledgement{}, err
		}
	}
	var result <-chan hyperliquid.PostResult
	if backend.meta.product == exchange.ProductSpot {
		native, buildErr := hyperliquidSpotPrivatePlaceRequest(meta, request, nativeClientID, mid)
		if buildErr != nil {
			return exchange.OrderAcknowledgement{}, buildErr
		}
		result, err = backend.spot.PlaceOrder(ctx, native)
	} else {
		native, buildErr := hyperliquidPerpPrivatePlaceRequest(meta, request, nativeClientID, mid)
		if buildErr != nil {
			return exchange.OrderAcknowledgement{}, buildErr
		}
		result, err = backend.perp.PlaceOrder(ctx, native)
	}
	if err != nil {
		return hlMutationErr(backend.meta.product, exchange.OrderOperationPlace, request.Instrument, "", request.ClientOrderID, err, nil)
	}
	post, err := hyperliquidWaitPostResult(ctx, result)
	if err != nil {
		return hyperliquidAmbiguousAck(backend.meta.product, exchange.OrderOperationPlace, request.Instrument, "", request.ClientOrderID), err
	}
	if backend.meta.product == exchange.ProductSpot {
		return hyperliquidSpotPostPlaceAck(request.Instrument, request.ClientOrderID, nativeClientID, request.Type, post)
	}
	return hyperliquidPerpPostPlaceAck(request.Instrument, request.ClientOrderID, nativeClientID, request.Type, post)
}

func (backend *hyperliquidPrivateWSBackend) CancelOrder(
	ctx context.Context,
	request exchange.CancelOrderRequest,
) (exchange.OrderAcknowledgement, error) {
	const operation = "CancelOrder"
	meta, oid, err := backend.cancelMeta(ctx, request)
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	if err := backend.ensureConnected(operation); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	var result <-chan hyperliquid.PostResult
	if backend.meta.product == exchange.ProductSpot {
		result, err = backend.spot.CancelOrder(ctx, hyperliquidspot.CancelOrderRequest{AssetID: meta.assetID, OrderID: oid})
	} else {
		result, err = backend.perp.CancelOrder(ctx, hyperliquidperp.CancelOrderRequest{AssetID: meta.assetID, OrderID: oid})
	}
	if err != nil {
		return hlMutationErr(backend.meta.product, exchange.OrderOperationCancel, request.Instrument, request.OrderID, "", err, nil)
	}
	post, err := hyperliquidWaitPostResult(ctx, result)
	if err != nil {
		return hyperliquidAmbiguousAck(backend.meta.product, exchange.OrderOperationCancel, request.Instrument, request.OrderID, ""), err
	}
	return hyperliquidPostCancelAck(backend.meta.product, request.Instrument, request.OrderID, post)
}

func hyperliquidSpotPrivatePlaceRequest(
	meta hyperliquidMarketMeta,
	request exchange.PlaceOrderRequest,
	nativeClientID string,
	mid float64,
) (hyperliquidspot.PlaceOrderRequest, error) {
	price, tif, err := hyperliquidPrivatePriceAndTIF(meta, request, mid, true)
	if err != nil {
		return hyperliquidspot.PlaceOrderRequest{}, err
	}
	return hyperliquidspot.PlaceOrderRequest{
		AssetID:       meta.assetID,
		IsBuy:         request.Side == exchange.SideBuy,
		Price:         price,
		Size:          hlMustFloat(request.Quantity),
		OrderType:     hyperliquidspot.OrderType{Limit: &hyperliquidspot.OrderTypeLimit{Tif: tif}},
		ClientOrderID: hlOptionalString(nativeClientID),
	}, nil
}

func hyperliquidPerpPrivatePlaceRequest(
	meta hyperliquidMarketMeta,
	request exchange.PlaceOrderRequest,
	nativeClientID string,
	mid float64,
) (hyperliquidperp.PlaceOrderRequest, error) {
	price, tif, err := hyperliquidPrivatePriceAndTIF(meta, request, mid, false)
	if err != nil {
		return hyperliquidperp.PlaceOrderRequest{}, err
	}
	return hyperliquidperp.PlaceOrderRequest{
		AssetID:       meta.assetID,
		IsBuy:         request.Side == exchange.SideBuy,
		Price:         price,
		Size:          hlMustFloat(request.Quantity),
		ReduceOnly:    request.ReduceOnly,
		OrderType:     hyperliquidperp.OrderType{Limit: &hyperliquidperp.OrderTypeLimit{Tif: tif}},
		ClientOrderID: hlOptionalString(nativeClientID),
	}, nil
}

func hyperliquidPrivatePriceAndTIF(
	meta hyperliquidMarketMeta,
	request exchange.PlaceOrderRequest,
	mid float64,
	spot bool,
) (float64, hyperliquid.Tif, error) {
	if request.Type == exchange.OrderTypeLimit {
		price, err := hlNormalizeLimitPrice(request.Side, request.LimitPrice, meta.priceDecimals)
		if err != nil {
			product := exchange.ProductPerp
			if spot {
				product = exchange.ProductSpot
			}
			return 0, "", hlInvalid(product, "PlaceOrder", "limit_price cannot be represented within Hyperliquid price precision")
		}
		return hlMustFloat(price), hlLimitTIF(request.LimitPolicy), nil
	}
	protected, err := hyperliquid.ProtectedMarketPrice(
		mid,
		request.Side == exchange.SideBuy,
		spot,
		meta.sizeDecimals,
	)
	if err != nil {
		product := exchange.ProductPerp
		if spot {
			product = exchange.ProductSpot
		}
		return 0, "", hlMalformed(product, "PlaceOrder", "invalid Hyperliquid market protection price")
	}
	return protected, hyperliquid.TifIoc, nil
}

func (backend *hyperliquidPrivateWSBackend) cancelMeta(
	ctx context.Context,
	request exchange.CancelOrderRequest,
) (hyperliquidMarketMeta, int64, error) {
	meta, err := backend.resolve(ctx, "CancelOrder", request.Instrument)
	if err != nil {
		return hyperliquidMarketMeta{}, 0, err
	}
	oid, err := hlValidateCancel(backend.meta.product, request)
	if err != nil {
		return hyperliquidMarketMeta{}, 0, err
	}
	return meta, oid, nil
}

func (backend *hyperliquidPrivateWSBackend) nativeStop(
	channel string,
	subscription any,
	operation string,
) func() error {
	var once sync.Once
	var stopErr error
	return func() error {
		once.Do(func() {
			if err := backend.base.Unsubscribe(channel, subscription); err != nil {
				stopErr = websocketError(backend.meta, operation, exchange.KindTransport, "websocket unsubscribe failed")
			}
		})
		return stopErr
	}
}

func (backend *hyperliquidPrivateWSBackend) Close() error {
	if backend == nil {
		return nil
	}
	backend.closeOnce.Do(func() {
		backend.connectMu.Lock()
		backend.closed = true
		backend.connected = false
		backend.connectMu.Unlock()
		backend.closeErr = errors.Join(
			backend.orders.close(),
			backend.fills.close(),
			backend.state.close(),
		)
		if backend.cancel != nil {
			backend.cancel()
		}
		if backend.base != nil {
			backend.base.Close()
		}
	})
	return backend.closeErr
}

func hyperliquidPrivateStop(removeLifecycle func(), stop func() error) func() error {
	var once sync.Once
	var stopErr error
	return func() error {
		once.Do(func() {
			removeLifecycle()
			if stop != nil {
				stopErr = stop()
			}
		})
		return stopErr
	}
}

func hyperliquidPrivateStartError(meta clientMeta, operation string, err error) error {
	if err == nil {
		return nil
	}
	var normalized *exchange.Error
	if errors.As(err, &normalized) {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return websocketContextError(meta, operation, err)
	}
	return websocketError(meta, operation, exchange.KindTransport, "Hyperliquid private websocket startup failed")
}

func (client *hyperliquidSpotClient) hyperliquidSpotMid(ctx context.Context, coin string) (float64, error) {
	mids, err := client.sdk.AllMids(ctx)
	if err != nil {
		return 0, hlNormalizeQueryErr(exchange.ProductSpot, "PlaceOrder", err, nil)
	}
	return hyperliquidMidFromMap(exchange.ProductSpot, coin, mids)
}

func (client *hyperliquidPerpClient) hyperliquidPerpMid(ctx context.Context, coin string) (float64, error) {
	mids, err := client.sdk.AllMids(ctx)
	if err != nil {
		return 0, hlNormalizeQueryErr(exchange.ProductPerp, "PlaceOrder", err, nil)
	}
	return hyperliquidMidFromMap(exchange.ProductPerp, coin, mids)
}

func hyperliquidMidFromMap(product exchange.Product, coin string, mids map[string]string) (float64, error) {
	midS := mids[coin]
	mid, err := strconv.ParseFloat(midS, 64)
	if err != nil || mid <= 0 {
		return 0, hlMalformed(product, "PlaceOrder", "invalid Hyperliquid mid price")
	}
	return mid, nil
}

func hyperliquidPrivateOrderEvent(meta hyperliquidMarketMeta, update hyperliquid.WsOrderUpdate) (exchange.OrderEvent, error) {
	row := update.Order
	orderType := exchange.OrderTypeLimit
	switch row.Tif {
	case "Gtc", "Ioc", "Alo", "":
	default:
		return exchange.OrderEvent{}, fmt.Errorf("unsupported time in force")
	}
	switch row.OrderType {
	case "", "Limit":
	case "Market":
		orderType = exchange.OrderTypeMarket
	default:
		return exchange.OrderEvent{}, hlMalformed(meta.instrument.Product, "WatchOrders", "unsupported Hyperliquid order type")
	}
	order, err := hlOrder(meta, row.Coin, row.Side, row.LimitPx, row.Sz, row.OrigSz, row.Oid, row.Cliod, row.Timestamp, update.StatusTimestamp, "Limit", string(row.Tif), row.ReduceOnly != nil && *row.ReduceOnly, false)
	if err != nil {
		return exchange.OrderEvent{}, err
	}
	order.Type = orderType
	if orderType == exchange.OrderTypeMarket {
		order.LimitPolicy = ""
	}
	order.Status = string(update.Status)
	return exchange.OrderEvent{Kind: exchange.EventDelta, Order: order}, nil
}

func hyperliquidPerpBalanceEvent(state hyperliquidperp.PerpPosition) (exchange.BalanceEvent, error) {
	available, err := hlNonNegativeDecimal(state.Withdrawable)
	if err != nil {
		return exchange.BalanceEvent{}, hlMalformed(exchange.ProductPerp, "WatchBalances", "invalid withdrawable balance")
	}
	total, err := hlNonNegativeDecimal(state.MarginSummary.AccountValue)
	if err != nil {
		return exchange.BalanceEvent{}, hlMalformed(exchange.ProductPerp, "WatchBalances", "invalid account value")
	}
	locked := total.Sub(available)
	if locked.IsNegative() {
		return exchange.BalanceEvent{}, hlMalformed(exchange.ProductPerp, "WatchBalances", "withdrawable exceeds account value")
	}
	if state.Time <= 0 {
		return exchange.BalanceEvent{}, hlMalformed(exchange.ProductPerp, "WatchBalances", "invalid clearinghouse state time")
	}
	return exchange.BalanceEvent{
		Kind:     exchange.EventSnapshot,
		Balances: []exchange.Balance{{Asset: "USDC", Available: available, Locked: locked, Total: total}},
		Time:     hyperliquidStateTime(state.Time),
	}, nil
}

func hyperliquidPerpPositionEvent(meta hyperliquidMarketMeta, state hyperliquidperp.PerpPosition) (exchange.PositionEvent, error) {
	positions := make([]exchange.Position, 0, len(state.AssetPositions))
	for _, row := range state.AssetPositions {
		pos := row.Position
		if pos.Coin != meta.nativeCoin {
			continue
		}
		normalized, err := hlPosition(meta, pos.Szi, pos.EntryPx, pos.UnrealizedPnl, pos.LiquidationPx, pos.MarginUsed, pos.Leverage.Value)
		if err != nil {
			return exchange.PositionEvent{}, hlMalformed(exchange.ProductPerp, "WatchPositions", err.Error())
		}
		if !normalized.Quantity.IsZero() {
			positions = append(positions, normalized)
		}
	}
	if state.Time <= 0 {
		return exchange.PositionEvent{}, hlMalformed(exchange.ProductPerp, "WatchPositions", "invalid clearinghouse state time")
	}
	return exchange.PositionEvent{Kind: exchange.EventSnapshot, Positions: positions, Time: hyperliquidStateTime(state.Time)}, nil
}

func hyperliquidStateTime(ms int64) time.Time {
	return time.UnixMilli(ms).UTC()
}

func hyperliquidWaitPostResult(ctx context.Context, ch <-chan hyperliquid.PostResult) (hyperliquid.PostResult, error) {
	if ch == nil {
		return hyperliquid.PostResult{}, exchange.NewError(exchange.KindAmbiguousOutcome, exchange.ErrorDetails{Venue: exchange.VenueHyperliquid, SafeMessage: "order command outcome is unknown because websocket post result is missing"})
	}
	select {
	case result, ok := <-ch:
		if !ok {
			return hyperliquid.PostResult{}, exchange.NewError(exchange.KindAmbiguousOutcome, exchange.ErrorDetails{Venue: exchange.VenueHyperliquid, SafeMessage: "order command outcome is unknown after websocket post channel closed"})
		}
		if result.Error != nil {
			return hyperliquid.PostResult{}, exchange.NewError(exchange.KindAmbiguousOutcome, exchange.ErrorDetails{Venue: exchange.VenueHyperliquid, SafeMessage: "order command outcome is unknown after websocket post error"})
		}
		return result, nil
	case <-ctx.Done():
		return hyperliquid.PostResult{}, exchange.NewError(exchange.KindAmbiguousOutcome, exchange.ErrorDetails{Venue: exchange.VenueHyperliquid, SafeMessage: "order command outcome is unknown after websocket post timeout"})
	}
}

func hyperliquidSpotPostPlaceAck(instrument, clientOrderID, nativeClientOrderID string, orderType exchange.OrderType, result hyperliquid.PostResult) (exchange.OrderAcknowledgement, error) {
	var response hyperliquid.APIResponse[hyperliquidspot.PlaceOrderResponse]
	if err := hyperliquidDecodePostAction(exchange.ProductSpot, "PlaceOrder", result, &response); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	status, err := hyperliquidSpotSinglePlaceStatus(response.Status, response.FailureMessage(), response.Response)
	if err != nil {
		if errors.Is(err, exchange.ErrVenueRejected) {
			return hyperliquidRejectedAck(exchange.ProductSpot, exchange.OrderOperationPlace, instrument, "", clientOrderID, orderType, err), err
		}
		return exchange.OrderAcknowledgement{}, err
	}
	ack, err := hlSpotPlaceAck(instrument, nativeClientOrderID, status)
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	ack.ClientOrderID = clientOrderID
	ack.OrderType = orderType
	return ack, ack.Validate()
}

func hyperliquidPerpPostPlaceAck(instrument, clientOrderID, nativeClientOrderID string, orderType exchange.OrderType, result hyperliquid.PostResult) (exchange.OrderAcknowledgement, error) {
	var response hyperliquid.APIResponse[hyperliquidperp.PlaceOrderResponse]
	if err := hyperliquidDecodePostAction(exchange.ProductPerp, "PlaceOrder", result, &response); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	status, err := hyperliquidPerpSinglePlaceStatus(response.Status, response.FailureMessage(), response.Response)
	if err != nil {
		if errors.Is(err, exchange.ErrVenueRejected) {
			return hyperliquidRejectedAck(exchange.ProductPerp, exchange.OrderOperationPlace, instrument, "", clientOrderID, orderType, err), err
		}
		return exchange.OrderAcknowledgement{}, err
	}
	ack, err := hlPerpPlaceAck(instrument, nativeClientOrderID, status)
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	ack.ClientOrderID = clientOrderID
	ack.OrderType = orderType
	return ack, ack.Validate()
}

func hyperliquidSpotSinglePlaceStatus(status string, failure string, body *hyperliquid.APIResponseBody[hyperliquidspot.PlaceOrderResponse]) (*hyperliquidspot.OrderStatus, error) {
	if status != "ok" {
		return nil, exchange.NewError(exchange.KindVenueRejected, exchange.ErrorDetails{Venue: exchange.VenueHyperliquid, Product: exchange.ProductSpot, Operation: "PlaceOrder", SafeMessage: failure})
	}
	if body == nil {
		return nil, hlMalformed(exchange.ProductSpot, "PlaceOrder", "missing Hyperliquid post response")
	}
	if len(body.Data.Statuses) != 1 {
		return nil, hlMalformed(exchange.ProductSpot, "PlaceOrder", "venue returned unexpected place status count")
	}
	if body.Data.Statuses[0].Error != nil && *body.Data.Statuses[0].Error != "" {
		return nil, exchange.NewError(exchange.KindVenueRejected, exchange.ErrorDetails{Venue: exchange.VenueHyperliquid, Product: exchange.ProductSpot, Operation: "PlaceOrder", SafeMessage: "Hyperliquid rejected order command"})
	}
	return &body.Data.Statuses[0], nil
}

func hyperliquidPerpSinglePlaceStatus(status string, failure string, body *hyperliquid.APIResponseBody[hyperliquidperp.PlaceOrderResponse]) (*hyperliquidperp.OrderStatus, error) {
	if status != "ok" {
		return nil, exchange.NewError(exchange.KindVenueRejected, exchange.ErrorDetails{Venue: exchange.VenueHyperliquid, Product: exchange.ProductPerp, Operation: "PlaceOrder", SafeMessage: failure})
	}
	if body == nil {
		return nil, hlMalformed(exchange.ProductPerp, "PlaceOrder", "missing Hyperliquid post response")
	}
	if len(body.Data.Statuses) != 1 {
		return nil, hlMalformed(exchange.ProductPerp, "PlaceOrder", "venue returned unexpected place status count")
	}
	if body.Data.Statuses[0].Error != nil && *body.Data.Statuses[0].Error != "" {
		return nil, exchange.NewError(exchange.KindVenueRejected, exchange.ErrorDetails{Venue: exchange.VenueHyperliquid, Product: exchange.ProductPerp, Operation: "PlaceOrder", SafeMessage: "Hyperliquid rejected order command"})
	}
	return &body.Data.Statuses[0], nil
}

func hyperliquidPostCancelAck(product exchange.Product, instrument, orderID string, result hyperliquid.PostResult) (exchange.OrderAcknowledgement, error) {
	var response hyperliquid.APIResponse[struct {
		Statuses hyperliquid.MixedArray `json:"statuses"`
	}]
	if err := hyperliquidDecodePostAction(product, "CancelOrder", result, &response); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	if response.Status != "ok" {
		err := exchange.NewError(exchange.KindVenueRejected, exchange.ErrorDetails{Venue: exchange.VenueHyperliquid, Product: product, Operation: "CancelOrder", SafeMessage: response.FailureMessage()})
		return hyperliquidRejectedAck(product, exchange.OrderOperationCancel, instrument, orderID, "", "", err), err
	}
	if response.Response == nil || len(response.Response.Data.Statuses) != 1 {
		return exchange.OrderAcknowledgement{}, hlMalformed(product, "CancelOrder", "venue returned unexpected cancel status count")
	}
	if err := response.Response.Data.Statuses.FirstError(); err != nil {
		normalized := exchange.NewError(exchange.KindVenueRejected, exchange.ErrorDetails{Venue: exchange.VenueHyperliquid, Product: product, Operation: "CancelOrder", SafeMessage: "Hyperliquid rejected order command"})
		return hyperliquidRejectedAck(product, exchange.OrderOperationCancel, instrument, orderID, "", "", normalized), normalized
	}
	status, ok := response.Response.Data.Statuses[0].String()
	if !ok {
		return exchange.OrderAcknowledgement{}, hlMalformed(product, "CancelOrder", "malformed Hyperliquid cancel status")
	}
	return hlCancelAck(product, instrument, orderID, &status)
}

func hyperliquidRejectedAck(
	product exchange.Product,
	operation exchange.OrderOperation,
	instrument string,
	orderID string,
	clientOrderID string,
	orderType exchange.OrderType,
	err error,
) exchange.OrderAcknowledgement {
	ack := hlBaseAck(product, operation, instrument, orderID, clientOrderID, exchange.AckRejected)
	ack.OrderType = orderType
	ack.VenueCode = "order_rejected"
	ack.VenueMessage = "Hyperliquid rejected order command"
	var normalized *exchange.Error
	if errors.As(err, &normalized) && normalized.Details().SafeMessage != "" {
		ack.VenueMessage = normalized.Details().SafeMessage
	}
	return ack
}

func hyperliquidDecodePostAction(product exchange.Product, operation string, result hyperliquid.PostResult, out any) error {
	if result.Response.Type != "action" {
		return hlMalformed(product, operation, "unexpected Hyperliquid websocket post response type")
	}
	if len(result.Response.Payload) == 0 {
		return hlMalformed(product, operation, "missing Hyperliquid websocket post payload")
	}
	if err := json.Unmarshal(result.Response.Payload, out); err != nil {
		return hlMalformed(product, operation, "malformed Hyperliquid websocket post payload")
	}
	return nil
}

func hyperliquidAmbiguousAck(product exchange.Product, op exchange.OrderOperation, instrument, orderID, clientOrderID string) exchange.OrderAcknowledgement {
	return exchange.OrderAcknowledgement{
		Venue:         exchange.VenueHyperliquid,
		Product:       product,
		Operation:     op,
		State:         exchange.AckAmbiguous,
		Instrument:    instrument,
		OrderID:       orderID,
		ClientOrderID: clientOrderID,
	}
}
