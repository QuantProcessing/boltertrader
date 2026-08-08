package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/backend/internal/config"
	frozencontract "github.com/QuantProcessing/boltertrader/backend/internal/contract"
	"github.com/QuantProcessing/boltertrader/backend/internal/httpapi"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/accounts"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/audit"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/catalog"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/ledger"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/marketdata"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/orders"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/portfolio"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/realtime"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/reservations"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/secret"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/session"
	platformsystem "github.com/QuantProcessing/boltertrader/backend/internal/platform/system"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/work"
	"github.com/QuantProcessing/boltertrader/backend/internal/products/funding/accounting"
	fundingexecution "github.com/QuantProcessing/boltertrader/backend/internal/products/funding/execution"
	fundingpolicy "github.com/QuantProcessing/boltertrader/backend/internal/products/funding/policy"
	"github.com/QuantProcessing/boltertrader/backend/internal/products/manual"
	"github.com/QuantProcessing/boltertrader/backend/internal/store/postgres"
	"github.com/QuantProcessing/boltertrader/backend/internal/workapi"
	backendcontract "github.com/QuantProcessing/boltertrader/internal/productplatformcontract"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/shopspring/decimal"
)

type App struct {
	cfg           config.Config
	contract      backendcontract.Contract
	accounts      *accounts.Service
	browser       *http.Server
	work          *http.Server
	browserLn     net.Listener
	workLn        net.Listener
	startOnce     sync.Once
	shutdownOnce  sync.Once
	reconciler    *orders.Reconciler
	reconcileStop context.CancelFunc
	reconcileDone chan struct{}
}

type Dependencies struct {
	Database              *sql.DB
	SecretProvider        secret.Provider
	CatalogSource         catalog.Source
	MarketDataSource      marketdata.Source
	MarketDataProbe       marketDataRouteProbe
	RuntimeProbe          runtimeRouteProbe
	ManualGateway         manual.Gateway
	FundingGateway        work.CommandGateway
	ReconciliationGateway orders.ReconciliationGateway
}

func New(cfg config.Config) (*App, error) {
	adapter, err := newGatewayAdapterForMode(cfg.GatewayURL, cfg.GatewayToken, orders.Mode(cfg.Mode), cfg.LivePrivateWritesEnabled, nil)
	if err != nil {
		return nil, err
	}
	return NewWithDependencies(cfg, Dependencies{CatalogSource: adapter, MarketDataSource: gatewayMarketDataSource{adapter: adapter}, MarketDataProbe: adapter, RuntimeProbe: adapter, ManualGateway: adapter, FundingGateway: adapter, ReconciliationGateway: adapter})
}

