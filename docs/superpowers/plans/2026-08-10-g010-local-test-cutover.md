# G010 Local Test Cutover Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement and execute the approved Docker-free, no-trade G010 cutover so the local Test Platform and Gateway use separated PostgreSQL authority, activation is crash-resumable and database-derived, and two pre/post-activation real-browser cycles pass without canonical drift or venue writes.

**Architecture:** A shared versioned authority matrix defines every Platform and Gateway object and privilege. Dedicated migrator commands own DDL while non-owner runtimes verify schema and effective grants; Test-only validation profiles keep reads/authentication available while all canonical mutation is denied. A single `migration-tool activate` operation promotes Gateway state through store-only transactions, grants both writer capabilities atomically, restarts only owned application processes, verifies live postconditions, and ratchets a manifest-backed activation journal to `activated`.

**Tech Stack:** Go 1.26, PostgreSQL 16/libpq services, pgx/pgxpool, POSIX shell, React/TypeScript, Playwright, OMX Ultragoal and Team.

---

## File And Ownership Map

| Area | Files | Responsibility |
| --- | --- | --- |
| Authority contract | `migration/authority/*.go` | Versioned role topology, exhaustive object classification, canonical digest, catalog and effective-privilege verification. |
| Provisioning | `backend/scripts/db-bootstrap.sh`, `backend/scripts/db_bootstrap_test.go`, `scripts/g010-cutover` | Create Test maintenance/migrator/runtime/capability roles, recreate exact databases, revoke `PUBLIC`, and fence exact sessions. |
| Platform runtime | `backend/cmd/platform-migrate/*`, `backend/internal/store/postgres/authority*.go`, `backend/internal/config/*`, `backend/internal/app/*`, `backend/internal/httpapi/*`, `backend/internal/workapi/*`, `backend/internal/platform/system/*` | Migrator-only DDL/bootstrap, runtime verification, validation profile, mutation gate, named canonical-write readiness. |
| Gateway runtime | `cmd/gateway-migrate/*`, `cmd/exchange-gateway/*`, `gateway/config/*`, `gateway/process/*`, `gateway/health/*`, `gateway/httpapi/*`, `gateway/store/postgres/authority*.go` | Migrator-only DDL, runtime verification, validation profile, mutation gate, named canonical-write readiness. |
| Funding worker | `backend/cmd/funding-worker/*`, `backend/internal/products/funding/*`, `scripts/dev-platform*` | Nonmutating validation profile, correct v4 work contract, owned process inspection and app-only restart. |
| Promotion/activation | `gateway/store/desired.go`, `gateway/store/postgres/migration_promotion*.go`, `migration/manifest.go`, `migration/workflow.go`, `migration/executor.go`, `migration/activation*.go`, `migration/evidence_verification*.go`, `cmd/migration-tool/*` | Exact pending-set promotion, authoritative activation attempt, crash recovery, generated evidence, CLI surface. |
| Validation/e2e | `migration/validation_snapshot*.go`, `scripts/test-real-platform`, `scripts/g010_cutover_test.go`, `web/e2e/*`, `web/playwright.real.config.ts`, `web/src/app/App.test.tsx` | Canonical digests, two-cycle restart evidence, real browser read/auth/SSE/resnapshot checks, redaction and no-write assertions. |

The existing uncommitted edits in `cmd/account-route-backfill`, `cmd/migration-tool`, `migration/executor*`, `migration/evidence_verification*`, and `scripts/g010-cutover*` are preserved as input. No worker may discard them; each behavior is accepted only after the RED/GREEN step that owns it.

### Task 1: Freeze The Versioned Authority Matrix

**Files:**
- Create: `migration/authority/matrix.go`
- Create: `migration/authority/platform_v15.go`
- Create: `migration/authority/gateway_v6.go`
- Create: `migration/authority/matrix_test.go`

- [ ] **Step 1: Write failing exhaustive-matrix tests**

Define table-driven tests which require exact Test roles, Platform schema version 15, the current Gateway migration digest, unique object names, side-effect-free probes, and a stable digest:

```go
func TestPlatformV15IsExhaustiveAndStable(t *testing.T) {
	matrix := authority.PlatformV15()
	if err := matrix.Validate(); err != nil { t.Fatal(err) }
	if matrix.SchemaVersion != 15 { t.Fatalf("schema=%d", matrix.SchemaVersion) }
	if got := matrix.Class("platform_sessions"); got != authority.ValidationAuthSession { t.Fatalf("class=%s", got) }
	if got := matrix.Class("platform_audit"); got != authority.Canonical { t.Fatalf("class=%s", got) }
	if got := matrix.Digest(); got != authority.PlatformV15Digest { t.Fatalf("digest=%s", got) }
}

func TestMatricesRejectUnknownOrUnsafeObjects(t *testing.T) {
	matrix := authority.PlatformV15()
	matrix.Objects = append(matrix.Objects, authority.Object{Name: "platform_unknown", Kind: authority.Table})
	if err := matrix.Validate(); !errors.Is(err, authority.ErrUnclassifiedObject) { t.Fatalf("err=%v", err) }
	for _, object := range authority.GatewayV6().Objects {
		if strings.Contains(strings.ToLower(object.Probe), "nextval") { t.Fatalf("unsafe probe: %s", object.Name) }
	}
}
```

- [ ] **Step 2: Run the tests and verify RED**

Run: `go test ./migration/authority -count=1`

Expected: FAIL because `migration/authority` does not exist.

- [ ] **Step 3: Implement the matrix types and exact constructors**

Use these public types consistently in every later task:

