package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
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

func TestRunEmitsOnlyCountsAndAccountIDs(t *testing.T) {
	path := writeMapping(t)
	prior := executeBackfill
	executeBackfill = func(_ context.Context, request backfillRequest) (backfillResult, error) {
		if request.SourceService != "source" || request.SourceDatabase != "source_db" || request.PlatformService != "platform" || request.PlatformDatabase != "platform_db" || request.GatewayService != "gateway" || request.GatewayDatabase != "gateway_db" || request.Mode != "test" {
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
	return []string{"--mapping", path, "--mode", mode, "--source-service", "source", "--source-database", "source_db", "--platform-service", "platform", "--platform-database", "platform_db", "--gateway-service", "gateway", "--gateway-database", "gateway_db"}
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
	stageGateway := functionBlock(t, backfillSource, "func (coordinator *postgresCoordinator) StageGateway")
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
	committed         bool
	platformRollbacks int
	gatewayRollbacks  int
}

func (coordinator *recordingCoordinator) Begin(context.Context) error { return nil }
func (coordinator *recordingCoordinator) StagePlatform(context.Context, accountPlan) error {
	return nil
}
func (coordinator *recordingCoordinator) StageGateway(context.Context, accountPlan) error {
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
