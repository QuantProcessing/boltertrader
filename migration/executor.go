package migration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	MatchCapturedInput   bool
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
	SourceRole          string
	TargetRole          string
	GatewayRole         string
	MaintenanceRole     string
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
	if manifest.TargetInvalidatedAt != nil && request.Operation != FinalDelta {
		return ExecutionResult{}, fmt.Errorf("target is invalidated; a new explicit final delta is required")
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
	if request.Operation == Backfill {
		acceptedPath, acceptErr := acceptMappingSnapshot(applyRequest.ArtifactPath, request.EvidenceDir, inputSHA)
		if acceptErr != nil {
			return ExecutionResult{}, acceptErr
		}
		for index := range commands {
			if filepath.Base(commands[index].Name) == "account-route-backfill" {
				replaceCommandArg(commands[index].Args, "--mapping", acceptedPath)
			}
		}
		applyRequest.ArtifactPath = acceptedPath
		applyRequest.acceptedArtifactSHA = inputSHA
	}
	if priorOperation {
		if replay, replayErr := replayExecution(manifest, request.EvidenceDir, applyRequest, commands, inputSHA); replayErr != nil || replay != nil {
			if replayErr != nil {
				return ExecutionResult{}, replayErr
			}
			return *replay, nil
		}
	}
	if request.Operation == FinalDelta {
		if err := prepareAcceptedArchive(request.ArtifactPath); err != nil {
			return ExecutionResult{}, err
		}
	}
	run := options.Run
	if run == nil {
		run = runCommand
	}
	commandProofs := make([]commandEvidence, 0, len(commands))
	acceptedArchiveSHA := ""
	targetMutated := false
	for index, command := range commands {
		if request.Operation == FinalDelta && acceptedArchiveSHA != "" && index >= 8 {
			if currentSHA, archiveErr := acceptedArchiveSHA256(request.ArtifactPath); archiveErr != nil || currentSHA != acceptedArchiveSHA {
				return ExecutionResult{}, rejectChangedFinalDeltaArchive(request.ManifestPath, request.ArtifactPath, targetMutated, manifest, options)
			}
		}
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
		if command.MatchCapturedInput && (inputSHA == "" || sha256Hex(bytes.TrimSpace(result.Output)) != inputSHA) {
			if request.Operation == FinalDelta {
				if removeErr := os.Remove(request.ArtifactPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
					return ExecutionResult{}, fmt.Errorf("SOURCE_CHANGED_DURING_FINAL_DELTA: discard unsafe snapshot: %w", removeErr)
				}
				return ExecutionResult{}, fmt.Errorf("SOURCE_CHANGED_DURING_FINAL_DELTA: snapshot discarded; capture a new final delta")
			}
			return ExecutionResult{}, fmt.Errorf("%s captured input mismatch", request.Operation)
		}
		commandProofs = append(commandProofs, commandDigest(command, result.Output))
		if command.CaptureOutputAsInput {
			inputSHA = sha256Hex(bytes.TrimSpace(result.Output))
		}
		if request.Operation == FinalDelta && index == 6 {
			acceptedArchiveSHA, err = acceptedArchiveSHA256(request.ArtifactPath)
			if err != nil {
				_ = os.Remove(request.ArtifactPath)
				return ExecutionResult{}, fmt.Errorf("FINAL_DELTA_ARCHIVE_CHANGED: capture a new final delta")
			}
		}
		if request.Operation == FinalDelta && index == 8 {
			targetMutated = true
		}
		if request.Operation == FinalDelta && acceptedArchiveSHA != "" && index >= 8 {
			if currentSHA, archiveErr := acceptedArchiveSHA256(request.ArtifactPath); archiveErr != nil || currentSHA != acceptedArchiveSHA {
				return ExecutionResult{}, rejectChangedFinalDeltaArchive(request.ManifestPath, request.ArtifactPath, targetMutated, manifest, options)
			}
		}
	}
	if request.Operation == FinalDelta {
		applyRequest.acceptedArtifactSHA = acceptedArchiveSHA
	}
	artifactSHA := applyRequest.acceptedArtifactSHA
	if artifactSHA == "" {
		artifactSHA, err = digestFile(applyRequest.ArtifactPath, request.Operation == Backup || request.Operation == Restore || request.Operation == FinalDelta || request.Operation == Backfill)
		if err != nil {
			return ExecutionResult{}, err
		}
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
	apply := Request{Operation: request.Operation, ID: request.ID, Verifier: request.Verifier, Reason: request.Reason}
	source, err := postgresService(request.SourceService)
	if err != nil && (request.Operation == Backup || request.Operation == FinalDelta || request.Operation == Fence || request.Operation == Backfill || request.Operation == Rollback) {
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
		args := []string{"--no-psqlrc", "--set", "ON_ERROR_STOP=1", "--dbname", connection, "--tuples-only", "--no-align"}
		for _, variable := range variables {
			args = append(args, "--set", variable)
		}
		args = append(args, "--file", "-")
		return Command{Name: "psql", Args: args, Stdin: []byte(script)}
	}
	identityProbe := func(service, database, role, label string) (Command, error) {
		expectedDatabase, databaseErr := expectedTestIdentity(label+" database", database)
		if databaseErr != nil {
			return Command{}, databaseErr
		}
		expectedRole, roleErr := expectedTestIdentity(label+" connection role", role)
		if roleErr != nil {
			return Command{}, roleErr
		}
		probe := psqlInput(service, expectedDatabase, `SELECT (current_database()=:'expected_database' AND current_user=:'expected_role')::text;`+"\n", "expected_database="+expectedDatabase, "expected_role="+expectedRole)
		probe.ExpectedOutput = "true"
		probe.Env = []string{"PGOPTIONS=-c default_transaction_read_only=on"}
		return probe, nil
	}
	switch request.Operation {
	case Backup:
		sourceDatabase, databaseErr := postgresIdentifier("source database", request.SourceDatabase)
		if databaseErr != nil {
			return nil, Request{}, nil, "", databaseErr
		}
		probe, probeErr := identityProbe(source, sourceDatabase, request.SourceRole, "source")
		if probeErr != nil {
			return nil, Request{}, nil, "", probeErr
		}
		apply.ArtifactPath = request.ArtifactPath
		return []Command{
			probe,
			{Name: "pg_dump", Args: []string{"--format=custom", "--no-owner", "--no-privileges", "--file", request.ArtifactPath, "service=" + source + " dbname=" + sourceDatabase}},
			{Name: "pg_restore", Args: []string{"--list", request.ArtifactPath}},
		}, apply, []string{"connected-identities-verified", "dump-created", "dump-format-readable"}, "", nil
	case Restore:
		targetDatabase, databaseErr := postgresIdentifier("target database", request.TargetDatabase)
		if databaseErr != nil {
			return nil, Request{}, nil, "", databaseErr
		}
		maintenanceDatabase, databaseErr := postgresIdentifier("maintenance database", request.MaintenanceDatabase)
		if databaseErr != nil {
			return nil, Request{}, nil, "", databaseErr
		}
		targetProbe, probeErr := identityProbe(target, targetDatabase, request.TargetRole, "target")
		if probeErr != nil {
			return nil, Request{}, nil, "", probeErr
		}
		maintenanceProbe, probeErr := identityProbe(maintenance, maintenanceDatabase, request.MaintenanceRole, "maintenance")
		if probeErr != nil {
			return nil, Request{}, nil, "", probeErr
		}
		if err := requireDistinctExpectedIdentities(
			expectedIdentity{database: targetDatabase, role: request.TargetRole},
			expectedIdentity{database: maintenanceDatabase, role: request.MaintenanceRole},
		); err != nil {
			return nil, Request{}, nil, "", err
		}
		apply.ArtifactPath = request.ArtifactPath
		verify := psql(target, targetDatabase, "SELECT (count(*) > 0)::text FROM information_schema.tables WHERE table_schema NOT IN ('pg_catalog','information_schema')")
		verify.ExpectedOutput = "true"
		return []Command{
			targetProbe,
			maintenanceProbe,
			psqlInput(maintenance, maintenanceDatabase, `DROP DATABASE IF EXISTS :"database" WITH (FORCE); CREATE DATABASE :"database";`+"\n", "database="+targetDatabase),
			{Name: "pg_restore", Args: []string{"--exit-on-error", "--no-owner", "--no-privileges", "--dbname", "service=" + target + " dbname=" + targetDatabase, request.ArtifactPath}},
			verify,
		}, apply, []string{"connected-identities-verified", "clean-target-created", "restored-schema-nonempty"}, "", nil
	case FinalDelta:
		sourceDatabase, databaseErr := postgresIdentifier("source database", request.SourceDatabase)
		if databaseErr != nil {
			return nil, Request{}, nil, "", databaseErr
		}
		targetDatabase, databaseErr := postgresIdentifier("target database", request.TargetDatabase)
		if databaseErr != nil {
			return nil, Request{}, nil, "", databaseErr
		}
		sourceProbe, probeErr := identityProbe(source, sourceDatabase, request.SourceRole, "source")
		if probeErr != nil {
			return nil, Request{}, nil, "", probeErr
		}
		targetProbe, probeErr := identityProbe(target, targetDatabase, request.TargetRole, "target")
		if probeErr != nil {
			return nil, Request{}, nil, "", probeErr
		}
		maintenanceDatabase, databaseErr := postgresIdentifier("maintenance database", request.MaintenanceDatabase)
		if databaseErr != nil {
			return nil, Request{}, nil, "", databaseErr
		}
		maintenanceProbe, probeErr := identityProbe(maintenance, maintenanceDatabase, request.MaintenanceRole, "maintenance")
		if probeErr != nil {
			return nil, Request{}, nil, "", probeErr
		}
		if err := requireDistinctExpectedIdentities(
			expectedIdentity{database: sourceDatabase, role: request.SourceRole},
			expectedIdentity{database: targetDatabase, role: request.TargetRole},
			expectedIdentity{database: maintenanceDatabase, role: request.MaintenanceRole},
		); err != nil {
			return nil, Request{}, nil, "", err
		}
		apply.ArtifactPath = request.ArtifactPath
		readOnly := []string{"PGOPTIONS=-c default_transaction_read_only=on"}
		check := psql(source, sourceDatabase, "SHOW transaction_read_only")
		check.Env = readOnly
		check.ExpectedOutput = "on"
		verify := psql(target, targetDatabase, "SELECT (count(*) > 0)::text FROM information_schema.tables WHERE table_schema NOT IN ('pg_catalog','information_schema')")
		verify.ExpectedOutput = "true"
		highWaterBefore := psql(source, sourceDatabase, `SELECT COALESCE(json_agg(row_to_json(mark) ORDER BY mark.relid)::text,'[]') FROM (SELECT relid::text,n_tup_ins,n_tup_upd,n_tup_del FROM pg_stat_user_tables) mark`)
		highWaterBefore.Env = readOnly
		highWaterBefore.CaptureOutputAsInput = true
		highWaterAfter := highWaterBefore
		highWaterAfter.CaptureOutputAsInput = false
		highWaterAfter.MatchCapturedInput = true
		return []Command{
			sourceProbe,
			targetProbe,
			maintenanceProbe,
			check,
			highWaterBefore,
			{Name: "pg_dump", Args: []string{"--format=custom", "--no-owner", "--no-privileges", "--file", request.ArtifactPath, "service=" + source + " dbname=" + sourceDatabase}, Env: readOnly},
			highWaterAfter,
			{Name: "pg_restore", Args: []string{"--list", request.ArtifactPath}},
			psqlInput(maintenance, maintenanceDatabase, `DROP DATABASE IF EXISTS :"database" WITH (FORCE); CREATE DATABASE :"database";`+"\n", "database="+targetDatabase),
			{Name: "pg_restore", Args: []string{"--exit-on-error", "--no-owner", "--no-privileges", "--dbname", "service=" + target + " dbname=" + targetDatabase, request.ArtifactPath}},
			verify,
		}, apply, []string{"connected-identities-verified", "capture-session-read-only", "source-high-water-stable-across-dump", "final-snapshot-readable", "source-high-water-captured", "clean-target-created", "final-snapshot-applied", "restored-schema-nonempty"}, "", nil
	case Fence:
		sourceDatabase, sourceRole, targetDatabase, targetRole, identityErr := fenceIdentities(request)
		if identityErr != nil {
			return nil, Request{}, nil, "", identityErr
		}
		sourceProbe, probeErr := identityProbe(source, sourceDatabase, request.SourceRole, "source")
		if probeErr != nil {
			return nil, Request{}, nil, "", probeErr
		}
		targetProbe, probeErr := identityProbe(target, targetDatabase, request.TargetRole, "target")
		if probeErr != nil {
			return nil, Request{}, nil, "", probeErr
		}
		if err := requireDistinctExpectedIdentities(expectedIdentity{database: sourceDatabase, role: request.SourceRole}, expectedIdentity{database: targetDatabase, role: request.TargetRole}); err != nil {
			return nil, Request{}, nil, "", err
		}
		stateQuery := `SELECT r.rolcanlogin::text || '|' || count(a.pid)::text FROM pg_roles r LEFT JOIN pg_stat_activity a ON a.usename=r.rolname AND a.pid<>pg_backend_pid() WHERE r.rolname=:'writer_role' GROUP BY r.rolcanlogin`
		fenceSQL := `REVOKE CONNECT ON DATABASE :"database" FROM :"writer_role"; ALTER ROLE :"writer_role" NOLOGIN; SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE usename=:'writer_role' AND pid<>pg_backend_pid();` + "\n"
		sourceBefore := psqlInput(source, "", stateQuery+"\n", "database="+sourceDatabase, "writer_role="+sourceRole)
		sourceAfter := psqlInput(source, "", stateQuery+"\n", "database="+sourceDatabase, "writer_role="+sourceRole)
		sourceAfter.ExpectedOutput = "false|0"
		targetAfter := psqlInput(target, "", stateQuery+"\n", "database="+targetDatabase, "writer_role="+targetRole)
		targetAfter.ExpectedOutput = "false|0"
		return []Command{
			sourceProbe,
			targetProbe,
			sourceBefore,
			psqlInput(source, "", fenceSQL, "database="+sourceDatabase, "writer_role="+sourceRole),
			sourceAfter,
			psqlInput(target, "", fenceSQL, "database="+targetDatabase, "writer_role="+targetRole),
			targetAfter,
		}, apply, []string{"connected-identities-verified", "source-writer-login-disabled", "source-writer-sessions-terminated", "target-writer-login-disabled", "target-writer-sessions-terminated"}, "", nil
	case Backfill:
		root := manifest.TargetRepo
		if request.repositoryRoot != "" {
			root = request.repositoryRoot
		}
		backfillPath, pathErr := canonicalBackfillPath(root, request.BackfillPath)
		if pathErr != nil {
			return nil, Request{}, nil, "", pathErr
		}
		platformMigrateExecutable, executableErr := repositoryExecutable(root, "platform-migrate")
		if executableErr != nil {
			return nil, Request{}, nil, "", executableErr
		}
		backfillExecutable, executableErr := repositoryExecutable(root, "account-route-backfill")
		if executableErr != nil {
			return nil, Request{}, nil, "", executableErr
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
		sourceRole, roleErr := expectedTestIdentity("source connection role", request.SourceRole)
		if roleErr != nil {
			return nil, Request{}, nil, "", roleErr
		}
		targetRole, roleErr := expectedTestIdentity("target connection role", request.TargetRole)
		if roleErr != nil {
			return nil, Request{}, nil, "", roleErr
		}
		gatewayDatabase, gatewayDatabaseErr = expectedTestIdentity("Gateway database", gatewayDatabase)
		if gatewayDatabaseErr != nil {
			return nil, Request{}, nil, "", gatewayDatabaseErr
		}
		gatewayRole, roleErr := expectedTestIdentity("Gateway connection role", request.GatewayRole)
		if roleErr != nil {
			return nil, Request{}, nil, "", roleErr
		}
		if err := requireDistinctExpectedIdentities(
			expectedIdentity{database: sourceDatabase, role: sourceRole},
			expectedIdentity{database: targetDatabase, role: targetRole},
			expectedIdentity{database: gatewayDatabase, role: gatewayRole},
		); err != nil {
			return nil, Request{}, nil, "", err
		}
		command := Command{Name: backfillExecutable, Args: []string{
			"--mapping", backfillPath,
			"--mapping-sha256", inputSHA,
			"--source-service", source,
			"--source-database", sourceDatabase,
			"--source-role", sourceRole,
			"--platform-service", target,
			"--platform-database", targetDatabase,
			"--platform-role", targetRole,
			"--gateway-service", gateway,
			"--gateway-database", gatewayDatabase,
			"--gateway-role", gatewayRole,
		}}
		highWater := psql(source, sourceDatabase, `SELECT COALESCE(json_agg(row_to_json(mark) ORDER BY mark.relid)::text,'[]') FROM (SELECT relid::text,n_tup_ins,n_tup_upd,n_tup_del FROM pg_stat_user_tables) mark`)
		highWater.Env = []string{"PGOPTIONS=-c default_transaction_read_only=on"}
		highWater.ExpectedOutputSHA = capturedHighWater
		migrate := Command{Name: platformMigrateExecutable, Args: []string{"--service", target, "--database", targetDatabase, "--role", targetRole}}
		verifySchema := psql(target, targetDatabase, `SELECT (COALESCE((SELECT max(version) FROM platform_schema_migrations),0)=15 AND to_regclass('platform_account_routes') IS NOT NULL AND (SELECT count(*)=2 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='platform_accounts' AND column_name IN ('mode','native_environment')))::text`)
		verifySchema.ExpectedOutput = "true"
		sourceProbe, probeErr := identityProbe(source, sourceDatabase, sourceRole, "source")
		if probeErr != nil {
			return nil, Request{}, nil, "", probeErr
		}
		return []Command{sourceProbe, highWater, migrate, verifySchema, command}, apply, []string{"connected-identities-verified", "source-high-water-equal", "platform-schema-migrated", "mapping-content-stable", "durable-idempotent-saga", "pending-only"}, inputSHA, nil
	case Validate:
		apply.Verifier = request.Verifier
		realEnv := []string{"BT_DEV_REAL_STACK_ACCEPTANCE=1", "FT_MODE=test", "BT_DEV_ENABLE_LIVE_PRIVATE_WRITES=0", "BT_LIVE_PRIVATE_WRITES_ENABLED=0"}
		realBrowser := Command{Name: "npm", Args: []string{"--prefix", "web", "run", "test:e2e", "--", "platform-real.spec.ts"}, Dir: manifest.TargetRepo, Env: realEnv}
		return []Command{
			{Name: "go", Args: []string{"test", "./credential", "./routeprofile", "./secretstore", "./gateway/...", "./cmd/exchange-gateway", "./migration", "./cmd/migration-tool", "./cmd/account-route-backfill", "./scripts"}, Dir: manifest.TargetRepo},
			{Name: "go", Args: []string{"test", "./internal/config", "./internal/platform/accounts", "./internal/platform/secret", "./internal/store/postgres", "./cmd/platform-credential", "./cmd/platform-migrate"}, Dir: filepath.Join(manifest.TargetRepo, "backend")},
			{Name: "make", Args: []string{"test-browser"}, Dir: manifest.TargetRepo},
			{Name: "make", Args: []string{"test-real"}, Dir: manifest.TargetRepo, Env: realEnv},
			realBrowser,
			{Name: "make", Args: []string{"dev-platform-restart"}, Dir: manifest.TargetRepo},
			{Name: "make", Args: []string{"test-real"}, Dir: manifest.TargetRepo, Env: realEnv},
			realBrowser,
		}, apply, []string{"offline-targeted", "fixture-browser", "real-test-read", "restart-recovery", "real-browser-repeat", "readiness-stream-idempotency"}, "", nil
	case Rollback:
		sourceDatabase, sourceWriterRole, targetDatabase, targetWriterRole, identityErr := fenceIdentities(request)
		if identityErr != nil {
			return nil, Request{}, nil, "", identityErr
		}
		sourceProbe, probeErr := identityProbe(source, sourceDatabase, request.SourceRole, "source")
		if probeErr != nil {
			return nil, Request{}, nil, "", probeErr
		}
		targetProbe, probeErr := identityProbe(target, targetDatabase, request.TargetRole, "target")
		if probeErr != nil {
			return nil, Request{}, nil, "", probeErr
		}
		if err := requireDistinctExpectedIdentities(expectedIdentity{database: sourceDatabase, role: request.SourceRole}, expectedIdentity{database: targetDatabase, role: request.TargetRole}); err != nil {
			return nil, Request{}, nil, "", err
		}
		stateQuery := `SELECT r.rolcanlogin::text || '|' || count(a.pid)::text FROM pg_roles r LEFT JOIN pg_stat_activity a ON a.usename=r.rolname AND a.pid<>pg_backend_pid() WHERE r.rolname=:'writer_role' GROUP BY r.rolcanlogin`
		restoreSQL := `ALTER ROLE :"writer_role" LOGIN; GRANT CONNECT ON DATABASE :"database" TO :"writer_role";` + "\n"
		sourceBefore := psqlInput(source, "", stateQuery+"\n", "database="+sourceDatabase, "writer_role="+sourceWriterRole)
		sourceBefore.ExpectedOutput = "false|0"
		sourceAfter := psqlInput(source, "", stateQuery+"\n", "database="+sourceDatabase, "writer_role="+sourceWriterRole)
		sourceAfter.ExpectedOutput = "true|0"
		targetAfter := psqlInput(target, "", stateQuery+"\n", "database="+targetDatabase, "writer_role="+targetWriterRole)
		targetAfter.ExpectedOutput = "false|0"
		return []Command{
			sourceProbe,
			targetProbe,
			sourceBefore,
			psqlInput(source, "", restoreSQL, "database="+sourceDatabase, "writer_role="+sourceWriterRole),
			sourceAfter,
			targetAfter,
		}, apply, []string{"connected-identities-verified", "source-writer-login-restored", "source-writer-sessions-cleared", "target-writer-login-disabled", "target-writer-sessions-terminated"}, "", nil
	default:
		return nil, Request{}, nil, "", fmt.Errorf("operation %q has no executable plan", request.Operation)
	}
}

func replaceCommandArg(args []string, flag, value string) {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == flag {
			args[index+1] = value
			return
		}
	}
}

func acceptMappingSnapshot(sourcePath, evidenceDir, expectedSHA string) (string, error) {
	if !validSHA256(expectedSHA) {
		return "", fmt.Errorf("mapping checksum is invalid")
	}
	file, err := os.Open(sourcePath)
	if err != nil {
		return "", fmt.Errorf("backfill mapping snapshot is unavailable")
	}
	info, statErr := file.Stat()
	if statErr != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 1<<20 {
		_ = file.Close()
		return "", fmt.Errorf("backfill mapping snapshot is unavailable")
	}
	data, readErr := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(data) == 0 || len(data) > 1<<20 || sha256Hex(data) != expectedSHA {
		clear(data)
		return "", fmt.Errorf("backfill mapping changed before acceptance")
	}
	if err := os.MkdirAll(evidenceDir, 0o700); err != nil {
		clear(data)
		return "", fmt.Errorf("create accepted mapping directory: %w", err)
	}
	path := filepath.Join(evidenceDir, ".accepted-mapping-"+expectedSHA+".json")
	if info, statErr := os.Lstat(path); statErr == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			clear(data)
			return "", fmt.Errorf("accepted mapping snapshot conflict")
		}
		existing, readErr := os.ReadFile(path)
		if readErr != nil || sha256Hex(existing) != expectedSHA {
			clear(data)
			clear(existing)
			return "", fmt.Errorf("accepted mapping snapshot conflict")
		}
		clear(data)
		clear(existing)
		return path, nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		clear(data)
		return "", fmt.Errorf("accepted mapping snapshot conflict")
	}
	accepted, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		clear(data)
		return "", fmt.Errorf("create accepted mapping snapshot: %w", err)
	}
	_, writeErr := accepted.Write(data)
	syncErr := accepted.Sync()
	closeErr = accepted.Close()
	clear(data)
	if writeErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("write accepted mapping snapshot")
	}
	return path, nil
}

