package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/QuantProcessing/boltertrader/migration"
)

var executeMigration = migration.Execute

type output struct {
	Phase              migration.Phase `json:"phase"`
	Replayed           bool            `json:"replayed,omitempty"`
	ActivationPrepared bool            `json:"activationPrepared"`
	RetirementPrepared bool            `json:"retirementPrepared"`
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("migration command is required")
	}
	command := args[0]
	if command == "retire" {
		return fmt.Errorf("unsupported command %q: this tool prepares but does not execute activation or retirement", command)
	}
	if command == "status" {
		return runStatus(args[1:], stdout, stderr)
	}
	if command == "verify-evidence" {
		return runVerifyEvidence(args[1:], stdout, stderr)
	}
	if command == "validation-snapshot" {
		return runValidationSnapshot(args[1:], stdout, stderr)
	}
	if command == "validation-compare" {
		return runValidationCompare(args[1:], stdout, stderr)
	}
	operation, ok := map[string]migration.Operation{
		"record-import": migration.RecordImport,
		"backup":        migration.Backup, "restore": migration.Restore, "final-delta": migration.FinalDelta,
		"fence": migration.Fence, "backfill": migration.Backfill, "validate": migration.Validate,
		"activate":           migration.Activate,
		"prepare-activation": migration.PrepareActivation,
		"prepare-retirement": migration.PrepareRetirement,
		"rollback":           migration.Rollback,
	}[command]
	if !ok {
		return fmt.Errorf("unsupported command %q", command)
	}

	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	manifestPath := flags.String("manifest", "", "migration manifest path")
	id := flags.String("id", "", "non-secret backup identifier")
	backupID := flags.String("backup-id", "", "prior backup identifier required for activation replay")
	name := flags.String("name", "", "backfill step name")
	artifact := flags.String("artifact", "", "backup or delta artifact path")
	backupArtifact := flags.String("backup-artifact", "", "prior backup artifact path required for activation replay")
	finalDeltaArtifact := flags.String("final-delta-artifact", "", "prior final delta artifact path required for activation replay")
	proof := flags.String("proof", "", "verification proof artifact path")
	evidenceDir := flags.String("evidence-dir", "", "generated evidence directory")
	backfillMapping := flags.String("backfill-mapping", "", "repository-owned route mapping artifact")
	sourceService := flags.String("source-service", "", "non-secret libpq source service name")
	targetService := flags.String("target-service", "", "non-secret libpq target service name")
	gatewayService := flags.String("gateway-service", "", "non-secret libpq Gateway service name")
	maintenanceService := flags.String("maintenance-service", "", "non-secret libpq target maintenance service name")
	sourceDatabase := flags.String("source-database", "", "non-secret source database name")
	targetDatabase := flags.String("target-database", "", "non-secret target database name")
	gatewayDatabase := flags.String("gateway-database", "", "non-secret Gateway database name")
	maintenanceDatabase := flags.String("maintenance-database", "", "non-secret maintenance database name")
	sourceRole := flags.String("source-role", "", "expected source PostgreSQL connection role")
	targetRole := flags.String("target-role", "", "expected target PostgreSQL connection role")
	gatewayRole := flags.String("gateway-role", "", "expected Gateway PostgreSQL connection role")
	targetRuntimeRole := flags.String("target-runtime-role", "", "target Platform runtime PostgreSQL role activated after migration")
	gatewayRuntimeRole := flags.String("gateway-runtime-role", "", "Gateway runtime PostgreSQL role activated after migration")
	maintenanceRole := flags.String("maintenance-role", "", "expected maintenance PostgreSQL connection role")
	sourceWriterRole := flags.String("source-writer-role", "", "non-secret source canonical writer role")
	targetWriterRole := flags.String("target-writer-role", "", "non-secret target canonical writer role")
	gatewayWriterRole := flags.String("gateway-writer-role", "", "non-secret Gateway canonical writer role")
	journalPath := flags.String("journal", "", "activation progress journal path")
	stateDir := flags.String("state-dir", "", "sealed Test supervisor state directory")
	verifier := flags.String("verifier", "", "non-secret verifier identity")
	reason := flags.String("reason", "", "non-secret rollback reason")
	activationAt := flags.String("activation-at", "", "externally observed activation timestamp")
	cutoverAt := flags.String("cutover-at", "", "externally observed cutover timestamp")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*manifestPath) == "" {
		return fmt.Errorf("--manifest is required and positional arguments are not accepted")
	}
	var result migration.Result
	executable := operation == migration.RecordImport || operation == migration.Backup || operation == migration.Restore || operation == migration.FinalDelta || operation == migration.Fence || operation == migration.Backfill || operation == migration.Validate || operation == migration.Activate || operation == migration.Rollback
	if executable {
		if strings.TrimSpace(*proof) != "" {
			return fmt.Errorf("--proof is not accepted for executable operations; evidence is generated after command success")
		}
		if operation == migration.Activate && (strings.TrimSpace(*activationAt) != "" || strings.TrimSpace(*cutoverAt) != "") {
			return fmt.Errorf("--activation-at and --cutover-at are not accepted for activation; evidence is generated after command success")
		}
		if operation == migration.Activate {
			for _, required := range []struct {
				name  string
				value string
			}{
				{name: "--backup-id", value: *backupID},
				{name: "--backup-artifact", value: *backupArtifact},
				{name: "--final-delta-artifact", value: *finalDeltaArtifact},
				{name: "--backfill-mapping", value: *backfillMapping},
				{name: "--verifier", value: *verifier},
			} {
				if strings.TrimSpace(required.value) == "" {
					return fmt.Errorf("%s is required for activation", required.name)
				}
			}
		}
		executed, executeErr := executeMigration(context.Background(), migration.ExecutionRequest{
			Operation: operation, ManifestPath: *manifestPath, EvidenceDir: *evidenceDir,
			ArtifactPath: *artifact, BackfillPath: *backfillMapping, ID: *id,
			BackupID: *backupID, BackupArtifactPath: *backupArtifact, FinalDeltaArtifactPath: *finalDeltaArtifact,
			Verifier: *verifier, Reason: *reason, SourceService: *sourceService, TargetService: *targetService, GatewayService: *gatewayService, MaintenanceService: *maintenanceService,
			SourceDatabase: *sourceDatabase, TargetDatabase: *targetDatabase, GatewayDatabase: *gatewayDatabase,
			MaintenanceDatabase: *maintenanceDatabase,
			SourceRole:          *sourceRole,
			TargetRole:          *targetRole,
			GatewayRole:         *gatewayRole,
			TargetRuntimeRole:   *targetRuntimeRole,
			GatewayRuntimeRole:  *gatewayRuntimeRole,
			MaintenanceRole:     *maintenanceRole,
			SourceWriterRole:    *sourceWriterRole, TargetWriterRole: *targetWriterRole,
			GatewayWriterRole: *gatewayWriterRole, JournalPath: *journalPath, StateDir: *stateDir,
		}, migration.ExecuteOptions{})
		if executeErr != nil {
			return executeErr
		}
		result = executed.Result
	} else {
		applied, applyErr := migration.Apply(*manifestPath, migration.Request{
			Operation: operation, ID: *id, Name: *name, ArtifactPath: *artifact,
			ProofPath: *proof, Verifier: *verifier, Reason: *reason,
			ActivationAt: *activationAt, CutoverAt: *cutoverAt,
		}, migration.ApplyOptions{})
		if applyErr != nil {
			return applyErr
		}
		result = applied
	}
	return json.NewEncoder(stdout).Encode(output{
		Phase: result.Phase, Replayed: result.Replayed,
		ActivationPrepared: result.Phase == migration.PhaseActivationReady,
		RetirementPrepared: result.Phase == migration.PhaseRetirementReady,
	})
}

