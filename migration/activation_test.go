package migration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/gateway/domain"
	storepkg "github.com/QuantProcessing/boltertrader/gateway/store"
	"github.com/QuantProcessing/boltertrader/migration/authority"
	"github.com/QuantProcessing/boltertrader/secretstore"
)

func TestActivationSupervisorTopologySHA256NormalizesOwnedProcessIDs(t *testing.T) {
	readiness := "postgres ready\ngateway ready\nplatform ready\nfunding-worker ready\nweb ready"
	before := "postgres running pid=101\ngateway running pid=102\nplatform running pid=103\nfunding-worker running pid=104\nweb running pid=105"
	after := "postgres running pid=201\ngateway running pid=202\nplatform running pid=203\nfunding-worker running pid=204\nweb running pid=205"

	beforeSHA, err := activationSupervisorTopologySHA256(before, readiness)
	if err != nil {
		t.Fatalf("before topology: %v", err)
	}
	afterSHA, err := activationSupervisorTopologySHA256(after, readiness)
	if err != nil {
		t.Fatalf("after topology: %v", err)
	}
	if beforeSHA != afterSHA {
		t.Fatalf("owned PID restart changed supervisor topology: before=%s after=%s", beforeSHA, afterSHA)
	}
}

func TestActivationSupervisorTopologySHA256RejectsSemanticDrift(t *testing.T) {
	readiness := "postgres ready\ngateway ready\nplatform ready\nfunding-worker ready\nweb ready"
	status := "postgres running pid=101\ngateway running pid=102\nplatform running pid=103\nfunding-worker running pid=104\nweb running pid=105"
	for name, changed := range map[string][2]string{
		"service state":   {strings.Replace(status, "gateway running", "gateway stopped", 1), readiness},
		"service order":   {strings.Replace(status, "postgres running pid=101\ngateway running pid=102", "gateway running pid=102\npostgres running pid=101", 1), readiness},
		"missing service": {strings.Replace(status, "\nweb running pid=105", "", 1), readiness},
		"extra service":   {status + "\nother running pid=106", readiness},
		"readiness state": {status, strings.Replace(readiness, "gateway ready", "gateway degraded", 1)},
		"readiness order": {status, strings.Replace(readiness, "postgres ready\ngateway ready", "gateway ready\npostgres ready", 1)},
		"invalid pid":     {strings.Replace(status, "pid=102", "pid=not-a-pid", 1), readiness},
		"zero pid":        {strings.Replace(status, "pid=102", "pid=0", 1), readiness},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := activationSupervisorTopologySHA256(changed[0], changed[1]); err == nil {
				t.Fatal("semantic supervisor drift was accepted")
			}
		})
	}
}

func TestActivationAuthorityRejectsTamperedDatabaseMigrationLedger(t *testing.T) {
	matrix := authority.PlatformV15()
	entries := make([]authority.MigrationLedgerEntry, matrix.SchemaVersion)
	for index := range entries {
		entries[index] = authority.MigrationLedgerEntry{Version: index + 1, Checksum: strings.Repeat("0", 64)}
	}
	_, err := readVerifiedActivationLedger(context.Background(), activationLedgerQuerier{entries: entries}, matrix, `SELECT version,checksum FROM platform_schema_migrations ORDER BY version`)
	if err == nil || !strings.Contains(err.Error(), "MIGRATION_DIGEST_MISMATCH") {
		t.Fatalf("tampered activation ledger error=%v", err)
	}
}

func TestActivationAuthorityTransactionAssumesRuntimeBeforeLedgerAndRollsBack(t *testing.T) {
	entry := authority.MigrationLedgerEntry{Version: 1, Checksum: strings.Repeat("a", 64)}
	digest, err := authority.CanonicalMigrationLedgerDigest([]authority.MigrationLedgerEntry{entry})
	if err != nil {
		t.Fatal(err)
	}
	matrix := authority.Matrix{
		Name: "activation-order-test", SchemaVersion: 1, MigrationDigest: digest,
		Roles: authority.RoleTopology{Maintenance: "maintenance_test", Migrator: "migrator_test", Runtime: "runtime_test", Writer: "writer_test"},
	}
	transaction := &activationAuthorityOrderTransaction{
		t: t, entries: []authority.MigrationLedgerEntry{entry},
		migratorAdmin: false, migratorInherit: false, migratorSet: true,
		runtimeAdmin: true, runtimeInherit: false, runtimeSet: false,
		writerAdmin: true, writerInherit: false, writerSet: false,
	}
	_, err = verifyActivationAuthorityCatalogTransaction(
		context.Background(), transaction, "maintenance_test", "platform_test", "runtime_test", matrix,
		`SELECT schema_version FROM test_schema`, `SELECT version,checksum FROM test_schema ORDER BY version`,
	)
	if err == nil || !strings.Contains(err.Error(), "AUTHORITY_CATALOG_QUERY_FAILED: schema-version") {
		t.Fatalf("catalog stop error=%v", err)
	}
	want := []string{"verify-migrator-membership", "set-migrator", "grant-connect", "reset-role", "verify-runtime-membership", "verify-writer-membership", "grant-set", "set-runtime", "ledger", "catalog", "rollback"}
	if !reflect.DeepEqual(transaction.events, want) {
		t.Fatalf("authority transaction order=%v, want %v", transaction.events, want)
	}
}

func TestActivationAuthorityTransactionRejectsNonCanonicalMaintenanceMembership(t *testing.T) {
	for _, tc := range []struct {
		name                string
		admin, inherit, set bool
	}{
		{name: "missing admin", admin: false, inherit: false, set: false},
		{name: "inherits runtime", admin: true, inherit: true, set: false},
		{name: "permanent set", admin: true, inherit: false, set: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			matrix := authority.Matrix{Roles: authority.RoleTopology{Maintenance: "maintenance_test", Migrator: "migrator_test", Runtime: "runtime_test", Writer: "writer_test"}}
			transaction := &activationAuthorityOrderTransaction{
				t: t, migratorSet: true, runtimeAdmin: tc.admin, runtimeInherit: tc.inherit, runtimeSet: tc.set, writerAdmin: true,
			}
			_, err := verifyActivationAuthorityCatalogTransaction(
				context.Background(), transaction, "maintenance_test", "platform_test", "runtime_test", matrix, "schema", "ledger",
			)
			if err == nil || !strings.Contains(err.Error(), "maintenance runtime membership") {
				t.Fatalf("membership error=%v", err)
			}
			want := []string{"verify-migrator-membership", "set-migrator", "grant-connect", "reset-role", "verify-runtime-membership", "rollback"}
			if !reflect.DeepEqual(transaction.events, want) {
				t.Fatalf("authority transaction order=%v, want %v", transaction.events, want)
			}
		})
	}
}

func TestActivationAuthorityTransactionRejectsNonCanonicalWriterAdminOption(t *testing.T) {
	for _, tc := range []struct {
		name                string
		admin, inherit, set bool
	}{
		{name: "missing admin", admin: false},
		{name: "inherits writer", admin: true, inherit: true},
		{name: "permanent set", admin: true, set: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			matrix := authority.Matrix{Roles: authority.RoleTopology{Maintenance: "maintenance_test", Migrator: "migrator_test", Runtime: "runtime_test", Writer: "writer_test"}}
			transaction := &activationAuthorityOrderTransaction{
				t: t, migratorSet: true, runtimeAdmin: true,
				writerAdmin: tc.admin, writerInherit: tc.inherit, writerSet: tc.set,
			}
			_, err := verifyActivationAuthorityCatalogTransaction(
				context.Background(), transaction, "maintenance_test", "platform_test", "runtime_test", matrix, "schema", "ledger",
			)
			if err == nil || !strings.Contains(err.Error(), "maintenance writer membership") {
				t.Fatalf("membership error=%v", err)
			}
			want := []string{"verify-migrator-membership", "set-migrator", "grant-connect", "reset-role", "verify-runtime-membership", "verify-writer-membership", "rollback"}
			if !reflect.DeepEqual(transaction.events, want) {
				t.Fatalf("authority transaction order=%v, want %v", transaction.events, want)
			}
		})
	}
}

func TestActivationAuthorityTransactionRejectsNonCanonicalMigratorMembershipBeforeConnectGrant(t *testing.T) {
	for _, tc := range []struct {
		name                string
		admin, inherit, set bool
	}{
		{name: "admin option", admin: true, inherit: false, set: true},
		{name: "inherits migrator", admin: false, inherit: true, set: true},
		{name: "missing set", admin: false, inherit: false, set: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			matrix := authority.Matrix{Roles: authority.RoleTopology{Maintenance: "maintenance_test", Migrator: "migrator_test", Runtime: "runtime_test"}}
			transaction := &activationAuthorityOrderTransaction{
				t: t, migratorAdmin: tc.admin, migratorInherit: tc.inherit, migratorSet: tc.set,
				runtimeAdmin: true,
			}
			_, err := verifyActivationAuthorityCatalogTransaction(
				context.Background(), transaction, "maintenance_test", "platform_test", "runtime_test", matrix, "schema", "ledger",
			)
			if err == nil || !strings.Contains(err.Error(), "maintenance migrator membership") {
				t.Fatalf("membership error=%v", err)
			}
			want := []string{"verify-migrator-membership", "rollback"}
			if !reflect.DeepEqual(transaction.events, want) {
				t.Fatalf("authority transaction order=%v, want %v", transaction.events, want)
			}
		})
	}
}

func TestActivationEnvironmentRejectsModeAndVenueWriteDrift(t *testing.T) {
	t.Setenv("FT_MODE", "live")
	if err := verifyActivationEnvironment(); err == nil {
		t.Fatal("Live mode was accepted")
	}
	t.Setenv("FT_MODE", "test")
	t.Setenv("BOLTER_ENABLE_BINANCE_WRITES", "1")
	if err := verifyActivationEnvironment(); err == nil {
		t.Fatal("venue write opt-in was accepted")
	}
	t.Setenv("BOLTER_ENABLE_BINANCE_WRITES", "")
	for _, name := range []string{"BT_GATEWAY_STARTUP_PROFILE", "BT_PLATFORM_STARTUP_PROFILE", "BT_FUNDING_STARTUP_PROFILE"} {
		t.Setenv(name, "migration-validation")
		if err := verifyActivationEnvironment(); err == nil || !strings.Contains(err.Error(), name) {
			t.Fatalf("migration-validation profile %s was accepted: %v", name, err)
		}
		t.Setenv(name, "")
	}
	t.Setenv("BT_DEV_ENABLE_LIVE_PRIVATE_WRITES", "0")
	t.Setenv("BT_LIVE_PRIVATE_WRITES_ENABLED", "0")
	if err := verifyActivationEnvironment(); err != nil {
		t.Fatalf("canonical Test environment: %v", err)
	}
}

func TestActivationEnvironmentDriftStopsBeforePostgresLockAndEvidence(t *testing.T) {
	t.Setenv("FT_MODE", "live")
	prior := acquireActivationPostgresLock
	acquires := 0
	acquireActivationPostgresLock = func(context.Context, ExecutionRequest) (func(context.Context) error, error) {
		acquires++
		return func(context.Context) error { return nil }, nil
	}
	t.Cleanup(func() { acquireActivationPostgresLock = prior })
	manifest := Manifest{MigrationID: "G010", Workflow: Workflow{Phase: PhaseActivationReady}}
	_, err := executeActivationLocked(context.Background(), ExecutionRequest{EvidenceDir: t.TempDir()}, ExecuteOptions{}, manifest)
	if err == nil || !strings.Contains(err.Error(), "FT_MODE=test") || acquires != 0 {
		t.Fatalf("error=%v lock acquires=%d", err, acquires)
	}
}

func TestExecuteActivationActivatedLockFailureDoesNotContain(t *testing.T) {
	t.Setenv("FT_MODE", "test")
	t.Setenv("BOLTER_ENABLE_BINANCE_WRITES", "")
	prior := acquireActivationPostgresLock
	acquireActivationPostgresLock = func(context.Context, ExecutionRequest) (func(context.Context) error, error) {
		return nil, errors.New("activation PostgreSQL lock is held")
	}
	t.Cleanup(func() { acquireActivationPostgresLock = prior })
	var commands []Command
	options := ExecuteOptions{Run: func(_ context.Context, command Command) (CommandResult, error) {
		commands = append(commands, command)
		return CommandResult{}, nil
	}}
	manifest := Manifest{MigrationID: "G010", Workflow: Workflow{Phase: PhaseActivated}}
	request := ExecutionRequest{ManifestPath: filepath.Join(t.TempDir(), "manifest.json"), EvidenceDir: t.TempDir(), StateDir: t.TempDir()}
	if _, err := executeActivationLocked(context.Background(), request, options, manifest); err == nil || !strings.Contains(err.Error(), "lock is held") {
		t.Fatalf("lock error=%v", err)
	}
	if len(commands) != 0 {
		t.Fatalf("lock competition triggered containment commands: %+v", commands)
	}
}

func TestExecuteActivationActivatedFailureContainsOnlyAfterLockAcquired(t *testing.T) {
	t.Setenv("FT_MODE", "test")
	t.Setenv("BOLTER_ENABLE_BINANCE_WRITES", "")
	stateDir := t.TempDir()
	for path, value := range map[string]string{filepath.Join(stateDir, "pids", "postgres.pid"): "123\n", filepath.Join(stateDir, "ownership", "postgres.start"): "987654\n"} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	prior := acquireActivationPostgresLock
	acquires := 0
	var events []string
	acquireActivationPostgresLock = func(context.Context, ExecutionRequest) (func(context.Context) error, error) {
		acquires++
		return func(context.Context) error { events = append(events, "release-lock"); return nil }, nil
	}
	t.Cleanup(func() { acquireActivationPostgresLock = prior })
	var commands []Command
	options := ExecuteOptions{Run: func(_ context.Context, command Command) (CommandResult, error) {
		events = append(events, "contain")
		commands = append(commands, command)
		script := string(command.Stdin)
		if strings.Contains(script, "pg_postmaster_start_time") {
			return CommandResult{Output: []byte("2026-08-10 00:00:00+00\n")}, nil
		}
		if strings.Contains(script, "pg_terminate_backend") {
			return CommandResult{Output: []byte("true\n")}, nil
		}
		return CommandResult{}, nil
	}}
	manifest := Manifest{MigrationID: "G010", Workflow: Workflow{Phase: PhaseActivated}}
	request := ExecutionRequest{ManifestPath: filepath.Join(t.TempDir(), "manifest.json"), EvidenceDir: t.TempDir(), StateDir: stateDir}
	if _, err := executeActivationLocked(context.Background(), request, options, manifest); err == nil || !strings.Contains(err.Error(), "missing its verified attempt") {
		t.Fatalf("activated binding error=%v", err)
	}
	joined := ""
	for _, command := range commands {
		joined += command.Name + " " + strings.Join(command.Args, " ") + "\n" + string(command.Stdin)
	}
	if acquires != 1 || !strings.Contains(joined, "stop-app") || !strings.Contains(joined, "pg_terminate_backend") {
		t.Fatalf("acquires=%d commands=%s", acquires, joined)
	}
	if len(events) == 0 || events[0] != "contain" || events[len(events)-1] != "release-lock" {
		t.Fatalf("containment did not finish while the advisory lock was held: %v", events)
	}
}

func TestExecuteActivationHoldsOperationWidePostgresLockBeforeEvidenceRevalidation(t *testing.T) {
	t.Setenv("FT_MODE", "test")
	prior := acquireActivationPostgresLock
	locked := make(chan struct{})
	allowAcquire := make(chan struct{})
	held := false
	acquireActivationPostgresLock = func(context.Context, ExecutionRequest) (func(context.Context) error, error) {
		if held {
			return nil, errors.New("activation PostgreSQL lock is held")
		}
		held = true
		close(locked)
		<-allowAcquire
		return func(context.Context) error { held = false; return nil }, nil
	}
	t.Cleanup(func() { acquireActivationPostgresLock = prior })

	manifest := Manifest{MigrationID: "G010", Workflow: Workflow{Phase: PhaseActivationReady}}
	firstRequest := ExecutionRequest{ManifestPath: filepath.Join(t.TempDir(), "first.json"), EvidenceDir: t.TempDir()}
	firstDone := make(chan error, 1)
	go func() {
		_, err := executeActivationLocked(context.Background(), firstRequest, ExecuteOptions{}, manifest)
		firstDone <- err
	}()
	<-locked
	secondRequest := firstRequest
	secondRequest.ManifestPath = filepath.Join(t.TempDir(), "second.json")
	if _, err := executeActivationLocked(context.Background(), secondRequest, ExecuteOptions{}, manifest); err == nil || !strings.Contains(err.Error(), "lock is held") {
		t.Fatalf("second activation lock error=%v", err)
	}
	close(allowAcquire)
	if err := <-firstDone; err == nil || !strings.Contains(err.Error(), "prior replay") {
		t.Fatalf("first activation error=%v", err)
	}
}

func TestExecuteActivationOrdersBoundariesAndReplaysVerifiedAttempt(t *testing.T) {
	input := testActivationInput(t)
	var attempt *ActivationAttempt
	var calls []string
	deps := testActivationDependencies(&attempt, &calls)
	stamp := time.Now().UTC().Add(time.Hour)
	deps.Now = func() time.Time { return stamp }
	result, err := ExecuteActivation(context.Background(), input, deps)
	if err != nil {
		t.Fatal(err)
	}
	if result.Phase != PhaseActivated || attempt == nil || attempt.Boundary != ActivationVerified {
		t.Fatalf("result=%+v attempt=%+v", result, attempt)
	}
	want := []string{"promotion-state", "prior-state", "source-fence", "pre-grant-authority", "stop", "fence-target-connect", "observe-gateway-promotion-head", "promote", "verify-gateway-promotion", "grant", "verify-authority", "connect", "verify-restored-sessions", "start", "verify", "verify-running-post-grant-dml"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=%v, want %v", calls, want)
	}
	calls = nil
	replay, err := ExecuteActivation(context.Background(), input, deps)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Replayed || !reflect.DeepEqual(calls, []string{"promotion-state", "recover-prior-state", "source-fence", "verify-gateway-promotion", "verify-running-post-grant-dml", "verify"}) {
		t.Fatalf("replay=%+v calls=%v", replay, calls)
	}
}

func TestExecuteActivationVerifiedReplayAllowsOwnedSupervisorRestart(t *testing.T) {
	input := testActivationInput(t)
	var attempt *ActivationAttempt
	var calls []string
	deps := testActivationDependencies(&attempt, &calls)
	if _, err := ExecuteActivation(context.Background(), input, deps); err != nil {
		t.Fatalf("first activation: %v", err)
	}
	sealedSupervisorSHA := attempt.Observations.SupervisorStatusSHA256
	sealedProofSHA := attempt.ProofSHA256
	sealedEvidencePath := attempt.EvidencePath
	originalObserve := deps.ObserveActivation
	deps.ObserveActivation = func(ctx context.Context, input ActivationInput, value ActivationAttempt, inputSHA string, facts ActivationVerificationFacts) (ActivationObservations, error) {
		observed, err := originalObserve(ctx, input, value, inputSHA, facts)
		observed.SupervisorStatusSHA256 = strings.Repeat("7", 64)
		return observed, err
	}

	calls = nil
	replay, err := ExecuteActivation(context.Background(), input, deps)
	if err != nil {
		t.Fatalf("replay after owned supervisor restart: %v", err)
	}
	if !replay.Replayed {
		t.Fatalf("replay=%+v", replay)
	}
	if attempt.Observations.SupervisorStatusSHA256 != sealedSupervisorSHA {
		t.Fatal("verified replay replaced sealed supervisor evidence")
	}
	if attempt.ProofSHA256 != sealedProofSHA || attempt.EvidencePath != sealedEvidencePath {
		t.Fatal("verified replay replaced sealed proof evidence")
	}
	wantCalls := []string{"promotion-state", "recover-prior-state", "source-fence", "verify-gateway-promotion", "verify-running-post-grant-dml", "verify"}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("replay calls=%v want=%v", calls, wantCalls)
	}
}

func TestExecuteActivationVerifiedReplayRejectsSupervisorTopologyDrift(t *testing.T) {
	input := testActivationInput(t)
	var attempt *ActivationAttempt
	var calls []string
	deps := testActivationDependencies(&attempt, &calls)
	if _, err := ExecuteActivation(context.Background(), input, deps); err != nil {
		t.Fatalf("first activation: %v", err)
	}
	originalObserve := deps.ObserveActivation
	deps.ObserveActivation = func(ctx context.Context, input ActivationInput, value ActivationAttempt, inputSHA string, facts ActivationVerificationFacts) (ActivationObservations, error) {
		observed, err := originalObserve(ctx, input, value, inputSHA, facts)
		observed.SupervisorStatusSHA256 = strings.Repeat("8", 64)
		observed.SupervisorTopologySHA256 = strings.Repeat("9", 64)
		return observed, err
	}
	calls = nil

	if _, err := ExecuteActivation(context.Background(), input, deps); err == nil || !strings.Contains(err.Error(), "measured observations changed") {
		t.Fatalf("supervisor topology drift error=%v", err)
	}
	if !containsString(calls, "contain") {
		t.Fatalf("supervisor topology drift was not contained: calls=%v", calls)
	}
}

