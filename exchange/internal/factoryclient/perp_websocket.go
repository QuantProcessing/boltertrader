package factoryclient

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/QuantProcessing/boltertrader/exchange"
)

type perpReferenceEvent struct {
	MarkPrice    exchange.MarkPriceEvent
	MarkValid    bool
	FundingRate  exchange.FundingRateEvent
	FundingValid bool
}

type perpWSBackend interface {
	publicWSBackend
	StartReference(context.Context, string, streamCallbacks[perpReferenceEvent]) (func() error, error)
}

type referenceTopic struct {
	mu       sync.Mutex
	marks    map[string]*typedSubscription[exchange.MarkPriceEvent]
	funding  map[string]*typedSubscription[exchange.FundingRateEvent]
	stop     func() error
	ready    chan struct{}
	starting bool
	startErr error
}

func newReferenceTopic() *referenceTopic {
	return &referenceTopic{
		marks:    make(map[string]*typedSubscription[exchange.MarkPriceEvent]),
		funding:  make(map[string]*typedSubscription[exchange.FundingRateEvent]),
		ready:    make(chan struct{}),
		starting: true,
	}
}

func (topic *referenceTopic) empty() bool {
	topic.mu.Lock()
	defer topic.mu.Unlock()
	return len(topic.marks) == 0 && len(topic.funding) == 0
}

func (topic *referenceTopic) count() int {
	topic.mu.Lock()
	defer topic.mu.Unlock()
	return len(topic.marks) + len(topic.funding)
}

func (topic *referenceTopic) callbacks() streamCallbacks[perpReferenceEvent] {
	return streamCallbacks[perpReferenceEvent]{
		Event: func(event perpReferenceEvent) {
			topic.mu.Lock()
			marks := make([]*typedSubscription[exchange.MarkPriceEvent], 0, len(topic.marks))
			for _, subscription := range topic.marks {
				marks = append(marks, subscription)
			}
			funding := make([]*typedSubscription[exchange.FundingRateEvent], 0, len(topic.funding))
			for _, subscription := range topic.funding {
				funding = append(funding, subscription)
			}
			topic.mu.Unlock()
			if event.MarkValid {
				for _, subscription := range marks {
					subscription.emit(event.MarkPrice)
				}
			}
			if event.FundingValid {
				for _, subscription := range funding {
					subscription.emit(event.FundingRate)
				}
			}
		},
		Status: func(status backendStatus) {
			topic.mu.Lock()
			subscriptions := make([]func(backendStatus), 0, len(topic.marks)+len(topic.funding))
			for _, subscription := range topic.marks {
				sub := subscription
				subscriptions = append(subscriptions, func(status backendStatus) { sub.emitStatus(status) })
			}
			for _, subscription := range topic.funding {
				sub := subscription
				subscriptions = append(subscriptions, func(status backendStatus) { sub.emitStatus(status) })
			}
			topic.mu.Unlock()
			for _, emit := range subscriptions {
				emit(status)
			}
		},
		Error: func(err error) {
			topic.mu.Lock()
			subscriptions := make([]func(error), 0, len(topic.marks)+len(topic.funding))
			for _, subscription := range topic.marks {
				sub := subscription
				subscriptions = append(subscriptions, func(err error) { sub.emitError(err) })
			}
			for _, subscription := range topic.funding {
				sub := subscription
				subscriptions = append(subscriptions, func(err error) { sub.emitError(err) })
			}
			topic.mu.Unlock()
			for _, emit := range subscriptions {
				emit(err)
			}
		},
	}
}

type perpWebSocket struct {
	*spotWebSocket
	backend        perpWSBackend
	privateBackend perpPrivateWSBackend
	references     map[string]*referenceTopic
	positions      map[string]*websocketTopic[exchange.PositionEvent]
	closeOnce      sync.Once
	closeErr       error
}

func (socket *perpWebSocket) String() string {
	if socket == nil || socket.spotWebSocket == nil {
		return "exchange/factory.WebSocket{nil, credentials:redacted}"
	}
	return socket.spotWebSocket.String()
}

func (socket *perpWebSocket) GoString() string { return socket.String() }

func newPerpWebSocket(meta clientMeta, backend perpWSBackend, privateBackend ...perpPrivateWSBackend) *perpWebSocket {
	var private perpPrivateWSBackend
	if len(privateBackend) > 0 {
		private = privateBackend[0]
	}
	return &perpWebSocket{
		spotWebSocket:  newSpotWebSocket(newPublicWebSocket(meta, backend), private),
		backend:        backend,
		privateBackend: private,
		references:     make(map[string]*referenceTopic),
		positions:      make(map[string]*websocketTopic[exchange.PositionEvent]),
	}
}

func (socket *perpWebSocket) WatchPositions(
	ctx context.Context,
	request exchange.WatchRequest,
) (exchange.Subscription[exchange.PositionEvent], error) {
	if socket == nil || socket.privateBackend == nil {
		return nil, websocketError(socketMeta(nil), "WatchPositions", exchange.KindInvalidConfig, "websocket backend is not configured")
	}
	if err := socket.private.validateWatch(ctx, "WatchPositions", request); err != nil {
		return nil, err
	}
	return watchPrivateWebSocketTopic(
		socket.publicWebSocket,
		ctx,
		"WatchPositions",
		"positions",
		request.Instrument,
		request.Instrument,
		request.Options.Buffer,
		socket.positions,
		func(startCtx context.Context, callbacks streamCallbacks[exchange.PositionEvent]) (func() error, error) {
			return socket.privateBackend.StartPositions(startCtx, request.Instrument, callbacks)
		},
	)
}