```go
type ObjectClass string
const (
	Canonical ObjectClass = "canonical"
	ValidationAuthSession ObjectClass = "validation-auth-session"
	MigrationOnly ObjectClass = "migration-only"
)
type ObjectKind string
const (Table ObjectKind = "table"; Sequence ObjectKind = "sequence"; Routine ObjectKind = "routine")
type RoleTopology struct { Maintenance, Migrator, Runtime, Writer string }
type Object struct { Schema, Name string; Kind ObjectKind; Class ObjectClass; Runtime []string; Writer []string; Probe string }
type Matrix struct { Name string; SchemaVersion int; MigrationDigest string; Roles RoleTopology; Objects []Object }
func PlatformV15() Matrix
func GatewayV6() Matrix
func (Matrix) Validate() error
func (Matrix) Digest() string
func (Matrix) Class(name string) ObjectClass
```

`PlatformV15` classifies only `platform_sessions` as `validation-auth-session`; all business, audit, account, route, command, work, reservation, portfolio, ledger, position, order, funding, and credential-operation tables are `canonical`; migration ledgers and migration routines are `migration-only`. `GatewayV6` classifies operational tables as canonical and its migration ledger as migration-only. Every table, sequence, and write-capable routine in the embedded migrations must appear exactly once.

The frozen exact inventory is: Platform has 25 tables (`platform_schema_migrations` migration-only, `platform_sessions` validation-auth-session, and 23 canonical tables), sequences `platform_realtime_events_sequence_seq`, `platform_work_lease_seq`, and `platform_funding_execution_transitions_id_seq`, and function `platform_valid_funding_account_fences(jsonb)`; Gateway has 22 tables (`schema_migrations` migration-only and 21 canonical tables), no explicit sequence, and migration-only trigger function `prevent_terminal_order_state_regression()`. Both functions lose `PUBLIC EXECUTE`; only the matrix-declared role may execute each one.

- [ ] **Step 4: Run tests and commit**

Run: `go test ./migration/authority -count=1`

Expected: PASS.

```bash
git add migration/authority
git commit -m "feat: define database authority matrices"
```

### Task 2: Provision Owner, Runtime, Capability, And Maintenance Roles

**Files:**
- Modify: `backend/scripts/db-bootstrap.sh`
- Modify: `backend/scripts/db_bootstrap_test.go`
- Create: `migration/authority/postgres_integration_test.go`

- [ ] **Step 1: Add RED role-topology and catalog tests**

Add shell-contract assertions for these exact identities and attributes:

```text
funding_source_test / bolter_funding_test / bolter_funding_test_migrator
platform_target_test / bolter_platform_test / bolter_platform_test_migrator
gateway_test / bolter_gateway_test / bolter_gateway_test_migrator
platform_maintenance_test / bolter_maintenance_test / bolter_platform_test_admin
bolter_platform_test_admin LOGIN CREATEDB CREATEROLE NOINHERIT
bolter_funding_test_migrator NOLOGIN NOCREATEDB NOCREATEROLE NOINHERIT
bolter_platform_test_migrator NOLOGIN NOCREATEDB NOCREATEROLE NOINHERIT
bolter_gateway_test_migrator NOLOGIN NOCREATEDB NOCREATEROLE NOINHERIT
bolter_platform_test LOGIN INHERIT
bolter_gateway_test LOGIN INHERIT
bolter_funding_test_writer LOGIN before fence, NOLOGIN after fence
bolter_platform_test_writer NOLOGIN
bolter_gateway_test_writer NOLOGIN
```

The real PostgreSQL test must assert database/schema/object ownership belongs to the matching migrator; runtime owns nothing; `PUBLIC` lacks database `CONNECT`, schema `CREATE`, table/sequence privileges, routine execute, and default privileges for future objects; the admin has only membership in the three enumerated migrators and `pg_signal_backend`, with `ADMIN OPTION` only for the source writer, Platform runtime/capability, and Gateway runtime/capability roles.

- [ ] **Step 2: Run RED**

Run: `cd backend && go test ./scripts -run 'TestDatabaseBootstrap|TestAuthority' -count=1`

Expected: FAIL because runtimes are currently database owners and `NOINHERIT`.

- [ ] **Step 3: Rewrite bootstrap idempotently**

The script must create/alter roles without passwords, create exact Test databases with migrator owners, revoke `PUBLIC`, grant only matrix-derived access, and validate topology after every run. Keep Live identities unchanged except that the common runtime invariant is `INHERIT`; do not grant any Live writer membership. Run role-changing operations only through `bolter_platform_test_admin`, and verify `current_user` before and after each bounded `SET ROLE`.

- [ ] **Step 4: Run unit and real PostgreSQL tests, then commit**

Run: `cd backend && go test ./scripts -count=1`

Run: `go test ./migration/authority -count=1`

Run: `BT_G010_POSTGRES_ACCEPT=1 go test ./migration/authority -run TestAuthorityMatrixRealPostgres -count=1 -v`

Expected: PASS; the second test skips only when its explicit opt-in is absent.

```bash
git add backend/scripts/db-bootstrap.sh backend/scripts/db_bootstrap_test.go migration/authority
git commit -m "ops: separate database owners from runtimes"
```

### Task 3: Split Platform Migration From Runtime Startup

**Files:**
- Modify: `backend/cmd/platform-migrate/app.go`
- Modify: `backend/cmd/platform-migrate/app_test.go`
- Create: `backend/internal/store/postgres/authority.go`
- Create: `backend/internal/store/postgres/authority_test.go`
- Create: `backend/internal/store/postgres/authority_integration_test.go`
- Modify: `backend/internal/store/postgres/migrations.go`
- Modify: `backend/internal/app/app.go`
- Modify: `backend/internal/app/app_test.go`