func TestExecuteActivationVerifiedReplayAllowsLegacySupervisorEvidenceAfterOwnedRestart(t *testing.T) {
	input := testActivationInput(t)
	var attempt *ActivationAttempt
	var calls []string
	deps := testActivationDependencies(&attempt, &calls)
	if _, err := ExecuteActivation(context.Background(), input, deps); err != nil {
		t.Fatalf("first activation: %v", err)
	}

	data, err := os.ReadFile(attempt.EvidencePath)
	if err != nil {
		t.Fatal(err)
	}
	var proof operationProof
	if err := json.Unmarshal(data, &proof); err != nil {
		t.Fatal(err)
	}
	attempt.Observations.SupervisorTopologySHA256 = ""
	proof.ActivationObservations.SupervisorTopologySHA256 = ""
	data, err = json.MarshalIndent(proof, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(attempt.EvidencePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	attempt.ProofSHA256 = sha256Hex(data)
	legacySupervisorSHA := attempt.Observations.SupervisorStatusSHA256
	legacyProofSHA := attempt.ProofSHA256
	legacyEvidencePath := attempt.EvidencePath

	originalObserve := deps.ObserveActivation
	deps.ObserveActivation = func(ctx context.Context, input ActivationInput, value ActivationAttempt, inputSHA string, facts ActivationVerificationFacts) (ActivationObservations, error) {
		observed, err := originalObserve(ctx, input, value, inputSHA, facts)
		observed.SupervisorStatusSHA256 = strings.Repeat("8", 64)
		return observed, err
	}
	calls = nil

	replay, err := ExecuteActivation(context.Background(), input, deps)
	if err != nil {
		t.Fatalf("legacy replay after owned supervisor restart: %v", err)
	}
	if !replay.Replayed {
		t.Fatalf("replay=%+v", replay)
	}
	if attempt.Observations.SupervisorStatusSHA256 != legacySupervisorSHA || attempt.Observations.SupervisorTopologySHA256 != "" {
		t.Fatal("legacy replay replaced sealed supervisor evidence")
	}
	if attempt.ProofSHA256 != legacyProofSHA || attempt.EvidencePath != legacyEvidencePath {
		t.Fatal("legacy replay replaced sealed proof evidence")
	}
}

func TestExecuteActivationVerifiedReplayRejectsInvalidCurrentSupervisorEvidence(t *testing.T) {
	input := testActivationInput(t)
	var attempt *ActivationAttempt
	var calls []string
	deps := testActivationDependencies(&attempt, &calls)
	if _, err := ExecuteActivation(context.Background(), input, deps); err != nil {
		t.Fatalf("first activation: %v", err)
	}
	originalObserve := deps.ObserveActivation
	deps.ObserveActivation = func(ctx context.Context, input ActivationInput, value ActivationAttempt, inputSHA string, facts ActivationVerificationFacts) (ActivationObservations, error) {
		observed, err := originalObserve(ctx, input, value, inputSHA, facts)
		observed.SupervisorTopologySHA256 = ""
		return observed, err
	}
	calls = nil

	if _, err := ExecuteActivation(context.Background(), input, deps); err == nil || !strings.Contains(err.Error(), "replay observations") {
		t.Fatalf("invalid current supervisor observation error=%v", err)
	}
	if !containsString(calls, "contain") {
		t.Fatalf("invalid current supervisor observation was not contained: calls=%v", calls)
	}
}

func TestExecuteActivationVerifiedReplayRejectsOtherObservationDrift(t *testing.T) {
	input := testActivationInput(t)
	var attempt *ActivationAttempt
	var calls []string
	deps := testActivationDependencies(&attempt, &calls)
	if _, err := ExecuteActivation(context.Background(), input, deps); err != nil {
		t.Fatalf("first activation: %v", err)
	}
	originalObserve := deps.ObserveActivation
	deps.ObserveActivation = func(ctx context.Context, input ActivationInput, value ActivationAttempt, inputSHA string, facts ActivationVerificationFacts) (ActivationObservations, error) {
		observed, err := originalObserve(ctx, input, value, inputSHA, facts)
		observed.PostgresIdentitySHA256 = strings.Repeat("7", 64)
		return observed, err
	}
	calls = nil

	if _, err := ExecuteActivation(context.Background(), input, deps); err == nil || !strings.Contains(err.Error(), "measured observations changed") {
		t.Fatalf("PostgreSQL identity drift error=%v", err)
	}
	if !containsString(calls, "contain") {
		t.Fatalf("PostgreSQL identity drift was not contained: calls=%v", calls)
	}
}

func TestExecuteActivationAllowsCanonicalEmptyGatewayPromotion(t *testing.T) {
	input := testActivationInput(t)
	var attempt *ActivationAttempt
	var calls []string
	deps := testActivationDependencies(&attempt, &calls)
	operationSet := strings.Repeat("a", 64)
	emptyHead := activationGatewayHeadObservation(storepkg.MigrationPromotionDigest{
		PendingSHA256:      activationMigrationStateSHA256("pending", nil),
		ActiveSHA256:       activationMigrationStateSHA256("active", nil),
		OperationSetSHA256: operationSet,
	})
	deps.ExpectedPromotion = func(context.Context) (string, string, error) {
		calls = append(calls, "promotion-state")
		return operationSet, operationSet, nil
	}
	deps.ObserveGatewayPromotionHead = func(context.Context) (ActivationGatewayHeadObservation, error) {
		calls = append(calls, "observe-gateway-promotion-head")
		return emptyHead, nil
	}
	deps.PromoteGateway = func(context.Context) (ActivationGatewayPromotionObservation, error) {
		calls = append(calls, "promote")
		return ActivationGatewayPromotionObservation{Before: emptyHead, After: emptyHead}, nil
	}
	deps.VerifyGatewayPromotion = func(context.Context) (ActivationGatewayHeadObservation, error) {
		calls = append(calls, "verify-gateway-promotion")
		return emptyHead, nil
	}

	result, err := ExecuteActivation(context.Background(), input, deps)
	if err != nil {
		t.Fatalf("empty Gateway promotion: %v", err)
	}
	if result.Phase != PhaseActivated || attempt == nil || attempt.GatewayPromotion == nil ||
		!reflect.DeepEqual(*attempt.GatewayPromotion, (ActivationGatewayPromotionObservation{Before: emptyHead, After: emptyHead})) {
		t.Fatalf("result=%+v attempt=%+v", result, attempt)
	}
}

func TestExecuteActivationSourceFenceIdentityFailureStopsBeforeTargetSideEffects(t *testing.T) {
	for _, drift := range []string{"source user", "source database", "missing writer"} {
		t.Run(drift, func(t *testing.T) {
			input := testActivationInput(t)
			var attempt *ActivationAttempt
			var calls []string
			deps := testActivationDependencies(&attempt, &calls)
			deps.VerifySourceFence = func(context.Context) error {
				calls = append(calls, "source-fence:"+drift)
				return errors.New("postcondition mismatch")
			}
			_, err := ExecuteActivation(context.Background(), input, deps)
			if err == nil || !strings.Contains(err.Error(), "source fence verification") {
				t.Fatalf("source fence error=%v", err)
			}
			want := []string{"promotion-state", "prior-state", "source-fence:" + drift}
			if !reflect.DeepEqual(calls, want) {
				t.Fatalf("source drift advanced into target side effects: calls=%v want=%v", calls, want)
			}
		})
	}
}

func TestExecuteActivationFreshMeasuresRunningPostGrantDMLBeforePublication(t *testing.T) {
	input := testActivationInput(t)
	var attempt *ActivationAttempt
	var calls []string
	deps := testActivationDependencies(&attempt, &calls)
	running := ActivationRunningPostGrantDMLObservation{PlatformPassed: true, GatewayPassed: true, ProbeSHA256: strings.Repeat("f", 64)}
	deps.VerifyRunningPostGrantDML = func(context.Context) (ActivationRunningPostGrantDMLObservation, error) {
		calls = append(calls, "verify-running-post-grant-dml")
		return running, nil
	}
	var measured ActivationRunningPostGrantDMLObservation
	observe := deps.ObserveActivation
	deps.ObserveActivation = func(ctx context.Context, input ActivationInput, value ActivationAttempt, inputSHA string, facts ActivationVerificationFacts) (ActivationObservations, error) {
		calls = append(calls, "observe")
		measured = facts.RunningPostGrantDML
		return observe(ctx, input, value, inputSHA, facts)
	}
	published := false
	complete := deps.CompleteVerifiedAttempt
	deps.CompleteVerifiedAttempt = func(ctx context.Context, input ActivationInput, value ActivationAttempt, inputSHA string) (ExecutionResult, error) {
		calls = append(calls, "complete")
		published = true
		return complete(ctx, input, value, inputSHA)
	}

	result, err := ExecuteActivation(context.Background(), input, deps)
	if err != nil {
		t.Fatalf("fresh activation: %v", err)
	}
	if result.Phase != PhaseActivated || !published || !reflect.DeepEqual(measured, running) {
		t.Fatalf("result=%+v published=%t measured=%+v running=%+v", result, published, measured, running)
	}
	want := []string{"promotion-state", "prior-state", "source-fence", "pre-grant-authority", "stop", "fence-target-connect", "observe-gateway-promotion-head", "promote", "verify-gateway-promotion", "grant", "verify-authority", "connect", "verify-restored-sessions", "start", "verify", "verify-running-post-grant-dml", "observe", "complete"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=%v, want %v", calls, want)
	}
}

func TestExecuteActivationFreshRunningPostGrantDMLFailureContainsWithoutPublication(t *testing.T) {
	input := testActivationInput(t)
	var attempt *ActivationAttempt
	var calls []string
	deps := testActivationDependencies(&attempt, &calls)
	deps.VerifyRunningPostGrantDML = func(context.Context) (ActivationRunningPostGrantDMLObservation, error) {
		calls = append(calls, "verify-running-post-grant-dml")
		return ActivationRunningPostGrantDMLObservation{}, errors.New("running matrix failed")
	}
	published := false
	deps.CompleteVerifiedAttempt = func(context.Context, ActivationInput, ActivationAttempt, string) (ExecutionResult, error) {
		published = true
		return ExecutionResult{}, nil
	}

	if _, err := ExecuteActivation(context.Background(), input, deps); err == nil || !strings.Contains(err.Error(), "running post-grant DML") {
		t.Fatalf("running DML error=%v", err)
	}
	if published || !containsString(calls, "contain") || attempt == nil || attempt.Boundary != ActivationServicesStarted || attempt.Observations != nil {
		t.Fatalf("published=%t attempt=%+v calls=%v", published, attempt, calls)
	}
}

func TestExecuteActivationVerifiedReplayContainsCurrentFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*ActivationDependencies)
		want string
	}{
		{name: "promotion snapshot", want: "promotion snapshot", edit: func(deps *ActivationDependencies) {
			deps.ExpectedPromotion = func(context.Context) (string, string, error) {
				return "", "", errors.New("promotion snapshot drift")
			}
		}},
		{name: "running DML", want: "running post-grant DML", edit: func(deps *ActivationDependencies) {
			deps.VerifyRunningPostGrantDML = func(context.Context) (ActivationRunningPostGrantDMLObservation, error) {
				return ActivationRunningPostGrantDMLObservation{}, errors.New("running DML drift")
			}
		}},
		{name: "running stack", want: "replay verification", edit: func(deps *ActivationDependencies) {
			deps.VerifyActivation = func(context.Context) error { return errors.New("supervisor status or readiness failed") }
		}},
		{name: "observation", want: "replay observations", edit: func(deps *ActivationDependencies) {
			deps.ObserveActivation = func(context.Context, ActivationInput, ActivationAttempt, string, ActivationVerificationFacts) (ActivationObservations, error) {
				return ActivationObservations{}, errors.New("observation drift")
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := testActivationInput(t)
			var attempt *ActivationAttempt
			var calls []string
			deps := testActivationDependencies(&attempt, &calls)
			if _, err := ExecuteActivation(context.Background(), input, deps); err != nil {
				t.Fatalf("first activation: %v", err)
			}
			deps.ContainPostGrantFailure = func(context.Context) error {
				calls = append(calls, "contain")
				return nil
			}
			tc.edit(&deps)
			calls = nil
			if _, err := ExecuteActivation(context.Background(), input, deps); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("verified replay error=%v", err)
			}
			if !containsString(calls, "contain") {
				t.Fatalf("verified replay failure was not contained: %v", calls)
			}
		})
	}
}

func TestExecuteActivationManifestActivatedEarlyFailuresContain(t *testing.T) {
	tests := []struct {
		name string
		edit func(*ActivationDependencies)
		want string
	}{
		{name: "attempt load", want: "load activation attempt", edit: func(deps *ActivationDependencies) {
			deps.LoadAttempt = func(context.Context) (*ActivationAttempt, error) { return nil, errors.New("attempt load failed") }
		}},
		{name: "promotion plan", want: "promotion snapshot", edit: func(deps *ActivationDependencies) {
			deps.ExpectedPromotion = func(context.Context) (string, string, error) { return "", "", errors.New("promotion plan failed") }
		}},
		{name: "prior plan", want: "prior state", edit: func(deps *ActivationDependencies) {
			deps.VerifyPriorState = func(context.Context, ActivationPriorVerificationPlan, ActivationAttempt) (ActivationPriorVerificationObservation, error) {
				return ActivationPriorVerificationObservation{}, errors.New("prior plan failed")
			}
		}},
		{name: "prior evidence", want: "prior state", edit: func(deps *ActivationDependencies) {
			deps.VerifyPriorState = func(context.Context, ActivationPriorVerificationPlan, ActivationAttempt) (ActivationPriorVerificationObservation, error) {
				return ActivationPriorVerificationObservation{}, errors.New("prior evidence failed")
			}
		}},
		{name: "prior current verification", want: "prior state", edit: func(deps *ActivationDependencies) {
			deps.VerifyPriorState = func(context.Context, ActivationPriorVerificationPlan, ActivationAttempt) (ActivationPriorVerificationObservation, error) {
				return ActivationPriorVerificationObservation{}, errors.New("prior current verification failed")
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			input := testActivationInput(t)
			input.ManifestActivated = true
			var attempt *ActivationAttempt
			var calls []string
			deps := testActivationDependencies(&attempt, &calls)
			deps.ContainPostGrantFailure = func(context.Context) error {
				calls = append(calls, "stop", "fence")
				return errors.Join(errors.New("stop failed"), errors.New("fence failed"))
			}
			tc.edit(&deps)
			if _, err := ExecuteActivation(context.Background(), input, deps); err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "stop failed") || !strings.Contains(err.Error(), "fence failed") {
				t.Fatalf("early failure error=%v", err)
			}
			if !reflect.DeepEqual(calls[len(calls)-2:], []string{"stop", "fence"}) {
				t.Fatalf("early containment calls=%v", calls)
			}
		})
	}
}

func TestExecuteActivationLockedContainsActivatedManifestBindingFailure(t *testing.T) {
	var calls []string
	deps := ActivationDependencies{ContainPostGrantFailure: func(context.Context) error {
		calls = append(calls, "stop", "fence")
		return errors.Join(errors.New("stop exploded"), errors.New("fence exploded"))
	}}
	request := ExecutionRequest{ManifestPath: filepath.Join(t.TempDir(), "manifest.json"), EvidenceDir: t.TempDir()}
	manifest := Manifest{Workflow: Workflow{Phase: PhaseActivated}}
	if _, err := executeActivationLocked(context.Background(), request, ExecuteOptions{Activation: &deps}, manifest); err == nil || !strings.Contains(err.Error(), "missing its verified attempt") || !strings.Contains(err.Error(), "stop exploded") || !strings.Contains(err.Error(), "fence exploded") {
		t.Fatalf("activated binding error=%v", err)
	}
	if !reflect.DeepEqual(calls, []string{"stop", "fence"}) {
		t.Fatalf("activated binding containment calls=%v", calls)
	}
}

func TestExecuteActivationPreGrantAuthorityFailureHasNoSideEffects(t *testing.T) {
	input := testActivationInput(t)
	var attempt *ActivationAttempt
	var calls []string
	deps := testActivationDependencies(&attempt, &calls)
	deps.VerifyPreGrantAuthority = func(context.Context, bool) (ActivationPreGrantObservation, error) {
		calls = append(calls, "pre-grant-authority")
		return ActivationPreGrantObservation{}, errors.New("partial writer membership")
	}
	if _, err := ExecuteActivation(context.Background(), input, deps); err == nil || !strings.Contains(err.Error(), "pre-grant authority") {
		t.Fatalf("error=%v", err)
	}
	if attempt != nil {
		t.Fatalf("pre-grant failure persisted an activation attempt: %+v", attempt)
	}
	for _, forbidden := range []string{"stop", "promote", "grant"} {
		if containsString(calls, forbidden) {
			t.Fatalf("side effect %q ran after pre-grant failure: %v", forbidden, calls)
		}
	}
}

func TestExecuteActivationRejectsTamperedMeasuredPreGrantAuthorityIdentity(t *testing.T) {
	input := testActivationInput(t)
	var attempt *ActivationAttempt
	var calls []string
	deps := testActivationDependencies(&attempt, &calls)
	baseVerify := deps.VerifyPreGrantAuthority
	deps.VerifyPreGrantAuthority = func(ctx context.Context, resuming bool) (ActivationPreGrantObservation, error) {
		observation, err := baseVerify(ctx, resuming)
		observation.Platform.MatrixSHA256 = strings.Repeat("d", 64)
		return observation, err
	}
	if _, err := ExecuteActivation(context.Background(), input, deps); err == nil || !strings.Contains(err.Error(), "pre-grant authority") {
		t.Fatalf("tampered pre-grant authority error=%v", err)
	}
	if containsString(calls, "stop") {
		t.Fatalf("tampered pre-grant authority caused side effects: %v", calls)
	}
}

func TestActivationCarriesTypedPriorVerificationPlan(t *testing.T) {
	inputType := reflect.TypeOf(ActivationInput{})
	if _, ok := inputType.FieldByName("PriorVerification"); !ok {
		t.Fatal("ActivationInput is missing the typed prior verification plan")
	}
	dependencyType := reflect.TypeOf(ActivationDependencies{})
	if _, ok := dependencyType.FieldByName("VerifyPriorState"); !ok {
		t.Fatal("ActivationDependencies is missing the held-lock prior-state verifier")
	}
}

func TestBuildActivationInputUsesConnectedMigratorTuplesAndSeparateRuntimeRoles(t *testing.T) {
	dir := t.TempDir()
	request := canonicalActivationExecutionRequest(filepath.Join(dir, "manifest.json"), filepath.Join(dir, "evidence"))
	plan := testActivationInput(t).PriorVerification
	input := buildActivationInput(Manifest{MigrationID: "G010"}, request, request.JournalPath, plan, false)
	wantIdentities := []DatabaseIdentity{
		{Service: request.SourceService, Database: request.SourceDatabase, Role: request.SourceRole},
		{Service: request.TargetService, Database: request.TargetDatabase, Role: request.TargetRole},
		{Service: request.GatewayService, Database: request.GatewayDatabase, Role: request.GatewayRole},
		{Service: request.MaintenanceService, Database: request.MaintenanceDatabase, Role: request.MaintenanceRole},
	}
	if got := []DatabaseIdentity{input.Source, input.Platform, input.Gateway, input.Maintenance}; !reflect.DeepEqual(got, wantIdentities) {
		t.Fatalf("connected activation identities=%+v, want %+v", got, wantIdentities)
	}
	if input.PlatformRuntimeRole != request.TargetRuntimeRole || input.GatewayRuntimeRole != request.GatewayRuntimeRole ||
		input.Platform.Role == input.PlatformRuntimeRole || input.Gateway.Role == input.GatewayRuntimeRole {
		t.Fatalf("runtime identities are not separate from connected tuples: %+v", input)
	}
	if err := validateActivationInput(input); err != nil {
		t.Fatalf("canonical activation input: %v", err)
	}
	baselineSHA, err := activationInputSHA(input)
	if err != nil {
		t.Fatal(err)
	}
	changedRuntime := input
	changedRuntime.PlatformRuntimeRole = "other_platform_test"
	changedSHA, err := activationInputSHA(changedRuntime)
	if err != nil || changedSHA == baselineSHA {
		t.Fatalf("runtime role is not input-digest bound: baseline=%s changed=%s error=%v", baselineSHA, changedSHA, err)
	}
	for _, tc := range []struct {
		name string
		edit func(*ActivationInput)
	}{
		{name: "platform service/runtime mix", edit: func(value *ActivationInput) { value.Platform.Role = value.PlatformRuntimeRole }},
		{name: "Gateway service/runtime mix", edit: func(value *ActivationInput) { value.Gateway.Role = value.GatewayRuntimeRole }},
		{name: "crossed target service", edit: func(value *ActivationInput) { value.Platform.Service = value.Gateway.Service }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mixed := input
			tc.edit(&mixed)
			if err := validateActivationInput(mixed); err == nil || !strings.Contains(err.Error(), "canonical Test") {
				t.Fatalf("mixed connected identity error=%v input=%+v", err, mixed)
			}
		})
	}
}

func TestActivationMeasurementDependenciesReturnTypedFacts(t *testing.T) {
	if _, ok := reflect.TypeOf(ActivationAttempt{}).FieldByName("PreGrantObservation"); !ok {
		t.Fatal("ActivationAttempt is missing the durable pre-grant observation")
	}
	if _, ok := reflect.TypeOf(ActivationAttempt{}).FieldByName("PriorVerification"); !ok {
		t.Fatal("ActivationAttempt is missing the durable prior verification observation")
	}
	dependencyType := reflect.TypeOf(ActivationDependencies{})
	for _, fieldName := range []string{"RecoverPriorState", "VerifyPreGrantAuthority", "FenceTargetConnect", "ObserveGatewayPromotionHead", "VerifyGatewayPromotion", "VerifyPostGrantAuthority", "VerifyRestoredTargetSessions", "VerifyRunningPostGrantDML"} {
		field, ok := dependencyType.FieldByName(fieldName)
		if !ok || field.Type.Kind() != reflect.Func || field.Type.NumOut() != 2 {
			t.Fatalf("%s must return a typed observation and error", fieldName)
		}
	}
	observe, ok := dependencyType.FieldByName("ObserveActivation")
	if !ok || observe.Type.NumIn() != 5 {
		t.Fatal("ObserveActivation must consume measured verification facts")
	}
}

func TestExecuteActivationRequiresContainmentDependency(t *testing.T) {
	input := testActivationInput(t)
	var attempt *ActivationAttempt
	var calls []string
	deps := testActivationDependencies(&attempt, &calls)
	deps.ContainPostGrantFailure = nil
	if _, err := ExecuteActivation(context.Background(), input, deps); err == nil || !strings.Contains(err.Error(), "dependencies are incomplete") {
		t.Fatalf("missing containment dependency error=%v", err)
	}
	if attempt != nil || len(calls) != 0 {
		t.Fatalf("missing containment dependency caused work: attempt=%+v calls=%v", attempt, calls)
	}
}

func TestExecuteActivationRejectsPriorArtifactDriftBeforeAttemptPersistence(t *testing.T) {
	input := testActivationInput(t)
	var attempt *ActivationAttempt
	var calls []string
	saves := 0
	deps := testActivationDependencies(&attempt, &calls)
	baseSave := deps.SaveAttempt
	deps.SaveAttempt = func(ctx context.Context, value ActivationAttempt) error {
		saves++
		return baseSave(ctx, value)
	}
	deps.VerifyPriorState = func(context.Context, ActivationPriorVerificationPlan, ActivationAttempt) (ActivationPriorVerificationObservation, error) {
		calls = append(calls, "prior-state")
		return ActivationPriorVerificationObservation{}, errors.New("final delta artifact drift")
	}
	if _, err := ExecuteActivation(context.Background(), input, deps); err == nil || !strings.Contains(err.Error(), "prior state") {
		t.Fatalf("error=%v", err)
	}
	if saves != 0 || containsString(calls, "stop") || containsString(calls, "promote") || containsString(calls, "grant") {
		t.Fatalf("prior drift allowed side effects: saves=%d calls=%v", saves, calls)
	}
}

