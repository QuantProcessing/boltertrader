package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
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
	"github.com/QuantProcessing/boltertrader/gateway/reference"
	"github.com/QuantProcessing/boltertrader/gateway/secret"
	"github.com/QuantProcessing/boltertrader/gateway/store/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

type runtimeDeps struct {
	connectPostgres       func(context.Context, string) (process.PostgresPool, error)
	migrate               func(context.Context, process.PostgresPool) error
	registerAccount       func(context.Context, process.PostgresPool, process.AccountRegistration) error
	acquireOwnership      func(context.Context, process.PostgresPool, process.AccountRegistration) (process.Ownership, error)
	buildAccountManager   func(context.Context, []config.AccountConfig, map[string]string) (process.AccountManager, error)
	buildOrderCoordinator func(context.Context, process.PostgresPool, process.AccountManager) (process.OrderCoordinator, error)
	buildHandler          func(*health.Monitor, process.PostgresPool, process.AccountManager, process.OrderCoordinator) (http.Handler, error)
	buildInternalHandler  func(context.Context, process.PostgresPool, process.AccountManager) (http.Handler, error)
	listen                func(network, address string) (net.Listener, error)
	listenInternal        func(network, address string) (net.Listener, error)
	wait                  func(context.Context, *process.Service) error
	metrics               *observability.Metrics
	runtimeMode           account.RuntimeMode
	runtimeLifecycle      account.RuntimeLifecycle
	resolveRuntimeSecret  func(context.Context, secret.OpaqueRef) (string, error)
	activateRuntime       func(context.Context, account.DesiredRuntime, string) error
	deactivateRuntime     func(context.Context, domain.AccountID) error
	runtimeBlockers       func(context.Context, domain.AccountID) (account.RuntimeBlockers, error)
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
	registerAccount := defaultRegisterAccount(deps.registerAccount)
	acquireOwnership := defaultAcquireOwnership(deps.acquireOwnership)
	runtimeMode := deps.runtimeMode
	if runtimeMode == "" {
		runtimeMode = account.RuntimeModeTest
	}
	runtimeLifecycle := deps.runtimeLifecycle
	if file.Server.InternalListen != "" && deps.buildInternalHandler == nil && runtimeLifecycle == nil {
		runtimeLifecycle, err = newOpaqueRuntimeLifecycle(
			deps.resolveRuntimeSecret, deps.activateRuntime, deps.deactivateRuntime, deps.runtimeBlockers,
		)
		if err != nil {
			return fmt.Errorf("configure internal runtime lifecycle failed")
		}
	}
	publicHandlerFactory := defaultHandlerFactory(deps.buildHandler, authMiddleware, metrics)
	var internalHandler http.Handler
	var internalHandlerFactory func(context.Context, process.PostgresPool, process.AccountManager) (http.Handler, error)
	if file.Server.InternalListen != "" {
		internalAuth, authErr := auth.NewBearerMiddleware(ctx, provider, file.Server.InternalAuthTokenRef)
		if authErr != nil {
			return fmt.Errorf("configure internal auth failed")
		}
		internalHandlerFactory = defaultInternalHandlerFactory(deps.buildInternalHandler, internalAuth, runtimeMode)
	}
	processDeps := process.Dependencies{
		ResolveSecret:         provider.Resolve,
		ConnectPostgres:       observedConnectPostgres(metrics, connectPostgres),
		Migrate:               observedMigrate(metrics, migrate),
		RegisterAccount:       observedRegisterAccount(metrics, registerAccount),
		AcquireOwnership:      observedAcquireOwnership(metrics, acquireOwnership),
		BuildAccountManager:   defaultBuildAccountManager(deps.buildAccountManager, runtimeLifecycle, runtimeMode),
		BuildOrderCoordinator: defaultBuildOrderCoordinator(deps.buildOrderCoordinator, authMiddleware.PrincipalID(), metrics),
		Listen:                defaultListen(deps.listen),
		HandlerFactory: func(monitor *health.Monitor, pool process.PostgresPool, accounts process.AccountManager, orders process.OrderCoordinator) (http.Handler, error) {
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
			return publicHandler, nil
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
		internalService = startAuthorityHTTPServer(listener, internalHandler)
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

func defaultInternalHandlerFactory(
	override func(context.Context, process.PostgresPool, process.AccountManager) (http.Handler, error),
	authMiddleware auth.BearerMiddleware,
	mode account.RuntimeMode,
) func(context.Context, process.PostgresPool, process.AccountManager) (http.Handler, error) {
	return func(ctx context.Context, pool process.PostgresPool, accounts process.AccountManager) (http.Handler, error) {
		var handler http.Handler
		var err error
		if override != nil {
			handler, err = override(ctx, pool, accounts)
		} else {
			realPool, poolOK := pool.(*pgxpool.Pool)
			manager, managerOK := accounts.(*account.Manager)
			if !poolOK || !managerOK {
				return nil, fmt.Errorf("internal authority dependency type")
			}
			authority, authorityErr := newGatewayAuthority(ctx, postgres.NewStore(realPool), manager, mode)
			if authorityErr != nil {
				return nil, authorityErr
			}
			handler, err = httpapi.NewInternalHandler(httpapi.InternalOptions{
				Authority: authority,
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
		return authMiddleware.Wrap(handler), nil
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

func accountMetricSnapshot(manager *account.Manager) observability.SnapshotProvider {
	return func(_ time.Time) ([]observability.AccountState, []observability.CatalogState) {
		resources := manager.Accounts()
		accounts := make([]observability.AccountState, 0, len(resources))
		catalogs := make([]observability.CatalogState, 0, len(resources))
		for _, resource := range resources {
			status, err := manager.Status(resource.AccountID)
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

func observedRegisterAccount(
	metrics *observability.Metrics,
	register func(context.Context, process.PostgresPool, process.AccountRegistration) error,
) func(context.Context, process.PostgresPool, process.AccountRegistration) error {
	return func(ctx context.Context, pool process.PostgresPool, registration process.AccountRegistration) error {
		start := time.Now()
		err := register(ctx, pool, registration)
		metrics.ObservePostgres(observability.PostgresRegister, postgresErrorClass(err), time.Since(start))
		return err
	}
}

func observedAcquireOwnership(
	metrics *observability.Metrics,
	acquire func(context.Context, process.PostgresPool, domain.AccountID) (process.Ownership, error),
) func(context.Context, process.PostgresPool, domain.AccountID) (process.Ownership, error) {
	return func(ctx context.Context, pool process.PostgresPool, accountID domain.AccountID) (process.Ownership, error) {
		start := time.Now()
		ownership, err := acquire(ctx, pool, accountID)
		metrics.ObservePostgres(observability.PostgresOwnership, postgresErrorClass(err), time.Since(start))
		return ownership, err
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
	override func(context.Context, []config.AccountConfig, map[string]string) (process.AccountManager, error),
	lifecycle account.RuntimeLifecycle,
	mode account.RuntimeMode,
) func(context.Context, []config.AccountConfig, map[string]string) (process.AccountManager, error) {
	if override != nil {
		return override
	}
	return func(ctx context.Context, accounts []config.AccountConfig, resolved map[string]string) (process.AccountManager, error) {
		gatewayCatalog, err := catalog.NewService(5*time.Minute, time.Now)
		if err != nil {
			return nil, fmt.Errorf("catalog setup")
		}
		manager, err := account.NewManager(ctx, accounts, account.Options{
			Secrets:         secret.StaticProvider(resolved),
			ExchangeCatalog: exchange.NewDefaultMemoryCatalog(),
			GatewayCatalog:  gatewayCatalog,
			HTTPClient:      &http.Client{Timeout: 30 * time.Second},
			RuntimeMode:     mode,
			Lifecycle:       lifecycle,
		})
		if err != nil {
			return nil, fmt.Errorf("account manager setup")
		}
		return manager, nil
	}
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

func defaultRegisterAccount(override func(context.Context, process.PostgresPool, process.AccountRegistration) error) func(context.Context, process.PostgresPool, process.AccountRegistration) error {
	if override != nil {
		return override
	}
	return func(ctx context.Context, pool process.PostgresPool, registration process.AccountRegistration) error {
		realPool, ok := pool.(*pgxpool.Pool)
		if !ok {
			return fmt.Errorf("postgres pool type")
		}
		return postgres.NewStore(realPool).RegisterAccount(ctx, postgres.AccountRegistration{
			AccountID:   registration.AccountID,
			Venue:       registration.Venue,
			Product:     registration.Product,
			Environment: registration.Environment,
			Settlement:  registration.Settlement,
			ConfigHash:  registration.ConfigHash,
			Status:      registration.Status,
		})
	}
}

func defaultAcquireOwnership(override func(context.Context, process.PostgresPool, process.AccountRegistration) (process.Ownership, error)) func(context.Context, process.PostgresPool, domain.AccountID) (process.Ownership, error) {
	if override != nil {
		return func(ctx context.Context, pool process.PostgresPool, accountID domain.AccountID) (process.Ownership, error) {
			return override(ctx, pool, process.AccountRegistration{AccountID: accountID})
		}
	}
	return func(ctx context.Context, pool process.PostgresPool, accountID domain.AccountID) (process.Ownership, error) {
		realPool, ok := pool.(*pgxpool.Pool)
		if !ok {
			return nil, fmt.Errorf("postgres pool type")
		}
		return postgres.AcquireAccountOwnership(ctx, realPool, accountID)
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