func NewWithDependencies(cfg config.Config, dependencies Dependencies) (*App, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	liveMaximum := decimal.Zero
	if cfg.FundingLiveEnabled {
		parsedMaximum, parseErr := decimal.NewFromString(cfg.FundingLiveMaxNotional)
		if parseErr != nil {
			return nil, fmt.Errorf("parse Funding Live maximum notional: %w", parseErr)
		}
		liveMaximum = parsedMaximum
	}
	fundingAdmission, err := fundingpolicy.NewAdmission(cfg.FundingLiveEnabled, liveMaximum)
	if err != nil {
		return nil, err
	}
	if dependencies.ManualGateway == nil {
		return nil, fmt.Errorf("Manual Gateway dependency is required")
	}
	if dependencies.FundingGateway == nil {
		return nil, fmt.Errorf("Funding Gateway dependency is required")
	}
	contract, err := loadContract()
	if err != nil {
		return nil, err
	}
	var sessionStore session.Store = session.NewMemoryStore()
	var identityStore session.IdentityStore
	var accountStore accounts.Store = accounts.NewMemoryStore()
	var auditStore audit.Store = audit.NewMemoryStore()
	var reservationStore reservations.Store = reservations.NewMemoryStore()
	var commandStore orders.Store = orders.NewMemoryStore()
	var ledgerStore ledger.Store = ledger.NewMemoryStore()
	var workStore work.Store = work.NewMemoryStore()
	var fundingPlanStore work.FundingPlanStore = work.NewMemoryFundingPlanStore()
	var fundingExecutionStore work.FundingExecutionStore = work.NewMemoryFundingExecutionStore()
	portfolioProjection := portfolio.NewProjection()
	if dependencies.Database != nil {
		sessionStore = postgres.NewSessionStore(dependencies.Database)
		persistentIdentities := postgres.NewIdentityStore(dependencies.Database)
		if err := seedBootstrapIdentity(context.Background(), persistentIdentities, cfg); err != nil {
			return nil, fmt.Errorf("seed bootstrap identity: %w", err)
		}
		identityStore = persistentIdentities
		accountStore = postgres.NewAccountStore(dependencies.Database)
		auditStore = postgres.NewAuditStore(dependencies.Database)
		reservationStore = postgres.NewReservationStore(dependencies.Database)
		commandStore = postgres.NewCommandStore(dependencies.Database)
		ledgerStore = postgres.NewLedgerStore(dependencies.Database)
		workStore = postgres.NewWorkStore(dependencies.Database)
		fundingPlanStore = postgres.NewFundingPlanStore(dependencies.Database)
		fundingExecutionStore = postgres.NewFundingExecutionStore(dependencies.Database)
		portfolioProjection = portfolio.NewPersistentProjection(postgres.NewPortfolioStore(dependencies.Database))
	}
	sessionService, err := session.New(session.Options{
		Username:     cfg.BootstrapUsername,
		PasswordHash: cfg.BootstrapPasswordHash,
		Mode:         cfg.Mode,
		TTL:          24 * time.Hour,
		CookieSecure: cfg.Mode == "live",
		Store:        sessionStore,
		Identities:   identityStore,
	})
	if err != nil {
		return nil, err
	}
	provider := dependencies.SecretProvider
	if provider == nil {
		provider = unavailableSecretProvider{}
	}
	secretMode, namespace := secret.ModeTest, secret.TestNamespace
	if cfg.Mode == "live" {
		secretMode, namespace = secret.ModeLive, secret.LiveNamespace
	}
	secretStore, err := secret.NewStore(secret.Config{Mode: secretMode, Namespace: namespace, KeyVersion: "v1"}, provider)
	if err != nil {
		return nil, err
	}
	auditService, err := audit.New(auditStore, nil, randomID("audit"))
	if err != nil {
		return nil, err
	}
	accountService, err := accounts.New(accounts.Options{Store: accountStore, Secrets: accounts.SecretStoreAdapter{Store: secretStore}, Audit: accounts.AuditServiceAdapter{Service: auditService}})
	if err != nil {
		return nil, err
	}

	now := func() time.Time { return time.Now().UTC() }
	catalogSource := dependencies.CatalogSource
	if catalogSource == nil {
		catalogSource = unavailableCatalogSource{}
	}
	marketSource := dependencies.MarketDataSource
	if marketSource == nil {
		marketSource = unavailableMarketDataSource{}
	}
	catalogProjection, err := catalog.NewProjection(catalogSource, 30*time.Second, now)
	if err != nil {
		return nil, err
	}
	budget, err := marketdata.NewCoordinator(30, time.Minute, now)
	if err != nil {
		return nil, err
	}
	marketService, err := marketdata.NewService(marketSource, budget, 5*time.Second, now)
	if err != nil {
		return nil, err
	}
	hub, err := realtime.NewHub(1024, now)
	if err != nil {
		return nil, err
	}
	readiness := platformsystem.NewReadiness()
	databaseReady := dependencies.Database != nil
	secretReady := dependencies.SecretProvider != nil
	readiness.Update("process", readinessComponent(true, ""))
	readiness.Update("database", readinessComponent(databaseReady, "database_dependency_missing"))
	readiness.Update("secretStore", readinessComponent(secretReady, "secret_provider_missing"))
	projectionReadiness := func() platformsystem.Component {
		status := catalogProjection.Status()
		return platformsystem.Component{Ready: status.Ready, Reason: status.Reason, ObservedAt: status.ObservedAt}
	}
	readiness.SetEvaluator("gateway", projectionReadiness)
	readiness.SetEvaluator("publicData", func() platformsystem.Component {
		if dependencies.MarketDataProbe == nil {
			return readinessComponent(false, "market_data_route_probe_missing")
		}
		probeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := dependencies.MarketDataProbe.CheckMarketDataRoute(probeCtx); err != nil {
			return readinessComponent(false, "market_data_route_unavailable")
		}
		return readinessComponent(true, "")
	})
	readiness.Update("accounts", readinessComponent(databaseReady && secretReady, "durable_account_dependencies_missing"))
	readiness.Update("fundingWorker", readinessComponent(false, "worker_not_registered"))
	publicAPI, err := httpapi.NewPublicDataAPI(httpapi.PublicDataDependencies{Catalog: catalogProjection, MarketData: marketService, Readiness: readiness, Realtime: hub})
	if err != nil {
		return nil, err
	}

	reservationService := reservations.NewService(reservationStore, now)
	commandService := orders.NewService(commandStore, reservationGate{service: reservationService}, now)
	commandService.SetLiveWritesEnabled(cfg.LivePrivateWritesEnabled)
	ledgerService := ledger.NewService(ledgerStore, now)
	manualService := manual.NewGatewayServiceWithPreviewKey(commandService, portfolioProjection, ledgerService, dependencies.ManualGateway, now, cfg.ManualPreviewSigningKey)
	if err := manualService.SetExecutionAdmission(orders.Mode(cfg.Mode), cfg.LivePrivateWritesEnabled); err != nil {
		return nil, err
	}
	for index := range cfg.ManualPreviewSigningKey {
		cfg.ManualPreviewSigningKey[index] = 0
	}
	cfg.ManualPreviewSigningKey = nil
	fundingProcessor, err := work.NewDurableFundingPayloadProcessor(commandService, fundingPlanStore, fundingExecutionStore, now)
	if err != nil {
		return nil, err
	}
	fundingProcessor.WithExecutionEvidence(portfolioProjection, ledgerService).WithAdmission(fundingAdmission)
	reconciliationGateway := dependencies.ReconciliationGateway
	if reconciliationGateway == nil {
		reconciliationGateway, _ = dependencies.FundingGateway.(orders.ReconciliationGateway)
	}
	var reconciliationRunner *orders.Reconciler
	if reconciliationGateway != nil {
		reconciliationRunner, err = orders.NewReconciler(commandService, reconciliationGateway, reconciliationDispatcher{manual: manualService, funding: fundingProcessor}, orders.Mode(cfg.Mode), now)
		if err != nil {
			return nil, err
		}
	}
	fundingPlanProducer, err := fundingexecution.NewPlanProducer(fundingPlanStore, fundingExecutionStore)
	if err != nil {
		return nil, err
	}
	fundingPlanProducer.WithAdmission(fundingAdmission).WithAccounting(accounting.NewPlatformAdapter(ledgerService, portfolioProjection))
	fundingProjection, err := httpapi.NewSharedFundingProjection(httpapi.SharedFundingProjectionOptions{
		Catalog: catalogProjection, MarketData: marketService, Accounts: accountService,
		Reservations: reservationStore, Admission: fundingAdmission, Now: now,
	})
	if err != nil {
		return nil, err
	}
	authenticate := httpapi.SessionAuthenticator(sessionService)
	principal := func(r *http.Request) (string, string, error) {
		cookie, cookieErr := r.Cookie(httpapi.SessionCookieName)
		if cookieErr != nil {
			return "", "", cookieErr
		}
		result, currentErr := sessionService.Current(r.Context(), cookie.Value)
		return result.Principal.ID, result.Principal.Mode, currentErr
	}
	workService := work.NewWithStoreGatewayProcessorAndPlanProducer(workapi.ContractVersion, 15*time.Second, workStore, fundingProcessor, fundingPlanProducer, dependencies.FundingGateway)
	workService.SetDependencyReadinessEvaluator(work.DependencyReadinessFunc(func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		persistence, ok := workStore.(interface{ Readiness(context.Context) error })
		if !ok {
			return fmt.Errorf("Platform work persistence readiness is unavailable")
		}
		if err := persistence.Readiness(ctx); err != nil {
			return err
		}
		if _, err := catalogProjection.Snapshot(ctx); err != nil {
			return fmt.Errorf("Gateway catalog authority is unavailable: %w", err)
		}
		if dependencies.MarketDataProbe == nil {
			return fmt.Errorf("Gateway market-data readiness probe is unavailable")
		}
		if err := dependencies.MarketDataProbe.CheckMarketDataRoute(ctx); err != nil {
			return fmt.Errorf("Gateway market-data authority is unavailable: %w", err)
		}
		if dependencies.RuntimeProbe == nil {
			return fmt.Errorf("Gateway runtime readiness probe is unavailable")
		}
		if err := dependencies.RuntimeProbe.CheckRuntimeRoute(ctx); err != nil {
			return fmt.Errorf("Gateway account runtime authority is unavailable: %w", err)
		}
		return nil
	}))
	workService.SetLiveWritesEnabled(cfg.FundingLiveEnabled)
	browserHandler, err := httpapi.New(httpapi.Options{
		Sessions: sessionService, Mode: cfg.Mode, ContractVersion: httpapi.ContractVersion,
		Accounts:   &httpapi.AccountRoutesOptions{Service: accountService, Authenticate: authenticate, AuthorizeMutation: httpapi.SessionMutationAuthorizer(sessionService), Blockers: func(context.Context, string) (accounts.Blockers, error) { return accounts.Blockers{}, nil }},
		Audit:      &httpapi.AuditRouteOptions{Service: auditService, Authenticate: authenticate},
		PublicData: publicAPI,
		Trading:    &httpapi.TradingOptions{Reservations: reservationService, Commands: commandService, Portfolio: portfolioProjection, Ledger: ledgerService, Catalog: catalogProjection, Manual: manualService, ManualAccounts: accountService, Principal: principal, AuthorizeMutation: httpapi.SessionMutationAuthorizer(sessionService)},
		Funding:    &httpapi.FundingRoutesOptions{Projection: fundingProjection, Work: workService, Executions: fundingExecutionStore, Principal: principal, AuthorizeMutation: httpapi.SessionMutationAuthorizer(sessionService)},
	})
	if err != nil {
		return nil, err
	}
	workHandler, err := workapi.New(workapi.Options{
		TokenDigest: cfg.FundingWorkerTokenDigest, Mode: cfg.Mode, ContractVersion: workapi.ContractVersion,
		Service: workService,
		OnReadiness: func(ready bool) {
			readiness.Update("fundingWorker", readinessComponent(ready, "worker_not_registered"))
		},
	})
	if err != nil {
		return nil, err
	}
	readiness.SetEvaluator("fundingWorker", func() platformsystem.Component {
		return readinessComponent(workService.AnyWorkerReady(), "worker_not_ready")
	})
	return &App{
		cfg:        cfg,
		contract:   contract,
		accounts:   accountService,
		browser:    &http.Server{Addr: cfg.BrowserListenAddr, Handler: browserHandler},
		work:       &http.Server{Addr: cfg.WorkListenAddr, Handler: workHandler},
		reconciler: reconciliationRunner,
	}, nil
}

