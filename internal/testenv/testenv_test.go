package testenv

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRequireFullSkipsWithoutRunFull(t *testing.T) {
	t.Setenv("RUN_FULL", "")

	skipped := false
	t.Run("skip", func(t *testing.T) {
		defer func() {
			skipped = t.Skipped()
		}()
		RequireFull(t)
		t.Fatalf("expected RequireFull to skip without RUN_FULL=1")
	})

	if !skipped {
		t.Fatalf("expected subtest to skip")
	}
}

func TestRequireFullSkipsWhenRequiredEnvMissing(t *testing.T) {
	t.Setenv("RUN_FULL", "1")
	t.Setenv("TESTENV_REQUIRED_VAR", "")

	skipped := false
	t.Run("skip", func(t *testing.T) {
		defer func() {
			skipped = t.Skipped()
		}()
		RequireFull(t, "TESTENV_REQUIRED_VAR")
		t.Fatalf("expected RequireFull to skip when required env is missing")
	})

	if !skipped {
		t.Fatalf("expected subtest to skip")
	}
}

func TestRequireLiveCredentialsSkipsWhenRequiredEnvMissing(t *testing.T) {
	t.Setenv("TESTENV_REQUIRED_VAR", "")

	skipped := false
	t.Run("skip", func(t *testing.T) {
		defer func() {
			skipped = t.Skipped()
		}()
		RequireLiveCredentials(t, "TESTENV_REQUIRED_VAR")
		t.Fatalf("expected RequireLiveCredentials to skip when required env is missing")
	})

	if !skipped {
		t.Fatalf("expected subtest to skip")
	}
}

func TestRequireLiveReadSkipsWithoutEnableFlag(t *testing.T) {
	t.Setenv("BOLTER_ENABLE_LIVE_READ_TESTS", "")

	skipped := false
	t.Run("skip", func(t *testing.T) {
		defer func() {
			skipped = t.Skipped()
		}()
		RequireLiveRead(t)
		t.Fatalf("expected RequireLiveRead to skip without BOLTER_ENABLE_LIVE_READ_TESTS=1")
	})

	if !skipped {
		t.Fatalf("expected subtest to skip")
	}
}

func TestRequireLiveReadSkipsWhenRequiredEnvMissing(t *testing.T) {
	t.Setenv("BOLTER_ENABLE_LIVE_READ_TESTS", "1")
	t.Setenv("TESTENV_REQUIRED_VAR", "")

	skipped := false
	t.Run("skip", func(t *testing.T) {
		defer func() {
			skipped = t.Skipped()
		}()
		RequireLiveRead(t, "TESTENV_REQUIRED_VAR")
		t.Fatalf("expected RequireLiveRead to skip when required env is missing")
	})

	if !skipped {
		t.Fatalf("expected subtest to skip")
	}
}

func TestRequireLiveReadAllowsEnabledReadWithoutCredentials(t *testing.T) {
	t.Setenv("BOLTER_ENABLE_LIVE_READ_TESTS", "1")

	RequireLiveRead(t)
}

func TestTransientLiveNetworkErrorIncludesHostDown(t *testing.T) {
	err := fmt.Errorf(`failed to execute request: Get "https://openapi.okx.com/api/v5/public/instruments?instType=SWAP": dial tcp 169.254.0.2:443: connect: host is down`)
	if !IsTransientLiveNetworkError(err) {
		t.Fatalf("host down error should be treated as transient live network failure")
	}
}

func TestRequireLiveWriteSkipsWithoutEnableFlag(t *testing.T) {
	t.Setenv("TESTENV_ENABLE_LIVE_WRITE", "")

	skipped := false
	t.Run("skip", func(t *testing.T) {
		defer func() {
			skipped = t.Skipped()
		}()
		RequireLiveWrite(t, "TESTENV_ENABLE_LIVE_WRITE")
		t.Fatalf("expected RequireLiveWrite to skip without enable flag")
	})

	if !skipped {
		t.Fatalf("expected subtest to skip")
	}
}

func TestBinanceDemoEnvContractConstants(t *testing.T) {
	if BinanceDemoAPIKeyEnv != "BINANCE_DEMO_API_KEY" {
		t.Fatalf("BinanceDemoAPIKeyEnv=%q", BinanceDemoAPIKeyEnv)
	}
	if BinanceDemoAPISecretEnv != "BINANCE_DEMO_API_SECRET" {
		t.Fatalf("BinanceDemoAPISecretEnv=%q", BinanceDemoAPISecretEnv)
	}
	if BinanceDemoEnableWriteEnv != "BOLTER_ENABLE_BINANCE_DEMO_WRITES" {
		t.Fatalf("BinanceDemoEnableWriteEnv=%q", BinanceDemoEnableWriteEnv)
	}
}

func TestRequireBinanceDemoWriteSkipsCanonicalDemoCredentialsWithoutEnableFlag(t *testing.T) {
	if testing.Short() {
		t.Skip("live-write gate skip-path test excluded by -short")
	}
	t.Setenv(BinanceDemoEnableWriteEnv, "")
	t.Setenv("BINANCE_DEMO_API_KEY", "demo-key")
	t.Setenv("BINANCE_DEMO_API_SECRET", "demo-secret")

	skipped := false
	t.Run("skip", func(t *testing.T) {
		defer func() {
			skipped = t.Skipped()
		}()
		RequireBinanceDemoWrite(t)
		t.Fatal("expected Binance Demo writes to require an explicit enable flag")
	})
	if !skipped {
		t.Fatal("expected RequireBinanceDemoWrite to skip without its explicit enable flag")
	}
}

func TestLighterTestnetEnvContractConstants(t *testing.T) {
	if LighterTestnetPrivateKeyEnv != "LIGHTER_TESTNET_PRIVATE_KEY" {
		t.Fatalf("LighterTestnetPrivateKeyEnv=%q", LighterTestnetPrivateKeyEnv)
	}
	if LighterTestnetAccountIndexEnv != "LIGHTER_TESTNET_ACCOUNT_INDEX" {
		t.Fatalf("LighterTestnetAccountIndexEnv=%q", LighterTestnetAccountIndexEnv)
	}
	if LighterTestnetAPIKeyIndexEnv != "LIGHTER_TESTNET_API_KEY_INDEX" {
		t.Fatalf("LighterTestnetAPIKeyIndexEnv=%q", LighterTestnetAPIKeyIndexEnv)
	}
}

