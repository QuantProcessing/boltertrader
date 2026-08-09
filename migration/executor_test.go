package migration

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestExecuteBackupRunsCanonicalCommandAndGeneratesEvidence(t *testing.T) {
	manifestPath := executorManifest(t)
	artifactPath := filepath.Join(t.TempDir(), "backup.dump")
	evidenceDir := t.TempDir()
	var commands []Command

	result, err := Execute(context.Background(), ExecutionRequest{
		Operation: Backup, ManifestPath: manifestPath, EvidenceDir: evidenceDir,
		ArtifactPath: artifactPath, ID: "backup-001", SourceService: "funding-source", SourceDatabase: "funding_source_test", SourceRole: "funding_source_test_migrator",
	}, ExecuteOptions{Run: func(_ context.Context, command Command) (CommandResult, error) {
		commands = append(commands, command)
		if command.ExpectedOutput != "" {
			return CommandResult{Output: []byte(command.ExpectedOutput)}, nil
		}
		if err := os.WriteFile(artifactPath, []byte("verified backup"), 0o600); err != nil {
			t.Fatal(err)
		}
		return CommandResult{Output: []byte("pg_dump complete")}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != 3 || commands[0].Name != "psql" || commands[1].Name != "pg_dump" || commands[2].Name != "pg_restore" || strings.Contains(strings.Join(commands[1].Args, " "), "sh -c") {
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
		ArtifactPath: filepath.Join(t.TempDir(), "backup.dump"), ID: "backup-001", SourceService: "funding-source", SourceDatabase: "funding_source_test", SourceRole: "funding_source_test_migrator",
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

func TestExecuteReplayReturnsPriorEvidenceWithoutRunningCommands(t *testing.T) {
	manifestPath := executorManifest(t)
	artifactPath := filepath.Join(t.TempDir(), "backup.dump")
	evidenceDir := t.TempDir()
	request := ExecutionRequest{Operation: Backup, ManifestPath: manifestPath, EvidenceDir: evidenceDir, ArtifactPath: artifactPath, ID: "backup-001", SourceService: "source", SourceDatabase: "source_test", SourceRole: "source_test_migrator"}
	first, err := Execute(context.Background(), request, ExecuteOptions{Run: func(_ context.Context, command Command) (CommandResult, error) {
		if command.Name == "pg_dump" {
			if err := os.WriteFile(artifactPath, []byte("backup"), 0o600); err != nil {
				return CommandResult{}, err
			}
		}
		if command.ExpectedOutput != "" {
			return CommandResult{Output: []byte(command.ExpectedOutput)}, nil
		}
		return CommandResult{Output: []byte("ok")}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Execute(context.Background(), request, ExecuteOptions{Run: func(context.Context, Command) (CommandResult, error) {
		t.Fatal("completed replay reran a command")
		return CommandResult{}, nil
	}})
	if err != nil || !second.Replayed || second.EvidencePath != first.EvidencePath {
		t.Fatalf("replay = %+v err=%v first=%+v", second, err, first)
	}
}

func TestExecuteLockCoversExternalCommands(t *testing.T) {
	manifestPath := executorManifest(t)
	artifactPath := filepath.Join(t.TempDir(), "backup.dump")
	request := ExecutionRequest{Operation: Backup, ManifestPath: manifestPath, EvidenceDir: t.TempDir(), ArtifactPath: artifactPath, ID: "backup-001", SourceService: "source", SourceDatabase: "source_test", SourceRole: "source_test_migrator"}
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		var first atomic.Bool
		_, err := Execute(context.Background(), request, ExecuteOptions{Run: func(_ context.Context, command Command) (CommandResult, error) {
			if command.Name == "pg_dump" && first.CompareAndSwap(false, true) {
				close(started)
				<-release
				if err := os.WriteFile(artifactPath, []byte("backup"), 0o600); err != nil {
					return CommandResult{}, err
				}
			}
			if command.ExpectedOutput != "" {
				return CommandResult{Output: []byte(command.ExpectedOutput)}, nil
			}
			return CommandResult{Output: []byte("ok")}, nil
		}})
		done <- err
	}()
	<-started
	var duplicateCalls int
	_, duplicateErr := Execute(context.Background(), request, ExecuteOptions{Run: func(context.Context, Command) (CommandResult, error) {
		duplicateCalls++
		return CommandResult{}, nil
	}})
	close(release)
	if firstErr := <-done; firstErr != nil {
		t.Fatal(firstErr)
	}
	if duplicateErr == nil || !strings.Contains(duplicateErr.Error(), "migration lock is held") || duplicateCalls != 0 {
		t.Fatalf("duplicate err=%v calls=%d", duplicateErr, duplicateCalls)
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
		BackfillPath: scriptPath, SourceService: "funding-source", SourceDatabase: "funding_source_test", SourceRole: "funding_source_test_migrator", SourceWriterRole: "funding_writer",
		TargetService: "bolter-platform", TargetDatabase: "bolter_platform_test", TargetRole: "bolter_platform_test_migrator", TargetWriterRole: "bolter_writer", GatewayService: "bolter-gateway", GatewayDatabase: "bolter_gateway_test", GatewayRole: "bolter_gateway_test_migrator", repositoryRoot: repoRoot,
	}, ExecuteOptions{Run: func(_ context.Context, command Command) (CommandResult, error) {
		if command.Name == "psql" {
			if command.ExpectedOutput != "" {
				return CommandResult{Output: []byte(command.ExpectedOutput)}, nil
			}
			return CommandResult{Output: []byte("captured-high-water")}, nil
		}
		if command.Name == "platform-migrate" {
			return CommandResult{Output: []byte(`{"schemaVersion":15}`)}, nil
		}
		if command.Name != "account-route-backfill" {
			t.Fatalf("command = %+v", command)
		}
		joined := strings.Join(command.Args, " ")
		for _, want := range []string{"--mapping", scriptPath, "--source-service", "funding-source", "--source-database", "funding_source_test", "--source-role", "funding_source_test_migrator", "--platform-service", "bolter-platform", "--platform-database", "bolter_platform_test", "--platform-role", "bolter_platform_test_migrator", "--gateway-service", "bolter-gateway", "--gateway-database", "bolter_gateway_test", "--gateway-role", "bolter_gateway_test_migrator"} {
			if !strings.Contains(joined, want) {
				t.Fatalf("backfill command missing %q: %+v", want, command)
			}
		}
		if len(command.Stdin) != 0 {
			t.Fatal("mapping artifact was sent through command stdin")
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
		BackfillPath: scriptPath, SourceService: "funding-source", SourceDatabase: "funding_source_test", SourceRole: "funding_source_test_migrator", SourceWriterRole: "funding_writer", TargetService: "bolter-platform", TargetDatabase: "bolter_platform_test", TargetRole: "bolter_platform_test_migrator", TargetWriterRole: "bolter_writer", GatewayService: "bolter-gateway", GatewayDatabase: "bolter_gateway_test", GatewayRole: "bolter_gateway_test_migrator", repositoryRoot: repoRoot,
	}, ExecuteOptions{Run: func(context.Context, Command) (CommandResult, error) {
		return CommandResult{}, nil
	}}); err == nil || !strings.Contains(err.Error(), "replay conflict") {
		t.Fatalf("changed script replay error = %v", err)
	}
}

func TestBackfillPlanMigratesAndVerifiesLegacyPlatformSchemaBeforeRouteWrites(t *testing.T) {
	manifestPath := preparedThroughFence(t)
	manifest := readWorkflowManifest(t, manifestPath)
	repoRoot, scriptPath := canonicalBackfillFixture(t, `{"schemaVersion":1,"mappings":[]}`)
	commands, _, _, _, err := executionPlan(manifest, ExecutionRequest{
		Operation: Backfill, ManifestPath: manifestPath, EvidenceDir: t.TempDir(), BackfillPath: scriptPath,
		SourceService: "source-test", SourceDatabase: "source_test", SourceRole: "source_test_migrator", SourceWriterRole: "source_test_writer",
		TargetService: "platform-test", TargetDatabase: "platform_test", TargetRole: "platform_test_migrator", TargetWriterRole: "platform_test_writer",
		GatewayService: "gateway-test", GatewayDatabase: "gateway_test", GatewayRole: "gateway_test_migrator", repositoryRoot: repoRoot,
	})
	if err != nil {
		t.Fatal(err)
	}
	var migrateIndex, verifyIndex, backfillIndex = -1, -1, -1
	for index, command := range commands {
		switch command.Name {
		case "platform-migrate":
			migrateIndex = index
		case "account-route-backfill":
			backfillIndex = index
		}
		if command.Name == "psql" && strings.Contains(strings.Join(command.Args, " "), "platform_schema_migrations") && strings.Contains(strings.Join(command.Args, " "), "platform_account_routes") {
			verifyIndex = index
		}
	}
	if migrateIndex < 0 || verifyIndex <= migrateIndex || backfillIndex <= verifyIndex {
		t.Fatalf("legacy target was not migrated and verified before backfill: %+v", commands)
	}
}

func TestBackfillPlanBindsExpectedConnectedRoles(t *testing.T) {
	manifestPath := preparedThroughFence(t)
	manifest := readWorkflowManifest(t, manifestPath)
	repoRoot, scriptPath := canonicalBackfillFixture(t, `{"schemaVersion":1,"mappings":[]}`)
	commands, _, _, _, err := executionPlan(manifest, ExecutionRequest{
		Operation: Backfill, ManifestPath: manifestPath, EvidenceDir: t.TempDir(), BackfillPath: scriptPath,
		SourceService: "deceptive-test", SourceDatabase: "source_test", SourceRole: "source_test_migrator", SourceWriterRole: "source_test_writer",
		TargetService: "platform-test", TargetDatabase: "platform_test", TargetRole: "platform_test_migrator", TargetWriterRole: "platform_test_writer",
		GatewayService: "gateway-test", GatewayDatabase: "gateway_test", GatewayRole: "gateway_test_migrator", repositoryRoot: repoRoot,
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := make([]string, len(commands))
	for index, command := range commands {
		joined[index] = command.Name + " " + strings.Join(command.Args, " ")
	}
	all := strings.Join(joined, "\n")
	for _, want := range []string{"--role platform_test_migrator", "--source-role source_test_migrator", "--platform-role platform_test_migrator", "--gateway-role gateway_test_migrator"} {
		if !strings.Contains(all, want) {
			t.Fatalf("plan missing %q: %s", want, all)
		}
	}
	if len(commands) == 0 || commands[0].Name != "psql" || !strings.Contains(string(commands[0].Stdin), "current_database()") || commands[0].ExpectedOutput != "true" {
		t.Fatalf("source connected identity is not verified before backfill: %+v", commands)
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
		BackfillPath: scriptPath, SourceService: "funding-source", SourceDatabase: "funding_source_test", SourceRole: "funding_source_test_migrator", SourceWriterRole: "funding_writer", TargetService: "bolter-platform", TargetDatabase: "bolter_platform_test", TargetRole: "bolter_platform_test_migrator", TargetWriterRole: "bolter_writer", GatewayService: "bolter-gateway", GatewayDatabase: "bolter_gateway_test", GatewayRole: "bolter_gateway_test_migrator", repositoryRoot: repoRoot,
	}, ExecuteOptions{Run: func(_ context.Context, command Command) (CommandResult, error) {
		if command.Name == "account-route-backfill" {
			return CommandResult{Output: []byte("backfill complete")}, os.WriteFile(scriptPath, []byte("select 2;"), 0o600)
		}
		if command.ExpectedOutput != "" {
			return CommandResult{Output: []byte(command.ExpectedOutput)}, nil
		}
		return CommandResult{Output: []byte("captured-high-water")}, nil
	}})
	if err == nil || !strings.Contains(err.Error(), "changed during execution") {
		t.Fatalf("error = %v", err)
	}
	after, readErr := os.ReadFile(manifestPath)
	if readErr != nil || string(after) != string(before) {
		t.Fatalf("script drift changed manifest: err=%v", readErr)
	}
}

func TestExecutionPlanRejectsBackfillOutsideRepository(t *testing.T) {
	root := t.TempDir()
	outside := writeArtifact(t, "outside.sql", "select 1;")
	_, _, _, _, err := executionPlan(Manifest{}, ExecutionRequest{Operation: Backfill, EvidenceDir: t.TempDir(), BackfillPath: outside, SourceService: "source", TargetService: "platform", TargetDatabase: "platform_db", GatewayService: "gateway", GatewayDatabase: "gateway_db", repositoryRoot: root})
	if err == nil || !strings.Contains(err.Error(), "outside the frozen repository") {
		t.Fatalf("outside backfill error = %v", err)
	}
}

func TestBackfillPlanRequiresValidatedSourceService(t *testing.T) {
	manifestPath := preparedThroughFence(t)
	manifest := readWorkflowManifest(t, manifestPath)
	repoRoot, scriptPath := canonicalBackfillFixture(t, `{"schemaVersion":1,"mappings":[]}`)
	_, _, _, _, err := executionPlan(manifest, ExecutionRequest{
		Operation: Backfill, EvidenceDir: t.TempDir(), BackfillPath: scriptPath,
		SourceDatabase: "source_test", SourceRole: "source_test_migrator", SourceWriterRole: "source_test_writer",
		TargetService: "platform", TargetDatabase: "platform_test", TargetRole: "platform_test_migrator", TargetWriterRole: "platform_test_writer",
		GatewayService: "gateway", GatewayDatabase: "gateway_test", GatewayRole: "gateway_test_migrator", repositoryRoot: repoRoot,
	})
	if err == nil || !strings.Contains(err.Error(), "PostgreSQL service name is required") {
		t.Fatalf("error=%v", err)
	}
}

func canonicalBackfillFixture(t *testing.T, content string) (string, string) {
	t.Helper()
	root := t.TempDir()
	directory := filepath.Join(root, "migration", "backfills")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "account-route-v1.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "init", "-b", "master")
	runGit(t, root, "add", filepath.ToSlash(filepath.Join("migration", "backfills", "account-route-v1.json")))
	return root, path
}

func TestFencePlanUsesDigestibleNonTransactionalPSQLInput(t *testing.T) {
	commands, _, _, _, err := executionPlan(Manifest{}, ExecutionRequest{
		Operation: Fence, EvidenceDir: t.TempDir(), SourceService: "source", TargetService: "target",
		SourceDatabase: "funding_source_test", TargetDatabase: "bolter_target_test", SourceRole: "funding_source_test_migrator", TargetRole: "bolter_target_test_migrator", SourceWriterRole: "funding_writer", TargetWriterRole: "bolter_writer",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != 7 {
		t.Fatalf("commands = %+v", commands)
	}
	for _, index := range []int{3, 5} {
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
	if commands[2].ExpectedOutput != "" {
		t.Fatal("fence incorrectly requires writer sessions to be drained before termination")
	}
	if strings.Contains(string(commands[4].Stdin), "has_database_privilege") || commands[4].ExpectedOutput != "false|0" {
		t.Fatalf("fence verifies an ineffective CONNECT assertion: %+v", commands[4])
	}
}

func TestExecutablePlansRejectCrossModeDatabaseAndRoleIdentities(t *testing.T) {
	for name, mutate := range map[string]func(*ExecutionRequest){
		"source role":     func(request *ExecutionRequest) { request.SourceRole = "funding_live" },
		"target database": func(request *ExecutionRequest) { request.TargetDatabase = "bolter_platform_live" },
		"writer role":     func(request *ExecutionRequest) { request.SourceWriterRole = "funding_live_writer" },
	} {
		t.Run(name, func(t *testing.T) {
			request := ExecutionRequest{Operation: Fence, EvidenceDir: t.TempDir(), SourceService: "source_test", TargetService: "target_test", SourceDatabase: "source_test", TargetDatabase: "target_test", SourceRole: "source_test_migrator", TargetRole: "target_test_migrator", SourceWriterRole: "source_test_writer", TargetWriterRole: "target_test_writer"}
			mutate(&request)
			if _, _, _, _, err := executionPlan(Manifest{}, request); err == nil || !strings.Contains(err.Error(), "cross-mode") {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestRollbackPlanDoesNotClaimEffectiveConnectVerification(t *testing.T) {
	commands, _, checks, _, err := executionPlan(Manifest{}, ExecutionRequest{
		Operation: Rollback, EvidenceDir: t.TempDir(), SourceService: "source", TargetService: "target",
		SourceDatabase: "funding_source_test", TargetDatabase: "bolter_target_test", SourceRole: "funding_source_test_migrator", TargetRole: "bolter_target_test_migrator", SourceWriterRole: "funding_writer", TargetWriterRole: "bolter_writer",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != 6 || !strings.Contains(string(commands[3].Stdin), "GRANT CONNECT") {
		t.Fatalf("rollback no longer executes the role-scoped connect grant: %+v", commands)
	}
	for _, check := range checks {
		if strings.Contains(check, "effective-connect") {
			t.Fatalf("rollback overclaims effective CONNECT verification: %v", checks)
		}
	}
	for _, want := range []string{"source-writer-login-restored", "source-writer-sessions-cleared", "target-writer-login-disabled", "target-writer-sessions-terminated"} {
		if !containsString(checks, want) {
			t.Fatalf("rollback checks %v missing %q", checks, want)
		}
	}
}

func TestRestorePlanUsesExplicitMaintenanceConnection(t *testing.T) {
	commands, _, _, _, err := executionPlan(Manifest{}, ExecutionRequest{
		Operation: Restore, EvidenceDir: t.TempDir(), ArtifactPath: "backup.dump",
		TargetService: "target", TargetDatabase: "target_test", TargetRole: "target_test_migrator",
		MaintenanceService: "target_admin", MaintenanceDatabase: "postgres", MaintenanceRole: "target_test_admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != 5 || commands[2].Name != "psql" {
		t.Fatalf("commands = %+v", commands)
	}
	args := strings.Join(commands[2].Args, " ")
	if !strings.Contains(args, "service=target_admin dbname=postgres") || strings.Contains(args, "service=target dbname=target_test") {
		t.Fatalf("restore does not use explicit maintenance connection: %+v", commands[2])
	}
}

func TestFinalSnapshotPlanCapturesOneReadableArchiveAndAppliesToCleanTarget(t *testing.T) {
	commands, _, checks, _, err := executionPlan(Manifest{}, ExecutionRequest{
		Operation: FinalDelta, EvidenceDir: t.TempDir(), ArtifactPath: "final.dump",
		SourceService: "source", SourceDatabase: "source_test", SourceRole: "source_test_migrator",
		TargetService: "target", TargetDatabase: "target_test", TargetRole: "target_test_migrator",
		MaintenanceService: "target_admin", MaintenanceDatabase: "postgres", MaintenanceRole: "target_test_admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != 11 {
		t.Fatalf("commands = %+v", commands)
	}
	var dumps int
	for _, command := range commands {
		if command.Name == "pg_dump" {
			dumps++
		}
	}
	if dumps != 1 || commands[7].Name != "pg_restore" || !containsArg(commands[7].Args, "--list") || commands[4].Name != "psql" || !commands[4].CaptureOutputAsInput || commands[6].Name != "psql" || !commands[6].MatchCapturedInput {
		t.Fatalf("final snapshot is not captured once and archive-verified: %+v", commands)
	}
	if !strings.Contains(strings.Join(commands[8].Args, " "), "service=target_admin dbname=postgres") || !strings.Contains(string(commands[8].Stdin), "DROP DATABASE") {
		t.Fatalf("final snapshot does not recreate through maintenance connection: %+v", commands[8])
	}
	if commands[9].Name != "pg_restore" || commands[10].ExpectedOutput != "true" || !containsString(checks, "source-high-water-stable-across-dump") || !containsString(checks, "source-high-water-captured") || !containsString(checks, "restored-schema-nonempty") {
		t.Fatalf("final snapshot is not applied and verified: commands=%+v checks=%v", commands, checks)
	}
}

func TestFinalDeltaSamplesHighWaterBeforeAndAfterDumpBeforeTargetMutation(t *testing.T) {
	commands, _, checks, _, err := executionPlan(Manifest{}, ExecutionRequest{
		Operation: FinalDelta, EvidenceDir: t.TempDir(), ArtifactPath: "final.dump",
		SourceService: "source-test", SourceDatabase: "source_test", SourceRole: "source_test_migrator", TargetService: "target-test", TargetDatabase: "target_test", TargetRole: "target_test_migrator",
		MaintenanceService: "maintenance-test", MaintenanceDatabase: "postgres", MaintenanceRole: "maintenance_test_admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	var dumpIndex, firstMark, secondMark, targetMutation = -1, -1, -1, -1
	for index, command := range commands {
		joined := strings.Join(command.Args, " ")
		if command.Name == "pg_dump" {
			dumpIndex = index
		}
		if command.Name == "psql" && strings.Contains(joined, "pg_stat_user_tables") {
			if firstMark < 0 {
				firstMark = index
			} else {
				secondMark = index
			}
		}
		if targetMutation < 0 && (command.Name == "pg_restore" && !containsArg(command.Args, "--list") || command.Name == "psql" && len(command.Stdin) > 0 && strings.Contains(string(command.Stdin), "DROP DATABASE")) {
			targetMutation = index
		}
	}
	if firstMark < 0 || dumpIndex <= firstMark || secondMark <= dumpIndex || targetMutation <= secondMark || !containsString(checks, "source-high-water-stable-across-dump") {
		t.Fatalf("unsafe final-delta window: commands=%+v checks=%v", commands, checks)
	}
}

func TestFinalDeltaDiscardsDumpWhenHighWaterChangesDuringCapture(t *testing.T) {
	manifestPath := copyManifest(t)
	backup := writeArtifact(t, "backup.dump", "verified backup")
	if _, err := applyExecuted(manifestPath, withProof(t, manifestPath, Request{Operation: RecordImport, ID: "0123456789abcdef0123456789abcdef01234567"}), ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := applyExecuted(manifestPath, withProof(t, manifestPath, Request{Operation: Backup, ID: "backup-1", ArtifactPath: backup}), ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := applyExecuted(manifestPath, withProof(t, manifestPath, Request{Operation: Restore, ArtifactPath: backup, Verifier: "test"}), ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	artifactPath := filepath.Join(t.TempDir(), "unsafe-final.dump")
	highWaterCalls := 0
	targetMutated := false
	_, err := Execute(context.Background(), ExecutionRequest{
		Operation: FinalDelta, ManifestPath: manifestPath, EvidenceDir: t.TempDir(), ArtifactPath: artifactPath,
		SourceService: "source-test", SourceDatabase: "source_test", SourceRole: "source_test_migrator", TargetService: "target-test", TargetDatabase: "target_test", TargetRole: "target_test_migrator",
		MaintenanceService: "maintenance-test", MaintenanceDatabase: "postgres", MaintenanceRole: "maintenance_test_admin",
	}, ExecuteOptions{Run: func(_ context.Context, command Command) (CommandResult, error) {
		joined := strings.Join(command.Args, " ")
		if command.Name == "pg_dump" {
			return CommandResult{}, os.WriteFile(artifactPath, []byte("unsafe dump"), 0o600)
		}
		if command.Name == "psql" && strings.Contains(joined, "pg_stat_user_tables") {
			highWaterCalls++
			if highWaterCalls == 1 {
				return CommandResult{Output: []byte("before")}, nil
			}
			return CommandResult{Output: []byte("during")}, nil
		}
		if command.Name == "pg_restore" && !containsArg(command.Args, "--list") || len(command.Stdin) > 0 && strings.Contains(string(command.Stdin), "DROP DATABASE") {
			targetMutated = true
		}
		if command.ExpectedOutput != "" {
			return CommandResult{Output: []byte(command.ExpectedOutput)}, nil
		}
		return CommandResult{}, nil
	}})
	if err == nil || !strings.Contains(err.Error(), "SOURCE_CHANGED_DURING_FINAL_DELTA") {
		t.Fatalf("error=%v", err)
	}
	if targetMutated || highWaterCalls != 2 {
		t.Fatalf("targetMutated=%t highWaterCalls=%d", targetMutated, highWaterCalls)
	}
	if _, statErr := os.Stat(artifactPath); !os.IsNotExist(statErr) {
		t.Fatalf("unsafe dump remains: %v", statErr)
	}
	if phase := readWorkflowManifest(t, manifestPath).Workflow.Phase; phase != PhaseRestored {
		t.Fatalf("phase=%s", phase)
	}
}

func TestValidationPlanUsesTargetedOfflineThenRealReadRestartRepeatOrder(t *testing.T) {
	root := t.TempDir()
	commands, _, checks, _, err := executionPlan(Manifest{TargetRepo: root}, ExecutionRequest{Operation: Validate, EvidenceDir: t.TempDir(), Verifier: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != 8 {
		t.Fatalf("commands=%+v", commands)
	}
	joined := make([]string, len(commands))
	for index, command := range commands {
		joined[index] = command.Name + " " + strings.Join(command.Args, " ")
		if strings.Contains(joined[index], "go test ./...") {
			t.Fatalf("unsafe broad Go gate remains: %s", joined[index])
		}
	}
	for index, want := range []string{"go test ./credential", "go test ./internal/config ./internal/platform/accounts ./internal/platform/secret ./internal/store/postgres ./cmd/platform-credential ./cmd/platform-migrate", "make test-browser", "make test-real", "npm --prefix web run test:e2e -- platform-real.spec.ts", "make dev-platform-restart", "make test-real", "npm --prefix web run test:e2e -- platform-real.spec.ts"} {
		if !strings.Contains(joined[index], want) {
			t.Fatalf("command[%d]=%q want %q", index, joined[index], want)
		}
	}
	for _, want := range []string{"offline-targeted", "fixture-browser", "real-test-read", "restart-recovery", "real-browser-repeat", "readiness-stream-idempotency"} {
		if !containsString(checks, want) {
			t.Fatalf("checks=%v missing %q", checks, want)
		}
	}
}

func TestBackfillHighWaterMismatchRestoresSourceAndInvalidatesDelta(t *testing.T) {
	manifestPath := preparedThroughFence(t)
	repoRoot, artifactPath := canonicalBackfillFixture(t, `{"schemaVersion":1,"mappings":[]}`)
	var names []string
	_, err := Execute(context.Background(), ExecutionRequest{
		Operation: Backfill, ManifestPath: manifestPath, EvidenceDir: t.TempDir(), BackfillPath: artifactPath,
		SourceService: "source", SourceDatabase: "source_test", SourceRole: "source_test_migrator", SourceWriterRole: "source_writer",
		TargetService: "target", TargetDatabase: "target_test", TargetRole: "target_test_migrator", TargetWriterRole: "target_writer",
		GatewayService: "gateway", GatewayDatabase: "gateway_test", GatewayRole: "gateway_test_migrator", repositoryRoot: repoRoot,
	}, ExecuteOptions{Run: func(_ context.Context, command Command) (CommandResult, error) {
		names = append(names, command.Name)
		if command.Name == "psql" && strings.Contains(strings.Join(command.Args, " "), "pg_stat_user_tables") {
			return CommandResult{Output: []byte("changed-high-water")}, nil
		}
		if command.ExpectedOutput != "" {
			return CommandResult{Output: []byte(command.ExpectedOutput)}, nil
		}
		return CommandResult{Output: []byte("ok")}, nil
	}})
	if err == nil || !strings.Contains(err.Error(), "SOURCE_HIGH_WATER_MISMATCH") {
		t.Fatalf("error=%v commands=%v", err, names)
	}
	for _, name := range names {
		if name == "account-route-backfill" {
			t.Fatalf("backfill ran after mismatch: %v", names)
		}
	}
	manifest := readWorkflowManifest(t, manifestPath)
	last := manifest.Workflow.Events[len(manifest.Workflow.Events)-1]
	if manifest.Workflow.Phase != PhaseRestored || last.Operation != Rollback || last.Reason != "SOURCE_HIGH_WATER_MISMATCH" || manifest.FinalDeltaSHA256 != nil {
		t.Fatalf("mismatch was not durably invalidated: phase=%s last=%+v", manifest.Workflow.Phase, last)
	}
}

func TestExecuteFinalSnapshotAppliesVerifiedArchiveAndReplaysWithoutCommands(t *testing.T) {
	manifestPath := copyManifest(t)
	backup := writeArtifact(t, "backup.dump", "verified backup")
	for _, request := range []Request{
		{Operation: RecordImport, ID: "0123456789abcdef0123456789abcdef01234567"},
		{Operation: Backup, ID: "backup-001", ArtifactPath: backup},
		{Operation: Restore, ArtifactPath: backup, Verifier: "test"},
	} {
		if _, err := applyExecuted(manifestPath, withProof(t, manifestPath, request), ApplyOptions{}); err != nil {
			t.Fatalf("prepare %s: %v", request.Operation, err)
		}
	}
	artifactPath := filepath.Join(t.TempDir(), "final.dump")
	evidenceDir := t.TempDir()
	request := ExecutionRequest{
		Operation: FinalDelta, ManifestPath: manifestPath, EvidenceDir: evidenceDir, ArtifactPath: artifactPath,
		SourceService: "source", SourceDatabase: "source_test", SourceRole: "source_test_migrator",
		TargetService: "target", TargetDatabase: "target_test", TargetRole: "target_test_migrator",
		MaintenanceService: "target_admin", MaintenanceDatabase: "postgres", MaintenanceRole: "target_test_admin",
	}
	var calls int
	first, err := Execute(context.Background(), request, ExecuteOptions{Run: func(_ context.Context, command Command) (CommandResult, error) {
		calls++
		if command.Name == "pg_dump" {
			if err := os.WriteFile(artifactPath, []byte("custom final snapshot"), 0o600); err != nil {
				return CommandResult{}, err
			}
		}
		if command.ExpectedOutput != "" {
			return CommandResult{Output: []byte(command.ExpectedOutput)}, nil
		}
		return CommandResult{Output: []byte("ok")}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 11 || first.EvidencePath == "" || readWorkflowManifest(t, manifestPath).Workflow.Phase != PhaseDeltaCaptured {
		t.Fatalf("final snapshot result=%+v calls=%d", first, calls)
	}
	proofData, err := os.ReadFile(first.EvidencePath)
	if err != nil {
		t.Fatal(err)
	}
	var proof operationProof
	if err := json.Unmarshal(proofData, &proof); err != nil {
		t.Fatal(err)
	}
	if len(proof.Commands) != 11 || proof.InputSHA == "" || proof.Subject != sha256Hex([]byte("custom final snapshot")) {
		t.Fatalf("final snapshot proof=%+v", proof)
	}
	if proof.Commands[4].OutputSHA != proof.InputSHA || proof.Commands[6].OutputSHA != proof.InputSHA {
		t.Fatalf("high-water evidence does not bind both samples: proof=%+v", proof)
	}
	second, err := Execute(context.Background(), request, ExecuteOptions{Run: func(context.Context, Command) (CommandResult, error) {
		t.Fatal("completed final snapshot replay reran a command")
		return CommandResult{}, nil
	}})
	if err != nil || !second.Replayed || second.EvidencePath != first.EvidencePath {
		t.Fatalf("replay=%+v err=%v first=%+v", second, err, first)
	}
}

func containsArg(values []string, want string) bool { return containsString(values, want) }

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
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
