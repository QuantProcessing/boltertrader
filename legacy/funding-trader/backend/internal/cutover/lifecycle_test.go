package cutover

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func verifiedProof() Proof {
	return Proof{
		BackupID:            "backup-20260730-001",
		BackupSHA256:        strings.Repeat("a", 64),
		BackupVerifiedAt:    time.Date(2026, 7, 30, 7, 0, 0, 0, time.UTC),
		OldServiceStopped:   true,
		AutoRestartDisabled: true,
	}
}

func TestProofRequiresVerifiedBackupAndProcessFence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Proof)
	}{
		{name: "backup id", mutate: func(proof *Proof) { proof.BackupID = "" }},
		{name: "sha256", mutate: func(proof *Proof) { proof.BackupSHA256 = "not-a-sha" }},
		{name: "verification time", mutate: func(proof *Proof) { proof.BackupVerifiedAt = time.Time{} }},
		{name: "old service stopped", mutate: func(proof *Proof) { proof.OldServiceStopped = false }},
		{name: "automatic restart disabled", mutate: func(proof *Proof) { proof.AutoRestartDisabled = false }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			proof := verifiedProof()
			test.mutate(&proof)
			if err := proof.Validate(); !errors.Is(err, ErrInvalidProof) {
				t.Fatalf("Validate() error=%v, want ErrInvalidProof", err)
			}
		})
	}
}

func TestLifecycleAllowsRestoreOnlyBeforeTrafficOrWritesReopen(t *testing.T) {
	lifecycle, err := NewLifecycle(verifiedProof())
	if err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.MarkMigrationValidated(4); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.AuthorizeRestore("backup-20260730-001"); err != nil {
		t.Fatalf("restore before reopen rejected: %v", err)
	}

	reopened, err := NewLifecycle(verifiedProof())
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.MarkMigrationValidated(4); err != nil {
		t.Fatal(err)
	}
	if err := reopened.MarkTrafficReopened(); err != nil {
		t.Fatal(err)
	}
	if err := reopened.AuthorizeRestore("backup-20260730-001"); !errors.Is(err, ErrRestoreForbidden) {
		t.Fatalf("restore after reopen error=%v, want ErrRestoreForbidden", err)
	}

	written, err := NewLifecycle(verifiedProof())
	if err != nil {
		t.Fatal(err)
	}
	if err := written.MarkMigrationValidated(4); err != nil {
		t.Fatal(err)
	}
	written.MarkWriteAccepted()
	if err := written.AuthorizeRestore("backup-20260730-001"); !errors.Is(err, ErrRestoreForbidden) {
		t.Fatalf("restore after write error=%v, want ErrRestoreForbidden", err)
	}
}

func TestLifecycleRejectsWrongBackupAndPrematureReopen(t *testing.T) {
	lifecycle, err := NewLifecycle(verifiedProof())
	if err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.MarkTrafficReopened(); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("premature reopen error=%v, want ErrInvalidTransition", err)
	}
	if err := lifecycle.MarkWriteAccepted(); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("premature write error=%v, want ErrInvalidTransition", err)
	}
	if err := lifecycle.AuthorizeRestore("different-backup"); !errors.Is(err, ErrBackupMismatch) {
		t.Fatalf("wrong backup error=%v, want ErrBackupMismatch", err)
	}
}

func TestLifecycleSnapshotContainsOnlyOperationalEvidence(t *testing.T) {
	lifecycle, err := NewLifecycle(verifiedProof())
	if err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.MarkMigrationValidated(4); err != nil {
		t.Fatal(err)
	}
	snapshot := lifecycle.Snapshot()
	if snapshot.Phase != PhaseValidated || snapshot.SchemaVersion != 4 ||
		snapshot.BackupID != "backup-20260730-001" ||
		snapshot.NormalTrafficReopened || snapshot.NewContractWriteAccepted {
		t.Fatalf("unexpected lifecycle snapshot: %+v", snapshot)
	}
}