func TestLighterTestnetConfigFromEnvDefaultsMaxNotionalAndSymbols(t *testing.T) {
	t.Setenv(LighterTestnetPrivateKeyEnv, "test-private")
	t.Setenv(LighterTestnetAccountIndexEnv, "66")
	t.Setenv(LighterTestnetAPIKeyIndexEnv, "4")
	t.Setenv(LighterTestnetMaxNotionalUSDCEnv, "")
	t.Setenv(LighterTestnetSpotSymbolEnv, "")
	t.Setenv(LighterTestnetPerpSymbolEnv, "")

	cfg, err := LighterTestnetConfigFromEnv()
	if err != nil {
		t.Fatalf("LighterTestnetConfigFromEnv: %v", err)
	}
	if cfg.AccountIndex != 66 || cfg.APIKeyIndex != 4 {
		t.Fatalf("unexpected indexes: %+v", cfg)
	}
	if cfg.MaxNotionalUSDC.String() != "100" {
		t.Fatalf("max notional=%s, want 100", cfg.MaxNotionalUSDC)
	}
	if cfg.SpotSymbol != LighterTestnetDefaultSpotSymbol || cfg.PerpSymbol != LighterTestnetDefaultPerpSymbol {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if cfg.SpotSymbol != "ETH-USDC" || cfg.PerpSymbol != "ETH" {
		t.Fatalf("Lighter Testnet defaults = spot:%q perp:%q, want ETH-USDC and ETH", cfg.SpotSymbol, cfg.PerpSymbol)
	}
	if strings.Contains(cfg.String(), "test-private") {
		t.Fatalf("config String leaked private key: %s", cfg.String())
	}
}

func TestRequireBinanceDemoWriteSkipsWithoutDemoCredentials(t *testing.T) {
	t.Setenv("BINANCE_ENABLE_DEMO_WRITE_TESTS", "")
	t.Setenv("BINANCE_DEMO_API_KEY", "")
	t.Setenv("BINANCE_DEMO_API_SECRET", "")

	skipped := false
	t.Run("skip", func(t *testing.T) {
		defer func() {
			skipped = t.Skipped()
		}()
		RequireBinanceDemoWrite(t)
		t.Fatalf("expected RequireBinanceDemoWrite to skip without demo credentials")
	})

	if !skipped {
		t.Fatalf("expected subtest to skip")
	}
}

func TestRequireBinanceDemoWriteIgnoresProductionCredentials(t *testing.T) {
	t.Setenv("BINANCE_ENABLE_DEMO_WRITE_TESTS", "")
	t.Setenv("BINANCE_API_KEY", "prod-key")
	t.Setenv("BINANCE_API_SECRET", "prod-secret")
	t.Setenv("BINANCE_DEMO_API_KEY", "")
	t.Setenv("BINANCE_DEMO_API_SECRET", "")

	skipped := false
	t.Run("skip", func(t *testing.T) {
		defer func() {
			skipped = t.Skipped()
		}()
		RequireBinanceDemoWrite(t)
		t.Fatalf("expected RequireBinanceDemoWrite to ignore production credentials")
	})

	if !skipped {
		t.Fatalf("expected subtest to skip")
	}
}

func TestRequireBinanceDemoWriteDoesNotAcceptLegacyPerpTestnetCredentials(t *testing.T) {
	t.Setenv("BINANCE_ENABLE_DEMO_WRITE_TESTS", "")
	t.Setenv("BINANCE_PERP_TESTNET_API_KEY", "legacy-key")
	t.Setenv("BINANCE_PERP_TESTNET_API_SECRET", "legacy-secret")
	t.Setenv("BINANCE_DEMO_API_KEY", "")
	t.Setenv("BINANCE_DEMO_API_SECRET", "")

	skipped := false
	t.Run("skip", func(t *testing.T) {
		defer func() {
			skipped = t.Skipped()
		}()
		RequireBinanceDemoWrite(t)
		t.Fatalf("expected RequireBinanceDemoWrite to reject legacy perp testnet credentials")
	})

	if !skipped {
		t.Fatalf("expected subtest to skip")
	}
}

func TestRequireBinanceDemoWriteAllowsCanonicalDemoCredentials(t *testing.T) {
	t.Setenv(BinanceDemoEnableWriteEnv, "1")
	t.Setenv("BINANCE_DEMO_API_KEY", "demo-key")
	t.Setenv("BINANCE_DEMO_API_SECRET", "demo-secret")

	RequireBinanceDemoWrite(t)
}

func TestBybitDemoEnvContractConstants(t *testing.T) {
	if BybitDemoAPIKeyEnv != "BYBIT_DEMO_API_KEY" {
		t.Fatalf("BybitDemoAPIKeyEnv=%q", BybitDemoAPIKeyEnv)
	}
	if BybitDemoAPISecretEnv != "BYBIT_DEMO_API_SECRET" {
		t.Fatalf("BybitDemoAPISecretEnv=%q", BybitDemoAPISecretEnv)
	}
	if BybitDemoEnableWriteEnv != "BOLTER_ENABLE_BYBIT_DEMO_WRITES" {
		t.Fatalf("BybitDemoEnableWriteEnv=%q", BybitDemoEnableWriteEnv)
	}
}

func TestRequireBybitDemoWriteRequiresExplicitEnableFlag(t *testing.T) {
	if testing.Short() {
		t.Skip("live-write gate test excluded by -short")
	}
	setBybitDemoCredentials(t)
	clearBybitDemoOptionalEnv(t)
	t.Setenv(BybitDemoEnableWriteEnv, "")

	skipped := false
	t.Run("skip", func(t *testing.T) {
		defer func() { skipped = t.Skipped() }()
		_ = RequireBybitDemoWrite(t)
		t.Fatal("expected Bybit Demo writes to require an explicit enable flag")
	})
	if !skipped {
		t.Fatal("expected RequireBybitDemoWrite to skip without its explicit enable flag")
	}

	t.Setenv(BybitDemoEnableWriteEnv, "1")
	_ = RequireBybitDemoWrite(t)
}

func TestBybitDemoConfigFromEnvDefaultsSafetyEnvelope(t *testing.T) {
	setBybitDemoCredentials(t)
	clearBybitDemoOptionalEnv(t)

	cfg, err := BybitDemoConfigFromEnv()
	if err != nil {
		t.Fatalf("BybitDemoConfigFromEnv: %v", err)
	}
	if cfg.Profile.RESTBaseURL != "https://api-demo.bybit.com" {
		t.Fatalf("demo rest=%q", cfg.Profile.RESTBaseURL)
	}
	if cfg.Profile.PublicSpotWSURL != "wss://stream.bybit.com/v5/public/spot" {
		t.Fatalf("demo spot ws=%q", cfg.Profile.PublicSpotWSURL)
	}
	if cfg.Profile.PublicLinearWSURL != "wss://stream.bybit.com/v5/public/linear" {
		t.Fatalf("demo linear ws=%q", cfg.Profile.PublicLinearWSURL)
	}
	if cfg.Profile.PrivateWSURL != "wss://stream-demo.bybit.com/v5/private" {
		t.Fatalf("demo private ws=%q", cfg.Profile.PrivateWSURL)
	}
	if cfg.Profile.SupportsWSTrade || cfg.Profile.TradeWSURL != "" {
		t.Fatalf("Bybit Demo must not expose WS Trade: %+v", cfg.Profile)
	}
}

func TestRequireBybitDemoWriteRejectsProductionCredentials(t *testing.T) {
	t.Setenv("BYBIT_API_KEY", "prod-key")
	t.Setenv("BYBIT_API_SECRET", "prod-secret")
	clearBybitDemoCredentials(t)
	clearBybitDemoOptionalEnv(t)

	skipped := false
	t.Run("skip", func(t *testing.T) {
		defer func() {
			skipped = t.Skipped()
		}()
		_ = RequireBybitDemoWrite(t)
		t.Fatalf("expected RequireBybitDemoWrite to reject production credentials")
	})
	if !skipped {
		t.Fatalf("expected subtest to skip")
	}
}

func TestBitgetDemoWriteEnvContractAndExplicitEnableFlag(t *testing.T) {
	if BitgetDemoEnableWriteEnv != "BOLTER_ENABLE_BITGET_DEMO_WRITES" {
		t.Fatalf("BitgetDemoEnableWriteEnv=%q", BitgetDemoEnableWriteEnv)
	}
	if BitgetDemoAllowCustomWriteEnv != "BOLTER_ALLOW_BITGET_DEMO_CUSTOM_WRITES" {
		t.Fatalf("BitgetDemoAllowCustomWriteEnv=%q", BitgetDemoAllowCustomWriteEnv)
	}
	if testing.Short() {
		t.Skip("live-write gate test excluded by -short")
	}
	setBitgetDemoCredentials(t)
	clearBitgetDemoOptionalEnv(t)
	t.Setenv(BitgetDemoEnableWriteEnv, "")

	skipped := false
	t.Run("skip", func(t *testing.T) {
		defer func() { skipped = t.Skipped() }()
		_ = RequireBitgetDemoWrite(t)
		t.Fatal("expected Bitget Demo writes to require an explicit enable flag")
	})
	if !skipped {
		t.Fatal("expected RequireBitgetDemoWrite to skip without its explicit enable flag")
	}

	t.Setenv(BitgetDemoEnableWriteEnv, "1")
	_ = RequireBitgetDemoWrite(t)
}

func TestBitgetDemoWriteProfileRequiresOfficialDefaultsOrExplicitTLSCustomOptIn(t *testing.T) {
	t.Setenv(BitgetDemoAllowCustomWriteEnv, "")
	official := BitgetEndpointProfile{
		RESTBaseURL:  "https://api.bitget.com",
		PublicWSURL:  "wss://wspap.bitget.com/v3/ws/public",
		PrivateWSURL: "wss://wspap.bitget.com/v3/ws/private",
		PAPTrading:   true,
	}
	if err := validateBitgetDemoWriteProfile(official); err != nil {
		t.Fatalf("official Bitget PAP profile rejected: %v", err)
	}

	custom := BitgetEndpointProfile{
		RESTBaseURL:  "https://demo-api.bitget.example",
		PublicWSURL:  "wss://demo-ws.bitget.example/v3/ws/public",
		PrivateWSURL: "wss://demo-ws.bitget.example/v3/ws/private",
		PAPTrading:   true,
	}
	if err := validateBitgetDemoWriteProfile(custom); err == nil {
		t.Fatal("custom Bitget credentialed write profile accepted without explicit opt-in")
	}

	t.Setenv(BitgetDemoAllowCustomWriteEnv, "1")
	if err := validateBitgetDemoWriteProfile(custom); err != nil {
		t.Fatalf("explicitly approved TLS custom profile rejected: %v", err)
	}

	unsafe := []BitgetEndpointProfile{
		{RESTBaseURL: "http://demo-api.bitget.example", PublicWSURL: custom.PublicWSURL, PrivateWSURL: custom.PrivateWSURL, PAPTrading: true},
		{RESTBaseURL: custom.RESTBaseURL, PublicWSURL: "ws://demo-ws.bitget.example/v3/ws/public", PrivateWSURL: custom.PrivateWSURL, PAPTrading: true},
		{RESTBaseURL: custom.RESTBaseURL, PublicWSURL: custom.PublicWSURL, PrivateWSURL: "wss://ws.bitget.com/v2/ws/private", PAPTrading: true},
		{RESTBaseURL: custom.RESTBaseURL, PublicWSURL: custom.PublicWSURL, PrivateWSURL: custom.PrivateWSURL, PAPTrading: false},
	}
	for _, profile := range unsafe {
		if err := validateBitgetDemoWriteProfile(profile); err == nil {
			t.Fatalf("unsafe Bitget credentialed write profile accepted: %+v", profile)
		}
	}
}

func TestBybitDemoConfigRejectsTestnetCredentialScope(t *testing.T) {
	t.Setenv("BYBIT_TESTNET_API_KEY", "testnet-key")
	t.Setenv("BYBIT_TESTNET_API_SECRET", "testnet-secret")
	clearBybitDemoCredentials(t)
	clearBybitDemoOptionalEnv(t)

	_, err := BybitDemoConfigFromEnv()
	if err == nil {
		t.Fatalf("expected BybitDemoConfigFromEnv to reject Testnet credentials")
	}
	if !strings.Contains(err.Error(), "BYBIT_TESTNET") || !strings.Contains(err.Error(), "BYBIT_DEMO") {
		t.Fatalf("expected error to identify Testnet/Demo credential mismatch, got %v", err)
	}
}

func TestBitgetEnvContractConstants(t *testing.T) {
	if BitgetDemoAPIKeyEnv != "BITGET_DEMO_API_KEY" {
		t.Fatalf("BitgetDemoAPIKeyEnv=%q", BitgetDemoAPIKeyEnv)
	}
	if BitgetDemoAPISecretEnv != "BITGET_DEMO_SECRET_KEY" {
		t.Fatalf("BitgetDemoAPISecretEnv=%q", BitgetDemoAPISecretEnv)
	}
	if BitgetDemoPassphraseEnv != "BITGET_DEMO_PASSPHRASE" {
		t.Fatalf("BitgetDemoPassphraseEnv=%q", BitgetDemoPassphraseEnv)
	}
	if BitgetDemoUSDTPerpSymbolEnv != "BITGET_DEMO_USDT_PERP_SYMBOL" {
		t.Fatalf("BitgetDemoUSDTPerpSymbolEnv=%q", BitgetDemoUSDTPerpSymbolEnv)
	}
	if BitgetDemoUSDCPerpSymbolEnv != "BITGET_DEMO_USDC_PERP_SYMBOL" {
		t.Fatalf("BitgetDemoUSDCPerpSymbolEnv=%q", BitgetDemoUSDCPerpSymbolEnv)
	}
}

func TestBitgetDemoConfigDefaultsToPAPTradingProfile(t *testing.T) {
	setBitgetDemoCredentials(t)
	clearBitgetDemoOptionalEnv(t)

	cfg, err := BitgetDemoConfigFromEnv()
	if err != nil {
		t.Fatalf("BitgetDemoConfigFromEnv: %v", err)
	}
	if !cfg.Profile.PAPTrading {
		t.Fatalf("Bitget Demo must use paptrading simulated profile by default: %+v", cfg.Profile)
	}
	if cfg.Profile.RESTBaseURL != "https://api.bitget.com" {
		t.Fatalf("demo rest=%q", cfg.Profile.RESTBaseURL)
	}
	if cfg.Profile.PublicWSURL != "wss://wspap.bitget.com/v3/ws/public" {
		t.Fatalf("demo public ws=%q", cfg.Profile.PublicWSURL)
	}
	if cfg.Profile.PrivateWSURL != "wss://wspap.bitget.com/v3/ws/private" {
		t.Fatalf("demo private ws=%q", cfg.Profile.PrivateWSURL)
	}
}

func TestBitgetDemoConfigAcceptsExplicitDemoEndpointProfile(t *testing.T) {
	setBitgetDemoCredentials(t)
	clearBitgetDemoOptionalEnv(t)
	t.Setenv(BitgetDemoRESTBaseURLEnv, "https://demo-api.bitget.example")
	t.Setenv(BitgetDemoPublicWSURLEnv, "wss://demo-ws.bitget.example/v3/ws/public")
	t.Setenv(BitgetDemoPrivateWSURLEnv, "wss://demo-ws.bitget.example/v3/ws/private")

	cfg, err := BitgetDemoConfigFromEnv()
	if err != nil {
		t.Fatalf("BitgetDemoConfigFromEnv: %v", err)
	}
	if cfg.Profile.RESTBaseURL != "https://demo-api.bitget.example" {
		t.Fatalf("demo rest=%q", cfg.Profile.RESTBaseURL)
	}
	if !cfg.Profile.PAPTrading {
		t.Fatalf("Bitget Demo custom endpoints must retain paptrading mode: %+v", cfg.Profile)
	}
}

func TestBitgetDemoConfigRejectsKnownProductionWebSocketHosts(t *testing.T) {
	setBitgetDemoCredentials(t)

	for _, tc := range []struct {
		name       string
		publicURL  string
		privateURL string
	}{
		{
			name:       "production public websocket",
			publicURL:  "wss://ws.bitget.com/v2/ws/public",
			privateURL: "wss://wspap.bitget.com/v3/ws/private",
		},
		{
			name:       "production private websocket",
			publicURL:  "wss://wspap.bitget.com/v3/ws/public",
			privateURL: "wss://ws.bitget.com/v3/ws/private",
		},
		{
			name:       "production websocket fqdn",
			publicURL:  "wss://ws.bitget.com./v2/ws/public",
			privateURL: "wss://wspap.bitget.com/v3/ws/private",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearBitgetDemoOptionalEnv(t)
			t.Setenv(BitgetDemoRESTBaseURLEnv, "https://api.bitget.com")
			t.Setenv(BitgetDemoPublicWSURLEnv, tc.publicURL)
			t.Setenv(BitgetDemoPrivateWSURLEnv, tc.privateURL)

			if _, err := BitgetDemoConfigFromEnv(); err == nil || !strings.Contains(err.Error(), "production host") {
				t.Fatalf("BitgetDemoConfigFromEnv err=%v, want production-host rejection", err)
			}
		})
	}
}

