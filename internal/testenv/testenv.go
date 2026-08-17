// Package testenv holds the gates that keep the default test path offline.
//
// Every helper here is a skip, not a setup: it decides whether a test that
// would reach a real venue is allowed to run at all. Nothing in this package
// constructs a client, and nothing here knows a venue's endpoints.
//
// The package used to be much larger. It carried per-venue demo and testnet
// configuration — credential names, notional bounds, symbol selectors, and
// endpoint profiles for nine venues. Those environments were removed because
// each venue implemented them differently, and the configuration went with
// them. What survives is the small live-only core: a repo .env loader that
// refuses to import execution gates, and five skip helpers.
package testenv

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joho/godotenv"
)

// LoadRepoEnv loads the repo-root .env into the current process without
// overriding shell-exported environment variables. Execution gates are never
// imported from the file: live reads, writes, and extended test modes must be
// enabled explicitly in the process environment.
//
// This asymmetry is the point. Credentials may sit in a file, because a file
// is where a credential belongs. Permission to spend them may not, because a
// checked-out file would then decide what a test run does.
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
		if isRepoEnvExecutionGate(key) {
			continue
		}
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

func isRepoEnvExecutionGate(key string) bool {
	key = strings.ToUpper(strings.TrimSpace(key))
	if strings.HasPrefix(key, "RUN_") || strings.HasSuffix(key, "_REALTIME_WS") {
		return true
	}
	return strings.HasPrefix(key, "ENABLE_") || strings.Contains(key, "_ENABLE_") ||
		strings.HasPrefix(key, "ALLOW_") || strings.Contains(key, "_ALLOW_")
}

// RequireEnv skips the test when any named variable is empty, after loading
// the repo .env.
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

// RequireLiveRead gates a test that reads a real venue's public or private
// endpoints. It is off under -short, and off again unless
// BOLTER_ENABLE_LIVE_READ_TESTS=1. No Make target sets that variable.
func RequireLiveRead(t testing.TB, vars ...string) {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping: live read test excluded by -short")
	}
	if os.Getenv("BOLTER_ENABLE_LIVE_READ_TESTS") != "1" {
		t.Skip("skipping live read test: set BOLTER_ENABLE_LIVE_READ_TESTS=1 to enable real exchange read execution")
	}
	if err := LoadRepoEnv(); err != nil {
		t.Fatalf("load repo .env: %v", err)
	}
	RequireEnv(t, vars...)
}

// RequireLiveWrite gates a test that places a real order with real money. It
// is off under -short, and off again unless the caller's own enableVar is 1.
//
// Missing credentials fail rather than skip here: the caller asked for a write
// explicitly, and a silent skip would read as a passing write test.
func RequireLiveWrite(t testing.TB, enableVar string, vars ...string) {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping: live write test excluded by -short")
	}
	if os.Getenv(enableVar) != "1" {
		t.Skipf("skipping live write test: set %s=1 to enable real exchange write execution", enableVar)
	}
	if err := LoadRepoEnv(); err != nil {
		t.Fatalf("load repo .env: %v", err)
	}
	if missing := missingEnv(vars...); len(missing) > 0 {
		t.Fatalf("live write gate %s enabled but required env is missing: %s", enableVar, strings.Join(missing, ", "))
	}
}

// RequireFull gates an extended verification test behind RUN_FULL=1.
func RequireFull(t testing.TB, vars ...string) {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping: full verification test excluded by -short")
	}
	if os.Getenv("RUN_FULL") != "1" {
		t.Skip("skipping: set RUN_FULL=1 to run full verification tests")
	}
	if err := LoadRepoEnv(); err != nil {
		t.Fatalf("load repo .env: %v", err)
	}
	RequireEnv(t, vars...)
}

// RequireSoak gates a long-running stream test behind RUN_SOAK=1.
func RequireSoak(t testing.TB, vars ...string) {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping: soak verification test excluded by -short")
	}
	if os.Getenv("RUN_SOAK") != "1" {
		t.Skip("skipping: set RUN_SOAK=1 to run soak verification tests")
	}
	if err := LoadRepoEnv(); err != nil {
		t.Fatalf("load repo .env: %v", err)
	}
	RequireEnv(t, vars...)
}

// SkipIfTransientLiveNetworkError turns a network failure in an enabled live
// test into a skip. A test that reached the internet and found it down proved
// nothing either way, and failing would report the network as a defect in this
// repository.
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
		strings.Contains(lower, "host is down") ||
		strings.Contains(lower, "network is unreachable") ||
		strings.Contains(lower, "no route to host") ||
		strings.Contains(lower, "no such host") ||
		strings.TrimSpace(lower) == "eof"
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

// applyLegacyAliases maps older variable names onto the canonical ones. A
// canonical value already in the environment always wins, including when it is
// explicitly empty: an operator who blanked a variable meant to blank it.
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
