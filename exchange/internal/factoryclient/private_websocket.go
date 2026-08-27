package factoryclient

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/QuantProcessing/boltertrader/exchange"
)

type privateWSBackend interface {
	StartOrders(context.Context, exchange.Instrument, streamCallbacks[exchange.OrderEvent]) (func() error, error)
	StartFills(context.Context, exchange.Instrument, streamCallbacks[exchange.FillEvent]) (func() error, error)
	StartBalances(context.Context, streamCallbacks[exchange.BalanceEvent]) (func() error, error)
	PlaceOrder(context.Context, exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error)
	CancelOrder(context.Context, exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error)
	Close() error
}

type perpPrivateWSBackend interface {
	privateWSBackend
	StartPositions(context.Context, exchange.Instrument, streamCallbacks[exchange.PositionEvent]) (func() error, error)
}

type accountBackendEvent struct {
	Kind           exchange.AccountEventKind
	NativeSymbol   string
	NativeID       string
	SourceSequence string
	Order          *exchange.OrderEvent
	Fill           *exchange.FillEvent
	Balance        *exchange.BalanceEvent
	Position       *exchange.PositionEvent
}

type accountEventCallbacks struct {
	Event  func(accountBackendEvent)
	Status func(exchange.StreamStatusEvent)
	Error  func(error)
}

func accountStreamStatus(meta clientMeta, status backendStatus) exchange.StreamStatusEvent {
	return exchange.StreamStatusEvent{
		State: status.State, Phase: status.Phase, Venue: meta.venue, Product: meta.product,
		Generation: status.Generation, Reason: status.Reason, Time: status.Time,
	}
}

func closeAccountStops(stops ...func() error) func() error {
	var once sync.Once
	var result error
	return func() error {
		once.Do(func() {
			for index := len(stops) - 1; index >= 0; index-- {
				if stops[index] != nil {
					result = errors.Join(result, stops[index]())
				}
			}
		})
		return result
	}
}

type accountEventBackend interface {
	StartAccountEvents(context.Context, accountInstrumentIndex, accountEventCallbacks) (func() error, error)
}

var (
	_ accountEventBackend = (*binanceSpotPrivateWSBackend)(nil)
	_ accountEventBackend = (*binancePerpPrivateWSBackend)(nil)
	_ accountEventBackend = (*asterSpotWSBackend)(nil)
	_ accountEventBackend = (*asterPerpWSBackend)(nil)
	_ accountEventBackend = (*bybitPrivateWSBackend)(nil)
	_ accountEventBackend = (*bitgetPrivateWSBackend)(nil)
	_ accountEventBackend = (*okxPrivateWSBackend)(nil)
	_ accountEventBackend = (*gateSpotWSBackend)(nil)
	_ accountEventBackend = (*gatePerpWSBackend)(nil)
	_ accountEventBackend = (*hyperliquidPrivateWSBackend)(nil)
	_ accountEventBackend = (*lighterPrivateWSBackend)(nil)
	_ accountEventBackend = (*nadoSpotWSBackend)(nil)
	_ accountEventBackend = (*nadoPerpWSBackend)(nil)
)

type accountInstrumentIndex struct {
	byNative map[string]exchange.AccountInstrument
}

func newAccountInstrumentIndex(route exchange.AccountRoute, instruments []exchange.AccountInstrument) (accountInstrumentIndex, error) {
	index := accountInstrumentIndex{byNative: make(map[string]exchange.AccountInstrument, len(instruments)*2)}
	for _, item := range instruments {
		identity := item.Instrument.Identity
		if item.InstrumentID == "" || item.Instrument.Validate() != nil || identity.Venue != route.Venue || identity.Product != route.Product {
			return accountInstrumentIndex{}, fmt.Errorf("account event catalog is invalid")
		}
		for _, native := range []string{identity.NativeSymbol, identity.NativeID} {
			native = strings.TrimSpace(native)
			if native == "" {
				continue
			}
			if existing, duplicate := index.byNative[native]; duplicate && existing.InstrumentID != item.InstrumentID {
				return accountInstrumentIndex{}, fmt.Errorf("account event native identity is ambiguous")
			}
			index.byNative[native] = item
		}
	}
	if len(index.byNative) == 0 {
		return accountInstrumentIndex{}, fmt.Errorf("account instrument index is unavailable")
	}
	return index, nil
}

