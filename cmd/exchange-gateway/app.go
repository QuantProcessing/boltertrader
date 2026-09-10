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
	"sync"
	"syscall"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/account"
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
	"github.com/QuantProcessing/boltertrader/gateway/hotset"
	"github.com/QuantProcessing/boltertrader/gateway/httpapi"
	"github.com/QuantProcessing/boltertrader/gateway/marketcapability"
	"github.com/QuantProcessing/boltertrader/gateway/markethub"
	"github.com/QuantProcessing/boltertrader/gateway/marketowner"
	"github.com/QuantProcessing/boltertrader/gateway/observability"
	"github.com/QuantProcessing/boltertrader/gateway/order"
	"github.com/QuantProcessing/boltertrader/gateway/process"
	"github.com/QuantProcessing/boltertrader/gateway/publicconn"
	"github.com/QuantProcessing/boltertrader/gateway/publicmarket"
	"github.com/QuantProcessing/boltertrader/gateway/ratelimit"
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
}

const venueProbeTimeoutEnv = "BT_VENUE_PROBE_TIMEOUT"

func venueProbeTimeout() (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(venueProbeTimeoutEnv))
	if raw == "" {
		return 3 * time.Second, nil
	}
	timeout, err := time.ParseDuration(raw)
	if err != nil || timeout < time.Second || timeout > time.Minute {
		return 0, fmt.Errorf("%s must be a duration between 1s and 1m", venueProbeTimeoutEnv)
	}
	return timeout, nil
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
	var internalAuth auth.BearerMiddleware
	if !file.Server.InternalAuthTokenRef.IsZero() {
		internalAuth, err = auth.NewBearerMiddleware(ctx, provider, file.Server.InternalAuthTokenRef)
		if err != nil {
			return fmt.Errorf("configure internal auth failed")
		}
	}
	metrics := deps.metrics
	if metrics == nil {
		metrics = observability.NewMetrics()
	}
	connectPostgres := defaultConnectPostgres(deps.connectPostgres)
	validateDatabaseIdentity := defaultValidateDatabaseIdentity(deps.validateDatabaseIdentity)

	var accountCipher *secretstore.Cipher
	validationProfile := file.Server.StartupProfile == config.StartupProfileMigrationValidation
	if !validationProfile && deps.buildAccountManager == nil {
		accountCipher, err = newAccountCipher(ctx)
		if err != nil {
			return err
		}
	}
	checkCredentialResolver := deps.checkCredentialResolver

	sharedTransport, err := ratelimit.NewTransport(ratelimit.NewCoordinator(time.Now), http.DefaultTransport)
	if err != nil {
		return fmt.Errorf("HTTP request budget setup failed: %w", err)
	}
	sharedHTTPClient := &http.Client{Transport: sharedTransport, Timeout: 30 * time.Second}
	var venueConnectivity *connectivity.Manager
	if !validationProfile {
		probeTimeout, timeoutErr := venueProbeTimeout()
		if timeoutErr != nil {
			return fmt.Errorf("venue connectivity setup failed: %w", timeoutErr)
		}
		venueConnectivity, err = connectivity.New(connectivity.Options{
			Checkers: connectivity.DefaultHTTPCheckers(&http.Client{Transport: sharedTransport, Timeout: probeTimeout}),
			Interval: 10 * time.Second, Timeout: probeTimeout, Observer: metrics,
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
	var marketFacts *markethub.FactStore
	var marketCapabilities *marketcapability.Registry
	var hotsets *hotset.Manager
	var hotActivator *marketowner.HotActivator
	var marketRequests *ratelimit.MarketCoordinator
	if !validationProfile {
		marketRequests = ratelimit.NewMarketCoordinator(metrics, time.Now)
		marketCapabilities, err = marketcapability.DefaultRegistry()
		if err != nil {
			return fmt.Errorf("market capability setup failed: %w", err)
		}
		marketFactory, marketErr := publicmarket.NewExchangeFactory(sharedHTTPClient)
		if marketErr != nil {
			return fmt.Errorf("public market factory setup failed")
		}
		publicMarkets, marketErr = publicmarket.NewSource(marketFactory, routeprofile.DefaultRegistry())
		if marketErr != nil {
			return fmt.Errorf("public market source setup failed")
		}
		if marketErr = publicMarkets.SetCapabilities(marketCapabilities); marketErr != nil {
			return fmt.Errorf("public market capabilities failed: %w", marketErr)
		}
		publicMarkets.SetRequestCoordinator(marketRequests)
		go func() { _ = publicMarkets.Run(ctx) }()
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
	// Construction failure is fatal because this is now the shared market fact
	// authority for Trader marks and portfolio PnL as well as the
	// board. Runtime venue failures still degrade independently after startup.
	var fundingBoard *fundingboard.Projection
	var derivativeOwners *marketowner.DerivativeOwners
	if !validationProfile {
		boardFactory, boardErr := fundingboard.NewExchangeFactory(sharedHTTPClient)
		if boardErr == nil {
			boardFactory.SetMarketRequestCoordinator(marketRequests)
			marketFacts = markethub.NewFactStore(metrics, time.Now)
			go marketFacts.RunMetrics(ctx, time.Second)
			derivativeOwners, boardErr = marketowner.NewDerivativeOwners(boardFactory, routeprofile.DefaultRegistry(), marketCapabilities, publicMarkets, marketFacts, metrics)
		}
		if boardErr == nil {
			fundingBoard, boardErr = fundingboard.NewProjection(marketFacts, boardFactory, routeprofile.DefaultRegistry(), marketCapabilities)
		}
		if boardErr != nil {
			if derivativeOwners != nil {
				_ = derivativeOwners.Close()
			}
			return fmt.Errorf("derivative market setup failed: %w", boardErr)
		}
		if derivativeOwners != nil {
			go func() { _ = derivativeOwners.Run(ctx) }()
		}
		defer func() {
			if fundingBoard != nil {
				_ = fundingBoard.Close()
			}
			if derivativeOwners != nil {
				_ = derivativeOwners.Close()
			}
		}()
	}
	if !validationProfile {
		boardMarks := marketowner.BoardMarks{Facts: marketFacts, Markets: publicMarkets, Capabilities: marketCapabilities}
		marketHub, err = markethub.New(markethub.Options{
			Owner: marketowner.Owner{
				Source: publicMarkets, Capabilities: marketCapabilities,
				BoardMarks: boardMarks,
			},
			IdleGrace: 30 * time.Second,
			Observer:  metrics,
		})
		if err != nil {
			return fmt.Errorf("market Hub setup failed")
		}
		publicMarkets.SetMarketHub(marketHub)
		freshBBO, err = markethub.NewBBOCache(marketHub, publicMarkets, time.Now)
		if err != nil {
			return fmt.Errorf("fresh BBO cache setup failed: %w", err)
		}
		hotActivator = marketowner.NewHotActivator(boardMarks, derivativeOwners, metrics)
		hotsets, err = hotset.New(hotset.Options{
			Activator: hotActivator,
			Capacity: func(owner markethub.OwnerKey) int {
				return marketCapabilities.HotCapacity(owner.Venue, owner.NativeFamily)
			},
			Grace: 30 * time.Second,
		})
		if err != nil {
			return fmt.Errorf("market hotset setup failed")
		}
		defer hotsets.Close()
		defer hotActivator.Close()
		defer marketHub.Close()
	}
	publicHandlerFactory := defaultHandlerFactory(deps.buildHandler, metrics, httpapi.MarketOptions{Source: publicMarkets, Hub: marketHub, Hotset: hotsets})
	var internalHandler http.Handler
	var restoredAuthority *gatewayAuthority
	var gatewayMonitor *health.Monitor
	var internalHandlerFactory func(context.Context, process.PostgresPool, process.AccountManager, process.OrderCoordinator) (http.Handler, error)
	if file.Server.InternalListen != "" {
		internalHandlerFactory = defaultInternalHandlerFactory(deps.buildInternalHandler, internalAuth, func() *gatewayAuthority {
			return restoredAuthority
		}, func() bool { return gatewayMonitor != nil && gatewayMonitor.Component("canonicalWrites").Ready },
			accountRegistrationDeps{cipher: accountCipher}, publicMarkets, fundingBoard, marketFacts, venueConnectivity, freshBBO, marketHub, hotsets, authMiddleware.PrincipalID())
	}
	processDeps := process.Dependencies{
		WrapHTTP: func(handler http.Handler) http.Handler {
			return authMiddleware.Wrap(handler)
		},
		CheckCredentialResolver:  checkCredentialResolver,
		ResolveSecret:            provider.Resolve,
		ConnectPostgres:          observedConnectPostgres(metrics, connectPostgres),
		ValidateDatabaseIdentity: validateDatabaseIdentity,
		VerifyDatabaseAuthority:  defaultVerifyDatabaseAuthority(deps.verifyDatabaseAuthority),
		BuildAccountManager:      defaultBuildAccountManager(deps.buildAccountManager, sharedHTTPClient),
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
			authority, restoreErr := newGatewayAuthority(restoreCtx, realManager, withPublicMarkets(publicMarkets), withFundingBoard(fundingBoard))
			if restoreErr != nil {
				return restoreErr
			}
			if accountCipher != nil {
				accounts, err := account.NewRegistrations(postgres.NewRegistrationStore(realPool, accountCipher), realManager)
				if err != nil {
					return err
				}
				failures := accounts.Restore(restoreCtx)
				if err := failures[""]; err != nil {
					return fmt.Errorf("restore accounts: %w", err)
				}
				// Individual venue failures are shown per account and must not hold
				// the entire personal gateway in an unavailable state.
			}
			restoredAuthority = authority
			return nil
		},
		BuildOrderCoordinator: defaultBuildOrderCoordinator(deps.buildOrderCoordinator, authMiddleware.PrincipalID(), hotsets),
		Listen:                defaultListen(deps.listen),
		HandlerFactory: func(monitor *health.Monitor, pool process.PostgresPool, accounts process.AccountManager, orders process.OrderCoordinator) (http.Handler, error) {
			gatewayMonitor = monitor
			publicHandler, handlerErr := publicHandlerFactory(monitor, pool, accounts, orders)
			if handlerErr != nil {
				return nil, handlerErr
			}
			if internalHandlerFactory != nil {
				internalHandler, handlerErr = internalHandlerFactory(ctx, pool, accounts, orders)
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
					_, ok := pool.(*pgxpool.Pool)
					if !ok {
						return nil, fmt.Errorf("postgres pool type")
					}
					var handlerErr error
					internalHandler, handlerErr = httpapi.NewInternalHandler(httpapi.InternalOptions{
						Authority: validationSnapshotAuthority{},
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
			return httpapi.NewMigrationValidationHandler(monitor), nil
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
	accountDeps accountRegistrationDeps,
	publicMarkets *publicmarket.Source,
	fundingBoardChanges httpapi.InternalFundingBoardChanges,
	marketFacts httpapi.InternalMarketFacts,
	venueConnectivity httpapi.InternalVenueConnectivity,
	freshBBO httpapi.InternalFreshBBO,
	marketHub httpapi.MarketHub,
	hotsets httpapi.MarketHotset,
	accountEventOwner domain.PrincipalID,
) func(context.Context, process.PostgresPool, process.AccountManager, process.OrderCoordinator) (http.Handler, error) {
	return func(ctx context.Context, pool process.PostgresPool, accounts process.AccountManager, orders process.OrderCoordinator) (http.Handler, error) {
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
			boardSource, _ := fundingBoardChanges.(fundingboard.Board)
			marketSource := publicMarkets
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
					if observed, ok := hotsets.(interface{ SetObserver(hotset.Observer) }); ok {
						observed.SetObserver(newSettlementHotsetObserver(ctx, settlements))
					}
					go func() { _ = tracker.Run(ctx) }()
				}
			}
			options := httpapi.InternalOptions{
				Authority:           authority,
				WriteReady:          writeReady,
				FundingBoardChanges: fundingBoardChanges,
				MarketFacts:         marketFacts,
				VenueConnectivity:   venueConnectivity,
				FreshBBO:            freshBBO,
				MarketHub:           marketHub,
				FundingSettlements:  settlements,
				Hotset:              hotsets,
				AccountEventOwner:   accountEventOwner,
				Authenticate: func(request *http.Request) error {
					if _, ok := auth.Principal(request.Context()); !ok {
						return errors.New("internal authentication required")
					}
					return nil
				},
			}
			if coordinator, ok := orders.(processOrderCoordinator); ok {
				options.AccountEvents = coordinator.Events
			}
			if publicMarkets != nil {
				options.MarketKeys = publicMarkets
			}
			if accountDeps.available() {
				realPool, ok := pool.(*pgxpool.Pool)
				if !ok {
					return nil, fmt.Errorf("postgres pool type")
				}
				realManager, ok := accounts.(*account.Manager)
				if !ok {
					return nil, fmt.Errorf("account manager type")
				}
				service, err := account.NewRegistrations(postgres.NewRegistrationStore(realPool, accountDeps.cipher), realManager)
				if err != nil {
					return nil, err
				}
				options.Accounts = service
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

type settlementHotsetKey struct {
	instrumentID string
	reason       hotset.Reason
}

type settlementHotsetState struct {
	desired bool
	running bool
	stop    func()
}

// settlementHotsetObserver mirrors stable hotset reasons into the durable
// settlement tracker. Tracking may require a catalog/fact lookup and a database
// write, so transitions run outside market/account event loops. A reason that
// arrives before next-funding metadata is available retries while it remains
// desired instead of being silently lost.
type settlementHotsetObserver struct {
	ctx      context.Context
	tracking httpapi.InternalFundingSettlements
	mu       sync.Mutex
	states   map[settlementHotsetKey]*settlementHotsetState
}

func newSettlementHotsetObserver(ctx context.Context, tracking httpapi.InternalFundingSettlements) *settlementHotsetObserver {
	return &settlementHotsetObserver{ctx: ctx, tracking: tracking, states: make(map[settlementHotsetKey]*settlementHotsetState)}
}

func (observer *settlementHotsetObserver) ReasonChanged(key hotset.Key, reason hotset.Reason, active bool) {
	if observer == nil || observer.tracking == nil || strings.TrimSpace(key.InstrumentID) == "" {
		return
	}
	trackingKey := settlementHotsetKey{instrumentID: key.InstrumentID, reason: reason}
	observer.mu.Lock()
	state := observer.states[trackingKey]
	if state == nil {
		state = &settlementHotsetState{}
		observer.states[trackingKey] = state
	}
	state.desired = active
	if !active && state.stop != nil {
		stop := state.stop
		state.stop = nil
		delete(observer.states, trackingKey)
		observer.mu.Unlock()
		stop()
		return
	}
	if !active || state.running || state.stop != nil {
		if !active && !state.running {
			delete(observer.states, trackingKey)
		}
		observer.mu.Unlock()
		return
	}
	state.running = true
	observer.mu.Unlock()
	go observer.track(trackingKey)
}

func (observer *settlementHotsetObserver) track(key settlementHotsetKey) {
	for observer.ctx.Err() == nil {
		stop, err := observer.tracking.TrackActive(observer.ctx, key.instrumentID, string(key.reason))
		observer.mu.Lock()
		state := observer.states[key]
		if state == nil || !state.desired || observer.ctx.Err() != nil {
			if state != nil {
				delete(observer.states, key)
			}
			observer.mu.Unlock()
			if err == nil && stop != nil {
				stop()
			}
			return
		}
		if err == nil {
			state.running = false
			state.stop = stop
			observer.mu.Unlock()
			return
		}
		observer.mu.Unlock()
		timer := time.NewTimer(10 * time.Second)
		select {
		case <-observer.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (tracking settlementHistoryTracker) TrackNextSettlement(ctx context.Context, instrumentID string) error {
	return tracking.history.TrackNextSettlement(ctx, tracking.tracker, instrumentID)
}

func (tracking settlementHistoryTracker) TrackActive(ctx context.Context, instrumentID, reason string) (func(), error) {
	return tracking.history.TrackActive(ctx, tracking.tracker, instrumentID, reason)
}

type accountRegistrationDeps struct{ cipher *secretstore.Cipher }

func (deps accountRegistrationDeps) available() bool { return deps.cipher != nil }

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
	metrics *observability.Metrics,
	markets httpapi.MarketOptions,
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
			Monitor: monitor, Accounts: manager, References: references, Markets: markets,
			Orders:    coordinator.Coordinator,
			Events:    coordinator.Events,
			Positions: coordinator.Streams.Positions(),
			Auditor:   postgres.NewStore(realPool),
			Identity:  domain.NewIdentityGenerator(domain.IdentityGeneratorOptions{}),
		})
		if err != nil {
			return nil, fmt.Errorf("HTTP API setup")
		}
		return withOperationalHTTP(handler, metrics), nil
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
	hotsets *hotset.Manager,
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
		events, err := gatewayevent.NewHub(gatewayevent.Options{Identity: identity})
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
			Accounts: manager, Reconciler: coordinator, Hub: events, PrincipalID: principalID, Hotset: hotsets,
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

	httpClient *http.Client,
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
		if httpClient == nil {
			return nil, fmt.Errorf("shared HTTP client is required")
		}
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
			RuntimeGroupBuilder:     account.NewFactoryRuntimeGroupBuilder(httpClient, exchangeCatalog),
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

func newAccountCipher(ctx context.Context) (*secretstore.Cipher, error) {
	var source secretstore.KeySource
	switch strings.TrimSpace(os.Getenv("BT_SECRET_KEY_SOURCE")) {
	case "", "file":
		path := strings.TrimSpace(os.Getenv("BT_SECRET_MASTER_KEY_FILE"))
		if !filepath.IsAbs(path) {
			return nil, fmt.Errorf("account encryption key file must be absolute")
		}
		source = secretstore.FileKeySource{Path: path}
	case "keychain":
		source = secretstore.KeychainSource{Service: os.Getenv("BT_SECRET_KEYCHAIN_SERVICE"), Account: os.Getenv("BT_SECRET_KEYCHAIN_ACCOUNT"), Runner: gatewayKeychainRunner{}}
	default:
		return nil, fmt.Errorf("unsupported account encryption key source")
	}
	key, err := source.Load(ctx)
	if err != nil {
		return nil, fmt.Errorf("load account encryption key: %w", err)
	}
	defer clear(key)
	return secretstore.NewCipher(key)
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
		return postgres.VerifyRuntimeAuthority(ctx, realPool, authority.GatewayV12())
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
