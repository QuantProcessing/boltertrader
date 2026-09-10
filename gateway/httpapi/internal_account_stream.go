package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	realtimewire "github.com/QuantProcessing/boltertrader/api/exchange-gateway/v1"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	gatewayevent "github.com/QuantProcessing/boltertrader/gateway/event"
)

// accountEventStream is the browser-facing private realtime channel. Gateway
// owns the exchange account connections, so forwarding these notifications via
// Trading only creates another failure boundary. State is still read from the
// authoritative REST projections; this socket says only which resource moved.
func (handler *InternalHandler) accountEventStream(w http.ResponseWriter, r *http.Request) {
	if handler.accountEvents == nil || handler.accountEventOwner.Validate() != nil || len(r.URL.Query()) != 0 {
		if handler.accountEvents == nil || handler.accountEventOwner.Validate() != nil {
			handler.writeError(w, r, http.StatusServiceUnavailable, "ACCOUNT_STREAM_UNAVAILABLE", true)
		} else {
			handler.writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", false)
		}
		return
	}

	serveMarketStream(w, r, 1, func(request realtimewire.TopicRequest) error {
		if request.Topic != realtimewire.TopicAccountEvents {
			return fmt.Errorf("account topic required")
		}
		return nil
	}, func(ctx context.Context, id string, _ realtimewire.TopicRequest, emit func(realtimewire.Message) bool) error {
		handler.runAccountEvents(ctx, id, emit)
		return nil
	})
}

func (handler *InternalHandler) runAccountEvents(ctx context.Context, subscriptionID string, emit func(realtimewire.Message) bool) {
	subscription, connections, err := handler.accountEvents.SubscribePrincipalWithConnections(ctx, handler.accountEventOwner, realtimewire.OutboundBuffer)
	if err != nil {
		emit(realtimewire.Message{Type: realtimewire.MessageError, SubscriptionID: subscriptionID, Topic: realtimewire.TopicAccountEvents, Code: "UPSTREAM_UNAVAILABLE", Reason: "account-event-stream-unavailable", ObservedAt: time.Now().UTC()})
		return
	}
	defer subscription.Close()
	for _, event := range connections {
		if !emitAccountEvent(subscriptionID, event, emit) {
			return
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case event, open := <-subscription.Events():
			if !open {
				code, reason := "UPSTREAM_UNAVAILABLE", "account-event-stream-ended"
				select {
				case streamErr := <-subscription.Errors():
					if streamErr == gatewayevent.ErrSlowConsumer {
						code, reason = "CONSUMER_LAGGED", "account-event-consumer-lagged"
					}
				default:
				}
				emit(realtimewire.Message{Type: realtimewire.MessageError, SubscriptionID: subscriptionID, Topic: realtimewire.TopicAccountEvents, Code: code, Reason: reason, ObservedAt: time.Now().UTC()})
				return
			}
			if !emitAccountEvent(subscriptionID, event, emit) {
				return
			}
		}
	}
}

func emitAccountEvent(subscriptionID string, event gatewayevent.Event, emit func(realtimewire.Message) bool) bool {
	return emit(realtimewire.Message{
		Type: realtimewire.MessageDelta, SubscriptionID: subscriptionID, Topic: realtimewire.TopicAccountEvents,
		ObservedAt: event.OccurredAt, Data: realtimewire.EncodeData(accountWireEvent(event)),
	})
}

type browserAccountEvent struct {
	EventID         domain.EventID         `json:"eventId"`
	AccountID       domain.AccountID       `json:"accountId"`
	ResourceVersion domain.ResourceVersion `json:"resourceVersion"`
	Kind            string                 `json:"kind"`
	ObservedAt      time.Time              `json:"observedAt"`
	Payload         json.RawMessage        `json:"payload"`
}

func accountWireEvent(event gatewayevent.Event) browserAccountEvent {
	return browserAccountEvent{
		EventID: event.EventID, AccountID: event.AccountID, ResourceVersion: event.ResourceVersion,
		Kind: "account." + string(event.Kind), ObservedAt: event.OccurredAt.UTC(),
		Payload: append(json.RawMessage(nil), event.Payload...),
	}
}