func TestBitgetDemoConfigAcceptsLegacyTestnetEnvAliases(t *testing.T) {
	clearBitgetDemoOptionalEnv(t)
	unsetEnvForTest(t, BitgetDemoAPIKeyEnv)
	unsetEnvForTest(t, BitgetDemoAPISecretEnv)
	unsetEnvForTest(t, BitgetDemoPassphraseEnv)
	setBitgetLegacyTestnetCredentials(t)

	cfg, err := BitgetDemoConfigFromEnv()
	if err != nil {
		t.Fatalf("BitgetDemoConfigFromEnv with legacy aliases: %v", err)
	}
	if cfg.APIKey != "legacy-testnet-key" || cfg.APISecret != "legacy-testnet-secret" || cfg.Passphrase != "legacy-testnet-passphrase" {
		t.Fatalf("legacy aliases were not loaded: %+v", cfg)
	}
}

func TestGateTestnetEnvContractConstants(t *testing.T) {
	if GateTestnetAPIKeyEnv != "GATE_TESTNET_API_KEY" {
		t.Fatalf("GateTestnetAPIKeyEnv=%q", GateTestnetAPIKeyEnv)
	}
	if GateTestnetAPISecretEnv != "GATE_TESTNET_API_SECRET" {
		t.Fatalf("GateTestnetAPISecretEnv=%q", GateTestnetAPISecretEnv)
	}
	if GateTestnetEnableWriteEnv != "BOLTER_ENABLE_GATE_TESTNET_WRITES" {
		t.Fatalf("GateTestnetEnableWriteEnv=%q", GateTestnetEnableWriteEnv)
	}
	if GateTestnetUSDTFuturesWSURLEnv != "GATE_TESTNET_USDT_FUTURES_WS_URL" {
		t.Fatalf("GateTestnetUSDTFuturesWSURLEnv=%q", GateTestnetUSDTFuturesWSURLEnv)
	}
}

func TestGateTestnetConfigFromEnvDefaultsSafetyEnvelope(t *testing.T) {
	setGateTestnetCredentials(t)
	clearGateTestnetOptionalEnv(t)

	cfg, err := GateTestnetConfigFromEnv()
	if err != nil {
		t.Fatalf("GateTestnetConfigFromEnv: %v", err)
	}
	if got := cfg.MaxNotionalUSDT.String(); got != "100" {
		t.Fatalf("default max notional=%s, want 100", got)
	}
	if cfg.SpotSymbol != GateTestnetDefaultSpotSymbol {
		t.Fatalf("default spot symbol=%q, want %q", cfg.SpotSymbol, GateTestnetDefaultSpotSymbol)
	}
	if cfg.USDTPerpSymbol != GateTestnetDefaultUSDTPerpSymbol {
		t.Fatalf("default perp symbol=%q, want %q", cfg.USDTPerpSymbol, GateTestnetDefaultUSDTPerpSymbol)
	}
	if cfg.Profile.RESTBaseURL != "https://api-testnet.gateapi.io/api/v4" {
		t.Fatalf("testnet rest=%q", cfg.Profile.RESTBaseURL)
	}
	if cfg.Profile.SpotWSURL != "wss://ws-testnet.gate.com/v4/ws/spot" {
		t.Fatalf("testnet spot ws=%q", cfg.Profile.SpotWSURL)
	}
	if cfg.Profile.FuturesUSDTWSURL != "wss://ws-testnet.gate.com/v4/ws/futures/usdt" {
		t.Fatalf("testnet futures ws=%q", cfg.Profile.FuturesUSDTWSURL)
	}
	if !cfg.Profile.OfficialTestnet {
		t.Fatalf("Gate Testnet profile must be marked official testnet: %+v", cfg.Profile)
	}
}

func TestGateTestnetConfigFromEnvAcceptsOverrides(t *testing.T) {
	setGateTestnetCredentials(t)
	clearGateTestnetOptionalEnv(t)
	t.Setenv(GateTestnetMaxNotionalUSDTEnv, "12.5")
	t.Setenv(GateTestnetSpotSymbolEnv, "BTC_USDT")
	t.Setenv(GateTestnetUSDTPerpSymbolEnv, "ETH_USDT")
	t.Setenv(GateTestnetRESTBaseURLEnv, "https://gate-testnet.example/api/v4")
	t.Setenv(GateTestnetSpotWSURLEnv, "wss://gate-testnet.example/ws/spot")
	t.Setenv(GateTestnetUSDTFuturesWSURLEnv, "wss://gate-testnet.example/ws/usdt")

	cfg, err := GateTestnetConfigFromEnv()
	if err != nil {
		t.Fatalf("GateTestnetConfigFromEnv: %v", err)
	}
	if got := cfg.MaxNotionalUSDT.String(); got != "12.5" {
		t.Fatalf("max notional=%s, want 12.5", got)
	}
	if cfg.SpotSymbol != "BTC_USDT" || cfg.USDTPerpSymbol != "ETH_USDT" {
		t.Fatalf("symbols not applied: %+v", cfg)
	}
	if cfg.Profile.RESTBaseURL != "https://gate-testnet.example/api/v4" {
		t.Fatalf("rest override=%q", cfg.Profile.RESTBaseURL)
	}
	if cfg.Profile.OfficialTestnet {
		t.Fatalf("unverified custom endpoint profile must not be marked official Testnet: %+v", cfg.Profile)
	}
}

func TestGateTestnetConfigFromEnvAcceptsPartialEndpointOverrides(t *testing.T) {
	setGateTestnetCredentials(t)
	clearGateTestnetOptionalEnv(t)
	t.Setenv(GateTestnetRESTBaseURLEnv, "https://gate-testnet.example/api/v4")

	cfg, err := GateTestnetConfigFromEnv()
	if err != nil {
		t.Fatalf("GateTestnetConfigFromEnv: %v", err)
	}
	if cfg.Profile.RESTBaseURL != "https://gate-testnet.example/api/v4" {
		t.Fatalf("rest override=%q", cfg.Profile.RESTBaseURL)
	}
	if cfg.Profile.SpotWSURL != "wss://ws-testnet.gate.com/v4/ws/spot" {
		t.Fatalf("spot ws default=%q", cfg.Profile.SpotWSURL)
	}
	if cfg.Profile.OfficialTestnet {
		t.Fatalf("partially overridden endpoint profile must not be marked official Testnet: %+v", cfg.Profile)
	}
}

func TestGateTestnetWriteProfileAcceptsOnlyKnownOfficialEndpoints(t *testing.T) {
	profile := GateEndpointProfile{
		RESTBaseURL:      "https://api-testnet.gateapi.io/api/v4",
		SpotWSURL:        "wss://ws-testnet.gate.com/v4/ws/spot",
		FuturesUSDTWSURL: "wss://ws-testnet.gate.com/v4/ws/futures/usdt",
		OfficialTestnet:  true,
	}
	if err := validateGateTestnetWriteProfile(profile); err != nil {
		t.Fatalf("known official profile rejected: %v", err)
	}

	profile.FuturesUSDTWSURL = "wss://unverified.example/ws/futures/usdt"
	profile.OfficialTestnet = false
	if err := validateGateTestnetWriteProfile(profile); err == nil {
		t.Fatal("unverified Gate endpoint profile accepted for credentialed writes")
	}
}

func TestGateTestnetConfigFromEnvRejectsInvalidURLs(t *testing.T) {
	setGateTestnetCredentials(t)
	clearGateTestnetOptionalEnv(t)
	t.Setenv(GateTestnetRESTBaseURLEnv, "wss://not-rest.example.test")
	t.Setenv(GateTestnetSpotWSURLEnv, "wss://gate-testnet.example/ws/spot")
	t.Setenv(GateTestnetUSDTFuturesWSURLEnv, "wss://gate-testnet.example/ws/usdt")

	if _, err := GateTestnetConfigFromEnv(); err == nil {
		t.Fatalf("expected invalid REST URL scheme to fail")
	}
}

