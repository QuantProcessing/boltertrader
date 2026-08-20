package postgres

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/gateway/domain"
	storepkg "github.com/QuantProcessing/boltertrader/gateway/store"
	"github.com/QuantProcessing/boltertrader/secretstore"
)

func TestMigrationPromotionOperationSetDigestIsOrderIndependent(t *testing.T) {
	items := []storepkg.MigrationPromotionItem{
		{AccountID: "account-b", OperationID: "018f47a8-2578-7d3c-9d7d-5e4f4a445d12", RuntimeID: "018f47a8-2578-7d3c-9d7d-5e4f4a445e12", RouteSetSHA256: strings.Repeat("b", 64), InputSHA256: strings.Repeat("c", 64)},
		{AccountID: "account-a", OperationID: "018f47a8-2578-7d3c-9d7d-5e4f4a445d11", RuntimeID: "018f47a8-2578-7d3c-9d7d-5e4f4a445e11", RouteSetSHA256: strings.Repeat("d", 64), InputSHA256: strings.Repeat("e", 64)},
	}
	request := storepkg.MigrationPromotionRequest{MigrationID: "G010", FenceSHA256: strings.Repeat("a", 64), Items: items}
	first, err := request.OperationSetSHA256()
	if err != nil {
		t.Fatal(err)
	}
	slices.Reverse(request.Items)
	second, err := request.OperationSetSHA256()
	if err != nil || first != second {
		t.Fatalf("first=%s second=%s err=%v", first, second, err)
	}
}

func TestMigrationPromotionRejectsPartialIdentityAndDuplicateAccount(t *testing.T) {
	valid := storepkg.MigrationPromotionItem{AccountID: "account-a", OperationID: "018f47a8-2578-7d3c-9d7d-5e4f4a445d11", RuntimeID: "018f47a8-2578-7d3c-9d7d-5e4f4a445e11", RouteSetSHA256: strings.Repeat("b", 64), InputSHA256: strings.Repeat("c", 64)}
	for _, items := range [][]storepkg.MigrationPromotionItem{
		{{AccountID: "account-a"}},
		{valid, valid},
	} {
		request := storepkg.MigrationPromotionRequest{MigrationID: "G010", FenceSHA256: strings.Repeat("a", 64), Items: items}
		if _, err := request.OperationSetSHA256(); !errors.Is(err, storepkg.ErrMigrationPromotionConflict) {
			t.Fatalf("items=%+v err=%v", items, err)
		}
	}
}

