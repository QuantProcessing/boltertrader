package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestDecodeMappingValidatesTrustedProfiles(t *testing.T) {
	artifact, err := decodeMapping([]byte(`{
		"schemaVersion":1,
		"mappings":[{"venue":"binance","nativeEnvironment":"demo","legacyProduct":"perp-usdt","settlementAsset":"USDT","routeProfileId":"binance.perp.usdt.demo.v1","routeId":"perp-usdt"}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(artifact.Mappings) != 1 || artifact.Mappings[0].RouteID != "perp-usdt" {
		t.Fatalf("artifact=%+v", artifact)
	}
}

func TestRepositoryMappingValidates(t *testing.T) {
	data, err := os.ReadFile("../../migration/backfills/account-route-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := decodeMapping(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(artifact.Mappings) < 20 {
		t.Fatalf("mapping count=%d", len(artifact.Mappings))
	}
}

func TestDecodeMappingAllowsDistinctSettlementRoutes(t *testing.T) {
	artifact, err := decodeMapping([]byte(`{
		"schemaVersion":1,
		"mappings":[
			{"venue":"bybit","nativeEnvironment":"demo","legacyProduct":"perp","settlementAsset":"USDT","routeProfileId":"bybit.perp.usdt.demo.v1","routeId":"perp-usdt"},
			{"venue":"bybit","nativeEnvironment":"demo","legacyProduct":"perp","settlementAsset":"USDC","routeProfileId":"bybit.perp.usdc.demo.v1","routeId":"perp-usdc"}
		]
	}`))
	if err != nil || len(artifact.Mappings) != 2 {
		t.Fatalf("error=%v", err)
	}
}

func TestDecodeMappingRejectsDuplicateFourPartLegacyProfile(t *testing.T) {
	_, err := decodeMapping([]byte(`{"schemaVersion":1,"mappings":[
		{"venue":"bybit","nativeEnvironment":"demo","legacyProduct":"perp","settlementAsset":"USDT","routeProfileId":"bybit.perp.usdt.demo.v1","routeId":"perp-usdt"},
		{"venue":"bybit","nativeEnvironment":"demo","legacyProduct":"perp","settlementAsset":"USDT","routeProfileId":"bybit.perp.usdt.demo.v1","routeId":"other-usdt"}
	]}`))
	if err == nil || !strings.Contains(err.Error(), "AMBIGUOUS_LEGACY_PROFILE") {
		t.Fatalf("error=%v", err)
	}
}

func TestRunRejectsProfileProductMismatchBeforeExecution(t *testing.T) {
	path := filepath.Join(t.TempDir(), "account-route-v1.json")
	data := []byte(`{"schemaVersion":1,"mappings":[{"venue":"binance","nativeEnvironment":"demo","legacyProduct":"spot","settlementAsset":"USDT","routeProfileId":"binance.perp.usdt.demo.v1","routeId":"spot"}]}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	called := false
	prior := executeBackfill
	executeBackfill = func(context.Context, backfillRequest) (backfillResult, error) {
		called = true
		return backfillResult{}, nil
	}
	t.Cleanup(func() { executeBackfill = prior })
	err := run(backfillArgs(path, "test"), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "ROUTE_PROFILE_CONFLICT") || called {
		t.Fatalf("error=%v called=%t", err, called)
	}
}

func TestRunRejectsLiveBeforeExecution(t *testing.T) {
	path := writeMapping(t)
	called := false
	prior := executeBackfill
	executeBackfill = func(context.Context, backfillRequest) (backfillResult, error) {
		called = true
		return backfillResult{}, nil
	}
	t.Cleanup(func() { executeBackfill = prior })
	err := run(backfillArgs(path, "live"), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "LIVE_PROVISIONING_DISABLED") || called {
		t.Fatalf("error=%v called=%t", err, called)
	}
}

func TestConnectedIdentityRejectsDeceptiveTestServiceRoutedToLive(t *testing.T) {
	querier := staticIdentityQuerier{database: "funding_live", role: "funding_live_migrator"}
	_, err := verifyConnectedIdentity(context.Background(), querier, "source", "funding_test", "funding_test_migrator", "test")
	if err == nil || !strings.Contains(err.Error(), "CONNECTED_DATABASE_IDENTITY_MISMATCH") {
		t.Fatalf("error=%v", err)
	}
}

func TestConnectedIdentityRejectsCrossModeExpectedRole(t *testing.T) {
	querier := staticIdentityQuerier{database: "funding_test", role: "funding_live_migrator"}
	_, err := verifyConnectedIdentity(context.Background(), querier, "source", "funding_test", "funding_live_migrator", "test")
	if err == nil || !strings.Contains(err.Error(), "CROSS_MODE_DATABASE_IDENTITY") {
		t.Fatalf("error=%v", err)
	}
}

type staticIdentityQuerier struct {
	database string
	role     string
}

func (querier staticIdentityQuerier) QueryRow(context.Context, string, ...any) pgx.Row {
	return staticIdentityRow{database: querier.database, role: querier.role}
}

type staticIdentityRow struct {
	database string
	role     string
}

func (row staticIdentityRow) Scan(destinations ...any) error {
	*destinations[0].(*string) = row.database
	*destinations[1].(*string) = row.role
	return nil
}

func TestReadLegacyAccountsReturnsEmptyOnlyWhenCatalogProvesRelationAbsent(t *testing.T) {
	source := &legacyCatalogQuerier{relationPresent: false, queryErr: errors.New("account query must not run")}
	accounts, err := readLegacyAccounts(context.Background(), source, "test")
	if err != nil || len(accounts) != 0 {
		t.Fatalf("accounts=%v error=%v", accounts, err)
	}
	if !source.catalogCalled || source.queryCalled {
		t.Fatalf("catalogCalled=%t queryCalled=%t", source.catalogCalled, source.queryCalled)
	}
}

func TestReadLegacyAccountsRejectsCatalogPreflightFailure(t *testing.T) {
	source := &legacyCatalogQuerier{catalogErr: errors.New("catalog unavailable"), queryErr: errors.New("account query must not run")}
	_, err := readLegacyAccounts(context.Background(), source, "test")
	if err == nil || !strings.Contains(err.Error(), "LEGACY_IDENTITY_READ_BLOCKED") {
		t.Fatalf("error=%v", err)
	}
	if !source.catalogCalled || source.queryCalled {
		t.Fatalf("catalogCalled=%t queryCalled=%t", source.catalogCalled, source.queryCalled)
	}
}

func TestReadLegacyAccountsRejectsPresentRelationQueryFailure(t *testing.T) {
	source := &legacyCatalogQuerier{relationPresent: true, queryErr: errors.New("schema drift")}
	_, err := readLegacyAccounts(context.Background(), source, "test")
	if err == nil || !strings.Contains(err.Error(), "LEGACY_IDENTITY_READ_BLOCKED") {
		t.Fatalf("error=%v", err)
	}
	if !source.catalogCalled || !source.queryCalled {
		t.Fatalf("catalogCalled=%t queryCalled=%t", source.catalogCalled, source.queryCalled)
	}
}

type legacyCatalogQuerier struct {
	relationPresent bool
	catalogErr      error
	queryErr        error
	catalogCalled   bool
	queryCalled     bool
}

func (querier *legacyCatalogQuerier) QueryRow(context.Context, string, ...any) pgx.Row {
	querier.catalogCalled = true
	return legacyCatalogRow{present: querier.relationPresent, err: querier.catalogErr}
}

func (querier *legacyCatalogQuerier) Query(context.Context, string, ...any) (pgx.Rows, error) {
	querier.queryCalled = true
	return nil, querier.queryErr
}

type legacyCatalogRow struct {
	present bool
	err     error
}

func (row legacyCatalogRow) Scan(destinations ...any) error {
	if row.err != nil {
		return row.err
	}
	*destinations[0].(*bool) = row.present
	return nil
}

func TestRunEmitsOnlyCountsAndAccountIDs(t *testing.T) {
	path := writeMapping(t)
	prior := executeBackfill
	executeBackfill = func(_ context.Context, request backfillRequest) (backfillResult, error) {
		if request.SourceService != "source" || request.SourceDatabase != "source_test" || request.SourceRole != "source_test_migrator" || request.PlatformService != "platform" || request.PlatformDatabase != "platform_test" || request.PlatformRole != "platform_test_migrator" || request.GatewayService != "gateway" || request.GatewayDatabase != "gateway_test" || request.GatewayRole != "gateway_test_migrator" || request.Mode != "test" {
			t.Fatalf("request=%+v", request)
		}
		return backfillResult{Accounts: 1, Routes: 2, AccountIDs: []string{"acct_1"}}, nil
	}
	t.Cleanup(func() { executeBackfill = prior })
	var stdout bytes.Buffer
	if err := run(backfillArgs(path, "test"), &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	output := stdout.String()
	for _, want := range []string{`"accounts":1`, `"routes":2`, `"accountIds":["acct_1"]`} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %s: %s", want, output)
		}
	}
	for _, forbidden := range []string{"secretRef", "credential", "provider", "postgres://"} {
		if strings.Contains(output, forbidden) {
			t.Errorf("output exposes %q: %s", forbidden, output)
		}
	}
}

func backfillArgs(path, mode string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	digest := sha256.Sum256(data)
	return []string{"--mapping", path, "--mapping-sha256", hex.EncodeToString(digest[:]), "--mode", mode,
		"--source-service", "source", "--source-database", "source_test", "--source-role", "source_test_migrator",
		"--platform-service", "platform", "--platform-database", "platform_test", "--platform-role", "platform_test_migrator",
		"--gateway-service", "gateway", "--gateway-database", "gateway_test", "--gateway-role", "gateway_test_migrator"}
}

func TestRunRejectsMappingChecksumMismatchBeforeExecution(t *testing.T) {
	path := writeMapping(t)
	called := false
	prior := executeBackfill
	executeBackfill = func(context.Context, backfillRequest) (backfillResult, error) {
		called = true
		return backfillResult{}, nil
	}
	t.Cleanup(func() { executeBackfill = prior })
	args := backfillArgs(path, "test")
	for index := range args {
		if args[index] == "--mapping-sha256" {
			args[index+1] = strings.Repeat("0", 64)
		}
	}
	err := run(args, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "MAPPING_CHECKSUM_MISMATCH") || called {
		t.Fatalf("error=%v called=%t", err, called)
	}
}

func TestReadMappingSnapshotRejectsSymlinkAndPermissiveMode(t *testing.T) {
	data := []byte(`{"schemaVersion":1}`)
	digest := sha256.Sum256(data)
	expectedSHA := hex.EncodeToString(digest[:])
	for _, test := range []struct {
		name  string
		setup func(*testing.T) string
	}{
		{name: "permissive mode", setup: func(t *testing.T) string {
			path := filepath.Join(t.TempDir(), "mapping.json")
			if err := os.WriteFile(path, data, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatal(err)
			}
			return path
		}},
		{name: "symlink", setup: func(t *testing.T) string {
			directory := t.TempDir()
			target := filepath.Join(directory, "target.json")
			if err := os.WriteFile(target, data, 0o600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(directory, "mapping.json")
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
			return path
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := readMappingSnapshot(test.setup(t), expectedSHA); err == nil || !strings.Contains(err.Error(), "MAPPING_ARTIFACT_UNAVAILABLE") {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestBuildAccountPlansRejectsMissingAmbiguousAndCrossModeRows(t *testing.T) {
	artifact, err := decodeMapping([]byte(`{"schemaVersion":1,"mappings":[{"venue":"binance","nativeEnvironment":"demo","legacyProduct":"perp-usdt","settlementAsset":"USDT","routeProfileId":"binance.perp.usdt.demo.v1","routeId":"perp-usdt"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	base := legacyAccount{AccountID: "acct_1", Venue: "binance", NativeEnvironment: "demo", Mode: "test", OpaqueReference: "opaque://reference", Products: []legacyProduct{{Product: "perp-usdt", SettlementAsset: "USDT"}}}
	for name, mutate := range map[string]func(*legacyAccount){
		"missing":    func(row *legacyAccount) { row.Products[0].Product = "spot" },
		"ambiguous":  func(row *legacyAccount) { row.Products = append(row.Products, row.Products[0]) },
		"cross mode": func(row *legacyAccount) { row.Mode = "live"; row.NativeEnvironment = "live" },
	} {
		t.Run(name, func(t *testing.T) {
			row := base
			row.Products = append([]legacyProduct(nil), base.Products...)
			mutate(&row)
			if _, err := buildAccountPlans("test", []legacyAccount{row}, artifact); err == nil || !strings.Contains(err.Error(), map[string]string{"missing": "MISSING_ROUTE_MAPPING", "ambiguous": "AMBIGUOUS_LEGACY_PROFILE", "cross mode": "CROSS_MODE_ACCOUNT"}[name]) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestBackfillCoordinatorRollsBackBothDatabasesOnStageFailure(t *testing.T) {
	coordinator := &recordingCoordinator{failGateway: true}
	err := writeAccountPlans(context.Background(), coordinator, []accountPlan{{AccountID: "acct_1"}})
	if err == nil || !strings.Contains(err.Error(), "BACKFILL_WRITE_BLOCKED") {
		t.Fatalf("error=%v", err)
	}
	if coordinator.committed || coordinator.platformRollbacks != 1 || coordinator.gatewayRollbacks != 1 {
		t.Fatalf("coordinator=%+v", coordinator)
	}
}

func TestGatewayPendingHeadConflictIsStableBlocker(t *testing.T) {
	tx := &pendingHeadConflictTx{}
	err := stageGatewayPlan(context.Background(), tx, accountPlan{
		AccountID: "acct_1", OperationID: uuid.MustParse("018f47a8-2578-7d3c-9d7d-5e4f4a445c11"),
		SnapshotID: uuid.MustParse("018f47a8-2578-7d3c-9d7d-5e4f4a445c12"), RequestDigest: strings.Repeat("b", 64),
	})
	if err == nil || !strings.Contains(err.Error(), "GATEWAY_PENDING_HEAD_CONFLICT") {
		t.Fatalf("error=%v", err)
	}
}

func TestExistingDesiredOperationMustMatchPreparedIdentity(t *testing.T) {
	operationID := uuid.MustParse("018f47a8-2578-7d3c-9d7d-5e4f4a445c21")
	snapshotID := uuid.MustParse("018f47a8-2578-7d3c-9d7d-5e4f4a445c22")
	digest := strings.Repeat("b", 64)
	plan := accountPlan{AccountID: "acct_1", OperationID: operationID, SnapshotID: snapshotID, RequestDigest: digest}
	for _, test := range []struct {
		name       string
		snapshotID uuid.UUID
		digest     string
		state      string
	}{
		{name: "snapshot mismatch", snapshotID: uuid.MustParse("018f47a8-2578-7d3c-9d7d-5e4f4a445c23"), digest: digest, state: "prepared"},
		{name: "digest mismatch", snapshotID: snapshotID, digest: strings.Repeat("c", 64), state: "prepared"},
		{name: "candidate failed", snapshotID: snapshotID, digest: digest, state: "candidate_failed"},
		{name: "candidate ready", snapshotID: snapshotID, digest: digest, state: "candidate_ready"},
		{name: "applied", snapshotID: snapshotID, digest: digest, state: "applied"},
	} {
		t.Run(test.name, func(t *testing.T) {
			tx := &existingDesiredOperationTx{runtimeSnapshotID: snapshotID, runtimeDigest: digest, desiredSnapshotID: test.snapshotID, desiredDigest: test.digest, desiredState: test.state}
			if err := stageGatewayPlan(context.Background(), tx, plan); !errors.Is(err, errGatewayOperationConflict) {
				t.Fatalf("error=%v", err)
			}
			if !tx.desiredOperationLocked {
				t.Fatal("existing desired operation was not locked before validation")
			}
		})
	}
}

type existingDesiredOperationTx struct {
	runtimeSnapshotID      uuid.UUID
	runtimeDigest          string
	desiredSnapshotID      uuid.UUID
	desiredDigest          string
	desiredState           string
	desiredOperationLocked bool
}

func (*existingDesiredOperationTx) Exec(_ context.Context, query string, _ ...any) (pgconn.CommandTag, error) {
	if strings.Contains(query, "gateway_runtime_snapshots") || strings.Contains(query, "gateway_desired_operations") {
		return pgconn.NewCommandTag("INSERT 0 0"), nil
	}
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}

func (transaction *existingDesiredOperationTx) QueryRow(_ context.Context, query string, _ ...any) pgx.Row {
	if strings.Contains(query, "gateway_desired_operations") {
		transaction.desiredOperationLocked = strings.Contains(query, "FOR UPDATE")
		return desiredOperationRow{snapshotID: transaction.desiredSnapshotID, digest: transaction.desiredDigest, state: transaction.desiredState}
	}
	return desiredOperationRow{snapshotID: transaction.runtimeSnapshotID, digest: transaction.runtimeDigest}
}

type desiredOperationRow struct {
	snapshotID uuid.UUID
	digest     string
	state      string
}

func (row desiredOperationRow) Scan(destinations ...any) error {
	if len(destinations) == 2 {
		*destinations[0].(*string) = row.digest
		*destinations[1].(*uuid.UUID) = row.snapshotID
		return nil
	}
	*destinations[0].(*uuid.UUID) = row.snapshotID
	*destinations[1].(*string) = row.digest
	*destinations[2].(*string) = row.state
	return nil
}

func TestPendingHeadConflictRollsBackPlatformWithStableCode(t *testing.T) {
	coordinator := &recordingCoordinator{gatewayErr: errGatewayPendingHeadConflict}
	err := writeAccountPlans(context.Background(), coordinator, []accountPlan{{AccountID: "acct_1"}})
	if err == nil || !strings.Contains(err.Error(), "GATEWAY_PENDING_HEAD_CONFLICT") {
		t.Fatalf("error=%v", err)
	}
	if coordinator.committed || coordinator.platformRollbacks != 1 || coordinator.gatewayRollbacks != 1 {
		t.Fatalf("coordinator=%+v", coordinator)
	}
}

type pendingHeadConflictTx struct{}

func (*pendingHeadConflictTx) Exec(_ context.Context, query string, _ ...any) (pgconn.CommandTag, error) {
	if strings.Contains(query, "gateway_account_runtime_heads") {
		return pgconn.NewCommandTag("INSERT 0 0"), nil
	}
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}

func (*pendingHeadConflictTx) QueryRow(context.Context, string, ...any) pgx.Row {
	panic("unexpected QueryRow")
}

func TestBackfillCoordinatorConvergesAfterGatewayCommitPlatformFailure(t *testing.T) {
	plan := accountPlan{
		AccountID:     "acct_1",
		OperationID:   uuid.MustParse("018f47a8-2578-7d3c-9d7d-5e4f4a445c01"),
		SnapshotID:    uuid.MustParse("018f47a8-2578-7d3c-9d7d-5e4f4a445c02"),
		RequestDigest: strings.Repeat("a", 64),
	}
	coordinator := newCrashWindowCoordinator()
	if err := writeAccountPlans(context.Background(), coordinator, []accountPlan{plan}); err == nil || !strings.Contains(err.Error(), "BACKFILL_COMMIT_BLOCKED") {
		t.Fatalf("first attempt error=%v", err)
	}
	if coordinator.platformCommitted || coordinator.pendingSnapshot != plan.SnapshotID || coordinator.activeSnapshot != uuid.Nil || len(coordinator.gatewayOperations) != 1 {
		t.Fatalf("stranded state=%+v", coordinator)
	}
	if err := writeAccountPlans(context.Background(), coordinator, []accountPlan{plan}); err != nil {
		t.Fatalf("identical retry error=%v", err)
	}
	if !coordinator.platformCommitted || coordinator.pendingSnapshot != plan.SnapshotID || coordinator.activeSnapshot != uuid.Nil || len(coordinator.gatewayOperations) != 1 {
		t.Fatalf("converged state=%+v", coordinator)
	}
}

func TestBackfillPendingSnapshotIsDiagnosticOnlyUntilPromotion(t *testing.T) {
	backfillSource := readSourceFile(t, "app.go")
	stageGateway := functionBlock(t, backfillSource, "func stageGatewayPlan")
	for _, want := range []string{"'prepared'", "pending_snapshot_id"} {
		if !strings.Contains(stageGateway, want) {
			t.Fatalf("Gateway staging contract missing %q", want)
		}
	}
	if strings.Contains(stageGateway, "active_snapshot_id") {
		t.Fatal("Gateway staging can make a backfill snapshot active")
	}

	handoffSource := readSourceFile(t, "../../gateway/store/postgres/commands.go")
	handoff := functionBlock(t, handoffSource, "func (postgresStore *Store) BeginOrderHandoff")
	if !strings.Contains(handoff, "SELECT active_snapshot_id") || strings.Contains(handoff, "pending_snapshot_id") {
		t.Fatal("order handoff is not gated exclusively by the active snapshot")
	}

	runtimeSource := readSourceFile(t, "../../gateway/store/postgres/runtime_snapshots.go")
	diagnostics := functionBlock(t, runtimeSource, "func loadRuntimeState")
	if !strings.Contains(diagnostics, "SELECT active_snapshot_id,pending_snapshot_id") || !strings.Contains(diagnostics, "result.Pending") {
		t.Fatal("runtime diagnostics do not expose stranded pending snapshots")
	}
}

type recordingCoordinator struct {
	failGateway       bool
	gatewayErr        error
	committed         bool
	platformRollbacks int
	gatewayRollbacks  int
}

func (coordinator *recordingCoordinator) Begin(context.Context) error { return nil }
func (coordinator *recordingCoordinator) StagePlatform(context.Context, accountPlan) error {
	return nil
}
func (coordinator *recordingCoordinator) StageGateway(context.Context, accountPlan) error {
	if coordinator.gatewayErr != nil {
		return coordinator.gatewayErr
	}
	if coordinator.failGateway {
		return fmt.Errorf("synthetic")
	}
	return nil
}
func (coordinator *recordingCoordinator) Commit(context.Context) error {
	coordinator.committed = true
	return nil
}
func (coordinator *recordingCoordinator) RollbackPlatform(context.Context) {
	coordinator.platformRollbacks++
}
func (coordinator *recordingCoordinator) RollbackGateway(context.Context) {
	coordinator.gatewayRollbacks++
}

type gatewayOperation struct {
	digest     string
	snapshotID uuid.UUID
}

type crashWindowCoordinator struct {
	gatewayOperations  map[uuid.UUID]gatewayOperation
	pendingSnapshot    uuid.UUID
	activeSnapshot     uuid.UUID
	stagedPlan         *accountPlan
	platformCommitted  bool
	failPlatformCommit bool
}

func newCrashWindowCoordinator() *crashWindowCoordinator {
	return &crashWindowCoordinator{gatewayOperations: make(map[uuid.UUID]gatewayOperation), failPlatformCommit: true}
}

func (coordinator *crashWindowCoordinator) Begin(context.Context) error {
	coordinator.stagedPlan = nil
	return nil
}

func (coordinator *crashWindowCoordinator) StagePlatform(_ context.Context, plan accountPlan) error {
	copy := plan
	coordinator.stagedPlan = &copy
	return nil
}

func (coordinator *crashWindowCoordinator) StageGateway(_ context.Context, plan accountPlan) error {
	if existing, ok := coordinator.gatewayOperations[plan.OperationID]; ok && (existing.digest != plan.RequestDigest || existing.snapshotID != plan.SnapshotID) {
		return fmt.Errorf("gateway operation conflict")
	}
	return nil
}

func (coordinator *crashWindowCoordinator) Commit(context.Context) error {
	plan := *coordinator.stagedPlan
	coordinator.gatewayOperations[plan.OperationID] = gatewayOperation{digest: plan.RequestDigest, snapshotID: plan.SnapshotID}
	coordinator.pendingSnapshot = plan.SnapshotID
	if coordinator.failPlatformCommit {
		coordinator.failPlatformCommit = false
		return fmt.Errorf("synthetic platform commit failure")
	}
	coordinator.platformCommitted = true
	return nil
}

func (coordinator *crashWindowCoordinator) RollbackPlatform(context.Context) {
	coordinator.stagedPlan = nil
}

func (coordinator *crashWindowCoordinator) RollbackGateway(context.Context) {}

func readSourceFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func functionBlock(t *testing.T, source, signature string) string {
	t.Helper()
	start := strings.Index(source, signature)
	if start < 0 {
		t.Fatalf("source is missing %q", signature)
	}
	rest := source[start+len(signature):]
	end := strings.Index(rest, "\nfunc ")
	if end < 0 {
		return source[start:]
	}
	return source[start : start+len(signature)+end]
}

func writeMapping(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "account-route-v1.json")
	data := []byte(`{"schemaVersion":1,"mappings":[{"venue":"binance","nativeEnvironment":"demo","legacyProduct":"perp-usdt","settlementAsset":"USDT","routeProfileId":"binance.perp.usdt.demo.v1","routeId":"perp-usdt"}]}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
