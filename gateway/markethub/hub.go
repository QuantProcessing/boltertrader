// Package markethub owns Gateway's in-memory public market fanout. A native
// key is opened once and shared by every internal consumer; slow consumers are
// failed explicitly instead of being allowed to block the venue reader.
package markethub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
)

var (
	ErrUnavailable      = errors.New("market feed unavailable")
	ErrResyncRequired   = errors.New("market subscription requires resync")
	ErrConsumerLagged   = errors.New("market consumer lagged")
	ErrQuoteUnavailable = errors.New("QUOTE_UNAVAILABLE")
	ErrQuoteStale       = errors.New("QUOTE_STALE")
)

type OwnerKey struct {
	Venue        exchange.Venue       `json:"venue"`
	Environment  exchange.Environment `json:"environment"`
	NativeFamily string               `json:"nativeFamily"`
}

type SubscriptionKey struct {
	Owner        OwnerKey               `json:"owner"`
	Dataset      exchange.MarketDataset `json:"dataset"`
	InstrumentID string                 `json:"instrumentId,omitempty"`
	Interval     string                 `json:"interval,omitempty"`
	Depth        int                    `json:"depth,omitempty"`
}

func (key SubscriptionKey) Validate() error {
	if key.Owner.Venue == "" || key.Owner.Environment != exchange.EnvironmentLive || strings.TrimSpace(key.Owner.NativeFamily) == "" {
		return fmt.Errorf("market subscription owner is invalid")
	}
	if err := (exchange.MarketCapability{Venue: key.Owner.Venue, NativeMarketFamily: key.Owner.NativeFamily, Dataset: key.Dataset, Transport: exchange.TransportREST, Granularity: exchange.GranularityPollOnly}).Validate(); err != nil {
		return fmt.Errorf("market subscription dataset is invalid")
	}
	if key.Depth < 0 {
		return fmt.Errorf("market subscription depth is invalid")
	}
	return nil
}

type MessageKind string

const (
	MessageSnapshot       MessageKind = "snapshot"
	MessageDelta          MessageKind = "delta"
	MessageCheckpoint     MessageKind = "checkpoint"
	MessageResyncRequired MessageKind = "resync_required"
)

type Message struct {
	Kind           MessageKind     `json:"kind"`
	Key            SubscriptionKey `json:"key"`
	Generation     uint64          `json:"generation"`
	Sequence       uint64          `json:"sequence"`
	SourceSequence string          `json:"sourceSequence,omitempty"`
	ObservedAt     time.Time       `json:"observedAt"`
	Data           json.RawMessage `json:"data,omitempty"`
	Reason         string          `json:"reason,omitempty"`
}

// NativeStream represents one adapter-owned upstream topic or broad feed. Its
// first message must be a complete snapshot. A source that detects a gap emits
// MessageResyncRequired; Hub then increments generation and reopens it.
type NativeStream struct {
	Snapshot Message
	Events   <-chan Message
	Errors   <-chan error
	Close    func() error
}

type FeedOwner interface {
	Open(context.Context, SubscriptionKey) (NativeStream, error)
}

type Options struct {
	Owner            FeedOwner
	Observer         Observer
	IdleGrace        time.Duration
	ReconnectBackoff time.Duration
	Now              func() time.Time
}

// Observer exposes bounded owner-level health only. Instrument identifiers are
// intentionally absent so metrics cardinality cannot grow with the catalog.
type Observer interface {
	ObserveMarketFeed(venue, family, feed, state string, reconnects uint64)
	RecordMarketGap(venue, family, feed string, resnapshot bool)
	SetMarketFact(venue, family, dataset string, age time.Duration, coverage float64, hot int)
}

type feedState struct {
	key        SubscriptionKey
	ctx        context.Context
	cancel     context.CancelFunc
	ready      chan struct{}
	readyOnce  sync.Once
	startErr   error
	generation uint64
	sequence   uint64
	latest     Message
	subs       map[uint64]chan Message
	idle       *time.Timer
	close      func() error
	reconnects uint64
}

type Hub struct {
	owner            FeedOwner
	observer         Observer
	idleGrace        time.Duration
	reconnectBackoff time.Duration
	now              func() time.Time

	mu      sync.Mutex
	feeds   map[SubscriptionKey]*feedState
	nextSub uint64
	closed  bool
}

