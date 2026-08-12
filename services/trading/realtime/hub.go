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
	mode       string
}

type watcher struct {
	updates   chan Event
	principal string
	mode      string
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
	return hub.publish("", "", kind, payload)
}

func (hub *Hub) PublishScoped(principal, mode, kind string, payload any) (Event, error) {
	if strings.TrimSpace(principal) == "" || (mode != "test" && mode != "live") {
		return Event{}, fmt.Errorf("realtime event scope is required")
	}
	return hub.publish(principal, mode, kind, payload)
}

func (hub *Hub) publish(principal, mode, kind string, payload any) (Event, error) {
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
		Kind: kind, ObservedAt: hub.now().UTC(), Payload: encoded, principal: principal, mode: mode,
	}
	hub.events = append(hub.events, event)
	if len(hub.events) > hub.capacity {
		hub.events = append([]Event(nil), hub.events[len(hub.events)-hub.capacity:]...)
	}
	for _, watcher := range hub.watchers {
		if watcher.scoped && !visibleTo(event, watcher.principal, watcher.mode) {
			continue
		}
		select {
		case watcher.updates <- event:
		default:
		}
	}
	return cloneEvent(event), nil
}

func (hub *Hub) Replay(cursor string) ([]Event, error) {
	return hub.replay(cursor, "", "", false)
}

func (hub *Hub) ReplayScoped(cursor, principal, mode string) ([]Event, error) {
	if strings.TrimSpace(principal) == "" || (mode != "test" && mode != "live") {
		return nil, fmt.Errorf("realtime replay scope is required")
	}
	return hub.replay(cursor, principal, mode, true)
}

func (hub *Hub) replay(cursor, principal, mode string, scoped bool) ([]Event, error) {
	hub.mu.RLock()
	defer hub.mu.RUnlock()
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
		if event.Sequence > sequence && (!scoped || visibleTo(event, principal, mode)) {
			events = append(events, cloneEvent(event))
		}
	}
	return events, nil
}

func (hub *Hub) Subscribe(buffer int) (<-chan Event, func()) {
	return hub.subscribe(buffer, "", "", false)
}

func (hub *Hub) SubscribeScoped(buffer int, principal, mode string) (<-chan Event, func()) {
	if strings.TrimSpace(principal) == "" || (mode != "test" && mode != "live") {
		updates := make(chan Event)
		close(updates)
		return updates, func() {}
	}
	return hub.subscribe(buffer, principal, mode, true)
}

func (hub *Hub) subscribe(buffer int, principal, mode string, scoped bool) (<-chan Event, func()) {
	if buffer <= 0 {
		buffer = 1
	}
	hub.mu.Lock()
	hub.watcher++
	id := hub.watcher
	updates := make(chan Event, buffer)
	hub.watchers[id] = watcher{updates: updates, principal: principal, mode: mode, scoped: scoped}
	hub.mu.Unlock()
	var once sync.Once
	return updates, func() {
		once.Do(func() {
			hub.mu.Lock()
			watcher := hub.watchers[id]
			delete(hub.watchers, id)
			close(watcher.updates)
			hub.mu.Unlock()
		})
	}
}

func visibleTo(event Event, principal, mode string) bool {
	return (event.principal == "" && event.mode == "") || (event.principal == principal && event.mode == mode)
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
