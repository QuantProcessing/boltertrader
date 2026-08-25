package main

import (
	"context"
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
	"github.com/QuantProcessing/boltertrader/gateway/account/authorityclient"
	"github.com/QuantProcessing/boltertrader/gateway/account/lifecycle"
	"github.com/QuantProcessing/boltertrader/gateway/auth"
	"github.com/QuantProcessing/boltertrader/gateway/authority"
	"github.com/QuantProcessing/boltertrader/gateway/catalog"
	"github.com/QuantProcessing/boltertrader/gateway/config"
	"github.com/QuantProcessing/boltertrader/gateway/connectivity"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	gatewayevent "github.com/QuantProcessing/boltertrader/gateway/event"
	"github.com/QuantProcessing/boltertrader/gateway/fundingboard"
	"github.com/QuantProcessing/boltertrader/gateway/fundingsettlement"
	"github.com/QuantProcessing/boltertrader/gateway/health"
	"github.com/QuantProcessing/boltertrader/gateway/httpapi"
	"github.com/QuantProcessing/boltertrader/gateway/markethub"
	"github.com/QuantProcessing/boltertrader/gateway/marketowner"
	"github.com/QuantProcessing/boltertrader/gateway/observability"
	"github.com/QuantProcessing/boltertrader/gateway/order"
	"github.com/QuantProcessing/boltertrader/gateway/process"
	"github.com/QuantProcessing/boltertrader/gateway/publicconn"
	"github.com/QuantProcessing/boltertrader/gateway/publicmarket"
	"github.com/QuantProcessing/boltertrader/gateway/reference"
	"github.com/QuantProcessing/boltertrader/gateway/secret"
	"github.com/QuantProcessing/boltertrader/gateway/store/postgres"
	"github.com/QuantProcessing/boltertrader/internal/livewrite"
	"github.com/QuantProcessing/boltertrader/routeprofile"
	"github.com/QuantProcessing/boltertrader/secretstore"
	"github.com/jackc/pgx/v5/pgxpool"
)

