package lifecycle

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/secretstore"
)

var (
	ErrNotFound         = errors.New("account not found")
	ErrAlreadyExists    = errors.New("account already exists")
	ErrRevisionConflict = errors.New("desired state revision conflict")
)

type Store interface {
	Create(context.Context, Account) error
	CreateDraft(context.Context, Account, CredentialOperation) error
	BeginCredentialOperation(context.Context, int64, Account, CredentialOperation) error
	Get(context.Context, string) (Account, error)
	List(context.Context) ([]Account, error)
	CompareAndSwap(context.Context, string, int64, Account) error
	GetCredentialOperation(context.Context, secretstore.OperationID) (CredentialOperation, error)
	FindCredentialOperation(context.Context, OperationLookup) (CredentialOperation, error)
	BindCredentialReference(context.Context, secretstore.OperationID, string) (Account, CredentialOperation, error)
	CompareAndSwapAccountAndOperation(context.Context, TransitionRequest) (Account, CredentialOperation, error)
	RecordRotationCleanupProof(context.Context, RotationCleanupProof) (CredentialOperation, error)
	CompleteRotationCleanup(context.Context, RotationCleanupCompletion) (Account, CredentialOperation, error)
	ListRecoverableCredentialOperations(context.Context, int) ([]CredentialOperation, error)
}

func (store *MemoryStore) BeginCredentialOperation(_ context.Context, expectedRevision int64, account Account, operation CredentialOperation) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	current, exists := store.accounts[account.ID]
	if !exists {
		return ErrNotFound
	}
	if current.DesiredStateRevision != expectedRevision {
		return ErrRevisionConflict
	}
	if _, exists := store.operations[operation.ID]; exists {
		return ErrAlreadyExists
	}
	var superseded []secretstore.OperationID
	for _, existing := range store.operations {
		if existing.AccountID == operation.AccountID && credentialOperationRecoverable(existing.State) {
			if retirementSupersedesCreation(current, account, operation, existing) {
				superseded = append(superseded, existing.ID)
				continue
			}
			return ErrRevisionConflict
		}
		if operationLookupMatches(existing, OperationLookup{PrincipalID: operation.PrincipalID, Kind: operation.Kind, IdempotencyKeyDigest: operation.IdempotencyKeyDigest}) {
			return ErrAlreadyExists
		}
	}
	for _, id := range superseded {
		existing := store.operations[id]
		existing.State = OperationAbandoned
		existing.StableErrorCode = "ACCOUNT_REMOVED_DURING_CREATION"
		existing.UpdatedAt = operation.UpdatedAt
		store.operations[id] = existing
	}
	store.accounts[account.ID] = clone(account)
	store.operations[operation.ID] = operation
	return nil
}

type MemoryStore struct {
	mu         sync.RWMutex
	accounts   map[string]Account
	operations map[secretstore.OperationID]CredentialOperation
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{accounts: make(map[string]Account), operations: make(map[secretstore.OperationID]CredentialOperation)}
}

func (store *MemoryStore) Create(_ context.Context, account Account) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, exists := store.accounts[account.ID]; exists {
		return ErrAlreadyExists
	}
	store.accounts[account.ID] = clone(account)
	return nil
}

func (store *MemoryStore) CreateDraft(_ context.Context, account Account, operation CredentialOperation) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, exists := store.accounts[account.ID]; exists {
		return ErrAlreadyExists
	}
	if _, exists := store.operations[operation.ID]; exists {
		return ErrAlreadyExists
	}
	if operation.AccountID != account.ID || operation.State != OperationAwaitingMaterial {
		return ErrInvalidTransition
	}
	for _, existing := range store.operations {
		if operationLookupMatches(existing, OperationLookup{PrincipalID: operation.PrincipalID, Kind: operation.Kind, IdempotencyKeyDigest: operation.IdempotencyKeyDigest}) {
			return ErrAlreadyExists
		}
	}
	store.accounts[account.ID] = clone(account)
	store.operations[operation.ID] = operation
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

func (store *MemoryStore) GetCredentialOperation(_ context.Context, id secretstore.OperationID) (CredentialOperation, error) {
	store.mu.RLock()
	operation, exists := store.operations[id]
	store.mu.RUnlock()
	if !exists {
		return CredentialOperation{}, ErrNotFound
	}
	return operation, nil
}

func (store *MemoryStore) FindCredentialOperation(_ context.Context, lookup OperationLookup) (CredentialOperation, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	for _, operation := range store.operations {
		if operationLookupMatches(operation, lookup) {
			return operation, nil
		}
	}
	return CredentialOperation{}, ErrNotFound
}

