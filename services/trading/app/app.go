// Package app assembles the trading service out of its parts: the reservation
// gate, the command ledger, the accounting ledger, the portfolio projection,
// the manual product, the activity log and business realtime projection, the readiness view,
// and the background reconciler.
//
// It is deliberately host-neutral. The trading binary serves it in production,
// while tests mount the same service on their own mux.
// Everything a host does differently is an injected dependency rather than a
// branch in here: who the caller is (Principal, Authenticate,
// AuthorizeMutation), where the durable state lives (Database), and how the
// Gateway is reached.
package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	platformsystem "github.com/QuantProcessing/boltertrader/internal/system"
	"github.com/QuantProcessing/boltertrader/services/trading/accountpositions"
	"github.com/QuantProcessing/boltertrader/services/trading/arbitrage"
	"github.com/QuantProcessing/boltertrader/services/trading/audit"
	"github.com/QuantProcessing/boltertrader/services/trading/catalog"
	"github.com/QuantProcessing/boltertrader/services/trading/httpapi"
	"github.com/QuantProcessing/boltertrader/services/trading/ledger"
	"github.com/QuantProcessing/boltertrader/services/trading/manual"
	"github.com/QuantProcessing/boltertrader/services/trading/marketdata"
	"github.com/QuantProcessing/boltertrader/services/trading/orders"
	"github.com/QuantProcessing/boltertrader/services/trading/portfolio"
	"github.com/QuantProcessing/boltertrader/services/trading/reservations"
	tradingpg "github.com/QuantProcessing/boltertrader/services/trading/store/postgres"
)

// MarketDataRouteProbe and RuntimeRouteProbe are declared here, beside the
// readiness evaluators that ask them, rather than beside the Gateway client
// that answers them. Readiness is this package's judgement about what a
// reachable route means; the client only reports whether the route replied.
type MarketDataRouteProbe interface {
	CheckMarketDataRoute(context.Context) error
}

type RuntimeRouteProbe interface {
	CheckRuntimeRoute(context.Context) error
}

// Config is the trading service's own configuration, not a host's. A host with
// a larger config passes down the fields that mean something here and keeps the
// rest.
type Config struct {
	// LivePrivateWritesEnabled arms private writes. Every command reaches a real
	// venue, so this is the only switch between an accepted command and the
	// exchange. It used to also have to be paired with a live Mode, back when a
	// service could run under a test mode that fenced writes on its own.
	LivePrivateWritesEnabled bool
	// ManualPreviewSigningKey seals execution previews. New zeroes it before
	// returning, so the caller's copy does not outlive the call.
	ManualPreviewSigningKey []byte
	// ReconcilerDisabled suppresses the background reconciler. A host running a
	// migration-validation startup has no business issuing Gateway writes, and
	// the reconciler is the one component that would do so unprompted.
	ReconcilerDisabled bool
}

// Dependencies names everything the trading service cannot decide for itself.
//
// The eight Gateway-facing fields stay separate even though one client answers
// all of them. That one type satisfies all eight is a fact about the client,
// not about this assembly: tests substitute a different fake per capability,
// and collapsing them into a single interface would force every test to
// implement eight methods to exercise one.
type Dependencies struct {
	// Database is trading's own. Left nil, every store falls back to its
	// in-memory form, which is what the tests and a first local run want.
	Database              *sql.DB
	CatalogSource         catalog.Source
	MarketDataSource      marketdata.Source
	FreshMarketData       httpapi.MarketDataAuthority
	FreshBBO              marketdata.FreshBBOSource
	FundingBoardStream    marketdata.BoardStream
	MarketTopics          httpapi.MarketTopicProjection
	RealtimeObserver      httpapi.RealtimeObserver
	MarketDataProbe       MarketDataRouteProbe
	RuntimeProbe          RuntimeRouteProbe
	RuntimeProjection     httpapi.AccountRuntimeProjectionReader
	BalanceProjection     httpapi.AccountBalanceProjectionReader
	PositionProjection    httpapi.AccountPositionProjectionReader
	AccountPositionSource accountpositions.Source
	ManualGateway         manual.Gateway
	ManualAccounts        httpapi.ManualAccountAuthority
	ReconciliationGateway orders.ReconciliationGateway
	// Principal, Authenticate and AuthorizeMutation are how a host answers "who
	// is asking, and may they write". The backend reads a session cookie; the
	// trading binary reads a service token plus an identity header. No handler
	// in this service knows which.
	Principal         func(*http.Request) (principalID string, err error)
	Authenticate      func(*http.Request) (principalID string, err error)
	AuthorizeMutation func(*http.Request) error
}

