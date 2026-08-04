package main

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/gateway/account"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/gateway/httpapi"
	"github.com/QuantProcessing/boltertrader/gateway/secret"
	"github.com/QuantProcessing/boltertrader/gateway/store"
)

type gatewayAuthority struct {
	mu      sync.Mutex
	store   store.DesiredStateStore
	manager *account.Manager
	mode    account.RuntimeMode
	now     func() time.Time
}

type opaqueRuntimeLifecycle struct {
	resolve    func(context.Context, secret.OpaqueRef) (string, error)
	activate   func(context.Context, account.DesiredRuntime, string) error
	deactivate func(context.Context, domain.AccountID) error
	blockers   func(context.Context, domain.AccountID) (account.RuntimeBlockers, error)
}

func newOpaqueRuntimeLifecycle(
	resolve func(context.Context, secret.OpaqueRef) (string, error),
	activate func(context.Context, account.DesiredRuntime, string) error,
	deactivate func(context.Context, domain.AccountID) error,
	blockers func(context.Context, domain.AccountID) (account.RuntimeBlockers, error),
) (*opaqueRuntimeLifecycle, error) {
	if resolve == nil || activate == nil || deactivate == nil || blockers == nil {
		return nil, errors.New("runtime lifecycle dependencies are incomplete")
	}
	return &opaqueRuntimeLifecycle{resolve: resolve, activate: activate, deactivate: deactivate, blockers: blockers}, nil
}

func (lifecycle *opaqueRuntimeLifecycle) Validate(ctx context.Context, desired account.DesiredRuntime) error {
	material, err := lifecycle.resolve(ctx, desired.SecretRef)
	if err != nil || material == "" {
		return errors.New("runtime secret resolution failed")
	}
	material = ""
	return nil
}

func (lifecycle *opaqueRuntimeLifecycle) Activate(ctx context.Context, desired account.DesiredRuntime) error {
	material, err := lifecycle.resolve(ctx, desired.SecretRef)
	if err != nil || material == "" {
		return errors.New("runtime secret resolution failed")
	}
	defer func() { material = "" }()
	if err := lifecycle.activate(ctx, desired, material); err != nil {
		return errors.New("runtime activation failed")
	}
	return nil
}

func (lifecycle *opaqueRuntimeLifecycle) Deactivate(ctx context.Context, accountID domain.AccountID) error {
	if err := lifecycle.deactivate(ctx, accountID); err != nil {
		return errors.New("runtime deactivation failed")
	}
	return nil
}

func (lifecycle *opaqueRuntimeLifecycle) Blockers(ctx context.Context, accountID domain.AccountID) (account.RuntimeBlockers, error) {
	blockers, err := lifecycle.blockers(ctx, accountID)
	if err != nil {
		return account.RuntimeBlockers{}, errors.New("runtime blocker lookup failed")
	}
	return blockers, nil
}

func newGatewayAuthority(ctx context.Context, desiredStore store.DesiredStateStore, manager *account.Manager, mode account.RuntimeMode) (*gatewayAuthority, error) {
	if ctx == nil || desiredStore == nil || manager == nil {
		return nil, errors.New("gateway authority dependencies are incomplete")
	}
	if mode == "" {
		mode = account.RuntimeModeTest
	}
	authority := &gatewayAuthority{store: desiredStore, manager: manager, mode: mode, now: time.Now}
	if err := authority.restore(ctx); err != nil {
		return nil, errors.New("restore gateway authority failed")
	}
	return authority, nil
}