- [ ] **Step 1: Add RED tests for owner-only migration and runtime-only verification**

```go
func TestRunNeverMigratesAsRuntime(t *testing.T) {
	source := readAppSource(t)
	if strings.Contains(source, "postgres.Migrate(ctx, database)") { t.Fatal("runtime still executes DDL") }
}

func TestVerifyRuntimeAuthorityRequiresVersionDigestOwnershipAndEffectiveGrants(t *testing.T) {
	verifier := postgres.NewAuthorityVerifier(authority.PlatformV15())
	assertReason(t, verifier.Verify(context.Background(), fakeCatalog{RuntimeOwnsCanonical: true}), "RUNTIME_OWNS_CANONICAL_OBJECT")
	assertReason(t, verifier.Verify(context.Background(), fakeCatalog{UnknownTable: "platform_unknown"}), "AUTHORITY_MATRIX_DRIFT")
}
```

Also prove `platform-migrate` requires `bolter_platform_test_migrator`, applies version 15, applies the Platform authority grants, seeds the bootstrap identity under migration authority, and never starts listeners. The bcrypt hash is read from an exact mode-scoped 0600 file named by `BT_BOOTSTRAP_PASSWORD_HASH_FILE`; neither its content nor digest appears in argv, stdout, logs, or migration evidence.

- [ ] **Step 2: Run RED**

Run: `cd backend && go test ./cmd/platform-migrate ./internal/store/postgres ./internal/app -run 'TestRunNeverMigrates|TestVerifyRuntimeAuthority|TestPlatformMigrator' -count=1`

Expected: FAIL because `app.Run` migrates as the runtime and has no matrix verifier.

- [ ] **Step 3: Implement the split**

Keep DDL and bootstrap identity creation in `platform-migrate`. Replace runtime migration with:

```go
type RuntimeAuthority interface { VerifyRuntime(context.Context, *sql.DB, authority.Matrix) (postgres.AuthorityResult, error) }

result, err := postgres.VerifyRuntimeAuthority(ctx, database, authority.PlatformV15())
if err != nil { return fmt.Errorf("verify Platform database authority: %w", err) }
```

`AuthorityResult` exposes schema/migration digest, runtime identity/`INHERIT`, writer membership, effective table/sequence privileges, ownership violations, and matrix digest without DSNs or SQL output. Startup must accept read-only pre-activation state but fail on schema, ownership, matrix, or `PUBLIC` drift.

- [ ] **Step 4: Run tests and commit**

Run: `cd backend && go test ./cmd/platform-migrate ./internal/store/postgres ./internal/app -count=1`

Expected: PASS.

```bash
git add backend/cmd/platform-migrate backend/internal/store/postgres backend/internal/app
git commit -m "feat: verify platform schema at runtime"
```

### Task 4: Split Gateway Migration From Runtime Startup

**Files:**
- Create: `cmd/gateway-migrate/main.go`
- Create: `cmd/gateway-migrate/app.go`
- Create: `cmd/gateway-migrate/app_test.go`
- Create: `gateway/store/postgres/authority.go`
- Create: `gateway/store/postgres/authority_test.go`
- Create: `gateway/store/postgres/authority_integration_test.go`
- Modify: `gateway/process/process.go`
- Modify: `gateway/process/process_test.go`
- Modify: `cmd/exchange-gateway/app.go`
- Modify: `cmd/exchange-gateway/app_test.go`

- [ ] **Step 1: Add RED tests for a dedicated Gateway migrator**

Require `gateway-migrate --service gateway_test --database bolter_gateway_test --role bolter_gateway_test_migrator`, exact connected identity, complete migration digest, and no listener/runtime construction. Require `process.Start` to call `VerifyDatabaseAuthority` and never `Migrate`.

- [ ] **Step 2: Run RED**

Run: `go test ./cmd/gateway-migrate ./gateway/process ./cmd/exchange-gateway -run 'TestGatewayMigrate|TestStartVerifiesDatabaseAuthority|TestRunDoesNotMigrate' -count=1`

Expected: FAIL because the command is absent and runtime invokes `postgres.Migrate`.

- [ ] **Step 3: Implement the command and runtime verifier**

Use this dependency boundary:

```go
type Dependencies struct {
	ConnectPostgres func(context.Context, string) (PostgresPool, error)
	ValidateDatabaseIdentity func(context.Context, PostgresPool) error
	VerifyDatabaseAuthority func(context.Context, PostgresPool) (postgres.AuthorityResult, error)
	BuildAccountManager func(context.Context) (AccountManager, error)
	RestoreAccountManager func(context.Context, PostgresPool, AccountManager) error
	BuildOrderCoordinator func(context.Context, PostgresPool, AccountManager) (OrderCoordinator, error)
	Listen func(string, string) (net.Listener, error)
	HandlerFactory func(*health.Monitor, PostgresPool, AccountManager, OrderCoordinator) (http.Handler, error)
}
```

Delete `Dependencies.Migrate` and `defaultMigrate`; `gateway-migrate` is the sole caller of `postgres.Migrate` outside tests.

Extend the migration executor's restore/backfill preparation so it runs Platform migration first and Gateway migration second with their exact migrator services/roles; it must never fall back to either runtime identity.

- [ ] **Step 4: Run tests and commit**

Run: `go test ./cmd/gateway-migrate ./gateway/process ./cmd/exchange-gateway ./gateway/store/postgres -count=1`

Expected: PASS.

```bash
git add cmd/gateway-migrate cmd/exchange-gateway gateway/process gateway/store/postgres
git commit -m "feat: separate gateway migrations from runtime"
```

