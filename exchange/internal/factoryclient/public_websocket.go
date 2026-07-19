package factoryclient

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/internal/wsstream"
)

const (
	defaultWebSocketBuffer  = 1024
	maxWebSocketBuffer      = 65536
	statusWebSocketBuffer   = 64
	errorWebSocketBuffer    = 16
	webSocketStartupTimeout = 15 * time.Second
)

type backendStatus struct {
	State      exchange.SubscriptionState
	Phase      exchange.GapPhase
	Generation uint64
	Reason     string
	Time       time.Time
}

type streamCallbacks[T any] struct {
	Event  func(T)
	Status func(backendStatus)
	Error  func(error)
}

type publicWSBackend interface {
	StartOrderBook(context.Context, string, streamCallbacks[exchange.BookEvent]) (func() error, error)
	StartBBO(context.Context, string, streamCallbacks[exchange.BBOEvent]) (func() error, error)
	StartPublicTrades(context.Context, string, streamCallbacks[exchange.PublicTradeEvent]) (func() error, error)
	StartCandles(context.Context, string, string, streamCallbacks[exchange.CandleEvent]) (func() error, error)
	Close() error
}

type typedSubscription[T any] struct {
	id        string
	meta      clientMeta
	events    *wsstream.Stream[T]
	status    *wsstream.Stream[exchange.StreamStatusEvent]
	errors    *wsstream.Stream[error]
	closeFn   func() error
	closeOnce sync.Once
	closeErr  error
	closed    atomic.Bool
}

func newTypedSubscription[T any](
	id string,
	meta clientMeta,
	buffer int,
	closeFn func() error,
) *typedSubscription[T] {
	return &typedSubscription[T]{
		id:      id,
		meta:    meta,
		events:  wsstream.New[T](buffer),
		status:  wsstream.New[exchange.StreamStatusEvent](statusWebSocketBuffer),
		errors:  wsstream.New[error](errorWebSocketBuffer),
		closeFn: closeFn,
	}
}

func (subscription *typedSubscription[T]) ID() string {
	if subscription == nil {
		return ""
	}
	return subscription.id
}

func (subscription *typedSubscription[T]) Events() <-chan T {
	if subscription == nil {
		return nil
	}
	return subscription.events.C()
}

func (subscription *typedSubscription[T]) Status() <-chan exchange.StreamStatusEvent {
	if subscription == nil {
		return nil
	}
	return subscription.status.C()
}

func (subscription *typedSubscription[T]) Errors() <-chan error {
	if subscription == nil {
		return nil
	}
	return subscription.errors.C()
}

func (subscription *typedSubscription[T]) Close() error {
	if subscription == nil {
		return nil
	}
	subscription.closeOnce.Do(func() {
		subscription.closed.Store(true)
		if subscription.closeFn != nil {
			subscription.closeErr = subscription.closeFn()
		}
		subscription.emitStatusUnchecked(backendStatus{
			State: exchange.SubscriptionClosed,
			Time:  time.Now().UTC(),
		})
		subscription.events.Close()
		subscription.status.Close()
		subscription.errors.Close()
	})
	return subscription.closeErr
}

func (subscription *typedSubscription[T]) emit(event T) bool {
	return subscription != nil &&
		!subscription.closed.Load() &&
		subscription.events.Emit(event)
}

func (subscription *typedSubscription[T]) emitError(err error) bool {
	if subscription == nil || err == nil {
		return true
	}
	if subscription.closed.Load() {
		return false
	}
	return subscription.errors.Emit(err)
}

func (subscription *typedSubscription[T]) emitStatus(status backendStatus) bool {
	if subscription == nil || subscription.closed.Load() {
		return false
	}
	return subscription.emitStatusUnchecked(status)
}

