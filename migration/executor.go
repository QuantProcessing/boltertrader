package migration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const evidenceGenerator = "boltertrader-migration-tool"

type Command struct {
	Name  string
	Args  []string
	Env   []string
	Dir   string
	Stdin []byte
}

type CommandResult struct {
	Output []byte
}

type CommandRunner func(context.Context, Command) (CommandResult, error)

type ExecutionRequest struct {
	Operation     Operation
	ManifestPath  string
	EvidenceDir   string
	ArtifactPath  string
	BackfillPath  string
	ID            string
	Verifier      string
	Reason        string
	SourceService string
	TargetService string
}

type ExecuteOptions struct {
	Run CommandRunner
	Now func() time.Time
}

type ExecutionResult struct {
	Result
	EvidencePath string
	InputSHA256  string
}

type commandEvidence struct {
	Executable string `json:"executable"`
	ArgvSHA256 string `json:"argvSha256"`
	InputSHA   string `json:"inputSha256,omitempty"`
	OutputSHA  string `json:"outputSha256"`
}

func Execute(ctx context.Context, request ExecutionRequest, options ExecuteOptions) (ExecutionResult, error) {
	manifestData, err := os.ReadFile(request.ManifestPath)
	if err != nil {
		return ExecutionResult{}, fmt.Errorf("read migration manifest: %w", err)
	}
	manifest, err := DecodeManifest(manifestData)
	if err != nil {
		return ExecutionResult{}, err
	}
	if err := validateBaseline(manifest); err != nil {
		return ExecutionResult{}, err
	}
	if manifest.Workflow.Phase == "" {
		manifest.Workflow.Phase = inferPhase(manifest)
	}
	if err := validateWorkflow(manifest); err != nil {
		return ExecutionResult{}, err
	}
	priorOperation := false
	for _, event := range manifest.Workflow.Events {
		priorOperation = priorOperation || event.Operation == request.Operation
	}
	if !priorOperation {
		if err := validatePredecessor(manifest, request.Operation); err != nil {
			return ExecutionResult{}, err
		}
	}
	commands, applyRequest, checks, inputSHA, err := executionPlan(manifest, request)
	if err != nil {
		return ExecutionResult{}, err
	}
	run := options.Run
	if run == nil {
		run = runCommand
	}
	commandProofs := make([]commandEvidence, 0, len(commands))
	for index, command := range commands {
		result, runErr := run(ctx, command)
		if runErr != nil {
			return ExecutionResult{}, fmt.Errorf("%s command failed", request.Operation)
		}
		if err := verifyCommandPostcondition(request.Operation, index, command, result); err != nil {
			return ExecutionResult{}, err
		}
		commandProofs = append(commandProofs, commandDigest(command, result.Output))
	}
	artifactSHA, err := digestFile(applyRequest.ArtifactPath, request.Operation == Backup || request.Operation == Restore || request.Operation == FinalDelta || request.Operation == Backfill)
	if err != nil {
		return ExecutionResult{}, err
	}
	if request.Operation == Backfill {
		if inputSHA != artifactSHA {
			return ExecutionResult{}, fmt.Errorf("backfill script changed during execution")
		}
		inputSHA = artifactSHA
	}
	proof := operationProof{
		SchemaVersion: 1, Operation: request.Operation, Status: "pass", MigrationID: manifest.MigrationID,
		Subject: proofSubject(manifest, applyRequest, artifactSHA), Checks: checks,
		GeneratedBy: evidenceGenerator, PreStateSHA: sha256Hex(manifestData), InputSHA: inputSHA, Commands: commandProofs,
	}
	evidencePath, err := writeExecutionEvidence(request.EvidenceDir, len(manifest.Workflow.Events)+1, request.Operation, proof)
	if err != nil {
		return ExecutionResult{}, err
	}
	applyRequest.ProofPath = evidencePath
	result, err := applyExecuted(request.ManifestPath, applyRequest, ApplyOptions{Now: options.Now})
	if err != nil {
		_ = os.Remove(evidencePath)
		return ExecutionResult{}, err
	}
	return ExecutionResult{Result: result, EvidencePath: evidencePath, InputSHA256: inputSHA}, nil
}