func TestGateTestnetConfigFromEnvRejectsProductionHosts(t *testing.T) {
	setGateTestnetCredentials(t)
	clearGateTestnetOptionalEnv(t)
	t.Setenv(GateTestnetRESTBaseURLEnv, "https://api.gateio.ws/api/v4")

	if _, err := GateTestnetConfigFromEnv(); err == nil || !strings.Contains(err.Error(), "production host") {
		t.Fatalf("expected production REST host rejection, got %v", err)
	}

	clearGateTestnetOptionalEnv(t)
	t.Setenv(GateTestnetUSDTFuturesWSURLEnv, "wss://fx-ws.gateio.ws/v4/ws/usdt")
	if _, err := GateTestnetConfigFromEnv(); err == nil || !strings.Contains(err.Error(), "production host") {
		t.Fatalf("expected production WS host rejection, got %v", err)
	}
}

func TestGateTestnetConfigFromEnvAcceptsLegacyFuturesWSAlias(t *testing.T) {
	setGateTestnetCredentials(t)
	clearGateTestnetOptionalEnv(t)
	t.Setenv(GateTestnetFuturesUSDTWSURLEnv, "wss://gate-testnet.example/ws/legacy-usdt")

	cfg, err := GateTestnetConfigFromEnv()
	if err != nil {
		t.Fatalf("GateTestnetConfigFromEnv: %v", err)
	}
	if cfg.Profile.FuturesUSDTWSURL != "wss://gate-testnet.example/ws/legacy-usdt" {
		t.Fatalf("legacy futures ws alias not applied: %q", cfg.Profile.FuturesUSDTWSURL)
	}
}

func TestRequireGateTestnetWriteSkipsWithoutEnableFlag(t *testing.T) {
	setGateTestnetCredentials(t)
	clearGateTestnetOptionalEnv(t)
	t.Setenv(GateTestnetEnableWriteEnv, "")

	skipped := false
	t.Run("skip", func(t *testing.T) {
		defer func() {
			skipped = t.Skipped()
		}()
		_ = RequireGateTestnetWrite(t)
		t.Fatalf("expected RequireGateTestnetWrite to skip without enable flag")
	})
	if !skipped {
		t.Fatalf("expected subtest to skip")
	}
}

func TestGateTestnetConfigStringRedactsSecrets(t *testing.T) {
	setGateTestnetCredentials(t)
	clearGateTestnetOptionalEnv(t)
	t.Setenv("PROXY", "socks5://proxy-user:proxy-pass@127.0.0.1:1080")

	cfg, err := GateTestnetConfigFromEnv()
	if err != nil {
		t.Fatalf("GateTestnetConfigFromEnv: %v", err)
	}
	rendered := fmt.Sprintf("%v %#v", cfg, cfg)
	for _, secret := range []string{"gate-key", "gate-secret", "proxy-user", "proxy-pass"} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("rendered config leaked secret %q: %s", secret, rendered)
		}
	}
}

func TestOKXDemoEnvContractConstants(t *testing.T) {
	if OKXDemoAPIKeyEnv != "OKX_DEMO_API_KEY" {
		t.Fatalf("OKXDemoAPIKeyEnv=%q", OKXDemoAPIKeyEnv)
	}
	if OKXDemoAPISecretEnv != "OKX_DEMO_API_SECRET" {
		t.Fatalf("OKXDemoAPISecretEnv=%q", OKXDemoAPISecretEnv)
	}
	if OKXDemoAPIPassphraseEnv != "OKX_DEMO_API_PASSPHRASE" {
		t.Fatalf("OKXDemoAPIPassphraseEnv=%q", OKXDemoAPIPassphraseEnv)
	}
	if OKXDemoEnableWriteEnv != "BOLTER_ENABLE_OKX_DEMO_WRITES" {
		t.Fatalf("OKXDemoEnableWriteEnv=%q", OKXDemoEnableWriteEnv)
	}
	if OKXDemoAllowCustomWriteEnv != "BOLTER_ALLOW_OKX_DEMO_CUSTOM_WRITES" {
		t.Fatalf("OKXDemoAllowCustomWriteEnv=%q", OKXDemoAllowCustomWriteEnv)
	}
}

func TestRequireOKXDemoWriteSkipsCanonicalDemoCredentialsWithoutEnableFlag(t *testing.T) {
	if testing.Short() {
		t.Skip("live-write gate skip-path test excluded by -short")
	}
	setOKXDemoCredentials(t)
	clearOKXDemoOptionalEnv(t)
	t.Setenv(OKXDemoEnableWriteEnv, "")

	skipped := false
	t.Run("skip", func(t *testing.T) {
		defer func() {
			skipped = t.Skipped()
		}()
		_ = RequireOKXDemoWrite(t)
		t.Fatal("expected OKX Demo writes to require an explicit enable flag")
	})
	if !skipped {
		t.Fatal("expected RequireOKXDemoWrite to skip without its explicit enable flag")
	}
}

func TestRequireOKXDemoWriteAllowsCanonicalDemoCredentialsWithEnableFlag(t *testing.T) {
	if testing.Short() {
		t.Skip("live-write gate allow-path test excluded by -short")
	}
	setOKXDemoCredentials(t)
	clearOKXDemoOptionalEnv(t)
	t.Setenv(OKXDemoEnableWriteEnv, "1")

	_ = RequireOKXDemoWrite(t)
}

func TestRequireOKXDemoWriteSkipsWithoutDemoCredentials(t *testing.T) {
	clearOKXDemoCredentials(t)
	t.Setenv("OKX_API_KEY", "prod-key")
	t.Setenv("OKX_API_SECRET", "prod-secret")
	t.Setenv("OKX_API_PASSPHRASE", "prod-passphrase")

	skipped := false
	t.Run("skip", func(t *testing.T) {
		defer func() {
			skipped = t.Skipped()
		}()
		_ = RequireOKXDemoWrite(t)
		t.Fatalf("expected RequireOKXDemoWrite to skip without OKX Demo credentials")
	})

	if !skipped {
		t.Fatalf("expected subtest to skip")
	}
}

func TestOKXDemoConfigFromEnvDefaultsSafetyEnvelope(t *testing.T) {
	setOKXDemoCredentials(t)
	clearOKXDemoOptionalEnv(t)

	cfg, err := OKXDemoConfigFromEnv()
	if err != nil {
		t.Fatalf("OKXDemoConfigFromEnv: %v", err)
	}
	if got := cfg.MaxNotionalUSDT.String(); got != "100" {
		t.Fatalf("default max notional=%s, want 100", got)
	}
	if cfg.SpotSymbol != OKXDemoDefaultSpotSymbol {
		t.Fatalf("default spot symbol=%q, want %q", cfg.SpotSymbol, OKXDemoDefaultSpotSymbol)
	}
	if cfg.PerpSymbol != OKXDemoDefaultPerpSymbol {
		t.Fatalf("default perp symbol=%q, want %q", cfg.PerpSymbol, OKXDemoDefaultPerpSymbol)
	}
	if cfg.HostProfile != OKXDemoHostProfileGlobal {
		t.Fatalf("default host profile=%q, want %q", cfg.HostProfile, OKXDemoHostProfileGlobal)
	}
}

func TestOKXDemoConfigFromEnvAcceptsOverrides(t *testing.T) {
	setOKXDemoCredentials(t)
	clearOKXDemoOptionalEnv(t)
	t.Setenv(OKXDemoMaxNotionalUSDTEnv, "12.5")
	t.Setenv(OKXDemoSpotSymbolEnv, "BTC-USDT")
	t.Setenv(OKXDemoPerpSymbolEnv, "BTC-USDT-SWAP")
	t.Setenv(OKXDemoHostProfileEnv, OKXDemoHostProfileEEA)

	cfg, err := OKXDemoConfigFromEnv()
	if err != nil {
		t.Fatalf("OKXDemoConfigFromEnv: %v", err)
	}
	if got := cfg.MaxNotionalUSDT.String(); got != "12.5" {
		t.Fatalf("max notional=%s, want 12.5", got)
	}
	if cfg.SpotSymbol != "BTC-USDT" || cfg.PerpSymbol != "BTC-USDT-SWAP" {
		t.Fatalf("symbols not applied: spot=%q perp=%q", cfg.SpotSymbol, cfg.PerpSymbol)
	}
	if cfg.HostProfile != OKXDemoHostProfileEEA {
		t.Fatalf("host profile=%q, want %q", cfg.HostProfile, OKXDemoHostProfileEEA)
	}
}

func TestOKXDemoConfigFromEnvRejectsInvalidMaxNotional(t *testing.T) {
	setOKXDemoCredentials(t)
	clearOKXDemoOptionalEnv(t)
	t.Setenv(OKXDemoMaxNotionalUSDTEnv, "0")

	if _, err := OKXDemoConfigFromEnv(); err == nil {
		t.Fatalf("expected zero max notional to fail")
	}
}

func TestOKXDemoConfigFromEnvRequiresCustomEndpointOverrides(t *testing.T) {
	setOKXDemoCredentials(t)
	clearOKXDemoOptionalEnv(t)
	t.Setenv(OKXDemoHostProfileEnv, OKXDemoHostProfileCustom)

	if _, err := OKXDemoConfigFromEnv(); err == nil {
		t.Fatalf("expected custom host profile without endpoint overrides to fail")
	}

	t.Setenv(OKXDemoRESTBaseURLEnv, "https://okx-demo.example.test")
	t.Setenv(OKXDemoWSBaseURLEnv, "wss://okx-ws-demo.example.test")
	if _, err := OKXDemoConfigFromEnv(); err != nil {
		t.Fatalf("expected custom endpoint overrides to pass: %v", err)
	}
}

func TestOKXDemoConfigFromEnvRejectsInvalidURLs(t *testing.T) {
	setOKXDemoCredentials(t)
	clearOKXDemoOptionalEnv(t)
	t.Setenv(OKXDemoRESTBaseURLEnv, "wss://not-rest.example.test")

	if _, err := OKXDemoConfigFromEnv(); err == nil {
		t.Fatalf("expected invalid REST URL scheme to fail")
	}
}

