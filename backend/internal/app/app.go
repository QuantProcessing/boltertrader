package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
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
	"github.com/QuantProcessing/boltertrader/backend/internal/store/postgres"
	"github.com/QuantProcessing/boltertrader/backend/internal/workapi"
	backendcontract "github.com/QuantProcessing/boltertrader/internal/productplatformcontract"
)

type App struct {
	cfg          config.Config
	contract     backendcontract.Contract
	browser      *http.Server
	work         *http.Server
	browserLn    net.Listener
	workLn       net.Listener
	startOnce    sync.Once
	shutdownOnce sync.Once
}

type Dependencies struct {
	Database         *sql.DB
	SecretProvider   secret.Provider
	CatalogSource    catalog.Source
	MarketDataSource marketdata.Source
}

func New(cfg config.Config) (*App, error) {
	return NewWithDependencies(cfg, Dependencies{})
}

func NewWithDependencies(cfg config.Config, dependencies Dependencies) (*App, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
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
	gatewayReady := dependencies.CatalogSource != nil && dependencies.MarketDataSource != nil
	readiness.Update("process", readinessComponent(true, ""))
	readiness.Update("database", readinessComponent(databaseReady, "database_dependency_missing"))
	readiness.Update("secretStore", readinessComponent(secretReady, "secret_provider_missing"))
	readiness.Update("gateway", readinessComponent(gatewayReady, "gateway_projection_missing"))
	readiness.Update("publicData", readinessComponent(gatewayReady, "gateway_projection_missing"))
	readiness.Update("accounts", readinessComponent(databaseReady && secretReady, "durable_account_dependencies_missing"))
	readiness.Update("fundingWorker", readinessComponent(false, "worker_not_registered"))
	publicAPI, err := httpapi.NewPublicDataAPI(httpapi.PublicDataDependencies{Catalog: catalogProjection, MarketData: marketService, Readiness: readiness, Realtime: hub})
	if err != nil {
		return nil, err
	}

	reservationService := reservations.NewService(reservationStore, now)
	commandService := orders.NewService(commandStore, reservationGate{service: reservationService}, now)
	// Live venue writes require an explicit future enablement path; integration stays fail-closed.
	commandService.SetLiveWritesEnabled(false)
	ledgerService := ledger.NewService(ledgerStore, now)
	authenticate := httpapi.SessionAuthenticator(sessionService)
	principal := func(r *http.Request) (string, string, error) {
		cookie, cookieErr := r.Cookie(httpapi.SessionCookieName)
		if cookieErr != nil {
			return "", "", cookieErr
		}
		result, currentErr := sessionService.Current(r.Context(), cookie.Value)
		return result.Principal.ID, result.Principal.Mode, currentErr
	}
	browserHandler, err := httpapi.New(httpapi.Options{
		Sessions: sessionService, Mode: cfg.Mode, ContractVersion: httpapi.ContractVersion,
		Accounts:   &httpapi.AccountRoutesOptions{Service: accountService, Authenticate: authenticate, AuthorizeMutation: httpapi.SessionMutationAuthorizer(sessionService), Blockers: func(context.Context, string) (accounts.Blockers, error) { return accounts.Blockers{}, nil }},
		Audit:      &httpapi.AuditRouteOptions{Service: auditService, Authenticate: authenticate},
		PublicData: publicAPI,
		Trading:    &httpapi.TradingOptions{Reservations: reservationService, Commands: commandService, Portfolio: portfolioProjection, Ledger: ledgerService, Principal: principal, AuthorizeMutation: httpapi.SessionMutationAuthorizer(sessionService)},
	})
	if err != nil {
		return nil, err
	}
	workHandler, err := workapi.New(workapi.Options{
		TokenDigest: cfg.FundingWorkerTokenDigest, Mode: cfg.Mode, ContractVersion: workapi.ContractVersion,
		Service: work.New(workapi.ContractVersion, 15*time.Second),
		OnReadiness: func(ready bool) {
			readiness.Update("fundingWorker", readinessComponent(ready, "worker_not_registered"))
		},
	})
	if err != nil {
		return nil, err
	}
	return &App{
		cfg:      cfg,
		contract: contract,
		browser:  &http.Server{Addr: cfg.BrowserListenAddr, Handler: browserHandler},
		work:     &http.Server{Addr: cfg.WorkListenAddr, Handler: workHandler},
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
	app, err := New(cfg)
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
