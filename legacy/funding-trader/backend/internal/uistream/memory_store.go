package uistream

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"time"
)

type MemoryEventStore struct {
	mu        sync.Mutex
	next      map[string]uint64
	min       map[string]uint64
	events    map[string][]StoredEvent
	now       func() time.Time
	replayErr error
}

func NewMemoryEventStore() *MemoryEventStore {
	return newMemoryEventStore()
}

func newMemoryEventStore() *MemoryEventStore {
	return &MemoryEventStore{
		next:   make(map[string]uint64),
		min:    make(map[string]uint64),
		events: make(map[string][]StoredEvent),
		now:    func() time.Time { return time.Now().UTC() },
	}
}

func (store *MemoryEventStore) Latest(_ context.Context, scope string) (uint64, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.next[scope], nil
}

func (store *MemoryEventStore) Replay(_ context.Context, scope string, after uint64, kinds []string, limit int) ([]StoredEvent, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.replayErr != nil {
		return nil, store.replayErr
	}
	if min := store.min[scope]; min > 0 && after+1 < min {
		return nil, ErrResyncRequired
	}
	if limit <= 0 {
		limit = len(store.events[scope])
	}
	filter := kindSet(normalizeKinds(kinds))
	replayed := make([]StoredEvent, 0)
	for _, event := range store.events[scope] {
		if event.Sequence <= after {
			continue
		}
		if len(filter) != 0 {
			if _, ok := filter[event.Kind]; !ok {
				continue
			}
		}
		replayed = append(replayed, cloneStored(event))
		if len(replayed) == limit {
			break
		}
	}
	return replayed, nil
}

func (store *MemoryEventStore) Append(_ context.Context, input EventInput) (StoredEvent, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.next[input.Scope]++
	event := StoredEvent{
		Sequence:        store.next[input.Scope],
		Scope:           input.Scope,
		PrincipalID:     input.PrincipalID,
		Mode:            input.Mode,
		ContractVersion: input.ContractVersion,
		Kind:            input.Kind,
		Data:            cloneRaw(input.Data),
		OccurredAt:      store.now(),
	}
	store.events[input.Scope] = append(store.events[input.Scope], event)
	return cloneStored(event), nil
}

func (store *MemoryEventStore) dropBefore(scope string, sequence uint64) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.min[scope] = sequence
	events := store.events[scope]
	index := len(events)
	for i, event := range events {
		if event.Sequence >= sequence {
			index = i
			break
		}
	}
	store.events[scope] = slices.Clone(events[index:])
}

func cloneStored(event StoredEvent) StoredEvent {
	event.Data = json.RawMessage(cloneRaw(event.Data))
	return event
}
