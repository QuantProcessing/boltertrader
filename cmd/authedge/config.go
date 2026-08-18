package main

import (
	"bytes"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

const (
	listenAddrEnv            = "BT_AUTHEDGE_LISTEN_ADDR"
	databaseEnv              = "BT_AUTHEDGE_DATABASE"
	databaseRoleEnv          = "BT_AUTHEDGE_DATABASE_ROLE"
	databaseHostEnv          = "BT_AUTHEDGE_DATABASE_HOST"
	bootstrapUsernameEnv     = "BT_BOOTSTRAP_USERNAME"
	bootstrapPasswordHashEnv = "BT_BOOTSTRAP_PASSWORD_HASH"
	// The same hash, delivered as a path. See load for why the stack prefers it.
	bootstrapPasswordHashFileEnv = "BT_BOOTSTRAP_PASSWORD_HASH_FILE"

	tradingURLEnv       = "BT_TRADING_URL"
	tradingTokenFileEnv = "BT_TRADING_SERVICE_TOKEN_FILE"
	gatewayURLEnv       = "BT_GATEWAY_URL"
	gatewayTokenFileEnv = "BT_GATEWAY_TOKEN_FILE"
	allowNonLoopbackEnv = "BT_AUTHEDGE_ALLOW_NON_LOOPBACK_LISTEN"
	allowPlaintextEnv   = "BT_AUTHEDGE_ALLOW_NON_LOOPBACK_UPSTREAM"
	secureCookieEnv     = "BT_AUTHEDGE_SECURE_COOKIE"

	inlineTradingTokenEnv    = "BT_TRADING_SERVICE_TOKEN"
	inlineGatewayTokenEnv    = "BT_GATEWAY_TOKEN"
	inlineBootstrapPasswdEnv = "BT_BOOTSTRAP_PASSWORD"
)

type config struct {
	ListenAddr             string
	DatabaseName           string
	DatabaseRole           string
	DatabaseHost           string
	BootstrapUsername      string
	BootstrapPasswordHash  string
	TradingURL             string
	TradingToken           []byte
	GatewayURL             string
	GatewayToken           []byte
	AllowNonLoopbackListen bool
	// One flag for both upstreams. They are reached over the same network by the
	// same process; a deployment where one is loopback and the other is not does
	// not exist, and two flags would only offer a way to half-configure it.
	AllowNonLoopbackUpstream bool
	// SecureCookie marks the session cookie Secure. It defaults off because this
	// process has no TLS of its own — a browser would drop a Secure cookie
	// handed to it over the plaintext http the edge actually speaks, and the
	// failure would look like "login does nothing". Behind a TLS-terminating
	// proxy the flag is correct and the operator is the only one who knows that,
	// the same way they know whether a non-loopback bind faces the network.
	SecureCookie bool
}

// load reads the process's configuration from the environment and refuses to
// return a partially valid one. The two service tokens arrive as paths to 0600
// files rather than as values: an environment variable is readable from a
// process listing and survives in shell history, and these two tokens are the
// whole authority this edge has over the services behind it.
func load() (config, error) {
	for _, name := range []string{inlineTradingTokenEnv, inlineGatewayTokenEnv, inlineBootstrapPasswdEnv} {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			return config{}, fmt.Errorf("inline secrets are forbidden; set the corresponding _FILE or _HASH variable instead of %s", name)
		}
	}
	cfg := config{
		ListenAddr:            getenv(listenAddrEnv, "127.0.0.1:18091"),
		DatabaseName:          strings.TrimSpace(os.Getenv(databaseEnv)),
		DatabaseRole:          strings.TrimSpace(os.Getenv(databaseRoleEnv)),
		DatabaseHost:          getenv(databaseHostEnv, "127.0.0.1:15432"),
		BootstrapUsername:     strings.TrimSpace(os.Getenv(bootstrapUsernameEnv)),
		BootstrapPasswordHash: strings.TrimSpace(os.Getenv(bootstrapPasswordHashEnv)),
		TradingURL:            strings.TrimSpace(os.Getenv(tradingURLEnv)),
		GatewayURL:            strings.TrimSpace(os.Getenv(gatewayURLEnv)),
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
	switch strings.ToLower(strings.TrimSpace(os.Getenv(secureCookieEnv))) {
	case "", "false":
	case "true":
		cfg.SecureCookie = true
	default:
		return config{}, fmt.Errorf("%s must be true or false", secureCookieEnv)
	}
	// This edge speaks plaintext HTTP and hands out a session cookie. On a
	// routable address that cookie is readable by anyone on the path, so the
	// bind is checked before anything else can make the process useful. A
	// container is the one case where the process must bind the interface its
	// runtime routes to; the cookie is still only as exposed as the port
	// mapping the host chooses to publish.
	if err := validateListenAddr(cfg.ListenAddr, cfg.AllowNonLoopbackListen); err != nil {
		return config{}, err
	}
	// The database name is pinned rather than accepted as given. Nothing else
	// checks it, so a typo would authenticate operators against whatever schema
	// happened to answer.
	const wantDatabase = "bolter_authedge"
	if cfg.DatabaseName != wantDatabase || cfg.DatabaseRole != wantDatabase {
		return config{}, fmt.Errorf("%s and %s must both be %s", databaseEnv, databaseRoleEnv, wantDatabase)
	}
	if cfg.BootstrapUsername == "" {
		return config{}, fmt.Errorf("%s is required", bootstrapUsernameEnv)
	}
	// The hash may arrive as a path instead of a value, and in the container stack
	// it does. Both reasons are the ones that already put the two service tokens in
	// files: an environment variable is readable through `docker inspect` and
	// /proc/<pid>/environ by anything that reaches the daemon or the namespace, and
	// a bcrypt hash written into a compose `.env` has to survive that file's own
	// `$` expansion — `$2a$10$…` does not, unless it is quoted exactly right. A
	// path contains neither a secret nor a `$`.
	//
	// Deliberately not defaulted to the file: the variable stays the documented
	// way to run this process by hand.
	hashSource := bootstrapPasswordHashEnv
	if path := strings.TrimSpace(os.Getenv(bootstrapPasswordHashFileEnv)); path != "" {
		// Both set is not a preference to resolve. It is two operators, or one
		// operator twice, and whichever this code picked would be the one nobody
		// checked.
		if cfg.BootstrapPasswordHash != "" {
			return config{}, fmt.Errorf("set %s or %s, not both", bootstrapPasswordHashEnv, bootstrapPasswordHashFileEnv)
		}
		hash, err := readProtectedToken(path, bootstrapPasswordHashFileEnv)
		if err != nil {
			return config{}, err
		}
		cfg.BootstrapPasswordHash = string(hash)
		clearBytes(hash)
		hashSource = bootstrapPasswordHashFileEnv
	}
	// A hash that bcrypt cannot parse is not a weak password, it is a login that
	// can never succeed — and it would only be discovered by an operator locked
	// out at the console.
	if _, err := bcrypt.Cost([]byte(cfg.BootstrapPasswordHash)); err != nil {
		return config{}, fmt.Errorf("%s must be a bcrypt hash", hashSource)
	}
	for _, upstream := range []struct {
		env string
		raw string
	}{{tradingURLEnv, cfg.TradingURL}, {gatewayURLEnv, cfg.GatewayURL}} {
		if err := validateUpstreamURL(upstream.env, upstream.raw, cfg.AllowNonLoopbackUpstream); err != nil {
			return config{}, err
		}
	}

	secrets := []struct {
		env    string
		target *[]byte
	}{
		{tradingTokenFileEnv, &cfg.TradingToken},
		{gatewayTokenFileEnv, &cfg.GatewayToken},
	}
	for _, secret := range secrets {
		value, err := readProtectedToken(os.Getenv(secret.env), secret.env)
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
	for _, value := range [][]byte{cfg.TradingToken, cfg.GatewayToken} {
		clearBytes(value)
	}
}

func validateUpstreamURL(envName, rawURL string, allowNonLoopback bool) error {
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
	// A service token sent in cleartext to a routable host is a token anyone on
	// the path can replay against the service it authorises. The opt-in exists
	// for the container stack, where these two upstreams are DNS names on a
	// bridge network the operator created: nothing this process can observe
	// distinguishes that bridge from the open internet, so an operator's
	// assertion is the only honest mechanism — and its honest cost is that the
	// same flag also permits http://evil.example.com.
	if parsed.Scheme == "http" && !allowNonLoopback {
		address := net.ParseIP(parsed.Hostname())
		if address == nil || !address.IsLoopback() {
			return fmt.Errorf("%s permits plaintext HTTP only for a loopback IP origin unless %s=true", envName, allowPlaintextEnv)
		}
	}
	return nil
}

// readProtectedToken enforces the same file discipline the trading service
// uses: a regular 0600 file holding opaque printable bytes.
//
// The path used to have to sit in a directory named for the mode as well, which
// is what stopped a live process from starting on test credentials that
// happened to be readable. There is one set of credentials now and nothing for
// that check to discriminate between, so what is left is the part that was
// always doing the real work: a secret this process will spend must be a file
// only its owner can read, never a value someone can pull out of a process
// listing.
func readProtectedToken(path, envName string) ([]byte, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("%s is required", envName)
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
