package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestLoadRequiresProtectedCredentialsAndKeepsOnlyTokenDigest(t *testing.T) {
	setValidEnvironment(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.BrowserListenAddr != "127.0.0.1:18091" || cfg.WorkListenAddr != "127.0.0.1:18092" {
		t.Fatalf("unexpected listen addrs: %#v", cfg)
	}
	if allZero(cfg.FundingWorkerTokenDigest[:]) {
		t.Fatal("worker token digest is zero")
	}
}

func TestLoadRejectsCredentialDefaultsAndUnsafeTokenFiles(t *testing.T) {
	t.Run("missing credentials", func(t *testing.T) {
		clearEnvironment(t)
		if _, err := Load(); err == nil {
			t.Fatal("Load() error = nil")
		}
	})
	t.Run("inline token", func(t *testing.T) {
		setValidEnvironment(t)
		t.Setenv("BT_PLATFORM_WORK_TOKEN", strings.Repeat("x", 32))
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "inline worker tokens are forbidden") {
			t.Fatalf("Load() error = %v", err)
		}
	})
	for name, test := range map[string]struct {
		token      string
		permission os.FileMode
	}{
		"short":      {token: "too-short", permission: 0o600},
		"permission": {token: strings.Repeat("x", 32), permission: 0o644},
	} {
		t.Run(name, func(t *testing.T) {
			clearEnvironment(t)
			setBaseEnvironment(t)
			path := filepath.Join(t.TempDir(), "worker.token")
			if err := os.WriteFile(path, []byte(test.token), test.permission); err != nil {
				t.Fatal(err)
			}
			t.Setenv(FundingWorkerTokenFileEnv, path)
			if _, err := Load(); err == nil {
				t.Fatal("Load() error = nil")
			}
		})
	}
}

func setValidEnvironment(t *testing.T) {
	t.Helper()
	clearEnvironment(t)
	setBaseEnvironment(t)
	path := filepath.Join(t.TempDir(), "worker.token")
	if err := os.WriteFile(path, []byte(strings.Repeat("token-value-", 4)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(FundingWorkerTokenFileEnv, path)
}

func setBaseEnvironment(t *testing.T) {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("correct horse battery staple"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("BT_MODE", "test")
	t.Setenv("BT_BOOTSTRAP_USERNAME", "owner")
	t.Setenv("BT_BOOTSTRAP_PASSWORD_HASH", string(hash))
}

func clearEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"BT_MODE", "BT_BOOTSTRAP_USERNAME", "BT_BOOTSTRAP_PASSWORD_HASH",
		"BT_PLATFORM_WORK_TOKEN", "BT_FUNDING_WORKER_TOKEN", FundingWorkerTokenFileEnv,
		"BT_PLATFORM_BROWSER_LISTEN_ADDR", "BT_PLATFORM_WORK_LISTEN_ADDR",
	} {
		t.Setenv(key, "")
	}
}