### Task 5: Add Platform Validation Profile And Database-Derived Write Gate

**Files:**
- Modify: `backend/internal/config/config.go`
- Modify: `backend/internal/config/config_test.go`
- Create: `backend/internal/app/write_authority.go`
- Create: `backend/internal/app/write_authority_test.go`
- Modify: `backend/internal/app/app.go`
- Modify: `backend/internal/app/app_test.go`
- Modify: `backend/internal/httpapi/server.go`
- Modify: `backend/internal/httpapi/server_test.go`
- Modify: `backend/internal/workapi/server.go`
- Modify: `backend/internal/workapi/server_test.go`
- Modify: `backend/internal/platform/system/readiness.go`
- Modify: `backend/internal/platform/system/readiness_test.go`

- [ ] **Step 1: Add RED profile and mutation-gate tests**

Define `BT_PLATFORM_STARTUP_PROFILE=normal|migration-validation`. Reject validation unless `BT_MODE=test`, Live flags are false, and runtime writer `USAGE` is absent. In validation, login/current-session/logout succeed while every account, route, trading, funding, work, reservation, command, credential, and reconciliation mutation returns stable `CANONICAL_WRITES_NOT_READY` without touching its store or audit log.

```go
func TestMigrationValidationAllowsOnlySessionWrites(t *testing.T) {
	app := newValidationApp(t, authorityResult(false))
	assertStatus(t, app, "POST", "/api/v1/auth/login", 200)
	for _, route := range canonicalMutationCases() {
		assertCodeAndNoCalls(t, app, route, http.StatusServiceUnavailable, "CANONICAL_WRITES_NOT_READY")
	}
}
```

The readiness body must include nonblocking named component `canonicalWrites`; it is false before activation without making health/read surfaces unavailable.

- [ ] **Step 2: Run RED**

Run: `cd backend && go test ./internal/config ./internal/app ./internal/httpapi ./internal/workapi ./internal/platform/system -run 'TestMigrationValidation|TestCanonicalWrite' -count=1`

Expected: FAIL because the profile, gate, and named component do not exist.

- [ ] **Step 3: Implement profile and gate**

```go
type StartupProfile string
const (StartupNormal StartupProfile = "normal"; StartupMigrationValidation StartupProfile = "migration-validation")
type WriteAuthority interface { Ready(context.Context) (bool, string) }
```

In validation: skip bootstrap identity upsert, order reconciliation, account reconciliation, accounting ingestion, funding plan mutation, and login audit writes; bootstrap identity must already exist from `platform-migrate`. Route all canonical mutations through the same injected `WriteAuthority`. In normal profile, readiness is true only when PostgreSQL reports runtime `INHERIT`, `pg_has_role(...,'USAGE')`, full capability grants, effective table/sequence privileges, and zero canonical ownership.

- [ ] **Step 4: Run tests and commit**

Run: `cd backend && go test ./internal/config ./internal/app ./internal/platform/... -count=1`

Expected: PASS.

```bash
git add backend/internal/config backend/internal/app backend/internal/platform
git commit -m "feat: gate platform writes by database authority"
```

### Task 6: Add Gateway Validation Profile And Database-Derived Write Gate

**Files:**
- Modify: `gateway/config/config.go`
- Modify: `gateway/config/config_test.go`
- Modify: `gateway/process/process.go`
- Modify: `gateway/process/process_test.go`
- Modify: `gateway/health/health.go`
- Modify: `gateway/health/health_test.go`
- Modify: `gateway/httpapi/handler.go`
- Modify: `gateway/httpapi/handler_test.go`
- Modify: `gateway/httpapi/internal_routes.go`
- Modify: `gateway/httpapi/internal_routes_test.go`
- Modify: `cmd/exchange-gateway/app.go`
- Modify: `cmd/exchange-gateway/app_test.go`

- [ ] **Step 1: Add RED validation-profile tests**

Add `server.startup_profile: normal|migration-validation`. In validation, assert zero calls to credential resolution, candidate construction, account manager start/restore, active-head promotion, order coordinator construction/start, private runtime creation, reconciliation, handoffs, or commands. Reads, catalog, public market data, health, capability and durable snapshot reads remain available. All four public mutations and all three internal PUT routes return `GATEWAY_WRITES_NOT_READY` before decoding/dispatch.

Runtime list/status projections in validation are read from the durable snapshot store, not from an empty account manager, so browser account surfaces remain truthful without constructing a private venue runtime.

- [ ] **Step 2: Run RED**

Run: `go test ./gateway/config ./gateway/process ./gateway/health ./gateway/httpapi ./cmd/exchange-gateway -run 'TestMigrationValidation|TestGatewayWrite' -count=1`

Expected: FAIL because normal startup always builds private runtime and the handlers have no database authority gate.

- [ ] **Step 3: Implement profile and named readiness**

Extend `health.Readiness` with a sorted component map while preserving `Ready` for required health:

```go
type Component struct { Ready bool `json:"ready"`; Reason string `json:"reason"` }
type Readiness struct { Ready bool `json:"ready"`; Reasons []string `json:"reasons,omitempty"`; Components map[string]Component `json:"components"` }
```

Set `canonicalWrites` from the Gateway matrix verifier. Validation requires it false; normal mode gates every operational mutation until it becomes true. Validation must not resolve any account secret or contact a venue.

- [ ] **Step 4: Run tests and commit**

Run: `go test ./gateway/config ./gateway/process ./gateway/health ./gateway/httpapi ./cmd/exchange-gateway -count=1`

Expected: PASS.