func (subscription *typedSubscription[T]) emitStatusUnchecked(status backendStatus) bool {
	at := status.Time
	if at.IsZero() {
		at = time.Now().UTC()
	}
	return subscription.status.Emit(exchange.StreamStatusEvent{
		State:      status.State,
		Phase:      status.Phase,
		Venue:      subscription.meta.venue,
		Product:    subscription.meta.product,
		StreamID:   subscription.id,
		Generation: status.Generation,
		Reason:     status.Reason,
		Time:       at,
	})
}

type websocketTopic[T any] struct {
	mu            sync.Mutex
	subscriptions map[string]*typedSubscription[T]
	stop          func() error
	ready         chan struct{}
	starting      bool
	startErr      error
}

func newWebSocketTopic[T any]() *websocketTopic[T] {
	return &websocketTopic[T]{
		subscriptions: make(map[string]*typedSubscription[T]),
		ready:         make(chan struct{}),
		starting:      true,
	}
}

func (topic *websocketTopic[T]) add(subscription *typedSubscription[T]) {
	topic.mu.Lock()
	topic.subscriptions[subscription.id] = subscription
	topic.mu.Unlock()
}

func (topic *websocketTopic[T]) remove(id string) int {
	topic.mu.Lock()
	delete(topic.subscriptions, id)
	remaining := len(topic.subscriptions)
	topic.mu.Unlock()
	return remaining
}

func (topic *websocketTopic[T]) snapshot() []*typedSubscription[T] {
	topic.mu.Lock()
	subscriptions := make([]*typedSubscription[T], 0, len(topic.subscriptions))
	for _, subscription := range topic.subscriptions {
		subscriptions = append(subscriptions, subscription)
	}
	topic.mu.Unlock()
	return subscriptions
}

func (topic *websocketTopic[T]) count() int {
	topic.mu.Lock()
	defer topic.mu.Unlock()
	return len(topic.subscriptions)
}

func (topic *websocketTopic[T]) callbacks() streamCallbacks[T] {
	return streamCallbacks[T]{
		Event: func(event T) {
			for _, subscription := range topic.snapshot() {
				subscription.emit(event)
			}
		},
		Status: func(status backendStatus) {
			for _, subscription := range topic.snapshot() {
				subscription.emitStatus(status)
			}
		},
		Error: func(err error) {
			for _, subscription := range topic.snapshot() {
				subscription.emitError(err)
			}
		},
	}
}

type publicWebSocket struct {
	meta    clientMeta
	backend publicWSBackend

	mu      sync.Mutex
	closed  bool
	books   map[string]*websocketTopic[exchange.BookEvent]
	bbos    map[string]*websocketTopic[exchange.BBOEvent]
	trades  map[string]*websocketTopic[exchange.PublicTradeEvent]
	candles map[string]*websocketTopic[exchange.CandleEvent]

	lifecycleCtx    context.Context
	cancelLifecycle context.CancelFunc

	closeOnce sync.Once
	closeErr  error
}

var websocketSubscriptionSequence atomic.Uint64

func newPublicWebSocket(meta clientMeta, backend publicWSBackend) *publicWebSocket {
	lifecycleCtx, cancelLifecycle := context.WithCancel(context.Background())
	return &publicWebSocket{
		meta:            meta,
		backend:         backend,
		books:           make(map[string]*websocketTopic[exchange.BookEvent]),
		bbos:            make(map[string]*websocketTopic[exchange.BBOEvent]),
		trades:          make(map[string]*websocketTopic[exchange.PublicTradeEvent]),
		candles:         make(map[string]*websocketTopic[exchange.CandleEvent]),
		lifecycleCtx:    lifecycleCtx,
		cancelLifecycle: cancelLifecycle,
	}
}

func (socket *publicWebSocket) WatchOrderBook(
	ctx context.Context,
	request exchange.WatchRequest,
) (exchange.Subscription[exchange.BookEvent], error) {
	if err := socket.validateWatch(ctx, "WatchOrderBook", request); err != nil {
		return nil, err
	}
	return watchWebSocketTopic(
		socket,
		ctx,
		"WatchOrderBook",
		"order-book",
		request.Instrument,
		request,
		socket.books,
		socket.backend.StartOrderBook,
	)
}

