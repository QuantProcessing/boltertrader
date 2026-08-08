package migration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecuteBackupRunsCanonicalCommandAndGeneratesEvidence(t *testing.T) {
	manifestPath := executorManifest(t)
	artifactPath := filepath.Join(t.TempDir(), "backup.dump")
	evidenceDir := t.TempDir()
	var commands []Command

	result, err := Execute(context.Background(), ExecutionRequest{
		Operation: Backup, ManifestPath: manifestPath, EvidenceDir: evidenceDir,
		ArtifactPath: artifactPath, ID: "backup-001", SourceService: "funding-source",
	}, ExecuteOptions{Run: func(_ context.Context, command Command) (CommandResult, error) {
		commands = append(commands, command)
		if err := os.WriteFile(artifactPath, []byte("verified backup"), 0o600); err != nil {
			t.Fatal(err)
		}
		return CommandResult{Output: []byte("pg_dump complete")}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != 2 || commands[0].Name != "pg_dump" || commands[1].Name != "pg_restore" || strings.Contains(strings.Join(commands[0].Args, " "), "sh -c") {
		t.Fatalf("commands = %+v", commands)
	}
	if result.EvidencePath == "" {
		t.Fatal("missing generated evidence path")
	}
	evidence, err := os.ReadFile(result.EvidencePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"generatedBy": "boltertrader-migration-tool"`, `"operation": "backup"`, `"executable": "pg_dump"`, `"preStateSha256"`} {
		if !strings.Contains(string(evidence), want) {
			t.Errorf("evidence missing %s: %s", want, evidence)
		}
	}
	if got := readWorkflowManifest(t, manifestPath); got.Workflow.Phase != PhaseBackedUp || got.Workflow.Events[len(got.Workflow.Events)-1].ProofSHA == "" {
		t.Fatalf("manifest did not record generated evidence: %+v", got.Workflow)
	}
}

func TestExecuteFailureLeavesManifestAndEvidenceUnchanged(t *testing.T) {
	manifestPath := executorManifest(t)
	before, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	evidenceDir := t.TempDir()
	_, err = Execute(context.Background(), ExecutionRequest{
		Operation: Backup, ManifestPath: manifestPath, EvidenceDir: evidenceDir,
		ArtifactPath: filepath.Join(t.TempDir(), "backup.dump"), ID: "backup-001", SourceService: "funding-source",
	}, ExecuteOptions{Run: func(context.Context, Command) (CommandResult, error) {
		return CommandResult{Output: []byte("secret output must not persist")}, errors.New("command failed with secret")
	}})
	if err == nil || !strings.Contains(err.Error(), "backup command failed") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("error = %v", err)
	}
	after, readErr := os.ReadFile(manifestPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(after) != string(before) {
		t.Fatal("failed execution changed manifest")
	}
	entries, err := os.ReadDir(evidenceDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed execution left evidence: entries=%v err=%v", entries, err)
	}
}

func TestApplyRejectsCallerProofForExecutableOperations(t *testing.T) {
	for _, operation := range []Operation{Backup, Fence} {
		t.Run(string(operation), func(t *testing.T) {
			manifestPath := copyManifest(t)
			before, err := os.ReadFile(manifestPath)
			if err != nil {
				t.Fatal(err)
			}
			_, err = Apply(manifestPath, Request{Operation: operation, ProofPath: "fabricated.json"}, ApplyOptions{})
			if err == nil || !strings.Contains(err.Error(), "requires canonical execution") {
				t.Fatalf("error = %v", err)
			}
			after, readErr := os.ReadFile(manifestPath)
			if readErr != nil || string(after) != string(before) {
				t.Fatalf("direct Apply changed manifest: err=%v", readErr)
			}
		})
	}
}

func TestExecuteBackfillBindsScriptContent(t *testing.T) {
	manifestPath := preparedThroughFence(t)
	repoRoot, scriptPath := canonicalBackfillFixture(t, "select 1;")
	result, err := Execute(context.Background(), ExecutionRequest{
		Operation: Backfill, ManifestPath: manifestPath, EvidenceDir: t.TempDir(),
		BackfillPath: scriptPath, TargetService: "bolter-target", TargetDatabase: "bolter_target", repositoryRoot: repoRoot,
	}, ExecuteOptions{Run: func(_ context.Context, command Command) (CommandResult, error) {
		if command.Name != "psql" {
			t.Fatalf("command = %+v", command)
		}
		return CommandResult{Output: []byte("backfill complete")}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	manifest := readWorkflowManifest(t, manifestPath)
	event := manifest.Workflow.Events[len(manifest.Workflow.Events)-1]
	if event.ArtifactSHA == "" || event.ArtifactSHA != result.InputSHA256 {
		t.Fatalf("backfill event is not bound to script: event=%+v result=%+v", event, result)
	}
	if err := os.WriteFile(scriptPath, []byte("select 2;"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Execute(context.Background(), ExecutionRequest{
		Operation: Backfill, ManifestPath: manifestPath, EvidenceDir: t.TempDir(),
		BackfillPath: scriptPath, TargetService: "bolter-target", TargetDatabase: "bolter_target", repositoryRoot: repoRoot,
	}, ExecuteOptions{Run: func(context.Context, Command) (CommandResult, error) {
		return CommandResult{}, nil
	}}); err == nil || !strings.Contains(err.Error(), "replay conflict") {
		t.Fatalf("changed script replay error = %v", err)
	}
}

func TestExecuteBackfillRejectsScriptChangedDuringCommand(t *testing.T) {
	manifestPath := preparedThroughFence(t)
	repoRoot, scriptPath := canonicalBackfillFixture(t, "select 1;")
	before, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Execute(context.Background(), ExecutionRequest{
		Operation: Backfill, ManifestPath: manifestPath, EvidenceDir: t.TempDir(),
		BackfillPath: scriptPath, TargetService: "bolter-target", TargetDatabase: "bolter_target", repositoryRoot: repoRoot,
	}, ExecuteOptions{Run: func(context.Context, Command) (CommandResult, error) {
		return CommandResult{}, os.WriteFile(scriptPath, []byte("select 2;"), 0o600)
	}})
	if err == nil || !strings.Contains(err.Error(), "changed during execution") {
		t.Fatalf("error = %v", err)
	}
	after, readErr := os.ReadFile(manifestPath)
	if readErr != nil || string(after) != string(before) {
		t.Fatalf("script drift changed manifest: err=%v", readErr)
	}
}

func canonicalBackfillFixture(t *testing.T, content string) (string, string) {
	t.Helper()
	root := t.TempDir()
	directory := filepath.Join(root, "backend", "migrations")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "001-platform.sql")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return root, path
}

func TestFencePlanUsesDigestibleNonTransactionalPSQLInput(t *testing.T) {
	commands, _, _, _, err := executionPlan(Manifest{}, ExecutionRequest{
		Operation: Fence, EvidenceDir: t.TempDir(), SourceService: "source", TargetService: "target",
		SourceDatabase: "funding_source", TargetDatabase: "bolter_target", SourceWriterRole: "funding_writer", TargetWriterRole: "bolter_writer",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != 5 {
		t.Fatalf("commands = %+v", commands)
	}
	for _, index := range []int{1, 3} {
		command := commands[index]
		args := strings.Join(command.Args, " ")
		stdin := string(command.Stdin)
		if strings.Contains(args, "default_transaction_read_only") || strings.Contains(stdin, "DO $do$") || !strings.Contains(stdin, "NOLOGIN") || !strings.Contains(stdin, "REVOKE CONNECT") || !strings.Contains(stdin, "pg_terminate_backend") || command.Args[len(command.Args)-1] != "-" {
			t.Fatalf("unsafe fence command = %+v stdin=%q", command, command.Stdin)
		}
		if commandDigest(command, nil).InputSHA == "" {
			t.Fatal("controlled psql input was not digest-bound")
		}
	}
}

func executorManifest(t *testing.T) string {
	t.Helper()
	manifestPath := copyManifest(t)
	request := withProof(t, manifestPath, Request{Operation: RecordImport, ID: "0123456789abcdef0123456789abcdef01234567"})
	if _, err := Apply(manifestPath, request, ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	return manifestPath
}