func (index accountInstrumentIndex) resolve(nativeSymbol, nativeID string) (exchange.AccountInstrument, bool) {
	for _, native := range []string{nativeID, nativeSymbol} {
		if item, ok := index.byNative[strings.TrimSpace(native)]; ok {
			return item, true
		}
	}
	return exchange.AccountInstrument{}, false
}

func (index accountInstrumentIndex) instruments() []exchange.AccountInstrument {
	seen := make(map[string]struct{}, len(index.byNative))
	items := make([]exchange.AccountInstrument, 0, len(index.byNative))
	for _, item := range index.byNative {
		if _, ok := seen[item.InstrumentID]; ok {
			continue
		}
		seen[item.InstrumentID] = struct{}{}
		items = append(items, item)
	}
	return items
}

func projectAccountBackendEvent(route exchange.AccountRoute, index accountInstrumentIndex, event accountBackendEvent) ([]exchange.AccountEvent, error) {
	switch event.Kind {
	case exchange.AccountEventOrder:
		if event.Order == nil {
			return nil, fmt.Errorf("account order event payload is missing")
		}
		item, ok := index.resolve(event.Order.Order.NativeSymbol, event.Order.Order.NativeID)
		if !ok {
			return nil, fmt.Errorf("account order native instrument is unknown")
		}
		return []exchange.AccountEvent{{Kind: event.Kind, Route: route, InstrumentID: item.InstrumentID, ObservedAt: observedAccountTime(event.Order.Order.UpdatedAt), Phase: accountPhase(event.Order.Kind), Order: event.Order}}, nil
	case exchange.AccountEventFill:
		if event.Fill == nil {
			return nil, fmt.Errorf("account fill event payload is missing")
		}
		item, ok := index.resolve(event.Fill.Fill.NativeSymbol, event.Fill.Fill.NativeID)
		if !ok {
			return nil, fmt.Errorf("account fill native instrument is unknown")
		}
		return []exchange.AccountEvent{{Kind: event.Kind, Route: route, InstrumentID: item.InstrumentID, ObservedAt: observedAccountTime(event.Fill.Fill.Time), Phase: accountPhase(event.Fill.Kind), Fill: event.Fill}}, nil
	case exchange.AccountEventBalance:
		if event.Balance == nil {
			return nil, fmt.Errorf("account balance event payload is missing")
		}
		return []exchange.AccountEvent{{Kind: event.Kind, Route: route, ObservedAt: observedAccountTime(event.Balance.Time), Phase: accountPhase(event.Balance.Kind), Balance: event.Balance}}, nil
	case exchange.AccountEventPosition:
		if event.Position == nil {
			return nil, fmt.Errorf("account position event payload is missing")
		}
		byInstrument := make(map[string][]exchange.Position)
		items := make(map[string]exchange.AccountInstrument)
		for _, position := range event.Position.Positions {
			item, ok := index.resolve(position.NativeSymbol, position.NativeID)
			if !ok {
				return nil, fmt.Errorf("account position native instrument is unknown")
			}
			byInstrument[item.InstrumentID] = append(byInstrument[item.InstrumentID], position)
			items[item.InstrumentID] = item
		}
		if len(byInstrument) == 0 {
			item, ok := index.resolve(event.NativeSymbol, event.NativeID)
			if !ok {
				return nil, fmt.Errorf("empty account position event has no native instrument")
			}
			byInstrument[item.InstrumentID] = nil
			items[item.InstrumentID] = item
		}
		result := make([]exchange.AccountEvent, 0, len(byInstrument))
		for instrumentID, positions := range byInstrument {
			projected := &exchange.PositionEvent{Kind: event.Position.Kind, Positions: positions, Time: event.Position.Time}
			result = append(result, exchange.AccountEvent{Kind: event.Kind, Route: route, InstrumentID: items[instrumentID].InstrumentID, ObservedAt: observedAccountTime(event.Position.Time), Phase: accountPhase(event.Position.Kind), Position: projected})
		}
		return result, nil
	default:
		return nil, fmt.Errorf("account event kind is unsupported")
	}
}

type spotWebSocket struct {
	*publicWebSocket
	private          *privateWebSocket
	accountCatalogMu sync.RWMutex
	accountCatalog   []exchange.AccountInstrument
}

