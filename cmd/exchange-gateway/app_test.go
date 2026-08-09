package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/gateway/account"
	"github.com/QuantProcessing/boltertrader/gateway/config"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/gateway/health"
	"github.com/QuantProcessing/boltertrader/gateway/observability"
	"github.com/QuantProcessing/boltertrader/gateway/process"
)

func TestOperationalHTTPExposesMetricsAndObservesGatewayCommands(t *testing.T) {
	metrics := observability.NewMetrics()
	api := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"state":"uncertain"}`))
	})
	handler := withOperationalHTTP(api, metrics)

	orderResponse := httptest.NewRecorder()
	handler.ServeHTTP(orderResponse, httptest.NewRequest(http.MethodPost, "/v1/orders", nil))
	if orderResponse.Code != http.StatusAccepted {
		t.Fatalf("order status = %d", orderResponse.Code)
	}

	metricsResponse := httptest.NewRecorder()
	handler.ServeHTTP(metricsResponse, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if metricsResponse.Code != http.StatusOK ||
		!strings.Contains(metricsResponse.Body.String(), `gateway_command_total{operation="place_order",outcome="uncertain"} 1`) {
		t.Fatalf("metrics response=%d\n%s", metricsResponse.Code, metricsResponse.Body.String())
	}
}

func TestModeAttestedHandlerRejectsMissingAndMismatchedModes(t *testing.T) {
	calls := 0
	handler := modeAttestedHandler("live", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, mode := range []string{"", "test"} {
		request := httptest.NewRequest(http.MethodGet, "/v1/orders", nil)
		request.Header.Set(gatewayModeHeader, mode)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusConflict || response.Header().Get(gatewayModeHeader) != "live" || calls != 0 {
			t.Fatalf("mode=%q status=%d attestation=%q calls=%d", mode, response.Code, response.Header().Get(gatewayModeHeader), calls)
		}
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/orders", nil)
	request.Header.Set(gatewayModeHeader, "live")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || calls != 1 {
		t.Fatalf("matched status=%d calls=%d", response.Code, calls)
	}
}

func TestRunRequiresConfigFlagAndLoadsStrictConfigBeforeStart(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "gateway.yaml")
	if err := os.WriteFile(configPath, []byte(minimalConfigYAML()), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TOKEN", "auth-token")
	t.Setenv("POSTGRES_DSN", "postgres://user:pass@127.0.0.1/db?sslmode=disable")
	t.Setenv("API_KEY", "key")
	t.Setenv("API_SECRET", "secret")

	var started bool
	err := run(context.Background(), []string{"-config", configPath}, runtimeDeps{
		connectPostgres: func(ctx context.Context, dsn string) (process.PostgresPool, error) {
			return fakePool{}, nil
		},
		migrate: func(ctx context.Context, pool process.PostgresPool) error { return nil },
		registerAccount: func(ctx context.Context, pool process.PostgresPool, registration process.AccountRegistration) error {
			return nil
		},
		acquireOwnership: func(ctx context.Context, pool process.PostgresPool, registration process.AccountRegistration) (process.Ownership, error) {
			return fakeOwnership{}, nil
		},
		buildAccountManager: func(context.Context, []config.AccountConfig, map[string]string) (process.AccountManager, error) {
			return noopAccountManager{}, nil
		},
		buildOrderCoordinator: testBuildOrderCoordinator,
		buildHandler:          testBuildHandler,
		listen: func(network, address string) (net.Listener, error) {
			started = true
			if address != "127.0.0.1:0" {
				t.Fatalf("listen address = %q, want config value", address)
			}
			return fakeListener{}, nil
		},
		wait: func(ctx context.Context, service *process.Service) error {
			return service.Shutdown(ctx)
		},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !started {
		t.Fatal("service did not start")
	}
}

func TestRunStartsSeparateAuthenticatedInternalAuthorityListener(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "gateway.yaml")
	yaml := strings.Replace(minimalConfigYAML(),
		"  auth_token_ref: env://TOKEN",
		"  auth_token_ref: env://TOKEN\n  internal_listen: 127.0.0.1:0\n  internal_auth_token_ref: env://INTERNAL_TOKEN", 1)
	if err := os.WriteFile(configPath, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TOKEN", "public-token")
	t.Setenv("INTERNAL_TOKEN", "gateway-adapter-token")
	t.Setenv("POSTGRES_DSN", "postgres://user:pass@127.0.0.1/db?sslmode=disable")
	t.Setenv("API_KEY", "key")
	t.Setenv("API_SECRET", "secret")

	var internalAddress string
	err := run(context.Background(), []string{"-config", configPath}, runtimeDeps{
		connectPostgres: func(context.Context, string) (process.PostgresPool, error) { return fakePool{}, nil },
		migrate:         func(context.Context, process.PostgresPool) error { return nil },
		registerAccount: func(context.Context, process.PostgresPool, process.AccountRegistration) error { return nil },
		acquireOwnership: func(context.Context, process.PostgresPool, process.AccountRegistration) (process.Ownership, error) {
			return fakeOwnership{}, nil
		},
		buildAccountManager: func(context.Context, []config.AccountConfig, map[string]string) (process.AccountManager, error) {
			return noopAccountManager{}, nil
		},
		buildOrderCoordinator: testBuildOrderCoordinator,
		buildHandler:          testBuildHandler,
		buildInternalHandler: func(context.Context, process.PostgresPool, process.AccountManager) (http.Handler, error) {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/internal/v1/runtime/status" {
					http.NotFound(w, r)
					return
				}
				w.WriteHeader(http.StatusNoContent)
			}), nil
		},
		listen: func(network, _ string) (net.Listener, error) { return net.Listen(network, "127.0.0.1:0") },
		listenInternal: func(network, address string) (net.Listener, error) {
			listener, err := net.Listen(network, address)
			if err == nil {
				internalAddress = listener.Addr().String()
			}
			return listener, err
		},
		wait: func(ctx context.Context, service *process.Service) error {
			unauthorizedRequest, _ := http.NewRequest(http.MethodGet, "http://"+internalAddress+"/internal/v1/runtime/status", nil)
			unauthorizedRequest.Header.Set(gatewayModeHeader, "test")
			unauthorized, err := http.DefaultClient.Do(unauthorizedRequest)
			if err != nil {
				t.Fatal(err)
			}
			defer unauthorized.Body.Close()
			if unauthorized.StatusCode != http.StatusUnauthorized {
				t.Fatalf("unauthorized status=%d", unauthorized.StatusCode)
			}
			request, _ := http.NewRequest(http.MethodGet, "http://"+internalAddress+"/internal/v1/runtime/status", nil)
			request.Header.Set("Authorization", "Bearer gateway-adapter-token")
			request.Header.Set(gatewayModeHeader, "test")
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusNoContent {
				t.Fatalf("authorized status=%d", response.StatusCode)
			}
			return service.Shutdown(ctx)
		},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
}

func TestRunFailsClosedForMissingConfigAndUnsupportedSecretProvider(t *testing.T) {
	if err := run(context.Background(), nil, runtimeDeps{}); err == nil {
		t.Fatal("run without -config succeeded")
	}

	dir := t.TempDir()
	configPath := filepath.Join(dir, "gateway.yaml")
	if err := os.WriteFile(configPath, []byte(strings.Replace(minimalConfigYAML(), "env://API_SECRET", "vault://trading/api-secret", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TOKEN", "auth-token")
	t.Setenv("POSTGRES_DSN", "postgres://user:pass@127.0.0.1/db?sslmode=disable")
	t.Setenv("API_KEY", "key")

	err := run(context.Background(), []string{"-config", configPath}, runtimeDeps{
		connectPostgres: func(ctx context.Context, dsn string) (process.PostgresPool, error) {
			t.Fatal("postgres should not connect when vault secret is configured without provider")
			return nil, nil
		},
	})
	if err == nil {
		t.Fatal("run with vault ref succeeded, want fail closed")
	}
	if strings.Contains(err.Error(), "auth-token") || strings.Contains(err.Error(), "api-secret") {
		t.Fatalf("error leaked secret/reference material: %v", err)
	}
}

func TestRunDoesNotListenWhenStartupFails(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "gateway.yaml")
	if err := os.WriteFile(configPath, []byte(minimalConfigYAML()), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TOKEN", "auth-token")
	t.Setenv("POSTGRES_DSN", "postgres://user:pass@127.0.0.1/db?sslmode=disable")
	t.Setenv("API_KEY", "key")
	t.Setenv("API_SECRET", "secret")

	err := run(context.Background(), []string{"-config", configPath}, runtimeDeps{
		connectPostgres: func(ctx context.Context, dsn string) (process.PostgresPool, error) {
			return nil, errors.New("postgres down")
		},
		listen: func(network, address string) (net.Listener, error) {
			t.Fatal("listener opened after postgres failure")
			return nil, nil
		},
	})
	if err == nil {
		t.Fatal("run succeeded, want startup failure")
	}
}

func TestRunRejectsCredentialResolverFailureBeforeListen(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "gateway.yaml")
	if err := os.WriteFile(configPath, []byte(minimalConfigYAML()), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TOKEN", "auth-token")
	t.Setenv("POSTGRES_DSN", "postgres://user:pass@127.0.0.1/db?sslmode=disable")
	t.Setenv("API_KEY", "key")
	t.Setenv("API_SECRET", "secret")
	listened := false
	err := run(context.Background(), []string{"-config", configPath}, runtimeDeps{
		checkCredentialResolver: func(context.Context) error { return errors.New("provider detail canary") },
		listen: func(string, string) (net.Listener, error) {
			listened = true
			return nil, nil
		},
	})
	if err == nil || listened || strings.Contains(err.Error(), "provider detail canary") {
		t.Fatalf("err=%v listened=%t", err, listened)
	}
}

func TestProductionGatewayCredentialResolverUsesModeScopedFilesystemProvider(t *testing.T) {
	root := t.TempDir()
	keyFile := filepath.Join(root, "credentials", "test", "secret-store-master.key")
	if err := os.MkdirAll(filepath.Dir(keyFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, []byte(strings.Repeat("ab", 32)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BT_SECRET_KEY_SOURCE", "file")
	t.Setenv("BT_SECRET_STORE_DIR", filepath.Join(root, "secret-store"))
	t.Setenv("BT_SECRET_STORE_NAMESPACE", "boltertrader/test")
	t.Setenv("BT_SECRET_MASTER_KEY_VERSION", "v1")
	t.Setenv("BT_SECRET_MASTER_KEY_FILE", keyFile)
	t.Setenv("BT_SECRET_KEYCHAIN_SERVICE", "")
	t.Setenv("BT_SECRET_KEYCHAIN_ACCOUNT", "")

	provider, resolver, err := newGatewayCredentialResolver(account.RuntimeModeTest)
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	if err := resolver.Check(context.Background()); err != nil {
		t.Fatal(err)
	}

	t.Setenv("BT_SECRET_STORE_NAMESPACE", "boltertrader/live")
	if _, _, err := newGatewayCredentialResolver(account.RuntimeModeTest); err == nil {
		t.Fatal("cross-mode credential resolver configuration succeeded")
	}
}

func TestRunExitsOnServeFailureWithSafeErrorAndExactCleanup(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "gateway.yaml")
	if err := os.WriteFile(configPath, []byte(minimalConfigYAML()), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TOKEN", "auth-token")
	t.Setenv("POSTGRES_DSN", "postgres://user:pass@127.0.0.1/db?sslmode=disable")
	t.Setenv("API_KEY", "key")
	t.Setenv("API_SECRET", "secret")

	recorder := &cleanupRecorder{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() {
		errc <- run(ctx, []string{"-config", configPath}, runtimeDeps{
			connectPostgres: func(ctx context.Context, dsn string) (process.PostgresPool, error) {
				return recordingPool{recorder: recorder}, nil
			},
			migrate: func(ctx context.Context, pool process.PostgresPool) error { return nil },
			registerAccount: func(ctx context.Context, pool process.PostgresPool, registration process.AccountRegistration) error {
				return nil
			},
			acquireOwnership: func(ctx context.Context, pool process.PostgresPool, registration process.AccountRegistration) (process.Ownership, error) {
				return recordingOwnership{recorder: recorder}, nil
			},
			buildAccountManager: func(context.Context, []config.AccountConfig, map[string]string) (process.AccountManager, error) {
				return noopAccountManager{}, nil
			},
			buildOrderCoordinator: testBuildOrderCoordinator,
			buildHandler:          testBuildHandler,
			listen: func(network, address string) (net.Listener, error) {
				return serveErrorListener{recorder: recorder, err: errors.New("serve failed with auth-token postgres://user:pass@127.0.0.1/db")}, nil
			},
		})
	}()

	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("run returned nil, want safe serve failure")
		}
		if !strings.Contains(err.Error(), "serve failed") {
			t.Fatalf("run error = %v, want safe serve failure", err)
		}
		if strings.Contains(err.Error(), "auth-token") || strings.Contains(err.Error(), "postgres://") {
			t.Fatalf("run error leaked serve error material: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		cancel()
		t.Fatal("run did not exit promptly after Serve failure")
	}

	want := []string{"ping", "accept", "listener_close", "release", "pool_close"}
	if got := recorder.calls(); !reflect.DeepEqual(got, want) {
		t.Fatalf("cleanup calls = %v, want %v", got, want)
	}
}

func TestRunAlwaysPerformsBoundedIdempotentShutdownAfterInjectedWaitReturns(t *testing.T) {
	for name, waitErr := range map[string]error{"nil": nil, "error": errors.New("wait failed")} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			configPath := filepath.Join(dir, "gateway.yaml")
			if err := os.WriteFile(configPath, []byte(minimalConfigYAML()), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("TOKEN", "auth-token")
			t.Setenv("POSTGRES_DSN", "postgres://user:pass@127.0.0.1/db?sslmode=disable")
			t.Setenv("API_KEY", "key")
			t.Setenv("API_SECRET", "secret")

			recorder := &cleanupRecorder{}
			err := run(context.Background(), []string{"-config", configPath}, runtimeDeps{
				connectPostgres: func(ctx context.Context, dsn string) (process.PostgresPool, error) {
					return recordingPool{recorder: recorder}, nil
				},
				migrate: func(ctx context.Context, pool process.PostgresPool) error { return nil },
				registerAccount: func(ctx context.Context, pool process.PostgresPool, registration process.AccountRegistration) error {
					return nil
				},
				acquireOwnership: func(ctx context.Context, pool process.PostgresPool, registration process.AccountRegistration) (process.Ownership, error) {
					return recordingOwnership{recorder: recorder}, nil
				},
				buildAccountManager: func(context.Context, []config.AccountConfig, map[string]string) (process.AccountManager, error) {
					return noopAccountManager{}, nil
				},
				buildOrderCoordinator: testBuildOrderCoordinator,
				buildHandler:          testBuildHandler,
				listen: func(network, address string) (net.Listener, error) {
					return newIdleListener(recorder), nil
				},
				wait: func(ctx context.Context, service *process.Service) error {
					recorder.add("wait")
					return waitErr
				},
			})
			if waitErr == nil && err != nil {
				t.Fatalf("run returned %v, want nil", err)
			}
			if waitErr != nil {
				if err == nil || !strings.Contains(err.Error(), "wait failed") {
					t.Fatalf("run error = %v, want injected wait error", err)
				}
			}
			want := []string{"ping", "wait", "listener_close", "release", "pool_close"}
			if got := recorder.calls(); !reflect.DeepEqual(got, want) {
				t.Fatalf("cleanup calls = %v, want %v", got, want)
			}
		})
	}
}

func minimalConfigYAML() string {
	return `
