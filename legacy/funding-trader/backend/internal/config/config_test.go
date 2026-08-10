package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConfigUsesOnlyTestAndLiveProductModes(t *testing.T) {
	for _, test := range []struct {
		raw  string
		want Mode
	}{
		{raw: "test", want: Test},
		{raw: "live", want: Live},
	} {
		t.Run(test.raw, func(t *testing.T) {
			setValidConfigMode(t)
			t.Setenv("FT_MODE", test.raw)
			if test.want == Live {
				t.Setenv("FT_COOKIE_SECURE", "true")
			}

			cfg, err := LoadFromEnv()
			if err != nil {
				t.Fatalf("LoadFromEnv: %v", err)
			}
			if cfg.Mode != test.want {
				t.Fatalf("mode=%q, want %q", cfg.Mode, test.want)
			}
		})
	}

	for _, legacy := range []string{"demo", "testnet", "mainnet"} {
		t.Run("reject "+legacy, func(t *testing.T) {
			setValidConfigMode(t)
			t.Setenv("FT_MODE", legacy)
			if _, err := LoadFromEnv(); err == nil || !strings.Contains(err.Error(), "invalid mode") {
				t.Fatalf("legacy product mode %q error=%v", legacy, err)
			}
		})
	}

	t.Run("legacy environment variable is not a product selector", func(t *testing.T) {
		setValidConfigMode(t)
		t.Setenv("FT_MODE", "")
		t.Setenv("FT_ENVIRONMENT", "mainnet")

		cfg, err := LoadFromEnv()
		if err != nil {
			t.Fatalf("LoadFromEnv: %v", err)
		}
		if cfg.Mode != Test {
			t.Fatalf("legacy FT_ENVIRONMENT changed product mode to %q", cfg.Mode)
		}
	})
}

func TestConfigValidationDefaultDenyAndLiveCookie(t *testing.T) {
	t.Setenv("FT_MODE", "live")
	t.Setenv("FT_HTTP_ADDR", "127.0.0.1:0")
	t.Setenv("FT_DATABASE_URL", "postgres://user:secret@example/db")
	t.Setenv("FT_GATEWAY_BASE_URL", "https://gateway.example")
	t.Setenv("FT_GATEWAY_TOKEN", "gateway-secret")
	t.Setenv("FT_GATEWAY_TOKEN_FILE", "")
	t.Setenv("FT_SESSION_TTL", "30m")
	t.Setenv("FT_COOKIE_SECURE", "false")
	t.Setenv("FT_BOOTSTRAP_EMAIL", "owner@example.com")
	t.Setenv("FT_BOOTSTRAP_PASSWORD", "correct horse battery staple")

	_, err := LoadFromEnv()
	if err == nil || !strings.Contains(err.Error(), "live mode requires secure cookies") {
		t.Fatalf("expected live insecure-cookie rejection, got %v", err)
	}

	t.Setenv("FT_COOKIE_SECURE", "true")
	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv: %v", err)
	}
	if cfg.Mode != Live || cfg.ProductionWritesEnabled {
		t.Fatalf("live mode should default-deny production writes: %+v", cfg)
	}
	if cfg.SessionTTL != 30*time.Minute {
		t.Fatalf("session TTL = %s", cfg.SessionTTL)
	}
	if s := cfg.String(); strings.Contains(s, "secret") || strings.Contains(s, "correct horse") {
		t.Fatalf("String leaked sensitive values: %s", s)
	}
}

