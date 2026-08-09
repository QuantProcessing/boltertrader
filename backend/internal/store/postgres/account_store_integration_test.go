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
