package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/QuantProcessing/boltertrader/gateway/realtimewire"
	"github.com/QuantProcessing/boltertrader/internal/httpx"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

type MarketTopicProjection interface {
	Subscribe(context.Context, realtimewire.TopicRequest, int) (realtimewire.Subscription, error)
}

type browserControlResult struct {
	control realtimewire.Control
	err     error
}
type browserActiveTopic struct {
	cancel context.CancelFunc
	topic  realtimewire.Topic
}

func (api *PublicDataAPI) unifiedRealtime(w http.ResponseWriter, r *http.Request) {
	if api.dependencies.Arbitrage == nil || len(r.URL.Query()) != 0 {
		if api.dependencies.Arbitrage == nil {
			httpx.WriteError(w, http.StatusServiceUnavailable, "REALTIME_UNAVAILABLE", "realtime projection is unavailable", nil, true, httpx.RequestID(r))
		} else {
			httpx.WriteError(w, http.StatusBadRequest, "VALIDATION_ERROR", "request is invalid", nil, false, httpx.RequestID(r))
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
	controls := make(chan browserControlResult, 1)
	lagged := make(chan string, 1)
	done := make(chan string, realtimewire.MaxActiveTopics)
	go readBrowserControls(ctx, connection, controls)
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
	active := make(map[string]browserActiveTopic)
	var next uint64
	for {
		select {
		case <-ctx.Done():
			for _, current := range active {
				current.cancel()
			}
			return
		case <-writeDone:
			for _, current := range active {
				current.cancel()
			}
			return
		case id := <-done:
			if current, exists := active[id]; exists {
				current.cancel()
				delete(active, id)
			}
		case id := <-lagged:
			if current, ok := active[id]; ok && api.dependencies.RealtimeObserver != nil {
				api.dependencies.RealtimeObserver.ObserveRealtimeResync(current.topic)
			}
			emit(realtimewire.Message{Type: realtimewire.MessageResyncRequired, SubscriptionID: id, Code: "CONSUMER_LAGGED", Reason: "outbound-buffer-exhausted", ObservedAt: time.Now().UTC()})
			_ = connection.Close(websocket.StatusTryAgainLater, "resync_required")
			return
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
					if current, exists := active[request.SubscriptionID]; exists {
						current.cancel()
						delete(active, request.SubscriptionID)
					}
					emit(realtimewire.Message{Type: realtimewire.MessageAck, RequestID: control.RequestID, SubscriptionID: request.SubscriptionID, Topic: request.Topic, ObservedAt: time.Now().UTC()})
				}
			case realtimewire.OperationSubscribe:
				if len(active)+len(control.Topics) > realtimewire.MaxActiveTopics {
					emit(realtimewire.Message{Type: realtimewire.MessageError, RequestID: control.RequestID, Code: "TOPIC_LIMIT_EXCEEDED", ObservedAt: time.Now().UTC()})
					continue
				}
				for _, request := range control.Topics {
					if request.Topic != realtimewire.TopicArbitrageOpportunities {
						emit(realtimewire.Message{Type: realtimewire.MessageError, RequestID: control.RequestID, Topic: request.Topic, Code: "TOPIC_MOVED_TO_GATEWAY", ObservedAt: time.Now().UTC()})
						continue
					}
					next++
					id := fmt.Sprintf("browser-%d", next)
					subCtx, subCancel := context.WithCancel(ctx)
					active[id] = browserActiveTopic{cancel: subCancel, topic: request.Topic}
					emit(realtimewire.Message{Type: realtimewire.MessageAck, RequestID: control.RequestID, SubscriptionID: id, Topic: request.Topic, ObservedAt: time.Now().UTC()})
					go func() {
						defer func() {
							select {
							case done <- id:
							case <-ctx.Done():
							}
						}()
						streamErr := api.runBrowserArbitrageTopic(subCtx, id, request, emit)
						if streamErr != nil && subCtx.Err() == nil {
							if api.dependencies.RealtimeObserver != nil {
								api.dependencies.RealtimeObserver.ObserveRealtimeResync(request.Topic)
							}
							emit(realtimewire.Message{Type: realtimewire.MessageResyncRequired, SubscriptionID: id, Topic: request.Topic, Code: "UPSTREAM_UNAVAILABLE", Reason: "topic-stream-ended", ObservedAt: time.Now().UTC()})
						}
					}()
				}
			}
		}
	}
}

func (api *PublicDataAPI) runBrowserArbitrageTopic(ctx context.Context, id string, request realtimewire.TopicRequest, emit func(realtimewire.Message) bool) error {
	if api.dependencies.Arbitrage == nil {
		return fmt.Errorf("arbitrage projection unavailable")
	}
	current, ready, updates, cancel, err := api.dependencies.Arbitrage.Subscribe(ctx, 4)
	if err != nil {
		return err
	}
	defer cancel()
	send := func(snapshot arbitrageProjectionSnapshot, kind realtimewire.MessageType) bool {
		return emit(realtimewire.Message{Type: kind, SubscriptionID: id, Topic: request.Topic, Generation: snapshot.Generation, Sequence: snapshot.Sequence, ObservedAt: snapshot.ObservedAt, Data: realtimewire.EncodeData(map[string]any{"items": snapshot.Items, "observedAt": snapshot.ObservedAt})})
	}
	if ready && !send(current, realtimewire.MessageSnapshot) {
		return nil
	}
	first := !ready
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case snapshot, open := <-updates:
			if !open {
				return fmt.Errorf("arbitrage projection consumer gap")
			}
			kind := realtimewire.MessageDelta
			if first {
				kind, first = realtimewire.MessageSnapshot, false
			}
			if !send(snapshot, kind) {
				return nil
			}
		}
	}
}

func readBrowserControls(ctx context.Context, connection *websocket.Conn, target chan<- browserControlResult) {
	for {
		var control realtimewire.Control
		err := wsjson.Read(ctx, connection, &control)
		if err == nil {
			err = control.Validate()
		}
		select {
		case target <- browserControlResult{control: control, err: err}:
		case <-ctx.Done():
			return
		}
		if err != nil {
			return
		}
	}
}
