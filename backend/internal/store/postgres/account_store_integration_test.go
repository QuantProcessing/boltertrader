package postgres

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/backend/internal/platform/accounts"
	"github.com/QuantProcessing/boltertrader/secretstore"
)

func TestAccountStorePersistsNilRotationAsSQLNullAndObjectRotationAsJSON(t *testing.T) {
	database, _ := openIntegrationDatabase(t)
	store := NewAccountStore(database)
	ctx := context.Background()
	now := time.Date(2026, 8, 6, 3, 0, 0, 0, time.UTC)
	account := accounts.Account{
		ID: "account-rotation", Venue: "binance", VenueEnvironment: accounts.EnvironmentDemo,
		DisplayName: "Rotation Account", ProductIDs: []string{"funding"}, SecretRef: "secret://test/account-rotation",
		DesiredStateRevision: 1, RuntimeGeneration: 1, CredentialVersion: 1, State: accounts.StateActive, UpdatedAt: now,
	}
	if err := store.Create(ctx, account); err != nil {
		t.Fatal(err)
	}
	assertAccountRotationSQL(t, database, account.ID, true, "")
	created, err := store.Get(ctx, account.ID)
	if err != nil || created.Rotation != nil {
		t.Fatalf("created=%+v err=%v", created, err)
	}

	account.DesiredStateRevision = 2
	account.Rotation = &accounts.RotationFence{
		ID: "rotation-1", State: "armed", FromCredentialVersion: 1, ToCredentialVersion: 2,
		FromRuntimeGeneration: 1, ToRuntimeGeneration: 2, ArmedAt: now.Add(time.Minute),
	}
	account.UpdatedAt = now.Add(time.Minute)
	if err := store.CompareAndSwap(ctx, account.ID, 1, account); err != nil {
		t.Fatal(err)
	}
	assertAccountRotationSQL(t, database, account.ID, false, "object")
	rotating, err := store.Get(ctx, account.ID)
	if err != nil || rotating.Rotation == nil || rotating.Rotation.ID != "rotation-1" || rotating.Rotation.ToCredentialVersion != 2 {
		t.Fatalf("rotating=%+v err=%v", rotating, err)
	}

	account.DesiredStateRevision = 3
	account.Rotation = nil
	account.UpdatedAt = now.Add(2 * time.Minute)
	if err := store.CompareAndSwap(ctx, account.ID, 2, account); err != nil {
		t.Fatal(err)
	}
	assertAccountRotationSQL(t, database, account.ID, true, "")
	cleared, err := store.Get(ctx, account.ID)
	if err != nil || cleared.Rotation != nil {
		t.Fatalf("cleared=%+v err=%v", cleared, err)
	}
}