func prepareAcceptedArchive(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("final delta artifact path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("prepare final delta artifact: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("final delta accepted archive path must not already exist")
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("prepare final delta artifact: %w", err)
	}
	return nil
}

func acceptedArchiveSHA256(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() <= 0 {
		return "", fmt.Errorf("accepted final delta archive is unavailable")
	}
	return digestFile(path, true)
}

func rejectChangedFinalDeltaArchive(manifestPath, artifactPath string, targetMutated bool, manifest Manifest, options ExecuteOptions) error {
	_ = os.Remove(artifactPath)
	if targetMutated {
		now := time.Now().UTC()
		if options.Now != nil {
			now = options.Now().UTC()
		}
		at := now.Format(time.RFC3339Nano)
		reason := "FINAL_DELTA_ARCHIVE_CHANGED"
		manifest.TargetInvalidatedAt = &at
		manifest.TargetInvalidationReason = &reason
		if err := writeManifestAtomic(manifestPath, manifest); err != nil {
			return fmt.Errorf("FINAL_DELTA_ARCHIVE_CHANGED: persist target invalidation: %w", err)
		}
	}
	return fmt.Errorf("FINAL_DELTA_ARCHIVE_CHANGED: accepted archive discarded; new explicit final delta required")
}

func containsIdentityToken(value, want string) bool {
	for _, token := range strings.FieldsFunc(strings.ToLower(value), func(char rune) bool { return char == '_' || char == '-' || char == '.' }) {
		if token == want {
			return true
		}
	}
	return false
}

