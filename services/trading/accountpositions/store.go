package accountpositions

import (
	"context"
	"fmt"
	"sync"
)

type Store interface {
	LoadPrincipal(context.Context, string) ([]AccountState, []Item, error)
	ReplaceAccount(context.Context, string, AccountState, []Item) error
	Apply(context.Context, string, AccountState, Operation, Item) error
	RemoveAccount(context.Context, string, string) error
}

type MemoryStore struct {
	mu     sync.RWMutex
	states map[string]map[string]AccountState
	items  map[string]map[string]Item
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{states: make(map[string]map[string]AccountState), items: make(map[string]map[string]Item)}
}

func (store *MemoryStore) LoadPrincipal(_ context.Context, principal string) ([]AccountState, []Item, error) {
	if principal == "" {
		return nil, nil, fmt.Errorf("account position principal is required")
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	states := make([]AccountState, 0, len(store.states[principal]))
	for _, state := range store.states[principal] {
		states = append(states, cloneState(state))
	}
	items := make([]Item, 0, len(store.items[principal]))
	for _, item := range store.items[principal] {
		items = append(items, item)
	}
	return states, items, nil
}

func (store *MemoryStore) ReplaceAccount(_ context.Context, principal string, state AccountState, items []Item) error {
	if principal == "" || state.AccountID == "" {
		return fmt.Errorf("account position scope is required")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.ensure(principal)
	store.states[principal][state.AccountID] = cloneState(state)
	for key, item := range store.items[principal] {
		if item.AccountID == state.AccountID {
			delete(store.items[principal], key)
		}
	}
	for _, item := range items {
		store.items[principal][item.Key.String()] = item
	}
	return nil
}

func (store *MemoryStore) Apply(_ context.Context, principal string, state AccountState, operation Operation, item Item) error {
	if principal == "" || state.AccountID == "" || !item.Key.Valid() {
		return fmt.Errorf("account position mutation is invalid")
	}
	if operation != OperationUpsert && operation != OperationRemove {
		return fmt.Errorf("account position operation is invalid")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.ensure(principal)
	store.states[principal][state.AccountID] = cloneState(state)
	switch operation {
	case OperationUpsert:
		store.items[principal][item.Key.String()] = item
	case OperationRemove:
		delete(store.items[principal], item.Key.String())
	}
	return nil
}

func (store *MemoryStore) RemoveAccount(_ context.Context, principal, accountID string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	delete(store.states[principal], accountID)
	for key, item := range store.items[principal] {
		if item.AccountID == accountID {
			delete(store.items[principal], key)
		}
	}
	return nil
}

func (store *MemoryStore) ensure(principal string) {
	if store.states[principal] == nil {
		store.states[principal] = make(map[string]AccountState)
	}
	if store.items[principal] == nil {
		store.items[principal] = make(map[string]Item)
	}
}

func cloneState(state AccountState) AccountState {
	state.Routes = append([]AccountRoute(nil), state.Routes...)
	return state
}