func executionPlan(manifest Manifest, request ExecutionRequest) ([]Command, Request, []string, string, error) {
	if strings.TrimSpace(request.EvidenceDir) == "" {
		return nil, Request{}, nil, "", fmt.Errorf("evidence directory is required")
	}
	apply := Request{Operation: request.Operation, ID: request.ID, Verifier: request.Verifier, Reason: request.Reason}
	source, err := postgresService(request.SourceService)
	if err != nil && (request.Operation == Backup || request.Operation == FinalDelta || request.Operation == Fence || request.Operation == Rollback) {
		return nil, Request{}, nil, "", err
	}
	target, targetErr := postgresService(request.TargetService)
	if targetErr != nil && (request.Operation == Restore || request.Operation == Fence || request.Operation == Backfill || request.Operation == Rollback) {
		return nil, Request{}, nil, "", targetErr
	}
	psql := func(service, query string) Command {
		return Command{Name: "psql", Args: []string{"--no-psqlrc", "--set", "ON_ERROR_STOP=1", "--dbname", "service=" + service, "--tuples-only", "--no-align", "--command", query}}
	}
	psqlInput := func(service, script string) Command {
		return Command{Name: "psql", Args: []string{"--no-psqlrc", "--set", "ON_ERROR_STOP=1", "--dbname", "service=" + service, "--file", "-"}, Stdin: []byte(script)}
	}
	switch request.Operation {
	case Backup:
		apply.ArtifactPath = request.ArtifactPath
		return []Command{{Name: "pg_dump", Args: []string{"--format=custom", "--no-owner", "--no-privileges", "--file", request.ArtifactPath, "service=" + source}}}, apply, []string{"consistent-snapshot", "restorable-format"}, "", nil
	case Restore:
		apply.ArtifactPath = request.ArtifactPath
		return []Command{
			{Name: "pg_restore", Args: []string{"--clean", "--if-exists", "--exit-on-error", "--no-owner", "--no-privileges", "--dbname", "service=" + target, request.ArtifactPath}},
			psql(target, "SELECT current_database()"),
		}, apply, []string{"clean-restore", "source-stack-boot"}, "", nil
	case FinalDelta:
		apply.ArtifactPath = request.ArtifactPath
		return []Command{{Name: "pg_dump", Args: []string{"--data-only", "--inserts", "--column-inserts", "--no-owner", "--no-privileges", "--file", request.ArtifactPath, "service=" + source}}}, apply, []string{"deterministic", "source-read-only"}, "", nil
	case Fence:
		return []Command{
			psqlInput(source, "SELECT format('ALTER DATABASE %I SET default_transaction_read_only TO on', current_database()) \\gexec\n"),
			psql(source, "SHOW default_transaction_read_only"),
			psqlInput(target, "SELECT format('ALTER DATABASE %I SET default_transaction_read_only TO on', current_database()) \\gexec\n"),
			psql(target, "SHOW default_transaction_read_only"),
		}, apply, []string{"source-writes-disabled", "target-writes-disabled"}, "", nil
	case Backfill:
		info, statErr := os.Stat(request.BackfillPath)
		if statErr != nil || !info.Mode().IsRegular() {
			return nil, Request{}, nil, "", fmt.Errorf("backfill script is unavailable")
		}
		apply.Name, apply.ArtifactPath = filepath.Base(request.BackfillPath), request.BackfillPath
		inputSHA, digestErr := digestFile(request.BackfillPath, true)
		if digestErr != nil {
			return nil, Request{}, nil, "", digestErr
		}
		command := Command{Name: "psql", Args: []string{"--no-psqlrc", "--set", "ON_ERROR_STOP=1", "--single-transaction", "--dbname", "service=" + target, "--file", request.BackfillPath}, Env: []string{"PGOPTIONS=-c default_transaction_read_only=off"}}
		return []Command{command}, apply, []string{"authoritative-ownership-mapped", "deterministic"}, inputSHA, nil
	case Validate:
		apply.Verifier = request.Verifier
		return []Command{
			{Name: "go", Args: []string{"test", "./..."}, Dir: manifest.TargetRepo},
			{Name: "make", Args: []string{"test-browser"}, Dir: manifest.TargetRepo},
			{Name: "make", Args: []string{"test-real"}, Dir: manifest.TargetRepo},
			{Name: "make", Args: []string{"dev-platform-down"}, Dir: manifest.TargetRepo},
			{Name: "make", Args: []string{"dev-platform-up"}, Dir: manifest.TargetRepo},
			{Name: "make", Args: []string{"dev-platform-status"}, Dir: manifest.TargetRepo},
		}, apply, []string{"browser", "offline", "real-test", "restart-recovery"}, "", nil
	case Rollback:
		return []Command{
			psqlInput(source, "SELECT format('ALTER DATABASE %I RESET default_transaction_read_only', current_database()) \\gexec\n"),
			psql(source, "SHOW default_transaction_read_only"),
			psql(target, "SHOW default_transaction_read_only"),
		}, apply, []string{"source-writes-enabled", "target-writes-disabled"}, "", nil
	default:
		return nil, Request{}, nil, "", fmt.Errorf("operation %q has no executable plan", request.Operation)
	}
}

