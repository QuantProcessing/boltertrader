package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/connectivity"
	"github.com/QuantProcessing/boltertrader/gateway/hotset"
	"github.com/QuantProcessing/boltertrader/gateway/markethub"
	"github.com/QuantProcessing/boltertrader/gateway/realtimewire"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

type internalControlResult struct {
	control realtimewire.Control
	err     error
}

func (handler *InternalHandler) unifiedMarketStream(w http.ResponseWriter, r *http.Request) {
	if handler.publicMarkets == nil || len(r.URL.Query()) != 0 {
		if handler.publicMarkets == nil {
			handler.writeError(w, r, http.StatusServiceUnavailable, "MARKET_STREAM_UNAVAILABLE", true)
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
	controls := make(chan internalControlResult, 1)
	lagged := make(chan string, 1)
	done := make(chan string, realtimewire.MaxInternalActiveTopics)
	go readInternalControls(ctx, connection, controls)
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

	type activeSubscription struct{ cancel context.CancelFunc }
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
					if current, exists := active[request.SubscriptionID]; exists {
						current.cancel()
						delete(active, request.SubscriptionID)
					}
					emit(realtimewire.Message{Type: realtimewire.MessageAck, RequestID: control.RequestID, SubscriptionID: request.SubscriptionID, Topic: request.Topic, ObservedAt: time.Now().UTC()})
				}
			case realtimewire.OperationSubscribe:
				if len(active)+len(control.Topics) > realtimewire.MaxInternalActiveTopics {
					emit(realtimewire.Message{Type: realtimewire.MessageError, RequestID: control.RequestID, Code: "TOPIC_LIMIT_EXCEEDED", ObservedAt: time.Now().UTC()})
					continue
				}
				for _, request := range control.Topics {
					next++
					subscriptionID := fmt.Sprintf("market-%d", next)
					subCtx, subCancel := context.WithCancel(ctx)
					active[subscriptionID] = activeSubscription{cancel: subCancel}
					emit(realtimewire.Message{Type: realtimewire.MessageAck, RequestID: control.RequestID, SubscriptionID: subscriptionID, Topic: request.Topic, ObservedAt: time.Now().UTC()})
					go func() {
						defer func() {
							select {
							case done <- subscriptionID:
							case <-ctx.Done():
							}
						}()
						if streamErr := handler.runInternalTopic(subCtx, subscriptionID, request, emit); streamErr != nil && subCtx.Err() == nil {
							emit(realtimewire.Message{Type: realtimewire.MessageResyncRequired, SubscriptionID: subscriptionID, Topic: request.Topic, Generation: 1, ObservedAt: time.Now().UTC(), Code: "UPSTREAM_UNAVAILABLE", Reason: "upstream-stream-ended"})
						}
					}()
				}
			}
		}
	}
}

func readInternalControls(ctx context.Context, connection *websocket.Conn, target chan<- internalControlResult) {
	for {
		var control realtimewire.Control
		err := wsjson.Read(ctx, connection, &control)
		if err == nil {
			err = control.Validate()
		}
		select {
		case target <- internalControlResult{control: control, err: err}:
		case <-ctx.Done():
			return
		}
		if err != nil {
			return
		}
	}
}

func (handler *InternalHandler) runInternalTopic(ctx context.Context, subscriptionID string, request realtimewire.TopicRequest, emit func(realtimewire.Message) bool) error {
	if isSharedMarketTopic(request.Topic) {
		if handler.marketHub == nil || handler.marketKeys == nil {
			return fmt.Errorf("shared market Hub unavailable")
		}
		return handler.runSharedMarketTopic(ctx, subscriptionID, request, emit)
	}
	sequence, generation := uint64(0), uint64(1)
	send := func(kind realtimewire.MessageType, observedAt time.Time, sourceSequence string, data any) bool {
		sequence++
		return emit(realtimewire.Message{Type: kind, SubscriptionID: subscriptionID, Topic: request.Topic, Generation: generation, Sequence: sequence, SourceSequence: sourceSequence, ObservedAt: observedAt.UTC(), Data: realtimewire.EncodeData(data)})
	}
	switch request.Topic {
	case realtimewire.TopicFundingBoard:
		if handler.fundingBoardChanges == nil {
			return fmt.Errorf("funding board stream unavailable")
		}
		changes, cancel, err := handler.fundingBoardChanges.Subscribe(ctx)
		if err != nil {
			return err
		}
		defer cancel()
		read := func(kind realtimewire.MessageType) error {
			response, readErr := handler.authority.Execute(ctx, AuthorityRequest{Operation: AuthorityFundingBoard})
			if readErr != nil {
				return readErr
			}
			if !send(kind, time.Now().UTC(), "", response.Body) {
				return context.Canceled
			}
			return nil
		}
		if err := read(realtimewire.MessageSnapshot); err != nil {
			return err
		}
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case _, open := <-changes:
				if !open {
					return fmt.Errorf("funding board consumer gap")
				}
				if err := read(realtimewire.MessageDelta); err != nil {
					return err
				}
			}
		}
	case realtimewire.TopicVenueConnectivity:
		if handler.venueConnectivity == nil {
			return fmt.Errorf("venue connectivity unavailable")
		}
		var previous string
		for {
			data := connectivityWireSnapshot(handler.venueConnectivity.Snapshot())
			encoded, _ := json.Marshal(data)
			if string(encoded) != previous {
				kind := realtimewire.MessageDelta
				if previous == "" {
					kind = realtimewire.MessageSnapshot
				}
				if !send(kind, time.Now().UTC(), "", data) {
					return nil
				}
				previous = string(encoded)
			}
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	default:
		return fmt.Errorf("internal market topic unsupported")
	}
}

