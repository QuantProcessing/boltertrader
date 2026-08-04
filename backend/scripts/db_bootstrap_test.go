package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDatabaseBootstrapFreezesFourIsolatedOwners(t *testing.T) {
	path := filepath.Join("db-bootstrap.sh")
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	script := string(payload)
	for _, name := range []string{
		"bolter_platform_test",
		"bolter_gateway_test",
		"bolter_platform_live",
		"bolter_gateway_live",
	} {
		for _, required := range []string{
			"CREATE ROLE " + name,
			"CREATE DATABASE " + name + " OWNER " + name,
			"REVOKE CONNECT ON DATABASE " + name + " FROM PUBLIC",
			"GRANT CONNECT ON DATABASE " + name + " TO " + name,
		} {
			if !strings.Contains(script, required) {
				t.Errorf("bootstrap missing %q", required)
			}
		}
	}
	for _, capability := range []string{"NOSUPERUSER", "NOCREATEDB", "NOCREATEROLE", "NOINHERIT", "NOREPLICATION"} {
		if !strings.Contains(script, capability) {
			t.Errorf("bootstrap roles missing %s", capability)
		}
	}
	if !strings.Contains(script, "verify_cross_access_denied") {
		t.Error("bootstrap does not execute negative cross-access verification")
	}
}

func TestDatabaseBootstrapScriptsPassShellSyntaxCheck(t *testing.T) {
	paths := []string{
		"db-bootstrap.sh",
		"secret-bootstrap.sh",
		filepath.Join("..", "..", "gateway", "scripts", "db-bootstrap.sh"),
		filepath.Join("..", "..", "gateway", "scripts", "secret-bootstrap.sh"),
	}
	for _, path := range paths {
		command := exec.Command("sh", "-n", path)
		if output, err := command.CombinedOutput(); err != nil {
			t.Errorf("sh -n %s: %v\n%s", path, err, output)
		}
	}
}

func TestSecretBootstrapKeepsMasterKeysOpaqueAndModesSeparate(t *testing.T) {
	payload, err := os.ReadFile("secret-bootstrap.sh")
	if err != nil {
		t.Fatalf("read secret-bootstrap.sh: %v", err)
	}
	script := string(payload)
	for _, required := range []string{
		"boltertrader/test",
		"boltertrader/live",
		"BT_SECRET_MASTER_KEY_FILE",
		"SECRET_STORE_KEY_MISSING",
		"SECRET_STORE_KEY_VERSION_UNSUPPORTED",
	} {
		if !strings.Contains(script, required) {
			t.Errorf("secret bootstrap missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"cat \"$BT_SECRET_MASTER_KEY_FILE\"",
		"echo \"$BT_SECRET_MASTER_KEY",
		"printf '%s' \"$BT_SECRET_MASTER_KEY",
	} {
		if strings.Contains(script, forbidden) {
			t.Errorf("secret bootstrap reads or emits master-key material via %q", forbidden)
		}
	}
}

func TestSecretBootstrapRejectsKeyReuseAndWritesOnlySafeMetadata(t *testing.T) {
	runtimeDir := t.TempDir()
	testKey := filepath.Join(t.TempDir(), "test.key")
	copiedTestKey := filepath.Join(t.TempDir(), "copied-test.key")
	liveKey := filepath.Join(t.TempDir(), "live.key")
	const testCanary = "synthetic-test-master-key-canary"
	const liveCanary = "synthetic-live-master-key-canary"
	if err := os.WriteFile(testKey, []byte(testCanary), 0o600); err != nil {
		t.Fatalf("write Test key fixture: %v", err)
	}
	if err := os.WriteFile(copiedTestKey, []byte(testCanary), 0o600); err != nil {
		t.Fatalf("write copied Test key fixture: %v", err)
	}
	if err := os.WriteFile(liveKey, []byte(liveCanary), 0o600); err != nil {
		t.Fatalf("write Live key fixture: %v", err)
	}

	run := func(mode, key, version string) (string, error) {
		command := exec.Command("sh", "secret-bootstrap.sh")
		command.Env = []string{
			"PATH=" + os.Getenv("PATH"),
			"FT_MODE=" + mode,
			"FT_LOCAL_RUNTIME_DIR=" + runtimeDir,
			"BT_SECRET_MASTER_KEY_FILE=" + key,
			"BT_SECRET_MASTER_KEY_VERSION=" + version,
		}
		output, err := command.CombinedOutput()
		return string(output), err
	}

	if output, err := run("test", testKey, "v1"); err != nil {
		t.Fatalf("bootstrap Test: %v\n%s", err, output)
	}
	if output, err := run("live", testKey, "v1"); err == nil || !strings.Contains(output, "SECRET_STORE_KEY_MISMATCH") {
		t.Fatalf("cross-mode key reuse = (%v, %q), want mismatch", err, output)
	}
	if output, err := run("live", copiedTestKey, "v1"); err == nil || !strings.Contains(output, "SECRET_STORE_KEY_MISMATCH") {
		t.Fatalf("copied cross-mode key reuse = (%v, %q), want mismatch", err, output)
	}
	if output, err := run("live", liveKey, "v1"); err != nil {
		t.Fatalf("bootstrap Live: %v\n%s", err, output)
	}
	if output, err := run("test", testKey, "v2"); err == nil || !strings.Contains(output, "SECRET_STORE_KEY_VERSION_UNSUPPORTED") {
		t.Fatalf("unsupported version = (%v, %q)", err, output)
	}
	if output, err := run("test", filepath.Join(t.TempDir(), "missing.key"), "v1"); err == nil || !strings.Contains(output, "SECRET_STORE_KEY_MISSING") {
		t.Fatalf("missing key = (%v, %q)", err, output)
	}

	for _, mode := range []string{"test", "live"} {
		metadata, err := os.ReadFile(filepath.Join(runtimeDir, "secret-store", mode, "bootstrap.meta"))
		if err != nil {
			t.Fatalf("read %s metadata: %v", mode, err)
		}
		if strings.Contains(string(metadata), testCanary) || strings.Contains(string(metadata), liveCanary) {
			t.Fatalf("%s metadata leaked key fixture: %s", mode, metadata)
		}
		if !strings.Contains(string(metadata), "namespace=boltertrader/"+mode) {
			t.Fatalf("%s metadata has wrong namespace: %s", mode, metadata)
		}
	}
}
