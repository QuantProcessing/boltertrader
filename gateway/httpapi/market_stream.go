package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	realtimewire "github.com/QuantProcessing/boltertrader/api/exchange-gateway/v1"
	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

type realtimeControlResult struct {
	control realtimewire.Control
	err     error
}

// serveMarketStream owns the wire session for both application and internal
// consumers. The caller supplies the allowed topics and the subscription budget.
func serveMarketStream(w http.ResponseWriter, r *http.Request, maxTopics int, validateTopic func(realtimewire.TopicRequest) error, runTopic func(context.Context, string, realtimewire.TopicRequest, func(realtimewire.Message) bool) error) {
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
	lagged := make(chan string, 1)
	done := make(chan string, maxTopics)
	go readRealtimeControls(ctx, connection, controls)
	writeDone := make(chan error, 1)
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

	type activeSubscription struct {
		cancel context.CancelFunc
		topic  realtimewire.Topic
	}
	active := make(map[string]activeSubscription)
	var next uint64
	emit := func(message realtimewire.Message) bool {
		select {
		case <-ctx.Done():
			return false
		case outbound <- message:
			return true
		default:
			select {
			case lagged <- message.SubscriptionID:
			default:
			}
			return false
		}
	}
	for {
		select {
		case <-ctx.Done():
			<-writeDone
			return
		case <-writeDone:
			return
		case subscriptionID := <-lagged:
			emit(realtimewire.Message{Type: realtimewire.MessageResyncRequired, SubscriptionID: subscriptionID, ObservedAt: time.Now().UTC(), Code: "CONSUMER_LAGGED", Reason: "outbound-buffer-exhausted"})
			_ = connection.Close(websocket.StatusTryAgainLater, "resync_required")
			return
		case subscriptionID := <-done:
			if current, exists := active[subscriptionID]; exists {
				current.cancel()
				delete(active, subscriptionID)
			}
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
					var topic realtimewire.Topic
					if current, exists := active[request.SubscriptionID]; exists {
						topic = current.topic
						current.cancel()
						delete(active, request.SubscriptionID)
					}
					emit(realtimewire.Message{Type: realtimewire.MessageAck, RequestID: control.RequestID, SubscriptionID: request.SubscriptionID, Topic: topic, ObservedAt: time.Now().UTC()})
				}
			case realtimewire.OperationSubscribe:
				valid := true
				if validateTopic != nil {
					for _, request := range control.Topics {
						if validateTopic(request) != nil {
							valid = false
							break
						}
					}
				}
				if !valid {
					emit(realtimewire.Message{Type: realtimewire.MessageError, RequestID: control.RequestID, Code: "INVALID_TOPIC", ObservedAt: time.Now().UTC()})
					continue
				}
				if len(active)+len(control.Topics) > maxTopics {
					emit(realtimewire.Message{Type: realtimewire.MessageError, RequestID: control.RequestID, Code: "TOPIC_LIMIT_EXCEEDED", ObservedAt: time.Now().UTC()})
					continue
				}
				for _, request := range control.Topics {
					next++
					subscriptionID := fmt.Sprintf("market-%d", next)
					subCtx, subCancel := context.WithCancel(ctx)
					active[subscriptionID] = activeSubscription{cancel: subCancel, topic: request.Topic}
					emit(realtimewire.Message{Type: realtimewire.MessageAck, RequestID: control.RequestID, SubscriptionID: subscriptionID, Topic: request.Topic, ObservedAt: time.Now().UTC()})
					go func() {
						defer func() {
							select {
							case done <- subscriptionID:
							case <-ctx.Done():
							}
						}()
						if streamErr := runTopic(subCtx, subscriptionID, request, emit); streamErr != nil && subCtx.Err() == nil {
							message := realtimewire.Message{Type: realtimewire.MessageResyncRequired, SubscriptionID: subscriptionID, Topic: request.Topic, ObservedAt: time.Now().UTC(), Code: "UPSTREAM_UNAVAILABLE", Reason: "upstream-stream-ended"}
							if errors.Is(streamErr, exchange.ErrNotFound) {
								message.Type, message.Code, message.Reason = realtimewire.MessageError, "INSTRUMENT_UNKNOWN", "instrument-not-in-catalog"
							} else if errors.Is(streamErr, exchange.ErrUnsupported) {
								message.Type, message.Code, message.Reason = realtimewire.MessageError, "UNSUPPORTED_TOPIC", "topic-not-supported"
							}
							emit(message)
						}
					}()
				}
			}
		}
	}
}

func readRealtimeControls(ctx context.Context, connection *websocket.Conn, target chan<- realtimeControlResult) {
	for {
		var control realtimewire.Control
		err := wsjson.Read(ctx, connection, &control)
		if err == nil {
			err = control.Validate()
		}
		select {
		case target <- realtimeControlResult{control: control, err: err}:
		case <-ctx.Done():
			return
		}
		if err != nil {
			return
		}
	}
}