func (socket *publicWebSocket) WatchBBO(
	ctx context.Context,
	request exchange.WatchRequest,
) (exchange.Subscription[exchange.BBOEvent], error) {
	if err := socket.validateWatch(ctx, "WatchBBO", request); err != nil {
		return nil, err
	}
	return watchWebSocketTopic(
		socket,
		ctx,
		"WatchBBO",
		"bbo",
		request.Instrument,
		request,
		socket.bbos,
		socket.backend.StartBBO,
	)
}

func (socket *publicWebSocket) WatchPublicTrades(
	ctx context.Context,
	request exchange.WatchRequest,
) (exchange.Subscription[exchange.PublicTradeEvent], error) {
	if err := socket.validateWatch(ctx, "WatchPublicTrades", request); err != nil {
		return nil, err
	}
	return watchWebSocketTopic(
		socket,
		ctx,
		"WatchPublicTrades",
		"public-trades",
		request.Instrument,
		request,
		socket.trades,
		socket.backend.StartPublicTrades,
	)
}

func (socket *publicWebSocket) WatchCandles(
	ctx context.Context,
	request exchange.WatchCandlesRequest,
) (exchange.Subscription[exchange.CandleEvent], error) {
	watchRequest := exchange.WatchRequest{
		Instrument: request.Instrument,
		Options:    request.Options,
	}
	if err := socket.validateWatch(ctx, "WatchCandles", watchRequest); err != nil {
		return nil, err
	}
	if !portableCandleInterval(request.Interval) {
		return nil, websocketError(
			socket.meta,
			"WatchCandles",
			exchange.KindInvalidRequest,
			"interval must be one of 1m, 5m, 15m, 30m, 1h, 4h, 12h, or 1d",
		)
	}
	topicKey := request.Instrument + "\x00" + request.Interval
	return watchWebSocketTopic(
		socket,
		ctx,
		"WatchCandles",
		"candles:"+request.Interval,
		topicKey,
		watchRequest,
		socket.candles,
		func(
			startCtx context.Context,
			instrument string,
			callbacks streamCallbacks[exchange.CandleEvent],
		) (func() error, error) {
			return socket.backend.StartCandles(startCtx, instrument, request.Interval, callbacks)
		},
	)
}

func portableCandleInterval(interval string) bool {
	switch interval {
	case "1m", "5m", "15m", "30m", "1h", "4h", "12h", "1d":
		return true
	default:
		return false
	}
}

func (socket *publicWebSocket) validateWatch(
	ctx context.Context,
	operation string,
	request exchange.WatchRequest,
) error {
	if socket == nil || socket.backend == nil {
		return websocketError(socketMeta(socket), operation, exchange.KindInvalidConfig, "websocket backend is not configured")
	}
	if ctx == nil {
		return websocketError(socket.meta, operation, exchange.KindInvalidRequest, "context must not be nil")
	}
	if err := ctx.Err(); err != nil {
		return websocketContextError(socket.meta, operation, err)
	}
	if strings.TrimSpace(request.Instrument) == "" ||
		strings.TrimSpace(request.Instrument) != request.Instrument {
		return websocketError(socket.meta, operation, exchange.KindInvalidRequest, "instrument is required and must not have surrounding whitespace")
	}
	if request.Options.Buffer < 0 || request.Options.Buffer > maxWebSocketBuffer {
		return websocketError(socket.meta, operation, exchange.KindInvalidRequest, "buffer must be between 0 and 65536")
	}
	return nil
}

func socketMeta(socket *publicWebSocket) clientMeta {
	if socket == nil {
		return clientMeta{}
	}
	return socket.meta
}