func runValidationSnapshot(args []string, stdout, stderr io.Writer) error {
	if os.Getenv("FT_MODE") != "test" {
		return fmt.Errorf("validation-snapshot requires FT_MODE=test")
	}
	flags := flag.NewFlagSet("validation-snapshot", flag.ContinueOnError)
	flags.SetOutput(stderr)
	platformService := flags.String("platform-service", "", "Platform libpq service")
	platformDatabase := flags.String("platform-database", "", "Platform Test database")
	gatewayService := flags.String("gateway-service", "", "Gateway libpq service")
	gatewayDatabase := flags.String("gateway-database", "", "Gateway Test database")
	outputPath := flags.String("output", "", "private snapshot artifact")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *platformService == "" || *platformDatabase == "" || *gatewayService == "" || *gatewayDatabase == "" || *outputPath == "" {
		return fmt.Errorf("validation-snapshot requires Platform/Gateway service, database, and --output")
	}
	for label, value := range map[string]string{"Platform database": *platformDatabase, "Gateway database": *gatewayDatabase} {
		normalized := strings.ToLower(strings.NewReplacer("-", "_", ".", "_", ":", "_").Replace(value))
		if strings.Contains("_"+normalized+"_", "_live_") || !strings.Contains("_"+normalized+"_", "_test_") {
			return fmt.Errorf("%s must be an exact Test identity", label)
		}
	}
	snapshot, err := migration.CapturePostgresValidationSnapshot(context.Background(), migration.PostgresValidationSnapshotRequest{PlatformService: *platformService, PlatformDatabase: *platformDatabase, GatewayService: *gatewayService, GatewayDatabase: *gatewayDatabase})
	if err != nil {
		return err
	}
	if err := writePrivateJSON(*outputPath, snapshot); err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(snapshot)
}

