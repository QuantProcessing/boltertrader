package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/migration"
)

func TestRunBackupAndStatus(t *testing.T) {
	manifest := copyFixture(t)
	artifact := filepath.Join(t.TempDir(), "backup.dump")
	if err := os.WriteFile(artifact, []byte("backup data"), 0o600); err != nil {
		t.Fatal(err)
	}
	originalExecute := executeMigration
	t.Cleanup(func() { executeMigration = originalExecute })
	var executed migration.ExecutionRequest
	executeMigration = func(_ context.Context, request migration.ExecutionRequest, _ migration.ExecuteOptions) (migration.ExecutionResult, error) {
		executed = request
		return migration.ExecutionResult{Result: migration.Result{Phase: migration.PhaseBackedUp}}, nil
	}
	var stdout, stderr bytes.Buffer
	if err := run([]string{"backup", "--manifest", manifest, "--id", "backup-001", "--artifact", artifact, "--evidence-dir", t.TempDir(), "--source-service", "funding-source"}, &stdout, &stderr); err != nil {
		t.Fatalf("backup: %v stderr=%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"phase":"backed-up"`) {
		t.Fatalf("backup output = %q", stdout.String())
	}
	if executed.Operation != migration.Backup || executed.ManifestPath != manifest || executed.SourceService != "funding-source" {
		t.Fatalf("execution request = %+v", executed)
	}
	stdout.Reset()
	if err := run([]string{"status", "--manifest", manifest}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"phase":"baseline"`, `"activationPrepared":false`, `"retirementPrepared":false`} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("status %q missing %s", stdout.String(), want)
		}
	}
}

func TestStatusOutputExposesOnlyActivationAttemptBoundary(t *testing.T) {
	manifest := migration.Manifest{
		Workflow: migration.Workflow{Phase: migration.PhaseActivationReady},
		ActivationAttempt: &migration.ActivationAttempt{
			ID:       "private-attempt-id",
			Boundary: migration.ActivationConnectRestored,
		},
	}
	data, err := json.Marshal(statusOutput(manifest))
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.Contains(got, `"activationAttemptBoundary":"connect-restored"`) {
		t.Fatalf("status output = %s", got)
	}
	if strings.Contains(got, "private-attempt-id") {
		t.Fatalf("status output leaked activation attempt details: %s", got)
	}
}

func TestRunRejectsCallerProofForExecutableOperation(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run([]string{"backup", "--manifest", "manifest.json", "--proof", "fabricated.json"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "--proof is not accepted") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunWiresFinalSnapshotDatabaseAndMaintenanceInputs(t *testing.T) {
	originalExecute := executeMigration
	t.Cleanup(func() { executeMigration = originalExecute })
	var executed migration.ExecutionRequest
	executeMigration = func(_ context.Context, request migration.ExecutionRequest, _ migration.ExecuteOptions) (migration.ExecutionResult, error) {
		executed = request
		return migration.ExecutionResult{Result: migration.Result{Phase: migration.PhaseDeltaCaptured}}, nil
	}
	var stdout, stderr bytes.Buffer
	err := run([]string{
		"final-delta", "--manifest", copyFixture(t), "--artifact", "final.dump", "--evidence-dir", "evidence",
		"--source-service", "source", "--source-database", "source_test", "--source-role", "source_test_migrator",
		"--target-service", "target", "--target-database", "target_test", "--target-role", "target_test_migrator",
		"--maintenance-service", "target_admin", "--maintenance-database", "postgres_test", "--maintenance-role", "target_test_admin",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("final-delta: %v stderr=%s", err, stderr.String())
	}
	if executed.SourceDatabase != "source_test" || executed.SourceRole != "source_test_migrator" ||
		executed.TargetDatabase != "target_test" || executed.TargetRole != "target_test_migrator" ||
		executed.MaintenanceService != "target_admin" || executed.MaintenanceDatabase != "postgres_test" || executed.MaintenanceRole != "target_test_admin" {
		t.Fatalf("execution request = %+v", executed)
	}
}

func TestRunActivateIsExecutableAndRetirementIsUnsupported(t *testing.T) {
	originalExecute := executeMigration
	t.Cleanup(func() { executeMigration = originalExecute })
	var executed migration.ExecutionRequest
	executeMigration = func(_ context.Context, request migration.ExecutionRequest, _ migration.ExecuteOptions) (migration.ExecutionResult, error) {
		executed = request
		return migration.ExecutionResult{Result: migration.Result{Phase: migration.PhaseActivated}}, nil
	}
	var stdout, stderr bytes.Buffer
	err := run([]string{
		"activate", "--manifest", "manifest.json", "--evidence-dir", "evidence", "--journal", "evidence/attempt.json", "--state-dir", "runtime/state/test",
		"--backup-id", "g010-test-backup", "--backup-artifact", "runtime/migration/test/backup.dump",
		"--final-delta-artifact", "runtime/migration/test/final-delta.dump", "--backfill-mapping", "migration/backfills/account-route-v1.json", "--verifier", "g010-cutover",
		"--source-service", "source_test", "--source-database", "source_test", "--source-role", "source_test",
		"--target-service", "platform_test", "--target-database", "platform_test", "--target-role", "platform_test_migrator", "--target-runtime-role", "platform_test", "--target-writer-role", "platform_test_writer",
		"--gateway-service", "gateway_test", "--gateway-database", "gateway_test", "--gateway-role", "gateway_test_migrator", "--gateway-runtime-role", "gateway_test", "--gateway-writer-role", "gateway_test_writer",
		"--maintenance-service", "maintenance_test", "--maintenance-database", "maintenance_test", "--maintenance-role", "maintenance_test",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if executed.Operation != migration.Activate || executed.TargetRole != "platform_test_migrator" || executed.TargetRuntimeRole != "platform_test" ||
		executed.GatewayRole != "gateway_test_migrator" || executed.GatewayRuntimeRole != "gateway_test" ||
		executed.BackupID != "g010-test-backup" || executed.BackupArtifactPath != "runtime/migration/test/backup.dump" ||
		executed.FinalDeltaArtifactPath != "runtime/migration/test/final-delta.dump" || executed.BackfillPath != "migration/backfills/account-route-v1.json" || executed.Verifier != "g010-cutover" ||
		executed.GatewayWriterRole != "gateway_test_writer" || executed.JournalPath != "evidence/attempt.json" || executed.StateDir != "runtime/state/test" {
		t.Fatalf("execution request = %+v", executed)
	}
	for _, flag := range []string{"--proof", "--activation-at", "--cutover-at"} {
		stdout.Reset()
		stderr.Reset()
		if err := run([]string{"activate", "--manifest", "manifest.json", flag, "caller-value"}, &stdout, &stderr); err == nil || !strings.Contains(err.Error(), "not accepted") {
			t.Fatalf("%s error = %v", flag, err)
		}
	}
	if err := run([]string{"retire", "--manifest", "unused"}, &stdout, &stderr); err == nil || !strings.Contains(err.Error(), "unsupported command") {
		t.Fatalf("retire error = %v", err)
	}
}

func TestRunActivateRejectsMissingOrEmptyReplayInputs(t *testing.T) {
	originalExecute := executeMigration
	t.Cleanup(func() { executeMigration = originalExecute })
	executeCalls := 0
	executeMigration = func(_ context.Context, _ migration.ExecutionRequest, _ migration.ExecuteOptions) (migration.ExecutionResult, error) {
		executeCalls++
		return migration.ExecutionResult{}, nil
	}
	base := []string{
		"activate", "--manifest", "manifest.json",
		"--backup-id", "g010-test-backup",
		"--backup-artifact", "runtime/migration/test/backup.dump",
		"--final-delta-artifact", "runtime/migration/test/final-delta.dump",
		"--backfill-mapping", "migration/backfills/account-route-v1.json",
		"--verifier", "g010-cutover",
	}
	for _, flagName := range []string{"--backup-id", "--backup-artifact", "--final-delta-artifact", "--backfill-mapping", "--verifier"} {
		flagName := flagName
		for _, form := range []string{"missing", "empty"} {
			form := form
			t.Run(strings.TrimPrefix(flagName, "--")+"/"+form, func(t *testing.T) {
				args := append([]string(nil), base...)
				for index := range args {
					if args[index] != flagName {
						continue
					}
					if form == "missing" {
						args = append(args[:index], args[index+2:]...)
					} else {
						args[index+1] = "  "
					}
					break
				}
				var stdout, stderr bytes.Buffer
				err := run(args, &stdout, &stderr)
				if err == nil || !strings.Contains(err.Error(), flagName) {
					t.Fatalf("error = %v, want rejection naming %s", err, flagName)
				}
			})
		}
	}
	if executeCalls != 0 {
		t.Fatalf("executor called %d times for invalid activation requests", executeCalls)
	}
}

func TestRunVerifyEvidenceUsesReadOnlyStructuredVerifier(t *testing.T) {
	manifest := copyFixture(t)
	before, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	err = run([]string{
		"verify-evidence", "--manifest", manifest, "--evidence-dir", t.TempDir(),
		"--import-commit", strings.Repeat("a", 40),
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("verify-evidence: %v stderr=%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"phase":"baseline"`) {
		t.Fatalf("verify-evidence output = %q", stdout.String())
	}
	data, readErr := os.ReadFile(manifest)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != string(before) {
		t.Fatal("verify-evidence mutated the manifest")
	}
}

func TestRunValidationCompareAllowsOnlyNamedSessionLifecycle(t *testing.T) {
	t.Setenv("FT_MODE", "test")
	base := migration.ValidationSnapshot{MatrixSHA256: strings.Repeat("a", 64), PlatformCanonicalSHA256: strings.Repeat("b", 64), PlatformMigrationSHA256: strings.Repeat("c", 64), GatewayActiveSHA256: strings.Repeat("d", 64), AuthSessionDigests: map[string]string{}}
	allowedID := "browser-session"
	after := base
	after.AuthSessionDigests = map[string]string{sha256String(allowedID): strings.Repeat("e", 64)}
	beforePath, afterPath := filepath.Join(t.TempDir(), "before.json"), filepath.Join(t.TempDir(), "after.json")
	if err := writePrivateJSON(beforePath, base); err != nil {
		t.Fatal(err)
	}
	if err := writePrivateJSON(afterPath, after); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if err := run([]string{"validation-compare", "--before", beforePath, "--after", afterPath, "--allowed-session-id", allowedID}, &stdout, &stderr); err != nil {
		t.Fatalf("compare: %v stderr=%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"sessionLifecycleAllowed":true`) {
		t.Fatalf("output=%s", stdout.String())
	}
	after.AuthSessionDigests[sha256String("other-session")] = strings.Repeat("f", 64)
	if err := writePrivateJSON(afterPath, after); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if err := run([]string{"validation-compare", "--before", beforePath, "--after", afterPath, "--allowed-session-id", allowedID}, &stdout, &stderr); err == nil || !strings.Contains(err.Error(), "drift") {
		t.Fatalf("drift error=%v", err)
	}
}

func TestReadPrivateJSONRejectsAmbiguousDocuments(t *testing.T) {
	type nestedValue struct {
		Name string `json:"name"`
	}
	type document struct {
		Name   string      `json:"name"`
		Nested nestedValue `json:"nested"`
	}

	for _, tc := range []struct {
		name       string
		data       string
		wantError  string
		wantResult document
	}{
		{
			name:       "valid",
			data:       `{"name":"first","nested":{"name":"second"}}`,
			wantResult: document{Name: "first", Nested: nestedValue{Name: "second"}},
		},
		{
			name:      "trailing JSON value",
			data:      `{"name":"first","nested":{"name":"second"}} {"name":"replacement","nested":{"name":"other"}}`,
			wantError: "one JSON value",
		},
		{
			name:      "duplicate top-level key",
			data:      `{"name":"first","name":"replacement","nested":{"name":"second"}}`,
			wantError: "duplicate key",
		},
		{
			name:      "duplicate nested key",
			data:      `{"name":"first","nested":{"name":"second","name":"replacement"}}`,
			wantError: "duplicate key",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "snapshot.json")
			if err := os.WriteFile(path, []byte(tc.data), 0o600); err != nil {
				t.Fatal(err)
			}
			var got document
			err := readPrivateJSON(path, &got)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("readPrivateJSON error=%v, want %q", err, tc.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("readPrivateJSON: %v", err)
			}
			if got != tc.wantResult {
				t.Fatalf("readPrivateJSON result=%+v, want %+v", got, tc.wantResult)
			}
		})
	}
}

func sha256String(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func copyFixture(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "migration", "testdata", "manifest.baseline.json"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