```bash
git add gateway/config gateway/process gateway/health gateway/httpapi cmd/exchange-gateway
git commit -m "feat: add nonmutating gateway validation profile"
```

### Task 7: Add A Nonmutating Funding Worker Profile And App-Only Supervisor Controls

**Files:**
- Modify: `backend/internal/products/funding/client.go`
- Modify: `backend/internal/products/funding/client_test.go`
- Modify: `backend/internal/products/funding/runtime.go`
- Modify: `backend/internal/products/funding/runtime_test.go`
- Modify: `backend/cmd/funding-worker/main.go`
- Create: `backend/cmd/funding-worker/main_test.go`
- Modify: `scripts/dev-platform`
- Modify: `scripts/dev_platform_test.go`

- [ ] **Step 1: Add RED worker and supervisor tests**

Require `BT_FUNDING_STARTUP_PROFILE=migration-validation` to validate config/token/loopback Platform availability, then stay alive without Register, Heartbeat, Readiness, Claim, Renew, Complete, Fail, or Drain. Assert the launcher passes the repository contract `boltertrader-product-platform-upgrade/v4`, never v5. Add `stop-app`, `start-app`, and `restart-app` commands which preserve the PostgreSQL PID/start time and restart only Gateway, Platform, worker, and web owned processes.

- [ ] **Step 2: Run RED**

Run: `cd backend && go test ./internal/products/funding ./cmd/funding-worker -run 'TestMigrationValidation|TestWorkContract' -count=1`

Run: `go test ./scripts -run 'TestDevPlatform.*App|TestDevPlatform.*Funding' -count=1`

Expected: FAIL because the worker mutates immediately, the script sends v5, and app-only lifecycle commands are absent.

- [ ] **Step 3: Implement the profile and supervisor commands**

Add:

```go
func (runtime *Runtime) ValidateAndWait(ctx context.Context) error {
	if err := runtime.client.PassiveStatus(ctx); err != nil { return err }
	<-ctx.Done()
	return nil
}
```

`PassiveStatus` is a GET-only loopback probe. After its first success the worker atomically creates a mode-scoped 0600 readiness sentinel containing only worker ID, instance ID, profile, and observation timestamp. The launcher readiness check in validation uses that sentinel plus owned PID/start-time/command evidence, never a synthetic registration POST. `restart-app` records PostgreSQL identity before/after and fails if its PID or start time changes.

- [ ] **Step 4: Run tests and commit**

Run: `cd backend && go test ./internal/products/funding ./cmd/funding-worker -count=1`

Run: `go test ./scripts -run TestDevPlatform -count=1`

Expected: PASS.

```bash
git add backend/internal/products/funding backend/cmd/funding-worker scripts/dev-platform scripts/dev_platform_test.go
git commit -m "feat: validate funding worker without mutations"
```

### Task 8: Accept Only A Qualified Absent Source Inventory

**Files:**
- Modify: `cmd/account-route-backfill/app.go`
- Modify: `cmd/account-route-backfill/app_test.go`

- [ ] **Step 1: Preserve and complete the existing RED cases**

Require `to_regclass('public.platform_accounts')`; absence returns an explicit empty inventory, a complete qualified relation is read, and any partial/unqualified/incompatible relation returns `SOURCE_PLATFORM_ACCOUNTS_SCHEMA_INVALID`. Target schema version 15 must still be present for empty input.

- [ ] **Step 2: Run RED**

Run: `go test ./cmd/account-route-backfill -run 'Test.*PlatformAccounts|Test.*EmptyInventory' -count=1`

Expected: FAIL on at least the partial-schema or qualified-relation case before implementation is accepted.

- [ ] **Step 3: Implement the structured inventory probe**

Use one catalog query for relation existence and `information_schema.columns` for the exact required column set; never infer absence from a failed unqualified SELECT. Return a deterministic empty slice and digest for absence.

- [ ] **Step 4: Run tests and commit**

Run: `go test ./cmd/account-route-backfill -count=1`

Expected: PASS.

```bash
git add cmd/account-route-backfill
git commit -m "fix: accept qualified empty source inventory"
```

### Task 9: Implement Store-Only Gateway Migration Promotion

**Files:**
- Modify: `gateway/store/desired.go`
- Create: `gateway/store/postgres/migration_promotion.go`
- Create: `gateway/store/postgres/migration_promotion_test.go`
- Modify: `gateway/store/postgres/runtime_snapshot_crash_test.go`

- [ ] **Step 1: Add RED exact-set and crash-replay tests**

Cover empty set, exact pending set, matching active replay, conflicting active head, unexpected pending head, partial identity, order-independent digest, and crash retry. Inject a panic-on-construction venue factory and prove promotion never invokes it.

- [ ] **Step 2: Run RED**

Run: `go test ./gateway/store ./gateway/store/postgres -run 'TestMigrationPromotion' -count=1`

Expected: FAIL because the migration promotion API does not exist.

- [ ] **Step 3: Implement exact promotion types and transaction**

```go
type MigrationPromotionItem struct { AccountID domain.AccountID; OperationID string; DesiredStateRevision, RuntimeGeneration, CredentialVersion int64; RouteSetSHA256, InputSHA256 string }
type MigrationPromotionRequest struct { MigrationID, FenceSHA256 string; Items []MigrationPromotionItem }
type MigrationPromotionDigest struct { PendingSHA256, ActiveSHA256, OperationSetSHA256 string }
type MigrationPromotionResult struct { Before, After MigrationPromotionDigest; Replayed bool }
```