func New(options Options) (*Hub, error) {
	if options.Owner == nil {
		return nil, fmt.Errorf("market Hub owner is required")
	}
	if options.IdleGrace <= 0 {
		options.IdleGrace = 30 * time.Second
	}
	if options.ReconnectBackoff <= 0 {
		options.ReconnectBackoff = time.Second
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return &Hub{
		owner: options.Owner, observer: options.Observer, idleGrace: options.IdleGrace,
		reconnectBackoff: options.ReconnectBackoff, now: options.Now,
		feeds: make(map[SubscriptionKey]*feedState),
	}, nil
}

type Subscription struct {
	updates <-chan Message
	cancel  func()
	once    sync.Once
}

func (subscription *Subscription) Events() <-chan Message {
	if subscription == nil {
		return nil
	}
	return subscription.updates
}

func (subscription *Subscription) Close() {
	if subscription == nil {
		return
	}
	subscription.once.Do(subscription.cancel)
}

func (hub *Hub) Subscribe(ctx context.Context, key SubscriptionKey, buffer int) (*Subscription, error) {
	if ctx == nil || ctx.Err() != nil || key.Validate() != nil {
		return nil, ErrUnavailable
	}
	if buffer <= 0 {
		buffer = 1
	}
	hub.mu.Lock()
	if hub.closed {
		hub.mu.Unlock()
		return nil, ErrUnavailable
	}
	state := hub.feeds[key]
	if state == nil {
		feedCtx, cancel := context.WithCancel(context.Background())
		state = &feedState{key: key, ctx: feedCtx, cancel: cancel, ready: make(chan struct{}), generation: 1, subs: make(map[uint64]chan Message)}
		hub.feeds[key] = state
		go hub.run(state)
	}
	if state.idle != nil {
		state.idle.Stop()
		state.idle = nil
	}
	hub.nextSub++
	id := hub.nextSub
	updates := make(chan Message, buffer)
	ready := state.ready
	hub.mu.Unlock()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-ready:
	}

	hub.mu.Lock()
	if current := hub.feeds[key]; current != state || state.startErr != nil {
		err := state.startErr
		if err == nil {
			err = ErrUnavailable
		}
		hub.mu.Unlock()
		return nil, err
	}
	state.subs[id] = updates
	hub.observeFactLocked(state)
	latest := cloneMessage(state.latest)
	if latest.Kind != "" {
		select {
		case updates <- latest:
		default:
			hub.removeSubscriberLocked(key, state, id)
			hub.mu.Unlock()
			return nil, ErrConsumerLagged
		}
	}
	hub.mu.Unlock()

	subscription := &Subscription{updates: updates, cancel: func() { hub.unsubscribe(key, id) }}
	go func() {
		select {
		case <-ctx.Done():
			subscription.Close()
		case <-state.ctx.Done():
		}
	}()
	return subscription, nil
}

func (hub *Hub) run(state *feedState) {
	for state.ctx.Err() == nil {
		hub.observeFeed(state, "connecting")
		stream, err := hub.owner.Open(state.ctx, state.key)
		if err != nil {
			hub.failStart(state, err)
			return
		}
		if stream.Close == nil || stream.Snapshot.Kind != MessageSnapshot || stream.Snapshot.ObservedAt.IsZero() {
			if stream.Close != nil {
				_ = stream.Close()
			}
			hub.failStart(state, fmt.Errorf("%w: native stream has no valid snapshot", ErrUnavailable))
			return
		}
		hub.mu.Lock()
		if hub.feeds[state.key] != state || hub.closed {
			hub.mu.Unlock()
			_ = stream.Close()
			return
		}
		state.close = stream.Close
		hub.publishLocked(state, stream.Snapshot, MessageSnapshot)
		hub.observeFeed(state, "live")
		state.readyOnce.Do(func() { close(state.ready) })
		hub.mu.Unlock()

		needsReopen := hub.consume(state, stream)
		hub.mu.Lock()
		closeStream := state.close
		state.close = nil
		active := hub.feeds[state.key] == state && !hub.closed && state.ctx.Err() == nil
		if active && needsReopen {
			state.reconnects++
			if hub.observer != nil {
				hub.observer.RecordMarketGap(string(state.key.Owner.Venue), state.key.Owner.NativeFamily, string(state.key.Dataset), true)
			}
			state.generation++
			state.sequence = 0
			hub.publishLocked(state, Message{ObservedAt: hub.now().UTC(), Reason: "upstream-gap"}, MessageResyncRequired)
		}
		hub.mu.Unlock()
		if closeStream != nil {
			_ = closeStream()
		}
		if !active || !needsReopen || !waitContext(state.ctx, hub.reconnectBackoff) {
			return
		}
	}
}