func (authority *gatewayAuthority) Execute(ctx context.Context, request httpapi.AuthorityRequest) (httpapi.AuthorityResponse, error) {
	if ctx == nil {
		return httpapi.AuthorityResponse{}, authorityError(errors.New("context is required"))
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()

	var response httpapi.AuthorityResponse
	var err error
	switch request.Operation {
	case httpapi.AuthorityCreateDesired:
		response, err = authority.create(ctx, request.Desired)
	case httpapi.AuthorityListDesired:
		response, err = authority.listDesired(ctx)
	case httpapi.AuthorityGetDesired:
		response, err = authority.getDesired(ctx, request.AccountID)
	case httpapi.AuthorityValidateDesired:
		response, err = authority.validate(ctx, request)
	case httpapi.AuthorityRotateDesired:
		response, err = authority.rotate(ctx, request)
	case httpapi.AuthorityUpdateDesired:
		response, err = authority.update(ctx, request)
	case httpapi.AuthorityDisableDesired, httpapi.AuthorityDrainDesired:
		response, err = authority.transition(ctx, request)
	case httpapi.AuthorityDeleteDesired:
		err = authority.remove(ctx, request)
	case httpapi.AuthorityListRuntimeAccounts:
		response.Body = authority.manager.Runtimes()
	case httpapi.AuthorityGetRuntimeAccount:
		response.Body, err = authority.manager.Runtime(domain.AccountID(request.AccountID))
	case httpapi.AuthorityRuntimeStatus, httpapi.AuthorityRuntimeReadiness:
		response.Body = map[string]any{"mode": authority.mode, "accounts": authority.manager.Runtimes()}
	case httpapi.AuthorityRuntimeOpenOrders, httpapi.AuthorityRuntimePositions,
		httpapi.AuthorityRuntimeUncertain, httpapi.AuthorityRuntimeEvents,
		httpapi.AuthorityCatalogVenues, httpapi.AuthorityCatalogInstruments,
		httpapi.AuthorityMarketData, httpapi.AuthorityPrivateEvents:
		response.Body = []any{}
	default:
		err = errors.New("unsupported authority operation")
	}
	if err != nil {
		return httpapi.AuthorityResponse{}, authorityError(err)
	}
	return response, nil
}

func (authority *gatewayAuthority) create(ctx context.Context, input *httpapi.DesiredAccount) (httpapi.AuthorityResponse, error) {
	if input == nil {
		return httpapi.AuthorityResponse{}, errors.New("desired account is required")
	}
	desired, err := desiredFromHTTP(*input, authority.now())
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	created, err := authority.store.CreateDesiredAccount(ctx, desired)
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	return desiredResponse(created), nil
}

func (authority *gatewayAuthority) listDesired(ctx context.Context) (httpapi.AuthorityResponse, error) {
	items, err := authority.store.ListDesiredAccounts(ctx)
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].AccountID < items[j].AccountID })
	result := make([]httpapi.DesiredAccount, len(items))
	for index := range items {
		result[index] = desiredToHTTP(items[index])
	}
	return httpapi.AuthorityResponse{Body: result}, nil
}

func (authority *gatewayAuthority) getDesired(ctx context.Context, accountID string) (httpapi.AuthorityResponse, error) {
	desired, err := authority.store.LoadDesiredAccount(ctx, domain.AccountID(accountID))
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	return desiredResponse(desired), nil
}

func (authority *gatewayAuthority) validate(ctx context.Context, request httpapi.AuthorityRequest) (httpapi.AuthorityResponse, error) {
	current, err := authority.loadExpected(ctx, request.AccountID, request.ExpectedRevision)
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	if request.Mutation == nil || request.Mutation.DesiredStateRevision != current.DesiredStateRevision ||
		request.Mutation.RuntimeGeneration != current.RuntimeGeneration || request.Mutation.CredentialVersion != current.CredentialVersion {
		return httpapi.AuthorityResponse{}, store.ErrDesiredStateRevisionConflict
	}
	desired, err := authority.runtimeDesired(current, current.SecretRef, account.RuntimeStateActive)
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	if err := authority.manager.ValidateRuntime(ctx, desired); err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	return httpapi.AuthorityResponse{Body: map[string]any{"ready": true}, DesiredStateRevision: current.DesiredStateRevision}, nil
}