`PromoteMigrationPendingSet` locks all runtime heads in one PostgreSQL transaction, validates the exact complete set, promotes or accepts matching active heads, marks desired operations applied, and rejects extras/conflicts/fence drift. It imports only store/domain packages.

- [ ] **Step 4: Run tests and commit**

Run: `go test ./gateway/store ./gateway/store/postgres -run 'TestMigrationPromotion' -count=1`

Expected: PASS.

```bash
git add gateway/store/desired.go gateway/store/postgres
git commit -m "feat: promote gateway migration state offline"
```

### Task 10: Add Canonical Snapshot And Validation Evidence

**Files:**
- Create: `migration/validation_snapshot.go`
- Create: `migration/validation_snapshot_test.go`
- Modify: `migration/executor.go`
- Modify: `migration/executor_test.go`
- Modify: `migration/evidence_verification.go`
- Modify: `migration/evidence_verification_test.go`

- [ ] **Step 1: Add RED digest and replay-currentness tests**

Require sorted row digests for every matrix canonical Platform table, Gateway active heads, and migration-only objects; record auth-session rows separately. Before/after validation accepts only the validating browser session lifecycle and rejects audit/canonical/head drift. Replaying `validate` must rerun current postcondition probes instead of returning historical evidence alone.

- [ ] **Step 2: Run RED**

Run: `go test ./migration -run 'TestValidationSnapshot|TestValidateReplay' -count=1`

Expected: FAIL because the unified snapshot and current replay check are absent.

- [ ] **Step 3: Implement deterministic redacted snapshots**

```go
type ValidationSnapshot struct { MatrixSHA256, PlatformCanonicalSHA256, PlatformMigrationSHA256, GatewayActiveSHA256, AuthSessionSHA256 string; CapturedAt time.Time }
type ValidationComparison struct { CanonicalUnchanged, GatewayHeadsUnchanged, MigrationUnchanged, SessionLifecycleAllowed bool }
func CaptureValidationSnapshot(context.Context, SnapshotSource, authority.Matrix, authority.Matrix) (ValidationSnapshot, error)
func CompareValidationSnapshots(before, after ValidationSnapshot, allowedSessionID string) (ValidationComparison, error)
```

Hashes include typed row encodings, not string-concatenated SQL output. Evidence stores hashes/counts only. Verification checks file permissions, symlink rejection, manifest/event binding, current catalog state, and generated evidence identity.

- [ ] **Step 4: Run tests and commit**

Run: `go test ./migration -run 'TestValidationSnapshot|TestValidateReplay|TestVerifyEvidence' -count=1`

Expected: PASS.

```bash
git add migration
git commit -m "feat: bind validation to canonical state digests"
```

### Task 11: Implement The Durable Activation Attempt And Executor

**Files:**
- Modify: `migration/manifest.go`
- Modify: `migration/workflow.go`
- Modify: `migration/workflow_test.go`
- Modify: `migration/executor.go`
- Modify: `migration/executor_test.go`
- Create: `migration/activation.go`
- Create: `migration/activation_test.go`
- Create: `migration/activation_adversarial_test.go`

- [ ] **Step 1: Add RED state-machine, order, and failpoint tests**

Test boundaries `begun`, `gateway-promoted`, `writers-granted`, `connect-restored`, `services-started`, `verified`; changed input, second attempt, skipped/regressed boundary, tampered journal, promotion conflict, partial writer grants, residual sessions, readiness failure, and crash after every boundary fail closed or resume exactly once. Assert stop order preserves PostgreSQL and writer grants for Platform/Gateway occur in one transaction.

- [ ] **Step 2: Run RED**

Run: `go test ./migration -run 'TestExecuteActivation|TestActivationAttempt' -count=1`

Expected: FAIL because `ActivationAttempt` and `ExecuteActivation` do not exist.

- [ ] **Step 3: Implement authoritative attempt and injected executor**

```go
type ActivationBoundary string
const (
	ActivationBegun ActivationBoundary = "begun"
	ActivationGatewayPromoted ActivationBoundary = "gateway-promoted"
	ActivationWritersGranted ActivationBoundary = "writers-granted"
	ActivationConnectRestored ActivationBoundary = "connect-restored"
	ActivationServicesStarted ActivationBoundary = "services-started"
	ActivationVerified ActivationBoundary = "verified"
)
type ActivationAttempt struct { ID, InputSHA256, ExpectedPromotionSHA256 string; Boundary ActivationBoundary; StartedAt, UpdatedAt time.Time; GatewayBeforeSHA256, GatewayAfterSHA256, JournalSHA256, EvidencePath, ProofSHA256 string }
type ActivationInput struct { MigrationID, ManifestPath, EvidenceDir, JournalPath string; Source, Platform, Gateway, Maintenance DatabaseIdentity; PlatformWriterRole, GatewayWriterRole string }
func ExecuteActivation(context.Context, ActivationInput, ActivationDependencies) (ExecutionResult, error)
```

Acquire manifest and PostgreSQL advisory locks; revalidate all prior evidence; fence exact source/runtime sessions; stop owned app processes; promote store-only Gateway state; grant both memberships atomically; verify effective matrices while sessions remain zero; restore exact `CONNECT`; start normal Test app processes; verify source fence, database readiness, promotion digest, zero-row probes, disabled Live/venue flags; atomically write evidence and transition only from verified to `activated`. Journal/evidence persistence uses a same-directory 0600 temporary file, file `fsync`, rename, and directory `fsync`; the journal is diagnostic only and the matching manifest attempt plus external postconditions remain authoritative. A failed post-grant verification stops apps and re-fences target sessions but never restores the source writer.

- [ ] **Step 4: Run tests and commit**

