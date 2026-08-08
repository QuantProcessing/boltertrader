package migration

import (
	"fmt"
	"os/exec"
	"strings"
)

var frozenContractHashes = map[string]string{
	"prd-boltertrader-product-platform-upgrade.md":                    "6764daaae53516d46d74fb83c79c7d273ed369ee38231ec01397e5b409e7c757",
	"architecture-boltertrader-product-platform-upgrade.md":           "9222006854fe67590e1b8bd145827839485cd7974bf48a07a57c8a350d4f4260",
	"plan-boltertrader-product-platform-upgrade.md":                   "4e6dc69dd82f9e814e15aff54ff6900358d549bee5d015cc4d27501a0d215b78",
	"test-spec-boltertrader-product-platform-upgrade.md":              "cfebecc00705adb129623f1f4e1f4b84c7c5fa2d62aa537d6d6d46a020c7e48f",
	"api-contract-boltertrader-product-platform-upgrade.md":           "04f5272686fff30d96061896e05032ff96b570036a729dca869e800408567a5f",
	"local-runtime-contract-boltertrader-product-platform-upgrade.md": "f60934e72abd4b95a605c6bd4e9452ed8ae6c9f1bb73a266dbbc8cf6776395c7",
	"migration-contract-boltertrader-product-platform-upgrade.md":     "52be2f1ff3e9034749864620863b622bc2e8843ce13679922275a08c739fb600",
}

var validateGitProvenance = verifyGitProvenance

