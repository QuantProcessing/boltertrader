package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/gateway/store"
)

func TestDesiredStateStoreRealPostgresCASRecoveryRotationAndRedaction(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	postgresStore := newStoreForTest(pool, storeOptions{identity: testIdentityGenerator()})
	var _ store.DesiredStateStore = postgresStore
	var _ store.RuntimeSnapshotStore = postgresStore

	desired := store.DesiredAccount{
		AccountID:            testAccountID(t),
		Venue:                "binance",
		VenueEnvironment:     "live",
		SecretRef:            "opaque://credential-canary/current",
		DesiredStateRevision: 1,
		RuntimeGeneration:    1,
		CredentialVersion:    1,
		RequestedState:       store.DesiredCreating,
	}
	created, err := postgresStore.CreateDesiredAccount(ctx, desired)
	if err != nil {
		t.Fatalf("CreateDesiredAccount: %v", err)
	}
	replayed, err := postgresStore.CreateDesiredAccount(ctx, desired)
	if err != nil || replayed.UpdatedAt != created.UpdatedAt {
		t.Fatalf("idempotent create replay = %+v, %v; want original", replayed, err)
	}
	conflictingCreate := desired
	conflictingCreate.SecretRef = "opaque://credential-canary/conflict"
	if _, err := postgresStore.CreateDesiredAccount(ctx, conflictingCreate); !errors.Is(err, store.ErrDesiredStateRevisionConflict) {
		t.Fatalf("conflicting create error = %v", err)
	}

	active := desired
	active.DesiredStateRevision = 2
	active.RequestedState = store.DesiredActive
	if _, err := postgresStore.ApplyDesiredAccount(ctx, 1, active); err != nil {
		t.Fatalf("activate desired account: %v", err)
	}
	if err := postgresStore.ValidateRuntimeFence(ctx, store.RuntimeFence{
		AccountID: active.AccountID, DesiredStateRevision: 2, RuntimeGeneration: 1, CredentialVersion: 1,
	}); err != nil {
		t.Fatalf("active runtime fence rejected: %v", err)
	}
	if err := postgresStore.ValidateRuntimeFence(ctx, store.RuntimeFence{
		AccountID: active.AccountID, DesiredStateRevision: 1, RuntimeGeneration: 1, CredentialVersion: 1,
	}); !errors.Is(err, store.ErrStaleRuntimeGeneration) {
		t.Fatalf("stale desired revision error = %v", err)
	}

	rotating := active
	rotating.SecretRef = "opaque://credential-canary/replacement"
	rotating.DesiredStateRevision = 3
	rotating.RequestedState = store.DesiredRotating
	rotating.RotationFence = &store.RotationFence{
		ID:                    "rotation-1",
		State:                 store.RotationArmed,
		FromCredentialVersion: 1,
		ToCredentialVersion:   2,
		FromRuntimeGeneration: 1,
		ToRuntimeGeneration:   2,
		ArmedAt:               time.Unix(1710000000, 0).UTC(),
	}
	rotated, err := postgresStore.ApplyDesiredAccount(ctx, 2, rotating)
	if err != nil {
		t.Fatalf("arm rotation: %v", err)
	}
	if rotated.PreviousSecretRef != active.SecretRef {
		t.Fatalf("rotation recovery previous ref = %q, want opaque previous reference", rotated.PreviousSecretRef)
	}
	if err := postgresStore.ValidateRuntimeFence(ctx, store.RuntimeFence{
		AccountID: rotating.AccountID, DesiredStateRevision: 3, RuntimeGeneration: 1, CredentialVersion: 1,
	}); err != nil {
		t.Fatalf("old generation fenced before replacement activation: %v", err)
	}
	replacementReady := rotating
	replacementReady.DesiredStateRevision = 4
	replacementReady.RotationFence = &store.RotationFence{
		ID: "rotation-1", State: store.RotationReplacementReady,
		FromCredentialVersion: 1, ToCredentialVersion: 2,
		FromRuntimeGeneration: 1, ToRuntimeGeneration: 2,
		ArmedAt: time.Unix(1710000000, 0).UTC(),
	}
	if _, err := postgresStore.ApplyDesiredAccount(ctx, 3, replacementReady); err != nil {
		t.Fatalf("mark rotation replacement ready: %v", err)
	}
	if replayed, err := postgresStore.ApplyDesiredAccount(ctx, 2, rotating); !errors.Is(err, store.ErrDesiredStateRevisionConflict) || replayed.DesiredStateRevision != 0 {
		t.Fatalf("obsolete rotation replay = %+v, %v", replayed, err)
	}
	if replayed, err := postgresStore.ApplyDesiredAccount(ctx, 3, replacementReady); err != nil || replayed.DesiredStateRevision != 4 {
		t.Fatalf("idempotent rotation replay = %+v, %v", replayed, err)
	}
	if _, err := postgresStore.ApplyDesiredAccount(ctx, 99, replacementReady); !errors.Is(err, store.ErrDesiredStateRevisionConflict) {
		t.Fatalf("rotation replay with wrong prior revision error = %v", err)
	}
	conflict := replacementReady
	conflict.SecretRef = "opaque://credential-canary/different"
	if _, err := postgresStore.ApplyDesiredAccount(ctx, 3, conflict); !errors.Is(err, store.ErrDesiredStateRevisionConflict) {
		t.Fatalf("same-revision content conflict error = %v", err)
	}

	releasedAt := time.Unix(1710000001, 0).UTC()
	promoted := replacementReady
	promoted.DesiredStateRevision = 5
	promoted.RuntimeGeneration = 2
	promoted.CredentialVersion = 2
	promoted.RequestedState = store.DesiredActive
	promoted.RotationFence = &store.RotationFence{
		ID:                    "rotation-1",
		State:                 store.RotationReleased,
		FromCredentialVersion: 1,
		ToCredentialVersion:   2,
		FromRuntimeGeneration: 1,
		ToRuntimeGeneration:   2,
		ArmedAt:               time.Unix(1710000000, 0).UTC(),
		ReleasedAt:            &releasedAt,
	}
	activated, err := postgresStore.ApplyDesiredAccount(ctx, 4, promoted)
	if err != nil {
		t.Fatalf("promote rotation: %v", err)
	}
	if activated.PreviousSecretRef != "" {
		t.Fatalf("promoted rotation retained previous secret ref %q", activated.PreviousSecretRef)
	}
	if err := postgresStore.ValidateRuntimeFence(ctx, store.RuntimeFence{
		AccountID: promoted.AccountID, DesiredStateRevision: 5, RuntimeGeneration: 1, CredentialVersion: 1,
	}); !errors.Is(err, store.ErrStaleRuntimeGeneration) {
		t.Fatalf("old generation remained write-active: %v", err)
	}

	restartedStore := NewStore(pool)
	recovered, err := restartedStore.ListRecoverableDesiredAccounts(ctx)
	if err != nil {
		t.Fatalf("ListRecoverableDesiredAccounts after restart: %v", err)
	}
	if len(recovered) != 1 || recovered[0].RuntimeGeneration != 2 || recovered[0].CredentialVersion != 2 {
		t.Fatalf("recovered desired accounts = %+v", recovered)
	}

	rows, err := pool.Query(ctx, `SELECT metadata::text FROM audit_records WHERE resource_id = $1`, desired.AccountID)
	if err != nil {
		t.Fatalf("load desired audit: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var metadata string
		if err := rows.Scan(&metadata); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(metadata, "credential-canary") || strings.Contains(metadata, "secret_ref") {
			t.Fatalf("desired audit leaked secret reference: %s", metadata)
		}
	}
}
