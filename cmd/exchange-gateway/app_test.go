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

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/account"
	"github.com/QuantProcessing/boltertrader/gateway/catalog"
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

func TestAccountMetricSnapshotProjectsEachRouteStatus(t *testing.T) {
	loadedAt := time.Date(2026, 8, 10, 1, 2, 3, 0, time.UTC)
	source := &metricAccountSourceStub{
		resources: []account.Resource{
			{AccountID: "multi", Venue: exchange.VenueBinance, Environment: exchange.EnvironmentDemo, Product: exchange.ProductSpot},
			{AccountID: "multi", Venue: exchange.VenueBinance, Environment: exchange.EnvironmentDemo, Product: exchange.ProductPerp, Settlement: "USDT"},
		},
		statuses: map[[3]string]account.Status{
			{"multi", "spot", ""}:     {Resource: account.Resource{ReadReady: true, WriteReady: false}, Catalog: catalog.Status{LoadedAt: loadedAt, Generation: 3}},
			{"multi", "perp", "USDT"}: {Resource: account.Resource{ReadReady: false, WriteReady: true}, Catalog: catalog.Status{LoadedAt: loadedAt.Add(time.Minute), Generation: 4}},
		},
	}
	accounts, catalogs := accountMetricSnapshot(source)(time.Now())
	if len(accounts) != 2 || !accounts[0].ReadReady || accounts[0].WriteReady || accounts[1].ReadReady || !accounts[1].WriteReady {
		t.Fatalf("account metrics=%+v", accounts)
	}
	if len(catalogs) != 2 || catalogs[0].Generation != 3 || catalogs[1].Generation != 4 {
		t.Fatalf("catalog metrics=%+v", catalogs)
	}
}

type metricAccountSourceStub struct {
	resources []account.Resource
	statuses  map[[3]string]account.Status
}

func TestRuntimeAccountObserverFanoutRollsBackPartialRegistration(t *testing.T) {
	first := &runtimeAccountObserverStub{}
	second := &runtimeAccountObserverStub{registerErr: errors.New("reconciliation unavailable")}
	observer := newRuntimeAccountObserver(first, second)

	if err := observer.RegisterAccount("dynamic-account"); err == nil {
		t.Fatal("RegisterAccount succeeded")
	}
	if got, want := first.calls, []string{"register:dynamic-account", "unregister:dynamic-account"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("first observer calls=%v, want %v", got, want)
	}
	if got, want := second.calls, []string{"register:dynamic-account"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("second observer calls=%v, want %v", got, want)
	}
}

func TestRuntimeAccountObserverFanoutUnregistersInReverseOrder(t *testing.T) {
	calls := make([]string, 0, 4)
	streams := &runtimeAccountObserverStub{name: "streams", sharedCalls: &calls}
	reconciliation := &runtimeAccountObserverStub{name: "reconciliation", sharedCalls: &calls}
	observer := newRuntimeAccountObserver(streams, reconciliation)

	if err := observer.RegisterAccount("dynamic-account"); err != nil {
		t.Fatal(err)
	}
	observer.UnregisterAccount("dynamic-account")
	if want := []string{
		"streams:register:dynamic-account", "reconciliation:register:dynamic-account",
		"reconciliation:unregister:dynamic-account", "streams:unregister:dynamic-account",
	}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("observer calls=%v, want %v", calls, want)
	}
}

type runtimeAccountObserverStub struct {
	name        string
	registerErr error
	calls       []string
	sharedCalls *[]string
}

func (observer *runtimeAccountObserverStub) RegisterAccount(accountID domain.AccountID) error {
	call := "register:" + string(accountID)
	observer.calls = append(observer.calls, call)
	if observer.sharedCalls != nil {
		*observer.sharedCalls = append(*observer.sharedCalls, observer.name+":"+call)
	}
	return observer.registerErr
}

func (observer *runtimeAccountObserverStub) UnregisterAccount(accountID domain.AccountID) {
	call := "unregister:" + string(accountID)
	observer.calls = append(observer.calls, call)
	if observer.sharedCalls != nil {
		*observer.sharedCalls = append(*observer.sharedCalls, observer.name+":"+call)
	}
}

func (source *metricAccountSourceStub) Accounts() []account.Resource {
	return append([]account.Resource(nil), source.resources...)
}

