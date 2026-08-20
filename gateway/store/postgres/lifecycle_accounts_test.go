package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/gateway/account/lifecycle"
	"github.com/QuantProcessing/boltertrader/secretstore"
)

const (
	lifecycleCreateOperationID = "018f0000-0000-7000-8000-0000000000a1"
	lifecycleRotateOperationID = "018f0000-0000-7000-8000-0000000000a2"
	lifecycleRetireOperationID = "018f0000-0000-7000-8000-0000000000a3"
)

func newLifecycleTestStore(t *testing.T) (*AccountLifecycleStore, context.Context) {
	t.Helper()
	ctx := context.Background()
	pool := newTestPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return NewAccountLifecycleStore(pool), ctx
}

func lifecycleTestAccount(t *testing.T) lifecycle.Account {
	t.Helper()
	return lifecycle.Account{
		ID:               string(testAccountID(t)),
		Venue:            "binance",
		VenueEnvironment: lifecycle.EnvironmentLive,
		DisplayName:      "Binance Primary",
		ProductIDs:       []string{"manual"},
		Routes: []lifecycle.AccountRoute{
			{ID: "route-spot", Product: lifecycle.ProductSpot, RouteProfileID: "binance.spot.v1", RequestedState: lifecycle.RouteActive},
			{ID: "route-perp", Product: lifecycle.ProductPerp, SettlementAsset: "USDT", RouteProfileID: "binance.perp.usdt.v1", RequestedState: lifecycle.RouteDisabled},
		},
		DesiredStateRevision: 1,
		RuntimeGeneration:    1,
		CredentialVersion:    1,
		State:                lifecycle.StateCreating,
		UpdatedAt:            time.Unix(1710000000, 0).UTC(),
	}
}

func lifecycleTestOperation(account lifecycle.Account, id string, kind lifecycle.OperationKind, state lifecycle.OperationState) lifecycle.CredentialOperation {
	created := time.Unix(1710000000, 0).UTC()
	return lifecycle.CredentialOperation{
		ID:                         secretstore.OperationID(id),
		PrincipalID:                "principal-a",
		AccountID:                  account.ID,
		Kind:                       kind,
		State:                      state,
		IdempotencyKeyDigest:       "digest-" + id,
		RequestDigest:              "request-" + id,
		TargetDesiredStateRevision: account.DesiredStateRevision,
		TargetRuntimeGeneration:    account.RuntimeGeneration,
		TargetCredentialVersion:    account.CredentialVersion,
		CreatedAt:                  created,
		UpdatedAt:                  created,
	}
}

// The draft row is the one shape the schema is least forgiving about: it has no
// secret reference yet, so it only survives while the create operation is in
// flight.
func TestLifecycleStoreRoundTripsDraftAccountsAndRoutes(t *testing.T) {
	store, ctx := newLifecycleTestStore(t)
	account := lifecycleTestAccount(t)
	operation := lifecycleTestOperation(account, lifecycleCreateOperationID, lifecycle.OperationCreate, lifecycle.OperationAwaitingMaterial)

	if err := store.CreateDraft(ctx, account, operation); err != nil {
		t.Fatalf("CreateDraft: %v", err)
	}

	loaded, err := store.Get(ctx, account.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if loaded.DisplayName != account.DisplayName ||
		loaded.VenueEnvironment != account.VenueEnvironment || loaded.NativeEnvironment != account.VenueEnvironment {
		t.Fatalf("identity round trip = %+v", loaded)
	}
	if len(loaded.ProductIDs) != 1 || loaded.ProductIDs[0] != "manual" || len(loaded.ProductPolicy) != 1 {
		t.Fatalf("product round trip = %+v / %+v", loaded.ProductIDs, loaded.ProductPolicy)
	}
	if len(loaded.Routes) != 2 || loaded.Routes[0].ID != "route-perp" || loaded.Routes[0].SettlementAsset != "USDT" ||
		loaded.Routes[1].ID != "route-spot" || loaded.Routes[1].RequestedState != lifecycle.RouteActive {
		t.Fatalf("route round trip = %+v", loaded.Routes)
	}
	if loaded.SecretRef != "" || loaded.Rotation != nil || loaded.LastValidatedAt != nil {
		t.Fatalf("draft carried unexpected state = %+v", loaded)
	}

	listed, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed) != 1 || len(listed[0].Routes) != 2 {
		t.Fatalf("List = %+v", listed)
	}

	if _, err := store.Get(ctx, account.ID+"-missing"); !errors.Is(err, lifecycle.ErrNotFound) {
		t.Fatalf("Get missing = %v", err)
	}
}

