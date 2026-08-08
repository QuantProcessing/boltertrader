package funding

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type identityState struct {
	Next    uint64            `json:"next"`
	Pending map[string]string `json:"pending"`
}

type FileIdentityStore struct {
	path   string
	prefix string
	mu     sync.Mutex
}

func NewFileIdentityStore(path, workerID, instanceID string) (*FileIdentityStore, error) {
	if strings.TrimSpace(path) == "" || strings.TrimSpace(workerID) == "" || strings.TrimSpace(instanceID) == "" {
		return nil, fmt.Errorf("identity state path and worker identity are required")
	}
	store := &FileIdentityStore{path: path, prefix: sanitizeIdentity(workerID) + ":" + sanitizeIdentity(instanceID)}
	store.mu.Lock()
	defer store.mu.Unlock()
	state, err := store.load()
	if err != nil {
		return nil, err
	}
	return store, store.save(state)
}

func (store *FileIdentityStore) Begin(operation string) (string, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	state, err := store.load()
	if err != nil {
		return "", err
	}
	if key := state.Pending[operation]; key != "" {
		return key, nil
	}
	state.Next++
	key := fmt.Sprintf("%s:%s:%d", store.prefix, sanitizeIdentity(operation), state.Next)
	state.Pending[operation] = key
	if err := store.save(state); err != nil {
		return "", err
	}
	return key, nil
}

func (store *FileIdentityStore) Commit(operation, key string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	state, err := store.load()
	if err != nil {
		return err
	}
	if state.Pending[operation] != key {
		return fmt.Errorf("identity operation does not match pending key")
	}
	delete(state.Pending, operation)
	return store.save(state)
}

func (store *FileIdentityStore) load() (identityState, error) {
	state := identityState{Pending: make(map[string]string)}
	data, err := os.ReadFile(store.path)
	if os.IsNotExist(err) {
		return state, nil
	}
	if err != nil {
		return state, fmt.Errorf("read identity state: %w", err)
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, fmt.Errorf("decode identity state: %w", err)
	}
	if state.Pending == nil {
		state.Pending = make(map[string]string)
	}
	return state, nil
}

func (store *FileIdentityStore) save(state identityState) error {
	if err := os.MkdirAll(filepath.Dir(store.path), 0o700); err != nil {
		return fmt.Errorf("create identity state directory: %w", err)
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	temporary := store.path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return fmt.Errorf("write identity state: %w", err)
	}
	if err := os.Rename(temporary, store.path); err != nil {
		return fmt.Errorf("commit identity state: %w", err)
	}
	return nil
}

func sanitizeIdentity(value string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, value)
}