server:
  listen: 127.0.0.1:0
  auth_token_ref: env://TOKEN
postgres:
  dsn_ref: env://POSTGRES_DSN
accounts:
  - account_id: acct-a
    venue: binance
    product: spot
    environment: demo
    credentials:
      api_key_ref: env://API_KEY
      api_secret_ref: env://API_SECRET
`
}

type fakePool struct{}

func (fakePool) Ping(context.Context) error { return nil }
func (fakePool) Close()                     {}

type fakeOwnership struct{}

func (fakeOwnership) Healthy() bool                 { return true }
func (fakeOwnership) Release(context.Context) error { return nil }

type noopAccountManager struct{}

func (noopAccountManager) Start(context.Context) error { return nil }
func (noopAccountManager) AccountIDs() []domain.AccountID {
	return []domain.AccountID{"acct-a"}
}
func (noopAccountManager) Close() error { return nil }

type noopOrderCoordinator struct{}

func (noopOrderCoordinator) Start(context.Context, []domain.AccountID) error { return nil }
func (noopOrderCoordinator) Close(context.Context) error                     { return nil }

func testBuildOrderCoordinator(context.Context, process.PostgresPool, process.AccountManager) (process.OrderCoordinator, error) {
	return noopOrderCoordinator{}, nil
}

func testBuildHandler(monitor *health.Monitor, _ process.PostgresPool, _ process.AccountManager, _ process.OrderCoordinator) (http.Handler, error) {
	return health.NewHandler(monitor), nil
}

type fakeListener struct{ net.Listener }

func (fakeListener) Close() error { return nil }

type cleanupRecorder struct {
	mu    sync.Mutex
	order []string
}

func (recorder *cleanupRecorder) add(call string) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.order = append(recorder.order, call)
}

func (recorder *cleanupRecorder) calls() []string {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]string(nil), recorder.order...)
}

type recordingPool struct{ recorder *cleanupRecorder }

func (pool recordingPool) Ping(context.Context) error {
	pool.recorder.add("ping")
	return nil
}
func (pool recordingPool) Close() { pool.recorder.add("pool_close") }

type recordingOwnership struct{ recorder *cleanupRecorder }

func (ownership recordingOwnership) Healthy() bool { return true }
func (ownership recordingOwnership) Release(context.Context) error {
	ownership.recorder.add("release")
	return nil
}

type serveErrorListener struct {
	net.Listener
	recorder *cleanupRecorder
	err      error
}

func (listener serveErrorListener) Accept() (net.Conn, error) {
	listener.recorder.add("accept")
	return nil, listener.err
}

func (listener serveErrorListener) Close() error {
	listener.recorder.add("listener_close")
	return nil
}

func (listener serveErrorListener) Addr() net.Addr { return loopbackAddr{} }

type idleListener struct {
	net.Listener
	recorder *cleanupRecorder
	closed   chan struct{}
	once     sync.Once
}

func newIdleListener(recorder *cleanupRecorder) *idleListener {
	return &idleListener{recorder: recorder, closed: make(chan struct{})}
}

func (listener *idleListener) Accept() (net.Conn, error) {
	<-listener.closed
	return nil, net.ErrClosed
}

func (listener *idleListener) Close() error {
	listener.once.Do(func() {
		close(listener.closed)
	})
	listener.recorder.add("listener_close")
	return nil
}

func (listener *idleListener) Addr() net.Addr { return loopbackAddr{} }

type loopbackAddr struct{}

func (loopbackAddr) Network() string { return "tcp" }
func (loopbackAddr) String() string  { return "127.0.0.1:0" }
