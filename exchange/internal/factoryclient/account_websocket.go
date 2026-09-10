package factoryclient

import (
	"context"
	"fmt"
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
	catalog := spot.accountInstruments()
	if len(catalog) == 0 {
		return nil, fmt.Errorf("account instrument index is unavailable")
	}
	index, err := newAccountInstrumentIndex(request.Route, catalog)
	if err != nil {
		return nil, err
	}
	backend, ok := spot.private.backend.(accountEventBackend)
	if !ok {
		return nil, fmt.Errorf("native account event stream is unavailable")
	}
	watchCtx, cancel := context.WithCancel(ctx)
	var subscription *typedSubscription[exchange.AccountEvent]
	closeChildren := func() error {
		cancel()
		return nil
	}
	id := fmt.Sprintf("%s:%s:account:%s:%d", spot.meta.venue, spot.meta.product, request.Route.RouteID, websocketSubscriptionSequence.Add(1))
	subscription = newTypedSubscription[exchange.AccountEvent](id, spot.meta, buffer, closeChildren)
	emitEvent := func(event exchange.AccountEvent) {
		subscription.emit(event)
	}
	emitStatus := func(status exchange.StreamStatusEvent) {
		if status.Time.IsZero() {
			status.Time = time.Now().UTC()
		}
		state := exchange.SubscriptionConnecting
		if status.State == exchange.SubscriptionActive {
			state = exchange.SubscriptionActive
		} else if status.State == exchange.SubscriptionClosed {
			state = exchange.SubscriptionClosed
		}
		// Account streams expose connection liveness only. Sequence continuity,
		// generations and resync phases belong to incremental public books; the
		// fixed REST reconciliation pass repairs any private-event blind period.
		subscription.emitStatus(backendStatus{State: state, Reason: status.Reason, Time: status.Time})
	}
	emitError := func(err error) {
		subscription.emitError(err)
	}

	stop, err := backend.StartAccountEvents(watchCtx, index, accountEventCallbacks{
		Event: func(event accountBackendEvent) {
			if watchCtx.Err() != nil {
				return
			}
			projectedEvents, projectErr := projectAccountBackendEvent(request.Route, index, event)
			if projectErr != nil {
				emitError(projectErr)
				return
			}
			for _, projected := range projectedEvents {
				emitEvent(projected)
			}
		},
		Status: emitStatus,
		Error:  emitError,
	})
	if err != nil {
		_ = subscription.Close()
		return nil, err
	}
	previousClose := subscription.closeFn
	subscription.closeFn = func() error {
		if previousClose != nil {
			previousClose()
		}
		return stop()
	}
	emitStatus(exchange.StreamStatusEvent{State: exchange.SubscriptionActive, Time: time.Now().UTC()})
	return subscription, nil
}

func observedAccountTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Now().UTC()
	}
	return value.UTC()
}
