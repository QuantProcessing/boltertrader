package migration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const evidenceGenerator = "boltertrader-migration-tool"

type Command struct {
	Name                 string
	Args                 []string
	Env                  []string
	Dir                  string
	Stdin                []byte
	ExpectedOutput       string
	ExpectedOutputSHA    string
	CaptureOutputAsInput bool
}

type CommandResult struct {
	Output []byte
}

type CommandRunner func(context.Context, Command) (CommandResult, error)

type ExecutionRequest struct {
	Operation           Operation
	ManifestPath        string
	EvidenceDir         string
	ArtifactPath        string
	BackfillPath        string
	ID                  string
	Verifier            string
	Reason              string
	SourceService       string
	TargetService       string
	GatewayService      string
	MaintenanceService  string
	SourceDatabase      string
	TargetDatabase      string
	GatewayDatabase     string
	MaintenanceDatabase string
	SourceWriterRole    string
	TargetWriterRole    string
	repositoryRoot      string
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
	lock, err := AcquireLock(request.ManifestPath)
	if err != nil {
		return ExecutionResult{}, err
	}
	defer lock.Release()
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
	if request.Operation == RecordImport {
		return executeRecordImportLocked(request, options, manifestData, manifest)
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
	if priorOperation {
		if replay, replayErr := replayExecution(manifest, request.EvidenceDir, applyRequest, commands, inputSHA); replayErr != nil || replay != nil {
			if replayErr != nil {
				return ExecutionResult{}, replayErr
			}
			return *replay, nil
		}
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
			if request.Operation == Backfill && errors.Is(err, errSourceHighWaterMismatch) {
				if rollbackErr := rollbackAfterHighWaterMismatch(ctx, manifestData, manifest, request, options, run); rollbackErr != nil {
					return ExecutionResult{}, fmt.Errorf("SOURCE_HIGH_WATER_MISMATCH; automatic source writer restore failed: %w", rollbackErr)
				}
				return ExecutionResult{}, fmt.Errorf("SOURCE_HIGH_WATER_MISMATCH: source writer restored; capture a new final delta")
			}
			return ExecutionResult{}, err
		}
		commandProofs = append(commandProofs, commandDigest(command, result.Output))
		if command.CaptureOutputAsInput {
			inputSHA = sha256Hex(bytes.TrimSpace(result.Output))
		}
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
	applyRequest.generatedEvidence = &proof
	applyRequest.capturedInputSHA = inputSHA
	evidencePath, err := writeExecutionEvidence(request.EvidenceDir, len(manifest.Workflow.Events)+1, request.Operation, proof)
	if err != nil {
		return ExecutionResult{}, err
	}
	applyRequest.ProofPath = evidencePath
	result, err := applyLocked(request.ManifestPath, applyRequest, ApplyOptions{Now: options.Now, execution: &executionCapability{}})
	if err != nil {
		_ = os.Remove(evidencePath)
		return ExecutionResult{}, err
	}
	return ExecutionResult{Result: result, EvidencePath: evidencePath, InputSHA256: inputSHA}, nil
}

var errSourceHighWaterMismatch = errors.New("source high-water mismatch")

func rollbackAfterHighWaterMismatch(ctx context.Context, manifestData []byte, manifest Manifest, request ExecutionRequest, options ExecuteOptions, run CommandRunner) error {
	rollbackRequest := request
	rollbackRequest.Operation = Rollback
	rollbackRequest.Reason = "SOURCE_HIGH_WATER_MISMATCH"
	commands, applyRequest, checks, inputSHA, err := executionPlan(manifest, rollbackRequest)
	if err != nil {
		return err
	}
	proofs := make([]commandEvidence, 0, len(commands))
	for index, command := range commands {
		result, runErr := run(ctx, command)
		if runErr != nil {
			return fmt.Errorf("rollback command failed")
		}
		if err := verifyCommandPostcondition(Rollback, index, command, result); err != nil {
			return err
		}
		proofs = append(proofs, commandDigest(command, result.Output))
	}
	proof := operationProof{SchemaVersion: 1, Operation: Rollback, Status: "pass", MigrationID: manifest.MigrationID,
		Subject: proofStateDigest(manifest), Checks: checks, GeneratedBy: evidenceGenerator,
		PreStateSHA: sha256Hex(manifestData), InputSHA: inputSHA, Commands: proofs}
	applyRequest.generatedEvidence = &proof
	evidencePath, err := writeExecutionEvidence(request.EvidenceDir, len(manifest.Workflow.Events)+1, Rollback, proof)
	if err != nil {
		return err
	}
	applyRequest.ProofPath = evidencePath
	if _, err := applyLocked(request.ManifestPath, applyRequest, ApplyOptions{Now: options.Now, execution: &executionCapability{}}); err != nil {
		_ = os.Remove(evidencePath)
		return err
	}
	return nil
}

func executeRecordImportLocked(request ExecutionRequest, options ExecuteOptions, manifestData []byte, manifest Manifest) (ExecutionResult, error) {
	if request.ID == "" || strings.TrimSpace(request.EvidenceDir) == "" {
		return ExecutionResult{}, fmt.Errorf("record-import requires an import commit and evidence directory")
	}
	if err := validateGitProvenance(manifest, request.ID); err != nil {
		return ExecutionResult{}, err
	}
	command := commandEvidence{
		Executable: "git-provenance-verifier",
		ArgvSHA256: sha256Hex([]byte(request.ID)),
		OutputSHA:  sha256Hex([]byte("verified")),
	}
	proof := operationProof{
		SchemaVersion: 1, Operation: RecordImport, Status: "pass", MigrationID: manifest.MigrationID,
		Subject: request.ID, Checks: []string{"source-commit-matches", "source-tree-matches", "subtree-import-matches"},
		GeneratedBy: evidenceGenerator, PreStateSHA: sha256Hex(manifestData), InputSHA: sha256Hex([]byte(request.ID)), Commands: []commandEvidence{command},
	}
	path, err := writeExecutionEvidence(request.EvidenceDir, len(manifest.Workflow.Events)+1, RecordImport, proof)
	if err != nil {
		return ExecutionResult{}, err
	}
	applyRequest := Request{Operation: RecordImport, ID: request.ID, ProofPath: path, generatedEvidence: &proof}
	result, err := applyLocked(request.ManifestPath, applyRequest, ApplyOptions{Now: options.Now, execution: &executionCapability{}})
	if err != nil {
		_ = os.Remove(path)
		return ExecutionResult{}, err
	}
	return ExecutionResult{Result: result, EvidencePath: path, InputSHA256: proof.InputSHA}, nil
}

func replayExecution(manifest Manifest, evidenceDir string, request Request, commands []Command, inputSHA string) (*ExecutionResult, error) {
	if !operationPostconditionHolds(manifest, request) {
		return nil, nil
	}
	for index := len(manifest.Workflow.Events) - 1; index >= 0; index-- {
		event := manifest.Workflow.Events[index]
		if event.Operation != request.Operation {
			continue
		}
		path := filepath.Join(evidenceDir, fmt.Sprintf("%03d-%s.json", event.Sequence, event.Operation))
		data, digest, err := readProof(path)
		if err != nil || digest != event.ProofSHA {
			return nil, fmt.Errorf("%s replay conflict: evidence changed", request.Operation)
		}
		var proof operationProof
		if json.Unmarshal(data, &proof) != nil || proof.GeneratedBy != evidenceGenerator || !validSHA256(proof.PreStateSHA) || len(proof.Commands) != len(commands) {
			return nil, fmt.Errorf("%s replay conflict: generated evidence is invalid", request.Operation)
		}
		for commandIndex, command := range commands {
			planned := commandDigest(command, nil)
			recorded := proof.Commands[commandIndex]
			if recorded.Executable != planned.Executable || recorded.ArgvSHA256 != planned.ArgvSHA256 || recorded.InputSHA != planned.InputSHA || !validSHA256(recorded.OutputSHA) {
				return nil, fmt.Errorf("%s replay conflict: command plan changed", request.Operation)
			}
		}
		artifactSHA, digestErr := digestFile(request.ArtifactPath, operationNeedsArtifact(request.Operation))
		if digestErr != nil || artifactSHA != event.ArtifactSHA {
			return nil, fmt.Errorf("%s replay conflict: artifact changed", request.Operation)
		}
		request.capturedInputSHA = proof.InputSHA
		if (inputSHA != "" && proof.InputSHA != inputSHA) || requestFingerprint(request, artifactSHA, event.ProofSHA) != event.Fingerprint {
			return nil, fmt.Errorf("%s replay conflict: request changed", request.Operation)
		}
		return &ExecutionResult{Result: Result{Phase: manifest.Workflow.Phase, Replayed: true}, EvidencePath: path, InputSHA256: proof.InputSHA}, nil
	}
	return nil, fmt.Errorf("%s replay conflict", request.Operation)
}

func executionPlan(manifest Manifest, request ExecutionRequest) ([]Command, Request, []string, string, error) {
	if strings.TrimSpace(request.EvidenceDir) == "" {
		return nil, Request{}, nil, "", fmt.Errorf("evidence directory is required")
	}
	for _, identity := range []string{request.SourceService, request.TargetService, request.GatewayService, request.MaintenanceService, request.SourceDatabase, request.TargetDatabase, request.GatewayDatabase, request.MaintenanceDatabase, request.SourceWriterRole, request.TargetWriterRole} {
		if containsIdentityToken(identity, "live") {
			return nil, Request{}, nil, "", fmt.Errorf("cross-mode database or role identity rejected")
		}
	}
	apply := Request{Operation: request.Operation, ID: request.ID, Verifier: request.Verifier, Reason: request.Reason}
	source, err := postgresService(request.SourceService)
	if err != nil && (request.Operation == Backup || request.Operation == FinalDelta || request.Operation == Fence || request.Operation == Rollback) {
		return nil, Request{}, nil, "", err
	}
	target, targetErr := postgresService(request.TargetService)
	if targetErr != nil && (request.Operation == Restore || request.Operation == FinalDelta || request.Operation == Fence || request.Operation == Backfill || request.Operation == Rollback) {
		return nil, Request{}, nil, "", targetErr
	}
	maintenance := ""
	if request.Operation == Restore || request.Operation == FinalDelta {
		maintenance, err = postgresService(request.MaintenanceService)
		if err != nil {
			return nil, Request{}, nil, "", fmt.Errorf("maintenance %w", err)
		}
	}
	psql := func(service, database, query string) Command {
		connection := "service=" + service
		if database != "" {
			connection += " dbname=" + database
		}
		return Command{Name: "psql", Args: []string{"--no-psqlrc", "--set", "ON_ERROR_STOP=1", "--dbname", connection, "--tuples-only", "--no-align", "--command", query}}
	}
	psqlInput := func(service, database, script string, variables ...string) Command {
		connection := "service=" + service
		if database != "" {
			connection += " dbname=" + database
		}
		args := []string{"--no-psqlrc", "--set", "ON_ERROR_STOP=1", "--dbname", connection}
		for _, variable := range variables {
			args = append(args, "--set", variable)
		}
		args = append(args, "--file", "-")
		return Command{Name: "psql", Args: args, Stdin: []byte(script)}
	}
	switch request.Operation {
	case Backup:
		apply.ArtifactPath = request.ArtifactPath
		return []Command{
			{Name: "pg_dump", Args: []string{"--format=custom", "--no-owner", "--no-privileges", "--file", request.ArtifactPath, "service=" + source}},
			{Name: "pg_restore", Args: []string{"--list", request.ArtifactPath}},
		}, apply, []string{"dump-created", "dump-format-readable"}, "", nil
	case Restore:
		targetDatabase, databaseErr := postgresIdentifier("target database", request.TargetDatabase)
		if databaseErr != nil {
			return nil, Request{}, nil, "", databaseErr
		}
		maintenanceDatabase, databaseErr := postgresIdentifier("maintenance database", request.MaintenanceDatabase)
		if databaseErr != nil {
			return nil, Request{}, nil, "", databaseErr
		}
		apply.ArtifactPath = request.ArtifactPath
		verify := psql(target, targetDatabase, "SELECT (count(*) > 0)::text FROM information_schema.tables WHERE table_schema NOT IN ('pg_catalog','information_schema')")
		verify.ExpectedOutput = "true"
		return []Command{
			psqlInput(maintenance, maintenanceDatabase, `DROP DATABASE IF EXISTS :"database" WITH (FORCE); CREATE DATABASE :"database";`+"\n", "database="+targetDatabase),
			{Name: "pg_restore", Args: []string{"--exit-on-error", "--no-owner", "--no-privileges", "--dbname", "service=" + target + " dbname=" + targetDatabase, request.ArtifactPath}},
			verify,
		}, apply, []string{"clean-target-created", "restored-schema-nonempty"}, "", nil
	case FinalDelta:
		sourceDatabase, databaseErr := postgresIdentifier("source database", request.SourceDatabase)
		if databaseErr != nil {
			return nil, Request{}, nil, "", databaseErr
		}
		targetDatabase, databaseErr := postgresIdentifier("target database", request.TargetDatabase)
		if databaseErr != nil {
			return nil, Request{}, nil, "", databaseErr
		}
		maintenanceDatabase, databaseErr := postgresIdentifier("maintenance database", request.MaintenanceDatabase)
		if databaseErr != nil {
			return nil, Request{}, nil, "", databaseErr
		}
		apply.ArtifactPath = request.ArtifactPath
		readOnly := []string{"PGOPTIONS=-c default_transaction_read_only=on"}
		check := psql(source, sourceDatabase, "SHOW transaction_read_only")
		check.Env = readOnly
		check.ExpectedOutput = "on"
		verify := psql(target, targetDatabase, "SELECT (count(*) > 0)::text FROM information_schema.tables WHERE table_schema NOT IN ('pg_catalog','information_schema')")
		verify.ExpectedOutput = "true"
		highWater := psql(source, sourceDatabase, `SELECT COALESCE(json_agg(row_to_json(mark) ORDER BY mark.relid)::text,'[]') FROM (SELECT relid::text,n_tup_ins,n_tup_upd,n_tup_del FROM pg_stat_user_tables) mark`)
		highWater.Env = readOnly
		highWater.CaptureOutputAsInput = true
		return []Command{
			check,
			{Name: "pg_dump", Args: []string{"--format=custom", "--no-owner", "--no-privileges", "--file", request.ArtifactPath, "service=" + source + " dbname=" + sourceDatabase}, Env: readOnly},
			{Name: "pg_restore", Args: []string{"--list", request.ArtifactPath}},
			psqlInput(maintenance, maintenanceDatabase, `DROP DATABASE IF EXISTS :"database" WITH (FORCE); CREATE DATABASE :"database";`+"\n", "database="+targetDatabase),
			{Name: "pg_restore", Args: []string{"--exit-on-error", "--no-owner", "--no-privileges", "--dbname", "service=" + target + " dbname=" + targetDatabase, request.ArtifactPath}},
			verify,
			highWater,
		}, apply, []string{"capture-session-read-only", "final-snapshot-readable", "source-high-water-captured", "clean-target-created", "final-snapshot-applied", "restored-schema-nonempty"}, "", nil
	case Fence:
		sourceDatabase, sourceRole, targetDatabase, targetRole, identityErr := fenceIdentities(request)
		if identityErr != nil {
			return nil, Request{}, nil, "", identityErr
		}
		stateQuery := `SELECT r.rolcanlogin::text || '|' || count(a.pid)::text FROM pg_roles r LEFT JOIN pg_stat_activity a ON a.usename=r.rolname AND a.pid<>pg_backend_pid() WHERE r.rolname=:'writer_role' GROUP BY r.rolcanlogin`
		fenceSQL := `REVOKE CONNECT ON DATABASE :"database" FROM :"writer_role"; ALTER ROLE :"writer_role" NOLOGIN; SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE usename=:'writer_role' AND pid<>pg_backend_pid();` + "\n"
		sourceBefore := psqlInput(source, "", stateQuery+"\n", "database="+sourceDatabase, "writer_role="+sourceRole)
		sourceAfter := psqlInput(source, "", stateQuery+"\n", "database="+sourceDatabase, "writer_role="+sourceRole)
		sourceAfter.ExpectedOutput = "false|0"
		targetAfter := psqlInput(target, "", stateQuery+"\n", "database="+targetDatabase, "writer_role="+targetRole)
		targetAfter.ExpectedOutput = "false|0"
		return []Command{
			sourceBefore,
			psqlInput(source, "", fenceSQL, "database="+sourceDatabase, "writer_role="+sourceRole),
			sourceAfter,
			psqlInput(target, "", fenceSQL, "database="+targetDatabase, "writer_role="+targetRole),
			targetAfter,
		}, apply, []string{"source-writer-login-disabled", "source-writer-sessions-terminated", "target-writer-login-disabled", "target-writer-sessions-terminated"}, "", nil
	case Backfill:
		root := manifest.TargetRepo
		if request.repositoryRoot != "" {
			root = request.repositoryRoot
		}
		backfillPath, pathErr := canonicalBackfillPath(root, request.BackfillPath)
		if pathErr != nil {
			return nil, Request{}, nil, "", pathErr
		}
		apply.Name, apply.ArtifactPath = filepath.Base(backfillPath), backfillPath
		inputSHA, digestErr := digestFile(backfillPath, true)
		if digestErr != nil {
			return nil, Request{}, nil, "", digestErr
		}
		sourceDatabase, databaseErr := postgresIdentifier("source database", request.SourceDatabase)
		if databaseErr != nil {
			return nil, Request{}, nil, "", databaseErr
		}
		if _, _, _, _, identityErr := fenceIdentities(request); identityErr != nil {
			return nil, Request{}, nil, "", identityErr
		}
		capturedHighWater := latestDeltaInputSHA(manifest)
		if !validSHA256(capturedHighWater) {
			return nil, Request{}, nil, "", fmt.Errorf("captured source high-water evidence is unavailable; capture a new final delta")
		}
		targetDatabase, databaseErr := postgresIdentifier("target database", request.TargetDatabase)
		if databaseErr != nil {
			return nil, Request{}, nil, "", databaseErr
		}
		gateway, gatewayErr := postgresService(request.GatewayService)
		if gatewayErr != nil {
			return nil, Request{}, nil, "", gatewayErr
		}
		gatewayDatabase, gatewayDatabaseErr := postgresIdentifier("Gateway database", request.GatewayDatabase)
		if gatewayDatabaseErr != nil {
			return nil, Request{}, nil, "", gatewayDatabaseErr
		}
		if target == gateway && targetDatabase == gatewayDatabase {
			return nil, Request{}, nil, "", fmt.Errorf("Platform and Gateway databases must be separate")
		}
		command := Command{Name: "account-route-backfill", Args: []string{
			"--mapping", backfillPath,
			"--source-service", source,
			"--source-database", sourceDatabase,
			"--platform-service", target,
			"--platform-database", targetDatabase,
			"--gateway-service", gateway,
			"--gateway-database", gatewayDatabase,
		}}
		highWater := psql(source, sourceDatabase, `SELECT COALESCE(json_agg(row_to_json(mark) ORDER BY mark.relid)::text,'[]') FROM (SELECT relid::text,n_tup_ins,n_tup_upd,n_tup_del FROM pg_stat_user_tables) mark`)
		highWater.Env = []string{"PGOPTIONS=-c default_transaction_read_only=on"}
		highWater.ExpectedOutputSHA = capturedHighWater
		return []Command{highWater, command}, apply, []string{"source-high-water-equal", "mapping-content-stable", "durable-idempotent-saga", "pending-only"}, inputSHA, nil
	case Validate:
		apply.Verifier = request.Verifier
		realEnv := []string{"BT_DEV_REAL_STACK_ACCEPTANCE=1", "FT_MODE=test", "BT_DEV_ENABLE_LIVE_PRIVATE_WRITES=0", "BT_LIVE_PRIVATE_WRITES_ENABLED=0"}
		realBrowser := Command{Name: "npm", Args: []string{"--prefix", "web", "run", "test:e2e", "--", "platform-real.spec.ts"}, Dir: manifest.TargetRepo, Env: realEnv}
		return []Command{
			{Name: "go", Args: []string{"test", "./credential", "./routeprofile", "./secretstore", "./gateway/...", "./cmd/exchange-gateway", "./migration", "./cmd/migration-tool", "./cmd/account-route-backfill", "./scripts"}, Dir: manifest.TargetRepo},
			{Name: "go", Args: []string{"test", "./internal/config", "./internal/platform/accounts", "./internal/platform/secret", "./internal/store/postgres", "./cmd/platform-credential"}, Dir: filepath.Join(manifest.TargetRepo, "backend")},
			{Name: "make", Args: []string{"test-browser"}, Dir: manifest.TargetRepo},
			{Name: "make", Args: []string{"test-real"}, Dir: manifest.TargetRepo, Env: realEnv},
			realBrowser,
			{Name: "make", Args: []string{"dev-platform-restart"}, Dir: manifest.TargetRepo},
			{Name: "make", Args: []string{"test-real"}, Dir: manifest.TargetRepo, Env: realEnv},
			realBrowser,
		}, apply, []string{"offline-targeted", "fixture-browser", "real-test-read", "restart-recovery", "real-browser-repeat", "readiness-stream-idempotency"}, "", nil
	case Rollback:
		sourceDatabase, sourceRole, targetDatabase, targetRole, identityErr := fenceIdentities(request)
		if identityErr != nil {
			return nil, Request{}, nil, "", identityErr
		}
		stateQuery := `SELECT r.rolcanlogin::text || '|' || count(a.pid)::text FROM pg_roles r LEFT JOIN pg_stat_activity a ON a.usename=r.rolname AND a.pid<>pg_backend_pid() WHERE r.rolname=:'writer_role' GROUP BY r.rolcanlogin`
		restoreSQL := `ALTER ROLE :"writer_role" LOGIN; GRANT CONNECT ON DATABASE :"database" TO :"writer_role";` + "\n"
		sourceBefore := psqlInput(source, "", stateQuery+"\n", "database="+sourceDatabase, "writer_role="+sourceRole)
		sourceBefore.ExpectedOutput = "false|0"
		sourceAfter := psqlInput(source, "", stateQuery+"\n", "database="+sourceDatabase, "writer_role="+sourceRole)
		sourceAfter.ExpectedOutput = "true|0"
		targetAfter := psqlInput(target, "", stateQuery+"\n", "database="+targetDatabase, "writer_role="+targetRole)
		targetAfter.ExpectedOutput = "false|0"
		return []Command{
			sourceBefore,
			psqlInput(source, "", restoreSQL, "database="+sourceDatabase, "writer_role="+sourceRole),
			sourceAfter,
			targetAfter,
		}, apply, []string{"source-writer-login-restored", "source-writer-sessions-cleared", "target-writer-login-disabled", "target-writer-sessions-terminated"}, "", nil
	default:
		return nil, Request{}, nil, "", fmt.Errorf("operation %q has no executable plan", request.Operation)
	}
}

func containsIdentityToken(value, want string) bool {
	for _, token := range strings.FieldsFunc(strings.ToLower(value), func(char rune) bool { return char == '_' || char == '-' || char == '.' }) {
		if token == want {
			return true
		}
	}
	return false
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
	if command.ExpectedOutput != "" && strings.TrimSpace(string(result.Output)) != command.ExpectedOutput {
		return fmt.Errorf("%s postcondition verification failed", operation)
	}
	if command.ExpectedOutputSHA != "" && sha256Hex(bytes.TrimSpace(result.Output)) != command.ExpectedOutputSHA {
		return errSourceHighWaterMismatch
	}
	if command.Name != "psql" || len(command.Args) == 0 {
		return nil
	}
	query := command.Args[len(command.Args)-1]
	got := strings.TrimSpace(string(result.Output))
	if query == "SHOW default_transaction_read_only" || query == "SHOW transaction_read_only" {
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

func latestDeltaInputSHA(manifest Manifest) string {
	for index := len(manifest.Workflow.Events) - 1; index >= 0; index-- {
		if manifest.Workflow.Events[index].Operation == FinalDelta {
			return manifest.Workflow.Events[index].InputSHA
		}
	}
	return ""
}

func fenceIdentities(request ExecutionRequest) (string, string, string, string, error) {
	sourceDatabase, err := postgresIdentifier("source database", request.SourceDatabase)
	if err != nil {
		return "", "", "", "", err
	}
	sourceRole, err := postgresIdentifier("source writer role", request.SourceWriterRole)
	if err != nil {
		return "", "", "", "", err
	}
	targetDatabase, err := postgresIdentifier("target database", request.TargetDatabase)
	if err != nil {
		return "", "", "", "", err
	}
	targetRole, err := postgresIdentifier("target writer role", request.TargetWriterRole)
	if err != nil {
		return "", "", "", "", err
	}
	return sourceDatabase, sourceRole, targetDatabase, targetRole, nil
}

func postgresIdentifier(label, value string) (string, error) {
	if value == "" || len(value) > 63 || !((value[0] >= 'a' && value[0] <= 'z') || value[0] == '_') {
		return "", fmt.Errorf("%s is required and must be a lowercase PostgreSQL identifier", label)
	}
	for _, char := range value[1:] {
		if !((char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '_') {
			return "", fmt.Errorf("%s is required and must be a lowercase PostgreSQL identifier", label)
		}
	}
	return value, nil
}

func canonicalBackfillPath(root, value string) (string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("backfill script is outside the frozen repository")
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("backfill script is outside the frozen repository")
	}
	path := value
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("backfill script is unavailable")
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", fmt.Errorf("backfill script is outside the frozen repository")
	}
	parts := strings.Split(filepath.ToSlash(relative), "/")
	if len(parts) != 3 || parts[0] != "migration" || parts[1] != "backfills" || filepath.Ext(parts[2]) != ".json" {
		return "", fmt.Errorf("backfill artifact must be a repository-owned mapping")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("backfill script is unavailable")
	}
	tracked := exec.Command("git", "--no-replace-objects", "-C", root, "ls-files", "--error-unmatch", "--", filepath.ToSlash(relative))
	if tracked.Run() != nil {
		return "", fmt.Errorf("backfill script is not tracked by the frozen repository")
	}
	return path, nil
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
