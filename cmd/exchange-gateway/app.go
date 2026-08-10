package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/account"
	"github.com/QuantProcessing/boltertrader/gateway/auth"
	"github.com/QuantProcessing/boltertrader/gateway/catalog"
	"github.com/QuantProcessing/boltertrader/gateway/config"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	gatewayevent "github.com/QuantProcessing/boltertrader/gateway/event"
	"github.com/QuantProcessing/boltertrader/gateway/health"
	"github.com/QuantProcessing/boltertrader/gateway/httpapi"
	"github.com/QuantProcessing/boltertrader/gateway/observability"
	"github.com/QuantProcessing/boltertrader/gateway/order"
	"github.com/QuantProcessing/boltertrader/gateway/process"
	"github.com/QuantProcessing/boltertrader/gateway/publicconn"
	"github.com/QuantProcessing/boltertrader/gateway/reference"
	"github.com/QuantProcessing/boltertrader/gateway/secret"
	"github.com/QuantProcessing/boltertrader/gateway/store/postgres"
	"github.com/QuantProcessing/boltertrader/routeprofile"
	"github.com/QuantProcessing/boltertrader/secretstore"
	"github.com/jackc/pgx/v5/pgxpool"
)

type runtimeDeps struct {
	checkCredentialResolver  func(context.Context) error
	connectPostgres          func(context.Context, string) (process.PostgresPool, error)
	validateDatabaseIdentity func(context.Context, process.PostgresPool) error
	verifyCanonicalWrites    func(context.Context, process.PostgresPool) (bool, error)
	migrate                  func(context.Context, process.PostgresPool) error
	buildAccountManager      func(context.Context) (process.AccountManager, error)
	restoreAccountManager    func(context.Context, process.PostgresPool, process.AccountManager) error
	buildOrderCoordinator    func(context.Context, process.PostgresPool, process.AccountManager) (process.OrderCoordinator, error)
	buildHandler             func(*health.Monitor, process.PostgresPool, process.AccountManager, process.OrderCoordinator) (http.Handler, error)
	buildInternalHandler     func(context.Context, process.PostgresPool, process.AccountManager) (http.Handler, error)
	buildValidationHandler   func(*health.Monitor, process.PostgresPool) (http.Handler, error)
	listen                   func(network, address string) (net.Listener, error)
	listenInternal           func(network, address string) (net.Listener, error)
	wait                     func(context.Context, *process.Service) error
	metrics                  *observability.Metrics
	runtimeMode              account.RuntimeMode
	runtimeLifecycle         account.RuntimeLifecycle
	resolveRuntimeSecret     func(context.Context, secret.OpaqueRef) (string, error)
	activateRuntime          func(context.Context, account.DesiredRuntime, string) error
	deactivateRuntime        func(context.Context, domain.AccountID) error
	runtimeBlockers          func(context.Context, domain.AccountID) (account.RuntimeBlockers, error)
	credentialResolver       account.ScopedCredentialResolver
}