func (socket *perpWebSocket) WatchMarkPrice(
	ctx context.Context,
	request exchange.WatchRequest,
) (exchange.Subscription[exchange.MarkPriceEvent], error) {
	if err := socket.validateWatch(ctx, "WatchMarkPrice", request); err != nil {
		return nil, err
	}
	return watchReference(
		socket,
		ctx,
		request,
		"WatchMarkPrice",
		"mark-price",
		func(topic *referenceTopic, subscription *typedSubscription[exchange.MarkPriceEvent]) {
			topic.marks[subscription.id] = subscription
		},
		func(topic *referenceTopic, id string) {
			delete(topic.marks, id)
		},
	)
}

func (socket *perpWebSocket) WatchFundingRate(
	ctx context.Context,
	request exchange.WatchRequest,
) (exchange.Subscription[exchange.FundingRateEvent], error) {
	if err := socket.validateWatch(ctx, "WatchFundingRate", request); err != nil {
		return nil, err
	}
	return watchReference(
		socket,
		ctx,
		request,
		"WatchFundingRate",
		"funding-rate",
		func(topic *referenceTopic, subscription *typedSubscription[exchange.FundingRateEvent]) {
			topic.funding[subscription.id] = subscription
		},
		func(topic *referenceTopic, id string) {
			delete(topic.funding, id)
		},
	)
}

func watchReference[T any](
	socket *perpWebSocket,
	ctx context.Context,
	request exchange.WatchRequest,
	operation string,
	kind string,
	add func(*referenceTopic, *typedSubscription[T]),
	remove func(*referenceTopic, string),
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
		return nil, websocketError(socket.meta, "Watch"+kind, exchange.KindSubscriptionClosed, "websocket client is closed")
	}
	topic := socket.references[request.Instrument]
	first := topic == nil
	if first {
		topic = newReferenceTopic()
		socket.references[request.Instrument] = topic
	}
	var subscription *typedSubscription[T]
	subscription = newTypedSubscription[T](id, socket.meta, buffer, func() error {
		return removeReferenceSubscription[T](socket, request.Instrument, id, remove)
	})
	topic.mu.Lock()
	add(topic, subscription)
	topic.mu.Unlock()
	subscription.emitStatus(backendStatus{State: exchange.SubscriptionConnecting})
	socket.mu.Unlock()

	if first {
		startCtx, cancelStart := socket.startupContext(ctx)
		stop, startErr := socket.backend.StartReference(
			startCtx,
			request.Instrument,
			topic.callbacks(),
		)
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
		if startErr != nil && socket.references[request.Instrument] == topic {
			delete(socket.references, request.Instrument)
		}
		if topic.count() == 0 {
			if socket.references[request.Instrument] == topic {
				delete(socket.references, request.Instrument)
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

func removeReferenceSubscription[T any](
	socket *perpWebSocket,
	instrument string,
	id string,
	remove func(*referenceTopic, string),
) error {
	socket.mu.Lock()
	topic := socket.references[instrument]
	if topic == nil {
		socket.mu.Unlock()
		return nil
	}
	topic.mu.Lock()
	remove(topic, id)
	empty := len(topic.marks) == 0 && len(topic.funding) == 0
	topic.mu.Unlock()
	if !empty {
		socket.mu.Unlock()
		return nil
	}
	if topic.starting {
		socket.mu.Unlock()
		return nil
	}
	delete(socket.references, instrument)
	stop := topic.stop
	socket.mu.Unlock()
	if stop != nil {
		return stop()
	}
	return nil
}

func (socket *perpWebSocket) Close() error {
	if socket == nil {
		return nil
	}
	socket.closeOnce.Do(func() {
		socket.mu.Lock()
		socket.closed = true
		if socket.cancelLifecycle != nil {
			socket.cancelLifecycle()
		}
		var subscriptions []func() error
		for _, topic := range socket.positions {
			for _, subscription := range topic.snapshot() {
				sub := subscription
				subscriptions = append(subscriptions, sub.Close)
			}
		}
		for _, topic := range socket.references {
			topic.mu.Lock()
			for _, subscription := range topic.marks {
				sub := subscription
				subscriptions = append(subscriptions, sub.Close)
			}
			for _, subscription := range topic.funding {
				sub := subscription
				subscriptions = append(subscriptions, sub.Close)
			}
			topic.mu.Unlock()
		}
		socket.mu.Unlock()
		var errs []error
		for _, closeSubscription := range subscriptions {
			if err := closeSubscription(); err != nil {
				errs = append(errs, err)
			}
		}
		if socket.private != nil {
			if err := socket.private.Close(); err != nil {
				errs = append(errs, err)
			}
		}
		if err := socket.publicWebSocket.Close(); err != nil {
			errs = append(errs, err)
		}
		socket.closeErr = errors.Join(errs...)
	})
	return socket.closeErr
}
