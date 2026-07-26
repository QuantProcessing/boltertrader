package process_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/gateway/config"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/gateway/health"
	"github.com/QuantProcessing/boltertrader/gateway/process"
	"github.com/QuantProcessing/boltertrader/gateway/secret"
)

func TestServiceStartupOrdersDependenciesBeforeListeningAndMarksReady(t *testing.T) {
	fixture := newFixture(t)
	var calls []string
	var retainedSecrets map[string]string
	deps := process.Dependencies{
		ResolveSecret: func(ctx context.Context, ref secret.Ref) (string, error) {
			calls = append(calls, "resolve:"+ref.RawForProvider())
			return fixture.secrets[ref.RawForProvider()], nil
		},
		ConnectPostgres: func(ctx context.Context, dsn string) (process.PostgresPool, error) {
			calls = append(calls, "connect")
			if dsn != fixture.dsn {
				t.Fatalf("dsn = %q, want fixture dsn", dsn)
			}
			return fakePool{calls: &calls}, nil
		},
		Migrate: func(ctx context.Context, pool process.PostgresPool) error {
			calls = append(calls, "migrate")
			return nil
		},
		RegisterAccount: func(ctx context.Context, pool process.PostgresPool, registration process.AccountRegistration) error {
			calls = append(calls, "register:"+string(registration.AccountID))
			if registration.ConfigHash == "" || len(registration.ConfigHash) != 64 {
				t.Fatalf("config hash = %q, want sha256 hex", registration.ConfigHash)
			}
			return nil
		},
		AcquireOwnership: func(ctx context.Context, pool process.PostgresPool, accountID domain.AccountID) (process.Ownership, error) {
			calls = append(calls, "ownership:"+string(accountID))
			return fakeOwnership{calls: &calls}, nil
		},
		BuildAccountManager: func(ctx context.Context, accounts []config.AccountConfig, resolved map[string]string) (process.AccountManager, error) {
			calls = append(calls, "build_accounts")
			if len(accounts) != 1 || resolved["env://API_SECRET"] != "secret" {
				t.Fatalf("account build inputs accounts=%d resolved=%v", len(accounts), resolved)
			}
			retainedSecrets = resolved
			return &fakeAccountManager{calls: &calls}, nil
		},
		BuildOrderCoordinator: func(context.Context, process.PostgresPool, process.AccountManager) (process.OrderCoordinator, error) {
			calls = append(calls, "build_orders")
			return &fakeOrderCoordinator{calls: &calls}, nil
		},
		Listen: func(network, address string) (net.Listener, error) {
			calls = append(calls, "listen:"+network+":"+address)
			if address != "127.0.0.1:0" {
				t.Fatalf("listen address = %q, want loopback config", address)
			}
			return newFakeListener(), nil
		},
	}

	service, err := process.Start(context.Background(), fixture.config, deps)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = service.Shutdown(context.Background()) }()

	wantPrefix := []string{
		"resolve:env://TOKEN",
		"resolve:env://POSTGRES_DSN",
		"resolve:env://API_KEY",
		"resolve:env://API_SECRET",
		"connect",
		"ping",
		"migrate",
		"register:acct-a",
		"ownership:acct-a",
		"build_accounts",
		"accounts_start",
		"build_orders",
		"orders_recover:acct-a",
		"listen:tcp:127.0.0.1:0",
	}
	if !reflect.DeepEqual(calls[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("startup calls = %v, want prefix %v", calls, wantPrefix)
	}
	if ready := service.Health().Readiness(); !ready.Ready {
		t.Fatalf("readiness = %+v, want ready after listener bind", ready)
	}
	if len(retainedSecrets) != 0 {
		t.Fatalf("process retained resolved secret map after client construction: %v", retainedSecrets)
	}
}