func run(ctx context.Context, args []string, deps runtimeDeps) (returnErr error) {
	flags := flag.NewFlagSet("exchange-gateway", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	configPath := flags.String("config", "", "static gateway config path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *configPath == "" {
		return fmt.Errorf("-config is required")
	}
	data, err := os.ReadFile(*configPath)
	if err != nil {
		return fmt.Errorf("read config failed")
	}
	file, err := config.LoadYAML(data)
	if err != nil {
		return fmt.Errorf("load config failed: %w", err)
	}
	provider := secret.NewEnvProvider(allowedEnvRefs(file))
	authMiddleware, err := auth.NewBearerMiddleware(ctx, provider, file.Server.AuthTokenRef)
	if err != nil {
		return fmt.Errorf("configure auth failed")
	}
	metrics := deps.metrics
	if metrics == nil {
		metrics = observability.NewMetrics()
	}
	connectPostgres := defaultConnectPostgres(deps.connectPostgres)
	migrate := defaultMigrate(deps.migrate)
	runtimeMode := deps.runtimeMode
	if runtimeMode == "" {
		runtimeMode = account.RuntimeMode(file.Server.Mode)
	}
	validateDatabaseIdentity := defaultValidateDatabaseIdentity(deps.validateDatabaseIdentity, runtimeMode)
	credentialResolver := deps.credentialResolver
	var credentialProvider *secretstore.FilesystemProvider
	validationProfile := file.Server.StartupProfile == config.StartupProfileMigrationValidation
	if !validationProfile && credentialResolver == nil && deps.buildAccountManager == nil {
		credentialProvider, credentialResolver, err = newGatewayCredentialResolver(runtimeMode)
		if err != nil {
			return err
		}
		defer credentialProvider.Close()
	}
	checkCredentialResolver := deps.checkCredentialResolver
	if checkCredentialResolver == nil && credentialResolver != nil {
		checkCredentialResolver = credentialResolver.Check
	}
	runtimeLifecycle := deps.runtimeLifecycle
	var productionLifecycle *productionRuntimeLifecycle
	if !validationProfile && runtimeLifecycle == nil && (deps.resolveRuntimeSecret != nil || deps.activateRuntime != nil || deps.deactivateRuntime != nil || deps.runtimeBlockers != nil) {
		runtimeLifecycle, err = newOpaqueRuntimeLifecycle(
			deps.resolveRuntimeSecret, deps.activateRuntime, deps.deactivateRuntime, deps.runtimeBlockers,
		)
		if err != nil {
			return fmt.Errorf("configure internal runtime lifecycle failed")
		}
	}
	if !validationProfile && runtimeLifecycle == nil {
		productionLifecycle = &productionRuntimeLifecycle{}
		runtimeLifecycle = productionLifecycle
	}
	publicHandlerFactory := defaultHandlerFactory(deps.buildHandler, authMiddleware, metrics)
	var internalHandler http.Handler
	var restoredAuthority *gatewayAuthority
	var gatewayMonitor *health.Monitor
	var internalAuth auth.BearerMiddleware
	var internalHandlerFactory func(context.Context, process.PostgresPool, process.AccountManager) (http.Handler, error)
	if file.Server.InternalListen != "" {
		var authErr error
		internalAuth, authErr = auth.NewBearerMiddleware(ctx, provider, file.Server.InternalAuthTokenRef)
		if authErr != nil {
			return fmt.Errorf("configure internal auth failed")
		}
		internalHandlerFactory = defaultInternalHandlerFactory(deps.buildInternalHandler, internalAuth, func() *gatewayAuthority {
			return restoredAuthority
		}, func() bool { return gatewayMonitor != nil && gatewayMonitor.Component("canonicalWrites").Ready })
	}
	processDeps := process.Dependencies{
		CheckCredentialResolver:  checkCredentialResolver,
		ResolveSecret:            provider.Resolve,
		ConnectPostgres:          observedConnectPostgres(metrics, connectPostgres),
		ValidateDatabaseIdentity: validateDatabaseIdentity,
		VerifyCanonicalWrites:    defaultVerifyCanonicalWrites(deps.verifyCanonicalWrites, runtimeMode),
		Migrate:                  observedMigrate(metrics, migrate),
		BuildAccountManager:      defaultBuildAccountManager(deps.buildAccountManager, runtimeLifecycle, credentialResolver, runtimeMode),
		RestoreAccountManager: func(restoreCtx context.Context, pool process.PostgresPool, manager process.AccountManager) error {
			if deps.restoreAccountManager != nil {
				return deps.restoreAccountManager(restoreCtx, pool, manager)
			}
			realPool, poolOK := pool.(*pgxpool.Pool)
			realManager, managerOK := manager.(*account.Manager)
			if !poolOK || !managerOK {
				return fmt.Errorf("runtime restore dependency type")
			}
			if productionLifecycle != nil {
				if attachErr := productionLifecycle.attach(realPool, realManager); attachErr != nil {
					return attachErr
				}
			}
			authority, restoreErr := newGatewayAuthority(restoreCtx, postgres.NewStore(realPool), realManager, runtimeMode)
			if restoreErr != nil {
				return restoreErr
			}
			restoredAuthority = authority
			return nil
		},
		BuildOrderCoordinator: defaultBuildOrderCoordinator(deps.buildOrderCoordinator, authMiddleware.PrincipalID(), metrics),
		Listen:                defaultListen(deps.listen),
		HandlerFactory: func(monitor *health.Monitor, pool process.PostgresPool, accounts process.AccountManager, orders process.OrderCoordinator) (http.Handler, error) {
			gatewayMonitor = monitor
			publicHandler, handlerErr := publicHandlerFactory(monitor, pool, accounts, orders)
			if handlerErr != nil {
				return nil, handlerErr
			}
			if internalHandlerFactory != nil {
				internalHandler, handlerErr = internalHandlerFactory(ctx, pool, accounts)
				if handlerErr != nil {
					return nil, handlerErr
				}
			}
			return modeAttestedHandler(runtimeMode, publicHandler), nil
		},
		ValidationHandlerFactory: func(monitor *health.Monitor, pool process.PostgresPool) (http.Handler, error) {
			gatewayMonitor = monitor
			if file.Server.InternalListen != "" {
				if deps.buildInternalHandler != nil {
					var handlerErr error
					internalHandler, handlerErr = deps.buildInternalHandler(ctx, pool, nil)
					if handlerErr != nil {
						return nil, handlerErr
					}
				} else {
					realPool, ok := pool.(*pgxpool.Pool)
					if !ok {
						return nil, fmt.Errorf("postgres pool type")
					}
					var handlerErr error
					internalHandler, handlerErr = httpapi.NewInternalHandler(httpapi.InternalOptions{
						Authority: validationSnapshotAuthority{snapshots: postgres.NewStore(realPool), mode: runtimeMode},
						Authenticate: func(request *http.Request) error {
							if _, ok := auth.Principal(request.Context()); !ok {
								return errors.New("internal authentication required")
							}
							return nil
						},
						WriteReady: func() bool { return monitor.Component("canonicalWrites").Ready },
					})
					if handlerErr != nil {
						return nil, handlerErr
					}
				}
				internalHandler = internalAuth.Wrap(httpapi.NewWriteGate(func() bool { return monitor.Component("canonicalWrites").Ready }).Wrap(internalHandler))
			}
			if deps.buildValidationHandler != nil {
				return deps.buildValidationHandler(monitor, pool)
			}
			return authMiddleware.Wrap(httpapi.NewMigrationValidationHandler(monitor)), nil
		},
	}
	service, err := process.Start(ctx, file, processDeps)
	if err != nil {
		return err
	}
	var internalService *authorityHTTPServer
	if file.Server.InternalListen != "" {
		if internalHandler == nil {
			_ = service.Shutdown(context.Background())
			return fmt.Errorf("internal HTTP API setup")
		}
		listener, listenErr := defaultListen(deps.listenInternal)("tcp", file.Server.InternalListen)
		if listenErr != nil {
			_ = service.Shutdown(context.Background())
			return fmt.Errorf("internal listen failed")
		}
		internalService = startAuthorityHTTPServer(listener, modeAttestedHandler(runtimeMode, internalHandler))
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if internalService != nil {
			if err := internalService.Shutdown(shutdownCtx); err != nil {
				returnErr = errors.Join(returnErr, err)
			}
		}
		if err := service.Shutdown(shutdownCtx); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				metrics.RecordShutdown(observability.ShutdownTimedOut)
			} else {
				metrics.RecordShutdown(observability.ShutdownError)
			}
			returnErr = errors.Join(returnErr, err)
		} else {
			metrics.RecordShutdown(observability.ShutdownDrained)
		}
	}()
	wait := deps.wait
	if wait == nil {
		wait = waitForSignal
	}
	return wait(ctx, service)
}

