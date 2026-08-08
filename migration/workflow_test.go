package migration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWorkflowPreparesActivationWithoutActivatingTarget(t *testing.T) {
	manifestPath := copyManifest(t)
	backup := writeArtifact(t, "backup.dump", "verified backup")
	delta := writeArtifact(t, "final-delta.sql", "final delta")
	now := time.Date(2026, 8, 9, 1, 0, 0, 0, time.UTC)

	steps := []Request{
		{Operation: RecordImport, ID: "0123456789abcdef0123456789abcdef01234567"},
		{Operation: Backup, ID: "backup-001", ArtifactPath: backup},
		{Operation: Restore, ArtifactPath: backup, Verifier: "migration-test"},
		{Operation: Fence},
		{Operation: FinalDelta, ArtifactPath: delta},
		{Operation: Backfill, Name: "001-platform.sql"},
		{Operation: Validate},
		{Operation: PrepareActivation},
	}
	for index, request := range steps {
		request = withProof(t, manifestPath, request)
		if _, err := applyExecuted(manifestPath, request, ApplyOptions{Now: func() time.Time {
			return now.Add(time.Duration(index+1) * time.Minute)
		}}); err != nil {
			t.Fatalf("apply %s: %v", request.Operation, err)
		}
	}

	manifest := readWorkflowManifest(t, manifestPath)
	if manifest.Workflow.Phase != PhaseActivationReady {
		t.Fatalf("phase = %q, want %q", manifest.Workflow.Phase, PhaseActivationReady)
	}
	if manifest.TargetActivationAt != nil || manifest.CutoverAt != nil {
		t.Fatalf("preparation claimed activation: target=%v cutover=%v", manifest.TargetActivationAt, manifest.CutoverAt)
	}
	if manifest.OneWriter.TargetCanonicalWritesEnabled {
		t.Fatal("preparation enabled target canonical writes")
	}
	if len(manifest.Workflow.Events) != len(steps) {
		t.Fatalf("events = %d, want %d", len(manifest.Workflow.Events), len(steps))
	}
	for index, event := range manifest.Workflow.Events {
		if event.Sequence != index+1 {
			t.Fatalf("event %d sequence = %d", index, event.Sequence)
		}
	}
}

