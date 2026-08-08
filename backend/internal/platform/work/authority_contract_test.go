package work

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/backend/internal/platform/orders"
)

func TestRegistrationRejectsActiveTakeoverAndReplaysCommittedResult(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	service := New("boltertrader-product-platform-upgrade/v4", 15*time.Second)
	service.SetClock(func() time.Time { return now })

	request := RegistrationRequest{WorkerID: "worker-a", InstanceID: "instance-a", ProductID: "funding", Mode: "test", ContractVersion: "boltertrader-product-platform-upgrade/v4", IdempotencyKey: "register-a"}
	first, err := service.RegisterResult(request)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := service.RegisterResult(request)
	if err != nil || replay.Generation != first.Generation || !replay.RegisteredAt.Equal(first.RegisteredAt) {
		t.Fatalf("registration replay=%+v err=%v first=%+v", replay, err, first)
	}
	request.InstanceID, request.IdempotencyKey = "instance-b", "register-b"
	if _, err := service.RegisterResult(request); !errors.Is(err, ErrWorkerAlreadyActive) {
		t.Fatalf("active takeover error=%v", err)
	}
}

func TestClaimReplayNeverClaimsSecondItem(t *testing.T) {
	service, generation := readyService(t)
	request := ClaimRequest{WorkerID: "worker-a", WorkerGeneration: generation, InstanceID: "instance-a", IdempotencyKey: "claim-a", MaxItems: 1}
	first, err := service.ClaimPage(request)
	if err != nil || first.WorkItem == nil || first.Lease == nil {
		t.Fatalf("first claim=%+v err=%v", first, err)
	}
	replay, err := service.ClaimPage(request)
	if err != nil || replay.WorkItem == nil || replay.Lease == nil || replay.Lease.LeaseID != first.Lease.LeaseID {
		t.Fatalf("claim replay=%+v err=%v first=%+v", replay, err, first)
	}
}