type unavailableSecretProvider struct{}

func (unavailableSecretProvider) Check(context.Context, secret.Scope) error {
	return errors.New("unavailable")
}
func (unavailableSecretProvider) Put(context.Context, secret.Scope, []byte) (string, error) {
	return "", errors.New("unavailable")
}

type unavailableCatalogSource struct{}

func (unavailableCatalogSource) Fetch(context.Context) (catalog.Snapshot, error) {
	return catalog.Snapshot{}, catalog.ErrUnavailable
}

type unavailableMarketDataSource struct{}

func (unavailableMarketDataSource) Fetch(context.Context, marketdata.Query) (marketdata.Fact, error) {
	return marketdata.Fact{}, errors.New("gateway projection unavailable")
}

type reservationGate struct{ service *reservations.Service }

func (gate reservationGate) Consume(ctx context.Context, claim orders.ReservationClaim) error {
	_, err := gate.service.Consume(ctx, reservations.ConsumeRequest{ReservationID: claim.ReservationID, PrincipalID: claim.PrincipalID, ProductID: claim.ProductID, Mode: reservations.Mode(claim.Mode)})
	return err
}

func randomID(prefix string) func() string {
	var mu sync.Mutex
	var next uint64
	return func() string { mu.Lock(); defer mu.Unlock(); next++; return fmt.Sprintf("%s_%016x", prefix, next) }
}