func (socket *spotWebSocket) String() string {
	if socket == nil || socket.publicWebSocket == nil {
		return "exchange/factory.WebSocket{nil, credentials:redacted}"
	}
	return fmt.Sprintf("exchange/factory.WebSocket{venue:%q, product:%q, credentials:redacted}", socket.meta.venue, socket.meta.product)
}

func (socket *spotWebSocket) GoString() string { return socket.String() }

func newSpotWebSocket(public *publicWebSocket, backend privateWSBackend) *spotWebSocket {
	return &spotWebSocket{
		publicWebSocket: public,
		private:         newPrivateWebSocket(public, backend),
	}
}

func (socket *spotWebSocket) SetAccountInstruments(instruments []exchange.AccountInstrument) error {
	if socket == nil || socket.publicWebSocket == nil || len(instruments) == 0 {
		return fmt.Errorf("account instrument index is invalid")
	}
	seen := make(map[string]struct{}, len(instruments))
	copyOf := make([]exchange.AccountInstrument, 0, len(instruments))
	for _, item := range instruments {
		if item.InstrumentID == "" || item.Instrument.Validate() != nil || item.Instrument.Identity.Venue != socket.meta.venue || item.Instrument.Identity.Product != socket.meta.product {
			return fmt.Errorf("account instrument index is invalid")
		}
		if _, duplicate := seen[item.InstrumentID]; duplicate {
			return fmt.Errorf("account instrument index contains duplicate")
		}
		seen[item.InstrumentID] = struct{}{}
		copyOf = append(copyOf, item)
	}
	socket.accountCatalogMu.Lock()
	socket.accountCatalog = copyOf
	socket.accountCatalogMu.Unlock()
	return nil
}

func (socket *spotWebSocket) accountInstruments() []exchange.AccountInstrument {
	socket.accountCatalogMu.RLock()
	defer socket.accountCatalogMu.RUnlock()
	return append([]exchange.AccountInstrument(nil), socket.accountCatalog...)
}

func (socket *spotWebSocket) WatchOrders(
	ctx context.Context,
	request exchange.WatchRequest,
) (exchange.Subscription[exchange.OrderEvent], error) {
	if socket == nil || socket.private == nil {
		return nil, websocketError(socketMeta(nil), "WatchOrders", exchange.KindInvalidConfig, "websocket backend is not configured")
	}
	return socket.private.WatchOrders(ctx, request)
}

func (socket *spotWebSocket) WatchFills(
	ctx context.Context,
	request exchange.WatchRequest,
) (exchange.Subscription[exchange.FillEvent], error) {
	if socket == nil || socket.private == nil {
		return nil, websocketError(socketMeta(nil), "WatchFills", exchange.KindInvalidConfig, "websocket backend is not configured")
	}
	return socket.private.WatchFills(ctx, request)
}

func (socket *spotWebSocket) WatchBalances(
	ctx context.Context,
	request exchange.WatchAccountRequest,
) (exchange.Subscription[exchange.BalanceEvent], error) {
	if socket == nil || socket.private == nil {
		return nil, websocketError(socketMeta(nil), "WatchBalances", exchange.KindInvalidConfig, "websocket backend is not configured")
	}
	return socket.private.WatchBalances(ctx, request)
}

func (socket *spotWebSocket) PlaceOrder(
	ctx context.Context,
	request exchange.PlaceOrderRequest,
) (exchange.OrderAcknowledgement, error) {
	if socket == nil || socket.private == nil {
		return exchange.OrderAcknowledgement{}, websocketError(socketMeta(nil), "PlaceOrder", exchange.KindInvalidConfig, "websocket backend is not configured")
	}
	return socket.private.PlaceOrder(ctx, request)
}

func (socket *spotWebSocket) CancelOrder(
	ctx context.Context,
	request exchange.CancelOrderRequest,
) (exchange.OrderAcknowledgement, error) {
	if socket == nil || socket.private == nil {
		return exchange.OrderAcknowledgement{}, websocketError(socketMeta(nil), "CancelOrder", exchange.KindInvalidConfig, "websocket backend is not configured")
	}
	return socket.private.CancelOrder(ctx, request)
}