func (authority *gatewayAuthority) rotate(ctx context.Context, request httpapi.AuthorityRequest) (httpapi.AuthorityResponse, error) {
	current, err := authority.loadExpected(ctx, request.AccountID, request.ExpectedRevision)
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	if request.Mutation == nil || request.Mutation.RotationFence == nil {
		return httpapi.AuthorityResponse{}, errors.New("rotation mutation is incomplete")
	}
	next, err := desiredFromMutation(current, *request.Mutation, store.DesiredRotating, authority.now())
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	fence := next.RotationFence
	candidate := next
	candidate.RuntimeGeneration = fence.ToRuntimeGeneration
	candidate.CredentialVersion = fence.ToCredentialVersion
	candidate.RequestedState = store.DesiredActive
	desired, err := authority.runtimeDesired(candidate, next.SecretRef, account.RuntimeStateActive)
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	if err := authority.manager.ValidateRuntime(ctx, desired); err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	stored, err := authority.store.ApplyDesiredAccount(ctx, request.ExpectedRevision, next)
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	return desiredResponse(stored), nil
}

func (authority *gatewayAuthority) update(ctx context.Context, request httpapi.AuthorityRequest) (httpapi.AuthorityResponse, error) {
	current, err := authority.loadExpected(ctx, request.AccountID, request.ExpectedRevision)
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	if request.Mutation == nil {
		return httpapi.AuthorityResponse{}, errors.New("desired mutation is required")
	}
	next, err := desiredFromMutation(current, *request.Mutation, store.DesiredAccountState(request.Mutation.RequestedState), authority.now())
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	stored, err := authority.store.ApplyDesiredAccount(ctx, request.ExpectedRevision, next)
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}

	switch {
	case current.RequestedState == store.DesiredCreating && next.RequestedState == store.DesiredActive:
		runtimeDesired, convertErr := authority.runtimeDesired(next, next.SecretRef, account.RuntimeStateActive)
		if convertErr != nil {
			return httpapi.AuthorityResponse{}, convertErr
		}
		if _, err := authority.manager.CreateRuntime(ctx, runtimeDesired); err != nil {
			return httpapi.AuthorityResponse{}, err
		}
	case current.RequestedState == store.DesiredRotating && next.RequestedState == store.DesiredActive:
		runtimeDesired, convertErr := authority.runtimeDesired(next, next.SecretRef, account.RuntimeStateActive)
		if convertErr != nil {
			return httpapi.AuthorityResponse{}, convertErr
		}
		if _, err := authority.manager.RotateRuntime(ctx, runtimeDesired, uint64(request.ExpectedRevision)); err != nil {
			return httpapi.AuthorityResponse{}, err
		}
	}
	return desiredResponse(stored), nil
}

func (authority *gatewayAuthority) transition(ctx context.Context, request httpapi.AuthorityRequest) (httpapi.AuthorityResponse, error) {
	current, err := authority.loadExpected(ctx, request.AccountID, request.ExpectedRevision)
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	if request.Mutation == nil {
		return httpapi.AuthorityResponse{}, errors.New("desired mutation is required")
	}
	nextState := store.DesiredDisabled
	runtimeState := account.RuntimeStateDisabled
	if request.Operation == httpapi.AuthorityDrainDesired {
		nextState = store.DesiredDraining
		runtimeState = account.RuntimeStateDraining
	}
	next, err := desiredFromMutation(current, *request.Mutation, nextState, authority.now())
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	runtime, err := authority.manager.Runtime(current.AccountID)
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	if runtimeState == account.RuntimeStateDraining {
		_, err = authority.manager.DrainRuntime(ctx, current.AccountID, runtime.Desired.DesiredStateRevision, uint64(next.DesiredStateRevision))
	} else {
		_, err = authority.manager.DisableRuntime(ctx, current.AccountID, runtime.Desired.DesiredStateRevision, uint64(next.DesiredStateRevision))
	}
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	stored, err := authority.store.ApplyDesiredAccount(ctx, request.ExpectedRevision, next)
	if err != nil {
		return httpapi.AuthorityResponse{}, err
	}
	return desiredResponse(stored), nil
}

