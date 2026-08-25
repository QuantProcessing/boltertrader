// Package marketowner adapts the public connector surface to one native feed
// owner consumed by markethub. It is the only layer allowed to open public
// per-instrument WebSocket topics.
package marketowner

import (
	"context"
	"fmt"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/markethub"
	"github.com/QuantProcessing/boltertrader/gateway/publicmarket"
)

type Owner struct {
	Source     *publicmarket.Source
	BoardMarks BoardMarks
}

func (owner Owner) Open(ctx context.Context, key markethub.SubscriptionKey) (markethub.NativeStream, error) {
	if owner.Source == nil {
		return markethub.NativeStream{}, markethub.ErrUnavailable
	}
	switch key.Dataset {
	case exchange.DatasetDepth:
		stream, err := owner.Source.WatchOrderBook(ctx, key.InstrumentID, key.Depth)
		if err != nil {
			return markethub.NativeStream{}, err
		}
		events, failures := make(chan markethub.Message, 256), make(chan error, 1)
		go forwardBook(ctx, stream.Subscription, events, failures)
		return markethub.NativeStream{
			Snapshot: message(key, markethub.MessageSnapshot, observed(stream.Snapshot.Time), stream.Snapshot.Sequence, map[string]any{"bids": nonNilBook(stream.Snapshot.Bids), "asks": nonNilBook(stream.Snapshot.Asks)}),
			Events:   events, Errors: failures, Close: stream.Subscription.Close,
		}, nil
	case exchange.DatasetBBO:
		stream, err := owner.Source.WatchBBO(ctx, key.InstrumentID)
		if err != nil {
			return markethub.NativeStream{}, err
		}
		events, failures := make(chan markethub.Message, 256), make(chan error, 1)
		go forwardBBO(ctx, stream.Subscription, events, failures)
		return markethub.NativeStream{Snapshot: message(key, markethub.MessageSnapshot, observed(stream.Snapshot.Time), stream.Snapshot.SourceSequence, map[string]any{"bid": stream.Snapshot.Bid, "ask": stream.Snapshot.Ask}), Events: events, Errors: failures, Close: stream.Subscription.Close}, nil
	case exchange.DatasetCandle:
		stream, err := owner.Source.WatchCandles(ctx, key.InstrumentID, key.Interval)
		if err != nil {
			return markethub.NativeStream{}, err
		}
		events, failures := make(chan markethub.Message, 64), make(chan error, 1)
		go forwardCandles(ctx, stream.Subscription, events, failures)
		candles := make([]wireCandle, 0, len(stream.Snapshot.Candles))
		for _, candle := range stream.Snapshot.Candles {
			candles = append(candles, toWireCandle(candle))
		}
		return markethub.NativeStream{Snapshot: message(key, markethub.MessageSnapshot, time.Now().UTC(), "", map[string]any{"interval": key.Interval, "candles": candles}), Events: events, Errors: failures, Close: stream.Subscription.Close}, nil
	case exchange.DatasetMark:
		if stream, supported, err := owner.BoardMarks.Open(ctx, key); supported || err != nil {
			return stream, err
		}
		stream, err := owner.Source.WatchMarkPrice(ctx, key.InstrumentID)
		if err != nil {
			return markethub.NativeStream{}, err
		}
		events, failures := make(chan markethub.Message, 64), make(chan error, 1)
		owner.BoardMarks.PublishMark(ctx, key.InstrumentID, exchange.OptionalDecimal{Value: stream.Snapshot.Price, Valid: true}, observed(stream.Snapshot.Time))
		go forwardMark(ctx, key.InstrumentID, owner.BoardMarks, stream.Subscription, events, failures)
		return markethub.NativeStream{Snapshot: message(key, markethub.MessageSnapshot, observed(stream.Snapshot.Time), "", map[string]any{"price": stream.Snapshot.Price.String()}), Events: events, Errors: failures, Close: stream.Subscription.Close}, nil
	default:
		return markethub.NativeStream{}, fmt.Errorf("%w: dataset %s", markethub.ErrUnavailable, key.Dataset)
	}
}

