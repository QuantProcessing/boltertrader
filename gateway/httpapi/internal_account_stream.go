package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	realtimewire "github.com/QuantProcessing/boltertrader/api/exchange-gateway/v1"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	gatewayevent "github.com/QuantProcessing/boltertrader/gateway/event"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
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
	connection, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer connection.CloseNow()
	connection.SetReadLimit(realtimewire.MaxControlBytes)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	outbound := make(chan realtimewire.Message, realtimewire.OutboundBuffer)
	controls := make(chan realtimeControlResult, 1)
	writeDone := make(chan error, 1)
	topicDone := make(chan struct{}, 1)
	lagged := make(chan struct{}, 1)
	go readRealtimeControls(ctx, connection, controls)
	go func() {
		for {
			select {
			case <-ctx.Done():
				writeDone <- ctx.Err()
				return
			case message := <-outbound:
				writeCtx, writeCancel := context.WithTimeout(ctx, 10*time.Second)
				err := wsjson.Write(writeCtx, connection, message)
				writeCancel()
				if err != nil {
					writeDone <- err
					return
				}
			}
		}
	}()
	emit := func(message realtimewire.Message) bool {
		select {
		case <-ctx.Done():
			return false
		case outbound <- message:
			return true
		default:
			select {
			case lagged <- struct{}{}:
			default:
			}
			return false
		}
	}

	var activeCancel context.CancelFunc
	activeID := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-writeDone:
			return
		case <-lagged:
			_ = connection.Close(websocket.StatusTryAgainLater, "consumer_lagged")
			return
		case <-topicDone:
			if activeCancel != nil {
				activeCancel()
			}
			activeCancel, activeID = nil, ""
		case result := <-controls:
			if result.err != nil {
				_ = connection.Close(websocket.StatusPolicyViolation, "invalid_control")
				return
			}
			control := result.control
			switch control.Operation {
			case realtimewire.OperationPing:
				emit(realtimewire.Message{Type: realtimewire.MessagePong, RequestID: control.RequestID, ObservedAt: time.Now().UTC()})
			case realtimewire.OperationUnsubscribe:
				for _, request := range control.Topics {
					if request.SubscriptionID == activeID && activeCancel != nil {
						activeCancel()
						activeCancel, activeID = nil, ""
					}
					emit(realtimewire.Message{Type: realtimewire.MessageAck, RequestID: control.RequestID, SubscriptionID: request.SubscriptionID, Topic: request.Topic, ObservedAt: time.Now().UTC()})
				}
			case realtimewire.OperationSubscribe:
				for _, request := range control.Topics {
					if request.Topic != realtimewire.TopicAccountEvents || activeCancel != nil {
						emit(realtimewire.Message{Type: realtimewire.MessageError, RequestID: control.RequestID, Topic: request.Topic, Code: "TOPIC_UNSUPPORTED", ObservedAt: time.Now().UTC()})
						continue
					}
					activeID = "account-events"
					subCtx, subCancel := context.WithCancel(ctx)
					activeCancel = subCancel
					emit(realtimewire.Message{Type: realtimewire.MessageAck, RequestID: control.RequestID, SubscriptionID: activeID, Topic: request.Topic, ObservedAt: time.Now().UTC()})
					go func(subscriptionID string) {
						handler.runAccountEvents(subCtx, subscriptionID, emit)
						select {
						case topicDone <- struct{}{}:
						case <-ctx.Done():
						}
					}(activeID)
				}
			}
		}
	}
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
