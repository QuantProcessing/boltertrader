package accounts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
	OperationID          secretstore.OperationID
	AccountID            string
	DesiredStateRevision int64
	RuntimeGeneration    int64
	CredentialVersion    int64
	RequestedState       State
	Routes               []GatewayRuntimeRoute
	Active               bool
	Tombstoned           bool
}

type GatewayRuntimeRoute struct {
	RouteID         string
	Product         Product
	SettlementAsset string
	RouteProfileID  string
	RequestedState  RouteState
	Ready           bool
}

type ApplyResultError interface {
	ResultMayHaveCommitted() bool
}

type RetireRequest struct {
	OperationID          secretstore.OperationID
	AccountID            string
	DesiredStateRevision int64
	RuntimeGeneration    int64
	CredentialVersion    int64
}

type GatewayRetirement struct {
	Tombstoned bool
	Blockers   Blockers
}

type GatewayDesiredClient interface {
	ApplyDesired(context.Context, DesiredApply) (GatewayRuntimeTruth, error)
	GetRuntimeTruth(context.Context, string) (GatewayRuntimeTruth, error)
	FinalizeRotation(context.Context, RotationCleanupRequest) (RotationCleanupProof, error)
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
		return Account{}, validationError(errors.New("idempotency key is required"))
	}
	account, material, requestDigest, err := service.normalizeDurableCreate(request)
	if err != nil {
		return Account{}, err
	}
	defer clear(material)
	now := service.now().UTC()
	account.ID = service.id()
	account.UpdatedAt = now
	operation := CredentialOperation{ID: service.operationID(), PrincipalID: principalID, AccountID: account.ID, Mode: service.mode, Kind: OperationCreate, State: OperationAwaitingMaterial, IdempotencyKeyDigest: digestString(idempotencyKey), RequestDigest: requestDigest, TargetDesiredStateRevision: 1, TargetRuntimeGeneration: 1, TargetCredentialVersion: 1, CreatedAt: now, UpdatedAt: now}
	if err := account.Validate(); err != nil {
		return Account{}, validationError(err)
	}
	if err := operation.Validate(); err != nil {
		return Account{}, validationError(err)
	}
	lookup := OperationLookup{Mode: service.mode, PrincipalID: principalID, Kind: OperationCreate, IdempotencyKeyDigest: digestString(idempotencyKey)}
	found, err := service.store.FindCredentialOperation(ctx, lookup)
	newOperation := errors.Is(err, ErrNotFound)
	if err != nil && !newOperation {
		return Account{}, err
	}
	if !newOperation {
		operation = found
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
	if !exactGatewayActiveTruth(operation.ID, account, truth) {
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
	if strings.TrimSpace(request.DisplayName) == "" || request.DisplayName != strings.TrimSpace(request.DisplayName) || len(request.DisplayName) > 128 {
		return Account{}, nil, "", validationError(errors.New("display name is invalid"))
	}
	policy := append([]string(nil), request.ProductPolicy...)
	if len(policy) == 0 {
		policy = append([]string(nil), request.ProductIDs...)
	}
	if err := validateDurableProductPolicy(policy); err != nil {
		return Account{}, nil, "", validationError(err)
	}
	if len(request.Routes) == 0 || len(request.Routes) > 32 {
		return Account{}, nil, "", validationError(errors.New("account routes are invalid"))
	}
	material := append([]byte(nil), request.CredentialBundle...)
	if len(material) == 0 {
		material = append([]byte(nil), request.SecretMaterial...)
	}
	if len(material) == 0 {
		return Account{}, nil, "", validationError(errors.New("credential bundle is required"))
	}
	decoded, err := credential.DefaultRegistry().Decode(request.Venue, material)
	if err != nil {
		clear(material)
		return Account{}, nil, "", validationError(errors.New("credential bundle is invalid"))
	}
	decoded.Clear()
	routes := append([]AccountRoute(nil), request.Routes...)
	sort.Slice(routes, func(i, j int) bool { return routes[i].ID < routes[j].ID })
	profile, err := routeprofile.DefaultRegistry().Resolve(routeprofile.Mode(service.mode), request.Venue, routes[0].RouteProfileID)
	if err != nil {
		clear(material)
		return Account{}, nil, "", validationError(errors.New("route profile is invalid"))
	}
	account := Account{Venue: request.Venue, VenueEnvironment: Environment(profile.NativeEnvironment), Mode: service.mode, NativeEnvironment: Environment(profile.NativeEnvironment), DisplayName: request.DisplayName, ProductIDs: append([]string(nil), policy...), ProductPolicy: policy, Routes: routes, DesiredStateRevision: 1, RuntimeGeneration: 1, CredentialVersion: 1, State: StateCreating}
	validationAccount := account
	validationAccount.ID = "pending"
	if err := validationAccount.Validate(); err != nil {
		clear(material)
		return Account{}, nil, "", validationError(err)
	}
	if err := validateRouteProductCompatibility(policy, routes); err != nil {
		clear(material)
		return Account{}, nil, "", validationError(err)
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
		return Account{}, nil, "", validationError(err)
	}
	requestDigest := digestBytes(encoded)
	clear(encoded)
	return account, material, requestDigest, nil
}

func validationError(err error) error {
	return fmt.Errorf("%w: %v", ErrValidation, err)
}

func validateDurableProductPolicy(policy []string) error {
	if len(policy) == 0 {
		return errors.New("product policy is required")
	}
	seen := make(map[string]struct{}, len(policy))
	for _, product := range policy {
		if product != "funding" && product != "manual" {
			return errors.New("product policy is invalid")
		}
		if _, duplicate := seen[product]; duplicate {
			return errors.New("product policy is duplicated")
		}
		seen[product] = struct{}{}
	}
	return nil
}

func validateRouteProductCompatibility(policy []string, routes []AccountRoute) error {
	products := make(map[string]struct{}, len(policy))
	for _, product := range policy {
		products[product] = struct{}{}
	}
	fundingRoute := false
	for _, route := range routes {
		_, manual := products["manual"]
		_, funding := products["funding"]
		if funding && route.Product == ProductPerp {
			fundingRoute = fundingRoute || route.RequestedState == RouteActive
		}
		if !manual && !(funding && route.Product == ProductPerp) {
			return errors.New("route is incompatible with product policy")
		}
	}
	if _, funding := products["funding"]; funding && !fundingRoute {
		return errors.New("funding product requires an active perpetual route")
	}
	return nil
}

func digestString(value string) string { return digestBytes([]byte(value)) }
func digestBytes(value []byte) string  { sum := sha256.Sum256(value); return hex.EncodeToString(sum[:]) }
func callHook(hook func() error) error {
	if hook != nil {
		return hook()
	}
	return nil
}

func (service *Service) rotateDurable(ctx context.Context, id string, revision int64, bundle []byte, principalID, idempotencyKey string) (Account, error) {
	material := append([]byte(nil), bundle...)
	defer clear(material)
	account, err := service.store.Get(ctx, id)
	if err != nil {
		return Account{}, err
	}
	decoded, err := credential.DefaultRegistry().Decode(account.Venue, material)
	if err != nil {
		return Account{}, errors.New("credential bundle is invalid")
	}
	decoded.Clear()
	requestDigest := digestBytes(append([]byte(id+"\x00"+fmt.Sprint(revision)+"\x00"), material...))
	lookup := OperationLookup{Mode: service.mode, PrincipalID: principalID, Kind: OperationRotate, IdempotencyKeyDigest: digestString(strings.TrimSpace(idempotencyKey))}
	operation, findErr := service.store.FindCredentialOperation(ctx, lookup)
	if findErr == nil {
		if operation.RequestDigest != requestDigest || operation.AccountID != id {
			return Account{}, ErrIdempotencyConflict
		}
		account, err = service.store.Get(ctx, operation.AccountID)
		if err != nil {
			return Account{}, err
		}
		if operation.State == OperationActive || operation.State == OperationCleanupPending {
			return account, nil
		}
	} else if !errors.Is(findErr, ErrNotFound) {
		return Account{}, findErr
	} else {
		if account.DesiredStateRevision != revision {
			return Account{}, ErrRevisionConflict
		}
		if account.PreviousSecretRef != "" {
			return Account{}, ErrAccountBlocked
		}
		now := service.now().UTC()
		account.PreviousSecretRef = account.SecretRef
		account.State = StateRotating
		account.DesiredStateRevision++
		account.RuntimeGeneration++
		account.CredentialVersion++
		account.UpdatedAt = now
		operation = CredentialOperation{ID: service.operationID(), PrincipalID: principalID, AccountID: account.ID, Mode: service.mode, Kind: OperationRotate, State: OperationAwaitingMaterial, IdempotencyKeyDigest: lookup.IdempotencyKeyDigest, RequestDigest: requestDigest, TargetDesiredStateRevision: account.DesiredStateRevision, TargetRuntimeGeneration: account.RuntimeGeneration, TargetCredentialVersion: account.CredentialVersion, CreatedAt: now, UpdatedAt: now}
		if err := service.store.BeginCredentialOperation(ctx, revision, account, operation); err != nil {
			return Account{}, err
		}
		if err := callHook(service.Hooks.AfterDraftCommit); err != nil {
			return Account{}, err
		}
	}
	if operation.State == OperationAwaitingMaterial {
		var reference string
		if findErr == nil {
			if lookupWriter, ok := service.secrets.(SecretOperationLookup); ok {
				reference, _ = lookupWriter.LookupOperation(ctx, operation.ID)
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
		operation.State, operation.UpdatedAt = OperationApplying, service.now().UTC()
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
	if !exactGatewayActiveTruth(operation.ID, account, truth) {
		return Account{}, errors.New("gateway active fence mismatch")
	}
	if err := callHook(service.Hooks.AfterPromotionObservation); err != nil {
		return Account{}, err
	}
	account.State, account.UpdatedAt = StateActive, service.now().UTC()
	operation.State = OperationCleanupPending
	operation.UpdatedAt = account.UpdatedAt
	account, _, err = service.store.CompareAndSwapAccountAndOperation(ctx, TransitionRequest{OperationID: operation.ID, ExpectedOperationState: OperationApplying, ExpectedDesiredStateRevision: account.DesiredStateRevision, Account: account, Operation: operation})
	return account, err
}

func exactGatewayActiveTruth(operationID secretstore.OperationID, account Account, truth GatewayRuntimeTruth) bool {
	if !truth.Active || truth.OperationID != operationID || truth.AccountID != account.ID ||
		truth.DesiredStateRevision != account.DesiredStateRevision || truth.RuntimeGeneration != account.RuntimeGeneration ||
		truth.CredentialVersion != account.CredentialVersion || truth.RequestedState != account.State ||
		len(truth.Routes) != len(account.Routes) {
		return false
	}
	desired := make(map[string]AccountRoute, len(account.Routes))
	for _, route := range account.Routes {
		if _, duplicate := desired[route.ID]; duplicate {
			return false
		}
		desired[route.ID] = route
	}
	seen := make(map[string]struct{}, len(truth.Routes))
	for _, route := range truth.Routes {
		if _, duplicate := seen[route.RouteID]; duplicate {
			return false
		}
		seen[route.RouteID] = struct{}{}
		want, ok := desired[route.RouteID]
		if !ok || route.Product != want.Product || route.SettlementAsset != want.SettlementAsset ||
			route.RouteProfileID != want.RouteProfileID || route.RequestedState != want.RequestedState ||
			(route.RequestedState == RouteActive && !route.Ready) {
			return false
		}
	}
	return true
}

func (service *Service) removeDurable(ctx context.Context, id string, revision int64, principalID, idempotencyKey string) (Account, error) {
	requestDigest := digestString(id + "\x00" + fmt.Sprint(revision))
	lookup := OperationLookup{Mode: service.mode, PrincipalID: principalID, Kind: OperationRetire, IdempotencyKeyDigest: digestString(strings.TrimSpace(idempotencyKey))}
	operation, err := service.store.FindCredentialOperation(ctx, lookup)
	account, getErr := service.store.Get(ctx, id)
	if getErr != nil {
		return Account{}, getErr
	}
	if err == nil {
		if operation.RequestDigest != requestDigest || operation.AccountID != id {
			return Account{}, ErrIdempotencyConflict
		}
		if operation.State == OperationActive {
			return account, nil
		}
	} else if !errors.Is(err, ErrNotFound) {
		return Account{}, err
	} else {
		if account.DesiredStateRevision != revision {
			return Account{}, ErrRevisionConflict
		}
		if account.PreviousSecretRef != "" {
			return Account{}, ErrAccountBlocked
		}
		now := service.now().UTC()
		account.State, account.DesiredStateRevision, account.UpdatedAt = StateRemoving, account.DesiredStateRevision+1, now
		operation = CredentialOperation{ID: service.operationID(), PrincipalID: principalID, AccountID: id, Mode: service.mode, Kind: OperationRetire, State: OperationCleanupPending, IdempotencyKeyDigest: lookup.IdempotencyKeyDigest, RequestDigest: requestDigest, TargetDesiredStateRevision: account.DesiredStateRevision, TargetRuntimeGeneration: account.RuntimeGeneration, TargetCredentialVersion: account.CredentialVersion, CreatedAt: now, UpdatedAt: now}
		if err := service.store.BeginCredentialOperation(ctx, revision, account, operation); err != nil {
			return Account{}, err
		}
		if err := callHook(service.Hooks.AfterDraftCommit); err != nil {
			return Account{}, err
		}
	}
	if service.gateway == nil {
		return account, nil
	}
	retirement, err := service.gateway.RetireAccount(ctx, RetireRequest{
		OperationID: operation.ID, AccountID: id,
		DesiredStateRevision: account.DesiredStateRevision,
		RuntimeGeneration:    account.RuntimeGeneration,
		CredentialVersion:    account.CredentialVersion,
	})
	if err != nil || retirement.Blockers.Any() || !retirement.Tombstoned {
		return Account{}, ErrAccountBlocked
	}
	if err := callHook(service.Hooks.AfterGatewayTombstone); err != nil {
		return Account{}, err
	}
	if cleaner, ok := service.secrets.(SecretCleaner); ok {
		if account.PreviousSecretRef != "" {
			if err := cleaner.Delete(ctx, account.PreviousSecretRef); err != nil {
				return Account{}, errors.New("credential deletion failed")
			}
		}
		if account.SecretRef != "" {
			if err := cleaner.Delete(ctx, account.SecretRef); err != nil {
				return Account{}, errors.New("credential deletion failed")
			}
		}
	}
	if err := callHook(service.Hooks.AfterProviderDelete); err != nil {
		return Account{}, err
	}
	account.SecretRef, account.PreviousSecretRef, account.State, account.UpdatedAt = "", "", StateRemoved, service.now().UTC()
	operation.State, operation.UpdatedAt = OperationActive, account.UpdatedAt
	account, _, err = service.store.CompareAndSwapAccountAndOperation(ctx, TransitionRequest{OperationID: operation.ID, ExpectedOperationState: OperationCleanupPending, ExpectedDesiredStateRevision: account.DesiredStateRevision, Account: account, Operation: operation})
	return account, err
}