func TestWorkflowRejectsOutOfOrderStepWithoutChangingManifest(t *testing.T) {
	manifestPath := copyManifest(t)
	before, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = applyExecuted(manifestPath, Request{Operation: Restore}, ApplyOptions{})
	if err == nil || !strings.Contains(err.Error(), "requires backup") {
		t.Fatalf("restore error = %v, want ordering rejection", err)
	}
	after, readErr := os.ReadFile(manifestPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(after) != string(before) {
		t.Fatal("rejected transition changed manifest")
	}
}

func TestWorkflowReplayIsIdempotentAndConflictsFailClosed(t *testing.T) {
	manifestPath := copyManifest(t)
	if _, err := applyExecuted(manifestPath, withProof(t, manifestPath, Request{Operation: RecordImport, ID: "0123456789abcdef0123456789abcdef01234567"}), ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	artifact := writeArtifact(t, "backup.dump", "verified backup")
	request := withProof(t, manifestPath, Request{Operation: Backup, ID: "backup-001", ArtifactPath: artifact})
	if _, err := applyExecuted(manifestPath, request, ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	result, err := applyExecuted(manifestPath, request, ApplyOptions{})
	if err != nil {
		t.Fatalf("idempotent replay: %v", err)
	}
	if !result.Replayed {
		t.Fatal("same transition was not reported as replay")
	}
	if got := len(readWorkflowManifest(t, manifestPath).Workflow.Events); got != 2 {
		t.Fatalf("replay appended event: count=%d", got)
	}

	other := writeArtifact(t, "other.dump", "different backup")
	_, err = applyExecuted(manifestPath, withProof(t, manifestPath, Request{Operation: Backup, ID: "backup-001", ArtifactPath: other}), ApplyOptions{})
	if err == nil || !strings.Contains(err.Error(), "replay conflict") {
		t.Fatalf("conflicting replay error = %v", err)
	}
}

func TestWorkflowRollbackAllowedBeforeBackfill(t *testing.T) {
	manifestPath := preparedThroughFence(t)
	result, err := applyExecuted(manifestPath, withProof(t, manifestPath, Request{Operation: Rollback, Reason: "pre-boundary verification failed"}), ApplyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Phase != PhaseRestored {
		t.Fatalf("rollback phase = %q", result.Phase)
	}
	manifest := readWorkflowManifest(t, manifestPath)
	if manifest.FinalDeltaAt != nil || manifest.FinalDeltaSHA256 != nil || manifest.OneWriterFenceAt != nil {
		t.Fatal("rollback retained delta or fence evidence")
	}
	if manifest.OneWriter.Owner != "funding-trader" || manifest.OneWriter.FenceRaised || manifest.OneWriter.TargetCanonicalWritesEnabled {
		t.Fatalf("rollback writer state = %+v", manifest.OneWriter)
	}
}

func TestWorkflowRequiresForwardRecoveryAfterBackfill(t *testing.T) {
	manifestPath := preparedThroughFence(t)
	if _, err := applyExecuted(manifestPath, withProof(t, manifestPath, Request{Operation: Backfill, Name: "001-platform.sql"}), ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	_, err := applyExecuted(manifestPath, Request{Operation: Rollback, Reason: "too late"}, ApplyOptions{})
	if err == nil || !strings.Contains(err.Error(), "forward recovery required") {
		t.Fatalf("post-boundary rollback error = %v", err)
	}
}

func TestWorkflowReappliesIdenticalDeltaAndFenceAfterRollback(t *testing.T) {
	manifestPath := preparedThroughFence(t)
	if _, err := applyExecuted(manifestPath, withProof(t, manifestPath, Request{Operation: Rollback, Reason: "retry preparation"}), ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	result, err := applyExecuted(manifestPath, withProof(t, manifestPath, Request{Operation: Fence}), ApplyOptions{})
	if err != nil || result.Replayed || result.Phase != PhaseFenced {
		t.Fatalf("fence retry result=%+v err=%v", result, err)
	}
	delta := writeArtifact(t, "delta.sql", "final delta")
	result, err = applyExecuted(manifestPath, withProof(t, manifestPath, Request{Operation: FinalDelta, ArtifactPath: delta}), ApplyOptions{})
	if err != nil || result.Replayed || result.Phase != PhaseDeltaCaptured {
		t.Fatalf("delta retry result=%+v err=%v", result, err)
	}
}

func TestWorkflowRejectsStaleFenceProofAfterRollbackEpoch(t *testing.T) {
	manifestPath := copyManifest(t)
	backup := writeArtifact(t, "backup.dump", "verified backup")
	delta := writeArtifact(t, "delta.sql", "final delta")
	for _, request := range []Request{
		{Operation: RecordImport, ID: "0123456789abcdef0123456789abcdef01234567"},
		{Operation: Backup, ID: "backup-001", ArtifactPath: backup},
		{Operation: Restore, ArtifactPath: backup, Verifier: "test"},
	} {
		if _, err := applyExecuted(manifestPath, withProof(t, manifestPath, request), ApplyOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	staleFence := withProof(t, manifestPath, Request{Operation: Fence})
	if _, err := applyExecuted(manifestPath, staleFence, ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := applyExecuted(manifestPath, withProof(t, manifestPath, Request{Operation: FinalDelta, ArtifactPath: delta}), ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := applyExecuted(manifestPath, withProof(t, manifestPath, Request{Operation: Rollback, Reason: "new epoch"}), ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := applyExecuted(manifestPath, withProof(t, manifestPath, Request{Operation: Fence}), ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := applyExecuted(manifestPath, staleFence, ApplyOptions{}); err == nil || !strings.Contains(err.Error(), "proof") {
		t.Fatalf("stale fence proof error = %v", err)
	}
}

func TestWorkflowPreservesFirstIrreversibleBackfillBoundary(t *testing.T) {
	manifestPath := preparedThroughFence(t)
	if _, err := applyExecuted(manifestPath, withProof(t, manifestPath, Request{Operation: Backfill, Name: "001-platform.sql"}), ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	first := readWorkflowManifest(t, manifestPath).Workflow
	if _, err := applyExecuted(manifestPath, withProof(t, manifestPath, Request{Operation: Backfill, Name: "002-gateway.sql"}), ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	got := readWorkflowManifest(t, manifestPath).Workflow
	if *got.IrreversibleAt != *first.IrreversibleAt || *got.IrreversibleStep != "001-platform.sql" {
		t.Fatalf("irreversible boundary changed: %+v", got)
	}
}

func TestWorkflowRejectsTamperedPhaseThatSkipsEvidence(t *testing.T) {
	manifestPath := copyManifest(t)
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	tampered := strings.TrimSpace(string(data))
	tampered = strings.TrimSuffix(tampered, "}") + `,"workflow":{"phase":"validated"}}`
	if err := os.WriteFile(manifestPath, []byte(tampered), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = applyExecuted(manifestPath, Request{Operation: PrepareActivation}, ApplyOptions{})
	if err == nil || !strings.Contains(err.Error(), "workflow state is inconsistent") {
		t.Fatalf("tampered workflow error = %v", err)
	}
}

func TestWorkflowRejectsFutureEvidenceInEarlierPhase(t *testing.T) {
	manifestPath := copyManifest(t)
	if _, err := applyExecuted(manifestPath, withProof(t, manifestPath, Request{Operation: RecordImport, ID: "0123456789abcdef0123456789abcdef01234567"}), ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	manifest := readWorkflowManifest(t, manifestPath)
	manifest.BackupID = stringPtr("injected")
	manifest.BackupSHA256 = stringPtr(strings.Repeat("a", 64))
	if err := writeManifestAtomic(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	_, err := applyExecuted(manifestPath, Request{Operation: Backup, ID: "real", ArtifactPath: writeArtifact(t, "backup", "data")}, ApplyOptions{})
	if err == nil || !strings.Contains(err.Error(), "workflow state is inconsistent") {
		t.Fatalf("future evidence error = %v", err)
	}
}

func TestWorkflowRejectsTamperedLedgerSequence(t *testing.T) {
	manifestPath := preparedThroughFence(t)
	manifest := readWorkflowManifest(t, manifestPath)
	manifest.Workflow.Events[2].Sequence = 99
	if err := writeManifestAtomic(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateWorkflow(data); err == nil || !strings.Contains(err.Error(), "ledger") {
		t.Fatalf("tampered ledger error = %v", err)
	}
}

func TestWorkflowRejectsGenericUntypedProof(t *testing.T) {
	manifestPath := copyManifest(t)
	proof := writeArtifact(t, "proof.json", `{"status":"pass"}`)
	request := Request{Operation: RecordImport, ID: "0123456789abcdef0123456789abcdef01234567", ProofPath: proof}
	if _, err := applyExecuted(manifestPath, request, ApplyOptions{}); err == nil || !strings.Contains(err.Error(), "operation proof") {
		t.Fatalf("generic proof error = %v", err)
	}
}

func TestWorkflowRejectsUnsafeBackfillName(t *testing.T) {
	manifestPath := preparedThroughFence(t)
	_, err := applyExecuted(manifestPath, withProof(t, manifestPath, Request{Operation: Backfill, Name: "../unsafe.sql"}), ApplyOptions{})
	if err == nil || !strings.Contains(err.Error(), "backfill name") {
		t.Fatalf("unsafe backfill error = %v", err)
	}
}

func TestWorkflowPreparesRetirementOnlyAfterExternallyRecordedActivation(t *testing.T) {
	manifestPath := preparedForActivation(t)
	activation := time.Date(2026, 8, 9, 3, 0, 0, 0, time.UTC).Format(time.RFC3339)
	cutover := time.Date(2026, 8, 9, 3, 1, 0, 0, time.UTC).Format(time.RFC3339)
	activationRequest := withProof(t, manifestPath, Request{Operation: RecordActivation, ActivationAt: activation, CutoverAt: cutover})
	if _, err := applyExecuted(manifestPath, activationRequest, ApplyOptions{Now: func() time.Time {
		return time.Date(2026, 8, 9, 3, 1, 30, 0, time.UTC)
	}}); err != nil {
		t.Fatal(err)
	}
	result, err := applyExecuted(manifestPath, Request{Operation: PrepareRetirement}, ApplyOptions{
		Now: func() time.Time { return time.Date(2026, 8, 9, 3, 2, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Phase != PhaseRetirementReady {
		t.Fatalf("phase = %q", result.Phase)
	}
	if got := readWorkflowManifest(t, manifestPath).SourceRetirementAt; got != nil {
		t.Fatalf("preparation retired source at %v", got)
	}
}

func TestWorkflowRejectsFutureActivationEvidence(t *testing.T) {
	manifestPath := preparedForActivation(t)
	activation := time.Date(2026, 8, 10, 1, 0, 0, 0, time.UTC).Format(time.RFC3339)
	cutover := time.Date(2026, 8, 10, 1, 1, 0, 0, time.UTC).Format(time.RFC3339)
	request := withProof(t, manifestPath, Request{Operation: RecordActivation, ActivationAt: activation, CutoverAt: cutover})
	_, err := applyExecuted(manifestPath, request, ApplyOptions{Now: func() time.Time {
		return time.Date(2026, 8, 9, 1, 0, 0, 0, time.UTC)
	}})
	if err == nil || !strings.Contains(err.Error(), "later than") {
		t.Fatalf("future activation error = %v", err)
	}
}

func TestWorkflowLockFailsClosed(t *testing.T) {
	manifestPath := copyManifest(t)
	lock, err := AcquireLock(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	_, err = applyExecuted(manifestPath, Request{Operation: Backup, ID: "backup", ArtifactPath: writeArtifact(t, "backup", "x")}, ApplyOptions{})
	if err == nil || !strings.Contains(err.Error(), "migration lock is held") {
		t.Fatalf("lock contention error = %v", err)
	}
}

func TestWorkflowRejectsNonIncreasingTimestampsWithoutCommit(t *testing.T) {
	manifestPath := copyManifest(t)
	backup := writeArtifact(t, "backup.dump", "verified backup")
	fixed := time.Date(2026, 8, 9, 1, 0, 0, 0, time.UTC)
	for index, request := range []Request{
		{Operation: RecordImport, ID: "0123456789abcdef0123456789abcdef01234567"},
		{Operation: Backup, ID: "backup-001", ArtifactPath: backup},
		{Operation: Restore, ArtifactPath: backup, Verifier: "test"},
	} {
		request = withProof(t, manifestPath, request)
		at := fixed.Add(time.Duration(index) * time.Minute)
		if _, err := applyExecuted(manifestPath, request, ApplyOptions{Now: func() time.Time { return at }}); err != nil {
			t.Fatalf("apply %s: %v", request.Operation, err)
		}
	}
	fence := withProof(t, manifestPath, Request{Operation: Fence})
	if _, err := applyExecuted(manifestPath, fence, ApplyOptions{Now: func() time.Time { return fixed.Add(3 * time.Minute) }}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	request := withProof(t, manifestPath, Request{Operation: FinalDelta, ArtifactPath: writeArtifact(t, "delta", "delta")})
	_, err = applyExecuted(manifestPath, request, ApplyOptions{Now: func() time.Time { return fixed.Add(3 * time.Minute) }})
	if err == nil || !strings.Contains(err.Error(), "strictly after") {
		t.Fatalf("equal-time delta error = %v", err)
	}
	after, readErr := os.ReadFile(manifestPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(after) != string(before) {
		t.Fatal("timestamp rejection changed manifest")
	}
}

func preparedThroughFence(t *testing.T) string {
	t.Helper()
	manifestPath := copyManifest(t)
	backup := writeArtifact(t, "backup.dump", "verified backup")
	delta := writeArtifact(t, "delta.sql", "final delta")
	for _, request := range []Request{
		{Operation: RecordImport, ID: "0123456789abcdef0123456789abcdef01234567"},
		{Operation: Backup, ID: "backup-001", ArtifactPath: backup},
		{Operation: Restore, ArtifactPath: backup, Verifier: "test"},
		{Operation: Fence},
		{Operation: FinalDelta, ArtifactPath: delta},
	} {
		request = withProof(t, manifestPath, request)
		if _, err := applyExecuted(manifestPath, request, ApplyOptions{}); err != nil {
			t.Fatalf("apply %s: %v", request.Operation, err)
		}
	}
	return manifestPath
}

func preparedForActivation(t *testing.T) string {
	t.Helper()
	manifestPath := preparedThroughFence(t)
	for _, request := range []Request{
		{Operation: Backfill, Name: "001-platform.sql"},
		{Operation: Validate},
		{Operation: PrepareActivation},
	} {
		request = withProof(t, manifestPath, request)
		if _, err := applyExecuted(manifestPath, request, ApplyOptions{}); err != nil {
			t.Fatalf("apply %s: %v", request.Operation, err)
		}
	}
	return manifestPath
}

func copyManifest(t *testing.T) string {
	t.Helper()
	stubGitProvenance(t)
	data := readManifest(t)
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func stubGitProvenance(t *testing.T) {
	t.Helper()
	prior := validateGitProvenance
	validateGitProvenance = func(Manifest, string) error { return nil }
	t.Cleanup(func() { validateGitProvenance = prior })
}

func writeArtifact(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func readWorkflowManifest(t *testing.T, path string) Manifest {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := DecodeManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func withProof(t *testing.T, manifestPath string, request Request) Request {
	t.Helper()
	if request.Operation == Backfill && request.ArtifactPath == "" {
		request.ArtifactPath = writeArtifact(t, request.Name, "-- canonical test backfill\nselect 1;\n")
	}
	manifest := readWorkflowManifest(t, manifestPath)
	if manifest.Workflow.Phase == "" {
		manifest.Workflow.Phase = inferPhase(manifest)
	}
	artifactSHA := ""
	if request.ArtifactPath != "" {
		var err error
		artifactSHA, err = digestFile(request.ArtifactPath, true)
		if err != nil {
			t.Fatal(err)
		}
	}
	proof := operationProof{SchemaVersion: 1, Operation: request.Operation, Status: "pass", MigrationID: "boltertrader-product-platform-upgrade"}
	switch request.Operation {
	case RecordImport:
		proof.Subject = request.ID
		proof.Checks = []string{"source-commit-matches", "source-tree-matches", "subtree-import-matches"}
	case Backup:
		proof.Subject = artifactSHA
		proof.Checks = []string{"dump-created", "dump-format-readable"}
	case Restore:
		proof.Subject = artifactSHA
		proof.Checks = []string{"clean-target-created", "restored-schema-nonempty"}
	case FinalDelta:
		proof.Subject = artifactSHA
		proof.Checks = []string{"capture-session-read-only", "final-snapshot-readable", "clean-target-created", "final-snapshot-applied", "restored-schema-nonempty"}
	case Fence:
		proof.Subject = proofStateDigest(manifest)
		proof.Checks = []string{"source-writer-login-disabled", "source-writer-sessions-terminated", "target-writer-login-disabled", "target-writer-sessions-terminated"}
	case Backfill:
		proof.Subject = proofStateDigest(manifest) + "/" + request.Name + "/" + artifactSHA
		proof.Checks = []string{"script-content-stable", "single-transaction"}
	case Validate:
		proof.Subject = proofStateDigest(manifest)
		proof.Checks = []string{"browser", "offline", "real-test", "restart-recovery"}
	case RecordActivation:
		proof.Subject = proofStateDigest(manifest) + "/" + request.ActivationAt + "/" + request.CutoverAt
		proof.Checks = []string{"one-writer-target", "target-activated"}
	case Rollback:
		proof.Subject = proofStateDigest(manifest)
		proof.Checks = []string{"source-writer-login-restored", "source-writer-sessions-cleared", "target-writer-login-disabled", "target-writer-sessions-terminated"}
	default:
		return request
	}
	data, err := json.Marshal(proof)
	if err != nil {
		t.Fatal(err)
	}
	request.ProofPath = writeArtifact(t, "proof.json", string(data))
	return request
}
