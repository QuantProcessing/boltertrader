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
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/session"
	"github.com/QuantProcessing/boltertrader/backend/internal/store/postgres"
	"github.com/QuantProcessing/boltertrader/gateway/authority"
	backendcontract "github.com/QuantProcessing/boltertrader/internal/productplatformcontract"
	platformsystem "github.com/QuantProcessing/boltertrader/internal/system"
	"github.com/QuantProcessing/boltertrader/services/trading/audit"
	"github.com/QuantProcessing/boltertrader/services/trading/catalog"
	tradinghttp "github.com/QuantProcessing/boltertrader/services/trading/httpapi"
	"github.com/QuantProcessing/boltertrader/services/trading/ledger"
	"github.com/QuantProcessing/boltertrader/services/trading/manual"
	"github.com/QuantProcessing/boltertrader/services/trading/marketdata"
	"github.com/QuantProcessing/boltertrader/services/trading/orders"
	"github.com/QuantProcessing/boltertrader/services/trading/portfolio"
	"github.com/QuantProcessing/boltertrader/services/trading/realtime"
	"github.com/QuantProcessing/boltertrader/services/trading/reservations"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type App struct {
	cfg           config.Config
	contract      backendcontract.Contract
	browser       *http.Server
	browserLn     net.Listener
	startOnce     sync.Once
	shutdownOnce  sync.Once
	reconciler    *orders.Reconciler
	reconcileStop context.CancelFunc
	reconcileDone chan struct{}
}

type Dependencies struct {
	Database              *sql.DB
	CatalogSource         catalog.Source
	MarketDataSource      marketdata.Source
	MarketDataProbe       marketDataRouteProbe
	RuntimeProbe          runtimeRouteProbe
	RuntimeProjection     tradinghttp.AccountRuntimeProjectionReader
	ManualGateway         manual.Gateway
	ManualAccounts        tradinghttp.ManualAccountAuthority
	ReconciliationGateway orders.ReconciliationGateway
	WriteAuthority        WriteAuthority
}

func New(cfg config.Config) (*App, error) {
	adapter, err := newGatewayAdapterForMode(cfg.GatewayURL, cfg.GatewayToken, orders.Mode(cfg.Mode), cfg.LivePrivateWritesEnabled, nil)
	if err != nil {
		return nil, err
	}
	return NewWithDependencies(cfg, Dependencies{CatalogSource: adapter, MarketDataSource: gatewayMarketDataSource{adapter: adapter}, MarketDataProbe: adapter, RuntimeProbe: adapter, RuntimeProjection: adapter, ManualGateway: adapter, ManualAccounts: gatewayManualAccounts{adapter: adapter}, ReconciliationGateway: adapter})
}

