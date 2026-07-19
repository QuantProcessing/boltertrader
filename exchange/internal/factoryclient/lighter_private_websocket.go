package factoryclient

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/sdk/lighter"
)

type lighterPrivateWSClient interface {
	lighterPublicWSClient
	SubscribeAccountOrders(int, int64, string, func([]byte)) error
	UnsubscribeAccountOrders(int, int64) error
	SubscribeAccountAllTrades(int64, string, func([]byte)) error
	UnsubscribeAccountAllTrades(int64) error
	SubscribeAccountAllAssets(int64, string, func([]byte)) error
	UnsubscribeAccountAllAssets(int64) error
	SubscribeAccountAllPositions(int64, string, func([]byte)) error
	UnsubscribeAccountAllPositions(int64) error
	PlaceOrderOutcome(context.Context, *lighter.Client, lighter.CreateOrderRequest) (lighter.WSCommandOutcome, error)
	CancelOrderOutcome(context.Context, *lighter.Client, lighter.CancelOrderRequest) (lighter.WSCommandOutcome, error)
}

type lighterPrivateWSBackend struct {
	*lighterWSBackend
	ws        lighterPrivateWSClient
	authMu    sync.Mutex
	authToken string

	accountMu        sync.Mutex
	accountFills     *lighterAccountFillsHub
	accountPositions *lighterAccountPositionsHub
}

func newLighterSpotPrivateWSBackend(
	rest *lighter.Client,
	state *lighterRESTState,
	settings Settings,
) privateWSBackend {
	return newLighterPrivateWSBackend(exchange.ProductSpot, lighterSpot, rest, state, settings)
}

func newLighterPerpPrivateWSBackend(
	rest *lighter.Client,
	state *lighterRESTState,
	settings Settings,
) perpPrivateWSBackend {
	return newLighterPrivateWSBackend(exchange.ProductPerp, lighterPerp, rest, state, settings)
}

func newLighterPrivateWSBackend(
	product exchange.Product,
	marketType string,
	rest *lighter.Client,
	state *lighterRESTState,
	settings Settings,
) *lighterPrivateWSBackend {
	ws := lighter.NewWebsocketClientWithConfig(context.Background(), lighter.WSConfig{ReadOnly: false})
	if settings.Environment == "testnet" {
		ws.WithEnvironment(lighter.EnvironmentTestnet)
	} else {
		ws.WithEnvironment(lighter.EnvironmentMainnet)
	}
	if settings.WebSocketEndpoint != "" {
		ws.WithURL(settings.WebSocketEndpoint)
	}
	base := newLighterWSBackend(product, marketType, rest, state, ws)
	backend := &lighterPrivateWSBackend{lighterWSBackend: base, ws: ws}
	ws.SetSubscriptionAuthProvider(backend.refreshSubscriptionAuth)
	return backend
}

func (backend *lighterPrivateWSBackend) ensureAuth(operation string) (string, error) {
	return backend.auth(operation, false)
}

func (backend *lighterPrivateWSBackend) refreshSubscriptionAuth(string) (*string, error) {
	token, err := backend.auth("WebSocketReconnect", true)
	if err != nil {
		return nil, err
	}
	return &token, nil
}

func (backend *lighterPrivateWSBackend) auth(operation string, refresh bool) (string, error) {
	backend.authMu.Lock()
	defer backend.authMu.Unlock()
	if backend.authToken != "" && !refresh {
		return backend.authToken, nil
	}
	token, err := backend.rest.CreateAuthToken(time.Now().Add(10 * time.Minute))
	if err != nil {
		return "", websocketError(clientMeta{venue: exchange.VenueLighter, product: backend.product}, operation, exchange.KindAuthentication, "Lighter credentials required")
	}
	backend.authToken = token
	return token, nil
}