const gatewayModeHeader = "X-Bolter-Mode"

func modeAttestedHandler(mode account.RuntimeMode, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(gatewayModeHeader, string(mode))
		if r.Header.Get(gatewayModeHeader) != string(mode) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
				"code": "MODE_ATTESTATION_REQUIRED", "message": "request denied",
				"requestId": r.Header.Get("X-Request-ID"), "retryable": false,
			}})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func defaultInternalHandlerFactory(
	override func(context.Context, process.PostgresPool, process.AccountManager) (http.Handler, error),
	authMiddleware auth.BearerMiddleware,
	authorityProvider func() *gatewayAuthority,
	writeReady func() bool,
) func(context.Context, process.PostgresPool, process.AccountManager) (http.Handler, error) {
	return func(ctx context.Context, pool process.PostgresPool, accounts process.AccountManager) (http.Handler, error) {
		var handler http.Handler
		var err error
		if override != nil {
			handler, err = override(ctx, pool, accounts)
		} else {
			authority := authorityProvider()
			if authority == nil {
				return nil, fmt.Errorf("internal authority is unavailable")
			}
			handler, err = httpapi.NewInternalHandler(httpapi.InternalOptions{
				Authority:  authority,
				WriteReady: writeReady,
				Authenticate: func(request *http.Request) error {
					if _, ok := auth.Principal(request.Context()); !ok {
						return errors.New("internal authentication required")
					}
					return nil
				},
			})
		}
		if err != nil || handler == nil {
			return nil, fmt.Errorf("internal HTTP API setup")
		}
		return authMiddleware.Wrap(httpapi.NewWriteGate(writeReady).Wrap(handler)), nil
	}
}