func (store *MemoryStore) BindCredentialReference(_ context.Context, id secretstore.OperationID, reference string) (Account, CredentialOperation, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	operation, exists := store.operations[id]
	if !exists {
		return Account{}, CredentialOperation{}, ErrNotFound
	}
	account, exists := store.accounts[operation.AccountID]
	if !exists {
		return Account{}, CredentialOperation{}, ErrNotFound
	}
	if operation.State == OperationStored && operation.SecretRef == reference && account.SecretRef == reference {
		return clone(account), operation, nil
	}
	if operation.State != OperationAwaitingMaterial || reference == "" {
		return Account{}, CredentialOperation{}, ErrInvalidTransition
	}
	account.SecretRef = reference
	operation.SecretRef = reference
	operation.State = OperationStored
	operation.UpdatedAt = time.Now().UTC()
	store.accounts[account.ID] = clone(account)
	store.operations[id] = operation
	return clone(account), operation, nil
}

func (store *MemoryStore) CompareAndSwapAccountAndOperation(_ context.Context, request TransitionRequest) (Account, CredentialOperation, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	operation, exists := store.operations[request.OperationID]
	if !exists {
		return Account{}, CredentialOperation{}, ErrNotFound
	}
	account, exists := store.accounts[operation.AccountID]
	if !exists {
		return Account{}, CredentialOperation{}, ErrNotFound
	}
	if operation.State != request.ExpectedOperationState || account.DesiredStateRevision != request.ExpectedDesiredStateRevision || request.Account.ID != account.ID || request.Operation.ID != operation.ID {
		return Account{}, CredentialOperation{}, ErrRevisionConflict
	}
	store.accounts[account.ID] = clone(request.Account)
	store.operations[operation.ID] = request.Operation
	return clone(request.Account), request.Operation, nil
}

func (store *MemoryStore) RecordRotationCleanupProof(_ context.Context, proof RotationCleanupProof) (CredentialOperation, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	operation, exists := store.operations[proof.OperationID]
	if !exists {
		return CredentialOperation{}, ErrNotFound
	}
	account, exists := store.accounts[operation.AccountID]
	if !exists {
		return CredentialOperation{}, ErrNotFound
	}
	if err := ValidateRotationCleanupProof(account, operation, proof); err != nil {
		return CredentialOperation{}, ErrRevisionConflict
	}
	if HasRotationCleanupProof(account, operation) {
		return operation, nil
	}
	operation.RotationCleanupTombstoneRevision = proof.TombstoneRevision
	operation.RotationCleanupCompletedAt = proof.CompletedAt.UTC()
	operation.StableErrorCode = ""
	operation.UpdatedAt = proof.CompletedAt.UTC()
	store.operations[operation.ID] = operation
	return operation, nil
}

func (store *MemoryStore) CompleteRotationCleanup(_ context.Context, completion RotationCleanupCompletion) (Account, CredentialOperation, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	operation, exists := store.operations[completion.OperationID]
	if !exists {
		return Account{}, CredentialOperation{}, ErrNotFound
	}
	account, exists := store.accounts[operation.AccountID]
	if !exists {
		return Account{}, CredentialOperation{}, ErrNotFound
	}
	if err := ValidateRotationCleanupCompletion(account, operation, completion); err != nil {
		return Account{}, CredentialOperation{}, ErrRevisionConflict
	}
	if operation.State == OperationActive && account.PreviousSecretRef == "" {
		return clone(account), operation, nil
	}
	completedAt := completion.CompletedAt.UTC()
	account.PreviousSecretRef = ""
	account.Rotation = nil
	account.State = StateActive
	account.UpdatedAt = completedAt
	operation.State = OperationActive
	operation.StableErrorCode = ""
	operation.UpdatedAt = completedAt
	store.accounts[account.ID] = clone(account)
	store.operations[operation.ID] = operation
	return clone(account), operation, nil
}

func (store *MemoryStore) ListRecoverableCredentialOperations(_ context.Context, limit int) ([]CredentialOperation, error) {
	if limit < 1 {
		return nil, nil
	}
	store.mu.RLock()
	result := make([]CredentialOperation, 0)
	for _, operation := range store.operations {
		if credentialOperationRecoverable(operation.State) {
			result = append(result, operation)
		}
	}
	store.mu.RUnlock()
	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].ID < result[j].ID
		}
		return result[i].CreatedAt.Before(result[j].CreatedAt)
	})
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func operationLookupMatches(operation CredentialOperation, lookup OperationLookup) bool {
	return operation.PrincipalID == lookup.PrincipalID && operation.Kind == lookup.Kind && operation.IdempotencyKeyDigest == lookup.IdempotencyKeyDigest
}

func credentialOperationRecoverable(state OperationState) bool {
	switch state {
	case OperationAwaitingMaterial, OperationStored, OperationApplying, OperationCleanupPending:
		return true
	default:
		return false
	}
}

func retirementSupersedesCreation(current, next Account, requested, existing CredentialOperation) bool {
	return current.State == StateCreating && next.State == StateRemoving &&
		requested.Kind == OperationRetire && existing.Kind == OperationCreate
}

func clone(account Account) Account {
	account.ProductIDs = append([]string(nil), account.ProductIDs...)
	account.ProductPolicy = append([]string(nil), account.ProductPolicy...)
	account.Routes = append([]AccountRoute(nil), account.Routes...)
	if account.Rotation != nil {
		rotation := *account.Rotation
		account.Rotation = &rotation
	}
	return account
}
