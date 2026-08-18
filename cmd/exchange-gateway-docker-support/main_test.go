package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestRunLoadsDistinctEnvRefsOnceAndExecsGatewayWithChildOnlyEnvironment(t *testing.T) {
	secretRoot := t.TempDir()
	writeSecret(t, secretRoot, "TOKEN", "docker-token\n")
	writeSecret(t, secretRoot, "POSTGRES_DSN", "postgres://docker\n")
	writeSecret(t, secretRoot, "INTERNAL_TOKEN", "docker-internal-token\n")

	configPath := writeConfig(t, `
server:
  listen: 0.0.0.0:18080
  allow_non_loopback_listen: true
  auth_token_ref: env://TOKEN
  internal_listen: 127.0.0.1:18081
  internal_auth_token_ref: env://INTERNAL_TOKEN
postgres:
  dsn_ref: env://POSTGRES_DSN
`)

	t.Setenv("TOKEN", "parent-token")
	readCount := map[string]int{}
	var gotPath string
	var gotArgs, gotEnv []string
	execSentinel := errors.New("exec intercepted")
	err := runDockerSupport(context.Background(), []string{
		"run", "-config", configPath, "-gateway", "/opt/gateway",
	}, dockerSupportDeps{
		secretRoot: secretRoot,
		readFile: func(path string) ([]byte, error) {
			readCount[filepath.Base(path)]++
			return os.ReadFile(path)
		},
		exec: func(path string, args, env []string) error {
			gotPath = path
			gotArgs = slices.Clone(args)
			gotEnv = slices.Clone(env)
			return execSentinel
		},
		environ: os.Environ,
	})
	if !errors.Is(err, execSentinel) {
		t.Fatalf("runDockerSupport error = %v, want intercepted exec", err)
	}
	if gotPath != "/opt/gateway" || !slices.Equal(gotArgs, []string{"/opt/gateway", "-config", configPath}) {
		t.Fatalf("exec path/args = %q %v", gotPath, gotArgs)
	}
	for _, name := range []string{"TOKEN", "POSTGRES_DSN", "INTERNAL_TOKEN"} {
		if readCount[name] != 1 {
			t.Fatalf("secret %s read %d times, want once", name, readCount[name])
		}
	}
	if got := envValue(gotEnv, "TOKEN"); got != "docker-token" {
		t.Fatalf("child TOKEN = %q", got)
	}
	if got := envValue(gotEnv, "POSTGRES_DSN"); got != "postgres://docker" {
		t.Fatalf("child POSTGRES_DSN = %q", got)
	}
	if got := envValue(gotEnv, "INTERNAL_TOKEN"); got != "docker-internal-token" {
		t.Fatalf("child INTERNAL_TOKEN = %q", got)
	}
	if got := os.Getenv("TOKEN"); got != "parent-token" {
		t.Fatalf("parent TOKEN mutated to %q", got)
	}
}

func TestRunReadsRepeatedSecretReferenceOnce(t *testing.T) {
	secretRoot := t.TempDir()
	writeSecret(t, secretRoot, "TOKEN", "shared-token\n")
	writeSecret(t, secretRoot, "POSTGRES_DSN", "postgres://docker\n")

	configPath := writeConfig(t, `
server:
  listen: 127.0.0.1:18080
  auth_token_ref: env://TOKEN
  internal_listen: 127.0.0.1:18081
  internal_auth_token_ref: env://TOKEN
postgres:
  dsn_ref: env://POSTGRES_DSN
`)

	readCount := map[string]int{}
	var gotEnv []string
	execSentinel := errors.New("exec intercepted")
	err := runDockerSupport(context.Background(), []string{"run", "-config", configPath}, dockerSupportDeps{
		secretRoot: secretRoot,
		readFile: func(path string) ([]byte, error) {
			readCount[filepath.Base(path)]++
			return os.ReadFile(path)
		},
		exec: func(_ string, _, env []string) error {
			gotEnv = slices.Clone(env)
			return execSentinel
		},
		environ: os.Environ,
	})
	if !errors.Is(err, execSentinel) {
		t.Fatalf("runDockerSupport error = %v, want intercepted exec", err)
	}
	if readCount["TOKEN"] != 1 {
		t.Fatalf("repeated secret reference read %d times, want once", readCount["TOKEN"])
	}
	if got := envValue(gotEnv, "TOKEN"); got != "shared-token" {
		t.Fatalf("child TOKEN = %q", got)
	}
}

func TestRunRejectsUnsafeOrMissingSecretMappingsWithoutLeakingValues(t *testing.T) {
	tests := []struct {
		name      string
		authRef   string
		wantError string
	}{
		{name: "unsafe name", authRef: "env://BAD-NAME", wantError: "unsafe"},
		{name: "missing file", authRef: "env://MISSING", wantError: "missing"},
		{name: "unsupported provider", authRef: "vault://gateway/token", wantError: "env"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeSecret(t, root, "POSTGRES_DSN", "postgres-canary")
			configPath := writeConfig(t, `
server:
  listen: 127.0.0.1:18080
  auth_token_ref: `+test.authRef+`
postgres:
  dsn_ref: env://POSTGRES_DSN
`)
			called := false
			err := runDockerSupport(context.Background(), []string{"run", "-config", configPath}, dockerSupportDeps{
				secretRoot: root,
				exec: func(string, []string, []string) error {
					called = true
					return nil
				},
			})
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), test.wantError) {
				t.Fatalf("error = %v, want containing %q", err, test.wantError)
			}
			if called {
				t.Fatal("gateway exec called after secret mapping failure")
			}
			if strings.Contains(err.Error(), "postgres-canary") {
				t.Fatalf("error leaked secret value: %v", err)
			}
		})
	}
}

