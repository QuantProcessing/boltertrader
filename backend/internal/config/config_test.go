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
	if cfg.GatewayURL != "http://127.0.0.1:18082" || string(cfg.GatewayToken) != strings.Repeat("gateway-token-", 3) {
		t.Fatalf("Gateway adapter configuration not loaded")
	}
	if string(cfg.ManualPreviewSigningKey) != strings.Repeat("manual-preview-key-", 2) {
		t.Fatal("Manual preview signing key not loaded")
	}
}

func TestSecretStoreConfigurationIsExplicitAndModeScoped(t *testing.T) {
	setValidEnvironment(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SecretKeySource != "file" || cfg.SecretStoreNamespace != "boltertrader/test" || !filepath.IsAbs(cfg.SecretStoreDir) {
		t.Fatalf("secret config=%+v", cfg)
	}
	for name, mutate := range map[string]func(*Config){
		"relative store":       func(value *Config) { value.SecretStoreDir = "relative" },
		"cross mode namespace": func(value *Config) { value.SecretStoreNamespace = "boltertrader/live" },
		"unsupported version":  func(value *Config) { value.SecretMasterKeyVersion = "v2" },
		"mixed branches":       func(value *Config) { value.SecretKeychainService = "service" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := cfg
			mutate(&changed)
			if err := changed.Validate(); err == nil {
				t.Fatal("Validate succeeded")
			}
		})
	}
}

func TestLoadRejectsUnsafeManualPreviewSigningKeys(t *testing.T) {
	t.Run("inline", func(t *testing.T) {
		setValidEnvironment(t)
		t.Setenv("BT_MANUAL_PREVIEW_SIGNING_KEY", strings.Repeat("x", 32))
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "inline Manual preview signing keys are forbidden") {
			t.Fatalf("Load() error = %v", err)
		}
	})

	for name, configure := range map[string]func(*testing.T){
		"missing": func(t *testing.T) {
			t.Setenv(ManualPreviewSigningKeyFileEnv, "")
		},
		"short": func(t *testing.T) {
			setManualPreviewKeyFile(t, "test", "too-short", 0o600)
		},
		"weak permissions": func(t *testing.T) {
			setManualPreviewKeyFile(t, "test", strings.Repeat("x", 32), 0o644)
		},
		"cross mode": func(t *testing.T) {
			setManualPreviewKeyFile(t, "live", strings.Repeat("x", 32), 0o600)
		},
		"symlink": func(t *testing.T) {
			target := filepath.Join(t.TempDir(), "target")
			if err := os.WriteFile(target, []byte(strings.Repeat("x", 32)), 0o600); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(t.TempDir(), "test")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "manual-preview-signing.key")
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
			t.Setenv(ManualPreviewSigningKeyFileEnv, path)
		},
	} {
		t.Run(name, func(t *testing.T) {
			setValidEnvironment(t)
			configure(t)
			if _, err := Load(); err == nil {
				t.Fatal("Load() error = nil")
			}
		})
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

func TestGatewayTransportConfigurationIsModeScopedAndLoopbackOnlyForHTTP(t *testing.T) {
	setValidEnvironment(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Config){
		"plaintext non-loopback": func(value *Config) { value.GatewayURL = "http://192.0.2.10:18082" },
		"plaintext hostname":     func(value *Config) { value.GatewayURL = "http://localhost:18082" },
		"cross-mode token": func(value *Config) {
			value.GatewayTokenFile = filepath.Join(t.TempDir(), "live", "gateway.token")
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := cfg
			mutate(&changed)
			if err := changed.Validate(); err == nil {
				t.Fatal("Validate() unexpectedly succeeded")
			}
		})
	}
}

