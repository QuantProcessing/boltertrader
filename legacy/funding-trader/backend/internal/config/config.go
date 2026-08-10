package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/cutover"
)

type Mode string

const (
	Test Mode = "test"
	Live Mode = "live"

	defaultMarketSymbols = "BTC-USDT,ETH-USDT,SOL-USDT"
)

func (mode Mode) AllowsVenueEnvironment(environment string) bool {
	switch mode {
	case Test:
		return environment == "demo" || environment == "testnet"
	case Live:
		return environment == "live"
	default:
		return false
	}
}

func (mode Mode) String() string {
	return string(mode)
}

type StartupMode string

const (
	StartupServe       StartupMode = "serve"
	StartupMaintenance StartupMode = "maintenance"
	StartupMigrate     StartupMode = "migrate"
)

type Config struct {
	Mode                    Mode
	StartupMode             StartupMode
	HTTPAddr                string
	DatabaseURL             string
	CutoverDatabaseURL      string
	CutoverProof            cutover.Proof
	GatewayBaseURL          string
	GatewayToken            string
	SessionTTL              time.Duration
	CookieSecure            bool
	ProductionWritesEnabled bool
	MaintenanceReason       string
	BootstrapEmail          string
	BootstrapPassword       string
	MarketSymbols           []string
}