// Service is an assembled trading service. It holds no listener of its own:
// Register mounts it on whatever mux the host provides, and Run drives the
// background reconciler on whatever lifecycle the host has.
type Service struct {
	routes      httpapi.Options
	readiness   *platformsystem.Readiness
	reconciler  *orders.Reconciler
	catalog     *catalog.Projection
	board       *marketdata.BoardService
	boardStream marketdata.BoardStream
	positions   *accountpositions.Service
	arbitrage   *httpapi.ArbitrageProjection
}

// catalogTTL is how long the catalog projection's facts are considered fresh;
// after it, Status reports a degraded stale reason while the last validated
// directory remains readable. catalogRefresh is how often Run renews it.
//
// The refresh has to be strictly shorter than the TTL, and by enough to absorb
// a slow fetch or a missed tick: facts renewed only once they have already
// expired leave a window in which a healthy service reports itself unready. A
// third of the TTL gives two chances to land before that window opens.
//
// The TTL itself is not ours to pick freely. It is measured against the
// Gateway's observedAt, which is the moment the Gateway loaded the oldest
// catalog it is reporting — not the moment we fetched it — so refreshing faster
// cannot make it smaller. The Gateway republishes on its own timer (once a
// minute) and vouches for a catalog for two minutes; a TTL under that rejects
// facts the authority that produced them still considers current, and the
// service reports a stale directory for whatever fraction of each cycle the
// two numbers disagree by. Matching the Gateway's own validity window keeps the
// degradation signal meaningful without blanking public reads.
const (
	catalogTTL            = 2 * time.Minute
	catalogRefresh        = catalogTTL / 3
	readinessProbeTimeout = 3 * time.Second
)