func TestIntentReplayRequiresCurrentLeaseAndSurvivesReclaim(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	item := WorkItem{WorkItemID: "work-replay", WorkKind: "reconcile", LineageID: "lineage-replay", WorkRevision: 1, ProductID: "funding", Mode: "test", Status: "open", FactsCursor: "cursor-replay", AccountFences: json.RawMessage(`[{"accountId":"account-long","desiredStateRevision":1,"runtimeGeneration":1,"credentialVersion":1},{"accountId":"account-short","desiredStateRevision":1,"runtimeGeneration":1,"credentialVersion":1}]`), Payload: json.RawMessage(`{}`), MaxAttempts: 5, NotBefore: now, CreatedAt: now, UpdatedAt: now}
	service := NewWithStore("boltertrader-product-platform-upgrade/v4", 15*time.Second, NewMemoryStoreWithSnapshot([]WorkItem{item}, nil))
	service.SetClock(func() time.Time { return now })
	registration, err := service.RegisterResult(RegistrationRequest{WorkerID: "worker-a", InstanceID: "instance-a", ProductID: "funding", Mode: "test", ContractVersion: "boltertrader-product-platform-upgrade/v4", IdempotencyKey: "register-a"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Heartbeat("worker-a", HeartbeatRequest{WorkerGeneration: registration.Generation, InstanceID: "instance-a", State: "ready"}); err != nil {
		t.Fatal(err)
	}
	claim, err := service.ClaimPage(ClaimRequest{WorkerID: "worker-a", WorkerGeneration: registration.Generation, InstanceID: "instance-a", IdempotencyKey: "claim-a", MaxItems: 1})
	if err != nil {
		t.Fatal(err)
	}
	request := IntentRequest{WorkerID: "worker-a", WorkerGeneration: registration.Generation, WorkItemID: claim.WorkItem.WorkItemID, WorkRevision: claim.WorkItem.WorkRevision, LeaseGeneration: claim.Lease.LeaseGeneration, IdempotencyKey: "intent-a", IntentKind: "funding.reconcile", FactsCursor: claim.WorkItem.FactsCursor, AccountFences: claim.WorkItem.AccountFences, Payload: json.RawMessage(`{}`)}
	first, err := service.IntentResult(claim.Lease.LeaseID, request)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(31 * time.Second)
	if _, err := service.IntentResult(claim.Lease.LeaseID, request); !errors.Is(err, ErrLeaseStale) {
		t.Fatalf("expired replay error=%v want %v", err, ErrLeaseStale)
	}
	newIntent := request
	newIntent.IdempotencyKey = "intent-new"
	if _, err := service.IntentResult(claim.Lease.LeaseID, newIntent); !errors.Is(err, ErrLeaseStale) {
		t.Fatalf("new intent after expiry error=%v", err)
	}

	replacement, err := service.RegisterResult(RegistrationRequest{WorkerID: "worker-a", InstanceID: "instance-b", ProductID: "funding", Mode: "test", ContractVersion: "boltertrader-product-platform-upgrade/v4", IdempotencyKey: "register-b"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Heartbeat("worker-a", HeartbeatRequest{WorkerGeneration: replacement.Generation, InstanceID: "instance-b", State: "ready"}); err != nil {
		t.Fatal(err)
	}
	claimRequest := ClaimRequest{WorkerID: "worker-a", WorkerGeneration: replacement.Generation, InstanceID: "instance-b", IdempotencyKey: "claim-expire", MaxItems: 1}
	if page, err := service.ClaimPage(claimRequest); err != nil || page.WorkItem != nil {
		t.Fatalf("claim while retry backoff starts=%+v err=%v", page, err)
	}
	now = now.Add(time.Second)
	if _, err := service.Heartbeat("worker-a", HeartbeatRequest{WorkerGeneration: replacement.Generation, InstanceID: "instance-b", State: "ready"}); err != nil {
		t.Fatal(err)
	}
	claimRequest.IdempotencyKey = "claim-reclaimed"
	reclaimed, err := service.ClaimPage(claimRequest)
	if err != nil || reclaimed.WorkItem == nil || reclaimed.Lease == nil {
		t.Fatalf("reclaimed claim=%+v err=%v", reclaimed, err)
	}
	reclaimedIntent := request
	reclaimedIntent.WorkerGeneration = replacement.Generation
	reclaimedIntent.LeaseGeneration = reclaimed.Lease.LeaseGeneration
	replay, err := service.IntentResult(reclaimed.Lease.LeaseID, reclaimedIntent)
	if err != nil || replay["command"].(orders.Command).CommandID != first["command"].(orders.Command).CommandID {
		t.Fatalf("reclaimed replay=%+v err=%v first=%+v", replay, err, first)
	}
	reclaimedIntent.Payload = json.RawMessage(`{"changed":true}`)
	if _, err := service.IntentResult(reclaimed.Lease.LeaseID, reclaimedIntent); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed reclaimed intent error=%v", err)
	}
}

func TestClaimHonorsNotBefore(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	item := WorkItem{WorkItemID: "work-future", WorkKind: "open", LineageID: "lineage-future", WorkRevision: 1, ProductID: "funding", Mode: "test", Status: "open", FactsCursor: "cursor-future", AccountFences: json.RawMessage(`[{"accountId":"account-long","desiredStateRevision":1,"runtimeGeneration":1,"credentialVersion":1},{"accountId":"account-short","desiredStateRevision":1,"runtimeGeneration":1,"credentialVersion":1}]`), Payload: json.RawMessage(`{}`), MaxAttempts: 5, NotBefore: now.Add(time.Minute), CreatedAt: now, UpdatedAt: now}
	service := NewWithStore("boltertrader-product-platform-upgrade/v4", 15*time.Second, NewMemoryStoreWithSnapshot([]WorkItem{item}, nil))
	service.SetClock(func() time.Time { return now })
	registration, err := service.RegisterResult(RegistrationRequest{WorkerID: "worker-a", InstanceID: "instance-a", ProductID: "funding", Mode: "test", ContractVersion: "boltertrader-product-platform-upgrade/v4", IdempotencyKey: "register-a"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Heartbeat("worker-a", HeartbeatRequest{WorkerGeneration: registration.Generation, InstanceID: "instance-a", State: "ready"}); err != nil {
		t.Fatal(err)
	}
	page, err := service.ClaimPage(ClaimRequest{WorkerID: "worker-a", WorkerGeneration: registration.Generation, InstanceID: "instance-a", IdempotencyKey: "claim-before", MaxItems: 1})
	if err != nil || page.WorkItem != nil {
		t.Fatalf("claim before not_before=%+v err=%v", page, err)
	}
	now = now.Add(time.Minute)
	if _, err := service.Heartbeat("worker-a", HeartbeatRequest{WorkerGeneration: registration.Generation, InstanceID: "instance-a", State: "ready"}); err != nil {
		t.Fatal(err)
	}
	page, err = service.ClaimPage(ClaimRequest{WorkerID: "worker-a", WorkerGeneration: registration.Generation, InstanceID: "instance-a", IdempotencyKey: "claim-due", MaxItems: 1})
	if err != nil || page.WorkItem == nil || page.WorkItem.WorkItemID != item.WorkItemID {
		t.Fatalf("claim when due=%+v err=%v", page, err)
	}
}

func TestPersistedWorkerModeCannotBeReplaced(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	service := New("boltertrader-product-platform-upgrade/v4", time.Second)
	service.SetClock(func() time.Time { return now })
	if _, err := service.RegisterResult(RegistrationRequest{WorkerID: "worker-a", InstanceID: "instance-test", ProductID: "funding", Mode: "test", ContractVersion: "boltertrader-product-platform-upgrade/v4", IdempotencyKey: "register-test"}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	_, err := service.RegisterResult(RegistrationRequest{WorkerID: "worker-a", InstanceID: "instance-live", ProductID: "funding", Mode: "live", ContractVersion: "boltertrader-product-platform-upgrade/v4", IdempotencyKey: "register-live"})
	if !errors.Is(err, ErrWorkerAlreadyActive) {
		t.Fatalf("cross-mode registration error=%v", err)
	}
	if ready := service.Readiness("worker-a"); ready.Mode != "test" {
		t.Fatalf("cross-mode registration replaced readiness=%+v", ready)
	}
}

func TestRenewReplayKeepsLeaseGenerationAndExpiry(t *testing.T) {
	service, generation := readyService(t)
	claim, err := service.ClaimPage(ClaimRequest{WorkerID: "worker-a", WorkerGeneration: generation, InstanceID: "instance-a", IdempotencyKey: "claim-a", MaxItems: 1})
	if err != nil {
		t.Fatal(err)
	}
	request := LeaseRequest{WorkerID: "worker-a", WorkerGeneration: generation, WorkItemID: claim.WorkItem.WorkItemID, WorkRevision: claim.WorkItem.WorkRevision, LeaseGeneration: claim.Lease.LeaseGeneration, IdempotencyKey: "renew-a"}
	first, err := service.RenewLeaseResult(claim.Lease.LeaseID, request)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := service.RenewLeaseResult(claim.Lease.LeaseID, request)
	if err != nil || replay.LeaseGeneration != claim.Lease.LeaseGeneration || replay.RenewalCount != 1 || !replay.ExpiresAt.Equal(first.ExpiresAt) {
		t.Fatalf("renew replay=%+v err=%v first=%+v", replay, err, first)
	}
}

func TestFactsRequireActiveLeaseAndProvenCursor(t *testing.T) {
	service, generation := readyService(t)
	claim, err := service.ClaimPage(ClaimRequest{WorkerID: "worker-a", WorkerGeneration: generation, InstanceID: "instance-a", IdempotencyKey: "claim-a", MaxItems: 1})
	if err != nil {
		t.Fatal(err)
	}
	request := FactRequest{WorkerID: "worker-a", WorkerGeneration: generation, WorkItemID: claim.WorkItem.WorkItemID, WorkRevision: claim.WorkItem.WorkRevision, LeaseID: claim.Lease.LeaseID, LeaseGeneration: claim.Lease.LeaseGeneration, AfterCursor: "unknown", Limit: 10}
	if _, err := service.FactPage(request, false); !errors.Is(err, ErrResnapshotRequired) {
		t.Fatalf("unknown cursor error=%v", err)
	}
	request.AfterCursor = ""
	page, err := service.FactPage(request, true)
	if err != nil || page.ResnapshotRequired || page.NextCursor == "" {
		t.Fatalf("snapshot=%+v err=%v", page, err)
	}
}

func TestReadinessReportsActiveLeaseCount(t *testing.T) {
	service, generation := readyService(t)
	if ready := service.Readiness("worker-a"); ready.ActiveLeaseCount != 0 {
		t.Fatalf("readiness before claim=%+v", ready)
	}
	if _, err := service.ClaimPage(ClaimRequest{WorkerID: "worker-a", WorkerGeneration: generation, InstanceID: "instance-a", IdempotencyKey: "claim-a", MaxItems: 1}); err != nil {
		t.Fatal(err)
	}
	if ready := service.Readiness("worker-a"); ready.ActiveLeaseCount != 1 {
		t.Fatalf("readiness after claim=%+v", ready)
	}
}

func TestRenewRejectsWorkerAfterHeartbeatExpires(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	service := New("boltertrader-product-platform-upgrade/v4", time.Second)
	service.SetClock(func() time.Time { return now })
	registration, err := service.RegisterResult(RegistrationRequest{WorkerID: "worker-a", InstanceID: "instance-a", ProductID: "funding", Mode: "test", ContractVersion: "boltertrader-product-platform-upgrade/v4", IdempotencyKey: "register-a"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Heartbeat("worker-a", HeartbeatRequest{WorkerGeneration: registration.Generation, InstanceID: "instance-a", State: "ready"}); err != nil {
		t.Fatal(err)
	}
	claim, err := service.ClaimPage(ClaimRequest{WorkerID: "worker-a", WorkerGeneration: registration.Generation, InstanceID: "instance-a", IdempotencyKey: "claim-a", MaxItems: 1})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	request := LeaseRequest{WorkerID: "worker-a", WorkerGeneration: registration.Generation, WorkItemID: claim.WorkItem.WorkItemID, WorkRevision: claim.WorkItem.WorkRevision, LeaseGeneration: claim.Lease.LeaseGeneration, IdempotencyKey: "renew-a"}
	if _, err := service.RenewLeaseResult(claim.Lease.LeaseID, request); !errors.Is(err, ErrWorkerNotReady) {
		t.Fatalf("renew error=%v", err)
	}
}

func TestWorkItemSerializesFrozenStateField(t *testing.T) {
	payload, err := json.Marshal(WorkItem{Status: "leased"})
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) == "" || !json.Valid(payload) {
		t.Fatalf("payload=%q", payload)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["state"] != "leased" || decoded["status"] != nil {
		t.Fatalf("payload=%s", payload)
	}
}

func TestFactPageCursorStopsAtLastReturnedFact(t *testing.T) {
	service, generation := readyService(t)
	store := service.store.(*MemoryStore)
	now := service.now().UTC()
	store.facts = []Fact{
		{FactID: "fact-1", Cursor: "cursor_0002", ProductID: "funding", Mode: "test", EffectiveAt: now},
		{FactID: "fact-2", Cursor: "cursor_0003", ProductID: "funding", Mode: "test", EffectiveAt: now.Add(time.Second)},
	}
	claim, err := service.ClaimPage(ClaimRequest{WorkerID: "worker-a", WorkerGeneration: generation, InstanceID: "instance-a", IdempotencyKey: "claim-a", MaxItems: 1})
	if err != nil {
		t.Fatal(err)
	}
	page, err := service.FactPage(FactRequest{WorkerID: "worker-a", WorkerGeneration: generation, WorkItemID: claim.WorkItem.WorkItemID, WorkRevision: claim.WorkItem.WorkRevision, LeaseID: claim.Lease.LeaseID, LeaseGeneration: claim.Lease.LeaseGeneration, Limit: 1}, false)
	if err != nil || len(page.Facts) != 1 || page.NextCursor != "cursor_0002" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
}

func TestFactPageTreatsCursorAsOpaque(t *testing.T) {
	service, generation := readyService(t)
	store := service.store.(*MemoryStore)
	now := service.now().UTC()
	store.facts = []Fact{
		{FactID: "fact-2-a", Cursor: "cursor-a", ProductID: "funding", Mode: "test", EffectiveAt: now},
		{FactID: "fact-1-z-b", Cursor: "cursor-z", ProductID: "funding", Mode: "test", EffectiveAt: now},
		{FactID: "fact-1-z-a", Cursor: "cursor-z", ProductID: "funding", Mode: "test", EffectiveAt: now},
	}
	claim, err := service.ClaimPage(ClaimRequest{WorkerID: "worker-a", WorkerGeneration: generation, InstanceID: "instance-a", IdempotencyKey: "claim-opaque-facts", MaxItems: 1})
	if err != nil {
		t.Fatal(err)
	}
	request := FactRequest{WorkerID: "worker-a", WorkerGeneration: generation, WorkItemID: claim.WorkItem.WorkItemID, WorkRevision: claim.WorkItem.WorkRevision, LeaseID: claim.Lease.LeaseID, LeaseGeneration: claim.Lease.LeaseGeneration, Limit: 1}
	first, err := service.FactPage(request, false)
	if err != nil || len(first.Facts) != 2 || first.Facts[0].FactID != "fact-1-z-a" || first.Facts[1].FactID != "fact-1-z-b" || first.NextCursor != "cursor-z" {
		t.Fatalf("first page=%+v err=%v", first, err)
	}
	request.AfterCursor = first.NextCursor
	second, err := service.FactPage(request, false)
	if err != nil || len(second.Facts) != 1 || second.Facts[0].FactID != "fact-2-a" || second.NextCursor != "cursor-a" {
		t.Fatalf("second page after opaque cursor %q=%+v err=%v", first.NextCursor, second, err)
	}
	request.Limit = 10
	snapshot, err := service.FactPage(request, true)
	if err != nil || len(snapshot.Facts) != 3 || snapshot.NextCursor != "cursor-a" {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	request.AfterCursor = "unknown"
	if _, err := service.FactPage(request, false); !errors.Is(err, ErrResnapshotRequired) {
		t.Fatalf("unknown cursor err=%v", err)
	}
}

func TestFactPageLimitNeverSplitsCursorBatch(t *testing.T) {
	service, generation := readyService(t)
	store := service.store.(*MemoryStore)
	now := service.now().UTC()
	store.facts = []Fact{{FactID: "fact-a", Cursor: "cursor_0002", ProductID: "funding", Mode: "test", EffectiveAt: now}, {FactID: "fact-b", Cursor: "cursor_0002", ProductID: "funding", Mode: "test", EffectiveAt: now}, {FactID: "fact-c", Cursor: "cursor_0003", ProductID: "funding", Mode: "test", EffectiveAt: now.Add(time.Second)}}
	claim, err := service.ClaimPage(ClaimRequest{WorkerID: "worker-a", WorkerGeneration: generation, InstanceID: "instance-a", IdempotencyKey: "claim-batch", MaxItems: 1})
	if err != nil {
		t.Fatal(err)
	}
	request := FactRequest{WorkerID: "worker-a", WorkerGeneration: generation, WorkItemID: claim.WorkItem.WorkItemID, WorkRevision: claim.WorkItem.WorkRevision, LeaseID: claim.Lease.LeaseID, LeaseGeneration: claim.Lease.LeaseGeneration, Limit: 1}
	page, err := service.FactPage(request, false)
	if err != nil || len(page.Facts) != 2 || page.NextCursor != "cursor_0002" || page.Facts[0].FactID != "fact-a" || page.Facts[1].FactID != "fact-b" {
		t.Fatalf("first page=%+v err=%v", page, err)
	}
	request.AfterCursor = page.NextCursor
	page, err = service.FactPage(request, false)
	if err != nil || len(page.Facts) != 1 || page.Facts[0].FactID != "fact-c" || page.NextCursor != "cursor_0003" {
		t.Fatalf("second page=%+v err=%v", page, err)
	}
}

func TestFactsAndSnapshotsExposeOnlyClaimedFenceAccounts(t *testing.T) {
	service, generation := readyService(t)
	store := service.store.(*MemoryStore)
	now := service.now().UTC()
	item := store.workItems["work_0001"]
	item.AccountFences = json.RawMessage(`[{"accountId":"account-allowed","desiredStateRevision":1,"runtimeGeneration":1,"credentialVersion":1},{"accountId":"account-unused","desiredStateRevision":1,"runtimeGeneration":1,"credentialVersion":1}]`)
	store.workItems[item.WorkItemID] = item
	store.facts = []Fact{
		{FactID: "global", Cursor: "cursor_0002", ProductID: "funding", Mode: "test", EffectiveAt: now},
		{FactID: "allowed", Cursor: "cursor_0003", AccountID: "account-allowed", ProductID: "funding", Mode: "test", EffectiveAt: now.Add(time.Second)},
		{FactID: "other", Cursor: "cursor_0004", AccountID: "account-other", ProductID: "funding", Mode: "test", EffectiveAt: now.Add(2 * time.Second)},
	}
	claim, err := service.ClaimPage(ClaimRequest{WorkerID: "worker-a", WorkerGeneration: generation, InstanceID: "instance-a", IdempotencyKey: "claim-scoped", MaxItems: 1})
	if err != nil {
		t.Fatal(err)
	}
	req := FactRequest{WorkerID: "worker-a", WorkerGeneration: generation, WorkItemID: claim.WorkItem.WorkItemID, WorkRevision: claim.WorkItem.WorkRevision, LeaseID: claim.Lease.LeaseID, LeaseGeneration: claim.Lease.LeaseGeneration, Limit: 100}
	for _, snapshot := range []bool{false, true} {
		page, pageErr := service.FactPage(req, snapshot)
		if pageErr != nil || len(page.Facts) != 2 || page.Facts[0].FactID != "global" || page.Facts[1].FactID != "allowed" {
			t.Fatalf("snapshot=%v page=%+v err=%v", snapshot, page, pageErr)
		}
	}
	req.AfterCursor = "cursor_0004"
	if _, err := service.FactPage(req, false); !errors.Is(err, ErrResnapshotRequired) {
		t.Fatalf("unscoped cursor err=%v", err)
	}
}

func readyService(t *testing.T) (*Service, int) {
	t.Helper()
	now := time.Unix(1_700_000_000, 0).UTC()
	service := New("boltertrader-product-platform-upgrade/v4", 15*time.Second)
	service.SetClock(func() time.Time { return now })
	registration, err := service.RegisterResult(RegistrationRequest{WorkerID: "worker-a", InstanceID: "instance-a", ProductID: "funding", Mode: "test", ContractVersion: "boltertrader-product-platform-upgrade/v4", IdempotencyKey: "register-a"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Heartbeat("worker-a", HeartbeatRequest{WorkerGeneration: registration.Generation, InstanceID: "instance-a", State: "ready"}); err != nil {
		t.Fatal(err)
	}
	return service, registration.Generation
}