func TestExecuteActivationPersistsPriorVerificationBeforeFirstSideEffect(t *testing.T) {
	input := testActivationInput(t)
	var attempt *ActivationAttempt
	var calls []string
	deps := testActivationDependencies(&attempt, &calls)
	persistedBeforeStop := false
	baseSave := deps.SaveAttempt
	deps.SaveAttempt = func(ctx context.Context, value ActivationAttempt) error {
		if value.Boundary == ActivationBegun {
			if value.PriorVerification == nil || value.PreGrantObservation == nil {
				t.Fatalf("begun attempt persisted without prior facts: %+v", value)
			}
			persistedBeforeStop = true
		}
		return baseSave(ctx, value)
	}
	deps.StopServices = func(context.Context) error {
		if !persistedBeforeStop {
			t.Fatal("service stop ran before prior verification persistence")
		}
		calls = append(calls, "stop")
		return errors.New("stop failed")
	}
	if _, err := ExecuteActivation(context.Background(), input, deps); err == nil || !strings.Contains(err.Error(), "service stop") {
		t.Fatalf("service stop error=%v", err)
	}
	if attempt == nil || attempt.PriorVerification == nil || attempt.PriorVerification.AttemptID != attempt.ID {
		t.Fatalf("durable prior verification=%+v attempt=%+v", attempt.PriorVerification, attempt)
	}
}

func TestExecuteActivationResumeReusesPersistedPriorVerification(t *testing.T) {
	input := testActivationInput(t)
	var attempt *ActivationAttempt
	var calls []string
	deps := testActivationDependencies(&attempt, &calls)
	stopCalls := 0
	deps.StopServices = func(context.Context) error {
		calls = append(calls, "stop")
		stopCalls++
		if stopCalls == 1 {
			return errors.New("pause after durable prior verification")
		}
		return nil
	}
	if _, err := ExecuteActivation(context.Background(), input, deps); err == nil || !strings.Contains(err.Error(), "service stop") {
		t.Fatalf("first activation error=%v", err)
	}
	if attempt == nil || attempt.PriorVerification == nil {
		t.Fatalf("first activation did not persist prior verification: %+v", attempt)
	}
	priorCalls := 0
	deps.VerifyPriorState = func(context.Context, ActivationPriorVerificationPlan, ActivationAttempt) (ActivationPriorVerificationObservation, error) {
		priorCalls++
		return ActivationPriorVerificationObservation{}, errors.New("persisted prior verification was rerun")
	}
	if _, err := ExecuteActivation(context.Background(), input, deps); err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	if priorCalls != 0 {
		t.Fatalf("resume reran prior probes %d times", priorCalls)
	}
}

func TestExecuteActivationRecoversOrphanInitialBegunJournal(t *testing.T) {
	input := testActivationInput(t)
	var attempt *ActivationAttempt
	var calls []string
	deps := testActivationDependencies(&attempt, &calls)
	baseSave := deps.SaveAttempt
	saveCalls := 0
	deps.SaveAttempt = func(ctx context.Context, value ActivationAttempt) error {
		saveCalls++
		if saveCalls == 1 {
			return errors.New("injected initial attempt save failure")
		}
		return baseSave(ctx, value)
	}
	priorCalls := 0
	baseVerifyPrior := deps.VerifyPriorState
	deps.VerifyPriorState = func(ctx context.Context, plan ActivationPriorVerificationPlan, attempt ActivationAttempt) (ActivationPriorVerificationObservation, error) {
		priorCalls++
		if priorCalls > 1 {
			return ActivationPriorVerificationObservation{}, errors.New("orphan recovery reran current probes")
		}
		return baseVerifyPrior(ctx, plan, attempt)
	}
	recoverCalls := 0
	baseRecover := deps.RecoverPriorState
	deps.RecoverPriorState = func(ctx context.Context, plan ActivationPriorVerificationPlan, attempt ActivationAttempt) (ActivationPriorVerificationObservation, error) {
		recoverCalls++
		return baseRecover(ctx, plan, attempt)
	}
	if _, err := ExecuteActivation(context.Background(), input, deps); err == nil || !strings.Contains(err.Error(), "initial attempt save failure") {
		t.Fatalf("injected persistence error=%v", err)
	}
	if attempt != nil {
		t.Fatalf("authoritative attempt exists despite failed first save: %+v", attempt)
	}
	data, err := os.ReadFile(input.JournalPath)
	if err != nil {
		t.Fatal(err)
	}
	var orphan ActivationAttempt
	if err := json.Unmarshal(data, &orphan); err != nil {
		t.Fatal(err)
	}
	if orphan.Boundary != ActivationBegun || orphan.PriorVerification == nil || orphan.PreGrantObservation == nil || orphan.JournalSHA256 != "" {
		t.Fatalf("orphan initial journal=%+v", orphan)
	}
	result, err := ExecuteActivation(context.Background(), input, deps)
	if err != nil {
		t.Fatalf("retry after orphan initial journal: %v", err)
	}
	if result.Phase != PhaseActivated || attempt == nil || attempt.ID != orphan.ID || priorCalls != 1 || recoverCalls != 1 {
		t.Fatalf("result=%+v attempt=%+v orphan=%+v prior calls=%d recover calls=%d", result, attempt, orphan, priorCalls, recoverCalls)
	}
}

func TestExecuteActivationRecoversBegunGatewayBeforeHeadJournalEnrichment(t *testing.T) {
	input := testActivationInput(t)
	var attempt *ActivationAttempt
	var calls []string
	deps := testActivationDependencies(&attempt, &calls)
	baseSave := deps.SaveAttempt
	injected := false
	var retrySaves []ActivationAttempt
	deps.SaveAttempt = func(ctx context.Context, value ActivationAttempt) error {
		if value.Boundary == ActivationBegun && value.GatewayBeforeHead != nil && !injected {
			injected = true
			return errors.New("injected before-head attempt save failure")
		}
		if injected {
			retrySaves = append(retrySaves, value)
		}
		return baseSave(ctx, value)
	}
	if _, err := ExecuteActivation(context.Background(), input, deps); err == nil || !strings.Contains(err.Error(), "before-head attempt save failure") {
		t.Fatalf("injected persistence error=%v", err)
	}
	if attempt == nil || attempt.Boundary != ActivationBegun || attempt.GatewayBeforeHead != nil || attempt.GatewayBeforeSHA256 != "" {
		t.Fatalf("authoritative attempt contains failed before-head save: %+v", attempt)
	}
	retrySaves = nil
	result, err := ExecuteActivation(context.Background(), input, deps)
	if err != nil {
		t.Fatalf("retry after before-head journal enrichment: %v", err)
	}
	if result.Phase != PhaseActivated || len(retrySaves) == 0 || retrySaves[0].Boundary != ActivationGatewayPromoted || retrySaves[0].GatewayBeforeHead == nil {
		t.Fatalf("result=%+v retry saves=%+v", result, retrySaves)
	}
}

func TestExecuteActivationRejectsTamperedPersistedPriorVerification(t *testing.T) {
	input := testActivationInput(t)
	var attempt *ActivationAttempt
	var calls []string
	deps := testActivationDependencies(&attempt, &calls)
	deps.StopServices = func(context.Context) error {
		calls = append(calls, "stop")
		return errors.New("pause after durable prior verification")
	}
	if _, err := ExecuteActivation(context.Background(), input, deps); err == nil {
		t.Fatal("first activation unexpectedly succeeded")
	}
	if attempt == nil || attempt.PriorVerification == nil {
		t.Fatalf("attempt=%+v", attempt)
	}
	attempt.PriorVerification.ArtifactSetSHA256 = strings.Repeat("f", 64)
	calls = nil
	if _, err := ExecuteActivation(context.Background(), input, deps); err == nil || !strings.Contains(err.Error(), "journal verification") {
		t.Fatalf("tampered prior verification error=%v", err)
	}
	if len(calls) != 1 || calls[0] != "promotion-state" {
		t.Fatalf("tampered prior verification caused side effects: %v", calls)
	}
}

func TestExecuteActivationLegacyResumeRebuildsPriorAfterCurrentStateWitnesses(t *testing.T) {
	input := testActivationInput(t)
	var attempt *ActivationAttempt
	var calls []string
	deps := testActivationDependencies(&attempt, &calls)
	deps.VerifyActivation = func(context.Context) error {
		calls = append(calls, "verify")
		return errors.New("pause after services started")
	}
	if _, err := ExecuteActivation(context.Background(), input, deps); err == nil || !strings.Contains(err.Error(), "verification") {
		t.Fatalf("prepare legacy attempt error=%v", err)
	}
	if attempt == nil || attempt.Boundary != ActivationServicesStarted || attempt.PriorVerification == nil {
		t.Fatalf("prepared attempt=%+v", attempt)
	}
	attempt.PriorVerification = nil
	if err := persistActivationBoundary(context.Background(), input.JournalPath, deps.SaveAttempt, attempt); err != nil {
		t.Fatal(err)
	}
	deps.VerifyPriorState = func(context.Context, ActivationPriorVerificationPlan, ActivationAttempt) (ActivationPriorVerificationObservation, error) {
		return ActivationPriorVerificationObservation{}, errors.New("legacy recovery reran current probes")
	}
	deps.VerifyActivation = func(context.Context) error { calls = append(calls, "verify"); return nil }
	calls = nil
	if _, err := ExecuteActivation(context.Background(), input, deps); err != nil {
		t.Fatalf("legacy recovery failed: %v", err)
	}
	if attempt.PriorVerification == nil {
		t.Fatalf("legacy recovery did not persist prior verification: %+v", attempt)
	}
	wantOrder := []string{"promotion-state", "stop", "fence-target-connect", "source-fence", "verify-gateway-promotion", "pre-grant-authority", "verify-authority", "recover-prior-state"}
	if len(calls) < len(wantOrder) || !reflect.DeepEqual(calls[:len(wantOrder)], wantOrder) {
		t.Fatalf("legacy recovery calls=%v, want prefix %v", calls, wantOrder)
	}
}

func TestExecuteActivationRecoversLegacyPriorJournalWriteBeforeAttemptSave(t *testing.T) {
	input := testActivationInput(t)
	var attempt *ActivationAttempt
	var calls []string
	deps := testActivationDependencies(&attempt, &calls)
	deps.VerifyActivation = func(context.Context) error {
		calls = append(calls, "verify")
		return errors.New("pause after services started")
	}
	if _, err := ExecuteActivation(context.Background(), input, deps); err == nil || !strings.Contains(err.Error(), "verification") {
		t.Fatalf("prepare legacy attempt error=%v", err)
	}
	if attempt == nil || attempt.Boundary != ActivationServicesStarted || attempt.PriorVerification == nil {
		t.Fatalf("prepared attempt=%+v", attempt)
	}
	attempt.PriorVerification = nil
	if err := persistActivationBoundary(context.Background(), input.JournalPath, deps.SaveAttempt, attempt); err != nil {
		t.Fatal(err)
	}

	baseSave := deps.SaveAttempt
	injected := false
	deps.SaveAttempt = func(ctx context.Context, value ActivationAttempt) error {
		if value.Boundary == ActivationServicesStarted && value.PriorVerification != nil && !injected {
			injected = true
			return errors.New("injected legacy prior attempt save failure")
		}
		return baseSave(ctx, value)
	}
	deps.VerifyPriorState = func(context.Context, ActivationPriorVerificationPlan, ActivationAttempt) (ActivationPriorVerificationObservation, error) {
		return ActivationPriorVerificationObservation{}, errors.New("legacy recovery reran current probes")
	}
	recoverCalls := 0
	baseRecover := deps.RecoverPriorState
	deps.RecoverPriorState = func(ctx context.Context, plan ActivationPriorVerificationPlan, attempt ActivationAttempt) (ActivationPriorVerificationObservation, error) {
		recoverCalls++
		return baseRecover(ctx, plan, attempt)
	}
	if _, err := ExecuteActivation(context.Background(), input, deps); err == nil || !strings.Contains(err.Error(), "legacy prior attempt save failure") {
		t.Fatalf("injected persistence error=%v", err)
	}
	if attempt.PriorVerification != nil || attempt.Boundary != ActivationServicesStarted {
		t.Fatalf("authoritative attempt advanced despite failed save: %+v", attempt)
	}
	deps.VerifyActivation = func(context.Context) error { calls = append(calls, "verify"); return nil }
	result, err := ExecuteActivation(context.Background(), input, deps)
	if err != nil {
		t.Fatalf("retry after same-boundary journal write: %v", err)
	}
	if result.Phase != PhaseActivated || attempt.PriorVerification == nil || recoverCalls != 2 {
		t.Fatalf("result=%+v attempt=%+v recover calls=%d", result, attempt, recoverCalls)
	}
}

func TestExecuteActivationRejectsChangedInputAndTamperedJournal(t *testing.T) {
	input := testActivationInput(t)
	var attempt *ActivationAttempt
	var calls []string
	deps := testActivationDependencies(&attempt, &calls)
	if _, err := ExecuteActivation(context.Background(), input, deps); err != nil {
		t.Fatal(err)
	}
	changed := input
	changed.ManifestPath = filepath.Join(filepath.Dir(input.ManifestPath), "changed-manifest.json")
	if _, err := ExecuteActivation(context.Background(), changed, deps); err == nil || !strings.Contains(err.Error(), "input changed") {
		t.Fatalf("changed input error=%v", err)
	}
	if err := os.WriteFile(input.JournalPath, []byte("tampered\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ExecuteActivation(context.Background(), input, deps); err == nil || !strings.Contains(err.Error(), "journal changed") {
		t.Fatalf("tampered journal error=%v", err)
	}
}

func TestExecuteActivationRecoversWhenJournalRenamePrecedesManifestSave(t *testing.T) {
	input := testActivationInput(t)
	var attempt *ActivationAttempt
	var calls []string
	deps := testActivationDependencies(&attempt, &calls)
	save := deps.SaveAttempt
	injected := false
	deps.SaveAttempt = func(ctx context.Context, value ActivationAttempt) error {
		if value.Boundary == ActivationGatewayPromoted && !injected {
			injected = true
			return errors.New("injected manifest save failure")
		}
		return save(ctx, value)
	}
	if _, err := ExecuteActivation(context.Background(), input, deps); err == nil || !strings.Contains(err.Error(), "manifest save failure") {
		t.Fatalf("first activation error=%v", err)
	}
	if attempt == nil || attempt.Boundary != ActivationBegun {
		t.Fatalf("manifest attempt advanced despite failed save: %+v", attempt)
	}
	result, err := ExecuteActivation(context.Background(), input, deps)
	if err != nil {
		t.Fatalf("retry after journal-ahead crash: %v", err)
	}
	if result.Phase != PhaseActivated || attempt == nil || attempt.Boundary != ActivationVerified {
		t.Fatalf("retry result=%+v attempt=%+v", result, attempt)
	}
}

func TestExecuteActivationRejectsJournalAheadHistoricalWitnessChanges(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ActivationAttempt)
	}{
		{
			name: "pre-grant observation",
			mutate: func(journal *ActivationAttempt) {
				journal.PreGrantObservation.DMLProbeSHA256 = strings.Repeat("f", 64)
			},
		},
		{
			name: "Gateway before-head",
			mutate: func(journal *ActivationAttempt) {
				changed := journal.GatewayPromotion.Before
				changed.PendingSHA256 = strings.Repeat("f", 64)
				changed.HeadSHA256 = activationGatewayHeadSHA256(changed)
				journal.GatewayBeforeHead = &changed
				journal.GatewayBeforeSHA256 = changed.HeadSHA256
				journal.GatewayPromotion.Before = changed
			},
		},
		{
			name: "non-boundary evidence",
			mutate: func(journal *ActivationAttempt) {
				journal.EvidencePath = filepath.Join(filepath.Dir(journal.EvidencePath), "unexpected-evidence.json")
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			input := testActivationInput(t)
			var attempt *ActivationAttempt
			var calls []string
			deps := testActivationDependencies(&attempt, &calls)
			baseSave := deps.SaveAttempt
			injected := false
			deps.SaveAttempt = func(ctx context.Context, value ActivationAttempt) error {
				if value.Boundary == ActivationGatewayPromoted && !injected {
					injected = true
					return errors.New("injected promoted attempt save failure")
				}
				return baseSave(ctx, value)
			}
			if _, err := ExecuteActivation(context.Background(), input, deps); err == nil || !strings.Contains(err.Error(), "promoted attempt save failure") {
				t.Fatalf("injected persistence error=%v", err)
			}
			data, err := os.ReadFile(input.JournalPath)
			if err != nil {
				t.Fatal(err)
			}
			var journal ActivationAttempt
			if err := json.Unmarshal(data, &journal); err != nil {
				t.Fatal(err)
			}
			tc.mutate(&journal)
			data, err = json.MarshalIndent(journal, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(input.JournalPath, append(data, '\n'), 0o600); err != nil {
				t.Fatal(err)
			}
			calls = nil
			if _, err := ExecuteActivation(context.Background(), input, deps); err == nil || !strings.Contains(err.Error(), "journal verification") {
				t.Fatalf("tampered journal-ahead error=%v", err)
			}
			if len(calls) != 1 || calls[0] != "promotion-state" {
				t.Fatalf("tampered journal caused side effects: %v", calls)
			}
		})
	}
}

