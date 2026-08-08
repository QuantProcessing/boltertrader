package migration

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkflowRejectsTamperedReplayWithoutMutation(t *testing.T) {
	manifestPath := adversarialManifestCopy(t, nil)
	artifactPath := adversarialArtifact(t, "backup.dump", "original backup")
	request := adversarialWithProof(t, manifestPath, Request{Operation: Backup, ID: "backup-001", ArtifactPath: artifactPath})
	if _, err := applyExecuted(manifestPath, request, ApplyOptions{}); err != nil {
		t.Fatalf("initial backup: %v", err)
	}
	if err := os.WriteFile(artifactPath, []byte("tampered backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := adversarialReadFile(t, manifestPath)

	_, err := applyExecuted(manifestPath, request, ApplyOptions{})
	if err == nil {
		t.Fatal("tampered replay was accepted")
	}
	if after := adversarialReadFile(t, manifestPath); string(after) != string(before) {
		t.Fatal("tampered replay changed the manifest")
	}
}

func TestWorkflowCompletedRestoreReplayIsIdempotent(t *testing.T) {
	manifestPath := adversarialManifestCopy(t, nil)
	artifactPath := adversarialArtifact(t, "backup.dump", "backup")
	backup := adversarialWithProof(t, manifestPath, Request{Operation: Backup, ID: "backup-001", ArtifactPath: artifactPath})
	restore := adversarialWithProof(t, manifestPath, Request{
		Operation:    Restore,
		ArtifactPath: artifactPath,
		Verifier:     "adversarial-test",
	})
	if _, err := applyExecuted(manifestPath, backup, ApplyOptions{}); err != nil {
		t.Fatalf("backup: %v", err)
	}
	if _, err := applyExecuted(manifestPath, restore, ApplyOptions{}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	before := adversarialReadFile(t, manifestPath)

	result, err := applyExecuted(manifestPath, restore, ApplyOptions{})
	if err != nil {
		t.Fatalf("restore replay: %v", err)
	}
	if !result.Replayed {
		t.Fatal("completed restore replay was not reported as replayed")
	}
	if after := adversarialReadFile(t, manifestPath); string(after) != string(before) {
		t.Fatal("completed restore replay changed the manifest")
	}
}

func TestWorkflowEvidenceRedactsArtifactContents(t *testing.T) {
	manifestPath := adversarialManifestCopy(t, nil)
	secret := "private-key-material-must-not-appear"
	artifactPath := adversarialArtifact(t, "backup.dump", secret)
	request := adversarialWithProof(t, manifestPath, Request{
		Operation:    Backup,
		ID:           "backup-001",
		ArtifactPath: artifactPath,
	})
	if _, err := applyExecuted(manifestPath, request, ApplyOptions{}); err != nil {
		t.Fatalf("backup: %v", err)
	}

	data := adversarialReadFile(t, manifestPath)
	if strings.Contains(string(data), secret) {
		t.Fatal("migration evidence persisted artifact contents")
	}
	wantDigest := fmt.Sprintf("%x", sha256.Sum256([]byte(secret)))
	if !strings.Contains(string(data), wantDigest) {
		t.Fatal("migration evidence did not persist the artifact digest")
	}
}

func TestWorkflowFailedTransitionIsAtomic(t *testing.T) {
	manifestPath := adversarialManifestCopy(t, nil)
	backupPath := adversarialArtifact(t, "backup.dump", "backup")
	request := adversarialWithProof(t, manifestPath, Request{
		Operation:    Backup,
		ID:           "backup-001",
		ArtifactPath: backupPath,
	})
	if _, err := applyExecuted(manifestPath, request, ApplyOptions{}); err != nil {
		t.Fatalf("backup: %v", err)
	}
	before := adversarialReadFile(t, manifestPath)

	_, err := applyExecuted(manifestPath, Request{
		Operation:    Restore,
		ArtifactPath: backupPath,
		ProofPath:    filepath.Join(t.TempDir(), "missing-proof.json"),
		Verifier:     "adversarial-test",
	}, ApplyOptions{})
	if err == nil {
		t.Fatal("restore accepted missing proof")
	}
	if after := adversarialReadFile(t, manifestPath); string(after) != string(before) {
		t.Fatal("failed restore changed the manifest")
	}
}

func TestWorkflowErrorsRedactSecretBearingPaths(t *testing.T) {
	manifestPath := adversarialManifestCopy(t, nil)
	secret := "do-not-leak-private-key"
	_, err := applyExecuted(manifestPath, Request{
		Operation:    Backup,
		ID:           "backup-001",
		ArtifactPath: filepath.Join(t.TempDir(), secret),
	}, ApplyOptions{})
	if err == nil {
		t.Fatal("backup accepted a missing artifact")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("backup error leaked secret-bearing path: %v", err)
	}
}

func TestWorkflowDoesNotMutateSourceTree(t *testing.T) {
	sourceDir := t.TempDir()
	sentinelPath := filepath.Join(sourceDir, "sentinel.txt")
	if err := os.WriteFile(sentinelPath, []byte("frozen source"), 0o444); err != nil {
		t.Fatal(err)
	}
	manifestPath := adversarialRawManifestCopy(t, func(manifest map[string]any) {
		manifest["sourceRepo"] = sourceDir
	})
	beforeInfo, err := os.Stat(sentinelPath)
	if err != nil {
		t.Fatal(err)
	}

<<<<<<< HEAD
	if _, err := Apply(manifestPath, adversarialImportRequest(t), ApplyOptions{}); err == nil || !strings.Contains(err.Error(), "frozen") {
		t.Fatalf("source repository tamper error = %v, want frozen-identity rejection", err)
=======
	request := adversarialWithProof(t, manifestPath, Request{
		Operation:    Backup,
		ID:           "backup-001",
		ArtifactPath: artifactPath,
	})
	if _, err := applyExecuted(manifestPath, request, ApplyOptions{}); err == nil || !strings.Contains(err.Error(), "identity is not frozen") {
		t.Fatalf("tampered source identity error = %v", err)
>>>>>>> a678e34
	}
	afterInfo, err := os.Stat(sentinelPath)
	if err != nil {
		t.Fatalf("source sentinel disappeared: %v", err)
	}
	if got := string(adversarialReadFile(t, sentinelPath)); got != "frozen source" {
		t.Fatalf("source sentinel content = %q", got)
	}
	if !afterInfo.ModTime().Equal(beforeInfo.ModTime()) || afterInfo.Mode() != beforeInfo.Mode() {
		t.Fatal("migration changed source sentinel metadata")
	}
	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "sentinel.txt" {
		t.Fatalf("migration changed source tree entries: %v", entries)
	}
}

func adversarialManifestCopy(t *testing.T, mutate func(map[string]any)) string {
	t.Helper()
	stubGitProvenance(t)
<<<<<<< HEAD
	path := adversarialRawManifestCopy(t, mutate)
	if _, err := Apply(path, adversarialImportRequest(t), ApplyOptions{}); err != nil {
		t.Fatalf("record import fixture: %v", err)
	}
	return path
}

func adversarialRawManifestCopy(t *testing.T, mutate func(map[string]any)) string {
	t.Helper()
=======
>>>>>>> a678e34
	var manifest map[string]any
	if err := json.Unmarshal(readManifest(t), &manifest); err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(manifest)
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
<<<<<<< HEAD
	return path
}

func adversarialImportRequest(t *testing.T) Request {
	t.Helper()
=======
	if mutate != nil {
		return path
	}
>>>>>>> a678e34
	const importCommit = "0123456789abcdef0123456789abcdef01234567"
	proofPath := adversarialArtifact(t, "import-proof.json", `{
		"schemaVersion": 1,
		"operation": "record-import",
		"status": "pass",
		"migrationId": "boltertrader-product-platform-upgrade",
		"subject": "`+importCommit+`",
		"checks": ["source-commit-matches", "source-tree-matches", "subtree-import-matches"]
	}`)
<<<<<<< HEAD
	return Request{
=======
	if _, err := applyExecuted(path, Request{
>>>>>>> a678e34
		Operation: RecordImport,
		ID:        importCommit,
		ProofPath: proofPath,
	}
}

func adversarialArtifact(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func adversarialWithProof(t *testing.T, manifestPath string, request Request) Request {
	t.Helper()
	artifactSHA, err := digestFile(request.ArtifactPath, true)
	if err != nil {
		t.Fatal(err)
	}
	proof := operationProof{
		SchemaVersion: 1,
		Operation:     request.Operation,
		Status:        "pass",
		MigrationID:   "boltertrader-product-platform-upgrade",
		Subject:       artifactSHA,
	}
	switch request.Operation {
	case Backup:
		proof.Checks = []string{"dump-created", "dump-format-readable"}
	case Restore:
		proof.Checks = []string{"clean-target-created", "restored-schema-nonempty"}
	default:
		t.Fatalf("unsupported adversarial proof operation %q for %s", request.Operation, manifestPath)
	}
	data, err := json.Marshal(proof)
	if err != nil {
		t.Fatal(err)
	}
	request.ProofPath = adversarialArtifact(t, string(request.Operation)+"-proof.json", string(data))
	return request
}

func adversarialReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