func TestServiceBuildsHTTPHandlerAfterRecoveryAndBeforeListenerBind(t *testing.T) {
	fixture := newFixture(t)
	var calls []string
	deps := fixtureDeps(fixture, &calls)
	deps.HandlerFactory = func(
		_ *health.Monitor,
		pool process.PostgresPool,
		_ process.AccountManager,
		_ process.OrderCoordinator,
	) (http.Handler, error) {
		calls = append(calls, "build_handler")
		if _, ok := pool.(fakePool); !ok {
			t.Fatalf("handler pool=%T, want startup PostgreSQL pool", pool)
		}
		return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), nil
	}

	service, err := process.Start(context.Background(), fixture.config, deps)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = service.Shutdown(context.Background()) }()

	recovery := indexOf(calls, "orders_recover:acct-a")
	handler := indexOf(calls, "build_handler")
	listen := indexOf(calls, "listen")
	if recovery < 0 || handler < 0 || listen < 0 || !(recovery < handler && handler < listen) {
		t.Fatalf("startup calls=%v, want recovery < handler < listen", calls)
	}
}

func TestServiceHandlerBuildFailureNeverBindsListener(t *testing.T) {
	fixture := newFixture(t)
	var calls []string
	deps := fixtureDeps(fixture, &calls)
	deps.HandlerFactory = func(
		_ *health.Monitor,
		_ process.PostgresPool,
		_ process.AccountManager,
		_ process.OrderCoordinator,
	) (http.Handler, error) {
		calls = append(calls, "build_handler")
		return nil, errors.New("handler failed with auth-token")
	}

	_, err := process.Start(context.Background(), fixture.config, deps)
	if err == nil {
		t.Fatal("Start succeeded, want handler build failure")
	}
	if strings.Contains(err.Error(), "auth-token") {
		t.Fatalf("handler build error leaked detail: %v", err)
	}
	if indexOf(calls, "listen") >= 0 {
		t.Fatalf("listener bound after handler failure: %v", calls)
	}
	for _, want := range []string{"orders_close", "accounts_close", "release", "pool_close"} {
		if indexOf(calls, want) < 0 {
			t.Fatalf("startup cleanup calls=%v, missing %s", calls, want)
		}
	}
}

func TestServiceStartupFailureBeforeListenLeavesNoListenerAndCleansOwnership(t *testing.T) {
	fixture := newFixture(t)
	var calls []string
	deps := fixtureDeps(fixture, &calls)
	deps.AcquireOwnership = func(ctx context.Context, pool process.PostgresPool, accountID domain.AccountID) (process.Ownership, error) {
		calls = append(calls, "ownership:"+string(accountID))
		return fakeOwnership{calls: &calls}, nil
	}
	deps.Listen = func(network, address string) (net.Listener, error) {
		calls = append(calls, "listen")
		return nil, errors.New("bind failed with canary secret")
	}

	_, err := process.Start(context.Background(), fixture.config, deps)
	if err == nil {
		t.Fatal("Start succeeded, want failure")
	}
	joined := strings.Join(calls, ",")
	if !strings.Contains(joined, "release") {
		t.Fatalf("calls = %v, want ownership release on listen failure", calls)
	}
	if !strings.Contains(joined, "pool_close") {
		t.Fatalf("calls = %v, want pool close on startup failure", calls)
	}
	if strings.Contains(err.Error(), "auth-token") || strings.Contains(err.Error(), fixture.dsn) {
		t.Fatalf("startup error leaked secret/DSN: %v", err)
	}
}

func indexOf(values []string, want string) int {
	for index, value := range values {
		if value == want {
			return index
		}
	}
	return -1
}

func TestServiceAccountManagerStartFailureCleansManagerOwnershipAndPoolBeforeListen(t *testing.T) {
	fixture := newFixture(t)
	var calls []string
	deps := fixtureDeps(fixture, &calls)
	manager := &fakeAccountManager{calls: &calls, startErr: errors.New("catalog refresh failed with secret canary")}
	deps.BuildAccountManager = func(context.Context, []config.AccountConfig, map[string]string) (process.AccountManager, error) {
		calls = append(calls, "build_accounts")
		return manager, nil
	}
	deps.Listen = func(string, string) (net.Listener, error) {
		calls = append(calls, "listen")
		return newFakeListenerWithCalls(&calls), nil
	}

	_, err := process.Start(context.Background(), fixture.config, deps)
	if err == nil {
		t.Fatal("Start succeeded, want account manager start failure")
	}
	if strings.Contains(strings.ToLower(err.Error()), "secret canary") {
		t.Fatalf("startup error leaked manager detail: %v", err)
	}
	wantSuffix := []string{"build_accounts", "accounts_start", "accounts_close", "release", "pool_close"}
	if len(calls) < len(wantSuffix) || !reflect.DeepEqual(calls[len(calls)-len(wantSuffix):], wantSuffix) {
		t.Fatalf("startup cleanup calls = %v, want suffix %v", calls, wantSuffix)
	}
	if strings.Contains(strings.Join(calls, ","), "listen") {
		t.Fatalf("listener opened after account manager start failure: %v", calls)
	}
}

