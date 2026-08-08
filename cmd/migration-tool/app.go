package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/QuantProcessing/boltertrader/migration"
)

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
	result, err := migration.Apply(*manifestPath, migration.Request{
		Operation: operation, ID: *id, Name: *name, ArtifactPath: *artifact,
		ProofPath: *proof, Verifier: *verifier, Reason: *reason,
		ActivationAt: *activationAt, CutoverAt: *cutoverAt,
	}, migration.ApplyOptions{})
	if err != nil {
		return err
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
