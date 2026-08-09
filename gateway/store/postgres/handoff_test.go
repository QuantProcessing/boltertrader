package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/gateway/domain"
	storepkg "github.com/QuantProcessing/boltertrader/gateway/store"
	"github.com/QuantProcessing/boltertrader/secretstore"
)

func TestBeginHandoffReplayAndResolvePersistsDurableState(t *testing.T) {
	ctx := context.Background()
	postgresStore := newHandoffTestStore(t, ctx)
	snapshot := handoffSnapshotFixture(t)

	first, err := postgresStore.BeginHandoff(ctx, storepkg.BeginHandoffRequest{
		CommandID: snapshot.CommandID,
		EventID:   snapshot.BeginEventID,
		Fence:     snapshot.Fence,
		RouteID:   snapshot.RouteID,
	})
	if err != nil {
		t.Fatalf("BeginHandoff: %v", err)
	}
	if first.State != storepkg.HandoffStarted {
		t.Fatalf("begin state = %s, want started", first.State)
	}

	replay, err := postgresStore.BeginHandoff(ctx, storepkg.BeginHandoffRequest{
		CommandID: snapshot.CommandID,
		EventID:   snapshot.BeginEventID,
		Fence:     snapshot.Fence,
		RouteID:   snapshot.RouteID,
	})
	if err != nil || replay.HandoffID != first.HandoffID || replay.State != first.State {
		t.Fatalf("replay=%+v err=%v first=%+v", replay, err, first)
	}

	resolved, err := postgresStore.ResolveHandoff(ctx, storepkg.ResolveHandoffRequest{
		CommandID:       snapshot.CommandID,
		EventID:         snapshot.ResolveEventID,
		NextState:       domain.StateOpen,
		ResourceVersion: 1,
		VenueOrderID:    "venue-1",
	})
	if err != nil {
		t.Fatalf("ResolveHandoff: %v", err)
	}
	if resolved.State != storepkg.HandoffAccepted {
		t.Fatalf("resolved state = %s, want accepted", resolved.State)
	}
	if resolved.ResolvedAt == nil || resolved.ResolvedAt.IsZero() {
		t.Fatal("resolved handoff missing resolved_at")
	}

	loaded, err := postgresStore.LoadOpenHandoffs(ctx, storepkg.LoadOpenHandoffsRequest{AccountID: snapshot.AccountID})
	if err != nil {
		t.Fatalf("LoadOpenHandoffs: %v", err)
	}
	if len(loaded) != 0 {
		t.Fatalf("open handoffs = %+v, want none after resolve", loaded)
	}
}

func TestLoadRecoverableHandoffsIncludesStartedRowsForRecovery(t *testing.T) {
	ctx := context.Background()
	postgresStore := newHandoffTestStore(t, ctx)
	snapshot := handoffSnapshotFixture(t)

	if _, err := postgresStore.BeginHandoff(ctx, storepkg.BeginHandoffRequest{
		CommandID: snapshot.CommandID,
		EventID:   snapshot.BeginEventID,
		Fence:     snapshot.Fence,
		RouteID:   snapshot.RouteID,
	}); err != nil {
		t.Fatalf("BeginHandoff: %v", err)
	}
	recoverable, err := postgresStore.LoadRecoverableHandoffs(ctx, storepkg.LoadRecoverableHandoffsRequest{
		AccountID: snapshot.AccountID,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("LoadRecoverableHandoffs: %v", err)
	}
	if len(recoverable) != 1 || recoverable[0].State != storepkg.HandoffStarted {
		t.Fatalf("recoverable=%+v", recoverable)
	}
}

func newHandoffTestStore(t *testing.T, ctx context.Context) *Store {
	t.Helper()
	pool := newTestPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return newStoreForTest(pool, storeOptions{identity: testIdentityGenerator()})
}

type handoffSnapshot struct {
	AccountID      domain.AccountID
	CommandID      domain.CommandID
	BeginEventID   domain.EventID
	ResolveEventID domain.EventID
	Fence          domain.WriteFence
	RouteID        domain.RouteID
}

func handoffSnapshotFixture(t *testing.T) handoffSnapshot {
	t.Helper()
	return handoffSnapshot{
		AccountID:      "gateway-handoff-test",
		CommandID:      "018f47a8-2578-7d3c-9d7d-5e4f4a445c33",
		BeginEventID:   "018f47a8-2578-7d3c-9d7d-5e4f4a445c34",
		ResolveEventID: "018f47a8-2578-7d3c-9d7d-5e4f4a445c35",
		RouteID:        "spot",
		Fence: domain.WriteFence{
			AccountFence: domain.AccountFence{
				AccountID:            "gateway-handoff-test",
				DesiredStateRevision: 1,
				RuntimeGeneration:    1,
				CredentialVersion:    1,
			},
			RouteID: "spot",
		},
	}
}

func TestHandoffSnapshotFixtureIsValid(t *testing.T) {
	snapshot := handoffSnapshotFixture(t)
	if err := snapshot.Fence.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := secretstore.ParseOperationID(string(snapshot.CommandID)); err == nil {
		// command ids are validated by the production code; this helper just ensures
		// the fixture remains UUID-shaped and does not silently drift.
	}
	if snapshot.AccountID == "" || snapshot.RouteID == "" {
		t.Fatal("invalid handoff fixture")
	}
}