func LoadFromEnv() (Config, error) {
	startupMode := StartupMode(getenv("FT_STARTUP_MODE", string(StartupServe)))
	cfg := Config{
		Mode:              Mode(getenv("FT_MODE", string(Test))),
		StartupMode:       startupMode,
		HTTPAddr:          getenv("FT_HTTP_ADDR", "127.0.0.1:8080"),
		SessionTTL:        24 * time.Hour,
		MaintenanceReason: strings.TrimSpace(os.Getenv("FT_MAINTENANCE_REASON")),
	}

	switch startupMode {
	case StartupServe:
		token, err := gatewayToken()
		if err != nil {
			return Config{}, err
		}
		cookieSecure, err := parseBoolEnv("FT_COOKIE_SECURE", false)
		if err != nil {
			return Config{}, err
		}
		productionWrites, err := parseBoolEnv("FT_PRODUCTION_WRITES_ENABLED", false)
		if err != nil {
			return Config{}, err
		}
		marketSymbols, err := parseMarketSymbols(getenv("FT_MARKET_SYMBOLS", defaultMarketSymbols))
		if err != nil {
			return Config{}, err
		}
		cfg.DatabaseURL = os.Getenv("FT_DATABASE_URL")
		cfg.GatewayBaseURL = os.Getenv("FT_GATEWAY_BASE_URL")
		cfg.GatewayToken = token
		cfg.CookieSecure = cookieSecure
		cfg.ProductionWritesEnabled = productionWrites
		cfg.BootstrapEmail = strings.TrimSpace(os.Getenv("FT_BOOTSTRAP_EMAIL"))
		cfg.BootstrapPassword = os.Getenv("FT_BOOTSTRAP_PASSWORD")
		cfg.MarketSymbols = marketSymbols
		if ttl := os.Getenv("FT_SESSION_TTL"); ttl != "" {
			parsed, err := time.ParseDuration(ttl)
			if err != nil {
				return Config{}, fmt.Errorf("invalid FT_SESSION_TTL: %w", err)
			}
			cfg.SessionTTL = parsed
		}
	case StartupMaintenance:
		if cfg.MaintenanceReason == "" {
			cfg.MaintenanceReason = "operator_maintenance"
		}
	case StartupMigrate:
		oldServiceStopped, err := parseBoolEnv("FT_CUTOVER_OLD_SERVICE_STOPPED", false)
		if err != nil {
			return Config{}, err
		}
		autoRestartDisabled, err := parseBoolEnv("FT_CUTOVER_AUTO_RESTART_DISABLED", false)
		if err != nil {
			return Config{}, err
		}
		verifiedAt, err := parseTimeEnv("FT_CUTOVER_BACKUP_VERIFIED_AT")
		if err != nil {
			return Config{}, err
		}
		cfg.CutoverDatabaseURL = strings.TrimSpace(os.Getenv("FT_CUTOVER_DATABASE_URL"))
		cfg.CutoverProof = cutover.Proof{
			BackupID:            strings.TrimSpace(os.Getenv("FT_CUTOVER_BACKUP_ID")),
			BackupSHA256:        strings.ToLower(strings.TrimSpace(os.Getenv("FT_CUTOVER_BACKUP_SHA256"))),
			BackupVerifiedAt:    verifiedAt,
			OldServiceStopped:   oldServiceStopped,
			AutoRestartDisabled: autoRestartDisabled,
		}
	default:
		return Config{}, fmt.Errorf("invalid startup mode %q", startupMode)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func gatewayToken() (string, error) {
	value := os.Getenv("FT_GATEWAY_TOKEN")
	file := strings.TrimSpace(os.Getenv("FT_GATEWAY_TOKEN_FILE"))
	if value != "" && file != "" {
		return "", fmt.Errorf("set only one of FT_GATEWAY_TOKEN and FT_GATEWAY_TOKEN_FILE")
	}
	if value != "" {
		return value, nil
	}
	if file == "" {
		return "", nil
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return "", fmt.Errorf("read FT_GATEWAY_TOKEN_FILE: %w", err)
	}
	value = strings.TrimSpace(string(raw))
	if value == "" {
		return "", fmt.Errorf("FT_GATEWAY_TOKEN_FILE is empty")
	}
	return value, nil
}

func (c Config) Validate() error {
	switch c.Mode {
	case Test, Live:
	default:
		return fmt.Errorf("invalid mode %q", c.Mode)
	}
	switch c.StartupMode {
	case StartupMaintenance:
		if strings.TrimSpace(c.HTTPAddr) == "" {
			return fmt.Errorf("http addr is required")
		}
		if strings.TrimSpace(c.MaintenanceReason) == "" {
			return fmt.Errorf("maintenance reason is required")
		}
		return nil
	case StartupMigrate:
		if strings.TrimSpace(c.CutoverDatabaseURL) == "" {
			return fmt.Errorf("cutover database url is required")
		}
		if err := c.CutoverProof.Validate(); err != nil {
			return err
		}
		return nil
	case StartupServe:
		if c.HTTPAddr == "" {
			return fmt.Errorf("http addr is required")
		}
		if c.DatabaseURL == "" {
			return fmt.Errorf("database url is required")
		}
		gatewayURL, err := url.Parse(c.GatewayBaseURL)
		if c.GatewayBaseURL == "" || err != nil || !gatewayURL.IsAbs() ||
			(gatewayURL.Scheme != "http" && gatewayURL.Scheme != "https") ||
			gatewayURL.User != nil || gatewayURL.Host == "" {
			return fmt.Errorf("gateway base url is required")
		}
		if c.GatewayToken == "" {
			return fmt.Errorf("gateway token is required")
		}
		if c.SessionTTL <= 0 {
			return fmt.Errorf("session ttl must be positive")
		}
		if c.BootstrapEmail == "" || !strings.Contains(c.BootstrapEmail, "@") {
			return fmt.Errorf("bootstrap email is required")
		}
		if weakPassword(c.BootstrapPassword) {
			return fmt.Errorf("bootstrap password must be explicit and strong")
		}
		if len(c.MarketSymbols) == 0 {
			return fmt.Errorf("market symbols are required")
		}
		if c.Mode == Live && !c.CookieSecure {
			return fmt.Errorf("live mode requires secure cookies")
		}
		if err := ValidateGatewayTransport(c.Mode, gatewayURL); err != nil {
			return err
		}
		return nil
	default:
		return fmt.Errorf("invalid startup mode %q", c.StartupMode)
	}
}

func ValidateGatewayTransport(mode Mode, gatewayURL *url.URL) error {
	if mode != Live || gatewayURL == nil || gatewayURL.Scheme == "https" {
		return nil
	}
	host := gatewayURL.Hostname()
	ip := net.ParseIP(host)
	if gatewayURL.Scheme == "http" &&
		(strings.EqualFold(host, "localhost") || ip != nil && ip.IsLoopback()) {
		return nil
	}
	return fmt.Errorf("live mode gateway requires https or loopback")
}

func (c Config) String() string {
	return fmt.Sprintf(
		"Config{mode:%s startupMode:%s httpAddr:%s databaseURL:%s cutoverDatabaseURL:%s gatewayBaseURL:%s gatewayToken:%s sessionTTL:%s cookieSecure:%s productionWritesEnabled:%s maintenanceReason:%s cutoverProofConfigured:%s bootstrapEmail:%s bootstrapPassword:%s marketSymbols:%s}",
		c.Mode, c.StartupMode, c.HTTPAddr, redact(c.DatabaseURL), redact(c.CutoverDatabaseURL), sanitizeURL(c.GatewayBaseURL),
		redact(c.GatewayToken), c.SessionTTL, strconv.FormatBool(c.CookieSecure), strconv.FormatBool(c.ProductionWritesEnabled),
		c.MaintenanceReason, strconv.FormatBool(c.CutoverProof.BackupID != ""),
		c.BootstrapEmail, redact(c.BootstrapPassword), strings.Join(c.MarketSymbols, ","),
	)
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parseBoolEnv(key string, fallback bool) (bool, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("invalid %s: %w", key, err)
	}
	return parsed, nil
}

func parseTimeEnv(key string) (time.Time, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid %s: %w", key, err)
	}
	return parsed.UTC(), nil
}

func parseMarketSymbols(value string) ([]string, error) {
	seen := make(map[string]struct{})
	result := make([]string, 0)
	for _, raw := range strings.Split(value, ",") {
		symbol := strings.ToUpper(strings.TrimSpace(raw))
		if symbol == "" {
			continue
		}
		if symbol == "*" {
			if len(result) != 0 || strings.Contains(value, ",") {
				return nil, fmt.Errorf("FT_MARKET_SYMBOLS wildcard must be used alone")
			}
			return []string{"*"}, nil
		}
		pair := strings.Split(symbol, "-")
		if len(pair) != 2 || !validMarketAsset(pair[0]) || !validMarketAsset(pair[1]) {
			return nil, fmt.Errorf("invalid FT_MARKET_SYMBOLS value %q", raw)
		}
		if _, exists := seen[symbol]; exists {
			continue
		}
		seen[symbol] = struct{}{}
		result = append(result, symbol)
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("FT_MARKET_SYMBOLS must not be empty")
	}
	return result, nil
}

func validMarketAsset(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < 'A' || character > 'Z' {
			if character < '0' || character > '9' {
				return false
			}
		}
	}
	return true
}

func weakPassword(password string) bool {
	trimmed := strings.TrimSpace(password)
	if len(trimmed) < 12 {
		return true
	}
	switch strings.ToLower(trimmed) {
	case "password", "changeme", "defaultpassword", "adminpassword":
		return true
	default:
		return false
	}
}

func redact(value string) string {
	if value == "" {
		return ""
	}
	return "<redacted>"
}

func sanitizeURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed == nil {
		return redact(raw)
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}
