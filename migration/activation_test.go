package migration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestExecuteActivationOrdersBoundariesAndReplaysVerifiedAttempt(t *testing.T) {
	input := testActivationInput(t)
	var attempt *ActivationAttempt
	var calls []string
	deps := testActivationDependencies(&attempt, &calls)
	result, err := ExecuteActivation(context.Background(), input, deps)
	if err != nil {
		t.Fatal(err)
	}
	if result.Phase != PhaseActivated || attempt == nil || attempt.Boundary != ActivationVerified {
		t.Fatalf("result=%+v attempt=%+v", result, attempt)
	}
	want := []string{"promotion-state", "source-fence", "stop", "promote", "grant", "connect", "start", "verify"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=%v, want %v", calls, want)
	}
	calls = nil
	replay, err := ExecuteActivation(context.Background(), input, deps)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Replayed || !reflect.DeepEqual(calls, []string{"promotion-state", "source-fence", "verify"}) {
		t.Fatalf("replay=%+v calls=%v", replay, calls)
	}
}

func TestExecuteActivationRejectsChangedInputAndTamperedJournal(t *testing.T) {
	input := testActivationInput(t)
	var attempt *ActivationAttempt
	var calls []string
	deps := testActivationDependencies(&attempt, &calls)
	if _, err := ExecuteActivation(context.Background(), input, deps); err != nil {
		t.Fatal(err)
	}
	changed := input
	changed.PlatformWriterRole = "other_test_writer"
	if _, err := ExecuteActivation(context.Background(), changed, deps); err == nil || !strings.Contains(err.Error(), "input changed") {
		t.Fatalf("changed input error=%v", err)
	}
	if err := os.WriteFile(input.JournalPath, []byte("tampered\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ExecuteActivation(context.Background(), input, deps); err == nil || !strings.Contains(err.Error(), "journal changed") {
		t.Fatalf("tampered journal error=%v", err)
	}
}

func TestExecuteActivationContainsPostGrantFailure(t *testing.T) {
	input := testActivationInput(t)
	var attempt *ActivationAttempt
	var calls []string
	deps := testActivationDependencies(&attempt, &calls)
	deps.RestoreConnect = func(context.Context) error { calls = append(calls, "connect"); return errors.New("connect denied") }
	deps.ContainPostGrantFailure = func(context.Context) error { calls = append(calls, "contain"); return nil }
	if _, err := ExecuteActivation(context.Background(), input, deps); err == nil || !strings.Contains(err.Error(), "CONNECT restore failed") {
		t.Fatalf("error=%v", err)
	}
	if attempt == nil || attempt.Boundary != ActivationWritersGranted || calls[len(calls)-1] != "contain" {
		t.Fatalf("attempt=%+v calls=%v", attempt, calls)
	}
}

func TestDefaultActivationSupervisorPreservesPostgres(t *testing.T) {
	stateDir := t.TempDir()
	for path, value := range map[string]string{filepath.Join(stateDir, "pids", "postgres.pid"): "123\n", filepath.Join(stateDir, "ownership", "postgres.start"): "987654\n"} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var commands []Command
	options := ExecuteOptions{Run: func(_ context.Context, command Command) (CommandResult, error) {
		commands = append(commands, command)
		if strings.Contains(string(command.Stdin), "pg_postmaster_start_time") {
			return CommandResult{Output: []byte("2026-08-10 00:00:00+00\n")}, nil
		}
		return CommandResult{}, nil
	}}
	request := ExecutionRequest{ManifestPath: filepath.Join("repo", "migration", "manifest.json"), StateDir: stateDir, MaintenanceService: "maintenance_test", MaintenanceDatabase: "maintenance_test", TargetDatabase: "platform_test", TargetRole: "platform_test", GatewayDatabase: "gateway_test", GatewayRole: "gateway_test"}
	deps, err := defaultActivationDependencies(context.Background(), request, options)
	if err != nil {
		t.Fatal(err)
	}
	if err := deps.StopServices(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := deps.StartServices(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := deps.ContainPostGrantFailure(context.Background()); err != nil {
		t.Fatal(err)
	}
	var args []string
	for _, command := range commands {
		args = append(args, command.Args...)
	}
	joined := strings.Join(args, " ")
	if strings.Contains(joined, " down") || strings.Contains(joined, " up") {
		t.Fatalf("activation may not stop PostgreSQL: %s", joined)
	}
	if strings.Count(joined, "stop-app") != 2 || !strings.Contains(joined, "start-app") {
		t.Fatalf("supervisor boundaries=%s", joined)
	}
	for _, command := range commands {
		if strings.Contains(string(command.Stdin), "pg_read_file") || strings.Contains(string(command.Stdin), "pg_read_server_files") {
			t.Fatalf("activation requests forbidden server-file access: %s", command.Stdin)
		}
	}
}

func TestExecuteActivationRecordsGeneratedLedgerEvidence(t *testing.T) {
	manifestPath := preparedForActivation(t)
	evidenceDir := t.TempDir()
	var attempt *ActivationAttempt
	var calls []string
	deps := testActivationDependencies(&attempt, &calls)
	stamp := time.Now().UTC().Add(time.Hour)
	deps.Now = func() time.Time { return stamp }
	result, err := Execute(context.Background(), ExecutionRequest{
		Operation: Activate, ManifestPath: manifestPath, EvidenceDir: evidenceDir, JournalPath: filepath.Join(evidenceDir, "attempt.json"),
		SourceService: "source_test", SourceDatabase: "source_test", SourceRole: "source_test_migrator", SourceWriterRole: "source_test_writer",
		TargetService: "platform_test", TargetDatabase: "platform_test", TargetRole: "platform_test_migrator", TargetWriterRole: "platform_test_writer",
		GatewayService: "gateway_test", GatewayDatabase: "gateway_test", GatewayRole: "gateway_test_migrator", GatewayWriterRole: "gateway_test_writer",
		MaintenanceService: "maintenance_test", MaintenanceDatabase: "maintenance_test", MaintenanceRole: "maintenance_test_admin",
	}, ExecuteOptions{Now: deps.Now, Activation: &deps})
	if err != nil {
		t.Fatal(err)
	}
	if result.Phase != PhaseActivated {
		t.Fatalf("result=%+v", result)
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateWorkflow(data); err != nil {
		t.Fatalf("activated workflow: %v", err)
	}
	manifest, err := DecodeManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.TargetActivationAt == nil || manifest.CutoverAt == nil || *manifest.TargetActivationAt == *manifest.CutoverAt {
		t.Fatalf("activation timestamps=%v/%v", manifest.TargetActivationAt, manifest.CutoverAt)
	}
	if len(manifest.Workflow.Events) == 0 || manifest.Workflow.Events[len(manifest.Workflow.Events)-1].Operation != Activate {
		t.Fatalf("events=%+v", manifest.Workflow.Events)
	}
}

func testActivationInput(t *testing.T) ActivationInput {
	t.Helper()
	dir := t.TempDir()
	return ActivationInput{
		MigrationID: "G010", ManifestPath: filepath.Join(dir, "manifest.json"), EvidenceDir: filepath.Join(dir, "evidence"), JournalPath: filepath.Join(dir, "evidence", "attempt.json"),
		Source:             DatabaseIdentity{Service: "source_test", Database: "source_test", Role: "source_test_migrator"},
		Platform:           DatabaseIdentity{Service: "platform_test", Database: "platform_test", Role: "platform_test_migrator"},
		Gateway:            DatabaseIdentity{Service: "gateway_test", Database: "gateway_test", Role: "gateway_test_migrator"},
		Maintenance:        DatabaseIdentity{Service: "maintenance_test", Database: "maintenance_test", Role: "maintenance_test_admin"},
		PlatformWriterRole: "platform_test_writer", GatewayWriterRole: "gateway_test_writer",
	}
}

func testActivationDependencies(attempt **ActivationAttempt, calls *[]string) ActivationDependencies {
	digest := strings.Repeat("a", 64)
	return ActivationDependencies{
		Now: func() time.Time { return time.Date(2026, 8, 10, 1, 0, 0, 0, time.UTC) },
		LoadAttempt: func(context.Context) (*ActivationAttempt, error) {
			if *attempt == nil {
				return nil, nil
			}
			copy := **attempt
			return &copy, nil
		},
		SaveAttempt: func(_ context.Context, value ActivationAttempt) error { copy := value; *attempt = &copy; return nil },
		ExpectedPromotion: func(context.Context) (string, string, error) {
			*calls = append(*calls, "promotion-state")
			return digest, digest, nil
		},
		VerifySourceFence: func(context.Context) error { *calls = append(*calls, "source-fence"); return nil },
		StopServices:      func(context.Context) error { *calls = append(*calls, "stop"); return nil },
		PromoteGateway:    func(context.Context) (string, error) { *calls = append(*calls, "promote"); return digest, nil },
		GrantWriters:      func(context.Context) error { *calls = append(*calls, "grant"); return nil },
		RestoreConnect:    func(context.Context) error { *calls = append(*calls, "connect"); return nil },
		StartServices:     func(context.Context) error { *calls = append(*calls, "start"); return nil },
		VerifyActivation:  func(context.Context) error { *calls = append(*calls, "verify"); return nil },
	}
}