func TestAccountStoreCredentialOperationIntegration(t *testing.T) {
	database, _ := openIntegrationDatabase(t)
	store := NewAccountStore(database)
	ctx := context.Background()
	now := time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)
	account := accounts.Account{
		ID: "account-journal", Venue: "binance", VenueEnvironment: accounts.EnvironmentDemo,
		Mode: secretstore.ModeTest, NativeEnvironment: accounts.EnvironmentDemo,
		DisplayName: "Journal Account", ProductIDs: []string{"manual"}, ProductPolicy: []string{"manual"},
		Routes: []accounts.AccountRoute{
			{ID: "z-route", Product: accounts.ProductPerp, SettlementAsset: "USDT", RouteProfileID: "binance.perp.usdt.demo.v1", RequestedState: accounts.RouteActive},
			{ID: "a-route", Product: accounts.ProductSpot, RouteProfileID: "binance.spot.demo.v1", RequestedState: accounts.RouteActive},
		},
		DesiredStateRevision: 1, RuntimeGeneration: 1, CredentialVersion: 1, State: accounts.StateCreating, UpdatedAt: now,
	}
	operation := accounts.CredentialOperation{
		ID: "018f47a8-2578-7d3c-9d7d-5e4f4a445c33", PrincipalID: "principal-1", AccountID: account.ID,
		Mode: secretstore.ModeTest, Kind: accounts.OperationCreate, State: accounts.OperationAwaitingMaterial,
		IdempotencyKeyDigest: "idempotency", RequestDigest: "request", TargetDesiredStateRevision: 1,
		TargetRuntimeGeneration: 1, TargetCredentialVersion: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateDraft(ctx, account, operation); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, account.ID)
	if err != nil || len(got.Routes) != 2 || got.Routes[0].ID != "a-route" {
		t.Fatalf("account=%+v err=%v", got, err)
	}
	found, err := store.FindCredentialOperation(ctx, accounts.OperationLookup{Mode: operation.Mode, PrincipalID: operation.PrincipalID, Kind: operation.Kind, IdempotencyKeyDigest: operation.IdempotencyKeyDigest})
	if err != nil || found.ID != operation.ID {
		t.Fatalf("found=%+v err=%v", found, err)
	}
	boundAccount, bound, err := store.BindCredentialReference(ctx, operation.ID, "opaque-ref")
	if err != nil || bound.State != accounts.OperationStored || boundAccount.SecretRef != "opaque-ref" {
		t.Fatalf("account=%+v operation=%+v err=%v", boundAccount, bound, err)
	}
	if _, replay, err := store.BindCredentialReference(ctx, operation.ID, "opaque-ref"); err != nil || replay.State != accounts.OperationStored {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	recoverable, err := store.ListRecoverableCredentialOperations(ctx, 10)
	if err != nil || len(recoverable) != 1 || recoverable[0].ID != operation.ID {
		t.Fatalf("recoverable=%+v err=%v", recoverable, err)
	}
	bound.State = accounts.OperationApplying
	bound.UpdatedAt = now.Add(time.Minute)
	got, bound, err = store.CompareAndSwapAccountAndOperation(ctx, accounts.TransitionRequest{OperationID: operation.ID, ExpectedOperationState: accounts.OperationStored, ExpectedDesiredStateRevision: 1, Account: boundAccount, Operation: bound})
	if err != nil || bound.State != accounts.OperationApplying || got.ID != account.ID {
		t.Fatalf("account=%+v operation=%+v err=%v", got, bound, err)
	}
	if _, _, err := store.CompareAndSwapAccountAndOperation(ctx, accounts.TransitionRequest{OperationID: operation.ID, ExpectedOperationState: accounts.OperationStored, ExpectedDesiredStateRevision: 1, Account: boundAccount, Operation: bound}); !errors.Is(err, accounts.ErrRevisionConflict) {
		t.Fatalf("stale CAS err=%v", err)
	}
}

func TestAccountStoreRotationCleanupProofRejectsStaleFenceWithoutWrite(t *testing.T) {
	database, _ := openIntegrationDatabase(t)
	store := NewAccountStore(database)
	ctx := context.Background()
	account, operation := createPostgresRotationCleanupFixture(t, ctx, store, "account-rotation-proof-cas", "018f47a8-2578-7d3c-9d7d-5e4f4a445c41")
	base := postgresRotationCleanupProof(account, operation)
	tests := map[string]func(*accounts.RotationCleanupProof){
		"active fence":           func(proof *accounts.RotationCleanupProof) { proof.ActiveFence.DesiredStateRevision++ },
		"retiring fence":         func(proof *accounts.RotationCleanupProof) { proof.RetiringFence.RuntimeGeneration++ },
		"tombstone revision":     func(proof *accounts.RotationCleanupProof) { proof.TombstoneRevision++ },
		"retiring state":         func(proof *accounts.RotationCleanupProof) { proof.RetiringState = "draining" },
		"secret reference state": func(proof *accounts.RotationCleanupProof) { proof.SecretReferenceState = "active" },
		"completion time":        func(proof *accounts.RotationCleanupProof) { proof.CompletedAt = time.Time{} },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			proof := base
			mutate(&proof)
			if _, err := store.RecordRotationCleanupProof(ctx, proof); err == nil {
				t.Fatal("invalid proof was accepted")
			}
			assertNoPostgresRotationCleanupProof(t, ctx, database, operation.ID)
			persisted, err := store.Get(ctx, account.ID)
			if err != nil || persisted.PreviousSecretRef != account.PreviousSecretRef {
				t.Fatalf("account=%+v err=%v", persisted, err)
			}
		})
	}
}