func (backend *lighterPrivateWSBackend) StartOrders(ctx context.Context, instrument string, callbacks streamCallbacks[exchange.OrderEvent]) (func() error, error) {
	const operation = "WatchOrders"
	meta, err := backend.resolveMeta(ctx, operation, instrument)
	if err != nil {
		return nil, err
	}
	token, err := backend.ensureAuth(operation)
	if err != nil {
		return nil, err
	}
	if err := backend.ensureConnected(operation); err != nil {
		return nil, err
	}
	var stopped atomic.Bool
	ready := make(chan struct{})
	var readyOnce sync.Once
	lifecycle, recoveredOnValid := lighterSimpleLifecycle(callbacks)
	handler := func(payload []byte) {
		var event lighter.WsAccountOrdersEvent
		if err := json.Unmarshal(payload, &event); err != nil {
			emitLighterError(callbacks.Error, backend.malformed(operation))
			return
		}
		for _, orders := range event.Orders {
			for _, row := range orders {
				if stopped.Load() || row == nil || row.MarketIndex != meta.marketID {
					continue
				}
				order, err := lighterOrder(row, meta)
				if err != nil {
					emitLighterError(callbacks.Error, backend.malformed(operation))
					return
				}
				emitLighterEvent(callbacks.Event, exchange.OrderEvent{Kind: exchange.EventDelta, Order: order})
				readyOnce.Do(func() { close(ready) })
			}
		}
		recoveredOnValid()
		readyOnce.Do(func() { close(ready) })
	}
	key := lighterWSKey("private-orders", meta.marketID)
	backend.addLifecycle(key, lifecycle)
	if err := backend.ws.SubscribeAccountOrders(meta.marketID, backend.rest.AccountIndex, token, handler); err != nil {
		backend.removeLifecycle(key)
		return nil, backend.transport(operation)
	}
	stop := lighterStop(func() error {
		stopped.Store(true)
		backend.removeLifecycle(key)
		if err := backend.ws.UnsubscribeAccountOrders(meta.marketID, backend.rest.AccountIndex); err != nil {
			return backend.transport(operation)
		}
		return nil
	})
	if err := backend.waitFirst(ctx, operation, ready, stop); err != nil {
		return nil, err
	}
	return stop, nil
}

func (backend *lighterPrivateWSBackend) StartFills(ctx context.Context, instrument string, callbacks streamCallbacks[exchange.FillEvent]) (func() error, error) {
	const operation = "WatchFills"
	meta, err := backend.resolveMeta(ctx, operation, instrument)
	if err != nil {
		return nil, err
	}
	token, err := backend.ensureAuth(operation)
	if err != nil {
		return nil, err
	}
	if err := backend.ensureConnected(operation); err != nil {
		return nil, err
	}
	lifecycle, recoveredOnValid := lighterSimpleLifecycle(callbacks)
	hub := backend.accountFillsHub()
	id, ready, subscribe := hub.add(meta, callbacks, recoveredOnValid)
	key := "private-fills:" + strconv.Itoa(meta.marketID) + ":" + strconv.FormatUint(id, 10)
	backend.addLifecycle(key, lifecycle)
	stop := lighterStop(func() error {
		backend.removeLifecycle(key)
		if hub.remove(id) {
			if err := backend.ws.UnsubscribeAccountAllTrades(backend.rest.AccountIndex); err != nil {
				return backend.transport(operation)
			}
		}
		return nil
	})
	if subscribe {
		if err := backend.ws.SubscribeAccountAllTrades(backend.rest.AccountIndex, token, hub.handle); err != nil {
			hub.subscribeFailed()
			_ = stop()
			return nil, backend.transport(operation)
		}
		hub.subscribeSucceeded()
	}
	if err := backend.waitFirst(ctx, operation, ready, stop); err != nil {
		return nil, err
	}
	if hub.failed() {
		_ = stop()
		return nil, backend.transport(operation)
	}
	return stop, nil
}

