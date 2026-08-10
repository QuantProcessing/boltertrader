package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/QuantProcessing/boltertrader/backend/internal/products/funding"
)

func TestWriteValidationReadinessIsModeScopedAndPrivate(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "state", "test", "funding-worker.state")
	config := funding.Config{StateFile: stateFile, WorkerID: "worker-a", InstanceID: "instance-a"}
	if err := writeValidationReadiness(config); err != nil {
		t.Fatal(err)
	}
	path := stateFile + ".migration-validation.ready"
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
	var payload map[string]string
	contents, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(contents, &payload) != nil {
		t.Fatalf("read sentinel: %v", err)
	}
	if payload["workerId"] != "worker-a" || payload["instanceId"] != "instance-a" || payload["profile"] != "migration-validation" || payload["observedAt"] == "" {
		t.Fatalf("payload=%+v", payload)
	}
}
