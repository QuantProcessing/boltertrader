package postgres

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/gateway/domain"
	storepkg "github.com/QuantProcessing/boltertrader/gateway/store"
	"github.com/QuantProcessing/boltertrader/secretstore"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestRotationFinalizationRequestValidatesAdjacentFencesAndDigestExcludesOperation(t *testing.T) {
	firstOperation, _ := secretstore.ParseOperationID("018f47a8-2578-7d3c-9d7d-5e4f4a445ca1")
	secondOperation, _ := secretstore.ParseOperationID("018f47a8-2578-7d3c-9d7d-5e4f4a445ca2")
	retiringFence := domain.AccountFence{AccountID: "account-runtime", DesiredStateRevision: 4, RuntimeGeneration: 7, CredentialVersion: 9}
	request := storepkg.RotationFinalizationRequest{
		OperationID: firstOperation, AccountID: retiringFence.AccountID,
		ExpectedActiveFence:   domain.AccountFence{AccountID: retiringFence.AccountID, DesiredStateRevision: 5, RuntimeGeneration: 8, CredentialVersion: 10},
		ExpectedRetiringFence: retiringFence,
		Scope:                 secretstore.Scope{Mode: secretstore.ModeTest, Namespace: "boltertrader/test", KeyVersion: "v1"},
	}
	firstDigest, err := request.CanonicalDigest()
	if err != nil {
		t.Fatal(err)
	}
	request.OperationID = secondOperation
	secondDigest, err := request.CanonicalDigest()
	if err != nil {
		t.Fatal(err)
	}
	if firstDigest != secondDigest {
		t.Fatalf("digest includes operation id: first=%s second=%s", firstDigest, secondDigest)
	}
	request.ExpectedActiveFence.RuntimeGeneration++
	if err := request.Validate(); !errors.Is(err, storepkg.ErrPromotionFenceConflict) {
		t.Fatalf("non-adjacent fence validation err=%v", err)
	}
}

func TestRuntimeSnapshotReadinessRequiresLatestObservationForEveryActiveRoute(t *testing.T) {
	ctx := context.Background()
	postgresStore := newRuntimeSnapshotTestStore(t, ctx)
	pending := runtimeSnapshotFixture(t, "018f47a8-2578-7d3c-9d7d-5e4f4a445c41", "018f47a8-2578-7d3c-9d7d-5e4f4a445c42", 1)
	if _, err := postgresStore.PreparePending(ctx, pending); err != nil {
		t.Fatal(err)
	}
	if err := postgresStore.MarkCandidateReady(ctx, pending.OperationID); !errors.Is(err, storepkg.ErrCandidateNotReady) {
		t.Fatalf("zero observations err=%v", err)
	}
	if err := postgresStore.RecordCandidateObservation(ctx, observation(pending, 1, "unknown", storepkg.CandidateReady)); err == nil {
		t.Fatal("unknown route observation accepted")
	}
	if err := postgresStore.RecordCandidateObservation(ctx, observation(pending, 1, "spot", storepkg.CandidateReady)); err != nil {
		t.Fatal(err)
	}
	if err := postgresStore.MarkCandidateReady(ctx, pending.OperationID); !errors.Is(err, storepkg.ErrCandidateNotReady) {
		t.Fatalf("missing active route err=%v", err)
	}
	if err := postgresStore.RecordCandidateObservation(ctx, observation(pending, 1, "perp-usdt", storepkg.CandidateFailed)); err != nil {
		t.Fatal(err)
	}
	if err := postgresStore.RecordCandidateObservation(ctx, observation(pending, 2, "perp-usdt", storepkg.CandidateReady)); err != nil {
		t.Fatal(err)
	}
	if err := postgresStore.RecordCandidateObservation(ctx, observation(pending, 2, "spot", storepkg.CandidateReady)); err != nil {
		t.Fatal(err)
	}
	if err := postgresStore.MarkCandidateReady(ctx, pending.OperationID); err != nil {
		t.Fatalf("latest complete observations not ready: %v", err)
	}
}