type identitySeeder interface {
	Upsert(context.Context, session.Identity) error
}

func seedBootstrapIdentity(ctx context.Context, store identitySeeder, cfg config.Config) error {
	return store.Upsert(ctx, session.Identity{PrincipalID: session.DefaultPrincipalID, Username: cfg.BootstrapUsername, PasswordHash: cfg.BootstrapPasswordHash, Mode: cfg.Mode})
}

func readinessComponent(ready bool, missingReason string) platformsystem.Component {
	if ready {
		return platformsystem.Component{Ready: true, Reason: "ready"}
	}
	return platformsystem.Component{Ready: false, Reason: missingReason}
}

func (a *App) Start(ctx context.Context) error {
	var err error
	a.startOnce.Do(func() {
		a.browserLn, err = net.Listen("tcp", a.cfg.BrowserListenAddr)
		if err != nil {
			return
		}
		a.workLn, err = net.Listen("tcp", a.cfg.WorkListenAddr)
		if err != nil {
			_ = a.browserLn.Close()
			return
		}
		go func() { _ = a.browser.Serve(a.browserLn) }()
		go func() { _ = a.work.Serve(a.workLn) }()
		if a.reconciler != nil {
			reconcileCtx, cancel := context.WithCancel(ctx)
			a.reconcileStop, a.reconcileDone = cancel, make(chan struct{})
			go func() {
				defer close(a.reconcileDone)
				a.reconciler.Run(reconcileCtx)
			}()
		}
	})
	if err != nil {
		return err
	}
	<-ctx.Done()
	return a.Shutdown(context.Background())
}