type runtimeDeps struct {
	checkCredentialResolver  func(context.Context) error
	connectPostgres          func(context.Context, string) (process.PostgresPool, error)
	validateDatabaseIdentity func(context.Context, process.PostgresPool) error
	verifyDatabaseAuthority  func(context.Context, process.PostgresPool) (postgres.AuthorityResult, error)
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
	validateDatabaseIdentity := defaultValidateDatabaseIdentity(deps.validateDatabaseIdentity)
	credentialResolver := deps.credentialResolver
	var credentialProvider *secretstore.FilesystemProvider
	validationProfile := file.Server.StartupProfile == config.StartupProfileMigrationValidation
	if !validationProfile && credentialResolver == nil && deps.buildAccountManager == nil {
		credentialProvider, credentialResolver, err = newGatewayCredentialResolver()
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
	var venueConnectivity *connectivity.Manager
	if !validationProfile {
		venueConnectivity, err = connectivity.New(connectivity.Options{
			Checkers: connectivity.DefaultHTTPCheckers(&http.Client{Timeout: 3 * time.Second}),
			Interval: 10 * time.Second, Timeout: 3 * time.Second, Observer: metrics,
		})
		if err != nil {
			return fmt.Errorf("venue connectivity setup failed")
		}
		go func() { _ = venueConnectivity.Run(ctx) }()
	}
	// publicMarkets backs the catalog, order-book and candle authority without
	// borrowing an account runtime. Construction is local; one background loop
	// owns venue I/O and one failed venue only removes its own catalog column.
	var publicMarkets *publicmarket.Source
	var freshBBO *markethub.BBOCache
	var marketHub *markethub.Hub
	if !validationProfile {
		marketFactory, marketErr := publicmarket.NewExchangeFactory(&http.Client{Timeout: 30 * time.Second})
		if marketErr != nil {
			return fmt.Errorf("public market factory setup failed")
		}
		publicMarkets, marketErr = publicmarket.NewSource(marketFactory, routeprofile.DefaultRegistry())
		if marketErr != nil {
			return fmt.Errorf("public market source setup failed")
		}
		go func() { _ = publicMarkets.Run(ctx) }()
		freshBBO, marketErr = markethub.NewBBOCache(publicMarkets, time.Now)
		if marketErr != nil {
			return fmt.Errorf("fresh BBO cache setup failed")
		}
		freshBBO.SetObserver(metrics)
		defer publicMarkets.Close()
	}
	// fundingBoard is the resident DerivativeFact projection consumed by the
	// unified internal market socket and the retained REST snapshot/history
	// reads. It is built here, beside
	// the process wiring, rather than inside defaultBuildAccountManager because
	// that constructor early-returns a test-injected manager: assembling the
	// board there would make the matrix vanish under exactly the harnesses that
	// exercise the internal plane. Nothing about it is account-derived, so it
	// has no reason to live behind the manager anyway.
	//
	// A failure to build it is not a failure to boot. The board is one read of
	// many, and refusing to start the gateway because a public rate feed could
	// not be assembled would trade an entire trading surface for a table.
	var fundingBoard *fundingboard.Source
	if !validationProfile {
		boardFactory, boardErr := fundingboard.NewExchangeFactory(&http.Client{Timeout: 30 * time.Second})
		if boardErr == nil {
			fundingBoard, boardErr = fundingboard.NewSource(boardFactory, routeprofile.DefaultRegistry())
		}
		if boardErr != nil {
			fundingBoard = nil
		}
		if fundingBoard != nil {
			fundingBoard.SetObserver(metrics)
			go func() { _ = fundingBoard.Run(ctx) }()
		}
		defer func() {
			if fundingBoard != nil {
				_ = fundingBoard.Close()
			}
		}()
	}
	if !validationProfile {
		marketHub, err = markethub.New(markethub.Options{
			Owner: marketowner.Owner{
				Source: publicMarkets,
				BoardMarks: marketowner.BoardMarks{
					Board: fundingBoard, Markets: publicMarkets,
				},
			},
			IdleGrace: 30 * time.Second,
			Observer:  metrics,
		})
		if err != nil {
			return fmt.Errorf("market Hub setup failed")
		}
		publicMarkets.SetMarketHub(marketHub)
		defer marketHub.Close()
	}
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
		}, func() bool { return gatewayMonitor != nil && gatewayMonitor.Component("canonicalWrites").Ready },
			accountLifecycleDeps{credentials: credentialProvider, runtime: runtimeLifecycle}, publicMarkets, fundingBoard, venueConnectivity, freshBBO, marketHub)
	}
	processDeps := process.Dependencies{
		CheckCredentialResolver:  checkCredentialResolver,
		ResolveSecret:            provider.Resolve,
		ConnectPostgres:          observedConnectPostgres(metrics, connectPostgres),
		ValidateDatabaseIdentity: validateDatabaseIdentity,
		VerifyDatabaseAuthority:  defaultVerifyDatabaseAuthority(deps.verifyDatabaseAuthority),
		BuildAccountManager:      defaultBuildAccountManager(deps.buildAccountManager, runtimeLifecycle, credentialResolver),
		RestoreAccountManager: func(restoreCtx context.Context, pool process.PostgresPool, manager process.AccountManager) error {
			if deps.restoreAccountManager != nil {
				return deps.restoreAccountManager(restoreCtx, pool, manager)
			}
			realPool, poolOK := pool.(*pgxpool.Pool)
			realManager, managerOK := manager.(*account.Manager)
			if !poolOK || !managerOK {
				return fmt.Errorf("runtime restore dependency type")
			}
			if publicMarkets != nil {
				revisions := postgresCatalogRevisionStore{pool: realPool}
				if restoreCatalogErr := publicMarkets.RestoreCatalogRevisions(restoreCtx, revisions); restoreCatalogErr != nil {
					return fmt.Errorf("restore public market catalog revisions: %w", restoreCatalogErr)
				}
				publicMarkets.SetRevisionStore(revisions)
			}
			if productionLifecycle != nil {
				if attachErr := productionLifecycle.attach(realPool, realManager); attachErr != nil {
					return attachErr
				}
			}
			authority, restoreErr := newGatewayAuthority(restoreCtx, postgres.NewStore(realPool), realManager, withPublicMarkets(publicMarkets), withFundingBoard(fundingBoard))
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
			return publicHandler, nil
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
						Authority: validationSnapshotAuthority{snapshots: postgres.NewStore(realPool)},
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

type postgresCatalogRevisionStore struct{ pool *pgxpool.Pool }

func (store postgresCatalogRevisionStore) SaveCatalogRevision(ctx context.Context, revision publicmarket.CatalogRevision) error {
	_, _, err := postgres.SaveCatalogRevision(ctx, store.pool, postgres.CatalogRevision{
		ScopeKey: revision.ScopeKey, Fingerprint: revision.Fingerprint,
		ObservedAt: revision.ObservedAt, Instruments: revision.Instruments,
	})
	return err
}

func (store postgresCatalogRevisionStore) LoadCatalogRevision(ctx context.Context, scopeKey string) (publicmarket.CatalogRevision, bool, error) {
	revision, found, err := postgres.LoadCatalogHead(ctx, store.pool, scopeKey)
	return publicmarket.CatalogRevision{
		ScopeKey: revision.ScopeKey, Fingerprint: revision.Fingerprint,
		ObservedAt: revision.ObservedAt, Instruments: revision.Instruments,
	}, found, err
}

func defaultInternalHandlerFactory(
	override func(context.Context, process.PostgresPool, process.AccountManager) (http.Handler, error),
	authMiddleware auth.BearerMiddleware,
	authorityProvider func() *gatewayAuthority,
	writeReady func() bool,
	accountDeps accountLifecycleDeps,
	publicMarkets httpapi.InternalPublicMarketStreams,
	fundingBoardChanges httpapi.InternalFundingBoardChanges,
	venueConnectivity httpapi.InternalVenueConnectivity,
	freshBBO httpapi.InternalFreshBBO,
	marketHub httpapi.InternalMarketHub,
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
			var settlements httpapi.InternalFundingSettlements
			boardSource, _ := fundingBoardChanges.(*fundingboard.Source)
			marketSource, _ := publicMarkets.(*publicmarket.Source)
			if realPool, ok := pool.(*pgxpool.Pool); ok {
				if boardSource != nil && marketSource != nil {
					history := fundingsettlement.VenueHistory{Board: boardSource, Markets: marketSource}
					tracker, trackerErr := fundingsettlement.New(fundingsettlement.Options{
						Store: postgres.NewFundingSettlementStore(realPool), History: history,
					})
					if trackerErr != nil {
						return nil, fmt.Errorf("funding settlement tracker setup failed")
					}
					settlements = settlementHistoryTracker{tracker: tracker, history: history}
					go func() { _ = tracker.Run(ctx) }()
				}
			}
			options := httpapi.InternalOptions{
				Authority:           authority,
				WriteReady:          writeReady,
				PublicMarkets:       publicMarkets,
				FundingBoardChanges: fundingBoardChanges,
				MarketFacts:         marketowner.Facts{Board: boardSource, Markets: marketSource},
				VenueConnectivity:   venueConnectivity,
				FreshBBO:            freshBBO,
				MarketHub:           marketHub,
				FundingSettlements:  settlements,
				Authenticate: func(request *http.Request) error {
					if _, ok := auth.Principal(request.Context()); !ok {
						return errors.New("internal authentication required")
					}
					return nil
				},
			}
			if marketKeys, ok := publicMarkets.(httpapi.InternalMarketKeys); ok {
				options.MarketKeys = marketKeys
			}
			if accountDeps.available() {
				realPool, ok := pool.(*pgxpool.Pool)
				if !ok {
					return nil, fmt.Errorf("postgres pool type")
				}
				service, reconciler, lifecycleErr := accountDeps.newService(realPool, authority)
				if lifecycleErr != nil {
					return nil, lifecycleErr
				}
				options.Lifecycle = service
				options.Blockers = accountDeps.blockers()
				go runCredentialReconciler(ctx, reconciler)
			}
			handler, err = httpapi.NewInternalHandler(options)
		}
		if err != nil || handler == nil {
			return nil, fmt.Errorf("internal HTTP API setup")
		}
		return authMiddleware.Wrap(httpapi.NewWriteGate(writeReady).Wrap(handler)), nil
	}
}