func proofSubject(manifest Manifest, request Request, artifactSHA string) string {
	switch request.Operation {
	case Backup, Restore, FinalDelta:
		return artifactSHA
	case Fence, Validate, Rollback:
		return proofStateDigest(manifest)
	case Backfill:
		return proofStateDigest(manifest) + "/" + request.Name + "/" + artifactSHA
	default:
		return ""
	}
}

func verifyCommandPostcondition(operation Operation, index int, command Command, result CommandResult) error {
	if command.Name != "psql" || len(command.Args) == 0 {
		return nil
	}
	query := command.Args[len(command.Args)-1]
	got := strings.TrimSpace(string(result.Output))
	if query == "SHOW default_transaction_read_only" {
		want := "on"
		if operation == Rollback && index == 1 {
			want = "off"
		}
		if got != want {
			return fmt.Errorf("%s postcondition verification failed", operation)
		}
	}
	return nil
}

func postgresService(value string) (string, error) {
	if value == "" || len(value) > 64 || strings.ContainsAny(value, " /\\\t\r\n='\"") {
		return "", fmt.Errorf("PostgreSQL service name is required and must be non-secret")
	}
	return value, nil
}

func runCommand(ctx context.Context, command Command) (CommandResult, error) {
	cmd := exec.CommandContext(ctx, command.Name, command.Args...)
	cmd.Dir = command.Dir
	cmd.Env = append(os.Environ(), command.Env...)
	cmd.Stdin = bytes.NewReader(command.Stdin)
	output, err := cmd.CombinedOutput()
	if len(output) > 1<<20 {
		output = output[:1<<20]
	}
	return CommandResult{Output: output}, err
}

func commandDigest(command Command, output []byte) commandEvidence {
	argv := append([]string{command.Name}, command.Args...)
	proof := commandEvidence{Executable: command.Name, ArgvSHA256: sha256Hex([]byte(strings.Join(argv, "\x00"))), OutputSHA: sha256Hex(output)}
	if len(command.Stdin) != 0 {
		proof.InputSHA = sha256Hex(command.Stdin)
	}
	return proof
}

func writeExecutionEvidence(dir string, sequence int, operation Operation, proof operationProof) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create evidence directory: %w", err)
	}
	data, err := json.MarshalIndent(proof, "", "  ")
	if err != nil {
		return "", err
	}
	data = append(data, '\n')
	path := filepath.Join(dir, fmt.Sprintf("%03d-%s.json", sequence, operation))
	temp, err := os.CreateTemp(dir, ".evidence-*")
	if err != nil {
		return "", fmt.Errorf("create operation evidence: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return "", err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return "", err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return "", err
	}
	if err := temp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return "", fmt.Errorf("commit operation evidence: %w", err)
	}
	return path, nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