func (backend *lighterPrivateWSBackend) StartBalances(ctx context.Context, callbacks streamCallbacks[exchange.BalanceEvent]) (func() error, error) {
	const operation = "WatchBalances"
	token, err := backend.ensureAuth(operation)
	if err != nil {
		return nil, err
	}
	if err := backend.ensureConnected(operation); err != nil {
		return nil, err
	}
	ready := make(chan struct{})
	var readyOnce sync.Once
	lifecycle, recoveredOnValid := lighterSimpleLifecycle(callbacks)
	handler := func(payload []byte) {
		var event lighter.WsAccountAllAssetsEvent
		if err := json.Unmarshal(payload, &event); err != nil {
			emitLighterError(callbacks.Error, backend.malformed(operation))
			return
		}
		rows := make([]*lighter.SpotAsset, 0, len(event.Assets))
		for _, row := range event.Assets {
			rows = append(rows, row)
		}
		balances, err := lighterSpotBalances(rows, backend.product, operation)
		if err != nil {
			emitLighterError(callbacks.Error, err)
			return
		}
		emitLighterEvent(callbacks.Event, exchange.BalanceEvent{Kind: exchange.EventSnapshot, Balances: balances, Time: lighterUnixMillis(event.Timestamp)})
		recoveredOnValid()
		readyOnce.Do(func() { close(ready) })
	}
	key := "private-balances"
	backend.addLifecycle(key, lifecycle)
	if err := backend.ws.SubscribeAccountAllAssets(backend.rest.AccountIndex, token, handler); err != nil {
		backend.removeLifecycle(key)
		return nil, backend.transport(operation)
	}
	stop := lighterStop(func() error {
		backend.removeLifecycle(key)
		if err := backend.ws.UnsubscribeAccountAllAssets(backend.rest.AccountIndex); err != nil {
			return backend.transport(operation)
		}
		return nil
	})
	if err := backend.waitFirst(ctx, operation, ready, stop); err != nil {
		return nil, err
	}
	return stop, nil
}

func (backend *lighterPrivateWSBackend) StartPositions(ctx context.Context, instrument string, callbacks streamCallbacks[exchange.PositionEvent]) (func() error, error) {
	const operation = "WatchPositions"
	meta, err := backend.resolveMeta(ctx, operation, instrument)
	if err != nil {
		return nil, err
	}
	token, err := backend.ensureAuth(operation)
	if err != nil {
		return nil, err
	}
	if err := backend.ensureConnected(operation); err != nil {
		return nil, err
	}
	lifecycle, recoveredOnValid := lighterSimpleLifecycle(callbacks)
	hub := backend.accountPositionsHub()
	id, ready, subscribe := hub.add(meta, callbacks, recoveredOnValid)
	key := "private-positions:" + strconv.Itoa(meta.marketID) + ":" + strconv.FormatUint(id, 10)
	backend.addLifecycle(key, lifecycle)
	stop := lighterStop(func() error {
		backend.removeLifecycle(key)
		if hub.remove(id) {
			if err := backend.ws.UnsubscribeAccountAllPositions(backend.rest.AccountIndex); err != nil {
				return backend.transport(operation)
			}
		}
		return nil
	})
	if subscribe {
		if err := backend.ws.SubscribeAccountAllPositions(backend.rest.AccountIndex, token, hub.handle); err != nil {
			hub.subscribeFailed()
			_ = stop()
			return nil, backend.transport(operation)
		}
		hub.subscribeSucceeded()
	}
	if err := backend.waitFirst(ctx, operation, ready, stop); err != nil {
		return nil, err
	}
	if hub.failed() {
		_ = stop()
		return nil, backend.transport(operation)
	}
	return stop, nil
}

type lighterAccountFillsHub struct {
	backend *lighterPrivateWSBackend

	mu          sync.Mutex
	nextID      uint64
	watchers    map[uint64]lighterAccountFillsWatcher
	subscribing bool
	subscribed  bool
	ready       bool
	startFailed bool
	readyCh     chan struct{}
}

type lighterAccountFillsWatcher struct {
	meta      lighterMarketMeta
	callbacks streamCallbacks[exchange.FillEvent]
	recovered func()
}

func (backend *lighterPrivateWSBackend) accountFillsHub() *lighterAccountFillsHub {
	backend.accountMu.Lock()
	defer backend.accountMu.Unlock()
	if backend.accountFills != nil {
		return backend.accountFills
	}
	backend.accountFills = &lighterAccountFillsHub{
		backend:  backend,
		watchers: make(map[uint64]lighterAccountFillsWatcher),
		readyCh:  make(chan struct{}),
	}
	return backend.accountFills
}

