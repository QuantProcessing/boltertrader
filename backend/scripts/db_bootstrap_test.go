package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDatabaseBootstrapFreezesSeparatedTestAuthority(t *testing.T) {
	payload, err := os.ReadFile("db-bootstrap.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(payload)
	for _, required := range []string{
		"bolter_platform_test_admin LOGIN NOSUPERUSER CREATEDB CREATEROLE NOINHERIT",
		"bolter_funding_test_migrator",
		"bolter_platform_test_migrator",
		"bolter_gateway_test_migrator",
		"CREATE DATABASE bolter_funding_test OWNER bolter_funding_test_migrator",
		"CREATE DATABASE bolter_platform_test OWNER bolter_platform_test_migrator",
		"CREATE DATABASE bolter_gateway_test OWNER bolter_gateway_test_migrator",
		"CREATE DATABASE bolter_maintenance_test OWNER bolter_platform_test_admin",
		"ALTER DATABASE bolter_platform_test OWNER TO bolter_platform_test_migrator",
		"ALTER DATABASE bolter_gateway_test OWNER TO bolter_gateway_test_migrator",
		"REVOKE ALL ON ALL FUNCTIONS IN SCHEMA public FROM PUBLIC",
		"ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC",
		"GRANT pg_signal_backend TO bolter_platform_test_admin",
		"SET ROLE bolter_platform_test_admin",
		"SELECT CASE WHEN current_user = 'bolter_platform_test_admin'",
	} {
		if !strings.Contains(script, required) {
			t.Errorf("bootstrap missing %q", required)
		}
	}
	for _, role := range []string{"bolter_funding_test", "bolter_platform_test", "bolter_gateway_test"} {
		if !strings.Contains(script, "'"+role+"'") {
			t.Errorf("bootstrap missing runtime %s", role)
		}
	}
	for _, writer := range []string{"bolter_funding_test_writer", "bolter_platform_test_writer", "bolter_gateway_test_writer"} {
		if !strings.Contains(script, writer) {
			t.Errorf("bootstrap missing writer %s", writer)
		}
	}
}

func TestDatabaseBootstrapKeepsLiveRolesUnprivilegedAndInherited(t *testing.T) {
	payload, err := os.ReadFile("db-bootstrap.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(payload)
	for _, role := range []string{"bolter_platform_live", "bolter_gateway_live"} {
		if !strings.Contains(script, "CREATE DATABASE "+role+" OWNER "+role) {
			t.Errorf("bootstrap changed Live ownership for %s", role)
		}
	}
	if strings.Contains(script, "GRANT bolter_platform_live_writer") || strings.Contains(script, "GRANT bolter_gateway_live_writer") {
		t.Fatal("bootstrap grants a Live writer membership")
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