func (socket *spotWebSocket) Close() error {
	if socket == nil {
		return nil
	}
	var errs []error
	if socket.private != nil {
		if err := socket.private.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if socket.publicWebSocket != nil {
		if err := socket.publicWebSocket.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

type privateWebSocket struct {
	public  *publicWebSocket
	backend privateWSBackend

	orders   map[string]*websocketTopic[exchange.OrderEvent]
	fills    map[string]*websocketTopic[exchange.FillEvent]
	balances map[string]*websocketTopic[exchange.BalanceEvent]

	closeOnce sync.Once
	closeErr  error
}

func newPrivateWebSocket(public *publicWebSocket, backend privateWSBackend) *privateWebSocket {
	return &privateWebSocket{
		public:   public,
		backend:  backend,
		orders:   make(map[string]*websocketTopic[exchange.OrderEvent]),
		fills:    make(map[string]*websocketTopic[exchange.FillEvent]),
		balances: make(map[string]*websocketTopic[exchange.BalanceEvent]),
	}
}

func (socket *privateWebSocket) WatchOrders(
	ctx context.Context,
	request exchange.WatchRequest,
) (exchange.Subscription[exchange.OrderEvent], error) {
	if err := socket.validateWatch(ctx, "WatchOrders", request); err != nil {
		return nil, err
	}
	return watchPrivateWebSocketTopic(
		socket.public,
		ctx,
		"WatchOrders",
		"orders",
		websocketInstrumentKey(request.Instrument),
		websocketInstrumentKey(request.Instrument),
		request.Options.Buffer,
		socket.orders,
		func(startCtx context.Context, callbacks streamCallbacks[exchange.OrderEvent]) (func() error, error) {
			return socket.backend.StartOrders(startCtx, request.Instrument, callbacks)
		},
	)
}

func (socket *privateWebSocket) WatchFills(
	ctx context.Context,
	request exchange.WatchRequest,
) (exchange.Subscription[exchange.FillEvent], error) {
	if err := socket.validateWatch(ctx, "WatchFills", request); err != nil {
		return nil, err
	}
	return watchPrivateWebSocketTopic(
		socket.public,
		ctx,
		"WatchFills",
		"fills",
		websocketInstrumentKey(request.Instrument),
		websocketInstrumentKey(request.Instrument),
		request.Options.Buffer,
		socket.fills,
		func(startCtx context.Context, callbacks streamCallbacks[exchange.FillEvent]) (func() error, error) {
			return socket.backend.StartFills(startCtx, request.Instrument, callbacks)
		},
	)
}

func (socket *privateWebSocket) WatchBalances(
	ctx context.Context,
	request exchange.WatchAccountRequest,
) (exchange.Subscription[exchange.BalanceEvent], error) {
	if err := socket.validateAccountWatch(ctx, "WatchBalances", request); err != nil {
		return nil, err
	}
	return watchPrivateWebSocketTopic(
		socket.public,
		ctx,
		"WatchBalances",
		"balances",
		"",
		"account",
		request.Options.Buffer,
		socket.balances,
		func(startCtx context.Context, callbacks streamCallbacks[exchange.BalanceEvent]) (func() error, error) {
			return socket.backend.StartBalances(startCtx, callbacks)
		},
	)
}

func (socket *privateWebSocket) PlaceOrder(
	ctx context.Context,
	request exchange.PlaceOrderRequest,
) (exchange.OrderAcknowledgement, error) {
	if err := socket.validateCommandContext(ctx, "PlaceOrder"); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	if err := request.Validate(socket.public.meta.product); err != nil {
		return exchange.OrderAcknowledgement{}, withExchangeOperation(err, "PlaceOrder")
	}
	return socket.backend.PlaceOrder(ctx, request)
}

func (socket *privateWebSocket) CancelOrder(
	ctx context.Context,
	request exchange.CancelOrderRequest,
) (exchange.OrderAcknowledgement, error) {
	if err := socket.validateCommandContext(ctx, "CancelOrder"); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	if err := validatePrivateCancelOrder(socket.public.meta, request); err != nil {
		return exchange.OrderAcknowledgement{}, websocketError(socket.public.meta, "CancelOrder", exchange.KindInvalidRequest, err.Error())
	}
	return socket.backend.CancelOrder(ctx, request)
}

func (socket *privateWebSocket) Close() error {
	if socket == nil {
		return nil
	}
	socket.closeOnce.Do(func() {
		public := socket.public
		if public == nil {
			if socket.backend != nil {
				socket.closeErr = socket.backend.Close()
			}
			return
		}
		public.mu.Lock()
		public.closed = true
		if public.cancelLifecycle != nil {
			public.cancelLifecycle()
		}
		subscriptions := socket.subscriptionsLocked()
		public.mu.Unlock()

		var errs []error
		for _, closeSubscription := range subscriptions {
			if err := closeSubscription(); err != nil {
				errs = append(errs, err)
			}
		}
		if socket.backend != nil {
			if err := socket.backend.Close(); err != nil {
				errs = append(errs, err)
			}
		}
		socket.closeErr = errors.Join(errs...)
	})
	return socket.closeErr
}

func (socket *privateWebSocket) validateWatch(ctx context.Context, operation string, request exchange.WatchRequest) error {
	if err := socket.validateConfigured(operation); err != nil {
		return err
	}
	return socket.public.validateWatch(ctx, operation, request)
}

func (socket *privateWebSocket) validateAccountWatch(
	ctx context.Context,
	operation string,
	request exchange.WatchAccountRequest,
) error {
	if err := socket.validateConfigured(operation); err != nil {
		return err
	}
	if ctx == nil {
		return websocketError(socket.public.meta, operation, exchange.KindInvalidRequest, "context must not be nil")
	}
	if err := ctx.Err(); err != nil {
		return websocketContextError(socket.public.meta, operation, err)
	}
	if request.Options.Buffer < 0 || request.Options.Buffer > maxWebSocketBuffer {
		return websocketError(socket.public.meta, operation, exchange.KindInvalidRequest, "buffer must be between 0 and 65536")
	}
	return nil
}

func (socket *privateWebSocket) validateCommandContext(ctx context.Context, operation string) error {
	if err := socket.validateConfigured(operation); err != nil {
		return err
	}
	if ctx == nil {
		return websocketError(socket.public.meta, operation, exchange.KindInvalidRequest, "context must not be nil")
	}
	if err := ctx.Err(); err != nil {
		return websocketContextError(socket.public.meta, operation, err)
	}
	return nil
}

func (socket *privateWebSocket) validateConfigured(operation string) error {
	if socket == nil || socket.public == nil || socket.backend == nil {
		return websocketError(socketMeta(nil), operation, exchange.KindInvalidConfig, "websocket backend is not configured")
	}
	return nil
}

func (socket *privateWebSocket) subscriptionsLocked() []func() error {
	var closes []func() error
	for _, topic := range socket.orders {
		for _, subscription := range topic.snapshot() {
			sub := subscription
			closes = append(closes, sub.Close)
		}
	}
	for _, topic := range socket.fills {
		for _, subscription := range topic.snapshot() {
			sub := subscription
			closes = append(closes, sub.Close)
		}
	}
	for _, topic := range socket.balances {
		for _, subscription := range topic.snapshot() {
			sub := subscription
			closes = append(closes, sub.Close)
		}
	}
	return closes
}

func (socket *publicWebSocket) WatchOrders(
	context.Context,
	exchange.WatchRequest,
) (exchange.Subscription[exchange.OrderEvent], error) {
	return nil, websocketError(socketMeta(socket), "WatchOrders", exchange.KindInvalidConfig, "websocket backend is not configured")
}

func (socket *publicWebSocket) WatchFills(
	context.Context,
	exchange.WatchRequest,
) (exchange.Subscription[exchange.FillEvent], error) {
	return nil, websocketError(socketMeta(socket), "WatchFills", exchange.KindInvalidConfig, "websocket backend is not configured")
}

func (socket *publicWebSocket) WatchBalances(
	context.Context,
	exchange.WatchAccountRequest,
) (exchange.Subscription[exchange.BalanceEvent], error) {
	return nil, websocketError(socketMeta(socket), "WatchBalances", exchange.KindInvalidConfig, "websocket backend is not configured")
}

func (socket *publicWebSocket) PlaceOrder(
	context.Context,
	exchange.PlaceOrderRequest,
) (exchange.OrderAcknowledgement, error) {
	return exchange.OrderAcknowledgement{}, websocketError(socketMeta(socket), "PlaceOrder", exchange.KindInvalidConfig, "websocket backend is not configured")
}

func (socket *publicWebSocket) CancelOrder(
	context.Context,
	exchange.CancelOrderRequest,
) (exchange.OrderAcknowledgement, error) {
	return exchange.OrderAcknowledgement{}, websocketError(socketMeta(socket), "CancelOrder", exchange.KindInvalidConfig, "websocket backend is not configured")
}

func watchPrivateWebSocketTopic[T any](
	socket *publicWebSocket,
	ctx context.Context,
	operation string,
	kind string,
	idInstrument string,
	topicKey string,
	buffer int,
	topics map[string]*websocketTopic[T],
	start func(context.Context, streamCallbacks[T]) (func() error, error),
) (exchange.Subscription[T], error) {
	if buffer == 0 {
		buffer = defaultWebSocketBuffer
	}
	id := fmt.Sprintf(
		"%s:%s:%s:%s:%d",
		socket.meta.venue,
		socket.meta.product,
		kind,
		idInstrument,
		websocketSubscriptionSequence.Add(1),
	)

	socket.mu.Lock()
	if socket.closed {
		socket.mu.Unlock()
		return nil, websocketError(socket.meta, operation, exchange.KindSubscriptionClosed, "websocket client is closed")
	}
	topic := topics[topicKey]
	first := topic == nil
	if first {
		topic = newWebSocketTopic[T](nil, nil)
		topics[topicKey] = topic
	}
	var subscription *typedSubscription[T]
	subscription = newTypedSubscription[T](id, socket.meta, buffer, func() error {
		return removeWebSocketTopicSubscription(socket, topicKey, id, topics)
	})
	topic.add(subscription)
	subscription.emitStatus(backendStatus{State: exchange.SubscriptionConnecting})
	socket.mu.Unlock()

	if first {
		startCtx, cancelStart := socket.startupContext(ctx)
		stop, startErr := start(startCtx, topic.callbacks())
		startContextErr := startCtx.Err()
		cancelStart()

		var stopAfterStart func() error
		socket.mu.Lock()
		startErr = normalizeWebSocketStartupError(
			socket.meta,
			operation,
			startErr,
			startContextErr,
			socket.closed,
		)
		topic.starting = false
		topic.startErr = startErr
		topic.stop = stop
		if startErr != nil && topics[topicKey] == topic {
			delete(topics, topicKey)
		}
		if topic.count() == 0 {
			if topics[topicKey] == topic {
				delete(topics, topicKey)
			}
			stopAfterStart = topic.stop
			topic.stop = nil
		}
		close(topic.ready)
		socket.mu.Unlock()
		if stopAfterStart != nil {
			_ = stopAfterStart()
		}
	} else {
		select {
		case <-topic.ready:
		case <-ctx.Done():
			_ = subscription.Close()
			return nil, websocketContextError(socket.meta, operation, ctx.Err())
		case <-socket.lifecycleCtx.Done():
			_ = subscription.Close()
			return nil, websocketError(
				socket.meta,
				operation,
				exchange.KindSubscriptionClosed,
				"websocket client is closed",
			)
		}
	}

	socket.mu.Lock()
	startErr := topic.startErr
	closed := socket.closed
	socket.mu.Unlock()
	if startErr != nil {
		closeFailedSubscription(subscription)
		return nil, startErr
	}
	if closed || subscription.closed.Load() {
		closeFailedSubscription(subscription)
		return nil, websocketError(
			socket.meta,
			operation,
			exchange.KindSubscriptionClosed,
			"websocket client is closed",
		)
	}
	subscription.emitStatus(backendStatus{State: exchange.SubscriptionActive})
	go func() {
		select {
		case <-ctx.Done():
			_ = subscription.Close()
		case <-subscription.events.Done():
		}
	}()
	return subscription, nil
}

func validatePrivateCancelOrder(meta clientMeta, request exchange.CancelOrderRequest) error {
	if err := request.Instrument.Validate(); err != nil {
		return err
	}
	if request.Instrument.Identity.Venue != meta.venue || request.Instrument.Identity.Product != meta.product {
		return errors.New("instrument venue/product does not match websocket")
	}
	orderID, err := strconv.ParseInt(request.OrderID, 10, 64)
	if err == nil && orderID > 0 && strconv.FormatInt(orderID, 10) == request.OrderID {
		return nil
	}
	if validNadoOrderID(request.OrderID) {
		return nil
	}
	if validUUIDOrderID(request.OrderID) {
		return nil
	}
	return errors.New("order id must use the canonical venue-issued format")
}

func validNadoOrderID(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != 66 || !strings.HasPrefix(value, "0x") {
		return false
	}
	for _, ch := range value[2:] {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return true
}

func validUUIDOrderID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if character != '-' {
				return false
			}
			continue
		}
		if (character < '0' || character > '9') &&
			(character < 'a' || character > 'f') &&
			(character < 'A' || character > 'F') {
			return false
		}
	}
	return true
}
