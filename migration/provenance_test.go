package migration

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBaselineRejectsTamperedFrozenIdentity(t *testing.T) {
	base := mustDecodeManifest(t, readManifest(t))
	tests := map[string]func(*Manifest){
		"migration id":       func(got *Manifest) { got.MigrationID = "other-migration" },
		"contract revision":  func(got *Manifest) { got.ContractRevision++ },
		"source repo":        func(got *Manifest) { got.SourceRepo = t.TempDir() },
		"source remote name": func(got *Manifest) { got.SourceRemoteName = "upstream" },
		"source remote":      func(got *Manifest) { got.SourceRemote += ".invalid" },
		"source branch":      func(got *Manifest) { got.SourceBranch = "develop" },
		"source commit": func(got *Manifest) {
			got.SourceCommit = strings.Repeat("a", 40)
			got.Quiescence.FrozenCommit = got.SourceCommit
		},
		"source tree": func(got *Manifest) {
			got.SourceTreeHash = strings.Repeat("b", 40)
			got.Quiescence.FrozenTreeHash = got.SourceTreeHash
		},
		"target repo":        func(got *Manifest) { got.TargetRepo = t.TempDir() },
		"target remote name": func(got *Manifest) { got.TargetRemoteName = "upstream" },
		"target remote":      func(got *Manifest) { got.TargetRemote += ".invalid" },
		"target branch":      func(got *Manifest) { got.TargetBranch = "develop" },
		"target commit":      func(got *Manifest) { got.TargetCommit = strings.Repeat("c", 40) },
		"target tree":        func(got *Manifest) { got.TargetTreeHash = strings.Repeat("d", 40) },
		"subtree prefix":     func(got *Manifest) { got.SubtreePrefix = "legacy/other" },
		"contract hash": func(got *Manifest) {
			got.ApprovedContractHashes["prd-boltertrader-product-platform-upgrade.md"] = strings.Repeat("0", 64)
		},
		"extra contract": func(got *Manifest) { got.ApprovedContractHashes["unapproved.md"] = strings.Repeat("0", 64) },
		"missing contract": func(got *Manifest) {
			delete(got.ApprovedContractHashes, "prd-boltertrader-product-platform-upgrade.md")
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			got := cloneManifest(t, base)
			mutate(&got)
			if err := validateBaseline(got); err == nil || !strings.Contains(err.Error(), "frozen") {
				t.Fatalf("validateBaseline() error = %v, want frozen identity rejection", err)
			}
		})
	}
}

func TestVerifyGitProvenanceAcceptsMatchingImportedSubtree(t *testing.T) {
	manifest, importCommit := provenanceFixture(t)
	if err := verifyGitProvenance(manifest, importCommit); err != nil {
		t.Fatalf("verifyGitProvenance() error = %v", err)
	}
}

