package migration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type ActivationBoundary string

const (
	ActivationBegun           ActivationBoundary = "begun"
	ActivationGatewayPromoted ActivationBoundary = "gateway-promoted"
	ActivationWritersGranted  ActivationBoundary = "writers-granted"
	ActivationConnectRestored ActivationBoundary = "connect-restored"
	ActivationServicesStarted ActivationBoundary = "services-started"
	ActivationVerified        ActivationBoundary = "verified"
)

type ActivationAttempt struct {
	ID                      string             `json:"id"`
	InputSHA256             string             `json:"inputSha256"`
	ExpectedPromotionSHA256 string             `json:"expectedPromotionSha256"`
	Boundary                ActivationBoundary `json:"boundary"`
	StartedAt               time.Time          `json:"startedAt"`
	UpdatedAt               time.Time          `json:"updatedAt"`
	GatewayBeforeSHA256     string             `json:"gatewayBeforeSha256,omitempty"`
	GatewayAfterSHA256      string             `json:"gatewayAfterSha256,omitempty"`
	JournalSHA256           string             `json:"journalSha256,omitempty"`
	EvidencePath            string             `json:"evidencePath,omitempty"`
	ProofSHA256             string             `json:"proofSha256,omitempty"`
}

type DatabaseIdentity struct {
	Service  string `json:"service"`
	Database string `json:"database"`
	Role     string `json:"role"`
}

type ActivationInput struct {
	MigrationID, ManifestPath, EvidenceDir, JournalPath string
	Source, Platform, Gateway, Maintenance              DatabaseIdentity
	PlatformWriterRole, GatewayWriterRole               string
}

// ActivationDependencies keeps the irreversible operation injectable and
// makes every completed boundary independently re-verifiable on replay.
type ActivationDependencies struct {
	Now                     func() time.Time
	LoadAttempt             func(context.Context) (*ActivationAttempt, error)
	SaveAttempt             func(context.Context, ActivationAttempt) error
	ExpectedPromotion       func(context.Context) (string, string, error)
	VerifySourceFence       func(context.Context) error
	StopServices            func(context.Context) error
	PromoteGateway          func(context.Context) (string, error)
	GrantWriters            func(context.Context) error
	RestoreConnect          func(context.Context) error
	StartServices           func(context.Context) error
	VerifyActivation        func(context.Context) error
	ContainPostGrantFailure func(context.Context) error
}

