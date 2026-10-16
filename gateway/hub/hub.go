// Package hub fans one upstream feed out to many WebSocket clients.
//
// A topic is opened on its first subscriber and closed a linger period after
// its last one leaves, so page reloads do not churn venue subscriptions.
// Retained messages (latest value per channel) are replayed to late joiners;
// everything else is fire-and-forget. A sink that cannot keep up loses
// messages instead of blocking the feed: every retained channel carries a
// complete state, so the next message repairs it.
package hub

import (
	"context"
	"encoding/json"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"
)

// Key identifies one topic.
type Key struct {
	Channel    string `json:"ch"`
	Instrument string `json:"inst,omitempty"`
	Interval   string `json:"interval,omitempty"`
	Account    string `json:"account,omitempty"`
}

// Message is the wire envelope pushed to clients.
type Message struct {
	Channel    string `json:"ch"`
	Instrument string `json:"inst,omitempty"`
	Interval   string `json:"interval,omitempty"`
	Account    string `json:"account,omitempty"`
	Data       any    `json:"data"`
}

// Sink receives encoded messages. Send must not block.
type Sink interface {
	Send(payload []byte) bool
}

// Emit publishes one message; retain keeps it as the replay value of its
// channel for later subscribers.
type Emit func(message Message, retain bool)

// Source feeds a topic. It blocks until ctx ends (return nil) or the upstream
// fails (return error, the hub retries with backoff).
type Source func(ctx context.Context, key Key, emit Emit) error

type topic struct {
	sinks    map[Sink]struct{}
	retained map[string][]byte
	order    []string
	cancel   context.CancelFunc
	linger   *time.Timer
}

type Hub struct {
	source Source
	linger time.Duration

	mu     sync.Mutex
	topics map[Key]*topic
	closed bool
}

func New(source Source, linger time.Duration) *Hub {
	return &Hub{source: source, linger: linger, topics: make(map[Key]*topic)}
}

// Subscribe attaches sink to key, starting the upstream if needed, and
// replays retained messages to it.
func (hub *Hub) Subscribe(key Key, sink Sink) {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if hub.closed {
		return
	}
	current := hub.topics[key]
	if current == nil {
		ctx, cancel := context.WithCancel(context.Background())
		current = &topic{sinks: make(map[Sink]struct{}), retained: make(map[string][]byte), cancel: cancel}
		hub.topics[key] = current
		go hub.run(ctx, key, current)
	}
	if current.linger != nil {
		current.linger.Stop()
		current.linger = nil
	}
	current.sinks[sink] = struct{}{}
	for _, channel := range current.order {
		sink.Send(current.retained[channel])
	}
}

// Unsubscribe detaches sink; the topic closes after the linger period.
func (hub *Hub) Unsubscribe(key Key, sink Sink) {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	current := hub.topics[key]
	if current == nil {
		return
	}
	delete(current.sinks, sink)
	if len(current.sinks) > 0 || current.linger != nil {
		return
	}
	current.linger = time.AfterFunc(hub.linger, func() {
		hub.mu.Lock()
		defer hub.mu.Unlock()
		if hub.topics[key] == current && len(current.sinks) == 0 {
			current.cancel()
			delete(hub.topics, key)
		}
	})
}

// Close stops every topic.
func (hub *Hub) Close() {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	hub.closed = true
	for key, current := range hub.topics {
		if current.linger != nil {
			current.linger.Stop()
		}
		current.cancel()
		delete(hub.topics, key)
	}
}

// Topics reports the number of open topics.
func (hub *Hub) Topics() int {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	return len(hub.topics)
}

func (hub *Hub) run(ctx context.Context, key Key, current *topic) {
	emit := func(message Message, retain bool) {
		payload, err := json.Marshal(message)
		if err != nil {
			slog.Error("hub encode failed", "channel", message.Channel, "error", err)
			return
		}
		hub.mu.Lock()
		defer hub.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		if retain {
			if _, seen := current.retained[message.Channel]; !seen {
				current.order = append(current.order, message.Channel)
			}
			current.retained[message.Channel] = payload
		}
		for sink := range current.sinks {
			sink.Send(payload)
		}
	}
	backoff := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		err := hub.source(ctx, key, emit)
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
		slog.Warn("upstream feed ended; retrying", "channel", key.Channel, "instrument", key.Instrument, "account", key.Account, "error", err, "backoff", backoff)
		emit(Message{Channel: key.Channel + ".status", Instrument: key.Instrument, Interval: key.Interval, Account: key.Account, Data: map[string]any{"connected": false}}, false)
		jitter := time.Duration(rand.Int64N(int64(backoff/4) + 1))
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff + jitter):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}
