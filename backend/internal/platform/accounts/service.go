package accounts

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/backend/internal/platform/desiredstate"
	"github.com/QuantProcessing/boltertrader/secretstore"
	"github.com/google/uuid"
)

var (
	ErrInvalidTransition = errors.New("invalid account transition")
	ErrAccountBlocked    = errors.New("account lifecycle transition blocked")
)

type SecretWriter interface {
	Put(context.Context, secretstore.OperationID, []byte) (string, error)
}

type SecretCleaner interface {
	Delete(context.Context, string) error
}

type SecretOperationLookup interface {
	LookupOperation(context.Context, secretstore.OperationID) (string, error)
}

type AuditRecord struct {
	PrincipalID string    `json:"principalId"`
	Action      string    `json:"action"`
	ResourceID  string    `json:"resourceId"`
	Outcome     string    `json:"outcome"`
	OccurredAt  time.Time `json:"occurredAt"`
	RequestID   string    `json:"requestId"`
}

type AuditWriter interface {
	Append(context.Context, AuditRecord) error
}

type Options struct {
	Store       Store
	Secrets     SecretWriter
	Audit       AuditWriter
	Now         func() time.Time
	ID          func() string
	OperationID func() secretstore.OperationID
	Gateway     GatewayDesiredClient
	Mode        secretstore.Mode
	Hooks       LifecycleHooks
}

type Service struct {
	store       Store
	secrets     SecretWriter
	audit       AuditWriter
	now         func() time.Time
	id          func() string
	operationID func() secretstore.OperationID
	gateway     GatewayDesiredClient
	mode        secretstore.Mode
	Hooks       LifecycleHooks
}

func New(options Options) (*Service, error) {
	if options.Store == nil || options.Secrets == nil || options.Audit == nil {
		return nil, errors.New("account store, secret writer, and audit writer are required")
	}
	if options.Now == nil {
		options.Now = func() time.Time { return time.Now().UTC() }
	}
	if options.ID == nil {
		options.ID = func() string {
			var value [12]byte
			_, _ = rand.Read(value[:])
			return "acct_" + hex.EncodeToString(value[:])
		}
	}
	if options.OperationID == nil {
		options.OperationID = func() secretstore.OperationID { return secretstore.OperationID(uuid.NewString()) }
	}
	if options.Mode == "" {
		options.Mode = secretstore.ModeTest
	}
	return &Service{store: options.Store, secrets: options.Secrets, audit: options.Audit, now: options.Now, id: options.ID, operationID: options.OperationID, gateway: options.Gateway, mode: options.Mode, Hooks: options.Hooks}, nil
}

func (service *Service) Create(ctx context.Context, request CreateRequest, principalID string, idempotencyKey ...string) (Account, error) {
	if len(idempotencyKey) > 0 && idempotencyKey[0] != "" && (len(request.Routes) > 0 || len(request.CredentialBundle) > 0) {
		return service.createDurable(ctx, request, principalID, idempotencyKey[0])
	}
	return service.createLegacy(ctx, request, principalID)
}

func (service *Service) createLegacy(ctx context.Context, request CreateRequest, principalID string) (Account, error) {
	if err := validateCreate(request); err != nil {
		return Account{}, err
	}
	material := append([]byte(nil), request.SecretMaterial...)
	defer clear(material)
	secretRef, err := service.secrets.Put(ctx, service.operationID(), material)
	if err != nil || secretRef == "" {
		return Account{}, errors.New("credential storage failed")
	}
	now := service.now().UTC()
	account := Account{ID: service.id(), Venue: request.Venue, VenueEnvironment: request.VenueEnvironment, DisplayName: request.DisplayName, ProductIDs: append([]string(nil), request.ProductIDs...), SecretRef: secretRef, DesiredStateRevision: 1, RuntimeGeneration: 1, CredentialVersion: 1, State: StateCreating, UpdatedAt: now}
	if err := service.store.Create(ctx, account); err != nil {
		service.cleanupSecret(ctx, secretRef)
		return Account{}, err
	}
	if err := service.record(ctx, principalID, "account.create", account.ID, now); err != nil {
		return Account{}, err
	}
	return account, nil
}