Run: `go test ./migration -run 'TestExecuteActivation|TestActivationAttempt|TestVerifyEvidence' -count=1`

Expected: PASS.

```bash
git add migration
git commit -m "feat: execute crash-resumable activation"
```

### Task 12: Expose Canonical Activation In The CLI And Driver

**Files:**
- Modify: `cmd/migration-tool/app.go`
- Modify: `cmd/migration-tool/app_test.go`
- Modify: `scripts/g010-cutover`
- Modify: `scripts/g010_cutover_test.go`
- Modify: `docs/guides/docker-free-migration.md`

- [ ] **Step 1: Rewrite existing tests to RED against the approved contract**

`activate` must be executable and `retire` unsupported. Reject `--proof`, `--activation-at`, `--cutover-at`, any Live/mixed identity, Docker command, venue-write flag/route, wrong phase, wrong role/database/service/path, tampered journal, and caller-authored evidence. The driver must loop to `activated`, replay idempotently, and never print secrets or invoke source retirement.

Also retain the forward-only rollback boundary: before the first successful post-fence backfill, rollback may restore the source writer only after proving both target writers remain disabled; at and after that backfill, every failure keeps the source fenced and resumes forward.

- [ ] **Step 2: Run RED**

Run: `go test ./cmd/migration-tool ./scripts -run 'TestRunActivate|TestG010Cutover' -count=1`

Expected: FAIL because the current CLI rejects `activate` and the driver stops at external activation evidence.

- [ ] **Step 3: Wire the canonical operation**

Add `activate: migration.Activate` to executable operations and construct only the exact Test identities from the approved spec. Remove `BT_G010_ACTIVATION_AT`, `BT_G010_CUTOVER_AT`, `BT_G010_ACTIVATION_PROOF`, and the fresh `record-activation-evidence` path. The driver retains backup/restore/delta/fence/backfill/validate revalidation and invokes one guarded `migration-tool activate` at `activation-ready`.

- [ ] **Step 4: Run tests and commit**

Run: `go test ./cmd/migration-tool ./scripts -run 'TestRunActivate|TestG010Cutover' -count=1`

Expected: PASS.

```bash
git add cmd/migration-tool scripts/g010-cutover scripts/g010_cutover_test.go docs/guides/docker-free-migration.md
git commit -m "ops: drive local test cutover through activation"
```

### Task 13: Make Real Browser Validation Nonmutating And Restart-Aware

**Files:**
- Modify: `scripts/test-real-platform`
- Modify: `scripts/dev-platform`
- Modify: `scripts/dev_platform_test.go`
- Modify: `web/playwright.real.config.ts`
- Modify: `web/e2e/platform-real.spec.ts`
- Modify: `web/e2e/real-acceptance-helpers.ts`
- Create: `web/e2e/resnapshot-harness.ts`
- Modify: `web/src/app/App.test.tsx`
- Modify: `web/package.json`

- [ ] **Step 1: Add RED browser and shell-contract tests**

Remove account validation POST from the real spec. Cover login/session mode, overview, Manual, Funding, Accounts, Activity/Audit, Settings, credential redaction, all read requests, SSE gap, one resnapshot, cursor reset/reconnect, failure fail-closed, identical idempotency replay, and zero canonical/head drift. The Test-only harness must bind an exact loopback IP, require explicit opt-in, reject Live, mutate only in-memory event state, and expose no account/private route.

- [ ] **Step 2: Run RED**

Run: `npm --prefix web test -- --run`

Run: `go test ./scripts -run 'TestRealPlatform|TestDevPlatform.*App' -count=1`

Expected: FAIL because the current real spec may POST account validation, the harness is absent, and restart includes PostgreSQL.

- [ ] **Step 3: Implement the real acceptance flow**

`npm --prefix web run test:e2e:real` must select `playwright.real.config.ts` explicitly. One `scripts/test-real-platform` invocation captures before/after validation snapshots, runs one Playwright cycle, and records current owned process identities. The G010 driver performs two pre-activation invocations with `restart-app` between them; Task 15 performs the same two-cycle sequence after activation and verifies the PostgreSQL identity is unchanged. Each invocation refuses missing canaries, non-loopback URLs, Live/private write flags, or any observed mutation request outside auth session routes.

- [ ] **Step 4: Run tests and commit**

Run: `npm --prefix web test -- --run && npm --prefix web run build`

Run: `go test ./scripts -run 'TestRealPlatform|TestDevPlatform' -count=1`

Expected: PASS.

```bash
git add scripts/test-real-platform scripts/dev-platform scripts/dev_platform_test.go web
git commit -m "test: validate real platform without canonical writes"
```

### Task 14: Run Full Offline And Real PostgreSQL Verification

**Files:**
- Modify only if a failing test proves a defect in files owned by Tasks 1-13.

- [ ] **Step 1: Format and run focused suites**

Run: `git diff --name-only --diff-filter=ACMRT -- '*.go' | xargs gofmt -w`

Run: `go test ./migration/... ./gateway/store/... ./gateway/process ./gateway/health ./gateway/httpapi ./cmd/gateway-migrate ./cmd/exchange-gateway ./cmd/migration-tool ./cmd/account-route-backfill ./scripts -count=1`

Run: `cd backend && go test ./... -count=1`

Expected: PASS.

- [ ] **Step 2: Run race, vet, web, and shell gates**

Run: `go test -race ./migration/... ./gateway/store/... ./gateway/process ./gateway/httpapi ./scripts -count=1`

Run: `cd backend && go test -race ./internal/app ./internal/store/postgres ./internal/products/funding ./internal/platform/... -count=1`

