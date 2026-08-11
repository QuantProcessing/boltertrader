package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/backend/internal/httpapi"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/accounts"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/catalog"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/ledger"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/marketdata"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/orders"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/portfolio"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/reservations"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/work"
	"github.com/QuantProcessing/boltertrader/backend/internal/products/funding/calculation"
	fundingexecution "github.com/QuantProcessing/boltertrader/backend/internal/products/funding/execution"
	"github.com/QuantProcessing/boltertrader/backend/internal/products/funding/planning"
	"github.com/QuantProcessing/boltertrader/backend/internal/products/manual"
	"github.com/QuantProcessing/boltertrader/backend/internal/workapi"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

var integrationSchemaSequence atomic.Uint64

func TestMigrateAppliesPlatformSchemaIdempotently(t *testing.T) {
	database, _ := openIntegrationDatabaseWithMigrations(t, false)
	ctx := context.Background()
	if err := Migrate(ctx, database); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, database); err != nil {
		t.Fatalf("idempotent migrate: %v", err)
	}
	var versions int
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM platform_schema_migrations`).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if want := expectedPlatformMigrationCount(t); versions != want {
		t.Fatalf("migration versions=%d want %d", versions, want)
	}
	var latestVersion int
	if err := database.QueryRowContext(ctx, `SELECT max(version) FROM platform_schema_migrations`).Scan(&latestVersion); err != nil {
		t.Fatal(err)
	}
	if latestVersion != 15 {
		t.Fatalf("latest migration version=%d want 15", latestVersion)
	}
	for _, table := range []string{"platform_identities", "platform_accounts", "platform_commands", "platform_work_items", "platform_portfolio_fills"} {
		var exists bool
		if err := database.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatalf("migrated table %s is missing", table)
		}
	}
	if _, err := database.ExecContext(ctx, `UPDATE platform_schema_migrations SET checksum='mutated' WHERE version=1`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, database); !errors.Is(err, ErrMigrationMismatch) {
		t.Fatalf("checksum mismatch error=%v want %v", err, ErrMigrationMismatch)
	}
}

func TestWorkAPIRegistrationPersistsPostgresSafeModeScope(t *testing.T) {
	database, _ := openIntegrationDatabaseWithMigrations(t, true)
	service := work.NewWithStore(workapi.ContractVersion, time.Minute, NewWorkStore(database))
	handler, err := workapi.New(workapi.Options{
		TokenDigest: sha256.Sum256([]byte("test-worker-token")), Mode: "test",
		ContractVersion: workapi.ContractVersion, Service: service,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/funding-work/workers/registrations", strings.NewReader(`{"workerId":"funding-pg-worker","instanceId":"instance-a","productId":"funding","mode":"test","contractVersion":"boltertrader-product-platform-upgrade/v4"}`))
	request.Header.Set("Authorization", "Bearer test-worker-token")
	request.Header.Set("Idempotency-Key", "register-funding-pg-worker")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("registration=%d body=%s", response.Code, response.Body.String())
	}
	var workerID string
	if err := database.QueryRow(`SELECT worker_id FROM platform_work_workers`).Scan(&workerID); err != nil {
		t.Fatal(err)
	}
	if workerID != "test:funding-pg-worker" || strings.ContainsRune(workerID, '\x00') {
		t.Fatalf("persisted worker key=%q", workerID)
	}
}

func TestFundingBrowserWorkProducerPersistsOwnershipAndReplay(t *testing.T) {
	database, _ := openIntegrationDatabaseWithMigrations(t, true)
	now := time.Date(2026, 8, 6, 2, 30, 0, 0, time.UTC)
	service := work.NewWithStore("contract-v1", time.Minute, NewWorkStore(database))
	service.SetClock(func() time.Time { return now })
	request := work.EnqueueRequest{
		PrincipalID: "browser-a", IdempotencyKey: "funding-open-a", WorkKind: "open",
		LineageID: "lineage-a", ProductID: "funding", Mode: "test",
		FactsCursor: "cursor-preview-a", Facts: []work.Fact{{FactID: "fact-preview-a", Cursor: "cursor-preview-a", ProductID: "funding", Mode: "test", Kind: "funding.execution-input", Payload: json.RawMessage(`{}`), EffectiveAt: now}},
		AccountFences: json.RawMessage(`[{"accountId":"long","desiredStateRevision":4,"runtimeGeneration":2,"credentialVersion":3},{"accountId":"short","desiredStateRevision":5,"runtimeGeneration":3,"credentialVersion":4}]`),
		Payload:       json.RawMessage(`{"previewId":"preview-a"}`),
	}
	first, replay, err := service.Enqueue(context.Background(), request)
	if err != nil || replay {
		t.Fatalf("Enqueue() first=%+v replay=%v err=%v", first, replay, err)
	}
	second, replay, err := service.Enqueue(context.Background(), request)
	if err != nil || !replay || second.WorkItemID != first.WorkItemID {
		t.Fatalf("Enqueue() second=%+v replay=%v err=%v", second, replay, err)
	}
	owned, err := service.GetOwnedWork(context.Background(), "browser-a", "test", first.WorkItemID)
	if err != nil || owned.LineageID != "lineage-a" {
		t.Fatalf("GetOwnedWork()=%+v err=%v", owned, err)
	}
	if _, err := service.GetOwnedWork(context.Background(), "browser-b", "test", first.WorkItemID); !errors.Is(err, work.ErrWorkNotFound) {
		t.Fatalf("cross-principal GetOwnedWork() err=%v", err)
	}
	var principal string
	if err := database.QueryRow(`SELECT principal_id FROM platform_work_items WHERE work_item_id=$1`, first.WorkItemID).Scan(&principal); err != nil || principal != "browser-a" {
		t.Fatalf("persisted principal=%q err=%v", principal, err)
	}
	concurrent := request
	concurrent.IdempotencyKey = "funding-open-concurrent"
	type result struct {
		item   work.WorkItem
		replay bool
		err    error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			item, replay, err := service.Enqueue(context.Background(), concurrent)
			results <- result{item: item, replay: replay, err: err}
		}()
	}
	left, right := <-results, <-results
	if left.err != nil || right.err != nil || left.item.WorkItemID != right.item.WorkItemID || left.replay == right.replay {
		t.Fatalf("concurrent Enqueue() leftItem=%+v leftReplay=%v leftErr=%v rightItem=%+v rightReplay=%v rightErr=%v", left.item, left.replay, left.err, right.item, right.replay, right.err)
	}
}

func TestWorkClaimPaginationDoesNotStarveLexicallyLowerCursors(t *testing.T) {
	database, _ := openIntegrationDatabaseWithMigrations(t, true)
	ctx := context.Background()
	now := time.Date(2026, 8, 6, 5, 30, 0, 0, time.UTC)
	store := NewWorkStore(database)
	registration, err := store.RegisterWorker(ctx, work.RegistrationRequest{WorkerID: "worker-claim", InstanceID: "instance-claim", ProductID: "funding", Mode: "test", ContractVersion: "contract-v1", IdempotencyKey: "register-claim"}, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.HeartbeatWorker(ctx, registration.WorkerID, work.HeartbeatRequest{WorkerGeneration: registration.Generation, InstanceID: registration.InstanceID, State: "ready"}, now); err != nil {
		t.Fatal(err)
	}
	fences := json.RawMessage(`[{"accountId":"account-long","desiredStateRevision":1,"runtimeGeneration":1,"credentialVersion":1},{"accountId":"account-short","desiredStateRevision":1,"runtimeGeneration":1,"credentialVersion":1}]`)
	older := now.Add(-time.Minute)
	newer := now
	for _, item := range []struct {
		id, cursor string
		updatedAt  time.Time
	}{
		{id: "work-z", cursor: "z-cursor", updatedAt: older},
		{id: "work-a", cursor: "a-cursor", updatedAt: newer},
	} {
		if _, err := database.ExecContext(ctx, `INSERT INTO platform_work_items(work_item_id,principal_id,work_kind,lineage_id,work_revision,product_id,mode,status,facts_cursor,account_fences,payload,not_before,created_at,updated_at) VALUES ($1,'browser-a','open','lineage-claim',1,'funding','test','open',$2,$3,'{}',$4,$4,$4)`, item.id, item.cursor, fences, item.updatedAt); err != nil {
			t.Fatal(err)
		}
	}
	first, err := store.ClaimWork(ctx, work.ClaimRequest{WorkerID: registration.WorkerID, WorkerGeneration: registration.Generation, InstanceID: registration.InstanceID, IdempotencyKey: "claim-claim-1", MaxItems: 1}, newer.Add(time.Second), time.Minute)
	if err != nil || first.WorkItem == nil || first.Lease == nil || first.NextCursor != "z-cursor" {
		t.Fatalf("first claim=%+v err=%v", first, err)
	}
	second, err := store.ClaimWork(ctx, work.ClaimRequest{WorkerID: registration.WorkerID, WorkerGeneration: registration.Generation, InstanceID: registration.InstanceID, IdempotencyKey: "claim-claim-2", AfterCursor: first.NextCursor, MaxItems: 1}, newer.Add(2*time.Second), time.Minute)
	if err != nil || second.WorkItem == nil || second.Lease == nil || second.WorkItem.WorkItemID != "work-a" || second.NextCursor != "a-cursor" {
		t.Fatalf("second claim=%+v err=%v", second, err)
	}
}

func TestEmptyClaimDoesNotPersistOperationAndCanLaterClaimWithSameKey(t *testing.T) {
	database, _ := openIntegrationDatabaseWithMigrations(t, true)
	ctx := context.Background()
	now := time.Date(2026, 8, 6, 5, 15, 0, 0, time.UTC)
	store := NewWorkStore(database)
	registration, err := store.RegisterWorker(ctx, work.RegistrationRequest{
		WorkerID: "worker-empty", InstanceID: "instance-empty", ProductID: "funding", Mode: "test",
		ContractVersion: "contract-v1", IdempotencyKey: "register-empty",
	}, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.HeartbeatWorker(ctx, registration.WorkerID, work.HeartbeatRequest{WorkerGeneration: registration.Generation, InstanceID: registration.InstanceID, State: "ready"}, now); err != nil {
		t.Fatal(err)
	}

	request := work.ClaimRequest{WorkerID: registration.WorkerID, WorkerGeneration: registration.Generation, InstanceID: registration.InstanceID, IdempotencyKey: "claim-empty", MaxItems: 1}
	empty, err := store.ClaimWork(ctx, request, now, time.Minute)
	if err != nil || empty.WorkItem != nil || empty.Lease != nil {
		t.Fatalf("empty claim=%+v err=%v", empty, err)
	}
	var operations int
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM platform_work_operations WHERE operation_kind='claim' AND idempotency_key=$1`, request.IdempotencyKey).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if operations != 0 {
		t.Fatalf("empty claim operations=%d want 0", operations)
	}

	fences := json.RawMessage(`[{"accountId":"account-long","desiredStateRevision":1,"runtimeGeneration":1,"credentialVersion":1},{"accountId":"account-short","desiredStateRevision":1,"runtimeGeneration":1,"credentialVersion":1}]`)
	if _, err := database.ExecContext(ctx, `INSERT INTO platform_work_items(work_item_id,principal_id,work_kind,lineage_id,work_revision,product_id,mode,status,facts_cursor,account_fences,payload,not_before,created_at,updated_at) VALUES ('work-after-empty','browser-a','open','lineage-after-empty',1,'funding','test','open','cursor-after-empty',$1,'{}',$2,$2,$2)`, fences, now); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimWork(ctx, request, now.Add(time.Second), time.Minute)
	if err != nil || claimed.WorkItem == nil || claimed.Lease == nil || claimed.WorkItem.WorkItemID != "work-after-empty" {
		t.Fatalf("claim after empty=%+v err=%v", claimed, err)
	}
	replayed, err := store.ClaimWork(ctx, request, now.Add(2*time.Second), time.Minute)
	if err != nil || replayed.Lease == nil || replayed.Lease.LeaseID != claimed.Lease.LeaseID {
		t.Fatalf("non-empty replay=%+v first=%+v err=%v", replayed, claimed, err)
	}
}

