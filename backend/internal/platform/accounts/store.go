package accounts

import (
	"context"
	"errors"
	"sort"
	"sync"
)

var (
	ErrNotFound         = errors.New("account not found")
	ErrAlreadyExists    = errors.New("account already exists")
	ErrRevisionConflict = errors.New("desired state revision conflict")
)

type Store interface {
	Create(context.Context, Account) error
	Get(context.Context, string) (Account, error)
	List(context.Context) ([]Account, error)
	CompareAndSwap(context.Context, string, int64, Account) error
}

type MemoryStore struct {
	mu       sync.RWMutex
	accounts map[string]Account
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{accounts: make(map[string]Account)} }

func (store *MemoryStore) Create(_ context.Context, account Account) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, exists := store.accounts[account.ID]; exists {
		return ErrAlreadyExists
	}
	store.accounts[account.ID] = clone(account)
	return nil
}

func (store *MemoryStore) Get(_ context.Context, id string) (Account, error) {
	store.mu.RLock()
	account, exists := store.accounts[id]
	store.mu.RUnlock()
	if !exists {
		return Account{}, ErrNotFound
	}
	return clone(account), nil
}

func (store *MemoryStore) List(_ context.Context) ([]Account, error) {
	store.mu.RLock()
	result := make([]Account, 0, len(store.accounts))
	for _, account := range store.accounts {
		result = append(result, clone(account))
	}
	store.mu.RUnlock()
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func (store *MemoryStore) CompareAndSwap(_ context.Context, id string, expectedRevision int64, next Account) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	current, exists := store.accounts[id]
	if !exists {
		return ErrNotFound
	}
	if current.DesiredStateRevision != expectedRevision {
		return ErrRevisionConflict
	}
	store.accounts[id] = clone(next)
	return nil
}

func clone(account Account) Account {
	account.ProductIDs = append([]string(nil), account.ProductIDs...)
	if account.Rotation != nil {
		rotation := *account.Rotation
		account.Rotation = &rotation
	}
	return account
}