func (hub *lighterAccountFillsHub) add(meta lighterMarketMeta, callbacks streamCallbacks[exchange.FillEvent], recovered func()) (uint64, <-chan struct{}, bool) {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	hub.nextID++
	id := hub.nextID
	hub.watchers[id] = lighterAccountFillsWatcher{meta: meta, callbacks: callbacks, recovered: recovered}
	var ready <-chan struct{} = hub.readyCh
	if hub.ready {
		ready = closedLighterReady()
	}
	subscribe := !hub.subscribed && !hub.subscribing
	if subscribe {
		hub.subscribing = true
	}
	return id, ready, subscribe
}

func (hub *lighterAccountFillsHub) subscribeSucceeded() {
	hub.mu.Lock()
	hub.subscribing = false
	hub.subscribed = true
	hub.startFailed = false
	hub.mu.Unlock()
}

func (hub *lighterAccountFillsHub) subscribeFailed() {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	hub.subscribing = false
	hub.subscribed = false
	hub.startFailed = true
	if !hub.ready {
		hub.ready = true
		close(hub.readyCh)
	}
}

func (hub *lighterAccountFillsHub) failed() bool {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	return hub.startFailed
}

func (hub *lighterAccountFillsHub) remove(id uint64) bool {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	delete(hub.watchers, id)
	if len(hub.watchers) > 0 {
		return false
	}
	unsubscribe := hub.subscribed
	hub.subscribed = false
	hub.subscribing = false
	hub.ready = false
	hub.startFailed = false
	hub.readyCh = make(chan struct{})
	return unsubscribe
}

func (hub *lighterAccountFillsHub) handle(payload []byte) {
	const operation = "WatchFills"
	var event lighter.WsAccountAllTradesEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		hub.reportMalformed(operation)
		return
	}
	hub.markReady()
	watchers := hub.fillsWatchers()
	for _, watcher := range watchers {
		if watcher.recovered != nil {
			watcher.recovered()
		}
	}
	for _, trades := range event.Trades {
		for _, row := range trades {
			for _, watcher := range watchers {
				if row.MarketId != watcher.meta.marketID {
					continue
				}
				fill, err := lighterFill(row, watcher.meta, hub.backend.rest.AccountIndex)
				if err != nil {
					emitLighterError(watcher.callbacks.Error, hub.backend.malformed(operation))
					continue
				}
				emitLighterEvent(watcher.callbacks.Event, exchange.FillEvent{Kind: exchange.EventDelta, Fill: fill})
			}
		}
	}
}

func (hub *lighterAccountFillsHub) fillsWatchers() []lighterAccountFillsWatcher {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	watchers := make([]lighterAccountFillsWatcher, 0, len(hub.watchers))
	for _, watcher := range hub.watchers {
		watchers = append(watchers, watcher)
	}
	return watchers
}

func (hub *lighterAccountFillsHub) markReady() {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if !hub.ready {
		hub.ready = true
		close(hub.readyCh)
	}
}

func (hub *lighterAccountFillsHub) reportMalformed(operation string) {
	for _, watcher := range hub.fillsWatchers() {
		emitLighterError(watcher.callbacks.Error, hub.backend.malformed(operation))
	}
}

type lighterAccountPositionsHub struct {
	backend *lighterPrivateWSBackend

	mu          sync.Mutex
	nextID      uint64
	watchers    map[uint64]lighterAccountPositionsWatcher
	subscribing bool
	subscribed  bool
	ready       bool
	startFailed bool
	readyCh     chan struct{}
}

type lighterAccountPositionsWatcher struct {
	meta      lighterMarketMeta
	callbacks streamCallbacks[exchange.PositionEvent]
	recovered func()
}