type settlementHistoryTracker struct {
	tracker *fundingsettlement.Tracker
	history fundingsettlement.VenueHistory
}

func (tracking settlementHistoryTracker) TrackNextSettlement(ctx context.Context, instrumentID string) error {
	return tracking.history.TrackNextSettlement(ctx, tracking.tracker, instrumentID)
}

func (tracking settlementHistoryTracker) TrackActive(ctx context.Context, instrumentID, reason string) (func(), error) {
	return tracking.history.TrackActive(ctx, tracking.tracker, instrumentID, reason)
}

// accountLifecycleDeps carries what the durable account lifecycle needs but the
// internal handler factory has no other way to reach: somewhere to write
// credentials, and something that can say what still holds an account open. A
// gateway started without a credential provider — the migration-validation
// profile, or a test that supplies its own resolver — leaves this empty and the
// lifecycle routes are simply not mounted.
type accountLifecycleDeps struct {
	credentials *secretstore.FilesystemProvider
	runtime     account.RuntimeLifecycle
}

func (deps accountLifecycleDeps) available() bool {
	return deps.credentials != nil && deps.runtime != nil
}

// newService builds the durable account lifecycle and the reconciler that
// finishes what it starts.
//
// The two are returned together because neither is complete alone. A durable
// create applies the desired state to the runtime after one private balance
// read proves the credential. Promotion of that snapshot is the account-level
// proof; route private streams and write permissions continue warming as
// separate readiness signals and do not hold the account in "creating".
//
// The reconciler is the designed answer: it rescans operations parked in a
// recoverable state, re-reads the runtime's truth, and performs the same
// promotion after an interrupted response or process restart. Creation is
// eventually consistent by construction, so nothing else has to be.
func (deps accountLifecycleDeps) newService(pool *pgxpool.Pool, authority httpapi.InternalAuthority) (*lifecycle.Service, *lifecycle.Reconciler, error) {
	// The namespace and key version are the ones newGatewayCredentialResolver
	// already refused to start without, so the lifecycle writes into the same
	// scope the runtime reads from.
	credentials, err := secret.NewStore(secret.Config{
		Namespace: secret.Namespace, KeyVersion: "v1",
	}, deps.credentials.Writer())
	if err != nil {
		return nil, nil, fmt.Errorf("account lifecycle credential store")
	}
	desired, err := authorityclient.New(authority)
	if err != nil {
		return nil, nil, fmt.Errorf("account lifecycle authority client")
	}
	store := postgres.NewAccountLifecycleStore(pool)
	secrets := lifecycle.SecretStoreAdapter{Store: credentials}
	service, err := lifecycle.New(lifecycle.Options{
		Store:   store,
		Secrets: secrets,
		Audit:   postgres.NewAccountLifecycleAuditStore(pool),
		Gateway: desired,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("account lifecycle setup")
	}
	reconciler, err := lifecycle.NewReconciler(store, secrets, desired, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("account lifecycle reconciler setup")
	}
	return service, reconciler, nil
}

// credentialReconcileInterval is how often parked credential operations are
// retried, and credentialReconcileBatch how many are taken per pass.
//
// The interval is set by what it is waiting for. The common case is an
// interrupted create response or service restart; a slower sweep would leave a
// verified account untradeable for no reason other than the timer. It is not a
// poll of anything remote — a pass with nothing parked is one indexed query.
const (
	credentialReconcileInterval = 5 * time.Second
	credentialReconcileBatch    = 32
)

// runCredentialReconciler drives the reconciler until ctx ends.
//
// A failed pass is not reported here. The reconciler already writes its reason
// onto the operation row as a stable error code, which outlives any log line
// and is what an operator inspecting a stuck account actually reads. Repeating
// it here would emit one line every interval for as long as a venue takes to
// come up, which on a cold stack is every start.
func runCredentialReconciler(ctx context.Context, reconciler *lifecycle.Reconciler) {
	if reconciler == nil {
		return
	}
	ticker := time.NewTicker(credentialReconcileInterval)
	defer ticker.Stop()
	for {
		_ = reconciler.RunOnce(ctx, credentialReconcileBatch)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// blockers translates the runtime's inventory count into the lifecycle's. The
// gateway owns orders and positions; reservations belong to trading-svc, which
// does not exist yet, so that count stays zero rather than being guessed.
func (deps accountLifecycleDeps) blockers() httpapi.AccountBlockers {
	runtime := deps.runtime
	return func(ctx context.Context, accountID string) (lifecycle.Blockers, error) {
		id := domain.AccountID(accountID)
		if id.Validate() != nil {
			return lifecycle.Blockers{}, fmt.Errorf("account identifier is invalid")
		}
		blockers, err := runtime.Blockers(ctx, id)
		if err != nil {
			return lifecycle.Blockers{}, err
		}
		return lifecycle.Blockers{
			OpenOrders:        blockers.OpenOrders,
			UncertainCommands: blockers.UncertainCommands,
			OwnedPositions:    blockers.Positions,
		}, nil
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
			Orders:    coordinator.Coordinator,
			Events:    coordinator.Events,
			Positions: coordinator.Streams.Positions(),
			Auditor:   postgres.NewStore(realPool),
			Identity:  domain.NewIdentityGenerator(domain.IdentityGeneratorOptions{}),
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
) func(context.Context) (process.AccountManager, error) {
	if override != nil {
		return override
	}
	return func(ctx context.Context) (process.AccountManager, error) {
		allowLiveWrites, err := livewrite.Enabled()
		if err != nil {
			return nil, fmt.Errorf("live write configuration")
		}
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
			AllowLiveWrites:         allowLiveWrites,
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

func newGatewayCredentialResolver() (*secretstore.FilesystemProvider, account.ScopedCredentialResolver, error) {
	keySourceName := strings.TrimSpace(os.Getenv("BT_SECRET_KEY_SOURCE"))
	stateDir := strings.TrimSpace(os.Getenv("BT_SECRET_STORE_DIR"))
	namespace := strings.TrimSpace(os.Getenv("BT_SECRET_STORE_NAMESPACE"))
	keyVersion := strings.TrimSpace(os.Getenv("BT_SECRET_MASTER_KEY_VERSION"))
	if !filepath.IsAbs(stateDir) || namespace != secretstore.Namespace || keyVersion != "v1" {
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
	provider, err := secretstore.NewFilesystemProvider(secretstore.FilesystemConfig{StateDir: stateDir, KeySource: keySource})
	if err != nil {
		return nil, nil, fmt.Errorf("initialize credential resolver failed")
	}
	resolver, err := secret.NewScopedCredentialResolver(provider.Resolver(), secretstore.Scope{Namespace: namespace, KeyVersion: keyVersion})
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

// gatewayDatabaseIdentity is the database and role the gateway must be
// connected as. Both halves are checked against this one name: a gateway
// pointed at another service's database would migrate and write into it.
const gatewayDatabaseIdentity = "bolter_gateway"

func defaultValidateDatabaseIdentity(override func(context.Context, process.PostgresPool) error) func(context.Context, process.PostgresPool) error {
	if override != nil {
		return override
	}
	return func(ctx context.Context, pool process.PostgresPool) error {
		realPool, ok := pool.(*pgxpool.Pool)
		if !ok {
			return fmt.Errorf("postgres pool type")
		}
		var database, role string
		if err := realPool.QueryRow(ctx, `SELECT current_database(), current_user`).Scan(&database, &role); err != nil {
			return fmt.Errorf("query postgres identity")
		}
		return validateConnectedDatabaseIdentity(database, role, gatewayDatabaseIdentity)
	}
}

func validateConnectedDatabaseIdentity(database, role, expectedIdentity string) error {
	if database != expectedIdentity || role != expectedIdentity {
		return fmt.Errorf("postgres identity mismatch")
	}
	return nil
}

func defaultVerifyDatabaseAuthority(override func(context.Context, process.PostgresPool) (postgres.AuthorityResult, error)) func(context.Context, process.PostgresPool) (postgres.AuthorityResult, error) {
	if override != nil {
		return override
	}
	return func(ctx context.Context, pool process.PostgresPool) (postgres.AuthorityResult, error) {
		realPool, ok := pool.(*pgxpool.Pool)
		if !ok {
			return postgres.AuthorityResult{}, fmt.Errorf("postgres pool type")
		}
		return postgres.VerifyRuntimeAuthority(ctx, realPool, authority.GatewayV11())
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
