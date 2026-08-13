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
	modeEnv             = "BT_MODE"
	listenAddrEnv       = "BT_TRADING_LISTEN_ADDR"
	databaseEnv         = "BT_TRADING_DATABASE"
	databaseRoleEnv     = "BT_TRADING_DATABASE_ROLE"
	databaseHostEnv     = "BT_TRADING_DATABASE_HOST"
	serviceTokenFileEnv = "BT_TRADING_SERVICE_TOKEN_FILE"
	gatewayURLEnv       = "BT_GATEWAY_URL"
	gatewayTokenFileEnv = "BT_GATEWAY_TOKEN_FILE"
	// The Gateway answers on two listeners with two tokens: /v1 is the
	// capability plane this service uses for orders and fills, /internal/v1 is
	// the authority plane it uses for the catalog, runtime accounts, market-data
	// routing and its own readiness probe. Both variables are optional and
	// default to the capability plane's, which is the correct configuration for
	// any deployment that serves both paths from one listener.
	gatewayInternalURLEnv       = "BT_GATEWAY_INTERNAL_URL"
	gatewayInternalTokenFileEnv = "BT_GATEWAY_INTERNAL_TOKEN_FILE"
	previewSigningKeyEnv        = "BT_MANUAL_PREVIEW_SIGNING_KEY_FILE"
	livePrivateWritesEnv        = "BT_LIVE_PRIVATE_WRITES_ENABLED"
	allowNonLoopbackEnv         = "BT_TRADING_ALLOW_NON_LOOPBACK_LISTEN"
	allowPlaintextEnv           = "BT_TRADING_ALLOW_NON_LOOPBACK_UPSTREAM"
	inlinePreviewKeyEnv         = "BT_MANUAL_PREVIEW_SIGNING_KEY"
	inlineServiceTokenEnv       = "BT_TRADING_SERVICE_TOKEN"
	inlineGatewayTokenName      = "BT_GATEWAY_TOKEN"

	inlineGatewayInternalTokenName = "BT_GATEWAY_INTERNAL_TOKEN"
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
	GatewayInternalURL       string
	GatewayInternalToken     []byte
	ManualPreviewSigningKey  []byte
	LivePrivateWritesEnabled bool
	AllowNonLoopbackListen   bool
	AllowNonLoopbackUpstream bool
}

// load reads the process's configuration from the environment and refuses to
// return a partially valid one. Every secret arrives as a path to a 0600 file
// rather than a value: an environment variable is readable from a process
// listing and survives in shell history, and there is no version of this
// service where that is an acceptable place to keep a token.
func load() (config, error) {
	for _, name := range []string{inlinePreviewKeyEnv, inlineServiceTokenEnv, inlineGatewayTokenName, inlineGatewayInternalTokenName} {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			return config{}, fmt.Errorf("inline secrets are forbidden; set the corresponding _FILE variable instead of %s", name)
		}
	}
	cfg := config{
		Mode:               strings.TrimSpace(os.Getenv(modeEnv)),
		ListenAddr:         getenv(listenAddrEnv, "127.0.0.1:18092"),
		DatabaseName:       strings.TrimSpace(os.Getenv(databaseEnv)),
		DatabaseRole:       strings.TrimSpace(os.Getenv(databaseRoleEnv)),
		DatabaseHost:       getenv(databaseHostEnv, "127.0.0.1:15432"),
		GatewayURL:         strings.TrimSpace(os.Getenv(gatewayURLEnv)),
		GatewayInternalURL: strings.TrimSpace(os.Getenv(gatewayInternalURLEnv)),
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
	switch strings.ToLower(strings.TrimSpace(os.Getenv(allowNonLoopbackEnv))) {
	case "", "false":
	case "true":
		cfg.AllowNonLoopbackListen = true
	default:
		return config{}, fmt.Errorf("%s must be true or false", allowNonLoopbackEnv)
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv(allowPlaintextEnv))) {
	case "", "false":
	case "true":
		cfg.AllowNonLoopbackUpstream = true
	default:
		return config{}, fmt.Errorf("%s must be true or false", allowPlaintextEnv)
	}
	// A routable bind exposes the service token plane to anyone who can reach
	// the port, so it stays off unless the operator says otherwise. The one
	// case that needs it is a container, where the port is reachable only
	// through a mapping the host controls; there the process must bind the
	// interface the runtime routes to, not loopback inside its own namespace.
	if err := validateListenAddr(cfg.ListenAddr, cfg.AllowNonLoopbackListen); err != nil {
		return config{}, err
	}
	// The database is named for the mode it holds. A live process pointed at
	// the test database would reconcile live orders against test state, and the
	// name is the only check available before the first write.
	wantDatabase := "bolter_trading_" + cfg.Mode
	if cfg.DatabaseName != wantDatabase || cfg.DatabaseRole != wantDatabase {
		return config{}, fmt.Errorf("%s and %s must both be %s", databaseEnv, databaseRoleEnv, wantDatabase)
	}
	if err := validateGatewayURL(cfg.GatewayURL, gatewayURLEnv, cfg.AllowNonLoopbackUpstream); err != nil {
		return config{}, err
	}
	// The authority plane defaults to the capability plane. That is a real
	// configuration rather than a placeholder: a Gateway that serves /v1 and
	// /internal/v1 from one listener behind one token is what a single-binary
	// deployment is, and what every in-process test fixture is. The compose
	// stack sets both variables because there the planes are two listeners.
	//
	// Setting one without the other is refused rather than half-applied. The
	// tempting fallback — take the new origin, keep the old token — would send
	// the capability plane's credential to the routes that create accounts and
	// rotate venue credentials, which is precisely the conflation the Gateway
	// runs two listeners to prevent.
	internalTokenFile := strings.TrimSpace(os.Getenv(gatewayInternalTokenFileEnv))
	if (cfg.GatewayInternalURL == "") != (internalTokenFile == "") {
		return config{}, fmt.Errorf("%s and %s must be set together", gatewayInternalURLEnv, gatewayInternalTokenFileEnv)
	}
	if cfg.GatewayInternalURL == "" {
		cfg.GatewayInternalURL = cfg.GatewayURL
	} else if err := validateGatewayURL(cfg.GatewayInternalURL, gatewayInternalURLEnv, cfg.AllowNonLoopbackUpstream); err != nil {
		return config{}, err
	}

	type secretFile struct {
		env    string
		target *[]byte
	}
	secrets := []secretFile{
		{serviceTokenFileEnv, &cfg.ServiceToken},
		{gatewayTokenFileEnv, &cfg.GatewayToken},
		{previewSigningKeyEnv, &cfg.ManualPreviewSigningKey},
	}
	if internalTokenFile != "" {
		secrets = append(secrets, secretFile{gatewayInternalTokenFileEnv, &cfg.GatewayInternalToken})
	}
	for _, secret := range secrets {
		value, err := readProtectedToken(os.Getenv(secret.env), secret.env, cfg.Mode)
		if err != nil {
			cfg.clear()
			return config{}, err
		}
		*secret.target = value
	}
	// Aliasing rather than copying, so clear() zeroes one buffer twice instead
	// of missing a second copy of the same secret.
	if cfg.GatewayInternalToken == nil {
		cfg.GatewayInternalToken = cfg.GatewayToken
	}
	return cfg, nil
}

