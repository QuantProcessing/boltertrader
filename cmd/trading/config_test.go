package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeToken(t *testing.T, dir, name string, mode os.FileMode) string {
	t.Helper()
	modeDir := filepath.Join(dir, "test")
	if err := os.MkdirAll(modeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(modeDir, name)
	if err := os.WriteFile(path, []byte(strings.Repeat("t", 40)), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func validEnv(t *testing.T) map[string]string {
	t.Helper()
	dir := t.TempDir()
	return map[string]string{
		modeEnv:              "test",
		listenAddrEnv:        "127.0.0.1:18092",
		databaseEnv:          "bolter_trading_test",
		databaseRoleEnv:      "bolter_trading_test",
		gatewayURLEnv:        "http://127.0.0.1:18080",
		serviceTokenFileEnv:  writeToken(t, dir, "service.token", 0o600),
		gatewayTokenFileEnv:  writeToken(t, dir, "gateway.token", 0o600),
		previewSigningKeyEnv: writeToken(t, dir, "preview.key", 0o600),
	}
}

func applyEnv(t *testing.T, env map[string]string) {
	t.Helper()
	for _, name := range []string{modeEnv, listenAddrEnv, databaseEnv, databaseRoleEnv, databaseHostEnv, gatewayURLEnv, serviceTokenFileEnv, gatewayTokenFileEnv, previewSigningKeyEnv, livePrivateWritesEnv, inlinePreviewKeyEnv, inlineServiceTokenEnv, inlineGatewayTokenName} {
		t.Setenv(name, "")
		os.Unsetenv(name)
	}
	for name, value := range env {
		t.Setenv(name, value)
	}
}

func TestLoadAcceptsAValidEnvironment(t *testing.T) {
	applyEnv(t, validEnv(t))
	cfg, err := load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.ServiceToken) < 32 || len(cfg.GatewayToken) < 32 || len(cfg.ManualPreviewSigningKey) < 32 {
		t.Fatal("secrets were not loaded")
	}
	if cfg.DatabaseHost != "127.0.0.1:15432" {
		t.Fatalf("database host=%q", cfg.DatabaseHost)
	}
}

// Nothing in this process authenticates a browser, so a routable listener would
// publish the trading surface to anyone who could reach the host.
func TestLoadRequiresALoopbackListener(t *testing.T) {
	for _, address := range []string{"0.0.0.0:18092", "192.168.1.10:18092", ":18092", "127.0.0.1"} {
		env := validEnv(t)
		env[listenAddrEnv] = address
		applyEnv(t, env)
		if _, err := load(); err == nil || !strings.Contains(err.Error(), listenAddrEnv) {
			t.Fatalf("address %q accepted: %v", address, err)
		}
	}
}

// An environment variable is readable from a process listing and survives in
// shell history. Accepting one would make every other precaution here decorative.
func TestLoadRefusesInlineSecrets(t *testing.T) {
	for _, name := range []string{inlinePreviewKeyEnv, inlineServiceTokenEnv, inlineGatewayTokenName} {
		env := validEnv(t)
		env[name] = strings.Repeat("t", 40)
		applyEnv(t, env)
		if _, err := load(); err == nil || !strings.Contains(err.Error(), "inline secrets are forbidden") {
			t.Fatalf("%s accepted: %v", name, err)
		}
	}
}

func TestLoadRequiresProtectedSecretFiles(t *testing.T) {
	dir := t.TempDir()
	env := validEnv(t)
	env[serviceTokenFileEnv] = writeToken(t, dir, "world-readable.token", 0o644)
	applyEnv(t, env)
	if _, err := load(); err == nil || !strings.Contains(err.Error(), "0600") {
		t.Fatalf("world-readable token accepted: %v", err)
	}

	// A secret file outside a directory named for the mode is how a live process
	// ends up quietly running on test credentials.
	unscoped := filepath.Join(t.TempDir(), "service.token")
	if err := os.WriteFile(unscoped, []byte(strings.Repeat("t", 40)), 0o600); err != nil {
		t.Fatal(err)
	}
	env = validEnv(t)
	env[serviceTokenFileEnv] = unscoped
	applyEnv(t, env)
	if _, err := load(); err == nil || !strings.Contains(err.Error(), "scoped to") {
		t.Fatalf("unscoped token accepted: %v", err)
	}
}

// The database is named for the mode it holds, and the name is the only check
// available before the first write.
func TestLoadRequiresAModeScopedDatabase(t *testing.T) {
	for _, name := range []string{"bolter_trading_live", "bolter_platform_test", ""} {
		env := validEnv(t)
		env[databaseEnv] = name
		applyEnv(t, env)
		if _, err := load(); err == nil || !strings.Contains(err.Error(), "bolter_trading_test") {
			t.Fatalf("database %q accepted: %v", name, err)
		}
	}
}

func TestLoadRequiresLiveModeForLiveWrites(t *testing.T) {
	env := validEnv(t)
	env[livePrivateWritesEnv] = "true"
	applyEnv(t, env)
	if _, err := load(); err == nil || !strings.Contains(err.Error(), livePrivateWritesEnv) {
		t.Fatalf("live writes accepted under test mode: %v", err)
	}

	env = validEnv(t)
	env[livePrivateWritesEnv] = "yes"
	applyEnv(t, env)
	if _, err := load(); err == nil || !strings.Contains(err.Error(), "must be true or false") {
		t.Fatalf("ambiguous flag accepted: %v", err)
	}
}

func TestLoadRejectsUnsafeGatewayOrigins(t *testing.T) {
	for _, origin := range []string{"http://gateway.internal:18080", "ftp://127.0.0.1", "http://user:pass@127.0.0.1:18080", "http://127.0.0.1:18080/v1", "http://127.0.0.1:18080?x=1", ""} {
		env := validEnv(t)
		env[gatewayURLEnv] = origin
		applyEnv(t, env)
		if _, err := load(); err == nil || !strings.Contains(err.Error(), gatewayURLEnv) {
			t.Fatalf("origin %q accepted: %v", origin, err)
		}
	}
}