func TestServiceShutdownClosesListenerReleasesOwnershipThenPool(t *testing.T) {
	fixture := newFixture(t)
	var calls []string
	deps := fixtureDeps(fixture, &calls)

	service, err := process.Start(context.Background(), fixture.config, deps)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := service.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	joined := strings.Join(calls, ",")
	for _, want := range []string{"listener_close", "orders_close", "accounts_close", "release", "pool_close"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("calls = %v, missing %s", calls, want)
		}
	}
	if strings.Index(joined, "orders_close") > strings.Index(joined, "accounts_close") ||
		strings.Index(joined, "accounts_close") > strings.Index(joined, "release") ||
		strings.Index(joined, "release") > strings.Index(joined, "pool_close") {
		t.Fatalf("shutdown order = %v, want orders before clients before release before pool close", calls)
	}
}

func TestServiceShutdownRetriesFailedCleanupWithoutClosingDependentPool(t *testing.T) {
	fixture := newFixture(t)
	fixture.config.Accounts = append(fixture.config.Accounts, fixture.config.Accounts[0])
	fixture.config.Accounts[1].AccountID = "acct-b"
	var calls []string
	deps := fixtureDeps(fixture, &calls)
	listener := &flakyListener{calls: &calls, remainingFailures: 1}
	ownerships := []*flakyOwnership{
		{name: "acct-a", calls: &calls},
		{name: "acct-b", calls: &calls, remainingFailures: 1},
	}
	nextOwnership := 0
	deps.Listen = func(network, address string) (net.Listener, error) {
		calls = append(calls, "listen")
		return listener, nil
	}
	deps.AcquireOwnership = func(ctx context.Context, pool process.PostgresPool, accountID domain.AccountID) (process.Ownership, error) {
		calls = append(calls, "ownership:"+string(accountID))
		ownership := ownerships[nextOwnership]
		nextOwnership++
		return ownership, nil
	}

	service, err := process.Start(context.Background(), fixture.config, deps)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if ready := service.Health().Readiness(); !ready.Ready {
		t.Fatalf("readiness before shutdown = %+v, want ready", ready)
	}
	shutdownStart := len(calls)

	firstErr := service.Shutdown(context.Background())
	if firstErr == nil {
		t.Fatal("first Shutdown succeeded, want cleanup failure")
	}
	if ready := service.Health().Readiness(); ready.Ready {
		t.Fatalf("readiness after failed shutdown = %+v, want not ready", ready)
	}
	wantFirstShutdown := []string{
		"listener_close",
		"orders_close",
		"accounts_close",
		"release:acct-b",
		"release:acct-a",
	}
	if got := calls[shutdownStart:]; !reflect.DeepEqual(got, wantFirstShutdown) {
		t.Fatalf("calls from first Shutdown = %v, want %v", got, wantFirstShutdown)
	}

	if err := service.Shutdown(context.Background()); err != nil {
		t.Fatalf("second Shutdown: %v", err)
	}
	wantShutdownCalls := append(wantFirstShutdown, "listener_close", "release:acct-b", "pool_close")
	if got := calls[shutdownStart:]; !reflect.DeepEqual(got, wantShutdownCalls) {
		t.Fatalf("calls through second Shutdown = %v, want %v", got, wantShutdownCalls)
	}

	const concurrentCalls = 8
	done := make(chan error, concurrentCalls)
	for i := 0; i < concurrentCalls; i++ {
		go func() {
			done <- service.Shutdown(context.Background())
		}()
	}
	for i := 0; i < concurrentCalls; i++ {
		if err := <-done; err != nil {
			t.Fatalf("concurrent Shutdown %d: %v", i, err)
		}
	}
	if got := calls[shutdownStart:]; !reflect.DeepEqual(got, wantShutdownCalls) {
		t.Fatalf("calls after idempotent Shutdown = %v, want %v", got, wantShutdownCalls)
	}
}

