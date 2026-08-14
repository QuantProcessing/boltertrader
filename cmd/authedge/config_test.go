package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testBootstrapHash is a bcrypt hash of an arbitrary password. Only its
// parseability is ever checked, and it is shared so the file-backed and
// variable-backed paths are proven against the same bytes.
const testBootstrapHash = "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"

// writeModeScopedToken writes an opaque token into a directory named for the
// mode, which is the layout readProtectedToken insists on.
func writeModeScopedToken(t *testing.T, mode, name string) string {
	t.Helper()
	return writeModeScopedFile(t, mode, name, strings.Repeat("a", 48))
}

// writeModeScopedFile is the same layout with the contents chosen by the caller.
func writeModeScopedFile(t *testing.T, mode, name, contents string) string {
	t.Helper()
	directory := filepath.Join(t.TempDir(), mode)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatalf("create token directory: %v", err)
	}
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	return path
}

// setValidAuthEdgeEnv establishes an otherwise-loadable configuration so each
// test can vary exactly one variable and attribute the failure to it.
func setValidAuthEdgeEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{inlineTradingTokenEnv, inlineGatewayTokenEnv, inlineBootstrapPasswdEnv} {
		t.Setenv(name, "")
	}
	t.Setenv(modeEnv, "test")
	t.Setenv(databaseEnv, "bolter_authedge_test")
	t.Setenv(databaseRoleEnv, "bolter_authedge_test")
	t.Setenv(bootstrapUsernameEnv, "operator")
	t.Setenv(bootstrapPasswordHashEnv, testBootstrapHash)
	t.Setenv(bootstrapPasswordHashFileEnv, "")
	t.Setenv(tradingURLEnv, "http://127.0.0.1:18092")
	t.Setenv(gatewayURLEnv, "http://127.0.0.1:18080")
	t.Setenv(tradingTokenFileEnv, writeModeScopedToken(t, "test", "trading.token"))
	t.Setenv(gatewayTokenFileEnv, writeModeScopedToken(t, "test", "gateway.token"))
	t.Setenv(allowNonLoopbackEnv, "")
	t.Setenv(allowPlaintextEnv, "")
}

func TestAuthEdgeRejectsNonLoopbackListenByDefault(t *testing.T) {
	setValidAuthEdgeEnv(t)
	t.Setenv(listenAddrEnv, "0.0.0.0:18091")

	_, err := load()
	if err == nil {
		t.Fatal("expected a non-loopback bind to be rejected when the escape hatch is unset")
	}
	if !strings.Contains(err.Error(), listenAddrEnv) || !strings.Contains(err.Error(), allowNonLoopbackEnv) {
		t.Fatalf("error must name both the bind and the opt-in variable, got: %v", err)
	}
}

func TestAuthEdgeAllowsNonLoopbackListenWhenOptedIn(t *testing.T) {
	setValidAuthEdgeEnv(t)
	t.Setenv(listenAddrEnv, "0.0.0.0:18091")
	t.Setenv(allowNonLoopbackEnv, "true")

	cfg, err := load()
	if err != nil {
		t.Fatalf("expected the opt-in to permit a routable bind: %v", err)
	}
	defer cfg.clear()
	if !cfg.AllowNonLoopbackListen {
		t.Fatal("expected AllowNonLoopbackListen to be recorded on the config")
	}
	if cfg.ListenAddr != "0.0.0.0:18091" {
		t.Fatalf("unexpected listen address: %q", cfg.ListenAddr)
	}
}

func TestAuthEdgeRejectsNonBooleanEscapeHatch(t *testing.T) {
	setValidAuthEdgeEnv(t)
	t.Setenv(listenAddrEnv, "127.0.0.1:18091")
	t.Setenv(allowNonLoopbackEnv, "yes")

	// "yes" is the shape of an operator who believes they have opted in. Taking
	// it as false would bind loopback while they expect a routable listener;
	// taking it as true would widen the bind on a typo.
	_, err := load()
	if err == nil {
		t.Fatal("expected a non-boolean escape hatch to be rejected")
	}
	if !strings.Contains(err.Error(), allowNonLoopbackEnv) {
		t.Fatalf("error must name the offending variable, got: %v", err)
	}
}

func TestAuthEdgeLoopbackListenNeedsNoOptIn(t *testing.T) {
	setValidAuthEdgeEnv(t)
	t.Setenv(listenAddrEnv, "127.0.0.1:18091")

	cfg, err := load()
	if err != nil {
		t.Fatalf("expected the default loopback bind to load: %v", err)
	}
	defer cfg.clear()
	if cfg.AllowNonLoopbackListen {
		t.Fatal("expected AllowNonLoopbackListen to stay false when unset")
	}
}