func (hub *Hub) consume(state *feedState, stream NativeStream) bool {
	events, streamErrors := stream.Events, stream.Errors
	for events != nil || streamErrors != nil {
		select {
		case <-state.ctx.Done():
			return false
		case message, open := <-events:
			if !open {
				events = nil
				continue
			}
			// Native event payloads intentionally do not need to repeat the
			// subscription key: the owning feed state is the authority for it.
			// Populate it before sequence validation so depth gaps are checked
			// against the correct dataset rather than an empty event key.
			message.Key = state.key
			if message.Kind == MessageResyncRequired {
				return true
			}
			if message.ObservedAt.IsZero() {
				continue
			}
			hub.mu.Lock()
			if hub.feeds[state.key] == state {
				if sourceSequenceGap(state.latest, message) {
					hub.mu.Unlock()
					return true
				}
				hub.publishLocked(state, message, MessageDelta)
			}
			hub.mu.Unlock()
		case _, open := <-streamErrors:
			if !open {
				streamErrors = nil
				continue
			}
			return true
		}
	}
	return state.ctx.Err() == nil
}

func (hub *Hub) failStart(state *feedState, err error) {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if hub.feeds[state.key] != state {
		return
	}
	state.startErr = errors.Join(ErrUnavailable, err)
	hub.observeFeed(state, "unavailable")
	delete(hub.feeds, state.key)
	state.readyOnce.Do(func() { close(state.ready) })
}

func (hub *Hub) publishLocked(state *feedState, source Message, kind MessageKind) {
	state.sequence++
	message := Message{
		Kind: kind, Key: state.key, Generation: state.generation, Sequence: state.sequence,
		SourceSequence: source.SourceSequence, ObservedAt: source.ObservedAt.UTC(),
		Data: append(json.RawMessage(nil), source.Data...), Reason: source.Reason,
	}
	if kind == MessageSnapshot {
		state.latest = cloneMessage(message)
	} else if kind == MessageDelta {
		state.latest = materializeLatest(state.latest, message)
	}
	hub.observeFactLocked(state)
	for id, updates := range state.subs {
		select {
		case updates <- cloneMessage(message):
		default:
			// The subscriber receives an explicit terminal resync when its one
			// remaining buffer slot permits it; closing always makes the gap
			// observable to callers even when that slot is already occupied.
			resync := message
			resync.Kind, resync.Reason, resync.Data = MessageResyncRequired, "consumer-lag", nil
			select {
			case updates <- resync:
			default:
			}
			delete(state.subs, id)
			close(updates)
		}
	}
}

func (hub *Hub) observeFeed(state *feedState, status string) {
	if hub == nil || hub.observer == nil || state == nil {
		return
	}
	hub.observer.ObserveMarketFeed(string(state.key.Owner.Venue), state.key.Owner.NativeFamily, string(state.key.Dataset), status, state.reconnects)
}

func (hub *Hub) observeFactLocked(state *feedState) {
	if hub == nil || hub.observer == nil || state == nil || state.latest.ObservedAt.IsZero() {
		return
	}
	age := hub.now().UTC().Sub(state.latest.ObservedAt.UTC())
	if age < 0 {
		age = 0
	}
	hub.observer.SetMarketFact(string(state.key.Owner.Venue), state.key.Owner.NativeFamily, string(state.key.Dataset), age, 1, len(state.subs))
}

// materializeLatest keeps the first message for every future consumer a full
// snapshot. Native streams continue to deliver deltas to existing consumers;
// only the retained in-memory state is folded here.
func materializeLatest(current, delta Message) Message {
	latest := cloneMessage(delta)
	latest.Kind = MessageSnapshot
	switch delta.Key.Dataset {
	case exchange.DatasetDepth:
		var snapshot, update wireBook
		if json.Unmarshal(current.Data, &snapshot) != nil || json.Unmarshal(delta.Data, &update) != nil {
			return current
		}
		if strings.EqualFold(update.Kind, string(exchange.EventSnapshot)) {
			snapshot.Bids, snapshot.Asks = update.Bids, update.Asks
		} else {
			snapshot.Bids = applyBookLevels(snapshot.Bids, update.Bids, true, delta.Key.Depth)
			snapshot.Asks = applyBookLevels(snapshot.Asks, update.Asks, false, delta.Key.Depth)
		}
		latest.Data = Encode(wireBook{Bids: nonNilLevels(snapshot.Bids), Asks: nonNilLevels(snapshot.Asks)})
	case exchange.DatasetCandle:
		var snapshot wireCandles
		var update wireCandleDelta
		if json.Unmarshal(current.Data, &snapshot) != nil || json.Unmarshal(delta.Data, &update) != nil || len(update.Candle) == 0 {
			return current
		}
		if update.Interval != "" {
			snapshot.Interval = update.Interval
		}
		var key struct {
			OpenTime time.Time `json:"openTime"`
		}
		if json.Unmarshal(update.Candle, &key) != nil || key.OpenTime.IsZero() {
			return current
		}
		replaced := false
		for index, candle := range snapshot.Candles {
			var existing struct {
				OpenTime time.Time `json:"openTime"`
			}
			if json.Unmarshal(candle, &existing) == nil && existing.OpenTime.Equal(key.OpenTime) {
				snapshot.Candles[index], replaced = append(json.RawMessage(nil), update.Candle...), true
				break
			}
		}
		if !replaced {
			snapshot.Candles = append(snapshot.Candles, append(json.RawMessage(nil), update.Candle...))
		}
		latest.Data = Encode(snapshot)
	}
	return latest
}

