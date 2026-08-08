package postgres

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/backend/internal/platform/accounts"
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
