package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/gateway/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func newTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("GATEWAY_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("GATEWAY_TEST_POSTGRES_DSN is not set; skipping real PostgreSQL integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	schema := fmt.Sprintf("gateway_test_%d", time.Now().UnixNano())
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse postgres dsn: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	config.ConnConfig.RuntimeParams["statement_timeout"] = "5000"
	config.ConnConfig.RuntimeParams["lock_timeout"] = "1000"

	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	_, err = admin.Exec(ctx, `CREATE SCHEMA `+schema)
	admin.Close()
	if err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		admin, err := pgxpool.New(cleanupCtx, dsn)
		if err == nil {
			_, _ = admin.Exec(cleanupCtx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
			admin.Close()
		}
	})

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("connect schema pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func testIdentityGenerator() *domain.IdentityGenerator {
	return domain.NewIdentityGenerator(domain.IdentityGeneratorOptions{
		Now:    func() time.Time { return time.UnixMilli(1710000000123) },
		Random: bytes.NewReader(bytes.Repeat([]byte{0x42}, 1<<20)),
	})
}

func testAccountID(t *testing.T) domain.AccountID {
	t.Helper()
	name := strings.NewReplacer("/", "-", " ", "-", "_", "-").Replace(strings.ToLower(t.Name()))
	return domain.AccountID(fmt.Sprintf("binance-main-%d-%s", os.Getpid(), name))
}

func registerTestAccount(t *testing.T, ctx context.Context, postgresStore *Store) {
	t.Helper()
	err := postgresStore.RegisterAccount(ctx, AccountRegistration{
		AccountID:   testAccountID(t),
		Venue:       "binance",
		Product:     "spot",
		Environment: "demo",
		ConfigHash:  strings.Repeat("a", 64),
		Status:      "enabled",
	})
	if err != nil {
		t.Fatalf("RegisterAccount: %v", err)
	}
}

func reserveRequest(t *testing.T, payload json.RawMessage) store.ReserveCommandRequest {
	t.Helper()
	hash, err := domain.CanonicalPayloadHash(payload)
	if err != nil {
		panic(err)
	}
	return store.ReserveCommandRequest{
		PrincipalID:    domain.PrincipalID("principal-a"),
		AccountID:      testAccountID(t),
		InstrumentID:   domain.InstrumentID("binance:demo:spot:BTC-USDT"),
		IdempotencyKey: domain.IdempotencyKey("strategy-a-1"),
		CorrelationID:  domain.EventID("018f0000-0000-7000-8000-000000000003"),
		PayloadHash:    hash,
		Payload:        payload,
	}
}

func TestMigrateRealPostgresIsIdempotentAndFailClosed(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate empty database: %v", err)
	}
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate second run: %v", err)
	}

	_, err := pool.Exec(ctx, `UPDATE schema_migrations SET checksum = 'mutated' WHERE version = 1`)
	if err != nil {
		t.Fatalf("mutate checksum: %v", err)
	}
	if err := Migrate(ctx, pool); err == nil {
		t.Fatal("Migrate accepted historical checksum mutation")
	}
}

func TestMigrateRealPostgresHoldsAndReleasesAdvisoryLockOnOneConnection(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	var observedLockHeld bool
	err := migrateWithOptions(ctx, pool, migrateOptions{
		afterLock: func(ctx context.Context) error {
			var acquired bool
			if err := pool.QueryRow(ctx, `SELECT pg_try_advisory_lock($1, $2)`, migrationLockKey, int32(1)).Scan(&acquired); err != nil {
				return err
			}
			observedLockHeld = !acquired
			if acquired {
				_, _ = pool.Exec(ctx, `SELECT pg_advisory_unlock($1, $2)`, migrationLockKey, int32(1))
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("migrateWithOptions: %v", err)
	}
	if !observedLockHeld {
		t.Fatal("migration advisory lock was not visible as held from a second session")
	}
	var acquiredAfter bool
	if err := pool.QueryRow(ctx, `SELECT pg_try_advisory_lock($1, $2)`, migrationLockKey, int32(1)).Scan(&acquiredAfter); err != nil {
		t.Fatalf("probe migration lock after migrate: %v", err)
	}
	if !acquiredAfter {
		t.Fatal("migration advisory lock leaked after migrate returned")
	}
	_, _ = pool.Exec(ctx, `SELECT pg_advisory_unlock($1, $2)`, migrationLockKey, int32(1))
}

func TestMigrateRealPostgresUnlockFailureDiscardsConnectionAndDoesNotLeakLock(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	err := migrateWithOptions(ctx, pool, migrateOptions{
		beforeUnlock: func(conn *pgxpool.Conn) {
			_ = conn.Conn().Close(context.Background())
		},
	})
	if err == nil || !strings.Contains(err.Error(), "release migration lock") {
		t.Fatalf("migrateWithOptions error = %v, want sanitized unlock failure", err)
	}

	retryCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := Migrate(retryCtx, pool); err != nil {
		t.Fatalf("Migrate after unlock connection failure: %v", err)
	}

	var acquired bool
	if err := pool.QueryRow(retryCtx, `SELECT pg_try_advisory_lock($1, $2)`, migrationLockKey, int32(1)).Scan(&acquired); err != nil {
		t.Fatalf("probe migration lock after retry: %v", err)
	}
	if !acquired {
		t.Fatal("migration advisory lock leaked after unlock connection failure")
	}
	_, _ = pool.Exec(retryCtx, `SELECT pg_advisory_unlock($1, $2)`, migrationLockKey, int32(1))
}

func TestMigrateRealPostgresUnlockFalseDiscardsConnection(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	var injectedUnlockErr error

	err := migrateWithOptions(ctx, pool, migrateOptions{
		beforeUnlock: func(conn *pgxpool.Conn) {
			var unlocked bool
			injectedUnlockErr = conn.QueryRow(ctx, `SELECT pg_advisory_unlock($1, $2)`, migrationLockKey, int32(1)).Scan(&unlocked)
			if injectedUnlockErr == nil && !unlocked {
				injectedUnlockErr = errors.New("test setup did not release migration lock")
			}
		},
	})
	if injectedUnlockErr != nil {
		t.Fatalf("inject prior unlock: %v", injectedUnlockErr)
	}
	if err == nil || !strings.Contains(err.Error(), "release migration lock") {
		t.Fatalf("migrateWithOptions error = %v, want unlock-not-held failure", err)
	}

	retryCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := Migrate(retryCtx, pool); err != nil {
		t.Fatalf("Migrate after false unlock result: %v", err)
	}
}

func TestMigrateRealPostgresConcurrentStartersSerialize(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	const starters = 4
	errs := make(chan error, starters)
	var wg sync.WaitGroup
	for range starters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- Migrate(ctx, pool)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Migrate: %v", err)
		}
	}
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&rows); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if rows != 4 {
		t.Fatalf("schema_migrations rows = %d, want 4", rows)
	}
}