func TestLiveGatewayTransportRejectsRemotePlaintextAndAllowsLoopback(t *testing.T) {
	for _, rawURL := range []string{
		"http://gateway.example",
		"http://192.0.2.10:18082",
		"http://0.0.0.0:18082",
	} {
		t.Run("reject "+rawURL, func(t *testing.T) {
			setValidConfigMode(t)
			t.Setenv("FT_MODE", "live")
			t.Setenv("FT_COOKIE_SECURE", "true")
			t.Setenv("FT_GATEWAY_BASE_URL", rawURL)
			if _, err := LoadFromEnv(); err == nil ||
				!strings.Contains(err.Error(), "live mode gateway requires https or loopback") {
				t.Fatalf("live plaintext URL %q error=%v", rawURL, err)
			}
		})
	}

	for _, rawURL := range []string{
		"http://127.0.0.1:18082",
		"http://localhost:18082",
		"http://[::1]:18082",
		"https://gateway.example",
	} {
		t.Run("allow "+rawURL, func(t *testing.T) {
			setValidConfigMode(t)
			t.Setenv("FT_MODE", "live")
			t.Setenv("FT_COOKIE_SECURE", "true")
			t.Setenv("FT_GATEWAY_BASE_URL", rawURL)
			if _, err := LoadFromEnv(); err != nil {
				t.Fatalf("live Gateway URL %q rejected: %v", rawURL, err)
			}
		})
	}
}

func TestConfigRejectsPlaintextDefaultPassword(t *testing.T) {
	t.Setenv("FT_MODE", "test")
	t.Setenv("FT_HTTP_ADDR", "127.0.0.1:0")
	t.Setenv("FT_DATABASE_URL", "postgres://user:secret@example/db")
	t.Setenv("FT_GATEWAY_BASE_URL", "https://gateway.example")
	t.Setenv("FT_GATEWAY_TOKEN", "gateway-secret")
	t.Setenv("FT_GATEWAY_TOKEN_FILE", "")
	t.Setenv("FT_BOOTSTRAP_EMAIL", "owner@example.com")
	t.Setenv("FT_BOOTSTRAP_PASSWORD", "password")

	if _, err := LoadFromEnv(); err == nil {
		t.Fatal("expected weak default password rejection")
	}
}

func TestConfigRejectsInvalidBooleanAndUnsafeGatewayURL(t *testing.T) {
	base := func() {
		t.Setenv("FT_MODE", "test")
		t.Setenv("FT_HTTP_ADDR", "127.0.0.1:0")
		t.Setenv("FT_DATABASE_URL", "postgres://user:secret@example/db?password=query-secret")
		t.Setenv("FT_GATEWAY_TOKEN", "gateway-secret")
		t.Setenv("FT_GATEWAY_TOKEN_FILE", "")
		t.Setenv("FT_BOOTSTRAP_EMAIL", "owner@example.com")
		t.Setenv("FT_BOOTSTRAP_PASSWORD", "correct horse battery staple")
		t.Setenv("FT_COOKIE_SECURE", "false")
		t.Setenv("FT_PRODUCTION_WRITES_ENABLED", "false")
		t.Setenv("FT_STARTUP_MODE", "")
		t.Setenv("FT_CUTOVER_DATABASE_URL", "")
	}
	base()
	t.Setenv("FT_GATEWAY_BASE_URL", "https://gateway.example")
	t.Setenv("FT_COOKIE_SECURE", "definitely")
	if _, err := LoadFromEnv(); err == nil || !strings.Contains(err.Error(), "FT_COOKIE_SECURE") {
		t.Fatalf("expected invalid cookie bool rejection, got %v", err)
	}

	base()
	t.Setenv("FT_GATEWAY_BASE_URL", "https://gateway.example")
	t.Setenv("FT_COOKIE_SECURE", "false")
	t.Setenv("FT_PRODUCTION_WRITES_ENABLED", "perhaps")
	if _, err := LoadFromEnv(); err == nil || !strings.Contains(err.Error(), "FT_PRODUCTION_WRITES_ENABLED") {
		t.Fatalf("expected invalid production writes bool rejection, got %v", err)
	}

	for _, rawURL := range []string{"ftp://gateway.example", "https://user:pass@gateway.example", "gateway.example"} {
		base()
		t.Setenv("FT_GATEWAY_BASE_URL", rawURL)
		if _, err := LoadFromEnv(); err == nil {
			t.Fatalf("expected gateway URL rejection for %q", rawURL)
		}
	}

	base()
	t.Setenv("FT_GATEWAY_BASE_URL", "https://gateway.example/path?api_key=gateway-url-secret")
	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if s := cfg.String(); strings.Contains(s, "gateway-url-secret") || strings.Contains(s, "query-secret") {
		t.Fatalf("String leaked URL secret: %s", s)
	}

	base()
	t.Setenv("FT_STARTUP_MODE", "unknown")
	if _, err := LoadFromEnv(); err == nil || !strings.Contains(err.Error(), "startup mode") {
		t.Fatalf("expected startup mode rejection, got %v", err)
	}
}