func TestOKXDemoWriteProfileAcceptsOnlyOfficialDefaultsWithoutCustomOptIn(t *testing.T) {
	for _, profile := range []string{OKXDemoHostProfileGlobal, OKXDemoHostProfileEEA} {
		t.Run(profile, func(t *testing.T) {
			cfg := OKXDemoConfig{HostProfile: profile}
			if err := validateOKXDemoWriteProfile(cfg); err != nil {
				t.Fatalf("official %s profile rejected: %v", profile, err)
			}
		})
	}

	for _, cfg := range []OKXDemoConfig{
		{HostProfile: OKXDemoHostProfileGlobal, RESTBaseURL: "https://www.okx.com"},
		{HostProfile: OKXDemoHostProfileEEA, WSBaseURL: "wss://ws.okx.com:8443"},
		{HostProfile: OKXDemoHostProfileCustom, RESTBaseURL: "https://openapi.okx.com", WSBaseURL: "wss://wspap.okx.com:8443"},
	} {
		if err := validateOKXDemoWriteProfile(cfg); err == nil {
			t.Fatalf("unapproved endpoint override accepted for credentialed writes: %+v", cfg)
		}
	}
}

func TestOKXDemoWriteProfileCustomOptInRequiresTLSAndRejectsProductionWebSocket(t *testing.T) {
	t.Setenv(OKXDemoAllowCustomWriteEnv, "1")

	valid := OKXDemoConfig{
		HostProfile: OKXDemoHostProfileCustom,
		RESTBaseURL: "https://openapi.okx.com",
		WSBaseURL:   "wss://wspap.okx.com:8443",
	}
	if err := validateOKXDemoWriteProfile(valid); err != nil {
		t.Fatalf("explicitly approved TLS Demo custom profile rejected: %v", err)
	}

	tests := []OKXDemoConfig{
		{HostProfile: OKXDemoHostProfileCustom, RESTBaseURL: "http://openapi.okx.com", WSBaseURL: "wss://wspap.okx.com:8443"},
		{HostProfile: OKXDemoHostProfileCustom, RESTBaseURL: "https://openapi.okx.com", WSBaseURL: "ws://wspap.okx.com:8443"},
		{HostProfile: OKXDemoHostProfileCustom, RESTBaseURL: "https://openapi.okx.com", WSBaseURL: "wss://ws.okx.com:8443"},
		{HostProfile: OKXDemoHostProfileCustom, RESTBaseURL: "https://eea.okx.com", WSBaseURL: "wss://wseea.okx.com:8443"},
	}
	for _, cfg := range tests {
		if err := validateOKXDemoWriteProfile(cfg); err == nil {
			t.Fatalf("unsafe custom OKX Demo write profile accepted: %+v", cfg)
		}
	}
}

func TestOKXDemoConfigStringRedactsSecrets(t *testing.T) {
	setOKXDemoCredentials(t)
	clearOKXDemoOptionalEnv(t)
	t.Setenv("PROXY", "socks5://proxy-user:proxy-pass@127.0.0.1:1080")

	cfg, err := OKXDemoConfigFromEnv()
	if err != nil {
		t.Fatalf("OKXDemoConfigFromEnv: %v", err)
	}
	rendered := fmt.Sprintf("%v %#v", cfg, cfg)
	for _, secret := range []string{"demo-key", "demo-secret", "demo-passphrase", "proxy-user", "proxy-pass"} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("rendered config leaked secret %q: %s", secret, rendered)
		}
	}
}

func TestOKXDemoHTTPClientRejectsInvalidProxy(t *testing.T) {
	t.Setenv("PROXY", ":// bad proxy")

	if _, err := OKXDemoHTTPClient(time.Second); err == nil {
		t.Fatalf("expected invalid PROXY to fail")
	}
}

func TestProxiedHTTPClientRedactsInvalidProxyCredentials(t *testing.T) {
	const secret = "proxy-super-secret"
	t.Setenv("PROXY", "http://user:"+secret+"@%gh")

	_, err := HyperliquidTestnetHTTPClient(time.Second)
	if err == nil {
		t.Fatal("expected malformed PROXY to fail")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("proxy credential leaked in error: %v", err)
	}
}

func TestConfigStringsRedactEndpointAndProxyURLCredentials(t *testing.T) {
	const (
		username = "url-credential-user"
		secret   = "sentinel-url-credential"
		fragment = "sentinel-url-fragment"
	)
	credentialURL := func(scheme, host, path string) string {
		return fmt.Sprintf("%s://%s:%s@%s%s?token=%s#%s", scheme, username, secret, host, path, secret, fragment)
	}

	tests := []struct {
		name  string
		value any
	}{
		{name: "okx", value: OKXDemoConfig{
			RESTBaseURL: credentialURL("https", "okx.example.test", "/api"),
			WSBaseURL:   credentialURL("wss", "okx-ws.example.test", "/ws"),
			ProxyURL:    credentialURL("socks5", "proxy.example.test", ""),
		}},
		{name: "bybit", value: BybitDemoConfig{
			Profile: BybitEndpointProfile{
				RESTBaseURL:       credentialURL("https", "bybit.example.test", "/api"),
				PublicSpotWSURL:   credentialURL("wss", "bybit-spot.example.test", "/ws"),
				PublicLinearWSURL: credentialURL("wss", "bybit-linear.example.test", "/ws"),
				PrivateWSURL:      credentialURL("wss", "bybit-private.example.test", "/ws"),
				TradeWSURL:        credentialURL("wss", "bybit-trade.example.test", "/ws"),
			},
			ProxyURL: credentialURL("https", "proxy.example.test", ""),
		}},
		{name: "bitget", value: BitgetDemoConfig{
			Profile: BitgetEndpointProfile{
				RESTBaseURL:  credentialURL("https", "bitget.example.test", "/api"),
				PublicWSURL:  credentialURL("wss", "bitget-public.example.test", "/ws"),
				PrivateWSURL: credentialURL("wss", "bitget-private.example.test", "/ws"),
			},
			ProxyURL: credentialURL("https", "proxy.example.test", ""),
		}},
		{name: "gate", value: GateTestnetConfig{
			Profile: GateEndpointProfile{
				RESTBaseURL:      credentialURL("https", "gate.example.test", "/api"),
				SpotWSURL:        credentialURL("wss", "gate-spot.example.test", "/ws"),
				FuturesUSDTWSURL: credentialURL("wss", "gate-futures.example.test", "/ws"),
			},
			ProxyURL: credentialURL("https", "proxy.example.test", ""),
		}},
		{name: "aster", value: AsterTestnetConfig{
			SpotProfile: AsterEndpointProfile{
				RESTURL:     credentialURL("https", "aster-spot.example.test", "/api"),
				PublicWSURL: credentialURL("wss", "aster-spot-public.example.test", "/ws"),
				UserWSURL:   credentialURL("wss", "aster-spot-user.example.test", "/ws"),
			},
			PerpProfile: AsterEndpointProfile{
				RESTURL:     credentialURL("https", "aster-perp.example.test", "/api"),
				PublicWSURL: credentialURL("wss", "aster-perp-public.example.test", "/ws"),
				UserWSURL:   credentialURL("wss", "aster-perp-user.example.test", "/ws"),
			},
			ProxyURL: credentialURL("https", "proxy.example.test", ""),
		}},
		{name: "nado", value: NadoTestnetConfig{
			Profile: NadoEndpointProfile{
				GatewayV1URL:       credentialURL("https", "nado-gateway-v1.example.test", "/api"),
				GatewayV2URL:       credentialURL("https", "nado-gateway-v2.example.test", "/api"),
				ArchiveV1URL:       credentialURL("https", "nado-archive-v1.example.test", "/api"),
				ArchiveV2URL:       credentialURL("https", "nado-archive-v2.example.test", "/api"),
				GatewayWSURL:       credentialURL("wss", "nado-gateway.example.test", "/ws"),
				SubscriptionsWSURL: credentialURL("wss", "nado-subscriptions.example.test", "/ws"),
				TriggerURL:         credentialURL("https", "nado-trigger.example.test", "/api"),
			},
			ProxyURL: credentialURL("https", "proxy.example.test", ""),
		}},
		{name: "hyperliquid", value: HyperliquidTestnetConfig{ProxyURL: credentialURL("https", "proxy.example.test", "")}},
		{name: "lighter", value: LighterTestnetConfig{ProxyURL: credentialURL("https", "proxy.example.test", "")}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rendered := fmt.Sprintf("%v %#v", tt.value, tt.value)
			for _, sensitive := range []string{username, secret, fragment, "token="} {
				if strings.Contains(rendered, sensitive) {
					t.Fatalf("rendered config leaked URL credential %q: %s", sensitive, rendered)
				}
			}
			if !strings.Contains(rendered, "example.test") {
				t.Fatalf("rendered config removed all endpoint identity: %s", rendered)
			}
		})
	}
}

func TestCredentialedURLValidationErrorsDoNotEchoRawURLs(t *testing.T) {
	const secret = "sentinel-validation-url-secret"
	tests := []struct {
		name string
		err  error
	}{
		{
			name: "malformed generic URL",
			err:  validateURL("https://user:"+secret+"@%gh/path?token="+secret+"#"+secret, "TEST_URL", "https"),
		},
		{
			name: "Bitget production websocket",
			err: validateBitgetDemoWSURL(
				"wss://user:"+secret+"@ws.bitget.com/v3/ws/private?token="+secret+"#"+secret,
				BitgetDemoPrivateWSURLEnv,
			),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.err == nil {
				t.Fatal("credentialed unsafe URL unexpectedly validated")
			}
			if strings.Contains(tt.err.Error(), secret) {
				t.Fatalf("validation error leaked URL credentials: %v", tt.err)
			}
		})
	}

	t.Setenv(AsterTestnetSpotRESTURLEnv, "https://user:"+secret+"@sapi.asterdex.com/private?token="+secret+"#"+secret)
	if err := validateEndpointOverride(AsterTestnetSpotRESTURLEnv, "https://sapi.asterdex-testnet.com"); err == nil {
		t.Fatal("unsafe Aster endpoint override unexpectedly validated")
	} else if strings.Contains(err.Error(), secret) {
		t.Fatalf("Aster endpoint validation leaked URL credentials: %v", err)
	}
}

