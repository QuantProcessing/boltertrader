package factoryclient

import (
	"context"
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
	var mu sync.Mutex
	generation, sequence := spot.accountGeneration.Add(1), uint64(0)
	backendGeneration := uint64(0)
	gapOpen := false
	var subscription *typedSubscription[exchange.AccountEvent]
	closeChildren := func() error {
		cancel()
		return nil
	}
	id := fmt.Sprintf("%s:%s:account:%s:%d", spot.meta.venue, spot.meta.product, request.Route.RouteID, websocketSubscriptionSequence.Add(1))
	subscription = newTypedSubscription[exchange.AccountEvent](id, spot.meta, buffer, closeChildren)
	emitEvent := func(event exchange.AccountEvent) {
		mu.Lock()
		sequence++
		event.Generation, event.Sequence = generation, sequence
		if event.Phase == "" {
			event.Phase = exchange.AccountEventPhaseLive
		}
		mu.Unlock()
		subscription.emit(event)
	}
	emitStatus := func(status exchange.StreamStatusEvent) {
		if status.Time.IsZero() {
			status.Time = time.Now().UTC()
		}
		mu.Lock()
		if status.State == exchange.SubscriptionGap && status.Phase == exchange.GapStarted {
			generation = spot.accountGeneration.Add(1)
			sequence = 0
			gapOpen = true
		} else if status.Generation > backendGeneration && backendGeneration != 0 {
			if !gapOpen {
				generation = spot.accountGeneration.Add(1)
				sequence = 0
			}
		}
		if status.Generation > backendGeneration {
			backendGeneration = status.Generation
		}
		if status.State == exchange.SubscriptionActive {
			gapOpen = false
		}
		status.Generation = generation
		mu.Unlock()
		subscription.emitStatus(backendStatus{State: status.State, Phase: status.Phase, Generation: status.Generation, Reason: status.Reason, Time: status.Time})
		event := exchange.AccountEvent{Kind: exchange.AccountEventStreamStatus, Route: request.Route, ObservedAt: status.Time, Status: &status, Phase: exchange.AccountEventPhaseLive}
		emitEvent(event)
		if status.State == exchange.SubscriptionGap {
			emitEvent(exchange.AccountEvent{Kind: exchange.AccountEventResyncRequired, Route: request.Route, ObservedAt: status.Time, Phase: exchange.AccountEventPhaseLive, Reason: firstNonEmpty(status.Reason, "private-stream-gap")})
		}
	}
	emitError := func(err error) {
		subscription.emitError(err)
		emitEvent(exchange.AccountEvent{Kind: exchange.AccountEventResyncRequired, Route: request.Route, ObservedAt: time.Now().UTC(), Phase: exchange.AccountEventPhaseLive, Reason: "private-stream-error"})
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
				projected.SourceSequence = event.SourceSequence
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
	emitStatus(exchange.StreamStatusEvent{State: exchange.SubscriptionActive, Generation: generation, Time: time.Now().UTC()})
	return subscription, nil
}

func accountPhase(kind exchange.EventKind) exchange.AccountEventPhase {
	if kind == exchange.EventSnapshot {
		return exchange.AccountEventPhaseSnapshot
	}
	return exchange.AccountEventPhaseLive
}

func observedAccountTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Now().UTC()
	}
	return value.UTC()
}