func TestCommandStoreRealPostgresReservesIdempotentlyAndAtomically(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	postgresStore := newStoreForTest(pool, storeOptions{identity: testIdentityGenerator()})
	registerTestAccount(t, ctx, postgresStore)

	first, err := postgresStore.ReserveCommand(ctx, reserveRequest(t, json.RawMessage(`{"quantity":"0.001"}`)))
	if err != nil {
		t.Fatalf("ReserveCommand first: %v", err)
	}
	if !first.New || first.State != domain.StatePrepared || first.ClientOrderID != "1" {
		t.Fatalf("first reservation = %+v", first)
	}

	second, err := postgresStore.ReserveCommand(ctx, reserveRequest(t, json.RawMessage(`{"quantity":"0.001"}`)))
	if err != nil {
		t.Fatalf("ReserveCommand replay: %v", err)
	}
	if second.New || second.CommandID != first.CommandID || second.GatewayOrderID != first.GatewayOrderID || second.ClientOrderID != first.ClientOrderID {
		t.Fatalf("idempotent replay changed identity: first=%+v second=%+v", first, second)
	}

	conflict, err := postgresStore.ReserveCommand(ctx, reserveRequest(t, json.RawMessage(`{"quantity":"0.002"}`)))
	if err != nil {
		t.Fatalf("ReserveCommand conflict: %v", err)
	}
	if conflict.Conflict != store.ConflictPayload || conflict.CommandID != first.CommandID {
		t.Fatalf("payload conflict = %+v, want existing command and payload conflict", conflict)
	}

	var commandRows, orderRows, eventRows, auditRows int
	err = pool.QueryRow(ctx, `SELECT count(*) FROM idempotency_commands`).Scan(&commandRows)
	if err != nil {
		t.Fatalf("count commands: %v", err)
	}
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM orders`).Scan(&orderRows)
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM order_events`).Scan(&eventRows)
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM audit_records`).Scan(&auditRows)
	if commandRows != 1 || orderRows != 1 || eventRows != 1 || auditRows != 1 {
		t.Fatalf("atomic row counts command/order/event/audit = %d/%d/%d/%d, want 1/1/1/1", commandRows, orderRows, eventRows, auditRows)
	}
	var principalID, action, resourceID, outcome string
	var metadata []byte
	var occurredAt time.Time
	if err := pool.QueryRow(ctx, `
		SELECT principal_id, action, resource_id, outcome, metadata, occurred_at
		FROM audit_records
	`).Scan(&principalID, &action, &resourceID, &outcome, &metadata, &occurredAt); err != nil {
		t.Fatalf("read atomic audit: %v", err)
	}
	if principalID != "principal-a" || action != string(store.AuditPlaceOrder) ||
		resourceID != string(first.GatewayOrderID) || outcome != string(store.AuditPrepared) ||
		occurredAt.IsZero() {
		t.Fatalf("atomic audit principal/action/resource/outcome/time = %q/%q/%q/%q/%v",
			principalID, action, resourceID, outcome, occurredAt)
	}
	var auditMetadata map[string]string
	if err := json.Unmarshal(metadata, &auditMetadata); err != nil {
		t.Fatalf("decode audit metadata: %v", err)
	}
	if auditMetadata["correlation_id"] != "018f0000-0000-7000-8000-000000000003" ||
		auditMetadata["account_id"] != string(testAccountID(t)) ||
		auditMetadata["instrument_id"] != "binance:demo:spot:BTC-USDT" {
		t.Fatalf("audit metadata=%v", auditMetadata)
	}
	for _, forbidden := range []string{"quantity", "0.001", "payload", "secret", "signature"} {
		if strings.Contains(strings.ToLower(string(metadata)), forbidden) {
			t.Fatalf("audit metadata contains forbidden %q: %s", forbidden, metadata)
		}
	}
}

func TestCommandStoreRealPostgresPersistsAndResolvesGenerationHandoff(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	postgresStore := NewStore(pool)
	registerTestAccount(t, ctx, postgresStore)
	reserved, err := postgresStore.ReserveCommand(ctx, reserveRequest(t, json.RawMessage(`{"quantity":"0.001"}`)))
	if err != nil {
		t.Fatal(err)
	}
	request := store.BeginOrderHandoffRequest{
		HandoffID: "018f0000-0000-7000-8000-000000000090", CommandID: reserved.CommandID,
		AccountID: testAccountID(t), RouteID: "spot",
		Fence: domain.AccountFence{AccountID: testAccountID(t), DesiredStateRevision: 1, RuntimeGeneration: 1, CredentialVersion: 1},
	}
	dispatching, err := postgresStore.MarkDispatching(ctx, store.MarkDispatchingRequest{
		CommandID: reserved.CommandID, EventID: "018f0000-0000-7000-8000-000000000091",
	})
	if err != nil {
		t.Fatal(err)
	}
	handoff, err := postgresStore.BeginOrderHandoff(ctx, request)
	if err != nil || handoff.State != store.OrderHandoffStarted || handoff.ResolvedAt != nil {
		t.Fatalf("started handoff=%+v err=%v", handoff, err)
	}
	if _, err := postgresStore.RecordVenueResult(ctx, store.RecordVenueResultRequest{
		CommandID: reserved.CommandID, EventID: "018f0000-0000-7000-8000-000000000092",
		NextState: domain.StateAccepted, ResourceVersion: dispatching.ResourceVersion,
	}); err != nil {
		t.Fatal(err)
	}
	resolved, err := postgresStore.loadOrderHandoff(ctx, request.HandoffID)
	if err != nil || resolved.State != store.OrderHandoffAccepted || resolved.ResolvedAt == nil {
		t.Fatalf("resolved handoff=%+v err=%v", resolved, err)
	}
}

func TestAuditStoreRealPostgresAppendsOnlySafeStructuredFields(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	postgresStore := newStoreForTest(pool, storeOptions{identity: testIdentityGenerator()})
	record := store.AuditRecord{
		PrincipalID:   "principal-a",
		Action:        store.AuditSetLeverage,
		ResourceID:    "binance:demo:perp:USDT:BTC-USDT",
		Outcome:       store.AuditRequested,
		CorrelationID: "018f0000-0000-7000-8000-000000000003",
		AccountID:     "acct-a",
		InstrumentID:  "binance:demo:perp:USDT:BTC-USDT",
	}
	if err := postgresStore.AppendAudit(ctx, record); err != nil {
		t.Fatalf("AppendAudit: %v", err)
	}
	var action, resourceID, outcome string
	var metadata []byte
	if err := pool.QueryRow(ctx, `
		SELECT action, resource_id, outcome, metadata
		FROM audit_records
	`).Scan(&action, &resourceID, &outcome, &metadata); err != nil {
		t.Fatal(err)
	}
	if action != string(record.Action) || resourceID != record.ResourceID || outcome != string(record.Outcome) {
		t.Fatalf("audit row=%q/%q/%q", action, resourceID, outcome)
	}
	if strings.Contains(strings.ToLower(string(metadata)), "secret") ||
		strings.Contains(strings.ToLower(string(metadata)), "payload") {
		t.Fatalf("unsafe audit metadata=%s", metadata)
	}
}

func TestCommandStoreRealPostgresScopesOrderReadsToPrincipalAndFilters(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	postgresStore := newStoreForTest(pool, storeOptions{identity: testIdentityGenerator()})
	registerTestAccount(t, ctx, postgresStore)

	requestA := reserveRequest(t, json.RawMessage(`{"quantity":"0.001"}`))
	first, err := postgresStore.ReserveCommand(ctx, requestA)
	if err != nil {
		t.Fatal(err)
	}
	requestA.IdempotencyKey = "strategy-a-2"
	requestA.InstrumentID = "binance:demo:spot:ETH-USDT"
	requestA.CorrelationID = "018f0000-0000-7000-8000-000000000004"
	requestA.Payload = json.RawMessage(`{"quantity":"0.002"}`)
	requestA.PayloadHash, _ = domain.CanonicalPayloadHash(requestA.Payload)
	second, err := postgresStore.ReserveCommand(ctx, requestA)
	if err != nil {
		t.Fatal(err)
	}
	requestB := reserveRequest(t, json.RawMessage(`{"quantity":"0.003"}`))
	requestB.PrincipalID = "principal-b"
	requestB.IdempotencyKey = "strategy-b-1"
	requestB.CorrelationID = "018f0000-0000-7000-8000-000000000005"
	requestB.PayloadHash, _ = domain.CanonicalPayloadHash(requestB.Payload)
	if _, err := postgresStore.ReserveCommand(ctx, requestB); err != nil {
		t.Fatal(err)
	}

	loaded, err := postgresStore.LoadOrder(ctx, store.LoadOrderRequest{
		PrincipalID: "principal-a", GatewayOrderID: first.GatewayOrderID,
	})
	if err != nil || loaded.PrincipalID != "principal-a" || loaded.GatewayOrderID != first.GatewayOrderID {
		t.Fatalf("principal-a LoadOrder=%+v err=%v", loaded, err)
	}
	if _, err := postgresStore.LoadOrder(ctx, store.LoadOrderRequest{
		PrincipalID: "principal-b", GatewayOrderID: first.GatewayOrderID,
	}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-principal LoadOrder error=%v, want not found", err)
	}

	filtered, err := postgresStore.ListOrders(ctx, store.ListOrdersRequest{
		PrincipalID: "principal-a", InstrumentID: "binance:demo:spot:ETH-USDT", Limit: 10,
	})
	if err != nil || len(filtered) != 1 || filtered[0].GatewayOrderID != second.GatewayOrderID {
		t.Fatalf("instrument-filtered orders=%+v err=%v", filtered, err)
	}
	page, err := postgresStore.ListOrders(ctx, store.ListOrdersRequest{
		PrincipalID: "principal-a", Limit: 1, Cursor: first.GatewayOrderID,
	})
	if err != nil || len(page) != 1 || page[0].GatewayOrderID != second.GatewayOrderID {
		t.Fatalf("cursor page=%+v err=%v", page, err)
	}
}

func TestCommandStoreRealPostgresConcurrentSameIdempotencyKeyReplaysSamePayload(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	barrier := newReserveBarrier(2)
	postgresStore := newStoreForTest(pool, storeOptions{
		identity:            testIdentityGenerator(),
		beforeReserveInsert: barrier.Wait,
	})
	registerTestAccount(t, ctx, postgresStore)

	results, errs := runConcurrentReservations(ctx, postgresStore,
		reserveRequest(t, json.RawMessage(`{"quantity":"0.001"}`)),
		reserveRequest(t, json.RawMessage(`{"quantity":"0.001"}`)),
	)
	for _, err := range errs {
		if err != nil {
			t.Fatalf("ReserveCommand concurrent same payload: %v", err)
		}
	}
	if results[0].CommandID != results[1].CommandID ||
		results[0].GatewayOrderID != results[1].GatewayOrderID ||
		results[0].ClientOrderID != results[1].ClientOrderID {
		t.Fatalf("same idempotency replay returned different identities: %+v vs %+v", results[0], results[1])
	}
	newCount := 0
	for _, result := range results {
		if result.New {
			newCount++
		}
	}
	if newCount != 1 {
		t.Fatalf("New count = %d, want 1", newCount)
	}
}

func TestCommandStoreRealPostgresConcurrentSameIdempotencyKeyConflictsDeterministically(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	barrier := newReserveBarrier(2)
	postgresStore := newStoreForTest(pool, storeOptions{
		identity:            testIdentityGenerator(),
		beforeReserveInsert: barrier.Wait,
	})
	registerTestAccount(t, ctx, postgresStore)

	results, errs := runConcurrentReservations(ctx, postgresStore,
		reserveRequest(t, json.RawMessage(`{"quantity":"0.001"}`)),
		reserveRequest(t, json.RawMessage(`{"quantity":"0.002"}`)),
	)
	for _, err := range errs {
		if err != nil {
			t.Fatalf("ReserveCommand concurrent different payload returned raw error: %v", err)
		}
	}
	conflicts := 0
	for _, result := range results {
		if result.Conflict == store.ConflictPayload {
			conflicts++
		}
	}
	if conflicts != 1 {
		t.Fatalf("payload conflict count = %d, want 1; results=%+v", conflicts, results)
	}
	if results[0].CommandID != results[1].CommandID {
		t.Fatalf("conflict did not return existing command identity: %+v vs %+v", results[0], results[1])
	}
}

func TestCommandStoreRealPostgresTransitionsUseCASAndRecoverableLoad(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	postgresStore := newStoreForTest(pool, storeOptions{identity: testIdentityGenerator()})
	registerTestAccount(t, ctx, postgresStore)
	reserved, err := postgresStore.ReserveCommand(ctx, reserveRequest(t, json.RawMessage(`{"quantity":"0.001"}`)))
	if err != nil {
		t.Fatalf("ReserveCommand: %v", err)
	}

	dispatching, err := postgresStore.MarkDispatching(ctx, store.MarkDispatchingRequest{
		CommandID: reserved.CommandID,
		EventID:   domain.EventID("018f0000-0000-7000-8000-000000000010"),
	})
	if err != nil {
		t.Fatalf("MarkDispatching: %v", err)
	}
	if dispatching.State != domain.StateDispatching || dispatching.ResourceVersion != 1 {
		t.Fatalf("dispatching stored command = %+v", dispatching)
	}
	if dispatching.PrincipalID != "principal-a" {
		t.Fatalf("dispatching principal = %q, want principal-a", dispatching.PrincipalID)
	}

	accepted, err := postgresStore.RecordVenueResult(ctx, store.RecordVenueResultRequest{
		CommandID:       reserved.CommandID,
		VenueOrderID:    "venue-1",
		NextState:       domain.StateAccepted,
		EventID:         domain.EventID("018f0000-0000-7000-8000-000000000011"),
		ResourceVersion: 1,
	})
	if err != nil {
		t.Fatalf("RecordVenueResult accepted: %v", err)
	}
	if accepted.State != domain.StateAccepted || accepted.ResourceVersion != 2 || accepted.VenueOrderID != "venue-1" {
		t.Fatalf("accepted stored command = %+v", accepted)
	}
	byClientOrderID, err := postgresStore.LoadOrderByVenueIdentity(ctx, store.LoadOrderByVenueIdentityRequest{
		AccountID:     testAccountID(t),
		Product:       "spot",
		ClientOrderID: reserved.ClientOrderID,
	})
	if err != nil || byClientOrderID.GatewayOrderID != reserved.GatewayOrderID ||
		byClientOrderID.PrincipalID != "principal-a" {
		t.Fatalf("LoadOrderByVenueIdentity client=%+v err=%v", byClientOrderID, err)
	}
	byVenueOrderID, err := postgresStore.LoadOrderByVenueIdentity(ctx, store.LoadOrderByVenueIdentityRequest{
		AccountID:    testAccountID(t),
		Product:      "spot",
		VenueOrderID: "venue-1",
	})
	if err != nil || byVenueOrderID.GatewayOrderID != reserved.GatewayOrderID ||
		byVenueOrderID.ResourceVersion != accepted.ResourceVersion {
		t.Fatalf("LoadOrderByVenueIdentity venue=%+v err=%v", byVenueOrderID, err)
	}
	_, err = postgresStore.RecordVenueResult(ctx, store.RecordVenueResultRequest{
		CommandID:       reserved.CommandID,
		VenueOrderID:    "venue-1",
		NextState:       domain.StateOpen,
		EventID:         domain.EventID("018f0000-0000-7000-8000-000000000012"),
		ResourceVersion: 1,
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("stale resource version error = %v, want ErrConflict", err)
	}

	recoverable, err := postgresStore.LoadRecoverableCommands(ctx, store.LoadRecoverableCommandsRequest{
		AccountID: testAccountID(t),
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("LoadRecoverableCommands: %v", err)
	}
	if len(recoverable) != 1 || recoverable[0].State != domain.StateAccepted {
		t.Fatalf("recoverable = %+v", recoverable)
	}
	if got, want := string(recoverable[0].Payload), `{"quantity": "0.001"}`; got != want {
		t.Fatalf("recoverable payload = %s, want %s", got, want)
	}
}

func TestCommandStoreRealPostgresPersistsClaimsAndClearsBoundedReconciliation(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	postgresStore := newStoreForTest(pool, storeOptions{identity: testIdentityGenerator()})
	registerTestAccount(t, ctx, postgresStore)
	reserved, err := postgresStore.ReserveCommand(ctx, reserveRequest(t, json.RawMessage(`{"quantity":"0.001"}`)))
	if err != nil {
		t.Fatalf("ReserveCommand: %v", err)
	}
	dispatching, err := postgresStore.MarkDispatching(ctx, store.MarkDispatchingRequest{
		CommandID: reserved.CommandID,
		EventID:   domain.EventID("018f0000-0000-7000-8000-000000000031"),
	})
	if err != nil {
		t.Fatalf("MarkDispatching: %v", err)
	}
	dueAt := time.Unix(1710000100, 0).UTC()
	uncertain, err := postgresStore.RecordVenueResult(ctx, store.RecordVenueResultRequest{
		CommandID:       reserved.CommandID,
		NextState:       domain.StateUncertain,
		EventID:         domain.EventID("018f0000-0000-7000-8000-000000000032"),
		ResourceVersion: dispatching.ResourceVersion,
		Reconciliation: &store.ReconciliationPlan{
			TaskID:        domain.EventID("018f0000-0000-7000-8000-000000000033"),
			Reason:        store.ReconciliationAmbiguousHandoff,
			NextAttemptAt: dueAt,
		},
	})
	if err != nil {
		t.Fatalf("RecordVenueResult uncertain: %v", err)
	}

	// Reconstruct the store around the same PostgreSQL pool to model restart;
	// no reconciliation state may live only in process memory.
	restarted := newStoreForTest(pool, storeOptions{identity: testIdentityGenerator()})
	claimed, err := restarted.ClaimDueReconciliationTasks(ctx, store.ClaimDueReconciliationTasksRequest{
		AccountID: testAccountID(t), Now: dueAt.Add(time.Second),
		LeaseUntil: dueAt.Add(time.Minute), Limit: 1, MaxAttempts: 1,
	})
	if err != nil {
		t.Fatalf("ClaimDueReconciliationTasks: %v", err)
	}
	if len(claimed) != 1 || claimed[0].Command.CommandID != reserved.CommandID ||
		claimed[0].Reason != store.ReconciliationAmbiguousHandoff || claimed[0].Attempts != 1 ||
		string(claimed[0].Command.Payload) != `{"quantity": "0.001"}` {
		t.Fatalf("claimed reconciliation task = %+v", claimed)
	}
	if second, err := restarted.ClaimDueReconciliationTasks(ctx, store.ClaimDueReconciliationTasksRequest{
		AccountID: testAccountID(t), Now: dueAt.Add(2 * time.Second),
		LeaseUntil: dueAt.Add(2 * time.Minute), Limit: 1, MaxAttempts: 1,
	}); err != nil || len(second) != 0 {
		t.Fatalf("bounded second claim = %+v, err=%v", second, err)
	}

	if _, err := restarted.RescheduleReconciliation(ctx, store.RescheduleReconciliationRequest{
		GatewayOrderID: reserved.GatewayOrderID,
		Plan: store.ReconciliationPlan{
			TaskID:        domain.EventID("018f0000-0000-7000-8000-000000000034"),
			Reason:        store.ReconciliationEvidenceUnconfirmed,
			NextAttemptAt: dueAt.Add(3 * time.Second),
		},
	}); err != nil {
		t.Fatalf("RescheduleReconciliation: %v", err)
	}
	if exhausted, err := restarted.ClaimDueReconciliationTasks(ctx, store.ClaimDueReconciliationTasksRequest{
		AccountID: testAccountID(t), Now: dueAt.Add(4 * time.Second),
		LeaseUntil: dueAt.Add(3 * time.Minute), Limit: 1, MaxAttempts: 1,
	}); err != nil || len(exhausted) != 0 {
		t.Fatalf("exhausted claim = %+v, err=%v", exhausted, err)
	}

	resolved, err := restarted.RecordVenueResult(ctx, store.RecordVenueResultRequest{
		CommandID:       reserved.CommandID,
		VenueOrderID:    "venue-1",
		NextState:       domain.StateOpen,
		EventID:         domain.EventID("018f0000-0000-7000-8000-000000000035"),
		ResourceVersion: uncertain.ResourceVersion,
	})
	if err != nil || resolved.State != domain.StateOpen {
		t.Fatalf("resolve uncertain command = %+v, err=%v", resolved, err)
	}
	var tasks int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM reconciliation_tasks WHERE gateway_order_id = $1`, reserved.GatewayOrderID).Scan(&tasks); err != nil {
		t.Fatalf("count reconciliation tasks: %v", err)
	}
	if tasks != 0 {
		t.Fatalf("resolved reconciliation tasks = %d, want 0", tasks)
	}
}

