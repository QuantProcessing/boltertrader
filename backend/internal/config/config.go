package config

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"
	"golang.org/x/crypto/bcrypt"
)

const (
	FundingWorkerTokenFileEnv      = "BT_FUNDING_WORKER_TOKEN_FILE"
	ManualPreviewSigningKeyFileEnv = "BT_MANUAL_PREVIEW_SIGNING_KEY_FILE"
	GatewayURLEnv                  = "BT_GATEWAY_URL"
	GatewayTokenFileEnv            = "BT_GATEWAY_TOKEN_FILE"
	FundingLiveEnabledEnv          = "BT_FUNDING_LIVE_ENABLED"
	FundingLiveMaxNotionalEnv      = "BT_FUNDING_LIVE_MAX_NOTIONAL"
	LivePrivateWritesEnabledEnv    = "BT_LIVE_PRIVATE_WRITES_ENABLED"
)

type Config struct {
	Mode                        string
	BrowserListenAddr           string
	WorkListenAddr              string
	BootstrapUsername           string
	BootstrapPasswordHash       string
	FundingWorkerTokenFile      string
	FundingWorkerTokenDigest    [sha256.Size]byte
	ManualPreviewSigningKeyFile string
	ManualPreviewSigningKey     []byte
	GatewayURL                  string
	GatewayTokenFile            string
	GatewayToken                []byte
	LivePrivateWritesEnabled    bool
	DatabaseName                string
	DatabaseRole                string
	FundingLiveEnabled          bool
	FundingLiveMaxNotional      string
}

func Load() (Config, error) {
	if strings.TrimSpace(os.Getenv("BT_PLATFORM_WORK_TOKEN")) != "" || strings.TrimSpace(os.Getenv("BT_FUNDING_WORKER_TOKEN")) != "" {
		return Config{}, fmt.Errorf("inline worker tokens are forbidden; use %s", FundingWorkerTokenFileEnv)
	}
	if strings.TrimSpace(os.Getenv("BT_MANUAL_PREVIEW_SIGNING_KEY")) != "" {
		return Config{}, fmt.Errorf("inline Manual preview signing keys are forbidden; use %s", ManualPreviewSigningKeyFileEnv)
	}
	fundingLiveEnabled, err := getenvBool(FundingLiveEnabledEnv)
	if err != nil {
		return Config{}, err
	}
	cfg := Config{
		Mode:                        strings.TrimSpace(os.Getenv("BT_MODE")),
		BrowserListenAddr:           getenv("BT_PLATFORM_BROWSER_LISTEN_ADDR", "127.0.0.1:18091"),
		WorkListenAddr:              getenv("BT_PLATFORM_WORK_LISTEN_ADDR", "127.0.0.1:18092"),
		BootstrapUsername:           strings.TrimSpace(os.Getenv("BT_BOOTSTRAP_USERNAME")),
		BootstrapPasswordHash:       strings.TrimSpace(os.Getenv("BT_BOOTSTRAP_PASSWORD_HASH")),
		FundingWorkerTokenFile:      strings.TrimSpace(os.Getenv(FundingWorkerTokenFileEnv)),
		ManualPreviewSigningKeyFile: strings.TrimSpace(os.Getenv(ManualPreviewSigningKeyFileEnv)),
		GatewayURL:                  strings.TrimSpace(os.Getenv(GatewayURLEnv)),
		GatewayTokenFile:            strings.TrimSpace(os.Getenv(GatewayTokenFileEnv)),
		DatabaseName:                strings.TrimSpace(os.Getenv("BT_PLATFORM_DATABASE")),
		DatabaseRole:                strings.TrimSpace(os.Getenv("BT_PLATFORM_DATABASE_ROLE")),
		FundingLiveEnabled:          fundingLiveEnabled,
		FundingLiveMaxNotional:      strings.TrimSpace(os.Getenv(FundingLiveMaxNotionalEnv)),
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv(LivePrivateWritesEnabledEnv))) {
	case "", "false":
	case "true":
		cfg.LivePrivateWritesEnabled = true
	default:
		return Config{}, fmt.Errorf("%s must be true or false", LivePrivateWritesEnabledEnv)
	}
	if cfg.FundingWorkerTokenFile == "" {
		return Config{}, fmt.Errorf("%s is required", FundingWorkerTokenFileEnv)
	}
	token, err := readProtectedToken(cfg.FundingWorkerTokenFile, FundingWorkerTokenFileEnv)
	if err != nil {
		return Config{}, err
	}
	cfg.FundingWorkerTokenDigest = sha256.Sum256(token)
	for index := range token {
		token[index] = 0
	}
	if cfg.ManualPreviewSigningKeyFile == "" {
		return Config{}, fmt.Errorf("%s is required", ManualPreviewSigningKeyFileEnv)
	}
	if filepath.Base(filepath.Dir(filepath.Clean(cfg.ManualPreviewSigningKeyFile))) != cfg.Mode {
		return Config{}, fmt.Errorf("%s must be scoped to BT_MODE %s", ManualPreviewSigningKeyFileEnv, cfg.Mode)
	}
	cfg.ManualPreviewSigningKey, err = readProtectedToken(cfg.ManualPreviewSigningKeyFile, ManualPreviewSigningKeyFileEnv)
	if err != nil {
		return Config{}, err
	}
	if cfg.GatewayTokenFile == "" {
		clearBytes(cfg.ManualPreviewSigningKey)
		return Config{}, fmt.Errorf("%s is required", GatewayTokenFileEnv)
	}
	cfg.GatewayToken, err = readProtectedToken(cfg.GatewayTokenFile, GatewayTokenFileEnv)
	if err != nil {
		clearBytes(cfg.ManualPreviewSigningKey)
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		clearBytes(cfg.ManualPreviewSigningKey)
		return Config{}, err
	}
	wantDatabase := "bolter_platform_" + cfg.Mode
	if cfg.DatabaseName != wantDatabase || cfg.DatabaseRole != wantDatabase {
		clearBytes(cfg.ManualPreviewSigningKey)
		return Config{}, fmt.Errorf("BT_PLATFORM_DATABASE and BT_PLATFORM_DATABASE_ROLE must both be %s", wantDatabase)
	}
	return cfg, nil
}

