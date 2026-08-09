package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/QuantProcessing/boltertrader/gateway/domain"
	storepkg "github.com/QuantProcessing/boltertrader/gateway/store"
	"github.com/QuantProcessing/boltertrader/secretstore"
	"github.com/google/uuid"
)

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
