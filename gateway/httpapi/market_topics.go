package httpapi

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	realtimewire "github.com/QuantProcessing/boltertrader/api/exchange-gateway/v1"
	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/hotset"
	"github.com/QuantProcessing/boltertrader/gateway/markethub"
)

type marketStreams struct {
	hub    MarketHub
	keys   MarketKeys
	hotset MarketHotset
}

func (streams marketStreams) run(ctx context.Context, subscriptionID string, request realtimewire.TopicRequest, emit func(realtimewire.Message) bool) error {
	dataset := exchange.DatasetDepth
	switch request.Topic {
	case realtimewire.TopicBBO:
		dataset = exchange.DatasetBBO
	case realtimewire.TopicCandles:
		dataset = exchange.DatasetCandle
	case realtimewire.TopicMarkPrice:
		dataset = exchange.DatasetMark
	case realtimewire.TopicIndexPrice:
		dataset = exchange.DatasetIndex
	}
	key, err := streams.keys.SubscriptionKey(ctx, request.InstrumentID, dataset, request.Interval, request.Depth)
	if err != nil {
		return err
	}
	if streams.hotset != nil {
		reason := hotset.Reason(request.InternalPurpose)
		if reason == "" || reason.Validate() != nil {
			reason = hotset.ReasonBrowser
		}
		lease, leaseErr := streams.hotset.Acquire(ctx, hotset.Key{Owner: key.Owner, InstrumentID: request.InstrumentID}, reason)
		if leaseErr != nil {
			return leaseErr
		}
		defer lease.Release()
	}
	subscription, err := streams.hub.Subscribe(ctx, key, realtimewire.OutboundBuffer)
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
			wire := realtimewire.Message{Type: kind, SubscriptionID: subscriptionID, Topic: request.Topic, Generation: message.Generation, Sequence: message.Sequence, SourceSequence: message.SourceSequence, ObservedAt: message.ObservedAt, Data: marketWireData(request.Topic, message.Data, time.Now().UTC()), Reason: message.Reason}
			if kind == realtimewire.MessageResyncRequired {
				wire.Code = "UPSTREAM_GAP"
			}
			if !emit(wire) {
				return nil
			}
		}
	}
}

// A Hub replay retains the original fact. Recompute its age at delivery so a
// late consumer cannot mistake an old mark with cached ageMs=0 for a fresh one.
func marketWireData(topic realtimewire.Topic, payload json.RawMessage, now time.Time) json.RawMessage {
	if topic != realtimewire.TopicMarkPrice && topic != realtimewire.TopicIndexPrice {
		return payload
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(payload, &fields) != nil {
		return payload
	}
	var receivedAt time.Time
	if json.Unmarshal(fields["receivedAt"], &receivedAt) != nil || receivedAt.IsZero() {
		return payload
	}
	age := max(int64(0), now.Sub(receivedAt).Milliseconds())
	fields["ageMs"] = json.RawMessage(strconv.FormatInt(age, 10))
	encoded, err := json.Marshal(fields)
	if err != nil {
		return payload
	}
	return encoded
}
