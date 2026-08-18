package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

var ErrNotFound = errors.New("session not found")

type MemoryStore struct {
	mu       sync.RWMutex
	sessions map[string]StoredSession
}

type MemoryIdentityStore struct {
	mu         sync.RWMutex
	identities map[string]Identity
}

func NewMemoryIdentityStore(identities ...Identity) *MemoryIdentityStore {
	store := &MemoryIdentityStore{identities: make(map[string]Identity)}
	for _, identity := range identities {
		store.Put(identity)
	}
	return store
}

func (store *MemoryIdentityStore) Put(identity Identity) {
	store.mu.Lock()
	store.identities[identity.Username] = identity
	store.mu.Unlock()
}

func (store *MemoryIdentityStore) FindByUsername(_ context.Context, username string) (Identity, error) {
	store.mu.RLock()
	identity, ok := store.identities[username]
	store.mu.RUnlock()
	if !ok {
		return Identity{}, ErrNotFound
	}
	return identity, nil
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{sessions: make(map[string]StoredSession)}
}

func (store *MemoryStore) Save(_ context.Context, stored StoredSession) error {
	store.mu.Lock()
	store.sessions[stored.TokenDigest] = stored
	store.mu.Unlock()
	return nil
}

func (store *MemoryStore) Get(_ context.Context, tokenDigest string) (StoredSession, error) {
	store.mu.RLock()
	stored, ok := store.sessions[tokenDigest]
	store.mu.RUnlock()
	if !ok {
		return StoredSession{}, ErrNotFound
	}
	return stored, nil
}

func (store *MemoryStore) Delete(_ context.Context, tokenDigest string) error {
	store.mu.Lock()
	delete(store.sessions, tokenDigest)
	store.mu.Unlock()
	return nil
}

func (store *MemoryStore) ContainsRaw(value string) bool {
	store.mu.RLock()
	defer store.mu.RUnlock()
	for key, stored := range store.sessions {
		if strings.Contains(fmt.Sprintf("%s:%+v", key, stored), value) {
			return true
		}
	}
	return false
}
