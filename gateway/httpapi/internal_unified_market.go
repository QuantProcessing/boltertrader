package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	realtimewire "github.com/QuantProcessing/boltertrader/api/exchange-gateway/v1"
	"github.com/QuantProcessing/boltertrader/gateway/connectivity"
)

func (handler *InternalHandler) unifiedMarketStream(w http.ResponseWriter, r *http.Request) {
	if len(r.URL.Query()) != 0 {
		handler.writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", false)
		return
	}
	if handler.marketHub == nil || handler.marketKeys == nil {
		handler.writeError(w, r, http.StatusServiceUnavailable, "MARKET_STREAM_UNAVAILABLE", true)
		return
	}
	serveMarketStream(w, r, realtimewire.MaxInternalActiveTopics, nil, handler.runInternalTopic)
}

func (handler *InternalHandler) runInternalTopic(ctx context.Context, subscriptionID string, request realtimewire.TopicRequest, emit func(realtimewire.Message) bool) error {
	if isSharedMarketTopic(request.Topic) {
		if handler.marketHub == nil || handler.marketKeys == nil {
			return fmt.Errorf("shared market Hub unavailable")
		}
		return (marketStreams{hub: handler.marketHub, keys: handler.marketKeys, hotset: handler.hotset}).run(ctx, subscriptionID, request, emit)
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
	case realtimewire.TopicOrderBook, realtimewire.TopicBBO, realtimewire.TopicCandles, realtimewire.TopicMarkPrice, realtimewire.TopicIndexPrice:
		return true
	default:
		return false
	}
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