func TestLivePrivateWritesRequireExplicitLiveConfiguration(t *testing.T) {
	setValidEnvironment(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LivePrivateWritesEnabled {
		t.Fatal("Live private writes defaulted to enabled")
	}

	t.Setenv(LivePrivateWritesEnabledEnv, "true")
	if _, err := Load(); err == nil {
		t.Fatal("test mode accepted the Live private-write gate")
	}

	t.Setenv("BT_MODE", "live")
	t.Setenv(SecretStoreNamespaceEnv, "boltertrader/live")
	t.Setenv("BT_PLATFORM_DATABASE", "bolter_platform_live")
	t.Setenv("BT_PLATFORM_DATABASE_ROLE", "bolter_platform_live")
	gatewayDir := filepath.Join(t.TempDir(), "live")
	if err := os.MkdirAll(gatewayDir, 0o700); err != nil {
		t.Fatal(err)
	}
	gatewayPath := filepath.Join(gatewayDir, "gateway.token")
	if err := os.WriteFile(gatewayPath, []byte(strings.Repeat("gateway-token-", 3)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(GatewayTokenFileEnv, gatewayPath)
	setManualPreviewKeyFile(t, "live", strings.Repeat("manual-preview-key-", 2), 0o600)
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.LivePrivateWritesEnabled {
		t.Fatal("explicit Live private-write gate was not enabled")
	}
}

func TestFundingLiveAdmissionConfigurationIsExplicitAndModeScoped(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv(FundingLiveEnabledEnv, "not-a-bool")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), FundingLiveEnabledEnv) {
		t.Fatalf("invalid bool Load() error = %v", err)
	}

	setValidEnvironment(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.FundingLiveEnabled = true
	cfg.FundingLiveMaxNotional = "250"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "BT_MODE=live") {
		t.Fatalf("cross-mode Validate() error = %v", err)
	}
	cfg.Mode = "live"
	cfg.FundingLiveMaxNotional = ""
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), FundingLiveMaxNotionalEnv) {
		t.Fatalf("missing maximum Validate() error = %v", err)
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
	gatewayDir := filepath.Join(t.TempDir(), "test")
	if err := os.MkdirAll(gatewayDir, 0o700); err != nil {
		t.Fatal(err)
	}
	gatewayPath := filepath.Join(gatewayDir, "gateway.token")
	if err := os.WriteFile(gatewayPath, []byte(strings.Repeat("gateway-token-", 3)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(GatewayURLEnv, "http://127.0.0.1:18082")
	t.Setenv(GatewayTokenFileEnv, gatewayPath)
	setManualPreviewKeyFile(t, "test", strings.Repeat("manual-preview-key-", 2), 0o600)
	secretRoot := t.TempDir()
	secretKey := filepath.Join(secretRoot, "secret-store-master.key")
	if err := os.WriteFile(secretKey, []byte(strings.Repeat("01", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(SecretKeySourceEnv, "file")
	t.Setenv(SecretStoreDirEnv, filepath.Join(secretRoot, "secret-store"))
	t.Setenv(SecretStoreNamespaceEnv, "boltertrader/test")
	t.Setenv(SecretMasterKeyVersionEnv, "v1")
	t.Setenv(SecretMasterKeyFileEnv, secretKey)
}

func setManualPreviewKeyFile(t *testing.T, mode, value string, permission os.FileMode) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), mode)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "manual-preview-signing.key")
	if err := os.WriteFile(path, []byte(value), permission); err != nil {
		t.Fatal(err)
	}
	t.Setenv(ManualPreviewSigningKeyFileEnv, path)
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
	t.Setenv("BT_PLATFORM_DATABASE", "bolter_platform_test")
	t.Setenv("BT_PLATFORM_DATABASE_ROLE", "bolter_platform_test")
}

func clearEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"BT_MODE", "BT_BOOTSTRAP_USERNAME", "BT_BOOTSTRAP_PASSWORD_HASH",
		"BT_PLATFORM_WORK_TOKEN", "BT_FUNDING_WORKER_TOKEN", FundingWorkerTokenFileEnv,
		"BT_PLATFORM_BROWSER_LISTEN_ADDR", "BT_PLATFORM_WORK_LISTEN_ADDR",
		GatewayURLEnv, GatewayTokenFileEnv, "BT_PLATFORM_DATABASE", "BT_PLATFORM_DATABASE_ROLE",
		"BT_MANUAL_PREVIEW_SIGNING_KEY", ManualPreviewSigningKeyFileEnv,
		LivePrivateWritesEnabledEnv,
		FundingLiveEnabledEnv, FundingLiveMaxNotionalEnv,
		SecretKeySourceEnv, SecretStoreDirEnv, SecretStoreNamespaceEnv, SecretMasterKeyVersionEnv,
		SecretMasterKeyFileEnv, SecretKeychainServiceEnv, SecretKeychainAccountEnv,
	} {
		t.Setenv(key, "")
	}
}
