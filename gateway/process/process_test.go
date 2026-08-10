package process_test

import (
	"context"
	"errors"
	"fmt"
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
	storepostgres "github.com/QuantProcessing/boltertrader/gateway/store/postgres"
)

func TestServiceStartsWithDynamicZeroAccountRuntime(t *testing.T) {
	fixture := newFixture(t)
	var calls []string
	deps := fixtureDeps(fixture, &calls)

	service, err := process.Start(t.Context(), fixture.config, deps)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = service.Shutdown(context.Background()) }()

	want := []string{
		"resolve:env://TOKEN",
		"resolve:env://POSTGRES_DSN",
		"connect",
		"ping",
		"validate_identity",
		"verify_authority",
		"build_accounts",
		"accounts_start",
		"restore_accounts",
		"build_orders",
		"orders_start:0",
		"listen",
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("startup calls = %v, want %v", calls, want)
	}
	if ready := service.Health().Readiness(); !ready.Ready {
		t.Fatalf("readiness = %+v, want ready", ready)
	}
}

func TestMigrationValidationSkipsPrivateRuntimeAndCanonicalMutations(t *testing.T) {
	fixture := newFixture(t)
	fixture.config.Server.StartupProfile = config.StartupProfileMigrationValidation
	var calls []string
	deps := fixtureDeps(fixture, &calls)
	deps.CheckCredentialResolver = func(context.Context) error { calls = append(calls, "credential_check"); return nil }
	deps.VerifyDatabaseAuthority = func(context.Context, process.PostgresPool) (storepostgres.AuthorityResult, error) {
		calls = append(calls, "verify_writes")
		return storepostgres.AuthorityResult{}, nil
	}
	deps.ValidationHandlerFactory = func(*health.Monitor, process.PostgresPool) (http.Handler, error) {
		calls = append(calls, "build_validation_handler")
		return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), nil
	}
	service, err := process.Start(t.Context(), fixture.config, deps)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = service.Shutdown(context.Background()) }()
	want := []string{"resolve:env://TOKEN", "resolve:env://POSTGRES_DSN", "connect", "ping", "validate_identity", "verify_writes", "build_validation_handler", "listen"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("startup calls = %v, want %v", calls, want)
	}
	component := service.Health().Component("canonicalWrites")
	if component.Ready {
		t.Fatalf("canonicalWrites = %+v", component)
	}
}

func TestNormalReadOnlyStartupSkipsPrivateRuntimeWithoutWriterMembership(t *testing.T) {
	fixture := newFixture(t)
	var calls []string
	deps := fixtureDeps(fixture, &calls)
	deps.CheckCredentialResolver = func(context.Context) error {
		calls = append(calls, "credential_check")
		return nil
	}
	deps.VerifyDatabaseAuthority = func(context.Context, process.PostgresPool) (storepostgres.AuthorityResult, error) {
		calls = append(calls, "verify_writes")
		return storepostgres.AuthorityResult{}, nil
	}
	deps.ValidationHandlerFactory = func(monitor *health.Monitor, _ process.PostgresPool) (http.Handler, error) {
		calls = append(calls, "build_read_only_handler")
		if component := monitor.Component("canonicalWrites"); component.Ready || component.Reason != "writer_role_unavailable" {
			t.Fatalf("canonicalWrites = %+v", component)
		}
		return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), nil
	}

	service, err := process.Start(t.Context(), fixture.config, deps)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = service.Shutdown(context.Background()) }()
	want := []string{
		"credential_check", "resolve:env://TOKEN", "resolve:env://POSTGRES_DSN", "connect", "ping", "validate_identity", "verify_writes", "build_read_only_handler", "listen",
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("read-only startup calls = %v, want %v", calls, want)
	}
}

func TestServiceResolvesOptionalInternalAuthAsOperationalSecret(t *testing.T) {
	fixture := newFixture(t)
	internalRef, err := secret.ParseRef("env://INTERNAL_TOKEN")
	if err != nil {
		t.Fatal(err)
	}
	fixture.config.Server.InternalListen = "127.0.0.1:1"
	fixture.config.Server.InternalAuthTokenRef = internalRef
	fixture.secrets[internalRef.RawForProvider()] = "internal-token"
	var calls []string

	service, err := process.Start(t.Context(), fixture.config, fixtureDeps(fixture, &calls))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = service.Shutdown(context.Background()) }()
	if got := calls[:3]; !reflect.DeepEqual(got, []string{
		"resolve:env://TOKEN", "resolve:env://POSTGRES_DSN", "resolve:env://INTERNAL_TOKEN",
	}) {
		t.Fatalf("resolved refs = %v", got)
	}
}