func TestHealthReadsTokenFileDirectlyAndUsesAuthenticatedLoopbackEndpoints(t *testing.T) {
	token := `health-token-{"雪"}`
	var gotHost, gotPath, gotAuthorization string
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		gotHost = request.URL.Host
		gotPath = request.URL.Path
		gotAuthorization = request.Header.Get("Authorization")
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("ok")),
			Header:     make(http.Header),
		}, nil
	})}

	root := t.TempDir()
	writeSecret(t, root, "TOKEN", token+"\n")
	configPath := writeConfig(t, `
server:
  listen: 0.0.0.0:18080
  allow_non_loopback_listen: true
  auth_token_ref: env://TOKEN
postgres:
  dsn_ref: env://POSTGRES_DSN
`)

	err := runDockerSupport(context.Background(), []string{
		"health", "-config", configPath, "-kind", "ready",
	}, dockerSupportDeps{secretRoot: root, httpClient: client})
	if err != nil {
		t.Fatalf("health ready: %v", err)
	}
	if gotHost != "127.0.0.1:18080" {
		t.Fatalf("health host = %q, want loopback", gotHost)
	}
	if gotPath != "/v1/health/ready" {
		t.Fatalf("health path = %q", gotPath)
	}
	if gotAuthorization != "Bearer "+token {
		t.Fatalf("authorization header mismatch")
	}
}

// A malformed probe must fail before it dials, not after: a request built from
// a bad flag would either hit the wrong path or hang past the healthcheck's own
// interval, and Compose reads both as unhealthy forever, holding back every
// service that waits on the Gateway. So each guard is asserted alongside the
// fact that nothing was sent.
//
// This replaces a test that varied `mode:` in the config and asserted the value
// reached an X-Bolter-Mode header. There is one mode now and the header is gone
// from both ends; the token is the only config-derived value still on the wire,
// and the test above already covers it.
func TestHealthRejectsMalformedInvocationsWithoutDialing(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "no config", args: []string{"health", "-kind", "ready"}, want: "requires -config"},
		{name: "unknown kind", args: []string{"health", "-kind", "readyish"}, want: "kind must be live or ready"},
		{name: "negative timeout", args: []string{"health", "-kind", "ready", "-timeout", "-1s"}, want: "timeout must be positive"},
		{name: "timeout past the ceiling", args: []string{"health", "-kind", "ready", "-timeout", "30s"}, want: "at most 10s"},
		{name: "positional argument", args: []string{"health", "-kind", "ready", "extra"}, want: "does not accept positional arguments"},
		{name: "unknown flag", args: []string{"health", "-kind", "ready", "-mode", "test"}, want: "parse health arguments"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dialed := false
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				dialed = true
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader("ok")),
					Header:     make(http.Header),
				}, nil
			})}

			root := t.TempDir()
			writeSecret(t, root, "TOKEN", "token-long-enough-to-be-a-token")
			configPath := writeConfig(t, `
server:
  listen: 127.0.0.1:18080
  auth_token_ref: env://TOKEN
postgres:
  dsn_ref: env://POSTGRES_DSN
`)
			args := test.args
			if test.name != "no config" {
				// Inserted right after the subcommand rather than appended:
				// flag parsing stops at the first positional, so a trailing
				// -config would itself become one and the missing-config check
				// would fire before the guard under test.
				args = append([]string{args[0], "-config", configPath}, args[1:]...)
			}

			err := runDockerSupport(context.Background(), args, dockerSupportDeps{secretRoot: root, httpClient: client})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err = %v, want one containing %q", err, test.want)
			}
			if dialed {
				t.Fatal("a malformed probe reached the Gateway")
			}
		})
	}
}

func TestHealthFailureIsBoundedAndRedacted(t *testing.T) {
	token := "health-secret-canary"
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Body:       io.NopCloser(strings.NewReader("response-body-secret-canary")),
			Header:     make(http.Header),
		}, nil
	})}

	root := t.TempDir()
	writeSecret(t, root, "TOKEN", token)
	configPath := writeConfig(t, `
server:
  listen: 127.0.0.1:18080
  auth_token_ref: env://TOKEN
postgres:
  dsn_ref: env://POSTGRES_DSN
`)
	err := runDockerSupport(context.Background(), []string{
		"health", "-config", configPath, "-kind", "live", "-timeout", "2s",
	}, dockerSupportDeps{secretRoot: root, httpClient: client})
	if err == nil {
		t.Fatal("health unexpectedly succeeded")
	}
	if !strings.Contains(err.Error(), "status 503") {
		t.Fatalf("health error = %v, want status only", err)
	}
	for _, canary := range []string{token, "response-body-secret-canary"} {
		if strings.Contains(err.Error(), canary) {
			t.Fatalf("health error leaked %q: %v", canary, err)
		}
	}
}

func writeConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gateway.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeSecret(t *testing.T, root, name, value string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(value), 0o400); err != nil {
		t.Fatal(err)
	}
}

func envValue(environ []string, name string) string {
	prefix := name + "="
	for _, entry := range environ {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	return ""
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}