func (backend *lighterPrivateWSBackend) accountPositionsHub() *lighterAccountPositionsHub {
	backend.accountMu.Lock()
	defer backend.accountMu.Unlock()
	if backend.accountPositions != nil {
		return backend.accountPositions
	}
	backend.accountPositions = &lighterAccountPositionsHub{
		backend:  backend,
		watchers: make(map[uint64]lighterAccountPositionsWatcher),
		readyCh:  make(chan struct{}),
	}
	return backend.accountPositions
}

func (hub *lighterAccountPositionsHub) add(meta lighterMarketMeta, callbacks streamCallbacks[exchange.PositionEvent], recovered func()) (uint64, <-chan struct{}, bool) {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	hub.nextID++
	id := hub.nextID
	hub.watchers[id] = lighterAccountPositionsWatcher{meta: meta, callbacks: callbacks, recovered: recovered}
	var ready <-chan struct{} = hub.readyCh
	if hub.ready {
		ready = closedLighterReady()
	}
	subscribe := !hub.subscribed && !hub.subscribing
	if subscribe {
		hub.subscribing = true
	}
	return id, ready, subscribe
}

func (hub *lighterAccountPositionsHub) subscribeSucceeded() {
	hub.mu.Lock()
	hub.subscribing = false
	hub.subscribed = true
	hub.startFailed = false
	hub.mu.Unlock()
}

func (hub *lighterAccountPositionsHub) subscribeFailed() {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	hub.subscribing = false
	hub.subscribed = false
	hub.startFailed = true
	if !hub.ready {
		hub.ready = true
		close(hub.readyCh)
	}
}

func (hub *lighterAccountPositionsHub) failed() bool {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	return hub.startFailed
}

func (hub *lighterAccountPositionsHub) remove(id uint64) bool {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	delete(hub.watchers, id)
	if len(hub.watchers) > 0 {
		return false
	}
	unsubscribe := hub.subscribed
	hub.subscribed = false
	hub.subscribing = false
	hub.ready = false
	hub.startFailed = false
	hub.readyCh = make(chan struct{})
	return unsubscribe
}

func (hub *lighterAccountPositionsHub) handle(payload []byte) {
	const operation = "WatchPositions"
	var event lighter.WsAccountAllPositionsEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		hub.reportMalformed(operation)
		return
	}
	hub.markReady()
	watchers := hub.positionsWatchers()
	for _, watcher := range watchers {
		if watcher.recovered != nil {
			watcher.recovered()
		}
		positions := make([]exchange.Position, 0, 1)
		for _, row := range event.Positions {
			if row == nil || row.MarketId != watcher.meta.marketID {
				continue
			}
			position, err := lighterPosition(row, watcher.meta)
			if err != nil {
				emitLighterError(watcher.callbacks.Error, hub.backend.malformed(operation))
				continue
			}
			positions = append(positions, position)
		}
		emitLighterEvent(watcher.callbacks.Event, exchange.PositionEvent{Kind: exchange.EventSnapshot, Positions: positions, Time: time.Now()})
	}
}

func (hub *lighterAccountPositionsHub) positionsWatchers() []lighterAccountPositionsWatcher {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	watchers := make([]lighterAccountPositionsWatcher, 0, len(hub.watchers))
	for _, watcher := range hub.watchers {
		watchers = append(watchers, watcher)
	}
	return watchers
}

func (hub *lighterAccountPositionsHub) markReady() {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if !hub.ready {
		hub.ready = true
		close(hub.readyCh)
	}
}

func (hub *lighterAccountPositionsHub) reportMalformed(operation string) {
	for _, watcher := range hub.positionsWatchers() {
		emitLighterError(watcher.callbacks.Error, hub.backend.malformed(operation))
	}
}

func closedLighterReady() <-chan struct{} {
	ready := make(chan struct{})
	close(ready)
	return ready
}