func TestCommandStoreRealPostgresReportsCurrentReconciliationMetrics(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	postgresStore := newStoreForTest(pool, storeOptions{identity: testIdentityGenerator()})
	registerTestAccount(t, ctx, postgresStore)

	now := time.Unix(1710000300, 0).UTC()
	count, age, err := postgresStore.ReconciliationStatus(ctx, now)
	if err != nil {
		t.Fatalf("empty ReconciliationStatus: %v", err)
	}
	if count != 0 || age != 0 {
		t.Fatalf("empty reconciliation status = count %d age %s", count, age)
	}

	reserved, err := postgresStore.ReserveCommand(ctx, reserveRequest(t, json.RawMessage(`{"quantity":"0.001"}`)))
	if err != nil {
		t.Fatalf("ReserveCommand: %v", err)
	}
	dispatching, err := postgresStore.MarkDispatching(ctx, store.MarkDispatchingRequest{
		CommandID: reserved.CommandID,
		EventID:   domain.EventID("018f0000-0000-7000-8000-000000000041"),
	})
	if err != nil {
		t.Fatalf("MarkDispatching: %v", err)
	}
	if _, err := postgresStore.RecordVenueResult(ctx, store.RecordVenueResultRequest{
		CommandID:       reserved.CommandID,
		NextState:       domain.StateUncertain,
		EventID:         domain.EventID("018f0000-0000-7000-8000-000000000042"),
		ResourceVersion: dispatching.ResourceVersion,
		Reconciliation: &store.ReconciliationPlan{
			TaskID:        domain.EventID("018f0000-0000-7000-8000-000000000043"),
			Reason:        store.ReconciliationAmbiguousHandoff,
			NextAttemptAt: now.Add(time.Second),
		},
	}); err != nil {
		t.Fatalf("RecordVenueResult uncertain: %v", err)
	}
	oldest := now.Add(-45 * time.Second)
	if _, err := pool.Exec(ctx, `UPDATE orders SET updated_at = $1 WHERE gateway_order_id = $2`, oldest, reserved.GatewayOrderID); err != nil {
		t.Fatalf("set deterministic uncertain age: %v", err)
	}

	count, age, err = postgresStore.ReconciliationStatus(ctx, now)
	if err != nil {
		t.Fatalf("ReconciliationStatus: %v", err)
	}
	if count != 1 || age != 45*time.Second {
		t.Fatalf("reconciliation status = count %d age %s, want count 1 age 45s", count, age)
	}
}

