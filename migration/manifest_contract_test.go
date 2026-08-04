package migration_test

import (
	"encoding/json"
	"os"
	"testing"
)

type manifest struct {
	SchemaVersion           int      `json:"schemaVersion"`
	MigrationID             string   `json:"migrationId"`
	SourceRepo              string   `json:"sourceRepo"`
	SourceRemoteName        string   `json:"sourceRemoteName"`
	SourceRemote            string   `json:"sourceRemote"`
	SourceBranch            string   `json:"sourceBranch"`
	SourceCommit            string   `json:"sourceCommit"`
	SourceTreeHash          string   `json:"sourceTreeHash"`
	SourceQuiesceAt         string   `json:"sourceQuiesceAt"`
	TargetRepo              string   `json:"targetRepo"`
	TargetRemoteName        string   `json:"targetRemoteName"`
	TargetRemote            string   `json:"targetRemote"`
	TargetBranch            string   `json:"targetBranch"`
	TargetCommit            string   `json:"targetCommit"`
	TargetTreeHash          string   `json:"targetTreeHash"`
	SubtreePrefix           string   `json:"subtreePrefix"`
	ImportCommit            *string  `json:"importCommit"`
	BackfillScripts         []string `json:"backfillScripts"`
	BackupID                *string  `json:"backupId"`
	BackupSHA256            *string  `json:"backupSha256"`
	RestoreVerifiedAt       *string  `json:"restoreVerifiedAt"`
	FinalDeltaAt            *string  `json:"finalDeltaAt"`
	FinalDeltaSHA256        *string  `json:"finalDeltaSha256"`
	OneWriterFenceAt        *string  `json:"oneWriterFenceAt"`
	ValidationAt            *string  `json:"validationAt"`
	TargetActivationAt      *string  `json:"targetActivationAt"`
	CutoverAt               *string  `json:"cutoverAt"`
	RollbackBoundary        string   `json:"rollbackBoundary"`
	ForwardRecoveryBoundary string   `json:"forwardRecoveryBoundary"`
	SourceRetirementAt      *string  `json:"sourceRetirementAt"`
	Quiescence              struct {
		Status         string `json:"status"`
		WorktreeClean  bool   `json:"worktreeClean"`
		FrozenCommit   string `json:"frozenCommit"`
		FrozenTreeHash string `json:"frozenTreeHash"`
	} `json:"quiescence"`
	OneWriter struct {
		Owner                        string `json:"owner"`
		FenceRaised                  bool   `json:"fenceRaised"`
		TargetCanonicalWritesEnabled bool   `json:"targetCanonicalWritesEnabled"`
	} `json:"oneWriter"`
	ApprovedContractHashes map[string]string `json:"approvedContractHashes"`
}