func TestServiceBuildsHandlerAfterEmptyRecoveryAndBeforeListen(t *testing.T) {
	fixture := newFixture(t)
	var calls []string
	deps := fixtureDeps(fixture, &calls)
	deps.HandlerFactory = func(*health.Monitor, process.PostgresPool, process.AccountManager, process.OrderCoordinator) (http.Handler, error) {
		calls = append(calls, "build_handler")
		return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), nil
	}

	service, err := process.Start(t.Context(), fixture.config, deps)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = service.Shutdown(context.Background()) }()
	if recovery, handler, listen := indexOf(calls, "orders_start:0"), indexOf(calls, "build_handler"), indexOf(calls, "listen"); recovery < 0 || handler < 0 || listen < 0 || !(recovery < handler && handler < listen) {
		t.Fatalf("startup calls = %v", calls)
	}
}

func TestServiceRestoresDynamicAccountIDsBeforeOrderRecovery(t *testing.T) {
	fixture := newFixture(t)
	var calls []string
	deps := fixtureDeps(fixture, &calls)
	deps.RestoreAccountManager = func(_ context.Context, _ process.PostgresPool, accounts process.AccountManager) error {
		calls = append(calls, "restore_accounts")
		accounts.(*fakeAccountManager).accountIDs = []domain.AccountID{"restored-dynamic"}
		return nil
	}
	service, err := process.Start(t.Context(), fixture.config, deps)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = service.Shutdown(context.Background()) }()
	if restore, recovery := indexOf(calls, "restore_accounts"), indexOf(calls, "orders_start:1"); restore < 0 || recovery < 0 || restore >= recovery {
		t.Fatalf("startup calls = %v", calls)
	}
}

func TestServiceStartupFailureCleansDynamicRuntimeBeforePool(t *testing.T) {
	fixture := newFixture(t)
	var calls []string
	deps := fixtureDeps(fixture, &calls)
	deps.Listen = func(string, string) (net.Listener, error) {
		calls = append(calls, "listen")
		return nil, errors.New("bind failed with postgres://secret")
	}

	_, err := process.Start(t.Context(), fixture.config, deps)
	if err == nil {
		t.Fatal("Start succeeded")
	}
	if strings.Contains(err.Error(), "postgres://secret") {
		t.Fatalf("error leaked detail: %v", err)
	}
	wantSuffix := []string{"listen", "orders_close", "accounts_close", "pool_close"}
	if got := calls[len(calls)-len(wantSuffix):]; !reflect.DeepEqual(got, wantSuffix) {
		t.Fatalf("cleanup calls = %v, want suffix %v", calls, wantSuffix)
	}
}

func TestServiceRejectsWrongDatabaseIdentityBeforeAuthorityOrRuntimeBuild(t *testing.T) {
	fixture := newFixture(t)
	var calls []string
	deps := fixtureDeps(fixture, &calls)
	deps.ValidateDatabaseIdentity = func(context.Context, process.PostgresPool) error {
		calls = append(calls, "validate_identity")
		return errors.New("connected to forbidden_database as elevated_role")
	}

	_, err := process.Start(t.Context(), fixture.config, deps)
	if err == nil {
		t.Fatal("Start succeeded")
	}
	if strings.Contains(err.Error(), "forbidden_database") || strings.Contains(err.Error(), "elevated_role") {
		t.Fatalf("identity error leaked detail: %v", err)
	}
	if got := calls; !reflect.DeepEqual(got, []string{
		"resolve:env://TOKEN", "resolve:env://POSTGRES_DSN", "connect", "ping", "validate_identity", "pool_close",
	}) {
		t.Fatalf("calls = %v", got)
	}
}

func TestServiceFailsClosedOnDatabaseAuthorityErrorInEveryProfile(t *testing.T) {
	for _, profile := range []config.StartupProfile{"", config.StartupProfileMigrationValidation} {
		t.Run(string(profile), func(t *testing.T) {
			fixture := newFixture(t)
			fixture.config.Server.StartupProfile = profile
			var calls []string
			deps := fixtureDeps(fixture, &calls)
			deps.VerifyDatabaseAuthority = func(context.Context, process.PostgresPool) (storepostgres.AuthorityResult, error) {
				calls = append(calls, "verify_authority")
				return storepostgres.AuthorityResult{}, errors.New("postgres://secret structural drift")
			}
			if profile == config.StartupProfileMigrationValidation {
				deps.ValidationHandlerFactory = func(*health.Monitor, process.PostgresPool) (http.Handler, error) {
					return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), nil
				}
			}
			_, err := process.Start(t.Context(), fixture.config, deps)
			if err == nil || strings.Contains(err.Error(), "postgres://secret") {
				t.Fatalf("error=%v", err)
			}
			if indexOf(calls, "build_accounts") >= 0 || indexOf(calls, "listen") >= 0 {
				t.Fatalf("authority failure started runtime: %v", calls)
			}
		})
	}
}