Run: `go vet ./... && (cd backend && go vet ./...)`

Run: `npm --prefix web test -- --run && npm --prefix web run build`

Run: `sh -n scripts/g010-cutover scripts/dev-platform scripts/test-real-platform backend/scripts/db-bootstrap.sh`

Expected: PASS with no Docker invocation.

- [ ] **Step 3: Run real PostgreSQL authority and crash-recovery tests**

Run: `BT_G010_POSTGRES_ACCEPT=1 go test ./migration/authority ./gateway/store/postgres ./migration -run 'Test.*RealPostgres|TestExecuteActivation' -count=1 -v`

Run: `cd backend && BT_G010_POSTGRES_ACCEPT=1 go test ./internal/store/postgres -run 'Test.*RealPostgres' -count=1 -v`

Expected: PASS against the local PostgreSQL 16 service, including pre/post privilege matrices and every activation failpoint.

- [ ] **Step 4: Check hygiene and commit any test-proven fixes**

Run: `git diff --check && test -z "$(find migration backend/internal cmd gateway scripts -name '*.go' -print0 | xargs -0 gofmt -l | head -1)"`

Expected: PASS. If Task 14 required a fix, commit only the scoped files with `test: harden G010 integration`; otherwise create no empty commit.

### Task 15: Execute The Real Local Test Cutover And Checkpoint G010

**Files:**
- Modify through command-generated output only: `migration/manifest.json`
- Create through command-generated output only: configured G010 evidence directory and activation journal
- Modify through leader-only checkpoint: `/Users/dddd/Documents/GitHub/BolterTrader/.omx/ultragoal/ledger.jsonl`

- [ ] **Step 1: Record the frozen baseline and preflight**

Run: `git status --short && git rev-parse HEAD && git -C /Users/dddd/Documents/GitHub/funding-trader status --short && git -C /Users/dddd/Documents/GitHub/funding-trader rev-parse HEAD^{commit} HEAD^{tree}`

Expected: implementation worktree clean; source clean at commit `e29f4ccb3f21e53e1424642c10b11e65a1b6a52a`, tree `99821acf574c87741b08ae3dc3a913f12b2835d7`.

Run: `FT_MODE=test ./scripts/dev-platform doctor`

Expected: exact loopback/Test identities, protected state files, no Live/private write flags, and no Docker requirement.

- [ ] **Step 2: Provision and migrate with dedicated roles**

Run: `backend/scripts/db-bootstrap.sh`

Run: `cd backend && go run ./cmd/platform-migrate --service platform_target_test --database bolter_platform_test --role bolter_platform_test_migrator`

Run: `go run ./cmd/gateway-migrate --service gateway_test --database bolter_gateway_test --role bolter_gateway_test_migrator`

Expected: schema version 15 and exact Gateway migration digest; runtimes remain non-owners without writer membership.

- [ ] **Step 3: Execute the guarded cutover through activation**

Run: `FT_MODE=test BT_G010_CUTOVER_ACCEPT=1 ./scripts/g010-cutover`

Expected: ordered provenance, backup, restore, final delta, source fence, backfill, two pre-activation validation cycles, activation, and generated evidence; manifest phase `activated`; source writer `NOLOGIN` with zero sessions; no retirement, Docker, venue request, credential read from `.env`, or Live write.

- [ ] **Step 4: Run two post-activation real-stack/browser cycles**

Run: `FT_MODE=test BT_DEV_REAL_STACK_ACCEPTANCE=1 ./scripts/test-real-platform`

Run: `FT_MODE=test BT_DEV_REAL_BROWSER_ACCEPTANCE=1 npm --prefix web run test:e2e:real`

Run: `FT_MODE=test ./scripts/dev-platform restart-app`

Run: `FT_MODE=test BT_DEV_REAL_STACK_ACCEPTANCE=1 ./scripts/test-real-platform`

Run: `FT_MODE=test BT_DEV_REAL_BROWSER_ACCEPTANCE=1 npm --prefix web run test:e2e:real`

Expected: all app identities change across restart, PostgreSQL identity does not, Platform/Gateway `canonicalWrites` are ready, source remains fenced, Gateway heads match promotion evidence, session/auth/read/SSE/idempotency checks pass, and no venue order/private command is emitted.

Gateway active-head equality proves only configuration ownership. G010 does not claim that any private stream or order route is ready; G011 may select only routes which independently satisfy all runtime, generation, reconciliation, capability, and private-stream barriers.

- [ ] **Step 5: Re-run the driver and verify replay**

Run: `FT_MODE=test BT_G010_CUTOVER_ACCEPT=1 ./scripts/g010-cutover`

Expected: replay success for the same activation attempt and fingerprint after live postcondition revalidation; no new event, role grant, promotion, or evidence replacement.

- [ ] **Step 6: Commit generated manifest and checkpoint only G010**

```bash
git add migration/manifest.json
git commit -m "ops: complete local test platform cutover"
```

Call `get_goal` for a fresh active aggregate snapshot and write that exact JSON result to `/private/tmp/g010-codex-goal.json`, then run from `/Users/dddd/Documents/GitHub/BolterTrader`:

```bash
omx ultragoal checkpoint --goal-id G010-execute-fenced-cutover-and-real-stac --status complete --evidence "G010 activated through the canonical Test-only operation; source fenced; authority matrices verified; two pre/post restart browser cycles passed; no Docker, Live write, venue order, credential exposure, or retirement" --codex-goal-json /private/tmp/g010-codex-goal.json
```

Expected: G010 becomes complete, G011 becomes next, and the aggregate Codex goal remains active. Do not call `update_goal`; G011 and G012 are still pending.