func TestMigrationManifestFreezesProvenanceWithoutClaimingCutover(t *testing.T) {
	data, err := os.ReadFile("manifest.json")
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var got manifest
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}

	if got.SchemaVersion != 1 || got.MigrationID != "boltertrader-product-platform-upgrade" {
		t.Fatalf("unexpected manifest identity: schema=%d migration=%q", got.SchemaVersion, got.MigrationID)
	}
	if got.SourceRemoteName != "origin" || got.SourceRemote != "https://github.com/QuantProcessing/funding-trader.git" || got.SourceBranch != "master" {
		t.Fatalf("unexpected source provenance: remoteName=%q remote=%q branch=%q", got.SourceRemoteName, got.SourceRemote, got.SourceBranch)
	}
	if got.SourceCommit != "e29f4ccb3f21e53e1424642c10b11e65a1b6a52a" || got.SourceTreeHash != "99821acf574c87741b08ae3dc3a913f12b2835d7" {
		t.Fatalf("unexpected source snapshot: commit=%q tree=%q", got.SourceCommit, got.SourceTreeHash)
	}
	if got.TargetRemoteName != "origin" || got.TargetRemote != "https://github.com/QuantProcessing/boltertrader.git" || got.TargetBranch != "master" {
		t.Fatalf("unexpected target provenance: remoteName=%q remote=%q branch=%q", got.TargetRemoteName, got.TargetRemote, got.TargetBranch)
	}
	if got.TargetCommit != "9811190344a631f4bd6906c5f8830b07643a82cd" || got.TargetTreeHash != "ea4e6ac8dd3b3cf2428efcd3879b7a97ab46b761" {
		t.Fatalf("unexpected target snapshot: commit=%q tree=%q", got.TargetCommit, got.TargetTreeHash)
	}
	if got.SourceRepo == "" || got.TargetRepo == "" || got.SourceQuiesceAt == "" || got.SubtreePrefix != "legacy/funding-trader" {
		t.Fatal("repository paths, quiesce timestamp, and subtree prefix must be frozen")
	}
	if got.Quiescence.Status != "quiesced" || !got.Quiescence.WorktreeClean || got.Quiescence.FrozenCommit != got.SourceCommit || got.Quiescence.FrozenTreeHash != got.SourceTreeHash {
		t.Fatalf("invalid quiescence evidence: %+v", got.Quiescence)
	}
	if got.OneWriter.Owner != "funding-trader" || got.OneWriter.FenceRaised || got.OneWriter.TargetCanonicalWritesEnabled {
		t.Fatalf("invalid pre-cutover one-writer baseline: %+v", got.OneWriter)
	}

	proofs := map[string]*string{
		"importCommit":       got.ImportCommit,
		"backupId":           got.BackupID,
		"backupSha256":       got.BackupSHA256,
		"restoreVerifiedAt":  got.RestoreVerifiedAt,
		"finalDeltaAt":       got.FinalDeltaAt,
		"finalDeltaSha256":   got.FinalDeltaSHA256,
		"oneWriterFenceAt":   got.OneWriterFenceAt,
		"validationAt":       got.ValidationAt,
		"targetActivationAt": got.TargetActivationAt,
		"cutoverAt":          got.CutoverAt,
		"sourceRetirementAt": got.SourceRetirementAt,
	}
	for name, value := range proofs {
		if value != nil {
			t.Errorf("%s must remain unset before its proof gate", name)
		}
	}
	if got.BackfillScripts == nil || len(got.BackfillScripts) != 0 {
		t.Fatalf("backfillScripts must be an explicit empty list before implementation: %v", got.BackfillScripts)
	}
	if got.RollbackBoundary == "" || got.ForwardRecoveryBoundary == "" {
		t.Fatal("rollback and forward-recovery boundaries must be frozen")
	}
	wantHashes := map[string]string{
		"prd-boltertrader-product-platform-upgrade.md":                    "6764daaae53516d46d74fb83c79c7d273ed369ee38231ec01397e5b409e7c757",
		"architecture-boltertrader-product-platform-upgrade.md":           "9222006854fe67590e1b8bd145827839485cd7974bf48a07a57c8a350d4f4260",
		"plan-boltertrader-product-platform-upgrade.md":                   "4e6dc69dd82f9e814e15aff54ff6900358d549bee5d015cc4d27501a0d215b78",
		"test-spec-boltertrader-product-platform-upgrade.md":              "cfebecc00705adb129623f1f4e1f4b84c7c5fa2d62aa537d6d6d46a020c7e48f",
		"api-contract-boltertrader-product-platform-upgrade.md":           "04f5272686fff30d96061896e05032ff96b570036a729dca869e800408567a5f",
		"local-runtime-contract-boltertrader-product-platform-upgrade.md": "f60934e72abd4b95a605c6bd4e9452ed8ae6c9f1bb73a266dbbc8cf6776395c7",
		"migration-contract-boltertrader-product-platform-upgrade.md":     "52be2f1ff3e9034749864620863b622bc2e8843ce13679922275a08c739fb600",
	}
	if len(got.ApprovedContractHashes) != len(wantHashes) {
		t.Fatalf("approved contract hash count = %d, want %d", len(got.ApprovedContractHashes), len(wantHashes))
	}
	for name, want := range wantHashes {
		if gotHash := got.ApprovedContractHashes[name]; gotHash != want {
			t.Errorf("approved contract hash %s = %q, want %q", name, gotHash, want)
		}
	}
}
