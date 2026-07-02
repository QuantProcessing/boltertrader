package testenv

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joho/godotenv"
	"github.com/shopspring/decimal"
)

const (
	BinanceDemoAPIKeyEnv    = "BINANCE_DEMO_API_KEY"
	BinanceDemoAPISecretEnv = "BINANCE_DEMO_API_SECRET"

	OKXDemoAPIKeyEnv        = "OKX_DEMO_API_KEY"
	OKXDemoAPISecretEnv     = "OKX_DEMO_API_SECRET"
	OKXDemoAPIPassphraseEnv = "OKX_DEMO_API_PASSPHRASE"

	OKXDemoMaxNotionalUSDTEnv = "OKX_DEMO_MAX_NOTIONAL_USDT"
	OKXDemoSpotSymbolEnv      = "OKX_DEMO_SPOT_SYMBOL"
	OKXDemoPerpSymbolEnv      = "OKX_DEMO_PERP_SYMBOL"
	OKXDemoHostProfileEnv     = "OKX_DEMO_HOST_PROFILE"
	OKXDemoRESTBaseURLEnv     = "OKX_DEMO_REST_BASE_URL"
	OKXDemoWSBaseURLEnv       = "OKX_DEMO_WS_BASE_URL"

	OKXDemoDefaultMaxNotionalUSDT = "100"
	OKXDemoDefaultSpotSymbol      = "ETH-USDT"
	OKXDemoDefaultPerpSymbol      = "ETH-USDT-SWAP"

	OKXDemoHostProfileGlobal = "global"
	OKXDemoHostProfileEEA    = "eea"
	OKXDemoHostProfileCustom = "custom"
)

type OKXDemoConfig struct {
	APIKey          string
	APISecret       string
	Passphrase      string
	MaxNotionalUSDT decimal.Decimal
	SpotSymbol      string
	PerpSymbol      string
	HostProfile     string
	RESTBaseURL     string
	WSBaseURL       string
	ProxyURL        string
}

func (c OKXDemoConfig) String() string {
	return fmt.Sprintf(
		"OKXDemoConfig{APIKey:%s APISecret:%s Passphrase:%s MaxNotionalUSDT:%s SpotSymbol:%q PerpSymbol:%q HostProfile:%q RESTBaseURL:%q WSBaseURL:%q ProxyURL:%q}",
		redactSecret(c.APIKey),
		redactSecret(c.APISecret),
		redactSecret(c.Passphrase),
		c.MaxNotionalUSDT.String(),
		c.SpotSymbol,
		c.PerpSymbol,
		c.HostProfile,
		c.RESTBaseURL,
		c.WSBaseURL,
		redactURL(c.ProxyURL),
	)
}

func (c OKXDemoConfig) GoString() string {
	return c.String()
}

// LoadRepoEnv loads the repo-root .env into the current process without
// overriding shell-exported environment variables.
func LoadRepoEnv() error {
	root, err := findRepoRoot()
	if err != nil {
		return err
	}

	values, err := godotenv.Read(filepath.Join(root, ".env"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for key, value := range values {
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}

	applyLegacyAliases()

	return nil
}

func RequireEnv(t testing.TB, vars ...string) {
	t.Helper()

	if err := LoadRepoEnv(); err != nil {
		t.Fatalf("load repo .env: %v", err)
	}

	var missing []string
	for _, key := range vars {
		if os.Getenv(key) == "" {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		t.Skipf("skipping: missing required env %s", strings.Join(missing, ", "))
	}
}

func RequireLiveCredentials(t testing.TB, vars ...string) {
	t.Helper()

	if err := LoadRepoEnv(); err != nil {
		t.Fatalf("load repo .env: %v", err)
	}
	RequireEnv(t, vars...)
}

func RequireLiveRead(t testing.TB, vars ...string) {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping: live read test excluded by -short")
	}
	if err := LoadRepoEnv(); err != nil {
		t.Fatalf("load repo .env: %v", err)
	}
	if os.Getenv("BOLTER_ENABLE_LIVE_READ_TESTS") != "1" {
		t.Skip("skipping live read test: set BOLTER_ENABLE_LIVE_READ_TESTS=1 to enable real exchange read execution")
	}
	RequireEnv(t, vars...)
}

func RequireLiveWrite(t testing.TB, enableVar string, vars ...string) {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping: live write test excluded by -short")
	}
	if err := LoadRepoEnv(); err != nil {
		t.Fatalf("load repo .env: %v", err)
	}
	if os.Getenv(enableVar) != "1" {
		t.Skipf("skipping live write test: set %s=1 to enable real exchange write execution", enableVar)
	}
	RequireEnv(t, vars...)
}

func RequireBinanceDemoWrite(t testing.TB) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping: Binance Demo write test excluded by -short")
	}
	RequireEnv(t, BinanceDemoAPIKeyEnv, BinanceDemoAPISecretEnv)
}