// The opt-in widens which host may be bound; it does not excuse an address the
// process could never listen on. Folding the two checks into one condition
// silently accepted a portless address whenever the hatch was open, so both
// sides of the hatch are pinned here.
func TestAuthEdgeRejectsAMalformedListenAddrOnEitherSideOfTheOptIn(t *testing.T) {
	for _, optIn := range []string{"", "true"} {
		for _, address := range []string{"127.0.0.1", "0.0.0.0", "127.0.0.1:", "[::1]"} {
			setValidAuthEdgeEnv(t)
			t.Setenv(listenAddrEnv, address)
			t.Setenv(allowNonLoopbackEnv, optIn)

			_, err := load()
			if err == nil || !strings.Contains(err.Error(), listenAddrEnv) {
				t.Fatalf("address %q accepted with %s=%q: %v", address, allowNonLoopbackEnv, optIn, err)
			}
		}
	}
}

// Two service tokens leave this process on every forwarded request. In plaintext
// to a host that is not loopback they are readable by anyone on the path, so the
// refusal covers both upstreams and is not unlocked by the listen opt-in — an
// operator who bound a routable interface did not thereby ask to trust the
// network they dial.
func TestAuthEdgeRejectsNonLoopbackPlaintextUpstreamsByDefault(t *testing.T) {
	for _, upstream := range []struct{ env, rawURL string }{
		{tradingURLEnv, "http://trading:18092"},
		{gatewayURLEnv, "http://gateway:18080"},
		{tradingURLEnv, "http://10.0.0.4:18092"},
		{gatewayURLEnv, "http://evil.example.com"},
	} {
		setValidAuthEdgeEnv(t)
		t.Setenv(listenAddrEnv, "127.0.0.1:18091")
		t.Setenv(upstream.env, upstream.rawURL)
		t.Setenv(allowNonLoopbackEnv, "true")

		_, err := load()
		if err == nil || !strings.Contains(err.Error(), upstream.env) {
			t.Fatalf("%s=%q accepted: %v", upstream.env, upstream.rawURL, err)
		}
		if !strings.Contains(err.Error(), allowPlaintextEnv) {
			t.Fatalf("error must name the opt-in that would permit it, got: %v", err)
		}
	}
}

// Both upstreams are DNS names on the compose bridge, and nothing this process
// can observe distinguishes that bridge from the open internet — so the operator
// asserts it, for both at once. The honest cost, pinned by the evil.example.com
// case, is that the same flag admits any routable origin.
func TestAuthEdgeAllowsNonLoopbackPlaintextUpstreamsWhenOptedIn(t *testing.T) {
	setValidAuthEdgeEnv(t)
	t.Setenv(listenAddrEnv, "127.0.0.1:18091")
	t.Setenv(tradingURLEnv, "http://trading:18092")
	t.Setenv(gatewayURLEnv, "http://gateway:18080")
	t.Setenv(allowPlaintextEnv, "true")

	cfg, err := load()
	if err != nil {
		t.Fatalf("opted-in plaintext upstreams rejected: %v", err)
	}
	defer cfg.clear()
	if !cfg.AllowNonLoopbackUpstream {
		t.Fatal("expected AllowNonLoopbackUpstream to be recorded on the config")
	}
	if cfg.TradingURL != "http://trading:18092" || cfg.GatewayURL != "http://gateway:18080" {
		t.Fatalf("unexpected upstreams: %q %q", cfg.TradingURL, cfg.GatewayURL)
	}

	// The opt-in widens the host an origin may name. It does not make a path, a
	// query, or embedded credentials into an origin.
	for _, rawURL := range []string{"http://trading:18092/api", "http://user:pw@trading:18092", "ftp://trading:18092"} {
		setValidAuthEdgeEnv(t)
		t.Setenv(listenAddrEnv, "127.0.0.1:18091")
		t.Setenv(tradingURLEnv, rawURL)
		t.Setenv(allowPlaintextEnv, "true")

		if _, err := load(); err == nil || !strings.Contains(err.Error(), tradingURLEnv) {
			t.Fatalf("%q accepted under the opt-in: %v", rawURL, err)
		}
	}

	// "yes" is the shape of an operator who believes they have opted in, and this
	// opt-in governs where two service tokens may travel in cleartext.
	setValidAuthEdgeEnv(t)
	t.Setenv(listenAddrEnv, "127.0.0.1:18091")
	t.Setenv(allowPlaintextEnv, "yes")
	if _, err := load(); err == nil || !strings.Contains(err.Error(), allowPlaintextEnv) {
		t.Fatalf("ambiguous opt-in accepted: %v", err)
	}
}