// New assembles the service. It reports a configuration error rather than
// degrading: a trading service missing its command store or its principal
// resolver would answer requests wrongly instead of not at all.
func New(cfg Config, dependencies Dependencies) (*Service, error) {
	if dependencies.ManualGateway == nil {
		return nil, fmt.Errorf("trading Manual Gateway dependency is required")
	}
	if dependencies.Principal == nil || dependencies.Authenticate == nil || dependencies.AuthorizeMutation == nil {
		return nil, fmt.Errorf("trading caller identity dependencies are required")
	}

	now := func() time.Time { return time.Now().UTC() }
	auditStore, reservationStore, commandStore, ledgerStore, portfolioProjection := stores(dependencies.Database)

	auditService, err := audit.New(auditStore, nil, sequentialID("audit"))
	if err != nil {
		return nil, err
	}
	catalogSource := dependencies.CatalogSource
	if catalogSource == nil {
		catalogSource = unavailableCatalogSource{}
	}
	marketSource := dependencies.MarketDataSource
	if marketSource == nil {
		marketSource = unavailableMarketDataSource{}
	}
	catalogProjection, err := catalog.NewProjection(catalogSource, catalogTTL, now)
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
	boardService, err := marketdata.NewBoardService(now)
	if err != nil {
		return nil, err
	}
	arbitrageProjection, err := httpapi.NewArbitrageProjection(catalogProjection, boardService, dependencies.MarketTopics, now)
	if err != nil {
		return nil, err
	}
	var accountPositionProjection *accountpositions.Service
	if dependencies.AccountPositionSource != nil {
		var accountPositionStore accountpositions.Store = accountpositions.NewMemoryStore()
		if dependencies.Database != nil {
			accountPositionStore = tradingpg.NewAccountPositionStore(dependencies.Database)
		}
		accountPositionProjection, err = accountpositions.NewService(dependencies.AccountPositionSource, accountPositionStore, now)
		if err != nil {
			return nil, err
		}
	}
	activityLog, err := httpapi.NewActivityLog(1024, now)
	if err != nil {
		return nil, err
	}
	runtimeProjection := dependencies.RuntimeProjection
	if runtimeProjection == nil {
		runtimeProjection = httpapi.FailClosedAccountRuntimeProjectionReader{}
	}
	balanceProjection := dependencies.BalanceProjection
	if balanceProjection == nil {
		balanceProjection = httpapi.FailClosedAccountBalanceProjectionReader{}
	}
	positionProjection := dependencies.PositionProjection
	if positionProjection == nil {
		positionProjection = httpapi.FailClosedAccountPositionProjectionReader{}
	}
	var manualAccounts httpapi.ManualAccountAuthority = unavailableManualAccounts{}
	if dependencies.ManualAccounts != nil {
		manualAccounts = dependencies.ManualAccounts
	}

	readiness := newReadiness(dependencies, catalogProjection)
	publicAPI, err := httpapi.NewPublicDataAPI(httpapi.PublicDataDependencies{
		Readiness:        readiness,
		RealtimeObserver: dependencies.RealtimeObserver,
		Arbitrage:        arbitrageProjection,
	})
	if err != nil {
		return nil, err
	}

	reservationService := reservations.NewService(reservationStore, now)
	commandService := orders.NewService(commandStore, reservationGate{service: reservationService}, now)
	commandService.SetLiveWritesEnabled(cfg.LivePrivateWritesEnabled)
	ledgerService := ledger.NewService(ledgerStore, now)
	manualService := manual.NewGatewayServiceWithPreviewKey(commandService, portfolioProjection, ledgerService, dependencies.ManualGateway, now, cfg.ManualPreviewSigningKey)
	if err := manualService.SetExecutionAdmission(cfg.LivePrivateWritesEnabled); err != nil {
		return nil, err
	}
	// Arbitrage takes the same signing key and the same Gateway channel: a hedge
	// leg is placed the way a manual order is, and both constructors clone the
	// key, so the zeroing below still leaves nothing for a later caller to reuse.
	// The two products cannot open each other's previews despite the shared key —
	// arbitrage's MAC covers an extra domain label for exactly that reason.
	arbitrageService := arbitrage.NewGatewayServiceWithPreviewKey(commandService, portfolioProjection, ledgerService, dependencies.ManualGateway, now, cfg.ManualPreviewSigningKey)
	if err := arbitrageService.SetExecutionAdmission(cfg.LivePrivateWritesEnabled); err != nil {
		return nil, err
	}
	for index := range cfg.ManualPreviewSigningKey {
		cfg.ManualPreviewSigningKey[index] = 0
	}
	cfg.ManualPreviewSigningKey = nil

	var reconciler *orders.Reconciler
	if !cfg.ReconcilerDisabled && dependencies.ReconciliationGateway != nil {
		reconciler, err = orders.NewReconciler(commandService, dependencies.ReconciliationGateway, reconciliationDispatcher{manual: manualService, arbitrage: arbitrageService}, now)
		if err != nil {
			return nil, err
		}
		reconciler.SetSettlementObserver(func(command orders.Command) {
			httpapi.PublishCommandActivity(activityLog, command)
		})
	}

	return &Service{
		routes: httpapi.Options{
			Audit:      &httpapi.AuditRouteOptions{Service: auditService, Authenticate: dependencies.Authenticate},
			PublicData: publicAPI,
			Trading: &httpapi.TradingOptions{
				Reservations: reservationService, Commands: commandService, Portfolio: portfolioProjection,
				Ledger: ledgerService, Catalog: catalogProjection, Manual: manualService,
				ManualAccounts: manualAccounts,
				Arbitrage:      arbitrageService, FundingBoard: boardService, MarketData: marketService,
				FreshMarketData: dependencies.FreshMarketData,
				FreshBBO:        dependencies.FreshBBO,
				Principal:       dependencies.Principal, AuthorizeMutation: dependencies.AuthorizeMutation,
				Activity: activityLog,
			},
			Projections: &httpapi.BrowserProjectionRoutesOptions{
				Runtimes: runtimeProjection, Balances: balanceProjection, Positions: positionProjection,
				PortfolioPositions: accountPositionProjection, Commands: commandStore, Activity: activityLog,
				Principal: dependencies.Principal,
			},
		},
		readiness:   readiness,
		reconciler:  reconciler,
		catalog:     catalogProjection,
		board:       boardService,
		boardStream: dependencies.FundingBoardStream,
		positions:   accountPositionProjection,
		arbitrage:   arbitrageProjection,
	}, nil
}