func TestLifecycleStoreBindsCredentialReferenceIdempotently(t *testing.T) {
	store, ctx := newLifecycleTestStore(t)
	account := lifecycleTestAccount(t)
	operationID := secretstore.OperationID(lifecycleCreateOperationID)
	if err := store.CreateDraft(ctx, account, lifecycleTestOperation(account, lifecycleCreateOperationID, lifecycle.OperationCreate, lifecycle.OperationAwaitingMaterial)); err != nil {
		t.Fatalf("CreateDraft: %v", err)
	}

	bound, operation, err := store.BindCredentialReference(ctx, operationID, "secret-ref-1")
	if err != nil {
		t.Fatalf("BindCredentialReference: %v", err)
	}
	if bound.SecretRef != "secret-ref-1" || operation.State != lifecycle.OperationStored || len(bound.Routes) != 2 {
		t.Fatalf("bind result = %+v / %+v", bound, operation)
	}

	// A crash between the secret write and the reply must replay to the same
	// reference rather than fail the operation.
	replayed, replayedOperation, err := store.BindCredentialReference(ctx, operationID, "secret-ref-1")
	if err != nil {
		t.Fatalf("BindCredentialReference replay: %v", err)
	}
	if replayed.SecretRef != "secret-ref-1" || replayedOperation.State != lifecycle.OperationStored {
		t.Fatalf("replay result = %+v / %+v", replayed, replayedOperation)
	}

	if _, _, err := store.BindCredentialReference(ctx, operationID, "secret-ref-2"); !errors.Is(err, lifecycle.ErrInvalidTransition) {
		t.Fatalf("conflicting bind = %v", err)
	}
}