func TestVerifyGitProvenanceRejectsNonCommitAndMismatchedSubtree(t *testing.T) {
	manifest, importCommit := provenanceFixture(t)

	t.Run("non commit", func(t *testing.T) {
		if err := verifyGitProvenance(manifest, manifest.TargetTreeHash); err == nil || !strings.Contains(err.Error(), "commit") {
			t.Fatalf("verifyGitProvenance() error = %v, want commit rejection", err)
		}
	})

	t.Run("mismatched subtree", func(t *testing.T) {
		runGit(t, manifest.TargetRepo, "checkout", manifest.TargetBranch)
		path := filepath.Join(manifest.TargetRepo, filepath.FromSlash(manifest.SubtreePrefix), "payload.txt")
		if err := os.WriteFile(path, []byte("tampered\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		runGit(t, manifest.TargetRepo, "add", ".")
		tampered := commitAll(t, manifest.TargetRepo, "tampered import")
		if err := verifyGitProvenance(manifest, tampered); err == nil || !strings.Contains(err.Error(), "subtree") {
			t.Fatalf("verifyGitProvenance() error = %v, want subtree rejection", err)
		}
	})

	_ = importCommit
}

func TestVerifyGitProvenanceRejectsRemoteAndUnreachableBaseline(t *testing.T) {
	manifest, importCommit := provenanceFixture(t)

	t.Run("remote", func(t *testing.T) {
		got := manifest
		got.SourceRemote = "https://example.invalid/wrong.git"
		if err := verifyGitProvenance(got, importCommit); err == nil || !strings.Contains(err.Error(), "remote") {
			t.Fatalf("verifyGitProvenance() error = %v, want remote rejection", err)
		}
	})

	t.Run("unreachable baseline", func(t *testing.T) {
		got := manifest
		other := initRepo(t, "https://example.test/other.git")
		writeAndCommit(t, other, "other.txt", "other\n", "other")
		got.SourceRepo = other
		got.SourceRemote = "https://example.test/other.git"
		if err := verifyGitProvenance(got, importCommit); err == nil || !strings.Contains(err.Error(), "source commit") {
			t.Fatalf("verifyGitProvenance() error = %v, want source commit rejection", err)
		}
	})
}

func TestVerifyGitProvenanceRejectsSourceMutationAfterFreeze(t *testing.T) {
	manifest, importCommit := provenanceFixture(t)
	if err := os.WriteFile(filepath.Join(manifest.SourceRepo, "payload.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyGitProvenance(manifest, importCommit); err == nil || !strings.Contains(err.Error(), "worktree") {
		t.Fatalf("verifyGitProvenance() error = %v, want source worktree rejection", err)
	}
}

func TestVerifyGitProvenanceRejectsUntrackedSourceMutationAfterFreeze(t *testing.T) {
	manifest, importCommit := provenanceFixture(t)
	if err := os.WriteFile(filepath.Join(manifest.SourceRepo, "untracked.txt"), []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyGitProvenance(manifest, importCommit); err == nil || !strings.Contains(err.Error(), "worktree") {
		t.Fatalf("verifyGitProvenance() error = %v, want source worktree rejection", err)
	}
}

<<<<<<< HEAD
=======
func TestRecordImportValidatesGitProvenanceBeforeAcceptingProof(t *testing.T) {
	manifest, importCommit := provenanceFixture(t)
	manifest.MigrationID = "boltertrader-product-platform-upgrade"
	proofPath := writeArtifact(t, "import-proof.json", `{
		"schemaVersion": 1,
		"operation": "record-import",
		"status": "pass",
		"migrationId": "boltertrader-product-platform-upgrade",
		"subject": "`+importCommit+`",
		"checks": ["source-commit-matches", "source-tree-matches", "subtree-import-matches"]
	}`)
	request := Request{Operation: RecordImport, ID: importCommit, ProofPath: proofPath}
	if _, _, err := requestDigests(manifest, request); err != nil {
		t.Fatalf("requestDigests() error = %v", err)
	}

	request.ID = strings.Repeat("a", 40)
	if _, _, err := requestDigests(manifest, request); err == nil || !strings.Contains(err.Error(), "git provenance") {
		t.Fatalf("requestDigests() error = %v, want Git provenance rejection", err)
	}
}

>>>>>>> cfb154b
func provenanceFixture(t *testing.T) (Manifest, string) {
	t.Helper()
	source := initRepo(t, "https://example.test/source.git")
	sourceCommit := writeAndCommit(t, source, "payload.txt", "frozen\n", "source")
	sourceTree := strings.TrimSpace(runGit(t, source, "rev-parse", sourceCommit+"^{tree}"))

	target := initRepo(t, "https://example.test/target.git")
	targetCommit := writeAndCommit(t, target, "README.md", "target\n", "target")
	targetTree := strings.TrimSpace(runGit(t, target, "rev-parse", targetCommit+"^{tree}"))
	prefix := "legacy/funding-trader"
	if err := os.MkdirAll(filepath.Join(target, filepath.FromSlash(prefix)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, filepath.FromSlash(prefix), "payload.txt"), []byte("frozen\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, target, "add", ".")
	importCommit := commitAll(t, target, "import")

	return Manifest{
		SourceRepo: source, SourceRemoteName: "origin", SourceRemote: "https://example.test/source.git", SourceBranch: "master",
		SourceCommit: sourceCommit, SourceTreeHash: sourceTree,
		TargetRepo: target, TargetRemoteName: "origin", TargetRemote: "https://example.test/target.git", TargetBranch: "master",
		TargetCommit: targetCommit, TargetTreeHash: targetTree, SubtreePrefix: prefix,
	}, importCommit
}

func initRepo(t *testing.T, remote string) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "master")
	runGit(t, dir, "config", "user.name", "Migration Test")
	runGit(t, dir, "config", "user.email", "migration@example.test")
	runGit(t, dir, "remote", "add", "origin", remote)
	return dir
}

func writeAndCommit(t *testing.T, repo, name, contents, message string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, name), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", ".")
	return commitAll(t, repo, message)
}

func commitAll(t *testing.T, repo, message string) string {
	t.Helper()
	runGit(t, repo, "commit", "-m", message)
	return strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
}

func runGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	commandArgs := append([]string{"-C", repo}, args...)
	output, err := exec.Command("git", commandArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func mustDecodeManifest(t *testing.T, data []byte) Manifest {
	t.Helper()
	manifest, err := DecodeManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func cloneManifest(t *testing.T, manifest Manifest) Manifest {
	t.Helper()
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return mustDecodeManifest(t, data)
}
