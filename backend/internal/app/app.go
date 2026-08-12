package app

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/backend/internal/config"
	"github.com/QuantProcessing/boltertrader/backend/internal/httpapi"
	"github.com/QuantProcessing/boltertrader/backend/internal/store/postgres"
	"github.com/QuantProcessing/boltertrader/gateway/authority"
	platformsystem "github.com/QuantProcessing/boltertrader/internal/system"
	"github.com/QuantProcessing/boltertrader/services/authedge/session"
	authedgepostgres "github.com/QuantProcessing/boltertrader/services/authedge/store/postgres"
	tradingapp "github.com/QuantProcessing/boltertrader/services/trading/app"
	"github.com/QuantProcessing/boltertrader/services/trading/catalog"
	tradinggateway "github.com/QuantProcessing/boltertrader/services/trading/gateway"
	tradinghttp "github.com/QuantProcessing/boltertrader/services/trading/httpapi"
	"github.com/QuantProcessing/boltertrader/services/trading/manual"
	"github.com/QuantProcessing/boltertrader/services/trading/marketdata"
	"github.com/QuantProcessing/boltertrader/services/trading/orders"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type App struct {
	cfg           config.Config
	browser       *http.Server
	browserLn     net.Listener
	startOnce     sync.Once
	shutdownOnce  sync.Once
	trading       *tradingapp.Service
	reconcileStop context.CancelFunc
	reconcileDone chan struct{}
}

type Dependencies struct {
	Database *sql.DB
	// TradingDatabase is deliberately separate from Database. The trading
	// stores now speak their own schema — bare `commands`, `reservations`,
	// `ledger_entries` — so pointing them at the platform database would send
	// their statements at tables that are not there. Until trading runs as its
	// own process it stays unset, and the trading side falls back to the same
	// in-memory stores it uses when no database is configured at all.
	TradingDatabase       *sql.DB
	CatalogSource         catalog.Source
	MarketDataSource      marketdata.Source
	MarketDataProbe       tradingapp.MarketDataRouteProbe
	RuntimeProbe          tradingapp.RuntimeRouteProbe
	RuntimeProjection     tradinghttp.AccountRuntimeProjectionReader
	ManualGateway         manual.Gateway
	ManualAccounts        tradinghttp.ManualAccountAuthority
	ReconciliationGateway orders.ReconciliationGateway
	WriteAuthority        WriteAuthority
}

// tradingDependencies restates the Gateway-facing dependencies in the trading
// service's own vocabulary. The two structs stay separate because they answer
// different questions: this one is what a host can supply, that one is what
// trading requires, and the day they stop matching is the day trading gained a
// dependency the auth edge has no business knowing about.
func (dependencies Dependencies) tradingDependencies() tradingapp.Dependencies {
	return tradingapp.Dependencies{
		Database:              dependencies.TradingDatabase,
		CatalogSource:         dependencies.CatalogSource,
		MarketDataSource:      dependencies.MarketDataSource,
		MarketDataProbe:       dependencies.MarketDataProbe,
		RuntimeProbe:          dependencies.RuntimeProbe,
		RuntimeProjection:     dependencies.RuntimeProjection,
		ManualGateway:         dependencies.ManualGateway,
		ManualAccounts:        dependencies.ManualAccounts,
		ReconciliationGateway: dependencies.ReconciliationGateway,
	}
}

func New(cfg config.Config) (*App, error) {
	gatewayClient, err := tradinggateway.NewClientForMode(cfg.GatewayURL, cfg.GatewayToken, orders.Mode(cfg.Mode), cfg.LivePrivateWritesEnabled, nil)
	if err != nil {
		return nil, err
	}
	return NewWithDependencies(cfg, gatewayDependencies(gatewayClient))
}