func (backend *lighterPrivateWSBackend) PlaceOrder(ctx context.Context, req exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error) {
	const operation = "PlaceOrder"
	meta, err := backend.resolveMeta(ctx, operation, req.Instrument)
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	price, qty, clientID, err := lighterValidatePlace(backend.product, meta, req)
	if err != nil {
		return exchange.OrderAcknowledgement{}, lighterInvalid(backend.product, operation, err.Error())
	}
	if req.Type == exchange.OrderTypeMarket {
		price, err = lighterMarketProtectionPrice(ctx, backend.rest, backend.product, meta, req.Side)
		if err != nil {
			return exchange.OrderAcknowledgement{}, err
		}
	}
	if err := backend.ensureConnected(operation); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	isAsk := uint32(0)
	if req.Side == exchange.SideSell {
		isAsk = 1
	}
	outcome, err := backend.ws.PlaceOrderOutcome(ctx, backend.rest, lighterPlaceRequest(meta, req, price, qty, clientID, isAsk))
	return lighterWSCommandAck(
		backend.product,
		exchange.OrderOperationPlace,
		meta.instrument.Symbol,
		"",
		req.ClientOrderID,
		req.Type,
		outcome,
		err,
		backend.rest,
	)
}

func (backend *lighterPrivateWSBackend) CancelOrder(ctx context.Context, req exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error) {
	const operation = "CancelOrder"
	meta, err := backend.resolveMeta(ctx, operation, req.Instrument)
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	orderID, err := lighterValidateCancel(req)
	if err != nil {
		return exchange.OrderAcknowledgement{}, lighterInvalid(backend.product, operation, err.Error())
	}
	if err := backend.ensureConnected(operation); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	outcome, err := backend.ws.CancelOrderOutcome(ctx, backend.rest, lighter.CancelOrderRequest{MarketId: meta.marketID, OrderId: orderID})
	return lighterWSCommandAck(
		backend.product,
		exchange.OrderOperationCancel,
		meta.instrument.Symbol,
		req.OrderID,
		"",
		"",
		outcome,
		err,
		backend.rest,
	)
}

func lighterWSCommandAck(
	product exchange.Product,
	operation exchange.OrderOperation,
	instrument string,
	orderID string,
	clientOrderID string,
	orderType exchange.OrderType,
	outcome lighter.WSCommandOutcome,
	commandErr error,
	client *lighter.Client,
) (exchange.OrderAcknowledgement, error) {
	if outcome.Code != 0 && commandErr == nil {
		ack, err := lighterCommandAck(
			product,
			lighterCommandOperation(operation),
			operation,
			instrument,
			orderID,
			clientOrderID,
			int32(outcome.Code),
			"",
			outcome.TransactionHash,
			client,
			nil,
		)
		ack.OrderType = orderType
		if err != nil {
			return ack, err
		}
		return ack, ack.Validate()
	}
	if outcome.Code != 0 && errors.Is(commandErr, lighter.ErrOrderRejected) {
		ack, err := lighterCommandAck(
			product,
			lighterCommandOperation(operation),
			operation,
			instrument,
			orderID,
			clientOrderID,
			int32(outcome.Code),
			"",
			outcome.TransactionHash,
			client,
			nil,
		)
		ack.OrderType = orderType
		return ack, err
	}
	if outcome.Sent {
		ack := exchange.OrderAcknowledgement{
			Venue:           exchange.VenueLighter,
			Product:         product,
			Operation:       operation,
			State:           exchange.AckAmbiguous,
			Instrument:      instrument,
			OrderID:         orderID,
			ClientOrderID:   clientOrderID,
			TransactionHash: outcome.TransactionHash,
			OrderType:       orderType,
		}
		if err := ack.Validate(); err != nil {
			return exchange.OrderAcknowledgement{}, err
		}
		return ack, exchange.NewError(exchange.KindAmbiguousOutcome, exchange.ErrorDetails{
			Venue:       exchange.VenueLighter,
			Product:     product,
			Operation:   lighterCommandOperation(operation),
			SafeMessage: "order command outcome is unknown after possible send",
		})
	}
	if commandErr == nil {
		return exchange.OrderAcknowledgement{}, lighterMalformed(product, lighterCommandOperation(operation), "Lighter websocket command response is missing")
	}
	return lighterCommandErr(product, operation, instrument, orderID, clientOrderID, commandErr, client, nil)
}