func (source *metricAccountSourceStub) RouteStatus(accountID domain.AccountID, product, settlement string) (account.Status, error) {
	status, ok := source.statuses[[3]string{string(accountID), product, settlement}]
	if !ok {
		return account.Status{}, errors.New("route status unavailable")
	}
	return status, nil
}

func TestDefaultAccountManagerBuilderSupportsDynamicOnlyRuntime(t *testing.T) {
	builder := defaultBuildAccountManager(nil, nil, authorityCredentialResolver{}, account.RuntimeModeTest)
	built, err := builder(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	manager, ok := built.(*account.Manager)
	if !ok {
		t.Fatalf("manager type=%T", built)
	}
	if got := manager.AccountIDs(); len(got) != 0 {
		t.Fatalf("startup accounts=%v", got)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
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
		request.Header.Set("X-Request-ID", "mode-request")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusConflict || response.Header().Get(gatewayModeHeader) != "live" ||
			response.Header().Get("Content-Type") != "application/json" ||
			!strings.Contains(response.Body.String(), `"code":"MODE_ATTESTATION_REQUIRED"`) ||
			!strings.Contains(response.Body.String(), `"requestId":"mode-request"`) || calls != 0 {
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

	var started bool
	err := run(context.Background(), []string{"-config", configPath}, runtimeDeps{
		connectPostgres: func(ctx context.Context, dsn string) (process.PostgresPool, error) {
			return fakePool{}, nil
		},
		validateDatabaseIdentity: testValidateDatabaseIdentity,
		restoreAccountManager:    testRestoreAccountManager,
		migrate:                  func(ctx context.Context, pool process.PostgresPool) error { return nil },
		buildAccountManager: func(context.Context) (process.AccountManager, error) {
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

	var internalAddress string
	err := run(context.Background(), []string{"-config", configPath}, runtimeDeps{
		connectPostgres:          func(context.Context, string) (process.PostgresPool, error) { return fakePool{}, nil },
		validateDatabaseIdentity: testValidateDatabaseIdentity,
		restoreAccountManager:    testRestoreAccountManager,
		migrate:                  func(context.Context, process.PostgresPool) error { return nil },
		buildAccountManager: func(context.Context) (process.AccountManager, error) {
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

func TestRunFailsClosedForMissingConfigAndStaticAccounts(t *testing.T) {
	if err := run(context.Background(), nil, runtimeDeps{}); err == nil {
		t.Fatal("run without -config succeeded")
	}

	dir := t.TempDir()
	configPath := filepath.Join(dir, "gateway.yaml")
	staticAccounts := minimalConfigYAML() + "accounts: []\n"
	if err := os.WriteFile(configPath, []byte(staticAccounts), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TOKEN", "auth-token")
	t.Setenv("POSTGRES_DSN", "postgres://user:pass@127.0.0.1/db?sslmode=disable")

	err := run(context.Background(), []string{"-config", configPath}, runtimeDeps{
		connectPostgres: func(ctx context.Context, dsn string) (process.PostgresPool, error) {
			t.Fatal("postgres should not connect when static accounts are configured")
			return nil, nil
		},
	})
	if err == nil {
		t.Fatal("run with static accounts succeeded, want fail closed")
	}
	if strings.Contains(err.Error(), "auth-token") {
		t.Fatalf("error leaked secret/reference material: %v", err)
	}
}

func TestAllowedEnvRefsExcludeExchangeCredentials(t *testing.T) {
	file, err := config.LoadYAML([]byte(minimalConfigYAML()))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := allowedEnvRefs(file), []string{"env://TOKEN", "env://POSTGRES_DSN"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("allowed env refs = %v, want %v", got, want)
	}
}

func TestDefaultDatabaseIdentityValidatorRejectsUnknownMode(t *testing.T) {
	err := defaultValidateDatabaseIdentity(nil, account.RuntimeMode("unknown"))(t.Context(), fakePool{})
	if err == nil || strings.Contains(err.Error(), "postgres://") {
		t.Fatalf("identity validation error = %v", err)
	}
}

func TestConnectedDatabaseIdentityRequiresExactDerivedDatabaseAndRole(t *testing.T) {
	for _, test := range []struct {
		database string
		role     string
		wantErr  bool
	}{
		{database: "bolter_gateway_test", role: "bolter_gateway_test"},
		{database: "other", role: "bolter_gateway_test", wantErr: true},
		{database: "bolter_gateway_test", role: "elevated", wantErr: true},
	} {
		err := validateConnectedDatabaseIdentity(test.database, test.role, "bolter_gateway_test")
		if (err != nil) != test.wantErr {
			t.Fatalf("database=%q role=%q err=%v", test.database, test.role, err)
		}
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

func TestMigrationValidationSkipsPrivateRuntimeConstruction(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "gateway.yaml")
	yaml := strings.Replace(minimalConfigYAML(), "server:\n", "server:\n  startup_profile: migration-validation\n", 1)
	if err := os.WriteFile(configPath, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TOKEN", "auth-token")
	t.Setenv("POSTGRES_DSN", "postgres://user:pass@127.0.0.1/db?sslmode=disable")
	privateCall := func(name string) { t.Fatalf("migration validation called %s", name) }
	err := run(context.Background(), []string{"-config", configPath}, runtimeDeps{
		checkCredentialResolver:  func(context.Context) error { privateCall("credential resolver"); return nil },
		connectPostgres:          func(context.Context, string) (process.PostgresPool, error) { return fakePool{}, nil },
		validateDatabaseIdentity: testValidateDatabaseIdentity,
		verifyCanonicalWrites:    func(context.Context, process.PostgresPool) (bool, error) { return false, nil },
		migrate:                  func(context.Context, process.PostgresPool) error { privateCall("migration"); return nil },
		buildAccountManager:      func(context.Context) (process.AccountManager, error) { privateCall("account manager"); return nil, nil },
		buildOrderCoordinator: func(context.Context, process.PostgresPool, process.AccountManager) (process.OrderCoordinator, error) {
			privateCall("order coordinator")
			return nil, nil
		},
		buildValidationHandler: func(*health.Monitor, process.PostgresPool) (http.Handler, error) {
			return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), nil
		},
		listen: func(network, _ string) (net.Listener, error) { return net.Listen(network, "127.0.0.1:0") },
		wait:   func(ctx context.Context, service *process.Service) error { return service.Shutdown(ctx) },
	})
	if err != nil {
		t.Fatalf("run: %v", err)
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

	recorder := &cleanupRecorder{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() {
		errc <- run(ctx, []string{"-config", configPath}, runtimeDeps{
			connectPostgres: func(ctx context.Context, dsn string) (process.PostgresPool, error) {
				return recordingPool{recorder: recorder}, nil
			},
			migrate:                  func(ctx context.Context, pool process.PostgresPool) error { return nil },
			validateDatabaseIdentity: testValidateDatabaseIdentity,
			restoreAccountManager:    testRestoreAccountManager,
			buildAccountManager: func(context.Context) (process.AccountManager, error) {
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

	want := []string{"ping", "accept", "listener_close", "pool_close"}
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

			recorder := &cleanupRecorder{}
			err := run(context.Background(), []string{"-config", configPath}, runtimeDeps{
				connectPostgres: func(ctx context.Context, dsn string) (process.PostgresPool, error) {
					return recordingPool{recorder: recorder}, nil
				},
				migrate:                  func(ctx context.Context, pool process.PostgresPool) error { return nil },
				validateDatabaseIdentity: testValidateDatabaseIdentity,
				restoreAccountManager:    testRestoreAccountManager,
				buildAccountManager: func(context.Context) (process.AccountManager, error) {
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
			want := []string{"ping", "wait", "listener_close", "pool_close"}
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
`
}

type fakePool struct{}

func (fakePool) Ping(context.Context) error { return nil }
func (fakePool) Close()                     {}

type noopAccountManager struct{}

func (noopAccountManager) Start(context.Context) error { return nil }
func (noopAccountManager) AccountIDs() []domain.AccountID {
	return nil
}
func (noopAccountManager) Close() error { return nil }

type noopOrderCoordinator struct{}

func (noopOrderCoordinator) Start(context.Context, []domain.AccountID) error { return nil }
func (noopOrderCoordinator) Close(context.Context) error                     { return nil }

func testBuildOrderCoordinator(context.Context, process.PostgresPool, process.AccountManager) (process.OrderCoordinator, error) {
	return noopOrderCoordinator{}, nil
}

func testValidateDatabaseIdentity(context.Context, process.PostgresPool) error { return nil }

func testRestoreAccountManager(context.Context, process.PostgresPool, process.AccountManager) error {
	return nil
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