type authorityHTTPServer struct {
	server   *http.Server
	listener net.Listener
	done     chan error
}

func startAuthorityHTTPServer(listener net.Listener, handler http.Handler) *authorityHTTPServer {
	service := &authorityHTTPServer{
		server:   &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second},
		listener: listener,
		done:     make(chan error, 1),
	}
	go func() {
		err := service.server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		service.done <- err
	}()
	return service
}

func (service *authorityHTTPServer) Shutdown(ctx context.Context) error {
	if service == nil || service.server == nil {
		return nil
	}
	err := service.server.Shutdown(ctx)
	if closeErr := service.listener.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
		err = errors.Join(err, closeErr)
	}
	return err
}

func defaultHandlerFactory(
	override func(*health.Monitor, process.PostgresPool, process.AccountManager, process.OrderCoordinator) (http.Handler, error),
	authMiddleware auth.BearerMiddleware,
	metrics *observability.Metrics,
) func(*health.Monitor, process.PostgresPool, process.AccountManager, process.OrderCoordinator) (http.Handler, error) {
	if override != nil {
		return override
	}
	return func(monitor *health.Monitor, pool process.PostgresPool, accounts process.AccountManager, orders process.OrderCoordinator) (http.Handler, error) {
		realPool, ok := pool.(*pgxpool.Pool)
		if !ok {
			return nil, fmt.Errorf("postgres pool type")
		}
		manager, ok := accounts.(*account.Manager)
		if !ok {
			return nil, fmt.Errorf("account manager type")
		}
		metrics.SetSnapshotProvider(accountMetricSnapshot(manager))
		metrics.SetReconciliationSnapshotProvider(postgres.NewStore(realPool).ReconciliationStatus)
		coordinator, ok := orders.(processOrderCoordinator)
		if !ok || coordinator.Coordinator == nil {
			return nil, fmt.Errorf("order coordinator type")
		}
		references, err := reference.NewService(manager)
		if err != nil {
			return nil, fmt.Errorf("reference service setup")
		}
		handler, err := httpapi.NewHandler(httpapi.Options{
			Monitor: monitor, Accounts: manager, References: references,
			Orders:   coordinator.Coordinator,
			Events:   coordinator.Events,
			Auditor:  postgres.NewStore(realPool),
			Identity: domain.NewIdentityGenerator(domain.IdentityGeneratorOptions{}),
		})
		if err != nil {
			return nil, fmt.Errorf("HTTP API setup")
		}
		return authMiddleware.Wrap(withOperationalHTTP(handler, metrics)), nil
	}
}

func withOperationalHTTP(api http.Handler, metrics *observability.Metrics) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/metrics", metrics.Handler())
	mux.Handle("/", metrics.CommandMiddleware(api))
	return mux
}

type accountMetricSource interface {
	Accounts() []account.Resource
	RouteStatus(domain.AccountID, string, string) (account.Status, error)
}

func accountMetricSnapshot(manager accountMetricSource) observability.SnapshotProvider {
	return func(_ time.Time) ([]observability.AccountState, []observability.CatalogState) {
		resources := manager.Accounts()
		accounts := make([]observability.AccountState, 0, len(resources))
		catalogs := make([]observability.CatalogState, 0, len(resources))
		for _, resource := range resources {
			status, err := manager.RouteStatus(resource.AccountID, string(resource.Product), resource.Settlement)
			if err != nil {
				continue
			}
			accounts = append(accounts, observability.AccountState{
				Venue: string(resource.Venue), Product: string(resource.Product),
				Environment: string(resource.Environment),
				ReadReady:   status.ReadReady, WriteReady: status.WriteReady,
			})
			catalogs = append(catalogs, observability.CatalogState{
				Venue: string(resource.Venue), Product: string(resource.Product),
				Environment: string(resource.Environment),
				LoadedAt:    status.Catalog.LoadedAt, Generation: status.Catalog.Generation,
			})
		}
		return accounts, catalogs
	}
}