func watchWebSocketTopic[T any](
	socket *publicWebSocket,
	ctx context.Context,
	operation string,
	kind string,
	topicKey string,
	request exchange.WatchRequest,
	topics map[string]*websocketTopic[T],
	start func(context.Context, string, streamCallbacks[T]) (func() error, error),
) (exchange.Subscription[T], error) {
	buffer := request.Options.Buffer
	if buffer == 0 {
		buffer = defaultWebSocketBuffer
	}
	id := fmt.Sprintf(
		"%s:%s:%s:%s:%d",
		socket.meta.venue,
		socket.meta.product,
		kind,
		request.Instrument,
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
		stop, startErr := start(startCtx, request.Instrument, topic.callbacks())
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

func removeWebSocketTopicSubscription[T any](
	socket *publicWebSocket,
	instrument string,
	id string,
	topics map[string]*websocketTopic[T],
) error {
	socket.mu.Lock()
	topic := topics[instrument]
	if topic == nil {
		socket.mu.Unlock()
		return nil
	}
	remaining := topic.remove(id)
	if remaining != 0 {
		socket.mu.Unlock()
		return nil
	}
	if topic.starting {
		socket.mu.Unlock()
		return nil
	}
	delete(topics, instrument)
	stop := topic.stop
	socket.mu.Unlock()
	if stop != nil {
		return stop()
	}
	return nil
}

func (socket *publicWebSocket) Close() error {
	if socket == nil {
		return nil
	}
	socket.closeOnce.Do(func() {
		socket.mu.Lock()
		socket.closed = true
		if socket.cancelLifecycle != nil {
			socket.cancelLifecycle()
		}
		subscriptions := socket.subscriptionsLocked()
		socket.mu.Unlock()

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

func (socket *publicWebSocket) subscriptionsLocked() []func() error {
	var closes []func() error
	for _, topic := range socket.books {
		for _, subscription := range topic.snapshot() {
			sub := subscription
			closes = append(closes, sub.Close)
		}
	}
	for _, topic := range socket.bbos {
		for _, subscription := range topic.snapshot() {
			sub := subscription
			closes = append(closes, sub.Close)
		}
	}
	for _, topic := range socket.trades {
		for _, subscription := range topic.snapshot() {
			sub := subscription
			closes = append(closes, sub.Close)
		}
	}
	for _, topic := range socket.candles {
		for _, subscription := range topic.snapshot() {
			sub := subscription
			closes = append(closes, sub.Close)
		}
	}
	return closes
}

func (socket *publicWebSocket) startupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	startCtx, cancelStart := context.WithTimeout(ctx, webSocketStartupTimeout)
	stopLifecycleCancel := context.AfterFunc(socket.lifecycleCtx, cancelStart)
	return startCtx, func() {
		stopLifecycleCancel()
		cancelStart()
	}
}

func normalizeWebSocketStartupError(
	meta clientMeta,
	operation string,
	startErr error,
	contextErr error,
	closed bool,
) error {
	if closed {
		return websocketError(
			meta,
			operation,
			exchange.KindSubscriptionClosed,
			"websocket client is closed",
		)
	}
	if startErr == nil {
		return nil
	}
	if contextErr != nil &&
		(errors.Is(startErr, context.Canceled) ||
			errors.Is(startErr, context.DeadlineExceeded)) {
		return websocketContextError(meta, operation, contextErr)
	}
	return startErr
}

func closeFailedSubscription[T any](subscription *typedSubscription[T]) {
	if subscription == nil {
		return
	}
	subscription.closed.Store(true)
	subscription.events.Close()
	subscription.status.Close()
	subscription.errors.Close()
}

func websocketContextError(meta clientMeta, operation string, err error) error {
	kind := exchange.KindCanceled
	if errors.Is(err, context.DeadlineExceeded) {
		kind = exchange.KindDeadlineExceeded
	}
	return websocketError(meta, operation, kind, "request context ended")
}

func websocketError(
	meta clientMeta,
	operation string,
	kind exchange.ErrorKind,
	message string,
) error {
	return exchange.NewError(kind, exchange.ErrorDetails{
		Venue:       meta.venue,
		Product:     meta.product,
		Operation:   operation,
		SafeMessage: message,
	})
}