type wireBook struct {
	Kind     string               `json:"kind,omitempty"`
	Previous string               `json:"previous,omitempty"`
	Bids     []exchange.BookLevel `json:"bids"`
	Asks     []exchange.BookLevel `json:"asks"`
}

func sourceSequenceGap(current, incoming Message) bool {
	if incoming.Key.Dataset != exchange.DatasetDepth || current.SourceSequence == "" || incoming.SourceSequence == "" {
		return false
	}
	var update wireBook
	if json.Unmarshal(incoming.Data, &update) != nil || update.Previous == "" {
		return false
	}
	return update.Previous != current.SourceSequence
}

type wireCandles struct {
	Interval string            `json:"interval"`
	Candles  []json.RawMessage `json:"candles"`
}

type wireCandleDelta struct {
	Interval string          `json:"interval"`
	Candle   json.RawMessage `json:"candle"`
}

func applyBookLevels(current, updates []exchange.BookLevel, bids bool, limit int) []exchange.BookLevel {
	levels := make(map[string]exchange.BookLevel, len(current)+len(updates))
	for _, level := range current {
		levels[level.Price.String()] = level
	}
	for _, level := range updates {
		if level.Quantity.IsZero() {
			delete(levels, level.Price.String())
		} else {
			levels[level.Price.String()] = level
		}
	}
	result := make([]exchange.BookLevel, 0, len(levels))
	for _, level := range levels {
		result = append(result, level)
	}
	sort.Slice(result, func(i, j int) bool {
		if bids {
			return result[i].Price.GreaterThan(result[j].Price)
		}
		return result[i].Price.LessThan(result[j].Price)
	})
	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}
	return result
}

func nonNilLevels(levels []exchange.BookLevel) []exchange.BookLevel {
	if levels == nil {
		return []exchange.BookLevel{}
	}
	return levels
}

func (hub *Hub) Latest(key SubscriptionKey) (Message, bool) {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	state := hub.feeds[key]
	if state == nil || state.latest.Kind == "" {
		return Message{}, false
	}
	return cloneMessage(state.latest), true
}

func (hub *Hub) unsubscribe(key SubscriptionKey, id uint64) {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	state := hub.feeds[key]
	if state == nil {
		return
	}
	hub.removeSubscriberLocked(key, state, id)
}

func (hub *Hub) removeSubscriberLocked(key SubscriptionKey, state *feedState, id uint64) {
	updates, exists := state.subs[id]
	if exists {
		delete(state.subs, id)
		close(updates)
	}
	if len(state.subs) != 0 || state.idle != nil {
		hub.observeFactLocked(state)
		return
	}
	hub.observeFactLocked(state)
	state.idle = time.AfterFunc(hub.idleGrace, func() {
		hub.mu.Lock()
		if hub.feeds[key] != state || len(state.subs) != 0 {
			hub.mu.Unlock()
			return
		}
		delete(hub.feeds, key)
		state.cancel()
		closeStream := state.close
		state.close = nil
		hub.mu.Unlock()
		if closeStream != nil {
			_ = closeStream()
		}
	})
}

func (hub *Hub) Close() error {
	if hub == nil {
		return nil
	}
	hub.mu.Lock()
	if hub.closed {
		hub.mu.Unlock()
		return nil
	}
	hub.closed = true
	closers := make([]func() error, 0, len(hub.feeds))
	for _, state := range hub.feeds {
		state.cancel()
		if state.close != nil {
			closers = append(closers, state.close)
			state.close = nil
		}
		for id, updates := range state.subs {
			delete(state.subs, id)
			close(updates)
		}
	}
	hub.feeds = make(map[SubscriptionKey]*feedState)
	hub.mu.Unlock()
	var result error
	for _, closeStream := range closers {
		result = errors.Join(result, closeStream())
	}
	return result
}

func cloneMessage(message Message) Message {
	message.Data = append(json.RawMessage(nil), message.Data...)
	return message
}

// Encode is shared with Feed Owner adapters so every payload enters the Hub as
// immutable JSON before fanout.
func Encode(value any) json.RawMessage {
	payload, _ := json.Marshal(value)
	return payload
}

func waitContext(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