func TestConfigLoadsGatewayBearerFromSecretFileAndRejectsAmbiguousSources(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "gateway-token")
	if err := os.WriteFile(tokenFile, []byte("file-backed-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FT_MODE", "test")
	t.Setenv("FT_HTTP_ADDR", "127.0.0.1:0")
	t.Setenv("FT_DATABASE_URL", "postgres://user:secret@example/db")
	t.Setenv("FT_GATEWAY_BASE_URL", "http://127.0.0.1:18082")
	t.Setenv("FT_GATEWAY_TOKEN", "")
	t.Setenv("FT_GATEWAY_TOKEN_FILE", tokenFile)
	t.Setenv("FT_BOOTSTRAP_EMAIL", "owner@example.com")
	t.Setenv("FT_BOOTSTRAP_PASSWORD", "correct horse battery staple")

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GatewayToken != "file-backed-token" {
		t.Fatal("gateway token file was not loaded")
	}
	if strings.Contains(cfg.String(), "file-backed-token") {
		t.Fatal("config string leaked file-backed token")
	}

	t.Setenv("FT_GATEWAY_TOKEN", "second-token")
	if _, err := LoadFromEnv(); err == nil || !strings.Contains(err.Error(), "only one") {
		t.Fatalf("expected ambiguous token source rejection, got %v", err)
	}
}

func TestConfigNormalizesMarketUniverseAndSupportsExplicitAllMarkets(t *testing.T) {
	setValidConfigMode(t)
	t.Setenv("FT_MARKET_SYMBOLS", "eth-usdt, BTC-USDT,eth-usdt")
	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(cfg.MarketSymbols, ",") != "ETH-USDT,BTC-USDT" {
		t.Fatalf("market symbols=%v", cfg.MarketSymbols)
	}

	t.Setenv("FT_MARKET_SYMBOLS", "*")
	cfg, err = LoadFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.MarketSymbols) != 1 || cfg.MarketSymbols[0] != "*" {
		t.Fatalf("all-market selector=%v", cfg.MarketSymbols)
	}

	for _, invalid := range []string{"BTCUSDT", "BTC-USDT,*", "BTC-"} {
		t.Setenv("FT_MARKET_SYMBOLS", invalid)
		if _, err := LoadFromEnv(); err == nil {
			t.Fatalf("expected invalid market symbols rejection for %q", invalid)
		}
	}
}

func TestConfigUsesProductMarketUniverseByDefault(t *testing.T) {
	setValidConfigMode(t)
	t.Setenv("FT_MARKET_SYMBOLS", "")
	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(cfg.MarketSymbols, ",") != "BTC-USDT,ETH-USDT,SOL-USDT" {
		t.Fatalf("default market symbols=%v", cfg.MarketSymbols)
	}
}