func (authority *gatewayAuthority) remove(ctx context.Context, request httpapi.AuthorityRequest) error {
	current, err := authority.loadExpected(ctx, request.AccountID, request.ExpectedRevision)
	if err != nil {
		return err
	}
	if current.RequestedState != store.DesiredRemoved {
		return store.ErrInvalidAccountTransition
	}
	if runtime, runtimeErr := authority.manager.Runtime(current.AccountID); runtimeErr == nil {
		if err := authority.manager.RemoveRuntime(ctx, current.AccountID, runtime.Desired.DesiredStateRevision); err != nil {
			return err
		}
	} else if !errors.Is(runtimeErr, account.ErrUnknownAccount) {
		return runtimeErr
	}
	return authority.store.DeleteDesiredAccount(ctx, current.AccountID, request.ExpectedRevision)
}

func (authority *gatewayAuthority) loadExpected(ctx context.Context, accountID string, revision int64) (store.DesiredAccount, error) {
	desired, err := authority.store.LoadDesiredAccount(ctx, domain.AccountID(accountID))
	if err != nil {
		return store.DesiredAccount{}, err
	}
	if revision <= 0 || desired.DesiredStateRevision != revision {
		return store.DesiredAccount{}, store.ErrDesiredStateRevisionConflict
	}
	return desired, nil
}

func (authority *gatewayAuthority) restore(ctx context.Context) error {
	items, err := authority.store.ListRecoverableDesiredAccounts(ctx)
	if err != nil {
		return err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].AccountID < items[j].AccountID })
	runtimes := make([]account.DesiredRuntime, 0, len(items))
	for _, desired := range items {
		if desired.RequestedState == store.DesiredCreating {
			continue
		}
		secretRef := desired.SecretRef
		state := account.RuntimeState(desired.RequestedState)
		if desired.RequestedState == store.DesiredRotating {
			secretRef = desired.PreviousSecretRef
			state = account.RuntimeStateActive
		}
		runtimeDesired, convertErr := authority.runtimeDesired(desired, secretRef, state)
		if convertErr != nil {
			return convertErr
		}
		runtimes = append(runtimes, runtimeDesired)
	}
	return authority.manager.RestoreRuntimes(ctx, runtimes)
}

func (authority *gatewayAuthority) runtimeDesired(desired store.DesiredAccount, refValue string, state account.RuntimeState) (account.DesiredRuntime, error) {
	ref, err := secret.ParseOpaqueRef(refValue)
	if err != nil {
		return account.DesiredRuntime{}, err
	}
	rotationFence := ""
	if desired.RotationFence != nil {
		rotationFence = desired.RotationFence.ID
	}
	return account.DesiredRuntime{
		AccountID: desired.AccountID, Venue: desired.Venue, VenueEnvironment: desired.VenueEnvironment,
		Mode: authority.mode, SecretRef: ref, DesiredStateRevision: uint64(desired.DesiredStateRevision),
		RuntimeGeneration: uint64(desired.RuntimeGeneration), CredentialVersion: uint64(desired.CredentialVersion),
		RequestedState: state, RotationFence: rotationFence,
	}, nil
}

func desiredFromHTTP(input httpapi.DesiredAccount, now time.Time) (store.DesiredAccount, error) {
	desired := store.DesiredAccount{
		AccountID: domain.AccountID(input.AccountID), Venue: input.Venue, VenueEnvironment: input.VenueEnvironment,
		SecretRef: input.SecretRef, DesiredStateRevision: input.DesiredStateRevision,
		RuntimeGeneration: input.RuntimeGeneration, CredentialVersion: input.CredentialVersion,
		RequestedState: store.DesiredAccountState(input.RequestedState), UpdatedAt: now.UTC(),
	}
	if input.RotationFence != nil {
		desired.RotationFence = rotationFenceFromHTTP(*input.RotationFence, now)
	}
	if err := desired.Validate(); err != nil {
		return store.DesiredAccount{}, err
	}
	return desired, nil
}