func (a *App) Shutdown(ctx context.Context) error {
	var first error
	a.shutdownOnce.Do(func() {
		if a.reconcileStop != nil {
			a.reconcileStop()
		}
		if a.reconcileDone != nil {
			select {
			case <-a.reconcileDone:
			case <-ctx.Done():
				first = ctx.Err()
			}
		}
		if a.work != nil {
			if err := a.work.Shutdown(ctx); err != nil && first == nil {
				first = err
			}
		}
		if a.browser != nil {
			if err := a.browser.Shutdown(ctx); err != nil && first == nil {
				first = err
			}
		}
		if a.workLn != nil {
			_ = a.workLn.Close()
		}
		if a.browserLn != nil {
			_ = a.browserLn.Close()
		}
	})
	return first
}

func Run(ctx context.Context, cfg config.Config) error {
	if cfg.DatabaseName == "" || cfg.DatabaseRole == "" {
		return fmt.Errorf("Platform database configuration is required")
	}
	databaseURL := &url.URL{Scheme: "postgres", User: url.User(cfg.DatabaseRole), Host: "127.0.0.1:15432", Path: cfg.DatabaseName, RawQuery: "sslmode=disable"}
	database, err := sql.Open("pgx", databaseURL.String())
	if err != nil {
		return fmt.Errorf("open Platform database: %w", err)
	}
	defer database.Close()
	if err := database.PingContext(ctx); err != nil {
		return fmt.Errorf("ping Platform database: %w", err)
	}
	if err := postgres.Migrate(ctx, database); err != nil {
		return fmt.Errorf("migrate Platform database: %w", err)
	}
	adapter, err := newGatewayAdapterForMode(cfg.GatewayURL, cfg.GatewayToken, orders.Mode(cfg.Mode), cfg.LivePrivateWritesEnabled, nil)
	if err != nil {
		return err
	}
	app, err := NewWithDependencies(cfg, Dependencies{Database: database, CatalogSource: adapter, MarketDataSource: gatewayMarketDataSource{adapter: adapter}, MarketDataProbe: adapter, RuntimeProbe: adapter, ManualGateway: adapter, FundingGateway: adapter, ReconciliationGateway: adapter})
	if err != nil {
		return err
	}
	return app.Start(ctx)
}

func loadContract() (backendcontract.Contract, error) {
	contract, err := frozencontract.Load()
	if err != nil {
		return backendcontract.Contract{}, fmt.Errorf("load frozen contract: %w", err)
	}
	return contract, nil
}