func forwardBook(ctx context.Context, subscription exchange.Subscription[exchange.BookEvent], output chan<- markethub.Message, failures chan<- error) {
	defer close(output)
	defer close(failures)
	for {
		select {
		case <-ctx.Done():
			return
		case event, open := <-subscription.Events():
			if !open {
				return
			}
			kind := markethub.MessageDelta
			if event.Resync {
				kind = markethub.MessageResyncRequired
			}
			if !emit(ctx, output, markethub.Message{Kind: kind, ObservedAt: observed(event.Time), SourceSequence: event.Sequence, Data: markethub.Encode(map[string]any{"kind": event.Kind, "previous": event.Previous, "bids": nonNilBook(event.Bids), "asks": nonNilBook(event.Asks)})}) {
				return
			}
		case err, open := <-subscription.Errors():
			if !open {
				return
			}
			select {
			case failures <- err:
			case <-ctx.Done():
			}
			return
		}
	}
}

func forwardBBO(ctx context.Context, subscription exchange.Subscription[exchange.BBOEvent], output chan<- markethub.Message, failures chan<- error) {
	defer close(output)
	defer close(failures)
	for {
		select {
		case <-ctx.Done():
			return
		case event, open := <-subscription.Events():
			if !open {
				return
			}
			if !emit(ctx, output, markethub.Message{Kind: markethub.MessageDelta, ObservedAt: observed(event.Time), SourceSequence: event.SourceSequence, Data: markethub.Encode(map[string]any{"bid": event.Bid, "ask": event.Ask})}) {
				return
			}
		case err, open := <-subscription.Errors():
			if !open {
				return
			}
			select {
			case failures <- err:
			case <-ctx.Done():
			}
			return
		}
	}
}

func forwardCandles(ctx context.Context, subscription exchange.Subscription[exchange.CandleEvent], output chan<- markethub.Message, failures chan<- error) {
	defer close(output)
	defer close(failures)
	for {
		select {
		case <-ctx.Done():
			return
		case event, open := <-subscription.Events():
			if !open {
				return
			}
			if !emit(ctx, output, markethub.Message{Kind: markethub.MessageDelta, ObservedAt: observed(event.Candle.CloseTime), Data: markethub.Encode(map[string]any{"interval": event.Interval, "candle": toWireCandle(event.Candle)})}) {
				return
			}
		case err, open := <-subscription.Errors():
			if !open {
				return
			}
			select {
			case failures <- err:
			case <-ctx.Done():
			}
			return
		}
	}
}

func forwardMark(ctx context.Context, instrumentID string, sink BoardMarks, subscription exchange.Subscription[exchange.MarkPriceEvent], output chan<- markethub.Message, failures chan<- error) {
	defer close(output)
	defer close(failures)
	for {
		select {
		case <-ctx.Done():
			return
		case event, open := <-subscription.Events():
			if !open {
				return
			}
			sink.PublishMark(ctx, instrumentID, exchange.OptionalDecimal{Value: event.Price, Valid: true}, observed(event.Time))
			if !emit(ctx, output, markethub.Message{Kind: markethub.MessageDelta, ObservedAt: observed(event.Time), Data: markethub.Encode(map[string]any{"price": event.Price.String()})}) {
				return
			}
		case err, open := <-subscription.Errors():
			if !open {
				return
			}
			select {
			case failures <- err:
			case <-ctx.Done():
			}
			return
		}
	}
}

func message(key markethub.SubscriptionKey, kind markethub.MessageKind, at time.Time, sourceSequence string, data any) markethub.Message {
	return markethub.Message{Kind: kind, Key: key, ObservedAt: at, SourceSequence: sourceSequence, Data: markethub.Encode(data)}
}

func emit(ctx context.Context, output chan<- markethub.Message, message markethub.Message) bool {
	select {
	case output <- message:
		return true
	case <-ctx.Done():
		return false
	}
}

func observed(at time.Time) time.Time {
	if at.IsZero() {
		return time.Now().UTC()
	}
	return at.UTC()
}
func nonNilBook(levels []exchange.BookLevel) []exchange.BookLevel {
	if levels == nil {
		return []exchange.BookLevel{}
	}
	return levels
}

type wireCandle struct {
	OpenTime  time.Time `json:"openTime"`
	CloseTime time.Time `json:"closeTime"`
	Open      string    `json:"open"`
	High      string    `json:"high"`
	Low       string    `json:"low"`
	Close     string    `json:"close"`
	Volume    string    `json:"volume"`
	Complete  bool      `json:"complete"`
}

func toWireCandle(candle exchange.Candle) wireCandle {
	return wireCandle{OpenTime: candle.OpenTime.UTC(), CloseTime: candle.CloseTime.UTC(), Open: candle.Open.String(), High: candle.High.String(), Low: candle.Low.String(), Close: candle.Close.String(), Volume: candle.Volume.String(), Complete: candle.Complete}
}
