package main

import (
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
	if command == "activate" || command == "retire" {
		return fmt.Errorf("unsupported command %q: this tool prepares but does not execute activation or retirement", command)
	}
	if command == "status" {
		return runStatus(args[1:], stdout, stderr)
	}
	operation, ok := map[string]migration.Operation{
		"record-import": migration.RecordImport,
		"backup":        migration.Backup, "restore": migration.Restore, "final-delta": migration.FinalDelta,
		"fence": migration.Fence, "backfill": migration.Backfill, "validate": migration.Validate,
		"prepare-activation": migration.PrepareActivation, "record-activation-evidence": migration.RecordActivation,
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
	name := flags.String("name", "", "backfill step name")
	artifact := flags.String("artifact", "", "backup or delta artifact path")
	proof := flags.String("proof", "", "verification proof artifact path")
	evidenceDir := flags.String("evidence-dir", "", "generated evidence directory")
	backfillScript := flags.String("backfill-script", "", "repository-owned backfill SQL path")
	sourceService := flags.String("source-service", "", "non-secret libpq source service name")
	targetService := flags.String("target-service", "", "non-secret libpq target service name")
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
	executable := operation == migration.Backup || operation == migration.Restore || operation == migration.FinalDelta || operation == migration.Fence || operation == migration.Backfill || operation == migration.Validate || operation == migration.Rollback
	if executable {
		if strings.TrimSpace(*proof) != "" {
			return fmt.Errorf("--proof is not accepted for executable operations; evidence is generated after command success")
		}
		executed, executeErr := executeMigration(context.Background(), migration.ExecutionRequest{
			Operation: operation, ManifestPath: *manifestPath, EvidenceDir: *evidenceDir,
			ArtifactPath: *artifact, BackfillPath: *backfillScript, ID: *id,
			Verifier: *verifier, Reason: *reason, SourceService: *sourceService, TargetService: *targetService,
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