func ExecuteActivation(ctx context.Context, input ActivationInput, dependencies ActivationDependencies) (ExecutionResult, error) {
	if err := validateActivationInput(input); err != nil {
		return ExecutionResult{}, err
	}
	if dependencies.LoadAttempt == nil || dependencies.SaveAttempt == nil || dependencies.ExpectedPromotion == nil ||
		dependencies.VerifySourceFence == nil || dependencies.StopServices == nil || dependencies.PromoteGateway == nil ||
		dependencies.GrantWriters == nil || dependencies.RestoreConnect == nil || dependencies.StartServices == nil || dependencies.VerifyActivation == nil {
		return ExecutionResult{}, fmt.Errorf("activation dependencies are incomplete")
	}
	now := dependencies.Now
	if now == nil {
		now = time.Now
	}
	inputSHA, err := activationInputSHA(input)
	if err != nil {
		return ExecutionResult{}, err
	}
	beforeSHA, expectedSHA, err := dependencies.ExpectedPromotion(ctx)
	if err != nil || !validSHA256(beforeSHA) || !validSHA256(expectedSHA) {
		return ExecutionResult{}, fmt.Errorf("activation promotion snapshot is invalid")
	}
	attempt, err := dependencies.LoadAttempt(ctx)
	if err != nil {
		return ExecutionResult{}, fmt.Errorf("load activation attempt: %w", err)
	}
	if attempt == nil {
		stamp := now().UTC()
		idSeed := sha256.Sum256([]byte(inputSHA + "\x00" + stamp.Format(time.RFC3339Nano)))
		attempt = &ActivationAttempt{ID: hex.EncodeToString(idSeed[:16]), InputSHA256: inputSHA, ExpectedPromotionSHA256: expectedSHA, Boundary: ActivationBegun, StartedAt: stamp, UpdatedAt: stamp, GatewayBeforeSHA256: beforeSHA}
		if err := persistActivationBoundary(ctx, input.JournalPath, dependencies.SaveAttempt, attempt); err != nil {
			return ExecutionResult{}, err
		}
	} else {
		if attempt.InputSHA256 != inputSHA || attempt.ExpectedPromotionSHA256 != expectedSHA {
			return ExecutionResult{}, fmt.Errorf("activation attempt input changed")
		}
		if err := verifyActivationJournal(input.JournalPath, attempt.JournalSHA256); err != nil {
			return ExecutionResult{}, err
		}
	}
	if attempt.Boundary == ActivationVerified {
		if err := dependencies.VerifySourceFence(ctx); err != nil {
			return ExecutionResult{}, fmt.Errorf("activation source fence verification failed: %w", err)
		}
		if err := dependencies.VerifyActivation(ctx); err != nil {
			return ExecutionResult{}, fmt.Errorf("activation replay verification failed: %w", err)
		}
		return ExecutionResult{Result: Result{Phase: PhaseActivated, Replayed: true}, EvidencePath: attempt.EvidencePath, InputSHA256: inputSHA}, nil
	}

	postGrant := boundaryAtLeast(attempt.Boundary, ActivationWritersGranted)
	fail := func(stage string, cause error) (ExecutionResult, error) {
		if postGrant && dependencies.ContainPostGrantFailure != nil {
			if containErr := dependencies.ContainPostGrantFailure(ctx); containErr != nil {
				return ExecutionResult{}, fmt.Errorf("activation %s failed: %v; containment failed: %v", stage, cause, containErr)
			}
		}
		return ExecutionResult{}, fmt.Errorf("activation %s failed: %w", stage, cause)
	}
	if err := dependencies.VerifySourceFence(ctx); err != nil {
		return fail("source fence verification", err)
	}
	if !boundaryAtLeast(attempt.Boundary, ActivationGatewayPromoted) {
		if err := dependencies.StopServices(ctx); err != nil {
			return fail("service stop", err)
		}
		afterSHA, err := dependencies.PromoteGateway(ctx)
		if err != nil || afterSHA != expectedSHA {
			if err == nil {
				err = fmt.Errorf("promotion digest mismatch")
			}
			return fail("Gateway promotion", err)
		}
		attempt.GatewayAfterSHA256 = afterSHA
		attempt.Boundary = ActivationGatewayPromoted
		attempt.UpdatedAt = now().UTC()
		if err := persistActivationBoundary(ctx, input.JournalPath, dependencies.SaveAttempt, attempt); err != nil {
			return ExecutionResult{}, err
		}
	}
	if !boundaryAtLeast(attempt.Boundary, ActivationWritersGranted) {
		if err := dependencies.GrantWriters(ctx); err != nil {
			return fail("writer grant", err)
		}
		postGrant = true
		attempt.Boundary, attempt.UpdatedAt = ActivationWritersGranted, now().UTC()
		if err := persistActivationBoundary(ctx, input.JournalPath, dependencies.SaveAttempt, attempt); err != nil {
			return fail("writer boundary persistence", err)
		}
	}
	if !boundaryAtLeast(attempt.Boundary, ActivationConnectRestored) {
		if err := dependencies.RestoreConnect(ctx); err != nil {
			return fail("CONNECT restore", err)
		}
		attempt.Boundary, attempt.UpdatedAt = ActivationConnectRestored, now().UTC()
		if err := persistActivationBoundary(ctx, input.JournalPath, dependencies.SaveAttempt, attempt); err != nil {
			return fail("CONNECT boundary persistence", err)
		}
	}
	if !boundaryAtLeast(attempt.Boundary, ActivationServicesStarted) {
		if err := dependencies.StartServices(ctx); err != nil {
			return fail("service start", err)
		}
		attempt.Boundary, attempt.UpdatedAt = ActivationServicesStarted, now().UTC()
		if err := persistActivationBoundary(ctx, input.JournalPath, dependencies.SaveAttempt, attempt); err != nil {
			return fail("service boundary persistence", err)
		}
	}
	if err := dependencies.VerifyActivation(ctx); err != nil {
		return fail("verification", err)
	}
	verifiedAt := now().UTC()
	if !verifiedAt.After(attempt.StartedAt) {
		verifiedAt = attempt.StartedAt.Add(time.Nanosecond)
	}
	attempt.Boundary, attempt.UpdatedAt = ActivationVerified, verifiedAt
	if err := persistActivationBoundary(ctx, input.JournalPath, dependencies.SaveAttempt, attempt); err != nil {
		return fail("verified boundary persistence", err)
	}
	return ExecutionResult{Result: Result{Phase: PhaseActivated, Replayed: attempt.EvidencePath != ""}, EvidencePath: attempt.EvidencePath, InputSHA256: inputSHA}, nil
}