func isSharedMarketTopic(topic realtimewire.Topic) bool {
	switch topic {
	case realtimewire.TopicOrderBook, realtimewire.TopicBBO, realtimewire.TopicCandles, realtimewire.TopicMarkPrice:
		return true
	default:
		return false
	}
}

func (handler *InternalHandler) runSharedMarketTopic(ctx context.Context, subscriptionID string, request realtimewire.TopicRequest, emit func(realtimewire.Message) bool) error {
	dataset := exchange.DatasetDepth
	switch request.Topic {
	case realtimewire.TopicBBO:
		dataset = exchange.DatasetBBO
	case realtimewire.TopicCandles:
		dataset = exchange.DatasetCandle
	case realtimewire.TopicMarkPrice:
		dataset = exchange.DatasetMark
	}
	key, err := handler.marketKeys.SubscriptionKey(ctx, request.InstrumentID, dataset, request.Interval, request.Depth)
	if err != nil {
		return err
	}
	if handler.hotset != nil {
		reason := hotset.Reason(request.InternalPurpose)
		if reason == "" || reason.Validate() != nil {
			reason = hotset.ReasonBrowser
		}
		lease, leaseErr := handler.hotset.Acquire(ctx, hotset.Key{Owner: key.Owner, InstrumentID: request.InstrumentID}, reason)
		if leaseErr != nil {
			return leaseErr
		}
		defer lease.Release()
	}
	subscription, err := handler.marketHub.Subscribe(ctx, key, realtimewire.OutboundBuffer)
	if err != nil {
		return err
	}
	defer subscription.Close()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case message, open := <-subscription.Events():
			if !open {
				return markethub.ErrResyncRequired
			}
			kind := realtimewire.MessageDelta
			switch message.Kind {
			case markethub.MessageSnapshot:
				kind = realtimewire.MessageSnapshot
			case markethub.MessageFull:
				kind = realtimewire.MessageFull
			case markethub.MessageResyncRequired:
				kind = realtimewire.MessageResyncRequired
			}
			wire := realtimewire.Message{Type: kind, SubscriptionID: subscriptionID, Topic: request.Topic, Generation: message.Generation, Sequence: message.Sequence, SourceSequence: message.SourceSequence, ObservedAt: message.ObservedAt, Data: message.Data, Reason: message.Reason}
			if kind == realtimewire.MessageResyncRequired {
				wire.Code = "UPSTREAM_GAP"
			}
			if dataset == exchange.DatasetBBO {
				handler.publishWireBBO(request.InstrumentID, message)
			}
			if !emit(wire) {
				return nil
			}
		}
	}
}

func (handler *InternalHandler) publishWireBBO(instrumentID string, message markethub.Message) {
	publisher, ok := handler.freshBBO.(interface {
		Publish(markethub.BBOQuote) error
	})
	if !ok || (message.Kind != markethub.MessageSnapshot && message.Kind != markethub.MessageDelta) {
		return
	}
	var value struct {
		Bid exchange.BookLevel `json:"bid"`
		Ask exchange.BookLevel `json:"ask"`
	}
	if json.Unmarshal(message.Data, &value) != nil {
		return
	}
	_ = publisher.Publish(markethub.BBOQuote{InstrumentID: instrumentID, Bid: value.Bid, Ask: value.Ask, ObservedAt: message.ObservedAt, SourceSequence: message.SourceSequence})
}

type connectivityWireItem struct {
	Venue               string     `json:"venue"`
	State               string     `json:"state"`
	LatencyMS           float64    `json:"latencyMs"`
	EWMALatencyMS       float64    `json:"ewmaLatencyMs"`
	ConsecutiveFailures int        `json:"consecutiveFailures"`
	LastSuccessAt       *time.Time `json:"lastSuccessAt,omitempty"`
	LastErrorAt         *time.Time `json:"lastErrorAt,omitempty"`
}

func connectivityWireSnapshot(items []connectivity.Snapshot) []connectivityWireItem {
	result := make([]connectivityWireItem, 0, len(items))
	for _, item := range items {
		wire := connectivityWireItem{
			Venue: string(item.Venue), State: string(item.State),
			LatencyMS:           float64(item.Latency) / float64(time.Millisecond),
			EWMALatencyMS:       float64(item.EWMALatency) / float64(time.Millisecond),
			ConsecutiveFailures: item.ConsecutiveFailures,
		}
		if !item.LastSuccessAt.IsZero() {
			at := item.LastSuccessAt.UTC()
			wire.LastSuccessAt = &at
		}
		if !item.LastErrorAt.IsZero() {
			at := item.LastErrorAt.UTC()
			wire.LastErrorAt = &at
		}
		result = append(result, wire)
	}
	return result
}
