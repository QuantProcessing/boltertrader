package accounts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/QuantProcessing/boltertrader/credential"
	"github.com/QuantProcessing/boltertrader/routeprofile"
	"github.com/QuantProcessing/boltertrader/secretstore"
)

var ErrIdempotencyConflict = errors.New("idempotency key conflicts with a different request")

type DesiredApply struct {
	OperationID secretstore.OperationID
	Account     Account
}

type GatewayRuntimeTruth struct {
	AccountID            string
	DesiredStateRevision int64
	RuntimeGeneration    int64
	CredentialVersion    int64
	Active               bool
	Tombstoned           bool
}

type RetireRequest struct {
	OperationID secretstore.OperationID
	AccountID   string
}

type GatewayRetirement struct {
	Tombstoned bool
	Blockers   Blockers
}

type GatewayDesiredClient interface {
	ApplyDesired(context.Context, DesiredApply) (GatewayRuntimeTruth, error)
	GetRuntimeTruth(context.Context, string) (GatewayRuntimeTruth, error)
	RetireAccount(context.Context, RetireRequest) (GatewayRetirement, error)
}

type LifecycleHooks struct {
	AfterDraftCommit          func() error
	AfterProviderPut          func() error
	AfterReferenceBinding     func() error
	AfterGatewayApply         func() error
	AfterPromotionObservation func() error
	AfterGatewayTombstone     func() error
	AfterProviderDelete       func() error
}

func (service *Service) createDurable(ctx context.Context, request CreateRequest, principalID, idempotencyKey string) (Account, error) {
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" {
		return Account{}, errors.New("idempotency key is required")
	}
	account, material, requestDigest, err := service.normalizeDurableCreate(request)
	if err != nil {
		return Account{}, err
	}
	defer clear(material)
	lookup := OperationLookup{Mode: service.mode, PrincipalID: principalID, Kind: OperationCreate, IdempotencyKeyDigest: digestString(idempotencyKey)}
	operation, err := service.store.FindCredentialOperation(ctx, lookup)
	newOperation := errors.Is(err, ErrNotFound)
	if err != nil && !newOperation {
		return Account{}, err
	}
	if !newOperation {
		if operation.RequestDigest != requestDigest {
			return Account{}, ErrIdempotencyConflict
		}
		account, err = service.store.Get(ctx, operation.AccountID)
		if err != nil {
			return Account{}, err
		}
		if operation.State == OperationActive {
			return account, nil
		}
	} else {
		now := service.now().UTC()
		account.ID = service.id()
		account.UpdatedAt = now
		operation = CredentialOperation{ID: service.operationID(), PrincipalID: principalID, AccountID: account.ID, Mode: service.mode, Kind: OperationCreate, State: OperationAwaitingMaterial, IdempotencyKeyDigest: lookup.IdempotencyKeyDigest, RequestDigest: requestDigest, TargetDesiredStateRevision: 1, TargetRuntimeGeneration: 1, TargetCredentialVersion: 1, CreatedAt: now, UpdatedAt: now}
		if err := service.store.CreateDraft(ctx, account, operation); err != nil {
			return Account{}, err
		}
		if err := callHook(service.Hooks.AfterDraftCommit); err != nil {
			return Account{}, err
		}
	}

	if operation.State == OperationAwaitingMaterial {
		var reference string
		if !newOperation {
			if lookupWriter, ok := service.secrets.(SecretOperationLookup); ok {
				reference, err = lookupWriter.LookupOperation(ctx, operation.ID)
				if err != nil && !errors.Is(err, secretstore.ErrSecretNotFound) {
					return Account{}, errors.New("credential lookup failed")
				}
			}
		}
		if reference == "" {
			reference, err = service.secrets.Put(ctx, operation.ID, material)
			if err != nil || reference == "" {
				return Account{}, errors.New("credential storage failed")
			}
			if err := callHook(service.Hooks.AfterProviderPut); err != nil {
				return Account{}, err
			}
		}
		account, operation, err = service.store.BindCredentialReference(ctx, operation.ID, reference)
		if err != nil {
			return Account{}, err
		}
		if err := callHook(service.Hooks.AfterReferenceBinding); err != nil {
			return Account{}, err
		}
	}

	if operation.State == OperationStored {
		operation.State = OperationApplying
		operation.UpdatedAt = service.now().UTC()
		account, operation, err = service.store.CompareAndSwapAccountAndOperation(ctx, TransitionRequest{OperationID: operation.ID, ExpectedOperationState: OperationStored, ExpectedDesiredStateRevision: account.DesiredStateRevision, Account: account, Operation: operation})
		if err != nil {
			return Account{}, err
		}
	}
	if service.gateway == nil {
		return account, nil
	}
	truth, err := service.gateway.ApplyDesired(ctx, DesiredApply{OperationID: operation.ID, Account: account})
	if err != nil {
		return Account{}, errors.New("gateway apply failed")
	}
	if err := callHook(service.Hooks.AfterGatewayApply); err != nil {
		return Account{}, err
	}
	if !truth.Active || truth.AccountID != account.ID || truth.DesiredStateRevision != account.DesiredStateRevision || truth.RuntimeGeneration != account.RuntimeGeneration || truth.CredentialVersion != account.CredentialVersion {
		return Account{}, errors.New("gateway active fence mismatch")
	}
	if err := callHook(service.Hooks.AfterPromotionObservation); err != nil {
		return Account{}, err
	}
	account.State = StateActive
	account.UpdatedAt = service.now().UTC()
	operation.State = OperationActive
	operation.UpdatedAt = account.UpdatedAt
	account, _, err = service.store.CompareAndSwapAccountAndOperation(ctx, TransitionRequest{OperationID: operation.ID, ExpectedOperationState: OperationApplying, ExpectedDesiredStateRevision: account.DesiredStateRevision, Account: account, Operation: operation})
	if err != nil {
		return Account{}, err
	}
	if err := service.record(ctx, principalID, "account.create", account.ID, account.UpdatedAt); err != nil {
		return Account{}, err
	}
	return account, nil
}