func TestExecuteActivationLegacyBegunRecoversFromGatewayPromotedJournalAhead(t *testing.T) {
	input := testActivationInput(t)
	var attempt *ActivationAttempt
	var calls []string
	deps := testActivationDependencies(&attempt, &calls)
	baseSave := deps.SaveAttempt
	injected := false
	deps.SaveAttempt = func(ctx context.Context, value ActivationAttempt) error {
		if value.Boundary == ActivationGatewayPromoted && !injected {
			injected = true
			return errors.New("injected promoted manifest save failure")
		}
		return baseSave(ctx, value)
	}
	if _, err := ExecuteActivation(context.Background(), input, deps); err == nil || !strings.Contains(err.Error(), "manifest save failure") {
		t.Fatalf("first activation error=%v", err)
	}
	if attempt == nil || attempt.Boundary != ActivationBegun || attempt.GatewayBeforeHead == nil {
		t.Fatalf("manifest attempt=%+v", attempt)
	}
	journalData, err := os.ReadFile(input.JournalPath)
	if err != nil {
		t.Fatal(err)
	}
	var ahead ActivationAttempt
	if err := json.Unmarshal(journalData, &ahead); err != nil {
		t.Fatal(err)
	}
	if ahead.Boundary != ActivationGatewayPromoted || ahead.GatewayPromotion == nil {
		t.Fatalf("journal-ahead attempt=%+v", ahead)
	}
	ahead.PriorVerification = nil
	legacyJournal, err := json.MarshalIndent(ahead, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(input.JournalPath, append(legacyJournal, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	attempt.PriorVerification = nil
	deps.VerifyPriorState = func(context.Context, ActivationPriorVerificationPlan, ActivationAttempt) (ActivationPriorVerificationObservation, error) {
		return ActivationPriorVerificationObservation{}, errors.New("legacy journal-ahead recovery reran current probes")
	}
	calls = nil
	result, err := ExecuteActivation(context.Background(), input, deps)
	if err != nil {
		t.Fatalf("legacy journal-ahead recovery: %v", err)
	}
	if result.Phase != PhaseActivated || attempt.PriorVerification == nil || !containsString(calls, "recover-prior-state") || containsString(calls, "prior-state") {
		t.Fatalf("result=%+v attempt=%+v calls=%v", result, attempt, calls)
	}
}

func TestExecuteActivationContainsPostGrantFailure(t *testing.T) {
	input := testActivationInput(t)
	var attempt *ActivationAttempt
	var calls []string
	deps := testActivationDependencies(&attempt, &calls)
	deps.RestoreConnect = func(context.Context) error { calls = append(calls, "connect"); return errors.New("connect denied") }
	deps.ContainPostGrantFailure = func(context.Context) error { calls = append(calls, "contain"); return nil }
	if _, err := ExecuteActivation(context.Background(), input, deps); err == nil || !strings.Contains(err.Error(), "CONNECT restore failed") {
		t.Fatalf("error=%v", err)
	}
	if attempt == nil || attempt.Boundary != ActivationWritersGranted || calls[len(calls)-1] != "contain" {
		t.Fatalf("attempt=%+v calls=%v", attempt, calls)
	}
}

func TestExecuteActivationDoesNotStartWithRestoredTargetSession(t *testing.T) {
	input := testActivationInput(t)
	var attempt *ActivationAttempt
	var calls []string
	deps := testActivationDependencies(&attempt, &calls)
	deps.VerifyRestoredTargetSessions = func(context.Context) (ActivationRestoredTargetSessionsObservation, error) {
		calls = append(calls, "verify-restored-sessions")
		return ActivationRestoredTargetSessionsObservation{SessionsZero: false, ProbeSHA256: strings.Repeat("d", 64)}, nil
	}
	if _, err := ExecuteActivation(context.Background(), input, deps); err == nil || !strings.Contains(err.Error(), "restored target session verification") {
		t.Fatalf("restored target session error=%v", err)
	}
	if !containsString(calls, "connect") || containsString(calls, "start") {
		t.Fatalf("restored target session calls=%v", calls)
	}
}

func TestExecuteActivationRecoversExternalStateAfterContainedServicesStartedFailure(t *testing.T) {
	input := testActivationInput(t)
	var attempt *ActivationAttempt
	var calls []string
	deps := testActivationDependencies(&attempt, &calls)
	verificationCalls := 0
	deps.VerifyActivation = func(context.Context) error {
		calls = append(calls, "verify")
		verificationCalls++
		if verificationCalls == 1 {
			return errors.New("readiness failed")
		}
		return nil
	}
	deps.ContainPostGrantFailure = func(context.Context) error {
		calls = append(calls, "contain")
		return nil
	}
	if _, err := ExecuteActivation(context.Background(), input, deps); err == nil || !strings.Contains(err.Error(), "verification failed") {
		t.Fatalf("first error=%v", err)
	}
	if attempt == nil || attempt.Boundary != ActivationServicesStarted || calls[len(calls)-1] != "contain" {
		t.Fatalf("attempt=%+v calls=%v", attempt, calls)
	}
	if attempt.PreGrantObservation == nil || !validActivationPreGrantObservation(*attempt.PreGrantObservation, false) {
		t.Fatalf("attempt did not preserve the historical pre-grant facts: %+v", attempt)
	}
	calls = nil
	result, err := ExecuteActivation(context.Background(), input, deps)
	if err != nil {
		t.Fatalf("contained retry: %v", err)
	}
	if result.Phase != PhaseActivated {
		t.Fatalf("retry result=%+v", result)
	}
	if attempt.Observations == nil || attempt.Observations.PreGrantPlatformAuthority.WriterMembership || attempt.Observations.PreGrantGatewayAuthority.WriterMembership {
		t.Fatalf("retry replaced measured pre-grant facts: %+v", attempt.Observations)
	}
	recordedPreGrant := ActivationPreGrantObservation{
		Platform:       attempt.Observations.PreGrantPlatformAuthority,
		Gateway:        attempt.Observations.PreGrantGatewayAuthority,
		DMLProbeSHA256: attempt.Observations.PreGrantDMLProbeSHA256,
	}
	if !reflect.DeepEqual(*attempt.PreGrantObservation, recordedPreGrant) {
		t.Fatalf("final evidence changed the historical pre-grant facts: stored=%+v recorded=%+v", attempt.PreGrantObservation, recordedPreGrant)
	}
	want := []string{"promotion-state", "recover-prior-state", "source-fence", "pre-grant-authority", "stop", "fence-target-connect", "verify-gateway-promotion", "verify-authority", "connect", "verify-restored-sessions", "start", "verify", "verify-running-post-grant-dml"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("retry calls=%v, want %v", calls, want)
	}
}

func TestExecuteActivationRejectsGatewayHeadDriftWithoutRePromotion(t *testing.T) {
	input := testActivationInput(t)
	var attempt *ActivationAttempt
	var calls []string
	deps := testActivationDependencies(&attempt, &calls)
	deps.VerifyActivation = func(context.Context) error {
		calls = append(calls, "verify")
		return errors.New("readiness failed")
	}
	if _, err := ExecuteActivation(context.Background(), input, deps); err == nil || !strings.Contains(err.Error(), "verification failed") {
		t.Fatalf("first error=%v", err)
	}
	if attempt == nil || attempt.GatewayPromotion == nil || attempt.Boundary != ActivationServicesStarted {
		t.Fatalf("attempt=%+v", attempt)
	}
	drifted := attempt.GatewayPromotion.After
	drifted.ActiveSHA256 = strings.Repeat("f", 64)
	drifted.HeadSHA256 = activationGatewayHeadSHA256(drifted)
	deps.VerifyGatewayPromotion = func(context.Context) (ActivationGatewayHeadObservation, error) {
		calls = append(calls, "verify-gateway-promotion")
		return drifted, nil
	}
	calls = nil
	if _, err := ExecuteActivation(context.Background(), input, deps); err == nil || !strings.Contains(err.Error(), "durable Gateway head changed") {
		t.Fatalf("Gateway head drift error=%v", err)
	}
	if containsString(calls, "promote") || !containsString(calls, "stop") || !containsString(calls, "fence-target-connect") || !containsString(calls, "contain") {
		t.Fatalf("Gateway head drift was re-promoted or not safely contained: %v", calls)
	}
}

func TestActivationMigrationStateSHA256MatchesGatewayStoreContract(t *testing.T) {
	items := []storepkg.MigrationPromotionItem{{AccountID: "account-a", OperationID: "operation-a"}}
	encoded, err := json.Marshal(struct {
		State string                            `json:"state"`
		Items []storepkg.MigrationPromotionItem `json:"items"`
	}{State: "pending", Items: items})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := activationMigrationStateSHA256("pending", items), sha256Hex(encoded); got != want {
		t.Fatalf("activation state digest=%s, Gateway store contract=%s", got, want)
	}
}

func TestExecuteActivationPreservesOriginalGatewayBeforeHeadAcrossPromotionCommitCrash(t *testing.T) {
	input := testActivationInput(t)
	var attempt *ActivationAttempt
	var calls []string
	deps := testActivationDependencies(&attempt, &calls)
	operationSet := strings.Repeat("a", 64)
	before := activationGatewayHeadObservation(storepkg.MigrationPromotionDigest{PendingSHA256: strings.Repeat("b", 64), ActiveSHA256: strings.Repeat("c", 64), OperationSetSHA256: operationSet})
	after := activationGatewayHeadObservation(storepkg.MigrationPromotionDigest{PendingSHA256: activationMigrationStateSHA256("pending", nil), ActiveSHA256: strings.Repeat("e", 64), OperationSetSHA256: operationSet})
	current := before
	promotionCalls := 0
	deps.ObserveGatewayPromotionHead = func(context.Context) (ActivationGatewayHeadObservation, error) {
		calls = append(calls, "observe-gateway-promotion-head")
		return current, nil
	}
	deps.PromoteGateway = func(context.Context) (ActivationGatewayPromotionObservation, error) {
		calls = append(calls, "promote")
		promotionCalls++
		if promotionCalls == 1 {
			current = after
			return ActivationGatewayPromotionObservation{}, errors.New("promotion response lost after commit")
		}
		return ActivationGatewayPromotionObservation{Before: current, After: current}, nil
	}
	deps.VerifyGatewayPromotion = func(context.Context) (ActivationGatewayHeadObservation, error) {
		calls = append(calls, "verify-gateway-promotion")
		return current, nil
	}

	if _, err := ExecuteActivation(context.Background(), input, deps); err == nil || !strings.Contains(err.Error(), "promotion response lost") {
		t.Fatalf("first activation error=%v", err)
	}
	if attempt == nil || attempt.Boundary != ActivationBegun || attempt.GatewayBeforeHead == nil || !reflect.DeepEqual(*attempt.GatewayBeforeHead, before) || attempt.GatewayPromotion != nil {
		t.Fatalf("attempt after promotion crash=%+v", attempt)
	}

	calls = nil
	result, err := ExecuteActivation(context.Background(), input, deps)
	if err != nil {
		t.Fatalf("promotion commit recovery: %v", err)
	}
	if result.Phase != PhaseActivated || promotionCalls != 2 || attempt == nil || attempt.GatewayPromotion == nil {
		t.Fatalf("result=%+v promotionCalls=%d attempt=%+v calls=%v", result, promotionCalls, attempt, calls)
	}
	wantPromotion := ActivationGatewayPromotionObservation{Before: before, After: after}
	if !reflect.DeepEqual(*attempt.GatewayPromotion, wantPromotion) || attempt.GatewayBeforeSHA256 != before.HeadSHA256 || attempt.GatewayAfterSHA256 != after.HeadSHA256 {
		t.Fatalf("promotion evidence=%+v want=%+v beforeSHA=%s afterSHA=%s", attempt.GatewayPromotion, wantPromotion, attempt.GatewayBeforeSHA256, attempt.GatewayAfterSHA256)
	}
}

func TestExecuteActivationLegacyBegunRecoversCommittedPromotionBeforeJournal(t *testing.T) {
	input := testActivationInput(t)
	var attempt *ActivationAttempt
	var calls []string
	deps := testActivationDependencies(&attempt, &calls)
	operationSet := strings.Repeat("a", 64)
	before := activationGatewayHeadObservation(storepkg.MigrationPromotionDigest{PendingSHA256: strings.Repeat("b", 64), ActiveSHA256: strings.Repeat("c", 64), OperationSetSHA256: operationSet})
	after := activationGatewayHeadObservation(storepkg.MigrationPromotionDigest{PendingSHA256: activationMigrationStateSHA256("pending", nil), ActiveSHA256: strings.Repeat("e", 64), OperationSetSHA256: operationSet})
	current := before
	promotionCalls := 0
	deps.ObserveGatewayPromotionHead = func(context.Context) (ActivationGatewayHeadObservation, error) { return current, nil }
	deps.PromoteGateway = func(context.Context) (ActivationGatewayPromotionObservation, error) {
		promotionCalls++
		if promotionCalls == 1 {
			current = after
			return ActivationGatewayPromotionObservation{}, errors.New("promotion response lost after commit")
		}
		return ActivationGatewayPromotionObservation{Before: current, After: current}, nil
	}
	deps.VerifyGatewayPromotion = func(context.Context) (ActivationGatewayHeadObservation, error) { return current, nil }
	if _, err := ExecuteActivation(context.Background(), input, deps); err == nil || !strings.Contains(err.Error(), "promotion response lost") {
		t.Fatalf("first activation error=%v", err)
	}
	if attempt == nil || attempt.Boundary != ActivationBegun || attempt.GatewayBeforeHead == nil || attempt.GatewayPromotion != nil || !reflect.DeepEqual(current, after) {
		t.Fatalf("attempt=%+v current=%+v", attempt, current)
	}
	attempt.PriorVerification = nil
	if err := persistActivationBoundary(context.Background(), input.JournalPath, deps.SaveAttempt, attempt); err != nil {
		t.Fatal(err)
	}
	deps.VerifyPriorState = func(context.Context, ActivationPriorVerificationPlan, ActivationAttempt) (ActivationPriorVerificationObservation, error) {
		return ActivationPriorVerificationObservation{}, errors.New("legacy committed-promotion recovery reran current probes")
	}
	calls = nil
	result, err := ExecuteActivation(context.Background(), input, deps)
	if err != nil {
		t.Fatalf("legacy committed-promotion recovery: %v", err)
	}
	if result.Phase != PhaseActivated || promotionCalls != 2 || attempt.PriorVerification == nil || attempt.GatewayPromotion == nil || !containsString(calls, "recover-prior-state") {
		t.Fatalf("result=%+v promotionCalls=%d attempt=%+v calls=%v", result, promotionCalls, attempt, calls)
	}
}

func TestExecuteActivationRejectsMissingOrTamperedGatewayBeforeHeadAfterPromotionCommit(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*ActivationAttempt)
	}{
		{name: "missing", edit: func(attempt *ActivationAttempt) { attempt.GatewayBeforeHead = nil }},
		{name: "tampered", edit: func(attempt *ActivationAttempt) {
			attempt.GatewayBeforeHead.ActiveSHA256 = strings.Repeat("f", 64)
			attempt.GatewayBeforeHead.HeadSHA256 = activationGatewayHeadSHA256(*attempt.GatewayBeforeHead)
			attempt.GatewayBeforeSHA256 = attempt.GatewayBeforeHead.HeadSHA256
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := testActivationInput(t)
			var attempt *ActivationAttempt
			var calls []string
			deps := testActivationDependencies(&attempt, &calls)
			operationSet := strings.Repeat("a", 64)
			before := activationGatewayHeadObservation(storepkg.MigrationPromotionDigest{PendingSHA256: strings.Repeat("b", 64), ActiveSHA256: strings.Repeat("c", 64), OperationSetSHA256: operationSet})
			after := activationGatewayHeadObservation(storepkg.MigrationPromotionDigest{PendingSHA256: activationMigrationStateSHA256("pending", nil), ActiveSHA256: strings.Repeat("e", 64), OperationSetSHA256: operationSet})
			current := before
			promotionCalls := 0
			deps.ObserveGatewayPromotionHead = func(context.Context) (ActivationGatewayHeadObservation, error) { return current, nil }
			deps.PromoteGateway = func(context.Context) (ActivationGatewayPromotionObservation, error) {
				promotionCalls++
				current = after
				return ActivationGatewayPromotionObservation{}, errors.New("promotion response lost after commit")
			}
			deps.VerifyGatewayPromotion = func(context.Context) (ActivationGatewayHeadObservation, error) { return current, nil }
			if _, err := ExecuteActivation(context.Background(), input, deps); err == nil {
				t.Fatal("first activation unexpectedly succeeded")
			}
			if attempt == nil || attempt.GatewayBeforeHead == nil {
				t.Fatalf("attempt=%+v", attempt)
			}
			tc.edit(attempt)
			if _, err := ExecuteActivation(context.Background(), input, deps); err == nil || !strings.Contains(err.Error(), "journal verification") {
				t.Fatalf("before-head error=%v", err)
			}
			if promotionCalls != 1 {
				t.Fatalf("invalid before-head retried promotion: calls=%d", promotionCalls)
			}
		})
	}
}

func TestExecuteActivationRejectsTamperedHistoricalPreGrantObservationOnResume(t *testing.T) {
	input := testActivationInput(t)
	var attempt *ActivationAttempt
	var calls []string
	deps := testActivationDependencies(&attempt, &calls)
	deps.VerifyActivation = func(context.Context) error {
		calls = append(calls, "verify")
		return errors.New("readiness failed")
	}
	if _, err := ExecuteActivation(context.Background(), input, deps); err == nil || !strings.Contains(err.Error(), "verification failed") {
		t.Fatalf("first error=%v", err)
	}
	if attempt == nil || attempt.PreGrantObservation == nil {
		t.Fatalf("attempt is missing historical pre-grant facts: %+v", attempt)
	}
	attempt.PreGrantObservation.Platform.WriterMembership = true
	attempt.PreGrantObservation.Gateway.WriterMembership = true
	calls = nil
	if _, err := ExecuteActivation(context.Background(), input, deps); err == nil || !strings.Contains(err.Error(), "journal verification") {
		t.Fatalf("tampered historical pre-grant error=%v", err)
	}
	if containsString(calls, "stop") {
		t.Fatalf("tampered historical pre-grant facts caused side effects: %v", calls)
	}
}

func TestExecuteActivationBegunResumeRejectsGrantedWriterMembership(t *testing.T) {
	input := testActivationInput(t)
	var attempt *ActivationAttempt
	var calls []string
	deps := testActivationDependencies(&attempt, &calls)
	deps.StopServices = func(context.Context) error {
		calls = append(calls, "stop")
		return errors.New("stop failed")
	}
	if _, err := ExecuteActivation(context.Background(), input, deps); err == nil || !strings.Contains(err.Error(), "service stop") {
		t.Fatalf("first error=%v", err)
	}
	if attempt == nil || attempt.Boundary != ActivationBegun {
		t.Fatalf("attempt=%+v", attempt)
	}
	baseVerify := testActivationDependencies(&attempt, &calls).VerifyPreGrantAuthority
	deps.VerifyPreGrantAuthority = func(ctx context.Context, _ bool) (ActivationPreGrantObservation, error) {
		return baseVerify(ctx, true)
	}
	deps.StopServices = func(context.Context) error {
		calls = append(calls, "stop")
		return nil
	}
	calls = nil
	if _, err := ExecuteActivation(context.Background(), input, deps); err == nil || !strings.Contains(err.Error(), "pre-grant authority") {
		t.Fatalf("begun granted membership error=%v", err)
	}
	if containsString(calls, "stop") || containsString(calls, "promote") || containsString(calls, "verify-gateway-promotion") {
		t.Fatalf("begun granted membership caused side effects or promotion repair: %v", calls)
	}
}

func TestExecuteActivationRecoversCommittedWriterGrantBeforeBoundarySave(t *testing.T) {
	input := testActivationInput(t)
	var attempt *ActivationAttempt
	var calls []string
	deps := testActivationDependencies(&attempt, &calls)
	verifyPreGrant := deps.VerifyPreGrantAuthority
	grantCommitted := false
	grantCalls := 0
	deps.VerifyPreGrantAuthority = func(ctx context.Context, _ bool) (ActivationPreGrantObservation, error) {
		return verifyPreGrant(ctx, grantCommitted)
	}
	deps.GrantWriters = func(context.Context) error {
		calls = append(calls, "grant")
		grantCalls++
		grantCommitted = true
		return nil
	}
	save := deps.SaveAttempt
	injected := false
	writersGrantedSaves := 0
	deps.SaveAttempt = func(ctx context.Context, value ActivationAttempt) error {
		if value.Boundary == ActivationWritersGranted {
			if !injected {
				injected = true
				return errors.New("injected writer boundary save failure")
			}
			writersGrantedSaves++
		}
		return save(ctx, value)
	}

	if _, err := ExecuteActivation(context.Background(), input, deps); err == nil || !strings.Contains(err.Error(), "writer boundary persistence") {
		t.Fatalf("first activation error=%v", err)
	}
	if attempt == nil || attempt.Boundary != ActivationGatewayPromoted || !grantCommitted || grantCalls != 1 {
		t.Fatalf("attempt=%+v grantCommitted=%t grantCalls=%d calls=%v", attempt, grantCommitted, grantCalls, calls)
	}

	calls = nil
	result, err := ExecuteActivation(context.Background(), input, deps)
	if err != nil {
		t.Fatalf("grant commit recovery: %v", err)
	}
	if result.Phase != PhaseActivated || grantCalls != 1 || writersGrantedSaves != 1 || containsString(calls, "grant") {
		t.Fatalf("result=%+v grantCalls=%d writersGrantedSaves=%d calls=%v", result, grantCalls, writersGrantedSaves, calls)
	}
	wantPrefix := "promotion-state,recover-prior-state,source-fence,pre-grant-authority,stop,fence-target-connect,verify-gateway-promotion,verify-authority"
	if !strings.HasPrefix(strings.Join(calls, ","), wantPrefix) {
		t.Fatalf("recovery did not stop, fence, and verify durable state before ratchet: %v", calls)
	}
}

func TestExecuteActivationGrantCommitRecoveryDoesNotVerifyOrRatchetBeforeTargetFence(t *testing.T) {
	input := testActivationInput(t)
	var attempt *ActivationAttempt
	var calls []string
	deps := testActivationDependencies(&attempt, &calls)
	deps.GrantWriters = func(context.Context) error { return errors.New("pause before writer grant") }
	if _, err := ExecuteActivation(context.Background(), input, deps); err == nil || !strings.Contains(err.Error(), "writer grant") {
		t.Fatalf("prepare Gateway-promoted attempt error=%v", err)
	}
	if attempt == nil || attempt.Boundary != ActivationGatewayPromoted {
		t.Fatalf("prepared attempt=%+v", attempt)
	}
	verifyPreGrant := deps.VerifyPreGrantAuthority
	deps.VerifyPreGrantAuthority = func(ctx context.Context, _ bool) (ActivationPreGrantObservation, error) {
		return verifyPreGrant(ctx, true)
	}
	postGrantCalls := 0
	verifyPostGrant := deps.VerifyPostGrantAuthority
	deps.VerifyPostGrantAuthority = func(ctx context.Context) (ActivationPostGrantObservation, error) {
		postGrantCalls++
		return verifyPostGrant(ctx)
	}
	deps.FenceTargetConnect = func(context.Context) (ActivationTargetFenceObservation, error) {
		calls = append(calls, "fence-target-connect")
		return ActivationTargetFenceObservation{}, errors.New("target sessions remain")
	}
	save := deps.SaveAttempt
	writersGrantedSaves := 0
	deps.SaveAttempt = func(ctx context.Context, value ActivationAttempt) error {
		if value.Boundary == ActivationWritersGranted {
			writersGrantedSaves++
		}
		return save(ctx, value)
	}

	calls = nil
	if _, err := ExecuteActivation(context.Background(), input, deps); err == nil || !strings.Contains(err.Error(), "target CONNECT fence") {
		t.Fatalf("target fence error=%v", err)
	}
	if postGrantCalls != 0 || writersGrantedSaves != 0 || attempt.Boundary != ActivationGatewayPromoted {
		t.Fatalf("postGrantCalls=%d writersGrantedSaves=%d attempt=%+v calls=%v", postGrantCalls, writersGrantedSaves, attempt, calls)
	}
}

func TestExecuteActivationRejectsUnprovenCommittedWriterGrantRecovery(t *testing.T) {
	tests := []struct {
		name        string
		want        string
		wantContain bool
	}{
		{name: "missing historical facts", want: "journal verification"},
		{name: "partial membership", want: "pre-grant authority"},
		{name: "promotion drift", want: "durable Gateway head changed", wantContain: true},
		{name: "incomplete post authority", want: "post-grant authority", wantContain: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			input := testActivationInput(t)
			var attempt *ActivationAttempt
			var calls []string
			deps := testActivationDependencies(&attempt, &calls)
			deps.GrantWriters = func(context.Context) error {
				calls = append(calls, "grant")
				return errors.New("pause before writer grant")
			}
			if _, err := ExecuteActivation(context.Background(), input, deps); err == nil || !strings.Contains(err.Error(), "writer grant") {
				t.Fatalf("prepare Gateway-promoted attempt error=%v", err)
			}
			if attempt == nil || attempt.Boundary != ActivationGatewayPromoted || attempt.PreGrantObservation == nil || attempt.GatewayPromotion == nil {
				t.Fatalf("prepared attempt=%+v", attempt)
			}

			verifyPreGrant := deps.VerifyPreGrantAuthority
			deps.VerifyPreGrantAuthority = func(ctx context.Context, _ bool) (ActivationPreGrantObservation, error) {
				return verifyPreGrant(ctx, true)
			}
			switch tc.name {
			case "missing historical facts":
				attempt.PreGrantObservation = nil
			case "partial membership":
				deps.VerifyPreGrantAuthority = func(ctx context.Context, _ bool) (ActivationPreGrantObservation, error) {
					observation, err := verifyPreGrant(ctx, true)
					observation.Gateway.WriterMembership = false
					return observation, err
				}
			case "promotion drift":
				drifted := attempt.GatewayPromotion.After
				drifted.ActiveSHA256 = strings.Repeat("f", 64)
				drifted.HeadSHA256 = activationGatewayHeadSHA256(drifted)
				deps.VerifyGatewayPromotion = func(context.Context) (ActivationGatewayHeadObservation, error) {
					calls = append(calls, "verify-gateway-promotion")
					return drifted, nil
				}
			case "incomplete post authority":
				verifyPostGrant := deps.VerifyPostGrantAuthority
				deps.VerifyPostGrantAuthority = func(ctx context.Context) (ActivationPostGrantObservation, error) {
					observation, err := verifyPostGrant(ctx)
					observation.TargetSessionsZero = false
					return observation, err
				}
			}
			save := deps.SaveAttempt
			writersGrantedSaves := 0
			deps.SaveAttempt = func(ctx context.Context, value ActivationAttempt) error {
				if value.Boundary == ActivationWritersGranted {
					writersGrantedSaves++
				}
				return save(ctx, value)
			}

			calls = nil
			if _, err := ExecuteActivation(context.Background(), input, deps); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("recovery error=%v", err)
			}
			if writersGrantedSaves != 0 || containsString(calls, "grant") || containsString(calls, "start") {
				t.Fatalf("unproven recovery advanced: saves=%d calls=%v", writersGrantedSaves, calls)
			}
			if containsString(calls, "contain") != tc.wantContain {
				t.Fatalf("containment=%t want=%t calls=%v", containsString(calls, "contain"), tc.wantContain, calls)
			}
			if tc.name == "incomplete post authority" && !strings.Contains(strings.Join(calls, ","), "stop,fence-target-connect,verify-gateway-promotion,verify-authority") {
				t.Fatalf("post-authority failure ran before stop/fence/durable promotion verification: %v", calls)
			}
		})
	}
}