func TestOKXDemoHTTPClientIgnoresInheritedProxyEnv(t *testing.T) {
	t.Setenv("PROXY", "")
	t.Setenv("ALL_PROXY", "http://127.0.0.1:65535")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:65535")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:65535")

	client, err := OKXDemoHTTPClient(time.Second)
	if err != nil {
		t.Fatalf("OKXDemoHTTPClient: %v", err)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type=%T, want *http.Transport", client.Transport)
	}
	if transport.Proxy != nil {
		req, _ := http.NewRequest(http.MethodGet, "https://example.test", nil)
		proxy, err := transport.Proxy(req)
		if err != nil {
			t.Fatalf("proxy func: %v", err)
		}
		if proxy != nil {
			t.Fatalf("OKX Demo HTTP client must ignore inherited proxy env unless PROXY is set")
		}
	}
}

func TestHyperliquidTestnetEnvContractConstants(t *testing.T) {
	if HyperliquidTestnetPrivateKeyEnv != "HYPERLIQUID_TESTNET_PK" {
		t.Fatalf("HyperliquidTestnetPrivateKeyEnv=%q", HyperliquidTestnetPrivateKeyEnv)
	}
	if HyperliquidTestnetEnableWriteEnv != "BOLTER_ENABLE_HYPERLIQUID_TESTNET_WRITES" {
		t.Fatalf("HyperliquidTestnetEnableWriteEnv=%q", HyperliquidTestnetEnableWriteEnv)
	}
	if HyperliquidTestnetMaxNotionalUSDCEnv != "HYPERLIQUID_TESTNET_MAX_NOTIONAL_USDC" {
		t.Fatalf("HyperliquidTestnetMaxNotionalUSDCEnv=%q", HyperliquidTestnetMaxNotionalUSDCEnv)
	}
}

func TestRequireHyperliquidTestnetWriteSkipsWithoutEnableFlag(t *testing.T) {
	t.Setenv(HyperliquidTestnetPrivateKeyEnv, strings.Repeat("01", 32))
	t.Setenv(HyperliquidTestnetEnableWriteEnv, "")
	clearHyperliquidTestnetOptionalEnv(t)

	skipped := false
	t.Run("skip", func(t *testing.T) {
		defer func() {
			skipped = t.Skipped()
		}()
		_ = RequireHyperliquidTestnetWrite(t)
		t.Fatalf("expected RequireHyperliquidTestnetWrite to require %s", HyperliquidTestnetEnableWriteEnv)
	})
	if !skipped {
		t.Fatal("expected subtest to skip")
	}
}

func TestRequireHyperliquidTestnetWriteAllowsCanonicalPrivateKeyWithEnableFlag(t *testing.T) {
	if testing.Short() {
		t.Skip("live-write gate allow-path test excluded by -short")
	}
	t.Setenv(HyperliquidTestnetPrivateKeyEnv, strings.Repeat("01", 32))
	t.Setenv(HyperliquidTestnetEnableWriteEnv, "1")
	clearHyperliquidTestnetOptionalEnv(t)

	completed := false
	t.Run("allow", func(t *testing.T) {
		_ = RequireHyperliquidTestnetWrite(t)
		completed = true
	})
	if !completed {
		t.Fatal("expected Hyperliquid testnet private key plus write gate to enable write tests")
	}
}

func TestRequireHyperliquidTestnetWriteRejectsLegacyLiveCredentials(t *testing.T) {
	t.Setenv(HyperliquidTestnetPrivateKeyEnv, "")
	t.Setenv("HYPERLIQUID_PRIVATE_KEY", strings.Repeat("01", 32))
	t.Setenv("HYPERLIQUID_ACCOUNT_ADDR", "0xabc")
	clearHyperliquidTestnetOptionalEnv(t)

	skipped := false
	t.Run("skip", func(t *testing.T) {
		defer func() {
			skipped = t.Skipped()
		}()
		_ = RequireHyperliquidTestnetWrite(t)
		t.Fatalf("expected RequireHyperliquidTestnetWrite to reject legacy live credentials")
	})
	if !skipped {
		t.Fatal("expected subtest to skip")
	}
}

func TestHyperliquidTestnetConfigFromEnvDefaultsSafetyEnvelope(t *testing.T) {
	t.Setenv(HyperliquidTestnetPrivateKeyEnv, strings.Repeat("01", 32))
	clearHyperliquidTestnetOptionalEnv(t)

	cfg, err := HyperliquidTestnetConfigFromEnv()
	if err != nil {
		t.Fatalf("HyperliquidTestnetConfigFromEnv: %v", err)
	}
	if got := cfg.MaxNotionalUSDC.String(); got != "100" {
		t.Fatalf("default max notional=%s, want 100", got)
	}
	if cfg.AccountAddress != "" || cfg.VaultAddress != "" {
		t.Fatalf("optional account/vault should default empty, got account=%q vault=%q", cfg.AccountAddress, cfg.VaultAddress)
	}
}

func TestHyperliquidTestnetReadConfigFromEnvDoesNotRequirePrivateKey(t *testing.T) {
	t.Setenv(HyperliquidTestnetPrivateKeyEnv, "")
	clearHyperliquidTestnetOptionalEnv(t)
	t.Setenv(HyperliquidTestnetAccountAddressEnv, "0xabc0000000000000000000000000000000000000")

	cfg, err := HyperliquidTestnetReadConfigFromEnv()
	if err != nil {
		t.Fatalf("HyperliquidTestnetReadConfigFromEnv: %v", err)
	}
	if cfg.PrivateKey != "" {
		t.Fatal("read config should not require or synthesize a private key")
	}
	if cfg.AccountAddress != "0xabc0000000000000000000000000000000000000" {
		t.Fatalf("account address=%q, want configured read identity", cfg.AccountAddress)
	}
	if got := cfg.MaxNotionalUSDC.String(); got != "100" {
		t.Fatalf("default max notional=%s, want 100", got)
	}
}

func TestHyperliquidTestnetReadConfigRequiresIdentity(t *testing.T) {
	t.Setenv(HyperliquidTestnetPrivateKeyEnv, "")
	clearHyperliquidTestnetOptionalEnv(t)

	if _, err := HyperliquidTestnetReadConfigFromEnv(); err == nil {
		t.Fatal("expected read config to require private key or account address")
	}
}

func TestHyperliquidTestnetConfigFromEnvAcceptsOverrides(t *testing.T) {
	t.Setenv(HyperliquidTestnetPrivateKeyEnv, strings.Repeat("01", 32))
	clearHyperliquidTestnetOptionalEnv(t)
	t.Setenv(HyperliquidTestnetAccountAddressEnv, "0xabc0000000000000000000000000000000000000")
	t.Setenv(HyperliquidTestnetVaultEnv, "0xdef")
	t.Setenv(HyperliquidTestnetMaxNotionalUSDCEnv, "12.5")
	t.Setenv(HyperliquidTestnetSpotSymbolEnv, "PURR/USDC")
	t.Setenv(HyperliquidTestnetPerpSymbolEnv, "BTC")
	t.Setenv(HyperliquidTestnetHIP3SymbolEnv, "dex:COIN")

	cfg, err := HyperliquidTestnetConfigFromEnv()
	if err != nil {
		t.Fatalf("HyperliquidTestnetConfigFromEnv: %v", err)
	}
	if cfg.AccountAddress != "0xabc0000000000000000000000000000000000000" || cfg.VaultAddress != "0xdef" {
		t.Fatalf("account/vault not applied: account=%q vault=%q", cfg.AccountAddress, cfg.VaultAddress)
	}
	if got := cfg.MaxNotionalUSDC.String(); got != "12.5" {
		t.Fatalf("max notional=%s, want 12.5", got)
	}
	if cfg.SpotSymbol != "PURR/USDC" || cfg.PerpSymbol != "BTC" || cfg.HIP3Symbol != "dex:COIN" {
		t.Fatalf("symbols not applied: spot=%q perp=%q hip3=%q", cfg.SpotSymbol, cfg.PerpSymbol, cfg.HIP3Symbol)
	}
}

func TestHyperliquidTestnetConfigRejectsInvalidMaxNotional(t *testing.T) {
	t.Setenv(HyperliquidTestnetPrivateKeyEnv, strings.Repeat("01", 32))
	clearHyperliquidTestnetOptionalEnv(t)
	t.Setenv(HyperliquidTestnetMaxNotionalUSDCEnv, "0")

	if _, err := HyperliquidTestnetConfigFromEnv(); err == nil {
		t.Fatal("expected zero max notional to fail")
	}
}

func TestHyperliquidTestnetHTTPClientIgnoresInheritedProxyEnv(t *testing.T) {
	t.Setenv("PROXY", "")
	t.Setenv("ALL_PROXY", "http://127.0.0.1:65535")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:65535")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:65535")

	client, err := HyperliquidTestnetHTTPClient(time.Second)
	if err != nil {
		t.Fatalf("HyperliquidTestnetHTTPClient: %v", err)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type=%T, want *http.Transport", client.Transport)
	}
	if transport.Proxy != nil {
		req, _ := http.NewRequest(http.MethodGet, "https://example.test", nil)
		proxy, err := transport.Proxy(req)
		if err != nil {
			t.Fatalf("proxy func: %v", err)
		}
		if proxy != nil {
			t.Fatal("Hyperliquid testnet HTTP client must ignore inherited proxy env unless PROXY is set")
		}
	}
}

func TestRequireSoakSkipsWithoutRunSoak(t *testing.T) {
	t.Setenv("RUN_SOAK", "")

	skipped := false
	t.Run("skip", func(t *testing.T) {
		defer func() {
			skipped = t.Skipped()
		}()
		RequireSoak(t)
		t.Fatalf("expected RequireSoak to skip without RUN_SOAK=1")
	})

	if !skipped {
		t.Fatalf("expected subtest to skip")
	}
}

func TestIsTransientLiveNetworkError(t *testing.T) {
	cases := []error{
		errors.New("Get https://api.example.test: context deadline exceeded (Client.Timeout exceeded while awaiting headers)"),
		errors.New("EOF"),
		errors.New("tls handshake timeout"),
		errors.New("tls: failed to verify certificate: x509: certificate is valid for unexpected.example.test, not demo-api.binance.com"),
	}
	for _, err := range cases {
		if !IsTransientLiveNetworkError(err) {
			t.Fatalf("expected transient live network error: %v", err)
		}
	}
	if IsTransientLiveNetworkError(errors.New("invalid signature")) {
		t.Fatal("semantic exchange errors should not be treated as transient network errors")
	}
}

