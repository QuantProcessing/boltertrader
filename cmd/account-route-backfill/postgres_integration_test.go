package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	gatewaypostgres "github.com/QuantProcessing/boltertrader/gateway/store/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var backfillIntegrationDatabaseSequence atomic.Uint64

func TestPostgresCoordinatorConvergesAfterGatewayCommitPlatformFailure(t *testing.T) {
	ctx := context.Background()
	platform, gateway := openBackfillIntegrationDatabases(t, ctx)
	var platformDatabase, gatewayDatabase string
	if err := platform.QueryRow(ctx, `SELECT current_database()`).Scan(&platformDatabase); err != nil {
		t.Fatal(err)
	}
	if err := gateway.QueryRow(ctx, `SELECT current_database()`).Scan(&gatewayDatabase); err != nil {
		t.Fatal(err)
	}
	if platformDatabase == gatewayDatabase || !strings.HasPrefix(platformDatabase, "backfill_platform_test_") || !strings.HasPrefix(gatewayDatabase, "backfill_gateway_test_") {
		t.Fatalf("database isolation platform=%q gateway=%q", platformDatabase, gatewayDatabase)
	}
	if err := gatewaypostgres.Migrate(ctx, gateway); err != nil {
		t.Fatal(err)
	}
	if _, err := platform.Exec(ctx, `
		CREATE TABLE platform_accounts (
			account_id text PRIMARY KEY,
			secret_ref text NOT NULL,
			mode text NOT NULL,
			native_environment text NOT NULL
		);
		CREATE TABLE platform_account_routes (
			account_id text NOT NULL REFERENCES platform_accounts(account_id),
			route_id text NOT NULL,
			product text NOT NULL,
			settlement_asset text NOT NULL,
			route_profile_id text NOT NULL,
			requested_state text NOT NULL,
			PRIMARY KEY (account_id, route_id)
		)
	`); err != nil {
		t.Fatal(err)
	}

	plan := integrationAccountPlan("acct_retry", "a")
	if _, err := platform.Exec(ctx, `INSERT INTO platform_accounts(account_id,secret_ref,mode,native_environment) VALUES($1,$2,'live','live')`, plan.AccountID, plan.OpaqueReference); err != nil {
		t.Fatal(err)
	}
	commitAttempts := 0
	coordinator := &postgresCoordinator{
		platform: platform,
		gateway:  gateway,
		commitPlatform: func(ctx context.Context, tx pgx.Tx) error {
			commitAttempts++
			if commitAttempts == 1 {
				return errors.New("injected platform commit failure")
			}
			return tx.Commit(ctx)
		},
	}

	if err := writeAccountPlans(ctx, coordinator, []accountPlan{plan}); err == nil || !strings.Contains(err.Error(), "BACKFILL_COMMIT_BLOCKED") {
		t.Fatalf("first attempt error=%v", err)
	}
	assertGatewayHead(t, ctx, gateway, plan.AccountID, plan.SnapshotID)
	assertSnapshotCount(t, ctx, gateway, plan.AccountID, 1)
	assertPlatformState(t, ctx, platform, plan.AccountID, "live", "live", 0)

	if err := writeAccountPlans(ctx, coordinator, []accountPlan{plan}); err != nil {
		t.Fatalf("identical retry error=%v", err)
	}
	assertGatewayHead(t, ctx, gateway, plan.AccountID, plan.SnapshotID)
	assertSnapshotCount(t, ctx, gateway, plan.AccountID, 1)
	assertPlatformState(t, ctx, platform, plan.AccountID, plan.Mode, plan.NativeEnvironment, 1)
	changedPlan := plan
	changedPlan.RequestDigest = strings.Repeat("d", 64)
	if err := writeAccountPlans(ctx, coordinator, []accountPlan{changedPlan}); err == nil || !strings.Contains(err.Error(), "GATEWAY_OPERATION_CONFLICT") {
		t.Fatalf("changed-input retry error=%v", err)
	}
	assertGatewayHead(t, ctx, gateway, plan.AccountID, plan.SnapshotID)
	assertSnapshotCount(t, ctx, gateway, plan.AccountID, 1)
	assertPlatformState(t, ctx, platform, plan.AccountID, plan.Mode, plan.NativeEnvironment, 1)

	conflictPlan := integrationAccountPlan("acct_conflict", "b")
	if _, err := platform.Exec(ctx, `INSERT INTO platform_accounts(account_id,secret_ref,mode,native_environment) VALUES($1,$2,'live','live')`, conflictPlan.AccountID, conflictPlan.OpaqueReference); err != nil {
		t.Fatal(err)
	}
	conflictingSnapshot := uuid.NewSHA1(uuid.NameSpaceOID, []byte("conflicting-pending/"+conflictPlan.AccountID))
	conflictingOperation := uuid.NewSHA1(uuid.NameSpaceOID, []byte("conflicting-operation/"+conflictPlan.AccountID))
	if _, err := gateway.Exec(ctx, `
		INSERT INTO gateway_runtime_snapshots(
			snapshot_id,account_id,operation_id,request_digest,venue,native_environment,
			secret_ref,desired_state_revision,runtime_generation,credential_version,requested_state
		) VALUES($1,$2,$3,$4,'binance','demo','opaque://existing',2,2,2,'active')
	`, conflictingSnapshot, conflictPlan.AccountID, conflictingOperation, strings.Repeat("c", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := gateway.Exec(ctx, `INSERT INTO gateway_account_runtime_heads(account_id,pending_snapshot_id) VALUES($1,$2)`, conflictPlan.AccountID, conflictingSnapshot); err != nil {
		t.Fatal(err)
	}

	if err := writeAccountPlans(ctx, coordinator, []accountPlan{conflictPlan}); !errors.Is(err, errGatewayPendingHeadConflict) {
		t.Fatalf("conflicting pending head error=%v", err)
	}
	assertGatewayHead(t, ctx, gateway, conflictPlan.AccountID, conflictingSnapshot)
	assertSnapshotCount(t, ctx, gateway, conflictPlan.AccountID, 1)
	assertPlatformState(t, ctx, platform, conflictPlan.AccountID, "live", "live", 0)
}

func integrationAccountPlan(accountID, digestByte string) accountPlan {
	return accountPlan{
		AccountID:         accountID,
		Venue:             "binance",
		NativeEnvironment: "demo",
		Mode:              "test",
		OpaqueReference:   "opaque://" + accountID,
		RequestedState:    "active",
		DesiredRevision:   1,
		RuntimeGeneration: 1,
		CredentialVersion: 1,
		OperationID:       uuid.NewSHA1(uuid.NameSpaceOID, []byte("account-route-backfill/operation/"+accountID)),
		SnapshotID:        uuid.NewSHA1(uuid.NameSpaceOID, []byte("account-route-backfill/snapshot/"+accountID)),
		RequestDigest:     strings.Repeat(digestByte, 64),
		Routes: []plannedRoute{{
			RouteID: "perp-usdt", Product: "perp", SettlementAsset: "USDT",
			RouteProfileID: "binance.perp.usdt.demo.v1", RequestedState: "active",
		}},
	}
}

func openBackfillIntegrationDatabases(t *testing.T, ctx context.Context) (*pgxpool.Pool, *pgxpool.Pool) {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("BT_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("BT_TEST_POSTGRES_DSN is not set")
	}
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	var canCreateDatabase bool
	if err := admin.QueryRow(ctx, `SELECT rolcreatedb FROM pg_roles WHERE rolname=current_user`).Scan(&canCreateDatabase); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	if !canCreateDatabase {
		admin.Close()
		t.Fatal("BT_TEST_POSTGRES_DSN role lacks CREATEDB; distinct-database saga test cannot run")
	}
	suffix := fmt.Sprintf("%d_%d", os.Getpid(), backfillIntegrationDatabaseSequence.Add(1))
	platformDatabase := "backfill_platform_test_" + suffix
	gatewayDatabase := "backfill_gateway_test_" + suffix
	var platform, gateway *pgxpool.Pool
	created := make([]string, 0, 2)
	t.Cleanup(func() {
		if platform != nil {
			platform.Close()
		}
		if gateway != nil {
			gateway.Close()
		}
		for index := len(created) - 1; index >= 0; index-- {
			database := created[index]
			if _, err := admin.Exec(context.Background(), `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=$1 AND pid<>pg_backend_pid()`, database); err != nil {
				t.Errorf("terminate connections to test database %s: %v", database, err)
				continue
			}
			if _, err := admin.Exec(context.Background(), `DROP DATABASE IF EXISTS `+pgx.Identifier{database}.Sanitize()); err != nil {
				t.Errorf("drop test database %s: %v", database, err)
			}
		}
		admin.Close()
	})
	for _, database := range []string{platformDatabase, gatewayDatabase} {
		if _, err := admin.Exec(ctx, `CREATE DATABASE `+pgx.Identifier{database}.Sanitize()+` TEMPLATE template0`); err != nil {
			t.Fatal(err)
		}
		created = append(created, database)
	}
	platform = openBackfillDatabasePool(t, ctx, dsn, platformDatabase)
	gateway = openBackfillDatabasePool(t, ctx, dsn, gatewayDatabase)
	return platform, gateway
}

func openBackfillDatabasePool(t *testing.T, ctx context.Context, dsn, database string) *pgxpool.Pool {
	t.Helper()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.Database = database
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	return pool
}

func assertGatewayHead(t *testing.T, ctx context.Context, pool *pgxpool.Pool, accountID string, wantPending uuid.UUID) {
	t.Helper()
	var activeIsNull bool
	var pending uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT active_snapshot_id IS NULL,pending_snapshot_id FROM gateway_account_runtime_heads WHERE account_id=$1`, accountID).Scan(&activeIsNull, &pending); err != nil {
		t.Fatal(err)
	}
	if !activeIsNull || pending != wantPending {
		t.Fatalf("gateway head activeIsNull=%t pending=%s, want pending=%s", activeIsNull, pending, wantPending)
	}
}

func assertSnapshotCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, accountID string, want int) {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM gateway_runtime_snapshots WHERE account_id=$1`, accountID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("gateway snapshot count=%d, want %d", count, want)
	}
}

func assertPlatformState(t *testing.T, ctx context.Context, pool *pgxpool.Pool, accountID, wantMode, wantEnvironment string, wantRoutes int) {
	t.Helper()
	var mode, environment string
	if err := pool.QueryRow(ctx, `SELECT mode,native_environment FROM platform_accounts WHERE account_id=$1`, accountID).Scan(&mode, &environment); err != nil {
		t.Fatal(err)
	}
	var routes int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM platform_account_routes WHERE account_id=$1`, accountID).Scan(&routes); err != nil {
		t.Fatal(err)
	}
	if mode != wantMode || environment != wantEnvironment || routes != wantRoutes {
		t.Fatalf("platform state mode=%q environment=%q routes=%d", mode, environment, routes)
	}
}