func TestAccountStoreRejectsSecondRecoverableCredentialOperationForAccount(t *testing.T) {
	ctx := context.Background()
	database, _ := openIntegrationDatabase(t)
	store := NewAccountStore(database)
	account, operation := createPostgresRotationCleanupFixture(t, ctx, store, "account-recoverable-operation", "018f47a8-2578-7d3c-9d7d-5e4f4a445c43")
	second := operation
	second.ID = "018f47a8-2578-7d3c-9d7d-5e4f4a445c44"
	second.State = accounts.OperationAwaitingMaterial
	second.IdempotencyKeyDigest = "second-idempotency-digest"
	second.RequestDigest = "second-request-digest"
	second.SecretRef = ""
	second.RotationCleanupTombstoneRevision = 0
	second.RotationCleanupCompletedAt = time.Time{}
	second.CreatedAt = second.CreatedAt.Add(time.Second)
	second.UpdatedAt = second.CreatedAt
	if err := store.BeginCredentialOperation(ctx, account.DesiredStateRevision, account, second); !errors.Is(err, accounts.ErrRevisionConflict) {
		t.Fatalf("second recoverable operation err=%v", err)
	}
	var count int
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM platform_credential_operations WHERE account_id=$1 AND state IN ('awaiting_material','stored','applying','cleanup_pending')`, account.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("recoverable operation count=%d", count)
	}
}

func TestAccountStoreRotationCleanupProofAndCompletionAreAtomicAndReplayable(t *testing.T) {
	database, reopen := openIntegrationDatabase(t)
	store := NewAccountStore(database)
	ctx := context.Background()
	account, operation := createPostgresRotationCleanupFixture(t, ctx, store, "account-rotation-cleanup", "018f47a8-2578-7d3c-9d7d-5e4f4a445c42")
	proof := postgresRotationCleanupProof(account, operation)
	persistedProof, err := store.RecordRotationCleanupProof(ctx, proof)
	if err != nil || persistedProof.State != accounts.OperationCleanupPending ||
		persistedProof.RotationCleanupTombstoneRevision != proof.TombstoneRevision ||
		!persistedProof.RotationCleanupCompletedAt.Equal(proof.CompletedAt) {
		t.Fatalf("operation=%+v err=%v", persistedProof, err)
	}
	replayedProof, err := store.RecordRotationCleanupProof(ctx, proof)
	if err != nil || replayedProof.RotationCleanupTombstoneRevision != proof.TombstoneRevision || !replayedProof.RotationCleanupCompletedAt.Equal(proof.CompletedAt) {
		t.Fatalf("replay=%+v err=%v", replayedProof, err)
	}

	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	database = reopen()
	store = NewAccountStore(database)
	restored, err := store.GetCredentialOperation(ctx, operation.ID)
	if err != nil || restored.RotationCleanupTombstoneRevision != proof.TombstoneRevision || !restored.RotationCleanupCompletedAt.Equal(proof.CompletedAt) {
		t.Fatalf("restored=%+v err=%v", restored, err)
	}

	completion := accounts.RotationCleanupCompletion{
		OperationID: operation.ID, AccountID: account.ID,
		PreviousSecretRef: account.PreviousSecretRef, CompletedAt: proof.CompletedAt.Add(time.Second),
	}
	wrongReference := completion
	wrongReference.PreviousSecretRef = "another-reference"
	if _, _, err := store.CompleteRotationCleanup(ctx, wrongReference); err == nil {
		t.Fatal("cleanup with the wrong previous reference was accepted")
	}
	stillPending, err := store.Get(ctx, account.ID)
	if err != nil || stillPending.PreviousSecretRef != account.PreviousSecretRef || stillPending.State != accounts.StateActive {
		t.Fatalf("account=%+v err=%v", stillPending, err)
	}
	stillPendingOperation, err := store.GetCredentialOperation(ctx, operation.ID)
	if err != nil || stillPendingOperation.State != accounts.OperationCleanupPending {
		t.Fatalf("operation=%+v err=%v", stillPendingOperation, err)
	}

	completedAccount, completedOperation, err := store.CompleteRotationCleanup(ctx, completion)
	if err != nil || completedAccount.PreviousSecretRef != "" || completedAccount.State != accounts.StateActive ||
		completedOperation.State != accounts.OperationActive || completedOperation.RotationCleanupTombstoneRevision != proof.TombstoneRevision ||
		!completedOperation.RotationCleanupCompletedAt.Equal(proof.CompletedAt) {
		t.Fatalf("account=%+v operation=%+v err=%v", completedAccount, completedOperation, err)
	}
	replayedAccount, replayedOperation, err := store.CompleteRotationCleanup(ctx, completion)
	if err != nil || replayedAccount.PreviousSecretRef != "" || replayedOperation.State != accounts.OperationActive ||
		replayedOperation.RotationCleanupTombstoneRevision != proof.TombstoneRevision {
		t.Fatalf("replayed account=%+v operation=%+v err=%v", replayedAccount, replayedOperation, err)
	}
}

func createPostgresRotationCleanupFixture(t *testing.T, ctx context.Context, store *AccountStore, accountID string, operationID secretstore.OperationID) (accounts.Account, accounts.CredentialOperation) {
	t.Helper()
	now := time.Date(2026, 8, 9, 8, 0, 0, 0, time.UTC)
	account := accounts.Account{
		ID: accountID, Venue: "binance", VenueEnvironment: accounts.EnvironmentDemo,
		Mode: secretstore.ModeTest, NativeEnvironment: accounts.EnvironmentDemo,
		DisplayName: "Rotation Cleanup", ProductIDs: []string{"manual"}, ProductPolicy: []string{"manual"},
		Routes:    []accounts.AccountRoute{{ID: "perp-usdt", Product: accounts.ProductPerp, SettlementAsset: "USDT", RouteProfileID: "binance.perp.usdt.demo.v1", RequestedState: accounts.RouteActive}},
		SecretRef: "new-opaque-reference", PreviousSecretRef: "old-opaque-reference",
		DesiredStateRevision: 2, RuntimeGeneration: 2, CredentialVersion: 2, State: accounts.StateActive,
		Rotation: &accounts.RotationFence{
			ID: "rotation-1", State: "promoted", FromCredentialVersion: 1, ToCredentialVersion: 2,
			FromRuntimeGeneration: 1, ToRuntimeGeneration: 2, ArmedAt: now.Add(-time.Minute),
		},
		UpdatedAt: now,
	}
	if err := store.Create(ctx, account); err != nil {
		t.Fatal(err)
	}
	operation := accounts.CredentialOperation{
		ID: operationID, PrincipalID: "principal-1", AccountID: account.ID, Mode: secretstore.ModeTest,
		Kind: accounts.OperationRotate, State: accounts.OperationCleanupPending,
		IdempotencyKeyDigest: "rotation-cleanup-idempotency", RequestDigest: "rotation-cleanup-request",
		TargetDesiredStateRevision: account.DesiredStateRevision, TargetRuntimeGeneration: account.RuntimeGeneration,
		TargetCredentialVersion: account.CredentialVersion, SecretRef: account.SecretRef,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := store.BeginCredentialOperation(ctx, account.DesiredStateRevision, account, operation); err != nil {
		t.Fatal(err)
	}
	return account, operation
}

func postgresRotationCleanupProof(account accounts.Account, operation accounts.CredentialOperation) accounts.RotationCleanupProof {
	return accounts.RotationCleanupProof{
		OperationID: operation.ID, AccountID: account.ID,
		ActiveFence: accounts.AccountFence{
			AccountID: account.ID, DesiredStateRevision: account.DesiredStateRevision,
			RuntimeGeneration: account.RuntimeGeneration, CredentialVersion: account.CredentialVersion,
		},
		RetiringFence: accounts.AccountFence{
			AccountID: account.ID, DesiredStateRevision: account.DesiredStateRevision - 1,
			RuntimeGeneration: account.RuntimeGeneration - 1, CredentialVersion: account.CredentialVersion - 1,
		},
		TombstoneRevision: account.DesiredStateRevision,
		RetiringState:     "retired", SecretReferenceState: "tombstoned",
		CompletedAt: time.Date(2026, 8, 9, 8, 9, 10, 0, time.UTC),
	}
}

func assertNoPostgresRotationCleanupProof(t *testing.T, ctx context.Context, database *sql.DB, operationID secretstore.OperationID) {
	t.Helper()
	var tombstoneNull, completedNull bool
	var state accounts.OperationState
	if err := database.QueryRowContext(ctx, `SELECT rotation_cleanup_tombstone_revision IS NULL, rotation_cleanup_completed_at IS NULL, state FROM platform_credential_operations WHERE operation_id=$1`, operationID).Scan(&tombstoneNull, &completedNull, &state); err != nil {
		t.Fatal(err)
	}
	if !tombstoneNull || !completedNull || state != accounts.OperationCleanupPending {
		t.Fatalf("tombstoneNull=%t completedNull=%t state=%q", tombstoneNull, completedNull, state)
	}
}

func assertAccountRotationSQL(t *testing.T, database *sql.DB, accountID string, wantNull bool, wantType string) {
	t.Helper()
	var isNull bool
	var jsonType *string
	if err := database.QueryRowContext(context.Background(), `SELECT rotation_fence IS NULL, jsonb_typeof(rotation_fence) FROM platform_accounts WHERE account_id=$1`, accountID).Scan(&isNull, &jsonType); err != nil {
		t.Fatal(err)
	}
	if isNull != wantNull || (jsonType != nil && *jsonType != wantType) || (wantType != "" && jsonType == nil) {
		t.Fatalf("rotation SQL null=%t type=%v, want null=%t type=%q", isNull, jsonType, wantNull, wantType)
	}
}