func TestDefaultActivationSupervisorPreservesPostgres(t *testing.T) {
	t.Setenv("BT_GATEWAY_STARTUP_PROFILE", "migration-validation")
	t.Setenv("BT_PLATFORM_STARTUP_PROFILE", "migration-validation")
	t.Setenv("BT_FUNDING_STARTUP_PROFILE", "migration-validation")
	stateDir := t.TempDir()
	for path, value := range map[string]string{filepath.Join(stateDir, "pids", "postgres.pid"): "123\n", filepath.Join(stateDir, "ownership", "postgres.start"): "987654\n"} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var commands []Command
	options := ExecuteOptions{Run: func(_ context.Context, command Command) (CommandResult, error) {
		commands = append(commands, command)
		script := string(command.Stdin)
		if strings.Contains(script, "pg_postmaster_start_time") {
			return CommandResult{Output: []byte("2026-08-10 00:00:00+00\n")}, nil
		}
		if strings.Contains(script, "pg_terminate_backend") {
			return CommandResult{Output: []byte("true\n")}, nil
		}
		if strings.Contains(script, "pg_has_role") {
			return CommandResult{Output: []byte("true\n")}, nil
		}
		return CommandResult{}, nil
	}}
	request := ExecutionRequest{ManifestPath: filepath.Join("repo", "migration", "manifest.json"), StateDir: stateDir, MaintenanceService: "maintenance_test", MaintenanceDatabase: "maintenance_test", TargetDatabase: "platform_test", TargetRole: "platform_test_migrator", TargetRuntimeRole: "platform_test_runtime", GatewayDatabase: "gateway_test", GatewayRole: "gateway_test_migrator", GatewayRuntimeRole: "gateway_test_runtime"}
	deps, err := defaultActivationDependencies(context.Background(), request, options)
	if err != nil {
		t.Fatal(err)
	}
	if err := deps.StopServices(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := deps.StartServices(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := deps.VerifyActivation(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := deps.ContainPostGrantFailure(context.Background()); err != nil {
		t.Fatal(err)
	}
	var args []string
	for _, command := range commands {
		args = append(args, command.Args...)
	}
	joined := strings.Join(args, " ")
	if strings.Contains(joined, " down") || strings.Contains(joined, " up") {
		t.Fatalf("activation may not stop PostgreSQL: %s", joined)
	}
	if strings.Count(joined, "stop-app") != 2 || !strings.Contains(joined, "start-app") {
		t.Fatalf("supervisor boundaries=%s", joined)
	}
	validationStop, normalStop, startNormal, waitNormal := false, false, false, false
	for _, command := range commands {
		if command.Name != "env" {
			continue
		}
		arguments := strings.Join(command.Args, " ")
		profilesNormal := strings.Contains(arguments, "BT_GATEWAY_STARTUP_PROFILE=normal") && strings.Contains(arguments, "BT_PLATFORM_STARTUP_PROFILE=normal") && strings.Contains(arguments, "BT_FUNDING_STARTUP_PROFILE=normal")
		profilesValidation := strings.Contains(arguments, "BT_GATEWAY_STARTUP_PROFILE=migration-validation") && strings.Contains(arguments, "BT_PLATFORM_STARTUP_PROFILE=migration-validation") && strings.Contains(arguments, "BT_FUNDING_STARTUP_PROFILE=migration-validation")
		validationStop = validationStop || profilesValidation && strings.HasSuffix(arguments, " stop-app")
		normalStop = normalStop || profilesNormal && strings.HasSuffix(arguments, " stop-app")
		startNormal = startNormal || profilesNormal && strings.HasSuffix(arguments, " start-app")
		waitNormal = waitNormal || profilesNormal && strings.HasSuffix(arguments, " wait")
	}
	if !validationStop || !normalStop || !startNormal || !waitNormal {
		t.Fatalf("activation did not force validation pre-grant stop and normal post-grant supervision: %+v", commands)
	}
	for _, command := range commands {
		if strings.Contains(string(command.Stdin), "pg_read_file") || strings.Contains(string(command.Stdin), "pg_read_server_files") {
			t.Fatalf("activation requests forbidden server-file access: %s", command.Stdin)
		}
	}
}

func TestDefaultActivationPsqlCommandsUseMachineReadableOutput(t *testing.T) {
	stateDir := t.TempDir()
	for path, value := range map[string]string{filepath.Join(stateDir, "pids", "postgres.pid"): "123\n", filepath.Join(stateDir, "ownership", "postgres.start"): "987654\n"} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var commands []Command
	deps, err := defaultActivationDependencies(context.Background(), ExecutionRequest{
		ManifestPath: filepath.Join("repo", "migration", "manifest.json"), StateDir: stateDir,
		SourceService: "source_test", SourceDatabase: "source_test", SourceWriterRole: "source_test_writer",
		MaintenanceService: "maintenance_test", MaintenanceDatabase: "maintenance_test",
		TargetDatabase: "platform_test", TargetRuntimeRole: "platform_test_runtime", TargetWriterRole: "platform_test_writer",
		GatewayDatabase: "gateway_test", GatewayRuntimeRole: "gateway_test_runtime", GatewayWriterRole: "gateway_test_writer",
	}, ExecuteOptions{Run: func(_ context.Context, command Command) (CommandResult, error) {
		commands = append(commands, command)
		script := string(command.Stdin)
		switch {
		case strings.Contains(script, "pg_postmaster_start_time"):
			return CommandResult{Output: []byte("2026-08-10 00:00:00+00\n")}, nil
		case strings.Contains(script, "GRANT :\"platform_writer\""):
			return CommandResult{}, nil
		default:
			return CommandResult{Output: []byte("true\n")}, nil
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := deps.VerifySourceFence(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := deps.FenceTargetConnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []func(context.Context) error{deps.GrantWriters, deps.RestoreConnect, deps.ContainPostGrantFailure, deps.VerifyActivation} {
		if err := operation(context.Background()); err != nil {
			t.Fatal(err)
		}
	}

	psqlCommands := 0
	for _, command := range commands {
		if command.Name != "psql" {
			continue
		}
		psqlCommands++
		hasArgument := func(want string) bool {
			for _, argument := range command.Args {
				if argument == want {
					return true
				}
			}
			return false
		}
		for _, required := range []string{"--quiet", "--tuples-only", "--no-align"} {
			if !hasArgument(required) {
				t.Errorf("psql command missing %s: %v", required, command.Args)
			}
		}
		onErrorStop := false
		for index := 0; index+1 < len(command.Args); index++ {
			if command.Args[index] == "--set" && command.Args[index+1] == "ON_ERROR_STOP=1" {
				onErrorStop = true
				break
			}
		}
		if !onErrorStop {
			t.Errorf("psql command missing --set ON_ERROR_STOP=1: %v", command.Args)
		}
	}
	if psqlCommands == 0 {
		t.Fatal("default activation emitted no psql commands")
	}
}

func TestDefaultActivationSourceFenceBindsExactConnectionAndWriterState(t *testing.T) {
	request := ExecutionRequest{
		SourceService: "source_service", SourceDatabase: "source_database", SourceRole: "source_migrator", SourceWriterRole: "source_writer",
	}
	var commands []Command
	deps, err := defaultActivationDependencies(context.Background(), request, ExecuteOptions{Run: func(_ context.Context, command Command) (CommandResult, error) {
		commands = append(commands, command)
		return CommandResult{Output: []byte("false\n")}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := deps.VerifySourceFence(context.Background()); err == nil || !strings.Contains(err.Error(), "postcondition mismatch") {
		t.Fatalf("false source identity/fence result error=%v", err)
	}
	if len(commands) != 1 || commands[0].Name != "psql" {
		t.Fatalf("source fence emitted side effects: %+v", commands)
	}
	command := commands[0]
	hasSetVariable := func(value string) bool {
		for index := 0; index+1 < len(command.Args); index++ {
			if command.Args[index] == "--set" && command.Args[index+1] == value {
				return true
			}
		}
		return false
	}
	for _, variable := range []string{"source_role=" + request.SourceRole, "source_database=" + request.SourceDatabase, "writer_role=" + request.SourceWriterRole} {
		if !hasSetVariable(variable) {
			t.Errorf("source fence is missing --set %s: %v", variable, command.Args)
		}
	}
	script := string(command.Stdin)
	for _, required := range []string{
		"current_user=:'source_role'",
		"current_database()=:'source_database'",
		"EXISTS (SELECT 1 FROM pg_roles writer",
		"writer.rolname=:'writer_role'",
		"NOT writer.rolcanlogin",
		"NOT EXISTS (SELECT 1 FROM pg_stat_activity activity",
		"activity.usename=:'writer_role'",
		"activity.pid<>pg_backend_pid()",
	} {
		if !strings.Contains(script, required) {
			t.Errorf("source fence SQL is missing %q: %s", required, script)
		}
	}
	if !strings.HasPrefix(script, "SELECT (") || !strings.HasSuffix(script, ")::text;\n") {
		t.Fatalf("source fence is not a constant single-row boolean query: %s", script)
	}
}

func TestDefaultActivationUsesRuntimeRolesForWriterAndSessionBoundaries(t *testing.T) {
	stateDir := t.TempDir()
	for path, value := range map[string]string{filepath.Join(stateDir, "pids", "postgres.pid"): "123\n", filepath.Join(stateDir, "ownership", "postgres.start"): "987654\n"} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var commands []Command
	request := ExecutionRequest{
		ManifestPath: filepath.Join("repo", "migration", "manifest.json"), StateDir: stateDir,
		MaintenanceService: "maintenance_test", MaintenanceDatabase: "maintenance_test",
		TargetDatabase: "platform_test", TargetRole: "platform_test_migrator", TargetRuntimeRole: "platform_test_runtime", TargetWriterRole: "platform_test_writer",
		GatewayDatabase: "gateway_test", GatewayRole: "gateway_test_migrator", GatewayRuntimeRole: "gateway_test_runtime", GatewayWriterRole: "gateway_test_writer",
	}
	priorAuthorityVerifier := verifyActivationAuthorityCatalogs
	authorityVerified := false
	verifyActivationAuthorityCatalogs = func(_ context.Context, got ExecutionRequest) ([2]ActivationAuthorityObservation, error) {
		authorityVerified = true
		if !reflect.DeepEqual(got, request) {
			t.Fatalf("authority request=%+v, want %+v", got, request)
		}
		complete := ActivationAuthorityObservation{RuntimeInherits: true, WriterMembership: true, FullCapabilities: true, EffectiveTablePrivileges: true, EffectiveSequencePrivileges: true, EffectiveRoutinePrivileges: true}
		return [2]ActivationAuthorityObservation{complete, complete}, nil
	}
	t.Cleanup(func() { verifyActivationAuthorityCatalogs = priorAuthorityVerifier })
	deps, err := defaultActivationDependencies(context.Background(), request, ExecuteOptions{Run: func(_ context.Context, command Command) (CommandResult, error) {
		commands = append(commands, command)
		script := string(command.Stdin)
		if strings.Contains(script, "pg_postmaster_start_time") {
			return CommandResult{Output: []byte("2026-08-10 00:00:00+00\n")}, nil
		}
		if strings.Contains(script, "'USAGE')::text ||") {
			return CommandResult{Output: []byte("false|false\n")}, nil
		}
		if strings.Contains(script, "DELETE FROM public.") {
			return CommandResult{Output: []byte("true\n")}, nil
		}
		if strings.Contains(script, "post_grant_target_sessions") {
			return CommandResult{Output: []byte("true\n")}, nil
		}
		if strings.Contains(script, "pg_has_role") || strings.Contains(script, "pg_terminate_backend") || strings.Contains(script, "has_database_privilege") {
			return CommandResult{Output: []byte("true\n")}, nil
		}
		return CommandResult{}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := deps.FenceTargetConnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := deps.GrantWriters(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := deps.VerifyPostGrantAuthority(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := deps.VerifyRunningPostGrantDML(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []func(context.Context) error{deps.RestoreConnect, deps.ContainPostGrantFailure, deps.VerifyActivation} {
		if err := operation(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	joined := ""
	for _, command := range commands {
		joined += "\n" + strings.Join(command.Args, " ") + "\n" + string(command.Stdin)
	}
	for _, runtimeRole := range []string{"platform_test_runtime", "gateway_test_runtime"} {
		if !strings.Contains(joined, runtimeRole) {
			t.Fatalf("activation did not use runtime role %q: %s", runtimeRole, joined)
		}
	}
	for _, command := range commands {
		if !strings.Contains(string(command.Stdin), "activation_dml_") {
			continue
		}
		probe := strings.Join(command.Args, " ") + "\n" + string(command.Stdin)
		for _, migratorRole := range []string{"platform_test_migrator", "gateway_test_migrator"} {
			if strings.Contains(probe, migratorRole) {
				t.Fatalf("runtime DML probe used migrator role %q: %s", migratorRole, probe)
			}
		}
	}
	if !strings.Contains(joined, "pg_has_role") || !strings.Contains(joined, "pg_stat_activity") {
		t.Fatalf("activation readiness did not verify runtime membership and sessions: %s", joined)
	}
	if !authorityVerified {
		t.Fatal("activation did not invoke complete post-grant authority verification")
	}
	for _, required := range []string{
		"REVOKE CONNECT ON DATABASE",
		"pg_terminate_backend", "pid<>pg_backend_pid()", "RAISE EXCEPTION", "NOT EXISTS",
		"BEGIN;", "COMMIT;", "GRANT CONNECT ON DATABASE", "has_database_privilege",
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("activation SQL is missing %q: %s", required, joined)
		}
	}
	if strings.Contains(joined, "pg_advisory_") || strings.Contains(joined, "G010:test:activation") {
		t.Fatalf("activation step attempted to reacquire the operation-wide lock: %s", joined)
	}
}

func TestDefaultActivationStepsDoNotReacquireOperationWidePostgresLock(t *testing.T) {
	stateDir := t.TempDir()
	for path, value := range map[string]string{filepath.Join(stateDir, "pids", "postgres.pid"): "123\n", filepath.Join(stateDir, "ownership", "postgres.start"): "987654\n"} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var commands []Command
	request := ExecutionRequest{
		ManifestPath: filepath.Join("repo", "migration", "manifest.json"), StateDir: stateDir,
		MaintenanceService: "maintenance_test", MaintenanceDatabase: "maintenance_test",
		TargetDatabase: "platform_test", TargetRuntimeRole: "platform_test_runtime", TargetWriterRole: "platform_test_writer",
		GatewayDatabase: "gateway_test", GatewayRuntimeRole: "gateway_test_runtime", GatewayWriterRole: "gateway_test_writer",
	}
	deps, err := defaultActivationDependencies(context.Background(), request, ExecuteOptions{Run: func(_ context.Context, command Command) (CommandResult, error) {
		commands = append(commands, command)
		if strings.Contains(string(command.Stdin), "pg_postmaster_start_time") {
			return CommandResult{Output: []byte("2026-08-10 00:00:00+00\n")}, nil
		}
		return CommandResult{Output: []byte("true\n")}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := deps.FenceTargetConnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []func(context.Context) error{deps.GrantWriters, deps.RestoreConnect, deps.ContainPostGrantFailure} {
		if err := operation(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	for _, command := range commands {
		script := string(command.Stdin)
		if strings.Contains(script, "pg_advisory_") || strings.Contains(script, "G010:test:activation") {
			t.Fatalf("nested activation lock in command: %s", script)
		}
	}
}

func TestDefaultActivationFenceAndRestoreUseCanonicalCapabilityRolesTransactionally(t *testing.T) {
	stateDir := t.TempDir()
	for path, value := range map[string]string{filepath.Join(stateDir, "pids", "postgres.pid"): "123\n", filepath.Join(stateDir, "ownership", "postgres.start"): "987654\n"} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	request := ExecutionRequest{
		ManifestPath: filepath.Join("repo", "migration", "manifest.json"), StateDir: stateDir,
		MaintenanceService: "maintenance_test", MaintenanceDatabase: "maintenance_test", MaintenanceRole: "maintenance_role",
		TargetDatabase: "platform_test", TargetRole: "platform_migrator", TargetRuntimeRole: "platform_runtime",
		GatewayDatabase: "gateway_test", GatewayRole: "gateway_migrator", GatewayRuntimeRole: "gateway_runtime",
	}
	var commands []Command
	deps, err := defaultActivationDependencies(context.Background(), request, ExecuteOptions{Run: func(_ context.Context, command Command) (CommandResult, error) {
		commands = append(commands, command)
		if strings.Contains(string(command.Stdin), "pg_postmaster_start_time") {
			return CommandResult{Output: []byte("2026-08-10 00:00:00+00\n")}, nil
		}
		return CommandResult{Output: []byte("true\n")}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := deps.FenceTargetConnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := deps.RestoreConnect(context.Background()); err != nil {
		t.Fatal(err)
	}

	var fence, restore Command
	for _, command := range commands {
		script := string(command.Stdin)
		if strings.Contains(script, "REVOKE CONNECT ON DATABASE") {
			fence = command
		}
		if strings.Contains(script, "GRANT CONNECT ON DATABASE") {
			restore = command
		}
	}
	if fence.Name != "psql" || restore.Name != "psql" {
		t.Fatalf("boundary commands fence=%+v restore=%+v", fence, restore)
	}
	hasSetVariable := func(command Command, value string) bool {
		for index := 0; index+1 < len(command.Args); index++ {
			if command.Args[index] == "--set" && command.Args[index+1] == value {
				return true
			}
		}
		return false
	}
	for _, command := range []Command{fence, restore} {
		for _, variable := range []string{
			"maintenance_role=" + request.MaintenanceRole,
			"platform_database=" + request.TargetDatabase,
			"platform_migrator=" + request.TargetRole,
			"platform_runtime=" + request.TargetRuntimeRole,
			"gateway_database=" + request.GatewayDatabase,
			"gateway_migrator=" + request.GatewayRole,
			"gateway_runtime=" + request.GatewayRuntimeRole,
		} {
			if !hasSetVariable(command, variable) {
				t.Errorf("boundary command is missing --set %s: %v", variable, command.Args)
			}
		}
	}
	assertOrder := func(name, script string, fragments []string) {
		t.Helper()
		position := -1
		for _, fragment := range fragments {
			start := position + 1
			relative := strings.Index(script[start:], fragment)
			if relative < 0 {
				t.Fatalf("%s fragment %q is missing or out of order:\n%s", name, fragment, script)
			}
			position = start + relative
		}
	}
	fenceSQL := string(fence.Stdin)
	assertOrder("fence", fenceSQL, []string{
		"BEGIN;",
		"current_user=:'maintenance_role'",
		"FROM pg_auth_members membership",
		`\gset`,
		`\if :activation_fence_roles_ok`,
		`SET LOCAL ROLE :"platform_migrator";`,
		`REVOKE CONNECT ON DATABASE :"platform_database" FROM :"platform_runtime";`,
		"RESET ROLE;",
		`SET LOCAL ROLE :"gateway_migrator";`,
		`REVOKE CONNECT ON DATABASE :"gateway_database" FROM :"gateway_runtime";`,
		"RESET ROLE;",
		`SET LOCAL ROLE "pg_signal_backend";`,
		"unowned target runtime session",
		"pg_terminate_backend",
		"unterminated target runtime session",
		"pg_stat_clear_snapshot()",
		"residual target runtime session",
		"SELECT true::text;",
		"COMMIT;",
	})
	for _, required := range []string{
		"NOT membership.admin_option", "NOT membership.inherit_option", "membership.set_option",
		`granted.rolname='pg_signal_backend'`, `\else`, "ROLLBACK;", "RAISE EXCEPTION",
		`activity.datname=:'platform_database' AND activity.usename=:'platform_runtime'`,
		`activity.datname=:'gateway_database' AND activity.usename=:'gateway_runtime'`, "5000",
	} {
		if !strings.Contains(fenceSQL, required) {
			t.Errorf("fence is missing fail-closed contract %q:\n%s", required, fenceSQL)
		}
	}
	if strings.Contains(fenceSQL, "CREATE TEMP TABLE") {
		t.Fatalf("fence leaks session targets through a temp table after role switch:\n%s", fenceSQL)
	}
	restoreSQL := string(restore.Stdin)
	assertOrder("restore", restoreSQL, []string{
		"BEGIN;",
		"current_user=:'maintenance_role'",
		`\gset`,
		`\if :activation_restore_roles_ok`,
		`SET LOCAL ROLE :"platform_migrator";`,
		`GRANT CONNECT ON DATABASE :"platform_database" TO :"platform_runtime";`,
		"RESET ROLE;",
		`SET LOCAL ROLE :"gateway_migrator";`,
		`GRANT CONNECT ON DATABASE :"gateway_database" TO :"gateway_runtime";`,
		"RESET ROLE;",
		"has_database_privilege",
		"SELECT true::text;",
		"COMMIT;",
	})
	for _, required := range []string{"NOT membership.admin_option", "NOT membership.inherit_option", "membership.set_option", `\else`, "ROLLBACK;", "RAISE EXCEPTION"} {
		if !strings.Contains(restoreSQL, required) {
			t.Errorf("restore is missing fail-closed contract %q:\n%s", required, restoreSQL)
		}
	}
}

func TestDefaultActivationContainmentAttemptsStopAndFenceAndReportsBothErrors(t *testing.T) {
	for _, tc := range []struct {
		name       string
		stopError  bool
		fenceError bool
	}{
		{name: "stop fails", stopError: true},
		{name: "fence fails", fenceError: true},
		{name: "both fail", stopError: true, fenceError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stateDir := t.TempDir()
			for path, value := range map[string]string{filepath.Join(stateDir, "pids", "postgres.pid"): "123\n", filepath.Join(stateDir, "ownership", "postgres.start"): "987654\n"} {
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			stopCalls, fenceCalls := 0, 0
			deps, err := defaultActivationDependencies(context.Background(), ExecutionRequest{
				ManifestPath: filepath.Join("repo", "migration", "manifest.json"), StateDir: stateDir,
				MaintenanceService: "maintenance_test", MaintenanceDatabase: "maintenance_test",
				TargetDatabase: "platform_test", TargetRuntimeRole: "platform_test_runtime",
				GatewayDatabase: "gateway_test", GatewayRuntimeRole: "gateway_test_runtime",
			}, ExecuteOptions{Run: func(_ context.Context, command Command) (CommandResult, error) {
				if command.Name == "env" && len(command.Args) > 0 && command.Args[len(command.Args)-1] == "stop-app" {
					arguments := strings.Join(command.Args, " ")
					if !strings.Contains(arguments, "BT_GATEWAY_STARTUP_PROFILE=normal") || !strings.Contains(arguments, "BT_PLATFORM_STARTUP_PROFILE=normal") || !strings.Contains(arguments, "BT_FUNDING_STARTUP_PROFILE=normal") {
						t.Fatalf("containment stop did not force normal profiles: %s", arguments)
					}
					stopCalls++
					if tc.stopError {
						return CommandResult{}, errors.New("stop exploded")
					}
					return CommandResult{}, nil
				}
				script := string(command.Stdin)
				if strings.Contains(script, "pg_postmaster_start_time") {
					return CommandResult{Output: []byte("2026-08-10 00:00:00+00\n")}, nil
				}
				if strings.Contains(script, "pg_terminate_backend") {
					fenceCalls++
					if tc.fenceError {
						return CommandResult{}, errors.New("fence exploded")
					}
					return CommandResult{Output: []byte("true\n")}, nil
				}
				return CommandResult{}, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			err = deps.ContainPostGrantFailure(context.Background())
			if stopCalls != 1 || fenceCalls != 1 {
				t.Fatalf("containment calls stop=%d fence=%d error=%v", stopCalls, fenceCalls, err)
			}
			if tc.stopError && (err == nil || !strings.Contains(err.Error(), "stop exploded")) {
				t.Fatalf("stop error was hidden: %v", err)
			}
			if tc.fenceError && (err == nil || !strings.Contains(err.Error(), "fence exploded")) {
				t.Fatalf("fence error was hidden: %v", err)
			}
		})
	}
}

func TestDefaultActivationProbesCompleteCanonicalDMLMatrixBeforeAndAfterGrant(t *testing.T) {
	stateDir := t.TempDir()
	for path, value := range map[string]string{filepath.Join(stateDir, "pids", "postgres.pid"): "123\n", filepath.Join(stateDir, "ownership", "postgres.start"): "987654\n"} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	request := ExecutionRequest{
		ManifestPath: filepath.Join("repo", "migration", "manifest.json"), StateDir: stateDir,
		MaintenanceService: "maintenance_test", MaintenanceDatabase: "maintenance_test", MaintenanceRole: "bolter_platform_test_admin",
		TargetDatabase: "platform_test", TargetRole: "bolter_platform_test_migrator", TargetRuntimeRole: "platform_test_runtime", TargetWriterRole: "platform_test_writer",
		GatewayDatabase: "gateway_test", GatewayRole: "bolter_gateway_test_migrator", GatewayRuntimeRole: "gateway_test_runtime", GatewayWriterRole: "gateway_test_writer",
	}
	var commands []Command
	authorityCalls := 0
	priorAuthorityVerifier := verifyActivationAuthorityCatalogs
	verifyActivationAuthorityCatalogs = func(context.Context, ExecutionRequest) ([2]ActivationAuthorityObservation, error) {
		authorityCalls++
		writer := authorityCalls > 1
		platformMatrix, gatewayMatrix := authority.PlatformV15(), authority.GatewayV6()
		return [2]ActivationAuthorityObservation{
			{MatrixSHA256: platformMatrix.Digest(), MigrationSHA256: platformMatrix.MigrationDigest, RuntimeRole: platformMatrix.Roles.Runtime, RuntimeInherits: true, WriterMembership: writer, FullCapabilities: true, EffectiveTablePrivileges: true, EffectiveSequencePrivileges: true, EffectiveRoutinePrivileges: true},
			{MatrixSHA256: gatewayMatrix.Digest(), MigrationSHA256: gatewayMatrix.MigrationDigest, RuntimeRole: gatewayMatrix.Roles.Runtime, RuntimeInherits: true, WriterMembership: writer, FullCapabilities: true, EffectiveTablePrivileges: true, EffectiveSequencePrivileges: true, EffectiveRoutinePrivileges: true},
		}, nil
	}
	t.Cleanup(func() { verifyActivationAuthorityCatalogs = priorAuthorityVerifier })
	deps, err := defaultActivationDependencies(context.Background(), request, ExecuteOptions{Run: func(_ context.Context, command Command) (CommandResult, error) {
		script := string(command.Stdin)
		commands = append(commands, command)
		switch {
		case strings.Contains(script, "pg_postmaster_start_time"):
			return CommandResult{Output: []byte("2026-08-10 00:00:00+00\n")}, nil
		case strings.Contains(script, "'USAGE')::text ||"):
			return CommandResult{Output: []byte("false|false\n")}, nil
		default:
			return CommandResult{Output: []byte("true\n")}, nil
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := deps.VerifyPreGrantAuthority(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if _, err := deps.VerifyPostGrantAuthority(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := deps.VerifyRunningPostGrantDML(context.Background()); err != nil {
		t.Fatal(err)
	}
	var scripts []string
	dmlCommands := 0
	for _, command := range commands {
		script := string(command.Stdin)
		scripts = append(scripts, script)
		if !strings.Contains(script, "activation_dml_") {
			continue
		}
		dmlCommands++
		hasArgument := func(want string) bool {
			for _, argument := range command.Args {
				if argument == want {
					return true
				}
			}
			return false
		}
		if !hasArgument("--quiet") || !hasArgument("maintenance_role="+request.MaintenanceRole) {
			t.Errorf("DML probe command is missing quiet maintenance binding: %v", command.Args)
		}
		if !hasArgument("runtime_role="+request.TargetRuntimeRole) && !hasArgument("runtime_role="+request.GatewayRuntimeRole) {
			t.Errorf("DML probe command is missing runtime binding: %v", command.Args)
		}
		if strings.Count(script, `\gset`) != 1 || strings.Count(script, "SELECT true::text;") != 1 {
			t.Errorf("DML probe does not preserve one machine-readable result: %s", script)
		}
	}
	if dmlCommands != 6 {
		t.Fatalf("DML probe commands=%d, want pre+post+running for platform and Gateway", dmlCommands)
	}
	joined := strings.Join(scripts, "\n")
	for _, matrix := range []authority.Matrix{authority.PlatformV15(), authority.GatewayV6()} {
		for _, object := range matrix.Objects {
			if object.Kind != authority.Table || object.Class != authority.Canonical {
				continue
			}
			qualified := object.Schema + "." + object.Name
			for _, operation := range []string{"INSERT INTO " + qualified, "UPDATE " + qualified, "DELETE FROM " + qualified} {
				if strings.Count(joined, operation) != 3 {
					t.Fatalf("%s probe count=%d, want pre+post+running replay in SQL:\n%s", operation, strings.Count(joined, operation), joined)
				}
			}
		}
	}
	if strings.Count(joined, "ROLLBACK;") < 4 {
		t.Fatalf("DML probes are not rollback-only: %s", joined)
	}
}

func TestPreGrantDMLProbeUsesValidPLpgSQLDeclarationOrder(t *testing.T) {
	script := activationCanonicalDMLProbe(authority.PlatformV15(), false)
	if strings.Contains(script, "BEGIN\n    DECLARE") || strings.Contains(script, "BEGIN\n  DECLARE") {
		t.Fatalf("PL/pgSQL declaration appears after BEGIN:\n%s", script)
	}
	if !strings.Contains(script, "DECLARE columns text;\nBEGIN") || !strings.Contains(script, "DECLARE column_name text;\nBEGIN") {
		t.Fatalf("DML probes do not declare variables before BEGIN:\n%s", script)
	}
}

func TestCanonicalDMLProbeChecksMaintenanceMembershipBeforeAssumingRuntime(t *testing.T) {
	script := activationCanonicalDMLProbe(authority.PlatformV15(), false)
	ordered := []string{
		"BEGIN;",
		"current_user=:'maintenance_role'",
		"FROM pg_auth_members membership",
		`\gset`,
		`\if :activation_membership_ok`,
		`GRANT :"runtime_role" TO :"maintenance_role" WITH SET TRUE;`,
		`SET LOCAL ROLE :"runtime_role";`,
		"INSERT INTO public.platform_account_routes",
		"SELECT true::text;",
		"ROLLBACK;",
	}
	position := -1
	for _, fragment := range ordered {
		next := strings.Index(script, fragment)
		if next <= position {
			t.Fatalf("DML probe fragment %q is missing or out of order:\n%s", fragment, script)
		}
		position = next
	}
}

func TestCanonicalDMLProbeFailsClosedOnMaintenanceMembershipDrift(t *testing.T) {
	script := activationCanonicalDMLProbe(authority.PlatformV15(), true)
	for _, required := range []string{
		"membership.admin_option",
		"NOT membership.inherit_option",
		"NOT membership.set_option",
		`\if :activation_membership_ok`,
		`\else`,
		"RAISE EXCEPTION 'activation maintenance runtime membership is not canonical'",
		`\endif`,
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("DML probe membership guard is missing %q:\n%s", required, script)
		}
	}
	if strings.Index(script, `\else`) > strings.Index(script, `GRANT :"runtime_role"`) {
		t.Fatalf("DML probe grants runtime SET before its fail-closed branch:\n%s", script)
	}
}

func TestDefaultActivationRejectsIncompletePostGrantAuthorityObservation(t *testing.T) {
	stateDir := t.TempDir()
	for path, value := range map[string]string{filepath.Join(stateDir, "pids", "postgres.pid"): "123\n", filepath.Join(stateDir, "ownership", "postgres.start"): "987654\n"} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	request := ExecutionRequest{ManifestPath: filepath.Join("repo", "migration", "manifest.json"), StateDir: stateDir, MaintenanceService: "maintenance_test", MaintenanceDatabase: "maintenance_test"}
	priorAuthorityVerifier := verifyActivationAuthorityCatalogs
	verifyActivationAuthorityCatalogs = func(context.Context, ExecutionRequest) ([2]ActivationAuthorityObservation, error) {
		return [2]ActivationAuthorityObservation{
			{RuntimeInherits: true, WriterMembership: true, FullCapabilities: true, EffectiveTablePrivileges: true, EffectiveSequencePrivileges: true, EffectiveRoutinePrivileges: true},
			{RuntimeInherits: true, WriterMembership: false, FullCapabilities: true, EffectiveTablePrivileges: true, EffectiveSequencePrivileges: true, EffectiveRoutinePrivileges: true},
		}, nil
	}
	t.Cleanup(func() { verifyActivationAuthorityCatalogs = priorAuthorityVerifier })
	deps, err := defaultActivationDependencies(context.Background(), request, ExecuteOptions{Run: func(_ context.Context, command Command) (CommandResult, error) {
		if strings.Contains(string(command.Stdin), "pg_postmaster_start_time") {
			return CommandResult{Output: []byte("2026-08-10 00:00:00+00\n")}, nil
		}
		return CommandResult{Output: []byte("true\n")}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := deps.VerifyPostGrantAuthority(context.Background()); err == nil || !strings.Contains(err.Error(), "post-grant authority") {
		t.Fatalf("incomplete post-grant authority error=%v", err)
	}
}

func TestDefaultActivationRejectsResidualTargetSessionsAfterWriterGrant(t *testing.T) {
	stateDir := t.TempDir()
	for path, value := range map[string]string{filepath.Join(stateDir, "pids", "postgres.pid"): "123\n", filepath.Join(stateDir, "ownership", "postgres.start"): "987654\n"} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	request := ExecutionRequest{ManifestPath: filepath.Join("repo", "migration", "manifest.json"), StateDir: stateDir, MaintenanceService: "maintenance_test", MaintenanceDatabase: "maintenance_test", TargetDatabase: "platform_test", TargetRuntimeRole: "platform_test_runtime", GatewayDatabase: "gateway_test", GatewayRuntimeRole: "gateway_test_runtime"}
	priorAuthorityVerifier := verifyActivationAuthorityCatalogs
	verifyActivationAuthorityCatalogs = func(context.Context, ExecutionRequest) ([2]ActivationAuthorityObservation, error) {
		complete := ActivationAuthorityObservation{RuntimeInherits: true, WriterMembership: true, FullCapabilities: true, EffectiveTablePrivileges: true, EffectiveSequencePrivileges: true, EffectiveRoutinePrivileges: true}
		return [2]ActivationAuthorityObservation{complete, complete}, nil
	}
	t.Cleanup(func() { verifyActivationAuthorityCatalogs = priorAuthorityVerifier })
	probedSessions := false
	deps, err := defaultActivationDependencies(context.Background(), request, ExecuteOptions{Run: func(_ context.Context, command Command) (CommandResult, error) {
		script := string(command.Stdin)
		if strings.Contains(script, "pg_postmaster_start_time") {
			return CommandResult{Output: []byte("2026-08-10 00:00:00+00\n")}, nil
		}
		if strings.Contains(script, "post_grant_target_sessions") {
			probedSessions = true
			return CommandResult{Output: []byte("false\n")}, nil
		}
		return CommandResult{Output: []byte("true\n")}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := deps.VerifyPostGrantAuthority(context.Background()); err == nil || !probedSessions {
		t.Fatalf("residual target sessions were accepted: probed=%v error=%v", probedSessions, err)
	}
}

func TestDefaultActivationRejectsTargetSessionInsertedAfterConnectRestore(t *testing.T) {
	stateDir := t.TempDir()
	for path, value := range map[string]string{filepath.Join(stateDir, "pids", "postgres.pid"): "123\n", filepath.Join(stateDir, "ownership", "postgres.start"): "987654\n"} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	probedRestoredSessions := false
	deps, err := defaultActivationDependencies(context.Background(), ExecutionRequest{
		ManifestPath: filepath.Join("repo", "migration", "manifest.json"), StateDir: stateDir,
		MaintenanceService: "maintenance_test", MaintenanceDatabase: "maintenance_test",
		TargetDatabase: "platform_test", TargetRuntimeRole: "platform_test_runtime",
		GatewayDatabase: "gateway_test", GatewayRuntimeRole: "gateway_test_runtime",
	}, ExecuteOptions{Run: func(_ context.Context, command Command) (CommandResult, error) {
		script := string(command.Stdin)
		switch {
		case strings.Contains(script, "pg_postmaster_start_time"):
			return CommandResult{Output: []byte("2026-08-10 00:00:00+00\n")}, nil
		case strings.Contains(script, "restored_target_sessions"):
			probedRestoredSessions = true
			return CommandResult{Output: []byte("false\n")}, nil
		default:
			return CommandResult{Output: []byte("true\n")}, nil
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := deps.RestoreConnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := deps.VerifyRestoredTargetSessions(context.Background()); err == nil || !probedRestoredSessions {
		t.Fatalf("restored target session was accepted: probed=%v error=%v", probedRestoredSessions, err)
	}
}

func TestDefaultActivationPromotesExactEvidenceSetThroughTypedStore(t *testing.T) {
	stateDir := t.TempDir()
	for path, value := range map[string]string{filepath.Join(stateDir, "pids", "postgres.pid"): "123\n", filepath.Join(stateDir, "ownership", "postgres.start"): "987654\n"} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	routes := []storepkg.RouteDefinition{{RouteID: "route_1", Product: "perp", SettlementAsset: "USDT", RouteProfileID: "profile_1", RequestedState: storepkg.RouteActive}}
	routeSetSHA, err := storepkg.MigrationRouteSetSHA256(routes)
	if err != nil {
		t.Fatal(err)
	}
	request := storepkg.MigrationPromotionRequest{
		MigrationID: "G010", FenceSHA256: strings.Repeat("f", 64),
		Items: []storepkg.MigrationPromotionItem{{
			AccountID: "acct_1", OperationID: "018f47a8-2578-7d3c-9d7d-5e4f4a445d11",
			DesiredStateRevision: 2, RuntimeGeneration: 3, CredentialVersion: 4,
			RouteSetSHA256: routeSetSHA, InputSHA256: strings.Repeat("b", 64),
		}},
	}
	digest, err := request.OperationSetSHA256()
	if err != nil {
		t.Fatal(err)
	}
	operationID, err := secretstore.ParseOperationID(request.Items[0].OperationID)
	if err != nil {
		t.Fatal(err)
	}
	state := storepkg.RuntimeState{AccountID: request.Items[0].AccountID, Active: &storepkg.ActiveSnapshot{DesiredSnapshot: storepkg.DesiredSnapshot{
		OperationID: operationID, RequestDigest: request.Items[0].InputSHA256, AccountID: request.Items[0].AccountID,
		Fence: domain.AccountFence{AccountID: request.Items[0].AccountID, DesiredStateRevision: 2, RuntimeGeneration: 3, CredentialVersion: 4}, Routes: routes,
	}}}
	fake := &recordingMigrationPromotionStore{result: storepkg.MigrationPromotionResult{
		Before: storepkg.MigrationPromotionDigest{PendingSHA256: activationMigrationStateSHA256("pending", request.Items), ActiveSHA256: activationMigrationStateSHA256("active", nil), OperationSetSHA256: digest},
		After:  storepkg.MigrationPromotionDigest{PendingSHA256: activationMigrationStateSHA256("pending", nil), ActiveSHA256: activationMigrationStateSHA256("active", request.Items), OperationSetSHA256: digest},
	}, states: []storepkg.RuntimeState{state}}
	prior := openMigrationPromotionStore
	openMigrationPromotionStore = func(_ context.Context, service, database, role string) (migrationPromotionStore, func(), error) {
		if service != "gateway_test" || database != "gateway_test" || role != "gateway_test_migrator" {
			t.Fatalf("promotion identity=%q/%q/%q", service, database, role)
		}
		return fake, func() {}, nil
	}
	t.Cleanup(func() { openMigrationPromotionStore = prior })
	var commands []Command
	deps, err := defaultActivationDependenciesForPromotion(context.Background(), ExecutionRequest{
		ManifestPath: filepath.Join("repo", "migration", "manifest.json"), StateDir: stateDir,
		GatewayService: "gateway_test", GatewayDatabase: "gateway_test", GatewayRole: "gateway_test_migrator",
		MaintenanceService: "maintenance_test", MaintenanceDatabase: "maintenance_test",
	}, ExecuteOptions{Run: func(_ context.Context, command Command) (CommandResult, error) {
		commands = append(commands, command)
		if strings.Contains(string(command.Stdin), "pg_postmaster_start_time") {
			return CommandResult{Output: []byte("2026-08-10 00:00:00+00\n")}, nil
		}
		return CommandResult{}, nil
	}}, &verifiedMigrationPromotion{Request: request, OperationSetSHA256: digest})
	if err != nil {
		t.Fatal(err)
	}
	before, expected, err := deps.ExpectedPromotion(context.Background())
	if err != nil || before != digest || expected != digest {
		t.Fatalf("before=%q expected=%q err=%v", before, expected, err)
	}
	promoted, err := deps.PromoteGateway(context.Background())
	if err != nil || promoted.After.OperationSetSHA256 != digest || !reflect.DeepEqual(fake.request, request) {
		t.Fatalf("promotion=%+v request=%+v err=%v", promoted, fake.request, err)
	}
	current, err := deps.VerifyGatewayPromotion(context.Background())
	if err != nil || !reflect.DeepEqual(current, promoted.After) {
		t.Fatalf("current=%+v promoted=%+v err=%v", current, promoted, err)
	}
	for _, command := range commands {
		if strings.Contains(string(command.Stdin), "UPDATE gateway_") {
			t.Fatalf("activation bypassed typed store API: %s", command.Stdin)
		}
	}
}

func TestObserveActivationGatewayPromotionHeadRejectsUnexpectedActiveHeads(t *testing.T) {
	routes := []storepkg.RouteDefinition{{RouteID: "route_1", Product: "perp", SettlementAsset: "USDT", RouteProfileID: "profile_1", RequestedState: storepkg.RouteActive}}
	active := func(accountID, operationID, inputSHA string, generation uint64) (storepkg.RuntimeState, storepkg.MigrationPromotionItem) {
		t.Helper()
		parsedOperationID, err := secretstore.ParseOperationID(operationID)
		if err != nil {
			t.Fatal(err)
		}
		snapshot := storepkg.DesiredSnapshot{
			OperationID: parsedOperationID, RequestDigest: inputSHA, AccountID: domain.AccountID(accountID),
			Fence: domain.AccountFence{AccountID: domain.AccountID(accountID), DesiredStateRevision: generation, RuntimeGeneration: generation, CredentialVersion: generation}, Routes: routes,
		}
		item, err := activationMigrationPromotionItem(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		return storepkg.RuntimeState{AccountID: domain.AccountID(accountID), Active: &storepkg.ActiveSnapshot{DesiredSnapshot: snapshot}}, item
	}
	expectedState, expectedItem := active("account-a", "018f47a8-2578-7d3c-9d7d-5e4f4a445d41", strings.Repeat("a", 64), 1)
	extraState, _ := active("account-b", "018f47a8-2578-7d3c-9d7d-5e4f4a445d42", strings.Repeat("b", 64), 2)
	for _, tc := range []struct {
		name   string
		items  []storepkg.MigrationPromotionItem
		states []storepkg.RuntimeState
	}{
		{name: "empty expected set", items: []storepkg.MigrationPromotionItem{}, states: []storepkg.RuntimeState{extraState}},
		{name: "nonempty expected set", items: []storepkg.MigrationPromotionItem{expectedItem}, states: []storepkg.RuntimeState{expectedState, extraState}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := storepkg.MigrationPromotionRequest{MigrationID: "G010", FenceSHA256: strings.Repeat("f", 64), Items: tc.items}
			digest, err := request.OperationSetSHA256()
			if err != nil {
				t.Fatal(err)
			}
			store := &recordingMigrationPromotionStore{states: tc.states}
			if _, err := observeActivationGatewayPromotionHead(context.Background(), store, verifiedMigrationPromotion{Request: request, OperationSetSHA256: digest}); err == nil {
				t.Fatal("unexpected active Gateway head was accepted")
			}
		})
	}
}

func TestDefaultActivationStoreConflictStopsBeforeWriterGrant(t *testing.T) {
	stateDir := t.TempDir()
	for path, value := range map[string]string{filepath.Join(stateDir, "pids", "postgres.pid"): "123\n", filepath.Join(stateDir, "ownership", "postgres.start"): "987654\n"} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	promotionRequest := storepkg.MigrationPromotionRequest{MigrationID: "G010", FenceSHA256: strings.Repeat("f", 64)}
	digest, err := promotionRequest.OperationSetSHA256()
	if err != nil {
		t.Fatal(err)
	}
	fake := &recordingMigrationPromotionStore{err: storepkg.ErrMigrationPromotionConflict}
	prior := openMigrationPromotionStore
	openMigrationPromotionStore = func(context.Context, string, string, string) (migrationPromotionStore, func(), error) {
		return fake, func() {}, nil
	}
	t.Cleanup(func() { openMigrationPromotionStore = prior })
	var commands []Command
	request := ExecutionRequest{
		ManifestPath: filepath.Join("repo", "migration", "manifest.json"), StateDir: stateDir,
		SourceService: "funding_source_test", SourceDatabase: "bolter_funding_test", SourceWriterRole: "bolter_funding_test_writer",
		TargetDatabase: "bolter_platform_test", TargetRole: "bolter_platform_test_migrator", TargetRuntimeRole: "bolter_platform_test", TargetWriterRole: "bolter_platform_test_writer",
		GatewayService: "gateway_test", GatewayDatabase: "bolter_gateway_test", GatewayRole: "bolter_gateway_test_migrator", GatewayRuntimeRole: "bolter_gateway_test", GatewayWriterRole: "bolter_gateway_test_writer",
		MaintenanceService: "platform_maintenance_test", MaintenanceDatabase: "bolter_maintenance_test", MaintenanceRole: "bolter_platform_test_admin",
	}
	priorAuthorityVerifier := verifyActivationAuthorityCatalogs
	verifyActivationAuthorityCatalogs = func(context.Context, ExecutionRequest) ([2]ActivationAuthorityObservation, error) {
		platformMatrix, gatewayMatrix := authority.PlatformV15(), authority.GatewayV6()
		return [2]ActivationAuthorityObservation{
			{MatrixSHA256: platformMatrix.Digest(), MigrationSHA256: platformMatrix.MigrationDigest, RuntimeRole: platformMatrix.Roles.Runtime, RuntimeInherits: true, FullCapabilities: true, EffectiveTablePrivileges: true, EffectiveSequencePrivileges: true, EffectiveRoutinePrivileges: true},
			{MatrixSHA256: gatewayMatrix.Digest(), MigrationSHA256: gatewayMatrix.MigrationDigest, RuntimeRole: gatewayMatrix.Roles.Runtime, RuntimeInherits: true, FullCapabilities: true, EffectiveTablePrivileges: true, EffectiveSequencePrivileges: true, EffectiveRoutinePrivileges: true},
		}, nil
	}
	t.Cleanup(func() { verifyActivationAuthorityCatalogs = priorAuthorityVerifier })
	deps, err := defaultActivationDependenciesForPromotion(context.Background(), request, ExecuteOptions{Run: func(_ context.Context, command Command) (CommandResult, error) {
		commands = append(commands, command)
		script := string(command.Stdin)
		if strings.Contains(script, "pg_postmaster_start_time") {
			return CommandResult{Output: []byte("2026-08-10 00:00:00+00\n")}, nil
		}
		if strings.Contains(script, "rolcanlogin") {
			return CommandResult{Output: []byte("true\n")}, nil
		}
		if strings.Contains(script, "'USAGE')::text ||") {
			return CommandResult{Output: []byte("false|false\n")}, nil
		}
		if strings.Contains(script, "DELETE FROM public.") {
			return CommandResult{Output: []byte("true\n")}, nil
		}
		if strings.Contains(script, "pg_terminate_backend") {
			return CommandResult{Output: []byte("true\n")}, nil
		}
		return CommandResult{}, nil
	}}, &verifiedMigrationPromotion{Request: promotionRequest, OperationSetSHA256: digest})
	if err != nil {
		t.Fatal(err)
	}
	var attempt *ActivationAttempt
	deps.LoadAttempt = func(context.Context) (*ActivationAttempt, error) { return attempt, nil }
	deps.SaveAttempt = func(_ context.Context, value ActivationAttempt) error { copy := value; attempt = &copy; return nil }
	input := testActivationInput(t)
	deps.VerifyPriorState = func(_ context.Context, _ ActivationPriorVerificationPlan, attempt ActivationAttempt) (ActivationPriorVerificationObservation, error) {
		planSHA, _ := activationPriorPlanSHA(input.PriorVerification)
		return ActivationPriorVerificationObservation{AttemptID: attempt.ID, PlanSHA256: planSHA, ArtifactSetSHA256: strings.Repeat("1", 64), CurrentPostconditionsSHA256: strings.Repeat("2", 64)}, nil
	}
	deps.RecoverPriorState = deps.VerifyPriorState
	if _, err := ExecuteActivation(context.Background(), input, deps); err == nil || !strings.Contains(err.Error(), "Gateway before-head verification failed") {
		t.Fatalf("error=%v", err)
	}
	for _, command := range commands {
		if strings.Contains(string(command.Stdin), `GRANT :"platform_writer"`) {
			t.Fatalf("writer grant ran after typed store conflict: %s", command.Stdin)
		}
	}
}

func TestLoadVerifiedMigrationPromotionRejectsTamperedEvidenceBinding(t *testing.T) {
	request := storepkg.MigrationPromotionRequest{
		MigrationID: "G010", FenceSHA256: strings.Repeat("f", 64),
		Items: []storepkg.MigrationPromotionItem{{
			AccountID: "acct_1", OperationID: "018f47a8-2578-7d3c-9d7d-5e4f4a445d11",
			DesiredStateRevision: 2, RuntimeGeneration: 3, CredentialVersion: 4,
			RouteSetSHA256: strings.Repeat("a", 64), InputSHA256: strings.Repeat("b", 64),
		}},
	}
	digest, err := request.OperationSetSHA256()
	if err != nil {
		t.Fatal(err)
	}
	validProof := operationProof{
		SchemaVersion: 1, Operation: Backfill, Status: "pass", MigrationID: "G010",
		Subject:     strings.Repeat("c", 64) + "/account-route-v1.json/" + strings.Repeat("d", 64),
		Checks:      []string{"connected-identities-verified", "source-high-water-equal", "platform-schema-migrated", "mapping-content-stable", "durable-idempotent-saga", "pending-only"},
		GeneratedBy: evidenceGenerator, PreStateSHA: strings.Repeat("e", 64), InputSHA: strings.Repeat("d", 64),
		Commands:         []commandEvidence{{Executable: "/repo/.local/bin/account-route-backfill", ArgvSHA256: strings.Repeat("1", 64), OutputSHA: strings.Repeat("2", 64)}},
		PromotionRequest: &request, PromotionOperationSetSHA256: digest,
	}
	for name, mutate := range map[string]func(*operationProof){
		"digest": func(proof *operationProof) { proof.PromotionOperationSetSHA256 = strings.Repeat("0", 64) },
		"fence":  func(proof *operationProof) { proof.PromotionRequest.FenceSHA256 = strings.Repeat("9", 64) },
		"item":   func(proof *operationProof) { proof.PromotionRequest.Items[0].RuntimeGeneration++ },
	} {
		t.Run(name, func(t *testing.T) {
			proof := validProof
			requestCopy := request
			requestCopy.Items = append([]storepkg.MigrationPromotionItem(nil), request.Items...)
			proof.PromotionRequest = &requestCopy
			if mutate != nil {
				mutate(&proof)
			}
			manifest, evidenceDir := writePromotionEvidenceFixture(t, proof)
			if _, err := loadVerifiedMigrationPromotion(manifest, evidenceDir); err == nil {
				t.Fatalf("tampered %s promotion was accepted", name)
			}
		})
	}
	manifest, evidenceDir := writePromotionEvidenceFixture(t, validProof)
	loaded, err := loadVerifiedMigrationPromotion(manifest, evidenceDir)
	if err != nil || loaded.OperationSetSHA256 != digest || !reflect.DeepEqual(loaded.Request, request) {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
}

func TestExecuteActivationFormalPathBuildsDefaultDependencies(t *testing.T) {
	manifestPath := preparedForActivation(t)
	evidenceDir := t.TempDir()
	_, err := Execute(context.Background(), ExecutionRequest{
		Operation: Activate, ManifestPath: manifestPath, EvidenceDir: evidenceDir,
		SourceService: "source_test", SourceDatabase: "source_test", SourceRole: "source_test_migrator", SourceWriterRole: "source_test_writer",
		TargetService: "platform_test", TargetDatabase: "platform_test", TargetRole: "platform_test_migrator", TargetRuntimeRole: "platform_test_runtime", TargetWriterRole: "platform_test_writer",
		GatewayService: "gateway_test", GatewayDatabase: "gateway_test", GatewayRole: "gateway_test_migrator", GatewayRuntimeRole: "gateway_test_runtime", GatewayWriterRole: "gateway_test_writer",
		MaintenanceService: "maintenance_test", MaintenanceDatabase: "maintenance_test", MaintenanceRole: "maintenance_test_admin",
	}, ExecuteOptions{})
	if err == nil || strings.Contains(err.Error(), "canonical activation dependencies are required") {
		t.Fatalf("formal activation path did not construct defaults: %v", err)
	}
}

func TestExecuteActivationRequiresExplicitPriorReplayPlan(t *testing.T) {
	manifestPath := preparedForActivation(t)
	evidenceDir := t.TempDir()
	request := canonicalActivationExecutionRequest(manifestPath, evidenceDir)
	request.BackupID, request.BackupArtifactPath, request.FinalDeltaArtifactPath, request.BackfillPath, request.Verifier = "", "", "", "", ""
	var attempt *ActivationAttempt
	var calls []string
	deps := testActivationDependencies(&attempt, &calls)
	if _, err := Execute(context.Background(), request, ExecuteOptions{Activation: &deps}); err == nil || !strings.Contains(err.Error(), "prior replay") {
		t.Fatalf("missing prior replay inputs error=%v", err)
	}
	if attempt != nil || containsString(calls, "stop") {
		t.Fatalf("missing prior replay inputs caused side effects: attempt=%+v calls=%v", attempt, calls)
	}
}

func TestVerifyActivationPriorStateRerunsOnlySixValidateCurrentProbes(t *testing.T) {
	manifest, request, plan := generatedActivationPriorFixture(t)
	calls := 0
	observation, err := verifyActivationPriorState(context.Background(), manifest, request, plan, ActivationAttempt{ID: "attempt-1"}, ExecuteOptions{Run: func(_ context.Context, command Command) (CommandResult, error) {
		calls++
		if command.ExpectedOutput != "" {
			return CommandResult{Output: []byte(command.ExpectedOutput)}, nil
		}
		return CommandResult{Output: []byte("current validation passed")}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 6 || observation.AttemptID != "attempt-1" || !validSHA256(observation.PlanSHA256) || !validSHA256(observation.ArtifactSetSHA256) || !validSHA256(observation.CurrentPostconditionsSHA256) {
		t.Fatalf("calls=%d observation=%+v", calls, observation)
	}
	calls = 0
	if _, err := verifyActivationPriorState(context.Background(), manifest, request, plan, ActivationAttempt{ID: "attempt-1"}, ExecuteOptions{Run: func(context.Context, Command) (CommandResult, error) {
		calls++
		if calls == 4 {
			return CommandResult{}, errors.New("stale current state")
		}
		return CommandResult{Output: []byte("current validation passed")}, nil
	}}); err == nil || !strings.Contains(err.Error(), "current postcondition 4") || calls != 4 {
		t.Fatalf("stale current postcondition calls=%d error=%v", calls, err)
	}
	driftedPlan := plan
	driftedPlan.Events = append([]ActivationPriorEvent(nil), plan.Events...)
	driftedPlan.Events[0].EvidencePath = filepath.Join(t.TempDir(), "other.json")
	calls = 0
	if _, err := verifyActivationPriorState(context.Background(), manifest, request, driftedPlan, ActivationAttempt{ID: "attempt-1"}, ExecuteOptions{Run: func(context.Context, Command) (CommandResult, error) {
		calls++
		return CommandResult{}, nil
	}}); err == nil || !strings.Contains(err.Error(), "plan changed") || calls != 0 {
		t.Fatalf("drifted plan calls=%d error=%v", calls, err)
	}
	originalDelta, err := os.ReadFile(request.FinalDeltaArtifactPath)
	if err != nil {
		t.Fatal(err)
	}
	symlinkTarget := filepath.Join(t.TempDir(), "same-content.dump")
	if err := os.WriteFile(symlinkTarget, originalDelta, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(request.FinalDeltaArtifactPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(symlinkTarget, request.FinalDeltaArtifactPath); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyActivationPriorState(context.Background(), manifest, request, plan, ActivationAttempt{ID: "attempt-1"}, ExecuteOptions{Run: func(context.Context, Command) (CommandResult, error) {
		return CommandResult{Output: []byte("current validation passed")}, nil
	}}); err == nil || !strings.Contains(err.Error(), "artifact") {
		t.Fatalf("symlinked artifact error=%v", err)
	}
	if err := os.Remove(request.FinalDeltaArtifactPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(request.FinalDeltaArtifactPath, originalDelta, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(request.FinalDeltaArtifactPath, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	calls = 0
	if _, err := verifyActivationPriorState(context.Background(), manifest, request, plan, ActivationAttempt{ID: "attempt-1"}, ExecuteOptions{Run: func(context.Context, Command) (CommandResult, error) {
		calls++
		return CommandResult{}, nil
	}}); err == nil || !strings.Contains(err.Error(), "artifact changed") || calls != 0 {
		t.Fatalf("tampered artifact calls=%d error=%v", calls, err)
	}
}

func TestRecoverActivationPriorStateRebuildsWithoutRunningCurrentProbes(t *testing.T) {
	manifest, request, plan := generatedActivationPriorFixture(t)
	attempt := ActivationAttempt{ID: "attempt-1"}
	expected, err := verifyActivationPriorState(context.Background(), manifest, request, plan, attempt, ExecuteOptions{Run: func(_ context.Context, command Command) (CommandResult, error) {
		if command.ExpectedOutput != "" {
			return CommandResult{Output: []byte(command.ExpectedOutput)}, nil
		}
		return CommandResult{Output: []byte("current validation passed")}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	probeCalls := 0
	rebuilt, err := recoverActivationPriorState(context.Background(), manifest, request, plan, attempt, ExecuteOptions{Run: func(context.Context, Command) (CommandResult, error) {
		probeCalls++
		return CommandResult{}, errors.New("current probes must not run during recovery")
	}})
	if err != nil {
		t.Fatal(err)
	}
	if probeCalls != 0 || !reflect.DeepEqual(rebuilt, expected) {
		t.Fatalf("probe calls=%d rebuilt=%+v expected=%+v", probeCalls, rebuilt, expected)
	}
}

func TestExecuteActivationReplayRevalidatesPriorEvidenceWithoutCurrentProbes(t *testing.T) {
	manifest, request, plan := generatedActivationPriorFixture(t)
	input := testActivationInput(t)
	input.MigrationID = plan.MigrationID
	input.PriorVerification = plan
	var attempt *ActivationAttempt
	var calls []string
	deps := testActivationDependencies(&attempt, &calls)
	verificationRun := 0
	probeCalls := 0
	deps.VerifyPriorState = func(ctx context.Context, _ ActivationPriorVerificationPlan, attempt ActivationAttempt) (ActivationPriorVerificationObservation, error) {
		verificationRun++
		currentRun := verificationRun
		return verifyActivationPriorState(ctx, manifest, request, plan, attempt, ExecuteOptions{Run: func(context.Context, Command) (CommandResult, error) {
			probeCalls++
			return CommandResult{Output: []byte(fmt.Sprintf("pass; run=%d; probe=%d; duration=%dms", currentRun, probeCalls, currentRun*probeCalls))}, nil
		}})
	}
	deps.RecoverPriorState = func(ctx context.Context, _ ActivationPriorVerificationPlan, attempt ActivationAttempt) (ActivationPriorVerificationObservation, error) {
		return recoverActivationPriorState(ctx, manifest, request, plan, attempt, ExecuteOptions{})
	}
	if _, err := ExecuteActivation(context.Background(), input, deps); err != nil {
		t.Fatalf("first activation: %v", err)
	}
	replayed, err := ExecuteActivation(context.Background(), input, deps)
	if err != nil {
		t.Fatalf("activated replay with different successful stdout: %v", err)
	}
	if !replayed.Replayed || verificationRun != 1 || probeCalls != 6 {
		t.Fatalf("replay=%+v verification runs=%d probe calls=%d", replayed, verificationRun, probeCalls)
	}
}

func TestExecuteActivationRecordsGeneratedLedgerEvidence(t *testing.T) {
	manifestPath := preparedForActivation(t)
	evidenceDir := t.TempDir()
	var attempt *ActivationAttempt
	var calls []string
	deps := testActivationDependencies(&attempt, &calls)
	stamp := time.Now().UTC().Add(time.Hour)
	deps.Now = func() time.Time { return stamp }
	request := canonicalActivationExecutionRequest(manifestPath, evidenceDir)
	result, err := Execute(context.Background(), request, ExecuteOptions{Now: deps.Now, Activation: &deps})
	if err != nil {
		t.Fatal(err)
	}
	if result.Phase != PhaseActivated {
		t.Fatalf("result=%+v", result)
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateWorkflow(data); err != nil {
		t.Fatalf("activated workflow: %v", err)
	}
	manifest, err := DecodeManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.TargetActivationAt == nil || manifest.CutoverAt == nil || *manifest.TargetActivationAt == *manifest.CutoverAt {
		t.Fatalf("activation timestamps=%v/%v", manifest.TargetActivationAt, manifest.CutoverAt)
	}
	if len(manifest.Workflow.Events) == 0 || manifest.Workflow.Events[len(manifest.Workflow.Events)-1].Operation != Activate {
		t.Fatalf("events=%+v", manifest.Workflow.Events)
	}
	if manifest.ActivationAttempt == nil || manifest.ActivationAttempt.Observations == nil {
		t.Fatalf("activation attempt is missing typed observations: %+v", manifest.ActivationAttempt)
	}
	proofData, err := os.ReadFile(result.EvidencePath)
	if err != nil {
		t.Fatal(err)
	}
	var proof operationProof
	if err := json.Unmarshal(proofData, &proof); err != nil || proof.ActivationObservations == nil || !reflect.DeepEqual(proof.ActivationObservations, manifest.ActivationAttempt.Observations) {
		t.Fatalf("activation proof observations=%+v attempt=%+v err=%v", proof.ActivationObservations, manifest.ActivationAttempt.Observations, err)
	}
}

func TestExecuteActivationPublishesAttemptEvidenceAndActivationStateAtomically(t *testing.T) {
	manifestPath := preparedForActivation(t)
	evidenceDir := t.TempDir()
	priorWriter := writeActivationManifestAtomic
	writeActivationManifestAtomic = func(path string, manifest Manifest) error {
		if manifest.Workflow.Phase == PhaseActivated {
			return errors.New("injected final manifest failure")
		}
		return writeManifestAtomic(path, manifest)
	}
	t.Cleanup(func() { writeActivationManifestAtomic = priorWriter })
	var attempt *ActivationAttempt
	var calls []string
	deps := testActivationDependencies(&attempt, &calls)
	stamp := time.Now().UTC().Add(time.Hour)
	deps.Now = func() time.Time { return stamp }
	deps.ContainPostGrantFailure = func(context.Context) error { calls = append(calls, "contain"); return nil }
	request := canonicalActivationExecutionRequest(manifestPath, evidenceDir)
	_, err := Execute(context.Background(), request, ExecuteOptions{Now: deps.Now, Activation: &deps})
	if err == nil || !strings.Contains(err.Error(), "injected final manifest failure") {
		t.Fatalf("activation error=%v", err)
	}
	manifest := readWorkflowManifest(t, manifestPath)
	if manifest.Workflow.Phase != PhaseActivationReady || manifest.ActivationAttempt == nil || manifest.ActivationAttempt.Boundary != ActivationServicesStarted {
		t.Fatalf("partial activation state=%+v", manifest)
	}
	if manifest.ActivationAttempt.EvidencePath != "" || manifest.ActivationAttempt.ProofSHA256 != "" || manifest.TargetActivationAt != nil || manifest.CutoverAt != nil || manifest.OneWriter.TargetCanonicalWritesEnabled {
		t.Fatalf("partial activation fields=%+v", manifest)
	}
	for _, event := range manifest.Workflow.Events {
		if event.Operation == Activate {
			t.Fatalf("partial activation event=%+v", event)
		}
	}
	if len(calls) == 0 || calls[len(calls)-1] != "contain" {
		t.Fatalf("post-grant failure was not contained: %v", calls)
	}
}

func generatedActivationPriorFixture(t *testing.T) (Manifest, ExecutionRequest, ActivationPriorVerificationPlan) {
	t.Helper()
	repoRoot, mappingPath := canonicalBackfillFixture(t, `{"schemaVersion":1,"mappings":[]}`)
	manifestPath := copyManifest(t)
	manifest := readWorkflowManifest(t, manifestPath)
	evidenceDir := t.TempDir()
	backupPath := filepath.Join(t.TempDir(), "backup.dump")
	deltaPath := filepath.Join(t.TempDir(), "final-delta.dump")
	stubGitProvenance(t)
	if _, err := Execute(context.Background(), ExecutionRequest{Operation: RecordImport, ManifestPath: manifestPath, EvidenceDir: evidenceDir, ID: "0123456789abcdef0123456789abcdef01234567"}, ExecuteOptions{}); err != nil {
		t.Fatal(err)
	}
	common := ExecutionRequest{
		ManifestPath: manifestPath, EvidenceDir: evidenceDir, repositoryRoot: repoRoot,
		SourceService: "source_test", SourceDatabase: "source_test", SourceRole: "source_test_migrator", SourceWriterRole: "source_test_writer",
		TargetService: "platform_test", TargetDatabase: "platform_test", TargetRole: "platform_test_migrator", TargetWriterRole: "platform_test_writer",
		GatewayService: "gateway_test", GatewayDatabase: "gateway_test", GatewayRole: "gateway_test_migrator",
		MaintenanceService: "maintenance_test", MaintenanceDatabase: "maintenance_test", MaintenanceRole: "maintenance_test_admin",
	}
	runner := func(_ context.Context, command Command) (CommandResult, error) {
		if command.StdoutFile != nil {
			if _, err := command.StdoutFile.Write([]byte("generated final delta")); err != nil {
				return CommandResult{}, err
			}
		}
		if command.Name == "pg_dump" && command.StdoutFile == nil {
			if err := os.WriteFile(backupPath, []byte("generated backup"), 0o600); err != nil {
				return CommandResult{}, err
			}
		}
		if filepath.Base(command.Name) == "account-route-backfill" {
			return CommandResult{Output: validBackfillPromotionOutput(t, readWorkflowManifest(t, manifestPath))}, nil
		}
		if command.ExpectedOutputSHA != "" || command.CaptureOutputAsInput || command.MatchCapturedInput {
			return CommandResult{Output: []byte("[]")}, nil
		}
		if command.ExpectedOutput != "" {
			return CommandResult{Output: []byte(command.ExpectedOutput)}, nil
		}
		return CommandResult{Output: []byte("ok")}, nil
	}
	steps := []ExecutionRequest{
		{Operation: Backup, ID: "backup-001", ArtifactPath: backupPath},
		{Operation: Restore, ArtifactPath: backupPath, Verifier: "g010-cutover"},
		{Operation: FinalDelta, ArtifactPath: deltaPath},
		{Operation: Fence},
		{Operation: Backfill, BackfillPath: mappingPath},
		{Operation: Validate, Verifier: "g010-cutover"},
	}
	for _, step := range steps {
		step.ManifestPath, step.EvidenceDir, step.repositoryRoot = common.ManifestPath, common.EvidenceDir, common.repositoryRoot
		step.SourceService, step.SourceDatabase, step.SourceRole, step.SourceWriterRole = common.SourceService, common.SourceDatabase, common.SourceRole, common.SourceWriterRole
		step.TargetService, step.TargetDatabase, step.TargetRole, step.TargetWriterRole = common.TargetService, common.TargetDatabase, common.TargetRole, common.TargetWriterRole
		step.GatewayService, step.GatewayDatabase, step.GatewayRole = common.GatewayService, common.GatewayDatabase, common.GatewayRole
		step.MaintenanceService, step.MaintenanceDatabase, step.MaintenanceRole = common.MaintenanceService, common.MaintenanceDatabase, common.MaintenanceRole
		if _, err := Execute(context.Background(), step, ExecuteOptions{Run: runner}); err != nil {
			t.Fatalf("execute %s: %v", step.Operation, err)
		}
	}
	if _, err := applyExecuted(manifestPath, Request{Operation: PrepareActivation}, ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	manifest = readWorkflowManifest(t, manifestPath)
	request := common
	request.Operation, request.BackupID, request.BackupArtifactPath = Activate, "backup-001", backupPath
	request.FinalDeltaArtifactPath, request.BackfillPath, request.Verifier = deltaPath, mappingPath, "g010-cutover"
	plan, err := buildActivationPriorVerificationPlan(manifest, request)
	if err != nil {
		t.Fatal(err)
	}
	return manifest, request, plan
}

func testActivationInput(t *testing.T) ActivationInput {
	t.Helper()
	dir := t.TempDir()
	return ActivationInput{
		MigrationID: "G010", ManifestPath: filepath.Join(dir, "manifest.json"), EvidenceDir: filepath.Join(dir, "evidence"), JournalPath: filepath.Join(dir, "evidence", "attempt.json"),
		Source:           DatabaseIdentity{Service: "funding_source_test", Database: "bolter_funding_test", Role: "bolter_funding_test_migrator"},
		Platform:         DatabaseIdentity{Service: "platform_target_test", Database: "bolter_platform_test", Role: "bolter_platform_test_migrator"},
		Gateway:          DatabaseIdentity{Service: "gateway_test", Database: "bolter_gateway_test", Role: "bolter_gateway_test_migrator"},
		Maintenance:      DatabaseIdentity{Service: "platform_maintenance_test", Database: "bolter_maintenance_test", Role: "bolter_platform_test_admin"},
		SourceWriterRole: "bolter_funding_test_writer", PlatformMigratorRole: "bolter_platform_test_migrator", PlatformRuntimeRole: "bolter_platform_test", PlatformWriterRole: "bolter_platform_test_writer",
		GatewayMigratorRole: "bolter_gateway_test_migrator", GatewayRuntimeRole: "bolter_gateway_test", GatewayWriterRole: "bolter_gateway_test_writer",
		PriorVerification: ActivationPriorVerificationPlan{SchemaVersion: 1, MigrationID: "G010", ManifestStateSHA256: strings.Repeat("4", 64), Events: []ActivationPriorEvent{{Sequence: 1, Epoch: 1, Operation: Backup, EvidencePath: filepath.Join(dir, "evidence", "001-backup.json"), ProofSHA256: strings.Repeat("3", 64), ArtifactPath: filepath.Join(dir, "backup.dump"), ArtifactSHA256: strings.Repeat("2", 64)}}},
	}
}

func canonicalActivationExecutionRequest(manifestPath, evidenceDir string) ExecutionRequest {
	return ExecutionRequest{
		Operation: Activate, ManifestPath: manifestPath, EvidenceDir: evidenceDir, JournalPath: filepath.Join(evidenceDir, "attempt.json"),
		BackupID: "backup-001", BackupArtifactPath: filepath.Join(filepath.Dir(manifestPath), "backup.dump"), FinalDeltaArtifactPath: filepath.Join(filepath.Dir(manifestPath), "final-delta.dump"), BackfillPath: filepath.Join(filepath.Dir(manifestPath), "001-platform.sql"), Verifier: "g010-cutover",
		SourceService: "funding_source_test", SourceDatabase: "bolter_funding_test", SourceRole: "bolter_funding_test_migrator", SourceWriterRole: "bolter_funding_test_writer",
		TargetService: "platform_target_test", TargetDatabase: "bolter_platform_test", TargetRole: "bolter_platform_test_migrator", TargetRuntimeRole: "bolter_platform_test", TargetWriterRole: "bolter_platform_test_writer",
		GatewayService: "gateway_test", GatewayDatabase: "bolter_gateway_test", GatewayRole: "bolter_gateway_test_migrator", GatewayRuntimeRole: "bolter_gateway_test", GatewayWriterRole: "bolter_gateway_test_writer",
		MaintenanceService: "platform_maintenance_test", MaintenanceDatabase: "bolter_maintenance_test", MaintenanceRole: "bolter_platform_test_admin",
	}
}

func testActivationDependencies(attempt **ActivationAttempt, calls *[]string) ActivationDependencies {
	digest := strings.Repeat("a", 64)
	promotion := ActivationGatewayPromotionObservation{
		Before: activationGatewayHeadObservation(storepkg.MigrationPromotionDigest{PendingSHA256: strings.Repeat("b", 64), ActiveSHA256: strings.Repeat("c", 64), OperationSetSHA256: digest}),
		After:  activationGatewayHeadObservation(storepkg.MigrationPromotionDigest{PendingSHA256: activationMigrationStateSHA256("pending", nil), ActiveSHA256: strings.Repeat("e", 64), OperationSetSHA256: digest}),
	}
	return ActivationDependencies{
		Now: func() time.Time { return time.Date(2026, 8, 10, 1, 0, 0, 0, time.UTC) },
		LoadAttempt: func(context.Context) (*ActivationAttempt, error) {
			if *attempt == nil {
				return nil, nil
			}
			copy := **attempt
			return &copy, nil
		},
		SaveAttempt: func(_ context.Context, value ActivationAttempt) error { copy := value; *attempt = &copy; return nil },
		ExpectedPromotion: func(context.Context) (string, string, error) {
			*calls = append(*calls, "promotion-state")
			return digest, digest, nil
		},
		VerifyPriorState: func(_ context.Context, plan ActivationPriorVerificationPlan, attempt ActivationAttempt) (ActivationPriorVerificationObservation, error) {
			*calls = append(*calls, "prior-state")
			data, err := json.Marshal(plan)
			if err != nil {
				return ActivationPriorVerificationObservation{}, err
			}
			return ActivationPriorVerificationObservation{AttemptID: attempt.ID, PlanSHA256: sha256Hex(data), ArtifactSetSHA256: strings.Repeat("1", 64), CurrentPostconditionsSHA256: strings.Repeat("2", 64)}, nil
		},
		RecoverPriorState: func(_ context.Context, plan ActivationPriorVerificationPlan, attempt ActivationAttempt) (ActivationPriorVerificationObservation, error) {
			*calls = append(*calls, "recover-prior-state")
			data, err := json.Marshal(plan)
			if err != nil {
				return ActivationPriorVerificationObservation{}, err
			}
			return ActivationPriorVerificationObservation{AttemptID: attempt.ID, PlanSHA256: sha256Hex(data), ArtifactSetSHA256: strings.Repeat("1", 64), CurrentPostconditionsSHA256: strings.Repeat("2", 64)}, nil
		},
		VerifySourceFence: func(context.Context) error { *calls = append(*calls, "source-fence"); return nil },
		VerifyPreGrantAuthority: func(_ context.Context, resuming bool) (ActivationPreGrantObservation, error) {
			*calls = append(*calls, "pre-grant-authority")
			platformMatrix, gatewayMatrix := authority.PlatformV15(), authority.GatewayV6()
			return ActivationPreGrantObservation{
				Platform:       ActivationAuthorityObservation{MatrixSHA256: platformMatrix.Digest(), MigrationSHA256: platformMatrix.MigrationDigest, RuntimeRole: platformMatrix.Roles.Runtime, RuntimeInherits: true, WriterMembership: resuming, FullCapabilities: true, EffectiveTablePrivileges: true, EffectiveSequencePrivileges: true, EffectiveRoutinePrivileges: true},
				Gateway:        ActivationAuthorityObservation{MatrixSHA256: gatewayMatrix.Digest(), MigrationSHA256: gatewayMatrix.MigrationDigest, RuntimeRole: gatewayMatrix.Roles.Runtime, RuntimeInherits: true, WriterMembership: resuming, FullCapabilities: true, EffectiveTablePrivileges: true, EffectiveSequencePrivileges: true, EffectiveRoutinePrivileges: true},
				DMLProbeSHA256: strings.Repeat("a", 64),
			}, nil
		},
		StopServices: func(context.Context) error { *calls = append(*calls, "stop"); return nil },
		ObserveGatewayPromotionHead: func(context.Context) (ActivationGatewayHeadObservation, error) {
			*calls = append(*calls, "observe-gateway-promotion-head")
			return promotion.Before, nil
		},
		PromoteGateway: func(context.Context) (ActivationGatewayPromotionObservation, error) {
			*calls = append(*calls, "promote")
			return promotion, nil
		},
		VerifyGatewayPromotion: func(context.Context) (ActivationGatewayHeadObservation, error) {
			*calls = append(*calls, "verify-gateway-promotion")
			return promotion.After, nil
		},
		FenceTargetConnect: func(context.Context) (ActivationTargetFenceObservation, error) {
			*calls = append(*calls, "fence-target-connect")
			return ActivationTargetFenceObservation{SessionsZero: true, ProbeSHA256: strings.Repeat("b", 64)}, nil
		},
		GrantWriters: func(context.Context) error { *calls = append(*calls, "grant"); return nil },
		VerifyPostGrantAuthority: func(context.Context) (ActivationPostGrantObservation, error) {
			*calls = append(*calls, "verify-authority")
			platformMatrix, gatewayMatrix := authority.PlatformV15(), authority.GatewayV6()
			return ActivationPostGrantObservation{
				Platform:           ActivationAuthorityObservation{MatrixSHA256: platformMatrix.Digest(), MigrationSHA256: platformMatrix.MigrationDigest, RuntimeRole: platformMatrix.Roles.Runtime, RuntimeInherits: true, WriterMembership: true, FullCapabilities: true, EffectiveTablePrivileges: true, EffectiveSequencePrivileges: true, EffectiveRoutinePrivileges: true},
				Gateway:            ActivationAuthorityObservation{MatrixSHA256: gatewayMatrix.Digest(), MigrationSHA256: gatewayMatrix.MigrationDigest, RuntimeRole: gatewayMatrix.Roles.Runtime, RuntimeInherits: true, WriterMembership: true, FullCapabilities: true, EffectiveTablePrivileges: true, EffectiveSequencePrivileges: true, EffectiveRoutinePrivileges: true},
				TargetSessionsZero: true, DMLProbeSHA256: strings.Repeat("c", 64),
			}, nil
		},
		RestoreConnect: func(context.Context) error { *calls = append(*calls, "connect"); return nil },
		VerifyRestoredTargetSessions: func(context.Context) (ActivationRestoredTargetSessionsObservation, error) {
			*calls = append(*calls, "verify-restored-sessions")
			return ActivationRestoredTargetSessionsObservation{SessionsZero: true, ProbeSHA256: strings.Repeat("d", 64)}, nil
		},
		StartServices: func(context.Context) error { *calls = append(*calls, "start"); return nil },
		VerifyRunningPostGrantDML: func(context.Context) (ActivationRunningPostGrantDMLObservation, error) {
			*calls = append(*calls, "verify-running-post-grant-dml")
			return ActivationRunningPostGrantDMLObservation{PlatformPassed: true, GatewayPassed: true, ProbeSHA256: strings.Repeat("c", 64)}, nil
		},
		VerifyActivation:        func(context.Context) error { *calls = append(*calls, "verify"); return nil },
		ContainPostGrantFailure: func(context.Context) error { *calls = append(*calls, "contain"); return nil },
		ObserveActivation: func(_ context.Context, input ActivationInput, value ActivationAttempt, inputSHA string, facts ActivationVerificationFacts) (ActivationObservations, error) {
			return ActivationObservations{
				SchemaVersion: 1, InputSHA256: inputSHA, Source: input.Source, Platform: input.Platform, Gateway: input.Gateway, Maintenance: input.Maintenance,
				SourceWriterRole: input.SourceWriterRole, PlatformMigratorRole: input.PlatformMigratorRole, PlatformRuntimeRole: input.PlatformRuntimeRole, PlatformWriterRole: input.PlatformWriterRole,
				GatewayMigratorRole: input.GatewayMigratorRole, GatewayRuntimeRole: input.GatewayRuntimeRole, GatewayWriterRole: input.GatewayWriterRole,
				GatewayBeforeSHA256: value.GatewayBeforeSHA256, GatewayAfterSHA256: value.GatewayAfterSHA256,
				GatewayPromotion:          facts.GatewayPromotion,
				PriorVerification:         facts.Prior,
				PreGrantPlatformAuthority: facts.PreGrant.Platform, PreGrantGatewayAuthority: facts.PreGrant.Gateway, PreGrantDMLProbeSHA256: facts.PreGrant.DMLProbeSHA256,
				PlatformAuthority: facts.PostGrant.Platform, GatewayAuthority: facts.PostGrant.Gateway, PostGrantDMLProbeSHA256: facts.RunningPostGrantDML.ProbeSHA256,
				SourceFenceVerified: true, TargetSessionsFenced: facts.Fence.SessionsZero && facts.PostGrant.TargetSessionsZero, TargetSessionFenceSHA256: facts.Fence.ProbeSHA256,
				RestoredTargetSessionsZero: facts.RestoredSessions.SessionsZero, RestoredTargetSessionSHA256: facts.RestoredSessions.ProbeSHA256,
				PostgresIdentitySHA256: strings.Repeat("5", 64), SupervisorStatusSHA256: strings.Repeat("6", 64), SupervisorTopologySHA256: strings.Repeat("7", 64), TestModeVerified: true, LivePrivateWritesDisabled: true, VenueWritesDisabled: true,
			}, nil
		},
		CompleteVerifiedAttempt: func(_ context.Context, input ActivationInput, value ActivationAttempt, inputSHA string) (ExecutionResult, error) {
			if err := os.MkdirAll(input.EvidenceDir, 0o700); err != nil {
				return ExecutionResult{}, err
			}
			path := filepath.Join(input.EvidenceDir, "activation-test-evidence.json")
			proof := operationProof{
				SchemaVersion: 1, Operation: Activate, Status: "pass", MigrationID: input.MigrationID, Subject: strings.Repeat("7", 64),
				Checks:      []string{"source-fenced", "gateway-promoted", "writers-granted", "connect-restored", "services-started", "activation-verified"},
				GeneratedBy: evidenceGenerator, PreStateSHA: strings.Repeat("8", 64), InputSHA: inputSHA,
				Commands: []commandEvidence{{Executable: "activation-test", ArgvSHA256: inputSHA, OutputSHA: strings.Repeat("9", 64)}}, ActivationObservations: value.Observations,
			}
			data, err := json.MarshalIndent(proof, "", "  ")
			if err != nil {
				return ExecutionResult{}, err
			}
			data = append(data, '\n')
			if err := os.WriteFile(path, data, 0o600); err != nil {
				return ExecutionResult{}, err
			}
			value.EvidencePath, value.ProofSHA256 = path, sha256Hex(data)
			copy := value
			*attempt = &copy
			return ExecutionResult{Result: Result{Phase: PhaseActivated}, EvidencePath: path, InputSHA256: inputSHA}, nil
		},
	}
}

type recordingMigrationPromotionStore struct {
	request storepkg.MigrationPromotionRequest
	result  storepkg.MigrationPromotionResult
	states  []storepkg.RuntimeState
	err     error
}

type activationLedgerQuerier struct {
	entries []authority.MigrationLedgerEntry
}

func (querier activationLedgerQuerier) QueryRow(context.Context, string, ...any) authority.CatalogRow {
	return nil
}

func (querier activationLedgerQuerier) Query(context.Context, string, ...any) (authority.CatalogRows, error) {
	return &activationLedgerRows{entries: querier.entries}, nil
}

type activationLedgerRows struct {
	entries []authority.MigrationLedgerEntry
	index   int
}

func (rows *activationLedgerRows) Next() bool { return rows.index < len(rows.entries) }

func (rows *activationLedgerRows) Scan(destinations ...any) error {
	entry := rows.entries[rows.index]
	rows.index++
	*destinations[0].(*int) = entry.Version
	*destinations[1].(*string) = entry.Checksum
	return nil
}

func (*activationLedgerRows) Err() error { return nil }
func (*activationLedgerRows) Close()     {}

type activationAuthorityOrderTransaction struct {
	t               *testing.T
	entries         []authority.MigrationLedgerEntry
	migratorAdmin   bool
	migratorInherit bool
	migratorSet     bool
	runtimeAdmin    bool
	runtimeInherit  bool
	runtimeSet      bool
	writerAdmin     bool
	writerInherit   bool
	writerSet       bool
	events          []string
}

func (transaction *activationAuthorityOrderTransaction) Exec(_ context.Context, query string, _ ...any) error {
	transaction.t.Helper()
	switch query {
	case `SET LOCAL ROLE "migrator_test"`:
		transaction.events = append(transaction.events, "set-migrator")
	case `GRANT CONNECT ON DATABASE "platform_test" TO "runtime_test"`:
		transaction.events = append(transaction.events, "grant-connect")
	case `RESET ROLE`:
		transaction.events = append(transaction.events, "reset-role")
	case `GRANT "runtime_test" TO "maintenance_test" WITH SET TRUE`:
		transaction.events = append(transaction.events, "grant-set")
	case `SET LOCAL ROLE "runtime_test"`:
		transaction.events = append(transaction.events, "set-runtime")
	default:
		transaction.t.Fatalf("unexpected activation authority Exec: %s", query)
	}
	return nil
}

func (transaction *activationAuthorityOrderTransaction) QueryRow(_ context.Context, query string, args ...any) authority.CatalogRow {
	transaction.t.Helper()
	if strings.Contains(query, "pg_auth_members") {
		if reflect.DeepEqual(args, []any{"migrator_test", "maintenance_test"}) {
			transaction.events = append(transaction.events, "verify-migrator-membership")
			return activationAuthorityOrderRow{scan: func(destinations ...any) error {
				*destinations[0].(*bool) = transaction.migratorAdmin
				*destinations[1].(*bool) = transaction.migratorInherit
				*destinations[2].(*bool) = transaction.migratorSet
				return nil
			}}
		}
		if reflect.DeepEqual(args, []any{"writer_test", "maintenance_test"}) {
			transaction.events = append(transaction.events, "verify-writer-membership")
			return activationAuthorityOrderRow{scan: func(destinations ...any) error {
				*destinations[0].(*bool) = transaction.writerAdmin
				*destinations[1].(*bool) = transaction.writerInherit
				*destinations[2].(*bool) = transaction.writerSet
				return nil
			}}
		}
		if !reflect.DeepEqual(args, []any{"runtime_test", "maintenance_test"}) {
			transaction.t.Fatalf("unexpected membership args=%v", args)
		}
		transaction.events = append(transaction.events, "verify-runtime-membership")
		return activationAuthorityOrderRow{scan: func(destinations ...any) error {
			*destinations[0].(*bool) = transaction.runtimeAdmin
			*destinations[1].(*bool) = transaction.runtimeInherit
			*destinations[2].(*bool) = transaction.runtimeSet
			return nil
		}}
	}
	transaction.events = append(transaction.events, "catalog")
	return activationAuthorityOrderRow{scan: func(...any) error { return errors.New("stop after catalog begins") }}
}

func (transaction *activationAuthorityOrderTransaction) Query(_ context.Context, query string, _ ...any) (authority.CatalogRows, error) {
	transaction.t.Helper()
	if query != `SELECT version,checksum FROM test_schema ORDER BY version` {
		transaction.t.Fatalf("ledger query=%s", query)
	}
	transaction.events = append(transaction.events, "ledger")
	return &activationLedgerRows{entries: transaction.entries}, nil
}

func (transaction *activationAuthorityOrderTransaction) Rollback(context.Context) error {
	transaction.events = append(transaction.events, "rollback")
	return nil
}

type activationAuthorityOrderRow struct {
	scan func(...any) error
}

func (row activationAuthorityOrderRow) Scan(destinations ...any) error {
	return row.scan(destinations...)
}

func writePromotionEvidenceFixture(t *testing.T, proof operationProof) (Manifest, string) {
	t.Helper()
	evidenceDir := t.TempDir()
	preBackfill := Manifest{MigrationID: "G010", Workflow: Workflow{Epoch: 1, Phase: PhaseFenced}}
	proof.Subject = proofStateDigest(preBackfill) + "/account-route-v1.json/" + strings.Repeat("d", 64)
	data, err := json.MarshalIndent(proof, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := filepath.Join(evidenceDir, "002-backfill.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{MigrationID: "G010", Workflow: Workflow{Events: []Event{
		{Sequence: 1, Epoch: 1, Operation: Fence, ProofSHA: strings.Repeat("f", 64)},
		{Sequence: 2, Epoch: 1, Operation: Backfill, Name: "account-route-v1.json", ArtifactSHA: strings.Repeat("d", 64), InputSHA: strings.Repeat("d", 64), ProofSHA: sha256Hex(data)},
	}}}
	return manifest, evidenceDir
}

func (store *recordingMigrationPromotionStore) PromoteMigrationPendingSet(_ context.Context, request storepkg.MigrationPromotionRequest) (storepkg.MigrationPromotionResult, error) {
	store.request = request
	return store.result, store.err
}

func (store *recordingMigrationPromotionStore) ListRuntimeStateForRecovery(context.Context) ([]storepkg.RuntimeState, error) {
	return append([]storepkg.RuntimeState(nil), store.states...), store.err
}
