package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRealGateFailsClosedWithoutExplicitAttestation(t *testing.T) {
	output, err := runRealGate(t, []string{"FT_MODE=test"})
	if err == nil {
		t.Fatalf("test-real succeeded without attestation: %s", output)
	}
	if !strings.Contains(output, "BT_DEV_REAL_STACK_ACCEPTANCE=1") {
		t.Fatalf("test-real did not identify the missing attestation: %s", output)
	}
}

func TestRealGateRejectsVenueWritesWithoutLeakingValue(t *testing.T) {
	const secret = "secret-value-must-not-be-printed"
	output, err := runRealGate(t, []string{
		"FT_MODE=test",
		"BT_DEV_REAL_STACK_ACCEPTANCE=1",
		"BT_DEV_ENABLE_LIVE_PRIVATE_WRITES=0",
		"BT_LIVE_PRIVATE_WRITES_ENABLED=0",
		"BOLTER_ENABLE_BINANCE_DEMO_WRITES=" + secret,
	})
	if err == nil {
		t.Fatalf("test-real accepted a venue-write enable variable: %s", output)
	}
	if !strings.Contains(output, "BOLTER_ENABLE_BINANCE_DEMO_WRITES must be unset") {
		t.Fatalf("test-real did not identify the unsafe variable: %s", output)
	}
	if strings.Contains(output, secret) {
		t.Fatalf("test-real leaked a venue-write variable value: %s", output)
	}
}

func runRealGate(t *testing.T, environment []string) (string, error) {
	t.Helper()
	repo := repoRoot(t)
	command := exec.Command(filepath.Join(repo, "scripts", "test-real-platform"))
	command.Dir = repo
	command.Env = append([]string{"PATH=" + os.Getenv("PATH")}, environment...)
	output, err := command.CombinedOutput()
	return string(output), err
}
