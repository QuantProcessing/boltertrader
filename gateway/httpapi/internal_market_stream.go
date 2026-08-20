package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/publicmarket"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

const internalPublicBookLimit = 100

// InternalPublicMarketStreams is deliberately narrower than publicmarket.Source
// so the HTTP boundary can only open read-only public subscriptions.
type InternalPublicMarketStreams interface {
	WatchOrderBook(context.Context, string, int) (publicmarket.OrderBookStream, error)
	WatchCandles(context.Context, string, string) (publicmarket.CandleStream, error)
}

type internalMarketStreamEvent struct {
	Kind         string               `json:"kind"`
	EventType    string               `json:"eventType"`
	InstrumentID string               `json:"instrumentId"`
	ObservedAt   time.Time            `json:"observedAt"`
	Sequence     string               `json:"sequence,omitempty"`
	Previous     string               `json:"previous,omitempty"`
	Bids         []exchange.BookLevel `json:"bids,omitempty"`
	Asks         []exchange.BookLevel `json:"asks,omitempty"`
	Candle       *internalCandle      `json:"candle,omitempty"`
	Reason       string               `json:"reason,omitempty"`
}

type internalCandle struct {
	OpenTime  time.Time `json:"openTime"`
	CloseTime time.Time `json:"closeTime"`
	Open      string    `json:"open"`
	High      string    `json:"high"`
	Low       string    `json:"low"`
	Close     string    `json:"close"`
	Volume    string    `json:"volume"`
	Complete  bool      `json:"complete"`
}

func (handler *InternalHandler) publicMarketStream(w http.ResponseWriter, r *http.Request) {
	if handler.publicMarkets == nil {
		handler.writeError(w, r, http.StatusServiceUnavailable, "PUBLIC_STREAM_UNAVAILABLE", true)
		return
	}
	values := r.URL.Query()
	for name, entries := range values {
		if (name != "instrumentId" && name != "kind" && name != "interval") || len(entries) != 1 || strings.TrimSpace(entries[0]) == "" {
			handler.writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", false)
			return
		}
	}
	instrumentID := strings.TrimSpace(values.Get("instrumentId"))
	kind := strings.TrimSpace(values.Get("kind"))
	interval := strings.TrimSpace(values.Get("interval"))
	if instrumentID == "" || (kind != "order_book" && kind != "candles") ||
		(kind == "order_book" && interval != "") || (kind == "candles" && !portableStreamInterval(interval)) {
		handler.writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", false)
		return
	}

	switch kind {
	case "order_book":
		stream, err := handler.publicMarkets.WatchOrderBook(r.Context(), instrumentID, internalPublicBookLimit)
		if err != nil {
			slog.Warn("public order-book stream unavailable", "instrument_id", instrumentID, "cause", err)
			handler.writeError(w, r, http.StatusServiceUnavailable, "PUBLIC_STREAM_UNAVAILABLE", true)
			return
		}
		handler.serveOrderBookStream(w, r, instrumentID, stream)
	case "candles":
		stream, err := handler.publicMarkets.WatchCandles(r.Context(), instrumentID, interval)
		if err != nil {
			slog.Warn("public candle stream unavailable", "instrument_id", instrumentID, "interval", interval, "cause", err)
			handler.writeError(w, r, http.StatusServiceUnavailable, "PUBLIC_STREAM_UNAVAILABLE", true)
			return
		}
		handler.serveCandleStream(w, r, instrumentID, stream)
	}
}

func (handler *InternalHandler) serveOrderBookStream(w http.ResponseWriter, r *http.Request, instrumentID string, stream publicmarket.OrderBookStream) {
	defer stream.Subscription.Close()
	connection, ctx, ok := handler.acceptInternalMarketSocket(w, r)
	if !ok {
		return
	}
	defer connection.CloseNow()

	if !sameStreamInstrument(stream.Instrument, stream.Snapshot.Instrument) {
		return
	}
	if err := writeInternalMarketEvent(ctx, connection, internalMarketStreamEvent{
		Kind: "order_book", EventType: "snapshot", InstrumentID: instrumentID,
		ObservedAt: observedTime(stream.Snapshot.Time), Sequence: stream.Snapshot.Sequence,
		Bids: nonNil(stream.Snapshot.Bids), Asks: nonNil(stream.Snapshot.Asks),
	}); err != nil {
		return
	}

	events, statuses, streamErrors := stream.Subscription.Events(), stream.Subscription.Status(), stream.Subscription.Errors()
	fence := newOrderBookSequenceFence(stream.Snapshot.Sequence)
	for events != nil || statuses != nil || streamErrors != nil {
		select {
		case <-ctx.Done():
			return
		case event, open := <-events:
			if !open {
				events = nil
				continue
			}
			if event.Resync || !sameStreamInstrument(stream.Instrument, event.Instrument) {
				writeInternalResync(ctx, connection, instrumentID, "order_book", "stream-gap")
				return
			}
			emit, gap := fence.Accept(event)
			if gap {
				writeInternalResync(ctx, connection, instrumentID, "order_book", "stream-gap")
				return
			}
			if !emit {
				continue
			}
			if err := writeInternalMarketEvent(ctx, connection, internalMarketStreamEvent{
				Kind: "order_book", EventType: string(event.Kind), InstrumentID: instrumentID,
				ObservedAt: observedTime(event.Time), Sequence: event.Sequence, Previous: event.Previous,
				Bids: nonNil(event.Bids), Asks: nonNil(event.Asks),
			}); err != nil {
				return
			}
		case status, open := <-statuses:
			if !open {
				statuses = nil
				continue
			}
			if streamStatusRequiresResync(status) {
				writeInternalResync(ctx, connection, instrumentID, "order_book", "stream-gap")
				return
			}
		case _, open := <-streamErrors:
			if !open {
				streamErrors = nil
				continue
			}
			writeInternalResync(ctx, connection, instrumentID, "order_book", "stream-error")
			return
		}
	}
}

