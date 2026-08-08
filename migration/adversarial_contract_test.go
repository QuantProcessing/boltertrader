package migration

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestActivationRejectsEvidenceWithoutStrictTimeProgression(t *testing.T) {
	manifest := activationReadyManifest(t)
	manifest["restoreVerifiedAt"] = manifest["sourceQuiesceAt"]

	err := ValidateActivation(marshalManifest(t, manifest))
	if err == nil || !strings.Contains(err.Error(), "restoreVerifiedAt") {
		t.Fatalf("ValidateActivation() error = %v, want strict ordering rejection", err)
	}
}

func TestManifestValidationRejectsAmbiguousInput(t *testing.T) {
	baseline := readManifest(t)

	t.Run("unknown field", func(t *testing.T) {
		data := append([]byte(nil), baseline[:len(baseline)-2]...)
		data = append(data, []byte(",\n  \"unexpectedProof\": \"accepted\"\n}\n")...)
		if err := ValidateBaseline(data); err == nil {
			t.Fatal("ValidateBaseline() accepted an unknown manifest field")
		}
	})

	t.Run("duplicate field", func(t *testing.T) {
		data := strings.Replace(
			string(baseline),
			`"schemaVersion": 1`,
			`"schemaVersion": 1, "schemaVersion": 1`,
			1,
		)
		if err := ValidateBaseline([]byte(data)); err == nil {
			t.Fatal("ValidateBaseline() accepted a duplicate manifest field")
		}
	})
}

func activationReadyManifest(t *testing.T) map[string]any {
	t.Helper()
	var manifest map[string]any
	if err := json.Unmarshal(readManifest(t), &manifest); err != nil {
		t.Fatalf("decode manifest fixture: %v", err)
	}
	manifest["importCommit"] = "0123456789abcdef0123456789abcdef01234567"
	manifest["backupId"] = "backup-001"
	manifest["backupSha256"] = strings.Repeat("a", 64)
	manifest["restoreVerifiedAt"] = "2026-08-04T16:07:00Z"
	manifest["finalDeltaAt"] = "2026-08-04T16:08:00Z"
	manifest["finalDeltaSha256"] = strings.Repeat("b", 64)
	manifest["oneWriterFenceAt"] = "2026-08-04T16:09:00Z"
	manifest["validationAt"] = "2026-08-04T16:10:00Z"
	manifest["backfillScripts"] = []string{"001_backfill.sql"}
	manifest["oneWriter"] = map[string]any{
		"owner":                        "BolterTrader",
		"fenceRaised":                  true,
		"targetCanonicalWritesEnabled": true,
	}
	return manifest
}

func marshalManifest(t *testing.T, manifest map[string]any) []byte {
	t.Helper()
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest fixture: %v", err)
	}
	return data
}