func TestServiceShutdownRetriesAccountCloseBeforePool(t *testing.T) {
	fixture := newFixture(t)
	var calls []string
	deps := fixtureDeps(fixture, &calls)
	manager := &fakeAccountManager{calls: &calls, remainingCloseFailures: 1}
	deps.BuildAccountManager = func(context.Context) (process.AccountManager, error) {
		calls = append(calls, "build_accounts")
		return manager, nil
	}
	service, err := process.Start(t.Context(), fixture.config, deps)
	if err != nil {
		t.Fatal(err)
	}
	start := len(calls)
	if err := service.Shutdown(t.Context()); err == nil {
		t.Fatal("first Shutdown succeeded")
	}
	if indexOf(calls[start:], "pool_close") >= 0 {
		t.Fatalf("pool closed before account manager: %v", calls[start:])
	}
	if err := service.Shutdown(t.Context()); err != nil {
		t.Fatalf("second Shutdown: %v", err)
	}
	if got := calls[start:]; !reflect.DeepEqual(got, []string{
		"listener_close", "orders_close", "accounts_close", "accounts_close", "pool_close",
	}) {
		t.Fatalf("shutdown calls = %v", got)
	}
}

func indexOf(values []string, want string) int {
	for i, value := range values {
		if value == want {
			return i
		}
	}
	return -1
}

type fixture struct {
	config  config.File
	secrets map[string]string
	dsn     string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	authRef, err := secret.ParseRef("env://TOKEN")
	if err != nil {
		t.Fatal(err)
	}
	dsnRef, err := secret.ParseRef("env://POSTGRES_DSN")
	if err != nil {
		t.Fatal(err)
	}
	dsn := "postgres://user:pass@127.0.0.1/db?sslmode=disable"
	return fixture{
		config: config.File{
			Server:   config.ServerConfig{Listen: "127.0.0.1:0", AuthTokenRef: authRef},
			Postgres: config.PostgresConfig{DSNRef: dsnRef},
		},
		secrets: map[string]string{"env://TOKEN": "auth-token", "env://POSTGRES_DSN": dsn},
		dsn:     dsn,
	}
}

func fixtureDeps(fixture fixture, calls *[]string) process.Dependencies {
	return process.Dependencies{
		ResolveSecret: func(_ context.Context, ref secret.Ref) (string, error) {
			*calls = append(*calls, "resolve:"+ref.RawForProvider())
			return fixture.secrets[ref.RawForProvider()], nil
		},
		ConnectPostgres: func(_ context.Context, dsn string) (process.PostgresPool, error) {
			*calls = append(*calls, "connect")
			if dsn != fixture.dsn {
				return nil, errors.New("unexpected dsn")
			}
			return fakePool{calls: calls}, nil
		},
		ValidateDatabaseIdentity: func(context.Context, process.PostgresPool) error {
			*calls = append(*calls, "validate_identity")
			return nil
		},
		VerifyDatabaseAuthority: func(context.Context, process.PostgresPool) (storepostgres.AuthorityResult, error) {
			*calls = append(*calls, "verify_authority")
			return storepostgres.AuthorityResult{WriterMembership: true}, nil
		},
		BuildAccountManager: func(context.Context) (process.AccountManager, error) {
			*calls = append(*calls, "build_accounts")
			return &fakeAccountManager{calls: calls}, nil
		},
		RestoreAccountManager: func(_ context.Context, _ process.PostgresPool, accounts process.AccountManager) error {
			*calls = append(*calls, "restore_accounts")
			return nil
		},
		BuildOrderCoordinator: func(context.Context, process.PostgresPool, process.AccountManager) (process.OrderCoordinator, error) {
			*calls = append(*calls, "build_orders")
			return &fakeOrderCoordinator{calls: calls}, nil
		},
		Listen: func(string, string) (net.Listener, error) {
			*calls = append(*calls, "listen")
			return &fakeListener{Listener: &net.TCPListener{}, calls: calls}, nil
		},
	}
}

type fakePool struct{ calls *[]string }

func (pool fakePool) Ping(context.Context) error {
	*pool.calls = append(*pool.calls, "ping")
	return nil
}
func (pool fakePool) Close() { *pool.calls = append(*pool.calls, "pool_close") }

type fakeAccountManager struct {
	calls                  *[]string
	accountIDs             []domain.AccountID
	remainingCloseFailures int
}

func (manager *fakeAccountManager) AccountIDs() []domain.AccountID {
	return append([]domain.AccountID(nil), manager.accountIDs...)
}
func (manager *fakeAccountManager) Start(context.Context) error {
	*manager.calls = append(*manager.calls, "accounts_start")
	return nil
}
func (manager *fakeAccountManager) Close() error {
	*manager.calls = append(*manager.calls, "accounts_close")
	if manager.remainingCloseFailures > 0 {
		manager.remainingCloseFailures--
		return errors.New("account close failed")
	}
	return nil
}

type fakeOrderCoordinator struct{ calls *[]string }

func (coordinator *fakeOrderCoordinator) Start(_ context.Context, accountIDs []domain.AccountID) error {
	*coordinator.calls = append(*coordinator.calls, fmt.Sprintf("orders_start:%d", len(accountIDs)))
	return nil
}
func (coordinator *fakeOrderCoordinator) Close(context.Context) error {
	*coordinator.calls = append(*coordinator.calls, "orders_close")
	return nil
}

type fakeListener struct {
	net.Listener
	calls *[]string
}

func (listener *fakeListener) Close() error {
	*listener.calls = append(*listener.calls, "listener_close")
	return nil
}
