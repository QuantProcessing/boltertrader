package migration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVerifyEvidenceRevalidatesGeneratedImportProofAndGitProvenance(t *testing.T) {
	manifestPath := copyManifest(t)
	evidenceDir := t.TempDir()
	importCommit := strings.Repeat("a", 40)
	result, err := Execute(context.Background(), ExecutionRequest{
		Operation: RecordImport, ManifestPath: manifestPath, EvidenceDir: evidenceDir, ID: importCommit,
	}, ExecuteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	request := EvidenceVerificationRequest{
		ManifestPath: manifestPath, EvidenceDir: evidenceDir, ImportCommit: importCommit,
	}
	if err := VerifyEvidence(request); err != nil {
		t.Fatalf("VerifyEvidence() error = %v", err)
	}

	proof, err := os.ReadFile(result.EvidencePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(result.EvidencePath, append(proof, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyEvidence(request); err == nil || !strings.Contains(err.Error(), "record-import evidence changed") {
		t.Fatalf("tampered VerifyEvidence() error = %v", err)
	}
	if err := os.WriteFile(result.EvidencePath, proof, 0o600); err != nil {
		t.Fatal(err)
	}

	prior := validateGitProvenance
	validateGitProvenance = func(Manifest, string) error { return fmt.Errorf("git provenance rejected: test drift") }
	t.Cleanup(func() { validateGitProvenance = prior })
	if err := VerifyEvidence(request); err == nil || !strings.Contains(err.Error(), "git provenance rejected") {
		t.Fatalf("drifted VerifyEvidence() error = %v", err)
	}
}

func TestVerifyEvidenceRevalidatesActivationProof(t *testing.T) {
	manifestPath := copyManifest(t)
	evidenceDir := t.TempDir()
	importCommit := strings.Repeat("a", 40)
	if _, err := Execute(context.Background(), ExecutionRequest{
		Operation: RecordImport, ManifestPath: manifestPath, EvidenceDir: evidenceDir, ID: importCommit,
	}, ExecuteOptions{}); err != nil {
		t.Fatal(err)
	}

	backup := writeArtifact(t, "backup.dump", "verified backup")
	delta := writeArtifact(t, "delta.dump", "verified delta")
	steps := []Request{
		{Operation: Backup, ID: "backup-001", ArtifactPath: backup},
		{Operation: Restore, ArtifactPath: backup, Verifier: "test"},
		{Operation: FinalDelta, ArtifactPath: delta},
		{Operation: Fence},
		{Operation: Backfill, Name: "mapping.json"},
		{Operation: Validate, Verifier: "test"},
		{Operation: PrepareActivation},
	}
	importAt, err := time.Parse(time.RFC3339, readWorkflowManifest(t, manifestPath).Workflow.Events[0].At)
	if err != nil {
		t.Fatal(err)
	}
	now := importAt
	for index, step := range steps {
		step = withProof(t, manifestPath, step)
		if _, err := applyExecuted(manifestPath, step, ApplyOptions{Now: func() time.Time {
			return now.Add(time.Duration(index+1) * time.Minute)
		}}); err != nil {
			t.Fatalf("apply %s: %v", step.Operation, err)
		}
	}
	activation := withProof(t, manifestPath, Request{
		Operation: RecordActivation, Verifier: "test",
		ActivationAt: now.Add(7 * time.Minute).Format(time.RFC3339),
		CutoverAt:    now.Add(8 * time.Minute).Format(time.RFC3339),
	})
	if _, err := applyExecuted(manifestPath, activation, ApplyOptions{Now: func() time.Time {
		return now.Add(9 * time.Minute)
	}}); err != nil {
		t.Fatal(err)
	}
	request := EvidenceVerificationRequest{
		ManifestPath: manifestPath, EvidenceDir: evidenceDir, ImportCommit: importCommit,
		ActivationProofPath: activation.ProofPath,
	}
	if err := VerifyEvidence(request); err != nil {
		t.Fatalf("VerifyEvidence() error = %v", err)
	}

	tampered := filepath.Join(t.TempDir(), "activation-proof.json")
	if err := os.WriteFile(tampered, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	request.ActivationProofPath = tampered
	if err := VerifyEvidence(request); err == nil || !strings.Contains(err.Error(), "record-activation-evidence evidence changed") {
		t.Fatalf("tampered VerifyEvidence() error = %v", err)
	}
}