// orderBookSequenceFence joins the REST snapshot to the subscription opened
// immediately before it. Updates older than the snapshot are discarded; once
// the first live delta bridges that snapshot, every advertised predecessor
// must match the last emitted sequence.
type orderBookSequenceFence struct {
	last    uint64
	numeric bool
	started bool
}

func newOrderBookSequenceFence(snapshot string) *orderBookSequenceFence {
	sequence, err := strconv.ParseUint(strings.TrimSpace(snapshot), 10, 64)
	return &orderBookSequenceFence{last: sequence, numeric: err == nil}
}

func (fence *orderBookSequenceFence) Accept(event exchange.BookEvent) (emit bool, gap bool) {
	if fence == nil || !fence.numeric {
		return true, false
	}
	sequence, err := strconv.ParseUint(strings.TrimSpace(event.Sequence), 10, 64)
	if err != nil {
		// Some venues use opaque sequence tokens. Do not invent ordering for
		// them; their adapter lifecycle remains the source of gap detection.
		fence.numeric = false
		return true, false
	}
	if sequence <= fence.last {
		return false, false
	}
	if event.Kind == exchange.EventSnapshot {
		fence.last = sequence
		fence.started = true
		return true, false
	}
	if previous := strings.TrimSpace(event.Previous); previous != "" {
		value, err := strconv.ParseUint(previous, 10, 64)
		if err == nil && ((!fence.started && value > fence.last) || (fence.started && value != fence.last)) {
			return false, true
		}
	}
	fence.last = sequence
	fence.started = true
	return true, false
}

func (handler *InternalHandler) serveCandleStream(w http.ResponseWriter, r *http.Request, instrumentID string, stream publicmarket.CandleStream) {
	defer stream.Subscription.Close()
	connection, ctx, ok := handler.acceptInternalMarketSocket(w, r)
	if !ok {
		return
	}
	defer connection.CloseNow()

	events, statuses, streamErrors := stream.Subscription.Events(), stream.Subscription.Status(), stream.Subscription.Errors()
	for events != nil || statuses != nil || streamErrors != nil {
		select {
		case <-ctx.Done():
			return
		case event, open := <-events:
			if !open {
				events = nil
				continue
			}
			if !sameStreamInstrument(stream.Instrument, event.Instrument) {
				writeInternalResync(ctx, connection, instrumentID, "candles", "instrument-mismatch")
				return
			}
			candle := internalCandle{
				OpenTime: event.Candle.OpenTime.UTC(), CloseTime: event.Candle.CloseTime.UTC(),
				Open: event.Candle.Open.String(), High: event.Candle.High.String(), Low: event.Candle.Low.String(),
				Close: event.Candle.Close.String(), Volume: event.Candle.Volume.String(), Complete: event.Candle.Complete,
			}
			if err := writeInternalMarketEvent(ctx, connection, internalMarketStreamEvent{
				Kind: "candles", EventType: "update", InstrumentID: instrumentID,
				ObservedAt: observedTime(event.Candle.CloseTime), Candle: &candle,
			}); err != nil {
				return
			}
		case status, open := <-statuses:
			if !open {
				statuses = nil
				continue
			}
			if streamStatusRequiresResync(status) {
				writeInternalResync(ctx, connection, instrumentID, "candles", "stream-gap")
				return
			}
		case _, open := <-streamErrors:
			if !open {
				streamErrors = nil
				continue
			}
			writeInternalResync(ctx, connection, instrumentID, "candles", "stream-error")
			return
		}
	}
}

func (handler *InternalHandler) acceptInternalMarketSocket(w http.ResponseWriter, r *http.Request) (*websocket.Conn, context.Context, bool) {
	connection, err := websocket.Accept(w, r, nil)
	if err != nil {
		return nil, nil, false
	}
	connection.SetReadLimit(handler.bodyLimit)
	return connection, connection.CloseRead(r.Context()), true
}

func writeInternalMarketEvent(ctx context.Context, connection *websocket.Conn, event internalMarketStreamEvent) error {
	writeCtx, cancel := context.WithTimeout(ctx, publicMarketWriteTimeout)
	defer cancel()
	return wsjson.Write(writeCtx, connection, event)
}

func writeInternalResync(ctx context.Context, connection *websocket.Conn, instrumentID, kind, reason string) {
	_ = writeInternalMarketEvent(ctx, connection, internalMarketStreamEvent{
		Kind: kind, EventType: "resync_required", InstrumentID: instrumentID,
		ObservedAt: time.Now().UTC(), Reason: reason,
	})
	_ = connection.Close(websocket.StatusPolicyViolation, "public_stream_resnapshot_required")
}

func streamStatusRequiresResync(status exchange.StreamStatusEvent) bool {
	return status.State == exchange.SubscriptionGap || status.State == exchange.SubscriptionResyncing || status.Phase == exchange.GapStarted
}

func sameStreamInstrument(expected, actual exchange.Instrument) bool {
	return expected.Identity == actual.Identity
}

func portableStreamInterval(interval string) bool {
	switch interval {
	case "1m", "5m", "15m", "30m", "1h", "4h", "12h", "1d":
		return true
	default:
		return false
	}
}