// Register mounts the service's routes on mux and reports which ones it took.
// The host needs that list when it also serves a frozen contract: routes named
// there but mounted here must not be shadowed by a scaffold.
func (service *Service) Register(mux *http.ServeMux) ([]string, error) {
	return httpapi.Register(mux, service.routes)
}

// Readiness exposes the readiness view so a host can report components trading
// cannot see. It reports "process", "gateway", "publicData" and "accounts"
// itself; the host owns "database", because which database has to be up is a
// fact about the deployment — the backend answers for the platform database it
// authenticates against, and a standalone trading process answers for its own.
// A required component nobody reports renders as "not_reported", so the host
// must report it even when the answer is no.
func (service *Service) Readiness() *platformsystem.Readiness { return service.readiness }

// Run maintains the service's background state until ctx is cancelled: it keeps
// the catalog projection warm, and drives the reconciler when one was assembled.
func (service *Service) Run(ctx context.Context) {
	refreshed := make(chan struct{})
	go func() {
		defer close(refreshed)
		service.refreshCatalog(ctx)
	}()
	boardDone := make(chan struct{})
	go func() {
		defer close(boardDone)
		if service.boardStream == nil {
			<-ctx.Done()
			return
		}
		_ = service.boardStream.RunFundingBoard(ctx, service.board.Replace)
	}()
	positionsDone := make(chan struct{})
	go func() {
		defer close(positionsDone)
		if service.positions == nil {
			<-ctx.Done()
			return
		}
		service.positions.Run(ctx)
	}()
	arbitrageDone := make(chan struct{})
	go func() {
		defer close(arbitrageDone)
		service.arbitrage.Run(ctx)
	}()
	if service.reconciler != nil {
		service.reconciler.Run(ctx)
	} else {
		<-ctx.Done()
	}
	<-refreshed
	<-boardDone
	<-positionsDone
	<-arbitrageDone
}