func TestConfigStartupModesHaveIsolatedRequirements(t *testing.T) {
	t.Run("maintenance is database and gateway free", func(t *testing.T) {
		t.Setenv("FT_STARTUP_MODE", "maintenance")
		t.Setenv("FT_MODE", "test")
		t.Setenv("FT_HTTP_ADDR", "127.0.0.1:0")
		t.Setenv("FT_DATABASE_URL", "")
		t.Setenv("FT_GATEWAY_BASE_URL", "")
		t.Setenv("FT_GATEWAY_TOKEN", "")
		t.Setenv("FT_GATEWAY_TOKEN_FILE", "")
		t.Setenv("FT_BOOTSTRAP_EMAIL", "")
		t.Setenv("FT_BOOTSTRAP_PASSWORD", "")
		t.Setenv("FT_MAINTENANCE_REASON", "product_mode_cutover")

		cfg, err := LoadFromEnv()
		if err != nil {
			t.Fatalf("LoadFromEnv: %v", err)
		}
		if cfg.StartupMode != StartupMaintenance ||
			cfg.MaintenanceReason != "product_mode_cutover" {
			t.Fatalf("maintenance config=%+v", cfg)
		}
	})

	t.Run("migrate requires cutover identity and verified proof only", func(t *testing.T) {
		t.Setenv("FT_STARTUP_MODE", "migrate")
		t.Setenv("FT_MODE", "test")
		t.Setenv("FT_HTTP_ADDR", "")
		t.Setenv("FT_DATABASE_URL", "")
		t.Setenv("FT_CUTOVER_DATABASE_URL", "postgres://cutover:secret@localhost/funding_trader_cutover_test")
		t.Setenv("FT_GATEWAY_BASE_URL", "")
		t.Setenv("FT_GATEWAY_TOKEN", "")
		t.Setenv("FT_GATEWAY_TOKEN_FILE", "")
		t.Setenv("FT_BOOTSTRAP_EMAIL", "")
		t.Setenv("FT_BOOTSTRAP_PASSWORD", "")
		t.Setenv("FT_CUTOVER_BACKUP_ID", "backup-1")
		t.Setenv("FT_CUTOVER_BACKUP_SHA256", strings.Repeat("b", 64))
		t.Setenv("FT_CUTOVER_BACKUP_VERIFIED_AT", "2026-07-30T07:00:00Z")
		t.Setenv("FT_CUTOVER_OLD_SERVICE_STOPPED", "true")
		t.Setenv("FT_CUTOVER_AUTO_RESTART_DISABLED", "true")

		cfg, err := LoadFromEnv()
		if err != nil {
			t.Fatalf("LoadFromEnv: %v", err)
		}
		if cfg.StartupMode != StartupMigrate ||
			cfg.CutoverDatabaseURL == "" ||
			cfg.CutoverProof.BackupID != "backup-1" ||
			cfg.CutoverProof.BackupVerifiedAt.UTC().Format(time.RFC3339) != "2026-07-30T07:00:00Z" {
			t.Fatalf("migration config=%+v", cfg)
		}
	})

	t.Run("migrate fails closed without complete proof", func(t *testing.T) {
		t.Setenv("FT_STARTUP_MODE", "migrate")
		t.Setenv("FT_MODE", "test")
		t.Setenv("FT_CUTOVER_DATABASE_URL", "postgres://cutover:secret@localhost/funding_trader_cutover_test")
		t.Setenv("FT_CUTOVER_BACKUP_ID", "backup-1")
		t.Setenv("FT_CUTOVER_BACKUP_SHA256", strings.Repeat("b", 64))
		t.Setenv("FT_CUTOVER_BACKUP_VERIFIED_AT", "2026-07-30T07:00:00Z")
		t.Setenv("FT_CUTOVER_OLD_SERVICE_STOPPED", "true")
		t.Setenv("FT_CUTOVER_AUTO_RESTART_DISABLED", "false")
		if _, err := LoadFromEnv(); err == nil ||
			!strings.Contains(err.Error(), "automatic restart") {
			t.Fatalf("incomplete proof error=%v", err)
		}
	})
}

func setValidConfigMode(t *testing.T) {
	t.Helper()
	t.Setenv("FT_MODE", "test")
	t.Setenv("FT_ENVIRONMENT", "")
	t.Setenv("FT_HTTP_ADDR", "127.0.0.1:0")
	t.Setenv("FT_DATABASE_URL", "postgres://user:secret@example/db")
	t.Setenv("FT_GATEWAY_BASE_URL", "https://gateway.example")
	t.Setenv("FT_GATEWAY_TOKEN", "gateway-secret")
	t.Setenv("FT_GATEWAY_TOKEN_FILE", "")
	t.Setenv("FT_BOOTSTRAP_EMAIL", "owner@example.com")
	t.Setenv("FT_BOOTSTRAP_PASSWORD", "correct horse battery staple")
	t.Setenv("FT_COOKIE_SECURE", "false")
	t.Setenv("FT_PRODUCTION_WRITES_ENABLED", "false")
	t.Setenv("FT_STARTUP_MODE", "")
}