func defaultBuildOrderCoordinator(
	override func(context.Context, process.PostgresPool, process.AccountManager) (process.OrderCoordinator, error),
	principalID domain.PrincipalID,
	metrics *observability.Metrics,
) func(context.Context, process.PostgresPool, process.AccountManager) (process.OrderCoordinator, error) {
	if override != nil {
		return override
	}
	return func(_ context.Context, pool process.PostgresPool, accounts process.AccountManager) (process.OrderCoordinator, error) {
		realPool, ok := pool.(*pgxpool.Pool)
		if !ok {
			return nil, fmt.Errorf("postgres pool type")
		}
		manager, ok := accounts.(*account.Manager)
		if !ok {
			return nil, fmt.Errorf("account manager type")
		}
		router, err := order.NewAccountRouter(manager)
		if err != nil {
			return nil, fmt.Errorf("order router setup")
		}
		identity := domain.NewIdentityGenerator(domain.IdentityGeneratorOptions{})
		events, err := gatewayevent.NewHub(gatewayevent.Options{Identity: identity, Observer: metrics})
		if err != nil {
			return nil, fmt.Errorf("event hub setup")
		}
		coordinator, err := order.NewCoordinator(order.Options{
			Store: postgres.NewStore(realPool), Router: router,
			Publisher: events,
			Identity:  identity,
		})
		if err != nil {
			events.Close()
			return nil, fmt.Errorf("order coordinator setup")
		}
		streams, err := gatewayevent.NewStreamManager(gatewayevent.StreamOptions{
			Accounts: manager, Reconciler: coordinator, Hub: events, PrincipalID: principalID,
		})
		if err != nil {
			events.Close()
			return nil, fmt.Errorf("private stream manager setup")
		}
		if err := manager.SetRuntimeAccountObserver(newRuntimeAccountObserver(streams, coordinator)); err != nil {
			streams.Close()
			events.Close()
			return nil, fmt.Errorf("private stream account observer setup")
		}
		return processOrderCoordinator{Coordinator: coordinator, Events: events, Streams: streams}, nil
	}
}

func observedConnectPostgres(
	metrics *observability.Metrics,
	connect func(context.Context, string) (process.PostgresPool, error),
) func(context.Context, string) (process.PostgresPool, error) {
	return func(ctx context.Context, dsn string) (process.PostgresPool, error) {
		start := time.Now()
		pool, err := connect(ctx, dsn)
		metrics.ObservePostgres(observability.PostgresConnect, postgresErrorClass(err), time.Since(start))
		return pool, err
	}
}

func observedMigrate(
	metrics *observability.Metrics,
	migrate func(context.Context, process.PostgresPool) error,
) func(context.Context, process.PostgresPool) error {
	return func(ctx context.Context, pool process.PostgresPool) error {
		start := time.Now()
		err := migrate(ctx, pool)
		metrics.ObservePostgres(observability.PostgresMigrate, postgresErrorClass(err), time.Since(start))
		return err
	}
}

func postgresErrorClass(err error) observability.PostgresErrorClass {
	switch {
	case err == nil:
		return observability.PostgresOK
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return observability.PostgresUnavailable
	default:
		return observability.PostgresInternal
	}
}

type runtimeAccountObserver struct {
	observers []account.RuntimeAccountObserver
}

func newRuntimeAccountObserver(observers ...account.RuntimeAccountObserver) *runtimeAccountObserver {
	return &runtimeAccountObserver{observers: append([]account.RuntimeAccountObserver(nil), observers...)}
}

func (observer *runtimeAccountObserver) RegisterAccount(accountID domain.AccountID) error {
	registered := 0
	for _, current := range observer.observers {
		if err := current.RegisterAccount(accountID); err != nil {
			for index := registered - 1; index >= 0; index-- {
				observer.observers[index].UnregisterAccount(accountID)
			}
			return err
		}
		registered++
	}
	return nil
}

func (observer *runtimeAccountObserver) UnregisterAccount(accountID domain.AccountID) {
	for index := len(observer.observers) - 1; index >= 0; index-- {
		observer.observers[index].UnregisterAccount(accountID)
	}
}

type processOrderCoordinator struct {
	*order.Coordinator
	Events  *gatewayevent.Hub
	Streams *gatewayevent.StreamManager
}

func (coordinator processOrderCoordinator) Start(ctx context.Context, accountIDs []domain.AccountID) error {
	if err := coordinator.Coordinator.Start(ctx, accountIDs); err != nil {
		return err
	}
	if coordinator.Streams != nil {
		if err := coordinator.Streams.Start(ctx, accountIDs); err != nil {
			_ = coordinator.Coordinator.Shutdown(context.Background())
			return err
		}
	}
	return nil
}