func TestServiceDoesNotReleaseOwnershipUntilAccountClientsClose(t *testing.T) {
	fixture := newFixture(t)
	var calls []string
	deps := fixtureDeps(fixture, &calls)
	manager := &fakeAccountManager{calls: &calls, remainingFailures: 1}
	deps.BuildAccountManager = func(context.Context, []config.AccountConfig, map[string]string) (process.AccountManager, error) {
		calls = append(calls, "build_accounts")
		return manager, nil
	}
	service, err := process.Start(context.Background(), fixture.config, deps)
	if err != nil {
		t.Fatal(err)
	}
	start := len(calls)
	if err := service.Shutdown(context.Background()); err == nil {
		t.Fatal("first Shutdown succeeded, want account close failure")
	}
	if got := calls[start:]; !reflect.DeepEqual(got, []string{"listener_close", "orders_close", "accounts_close"}) {
		t.Fatalf("first shutdown calls=%v", got)
	}
	if err := service.Shutdown(context.Background()); err != nil {
		t.Fatalf("second Shutdown: %v", err)
	}
	if got := calls[start:]; !reflect.DeepEqual(got, []string{"listener_close", "orders_close", "accounts_close", "accounts_close", "release", "pool_close"}) {
		t.Fatalf("retry shutdown calls=%v", got)
	}
}

type fixture struct {
	config  config.File
	secrets map[string]string
	dsn     string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	authRef, _ := secret.ParseRef("env://TOKEN")
	dsnRef, _ := secret.ParseRef("env://POSTGRES_DSN")
	apiKeyRef, _ := secret.ParseRef("env://API_KEY")
	apiSecretRef, _ := secret.ParseRef("env://API_SECRET")
	return fixture{
		config: config.File{
			Server:   config.ServerConfig{Listen: "127.0.0.1:0", AuthTokenRef: authRef},
			Postgres: config.PostgresConfig{DSNRef: dsnRef},
			Accounts: []config.AccountConfig{{
				AccountID:   "acct-a",
				Venue:       "binance",
				Product:     "spot",
				Environment: "demo",
				Credentials: config.APIKeySecretCredentials{APIKeyRef: apiKeyRef, APISecretRef: apiSecretRef},
			}},
		},
		secrets: map[string]string{
			"env://TOKEN":        "auth-token",
			"env://POSTGRES_DSN": "postgres://user:pass@127.0.0.1/db?sslmode=disable",
			"env://API_KEY":      "key",
			"env://API_SECRET":   "secret",
		},
		dsn: "postgres://user:pass@127.0.0.1/db?sslmode=disable",
	}
}

func fixtureDeps(fixture fixture, calls *[]string) process.Dependencies {
	return process.Dependencies{
		ResolveSecret: func(ctx context.Context, ref secret.Ref) (string, error) {
			*calls = append(*calls, "resolve:"+ref.RawForProvider())
			return fixture.secrets[ref.RawForProvider()], nil
		},
		ConnectPostgres: func(ctx context.Context, dsn string) (process.PostgresPool, error) {
			*calls = append(*calls, "connect")
			return fakePool{calls: calls}, nil
		},
		Migrate: func(ctx context.Context, pool process.PostgresPool) error {
			*calls = append(*calls, "migrate")
			return nil
		},
		RegisterAccount: func(ctx context.Context, pool process.PostgresPool, registration process.AccountRegistration) error {
			*calls = append(*calls, "register:"+string(registration.AccountID))
			return nil
		},
		AcquireOwnership: func(ctx context.Context, pool process.PostgresPool, accountID domain.AccountID) (process.Ownership, error) {
			*calls = append(*calls, "ownership:"+string(accountID))
			return fakeOwnership{calls: calls}, nil
		},
		BuildAccountManager: func(context.Context, []config.AccountConfig, map[string]string) (process.AccountManager, error) {
			*calls = append(*calls, "build_accounts")
			return &fakeAccountManager{calls: calls}, nil
		},
		BuildOrderCoordinator: func(context.Context, process.PostgresPool, process.AccountManager) (process.OrderCoordinator, error) {
			*calls = append(*calls, "build_orders")
			return &fakeOrderCoordinator{calls: calls}, nil
		},
		Listen: func(network, address string) (net.Listener, error) {
			*calls = append(*calls, "listen")
			return newFakeListenerWithCalls(calls), nil
		},
	}
}