func TestAsterTestnetReadConfigUsesOfficialProfilesAndDefaults(t *testing.T) {
	clearAsterTestnetEnv(t)

	cfg, err := AsterTestnetReadConfigFromEnv()
	if err != nil {
		t.Fatalf("AsterTestnetReadConfigFromEnv: %v", err)
	}
	if cfg.SpotProfile.RESTURL != "https://sapi.asterdex-testnet.com" || cfg.SpotProfile.ChainID != 714 {
		t.Fatalf("spot profile=%+v", cfg.SpotProfile)
	}
	if cfg.PerpProfile.RESTURL != "https://fapi.asterdex-testnet.com" || cfg.PerpProfile.ChainID != 714 {
		t.Fatalf("perp profile=%+v", cfg.PerpProfile)
	}
	if cfg.MaxNotionalUSDT.String() != "100" || cfg.SpotSymbol != "" || cfg.PerpSymbol != "" {
		t.Fatalf("defaults=%+v", cfg)
	}
}

func TestAsterTestnetConfigParsesCredentialsAndRedacts(t *testing.T) {
	clearAsterTestnetEnv(t)
	t.Setenv(AsterTestnetUserAddressEnv, "0x1111111111111111111111111111111111111111")
	t.Setenv(AsterTestnetSignerPrivateKeyEnv, strings.Repeat("a", 64))
	t.Setenv(AsterTestnetExpectedSignerAddressEnv, "0x2222222222222222222222222222222222222222")
	t.Setenv(AsterTestnetSpotSymbolEnv, "ETHUSDT")
	t.Setenv(AsterTestnetPerpSymbolEnv, "BTCUSDT")
	t.Setenv(AsterTestnetMaxNotionalUSDTEnv, "25")
	t.Setenv("PROXY", "http://user:proxy-password@127.0.0.1:7890")

	cfg, err := AsterTestnetConfigFromEnv()
	if err != nil {
		t.Fatalf("AsterTestnetConfigFromEnv: %v", err)
	}
	if cfg.UserAddress == "" || cfg.SignerPrivateKey == "" || cfg.ExpectedSignerAddress == "" {
		t.Fatalf("credentials missing: %+v", cfg)
	}
	if cfg.MaxNotionalUSDT.String() != "25" || cfg.SpotSymbol != "ETHUSDT" || cfg.PerpSymbol != "BTCUSDT" {
		t.Fatalf("parsed config=%+v", cfg)
	}
	printed := cfg.String()
	for _, secret := range []string{cfg.SignerPrivateKey, "proxy-password"} {
		if strings.Contains(printed, secret) {
			t.Fatalf("config string leaked secret %q: %s", secret, printed)
		}
	}
}

func TestAsterTestnetConfigRejectsProductionEndpointOverrideWithoutEcho(t *testing.T) {
	clearAsterTestnetEnv(t)
	t.Setenv(AsterTestnetSpotRESTURLEnv, "https://sapi.asterdex.com/private-path")

	_, err := AsterTestnetReadConfigFromEnv()
	if err == nil || !strings.Contains(err.Error(), AsterTestnetSpotRESTURLEnv) {
		t.Fatalf("production override err=%v", err)
	}
	if strings.Contains(err.Error(), "private-path") {
		t.Fatalf("endpoint error echoed configured URL: %v", err)
	}
}

func TestNadoTestnetReadConfigUsesOfficialProfileAndDefaults(t *testing.T) {
	clearNadoTestnetEnv(t)

	cfg, err := NadoTestnetReadConfigFromEnv()
	if err != nil {
		t.Fatalf("NadoTestnetReadConfigFromEnv: %v", err)
	}
	if cfg.Profile.GatewayV1URL != "https://gateway.test.nado.xyz/v1" || cfg.Profile.ChainID != 763373 {
		t.Fatalf("profile=%+v", cfg.Profile)
	}
	if cfg.Subaccount != "default" || cfg.MaxNotionalUSDT0.String() != "100" {
		t.Fatalf("defaults=%+v", cfg)
	}
}

func TestNadoTestnetConfigParsesCredentialsAndRedacts(t *testing.T) {
	clearNadoTestnetEnv(t)
	t.Setenv(NadoTestnetPrivateKeyEnv, strings.Repeat("b", 64))
	t.Setenv(NadoTestnetSubaccountNameEnv, "strategy-a")
	t.Setenv(NadoTestnetSpotSymbolEnv, "WBTC")
	t.Setenv(NadoTestnetPerpSymbolEnv, "BTC-PERP")
	t.Setenv(NadoTestnetMaxNotionalUSDT0Env, "40")
	t.Setenv("PROXY", "http://user:nado-proxy-password@127.0.0.1:7890")

	cfg, err := NadoTestnetConfigFromEnv()
	if err != nil {
		t.Fatalf("NadoTestnetConfigFromEnv: %v", err)
	}
	if cfg.PrivateKey == "" || cfg.Subaccount != "strategy-a" || cfg.MaxNotionalUSDT0.String() != "40" {
		t.Fatalf("parsed config=%+v", cfg)
	}
	printed := cfg.String()
	for _, secret := range []string{cfg.PrivateKey, "nado-proxy-password"} {
		if strings.Contains(printed, secret) {
			t.Fatalf("config string leaked secret %q: %s", secret, printed)
		}
	}
}

func TestNadoTestnetConfigRejectsProductionEndpointOverrideWithoutEcho(t *testing.T) {
	clearNadoTestnetEnv(t)
	t.Setenv(NadoTestnetGatewayV1URLEnv, "https://gateway.prod.nado.xyz/v1/private-path")

	_, err := NadoTestnetReadConfigFromEnv()
	if err == nil || !strings.Contains(err.Error(), NadoTestnetGatewayV1URLEnv) {
		t.Fatalf("production override err=%v", err)
	}
	if strings.Contains(err.Error(), "private-path") {
		t.Fatalf("endpoint error echoed configured URL: %v", err)
	}
}

func TestTestnetConfigDoesNotEchoMalformedProxyCredentials(t *testing.T) {
	clearAsterTestnetEnv(t)
	t.Setenv("PROXY", "http://proxy-user:proxy-secret@%zz")

	_, err := AsterTestnetReadConfigFromEnv()
	if err == nil {
		t.Fatal("malformed proxy unexpectedly accepted")
	}
	if strings.Contains(err.Error(), "proxy-secret") {
		t.Fatalf("proxy validation error leaked credentials: %v", err)
	}
}

func TestLoadRepoEnvDoesNotOverrideExistingEnv(t *testing.T) {
	tmp := t.TempDir()
	origWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(origWD)
	})

	if err := os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module testenv\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(tmp, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".env"), []byte("TESTENV_OVERRIDE=file\nTESTENV_FROM_FILE=present\n"), 0o644); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	if err := os.Chdir(filepath.Join(tmp, "nested")); err != nil {
		t.Fatalf("chdir nested: %v", err)
	}

	t.Setenv("TESTENV_OVERRIDE", "shell")
	if err := os.Unsetenv("TESTENV_FROM_FILE"); err != nil {
		t.Fatalf("unset TESTENV_FROM_FILE: %v", err)
	}

	if err := LoadRepoEnv(); err != nil {
		t.Fatalf("LoadRepoEnv: %v", err)
	}

	if got := os.Getenv("TESTENV_OVERRIDE"); got != "shell" {
		t.Fatalf("expected shell env to win, got %q", got)
	}
	if got := os.Getenv("TESTENV_FROM_FILE"); got != "present" {
		t.Fatalf("expected missing env to load from file, got %q", got)
	}
}

func TestLoadRepoEnvLegacyAliasDoesNotOverrideExplicitEmptyCanonical(t *testing.T) {
	tmp := t.TempDir()
	origWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(origWD)
	})

	if err := os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module testenv\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(tmp, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(tmp, ".env"),
		[]byte(BitgetLegacyTestnetAPIKeyEnv+"=legacy-demo-key\n"),
		0o600,
	); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	if err := os.Chdir(filepath.Join(tmp, "nested")); err != nil {
		t.Fatalf("chdir nested: %v", err)
	}

	unsetEnvForTest(t, BitgetLegacyTestnetAPIKeyEnv)
	t.Setenv(BitgetDemoAPIKeyEnv, "")

	if err := LoadRepoEnv(); err != nil {
		t.Fatalf("LoadRepoEnv: %v", err)
	}
	if got := os.Getenv(BitgetDemoAPIKeyEnv); got != "" {
		t.Fatalf("legacy alias overrode explicit empty canonical value with %q", got)
	}
	if got := os.Getenv(BitgetLegacyTestnetAPIKeyEnv); got != "legacy-demo-key" {
		t.Fatalf("legacy source was not loaded from repo env: %q", got)
	}
}

func TestLoadRepoEnvDoesNotImportExecutionGates(t *testing.T) {
	tmp := t.TempDir()
	origWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(origWD)
	})

	if err := os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module testenv\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(tmp, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	const ordinaryKey = "TESTENV_FROM_FILE_WITH_GATES"
	gates := []string{
		"BOLTER_ENABLE_LIVE_READ_TESTS",
		BinanceDemoEnableWriteEnv,
		OKXDemoAllowCustomWriteEnv,
		"BOLTER_ENABLE_NADO_UNSAFE_RAW_SDK_WRITES",
		"BINANCE_ENABLE_LIVE_WRITE_TESTS",
		"BINANCE_PERP_ENABLE_LIVE_WRITE_TESTS",
		"BYBIT_ENABLE_LIVE_WRITE_TESTS",
		"TESTENV_ENABLE_LIVE_WRITE",
		"ASTER_REALTIME_WS",
		"BINANCE_REALTIME_WS",
		"RUN_FULL",
		"RUN_SOAK",
	}
	contents := ordinaryKey + "=present\n"
	for _, key := range gates {
		contents += key + "=1\n"
	}
	if err := os.WriteFile(filepath.Join(tmp, ".env"), []byte(contents), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	if err := os.Chdir(filepath.Join(tmp, "nested")); err != nil {
		t.Fatalf("chdir nested: %v", err)
	}

	unsetEnvForTest(t, ordinaryKey)
	for _, key := range gates {
		unsetEnvForTest(t, key)
	}

	if err := LoadRepoEnv(); err != nil {
		t.Fatalf("LoadRepoEnv: %v", err)
	}
	if got := os.Getenv(ordinaryKey); got != "present" {
		t.Fatalf("ordinary repo env=%q, want present", got)
	}
	for _, key := range gates {
		if value, exists := os.LookupEnv(key); exists {
			t.Fatalf("execution gate %s was imported from .env with value %q", key, value)
		}
	}
}

func TestRequireLiveWriteDoesNotImportEnableFlagFromRepoEnv(t *testing.T) {
	if testing.Short() {
		t.Skip("live-write gate import regression excluded by -short")
	}
	tmp := t.TempDir()
	origWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(origWD)
	})

	if err := os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module testenv\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(tmp, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	const (
		gate       = "TESTENV_ENABLE_LIVE_WRITE"
		credential = "TESTENV_LIVE_WRITE_CREDENTIAL"
	)
	if err := os.WriteFile(
		filepath.Join(tmp, ".env"),
		[]byte(gate+"=1\n"+credential+"=present\n"),
		0o600,
	); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	if err := os.Chdir(filepath.Join(tmp, "nested")); err != nil {
		t.Fatalf("chdir nested: %v", err)
	}
	unsetEnvForTest(t, gate)
	unsetEnvForTest(t, credential)

	for attempt := 1; attempt <= 2; attempt++ {
		skipped := false
		t.Run(fmt.Sprintf("attempt-%d", attempt), func(t *testing.T) {
			defer func() { skipped = t.Skipped() }()
			RequireLiveWrite(t, gate, credential)
			t.Fatal("repo .env activated a live-write test")
		})
		if !skipped {
			t.Fatalf("attempt %d did not skip without a process-local gate", attempt)
		}
		if value, exists := os.LookupEnv(gate); exists {
			t.Fatalf("attempt %d imported gate %s=%q from .env", attempt, gate, value)
		}
	}
}