func RequireOKXDemoWrite(t testing.TB) OKXDemoConfig {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping: OKX Demo write test excluded by -short")
	}
	if err := LoadRepoEnv(); err != nil {
		t.Fatalf("load repo .env: %v", err)
	}
	if missing := missingEnv(OKXDemoAPIKeyEnv, OKXDemoAPISecretEnv, OKXDemoAPIPassphraseEnv); len(missing) > 0 {
		t.Skipf("skipping OKX Demo write test: missing required env %s", strings.Join(missing, ", "))
	}
	cfg, err := OKXDemoConfigFromEnv()
	if err != nil {
		t.Fatalf("OKX Demo env: %v", err)
	}
	return cfg
}

func OKXDemoConfigFromEnv() (OKXDemoConfig, error) {
	missing := missingEnv(OKXDemoAPIKeyEnv, OKXDemoAPISecretEnv, OKXDemoAPIPassphraseEnv)
	if len(missing) > 0 {
		return OKXDemoConfig{}, fmt.Errorf("missing required env %s", strings.Join(missing, ", "))
	}

	maxNotional, err := decimal.NewFromString(envOrDefault(OKXDemoMaxNotionalUSDTEnv, OKXDemoDefaultMaxNotionalUSDT))
	if err != nil {
		return OKXDemoConfig{}, fmt.Errorf("%s must be a decimal: %w", OKXDemoMaxNotionalUSDTEnv, err)
	}
	if !maxNotional.IsPositive() {
		return OKXDemoConfig{}, fmt.Errorf("%s must be positive", OKXDemoMaxNotionalUSDTEnv)
	}

	hostProfile := strings.ToLower(strings.TrimSpace(envOrDefault(OKXDemoHostProfileEnv, OKXDemoHostProfileGlobal)))
	switch hostProfile {
	case OKXDemoHostProfileGlobal, OKXDemoHostProfileEEA, OKXDemoHostProfileCustom:
	default:
		return OKXDemoConfig{}, fmt.Errorf("%s must be one of %q, %q, or %q", OKXDemoHostProfileEnv, OKXDemoHostProfileGlobal, OKXDemoHostProfileEEA, OKXDemoHostProfileCustom)
	}

	restBaseURL := strings.TrimSpace(os.Getenv(OKXDemoRESTBaseURLEnv))
	wsBaseURL := strings.TrimSpace(os.Getenv(OKXDemoWSBaseURLEnv))
	if hostProfile == OKXDemoHostProfileCustom && (restBaseURL == "" || wsBaseURL == "") {
		return OKXDemoConfig{}, fmt.Errorf("%s=%s requires both %s and %s", OKXDemoHostProfileEnv, OKXDemoHostProfileCustom, OKXDemoRESTBaseURLEnv, OKXDemoWSBaseURLEnv)
	}
	if restBaseURL != "" {
		if err := validateURL(restBaseURL, OKXDemoRESTBaseURLEnv, "http", "https"); err != nil {
			return OKXDemoConfig{}, err
		}
	}
	if wsBaseURL != "" {
		if err := validateURL(wsBaseURL, OKXDemoWSBaseURLEnv, "ws", "wss"); err != nil {
			return OKXDemoConfig{}, err
		}
	}

	proxyURL := strings.TrimSpace(os.Getenv("PROXY"))
	if proxyURL != "" {
		if err := validateURL(proxyURL, "PROXY", "http", "https", "socks5"); err != nil {
			return OKXDemoConfig{}, err
		}
	}

	return OKXDemoConfig{
		APIKey:          os.Getenv(OKXDemoAPIKeyEnv),
		APISecret:       os.Getenv(OKXDemoAPISecretEnv),
		Passphrase:      os.Getenv(OKXDemoAPIPassphraseEnv),
		MaxNotionalUSDT: maxNotional,
		SpotSymbol:      envOrDefault(OKXDemoSpotSymbolEnv, OKXDemoDefaultSpotSymbol),
		PerpSymbol:      envOrDefault(OKXDemoPerpSymbolEnv, OKXDemoDefaultPerpSymbol),
		HostProfile:     hostProfile,
		RESTBaseURL:     restBaseURL,
		WSBaseURL:       wsBaseURL,
		ProxyURL:        proxyURL,
	}, nil
}