func (service *Service) Get(ctx context.Context, id string) (Account, error) {
	return service.store.Get(ctx, id)
}

func (service *Service) UpdateMetadata(ctx context.Context, id string, revision int64, displayName string, productIDs []string, principalID string) (Account, error) {
	if err := validateMetadata(displayName, productIDs); err != nil {
		return Account{}, err
	}
	return service.mutate(ctx, id, revision, principalID, "account.update", func(account *Account) error {
		account.DisplayName = displayName
		account.ProductIDs = append([]string(nil), productIDs...)
		account.ProductPolicy = append([]string(nil), productIDs...)
		return nil
	})
}

func (service *Service) MarkValidated(ctx context.Context, id string, revision int64, principalID string) (Account, error) {
	return service.mutate(ctx, id, revision, principalID, "account.validate", func(account *Account) error {
		now := service.now().UTC()
		account.LastValidatedAt = &now
		return nil
	})
}
func (service *Service) List(ctx context.Context) ([]Account, error) { return service.store.List(ctx) }

func (service *Service) Activate(ctx context.Context, id string, revision int64, principalID string) (Account, error) {
	return service.transition(ctx, id, revision, StateActive, principalID, "account.activate", nil)
}

func (service *Service) Rotate(ctx context.Context, id string, revision int64, secretMaterial []byte, principalID string, idempotencyKey ...string) (Account, error) {
	if len(idempotencyKey) > 0 && strings.TrimSpace(idempotencyKey[0]) != "" {
		return service.rotateDurable(ctx, id, revision, secretMaterial, principalID, idempotencyKey[0])
	}
	return service.rotateLegacy(ctx, id, revision, secretMaterial, principalID)
}

func (service *Service) rotateLegacy(ctx context.Context, id string, revision int64, secretMaterial []byte, principalID string) (Account, error) {
	if len(secretMaterial) == 0 {
		return Account{}, errors.New("secret material is required")
	}
	material := append([]byte(nil), secretMaterial...)
	defer clear(material)
	secretRef, err := service.secrets.Put(ctx, service.operationID(), material)
	if err != nil || secretRef == "" {
		return Account{}, errors.New("credential storage failed")
	}
	account, err := service.transition(ctx, id, revision, StateRotating, principalID, "account.rotate", func(account *Account) {
		account.SecretRef = secretRef
		account.Rotation = &RotationFence{ID: "rotation_" + account.ID + fmt.Sprint("_", account.DesiredStateRevision+1), State: "armed", FromCredentialVersion: account.CredentialVersion, ToCredentialVersion: account.CredentialVersion + 1, FromRuntimeGeneration: account.RuntimeGeneration, ToRuntimeGeneration: account.RuntimeGeneration + 1, ArmedAt: service.now().UTC()}
	})
	if err != nil {
		service.cleanupSecret(ctx, secretRef)
	}
	return account, err
}

func (service *Service) ActivateRotation(ctx context.Context, id string, revision int64, principalID string) (Account, error) {
	return service.transition(ctx, id, revision, StateActive, principalID, "account.rotation.activate", func(account *Account) {
		if account.Rotation != nil {
			account.RuntimeGeneration = account.Rotation.ToRuntimeGeneration
			account.CredentialVersion = account.Rotation.ToCredentialVersion
			account.Rotation = nil
		}
	})
}

func (service *Service) Drain(ctx context.Context, id string, revision int64, blockers Blockers, principalID string) (Account, error) {
	if blockers.Any() {
		return Account{}, ErrAccountBlocked
	}
	return service.transition(ctx, id, revision, StateDraining, principalID, "account.drain", nil)
}