func TestRuntimeSnapshotPromotionReplayMonotonicFenceAndPromotedAtRecovery(t *testing.T) {
	ctx := context.Background()
	postgresStore := newRuntimeSnapshotTestStore(t, ctx)
	first := runtimeSnapshotFixture(t, "018f47a8-2578-7d3c-9d7d-5e4f4a445c51", "018f47a8-2578-7d3c-9d7d-5e4f4a445c52", 1)
	prepareRuntimeSnapshotReady(t, ctx, postgresStore, first)
	request := storepkg.PromotionRequest{AccountID: first.AccountID, OperationID: first.OperationID}
	promoted, err := postgresStore.PromotePending(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := postgresStore.PromotePending(ctx, request)
	if err != nil || replayed.Active.SnapshotID != promoted.Active.SnapshotID || !replayed.Active.PromotedAt.Equal(promoted.Active.PromotedAt) {
		t.Fatalf("replay=%+v err=%v promoted=%+v", replayed, err, promoted)
	}
	restored, err := postgresStore.LoadRuntimeState(ctx, first.AccountID)
	if err != nil || restored.Active == nil || !restored.Active.PromotedAt.Equal(promoted.Active.PromotedAt) {
		t.Fatalf("restored=%+v err=%v promotedAt=%v", restored, err, promoted.Active.PromotedAt)
	}

	stale := runtimeSnapshotFixture(t, "018f47a8-2578-7d3c-9d7d-5e4f4a445c53", "018f47a8-2578-7d3c-9d7d-5e4f4a445c54", 1)
	if _, err := postgresStore.PreparePending(ctx, stale); !errors.Is(err, storepkg.ErrStaleRuntimeGeneration) {
		t.Fatalf("stale fence err=%v", err)
	}
}

func TestMigrationPromotionCrashBeforeCommitReplaysExactlyOnce(t *testing.T) {
	ctx := context.Background()
	postgresStore := newRuntimeSnapshotTestStore(t, ctx)
	snapshot := runtimeSnapshotFixture(t, "018f47a8-2578-7d3c-9d7d-5e4f4a445d41", "018f47a8-2578-7d3c-9d7d-5e4f4a445d42", 1)
	pending, err := postgresStore.PreparePending(ctx, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	request := migrationPromotionRequest(t, pending.DesiredSnapshot)
	injected := errors.New("injected crash before migration promotion commit")
	if _, err := postgresStore.promoteMigrationPendingSet(ctx, request, func(context.Context) error { return injected }); !errors.Is(err, injected) {
		t.Fatalf("injected crash err=%v", err)
	}
	state, err := postgresStore.LoadRuntimeState(ctx, snapshot.AccountID)
	if err != nil || state.Pending == nil || state.Active != nil {
		t.Fatalf("state after rollback=%+v err=%v", state, err)
	}
	first, err := postgresStore.PromoteMigrationPendingSet(ctx, request)
	if err != nil || first.Replayed {
		t.Fatalf("retry result=%+v err=%v", first, err)
	}
	replay, err := postgresStore.PromoteMigrationPendingSet(ctx, request)
	if err != nil || !replay.Replayed || replay.After != first.After {
		t.Fatalf("replay=%+v err=%v first=%+v", replay, err, first)
	}
}

func TestRuntimeRetirementIsAtomicReplayableAndRejectsUnresolvedHandoff(t *testing.T) {
	ctx := context.Background()
	postgresStore := newRuntimeSnapshotTestStore(t, ctx)
	snapshot := runtimeSnapshotFixture(t, "018f47a8-2578-7d3c-9d7d-5e4f4a445c61", "018f47a8-2578-7d3c-9d7d-5e4f4a445c62", 1)
	prepareRuntimeSnapshotReady(t, ctx, postgresStore, snapshot)
	if _, err := postgresStore.PromotePending(ctx, storepkg.PromotionRequest{AccountID: snapshot.AccountID, OperationID: snapshot.OperationID}); err != nil {
		t.Fatal(err)
	}

	operationID, _ := secretstore.ParseOperationID("018f47a8-2578-7d3c-9d7d-5e4f4a445c63")
	request := storepkg.RetirementRequest{
		OperationID: operationID, AccountID: snapshot.AccountID, ExpectedActiveFence: snapshot.Fence,
		TombstoneRevision: snapshot.Fence.DesiredStateRevision + 1,
		Scope:             secretstore.Scope{Mode: secretstore.ModeTest, Namespace: "boltertrader/test", KeyVersion: "v1"},
	}
	commandID := uuid.MustParse("018f47a8-2578-7d3c-9d7d-5e4f4a445c64")
	handoffID := uuid.MustParse("018f47a8-2578-7d3c-9d7d-5e4f4a445c65")
	if _, err := postgresStore.pool.Exec(ctx, `
		INSERT INTO idempotency_commands(command_id, principal_id, idempotency_key, account_id, product, payload_hash, request_payload, gateway_order_id, portable_client_order_id, state)
		VALUES ($1,'principal','retirement-handoff',$2,'spot','hash','{}',$3,1,'dispatching')
	`, commandID, snapshot.AccountID, uuid.MustParse("018f47a8-2578-7d3c-9d7d-5e4f4a445c66")); err != nil {
		t.Fatal(err)
	}
	if _, err := postgresStore.pool.Exec(ctx, `
		INSERT INTO gateway_order_handoffs(handoff_id,command_id,account_id,route_id,desired_state_revision,runtime_generation,credential_version,state)
		VALUES ($1,$2,$3,'spot',$4,$5,$6,'started')
	`, handoffID, commandID, snapshot.AccountID, snapshot.Fence.DesiredStateRevision, snapshot.Fence.RuntimeGeneration, snapshot.Fence.CredentialVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := postgresStore.RetireAccount(ctx, request); !errors.Is(err, storepkg.ErrHandoffDrainBlocked) {
		t.Fatalf("unresolved handoff retirement err=%v", err)
	}
	if _, err := postgresStore.pool.Exec(ctx, `UPDATE gateway_order_handoffs SET state='uncertain',resolved_at=now() WHERE handoff_id=$1`, handoffID); err != nil {
		t.Fatal(err)
	}
	first, err := postgresStore.RetireAccount(ctx, request)
	if err != nil || !first.Tombstoned {
		t.Fatalf("retirement=%+v err=%v", first, err)
	}
	replayed, err := postgresStore.RetireAccount(ctx, request)
	if err != nil || replayed != first {
		t.Fatalf("replay=%+v err=%v first=%+v", replayed, err, first)
	}
	state, err := postgresStore.LoadRuntimeState(ctx, snapshot.AccountID)
	if err != nil || !state.Tombstoned || state.Active != nil || state.Pending != nil || len(state.Retiring) != 0 {
		t.Fatalf("retired state=%+v err=%v", state, err)
	}
	retired, err := postgresStore.IsSecretReferenceRetired(ctx, request.Scope, snapshot.SecretRef)
	if err != nil || !retired {
		t.Fatalf("retired reference lookup=%t err=%v", retired, err)
	}
	conflict := request
	conflict.ExpectedActiveFence.DesiredStateRevision++
	conflict.TombstoneRevision++
	if _, err := postgresStore.RetireAccount(ctx, conflict); !errors.Is(err, storepkg.ErrOperationConflict) {
		t.Fatalf("conflicting replay err=%v", err)
	}
}

func TestConcurrentIdenticalRetirementsReturnSameCommittedProof(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	postgresStore := newRuntimeSnapshotTestStore(t, ctx)
	active := runtimeSnapshotFixture(t, "018f47a8-2578-7d3c-9d7d-5e4f4a445c67", "018f47a8-2578-7d3c-9d7d-5e4f4a445c68", 1)
	prepareRuntimeSnapshotReady(t, ctx, postgresStore, active)
	if _, err := postgresStore.PromotePending(ctx, storepkg.PromotionRequest{AccountID: active.AccountID, OperationID: active.OperationID}); err != nil {
		t.Fatal(err)
	}
	operationID, _ := secretstore.ParseOperationID("018f47a8-2578-7d3c-9d7d-5e4f4a445c69")
	request := storepkg.RetirementRequest{
		OperationID: operationID, AccountID: active.AccountID, ExpectedActiveFence: active.Fence,
		TombstoneRevision: active.Fence.DesiredStateRevision + 1,
		Scope:             secretstore.Scope{Mode: secretstore.ModeTest, Namespace: "boltertrader/test", KeyVersion: "v1"},
	}

	blocker, err := postgresStore.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(ctx)
	if _, err := blocker.Exec(ctx, `LOCK TABLE gateway_retiring_generations IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	type retirementCall struct {
		result storepkg.RetirementResult
		err    error
	}
	firstCall := make(chan retirementCall, 1)
	go func() {
		result, err := postgresStore.RetireAccount(ctx, request)
		firstCall <- retirementCall{result: result, err: err}
	}()
	waitForPostgresLockWait(t, ctx, postgresStore, "%INSERT INTO gateway_retiring_generations%")

	secondCall := make(chan retirementCall, 1)
	go func() {
		result, err := postgresStore.RetireAccount(ctx, request)
		secondCall <- retirementCall{result: result, err: err}
	}()
	waitForPostgresLockWait(t, ctx, postgresStore, "%FROM gateway_account_runtime_heads WHERE account_id=$1 FOR UPDATE%")
	if err := blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	first := <-firstCall
	if first.err != nil {
		t.Fatalf("first retirement err=%v", first.err)
	}
	second := <-secondCall
	if second.err != nil || second.result != first.result {
		t.Fatalf("concurrent replay=%+v err=%v first=%+v", second.result, second.err, first.result)
	}
}

func TestRetiredAccountRejectsFreshPendingSnapshot(t *testing.T) {
	ctx := context.Background()
	postgresStore := newRuntimeSnapshotTestStore(t, ctx)
	active := runtimeSnapshotFixture(t, "018f47a8-2578-7d3c-9d7d-5e4f4a445c71", "018f47a8-2578-7d3c-9d7d-5e4f4a445c72", 1)
	prepareRuntimeSnapshotReady(t, ctx, postgresStore, active)
	if _, err := postgresStore.PromotePending(ctx, storepkg.PromotionRequest{AccountID: active.AccountID, OperationID: active.OperationID}); err != nil {
		t.Fatal(err)
	}
	retireOperation, _ := secretstore.ParseOperationID("018f47a8-2578-7d3c-9d7d-5e4f4a445c73")
	if _, err := postgresStore.RetireAccount(ctx, storepkg.RetirementRequest{
		OperationID: retireOperation, AccountID: active.AccountID, ExpectedActiveFence: active.Fence,
		TombstoneRevision: active.Fence.DesiredStateRevision + 1,
		Scope:             secretstore.Scope{Mode: secretstore.ModeTest, Namespace: "boltertrader/test", KeyVersion: "v1"},
	}); err != nil {
		t.Fatal(err)
	}
	freshRef, err := secretstore.ParseOpaqueRef("AgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgI")
	if err != nil {
		t.Fatal(err)
	}
	fresh := runtimeSnapshotFixtureWithRef(t, "018f47a8-2578-7d3c-9d7d-5e4f4a445c74", "018f47a8-2578-7d3c-9d7d-5e4f4a445c75", active.AccountID, 3, freshRef)
	if _, err := postgresStore.PreparePending(ctx, fresh); !errors.Is(err, storepkg.ErrOperationConflict) {
		t.Fatalf("prepare after account retirement err=%v", err)
	}
	state, err := postgresStore.LoadRuntimeState(ctx, active.AccountID)
	if err != nil || !state.Tombstoned || state.Active != nil || state.Pending != nil || len(state.Retiring) != 0 {
		t.Fatalf("retired state=%+v err=%v", state, err)
	}
}

func TestRetirementAndPendingPreparationNeverPublishContradictoryHeads(t *testing.T) {
	t.Run("retirement locks head before pending preparation", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		postgresStore := newRuntimeSnapshotTestStore(t, ctx)
		active := runtimeSnapshotFixture(t, "018f47a8-2578-7d3c-9d7d-5e4f4a445e71", "018f47a8-2578-7d3c-9d7d-5e4f4a445e72", 1)
		prepareRuntimeSnapshotReady(t, ctx, postgresStore, active)
		if _, err := postgresStore.PromotePending(ctx, storepkg.PromotionRequest{AccountID: active.AccountID, OperationID: active.OperationID}); err != nil {
			t.Fatal(err)
		}
		freshRef, _ := secretstore.ParseOpaqueRef("AgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgI")
		fresh := runtimeSnapshotFixtureWithRef(t, "018f47a8-2578-7d3c-9d7d-5e4f4a445e73", "018f47a8-2578-7d3c-9d7d-5e4f4a445e74", active.AccountID, 2, freshRef)

		blocker, err := postgresStore.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer blocker.Rollback(ctx)
		if _, err := blocker.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, freshRef.RawForTrustedBoundary()); err != nil {
			t.Fatal(err)
		}
		prepared := make(chan error, 1)
		go func() {
			_, err := postgresStore.PreparePending(ctx, fresh)
			prepared <- err
		}()
		waitForPostgresLockWait(t, ctx, postgresStore, "%pg_advisory_xact_lock(hashtextextended%")

		retireOperation, _ := secretstore.ParseOperationID("018f47a8-2578-7d3c-9d7d-5e4f4a445e75")
		if _, err := postgresStore.RetireAccount(ctx, storepkg.RetirementRequest{
			OperationID: retireOperation, AccountID: active.AccountID, ExpectedActiveFence: active.Fence,
			TombstoneRevision: active.Fence.DesiredStateRevision + 1,
			Scope:             secretstore.Scope{Mode: secretstore.ModeTest, Namespace: "boltertrader/test", KeyVersion: "v1"},
		}); err != nil {
			t.Fatal(err)
		}
		if err := blocker.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if err := <-prepared; !errors.Is(err, storepkg.ErrOperationConflict) {
			t.Fatalf("pending preparation after retirement err=%v", err)
		}
		assertOnlyTombstone(t, ctx, postgresStore, active.AccountID)
	})

	t.Run("pending preparation locks head before retirement", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		postgresStore := newRuntimeSnapshotTestStore(t, ctx)
		active := runtimeSnapshotFixture(t, "018f47a8-2578-7d3c-9d7d-5e4f4a445e81", "018f47a8-2578-7d3c-9d7d-5e4f4a445e82", 1)
		prepareRuntimeSnapshotReady(t, ctx, postgresStore, active)
		if _, err := postgresStore.PromotePending(ctx, storepkg.PromotionRequest{AccountID: active.AccountID, OperationID: active.OperationID}); err != nil {
			t.Fatal(err)
		}
		freshRef, _ := secretstore.ParseOpaqueRef("AgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgI")
		fresh := runtimeSnapshotFixtureWithRef(t, "018f47a8-2578-7d3c-9d7d-5e4f4a445e83", "018f47a8-2578-7d3c-9d7d-5e4f4a445e84", active.AccountID, 2, freshRef)

		blocker, err := postgresStore.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer blocker.Rollback(ctx)
		if _, err := blocker.Exec(ctx, `LOCK TABLE gateway_runtime_snapshots IN SHARE MODE`); err != nil {
			t.Fatal(err)
		}
		prepared := make(chan error, 1)
		go func() {
			_, err := postgresStore.PreparePending(ctx, fresh)
			prepared <- err
		}()
		waitForPostgresLockWait(t, ctx, postgresStore, "%INSERT INTO gateway_runtime_snapshots%")

		retireOperation, _ := secretstore.ParseOperationID("018f47a8-2578-7d3c-9d7d-5e4f4a445e85")
		retired := make(chan error, 1)
		go func() {
			_, err := postgresStore.RetireAccount(ctx, storepkg.RetirementRequest{
				OperationID: retireOperation, AccountID: active.AccountID, ExpectedActiveFence: active.Fence,
				TombstoneRevision: active.Fence.DesiredStateRevision + 1,
				Scope:             secretstore.Scope{Mode: secretstore.ModeTest, Namespace: "boltertrader/test", KeyVersion: "v1"},
			})
			retired <- err
		}()
		waitForPostgresLockWait(t, ctx, postgresStore, "%FROM gateway_account_runtime_heads WHERE account_id=$1 FOR UPDATE%")
		if err := blocker.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if err := <-prepared; err != nil {
			t.Fatalf("pending preparation: %v", err)
		}
		if err := <-retired; err != nil {
			t.Fatalf("retirement after pending preparation: %v", err)
		}
		assertOnlyTombstone(t, ctx, postgresStore, active.AccountID)
	})
}

func TestPromotionRejectsPendingSnapshotClearedByRetirement(t *testing.T) {
	ctx := context.Background()
	postgresStore := newRuntimeSnapshotTestStore(t, ctx)
	active := runtimeSnapshotFixture(t, "018f47a8-2578-7d3c-9d7d-5e4f4a445e91", "018f47a8-2578-7d3c-9d7d-5e4f4a445e92", 1)
	prepareRuntimeSnapshotReady(t, ctx, postgresStore, active)
	if _, err := postgresStore.PromotePending(ctx, storepkg.PromotionRequest{AccountID: active.AccountID, OperationID: active.OperationID}); err != nil {
		t.Fatal(err)
	}
	freshRef, _ := secretstore.ParseOpaqueRef("AgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgI")
	pending := runtimeSnapshotFixtureWithRef(t, "018f47a8-2578-7d3c-9d7d-5e4f4a445e93", "018f47a8-2578-7d3c-9d7d-5e4f4a445e94", active.AccountID, 2, freshRef)
	prepareRuntimeSnapshotReady(t, ctx, postgresStore, pending)
	retireOperation, _ := secretstore.ParseOperationID("018f47a8-2578-7d3c-9d7d-5e4f4a445e95")
	if _, err := postgresStore.RetireAccount(ctx, storepkg.RetirementRequest{
		OperationID: retireOperation, AccountID: active.AccountID, ExpectedActiveFence: active.Fence,
		TombstoneRevision: active.Fence.DesiredStateRevision + 1,
		Scope:             secretstore.Scope{Mode: secretstore.ModeTest, Namespace: "boltertrader/test", KeyVersion: "v1"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := postgresStore.PromotePending(ctx, storepkg.PromotionRequest{
		AccountID: active.AccountID, OperationID: pending.OperationID, ExpectedActiveFence: active.Fence,
	}); !errors.Is(err, storepkg.ErrOperationConflict) {
		t.Fatalf("promotion after tombstone err=%v", err)
	}
	assertOnlyTombstone(t, ctx, postgresStore, active.AccountID)
}

func waitForPostgresLockWait(t *testing.T, ctx context.Context, postgresStore *Store, queryPattern string) {
	t.Helper()
	for {
		var waiting bool
		if err := postgresStore.pool.QueryRow(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM pg_stat_activity
				WHERE pid <> pg_backend_pid() AND wait_event_type='Lock' AND query LIKE $1
			)
		`, queryPattern).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		if err := ctx.Err(); err != nil {
			t.Fatalf("wait for PostgreSQL lock on %q: %v", queryPattern, err)
		}
	}
}

func assertOnlyTombstone(t *testing.T, ctx context.Context, postgresStore *Store, accountID domain.AccountID) {
	t.Helper()
	state, err := postgresStore.LoadRuntimeState(ctx, accountID)
	if err != nil || !state.Tombstoned || state.Active != nil || state.Pending != nil || len(state.Retiring) != 0 {
		t.Fatalf("contradictory retired state=%+v err=%v", state, err)
	}
}

func TestRotationFinalizationIsAtomicReplayableAndLeavesActiveHeadUnchanged(t *testing.T) {
	ctx := context.Background()
	postgresStore := newRuntimeSnapshotTestStore(t, ctx)
	active, retiring := preparePromotedRotation(t, ctx, postgresStore)
	request := rotationFinalizationRequest(active, retiring)

	if _, err := postgresStore.LookupRotationFinalization(ctx, request); !errors.Is(err, storepkg.ErrNotFound) {
		t.Fatalf("lookup before finalization err=%v", err)
	}
	wrongFences := request
	wrongFences.ExpectedActiveFence.DesiredStateRevision++
	wrongFences.ExpectedRetiringFence.DesiredStateRevision++
	if _, err := postgresStore.FinalizeRotation(ctx, wrongFences); !errors.Is(err, storepkg.ErrPromotionFenceConflict) {
		t.Fatalf("wrong finalization fences err=%v", err)
	}

	commandID := uuid.MustParse("018f47a8-2578-7d3c-9d7d-5e4f4a445cb1")
	handoffID := uuid.MustParse("018f47a8-2578-7d3c-9d7d-5e4f4a445cb2")
	if _, err := postgresStore.pool.Exec(ctx, `
		INSERT INTO idempotency_commands(command_id, principal_id, idempotency_key, account_id, product, payload_hash, request_payload, gateway_order_id, portable_client_order_id, state)
		VALUES ($1,'principal','rotation-finalization-handoff',$2,'spot','hash','{}',$3,2,'dispatching')
	`, commandID, retiring.AccountID, uuid.MustParse("018f47a8-2578-7d3c-9d7d-5e4f4a445cb3")); err != nil {
		t.Fatal(err)
	}
	if _, err := postgresStore.pool.Exec(ctx, `
		INSERT INTO gateway_order_handoffs(handoff_id,command_id,account_id,route_id,desired_state_revision,runtime_generation,credential_version,state)
		VALUES ($1,$2,$3,'spot',$4,$5,$6,'started')
	`, handoffID, commandID, retiring.AccountID, retiring.Fence.DesiredStateRevision,
		retiring.Fence.RuntimeGeneration, retiring.Fence.CredentialVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := postgresStore.FinalizeRotation(ctx, request); !errors.Is(err, storepkg.ErrHandoffDrainBlocked) {
		t.Fatalf("started retiring-fence handoff finalization err=%v", err)
	}
	assertRotationFinalizationDurableCounts(t, ctx, postgresStore, retiring, 0, "reconciling", 0)

	if _, err := postgresStore.pool.Exec(ctx, `UPDATE gateway_order_handoffs SET state='uncertain',resolved_at=now() WHERE handoff_id=$1`, handoffID); err != nil {
		t.Fatal(err)
	}
	first, err := postgresStore.FinalizeRotation(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if first.OperationID != active.OperationID || first.AccountID != active.AccountID ||
		first.ActiveFence != active.Fence || first.RetiringFence != retiring.Fence ||
		first.TombstoneRevision != active.Fence.DesiredStateRevision || first.RetiringState != "retired" ||
		first.SecretReferenceState != "tombstoned" || first.CompletedAt.IsZero() {
		t.Fatalf("finalization result=%+v", first)
	}
	replayed, err := postgresStore.FinalizeRotation(ctx, request)
	if err != nil || replayed != first {
		t.Fatalf("finalization replay=%+v err=%v first=%+v", replayed, err, first)
	}
	lookedUp, err := postgresStore.LookupRotationFinalization(ctx, request)
	if err != nil || lookedUp != first {
		t.Fatalf("finalization lookup=%+v err=%v first=%+v", lookedUp, err, first)
	}
	changedRequest := request
	changedRequest.Scope = secretstore.Scope{Mode: secretstore.ModeLive, Namespace: "boltertrader/live", KeyVersion: "v1"}
	if _, err := postgresStore.FinalizeRotation(ctx, changedRequest); !errors.Is(err, storepkg.ErrOperationConflict) {
		t.Fatalf("changed request replay err=%v", err)
	}
	if _, err := postgresStore.LookupRotationFinalization(ctx, changedRequest); !errors.Is(err, storepkg.ErrOperationConflict) {
		t.Fatalf("changed request lookup err=%v", err)
	}
	assertRotationFinalizationDurableCounts(t, ctx, postgresStore, retiring, 1, "retired", 1)

	state, err := postgresStore.LoadRuntimeState(ctx, active.AccountID)
	if err != nil || state.Active == nil || state.Active.SnapshotID != active.SnapshotID || state.Active.SecretRef != active.SecretRef ||
		state.Pending != nil || len(state.Retiring) != 0 {
		t.Fatalf("post-finalization runtime state=%+v err=%v", state, err)
	}
	activeRetired, err := postgresStore.IsSecretReferenceRetired(ctx, request.Scope, active.SecretRef)
	if err != nil || activeRetired {
		t.Fatalf("active reference retired=%t err=%v", activeRetired, err)
	}
	retiringRetired, err := postgresStore.IsSecretReferenceRetired(ctx, request.Scope, retiring.SecretRef)
	if err != nil || !retiringRetired {
		t.Fatalf("retiring reference retired=%t err=%v", retiringRetired, err)
	}

	later := runtimeSnapshotFixture(t, "018f47a8-2578-7d3c-9d7d-5e4f4a445ce1", "018f47a8-2578-7d3c-9d7d-5e4f4a445ce2", 3)
	laterRef, err := secretstore.ParseOpaqueRef("AgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgI")
	if err != nil {
		t.Fatal(err)
	}
	later.SecretRef = laterRef
	prepareRuntimeSnapshotReady(t, ctx, postgresStore, later)
	if _, err := postgresStore.PromotePending(ctx, storepkg.PromotionRequest{
		AccountID: later.AccountID, OperationID: later.OperationID, ExpectedActiveFence: active.Fence,
	}); err != nil {
		t.Fatal(err)
	}
	replayedAfterLaterState, err := postgresStore.FinalizeRotation(ctx, request)
	if err != nil || replayedAfterLaterState != first {
		t.Fatalf("finalization replay after later state=%+v err=%v first=%+v", replayedAfterLaterState, err, first)
	}
}

func TestRotationFinalizationReplaysConcurrentCommitAfterInitialProofMiss(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool := newTestPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	firstReplayMiss := make(chan struct{})
	releaseFirst := make(chan struct{})
	var replayMisses atomic.Int32
	postgresStore := newStoreForTest(pool, storeOptions{
		identity: testIdentityGenerator(),
		afterRotationFinalizationReplayMiss: func(ctx context.Context) error {
			if replayMisses.Add(1) != 1 {
				return nil
			}
			close(firstReplayMiss)
			select {
			case <-releaseFirst:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	})
	active, retiring := preparePromotedRotation(t, ctx, postgresStore)
	request := rotationFinalizationRequest(active, retiring)
	type finalizationCall struct {
		result storepkg.RotationFinalizationResult
		err    error
	}
	firstCall := make(chan finalizationCall, 1)
	go func() {
		result, err := postgresStore.FinalizeRotation(ctx, request)
		firstCall <- finalizationCall{result: result, err: err}
	}()
	select {
	case <-firstReplayMiss:
	case <-ctx.Done():
		t.Fatalf("wait for initial finalization proof miss: %v", ctx.Err())
	}
	released := false
	release := func() {
		if !released {
			close(releaseFirst)
			released = true
		}
	}
	defer release()

	committed, commitErr := postgresStore.FinalizeRotation(ctx, request)
	release()
	var replayed finalizationCall
	select {
	case replayed = <-firstCall:
	case <-ctx.Done():
		t.Fatalf("wait for concurrent finalization replay: %v", ctx.Err())
	}
	if commitErr != nil {
		t.Fatalf("concurrent finalization commit: %v", commitErr)
	}
	if replayed.err != nil || replayed.result != committed {
		t.Fatalf("concurrent finalization replay=%+v err=%v committed=%+v", replayed.result, replayed.err, committed)
	}
}

func TestRotationFinalizationRejectsPreexistingTombstoneWithoutPartialMutation(t *testing.T) {
	ctx := context.Background()
	postgresStore := newRuntimeSnapshotTestStore(t, ctx)
	active, retiring := preparePromotedRotation(t, ctx, postgresStore)
	request := rotationFinalizationRequest(active, retiring)
	if _, err := postgresStore.TombstoneSecretReference(ctx, storepkg.TombstoneRequest{
		Scope: request.Scope, SecretRef: retiring.SecretRef, AccountID: retiring.AccountID,
		TombstoneRevision: active.Fence.DesiredStateRevision,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := postgresStore.FinalizeRotation(ctx, request); !errors.Is(err, storepkg.ErrRetiredSecretReference) {
		t.Fatalf("pre-tombstoned finalization err=%v", err)
	}
	assertRotationFinalizationDurableCounts(t, ctx, postgresStore, retiring, 1, "reconciling", 0)
	state, err := postgresStore.LoadRuntimeState(ctx, active.AccountID)
	if err != nil || state.Active == nil || state.Active.SnapshotID != active.SnapshotID || len(state.Retiring) != 1 {
		t.Fatalf("runtime state after rejected finalization=%+v err=%v", state, err)
	}
}

func TestRotationFinalizationRejectsSharedReferenceAcrossRetiringGenerations(t *testing.T) {
	ctx := context.Background()
	postgresStore := newRuntimeSnapshotTestStore(t, ctx)
	refA, _ := secretstore.ParseOpaqueRef("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	refB, _ := secretstore.ParseOpaqueRef("AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE")
	refC, _ := secretstore.ParseOpaqueRef("AgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgI")
	snapshots := []storepkg.DesiredSnapshot{
		runtimeSnapshotFixtureWithRef(t, "018f47a8-2578-7d3c-9d7d-5e4f4a445d01", "018f47a8-2578-7d3c-9d7d-5e4f4a445d02", "account-runtime", 1, refA),
		runtimeSnapshotFixtureWithRef(t, "018f47a8-2578-7d3c-9d7d-5e4f4a445d03", "018f47a8-2578-7d3c-9d7d-5e4f4a445d04", "account-runtime", 2, refB),
		runtimeSnapshotFixtureWithRef(t, "018f47a8-2578-7d3c-9d7d-5e4f4a445d05", "018f47a8-2578-7d3c-9d7d-5e4f4a445d06", "account-runtime", 3, refA),
		runtimeSnapshotFixtureWithRef(t, "018f47a8-2578-7d3c-9d7d-5e4f4a445d07", "018f47a8-2578-7d3c-9d7d-5e4f4a445d08", "account-runtime", 4, refC),
	}
	for index, snapshot := range snapshots {
		prepareRuntimeSnapshotReady(t, ctx, postgresStore, snapshot)
		request := storepkg.PromotionRequest{AccountID: snapshot.AccountID, OperationID: snapshot.OperationID}
		if index > 0 {
			request.ExpectedActiveFence = snapshots[index-1].Fence
		}
		if _, err := postgresStore.PromotePending(ctx, request); err != nil {
			t.Fatal(err)
		}
	}
	request := rotationFinalizationRequest(snapshots[3], snapshots[2])
	if _, err := postgresStore.FinalizeRotation(ctx, request); !errors.Is(err, storepkg.ErrRetiredSecretReference) {
		t.Fatalf("shared retiring reference finalization err=%v", err)
	}
	assertRotationFinalizationDurableCounts(t, ctx, postgresStore, snapshots[2], 0, "reconciling", 0)
	var earlierState string
	if err := postgresStore.pool.QueryRow(ctx, `SELECT state FROM gateway_retiring_generations WHERE snapshot_id=$1`, snapshots[0].SnapshotID).Scan(&earlierState); err != nil {
		t.Fatal(err)
	}
	if earlierState != "reconciling" {
		t.Fatalf("earlier shared-reference retiring state=%q", earlierState)
	}
}

func TestRotationFinalizationRejectsCrossAccountReferenceUsers(t *testing.T) {
	for _, state := range []string{"active", "pending", "retiring"} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			postgresStore := newRuntimeSnapshotTestStore(t, ctx)
			active, retiring := preparePromotedRotationForAccount(t, ctx, postgresStore, "account-finalizing",
				"018f47a8-2578-7d3c-9d7d-5e4f4a445d21", "018f47a8-2578-7d3c-9d7d-5e4f4a445d22",
				"018f47a8-2578-7d3c-9d7d-5e4f4a445d23", "018f47a8-2578-7d3c-9d7d-5e4f4a445d24")

			other := runtimeSnapshotFixtureWithRef(t,
				"018f47a8-2578-7d3c-9d7d-5e4f4a445d25", "018f47a8-2578-7d3c-9d7d-5e4f4a445d26",
				"account-reference-user", 1, retiring.SecretRef)
			prepareRuntimeSnapshotReady(t, ctx, postgresStore, other)
			if state != "pending" {
				if _, err := postgresStore.PromotePending(ctx, storepkg.PromotionRequest{
					AccountID: other.AccountID, OperationID: other.OperationID,
				}); err != nil {
					t.Fatal(err)
				}
			}
			if state == "retiring" {
				otherRef, err := secretstore.ParseOpaqueRef("AgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgI")
				if err != nil {
					t.Fatal(err)
				}
				replacement := runtimeSnapshotFixtureWithRef(t,
					"018f47a8-2578-7d3c-9d7d-5e4f4a445d27", "018f47a8-2578-7d3c-9d7d-5e4f4a445d28",
					other.AccountID, 2, otherRef)
				prepareRuntimeSnapshotReady(t, ctx, postgresStore, replacement)
				if _, err := postgresStore.PromotePending(ctx, storepkg.PromotionRequest{
					AccountID: replacement.AccountID, OperationID: replacement.OperationID,
					ExpectedActiveFence: other.Fence,
				}); err != nil {
					t.Fatal(err)
				}
			}

			if _, err := postgresStore.FinalizeRotation(ctx, rotationFinalizationRequest(active, retiring)); !errors.Is(err, storepkg.ErrRetiredSecretReference) {
				t.Fatalf("cross-account %s reference finalization err=%v", state, err)
			}
			assertRotationFinalizationDurableCounts(t, ctx, postgresStore, retiring, 0, "reconciling", 0)
		})
	}
}

func TestRotationFinalizationAndPendingPreparationShareReferenceLock(t *testing.T) {
	t.Run("pending preparation", func(t *testing.T) {
		ctx := context.Background()
		postgresStore := newRuntimeSnapshotTestStore(t, ctx)
		ref, err := secretstore.ParseOpaqueRef("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
		if err != nil {
			t.Fatal(err)
		}
		blocker, err := postgresStore.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer blocker.Rollback(ctx)
		if _, err := blocker.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, ref.RawForTrustedBoundary()); err != nil {
			t.Fatal(err)
		}
		snapshot := runtimeSnapshotFixtureWithRef(t,
			"018f47a8-2578-7d3c-9d7d-5e4f4a445d31", "018f47a8-2578-7d3c-9d7d-5e4f4a445d32",
			"account-reference-lock", 1, ref)
		prepared := make(chan error, 1)
		go func() {
			_, err := postgresStore.PreparePending(ctx, snapshot)
			prepared <- err
		}()
		select {
		case err := <-prepared:
			t.Fatalf("pending preparation bypassed reference lock: %v", err)
		case <-time.After(50 * time.Millisecond):
		}
		if err := blocker.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if err := <-prepared; err != nil {
			t.Fatalf("pending preparation after reference lock release: %v", err)
		}
	})

	t.Run("rotation finalization", func(t *testing.T) {
		ctx := context.Background()
		postgresStore := newRuntimeSnapshotTestStore(t, ctx)
		active, retiring := preparePromotedRotation(t, ctx, postgresStore)
		blocker, err := postgresStore.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer blocker.Rollback(ctx)
		if _, err := blocker.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, retiring.SecretRef.RawForTrustedBoundary()); err != nil {
			t.Fatal(err)
		}
		finalized := make(chan error, 1)
		go func() {
			_, err := postgresStore.FinalizeRotation(ctx, rotationFinalizationRequest(active, retiring))
			finalized <- err
		}()
		select {
		case err := <-finalized:
			t.Fatalf("rotation finalization bypassed reference lock: %v", err)
		case <-time.After(50 * time.Millisecond):
		}
		if err := blocker.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if err := <-finalized; err != nil {
			t.Fatalf("rotation finalization after reference lock release: %v", err)
		}
	})
}

func TestRotationFinalizationSerializesLateRetiringFenceHandoff(t *testing.T) {
	ctx := context.Background()
	postgresStore := newRuntimeSnapshotTestStore(t, ctx)
	active, retiring := preparePromotedRotation(t, ctx, postgresStore)
	request := rotationFinalizationRequest(active, retiring)
	commandID := uuid.MustParse("018f47a8-2578-7d3c-9d7d-5e4f4a445d11")
	handoffID := domain.EventID("018f47a8-2578-7d3c-9d7d-5e4f4a445d12")
	if _, err := postgresStore.pool.Exec(ctx, `
		INSERT INTO idempotency_commands(command_id, principal_id, idempotency_key, account_id, product, payload_hash, request_payload, gateway_order_id, portable_client_order_id, state)
		VALUES ($1,'principal','rotation-race-handoff',$2,'spot','hash','{}',$3,3,'dispatching')
	`, commandID, retiring.AccountID, uuid.MustParse("018f47a8-2578-7d3c-9d7d-5e4f4a445d13")); err != nil {
		t.Fatal(err)
	}

	blocker, err := postgresStore.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(ctx)
	if _, err := blocker.Exec(ctx, `SELECT 1 FROM gateway_retiring_generations WHERE snapshot_id=$1 FOR UPDATE`, retiring.SnapshotID); err != nil {
		t.Fatal(err)
	}
	finalizeErr := make(chan error, 1)
	go func() {
		_, err := postgresStore.FinalizeRotation(ctx, request)
		finalizeErr <- err
	}()
	waitForRuntimeHeadWriteLock(t, ctx, postgresStore, active.AccountID)

	handoffErr := make(chan error, 1)
	go func() {
		_, err := postgresStore.BeginOrderHandoff(ctx, storepkg.BeginOrderHandoffRequest{
			HandoffID: handoffID, CommandID: domain.CommandID(commandID.String()), AccountID: retiring.AccountID,
			RouteID: "spot", Fence: retiring.Fence,
		})
		handoffErr <- err
	}()
	select {
	case err := <-handoffErr:
		t.Fatalf("handoff returned before finalization released account lock: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-finalizeErr; err != nil {
		t.Fatalf("finalization err=%v", err)
	}
	if err := <-handoffErr; !errors.Is(err, storepkg.ErrPromotionFenceConflict) {
		t.Fatalf("late retiring-fence handoff err=%v", err)
	}
	var started int
	if err := postgresStore.pool.QueryRow(ctx, `SELECT count(*) FROM gateway_order_handoffs WHERE handoff_id=$1 AND state='started'`, handoffID).Scan(&started); err != nil {
		t.Fatal(err)
	}
	if started != 0 {
		t.Fatalf("late handoff started rows=%d", started)
	}
}

func TestRotationFinalizationProofRejectsMismatchedAndCrossAccountRows(t *testing.T) {
	ctx := context.Background()
	postgresStore := newRuntimeSnapshotTestStore(t, ctx)
	firstActive, firstRetiring := preparePromotedRotationForAccount(t, ctx, postgresStore, "account-proof-one",
		"018f47a8-2578-7d3c-9d7d-5e4f4a445e01", "018f47a8-2578-7d3c-9d7d-5e4f4a445e02",
		"018f47a8-2578-7d3c-9d7d-5e4f4a445e03", "018f47a8-2578-7d3c-9d7d-5e4f4a445e04")
	secondActive, _ := preparePromotedRotationForAccount(t, ctx, postgresStore, "account-proof-two",
		"018f47a8-2578-7d3c-9d7d-5e4f4a445e05", "018f47a8-2578-7d3c-9d7d-5e4f4a445e06",
		"018f47a8-2578-7d3c-9d7d-5e4f4a445e07", "018f47a8-2578-7d3c-9d7d-5e4f4a445e08")
	scope := secretstore.Scope{Mode: secretstore.ModeTest, Namespace: "boltertrader/test", KeyVersion: "v1"}
	if _, err := postgresStore.TombstoneSecretReference(ctx, storepkg.TombstoneRequest{
		Scope: scope, SecretRef: firstRetiring.SecretRef, AccountID: firstRetiring.AccountID,
		TombstoneRevision: firstActive.Fence.DesiredStateRevision,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := postgresStore.pool.Exec(ctx, `UPDATE gateway_retiring_generations SET state='retired' WHERE snapshot_id=$1`, firstRetiring.SnapshotID); err != nil {
		t.Fatal(err)
	}
	insertProof := func(operationID secretstore.OperationID, accountID domain.AccountID, activeID, retiringID uuid.UUID, activeFence, retiringFence domain.AccountFence) error {
		_, err := postgresStore.pool.Exec(ctx, `
			INSERT INTO gateway_rotation_finalizations(
				operation_id,account_id,request_digest,active_snapshot_id,retiring_snapshot_id,
				expected_active_desired_state_revision,expected_active_runtime_generation,expected_active_credential_version,
				expected_retiring_desired_state_revision,expected_retiring_runtime_generation,expected_retiring_credential_version,
				retired_secret_ref,tombstone_revision,mode,namespace,key_version,
				retiring_state,secret_reference_state,completed_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,'retired','tombstoned',now())
		`, operationID.String(), accountID, strings.Repeat("a", 64), activeID, retiringID,
			activeFence.DesiredStateRevision, activeFence.RuntimeGeneration, activeFence.CredentialVersion,
			retiringFence.DesiredStateRevision, retiringFence.RuntimeGeneration, retiringFence.CredentialVersion,
			firstRetiring.SecretRef.RawForTrustedBoundary(), activeFence.DesiredStateRevision,
			scope.Mode, scope.Namespace, scope.KeyVersion)
		return err
	}
	if err := insertProof(secondActive.OperationID, firstActive.AccountID, firstActive.SnapshotID, firstRetiring.SnapshotID, firstActive.Fence, firstRetiring.Fence); err == nil {
		t.Fatal("proof accepted operation bound to a different active snapshot")
	}
	if err := insertProof(secondActive.OperationID, secondActive.AccountID, secondActive.SnapshotID, firstRetiring.SnapshotID, secondActive.Fence, firstRetiring.Fence); err == nil {
		t.Fatal("proof accepted cross-account retiring snapshot and tombstone")
	}
}

func waitForRuntimeHeadWriteLock(t *testing.T, ctx context.Context, postgresStore *Store, accountID domain.AccountID) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var activeID uuid.UUID
		err := postgresStore.pool.QueryRow(ctx, `SELECT active_snapshot_id FROM gateway_account_runtime_heads WHERE account_id=$1 FOR UPDATE NOWAIT`, accountID).Scan(&activeID)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "55P03" {
			return
		}
		if err != nil {
			t.Fatalf("probe runtime head lock: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("finalization did not acquire runtime head write lock")
}

func preparePromotedRotation(t *testing.T, ctx context.Context, postgresStore *Store) (storepkg.DesiredSnapshot, storepkg.DesiredSnapshot) {
	t.Helper()
	return preparePromotedRotationForAccount(t, ctx, postgresStore, "account-runtime",
		"018f47a8-2578-7d3c-9d7d-5e4f4a445cc1", "018f47a8-2578-7d3c-9d7d-5e4f4a445cc2",
		"018f47a8-2578-7d3c-9d7d-5e4f4a445cc3", "018f47a8-2578-7d3c-9d7d-5e4f4a445cc4")
}

func preparePromotedRotationForAccount(t *testing.T, ctx context.Context, postgresStore *Store, accountID domain.AccountID, retiringOperationID, retiringSnapshotID, activeOperationID, activeSnapshotID string) (storepkg.DesiredSnapshot, storepkg.DesiredSnapshot) {
	t.Helper()
	refA, _ := secretstore.ParseOpaqueRef("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	retiring := runtimeSnapshotFixtureWithRef(t, retiringOperationID, retiringSnapshotID, accountID, 1, refA)
	prepareRuntimeSnapshotReady(t, ctx, postgresStore, retiring)
	if _, err := postgresStore.PromotePending(ctx, storepkg.PromotionRequest{AccountID: retiring.AccountID, OperationID: retiring.OperationID}); err != nil {
		t.Fatal(err)
	}
	active := runtimeSnapshotFixtureWithRef(t, activeOperationID, activeSnapshotID, accountID, 2, refA)
	activeRef, err := secretstore.ParseOpaqueRef("AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE")
	if err != nil {
		t.Fatal(err)
	}
	active.SecretRef = activeRef
	prepareRuntimeSnapshotReady(t, ctx, postgresStore, active)
	if _, err := postgresStore.PromotePending(ctx, storepkg.PromotionRequest{
		AccountID: active.AccountID, OperationID: active.OperationID, ExpectedActiveFence: retiring.Fence,
	}); err != nil {
		t.Fatal(err)
	}
	return active, retiring
}

func runtimeSnapshotFixtureWithRef(t *testing.T, operationID, snapshotID string, accountID domain.AccountID, generation uint64, ref secretstore.OpaqueRef) storepkg.DesiredSnapshot {
	t.Helper()
	snapshot := runtimeSnapshotFixture(t, operationID, snapshotID, generation)
	snapshot.AccountID = accountID
	snapshot.Fence.AccountID = accountID
	snapshot.SecretRef = ref
	return snapshot
}

func rotationFinalizationRequest(active, retiring storepkg.DesiredSnapshot) storepkg.RotationFinalizationRequest {
	return storepkg.RotationFinalizationRequest{
		OperationID: active.OperationID, AccountID: active.AccountID,
		ExpectedActiveFence: active.Fence, ExpectedRetiringFence: retiring.Fence,
		Scope: secretstore.Scope{Mode: secretstore.ModeTest, Namespace: "boltertrader/test", KeyVersion: "v1"},
	}
}

func assertRotationFinalizationDurableCounts(t *testing.T, ctx context.Context, postgresStore *Store, retiring storepkg.DesiredSnapshot, tombstones int, retiringState string, proofs int) {
	t.Helper()
	var gotTombstones, gotProofs int
	var gotRetiringState string
	if err := postgresStore.pool.QueryRow(ctx, `SELECT count(*) FROM gateway_retired_secret_refs WHERE secret_ref=$1`, retiring.SecretRef.RawForTrustedBoundary()).Scan(&gotTombstones); err != nil {
		t.Fatal(err)
	}
	if err := postgresStore.pool.QueryRow(ctx, `SELECT state FROM gateway_retiring_generations WHERE snapshot_id=$1`, retiring.SnapshotID).Scan(&gotRetiringState); err != nil {
		t.Fatal(err)
	}
	if err := postgresStore.pool.QueryRow(ctx, `SELECT count(*) FROM gateway_rotation_finalizations WHERE retiring_snapshot_id=$1`, retiring.SnapshotID).Scan(&gotProofs); err != nil {
		t.Fatal(err)
	}
	if gotTombstones != tombstones || gotRetiringState != retiringState || gotProofs != proofs {
		t.Fatalf("durable state tombstones=%d retiring_state=%q proofs=%d, want %d %q %d", gotTombstones, gotRetiringState, gotProofs, tombstones, retiringState, proofs)
	}
}

func newRuntimeSnapshotTestStore(t *testing.T, ctx context.Context) *Store {
	t.Helper()
	pool := newTestPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return newStoreForTest(pool, storeOptions{identity: testIdentityGenerator()})
}

func runtimeSnapshotFixture(t *testing.T, operationID, snapshotID string, generation uint64) storepkg.DesiredSnapshot {
	t.Helper()
	op, err := secretstore.ParseOperationID(operationID)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := secretstore.ParseOpaqueRef("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	if err != nil {
		t.Fatal(err)
	}
	fence := domain.AccountFence{AccountID: "account-runtime", DesiredStateRevision: generation, RuntimeGeneration: generation, CredentialVersion: generation}
	return storepkg.DesiredSnapshot{
		SnapshotID: uuid.MustParse(snapshotID), OperationID: op, AccountID: fence.AccountID,
		Venue: "binance", NativeEnvironment: "demo", SecretRef: ref, Fence: fence,
		RequestedState: storepkg.DesiredActive,
		Routes: []storepkg.RouteDefinition{
			{RouteID: "spot", Product: "spot", RouteProfileID: "binance.spot.demo.v1", RequestedState: storepkg.RouteActive},
			{RouteID: "perp-usdt", Product: "perp", SettlementAsset: "USDT", RouteProfileID: "binance.perp.usdt.demo.v1", RequestedState: storepkg.RouteActive},
		},
	}
}

func observation(snapshot storepkg.DesiredSnapshot, revision uint64, routeID domain.RouteID, state storepkg.CandidateState) storepkg.CandidateObservation {
	return storepkg.CandidateObservation{OperationID: snapshot.OperationID, ObservationRevision: revision, RouteID: routeID, State: state}
}

func prepareRuntimeSnapshotReady(t *testing.T, ctx context.Context, postgresStore *Store, snapshot storepkg.DesiredSnapshot) {
	t.Helper()
	if _, err := postgresStore.PreparePending(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	for _, route := range snapshot.Routes {
		if err := postgresStore.RecordCandidateObservation(ctx, observation(snapshot, 1, route.RouteID, storepkg.CandidateReady)); err != nil {
			t.Fatal(err)
		}
	}
	if err := postgresStore.MarkCandidateReady(ctx, snapshot.OperationID); err != nil {
		t.Fatal(err)
	}
}
