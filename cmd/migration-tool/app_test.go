package main

import (
	"bytes"
	"context"
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
	executeMigration = func(ctx context.Context, request migration.ExecutionRequest, _ migration.ExecuteOptions) (migration.ExecutionResult, error) {
		return migration.Execute(ctx, request, migration.ExecuteOptions{Run: func(context.Context, migration.Command) (migration.CommandResult, error) {
			return migration.CommandResult{Output: []byte("backup complete")}, nil
		}})
	}
	var stdout, stderr bytes.Buffer
	importProof := writeCLIArtifact(t, "import-proof.json", `{"schemaVersion":1,"operation":"record-import","status":"pass","migrationId":"boltertrader-product-platform-upgrade","subject":"0123456789abcdef0123456789abcdef01234567","checks":["source-commit-matches","source-tree-matches","subtree-import-matches"]}`)
	if err := run([]string{"record-import", "--manifest", manifest, "--id", "0123456789abcdef0123456789abcdef01234567", "--proof", importProof}, &stdout, &stderr); err != nil {
		t.Fatalf("record import: %v", err)
	}
	stdout.Reset()
	if err := run([]string{"backup", "--manifest", manifest, "--id", "backup-001", "--artifact", artifact, "--evidence-dir", t.TempDir(), "--source-service", "funding-source"}, &stdout, &stderr); err != nil {
		t.Fatalf("backup: %v stderr=%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"phase":"backed-up"`) {
		t.Fatalf("backup output = %q", stdout.String())
	}
	stdout.Reset()
	if err := run([]string{"status", "--manifest", manifest}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"phase":"backed-up"`, `"activationPrepared":false`, `"retirementPrepared":false`} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("status %q missing %s", stdout.String(), want)
		}
	}
}

func TestRunRejectsCallerProofForExecutableOperation(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run([]string{"backup", "--manifest", "manifest.json", "--proof", "fabricated.json"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "--proof is not accepted") {
		t.Fatalf("error = %v", err)
	}
}

func writeCLIArtifact(t *testing.T, name, value string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunDoesNotExposeActivationOrRetirementExecutionCommands(t *testing.T) {
	for _, command := range []string{"activate", "retire"} {
		var stdout, stderr bytes.Buffer
		err := run([]string{command, "--manifest", "unused"}, &stdout, &stderr)
		if err == nil || !strings.Contains(err.Error(), "unsupported command") {
			t.Fatalf("%s error = %v", command, err)
		}
	}
}

func copyFixture(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "migration", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
