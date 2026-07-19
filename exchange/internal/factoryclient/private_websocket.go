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
	StartOrders(context.Context, string, streamCallbacks[exchange.OrderEvent]) (func() error, error)
	StartFills(context.Context, string, streamCallbacks[exchange.FillEvent]) (func() error, error)
	StartBalances(context.Context, streamCallbacks[exchange.BalanceEvent]) (func() error, error)
	PlaceOrder(context.Context, exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error)
	CancelOrder(context.Context, exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error)
	Close() error
}

type perpPrivateWSBackend interface {
	privateWSBackend
	StartPositions(context.Context, string, streamCallbacks[exchange.PositionEvent]) (func() error, error)
}

type spotWebSocket struct {
	*publicWebSocket
	private *privateWebSocket
}

func newSpotWebSocket(public *publicWebSocket, backend privateWSBackend) *spotWebSocket {
	return &spotWebSocket{
		publicWebSocket: public,
		private:         newPrivateWebSocket(public, backend),
	}
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
		request.Instrument,
		request.Instrument,
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
		request.Instrument,
		request.Instrument,
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
	if err := validatePrivateCancelOrder(request); err != nil {
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
		topic = newWebSocketTopic[T]()
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

func validatePrivateCancelOrder(request exchange.CancelOrderRequest) error {
	if strings.TrimSpace(request.Instrument) == "" ||
		strings.TrimSpace(request.Instrument) != request.Instrument {
		return errors.New("instrument is required and must not have surrounding whitespace")
	}
	orderID, err := strconv.ParseInt(request.OrderID, 10, 64)
	if err != nil || orderID <= 0 || strconv.FormatInt(orderID, 10) != request.OrderID {
		return errors.New("order id must be a positive decimal int64")
	}
	return nil
}
