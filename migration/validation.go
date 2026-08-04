package migration

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type migrationManifest struct {
	SchemaVersion      int      `json:"schemaVersion"`
	MigrationID        string   `json:"migrationId"`
	SourceCommit       string   `json:"sourceCommit"`
	SourceTreeHash     string   `json:"sourceTreeHash"`
	SourceQuiesceAt    string   `json:"sourceQuiesceAt"`
	TargetCommit       string   `json:"targetCommit"`
	TargetTreeHash     string   `json:"targetTreeHash"`
	ImportCommit       *string  `json:"importCommit"`
	BackfillScripts    []string `json:"backfillScripts"`
	BackupID           *string  `json:"backupId"`
	BackupSHA256       *string  `json:"backupSha256"`
	RestoreVerifiedAt  *string  `json:"restoreVerifiedAt"`
	FinalDeltaAt       *string  `json:"finalDeltaAt"`
	FinalDeltaSHA256   *string  `json:"finalDeltaSha256"`
	OneWriterFenceAt   *string  `json:"oneWriterFenceAt"`
	ValidationAt       *string  `json:"validationAt"`
	TargetActivationAt *string  `json:"targetActivationAt"`
	CutoverAt          *string  `json:"cutoverAt"`
	Quiescence         struct {
		Status         string `json:"status"`
		WorktreeClean  bool   `json:"worktreeClean"`
		FrozenCommit   string `json:"frozenCommit"`
		FrozenTreeHash string `json:"frozenTreeHash"`
	} `json:"quiescence"`
	OneWriter struct {
		Owner                        string `json:"owner"`
		FenceRaised                  bool   `json:"fenceRaised"`
		TargetCanonicalWritesEnabled bool   `json:"targetCanonicalWritesEnabled"`
	} `json:"oneWriter"`
}

func ValidateBaseline(data []byte) error {
	manifest, err := decodeManifest(data)
	if err != nil {
		return err
	}
	return validateBaseline(manifest)
}

func ValidateActivation(data []byte) error {
	manifest, err := decodeManifest(data)
	if err != nil {
		return err
	}
	problems := collectBaselineProblems(manifest)
	problems = append(problems, missingActivationEvidence(manifest)...)
	problems = append(problems, validateActivationOrder(manifest)...)
	if len(problems) > 0 {
		return fmt.Errorf("activation rejected: %s", strings.Join(problems, "; "))
	}
	return nil
}

func ValidateRetirement(data []byte) error {
	manifest, err := decodeManifest(data)
	if err != nil {
		return err
	}
	problems := collectBaselineProblems(manifest)
	problems = append(problems, missingActivationEvidence(manifest)...)
	problems = append(problems, validateActivationOrder(manifest)...)
	for name, value := range map[string]*string{
		"targetActivationAt": manifest.TargetActivationAt,
		"cutoverAt":          manifest.CutoverAt,
	} {
		if blank(value) {
			problems = append(problems, "missing "+name)
		}
	}
	problems = append(problems, validateRetirementOrder(manifest)...)
	if len(problems) > 0 {
		return fmt.Errorf("retirement rejected: %s", strings.Join(problems, "; "))
	}
	return nil
}

func decodeManifest(data []byte) (migrationManifest, error) {
	var manifest migrationManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return migrationManifest{}, fmt.Errorf("decode migration manifest: %w", err)
	}
	return manifest, nil
}

func validateBaseline(manifest migrationManifest) error {
	if problems := collectBaselineProblems(manifest); len(problems) > 0 {
		return fmt.Errorf("baseline rejected: %s", strings.Join(problems, "; "))
	}
	return nil
}

func collectBaselineProblems(manifest migrationManifest) []string {
	var problems []string
	if manifest.SchemaVersion != 1 || manifest.MigrationID == "" {
		problems = append(problems, "manifest identity is not frozen")
	}
	if manifest.SourceCommit == "" || manifest.SourceTreeHash == "" || manifest.TargetCommit == "" || manifest.TargetTreeHash == "" {
		problems = append(problems, "source and target provenance is incomplete")
	}
	if manifest.Quiescence.Status != "quiesced" || !manifest.Quiescence.WorktreeClean || manifest.SourceQuiesceAt == "" || manifest.Quiescence.FrozenCommit != manifest.SourceCommit || manifest.Quiescence.FrozenTreeHash != manifest.SourceTreeHash {
		problems = append(problems, "quiescence evidence does not match the frozen source snapshot")
	}

	preCutover := manifest.OneWriter.Owner == "funding-trader" && !manifest.OneWriter.FenceRaised && !manifest.OneWriter.TargetCanonicalWritesEnabled
	postFence := manifest.OneWriter.Owner == "BolterTrader" && manifest.OneWriter.FenceRaised && manifest.OneWriter.TargetCanonicalWritesEnabled
	if !preCutover && !postFence {
		problems = append(problems, "oneWriter state is inconsistent or permits split-brain ownership")
	}
	return problems
}

func missingActivationEvidence(manifest migrationManifest) []string {
	var problems []string
	for name, value := range map[string]*string{
		"importCommit":      manifest.ImportCommit,
		"backupId":          manifest.BackupID,
		"backupSha256":      manifest.BackupSHA256,
		"restoreVerifiedAt": manifest.RestoreVerifiedAt,
		"finalDeltaAt":      manifest.FinalDeltaAt,
		"finalDeltaSha256":  manifest.FinalDeltaSHA256,
		"oneWriterFenceAt":  manifest.OneWriterFenceAt,
		"validationAt":      manifest.ValidationAt,
	} {
		if blank(value) {
			problems = append(problems, "missing "+name)
		}
	}
	if len(manifest.BackfillScripts) == 0 {
		problems = append(problems, "missing backfillScripts")
	}
	if manifest.OneWriter.Owner != "BolterTrader" || !manifest.OneWriter.FenceRaised || !manifest.OneWriter.TargetCanonicalWritesEnabled {
		problems = append(problems, "oneWriter fence has not transferred ownership to BolterTrader")
	}
	return problems
}

func validateActivationOrder(manifest migrationManifest) []string {
	return validateTimeOrder([]timeField{
		{"sourceQuiesceAt", &manifest.SourceQuiesceAt},
		{"restoreVerifiedAt", manifest.RestoreVerifiedAt},
		{"finalDeltaAt", manifest.FinalDeltaAt},
		{"oneWriterFenceAt", manifest.OneWriterFenceAt},
		{"validationAt", manifest.ValidationAt},
	})
}

func validateRetirementOrder(manifest migrationManifest) []string {
	return validateTimeOrder([]timeField{
		{"validationAt", manifest.ValidationAt},
		{"targetActivationAt", manifest.TargetActivationAt},
		{"cutoverAt", manifest.CutoverAt},
	})
}

type timeField struct {
	name  string
	value *string
}

func validateTimeOrder(fields []timeField) []string {
	var problems []string
	var previous time.Time
	var previousName string
	for _, field := range fields {
		if blank(field.value) {
			continue
		}
		parsed, err := time.Parse(time.RFC3339, *field.value)
		if err != nil {
			problems = append(problems, field.name+" is not RFC3339")
			continue
		}
		if !previous.IsZero() && parsed.Before(previous) {
			problems = append(problems, field.name+" precedes "+previousName)
		}
		previous = parsed
		previousName = field.name
	}
	return problems
}

func blank(value *string) bool {
	return value == nil || strings.TrimSpace(*value) == ""
}