func verifyActivationJournal(path, expectedSHA string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("activation journal is unavailable or unsafe")
	}
	data, err := os.ReadFile(path)
	if err != nil || sha256Hex(data) != expectedSHA {
		return fmt.Errorf("activation journal changed")
	}
	return nil
}

func validateActivationInput(input ActivationInput) error {
	if strings.TrimSpace(input.MigrationID) == "" || strings.TrimSpace(input.ManifestPath) == "" || strings.TrimSpace(input.EvidenceDir) == "" || strings.TrimSpace(input.JournalPath) == "" {
		return fmt.Errorf("activation paths and migration id are required")
	}
	for label, identity := range map[string]DatabaseIdentity{"source": input.Source, "Platform": input.Platform, "Gateway": input.Gateway, "maintenance": input.Maintenance} {
		if _, err := postgresService(identity.Service); err != nil {
			return fmt.Errorf("%s %w", label, err)
		}
		if _, err := expectedTestIdentity(label+" database", identity.Database); err != nil {
			return err
		}
		if _, err := expectedTestIdentity(label+" role", identity.Role); err != nil {
			return err
		}
	}
	if _, err := expectedTestIdentity("Platform writer role", input.PlatformWriterRole); err != nil {
		return err
	}
	if _, err := expectedTestIdentity("Gateway writer role", input.GatewayWriterRole); err != nil {
		return err
	}
	if input.Platform.Database == input.Gateway.Database || input.Platform.Role == input.Gateway.Role {
		return fmt.Errorf("Platform and Gateway identities must be separate")
	}
	return nil
}

func activationInputSHA(input ActivationInput) (string, error) {
	data, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf("encode activation input: %w", err)
	}
	return sha256Hex(data), nil
}

func boundaryAtLeast(got, want ActivationBoundary) bool {
	order := map[ActivationBoundary]int{ActivationBegun: 1, ActivationGatewayPromoted: 2, ActivationWritersGranted: 3, ActivationConnectRestored: 4, ActivationServicesStarted: 5, ActivationVerified: 6}
	return order[got] >= order[want] && order[got] != 0
}

func persistActivationBoundary(ctx context.Context, journalPath string, save func(context.Context, ActivationAttempt) error, attempt *ActivationAttempt) error {
	data, err := json.MarshalIndent(attempt, "", "  ")
	if err != nil {
		return fmt.Errorf("encode activation journal: %w", err)
	}
	data = append(data, '\n')
	if err := writeActivationJournal(journalPath, data); err != nil {
		return err
	}
	attempt.JournalSHA256 = sha256Hex(data)
	if err := save(ctx, *attempt); err != nil {
		return fmt.Errorf("persist activation attempt: %w", err)
	}
	return nil
}