func OKXDemoHTTPClient(timeout time.Duration) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil

	if proxy := strings.TrimSpace(os.Getenv("PROXY")); proxy != "" {
		proxyURL, err := url.Parse(proxy)
		if err != nil {
			return nil, fmt.Errorf("invalid PROXY: %w", err)
		}
		if err := validateURL(proxy, "PROXY", "http", "https", "socks5"); err != nil {
			return nil, err
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	}

	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}, nil
}

func SkipIfTransientLiveNetworkError(t testing.TB, err error, context string) {
	t.Helper()
	if IsTransientLiveNetworkError(err) {
		if context == "" {
			context = "live exchange endpoint"
		}
		t.Skipf("skipping: %s unavailable during live test: %v", context, err)
	}
}

func IsTransientLiveNetworkError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, io.EOF) {
		return true
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "client.timeout exceeded while awaiting headers") ||
		strings.Contains(lower, "context deadline exceeded") ||
		strings.Contains(lower, "tls handshake timeout") ||
		strings.Contains(lower, "failed to verify certificate") ||
		strings.Contains(lower, "x509: certificate is valid for") ||
		strings.Contains(lower, "connection reset by peer") ||
		strings.Contains(lower, "connection refused") ||
		strings.Contains(lower, "network is unreachable") ||
		strings.Contains(lower, "no such host") ||
		strings.TrimSpace(lower) == "eof"
}

func RequireFull(t testing.TB, vars ...string) {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping: full verification test excluded by -short")
	}
	if err := LoadRepoEnv(); err != nil {
		t.Fatalf("load repo .env: %v", err)
	}
	if os.Getenv("RUN_FULL") != "1" {
		t.Skip("skipping: set RUN_FULL=1 to run full verification tests")
	}
	RequireEnv(t, vars...)
}

func RequireSoak(t testing.TB, vars ...string) {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping: soak verification test excluded by -short")
	}
	if err := LoadRepoEnv(); err != nil {
		t.Fatalf("load repo .env: %v", err)
	}
	if os.Getenv("RUN_SOAK") != "1" {
		t.Skip("skipping: set RUN_SOAK=1 to run soak verification tests")
	}
	RequireEnv(t, vars...)
}

func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}

func applyLegacyAliases() {
	for legacy, canonical := range map[string]string{
		"EDGEX_PRIVATE_KEY":     "EDGEX_STARK_PRIVATE_KEY",
		"NADO_SUB_ACCOUNT_NAME": "NADO_SUBACCOUNT_NAME",
		"OKX_SECRET_KEY":        "OKX_API_SECRET",
		"OKX_PASSPHRASE":        "OKX_API_PASSPHRASE",
	} {
		if _, exists := os.LookupEnv(canonical); exists {
			continue
		}
		if value, exists := os.LookupEnv(legacy); exists {
			_ = os.Setenv(canonical, value)
		}
	}
}

func missingEnv(vars ...string) []string {
	var missing []string
	for _, key := range vars {
		if os.Getenv(key) == "" {
			missing = append(missing, key)
		}
	}
	return missing
}

func envOrDefault(key, defaultValue string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return defaultValue
	}
	return value
}

func validateURL(raw, envName string, allowedSchemes ...string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s has invalid URL: %w", envName, err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("%s must include URL scheme and host", envName)
	}
	scheme := strings.ToLower(parsed.Scheme)
	for _, allowed := range allowedSchemes {
		if scheme == allowed {
			return nil
		}
	}
	return fmt.Errorf("%s must use one of schemes: %s", envName, strings.Join(allowedSchemes, ", "))
}

func redactSecret(value string) string {
	if value == "" {
		return ""
	}
	if len(value) <= 4 {
		return "****"
	}
	return value[:2] + "****" + value[len(value)-2:]
}

func redactURL(raw string) string {
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return redactSecret(raw)
	}
	if parsed.User != nil {
		username := parsed.User.Username()
		if username == "" {
			parsed.User = url.UserPassword("****", "****")
		} else {
			parsed.User = url.UserPassword(redactSecret(username), "****")
		}
	}
	return parsed.String()
}
