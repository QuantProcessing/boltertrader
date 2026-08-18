package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeToken(t *testing.T, dir, name string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(strings.Repeat("t", 40)), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func validEnv(t *testing.T) map[string]string {
	t.Helper()
	dir := t.TempDir()
	return map[string]string{
		listenAddrEnv:        "127.0.0.1:18092",
		databaseEnv:          "bolter_trading",
		databaseRoleEnv:      "bolter_trading",
		gatewayURLEnv:        "http://127.0.0.1:18080",
		serviceTokenFileEnv:  writeToken(t, dir, "service.token", 0o600),
		gatewayTokenFileEnv:  writeToken(t, dir, "gateway.token", 0o600),
		previewSigningKeyEnv: writeToken(t, dir, "preview.key", 0o600),
	}
}

func applyEnv(t *testing.T, env map[string]string) {
	t.Helper()
	for _, name := range []string{listenAddrEnv, databaseEnv, databaseRoleEnv, databaseHostEnv, gatewayURLEnv, serviceTokenFileEnv, gatewayTokenFileEnv, previewSigningKeyEnv, livePrivateWritesEnv, allowNonLoopbackEnv, allowPlaintextEnv, inlinePreviewKeyEnv, inlineServiceTokenEnv, inlineGatewayTokenName} {
		t.Setenv(name, "")
		os.Unsetenv(name)
	}
	for name, value := range env {
		t.Setenv(name, value)
	}
}

func TestLoadAcceptsAValidEnvironment(t *testing.T) {
	applyEnv(t, validEnv(t))
	cfg, err := load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.ServiceToken) < 32 || len(cfg.GatewayToken) < 32 || len(cfg.ManualPreviewSigningKey) < 32 {
		t.Fatal("secrets were not loaded")
	}
	if cfg.DatabaseHost != "127.0.0.1:15432" {
		t.Fatalf("database host=%q", cfg.DatabaseHost)
	}
}

// Nothing in this process authenticates a browser, so a routable listener would
// publish the trading surface to anyone who could reach the host. It stays
// refused unless the operator opts in by name.
func TestLoadRequiresALoopbackListener(t *testing.T) {
	for _, address := range []string{"0.0.0.0:18092", "192.168.1.10:18092", ":18092", "127.0.0.1"} {
		env := validEnv(t)
		env[listenAddrEnv] = address
		applyEnv(t, env)
		if _, err := load(); err == nil || !strings.Contains(err.Error(), listenAddrEnv) {
			t.Fatalf("address %q accepted: %v", address, err)
		}
	}
}

// A container reaches its service through a port mapping the host controls, so
// the process must bind the interface its runtime routes to rather than the
// loopback of its own network namespace.
func TestLoadAllowsARoutableListenerOnlyWhenOptedIn(t *testing.T) {
	env := validEnv(t)
	env[listenAddrEnv] = "0.0.0.0:18092"
	env[allowNonLoopbackEnv] = "true"
	applyEnv(t, env)
	cfg, err := load()
	if err != nil {
		t.Fatalf("opted-in routable bind rejected: %v", err)
	}
	if !cfg.AllowNonLoopbackListen || cfg.ListenAddr != "0.0.0.0:18092" {
		t.Fatalf("opt-in not recorded: %+v", cfg.AllowNonLoopbackListen)
	}

	// A malformed address is still malformed; the opt-in widens the host, it
	// does not excuse a missing port.
	env = validEnv(t)
	env[listenAddrEnv] = "127.0.0.1"
	env[allowNonLoopbackEnv] = "true"
	applyEnv(t, env)
	if _, err := load(); err == nil || !strings.Contains(err.Error(), listenAddrEnv) {
		t.Fatalf("address without a port accepted under the opt-in: %v", err)
	}

	// "yes" is the shape of an operator who believes they have opted in.
	// Reading it as false would bind loopback while they expect a routable
	// listener; reading it as true would widen the bind on a typo.
	env = validEnv(t)
	env[allowNonLoopbackEnv] = "yes"
	applyEnv(t, env)
	if _, err := load(); err == nil || !strings.Contains(err.Error(), allowNonLoopbackEnv) {
		t.Fatalf("ambiguous opt-in accepted: %v", err)
	}
}

// The Gateway token travels on every upstream request, so a plaintext origin
// that is not loopback puts it in front of anyone on the path. The listen opt-in
// must not be what unlocks that — they are separate exposures and an operator
// who wanted a routable bind did not thereby ask to trust the network they dial.
func TestLoadRequiresALoopbackGatewayOriginForPlaintext(t *testing.T) {
	for _, rawURL := range []string{"http://gateway:18080", "http://10.0.0.4:18080", "http://evil.example.com"} {
		env := validEnv(t)
		env[gatewayURLEnv] = rawURL
		env[allowNonLoopbackEnv] = "true"
		applyEnv(t, env)
		if _, err := load(); err == nil || !strings.Contains(err.Error(), gatewayURLEnv) {
			t.Fatalf("plaintext origin %q accepted: %v", rawURL, err)
		}
	}

	// TLS carries its own protection for the token, so the hostname is not this
	// check's business.
	env := validEnv(t)
	env[gatewayURLEnv] = "https://gateway.internal:18080"
	applyEnv(t, env)
	if _, err := load(); err != nil {
		t.Fatalf("https origin rejected: %v", err)
	}
}