type expectedIdentity struct {
	database string
	role     string
}

func expectedTestIdentity(label, value string) (string, error) {
	identity, err := postgresIdentifier(label, value)
	if err != nil {
		return "", err
	}
	if !containsIdentityToken(identity, "test") || containsIdentityToken(identity, "live") {
		return "", fmt.Errorf("cross-mode or unscoped identity rejected: %s must be Test-scoped", label)
	}
	return identity, nil
}

func requireDistinctExpectedIdentities(identities ...expectedIdentity) error {
	seen := make(map[expectedIdentity]struct{}, len(identities))
	for _, identity := range identities {
		if _, duplicate := seen[identity]; duplicate {
			return fmt.Errorf("expected Test-scoped identities must be distinct")
		}
		seen[identity] = struct{}{}
	}
	return nil
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
	sourceDatabase, err := expectedTestIdentity("source database", request.SourceDatabase)
	if err != nil {
		return "", "", "", "", err
	}
	sourceRole, err := expectedTestIdentity("source writer role", request.SourceWriterRole)
	if err != nil {
		return "", "", "", "", err
	}
	targetDatabase, err := expectedTestIdentity("target database", request.TargetDatabase)
	if err != nil {
		return "", "", "", "", err
	}
	targetRole, err := expectedTestIdentity("target writer role", request.TargetWriterRole)
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

func repositoryExecutable(root, name string) (string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("repository executable %s has an invalid root", name)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("repository executable %s has an invalid root", name)
	}
	path := filepath.Join(root, ".local", "bin", name)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return "", fmt.Errorf("repository executable %s is missing, non-executable, or not a regular file", name)
	}
	return path, nil
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