// TLS carries its own protection for the tokens, so the hostname is not this
// check's business and needs no opt-in.
func TestAuthEdgeAcceptsTLSUpstreamsWithoutAnOptIn(t *testing.T) {
	setValidAuthEdgeEnv(t)
	t.Setenv(listenAddrEnv, "127.0.0.1:18091")
	t.Setenv(tradingURLEnv, "https://trading.internal")
	t.Setenv(gatewayURLEnv, "https://gateway.internal")

	cfg, err := load()
	if err != nil {
		t.Fatalf("https upstreams rejected: %v", err)
	}
	defer cfg.clear()
	if cfg.AllowNonLoopbackUpstream {
		t.Fatal("expected AllowNonLoopbackUpstream to stay false when unset")
	}
}

// The compose stack delivers the hash as a file, so this path is the one the
// MVP loop actually authenticates through — the variable is the manual-run
// fallback. Both must produce the same config.
func TestAuthEdgeLoadsBootstrapHashFromAFile(t *testing.T) {
	setValidAuthEdgeEnv(t)
	t.Setenv(bootstrapPasswordHashEnv, "")
	t.Setenv(bootstrapPasswordHashFileEnv, writeModeScopedFile(t, "test", "bootstrap_password_hash", testBootstrapHash))

	cfg, err := load()
	if err != nil {
		t.Fatalf("expected a file-backed bootstrap hash to load: %v", err)
	}
	defer cfg.clear()
	if cfg.BootstrapPasswordHash != testBootstrapHash {
		t.Fatalf("hash read from file = %q, want %q", cfg.BootstrapPasswordHash, testBootstrapHash)
	}
}

func TestAuthEdgeRejectsBootstrapHashFromBothSources(t *testing.T) {
	setValidAuthEdgeEnv(t)
	t.Setenv(bootstrapPasswordHashFileEnv, writeModeScopedFile(t, "test", "bootstrap_password_hash", testBootstrapHash))

	// Same value in both places here, so the failure cannot be blamed on a
	// mismatch: it is the ambiguity itself that is refused, because the next
	// operator to edit one of the two would have no way to tell which one wins.
	_, err := load()
	if err == nil {
		t.Fatal("expected two sources for one hash to be rejected")
	}
	if !strings.Contains(err.Error(), bootstrapPasswordHashEnv) || !strings.Contains(err.Error(), bootstrapPasswordHashFileEnv) {
		t.Fatalf("error must name both sources, got: %v", err)
	}
}

// The file gets the same treatment as the two service tokens: 0600, and parented
// by a directory named for the mode. A hash readable by every process on the
// host is a hash worth offline-cracking, and a test hash reachable from a live
// process is an operator authenticated against the wrong identity store.
func TestAuthEdgeRejectsAnUnprotectedBootstrapHashFile(t *testing.T) {
	setValidAuthEdgeEnv(t)
	t.Setenv(bootstrapPasswordHashEnv, "")

	loose := filepath.Join(t.TempDir(), "bootstrap_password_hash")
	if err := os.WriteFile(loose, []byte(testBootstrapHash), 0o644); err != nil {
		t.Fatalf("write hash: %v", err)
	}
	t.Setenv(bootstrapPasswordHashFileEnv, loose)

	_, err := load()
	if err == nil || !strings.Contains(err.Error(), bootstrapPasswordHashFileEnv) {
		t.Fatalf("expected the unscoped 0644 hash file to be rejected by name, got: %v", err)
	}
}

// The bcrypt check is shared by both sources, so its message has to follow the
// one in use. Naming the variable while the operator set the file sends them to
// edit something that is empty.
func TestAuthEdgeNamesTheFileWhenItsContentsAreNotABcryptHash(t *testing.T) {
	setValidAuthEdgeEnv(t)
	t.Setenv(bootstrapPasswordHashEnv, "")
	// Long enough and printable enough to clear readProtectedToken, so the only
	// thing left to reject it is bcrypt.
	t.Setenv(bootstrapPasswordHashFileEnv, writeModeScopedFile(t, "test", "bootstrap_password_hash", strings.Repeat("z", 60)))

	_, err := load()
	if err == nil || !strings.Contains(err.Error(), bootstrapPasswordHashFileEnv) {
		t.Fatalf("expected the hash file to be named as the bad source, got: %v", err)
	}
}