func (coordinator processOrderCoordinator) Close(ctx context.Context) error {
	if coordinator.Streams != nil {
		coordinator.Streams.Close()
	}
	err := coordinator.Coordinator.Shutdown(ctx)
	if coordinator.Events != nil {
		coordinator.Events.Close()
	}
	return err
}

func defaultBuildAccountManager(
	override func(context.Context) (process.AccountManager, error),
	lifecycle account.RuntimeLifecycle,
	credentialResolver account.ScopedCredentialResolver,
	mode account.RuntimeMode,
) func(context.Context) (process.AccountManager, error) {
	if override != nil {
		return override
	}
	return func(ctx context.Context) (process.AccountManager, error) {
		gatewayCatalog, err := catalog.NewService(5*time.Minute, time.Now)
		if err != nil {
			return nil, fmt.Errorf("catalog setup")
		}
		exchangeCatalog := exchange.NewDefaultMemoryCatalog()
		httpClient := &http.Client{Timeout: 30 * time.Second}
		trustedProfiles := routeprofile.DefaultRegistry()
		publicFactory, err := publicconn.NewExchangeFactory(httpClient, trustedProfiles)
		if err != nil {
			return nil, fmt.Errorf("public connector factory setup")
		}
		publicConnectors, err := publicconn.NewRegistry(publicFactory, trustedProfiles)
		if err != nil {
			return nil, fmt.Errorf("public connector registry setup")
		}
		manager, err := account.NewManager(ctx, nil, account.Options{
			ExchangeCatalog:         exchangeCatalog,
			GatewayCatalog:          gatewayCatalog,
			HTTPClient:              httpClient,
			RuntimeMode:             mode,
			Lifecycle:               lifecycle,
			CredentialResolver:      credentialResolver,
			RuntimeGroupBuilder:     account.NewFactoryRuntimeGroupBuilder(httpClient, exchangeCatalog),
			ProfileRegistry:         account.NewProfileRegistry(trustedProfiles),
			PublicConnectors:        publicConnectors,
			RequirePublicConnectors: true,
		})
		if err != nil {
			_ = publicConnectors.Close()
			return nil, fmt.Errorf("account manager setup")
		}
		return manager, nil
	}
}

type gatewayKeychainRunner struct{}