func TestCommandStoreRealPostgresConcurrentClaimsLeaseDifferentTasks(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	postgresStore := newStoreForTest(pool, storeOptions{identity: testIdentityGenerator()})
	registerTestAccount(t, ctx, postgresStore)
	accountID := testAccountID(t)
	dueAt := time.Unix(1710000200, 0).UTC()
	for index, key := range []domain.IdempotencyKey{"strategy-a-1", "strategy-a-2"} {
		request := reserveRequest(t, json.RawMessage(fmt.Sprintf(`{"quantity":"0.00%d"}`, index+1)))
		request.IdempotencyKey = key
		reserved, err := postgresStore.ReserveCommand(ctx, request)
		if err != nil {
			t.Fatalf("ReserveCommand %d: %v", index, err)
		}
		dispatchEvent := domain.EventID(fmt.Sprintf("018f0000-0000-7000-8000-%012x", 0x41+index*3))
		dispatching, err := postgresStore.MarkDispatching(ctx, store.MarkDispatchingRequest{CommandID: reserved.CommandID, EventID: dispatchEvent})
		if err != nil {
			t.Fatalf("MarkDispatching %d: %v", index, err)
		}
		_, err = postgresStore.RecordVenueResult(ctx, store.RecordVenueResultRequest{
			CommandID: reserved.CommandID, NextState: domain.StateUncertain,
			EventID:         domain.EventID(fmt.Sprintf("018f0000-0000-7000-8000-%012x", 0x42+index*3)),
			ResourceVersion: dispatching.ResourceVersion,
			Reconciliation: &store.ReconciliationPlan{
				TaskID: domain.EventID(fmt.Sprintf("018f0000-0000-7000-8000-%012x", 0x43+index*3)),
				Reason: store.ReconciliationAmbiguousHandoff, NextAttemptAt: dueAt,
			},
		})
		if err != nil {
			t.Fatalf("RecordVenueResult %d: %v", index, err)
		}
	}

	stores := []*Store{
		newStoreForTest(pool, storeOptions{identity: testIdentityGenerator()}),
		newStoreForTest(pool, storeOptions{identity: testIdentityGenerator()}),
	}
	start := make(chan struct{})
	results := make(chan []store.ReconciliationTask, 2)
	errs := make(chan error, 2)
	for _, claimant := range stores {
		go func(claimant *Store) {
			<-start
			tasks, err := claimant.ClaimDueReconciliationTasks(ctx, store.ClaimDueReconciliationTasksRequest{
				AccountID: accountID, Now: dueAt.Add(time.Second),
				LeaseUntil: dueAt.Add(time.Minute), Limit: 1, MaxAttempts: 3,
			})
			results <- tasks
			errs <- err
		}(claimant)
	}
	close(start)
	claimed := make(map[domain.GatewayOrderID]bool)
	for range 2 {
		tasks, err := <-results, <-errs
		if err != nil || len(tasks) != 1 {
			t.Fatalf("concurrent claim = %+v, err=%v", tasks, err)
		}
		claimed[tasks[0].Command.GatewayOrderID] = true
	}
	if len(claimed) != 2 {
		t.Fatalf("concurrent claims duplicated task: %+v", claimed)
	}
	remaining, err := postgresStore.ClaimDueReconciliationTasks(ctx, store.ClaimDueReconciliationTasksRequest{
		AccountID: accountID, Now: dueAt.Add(2 * time.Second),
		LeaseUntil: dueAt.Add(2 * time.Minute), Limit: 2, MaxAttempts: 3,
	})
	if err != nil || len(remaining) != 0 {
		t.Fatalf("leased tasks were immediately reclaimable: %+v, err=%v", remaining, err)
	}
}

