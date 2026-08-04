package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/account"
	"github.com/QuantProcessing/boltertrader/gateway/catalog"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/gateway/httpapi"
	"github.com/QuantProcessing/boltertrader/gateway/secret"
	"github.com/QuantProcessing/boltertrader/gateway/store"
)

func TestGatewayAuthorityCreateActivateAndReadRedactsOpaqueReference(t *testing.T) {
	ctx := context.Background()
	lifecycle := newAuthorityLifecycle()
	manager := newAuthorityManager(t, account.RuntimeModeTest, lifecycle)
	desiredStore := newMemoryDesiredStore()
	authority, err := newGatewayAuthority(ctx, desiredStore, manager, account.RuntimeModeTest)
	if err != nil {
		t.Fatalf("newGatewayAuthority: %v", err)
	}

	created, err := authority.Execute(ctx, httpapi.AuthorityRequest{
		Operation: httpapi.AuthorityCreateDesired,
		Desired: &httpapi.DesiredAccount{
			AccountID: "dynamic", Venue: "binance", VenueEnvironment: "demo",
			SecretRef: "opaque-secret-canary", DesiredStateRevision: 1,
			RuntimeGeneration: 1, CredentialVersion: 1, RequestedState: "creating",
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	assertRedactedDesiredResponse(t, created, 1)
	if _, err := manager.Runtime("dynamic"); !errors.Is(err, account.ErrUnknownAccount) {
		t.Fatalf("creating runtime exists: %v", err)
	}

	activated, err := authority.Execute(ctx, httpapi.AuthorityRequest{
		Operation: httpapi.AuthorityUpdateDesired, AccountID: "dynamic", ExpectedRevision: 1,
		Mutation: &httpapi.DesiredMutation{
			SecretRef: "opaque-secret-canary", DesiredStateRevision: 2,
			RuntimeGeneration: 1, CredentialVersion: 1, RequestedState: "active",
		},
	})
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	assertRedactedDesiredResponse(t, activated, 2)
	if runtime, err := manager.Runtime("dynamic"); err != nil || !runtime.Ready {
		t.Fatalf("runtime=%+v err=%v", runtime, err)
	}
	if lifecycle.activations != 1 || lifecycle.lastSecretRef != "opaque-secret-canary" {
		t.Fatalf("lifecycle=%+v", lifecycle)
	}

	read, err := authority.Execute(ctx, httpapi.AuthorityRequest{Operation: httpapi.AuthorityGetDesired, AccountID: "dynamic"})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	assertRedactedDesiredResponse(t, read, 2)
}

func TestGatewayAuthorityRotationAndDrainPreserveFenceSemantics(t *testing.T) {
	ctx := context.Background()
	lifecycle := newAuthorityLifecycle()
	manager := newAuthorityManager(t, account.RuntimeModeTest, lifecycle)
	desiredStore := newMemoryDesiredStore()
	authority, err := newGatewayAuthority(ctx, desiredStore, manager, account.RuntimeModeTest)
	if err != nil {
		t.Fatal(err)
	}
	seedActiveAuthorityAccount(t, ctx, authority)

	_, err = authority.Execute(ctx, httpapi.AuthorityRequest{
		Operation: httpapi.AuthorityRotateDesired, AccountID: "dynamic", ExpectedRevision: 2,
		Mutation: &httpapi.DesiredMutation{
			SecretRef: "opaque-replacement", DesiredStateRevision: 3,
			RuntimeGeneration: 1, CredentialVersion: 1,
			RotationFence: &httpapi.RotationFence{
				RotationFenceID: "rotate-1", State: "armed",
				FromRuntimeGeneration: 1, ToRuntimeGeneration: 2,
				FromCredentialVersion: 1, ToCredentialVersion: 2,
			},
		},
	})
	if err != nil {
		t.Fatalf("arm rotation: %v", err)
	}
	if lifecycle.validations < 2 || lifecycle.lastSecretRef != "opaque-replacement" {
		t.Fatalf("replacement was not validated before persistence: %+v", lifecycle)
	}

	_, err = authority.Execute(ctx, httpapi.AuthorityRequest{
		Operation: httpapi.AuthorityUpdateDesired, AccountID: "dynamic", ExpectedRevision: 3,
		Mutation: &httpapi.DesiredMutation{
			SecretRef: "opaque-replacement", DesiredStateRevision: 4,
			RuntimeGeneration: 2, CredentialVersion: 2, RequestedState: "active",
			RotationFence: &httpapi.RotationFence{
				RotationFenceID: "rotate-1", State: "released",
				FromRuntimeGeneration: 1, ToRuntimeGeneration: 2,
				FromCredentialVersion: 1, ToCredentialVersion: 2,
			},
		},
	})
	if err != nil {
		t.Fatalf("promote rotation: %v", err)
	}
	runtime, err := manager.Runtime("dynamic")
	if err != nil || runtime.Desired.DesiredStateRevision != 4 || runtime.Desired.RuntimeGeneration != 2 {
		t.Fatalf("promoted runtime=%+v err=%v", runtime, err)
	}

	_, err = authority.Execute(ctx, httpapi.AuthorityRequest{
		Operation: httpapi.AuthorityDrainDesired, AccountID: "dynamic", ExpectedRevision: 4,
		Mutation: &httpapi.DesiredMutation{
			DesiredStateRevision: 5, RuntimeGeneration: 2, CredentialVersion: 2, RequestedState: "draining",
		},
	})
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	drained, err := manager.Runtime("dynamic")
	if err != nil || drained.Desired.RuntimeGeneration != 2 || drained.Desired.CredentialVersion != 2 {
		t.Fatalf("drained runtime=%+v err=%v", drained, err)
	}
}

func TestGatewayAuthorityPersistsBeforeActivationSoFailureIsRestartRecoverable(t *testing.T) {
	ctx := context.Background()
	lifecycle := newAuthorityLifecycle()
	manager := newAuthorityManager(t, account.RuntimeModeTest, lifecycle)
	desiredStore := newMemoryDesiredStore()
	authority, err := newGatewayAuthority(ctx, desiredStore, manager, account.RuntimeModeTest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.Execute(ctx, httpapi.AuthorityRequest{Operation: httpapi.AuthorityCreateDesired, Desired: &httpapi.DesiredAccount{
		AccountID: "dynamic", Venue: "binance", VenueEnvironment: "demo", SecretRef: "opaque-initial",
		DesiredStateRevision: 1, RuntimeGeneration: 1, CredentialVersion: 1, RequestedState: "creating",
	}}); err != nil {
		t.Fatal(err)
	}
	lifecycle.activationErr = errors.New("secret-material-canary")
	_, err = authority.Execute(ctx, httpapi.AuthorityRequest{Operation: httpapi.AuthorityUpdateDesired, AccountID: "dynamic", ExpectedRevision: 1, Mutation: &httpapi.DesiredMutation{
		SecretRef: "opaque-initial", DesiredStateRevision: 2, RuntimeGeneration: 1, CredentialVersion: 1, RequestedState: "active",
	}})
	if err == nil || strings.Contains(err.Error(), "secret-material-canary") {
		t.Fatalf("activation error=%v", err)
	}
	persisted, err := desiredStore.LoadDesiredAccount(ctx, "dynamic")
	if err != nil || persisted.RequestedState != store.DesiredActive || persisted.DesiredStateRevision != 2 {
		t.Fatalf("persisted=%+v err=%v", persisted, err)
	}
	if _, err := manager.Runtime("dynamic"); !errors.Is(err, account.ErrUnknownAccount) {
		t.Fatalf("failed activation admitted runtime: %v", err)
	}
}

func TestGatewayAuthorityRestoresActiveAndRotatingAccounts(t *testing.T) {
	ctx := context.Background()
	desiredStore := newMemoryDesiredStore()
	desiredStore.items["active"] = store.DesiredAccount{
		AccountID: "active", Venue: "binance", VenueEnvironment: "demo", SecretRef: "opaque-active",
		DesiredStateRevision: 2, RuntimeGeneration: 1, CredentialVersion: 1, RequestedState: store.DesiredActive,
	}
	desiredStore.items["rotating"] = store.DesiredAccount{
		AccountID: "rotating", Venue: "binance", VenueEnvironment: "demo", SecretRef: "opaque-new", PreviousSecretRef: "opaque-old",
		DesiredStateRevision: 3, RuntimeGeneration: 1, CredentialVersion: 1, RequestedState: store.DesiredRotating,
		RotationFence: &store.RotationFence{ID: "rotate-1", State: store.RotationArmed, FromCredentialVersion: 1, ToCredentialVersion: 2, FromRuntimeGeneration: 1, ToRuntimeGeneration: 2, ArmedAt: time.Unix(1, 0)},
	}
	lifecycle := newAuthorityLifecycle()
	manager := newAuthorityManager(t, account.RuntimeModeTest, lifecycle)
	if _, err := newGatewayAuthority(ctx, desiredStore, manager, account.RuntimeModeTest); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got, err := manager.Runtime("active"); err != nil || !got.Ready {
		t.Fatalf("active=%+v err=%v", got, err)
	}
	rotating, err := manager.Runtime("rotating")
	if err != nil || !rotating.Ready || rotating.Desired.SecretRef.Fingerprint() == "" || rotating.Desired.DesiredStateRevision != 3 {
		t.Fatalf("rotating=%+v err=%v", rotating, err)
	}
	if lifecycle.activations != 2 || lifecycle.lastSecretRef != "opaque-old" {
		t.Fatalf("restore lifecycle=%+v", lifecycle)
	}
}

func TestOpaqueRuntimeLifecycleContainsSecretMaterialAndRedactsFailures(t *testing.T) {
	ctx := context.Background()
	desired := authorityDesiredRuntime(t, "opaque-provider-ref")
	var activatedSecret string
	deactivations := 0
	lifecycle, err := newOpaqueRuntimeLifecycle(
		func(context.Context, secret.OpaqueRef) (string, error) { return "secret-material-canary", nil },
		func(_ context.Context, _ account.DesiredRuntime, material string) error {
			activatedSecret = material
			return nil
		},
		func(context.Context, domain.AccountID) error { deactivations++; return nil },
		func(context.Context, domain.AccountID) (account.RuntimeBlockers, error) {
			return account.RuntimeBlockers{OpenOrders: 1}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Validate(ctx, desired); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if err := lifecycle.Activate(ctx, desired); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if activatedSecret != "secret-material-canary" {
		t.Fatalf("activation did not receive resolved material")
	}
	if blockers, err := lifecycle.Blockers(ctx, desired.AccountID); err != nil || blockers.OpenOrders != 1 {
		t.Fatalf("Blockers=%+v err=%v", blockers, err)
	}
	if err := lifecycle.Deactivate(ctx, desired.AccountID); err != nil || deactivations != 1 {
		t.Fatalf("Deactivate err=%v calls=%d", err, deactivations)
	}

	failing, err := newOpaqueRuntimeLifecycle(
		func(context.Context, secret.OpaqueRef) (string, error) {
			return "", errors.New("secret-material-canary")
		},
		func(context.Context, account.DesiredRuntime, string) error { return nil },
		func(context.Context, domain.AccountID) error { return nil },
		func(context.Context, domain.AccountID) (account.RuntimeBlockers, error) {
			return account.RuntimeBlockers{}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := failing.Validate(ctx, desired); err == nil || strings.Contains(err.Error(), "secret-material-canary") {
		t.Fatalf("validation error leaked secret material: %v", err)
	}
}

func authorityDesiredRuntime(t *testing.T, refValue string) account.DesiredRuntime {
	t.Helper()
	ref, err := secret.ParseOpaqueRef(refValue)
	if err != nil {
		t.Fatal(err)
	}
	return account.DesiredRuntime{
		AccountID: "dynamic", Venue: "binance", VenueEnvironment: "demo", Mode: account.RuntimeModeTest,
		SecretRef: ref, DesiredStateRevision: 1, RuntimeGeneration: 1, CredentialVersion: 1,
		RequestedState: account.RuntimeStateActive,
	}
}

func assertRedactedDesiredResponse(t *testing.T, response httpapi.AuthorityResponse, revision int64) {
	t.Helper()
	desired, ok := response.Body.(httpapi.DesiredAccount)
	if !ok {
		t.Fatalf("body type = %T", response.Body)
	}
	if desired.SecretRef != "redacted" || response.DesiredStateRevision != revision {
		t.Fatalf("response=%+v", response)
	}
}

func seedActiveAuthorityAccount(t *testing.T, ctx context.Context, authority *gatewayAuthority) {
	t.Helper()
	if _, err := authority.Execute(ctx, httpapi.AuthorityRequest{Operation: httpapi.AuthorityCreateDesired, Desired: &httpapi.DesiredAccount{
		AccountID: "dynamic", Venue: "binance", VenueEnvironment: "demo", SecretRef: "opaque-initial",
		DesiredStateRevision: 1, RuntimeGeneration: 1, CredentialVersion: 1, RequestedState: "creating",
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.Execute(ctx, httpapi.AuthorityRequest{Operation: httpapi.AuthorityUpdateDesired, AccountID: "dynamic", ExpectedRevision: 1, Mutation: &httpapi.DesiredMutation{
		SecretRef: "opaque-initial", DesiredStateRevision: 2, RuntimeGeneration: 1, CredentialVersion: 1, RequestedState: "active",
	}}); err != nil {
		t.Fatal(err)
	}
}

type authorityLifecycle struct {
	validations   int
	activations   int
	deactivations int
	lastSecretRef string
	blockers      account.RuntimeBlockers
	activationErr error
}

func newAuthorityLifecycle() *authorityLifecycle { return &authorityLifecycle{} }

func (lifecycle *authorityLifecycle) Validate(_ context.Context, desired account.DesiredRuntime) error {
	lifecycle.validations++
	lifecycle.lastSecretRef = opaqueRefValue(desired.SecretRef)
	return nil
}

func (lifecycle *authorityLifecycle) Activate(_ context.Context, desired account.DesiredRuntime) error {
	lifecycle.activations++
	lifecycle.lastSecretRef = opaqueRefValue(desired.SecretRef)
	return lifecycle.activationErr
}

func (lifecycle *authorityLifecycle) Deactivate(context.Context, domain.AccountID) error {
	lifecycle.deactivations++
	return nil
}

func (lifecycle *authorityLifecycle) Blockers(context.Context, domain.AccountID) (account.RuntimeBlockers, error) {
	return lifecycle.blockers, nil
}

func opaqueRefValue(ref secret.OpaqueRef) string { return ref.ProviderRef().RawForProvider() }

func newAuthorityManager(t *testing.T, mode account.RuntimeMode, lifecycle account.RuntimeLifecycle) *account.Manager {
	t.Helper()
	gatewayCatalog, err := catalog.NewService(time.Minute, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := account.NewManager(context.Background(), nil, account.Options{
		Secrets: secret.StaticProvider{}, ExchangeCatalog: exchange.NewDefaultMemoryCatalog(), GatewayCatalog: gatewayCatalog,
		HTTPClient: &http.Client{}, RuntimeMode: mode, Lifecycle: lifecycle,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	return manager
}

type memoryDesiredStore struct {
	items map[domain.AccountID]store.DesiredAccount
}

func newMemoryDesiredStore() *memoryDesiredStore {
	return &memoryDesiredStore{items: make(map[domain.AccountID]store.DesiredAccount)}
}

func (memory *memoryDesiredStore) CreateDesiredAccount(_ context.Context, desired store.DesiredAccount) (store.DesiredAccount, error) {
	if current, ok := memory.items[desired.AccountID]; ok {
		if current.SecretRef == desired.SecretRef && current.DesiredStateRevision == desired.DesiredStateRevision {
			return current, nil
		}
		return store.DesiredAccount{}, store.ErrDesiredStateRevisionConflict
	}
	memory.items[desired.AccountID] = desired
	return desired, nil
}

func (memory *memoryDesiredStore) ApplyDesiredAccount(_ context.Context, expected int64, desired store.DesiredAccount) (store.DesiredAccount, error) {
	current, ok := memory.items[desired.AccountID]
	if !ok {
		return store.DesiredAccount{}, store.ErrNotFound
	}
	if current.DesiredStateRevision != expected || desired.DesiredStateRevision != expected+1 {
		return store.DesiredAccount{}, store.ErrDesiredStateRevisionConflict
	}
	if err := store.ValidateDesiredAccountTransition(current.RequestedState, desired.RequestedState); err != nil {
		return store.DesiredAccount{}, err
	}
	if current.RequestedState == store.DesiredRotating && desired.RequestedState == store.DesiredActive {
		desired.PreviousSecretRef = ""
	} else if desired.RequestedState == store.DesiredRotating {
		desired.PreviousSecretRef = current.SecretRef
	}
	memory.items[desired.AccountID] = desired
	return desired, nil
}

func (memory *memoryDesiredStore) LoadDesiredAccount(_ context.Context, accountID domain.AccountID) (store.DesiredAccount, error) {
	desired, ok := memory.items[accountID]
	if !ok {
		return store.DesiredAccount{}, store.ErrNotFound
	}
	return desired, nil
}

func (memory *memoryDesiredStore) ListDesiredAccounts(context.Context) ([]store.DesiredAccount, error) {
	result := make([]store.DesiredAccount, 0, len(memory.items))
	for _, desired := range memory.items {
		result = append(result, desired)
	}
	return result, nil
}

func (memory *memoryDesiredStore) ListRecoverableDesiredAccounts(ctx context.Context) ([]store.DesiredAccount, error) {
	return memory.ListDesiredAccounts(ctx)
}

func (memory *memoryDesiredStore) DeleteDesiredAccount(_ context.Context, accountID domain.AccountID, expected int64) error {
	current, ok := memory.items[accountID]
	if !ok {
		return store.ErrNotFound
	}
	if current.DesiredStateRevision != expected {
		return store.ErrDesiredStateRevisionConflict
	}
	delete(memory.items, accountID)
	return nil
}

func (memory *memoryDesiredStore) ValidateRuntimeFence(_ context.Context, fence store.RuntimeFence) error {
	current, ok := memory.items[fence.AccountID]
	if !ok || current.DesiredStateRevision != fence.DesiredStateRevision || current.RuntimeGeneration != fence.RuntimeGeneration || current.CredentialVersion != fence.CredentialVersion {
		return store.ErrStaleRuntimeGeneration
	}
	return nil
}