func matchesFrozenBaseline(manifest Manifest) bool {
	if manifest.SchemaVersion != 1 ||
		manifest.MigrationID != "boltertrader-product-platform-upgrade" ||
		manifest.ContractRevision != 4 ||
		manifest.RecordedAt != "2026-08-04T16:06:38Z" ||
		manifest.SourceRepo != "/Users/dddd/Documents/GitHub/funding-trader" ||
		manifest.SourceRemoteName != "origin" ||
		manifest.SourceRemote != "https://github.com/QuantProcessing/funding-trader.git" ||
		manifest.SourceBranch != "master" ||
		manifest.SourceCommit != "e29f4ccb3f21e53e1424642c10b11e65a1b6a52a" ||
		manifest.SourceTreeHash != "99821acf574c87741b08ae3dc3a913f12b2835d7" ||
		manifest.SourceQuiesceAt != "2026-08-04T16:06:38Z" ||
		manifest.TargetRepo != "/Users/dddd/Documents/GitHub/BolterTrader" ||
		manifest.TargetRemoteName != "origin" ||
		manifest.TargetRemote != "https://github.com/QuantProcessing/boltertrader.git" ||
		manifest.TargetBranch != "master" ||
		manifest.TargetCommit != "9811190344a631f4bd6906c5f8830b07643a82cd" ||
		manifest.TargetTreeHash != "ea4e6ac8dd3b3cf2428efcd3879b7a97ab46b761" ||
		manifest.SubtreePrefix != "legacy/funding-trader" ||
		manifest.RollbackBoundary != "Allowed until the first irreversible target-side canonical write or authoritative backfill." ||
		manifest.ForwardRecoveryBoundary != "Required after the first irreversible target-side canonical write or authoritative backfill." ||
		manifest.Quiescence.Status != "quiesced" ||
		!manifest.Quiescence.WorktreeClean ||
		manifest.Quiescence.FrozenCommit != manifest.SourceCommit ||
		manifest.Quiescence.FrozenTreeHash != manifest.SourceTreeHash ||
		manifest.Quiescence.AllowedChanges != "retirement documentation or repository-administration metadata only" ||
		manifest.OneWriter.NextOwner != "BolterTrader" ||
		!sameStrings(manifest.OneWriter.TransitionRequires, []string{
			"verified clean backup restore",
			"recorded final delta",
			"durable one-writer fence",
		}) {
		return false
	}
	return sameStringMap(manifest.ApprovedContractHashes, frozenContractHashes)
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range want {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

func sameStringMap(got, want map[string]string) bool {
	if len(got) != len(want) {
		return false
	}
	for key, value := range want {
		if got[key] != value {
			return false
		}
	}
	return true
}

func verifyGitProvenance(manifest Manifest, importCommit string) error {
	if !validCommit(importCommit) {
		return fmt.Errorf("git provenance rejected: import commit is not a lowercase 40-character object id")
	}
	if err := verifyRemote(manifest.SourceRepo, manifest.SourceRemoteName, manifest.SourceRemote, "source"); err != nil {
		return err
	}
	if err := verifyRemote(manifest.TargetRepo, manifest.TargetRemoteName, manifest.TargetRemote, "target"); err != nil {
		return err
	}
	if err := verifyCommitTree(manifest.SourceRepo, manifest.SourceBranch, manifest.SourceCommit, manifest.SourceTreeHash, "source"); err != nil {
		return err
	}
	if err := verifyFrozenSourceWorktree(manifest); err != nil {
		return err
	}
	if err := verifyCommitTree(manifest.TargetRepo, manifest.TargetBranch, manifest.TargetCommit, manifest.TargetTreeHash, "target"); err != nil {
		return err
	}
	if objectType, err := gitOutput(manifest.TargetRepo, "cat-file", "-t", importCommit); err != nil || objectType != "commit" {
		return fmt.Errorf("git provenance rejected: import commit does not resolve to a commit")
	}
	if err := gitRun(manifest.TargetRepo, "merge-base", "--is-ancestor", importCommit, "refs/heads/"+manifest.TargetBranch); err != nil {
		return fmt.Errorf("git provenance rejected: import commit is not reachable from the frozen target branch")
	}
	subtree, err := gitOutput(manifest.TargetRepo, "rev-parse", "--verify", importCommit+":"+manifest.SubtreePrefix)
	if err != nil {
		return fmt.Errorf("git provenance rejected: imported subtree does not resolve")
	}
	if objectType, err := gitOutput(manifest.TargetRepo, "cat-file", "-t", subtree); err != nil || objectType != "tree" {
		return fmt.Errorf("git provenance rejected: imported subtree is not a tree")
	}
	if subtree != manifest.SourceTreeHash {
		return fmt.Errorf("git provenance rejected: imported subtree does not match frozen source tree")
	}
	return nil
}

func verifyFrozenSourceWorktree(manifest Manifest) error {
	branch, err := gitOutput(manifest.SourceRepo, "symbolic-ref", "--short", "HEAD")
	if err != nil || branch != manifest.SourceBranch {
		return fmt.Errorf("git provenance rejected: source worktree is not on the frozen branch")
	}
	head, err := gitOutput(manifest.SourceRepo, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || head != manifest.SourceCommit {
		return fmt.Errorf("git provenance rejected: source worktree is not at the frozen commit")
	}
	if err := gitRun(manifest.SourceRepo, "diff-index", "--quiet", manifest.SourceCommit, "--"); err != nil {
		return fmt.Errorf("git provenance rejected: source worktree has tracked changes after freeze")
	}
	untracked, err := gitOutput(manifest.SourceRepo, "ls-files", "--others", "--exclude-standard")
	if err != nil || untracked != "" {
		return fmt.Errorf("git provenance rejected: source worktree has untracked changes after freeze")
	}
	return nil
}

func verifyRemote(repo, remoteName, wantURL, label string) error {
	gotURL, err := gitOutput(repo, "remote", "get-url", remoteName)
	if err != nil || gotURL != wantURL {
		return fmt.Errorf("git provenance rejected: %s remote does not match frozen identity", label)
	}
	return nil
}

func verifyCommitTree(repo, branch, commit, tree, label string) error {
	if objectType, err := gitOutput(repo, "cat-file", "-t", commit); err != nil || objectType != "commit" {
		return fmt.Errorf("git provenance rejected: %s commit does not resolve to a commit", label)
	}
	resolvedTree, err := gitOutput(repo, "rev-parse", "--verify", commit+"^{tree}")
	if err != nil || resolvedTree != tree {
		return fmt.Errorf("git provenance rejected: %s tree does not match its frozen commit", label)
	}
	if objectType, err := gitOutput(repo, "cat-file", "-t", tree); err != nil || objectType != "tree" {
		return fmt.Errorf("git provenance rejected: %s tree does not resolve to a tree", label)
	}
	if err := gitRun(repo, "merge-base", "--is-ancestor", commit, "refs/heads/"+branch); err != nil {
		return fmt.Errorf("git provenance rejected: %s commit is not reachable from the frozen branch", label)
	}
	return nil
}

func gitOutput(repo string, args ...string) (string, error) {
	commandArgs := append([]string{"--no-replace-objects", "-C", repo}, args...)
	output, err := exec.Command("git", commandArgs...).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func gitRun(repo string, args ...string) error {
	commandArgs := append([]string{"--no-replace-objects", "-C", repo}, args...)
	return exec.Command("git", commandArgs...).Run()
}