func TestLifecycleStoreSwapsAccountAndOperationUnderOneFence(t *testing.T) {
	store, ctx := newLifecycleTestStore(t)
	account := lifecycleTestAccount(t)
	if err := store.CreateDraft(ctx, account, lifecycleTestOperation(account, lifecycleCreateOperationID, lifecycle.OperationCreate, lifecycle.OperationAwaitingMaterial)); err != nil {
		t.Fatalf("CreateDraft: %v", err)
	}
	if _, _, err := store.BindCredentialReference(ctx, secretstore.OperationID(lifecycleCreateOperationID), "secret-ref-1"); err != nil {
		t.Fatalf("BindCredentialReference: %v", err)
	}

	activated := account
	activated.SecretRef = "secret-ref-1"
	activated.State = lifecycle.StateActive
	validated := time.Unix(1710000100, 0).UTC()
	activated.LastValidatedAt = &validated
	activated.UpdatedAt = validated

	operation := lifecycleTestOperation(activated, lifecycleCreateOperationID, lifecycle.OperationCreate, lifecycle.OperationActive)
	operation.SecretRef = "secret-ref-1"
	operation.UpdatedAt = validated

	request := lifecycle.TransitionRequest{
		OperationID:                  secretstore.OperationID(lifecycleCreateOperationID),
		ExpectedOperationState:       lifecycle.OperationStored,
		ExpectedDesiredStateRevision: 1,
		Account:                      activated,
		Operation:                    operation,
	}
	if _, _, err := store.CompareAndSwapAccountAndOperation(ctx, request); err != nil {
		t.Fatalf("CompareAndSwapAccountAndOperation: %v", err)
	}

	// Replaying the same transition must lose: the operation has moved on.
	if _, _, err := store.CompareAndSwapAccountAndOperation(ctx, request); !errors.Is(err, lifecycle.ErrRevisionConflict) {
		t.Fatalf("stale transition = %v", err)
	}

	loaded, err := store.Get(ctx, account.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if loaded.State != lifecycle.StateActive || loaded.SecretRef != "secret-ref-1" ||
		loaded.LastValidatedAt == nil || !loaded.LastValidatedAt.Equal(validated) {
		t.Fatalf("activated account = %+v", loaded)
	}

	if err := store.CompareAndSwap(ctx, account.ID, 99, loaded); !errors.Is(err, lifecycle.ErrRevisionConflict) {
		t.Fatalf("stale CompareAndSwap = %v", err)
	}
}

func TestLifecycleStoreCommitsCredentialFreeRemovedTerminalState(t *testing.T) {
	store, ctx := newLifecycleTestStore(t)
	account := lifecycleTestAccount(t)
	account.SecretRef = "secret-ref-1"
	account.State = lifecycle.StateRemoving
	account.DesiredStateRevision = 2
	operation := lifecycleTestOperation(account, lifecycleRetireOperationID, lifecycle.OperationRetire, lifecycle.OperationCleanupPending)
	if err := store.CreateDraft(ctx, account, operation); err != nil {
		t.Fatalf("CreateDraft: %v", err)
	}

	removed := account
	removed.SecretRef = ""
	removed.PreviousSecretRef = ""
	removed.State = lifecycle.StateRemoved
	removed.UpdatedAt = time.Unix(1710000200, 0).UTC()
	completed := operation
	completed.State = lifecycle.OperationActive
	completed.UpdatedAt = removed.UpdatedAt
	request := lifecycle.TransitionRequest{
		OperationID:                  operation.ID,
		ExpectedOperationState:       lifecycle.OperationCleanupPending,
		ExpectedDesiredStateRevision: removed.DesiredStateRevision,
		Account:                      removed,
		Operation:                    completed,
	}
	settled, settledOperation, err := store.CompareAndSwapAccountAndOperation(ctx, request)
	if err != nil {
		t.Fatalf("CompareAndSwapAccountAndOperation: %v", err)
	}
	if settled.State != lifecycle.StateRemoved || settled.SecretRef != "" || settled.PreviousSecretRef != "" ||
		settledOperation.State != lifecycle.OperationActive {
		t.Fatalf("terminal cleanup = %+v / %+v", settled, settledOperation)
	}
}

func TestLifecycleStoreAdmitsOneRecoverableOperationPerAccount(t *testing.T) {
	store, ctx := newLifecycleTestStore(t)
	account := lifecycleTestAccount(t)
	if err := store.CreateDraft(ctx, account, lifecycleTestOperation(account, lifecycleCreateOperationID, lifecycle.OperationCreate, lifecycle.OperationAwaitingMaterial)); err != nil {
		t.Fatalf("CreateDraft: %v", err)
	}

	rotating := account
	rotating.SecretRef = "secret-ref-1"
	rotating.State = lifecycle.StateRotating
	rotating.DesiredStateRevision = 2
	rotating.RuntimeGeneration = 2
	rotating.CredentialVersion = 2
	rotating.Rotation = &lifecycle.RotationFence{
		ID: "rotation-1", State: "armed",
		FromCredentialVersion: 1, ToCredentialVersion: 2,
		FromRuntimeGeneration: 1, ToRuntimeGeneration: 2,
		ArmedAt: time.Unix(1710000200, 0).UTC(),
	}

	// The create operation is still in flight, so a rotation may not claim the
	// same fences underneath it.
	second := lifecycleTestOperation(rotating, lifecycleRotateOperationID, lifecycle.OperationRotate, lifecycle.OperationAwaitingMaterial)
	if err := store.BeginCredentialOperation(ctx, 1, rotating, second); !errors.Is(err, lifecycle.ErrRevisionConflict) {
		t.Fatalf("concurrent credential operation = %v", err)
	}

	// The rejected claim must not have advanced the account either.
	loaded, err := store.Get(ctx, account.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if loaded.DesiredStateRevision != 1 || loaded.State != lifecycle.StateCreating {
		t.Fatalf("rejected claim leaked = %+v", loaded)
	}

	recoverable, err := store.ListRecoverableCredentialOperations(ctx, 10)
	if err != nil {
		t.Fatalf("ListRecoverableCredentialOperations: %v", err)
	}
	if len(recoverable) != 1 || recoverable[0].ID != secretstore.OperationID(lifecycleCreateOperationID) {
		t.Fatalf("recoverable = %+v", recoverable)
	}
	if empty, err := store.ListRecoverableCredentialOperations(ctx, 0); err != nil || empty != nil {
		t.Fatalf("zero limit = %+v / %v", empty, err)
	}
}

func TestLifecycleStoreRoundTripsRotationFenceAndCleanup(t *testing.T) {
	store, ctx := newLifecycleTestStore(t)
	account := lifecycleTestAccount(t)
	account.SecretRef = "secret-ref-2"
	account.PreviousSecretRef = "secret-ref-1"
	account.State = lifecycle.StateActive
	account.DesiredStateRevision = 2
	account.RuntimeGeneration = 2
	account.CredentialVersion = 2
	armedAt := time.Unix(1710000300, 0).UTC()
	account.Rotation = &lifecycle.RotationFence{
		ID: "rotation-1", State: "armed",
		FromCredentialVersion: 1, ToCredentialVersion: 2,
		FromRuntimeGeneration: 1, ToRuntimeGeneration: 2,
		ArmedAt: armedAt,
	}

	operation := lifecycleTestOperation(account, lifecycleRotateOperationID, lifecycle.OperationRotate, lifecycle.OperationCleanupPending)
	operation.SecretRef = account.SecretRef
	if err := store.CreateDraft(ctx, account, operation); err != nil {
		t.Fatalf("CreateDraft: %v", err)
	}

	loaded, err := store.Get(ctx, account.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if loaded.Rotation == nil || *loaded.Rotation != *account.Rotation || loaded.PreviousSecretRef != "secret-ref-1" {
		t.Fatalf("rotation round trip = %+v", loaded.Rotation)
	}

	fence := func(revision int64) lifecycle.AccountFence {
		return lifecycle.AccountFence{AccountID: account.ID, DesiredStateRevision: revision, RuntimeGeneration: revision, CredentialVersion: revision}
	}
	completedAt := time.Unix(1710000400, 0).UTC()
	proof := lifecycle.RotationCleanupProof{
		OperationID: operation.ID, AccountID: account.ID,
		ActiveFence: fence(2), RetiringFence: fence(1),
		TombstoneRevision: 2, RetiringState: "retired", SecretReferenceState: "tombstoned",
		CompletedAt: completedAt,
	}
	recorded, err := store.RecordRotationCleanupProof(ctx, proof)
	if err != nil {
		t.Fatalf("RecordRotationCleanupProof: %v", err)
	}
	if recorded.RotationCleanupTombstoneRevision != 2 || !recorded.RotationCleanupCompletedAt.Equal(completedAt) {
		t.Fatalf("proof = %+v", recorded)
	}
	replayed, err := store.RecordRotationCleanupProof(ctx, proof)
	if err != nil || replayed.RotationCleanupTombstoneRevision != 2 {
		t.Fatalf("proof replay = %+v / %v", replayed, err)
	}

	completion := lifecycle.RotationCleanupCompletion{
		OperationID: operation.ID, AccountID: account.ID,
		PreviousSecretRef: "secret-ref-1", CompletedAt: completedAt,
	}
	settled, settledOperation, err := store.CompleteRotationCleanup(ctx, completion)
	if err != nil {
		t.Fatalf("CompleteRotationCleanup: %v", err)
	}
	if settled.PreviousSecretRef != "" || settled.Rotation != nil || settled.State != lifecycle.StateActive ||
		settledOperation.State != lifecycle.OperationActive {
		t.Fatalf("completion = %+v / %+v", settled, settledOperation)
	}

	// The completion is the tail of a crash-prone chain, so replaying it must be
	// a no-op rather than a second swap against a revision that has moved on.
	replayedAccount, replayedOperation, err := store.CompleteRotationCleanup(ctx, completion)
	if err != nil {
		t.Fatalf("CompleteRotationCleanup replay: %v", err)
	}
	if replayedAccount.PreviousSecretRef != "" || replayedOperation.State != lifecycle.OperationActive {
		t.Fatalf("completion replay = %+v / %+v", replayedAccount, replayedOperation)
	}

	settledLoad, err := store.Get(ctx, account.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if settledLoad.Rotation != nil || settledLoad.PreviousSecretRef != "" {
		t.Fatalf("settled account = %+v", settledLoad)
	}
}
