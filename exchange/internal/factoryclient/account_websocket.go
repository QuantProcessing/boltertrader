package factoryclient

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
)

// WatchAccountEvents exposes one route-level private stream. Venue-specific
// symbol routing, where required, stays inside the adapter and is invisible to
// Gateway.
func (socket *spotWebSocket) WatchAccountEvents(ctx context.Context, request exchange.WatchAccountEventsRequest) (exchange.Subscription[exchange.AccountEvent], error) {
	return watchAccountEvents(ctx, socket, nil, request)
}

func (socket *perpWebSocket) WatchAccountEvents(ctx context.Context, request exchange.WatchAccountEventsRequest) (exchange.Subscription[exchange.AccountEvent], error) {
	return watchAccountEvents(ctx, socket.spotWebSocket, socket, request)
}

func watchAccountEvents(ctx context.Context, spot *spotWebSocket, perp *perpWebSocket, request exchange.WatchAccountEventsRequest) (exchange.Subscription[exchange.AccountEvent], error) {
	if ctx == nil || ctx.Err() != nil || spot == nil || spot.private == nil || request.Route.Validate() != nil || request.Options.Buffer < 0 || request.Options.Buffer > maxWebSocketBuffer {
		return nil, fmt.Errorf("account event watch is invalid")
	}
	if request.Route.Venue != spot.meta.venue || request.Route.Product != spot.meta.product || (request.Route.Product == exchange.ProductPerp && perp == nil) {
		return nil, fmt.Errorf("account event route does not match websocket")
	}
	buffer := request.Options.Buffer
	if buffer == 0 {
		buffer = defaultWebSocketBuffer
	}
	watchCtx, cancel := context.WithCancel(ctx)
	var mu sync.Mutex
	children := make([]func() error, 0, len(request.Catalog)*3+1)
	var subscription *typedSubscription[exchange.AccountEvent]
	closeChildren := func() error {
		cancel()
		mu.Lock()
		closers := append([]func() error(nil), children...)
		mu.Unlock()
		var result error
		for index := len(closers) - 1; index >= 0; index-- {
			result = errors.Join(result, closers[index]())
		}
		return result
	}
	id := fmt.Sprintf("%s:%s:account:%s:%d", spot.meta.venue, spot.meta.product, request.Route.RouteID, websocketSubscriptionSequence.Add(1))
	subscription = newTypedSubscription[exchange.AccountEvent](id, spot.meta, buffer, closeChildren)
	add := func(child interface{ Close() error }) {
		mu.Lock()
		children = append(children, child.Close)
		mu.Unlock()
	}
	emitStatus := func(status exchange.StreamStatusEvent) {
		if status.Time.IsZero() {
			status.Time = time.Now().UTC()
		}
		subscription.emitStatus(backendStatus{State: status.State, Phase: status.Phase, Generation: status.Generation, Reason: status.Reason, Time: status.Time})
		event := exchange.AccountEvent{Kind: exchange.AccountEventStreamStatus, Route: request.Route, ObservedAt: status.Time, Status: &status}
		subscription.emit(event)
		if status.State == exchange.SubscriptionGap {
			subscription.emit(exchange.AccountEvent{Kind: exchange.AccountEventResyncRequired, Route: request.Route, ObservedAt: status.Time, Reason: firstNonEmpty(status.Reason, "private-stream-gap")})
		}
	}
	emitError := func(err error) {
		subscription.emitError(err)
		subscription.emit(exchange.AccountEvent{Kind: exchange.AccountEventResyncRequired, Route: request.Route, ObservedAt: time.Now().UTC(), Reason: "private-stream-error"})
	}

	balances, err := spot.WatchBalances(watchCtx, exchange.WatchAccountRequest{Options: request.Options})
	if err != nil {
		_ = subscription.Close()
		return nil, err
	}
	add(balances)
	go bridgeAccountChild(watchCtx, balances, func(event exchange.BalanceEvent) {
		subscription.emit(exchange.AccountEvent{Kind: exchange.AccountEventBalance, Route: request.Route, ObservedAt: observedAccountTime(event.Time), Balance: &event})
	}, emitStatus, emitError)

	for _, item := range request.Catalog {
		if item.InstrumentID == "" || item.Instrument.Validate() != nil || item.Instrument.Identity.Venue != request.Route.Venue || item.Instrument.Identity.Product != request.Route.Product {
			_ = subscription.Close()
			return nil, fmt.Errorf("account event catalog is invalid")
		}
		orders, openErr := spot.WatchOrders(watchCtx, exchange.WatchRequest{Instrument: item.Instrument, Options: request.Options})
		if openErr != nil {
			_ = subscription.Close()
			return nil, openErr
		}
		add(orders)
		instrumentID := item.InstrumentID
		go bridgeAccountChild(watchCtx, orders, func(event exchange.OrderEvent) {
			subscription.emit(exchange.AccountEvent{Kind: exchange.AccountEventOrder, Route: request.Route, InstrumentID: instrumentID, ObservedAt: time.Now().UTC(), Order: &event})
		}, emitStatus, emitError)
		fills, openErr := spot.WatchFills(watchCtx, exchange.WatchRequest{Instrument: item.Instrument, Options: request.Options})
		if openErr != nil {
			_ = subscription.Close()
			return nil, openErr
		}
		add(fills)
		go bridgeAccountChild(watchCtx, fills, func(event exchange.FillEvent) {
			subscription.emit(exchange.AccountEvent{Kind: exchange.AccountEventFill, Route: request.Route, InstrumentID: instrumentID, ObservedAt: time.Now().UTC(), Fill: &event})
		}, emitStatus, emitError)
		if perp != nil {
			positions, positionErr := perp.WatchPositions(watchCtx, exchange.WatchRequest{Instrument: item.Instrument, Options: request.Options})
			if positionErr != nil {
				_ = subscription.Close()
				return nil, positionErr
			}
			add(positions)
			go bridgeAccountChild(watchCtx, positions, func(event exchange.PositionEvent) {
				subscription.emit(exchange.AccountEvent{Kind: exchange.AccountEventPosition, Route: request.Route, InstrumentID: instrumentID, ObservedAt: observedAccountTime(event.Time), Position: &event})
			}, emitStatus, emitError)
		}
	}
	return subscription, nil
}

func bridgeAccountChild[T any](ctx context.Context, child exchange.Subscription[T], event func(T), status func(exchange.StreamStatusEvent), failure func(error)) {
	for {
		select {
		case <-ctx.Done():
			return
		case value, open := <-child.Events():
			if !open {
				return
			}
			event(value)
		case value, open := <-child.Status():
			if !open {
				return
			}
			status(value)
		case err, open := <-child.Errors():
			if !open {
				return
			}
			failure(err)
			return
		}
	}
}

func observedAccountTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Now().UTC()
	}
	return value.UTC()
}