func NewWithDependencies(cfg config.Config, dependencies Dependencies) (*App, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	writeAuthority := dependencies.WriteAuthority
	if writeAuthority == nil {
		writeAuthority = unavailableWriteAuthority{}
	}
	writeReady, writeReason := writeAuthority.Ready(context.Background())
	if cfg.StartupProfile == config.StartupMigrationValidation && (writeReady || writeReason != "writer_role_unavailable") {
		return nil, fmt.Errorf("migration-validation requires verified absent runtime writer USAGE")
	}
	validationProfile := cfg.StartupProfile == config.StartupMigrationValidation
	if dependencies.ManualGateway == nil {
		return nil, fmt.Errorf("Manual Gateway dependency is required")
	}
	contract, err := loadContract()
	if err != nil {
		return nil, err
	}
	var sessionStore session.Store = session.NewMemoryStore()
	var identityStore session.IdentityStore
	var auditStore audit.Store = audit.NewMemoryStore()
	var reservationStore reservations.Store = reservations.NewMemoryStore()
	var commandStore orders.Store = orders.NewMemoryStore()
	var ledgerStore ledger.Store = ledger.NewMemoryStore()
	portfolioProjection := portfolio.NewProjection()
	if dependencies.Database != nil {
		sessionStore = postgres.NewSessionStore(dependencies.Database)
		persistentIdentities := postgres.NewIdentityStore(dependencies.Database)
		if !validationProfile {
			if err := seedBootstrapIdentity(context.Background(), persistentIdentities, cfg); err != nil {
				return nil, fmt.Errorf("seed bootstrap identity: %w", err)
			}
		}
		identityStore = persistentIdentities
		auditStore = postgres.NewAuditStore(dependencies.Database)
		reservationStore = postgres.NewReservationStore(dependencies.Database)
		commandStore = postgres.NewCommandStore(dependencies.Database)
		ledgerStore = postgres.NewLedgerStore(dependencies.Database)
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
	auditService, err := audit.New(auditStore, nil, randomID("audit"))
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
	activityLog, err := tradinghttp.NewActivityLog(1024, now, hub)
	if err != nil {
		return nil, err
	}
	runtimeProjection := dependencies.RuntimeProjection
	if runtimeProjection == nil {
		runtimeProjection = tradinghttp.FailClosedAccountRuntimeProjectionReader{}
	}
	var manualAccounts tradinghttp.ManualAccountAuthority = unavailableManualAccounts{}
	if dependencies.ManualAccounts != nil {
		manualAccounts = dependencies.ManualAccounts
	}
	readiness := platformsystem.NewReadiness()
	databaseReady := dependencies.Database != nil
	readiness.Update("process", readinessComponent(true, ""))
	readiness.Update("database", readinessComponent(databaseReady, "database_dependency_missing"))
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
	// Accounts are the gateway's record now, not this service's. Readiness for
	// them therefore means one thing here: can we reach the authority that owns
	// them. A probe of the gateway's private plane answers exactly that, and
	// answering it any other way would be asserting a fact we no longer hold.
	readiness.SetEvaluator("accounts", func() platformsystem.Component {
		if dependencies.RuntimeProbe == nil {
			return readinessComponent(false, "account_authority_probe_missing")
		}
		probeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := dependencies.RuntimeProbe.CheckRuntimeRoute(probeCtx); err != nil {
			return readinessComponent(false, "account_authority_unavailable")
		}
		return readinessComponent(true, "")
	})
	readiness.SetEvaluator("canonicalWrites", func() platformsystem.Component {
		ready, reason := writeAuthority.Ready(context.Background())
		return readinessComponent(ready, reason)
	})
	authenticate := httpapi.SessionAuthenticator(sessionService)
	principal := func(r *http.Request) (string, string, error) {
		cookie, cookieErr := r.Cookie(httpapi.SessionCookieName)
		if cookieErr != nil {
			return "", "", cookieErr
		}
		result, currentErr := sessionService.Current(r.Context(), cookie.Value)
		return result.Principal.ID, result.Principal.Mode, currentErr
	}
	publicAPI, err := tradinghttp.NewPublicDataAPI(tradinghttp.PublicDataDependencies{Catalog: catalogProjection, MarketData: marketService, Readiness: readiness, Realtime: hub, Principal: principal})
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
	reconciliationGateway := dependencies.ReconciliationGateway
	var reconciliationRunner *orders.Reconciler
	if !validationProfile && reconciliationGateway != nil {
		reconciliationRunner, err = orders.NewReconciler(commandService, reconciliationGateway, reconciliationDispatcher{manual: manualService}, orders.Mode(cfg.Mode), now)
		if err != nil {
			return nil, err
		}
	}
	// The trading surface is mounted through the auth edge's downstream hook
	// rather than being wired into it field by field. This is the only place
	// that knows both packages, so when trading moves to its own process only
	// this closure changes: it becomes a proxy registration, and neither the
	// auth edge nor any trading handler is touched.
	browserHandler, err := httpapi.New(httpapi.Options{
		Sessions: sessionService, Mode: cfg.Mode, ContractVersion: httpapi.ContractVersion,
		WriteAuthority: writeAuthority,
		Downstream: func(mux *http.ServeMux) ([]string, error) {
			return tradinghttp.Register(mux, tradinghttp.Options{
				Audit:       &tradinghttp.AuditRouteOptions{Service: auditService, Authenticate: authenticate},
				PublicData:  publicAPI,
				Trading:     &tradinghttp.TradingOptions{Reservations: reservationService, Commands: commandService, Portfolio: portfolioProjection, Ledger: ledgerService, Catalog: catalogProjection, Manual: manualService, ManualAccounts: manualAccounts, ManualMarketData: marketService, Principal: principal, AuthorizeMutation: httpapi.SessionMutationAuthorizer(sessionService), Activity: activityLog},
				Projections: &tradinghttp.BrowserProjectionRoutesOptions{Runtimes: runtimeProjection, Commands: commandStore, Activity: activityLog, Principal: principal},
			})
		},
	})
	if err != nil {
		return nil, err
	}
	return &App{
		cfg:        cfg,
		contract:   contract,
		browser:    &http.Server{Addr: cfg.BrowserListenAddr, Handler: browserHandler},
		reconciler: reconciliationRunner,
	}, nil
}

// unavailableManualAccounts stands in when no account authority was wired. It
// refuses rather than returning a zero account, because a zero account reads as
// "not active, not live" — a shape the manual product would reject for the
// wrong reason, blaming the account instead of the missing wiring.
type unavailableManualAccounts struct{}

func (unavailableManualAccounts) Get(context.Context, string, string) (tradinghttp.ManualAccount, error) {
	return tradinghttp.ManualAccount{}, errors.New("account authority unavailable")
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
		go func() { _ = a.browser.Serve(a.browserLn) }()
		if a.reconciler != nil {
			reconcileCtx, cancel := context.WithCancel(ctx)
			a.reconcileStop = cancel
			a.reconcileDone = make(chan struct{})
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
		if a.browser != nil {
			if err := a.browser.Shutdown(ctx); err != nil && first == nil {
				first = err
			}
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
	authorityResult, err := postgres.VerifyRuntimeAuthority(ctx, database, authority.PlatformV15())
	if err != nil {
		return fmt.Errorf("verify Platform database authority: %w", err)
	}
	writeAuthority := NewWriteAuthority(WriteAuthorityFacts{
		RuntimeInherits: authorityResult.RuntimeInherits, WriterUsage: authorityResult.WriterMembership,
		FullCapabilityGrants:         authorityResult.FullCapabilityGrants,
		EffectiveTablePrivileges:     authorityResult.EffectiveTablePrivileges,
		EffectiveSequencePrivileges:  authorityResult.EffectiveSequencePrivileges,
		CanonicalOwnershipViolations: len(authorityResult.OwnershipViolations),
	})
	adapter, err := newGatewayAdapterForMode(cfg.GatewayURL, cfg.GatewayToken, orders.Mode(cfg.Mode), cfg.LivePrivateWritesEnabled, nil)
	if err != nil {
		return err
	}
	app, err := NewWithDependencies(cfg, Dependencies{Database: database, CatalogSource: adapter, MarketDataSource: gatewayMarketDataSource{adapter: adapter}, MarketDataProbe: adapter, RuntimeProbe: adapter, RuntimeProjection: adapter, ManualGateway: adapter, ManualAccounts: gatewayManualAccounts{adapter: adapter}, ReconciliationGateway: adapter, WriteAuthority: writeAuthority})
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