func desiredFromMutation(current store.DesiredAccount, mutation httpapi.DesiredMutation, state store.DesiredAccountState, now time.Time) (store.DesiredAccount, error) {
	next := current
	if mutation.SecretRef != "" {
		next.SecretRef = mutation.SecretRef
	}
	next.DesiredStateRevision = mutation.DesiredStateRevision
	next.RuntimeGeneration = mutation.RuntimeGeneration
	next.CredentialVersion = mutation.CredentialVersion
	next.RequestedState = state
	next.UpdatedAt = now.UTC()
	if mutation.RotationFence != nil {
		next.RotationFence = rotationFenceFromHTTP(*mutation.RotationFence, now)
	} else {
		next.RotationFence = nil
	}
	if err := next.Validate(); err != nil {
		return store.DesiredAccount{}, err
	}
	return next, nil
}

func rotationFenceFromHTTP(input httpapi.RotationFence, now time.Time) *store.RotationFence {
	fence := &store.RotationFence{
		ID: input.RotationFenceID, State: store.RotationFenceState(input.State),
		FromCredentialVersion: input.FromCredentialVersion, ToCredentialVersion: input.ToCredentialVersion,
		FromRuntimeGeneration: input.FromRuntimeGeneration, ToRuntimeGeneration: input.ToRuntimeGeneration,
		ArmedAt: now.UTC(),
	}
	if fence.State == store.RotationReleased {
		released := now.UTC()
		fence.ReleasedAt = &released
	}
	return fence
}

func desiredResponse(desired store.DesiredAccount) httpapi.AuthorityResponse {
	return httpapi.AuthorityResponse{Body: desiredToHTTP(desired), DesiredStateRevision: desired.DesiredStateRevision}
}

func desiredToHTTP(desired store.DesiredAccount) httpapi.DesiredAccount {
	result := httpapi.DesiredAccount{
		AccountID: string(desired.AccountID), Venue: desired.Venue, VenueEnvironment: desired.VenueEnvironment,
		SecretRef: "redacted", DesiredStateRevision: desired.DesiredStateRevision,
		RuntimeGeneration: desired.RuntimeGeneration, CredentialVersion: desired.CredentialVersion,
		RequestedState: string(desired.RequestedState),
	}
	if desired.RotationFence != nil {
		result.RotationFence = &httpapi.RotationFence{
			RotationFenceID: desired.RotationFence.ID, State: string(desired.RotationFence.State),
			FromCredentialVersion: desired.RotationFence.FromCredentialVersion,
			ToCredentialVersion:   desired.RotationFence.ToCredentialVersion,
			FromRuntimeGeneration: desired.RotationFence.FromRuntimeGeneration,
			ToRuntimeGeneration:   desired.RotationFence.ToRuntimeGeneration,
		}
	}
	return result
}

func authorityError(err error) error {
	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, account.ErrUnknownAccount):
		return httpapi.AuthorityError{Status: http.StatusNotFound, Code: "DESIRED_ACCOUNT_NOT_FOUND"}
	case errors.Is(err, store.ErrDesiredStateRevisionConflict), errors.Is(err, account.ErrRuntimeRevision):
		return httpapi.AuthorityError{Status: http.StatusPreconditionFailed, Code: "DESIRED_STATE_REVISION_CONFLICT", Retryable: true}
	case errors.Is(err, store.ErrInvalidAccountTransition):
		return httpapi.AuthorityError{Status: http.StatusConflict, Code: "INVALID_ACCOUNT_TRANSITION"}
	case errors.Is(err, store.ErrStaleRuntimeGeneration), errors.Is(err, account.ErrStaleWriteFence):
		return httpapi.AuthorityError{Status: http.StatusConflict, Code: "STALE_RUNTIME_FENCE", Retryable: true}
	case errors.Is(err, account.ErrRuntimeBlocked):
		return httpapi.AuthorityError{Status: http.StatusConflict, Code: "ACCOUNT_RUNTIME_BLOCKED"}
	case errors.Is(err, account.ErrRuntimeModeMismatch):
		return httpapi.AuthorityError{Status: http.StatusConflict, Code: "RUNTIME_MODE_MISMATCH"}
	case errors.Is(err, account.ErrLiveWriteDisabled):
		return httpapi.AuthorityError{Status: http.StatusForbidden, Code: "LIVE_PRIVATE_WRITE_DISABLED"}
	default:
		return httpapi.AuthorityError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Retryable: true}
	}
}