func TestClaimWorkTreatsAfterCursorAsOpaqueAndDoesNotStarveLaterWork(t *testing.T) {
	database, _ := openIntegrationDatabaseWithMigrations(t, true)
	ctx := context.Background()
	now := time.Date(2026, 8, 6, 5, 0, 0, 0, time.UTC)
	if _, err := database.ExecContext(ctx, `INSERT INTO platform_accounts(account_id,venue,venue_environment,mode,native_environment,display_name,product_ids,secret_ref,desired_state_revision,runtime_generation,credential_version,state,updated_at) VALUES ('account-a','venue-a','testnet','test','testnet','Account A','["funding"]','secret://test/account-a',1,1,1,'active',$1), ('account-b','venue-b','testnet','test','testnet','Account B','["funding"]','secret://test/account-b',1,1,1,'active',$1)`, now); err != nil {
		t.Fatal(err)
	}
	store := NewWorkStore(database)
	fences := json.RawMessage(`[{"accountId":"account-a","desiredStateRevision":1,"runtimeGeneration":1,"credentialVersion":1},{"accountId":"account-b","desiredStateRevision":1,"runtimeGeneration":1,"credentialVersion":1}]`)
	for index, cursor := range []string{"cursor-z", "cursor-a"} {
		created := now.Add(time.Duration(index) * time.Second)
		_, _, err := store.EnqueueWork(ctx, work.EnqueueRequest{
			PrincipalID: "browser-a", IdempotencyKey: "enqueue-" + cursor, WorkKind: "open", LineageID: "lineage-" + cursor,
			ProductID: "funding", Mode: "test", FactsCursor: cursor,
			Facts:         []work.Fact{{FactID: "fact-" + cursor, Cursor: cursor, ProductID: "funding", Mode: "test", Kind: "funding.execution-input", Payload: json.RawMessage(`{}`), EffectiveAt: created}},
			AccountFences: fences, Payload: json.RawMessage(`{}`),
		}, created)
		if err != nil {
			t.Fatalf("enqueue %s: %v", cursor, err)
		}
	}
	registration, err := store.RegisterWorker(ctx, work.RegistrationRequest{WorkerID: "worker-opaque", InstanceID: "instance-opaque", ProductID: "funding", Mode: "test", ContractVersion: "contract-v1", IdempotencyKey: "register-opaque"}, now.Add(2*time.Second), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.HeartbeatWorker(ctx, registration.WorkerID, work.HeartbeatRequest{WorkerGeneration: registration.Generation, InstanceID: registration.InstanceID, State: "ready"}, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	first, err := store.ClaimWork(ctx, work.ClaimRequest{WorkerID: registration.WorkerID, WorkerGeneration: registration.Generation, InstanceID: registration.InstanceID, IdempotencyKey: "claim-opaque-1", MaxItems: 1}, now.Add(3*time.Second), time.Minute)
	if err != nil || first.WorkItem == nil || first.WorkItem.FactsCursor != "cursor-z" {
		t.Fatalf("first claim=%+v err=%v", first, err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE platform_work_leases SET status='completed' WHERE lease_id=$1`, first.Lease.LeaseID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE platform_work_items SET status='completed' WHERE work_item_id=$1`, first.WorkItem.WorkItemID); err != nil {
		t.Fatal(err)
	}
	second, err := store.ClaimWork(ctx, work.ClaimRequest{WorkerID: registration.WorkerID, WorkerGeneration: registration.Generation, InstanceID: registration.InstanceID, IdempotencyKey: "claim-opaque-2", AfterCursor: first.NextCursor, MaxItems: 1}, now.Add(4*time.Second), time.Minute)
	if err != nil || second.WorkItem == nil || second.WorkItem.FactsCursor != "cursor-a" {
		t.Fatalf("second claim after opaque cursor %q=%+v err=%v", first.NextCursor, second, err)
	}
}

func TestFactPageTreatsCursorAsOpaque(t *testing.T) {
	database, _ := openIntegrationDatabaseWithMigrations(t, true)
	store, lease, _, now := seedClaimedWork(t, database)
	ctx := context.Background()
	if _, err := database.ExecContext(ctx, `UPDATE platform_work_items SET principal_id='platform:funding' WHERE work_item_id=$1`, lease.WorkItemID); err != nil {
		t.Fatal(err)
	}
	for _, fact := range []struct {
		id, cursor string
	}{
		{id: "fact-2-a", cursor: "cursor-a"},
		{id: "fact-1-z-b", cursor: "cursor-z"},
		{id: "fact-1-z-a", cursor: "cursor-z"},
	} {
		if _, err := database.ExecContext(ctx, `INSERT INTO platform_work_facts(fact_id,cursor,account_id,product_id,mode,kind,payload,effective_at) VALUES ($1,$2,'account-a','funding','test','funding.market','{}',$3)`, fact.id, fact.cursor, now); err != nil {
			t.Fatal(err)
		}
	}
	request := work.FactRequest{WorkerID: lease.WorkerID, WorkerGeneration: lease.WorkerGeneration, WorkItemID: lease.WorkItemID, WorkRevision: lease.WorkRevision, LeaseID: lease.LeaseID, LeaseGeneration: lease.LeaseGeneration, Limit: 1}
	first, err := store.FactPage(ctx, request, false, now)
	if err != nil || len(first.Facts) != 2 || first.Facts[0].FactID != "fact-1-z-a" || first.Facts[1].FactID != "fact-1-z-b" || first.NextCursor != "cursor-z" {
		t.Fatalf("first page=%+v err=%v", first, err)
	}
	request.AfterCursor = first.NextCursor
	second, err := store.FactPage(ctx, request, false, now)
	if err != nil || len(second.Facts) != 1 || second.Facts[0].FactID != "fact-2-a" || second.NextCursor != "cursor-a" {
		t.Fatalf("second page after opaque cursor %q=%+v err=%v", first.NextCursor, second, err)
	}
	request.Limit = 10
	snapshot, err := store.FactPage(ctx, request, true, now)
	if err != nil || len(snapshot.Facts) != 3 || snapshot.Facts[0].FactID != "fact-1-z-a" || snapshot.Facts[1].FactID != "fact-1-z-b" || snapshot.Facts[2].FactID != "fact-2-a" || snapshot.NextCursor != "cursor-a" {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	request.AfterCursor = "unknown"
	if _, err := store.FactPage(ctx, request, false, now); !errors.Is(err, work.ErrResnapshotRequired) {
		t.Fatalf("unknown cursor err=%v", err)
	}
}

func TestWorkIntentCommandsPreserveTwoPrincipalsWithSameLineage(t *testing.T) {
	database, reopen := openIntegrationDatabaseWithMigrations(t, true)
	ctx := context.Background()
	now := time.Date(2026, 8, 6, 4, 45, 0, 0, time.UTC)
	fences := json.RawMessage(`[{"accountId":"account-a","desiredStateRevision":1,"runtimeGeneration":1,"credentialVersion":1},{"accountId":"account-b","desiredStateRevision":1,"runtimeGeneration":1,"credentialVersion":1}]`)
	payload := json.RawMessage(`{"accountId":"account-a"}`)
	if _, err := database.ExecContext(ctx, `INSERT INTO platform_accounts(account_id,venue,venue_environment,mode,native_environment,display_name,product_ids,secret_ref,desired_state_revision,runtime_generation,credential_version,state,updated_at) VALUES ('account-a','venue-a','testnet','test','testnet','Account A','["funding"]','secret://test/account-a',1,1,1,'active',$1), ('account-b','venue-b','testnet','test','testnet','Account B','["funding"]','secret://test/account-b',1,1,1,'active',$1)`, now); err != nil {
		t.Fatal(err)
	}
	store := NewWorkStore(database)
	for index, principal := range []string{"browser-a", "browser-b"} {
		workID, leaseID := "work-"+principal, "lease-"+principal
		workerID, instanceID := "worker-"+principal, "instance-"+principal
		cursor := "cursor-" + principal
		if _, err := database.ExecContext(ctx, `INSERT INTO platform_work_items(work_item_id,principal_id,work_kind,lineage_id,work_revision,product_id,mode,status,facts_cursor,account_fences,payload,not_before,created_at,updated_at) VALUES ($1,$2,'open','shared-lineage',1,'funding','test','leased',$3,$4,$5,$6,$6,$6)`, workID, principal, cursor, fences, payload, now); err != nil {
			t.Fatal(err)
		}
		if _, err := database.ExecContext(ctx, `INSERT INTO platform_work_workers(worker_id,instance_id,product_id,mode,contract_version,generation,state,draining,registered_at,heartbeat_expires_at,last_heartbeat_at) VALUES ($1,$2,'funding','test','contract-v1',1,'ready',false,$3,$4,$3)`, workerID, instanceID, now, now.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		if _, err := database.ExecContext(ctx, `INSERT INTO platform_work_leases(lease_id,lease_generation,work_item_id,work_revision,worker_id,worker_mode,worker_generation,facts_cursor,status,expires_at,created_at,updated_at) VALUES ($1,1,$2,1,$3,'test',1,$4,'active',$5,$6,$6)`, leaseID, workID, workerID, cursor, now.Add(time.Minute), now); err != nil {
			t.Fatal(err)
		}
		record, err := store.RecordIntent(ctx, leaseID, work.IntentRequest{WorkerID: workerID, WorkerGeneration: 1, WorkItemID: workID, WorkRevision: 1, LeaseGeneration: 1, IdempotencyKey: "shared-intent", IntentKind: "funding.open", FactsCursor: cursor, AccountFences: fences, Payload: payload}, now.Add(time.Duration(index)*time.Second))
		if err != nil {
			t.Fatalf("principal %s RecordIntent() err=%v", principal, err)
		}
		if record.Command.PrincipalID != principal || record.Command.LineageID != "shared-lineage" {
			t.Fatalf("principal %s command=%+v", principal, record.Command)
		}
		if index == 0 {
			if err := database.Close(); err != nil {
				t.Fatal(err)
			}
			database = reopen()
			store = NewWorkStore(database)
		}
	}
	var commands int
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM platform_commands WHERE lineage_id='shared-lineage' AND principal_id IN ('browser-a','browser-b')`).Scan(&commands); err != nil || commands != 2 {
		t.Fatalf("principal-scoped command count=%d err=%v", commands, err)
	}
}

func TestFundingBrowserClosePersistsBothUnwindFacts(t *testing.T) {
	database, reopen := openIntegrationDatabaseWithMigrations(t, true)
	ctx := context.Background()
	now := time.Date(2026, 8, 6, 2, 30, 0, 0, time.UTC)
	store := NewWorkStore(database)
	service := work.NewWithStore("contract-v1", time.Minute, store)
	service.SetClock(func() time.Time { return now })
	fences := json.RawMessage(`[{"accountId":"long","desiredStateRevision":4,"runtimeGeneration":2,"credentialVersion":3},{"accountId":"short","desiredStateRevision":5,"runtimeGeneration":3,"credentialVersion":4}]`)
	request := work.EnqueueRequest{PrincipalID: "browser-a", IdempotencyKey: "funding-close-a", WorkKind: "close", LineageID: "lineage-a", ProductID: "funding", Mode: "test", FactsCursor: "cursor-close-a", AccountFences: fences, Payload: json.RawMessage(`{"lineageId":"lineage-a","reason":"browser_request","requestedAt":"2026-08-06T02:30:00Z"}`), Facts: []work.Fact{
		{FactID: "fact-close-long", Cursor: "cursor-close-a", AccountID: "long", ProductID: "funding", Mode: "test", Kind: "funding.unwind-reservation", Payload: json.RawMessage(`{"reservationId":"close-long"}`), EffectiveAt: now},
		{FactID: "fact-close-short", Cursor: "cursor-close-a", AccountID: "short", ProductID: "funding", Mode: "test", Kind: "funding.unwind-reservation", Payload: json.RawMessage(`{"reservationId":"close-short"}`), EffectiveAt: now},
	}}
	item, replay, err := service.Enqueue(context.Background(), request)
	if err != nil || replay || item.WorkKind != "close" {
		t.Fatalf("Enqueue() item=%+v replay=%v err=%v", item, replay, err)
	}
	var count int
	if err := database.QueryRow(`SELECT count(*) FROM platform_work_facts WHERE cursor=$1`, request.FactsCursor).Scan(&count); err != nil || count != 2 {
		t.Fatalf("persisted unwind facts=%d err=%v", count, err)
	}
	restarted := work.NewWithStore("contract-v1", time.Minute, NewWorkStore(reopen()))
	restarted.SetClock(func() time.Time { return now })
	replayed, replay, err := restarted.Enqueue(context.Background(), request)
	if err != nil || !replay || replayed.WorkItemID != item.WorkItemID {
		t.Fatalf("replay item=%+v replay=%v err=%v", replayed, replay, err)
	}
	if err := database.QueryRow(`SELECT count(*) FROM platform_work_facts WHERE cursor=$1`, request.FactsCursor).Scan(&count); err != nil || count != 2 {
		t.Fatalf("replay duplicated unwind facts=%d err=%v", count, err)
	}
	reordered := request
	reordered.Facts = []work.Fact{request.Facts[1], request.Facts[0]}
	if _, _, err := restarted.Enqueue(ctx, reordered); !errors.Is(err, work.ErrIdempotencyConflict) {
		t.Fatalf("reordered replay err=%v", err)
	}
	changed := request
	changed.IdempotencyKey = "funding-close-changed"
	changed.Facts = append([]work.Fact(nil), request.Facts...)
	changed.Facts[1].Payload = json.RawMessage(`{"reservationId":"changed"}`)
	if _, _, err := restarted.Enqueue(ctx, changed); !errors.Is(err, work.ErrIdempotencyConflict) {
		t.Fatalf("changed cursor member err=%v", err)
	}
	for _, account := range []struct {
		id, venue                        string
		revision, generation, credential int
	}{{"long", "venue-a", 4, 2, 3}, {"short", "venue-b", 5, 3, 4}} {
		if _, err := database.ExecContext(ctx, `INSERT INTO platform_accounts(account_id,venue,venue_environment,mode,native_environment,display_name,product_ids,secret_ref,desired_state_revision,runtime_generation,credential_version,state,updated_at) VALUES ($1,$2,'testnet','test','testnet',$1,'["funding"]','secret://test/'||$1,$3,$4,$5,'active',$6)`, account.id, account.venue, account.revision, account.generation, account.credential, now); err != nil {
			t.Fatal(err)
		}
	}
	registration, err := store.RegisterWorker(ctx, work.RegistrationRequest{WorkerID: "worker-close", InstanceID: "instance-close", ProductID: "funding", Mode: "test", ContractVersion: "contract-v1", IdempotencyKey: "register-close"}, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.HeartbeatWorker(ctx, registration.WorkerID, work.HeartbeatRequest{WorkerGeneration: registration.Generation, InstanceID: registration.InstanceID, State: "ready"}, now); err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimWork(ctx, work.ClaimRequest{WorkerID: registration.WorkerID, WorkerGeneration: registration.Generation, InstanceID: registration.InstanceID, IdempotencyKey: "claim-close", MaxItems: 1}, now, time.Minute)
	if err != nil || claim.WorkItem == nil || claim.Lease == nil {
		t.Fatalf("claim=%+v err=%v", claim, err)
	}
	factRequest := work.FactRequest{WorkerID: registration.WorkerID, WorkerGeneration: registration.Generation, WorkItemID: claim.WorkItem.WorkItemID, WorkRevision: claim.WorkItem.WorkRevision, LeaseID: claim.Lease.LeaseID, LeaseGeneration: claim.Lease.LeaseGeneration, Limit: 1}
	page, err := store.FactPage(ctx, factRequest, true, now)
	if err != nil || len(page.Facts) != 2 || page.NextCursor != request.FactsCursor || page.Facts[0].FactID != "fact-close-long" || page.Facts[1].FactID != "fact-close-short" {
		t.Fatalf("limit=1 page=%+v err=%v", page, err)
	}
	factRequest.AfterCursor = page.NextCursor
	next, err := store.FactPage(ctx, factRequest, false, now)
	if err != nil || len(next.Facts) != 0 || next.NextCursor != request.FactsCursor {
		t.Fatalf("next page=%+v err=%v", next, err)
	}
	intent, err := store.RecordIntent(ctx, claim.Lease.LeaseID, work.IntentRequest{WorkerID: registration.WorkerID, WorkerGeneration: registration.Generation, WorkItemID: claim.WorkItem.WorkItemID, WorkRevision: claim.WorkItem.WorkRevision, LeaseGeneration: claim.Lease.LeaseGeneration, IdempotencyKey: "intent-close", IntentKind: "funding.close", FactsCursor: request.FactsCursor, AccountFences: fences, Payload: request.Payload}, now)
	if err != nil || len(intent.Facts) != 2 || intent.Facts[0].FactID != "fact-close-long" || intent.Facts[1].FactID != "fact-close-short" {
		t.Fatalf("intent facts=%+v err=%v", intent.Facts, err)
	}
	if _, err := database.Exec(`INSERT INTO platform_work_facts(fact_id,cursor,account_id,product_id,mode,kind,payload,effective_at) VALUES ('fact-close-conflict','cursor-existing','short','funding','test','funding.unwind-reservation','{}',$1)`, now); err != nil {
		t.Fatal(err)
	}
	atomic := request
	atomic.IdempotencyKey = "funding-close-atomic"
	atomic.FactsCursor = "cursor-close-atomic"
	atomic.Facts = []work.Fact{{FactID: "fact-close-atomic-new", Cursor: atomic.FactsCursor, AccountID: "long", ProductID: "funding", Mode: "test", Kind: "funding.unwind-reservation", Payload: json.RawMessage(`{"reservationId":"close-long"}`), EffectiveAt: now}, {FactID: "fact-close-conflict", Cursor: atomic.FactsCursor, AccountID: "short", ProductID: "funding", Mode: "test", Kind: "funding.unwind-reservation", Payload: json.RawMessage(`{"reservationId":"changed"}`), EffectiveAt: now}}
	if _, _, err := service.Enqueue(context.Background(), atomic); !errors.Is(err, work.ErrIdempotencyConflict) {
		t.Fatalf("atomic conflict err=%v", err)
	}
	if err := database.QueryRow(`SELECT count(*) FROM platform_work_facts WHERE fact_id='fact-close-atomic-new'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed batch left partial fact count=%d err=%v", count, err)
	}
}

type postgresFundingCatalog struct{ snapshot catalog.Snapshot }

func (source postgresFundingCatalog) Snapshot(context.Context) (catalog.Snapshot, error) {
	return source.snapshot, nil
}
func (source postgresFundingCatalog) Status() catalog.Status {
	return catalog.Status{Ready: true, Revision: source.snapshot.Revision}
}

type postgresFundingMarkets map[marketdata.Query]marketdata.Fact

func (source postgresFundingMarkets) Get(_ context.Context, query marketdata.Query) (marketdata.Fact, error) {
	fact, ok := source[query]
	if !ok {
		return marketdata.Fact{}, fmt.Errorf("missing market fact")
	}
	return fact, nil
}

func TestFundingBrowserPreviewPersistsExactFactsThroughClaimAndPlan(t *testing.T) {
	database, _ := openIntegrationDatabaseWithMigrations(t, true)
	ctx := context.Background()
	now := time.Date(2026, 8, 6, 2, 30, 0, 0, time.UTC)
	accountStore := NewAccountStore(database)
	for _, account := range []accounts.Account{
		{ID: "funding-long", Venue: "venue-a", VenueEnvironment: accounts.EnvironmentTestnet, DisplayName: "Funding long", ProductIDs: []string{"funding"}, SecretRef: "secret://test/long", DesiredStateRevision: 11, RuntimeGeneration: 4, CredentialVersion: 3, State: accounts.StateActive, UpdatedAt: now},
		{ID: "funding-short", Venue: "venue-b", VenueEnvironment: accounts.EnvironmentTestnet, DisplayName: "Funding short", ProductIDs: []string{"funding"}, SecretRef: "secret://test/short", DesiredStateRevision: 13, RuntimeGeneration: 7, CredentialVersion: 5, State: accounts.StateActive, UpdatedAt: now},
	} {
		if err := accountStore.Create(ctx, account); err != nil {
			t.Fatal(err)
		}
	}
	reservationStore := NewReservationStore(database)
	reservationService := reservations.NewService(reservationStore, func() time.Time { return now })
	for index, scope := range []reservations.NettingScope{
		{AccountID: "funding-long", Venue: "venue-a", InstrumentID: "btc-a", VenueEnvironment: "testnet"},
		{AccountID: "funding-short", Venue: "venue-b", InstrumentID: "btc-b", VenueEnvironment: "testnet"},
	} {
		if _, err := reservationService.Create(ctx, reservations.CreateRequest{PrincipalID: "browser-a", Mode: reservations.ModeTest, ProductID: "funding", Kind: reservations.KindOpen, Scope: scope, Quantity: "0.5", Notional: "50", Currency: "USDT", IdempotencyKey: fmt.Sprintf("funding-reservation-%d", index)}); err != nil {
			t.Fatal(err)
		}
	}
	rules := catalog.TradeRules{PriceIncrement: "0.01", QuantityIncrement: "0.001", MinQuantity: "0.001", MinNotional: "5"}
	catalogSource := postgresFundingCatalog{snapshot: catalog.Snapshot{Revision: "catalog-composite-1", ObservedAt: now, Venues: []catalog.Venue{{Venue: "venue-a", Environment: "testnet", Ready: true}, {Venue: "venue-b", Environment: "testnet", Ready: true}}, Instruments: []catalog.Instrument{
		{InstrumentID: "btc-a", Venue: "venue-a", Environment: "testnet", Product: "perp", Symbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT", Rules: rules, Ready: true},
		{InstrumentID: "btc-b", Venue: "venue-b", Environment: "testnet", Product: "perp", Symbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT", Rules: rules, Ready: true},
	}}}
	marketFact := func(venue, instrument, rate string) marketdata.Fact {
		return marketdata.Fact{Venue: venue, Environment: "testnet", Product: "perp", InstrumentID: instrument, BestBid: "99.9", BestBidSize: "10", BestAsk: "100.1", BestAskSize: "10", Bids: []marketdata.Level{{Price: "99.9", Quantity: "10"}}, Asks: []marketdata.Level{{Price: "100.1", Quantity: "10"}}, MarkPrice: "100", FundingRate: rate, FundingObservedAt: now.Add(-time.Second), FundingTime: now, NextFundingTime: now.Add(8 * time.Hour), FundingHistory: []marketdata.FundingPoint{{Rate: rate, FundingTime: now.Add(-8 * time.Hour)}, {Rate: rate, FundingTime: now}}, ObservedAt: now.Add(-time.Second)}
	}
	marketSource := postgresFundingMarkets{
		{Venue: "venue-a", Environment: "testnet", Product: "perp", InstrumentID: "btc-a"}: marketFact("venue-a", "btc-a", "0.0001"),
		{Venue: "venue-b", Environment: "testnet", Product: "perp", InstrumentID: "btc-b"}: marketFact("venue-b", "btc-b", "0.0011"),
	}
	projection, err := httpapi.NewSharedFundingProjection(httpapi.SharedFundingProjectionOptions{Catalog: catalogSource, MarketData: marketSource, Accounts: accountStore, Reservations: reservationStore, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	opportunities, err := projection.ListOpportunities(ctx, "browser-a", "test")
	if err != nil || len(opportunities) != 1 {
		t.Fatalf("opportunities=%+v err=%v", opportunities, err)
	}
	preview, err := projection.CreatePreview(ctx, "browser-a", httpapi.FundingPreviewRequest{OpportunityID: opportunities[0].OpportunityID, LongAccountID: "funding-long", ShortAccountID: "funding-short", CanonicalMarketID: opportunities[0].CanonicalMarketID, NotionalPerLeg: "50", MaxSlippageBps: 10, HoldThroughSettlement: true, Mode: "test"})
	if err != nil {
		t.Fatalf("CreatePreview()=%+v err=%v", preview, err)
	}
	preview, err = projection.GetPreview(ctx, "browser-a", preview.PreviewID)
	if err != nil || len(preview.Facts) != 1 {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	if !strings.Contains(string(preview.Facts[0].Payload), `"venueEnvironment":"testnet"`) || !strings.Contains(string(preview.Facts[0].Payload), `"marketProduct":"perp"`) {
		t.Fatalf("preview facts=%s", preview.Facts[0].Payload)
	}
	store := NewWorkStore(database)
	service := work.NewWithStore("contract-v1", time.Minute, store)
	service.SetClock(func() time.Time { return now })
	item, replay, err := service.Enqueue(ctx, work.EnqueueRequest{PrincipalID: "browser-a", IdempotencyKey: "browser-open", WorkKind: "open", LineageID: "browser-lineage", ProductID: "funding", Mode: "test", FactsCursor: preview.Facts[0].Cursor, Facts: preview.Facts, AccountFences: preview.AccountFences, Payload: preview.WorkPayload})
	if err != nil || replay {
		t.Fatalf("enqueue=%+v replay=%v err=%v", item, replay, err)
	}
	registration := service.Register(work.RegistrationRequest{WorkerID: "worker-a", InstanceID: "instance-a", ProductID: "funding", Mode: "test", ContractVersion: "contract-v1"})
	page, err := service.ClaimPage(work.ClaimRequest{WorkerID: registration.WorkerID, WorkerGeneration: registration.Generation, InstanceID: registration.InstanceID, IdempotencyKey: "claim-a", MaxItems: 1})
	if err != nil || page.WorkItem == nil || page.Lease == nil {
		t.Fatalf("claim=%+v err=%v", page, err)
	}
	snapshot, err := service.FactPage(work.FactRequest{WorkerID: registration.WorkerID, WorkerGeneration: registration.Generation, WorkItemID: page.WorkItem.WorkItemID, WorkRevision: page.WorkItem.WorkRevision, LeaseID: page.Lease.LeaseID, LeaseGeneration: page.Lease.LeaseGeneration, Limit: 500}, true)
	if err != nil || len(snapshot.Facts) != 1 || snapshot.Facts[0].Cursor != preview.Facts[0].Cursor {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	record, err := store.RecordIntent(ctx, page.Lease.LeaseID, work.IntentRequest{WorkerID: registration.WorkerID, WorkerGeneration: registration.Generation, WorkItemID: page.WorkItem.WorkItemID, WorkRevision: page.WorkItem.WorkRevision, LeaseGeneration: page.Lease.LeaseGeneration, IdempotencyKey: "intent-a", IntentKind: "funding.open", FactsCursor: page.WorkItem.FactsCursor, AccountFences: page.WorkItem.AccountFences, Payload: preview.WorkPayload}, now)
	if err != nil || len(record.Facts) != 1 || record.Facts[0].Cursor != preview.Facts[0].Cursor {
		t.Fatalf("record=%+v err=%v", record, err)
	}
	planStore := NewFundingPlanStore(database)
	producer, err := fundingexecution.NewPlanProducer(planStore, NewFundingExecutionStore(database))
	if err != nil {
		t.Fatal(err)
	}
	var frozen planning.PlanningFact
	if err := json.Unmarshal(record.Facts[0].Payload, &frozen); err != nil {
		t.Fatal(err)
	}
	rebound := planning.NewPlanningFact(frozen.Input, frozen.PrincipalID, frozen.LongReservation, frozen.ShortReservation, frozen.ReservationAuthorities, frozen.PolicyAssumptions)
	recomputed, computeErr := calculation.Compute(rebound.Input)
	if frozen.Input.AuthorityIdentity != rebound.Input.AuthorityIdentity || computeErr != nil || recomputed.PreviewID != preview.Plan.PreviewID {
		t.Fatalf("persisted planning identity=%q rebound=%q recomputed=%s preview=%s err=%v", frozen.Input.AuthorityIdentity, rebound.Input.AuthorityIdentity, recomputed.PreviewID, preview.Plan.PreviewID, computeErr)
	}
	if err := producer.Prepare(ctx, record); err != nil {
		t.Fatalf("Prepare() error=%v", err)
	}
	storedPlan, err := planStore.GetFundingPlan(ctx, "browser-a", orders.ModeTest, "open:"+preview.Plan.PreviewID)
	if err != nil || storedPlan.Mode != orders.ModeTest || len(storedPlan.Legs) != 2 {
		t.Fatalf("stored plan=%+v err=%v", storedPlan, err)
	}
}

func TestFundingWorkIntegrityRejectsMalformedAndCrossModeAccountFences(t *testing.T) {
	database, _ := openIntegrationDatabaseWithMigrations(t, true)
	ctx := context.Background()
	now := time.Date(2026, 8, 6, 4, 0, 0, 0, time.UTC)
	valid := json.RawMessage(`[{"accountId":"account-a","desiredStateRevision":1,"runtimeGeneration":1,"credentialVersion":1},{"accountId":"account-b","desiredStateRevision":1,"runtimeGeneration":1,"credentialVersion":1}]`)
	for index, fences := range []json.RawMessage{
		json.RawMessage(`{}`), json.RawMessage(`null`), json.RawMessage(`[]`),
		json.RawMessage(`[{"accountId":"account-a","desiredStateRevision":1,"runtimeGeneration":1,"credentialVersion":1}]`),
		json.RawMessage(`[{"accountId":"account-a","desiredStateRevision":1,"runtimeGeneration":1,"credentialVersion":1},{"accountId":"account-a","desiredStateRevision":2,"runtimeGeneration":2,"credentialVersion":2}]`),
		json.RawMessage(`[{"accountId":"account-a","desiredStateRevision":1.5,"runtimeGeneration":1,"credentialVersion":1},{"accountId":"account-b","desiredStateRevision":1,"runtimeGeneration":1,"credentialVersion":1}]`),
	} {
		_, err := database.ExecContext(ctx, `INSERT INTO platform_work_items(work_item_id,work_kind,lineage_id,work_revision,product_id,mode,status,facts_cursor,account_fences,payload,not_before,created_at,updated_at) VALUES ($1,'open',$2,1,'funding','test','open',$3,$4,'{}',$5,$5,$5)`, fmt.Sprintf("invalid-work-%d", index), fmt.Sprintf("invalid-lineage-%d", index), fmt.Sprintf("invalid-cursor-%d", index), fences, now)
		if err == nil {
			t.Fatalf("invalid account fences persisted: %s", fences)
		}
	}

	accountStore := NewAccountStore(database)
	for _, account := range []accounts.Account{
		{ID: "account-a", Venue: "venue-a", VenueEnvironment: accounts.EnvironmentTestnet, DisplayName: "Account A", ProductIDs: []string{"funding"}, SecretRef: "secret://test/a", DesiredStateRevision: 1, RuntimeGeneration: 1, CredentialVersion: 1, State: accounts.StateActive, UpdatedAt: now},
		{ID: "account-b", Venue: "venue-b", VenueEnvironment: accounts.EnvironmentLive, DisplayName: "Account B", ProductIDs: []string{"funding"}, SecretRef: "secret://live/b", DesiredStateRevision: 1, RuntimeGeneration: 1, CredentialVersion: 1, State: accounts.StateActive, UpdatedAt: now},
	} {
		if err := accountStore.Create(ctx, account); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO platform_work_items(work_item_id,work_kind,lineage_id,work_revision,product_id,mode,status,facts_cursor,account_fences,payload,not_before,created_at,updated_at) VALUES ('cross-mode-work','open','cross-mode-lineage',1,'funding','test','open','cross-mode-cursor',$1,'{}',$2,$2,$2)`, valid, now); err != nil {
		t.Fatal(err)
	}
	store := NewWorkStore(database)
	registration, err := store.RegisterWorker(ctx, work.RegistrationRequest{WorkerID: "cross-mode-worker", InstanceID: "cross-mode-instance", ProductID: "funding", Mode: "test", ContractVersion: "contract-v1", IdempotencyKey: "cross-mode-register"}, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.HeartbeatWorker(ctx, registration.WorkerID, work.HeartbeatRequest{WorkerGeneration: registration.Generation, InstanceID: registration.InstanceID, State: "ready"}, now); err != nil {
		t.Fatal(err)
	}
	page, err := store.ClaimWork(ctx, work.ClaimRequest{WorkerID: registration.WorkerID, WorkerGeneration: registration.Generation, InstanceID: registration.InstanceID, IdempotencyKey: "cross-mode-claim", MaxItems: 1}, now, time.Minute)
	if err != nil || page.WorkItem == nil || page.Lease == nil {
		t.Fatalf("claim=%+v err=%v", page, err)
	}
	_, err = store.FactPage(ctx, work.FactRequest{WorkerID: registration.WorkerID, WorkerGeneration: registration.Generation, WorkItemID: page.WorkItem.WorkItemID, WorkRevision: page.WorkItem.WorkRevision, LeaseID: page.Lease.LeaseID, LeaseGeneration: page.Lease.LeaseGeneration, Limit: 1}, true, now)
	if !errors.Is(err, work.ErrAuthorizationStale) {
		t.Fatalf("cross-mode account fence error=%v", err)
	}
}

func TestMigrateAdoptsVerifiedLegacyPlatformSchema(t *testing.T) {
	database, _ := openIntegrationDatabaseWithMigrations(t, false)
	ctx := context.Background()
	applyIntegrationMigrations(t, database, 2, 4)

	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	if _, err := database.ExecContext(ctx, `INSERT INTO platform_accounts(account_id,venue,venue_environment,display_name,product_ids,secret_ref,desired_state_revision,runtime_generation,credential_version,state,updated_at) VALUES ('legacy-account','binance','demo','Legacy Account','["funding"]','secret://legacy/account',1,1,1,'active',$1)`, now); err != nil {
		t.Fatal(err)
	}

	if err := Migrate(ctx, database); err != nil {
		t.Fatal(err)
	}
	var versions int
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM platform_schema_migrations`).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if want := expectedPlatformMigrationCount(t); versions != want {
		t.Fatalf("migration versions=%d want %d", versions, want)
	}
	var displayName string
	if err := database.QueryRowContext(ctx, `SELECT display_name FROM platform_accounts WHERE account_id='legacy-account'`).Scan(&displayName); err != nil {
		t.Fatal(err)
	}
	if displayName != "Legacy Account" {
		t.Fatalf("legacy account display name=%q", displayName)
	}
	for _, table := range []string{"platform_account_routes", "platform_work_items", "platform_portfolio_fills"} {
		var exists bool
		if err := database.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatalf("post-adoption table %s is missing", table)
		}
	}
	var accountIdentityColumns int
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='platform_accounts' AND column_name IN ('mode','native_environment')`).Scan(&accountIdentityColumns); err != nil {
		t.Fatal(err)
	}
	if accountIdentityColumns != 2 {
		t.Fatalf("platform account identity columns=%d want 2", accountIdentityColumns)
	}
}

func TestMigrateRejectsPartialLegacyPlatformSchema(t *testing.T) {
	database, _ := openIntegrationDatabaseWithMigrations(t, false)
	ctx := context.Background()
	applyIntegrationMigrations(t, database, 2, 2)

	if err := Migrate(ctx, database); !errors.Is(err, ErrMigrationMismatch) {
		t.Fatalf("partial legacy migration error=%v want %v", err, ErrMigrationMismatch)
	}
	var ledgerExists bool
	if err := database.QueryRowContext(ctx, `SELECT to_regclass('platform_schema_migrations') IS NOT NULL`).Scan(&ledgerExists); err != nil {
		t.Fatal(err)
	}
	if ledgerExists {
		t.Fatal("partial legacy schema created a migration ledger")
	}
}

func TestMigrateRejectsLegacyPlatformSchemaSemanticDrift(t *testing.T) {
	mutations := []struct {
		name string
		sql  string
	}{
		{name: "column type", sql: `ALTER TABLE platform_accounts ALTER COLUMN desired_state_revision TYPE integer`},
		{name: "column nullability", sql: `ALTER TABLE platform_accounts ALTER COLUMN display_name DROP NOT NULL`},
		{name: "column default", sql: `ALTER TABLE platform_commands ALTER COLUMN blockers SET DEFAULT '{}'::jsonb`},
		{name: "check constraint", sql: `
			ALTER TABLE platform_accounts DROP CONSTRAINT platform_accounts_state_check;
			ALTER TABLE platform_accounts ADD CONSTRAINT platform_accounts_state_check CHECK (state IN ('active','disabled'))`},
		{name: "unique constraint", sql: `
			ALTER TABLE platform_identities DROP CONSTRAINT platform_identities_mode_username_key;
			ALTER TABLE platform_identities ADD CONSTRAINT platform_identities_mode_username_key UNIQUE (mode, password_hash)`},
		{name: "foreign key", sql: `
			ALTER TABLE platform_sessions DROP CONSTRAINT platform_sessions_principal_id_fkey;
			ALTER TABLE platform_sessions ADD CONSTRAINT platform_sessions_principal_id_fkey FOREIGN KEY (principal_id) REFERENCES platform_identities(principal_id) ON DELETE CASCADE`},
		{name: "index definition", sql: `
			DROP INDEX platform_audit_resource_idx;
			CREATE INDEX platform_audit_resource_idx ON platform_audit (occurred_at DESC, resource_id)`},
	}

	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			database, _ := openIntegrationDatabaseWithMigrations(t, false)
			applyIntegrationMigrations(t, database, 2, 4)
			if _, err := database.Exec(mutation.sql); err != nil {
				t.Fatalf("mutate legacy schema: %v", err)
			}

			assertMigrationMismatchWithoutLedger(t, database)
		})
	}
}

func assertMigrationMismatchWithoutLedger(t *testing.T, database *sql.DB) {
	t.Helper()
	if err := Migrate(context.Background(), database); !errors.Is(err, ErrMigrationMismatch) {
		t.Fatalf("mutated legacy migration error=%v want %v", err, ErrMigrationMismatch)
	}
	var ledgerExists bool
	if err := database.QueryRow(`SELECT to_regclass('platform_schema_migrations') IS NOT NULL`).Scan(&ledgerExists); err != nil {
		t.Fatal(err)
	}
	if ledgerExists {
		t.Fatal("mutated legacy schema created a migration ledger")
	}
}

func TestWorkStoreHeartbeatAndDrainPreserveHeartbeatTTL(t *testing.T) {
	for _, test := range []struct {
		name string
		act  func(*WorkStore, work.Registration, time.Time) (work.Registration, error)
	}{
		{
			name: "heartbeat",
			act: func(store *WorkStore, registration work.Registration, now time.Time) (work.Registration, error) {
				return store.HeartbeatWorker(context.Background(), registration.WorkerID, work.HeartbeatRequest{WorkerGeneration: registration.Generation, InstanceID: registration.InstanceID, State: "ready"}, now)
			},
		},
		{
			name: "drain",
			act: func(store *WorkStore, registration work.Registration, now time.Time) (work.Registration, error) {
				return store.DrainWorker(context.Background(), registration.WorkerID, work.DrainRequest{WorkerGeneration: registration.Generation, InstanceID: registration.InstanceID, IdempotencyKey: "drain-a"}, now)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			database, _ := openIntegrationDatabase(t)
			store := NewWorkStore(database)
			now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
			registration, err := store.RegisterWorker(context.Background(), work.RegistrationRequest{WorkerID: "worker-a", InstanceID: "instance-a", ProductID: "funding", Mode: "test", ContractVersion: "contract-v1", IdempotencyKey: "register-a"}, now, 15*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			updatedAt := now.Add(time.Second)
			updated, err := test.act(store, registration, updatedAt)
			if err != nil {
				t.Fatal(err)
			}
			if !updated.HeartbeatExpiresAt.Equal(updatedAt.Add(15 * time.Second)) {
				t.Fatalf("heartbeat deadline=%s want %s", updated.HeartbeatExpiresAt, updatedAt.Add(15*time.Second))
			}
		})
	}
}

func TestWorkStoreIntentReplayRequiresCurrentLease(t *testing.T) {
	database, _ := openIntegrationDatabase(t)
	store, lease, request, now := seedClaimedWork(t, database)

	first, err := store.RecordIntent(context.Background(), lease.LeaseID, request, now)
	if err != nil {
		t.Fatal(err)
	}
	if first.Command.CommandID == "" {
		t.Fatal("first intent did not create a command")
	}

	if _, err := store.RecordIntent(context.Background(), lease.LeaseID, request, lease.ExpiresAt); !errors.Is(err, work.ErrLeaseExpired) {
		t.Fatalf("expired replay error=%v want %v", err, work.ErrLeaseExpired)
	}
}

func TestWorkStoreIntentReplayRequiresCurrentAccountFence(t *testing.T) {
	database, _ := openIntegrationDatabase(t)
	store, lease, request, now := seedClaimedWork(t, database)

	if _, err := store.RecordIntent(context.Background(), lease.LeaseID, request, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE platform_accounts SET desired_state_revision=desired_state_revision+1 WHERE account_id='account-a'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordIntent(context.Background(), lease.LeaseID, request, now.Add(time.Second)); !errors.Is(err, work.ErrAuthorizationStale) {
		t.Fatalf("stale-fence replay error=%v want %v", err, work.ErrAuthorizationStale)
	}
}

func TestWorkCompletionRequiresCompatibleTerminalAuthorityAndReleasesReservations(t *testing.T) {
	tests := []struct {
		name           string
		commandState   orders.State
		execution      work.FundingExecutionState
		executionLeg   orders.State
		reconciliation bool
		wantErr        bool
	}{
		{name: "submitted", commandState: orders.StateSubmitted, execution: work.FundingExecutionSubmitting, wantErr: true},
		{name: "uncertain", commandState: orders.StateUncertain, execution: work.FundingExecutionUncertain, wantErr: true},
		{name: "failed", commandState: orders.StateFailed, execution: work.FundingExecutionManual, wantErr: true},
		{name: "pending reconciliation", commandState: orders.StateFilled, execution: work.FundingExecutionHedged, reconciliation: true, wantErr: true},
		{name: "nonterminal execution leg", commandState: orders.StateFilled, execution: work.FundingExecutionHedged, executionLeg: orders.StateAccepted, wantErr: true},
		{name: "fully terminal", commandState: orders.StateFilled, execution: work.FundingExecutionHedged},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database, _ := openIntegrationDatabase(t)
			store, lease, request, now := seedClaimedWork(t, database)
			record, err := store.RecordIntent(context.Background(), lease.LeaseID, request, now)
			if err != nil {
				t.Fatal(err)
			}
			reservationID := "reservation-completion"
			if _, err := database.ExecContext(context.Background(), `INSERT INTO platform_reservations(reservation_id,principal_id,mode,product_id,reservation_kind,netting_scope,scope_key,quantity,status,idempotency_key,payload_hash,created_at,updated_at) VALUES($1,$2,'test','funding','open','{}','completion-scope',1,'consumed','completion-reservation','hash',$3,$3)`, reservationID, record.Command.PrincipalID, now); err != nil {
				t.Fatal(err)
			}
			if _, err := database.ExecContext(context.Background(), `UPDATE platform_commands SET state=$2,reservation_id=$3 WHERE command_id=$1`, record.Command.CommandID, test.commandState, reservationID); err != nil {
				t.Fatal(err)
			}
			execution := work.FundingExecution{ExecutionID: "execution-completion", PrincipalID: record.Command.PrincipalID, Mode: orders.ModeTest, LineageID: record.Command.LineageID, ParentCommandID: record.Command.CommandID, PlanKey: "plan-completion", PreviewHash: "preview-hash", IntentKind: request.IntentKind, State: test.execution, CreatedAt: now, UpdatedAt: now}
			if test.executionLeg != "" {
				execution.Legs = []work.FundingExecutionLeg{{Role: "long", CommandID: record.Command.CommandID, CommandState: test.executionLeg}}
			}
			if _, _, err := NewFundingExecutionStore(database).ReserveFundingExecution(context.Background(), execution); err != nil {
				t.Fatal(err)
			}
			if test.reconciliation {
				if _, err := database.ExecContext(context.Background(), `INSERT INTO platform_reconciliation_tasks(command_id,mode,reason,next_attempt_at) VALUES($1,'test','lookup_failed',$2)`, record.Command.CommandID, now); err != nil {
					t.Fatal(err)
				}
			}
			_, err = store.CompleteLease(context.Background(), lease.LeaseID, work.CompleteRequest{WorkerID: request.WorkerID, WorkerGeneration: request.WorkerGeneration, WorkItemID: request.WorkItemID, WorkRevision: request.WorkRevision, LeaseGeneration: request.LeaseGeneration, IdempotencyKey: "complete-" + test.name, FactsCursor: request.FactsCursor, Result: json.RawMessage(`{"outcome":"completed","commandIds":[],"finalFactsCursor":"cursor-a","summaryCode":"DONE"}`)}, now.Add(time.Second))
			if test.wantErr && !errors.Is(err, work.ErrDependencyUnavailable) {
				t.Fatalf("CompleteLease() error=%v, want dependency unavailable", err)
			}
			if !test.wantErr && err != nil {
				t.Fatal(err)
			}
			var workStatus, reservationStatus string
			if err := database.QueryRow(`SELECT status FROM platform_work_items WHERE work_item_id=$1`, request.WorkItemID).Scan(&workStatus); err != nil {
				t.Fatal(err)
			}
			if err := database.QueryRow(`SELECT status FROM platform_reservations WHERE reservation_id=$1`, reservationID).Scan(&reservationStatus); err != nil {
				t.Fatal(err)
			}
			if test.wantErr && (workStatus == "completed" || reservationStatus == "released") {
				t.Fatalf("rejected completion mutated work=%q reservation=%q", workStatus, reservationStatus)
			}
			if !test.wantErr && (workStatus != "completed" || reservationStatus != "released") {
				t.Fatalf("completed work=%q reservation=%q", workStatus, reservationStatus)
			}
		})
	}
}

func TestWorkCompletionAllowsPlanOnlyWorkWithoutIntent(t *testing.T) {
	database, _ := openIntegrationDatabase(t)
	store, lease, request, now := seedClaimedWork(t, database)
	_, err := store.CompleteLease(context.Background(), lease.LeaseID, work.CompleteRequest{WorkerID: request.WorkerID, WorkerGeneration: request.WorkerGeneration, WorkItemID: request.WorkItemID, WorkRevision: request.WorkRevision, LeaseGeneration: request.LeaseGeneration, IdempotencyKey: "complete-plan-only", FactsCursor: request.FactsCursor, Result: json.RawMessage(`{"outcome":"completed","commandIds":[],"finalFactsCursor":"cursor-a","summaryCode":"OPEN_PLANNED"}`)}, now)
	if err != nil {
		t.Fatal(err)
	}
}

func TestWorkAdmissionsHoldAccountFenceUntilCommit(t *testing.T) {
	for _, test := range []struct {
		name    string
		install func(*testing.T, *sql.DB, int64)
		start   func(*WorkStore, work.Lease, work.IntentRequest, time.Time) error
	}{
		{
			name: "facts",
			install: func(t *testing.T, database *sql.DB, lockKey int64) {
				t.Helper()
				if _, err := database.Exec(fmt.Sprintf(`
					ALTER TABLE platform_work_facts RENAME TO platform_work_facts_backing;
					CREATE FUNCTION block_work_admission() RETURNS boolean LANGUAGE plpgsql VOLATILE AS $$
					BEGIN PERFORM pg_advisory_xact_lock(%d); RETURN true; END $$;
					CREATE VIEW platform_work_facts AS SELECT * FROM platform_work_facts_backing WHERE block_work_admission()`, lockKey)); err != nil {
					t.Fatal(err)
				}
			},
			start: func(store *WorkStore, lease work.Lease, _ work.IntentRequest, now time.Time) error {
				_, err := store.FactPage(context.Background(), work.FactRequest{WorkerID: lease.WorkerID, WorkerGeneration: lease.WorkerGeneration, WorkItemID: lease.WorkItemID, WorkRevision: lease.WorkRevision, LeaseID: lease.LeaseID, LeaseGeneration: lease.LeaseGeneration, Limit: 10}, true, now)
				return err
			},
		},
		{
			name: "renewal",
			install: func(t *testing.T, database *sql.DB, lockKey int64) {
				t.Helper()
				installWorkAdmissionTrigger(t, database, lockKey, "platform_work_leases", "UPDATE")
			},
			start: func(store *WorkStore, lease work.Lease, _ work.IntentRequest, now time.Time) error {
				_, err := store.RenewLease(context.Background(), lease.LeaseID, work.LeaseRequest{WorkerID: lease.WorkerID, WorkerGeneration: lease.WorkerGeneration, WorkItemID: lease.WorkItemID, WorkRevision: lease.WorkRevision, LeaseGeneration: lease.LeaseGeneration, IdempotencyKey: "renew-atomic"}, now, 15*time.Second)
				return err
			},
		},
		{
			name: "intent",
			install: func(t *testing.T, database *sql.DB, lockKey int64) {
				t.Helper()
				installWorkAdmissionTrigger(t, database, lockKey, "platform_commands", "INSERT")
			},
			start: func(store *WorkStore, lease work.Lease, request work.IntentRequest, now time.Time) error {
				_, err := store.RecordIntent(context.Background(), lease.LeaseID, request, now)
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			database, reopen := openIntegrationDatabase(t)
			store, lease, request, now := seedClaimedWork(t, database)
			if _, err := database.ExecContext(context.Background(), `INSERT INTO platform_work_facts(fact_id,cursor,account_id,product_id,mode,kind,payload,effective_at) VALUES ('fact-a','cursor-fact','account-a','funding','test','funding.market','{}',$1)`, now); err != nil {
				t.Fatal(err)
			}
			lockKey := int64(4_200_000 + integrationSchemaSequence.Load())
			test.install(t, database, lockKey)

			lockDatabase := reopen()
			lockConnection, err := lockDatabase.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := lockConnection.ExecContext(context.Background(), `SELECT pg_advisory_lock($1)`, lockKey); err != nil {
				t.Fatal(err)
			}
			released := false
			release := func() {
				if released {
					return
				}
				released = true
				_, _ = lockConnection.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, lockKey)
				_ = lockConnection.Close()
			}
			defer release()

			workDone := make(chan error, 1)
			go func() { workDone <- test.start(store, lease, request, now.Add(time.Second)) }()
			waitForAdvisoryLock(t, reopen(), lockKey)

			accountDatabase := reopen()
			accountStore := NewAccountStore(accountDatabase)
			account, err := accountStore.Get(context.Background(), "account-a")
			if err != nil {
				t.Fatal(err)
			}
			account.DesiredStateRevision++
			account.UpdatedAt = now.Add(2 * time.Second)
			casDone := make(chan error, 1)
			go func() { casDone <- accountStore.CompareAndSwap(context.Background(), account.ID, 1, account) }()

			if blocked, err := waitForAccountCASLock(reopen(), casDone); !blocked {
				release()
				<-workDone
				t.Fatalf("account CAS completed before %s transaction committed: %v", test.name, err)
			}
			release()
			if err := <-workDone; err != nil {
				t.Fatalf("%s admission: %v", test.name, err)
			}
			if err := <-casDone; err != nil {
				t.Fatalf("account CAS after %s commit: %v", test.name, err)
			}
		})
	}
}

func installWorkAdmissionTrigger(t *testing.T, database *sql.DB, lockKey int64, table, operation string) {
	t.Helper()
	statement := fmt.Sprintf(`
		CREATE FUNCTION block_work_admission() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN PERFORM pg_advisory_xact_lock(%d); RETURN NEW; END $$;
		CREATE TRIGGER block_work_admission BEFORE %s ON %s FOR EACH ROW EXECUTE FUNCTION block_work_admission()`, lockKey, operation, table)
	if _, err := database.Exec(statement); err != nil {
		t.Fatal(err)
	}
}

func waitForAdvisoryLock(t *testing.T, database *sql.DB, lockKey int64) {
	t.Helper()
	defer database.Close()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var waiting bool
		if err := database.QueryRow(`SELECT EXISTS (
			SELECT 1 FROM pg_locks waiting
			JOIN pg_locks holder ON holder.locktype='advisory' AND holder.granted
				AND holder.classid=waiting.classid AND holder.objid=waiting.objid AND holder.objsubid=waiting.objsubid
			WHERE waiting.locktype='advisory' AND NOT waiting.granted AND waiting.objid=$1
		)`, lockKey).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("work admission did not reach advisory-lock barrier")
}

func waitForAccountCASLock(database *sql.DB, casDone <-chan error) (bool, error) {
	defer database.Close()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-casDone:
			return false, err
		default:
		}
		var waiting bool
		if err := database.QueryRow(`SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity
			WHERE datname=current_database() AND wait_event_type='Lock'
			AND query LIKE '%UPDATE platform_accounts SET venue=%'
		)`).Scan(&waiting); err != nil {
			return false, err
		}
		if waiting {
			return true, nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false, errors.New("account CAS did not block on the work transaction")
}

func TestPortfolioProjectionPersistsPositionAndPnLAcrossDatabaseRestart(t *testing.T) {
	database, reopen := openIntegrationDatabase(t)
	projection := portfolio.NewPersistentProjection(NewPortfolioStore(database))
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	open := portfolio.Fill{PrincipalID: "principal-a",
		FillID: "fill-open", CommandID: "command-open", LineageID: "lineage-a", ProductID: "funding",
		Mode: portfolio.ModeTest, AccountID: "account-a", InstrumentID: "BTC-USDT", Side: portfolio.SideBuy,
		Quantity: "2", Price: "100", Fee: "1", FeeCurrency: "USDT", SettlementCurrency: "USDT", OccurredAt: now,
	}
	if _, _, err := projection.ApplyFill(context.Background(), open); err != nil {
		t.Fatal(err)
	}
	marked, err := projection.ApplyMark("principal-a", portfolio.ModeTest, "account-a", "funding", "lineage-a", "BTC-USDT", "110", now.Add(time.Second))
	if err != nil || marked.UnrealizedPnL != "20" {
		t.Fatalf("marked=%+v err=%v", marked, err)
	}

	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	database = reopen()
	projection = portfolio.NewPersistentProjection(NewPortfolioStore(database))
	positions := projection.List("principal-a", portfolio.ModeTest, "account-a", "funding")
	if len(positions) != 1 || positions[0].Quantity != "2" || positions[0].AverageEntryPrice != "100" || positions[0].MarkPrice != "110" || positions[0].UnrealizedPnL != "20" {
		t.Fatalf("restarted positions=%+v", positions)
	}

	closeFill := open
	closeFill.FillID, closeFill.CommandID = "fill-close", "command-close"
	closeFill.Side, closeFill.Quantity, closeFill.Price, closeFill.Fee = portfolio.SideSell, "1", "120", "0.5"
	closeFill.OccurredAt = now.Add(2 * time.Second)
	closed, result, err := projection.ApplyFill(context.Background(), closeFill)
	if err != nil || closed.Quantity != "1" || closed.AverageEntryPrice != "100" || closed.RealizedPnL != "20" || closed.UnrealizedPnL != "10" || result.RealizedPnL != "20" || result.Fee != "0.5" {
		t.Fatalf("closed=%+v result=%+v err=%v", closed, result, err)
	}

	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	database = reopen()
	projection = portfolio.NewPersistentProjection(NewPortfolioStore(database))
	replayed, replayResult, err := projection.ApplyFill(context.Background(), closeFill)
	if err != nil || replayed.Quantity != "1" || replayed.RealizedPnL != "20" || replayed.UnrealizedPnL != "10" || replayResult.RealizedPnL != "20" || replayResult.Fee != "0.5" {
		t.Fatalf("replayed=%+v result=%+v err=%v", replayed, replayResult, err)
	}
	conflicting := closeFill
	conflicting.Price = "121"
	if _, _, err := projection.ApplyFill(context.Background(), conflicting); !errors.Is(err, portfolio.ErrFillConflict) {
		t.Fatalf("conflicting replay error=%v want %v", err, portfolio.ErrFillConflict)
	}
}

func TestTradingServicesCreateDistinctRowsAcrossDatabaseRestart(t *testing.T) {
	database, reopen := openIntegrationDatabase(t)
	ctx := context.Background()
	now := func() time.Time { return time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC) }

	reservationRequest := func(key, instrument string) reservations.CreateRequest {
		return reservations.CreateRequest{
			PrincipalID: "principal-a", Mode: reservations.ModeTest, ProductID: "manual", Kind: reservations.KindOpen,
			IdempotencyKey: key,
			Scope:          reservations.NettingScope{AccountID: "account-a", Venue: "binance", InstrumentID: instrument, VenueEnvironment: "demo"},
			Quantity:       "1",
		}
	}
	commandRequest := func(key string) orders.CreateRequest {
		return orders.CreateRequest{
			PrincipalID: "principal-a", Mode: orders.ModeTest, AccountID: "account-a", ProductID: "manual", LineageID: "lineage-a",
			IdempotencyKey: key, Method: "POST", Path: "/api/v1/commands", CommandKind: "manual.place", Payload: json.RawMessage(`{}`),
		}
	}

	firstReservation, err := reservations.NewService(NewReservationStore(database), now).Create(ctx, reservationRequest("reservation-first", "BTC-USDT"))
	if err != nil {
		t.Fatal(err)
	}
	firstCommand, replay, err := orders.NewService(NewCommandStore(database), nil, now).Create(ctx, commandRequest("command-first"))
	if err != nil || replay {
		t.Fatalf("first command=%+v replay=%v err=%v", firstCommand, replay, err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	database = reopen()
	secondReservation, err := reservations.NewService(NewReservationStore(database), now).Create(ctx, reservationRequest("reservation-second", "ETH-USDT"))
	if err != nil {
		t.Fatal(err)
	}
	secondCommand, replay, err := orders.NewService(NewCommandStore(database), nil, now).Create(ctx, commandRequest("command-second"))
	if err != nil || replay {
		t.Fatalf("second command=%+v replay=%v err=%v", secondCommand, replay, err)
	}
	if firstReservation.ReservationID == secondReservation.ReservationID || firstCommand.CommandID == secondCommand.CommandID {
		t.Fatalf("restart collision reservations=%q/%q commands=%q/%q", firstReservation.ReservationID, secondReservation.ReservationID, firstCommand.CommandID, secondCommand.CommandID)
	}
	var reservationRows, commandRows int
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM platform_reservations`).Scan(&reservationRows); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM platform_commands`).Scan(&commandRows); err != nil {
		t.Fatal(err)
	}
	if reservationRows != 2 || commandRows != 2 {
		t.Fatalf("persisted rows reservations=%d commands=%d, want 2 each", reservationRows, commandRows)
	}
}

func TestSubmittedCommandReconciliationSurvivesDatabaseRestartAndClearsOnTerminal(t *testing.T) {
	database, reopen := openIntegrationDatabase(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 6, 3, 0, 0, 0, time.UTC)
	service := orders.NewService(NewCommandStore(database), nil, func() time.Time { return now })
	command, _, err := service.Create(ctx, orders.CreateRequest{PrincipalID: "principal-a", AccountID: "account-a", ProductID: "manual", LineageID: "lineage-a", IdempotencyKey: "reconcile-restart", Method: "POST", Path: "/api/v1/commands", CommandKind: "manual.place", Mode: orders.ModeTest, Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	evidence := orders.ExecutionEvidence{CommandID: "gateway-command", GatewayOrderID: "gateway-order", ClientOrderID: "client-order", State: "submitted", ResourceVersion: 1}
	if _, err := service.Transition(ctx, orders.TransitionRequest{CommandID: command.CommandID, State: orders.StateSubmitted, Evidence: evidence}); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	database = reopen()
	service = orders.NewService(NewCommandStore(database), nil, func() time.Time { return now })
	due, err := service.ClaimDueReconciliation(ctx, orders.ModeTest, now.Add(5*time.Second), time.Minute, 10)
	if err != nil || len(due) != 1 || due[0].CommandID != command.CommandID {
		t.Fatalf("restart due=%+v err=%v", due, err)
	}
	evidence.State, evidence.ResourceVersion = "filled", 2
	if _, err := service.Transition(ctx, orders.TransitionRequest{CommandID: command.CommandID, State: orders.StateFilled, Evidence: evidence}); err != nil {
		t.Fatal(err)
	}
	if due, err = service.ClaimDueReconciliation(ctx, orders.ModeTest, now.Add(time.Hour), time.Minute, 10); err != nil || len(due) != 0 {
		t.Fatalf("terminal due=%+v err=%v", due, err)
	}
}

func TestManualLifecyclePersistsCancelCloseAndReplayAcrossDatabaseRestart(t *testing.T) {
	database, reopen := openIntegrationDatabase(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 8, 6, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	gateway := &persistentManualGateway{now: now}

	newServices := func() (*orders.Service, *portfolio.Projection, *ledger.Service, *manual.Service) {
		commands := orders.NewService(NewCommandStore(database), nil, clock)
		positions := portfolio.NewPersistentProjection(NewPortfolioStore(database))
		entries := ledger.NewService(NewLedgerStore(database), clock)
		return commands, positions, entries, manual.NewGatewayService(commands, positions, entries, gateway, clock)
	}
	create := func(t *testing.T, commands *orders.Service, key, payload string) orders.Command {
		t.Helper()
		command, replay, err := commands.Create(ctx, orders.CreateRequest{
			PrincipalID: "principal-a", AccountID: "account-a", ProductID: "manual", LineageID: "manual-lineage-a",
			IdempotencyKey: key, Method: http.MethodPost, Path: "/api/v1/products/manual/commands",
			CommandKind: "manual.place", Mode: orders.ModeTest, Payload: json.RawMessage(payload),
		})
		if err != nil || replay {
			t.Fatalf("create command=%+v replay=%v err=%v", command, replay, err)
		}
		return command
	}

	commands, _, _, service := newServices()
	open := create(t, commands, "manual-open-a", `{"instrumentId":"BTC-USDT","side":"buy","quantity":"2","orderType":"market"}`)
	if result, err := service.Execute(ctx, open); err != nil || result.State != orders.StateFilled {
		t.Fatalf("open=%+v err=%v", result, err)
	}
	resting := create(t, commands, "manual-resting-a", `{"instrumentId":"BTC-USDT","side":"sell","quantity":"1","orderType":"limit","limitPrice":"110","timeInForce":"gtc","reduceOnly":true}`)
	if result, err := service.Execute(ctx, resting); err != nil || result.State != orders.StateSubmitted {
		t.Fatalf("resting=%+v err=%v", result, err)
	}

	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	database = reopen()
	commands, positions, entries, service := newServices()
	restored, err := commands.Get(ctx, resting.CommandID)
	if err != nil || restored.State != orders.StateSubmitted || restored.ExecutionEvidence.GatewayOrderID == "" {
		t.Fatalf("restored resting=%+v err=%v", restored, err)
	}
	if canceled, err := service.Cancel(ctx, restored); err != nil || canceled.State != orders.StateCanceled {
		t.Fatalf("cancel=%+v err=%v", canceled, err)
	}
	closeCommand := create(t, commands, "manual-close-a", `{"instrumentId":"BTC-USDT","side":"sell","quantity":"2","orderType":"market","reduceOnly":true}`)
	closed, err := service.Execute(ctx, closeCommand)
	if err != nil || closed.State != orders.StateFilled {
		t.Fatalf("close=%+v err=%v", closed, err)
	}
	if got := positions.List("principal-a", portfolio.ModeTest, "account-a", "manual"); len(got) != 1 || got[0].Quantity != "0" || got[0].RealizedPnL != "20" {
		t.Fatalf("closed positions=%+v", got)
	}
	accounting, err := entries.List(ctx, ledger.Query{PrincipalID: "principal-a", Mode: ledger.ModeTest, ProductID: "manual", LineageID: "manual-lineage-a"})
	if err != nil || len(accounting) != 3 {
		t.Fatalf("accounting=%+v err=%v", accounting, err)
	}

	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	database = reopen()
	commands, positions, entries, service = newServices()
	persistedClose, err := commands.Get(ctx, closeCommand.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	if replayed, err := service.Execute(ctx, persistedClose); err != nil || replayed.State != orders.StateFilled || gateway.executeCalls != 3 {
		t.Fatalf("replayed close=%+v calls=%d err=%v", replayed, gateway.executeCalls, err)
	}
	if got := positions.List("principal-a", portfolio.ModeTest, "account-a", "manual"); len(got) != 1 || got[0].Quantity != "0" || got[0].RealizedPnL != "20" {
		t.Fatalf("restarted positions=%+v", got)
	}
	accounting, err = entries.List(ctx, ledger.Query{PrincipalID: "principal-a", Mode: ledger.ModeTest, ProductID: "manual", LineageID: "manual-lineage-a"})
	if err != nil || len(accounting) != 3 || gateway.cancelCalls != 1 {
		t.Fatalf("restarted accounting=%+v cancel calls=%d err=%v", accounting, gateway.cancelCalls, err)
	}
}

type persistentManualGateway struct {
	now                       time.Time
	executeCalls, cancelCalls int
}

func (gateway *persistentManualGateway) Execute(_ context.Context, request manual.GatewayRequest) (manual.GatewayResult, error) {
	gateway.executeCalls++
	state := manual.GatewayStateFilled
	evidenceState := "filled"
	if request.OrderType == "limit" {
		state, evidenceState = manual.GatewayStateSubmitted, "submitted"
	}
	evidence := orders.ExecutionEvidence{
		CommandID: request.CommandID, GatewayOrderID: "gateway-" + request.CommandID,
		ClientOrderID: "client-" + request.CommandID, State: evidenceState, ResourceVersion: 1,
	}
	result := manual.GatewayResult{
		CommandID: request.CommandID, AccountID: request.AccountID, Mode: request.Mode, State: state,
		Handoff: orders.HandoffResult{Outcome: orders.HandoffAccepted, Evidence: evidence},
	}
	if state == manual.GatewayStateFilled {
		price := "100"
		if request.ReduceOnly {
			price = "110"
		}
		fill := manual.FillFact{
			FillID: "fill-" + request.CommandID, CommandID: request.CommandID, AccountID: request.AccountID,
			InstrumentID: request.InstrumentID, Mode: request.Mode, Side: portfolio.Side(request.Side),
			Quantity: request.Quantity, Price: price, Fee: "1", FeeCurrency: "USDT", SettlementCurrency: "USDT", OccurredAt: gateway.now.Add(time.Duration(gateway.executeCalls) * time.Second),
		}
		result.Fills = []manual.FillFact{fill}
		result.Handoff.Evidence.Fills = []orders.FillEvidence{{
			FillID: fill.FillID, AccountID: fill.AccountID, InstrumentID: fill.InstrumentID, Side: string(fill.Side),
			Quantity: fill.Quantity, Price: fill.Price, Fee: fill.Fee, FeeCurrency: fill.FeeCurrency,
			SettlementCurrency: fill.SettlementCurrency, OccurredAt: fill.OccurredAt,
		}}
	}
	return result, nil
}

func (gateway *persistentManualGateway) Cancel(_ context.Context, request manual.GatewayCancelRequest) (manual.GatewayResult, error) {
	gateway.cancelCalls++
	evidence := orders.ExecutionEvidence{
		CommandID: request.CommandID, GatewayOrderID: request.GatewayOrderID,
		ClientOrderID: "client-" + request.CommandID, State: "canceled", ResourceVersion: 2,
	}
	return manual.GatewayResult{
		CommandID: request.CommandID, AccountID: request.AccountID, Mode: request.Mode,
		State: manual.GatewayStateCanceled, Handoff: orders.HandoffResult{Outcome: orders.HandoffAccepted, Evidence: evidence},
	}, nil
}

func seedClaimedWork(t *testing.T, database *sql.DB) (*WorkStore, work.Lease, work.IntentRequest, time.Time) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	fences := json.RawMessage(`[{"accountId":"account-a","desiredStateRevision":1,"runtimeGeneration":1,"credentialVersion":1},{"accountId":"account-b","desiredStateRevision":1,"runtimeGeneration":1,"credentialVersion":1}]`)
	if _, err := database.ExecContext(ctx, `INSERT INTO platform_accounts(account_id,venue,venue_environment,mode,native_environment,display_name,product_ids,secret_ref,desired_state_revision,runtime_generation,credential_version,state,updated_at) VALUES ('account-a','binance','demo','test','demo','Account A','["funding"]','secret://test/account-a',1,1,1,'active',$1)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO platform_accounts(account_id,venue,venue_environment,mode,native_environment,display_name,product_ids,secret_ref,desired_state_revision,runtime_generation,credential_version,state,updated_at) VALUES ('account-b','binance','demo','test','demo','Account B','["funding"]','secret://test/account-b',1,1,1,'active',$1)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO platform_work_items(work_item_id,work_kind,lineage_id,work_revision,product_id,mode,status,facts_cursor,account_fences,payload,not_before,created_at,updated_at) VALUES ('work-a','open','lineage-a',1,'funding','test','open','cursor-a',$1,$2,$3,$3,$3)`, fences, json.RawMessage(`{"notionalPerLeg":"50","accountId":"account-a"}`), now); err != nil {
		t.Fatal(err)
	}
	store := NewWorkStore(database)
	registration, err := store.RegisterWorker(ctx, work.RegistrationRequest{WorkerID: "worker-a", InstanceID: "instance-a", ProductID: "funding", Mode: "test", ContractVersion: "contract-v1", IdempotencyKey: "register-a"}, now, 15*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.HeartbeatWorker(ctx, registration.WorkerID, work.HeartbeatRequest{WorkerGeneration: registration.Generation, InstanceID: registration.InstanceID, State: "ready"}, now); err != nil {
		t.Fatal(err)
	}
	page, err := store.ClaimWork(ctx, work.ClaimRequest{WorkerID: registration.WorkerID, WorkerGeneration: registration.Generation, InstanceID: registration.InstanceID, IdempotencyKey: "claim-a", MaxItems: 1}, now, 15*time.Second)
	if err != nil || page.WorkItem == nil || page.Lease == nil {
		t.Fatalf("claim=%+v err=%v", page, err)
	}
	request := work.IntentRequest{WorkerID: registration.WorkerID, WorkerGeneration: registration.Generation, WorkItemID: page.WorkItem.WorkItemID, WorkRevision: page.WorkItem.WorkRevision, LeaseGeneration: page.Lease.LeaseGeneration, IdempotencyKey: "intent-a", IntentKind: "funding.open", FactsCursor: page.WorkItem.FactsCursor, AccountFences: page.WorkItem.AccountFences, Payload: json.RawMessage(`{"notionalPerLeg":"50","accountId":"account-a"}`)}
	return store, *page.Lease, request, now
}

func openIntegrationDatabase(t *testing.T) (*sql.DB, func() *sql.DB) {
	return openIntegrationDatabaseWithMigrations(t, true)
}

func openIntegrationDatabaseWithMigrations(t *testing.T, applyMigrations bool) (*sql.DB, func() *sql.DB) {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("BT_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("BT_TEST_POSTGRES_DSN is not set")
	}
	adminConfig, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin := stdlib.OpenDB(*adminConfig)
	if err := admin.Ping(); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	schema := fmt.Sprintf("bolter_test_%d_%d", os.Getpid(), integrationSchemaSequence.Add(1))
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
		_ = admin.Close()
	})
	testConfig, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	testConfig.RuntimeParams["search_path"] = schema
	var databases []*sql.DB
	reopen := func() *sql.DB {
		database := stdlib.OpenDB(*testConfig)
		database.SetMaxOpenConns(1)
		databases = append(databases, database)
		return database
	}
	database := reopen()
	t.Cleanup(func() {
		for _, opened := range databases {
			_ = opened.Close()
		}
	})
	if applyMigrations {
		applyIntegrationMigrations(t, database, 1, 9999)
	}
	return database, reopen
}

func applyIntegrationMigrations(t *testing.T, database *sql.DB, first, last int) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join("..", "..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	var migrations []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		var version int
		if _, err := fmt.Sscanf(entry.Name(), "%04d_", &version); err == nil && version >= first && version <= last {
			migrations = append(migrations, entry.Name())
		}
	}
	sort.Strings(migrations)
	for _, name := range migrations {
		migration, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(string(migration)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
}

func expectedPlatformMigrationCount(t *testing.T) int {
	t.Helper()
	migrations, err := embeddedPlatformMigrations()
	if err != nil {
		t.Fatal(err)
	}
	return len(migrations)
}