func TestCommandStoreRealPostgresCommitFaultLeavesNoPartialRows(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	postgresStore := newStoreForTest(pool, storeOptions{
		identity: testIdentityGenerator(),
		beforeCommit: func(ctx context.Context) error {
			var events int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM order_events`).Scan(&events); err != nil {
				return err
			}
			if events != 0 {
				return fmt.Errorf("event visible before commit")
			}
			return errors.New("injected commit fault")
		},
	})
	registerTestAccount(t, ctx, postgresStore)

	_, err := postgresStore.ReserveCommand(ctx, reserveRequest(t, json.RawMessage(`{"quantity":"0.001"}`)))
	if err == nil {
		t.Fatal("ReserveCommand succeeded despite injected commit fault")
	}
	var rows int
	if scanErr := pool.QueryRow(ctx, `SELECT count(*) FROM idempotency_commands`).Scan(&rows); scanErr != nil {
		t.Fatalf("count idempotency rows: %v", scanErr)
	}
	if rows != 0 {
		t.Fatalf("commit fault left %d command rows", rows)
	}
}

func TestAccountOwnershipRealPostgresBlocksSecondWriterUntilRelease(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	postgresStore := newStoreForTest(pool, storeOptions{identity: testIdentityGenerator()})
	registerTestAccount(t, ctx, postgresStore)

	owner, err := AcquireAccountOwnership(ctx, pool, testAccountID(t))
	if err != nil {
		t.Fatalf("AcquireAccountOwnership first: %v", err)
	}
	if !owner.Healthy() {
		t.Fatal("owner is not healthy after acquire")
	}
	_, err = AcquireAccountOwnership(ctx, pool, testAccountID(t))
	if !errors.Is(err, ErrOwnershipUnavailable) {
		t.Fatalf("second ownership error = %v, want ErrOwnershipUnavailable", err)
	}
	if err := owner.Release(ctx); err != nil {
		t.Fatalf("Release: %v", err)
	}
	second, err := AcquireAccountOwnership(ctx, pool, testAccountID(t))
	if err != nil {
		t.Fatalf("AcquireAccountOwnership after release: %v", err)
	}
	if err := second.Release(ctx); err != nil {
		t.Fatalf("second Release: %v", err)
	}
}

func TestAccountOwnershipRealPostgresReleaseWithCanceledContextDoesNotReturnLockedConnection(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	postgresStore := newStoreForTest(pool, storeOptions{identity: testIdentityGenerator()})
	registerTestAccount(t, ctx, postgresStore)

	owner, err := AcquireAccountOwnership(ctx, pool, testAccountID(t))
	if err != nil {
		t.Fatalf("AcquireAccountOwnership: %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := owner.Release(canceled); err == nil {
		t.Fatal("Release with canceled context succeeded; expected unlock failure")
	}
	reacquired, err := AcquireAccountOwnership(ctx, pool, testAccountID(t))
	if err != nil {
		t.Fatalf("ownership remained locked or poisoned after failed release: %v", err)
	}
	defer reacquired.Release(ctx)
}

func TestAccountOwnershipRealPostgresReleaseIsConcurrentSafe(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	postgresStore := newStoreForTest(pool, storeOptions{identity: testIdentityGenerator()})
	registerTestAccount(t, ctx, postgresStore)

	owner, err := AcquireAccountOwnership(ctx, pool, testAccountID(t))
	if err != nil {
		t.Fatalf("AcquireAccountOwnership: %v", err)
	}
	const callers = 8
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- owner.Release(ctx)
		}()
	}
	wg.Wait()
	close(errs)
	failures := 0
	for err := range errs {
		if err != nil {
			failures++
		}
	}
	if failures != 0 {
		t.Fatalf("concurrent Release failures = %d, want 0", failures)
	}
	reacquired, err := AcquireAccountOwnership(ctx, pool, testAccountID(t))
	if err != nil {
		t.Fatalf("ownership not released after concurrent Release: %v", err)
	}
	defer reacquired.Release(ctx)
}

func TestAccountOwnershipRealPostgresHealthyTurnsFalseAfterConnectionLoss(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	postgresStore := newStoreForTest(pool, storeOptions{identity: testIdentityGenerator()})
	registerTestAccount(t, ctx, postgresStore)

	owner, err := AcquireAccountOwnership(ctx, pool, testAccountID(t))
	if err != nil {
		t.Fatalf("AcquireAccountOwnership: %v", err)
	}
	owner.conn.Conn().PgConn().Close(context.Background())
	if owner.Healthy() {
		t.Fatal("Healthy remained true after ownership connection was closed")
	}
	_ = owner.Release(ctx)
}

func TestCommandStoreRealPostgresConcurrentClientIDsStayScopedUint48(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	postgresStore := newStoreForTest(pool, storeOptions{identity: testIdentityGenerator()})
	registerTestAccount(t, ctx, postgresStore)

	const count = 16
	var wg sync.WaitGroup
	errs := make(chan error, count)
	ids := make(chan string, count)
	for index := range count {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			request := reserveRequest(t, json.RawMessage(fmt.Sprintf(`{"quantity":"0.%03d"}`, index+1)))
			request.IdempotencyKey = domain.IdempotencyKey(fmt.Sprintf("strategy-a-%d", index+1))
			result, err := postgresStore.ReserveCommand(ctx, request)
			if err != nil {
				errs <- err
				return
			}
			ids <- result.ClientOrderID
		}(index)
	}
	wg.Wait()
	close(errs)
	close(ids)
	for err := range errs {
		t.Fatalf("ReserveCommand concurrent: %v", err)
	}
	seen := map[string]bool{}
	for id := range ids {
		if _, err := domain.ParseClientOrderID(id); err != nil {
			t.Fatalf("invalid client id %q: %v", id, err)
		}
		if seen[id] {
			t.Fatalf("duplicate client id %q", id)
		}
		seen[id] = true
	}
}

type reserveBarrier struct {
	needed int
	ready  chan struct{}
	once   sync.Once
	mu     sync.Mutex
	seen   int
}

func newReserveBarrier(needed int) *reserveBarrier {
	return &reserveBarrier{needed: needed, ready: make(chan struct{})}
}

func (barrier *reserveBarrier) Wait(context.Context) error {
	barrier.mu.Lock()
	barrier.seen++
	if barrier.seen == barrier.needed {
		barrier.once.Do(func() { close(barrier.ready) })
	}
	barrier.mu.Unlock()
	<-barrier.ready
	return nil
}

func runConcurrentReservations(ctx context.Context, postgresStore *Store, requests ...store.ReserveCommandRequest) ([]store.ReservedCommand, []error) {
	var wg sync.WaitGroup
	results := make([]store.ReservedCommand, len(requests))
	errs := make([]error, len(requests))
	for index := range requests {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			results[index], errs[index] = postgresStore.ReserveCommand(ctx, requests[index])
		}(index)
	}
	wg.Wait()
	return results, errs
}

func TestRegisterAccountRealPostgresStoresNonSecretStatusAndConfigHash(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	postgresStore := NewStore(pool)
	registerTestAccount(t, ctx, postgresStore)

	var venue, product, environment, configHash, status string
	err := pool.QueryRow(ctx, `
		SELECT venue, product, environment, config_hash, status
		FROM gateway_accounts
		WHERE account_id = $1
	`, testAccountID(t)).Scan(&venue, &product, &environment, &configHash, &status)
	if err != nil {
		t.Fatalf("load gateway account: %v", err)
	}
	if venue != "binance" || product != "spot" || environment != "demo" || configHash != strings.Repeat("a", 64) || status != "enabled" {
		t.Fatalf("stored account metadata = %q/%q/%q/%q/%q", venue, product, environment, configHash, status)
	}
}
