package realtime

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

var ErrResnapshotRequired = errors.New("realtime resnapshot required")

type Event struct {
	EventID    string          `json:"eventId"`
	Sequence   uint64          `json:"sequence"`
	Cursor     string          `json:"cursor"`
	Kind       string          `json:"kind"`
	ObservedAt time.Time       `json:"observedAt"`
	Payload    json.RawMessage `json:"payload"`
	principal  string
}

type watcher struct {
	updates   chan Event
	principal string
	scoped    bool
}

type Hub struct {
	capacity int
	now      func() time.Time
	mu       sync.RWMutex
	next     uint64
	events   []Event
	watchers map[uint64]watcher
	watcher  uint64
}

func NewHub(capacity int, now func() time.Time) (*Hub, error) {
	if capacity <= 0 || now == nil {
		return nil, fmt.Errorf("realtime hub requires positive capacity and clock")
	}
	return &Hub{capacity: capacity, now: now, watchers: make(map[uint64]watcher)}, nil
}

func (hub *Hub) Publish(kind string, payload any) (Event, error) {
	return hub.publish("", kind, payload)
}

func (hub *Hub) PublishScoped(principal, kind string, payload any) (Event, error) {
	if strings.TrimSpace(principal) == "" {
		return Event{}, fmt.Errorf("realtime event scope is required")
	}
	return hub.publish(principal, kind, payload)
}

// Cursor returns the current global replay boundary. A materialized projection
// calls this while holding its own read lock; if that projection applies state
// before publishing, snapshot consumers cannot skip a matching realtime event.
func (hub *Hub) Cursor() string {
	if hub == nil {
		return ""
	}
	hub.mu.RLock()
	defer hub.mu.RUnlock()
	return encodeCursor(hub.next)
}

func (hub *Hub) publish(principal, kind string, payload any) (Event, error) {
	if strings.TrimSpace(kind) == "" {
		return Event{}, fmt.Errorf("realtime event kind is required")
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return Event{}, fmt.Errorf("encode realtime payload: %w", err)
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	hub.next++
	event := Event{
		EventID: fmt.Sprintf("event-%d", hub.next), Sequence: hub.next, Cursor: encodeCursor(hub.next),
		Kind: kind, ObservedAt: hub.now().UTC(), Payload: encoded, principal: principal,
	}
	hub.events = append(hub.events, event)
	if len(hub.events) > hub.capacity {
		hub.events = append([]Event(nil), hub.events[len(hub.events)-hub.capacity:]...)
	}
	for id, watcher := range hub.watchers {
		if watcher.scoped && !visibleTo(event, watcher.principal) {
			continue
		}
		select {
		case watcher.updates <- event:
		default:
			// A silent drop destroys cursor continuity. Closing the subscriber
			// makes EventSource reconnect with its last delivered event id; replay
			// either fills the gap or explicitly requires a fresh snapshot.
			delete(hub.watchers, id)
			close(watcher.updates)
		}
	}
	return cloneEvent(event), nil
}

func (hub *Hub) Replay(cursor string) ([]Event, error) {
	return hub.replay(cursor, "", false)
}

func (hub *Hub) ReplayScoped(cursor, principal string) ([]Event, error) {
	if strings.TrimSpace(principal) == "" {
		return nil, fmt.Errorf("realtime replay scope is required")
	}
	return hub.replay(cursor, principal, true)
}

// ReplayAndSubscribeScoped establishes the replay boundary and the live
// subscription under one Hub lock. An event can therefore be in the replay or
// in the live channel, but can never fall into the old replay/subscribe gap.
func (hub *Hub) ReplayAndSubscribeScoped(cursor string, buffer int, principal string) ([]Event, <-chan Event, func(), error) {
	if strings.TrimSpace(principal) == "" {
		return nil, nil, nil, fmt.Errorf("realtime replay scope is required")
	}
	if buffer <= 0 {
		buffer = 1
	}
	hub.mu.Lock()
	events, err := hub.replayLocked(cursor, principal, true)
	if err != nil {
		hub.mu.Unlock()
		return nil, nil, nil, err
	}
	hub.watcher++
	id := hub.watcher
	updates := make(chan Event, buffer)
	hub.watchers[id] = watcher{updates: updates, principal: principal, scoped: true}
	hub.mu.Unlock()
	return events, updates, hub.unsubscribe(id), nil
}

func (hub *Hub) replay(cursor, principal string, scoped bool) ([]Event, error) {
	hub.mu.RLock()
	defer hub.mu.RUnlock()
	return hub.replayLocked(cursor, principal, scoped)
}

func (hub *Hub) replayLocked(cursor, principal string, scoped bool) ([]Event, error) {
	sequence, err := decodeCursor(cursor)
	if err != nil {
		return nil, ErrResnapshotRequired
	}
	if len(hub.events) == 0 {
		if sequence == 0 {
			return nil, nil
		}
		return nil, ErrResnapshotRequired
	}
	oldest := hub.events[0].Sequence
	if sequence != 0 && sequence < oldest {
		return nil, ErrResnapshotRequired
	}
	if sequence > hub.next {
		return nil, ErrResnapshotRequired
	}
	events := make([]Event, 0, len(hub.events))
	for _, event := range hub.events {
		if event.Sequence > sequence && (!scoped || visibleTo(event, principal)) {
			events = append(events, cloneEvent(event))
		}
	}
	return events, nil
}

func (hub *Hub) Subscribe(buffer int) (<-chan Event, func()) {
	return hub.subscribe(buffer, "", false)
}

func (hub *Hub) SubscribeScoped(buffer int, principal string) (<-chan Event, func()) {
	if strings.TrimSpace(principal) == "" {
		updates := make(chan Event)
		close(updates)
		return updates, func() {}
	}
	return hub.subscribe(buffer, principal, true)
}

func (hub *Hub) subscribe(buffer int, principal string, scoped bool) (<-chan Event, func()) {
	if buffer <= 0 {
		buffer = 1
	}
	hub.mu.Lock()
	hub.watcher++
	id := hub.watcher
	updates := make(chan Event, buffer)
	hub.watchers[id] = watcher{updates: updates, principal: principal, scoped: scoped}
	hub.mu.Unlock()
	return updates, hub.unsubscribe(id)
}

func (hub *Hub) unsubscribe(id uint64) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			hub.mu.Lock()
			if watcher, exists := hub.watchers[id]; exists {
				delete(hub.watchers, id)
				close(watcher.updates)
			}
			hub.mu.Unlock()
		})
	}
}

func visibleTo(event Event, principal string) bool {
	return event.principal == "" || event.principal == principal
}

func encodeCursor(sequence uint64) string {
	buffer := make([]byte, 8)
	binary.BigEndian.PutUint64(buffer, sequence)
	return base64.RawURLEncoding.EncodeToString(buffer)
}

func decodeCursor(cursor string) (uint64, error) {
	if cursor == "" {
		return 0, nil
	}
	buffer, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || len(buffer) != 8 {
		return 0, ErrResnapshotRequired
	}
	return binary.BigEndian.Uint64(buffer), nil
}

func cloneEvent(event Event) Event {
	event.Payload = append(json.RawMessage(nil), event.Payload...)
	return event
}