// refreshCatalog renews the catalog projection for as long as ctx lives.
//
// Readiness reports the projection, so something has to maintain it whether or
// not a client asks for it. Left to Snapshot's demand-driven faulting, a service
// whose Gateway is perfectly reachable reports "catalog_unavailable" from
// startup until unrelated traffic happens to arrive, and reports
// "catalog_projection_stale" again after any lull longer than the TTL. Both
// describe this loop's absence rather than anything about the Gateway.
func (service *Service) refreshCatalog(ctx context.Context) {
	ticker := time.NewTicker(catalogRefresh)
	defer ticker.Stop()
	for {
		// A failed refresh is not logged here. The projection already records
		// why as the reason readiness reports, which is where an operator can
		// act on it; logging as well would repeat one line per tick for as long
		// as a Gateway takes to come up, which on a cold stack is every time.
		refreshCtx, cancel := context.WithTimeout(ctx, catalogRefresh)
		_ = service.catalog.Refresh(refreshCtx)
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// stores picks durable or in-memory backing for every store in one place, so
// there is no way to end up with half the service persistent and half not.
func stores(database *sql.DB) (audit.Store, reservations.Store, orders.Store, ledger.Store, *portfolio.Projection) {
	if database == nil {
		return audit.NewMemoryStore(), reservations.NewMemoryStore(), orders.NewMemoryStore(), ledger.NewMemoryStore(), portfolio.NewProjection()
	}
	return tradingpg.NewAuditStore(database), tradingpg.NewReservationStore(database), tradingpg.NewCommandStore(database),
		tradingpg.NewLedgerStore(database), portfolio.NewPersistentProjection(tradingpg.NewPortfolioStore(database))
}

// newReadiness reports every component trading can see for itself. "database"
// is left to the host: see Service.Readiness.
func newReadiness(dependencies Dependencies, catalogProjection *catalog.Projection) *platformsystem.Readiness {
	readiness := platformsystem.NewReadiness()
	readiness.Update("process", component(true, ""))
	readiness.SetEvaluator("gateway", func() platformsystem.Component {
		status := catalogProjection.Status()
		return platformsystem.Component{Ready: status.Ready, Reason: status.Reason, ObservedAt: status.ObservedAt}
	})
	readiness.SetEvaluator("publicData", func() platformsystem.Component {
		if dependencies.MarketDataProbe == nil {
			return component(false, "market_data_route_probe_missing")
		}
		ctx, cancel := context.WithTimeout(context.Background(), readinessProbeTimeout)
		defer cancel()
		if err := dependencies.MarketDataProbe.CheckMarketDataRoute(ctx); err != nil {
			return component(false, "market_data_route_unavailable")
		}
		return component(true, "")
	})
	// Accounts are the Gateway's record, not this service's. Readiness for them
	// therefore means one thing here: can we reach the authority that owns them.
	// A probe of the Gateway's private plane answers exactly that, and answering
	// it any other way would assert a fact we no longer hold.
	readiness.SetEvaluator("accounts", func() platformsystem.Component {
		if dependencies.RuntimeProbe == nil {
			return component(false, "account_authority_probe_missing")
		}
		ctx, cancel := context.WithTimeout(context.Background(), readinessProbeTimeout)
		defer cancel()
		if err := dependencies.RuntimeProbe.CheckRuntimeRoute(ctx); err != nil {
			return component(false, "account_authority_unavailable")
		}
		return component(true, "")
	})
	return readiness
}

func component(ready bool, missingReason string) platformsystem.Component {
	if ready {
		return platformsystem.Component{Ready: true, Reason: "ready"}
	}
	return platformsystem.Component{Ready: false, Reason: missingReason}
}

// unavailableManualAccounts stands in when no account authority was wired. It
// refuses rather than returning a zero account, because a zero account reads as
// "not active, not live" — a shape the manual product would reject for the
// wrong reason, blaming the account instead of the missing wiring.
type unavailableManualAccounts struct{}

func (unavailableManualAccounts) Get(context.Context, string, string) (httpapi.ManualAccount, error) {
	return httpapi.ManualAccount{}, errors.New("account authority unavailable")
}

type unavailableCatalogSource struct{}

func (unavailableCatalogSource) Fetch(context.Context) (catalog.Snapshot, error) {
	return catalog.Snapshot{}, catalog.ErrUnavailable
}

type unavailableMarketDataSource struct{}

func (unavailableMarketDataSource) Fetch(context.Context, marketdata.Query) (marketdata.Fact, error) {
	return marketdata.Fact{}, errors.New("gateway projection unavailable")
}

// reservationGate narrows the reservation service to the one question the
// command ledger asks of it, so the ledger cannot reach the rest of it.
type reservationGate struct{ service *reservations.Service }

func (gate reservationGate) Consume(ctx context.Context, claim orders.ReservationClaim) error {
	_, err := gate.service.Consume(ctx, reservations.ConsumeRequest{ReservationID: claim.ReservationID, PrincipalID: claim.PrincipalID, ProductID: claim.ProductID})
	return err
}

func sequentialID(prefix string) func() string {
	var mu sync.Mutex
	var next uint64
	return func() string { mu.Lock(); defer mu.Unlock(); next++; return fmt.Sprintf("%s_%016x", prefix, next) }
}