type fakePool struct{ calls *[]string }

func (pool fakePool) Ping(ctx context.Context) error {
	*pool.calls = append(*pool.calls, "ping")
	return nil
}

func (pool fakePool) Close() {
	*pool.calls = append(*pool.calls, "pool_close")
}

type fakeAccountManager struct {
	calls             *[]string
	remainingFailures int
	startErr          error
}

func (manager *fakeAccountManager) AccountIDs() []domain.AccountID {
	return []domain.AccountID{"acct-a"}
}

func (manager *fakeAccountManager) Start(context.Context) error {
	*manager.calls = append(*manager.calls, "accounts_start")
	return manager.startErr
}

func (manager *fakeAccountManager) Close() error {
	*manager.calls = append(*manager.calls, "accounts_close")
	if manager.remainingFailures > 0 {
		manager.remainingFailures--
		return errors.New("account manager close failed")
	}
	return nil
}

type fakeOrderCoordinator struct {
	calls             *[]string
	remainingFailures int
	recoverErr        error
}

func (coordinator *fakeOrderCoordinator) Start(_ context.Context, accountIDs []domain.AccountID) error {
	for _, accountID := range accountIDs {
		*coordinator.calls = append(*coordinator.calls, "orders_recover:"+string(accountID))
	}
	return coordinator.recoverErr
}

func (coordinator *fakeOrderCoordinator) Close(context.Context) error {
	*coordinator.calls = append(*coordinator.calls, "orders_close")
	if coordinator.remainingFailures > 0 {
		coordinator.remainingFailures--
		return errors.New("order coordinator close failed")
	}
	return nil
}

type fakeOwnership struct{ calls *[]string }

func (ownership fakeOwnership) Healthy() bool { return true }
func (ownership fakeOwnership) Release(ctx context.Context) error {
	*ownership.calls = append(*ownership.calls, "release")
	return nil
}

type fakeListener struct {
	net.Listener
	calls *[]string
}

func newFakeListener() *fakeListener {
	return newFakeListenerWithCalls(&[]string{})
}

func newFakeListenerWithCalls(calls *[]string) *fakeListener {
	return &fakeListener{Listener: &net.TCPListener{}, calls: calls}
}

func (listener *fakeListener) Close() error {
	*listener.calls = append(*listener.calls, "listener_close")
	return nil
}

type flakyOwnership struct {
	name              string
	calls             *[]string
	remainingFailures int
	released          bool
}

func (ownership *flakyOwnership) Healthy() bool { return !ownership.released }
func (ownership *flakyOwnership) Release(ctx context.Context) error {
	*ownership.calls = append(*ownership.calls, "release:"+ownership.name)
	if ownership.remainingFailures > 0 {
		ownership.remainingFailures--
		return errors.New("release failed")
	}
	ownership.released = true
	return nil
}

type flakyListener struct {
	net.Listener
	calls             *[]string
	remainingFailures int
	closed            bool
}

func (listener *flakyListener) Close() error {
	*listener.calls = append(*listener.calls, "listener_close")
	if listener.closed {
		return net.ErrClosed
	}
	if listener.remainingFailures > 0 {
		listener.remainingFailures--
		return errors.New("close failed")
	}
	listener.closed = true
	return nil
}