func (cfg Config) Validate() error {
	if cfg.Mode != "test" && cfg.Mode != "live" {
		return fmt.Errorf("BT_MODE must be test or live")
	}
	if cfg.FundingLiveEnabled && cfg.Mode != "live" {
		return fmt.Errorf("%s is valid only when BT_MODE=live", FundingLiveEnabledEnv)
	}
	if cfg.FundingLiveEnabled {
		maximum, err := decimal.NewFromString(cfg.FundingLiveMaxNotional)
		if err != nil || !maximum.IsPositive() {
			return fmt.Errorf("%s must be a positive decimal when Funding Live is enabled", FundingLiveMaxNotionalEnv)
		}
	}
	if !isLoopback(cfg.BrowserListenAddr) {
		return fmt.Errorf("BT_PLATFORM_BROWSER_LISTEN_ADDR must use a loopback host")
	}
	if !isLoopback(cfg.WorkListenAddr) {
		return fmt.Errorf("BT_PLATFORM_WORK_LISTEN_ADDR must use a loopback host")
	}
	if cfg.BrowserListenAddr == cfg.WorkListenAddr {
		return fmt.Errorf("browser and work listeners must use distinct addresses")
	}
	if strings.TrimSpace(cfg.BootstrapUsername) == "" {
		return fmt.Errorf("BT_BOOTSTRAP_USERNAME is required")
	}
	if _, err := bcrypt.Cost([]byte(cfg.BootstrapPasswordHash)); err != nil {
		return fmt.Errorf("BT_BOOTSTRAP_PASSWORD_HASH must be a bcrypt hash")
	}
	if strings.TrimSpace(cfg.FundingWorkerTokenFile) == "" {
		return fmt.Errorf("%s is required", FundingWorkerTokenFileEnv)
	}
	if allZero(cfg.FundingWorkerTokenDigest[:]) {
		return fmt.Errorf("funding worker token digest is required")
	}
	if strings.TrimSpace(cfg.ManualPreviewSigningKeyFile) == "" || len(cfg.ManualPreviewSigningKey) < sha256.Size {
		return fmt.Errorf("%s must contain at least 32 opaque bytes", ManualPreviewSigningKeyFileEnv)
	}
	if filepath.Base(filepath.Dir(filepath.Clean(cfg.ManualPreviewSigningKeyFile))) != cfg.Mode {
		return fmt.Errorf("%s must be scoped to BT_MODE %s", ManualPreviewSigningKeyFileEnv, cfg.Mode)
	}
	parsedGatewayURL, err := url.Parse(cfg.GatewayURL)
	if err != nil || parsedGatewayURL.Scheme == "" || parsedGatewayURL.Host == "" || (parsedGatewayURL.Scheme != "http" && parsedGatewayURL.Scheme != "https") || parsedGatewayURL.User != nil || parsedGatewayURL.RawQuery != "" || parsedGatewayURL.Fragment != "" {
		return fmt.Errorf("%s must be an http(s) origin without credentials, query, or fragment", GatewayURLEnv)
	}
	if strings.Trim(parsedGatewayURL.Path, "/") != "" {
		return fmt.Errorf("%s must not contain a path", GatewayURLEnv)
	}
	if parsedGatewayURL.Scheme == "http" {
		address := net.ParseIP(parsedGatewayURL.Hostname())
		if address == nil || !address.IsLoopback() {
			return fmt.Errorf("%s permits plaintext HTTP only for an exact loopback IP origin", GatewayURLEnv)
		}
	}
	if strings.TrimSpace(cfg.GatewayTokenFile) == "" || len(cfg.GatewayToken) < 32 {
		return fmt.Errorf("%s must contain at least 32 opaque bytes", GatewayTokenFileEnv)
	}
	if filepath.Base(filepath.Dir(filepath.Clean(cfg.GatewayTokenFile))) != cfg.Mode {
		return fmt.Errorf("%s must be scoped to BT_MODE %s", GatewayTokenFileEnv, cfg.Mode)
	}
	if cfg.LivePrivateWritesEnabled && cfg.Mode != "live" {
		return fmt.Errorf("%s is valid only when BT_MODE=live", LivePrivateWritesEnabledEnv)
	}
	return nil
}

func readProtectedToken(path, envName string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", envName, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s must name a regular non-symlink file", envName)
	}
	if info.Mode().Perm() != 0o600 {
		return nil, fmt.Errorf("%s must have permissions 0600", envName)
	}
	token, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", envName, err)
	}
	token = bytes.TrimSpace(token)
	if len(token) < 32 {
		clearBytes(token)
		return nil, fmt.Errorf("%s must contain at least 32 opaque bytes", envName)
	}
	for _, value := range token {
		if value < 0x21 || value > 0x7e {
			clearBytes(token)
			return nil, fmt.Errorf("%s must contain an opaque ASCII token without whitespace", envName)
		}
	}
	return token, nil
}

func clearBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

func getenv(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func getenvBool(key string) (bool, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return false, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", key)
	}
	return parsed, nil
}

func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func allZero(value []byte) bool {
	var combined byte
	for _, item := range value {
		combined |= item
	}
	return combined == 0
}
