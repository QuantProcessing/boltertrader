package migration

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestBaselineAcceptsFrozenPreCutoverManifest(t *testing.T) {
	data := readManifest(t)
	if err := ValidateBaseline(data); err != nil {
		t.Fatalf("validate baseline: %v", err)
	}
}

func TestManifestRejectsUnknownFields(t *testing.T) {
	data := strings.TrimSpace(string(readManifest(t)))
	data = strings.TrimSuffix(data, "}") + `,"unexpected":"ambiguous"}`
	if err := ValidateBaseline([]byte(data)); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown-field error = %v", err)
	}
}

func TestManifestRejectsDuplicateFields(t *testing.T) {
	data := strings.Replace(string(readManifest(t)), `"schemaVersion": 1,`, `"schemaVersion": 1, "schemaVersion": 1,`, 1)
	if err := ValidateBaseline([]byte(data)); err == nil || !strings.Contains(err.Error(), "duplicate field") {
		t.Fatalf("duplicate-field error = %v", err)
	}
}

func TestTimeOrderRequiresStrictlyIncreasingEvidence(t *testing.T) {
	value := time.Date(2026, 8, 9, 1, 0, 0, 0, time.UTC).Format(time.RFC3339)
	problems := validateTimeOrder([]timeField{{"restore", &value}, {"delta", &value}})
	if len(problems) != 1 || !strings.Contains(problems[0], "strictly after") {
		t.Fatalf("equal timestamp problems = %v", problems)
	}
}

func TestActivationRejectsMissingOrderedProofs(t *testing.T) {
	err := ValidateActivation(readManifest(t))
	if err == nil {
		t.Fatal("activation unexpectedly accepted the pre-cutover baseline")
	}
	for _, field := range []string{
		"restoreVerifiedAt",
		"finalDeltaAt",
		"finalDeltaSha256",
		"oneWriterFenceAt",
		"backfillScripts",
		"validationAt",
	} {
		if !strings.Contains(err.Error(), field) {
			t.Errorf("activation error %q does not identify missing %s", err, field)
		}
	}
}

func TestRetirementRejectsMissingActivationAndCutoverProofs(t *testing.T) {
	err := ValidateRetirement(readManifest(t))
	if err == nil {
		t.Fatal("retirement unexpectedly accepted the pre-cutover baseline")
	}
	for _, field := range []string{"targetActivationAt", "cutoverAt"} {
		if !strings.Contains(err.Error(), field) {
			t.Errorf("retirement error %q does not identify missing %s", err, field)
		}
	}
}

func TestBaselineRejectsSplitBrainWriterState(t *testing.T) {
	data := strings.ReplaceAll(
		string(readManifest(t)),
		`"targetCanonicalWritesEnabled": false`,
		`"targetCanonicalWritesEnabled": true`,
	)
	if err := ValidateBaseline([]byte(data)); err == nil || !strings.Contains(err.Error(), "oneWriter") {
		t.Fatalf("split-brain baseline error = %v, want oneWriter rejection", err)
	}
}

func TestBaselineRejectsIncoherentQuiescenceSnapshot(t *testing.T) {
	data := strings.ReplaceAll(
		string(readManifest(t)),
		`"frozenTreeHash": "99821acf574c87741b08ae3dc3a913f12b2835d7"`,
		`"frozenTreeHash": "0000000000000000000000000000000000000000"`,
	)
	if err := ValidateBaseline([]byte(data)); err == nil || !strings.Contains(err.Error(), "quiescence") {
		t.Fatalf("incoherent quiescence error = %v, want quiescence rejection", err)
	}
}

func readManifest(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("manifest.json")
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	return data
}
