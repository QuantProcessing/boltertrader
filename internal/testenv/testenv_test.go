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
}

func TestRequireBinanceDemoWriteAllowsCanonicalDemoCredentialsWithoutEnableFlag(t *testing.T) {
	t.Setenv("BINANCE_ENABLE_DEMO_WRITE_TESTS", "")
	t.Setenv("BINANCE_DEMO_API_KEY", "demo-key")
	t.Setenv("BINANCE_DEMO_API_SECRET", "demo-secret")

	completed := false
	t.Run("allow", func(t *testing.T) {
		RequireBinanceDemoWrite(t)
		completed = true
	})
	if !completed {
		t.Fatalf("expected RequireBinanceDemoWrite to allow Demo credentials without BINANCE_ENABLE_DEMO_WRITE_TESTS")
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
	t.Setenv("BINANCE_ENABLE_DEMO_WRITE_TESTS", "")
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
	if BitgetTestnetAPIKeyEnv != "BITGET_TESTNET_API_KEY" {
		t.Fatalf("BitgetTestnetAPIKeyEnv=%q", BitgetTestnetAPIKeyEnv)
	}
	if BitgetTestnetAPISecretEnv != "BITGET_TESTNET_SECRET_KEY" {
		t.Fatalf("BitgetTestnetAPISecretEnv=%q", BitgetTestnetAPISecretEnv)
	}
	if BitgetTestnetPassphraseEnv != "BITGET_TESTNET_PASSPHRASE" {
		t.Fatalf("BitgetTestnetPassphraseEnv=%q", BitgetTestnetPassphraseEnv)
	}
	if BitgetTestnetUSDTPerpSymbolEnv != "BITGET_TESTNET_USDT_PERP_SYMBOL" {
		t.Fatalf("BitgetTestnetUSDTPerpSymbolEnv=%q", BitgetTestnetUSDTPerpSymbolEnv)
	}
	if BitgetTestnetUSDCPerpSymbolEnv != "BITGET_TESTNET_USDC_PERP_SYMBOL" {
		t.Fatalf("BitgetTestnetUSDCPerpSymbolEnv=%q", BitgetTestnetUSDCPerpSymbolEnv)
	}
}

func TestBitgetTestnetConfigDefaultsToPAPTradingProfile(t *testing.T) {
	setBitgetTestnetCredentials(t)
	clearBitgetTestnetOptionalEnv(t)

	cfg, err := BitgetTestnetConfigFromEnv()
	if err != nil {
		t.Fatalf("BitgetTestnetConfigFromEnv: %v", err)
	}
	if !cfg.Profile.PAPTrading {
		t.Fatalf("Bitget Testnet must use paptrading simulated profile by default: %+v", cfg.Profile)
	}
	if cfg.Profile.RESTBaseURL != "https://api.bitget.com" {
		t.Fatalf("testnet rest=%q", cfg.Profile.RESTBaseURL)
	}
	if cfg.Profile.PublicWSURL != "wss://wspap.bitget.com/v3/ws/public" {
		t.Fatalf("testnet public ws=%q", cfg.Profile.PublicWSURL)
	}
	if cfg.Profile.PrivateWSURL != "wss://wspap.bitget.com/v3/ws/private" {
		t.Fatalf("testnet private ws=%q", cfg.Profile.PrivateWSURL)
	}
}

func TestBitgetTestnetConfigAcceptsExplicitOfficialEndpointProfile(t *testing.T) {
	setBitgetTestnetCredentials(t)
	clearBitgetTestnetOptionalEnv(t)
	t.Setenv(BitgetTestnetRESTBaseURLEnv, "https://testnet-api.bitget.example")
	t.Setenv(BitgetTestnetPublicWSURLEnv, "wss://testnet-ws.bitget.example/v3/ws/public")
	t.Setenv(BitgetTestnetPrivateWSURLEnv, "wss://testnet-ws.bitget.example/v3/ws/private")

	cfg, err := BitgetTestnetConfigFromEnv()
	if err != nil {
		t.Fatalf("BitgetTestnetConfigFromEnv: %v", err)
	}
	if cfg.Profile.RESTBaseURL != "https://testnet-api.bitget.example" {
		t.Fatalf("testnet rest=%q", cfg.Profile.RESTBaseURL)
	}
	if cfg.Profile.PAPTrading {
		t.Fatalf("Bitget Testnet must not silently use paptrading demo profile")
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
}

func TestRequireOKXDemoWriteAllowsCanonicalDemoCredentialsWithoutEnableFlag(t *testing.T) {
	setOKXDemoCredentials(t)
	clearOKXDemoOptionalEnv(t)

	completed := false
	t.Run("allow", func(t *testing.T) {
		_ = RequireOKXDemoWrite(t)
		completed = true
	})
	if !completed {
		t.Fatalf("expected RequireOKXDemoWrite to allow Demo credentials without an enable flag")
	}
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

	cfg, err := HyperliquidTestnetReadConfigFromEnv()
	if err != nil {
		t.Fatalf("HyperliquidTestnetReadConfigFromEnv: %v", err)
	}
	if cfg.PrivateKey != "" {
		t.Fatal("read config should not require or synthesize a private key")
	}
	if got := cfg.MaxNotionalUSDC.String(); got != "100" {
		t.Fatalf("default max notional=%s, want 100", got)
	}
}

func TestHyperliquidTestnetConfigFromEnvAcceptsOverrides(t *testing.T) {
	t.Setenv(HyperliquidTestnetPrivateKeyEnv, strings.Repeat("01", 32))
	clearHyperliquidTestnetOptionalEnv(t)
	t.Setenv(HyperliquidTestnetAccountAddressEnv, "0xabc")
	t.Setenv(HyperliquidTestnetVaultEnv, "0xdef")
	t.Setenv(HyperliquidTestnetMaxNotionalUSDCEnv, "12.5")
	t.Setenv(HyperliquidTestnetSpotSymbolEnv, "PURR/USDC")
	t.Setenv(HyperliquidTestnetPerpSymbolEnv, "BTC")
	t.Setenv(HyperliquidTestnetHIP3SymbolEnv, "dex:COIN")

	cfg, err := HyperliquidTestnetConfigFromEnv()
	if err != nil {
		t.Fatalf("HyperliquidTestnetConfigFromEnv: %v", err)
	}
	if cfg.AccountAddress != "0xabc" || cfg.VaultAddress != "0xdef" {
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

func setBitgetTestnetCredentials(t *testing.T) {
	t.Helper()
	t.Setenv(BitgetTestnetAPIKeyEnv, "testnet-key")
	t.Setenv(BitgetTestnetAPISecretEnv, "testnet-secret")
	t.Setenv(BitgetTestnetPassphraseEnv, "testnet-passphrase")
}

func clearBitgetTestnetOptionalEnv(t *testing.T) {
	t.Helper()
	t.Setenv(BitgetTestnetMaxNotionalUSDTEnv, "")
	t.Setenv(BitgetTestnetMaxNotionalUSDCEnv, "")
	t.Setenv(BitgetTestnetSpotSymbolEnv, "")
	t.Setenv(BitgetTestnetUSDTPerpSymbolEnv, "")
	t.Setenv(BitgetTestnetUSDCPerpSymbolEnv, "")
	t.Setenv(BitgetTestnetRESTBaseURLEnv, "")
	t.Setenv(BitgetTestnetPublicWSURLEnv, "")
	t.Setenv(BitgetTestnetPrivateWSURLEnv, "")
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