func (gatewayKeychainRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

func newGatewayCredentialResolver(mode account.RuntimeMode) (*secretstore.FilesystemProvider, account.ScopedCredentialResolver, error) {
	keySourceName := strings.TrimSpace(os.Getenv("BT_SECRET_KEY_SOURCE"))
	stateDir := strings.TrimSpace(os.Getenv("BT_SECRET_STORE_DIR"))
	namespace := strings.TrimSpace(os.Getenv("BT_SECRET_STORE_NAMESPACE"))
	keyVersion := strings.TrimSpace(os.Getenv("BT_SECRET_MASTER_KEY_VERSION"))
	secretMode := secretstore.Mode(mode)
	if !filepath.IsAbs(stateDir) || namespace != "boltertrader/"+string(mode) || keyVersion != "v1" || (secretMode != secretstore.ModeTest && secretMode != secretstore.ModeLive) {
		return nil, nil, fmt.Errorf("credential resolver configuration is invalid")
	}
	var keySource secretstore.KeySource
	switch keySourceName {
	case "file":
		keyFile := strings.TrimSpace(os.Getenv("BT_SECRET_MASTER_KEY_FILE"))
		if !filepath.IsAbs(keyFile) || os.Getenv("BT_SECRET_KEYCHAIN_SERVICE") != "" || os.Getenv("BT_SECRET_KEYCHAIN_ACCOUNT") != "" {
			return nil, nil, fmt.Errorf("credential resolver key source is invalid")
		}
		keySource = secretstore.FileKeySource{Path: keyFile}
	case "keychain":
		service := strings.TrimSpace(os.Getenv("BT_SECRET_KEYCHAIN_SERVICE"))
		keyAccount := strings.TrimSpace(os.Getenv("BT_SECRET_KEYCHAIN_ACCOUNT"))
		if service == "" || keyAccount == "" || os.Getenv("BT_SECRET_MASTER_KEY_FILE") != "" {
			return nil, nil, fmt.Errorf("credential resolver key source is invalid")
		}
		keySource = secretstore.KeychainSource{Service: service, Account: keyAccount, Runner: gatewayKeychainRunner{}}
	default:
		return nil, nil, fmt.Errorf("credential resolver key source is invalid")
	}
	provider, err := secretstore.NewFilesystemProvider(secretstore.FilesystemConfig{StateDir: stateDir, Mode: secretMode, KeySource: keySource})
	if err != nil {
		return nil, nil, fmt.Errorf("initialize credential resolver failed")
	}
	resolver, err := secret.NewScopedCredentialResolver(provider.Resolver(), secretstore.Scope{Mode: secretMode, Namespace: namespace, KeyVersion: keyVersion})
	if err != nil {
		provider.Close()
		return nil, nil, fmt.Errorf("initialize credential resolver failed")
	}
	return provider, resolver, nil
}

func defaultConnectPostgres(override func(context.Context, string) (process.PostgresPool, error)) func(context.Context, string) (process.PostgresPool, error) {
	if override != nil {
		return override
	}
	return func(ctx context.Context, dsn string) (process.PostgresPool, error) {
		pool, err := pgxpool.New(ctx, dsn)
		if err != nil {
			return nil, fmt.Errorf("connect postgres")
		}
		return pool, nil
	}
}

func defaultValidateDatabaseIdentity(override func(context.Context, process.PostgresPool) error, mode account.RuntimeMode) func(context.Context, process.PostgresPool) error {
	if override != nil {
		return override
	}
	return func(ctx context.Context, pool process.PostgresPool) error {
		if mode != account.RuntimeModeTest && mode != account.RuntimeModeLive {
			return fmt.Errorf("postgres identity configuration is invalid")
		}
		expectedIdentity := "bolter_gateway_" + string(mode)
		realPool, ok := pool.(*pgxpool.Pool)
		if !ok {
			return fmt.Errorf("postgres pool type")
		}
		var database, role string
		if err := realPool.QueryRow(ctx, `SELECT current_database(), current_user`).Scan(&database, &role); err != nil {
			return fmt.Errorf("query postgres identity")
		}
		return validateConnectedDatabaseIdentity(database, role, expectedIdentity)
	}
}

func validateConnectedDatabaseIdentity(database, role, expectedIdentity string) error {
	if database != expectedIdentity || role != expectedIdentity {
		return fmt.Errorf("postgres identity mismatch")
	}
	return nil
}

func defaultVerifyCanonicalWrites(override func(context.Context, process.PostgresPool) (bool, error), mode account.RuntimeMode) func(context.Context, process.PostgresPool) (bool, error) {
	if override != nil {
		return override
	}
	return func(ctx context.Context, pool process.PostgresPool) (bool, error) {
		realPool, ok := pool.(*pgxpool.Pool)
		if !ok {
			return false, fmt.Errorf("postgres pool type")
		}
		writerRole := "bolter_gateway_" + string(mode) + "_writer"
		var ready bool
		if err := realPool.QueryRow(ctx, `SELECT pg_has_role(current_user, $1, 'USAGE')`, writerRole).Scan(&ready); err != nil {
			return false, fmt.Errorf("verify gateway writer authority")
		}
		return ready, nil
	}
}

func defaultMigrate(override func(context.Context, process.PostgresPool) error) func(context.Context, process.PostgresPool) error {
	if override != nil {
		return override
	}
	return func(ctx context.Context, pool process.PostgresPool) error {
		realPool, ok := pool.(*pgxpool.Pool)
		if !ok {
			return fmt.Errorf("postgres pool type")
		}
		return postgres.Migrate(ctx, realPool)
	}
}

func defaultListen(override func(network, address string) (net.Listener, error)) func(network, address string) (net.Listener, error) {
	if override != nil {
		return override
	}
	return net.Listen
}

func waitForSignal(ctx context.Context, service *process.Service) error {
	signalCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := service.Done()
	if done == nil {
		<-signalCtx.Done()
		return nil
	}
	select {
	case err := <-done:
		if err != nil {
			return err
		}
		return nil
	case <-signalCtx.Done():
		return nil
	}
}

func allowedEnvRefs(file config.File) []string {
	secretRefs := file.SecretRefs()
	refs := make([]string, 0, len(secretRefs))
	for _, ref := range secretRefs {
		refs = append(refs, ref.RawForProvider())
	}
	return refs
}