func runValidationCompare(args []string, stdout, stderr io.Writer) error {
	if os.Getenv("FT_MODE") != "test" {
		return fmt.Errorf("validation-compare requires FT_MODE=test")
	}
	flags := flag.NewFlagSet("validation-compare", flag.ContinueOnError)
	flags.SetOutput(stderr)
	beforePath := flags.String("before", "", "pre-browser snapshot")
	afterPath := flags.String("after", "", "post-browser snapshot")
	allowedSessionID := flags.String("allowed-session-id", "", "single validating browser session identity")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *beforePath == "" || *afterPath == "" || *allowedSessionID == "" {
		return fmt.Errorf("validation-compare requires --before, --after, and --allowed-session-id")
	}
	var before, after migration.ValidationSnapshot
	if err := readPrivateJSON(*beforePath, &before); err != nil {
		return err
	}
	if err := readPrivateJSON(*afterPath, &after); err != nil {
		return err
	}
	comparison, err := migration.CompareValidationSnapshots(before, after, *allowedSessionID)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(stdout).Encode(comparison); err != nil {
		return err
	}
	if !comparison.CanonicalUnchanged || !comparison.GatewayHeadsUnchanged || !comparison.MigrationUnchanged || !comparison.SessionLifecycleAllowed {
		return fmt.Errorf("validation snapshot drift detected")
	}
	return nil
}

func writePrivateJSON(path string, value any) error {
	if info, err := os.Lstat(path); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		return fmt.Errorf("snapshot output is unsafe")
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func readPrivateJSON(path string, value any) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("snapshot artifact is unavailable or unsafe")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := rejectPrivateJSONDuplicateKeys(data); err != nil {
		return fmt.Errorf("decode snapshot artifact: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("decode snapshot artifact: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("decode snapshot artifact: must contain one JSON value")
	}
	return nil
}

func rejectPrivateJSONDuplicateKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := scanPrivateJSONValue(decoder); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("must contain one JSON value")
	}
	return nil
}

func scanPrivateJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("invalid JSON")
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		keys := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return fmt.Errorf("invalid JSON")
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("invalid JSON")
			}
			if _, exists := keys[key]; exists {
				return fmt.Errorf("contains duplicate key")
			}
			keys[key] = struct{}{}
			if err := scanPrivateJSONValue(decoder); err != nil {
				return err
			}
		}
		if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
			return fmt.Errorf("invalid JSON")
		}
	case '[':
		for decoder.More() {
			if err := scanPrivateJSONValue(decoder); err != nil {
				return err
			}
		}
		if token, err = decoder.Token(); err != nil || token != json.Delim(']') {
			return fmt.Errorf("invalid JSON")
		}
	default:
		return fmt.Errorf("invalid JSON")
	}
	return nil
}

func runVerifyEvidence(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("verify-evidence", flag.ContinueOnError)
	flags.SetOutput(stderr)
	manifestPath := flags.String("manifest", "", "migration manifest path")
	evidenceDir := flags.String("evidence-dir", "", "generated evidence directory")
	importCommit := flags.String("import-commit", "", "recorded import commit")
	activationProof := flags.String("activation-proof", "", "external activation proof path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*manifestPath) == "" || strings.TrimSpace(*evidenceDir) == "" || strings.TrimSpace(*importCommit) == "" {
		return fmt.Errorf("--manifest, --evidence-dir, and --import-commit are required and positional arguments are not accepted")
	}
	if err := migration.VerifyEvidence(migration.EvidenceVerificationRequest{
		ManifestPath: *manifestPath, EvidenceDir: *evidenceDir, ImportCommit: *importCommit,
		ActivationProofPath: *activationProof,
	}); err != nil {
		return err
	}
	return runStatus([]string{"--manifest", *manifestPath}, stdout, stderr)
}

func runStatus(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	flags.SetOutput(stderr)
	manifestPath := flags.String("manifest", "", "migration manifest path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*manifestPath) == "" {
		return fmt.Errorf("--manifest is required and positional arguments are not accepted")
	}
	data, err := os.ReadFile(*manifestPath)
	if err != nil {
		return fmt.Errorf("read migration manifest: %w", err)
	}
	manifest, err := migration.DecodeManifest(data)
	if err != nil {
		return err
	}
	if err := migration.ValidateWorkflow(data); err != nil {
		return err
	}
	phase := manifest.Workflow.Phase
	if phase == "" {
		phase = migration.PhaseBaseline
	}
	return json.NewEncoder(stdout).Encode(output{
		Phase: phase, ActivationPrepared: phase == migration.PhaseActivationReady,
		RetirementPrepared: phase == migration.PhaseRetirementReady,
	})
}