func (service *Service) MarkDrained(ctx context.Context, id string, revision int64, principalID string) (Account, error) {
	return service.transition(ctx, id, revision, StateDrained, principalID, "account.drained", nil)
}

func (service *Service) Disable(ctx context.Context, id string, revision int64, blockers Blockers, principalID string) (Account, error) {
	if blockers.Any() {
		return Account{}, ErrAccountBlocked
	}
	return service.transition(ctx, id, revision, StateDisabled, principalID, "account.disable", nil)
}

func (service *Service) Remove(ctx context.Context, id string, revision int64, principalID string, idempotencyKey ...string) (Account, error) {
	if len(idempotencyKey) > 0 && strings.TrimSpace(idempotencyKey[0]) != "" {
		return service.removeDurable(ctx, id, revision, principalID, idempotencyKey[0])
	}
	return service.removeLegacy(ctx, id, revision, principalID)
}

func (service *Service) removeLegacy(ctx context.Context, id string, revision int64, principalID string) (Account, error) {
	return service.transition(ctx, id, revision, StateRemoving, principalID, "account.remove", nil)
}

func (service *Service) MarkRemoved(ctx context.Context, id string, revision int64, principalID string) (Account, error) {
	return service.transition(ctx, id, revision, StateRemoved, principalID, "account.removed", nil)
}

func (service *Service) transition(ctx context.Context, id string, revision int64, next State, principalID, action string, mutate func(*Account)) (Account, error) {
	account, err := service.store.Get(ctx, id)
	if err != nil {
		return Account{}, err
	}
	if account.DesiredStateRevision != revision {
		return Account{}, ErrRevisionConflict
	}
	if account.PreviousSecretRef != "" {
		return Account{}, ErrAccountBlocked
	}
	if !desiredstate.AllowedTransition(account.State, next) {
		return Account{}, ErrInvalidTransition
	}
	if mutate != nil {
		mutate(&account)
	}
	account.State = next
	account.DesiredStateRevision++
	account.UpdatedAt = service.now().UTC()
	if err := service.store.CompareAndSwap(ctx, id, revision, account); err != nil {
		return Account{}, err
	}
	if err := service.record(ctx, principalID, action, account.ID, account.UpdatedAt); err != nil {
		return Account{}, err
	}
	return account, nil
}

func (service *Service) mutate(ctx context.Context, id string, revision int64, principalID, action string, change func(*Account) error) (Account, error) {
	account, err := service.store.Get(ctx, id)
	if err != nil {
		return Account{}, err
	}
	if account.DesiredStateRevision != revision {
		return Account{}, ErrRevisionConflict
	}
	if account.PreviousSecretRef != "" {
		return Account{}, ErrAccountBlocked
	}
	if err := change(&account); err != nil {
		return Account{}, err
	}
	account.DesiredStateRevision++
	account.UpdatedAt = service.now().UTC()
	if err := service.store.CompareAndSwap(ctx, id, revision, account); err != nil {
		return Account{}, err
	}
	if err := service.record(ctx, principalID, action, account.ID, account.UpdatedAt); err != nil {
		return Account{}, err
	}
	return account, nil
}

func (service *Service) record(ctx context.Context, principalID, action, resourceID string, occurredAt time.Time) error {
	return service.audit.Append(ctx, AuditRecord{PrincipalID: principalID, Action: action, ResourceID: resourceID, Outcome: "accepted", OccurredAt: occurredAt, RequestID: RequestIDFromContext(ctx)})
}

func (service *Service) cleanupSecret(ctx context.Context, reference string) {
	if cleaner, ok := service.secrets.(SecretCleaner); ok {
		_ = cleaner.Delete(ctx, reference)
	}
}

type requestIDKey struct{}

func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, requestID)
}
func RequestIDFromContext(ctx context.Context) string {
	if requestID, ok := ctx.Value(requestIDKey{}).(string); ok && requestID != "" {
		return requestID
	}
	return "internal"
}