func writeActivationJournal(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create activation journal directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".activation-journal-*")
	if err != nil {
		return fmt.Errorf("create activation journal: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("publish activation journal: %w", err)
	}
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func executeActivationLocked(ctx context.Context, request ExecutionRequest, options ExecuteOptions, manifest Manifest) (ExecutionResult, error) {
	if manifest.Workflow.Phase != PhaseActivationReady && manifest.Workflow.Phase != PhaseActivated {
		return ExecutionResult{}, fmt.Errorf("activation requires activation-ready preparation; current phase is %s", manifest.Workflow.Phase)
	}
	if options.Activation == nil {
		return ExecutionResult{}, fmt.Errorf("canonical activation dependencies are required")
	}
	journalPath := request.JournalPath
	if strings.TrimSpace(journalPath) == "" {
		journalPath = filepath.Join(request.EvidenceDir, "activation-attempt.json")
	}
	input := ActivationInput{
		MigrationID: manifest.MigrationID, ManifestPath: request.ManifestPath, EvidenceDir: request.EvidenceDir, JournalPath: journalPath,
		Source:             DatabaseIdentity{Service: request.SourceService, Database: request.SourceDatabase, Role: request.SourceRole},
		Platform:           DatabaseIdentity{Service: request.TargetService, Database: request.TargetDatabase, Role: request.TargetRole},
		Gateway:            DatabaseIdentity{Service: request.GatewayService, Database: request.GatewayDatabase, Role: request.GatewayRole},
		Maintenance:        DatabaseIdentity{Service: request.MaintenanceService, Database: request.MaintenanceDatabase, Role: request.MaintenanceRole},
		PlatformWriterRole: request.TargetWriterRole, GatewayWriterRole: request.GatewayWriterRole,
	}
	var dependencies ActivationDependencies
	if options.Activation == nil {
		defaults, err := defaultActivationDependencies(ctx, request, options)
		if err != nil {
			return ExecutionResult{}, err
		}
		dependencies = defaults
	} else {
		dependencies = *options.Activation
	}
	dependencies.Now = options.Now
	dependencies.LoadAttempt = func(context.Context) (*ActivationAttempt, error) {
		if manifest.ActivationAttempt == nil {
			return nil, nil
		}
		copy := *manifest.ActivationAttempt
		return &copy, nil
	}
	dependencies.SaveAttempt = func(_ context.Context, attempt ActivationAttempt) error {
		manifest.ActivationAttempt = &attempt
		return writeManifestAtomic(request.ManifestPath, manifest)
	}
	result, err := ExecuteActivation(ctx, input, dependencies)
	if err != nil {
		return ExecutionResult{}, err
	}
	if manifest.Workflow.Phase == PhaseActivated {
		return result, nil
	}
	manifestData, err := os.ReadFile(request.ManifestPath)
	if err != nil {
		return ExecutionResult{}, fmt.Errorf("read activation manifest: %w", err)
	}
	manifest, err = DecodeManifest(manifestData)
	if err != nil {
		return ExecutionResult{}, err
	}
	proof := operationProof{
		SchemaVersion: 1, Operation: Activate, Status: "pass", MigrationID: manifest.MigrationID,
		Subject: proofStateDigest(manifest), Checks: []string{"source-fenced", "gateway-promoted", "writers-granted", "connect-restored", "services-started", "activation-verified"},
		GeneratedBy: evidenceGenerator, PreStateSHA: sha256Hex(manifestData), InputSHA: result.InputSHA256,
		Commands: []commandEvidence{{Executable: "canonical-activation", ArgvSHA256: result.InputSHA256, OutputSHA: sha256Hex([]byte("verified"))}},
	}
	path, err := writeExecutionEvidence(request.EvidenceDir, len(manifest.Workflow.Events)+1, Activate, proof)
	if err != nil {
		return ExecutionResult{}, err
	}
	applyRequest := Request{Operation: Activate, ProofPath: path, ActivationAt: manifest.ActivationAttempt.StartedAt.Format(time.RFC3339Nano), CutoverAt: manifest.ActivationAttempt.UpdatedAt.Format(time.RFC3339Nano), generatedEvidence: &proof, capturedInputSHA: result.InputSHA256}
	applied, err := applyLocked(request.ManifestPath, applyRequest, ApplyOptions{Now: func() time.Time { return manifest.ActivationAttempt.UpdatedAt }, execution: &executionCapability{}})
	if err != nil {
		_ = os.Remove(path)
		return ExecutionResult{}, err
	}
	completedData, err := os.ReadFile(request.ManifestPath)
	if err != nil {
		return ExecutionResult{}, err
	}
	completed, err := DecodeManifest(completedData)
	if err != nil {
		return ExecutionResult{}, err
	}
	completed.ActivationAttempt.EvidencePath = path
	completed.ActivationAttempt.ProofSHA256 = sha256Hex(mustReadActivationEvidence(path))
	if err := writeManifestAtomic(request.ManifestPath, completed); err != nil {
		return ExecutionResult{}, err
	}
	return ExecutionResult{Result: applied, EvidencePath: path, InputSHA256: result.InputSHA256}, nil
}

func defaultActivationDependencies(_ context.Context, request ExecutionRequest, options ExecuteOptions) (ActivationDependencies, error) {
	run := options.Run
	if run == nil {
		run = runCommand
	}
	psql := func(service, database, script string, variables ...string) Command {
		args := []string{"--no-psqlrc", "--set", "ON_ERROR_STOP=1", "--dbname", "service=" + service + " dbname=" + database, "--tuples-only", "--no-align"}
		for _, variable := range variables {
			args = append(args, "--set", variable)
		}
		args = append(args, "--file", "-")
		return Command{Name: "psql", Args: args, Stdin: []byte(script)}
	}
	runChecked := func(ctx context.Context, command Command, expected string) (string, error) {
		result, err := run(ctx, command)
		if err != nil {
			return "", err
		}
		output := strings.TrimSpace(string(result.Output))
		if expected != "" && output != expected {
			return "", fmt.Errorf("postcondition mismatch")
		}
		return output, nil
	}
	sourceState := psql(request.SourceService, request.SourceDatabase, `SELECT (NOT rolcanlogin AND NOT EXISTS (SELECT 1 FROM pg_stat_activity WHERE usename=:'writer_role' AND pid<>pg_backend_pid()))::text FROM pg_roles WHERE rolname=:'writer_role';`+"\n", "writer_role="+request.SourceWriterRole)
	// The operation-set digest deliberately excludes pending/active state, so it
	// remains stable across the one-way head promotion transaction.
	promotionSet := psql(request.GatewayService, request.GatewayDatabase, `SELECT COALESCE(json_agg(row_to_json(item) ORDER BY item.account_id)::text,'[]') FROM (SELECT head.account_id::text,pending.operation_id::text,pending.desired_state_revision,pending.runtime_generation,pending.credential_version,pending.request_digest FROM gateway_account_runtime_heads head JOIN gateway_runtime_snapshots pending ON pending.snapshot_id=COALESCE(head.pending_snapshot_id,head.active_snapshot_id)) item;`+"\n")
	promote := psql(request.GatewayService, request.GatewayDatabase, `BEGIN; SELECT pg_advisory_xact_lock(hashtextextended('G010-activation',0)); UPDATE gateway_desired_operations operation SET state='applied',applied_at=now(),updated_at=now() FROM gateway_account_runtime_heads head,gateway_runtime_snapshots snapshot WHERE head.pending_snapshot_id=snapshot.snapshot_id AND operation.operation_id=snapshot.operation_id AND operation.state IN ('prepared','candidate_ready'); UPDATE gateway_account_runtime_heads SET active_snapshot_id=pending_snapshot_id,pending_snapshot_id=NULL,updated_at=now() WHERE pending_snapshot_id IS NOT NULL; COMMIT;`+"\n")
	grant := psql(request.MaintenanceService, request.MaintenanceDatabase, `BEGIN; GRANT :"platform_writer" TO :"platform_runtime"; GRANT :"gateway_writer" TO :"gateway_runtime"; COMMIT;`+"\n", "platform_writer="+request.TargetWriterRole, "platform_runtime="+request.TargetRole, "gateway_writer="+request.GatewayWriterRole, "gateway_runtime="+request.GatewayRole)
	restore := psql(request.MaintenanceService, request.MaintenanceDatabase, `GRANT CONNECT ON DATABASE :"platform_database" TO :"platform_runtime"; GRANT CONNECT ON DATABASE :"gateway_database" TO :"gateway_runtime";`+"\n", "platform_database="+request.TargetDatabase, "platform_runtime="+request.TargetRole, "gateway_database="+request.GatewayDatabase, "gateway_runtime="+request.GatewayRole)
	contain := psql(request.MaintenanceService, request.MaintenanceDatabase, `REVOKE CONNECT ON DATABASE :"platform_database" FROM :"platform_runtime"; REVOKE CONNECT ON DATABASE :"gateway_database" FROM :"gateway_runtime"; SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE (datname=:'platform_database' AND usename=:'platform_runtime') OR (datname=:'gateway_database' AND usename=:'gateway_runtime');`+"\n", "platform_database="+request.TargetDatabase, "platform_runtime="+request.TargetRole, "gateway_database="+request.GatewayDatabase, "gateway_runtime="+request.GatewayRole)
	postgresProbe := psql(request.MaintenanceService, request.MaintenanceDatabase, `SELECT pg_postmaster_start_time()::text;`+"\n")
	supervisor := filepath.Join(filepath.Dir(filepath.Dir(request.ManifestPath)), "scripts", "dev-platform")
	if request.repositoryRoot != "" {
		supervisor = filepath.Join(request.repositoryRoot, "scripts", "dev-platform")
	}
	promotionDigest := func(ctx context.Context) (string, error) {
		output, err := runChecked(ctx, promotionSet, "")
		if err != nil {
			return "", err
		}
		return sha256Hex([]byte(output)), nil
	}
	postgresIdentity := ""
	verifyPostgresIdentity := func(ctx context.Context) error {
		databaseStart, err := runChecked(ctx, postgresProbe, "")
		if err != nil {
			return err
		}
		sealed, err := sealedPostgresIdentity(request.StateDir)
		if err != nil {
			return err
		}
		if databaseStart == "" {
			return fmt.Errorf("PostgreSQL start time is unavailable")
		}
		observed := sealed + "|" + databaseStart
		if postgresIdentity == "" {
			postgresIdentity = observed
			return nil
		}
		if observed != postgresIdentity {
			return fmt.Errorf("PostgreSQL PID/start time changed during activation")
		}
		return nil
	}
	return ActivationDependencies{
		Now: options.Now,
		ExpectedPromotion: func(ctx context.Context) (string, string, error) {
			if err := verifyPostgresIdentity(ctx); err != nil {
				return "", "", err
			}
			digest, err := promotionDigest(ctx)
			return digest, digest, err
		},
		VerifySourceFence: func(ctx context.Context) error { _, err := runChecked(ctx, sourceState, "true"); return err },
		StopServices: func(ctx context.Context) error {
			if _, err := runChecked(ctx, Command{Name: supervisor, Args: []string{"stop-app"}}, ""); err != nil {
				return err
			}
			return verifyPostgresIdentity(ctx)
		},
		PromoteGateway: func(ctx context.Context) (string, error) {
			if _, err := runChecked(ctx, promote, ""); err != nil {
				return "", err
			}
			if err := verifyPostgresIdentity(ctx); err != nil {
				return "", err
			}
			return promotionDigest(ctx)
		},
		GrantWriters: func(ctx context.Context) error {
			if _, err := runChecked(ctx, grant, ""); err != nil {
				return err
			}
			return verifyPostgresIdentity(ctx)
		},
		RestoreConnect: func(ctx context.Context) error {
			if _, err := runChecked(ctx, restore, ""); err != nil {
				return err
			}
			return verifyPostgresIdentity(ctx)
		},
		StartServices: func(ctx context.Context) error {
			_, err := runChecked(ctx, Command{Name: supervisor, Args: []string{"start-app"}}, "")
			if err != nil {
				return err
			}
			return verifyPostgresIdentity(ctx)
		},
		VerifyActivation: func(ctx context.Context) error {
			if _, err := runChecked(ctx, Command{Name: supervisor, Args: []string{"status"}}, ""); err != nil {
				return err
			}
			return verifyPostgresIdentity(ctx)
		},
		ContainPostGrantFailure: func(ctx context.Context) error {
			_, _ = runChecked(ctx, Command{Name: supervisor, Args: []string{"stop-app"}}, "")
			if err := verifyPostgresIdentity(ctx); err != nil {
				return err
			}
			_, err := runChecked(ctx, contain, "")
			return err
		},
	}, nil
}

func sealedPostgresIdentity(stateDir string) (string, error) {
	if strings.TrimSpace(stateDir) == "" {
		return "", fmt.Errorf("sealed Test supervisor state directory is required")
	}
	read := func(path string) (string, error) {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > 256 {
			return "", fmt.Errorf("sealed PostgreSQL identity is unavailable or unsafe")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read sealed PostgreSQL identity: %w", err)
		}
		value := strings.TrimSpace(string(data))
		if value == "" || strings.ContainsAny(value, "\r\n|") {
			return "", fmt.Errorf("sealed PostgreSQL identity is invalid")
		}
		return value, nil
	}
	pid, err := read(filepath.Join(stateDir, "pids", "postgres.pid"))
	if err != nil {
		return "", err
	}
	start, err := read(filepath.Join(stateDir, "ownership", "postgres.start"))
	if err != nil {
		return "", err
	}
	return pid + "|" + start, nil
}

func mustReadActivationEvidence(path string) []byte {
	data, _ := os.ReadFile(path)
	return data
}
