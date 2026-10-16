// Package store persists exchange accounts and their credentials in one
// encrypted file. It is the gateway's only durable state.
//
// File format: JSON array of records. Each record's credential bundle is
// sealed individually with AES-256-GCM (secretstore.Cipher), bound to the
// account id and venue as additional data, so a record cannot be moved to
// another account or venue without failing authentication. Writes are atomic
// (temp file, fsync, rename) and the file is mode 0600.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/secretstore"
)

var (
	ErrNotFound = errors.New("account not found")
	ErrExists   = errors.New("account already exists")
)

// Account is the public part of a stored account.
type Account struct {
	ID        string    `json:"id"`
	Venue     string    `json:"venue"`
	Label     string    `json:"label"`
	CreatedAt time.Time `json:"createdAt"`
}

type record struct {
	Account
	Sealed []byte `json:"sealed"`
}

type Store struct {
	path   string
	cipher *secretstore.Cipher

	mu      sync.Mutex
	records map[string]record
}

// Open loads path (a missing file is an empty store) and verifies every
// record decrypts with cipher.
func Open(path string, cipher *secretstore.Cipher) (*Store, error) {
	if cipher == nil {
		return nil, fmt.Errorf("store cipher is required")
	}
	store := &Store{path: path, cipher: cipher, records: map[string]record{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read account store: %w", err)
	}
	var records []record
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, fmt.Errorf("account store is corrupt: %w", err)
	}
	for _, item := range records {
		plain, err := cipher.Open(item.ID, item.Venue, item.Sealed)
		if err != nil {
			return nil, fmt.Errorf("account %s cannot be decrypted with the configured key", item.ID)
		}
		clear(plain)
		store.records[item.ID] = item
	}
	return store, nil
}

// List returns accounts ordered by creation time.
func (store *Store) List() []Account {
	store.mu.Lock()
	defer store.mu.Unlock()
	accounts := make([]Account, 0, len(store.records))
	for _, item := range store.records {
		accounts = append(accounts, item.Account)
	}
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].CreatedAt.Before(accounts[j].CreatedAt) })
	return accounts
}

// Get returns an account and its decrypted credential bundle. Callers must
// clear the returned bundle when done.
func (store *Store) Get(id string) (Account, []byte, error) {
	store.mu.Lock()
	item, ok := store.records[id]
	store.mu.Unlock()
	if !ok {
		return Account{}, nil, ErrNotFound
	}
	plain, err := store.cipher.Open(item.ID, item.Venue, item.Sealed)
	if err != nil {
		return Account{}, nil, err
	}
	return item.Account, plain, nil
}

// Create seals bundle and persists the account.
func (store *Store) Create(account Account, bundle []byte) error {
	sealed, err := store.cipher.Seal(account.ID, account.Venue, bundle)
	if err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, exists := store.records[account.ID]; exists {
		return ErrExists
	}
	store.records[account.ID] = record{Account: account, Sealed: sealed}
	if err := store.flushLocked(); err != nil {
		delete(store.records, account.ID)
		return err
	}
	return nil
}

// Delete removes an account.
func (store *Store) Delete(id string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	item, ok := store.records[id]
	if !ok {
		return ErrNotFound
	}
	delete(store.records, id)
	if err := store.flushLocked(); err != nil {
		store.records[id] = item
		return err
	}
	return nil
}

func (store *Store) flushLocked() error {
	records := make([]record, 0, len(store.records))
	for _, item := range store.records {
		records = append(records, item)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].CreatedAt.Before(records[j].CreatedAt) })
	data, err := json.Marshal(records)
	if err != nil {
		return err
	}
	dir := filepath.Dir(store.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".accounts-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempName, store.path); err != nil {
		return err
	}
	if directory, err := os.Open(dir); err == nil {
		_ = directory.Sync()
		directory.Close()
	}
	return nil
}