func TestMigrationPromotionEmptySet(t *testing.T) {
	ctx := context.Background()
	postgresStore := newRuntimeSnapshotTestStore(t, ctx)
	request := storepkg.MigrationPromotionRequest{MigrationID: "G010", FenceSHA256: strings.Repeat("a", 64), Items: []storepkg.MigrationPromotionItem{}}
	result, err := postgresStore.PromoteMigrationPendingSet(ctx, request)
	if err != nil || !result.Replayed || result.Before != result.After || result.After.OperationSetSHA256 == "" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestMigrationPromotionExactSetReplayAndConflicts(t *testing.T) {
	ctx := context.Background()
	postgresStore := newRuntimeSnapshotTestStore(t, ctx)
	first := runtimeSnapshotFixture(t, "018f47a8-2578-7d3c-9d7d-5e4f4a445d21", "018f47a8-2578-7d3c-9d7d-5e4f4a445d22", 1)
	pending, err := postgresStore.PreparePending(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	request := migrationPromotionRequest(t, pending.DesiredSnapshot)
	result, err := postgresStore.PromoteMigrationPendingSet(ctx, request)
	if err != nil || result.Replayed || result.Before.PendingSHA256 == result.After.ActiveSHA256 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	replay, err := postgresStore.PromoteMigrationPendingSet(ctx, request)
	if err != nil || !replay.Replayed || replay.After != result.After {
		t.Fatalf("replay=%+v err=%v first=%+v", replay, err, result)
	}

	conflict := request
	conflict.Items[0].OperationID = "018f47a8-2578-7d3c-9d7d-5e4f4a445d23"
	if _, err := postgresStore.PromoteMigrationPendingSet(ctx, conflict); !errors.Is(err, storepkg.ErrMigrationPromotionConflict) {
		t.Fatalf("conflicting active err=%v", err)
	}
}

func TestMigrationPromotionRejectsUnexpectedPendingHead(t *testing.T) {
	ctx := context.Background()
	postgresStore := newRuntimeSnapshotTestStore(t, ctx)
	first := runtimeSnapshotFixtureWithRef(t, "018f47a8-2578-7d3c-9d7d-5e4f4a445d31", "018f47a8-2578-7d3c-9d7d-5e4f4a445d32", domain.AccountID("account-a"), 1, migrationPromotionRef(t, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"))
	second := runtimeSnapshotFixtureWithRef(t, "018f47a8-2578-7d3c-9d7d-5e4f4a445d33", "018f47a8-2578-7d3c-9d7d-5e4f4a445d34", domain.AccountID("account-b"), 1, migrationPromotionRef(t, "AgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgI"))
	firstPending, err := postgresStore.PreparePending(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := postgresStore.PreparePending(ctx, second); err != nil {
		t.Fatal(err)
	}
	if _, err := postgresStore.PromoteMigrationPendingSet(ctx, migrationPromotionRequest(t, firstPending.DesiredSnapshot)); !errors.Is(err, storepkg.ErrMigrationPromotionConflict) {
		t.Fatalf("unexpected pending err=%v", err)
	}
}

func TestMigrationPromotionRejectsUnexpectedActiveHead(t *testing.T) {
	ctx := context.Background()
	postgresStore := newRuntimeSnapshotTestStore(t, ctx)
	first := runtimeSnapshotFixtureWithRef(t, "018f47a8-2578-7d3c-9d7d-5e4f4a445d41", "018f47a8-2578-7d3c-9d7d-5e4f4a445d42", domain.AccountID("account-a"), 1, migrationPromotionRef(t, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"))
	firstPending, err := postgresStore.PreparePending(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := postgresStore.PromoteMigrationPendingSet(ctx, migrationPromotionRequest(t, firstPending.DesiredSnapshot)); err != nil {
		t.Fatal(err)
	}
	empty := storepkg.MigrationPromotionRequest{MigrationID: "G010", FenceSHA256: strings.Repeat("f", 64), Items: []storepkg.MigrationPromotionItem{}}
	if _, err := postgresStore.PromoteMigrationPendingSet(ctx, empty); !errors.Is(err, storepkg.ErrMigrationPromotionConflict) {
		t.Fatalf("empty request accepted unexpected active head: %v", err)
	}

	second := runtimeSnapshotFixtureWithRef(t, "018f47a8-2578-7d3c-9d7d-5e4f4a445d43", "018f47a8-2578-7d3c-9d7d-5e4f4a445d44", domain.AccountID("account-b"), 1, migrationPromotionRef(t, "AgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgI"))
	secondPending, err := postgresStore.PreparePending(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := postgresStore.PromoteMigrationPendingSet(ctx, migrationPromotionRequest(t, secondPending.DesiredSnapshot)); !errors.Is(err, storepkg.ErrMigrationPromotionConflict) {
		t.Fatalf("nonempty request accepted extra active head: %v", err)
	}
}

func migrationPromotionRequest(t *testing.T, snapshot storepkg.DesiredSnapshot) storepkg.MigrationPromotionRequest {
	t.Helper()
	routeSHA, err := storepkg.MigrationRouteSetSHA256(snapshot.Routes)
	if err != nil {
		t.Fatal(err)
	}
	return storepkg.MigrationPromotionRequest{
		MigrationID: "G010", FenceSHA256: strings.Repeat("f", 64),
		Items: []storepkg.MigrationPromotionItem{{
			AccountID: snapshot.AccountID, OperationID: snapshot.OperationID.String(), RuntimeID: snapshot.Fence.RuntimeID,
			RouteSetSHA256: routeSHA, InputSHA256: snapshot.RequestDigest,
		}},
	}
}

func migrationPromotionRef(t *testing.T, raw string) secretstore.OpaqueRef {
	t.Helper()
	ref, err := secretstore.ParseOpaqueRef(raw)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}