func (service *Service) normalizeDurableCreate(request CreateRequest) (Account, []byte, string, error) {
	material := append([]byte(nil), request.CredentialBundle...)
	if len(material) == 0 {
		material = append([]byte(nil), request.SecretMaterial...)
	}
	if len(material) == 0 {
		return Account{}, nil, "", errors.New("credential bundle is required")
	}
	decoded, err := credential.DefaultRegistry().Decode(request.Venue, material)
	if err != nil {
		clear(material)
		return Account{}, nil, "", errors.New("credential bundle is invalid")
	}
	decoded.Clear()
	routes := append([]AccountRoute(nil), request.Routes...)
	sort.Slice(routes, func(i, j int) bool { return routes[i].ID < routes[j].ID })
	if len(routes) == 0 {
		clear(material)
		return Account{}, nil, "", errors.New("account routes are required")
	}
	profile, err := routeprofile.DefaultRegistry().Resolve(routeprofile.Mode(service.mode), request.Venue, routes[0].RouteProfileID)
	if err != nil {
		clear(material)
		return Account{}, nil, "", errors.New("route profile is invalid")
	}
	policy := append([]string(nil), request.ProductPolicy...)
	if len(policy) == 0 {
		policy = append([]string(nil), request.ProductIDs...)
	}
	account := Account{Venue: request.Venue, VenueEnvironment: Environment(profile.NativeEnvironment), Mode: service.mode, NativeEnvironment: Environment(profile.NativeEnvironment), DisplayName: request.DisplayName, ProductIDs: append([]string(nil), policy...), ProductPolicy: policy, Routes: routes, DesiredStateRevision: 1, RuntimeGeneration: 1, CredentialVersion: 1, State: StateCreating}
	validationAccount := account
	validationAccount.ID = "pending"
	if err := validationAccount.Validate(); err != nil {
		clear(material)
		return Account{}, nil, "", err
	}
	digestInput := struct {
		Venue            string
		DisplayName      string
		ProductPolicy    []string
		Routes           []AccountRoute
		CredentialBundle json.RawMessage
	}{request.Venue, request.DisplayName, policy, routes, material}
	encoded, err := json.Marshal(digestInput)
	if err != nil {
		clear(material)
		return Account{}, nil, "", err
	}
	requestDigest := digestBytes(encoded)
	clear(encoded)
	return account, material, requestDigest, nil
}

func digestString(value string) string { return digestBytes([]byte(value)) }
func digestBytes(value []byte) string  { sum := sha256.Sum256(value); return hex.EncodeToString(sum[:]) }
func callHook(hook func() error) error {
	if hook != nil {
		return hook()
	}
	return nil
}
