package main

import (
	"bytes"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	platformsystem "github.com/QuantProcessing/boltertrader/internal/system"
)

const (
	modeEnv                = "BT_MODE"
	listenAddrEnv          = "BT_TRADING_LISTEN_ADDR"
	databaseEnv            = "BT_TRADING_DATABASE"
	databaseRoleEnv        = "BT_TRADING_DATABASE_ROLE"
	databaseHostEnv        = "BT_TRADING_DATABASE_HOST"
	serviceTokenFileEnv    = "BT_TRADING_SERVICE_TOKEN_FILE"
	gatewayURLEnv          = "BT_GATEWAY_URL"
	gatewayTokenFileEnv    = "BT_GATEWAY_TOKEN_FILE"
	previewSigningKeyEnv   = "BT_MANUAL_PREVIEW_SIGNING_KEY_FILE"
	livePrivateWritesEnv   = "BT_LIVE_PRIVATE_WRITES_ENABLED"
	inlinePreviewKeyEnv    = "BT_MANUAL_PREVIEW_SIGNING_KEY"
	inlineServiceTokenEnv  = "BT_TRADING_SERVICE_TOKEN"
	inlineGatewayTokenName = "BT_GATEWAY_TOKEN"
)

type config struct {
	Mode                     string
	ListenAddr               string
	DatabaseName             string
	DatabaseRole             string
	DatabaseHost             string
	ServiceToken             []byte
	GatewayURL               string
	GatewayToken             []byte
	ManualPreviewSigningKey  []byte
	LivePrivateWritesEnabled bool
}

// load reads the process's configuration from the environment and refuses to
// return a partially valid one. Every secret arrives as a path to a 0600 file
// rather than a value: an environment variable is readable from a process
// listing and survives in shell history, and there is no version of this
// service where that is an acceptable place to keep a token.
func load() (config, error) {
	for _, name := range []string{inlinePreviewKeyEnv, inlineServiceTokenEnv, inlineGatewayTokenName} {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			return config{}, fmt.Errorf("inline secrets are forbidden; set the corresponding _FILE variable instead of %s", name)
		}
	}
	cfg := config{
		Mode:         strings.TrimSpace(os.Getenv(modeEnv)),
		ListenAddr:   getenv(listenAddrEnv, "127.0.0.1:18092"),
		DatabaseName: strings.TrimSpace(os.Getenv(databaseEnv)),
		DatabaseRole: strings.TrimSpace(os.Getenv(databaseRoleEnv)),
		DatabaseHost: getenv(databaseHostEnv, "127.0.0.1:15432"),
		GatewayURL:   strings.TrimSpace(os.Getenv(gatewayURLEnv)),
	}
	if cfg.Mode != "test" && cfg.Mode != "live" {
		return config{}, fmt.Errorf("%s must be test or live", modeEnv)
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv(livePrivateWritesEnv))) {
	case "", "false":
	case "true":
		cfg.LivePrivateWritesEnabled = true
	default:
		return config{}, fmt.Errorf("%s must be true or false", livePrivateWritesEnv)
	}
	if cfg.LivePrivateWritesEnabled && cfg.Mode != "live" {
		return config{}, fmt.Errorf("%s requires %s=live", livePrivateWritesEnv, modeEnv)
	}
	if !isLoopback(cfg.ListenAddr) {
		return config{}, fmt.Errorf("%s must use a loopback host", listenAddrEnv)
	}
	// The database is named for the mode it holds. A live process pointed at
	// the test database would reconcile live orders against test state, and the
	// name is the only check available before the first write.
	wantDatabase := "bolter_trading_" + cfg.Mode
	if cfg.DatabaseName != wantDatabase || cfg.DatabaseRole != wantDatabase {
		return config{}, fmt.Errorf("%s and %s must both be %s", databaseEnv, databaseRoleEnv, wantDatabase)
	}
	if err := validateGatewayURL(cfg.GatewayURL); err != nil {
		return config{}, err
	}

	secrets := []struct {
		env    string
		target *[]byte
	}{
		{serviceTokenFileEnv, &cfg.ServiceToken},
		{gatewayTokenFileEnv, &cfg.GatewayToken},
		{previewSigningKeyEnv, &cfg.ManualPreviewSigningKey},
	}
	for _, secret := range secrets {
		value, err := readProtectedToken(os.Getenv(secret.env), secret.env, cfg.Mode)
		if err != nil {
			cfg.clear()
			return config{}, err
		}
		*secret.target = value
	}
	return cfg, nil
}

// clear zeroes every secret read so far. A configuration that failed to load
// must not leave key material in a heap this process keeps running on.
func (cfg config) clear() {
	for _, value := range [][]byte{cfg.ServiceToken, cfg.GatewayToken, cfg.ManualPreviewSigningKey} {
		for index := range value {
			value[index] = 0
		}
	}
}

func validateGatewayURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("%s must be an http(s) origin without credentials, query, or fragment", gatewayURLEnv)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("%s must be an http(s) origin without credentials, query, or fragment", gatewayURLEnv)
	}
	if strings.Trim(parsed.Path, "/") != "" {
		return fmt.Errorf("%s must not contain a path", gatewayURLEnv)
	}
	if parsed.Scheme == "http" {
		address := net.ParseIP(parsed.Hostname())
		if address == nil || !address.IsLoopback() {
			return fmt.Errorf("%s permits plaintext HTTP only for a loopback IP origin", gatewayURLEnv)
		}
	}
	return nil
}

// readProtectedToken enforces the same file discipline the auth edge uses: a
// regular 0600 file, scoped to a directory named for the mode, holding opaque
// printable bytes. The mode-scoped path is what stops a live process from
// silently starting with test credentials that happen to be readable.
func readProtectedToken(path, envName, mode string) ([]byte, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("%s is required", envName)
	}
	if filepath.Base(filepath.Dir(filepath.Clean(path))) != mode {
		return nil, fmt.Errorf("%s must be scoped to %s %s", envName, modeEnv, mode)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", envName, err)
	}
	if !info.Mode().IsRegular() {
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

func isLoopback(address string) bool {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	parsed := net.ParseIP(host)
	return parsed != nil && parsed.IsLoopback()
}

func getenv(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func readyComponent() platformsystem.Component {
	return platformsystem.Component{Ready: true, Reason: "ready"}
}
