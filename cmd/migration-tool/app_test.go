package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunBackupAndStatus(t *testing.T) {
	manifest := copyFixture(t)
	artifact := filepath.Join(t.TempDir(), "backup.dump")
	if err := os.WriteFile(artifact, []byte("backup data"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	importProof := writeCLIArtifact(t, "import-proof.json", `{"schemaVersion":1,"operation":"record-import","status":"pass","migrationId":"boltertrader-product-platform-upgrade","subject":"0123456789abcdef0123456789abcdef01234567","checks":["source-commit-matches","source-tree-matches","subtree-import-matches"]}`)
	if err := run([]string{"record-import", "--manifest", manifest, "--id", "0123456789abcdef0123456789abcdef01234567", "--proof", importProof}, &stdout, &stderr); err != nil {
		t.Fatalf("record import: %v", err)
	}
	stdout.Reset()
	backupSHA := fmt.Sprintf("%x", sha256.Sum256([]byte("backup data")))
	backupProof := writeCLIArtifact(t, "backup-proof.json", fmt.Sprintf(`{"schemaVersion":1,"operation":"backup","status":"pass","migrationId":"boltertrader-product-platform-upgrade","subject":%q,"checks":["consistent-snapshot","restorable-format"]}`, backupSHA))
	if err := run([]string{"backup", "--manifest", manifest, "--id", "backup-001", "--artifact", artifact, "--proof", backupProof}, &stdout, &stderr); err != nil {
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