func TestLoadRepoEnvAppliesLegacyAliases(t *testing.T) {
	tmp := t.TempDir()
	origWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(origWD)
	})

	if err := os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module testenv\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(tmp, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".env"), []byte("OKX_SECRET_KEY=legacy-secret\nNADO_SUB_ACCOUNT_NAME=legacy-sub\n"), 0o644); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	if err := os.Chdir(filepath.Join(tmp, "nested")); err != nil {
		t.Fatalf("chdir nested: %v", err)
	}
	if err := os.Unsetenv("OKX_API_SECRET"); err != nil {
		t.Fatalf("unset OKX_API_SECRET: %v", err)
	}
	if err := os.Unsetenv("OKX_SECRET_KEY"); err != nil {
		t.Fatalf("unset OKX_SECRET_KEY: %v", err)
	}
	if err := os.Unsetenv("NADO_SUBACCOUNT_NAME"); err != nil {
		t.Fatalf("unset NADO_SUBACCOUNT_NAME: %v", err)
	}
	if err := os.Unsetenv("NADO_SUB_ACCOUNT_NAME"); err != nil {
		t.Fatalf("unset NADO_SUB_ACCOUNT_NAME: %v", err)
	}

	if err := LoadRepoEnv(); err != nil {
		t.Fatalf("LoadRepoEnv: %v", err)
	}

	if got := os.Getenv("OKX_API_SECRET"); got != "legacy-secret" {
		t.Fatalf("expected legacy OKX secret alias to populate canonical env, got %q", got)
	}
	if got := os.Getenv("NADO_SUBACCOUNT_NAME"); got != "legacy-sub" {
		t.Fatalf("expected legacy Nado sub-account alias to populate canonical env, got %q", got)
	}
}

func unsetEnvForTest(t *testing.T, key string) {
	t.Helper()
	value, existed := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unset %s: %v", key, err)
	}
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv(key, value)
			return
		}
		_ = os.Unsetenv(key)
	})
}

func setOKXDemoCredentials(t *testing.T) {
	t.Helper()
	t.Setenv(OKXDemoAPIKeyEnv, "demo-key")
	t.Setenv(OKXDemoAPISecretEnv, "demo-secret")
	t.Setenv(OKXDemoAPIPassphraseEnv, "demo-passphrase")
}

func clearOKXDemoCredentials(t *testing.T) {
	t.Helper()
	t.Setenv(OKXDemoAPIKeyEnv, "")
	t.Setenv(OKXDemoAPISecretEnv, "")
	t.Setenv(OKXDemoAPIPassphraseEnv, "")
}

func clearOKXDemoOptionalEnv(t *testing.T) {
	t.Helper()
	t.Setenv(OKXDemoMaxNotionalUSDTEnv, "")
	t.Setenv(OKXDemoSpotSymbolEnv, "")
	t.Setenv(OKXDemoPerpSymbolEnv, "")
	t.Setenv(OKXDemoHostProfileEnv, "")
	t.Setenv(OKXDemoRESTBaseURLEnv, "")
	t.Setenv(OKXDemoWSBaseURLEnv, "")
	t.Setenv(OKXDemoAllowCustomWriteEnv, "")
	t.Setenv("PROXY", "")
}

func setBybitDemoCredentials(t *testing.T) {
	t.Helper()
	t.Setenv(BybitDemoAPIKeyEnv, "testnet-key")
	t.Setenv(BybitDemoAPISecretEnv, "testnet-secret")
}

func clearBybitDemoCredentials(t *testing.T) {
	t.Helper()
	t.Setenv(BybitDemoAPIKeyEnv, "")
	t.Setenv(BybitDemoAPISecretEnv, "")
}

func clearBybitDemoOptionalEnv(t *testing.T) {
	t.Helper()
	t.Setenv(BybitDemoMaxNotionalUSDTEnv, "")
	t.Setenv(BybitDemoMaxNotionalUSDCEnv, "")
	t.Setenv(BybitDemoSpotSymbolEnv, "")
	t.Setenv(BybitDemoUSDTPerpSymbolEnv, "")
	t.Setenv(BybitDemoUSDCPerpSymbolEnv, "")
	t.Setenv("PROXY", "")
}

func setBitgetDemoCredentials(t *testing.T) {
	t.Helper()
	t.Setenv(BitgetDemoAPIKeyEnv, "demo-key")
	t.Setenv(BitgetDemoAPISecretEnv, "demo-secret")
	t.Setenv(BitgetDemoPassphraseEnv, "demo-passphrase")
}

func setBitgetLegacyTestnetCredentials(t *testing.T) {
	t.Helper()
	t.Setenv(BitgetLegacyTestnetAPIKeyEnv, "legacy-testnet-key")
	t.Setenv(BitgetLegacyTestnetAPISecretEnv, "legacy-testnet-secret")
	t.Setenv(BitgetLegacyTestnetPassphraseEnv, "legacy-testnet-passphrase")
}

func clearBitgetDemoOptionalEnv(t *testing.T) {
	t.Helper()
	for _, env := range []string{
		BitgetDemoMaxNotionalUSDTEnv,
		BitgetDemoMaxNotionalUSDCEnv,
		BitgetDemoSpotSymbolEnv,
		BitgetDemoUSDTPerpSymbolEnv,
		BitgetDemoUSDCPerpSymbolEnv,
		BitgetDemoRESTBaseURLEnv,
		BitgetDemoPublicWSURLEnv,
		BitgetDemoPrivateWSURLEnv,
		BitgetLegacyTestnetSpotSymbolEnv,
		BitgetLegacyTestnetUSDTPerpSymbolEnv,
		BitgetLegacyTestnetUSDCPerpSymbolEnv,
		BitgetLegacyTestnetRESTBaseURLEnv,
		BitgetLegacyTestnetPublicWSURLEnv,
		BitgetLegacyTestnetPrivateWSURLEnv,
	} {
		t.Setenv(env, "")
	}
	t.Setenv(BitgetDemoAllowCustomWriteEnv, "")
	t.Setenv("PROXY", "")
}

func setGateTestnetCredentials(t *testing.T) {
	t.Helper()
	t.Setenv(GateTestnetAPIKeyEnv, "gate-key")
	t.Setenv(GateTestnetAPISecretEnv, "gate-secret")
}

func clearGateTestnetOptionalEnv(t *testing.T) {
	t.Helper()
	for _, env := range []string{
		GateTestnetMaxNotionalUSDTEnv,
		GateTestnetSpotSymbolEnv,
		GateTestnetUSDTPerpSymbolEnv,
		GateTestnetRESTBaseURLEnv,
		GateTestnetSpotWSURLEnv,
		GateTestnetUSDTFuturesWSURLEnv,
		GateTestnetFuturesUSDTWSURLEnv,
	} {
		t.Setenv(env, "")
	}
	t.Setenv("PROXY", "")
}

func clearHyperliquidTestnetOptionalEnv(t *testing.T) {
	t.Helper()
	t.Setenv(HyperliquidTestnetAccountAddressEnv, "")
	t.Setenv(HyperliquidTestnetVaultEnv, "")
	t.Setenv(HyperliquidTestnetMaxNotionalUSDCEnv, "")
	t.Setenv(HyperliquidTestnetSpotSymbolEnv, "")
	t.Setenv(HyperliquidTestnetPerpSymbolEnv, "")
	t.Setenv(HyperliquidTestnetHIP3SymbolEnv, "")
	t.Setenv("PROXY", "")
}

func clearAsterTestnetEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		AsterTestnetUserAddressEnv,
		AsterTestnetSignerPrivateKeyEnv,
		AsterTestnetExpectedSignerAddressEnv,
		AsterTestnetSpotSymbolEnv,
		AsterTestnetPerpSymbolEnv,
		AsterTestnetMaxNotionalUSDTEnv,
		AsterTestnetSpotRESTURLEnv,
		AsterTestnetSpotPublicWSURLEnv,
		AsterTestnetSpotUserWSURLEnv,
		AsterTestnetPerpRESTURLEnv,
		AsterTestnetPerpPublicWSURLEnv,
		AsterTestnetPerpUserWSURLEnv,
		AsterTestnetEnableWriteEnv,
		"PROXY",
	} {
		t.Setenv(key, "")
	}
}

func clearNadoTestnetEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		NadoTestnetPrivateKeyEnv,
		NadoTestnetSubaccountNameEnv,
		NadoTestnetSpotSymbolEnv,
		NadoTestnetPerpSymbolEnv,
		NadoTestnetMaxNotionalUSDT0Env,
		NadoTestnetGatewayV1URLEnv,
		NadoTestnetGatewayV2URLEnv,
		NadoTestnetArchiveV1URLEnv,
		NadoTestnetArchiveV2URLEnv,
		NadoTestnetGatewayWSURLEnv,
		NadoTestnetSubscriptionsWSURLEnv,
		NadoTestnetTriggerURLEnv,
		NadoTestnetEnableWriteEnv,
		"PROXY",
	} {
		t.Setenv(key, "")
	}
}