// gatewayDependencies fills every Gateway-facing field from one client. They
// stay separate fields rather than one because tests substitute a different fake
// per capability; that one client answers all of them is a fact about the
// client, not a shape the assembly should assume.
func gatewayDependencies(client *tradinggateway.Client) Dependencies {
	return Dependencies{
		CatalogSource:         client,
		MarketDataSource:      client.MarketData(),
		MarketDataProbe:       client,
		RuntimeProbe:          client,
		RuntimeProjection:     client,
		ManualGateway:         client,
		ManualAccounts:        client.Accounts(),
		ReconciliationGateway: client,
	}
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

	var sessionStore session.Store = session.NewMemoryStore()
	var identityStore session.IdentityStore
	if dependencies.Database != nil {
		sessionStore = authedgepostgres.NewSessionStore(dependencies.Database)
		persistentIdentities := authedgepostgres.NewIdentityStore(dependencies.Database)
		if !validationProfile {
			if err := seedBootstrapIdentity(context.Background(), persistentIdentities, cfg); err != nil {
				return nil, fmt.Errorf("seed bootstrap identity: %w", err)
			}
		}
		identityStore = persistentIdentities
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

	// Identity is resolved here and handed down. The trading service asks who is
	// calling without knowing that the answer comes from a session cookie, which
	// is what lets the same assembly serve a service token when trading moves
	// out of this process.
	tradingDependencies := dependencies.tradingDependencies()
	tradingDependencies.Authenticate = httpapi.SessionAuthenticator(sessionService)
	tradingDependencies.AuthorizeMutation = httpapi.SessionMutationAuthorizer(sessionService)
	tradingDependencies.Principal = func(r *http.Request) (string, string, error) {
		cookie, cookieErr := r.Cookie(httpapi.SessionCookieName)
		if cookieErr != nil {
			return "", "", cookieErr
		}
		result, currentErr := sessionService.Current(r.Context(), cookie.Value)
		return result.Principal.ID, result.Principal.Mode, currentErr
	}
	trading, err := tradingapp.New(tradingapp.Config{
		Mode:                     cfg.Mode,
		LivePrivateWritesEnabled: cfg.LivePrivateWritesEnabled,
		ManualPreviewSigningKey:  cfg.ManualPreviewSigningKey,
		// A migration-validation startup must not issue Gateway writes, and the
		// reconciler is the one component that would do so unprompted.
		ReconcilerDisabled: validationProfile,
	}, tradingDependencies)
	if err != nil {
		return nil, err
	}
	cfg.ManualPreviewSigningKey = nil

	// The two components trading cannot see: whether the database this edge
	// authenticates against is up, and whether it may be written to. Both are
	// facts about the platform database, which trading does not hold.
	readiness := trading.Readiness()
	readiness.Update("database", readinessComponent(dependencies.Database != nil, "database_dependency_missing"))
	readiness.SetEvaluator("canonicalWrites", func() platformsystem.Component {
		ready, reason := writeAuthority.Ready(context.Background())
		return readinessComponent(ready, reason)
	})

	// The trading surface is mounted through the auth edge's downstream hook
	// rather than being wired into it field by field. This is the only place
	// that knows both packages, so when trading moves to its own process only
	// this closure changes: it becomes a proxy registration, and neither the
	// auth edge nor any trading handler is touched.
	browserHandler, err := httpapi.New(httpapi.Options{
		Sessions: sessionService, Mode: cfg.Mode, ContractVersion: httpapi.ContractVersion,
		WriteAuthority: writeAuthority,
		Downstream:     trading.Register,
	})
	if err != nil {
		return nil, err
	}
	return &App{
		cfg:     cfg,
		browser: &http.Server{Addr: cfg.BrowserListenAddr, Handler: browserHandler},
		trading: trading,
	}, nil
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
		if a.trading.HasReconciler() {
			reconcileCtx, cancel := context.WithCancel(ctx)
			a.reconcileStop = cancel
			a.reconcileDone = make(chan struct{})
			go func() {
				defer close(a.reconcileDone)
				a.trading.Run(reconcileCtx)
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
	gatewayClient, err := tradinggateway.NewClientForMode(cfg.GatewayURL, cfg.GatewayToken, orders.Mode(cfg.Mode), cfg.LivePrivateWritesEnabled, nil)
	if err != nil {
		return err
	}
	dependencies := gatewayDependencies(gatewayClient)
	dependencies.Database = database
	dependencies.WriteAuthority = writeAuthority
	app, err := NewWithDependencies(cfg, dependencies)
	if err != nil {
		return err
	}
	return app.Start(ctx)
}