// clear zeroes every secret read so far. A configuration that failed to load
// must not leave key material in a heap this process keeps running on.
func (cfg config) clear() {
	for _, value := range [][]byte{cfg.ServiceToken, cfg.GatewayToken, cfg.GatewayInternalToken, cfg.ManualPreviewSigningKey} {
		for index := range value {
			value[index] = 0
		}
	}
}

// validateGatewayURL answers two separate questions about the origin this
// process dials, in the same shape and for the same reason as validateListenAddr
// above: whether the string is an origin at all, and whether a plaintext one may
// face something other than loopback. The second is a policy the operator sets,
// because the token this client carries is the whole authority it has over the
// Gateway, and in cleartext to a routable host it is a token anyone on the path
// can replay.
//
// The opt-in exists for the container stack, where services address each other
// by DNS name on a bridge network the operator created. No check inside this
// process can tell that bridge from the open internet, so the honest mechanism
// is an operator's assertion — and the honest cost is that the same flag also
// permits http://evil.example.com. That is why it is off by default and named
// for what it allows rather than for the deployment that needs it.
//
// envName is a parameter because two variables carry Gateway origins and the
// error has to name the one the operator set wrong; a message naming the other
// sends them to edit a line that was already correct.
func validateGatewayURL(rawURL, envName string, allowNonLoopback bool) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("%s must be an http(s) origin without credentials, query, or fragment", envName)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("%s must be an http(s) origin without credentials, query, or fragment", envName)
	}
	if strings.Trim(parsed.Path, "/") != "" {
		return fmt.Errorf("%s must not contain a path", envName)
	}
	if parsed.Scheme == "http" && !allowNonLoopback {
		address := net.ParseIP(parsed.Hostname())
		if address == nil || !address.IsLoopback() {
			return fmt.Errorf("%s permits plaintext HTTP only for a loopback IP origin unless %s=true", envName, allowPlaintextEnv)
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

// validateListenAddr answers two separate questions about the bind: whether the
// address can be listened on at all, which is a property of the string, and
// whether it may face the network, which is a policy the operator sets. They
// are checked in that order and never folded together — the opt-in widens the
// host it will accept, it is not a reason to stop parsing the address.
func validateListenAddr(address string, allowNonLoopback bool) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port == "" {
		return fmt.Errorf("%s must be a host:port address", listenAddrEnv)
	}
	if allowNonLoopback {
		return nil
	}
	if host == "localhost" {
		return nil
	}
	if parsed := net.ParseIP(host); parsed != nil && parsed.IsLoopback() {
		return nil
	}
	return fmt.Errorf("%s must use a loopback host unless %s=true", listenAddrEnv, allowNonLoopbackEnv)
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