// Services on a compose bridge address each other by DNS name, and no check
// inside this process can tell that bridge from the open internet — so the
// operator asserts it. The cost is that the same flag admits any routable
// origin, which is why it is off by default and never inferred.
func TestLoadAllowsAPlaintextNonLoopbackGatewayOnlyWhenOptedIn(t *testing.T) {
	env := validEnv(t)
	env[gatewayURLEnv] = "http://gateway:18080"
	env[allowPlaintextEnv] = "true"
	applyEnv(t, env)
	cfg, err := load()
	if err != nil {
		t.Fatalf("opted-in plaintext upstream rejected: %v", err)
	}
	if !cfg.AllowNonLoopbackUpstream || cfg.GatewayURL != "http://gateway:18080" {
		t.Fatalf("opt-in not recorded: %+v", cfg.AllowNonLoopbackUpstream)
	}

	// The opt-in widens the host the origin may name. It does not turn a path,
	// a query, or embedded credentials into an origin.
	for _, rawURL := range []string{"http://gateway:18080/v1", "http://user:pw@gateway:18080", "ftp://gateway:18080"} {
		env = validEnv(t)
		env[gatewayURLEnv] = rawURL
		env[allowPlaintextEnv] = "true"
		applyEnv(t, env)
		if _, err := load(); err == nil || !strings.Contains(err.Error(), gatewayURLEnv) {
			t.Fatalf("%q accepted under the opt-in: %v", rawURL, err)
		}
	}

	// "yes" is the shape of an operator who believes they have opted in. Reading
	// it as false fails the stack they expected to work; reading it as true
	// widens the trust boundary on a typo.
	env = validEnv(t)
	env[allowPlaintextEnv] = "yes"
	applyEnv(t, env)
	if _, err := load(); err == nil || !strings.Contains(err.Error(), allowPlaintextEnv) {
		t.Fatalf("ambiguous opt-in accepted: %v", err)
	}
}

// An environment variable is readable from a process listing and survives in
// shell history. Accepting one would make every other precaution here decorative.
func TestLoadRefusesInlineSecrets(t *testing.T) {
	for _, name := range []string{inlinePreviewKeyEnv, inlineServiceTokenEnv, inlineGatewayTokenName} {
		env := validEnv(t)
		env[name] = strings.Repeat("t", 40)
		applyEnv(t, env)
		if _, err := load(); err == nil || !strings.Contains(err.Error(), "inline secrets are forbidden") {
			t.Fatalf("%s accepted: %v", name, err)
		}
	}
}

func TestLoadRequiresProtectedSecretFiles(t *testing.T) {
	dir := t.TempDir()
	env := validEnv(t)
	env[serviceTokenFileEnv] = writeToken(t, dir, "world-readable.token", 0o644)
	applyEnv(t, env)
	if _, err := load(); err == nil || !strings.Contains(err.Error(), "0600") {
		t.Fatalf("world-readable token accepted: %v", err)
	}

	// A symlink is the way around every other check here: the mode bits that get
	// inspected are the link's, not the target's, and the operator who pointed
	// one at a 0644 file would see a process that started cleanly. Lstat plus a
	// regular-file requirement is what makes the permission check mean anything.
	//
	// This case used to assert something else — that a token outside a directory
	// named for the mode was refused, which is what stopped a live process from
	// starting on test credentials. There is one set of credentials now.
	linked := filepath.Join(t.TempDir(), "service.token")
	if err := os.Symlink(writeToken(t, dir, "target.token", 0o600), linked); err != nil {
		t.Fatal(err)
	}
	env = validEnv(t)
	env[serviceTokenFileEnv] = linked
	applyEnv(t, env)
	if _, err := load(); err == nil || !strings.Contains(err.Error(), "regular non-symlink file") {
		t.Fatalf("symlinked token accepted: %v", err)
	}
}

// The database name is pinned rather than accepted as given, and it is the only
// check available before the first write.
func TestLoadRequiresTheTradingDatabase(t *testing.T) {
	for _, name := range []string{"bolter_trading_test", "bolter_platform", ""} {
		env := validEnv(t)
		env[databaseEnv] = name
		applyEnv(t, env)
		if _, err := load(); err == nil || !strings.Contains(err.Error(), "bolter_trading") {
			t.Fatalf("database %q accepted: %v", name, err)
		}
	}
}

// The flag is the only thing standing between this process and an order that
// spends real money, so an operator who misspells their intent has to be told.
// Reading "yes" as false would leave them believing writes were armed; reading
// it as true would arm them on a word the switch never accepted.
func TestLoadRejectsAnAmbiguousLiveWriteFlag(t *testing.T) {
	env := validEnv(t)
	env[livePrivateWritesEnv] = "yes"
	applyEnv(t, env)
	if _, err := load(); err == nil || !strings.Contains(err.Error(), "must be true or false") {
		t.Fatalf("ambiguous flag accepted: %v", err)
	}
}

func TestLoadRejectsUnsafeGatewayOrigins(t *testing.T) {
	for _, origin := range []string{"http://gateway.internal:18080", "ftp://127.0.0.1", "http://user:pass@127.0.0.1:18080", "http://127.0.0.1:18080/v1", "http://127.0.0.1:18080?x=1", ""} {
		env := validEnv(t)
		env[gatewayURLEnv] = origin
		applyEnv(t, env)
		if _, err := load(); err == nil || !strings.Contains(err.Error(), gatewayURLEnv) {
			t.Fatalf("origin %q accepted: %v", origin, err)
		}
	}
}
