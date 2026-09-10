package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/credential"
	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/account"
	"github.com/QuantProcessing/boltertrader/gateway/catalog"
	"github.com/QuantProcessing/boltertrader/gateway/config"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/gateway/health"
	"github.com/QuantProcessing/boltertrader/gateway/order"
	"github.com/QuantProcessing/boltertrader/gateway/process"
	"github.com/QuantProcessing/boltertrader/gateway/publicconn"
	"github.com/QuantProcessing/boltertrader/gateway/secret"
	storepostgres "github.com/QuantProcessing/boltertrader/gateway/store/postgres"
	"github.com/QuantProcessing/boltertrader/routeprofile"

	"github.com/shopspring/decimal"
)

func TestPlatformRuntimeZeroAccountProcessIsReady(t *testing.T) {
	harness := newPlatformHarness(t)
	var keyChecks atomic.Int64
	if ready, active, routeReady := harness.manager.RuntimeReadiness(); !ready || active != 0 || routeReady != 0 {
		t.Fatalf("zero-account readiness=(%t,%d,%d)", ready, active, routeReady)
	}

	authRef := mustOperationalRef(t, "env://PLATFORM_TEST_TOKEN")
	dsnRef := mustOperationalRef(t, "env://PLATFORM_TEST_DSN")
	configFile := config.File{
		Server:   config.ServerConfig{Listen: "127.0.0.1:1", AuthTokenRef: authRef},
		Postgres: config.PostgresConfig{DSNRef: dsnRef},
	}
	pool := &integrationPool{}
	orders := &integrationOrderCoordinator{}
	service, err := process.Start(t.Context(), configFile, process.Dependencies{
		CheckCredentialResolver: func(context.Context) error { keyChecks.Add(1); return nil },
		ResolveSecret: func(_ context.Context, ref secret.Ref) (string, error) {
			switch ref.RawForProvider() {
			case authRef.RawForProvider():
				return "test-token", nil
			case dsnRef.RawForProvider():
				return "postgres://local-test", nil
			default:
				return "", errors.New("unknown operational reference")
			}
		},
		ConnectPostgres:          func(context.Context, string) (process.PostgresPool, error) { return pool, nil },
		ValidateDatabaseIdentity: func(context.Context, process.PostgresPool) error { return nil },
		VerifyDatabaseAuthority: func(context.Context, process.PostgresPool) (storepostgres.AuthorityResult, error) {
			return storepostgres.AuthorityResult{WriterMembership: true}, nil
		},
		BuildAccountManager: func(context.Context) (process.AccountManager, error) { return harness.manager, nil },
		RestoreAccountManager: func(ctx context.Context, _ process.PostgresPool, manager process.AccountManager) error {
			return nil
		},
		BuildOrderCoordinator: func(context.Context, process.PostgresPool, process.AccountManager) (process.OrderCoordinator, error) {
			return orders, nil
		},
		Listen: func(string, string) (net.Listener, error) { return &integrationListener{}, nil },
	})
	if err != nil {
		t.Fatalf("process.Start: %v", err)
	}
	t.Cleanup(func() { _ = service.Shutdown(context.Background()) })
	if readiness := service.Health().Readiness(); !readiness.Ready {
		t.Fatalf("process readiness=%+v", readiness)
	}
	if got := orders.startedAccounts(); len(got) != 0 {
		t.Fatalf("order recovery accounts=%v", got)
	}
	if checks := keyChecks.Load(); checks != 1 {
		t.Fatalf("credential resolver preflight calls=%d, want 1", checks)
	}
}

func TestPlatformRuntimeAbsentRouteDeniesOrderBeforePrivateIO(t *testing.T) {
	harness := newPlatformHarness(t)
	pending := domain.AccountID("account-route")
	harness.register(t, pending)
	router, err := order.NewAccountRouter(harness.manager)
	if err != nil {
		t.Fatal(err)
	}
	_, err = router.Route(t.Context(), pending, "binance:live:perp:USDC:BTC-USDC")
	if err == nil {
		t.Fatal("absent Perp route was accepted")
	}
	if calls := harness.builder.privateCalls.Load(); calls != 0 {
		t.Fatalf("absent route performed %d private calls", calls)
	}
}

func TestPlatformRuntimeTwoAccountsSharePublicConnectorButNotPrivateRoutes(t *testing.T) {
	harness := newPlatformHarness(t)
	first := domain.AccountID("account-one")
	second := domain.AccountID("account-two")
	firstGroup := harness.register(t, first)
	secondGroup := harness.register(t, second)

	if builds := harness.publicFactory.builds.Load(); builds != int64(len(account.RegistrationRoutes("binance"))) {
		t.Fatalf("public connector builds=%d, want 1", builds)
	}
	if refreshes := harness.publicFactory.refreshes.Load(); refreshes < int64(len(account.RegistrationRoutes("binance"))) {
		t.Fatalf("public connector refreshes=%d, want 1", refreshes)
	}
	if private := harness.builder.builds.Load(); private != 2*int64(len(account.RegistrationRoutes("binance"))) {
		t.Fatalf("private route groups=%d, want 2", private)
	}
	if firstGroup == secondGroup {
		t.Fatal("two accounts shared a private runtime generation")
	}
	if got := harness.manager.AccountIDs(); len(got) != 2 || got[0] != first || got[1] != second {
		t.Fatalf("account IDs=%v", got)
	}
}

// The harness never arms the operator switch, and that is now the whole reason
// the fence refuses. It used to be half the reason: the test built a live-mode
// manager on purpose, because every other test here ran in test mode and wrote
// past this fence for free. Nothing writes for free any more, so "live" is no
// longer a property this test has to arrange.
func TestPlatformRuntimeLiveWritesRemainDenied(t *testing.T) {
	harness := newPlatformHarness(t)
	pending := domain.AccountID("account-live")
	harness.register(t, pending)
	fence := account.WriteFence{
		AccountID: pending, RuntimeID: harness.manager.ActiveSnapshot(pending).Fence.RuntimeID,
	}
	if err := harness.manager.CheckWriteFence(fence); !errors.Is(err, account.ErrLiveWriteDisabled) {
		t.Fatalf("live write fence err=%v", err)
	}
	status, err := harness.manager.RouteStatus(pending, "spot", "")
	if err != nil || status.WriteReady || status.Reason != account.ErrLiveWriteDisabled.Error() {
		t.Fatalf("live route status=%+v err=%v", status, err)
	}
	router, err := order.NewAccountRouter(harness.manager)
	if err != nil {
		t.Fatal(err)
	}
	route, err := router.Route(t.Context(), pending, "binance:live:spot:BTC-USDT")
	if err != nil || route.WriteReady {
		t.Fatalf("live order route=%+v err=%v", route, err)
	}
	if calls := harness.builder.privateCalls.Load(); calls != 0 {
		t.Fatalf("denied live route performed %d private calls", calls)
	}
}

type platformHarness struct {
	manager        *account.Manager
	builder        *countingRouteBuilder
	publicFactory  *countingPublicFactory
	publicRegistry *publicconn.Registry
	catalog        *catalog.Service
}

func newPlatformHarness(t *testing.T) *platformHarness {
	t.Helper()
	service, err := catalog.NewService(time.Minute, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	publicFactory := &countingPublicFactory{}
	publicRegistry, err := publicconn.NewRegistry(publicFactory, routeprofile.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	builder := &countingRouteBuilder{}
	harness := &platformHarness{
		builder: builder, publicFactory: publicFactory,
		publicRegistry: publicRegistry, catalog: service,
	}
	harness.manager = harness.buildManager(t)
	t.Cleanup(func() {
		_ = harness.manager.Close()
		_ = publicRegistry.Close()
	})
	return harness
}

func (harness *platformHarness) buildManager(t *testing.T) *account.Manager {
	t.Helper()
	manager, err := account.NewManager(context.Background(), nil, account.Options{
		GatewayCatalog: harness.catalog, HTTPClient: &http.Client{Transport: denyNetworkTransport{}},
		RuntimeGroupBuilder: harness.builder,
		PublicConnectors:    harness.publicRegistry, RequirePublicConnectors: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func (harness *platformHarness) register(t *testing.T, accountID domain.AccountID) account.RegistrationConnection {
	t.Helper()
	record := account.RegistrationRecord{Registration: account.Registration{ID: accountID, Venue: "binance"}, CredentialBundle: []byte(`{"schemaVersion":1,"venue":"binance","authKind":"api_key_secret","metadata":{},"secrets":{"apiKey":"offline-key","apiSecret":"offline-secret"}}`)}
	connection, err := harness.manager.PrepareRegistration(t.Context(), record)
	if err != nil {
		t.Fatal(err)
	}
	if err := connection.Activate(); err != nil {
		t.Fatal(err)
	}
	for _, route := range account.RegistrationRoutes("binance") {
		if _, err := harness.publicRegistry.Refresh(t.Context(), routeprofile.Key{Venue: "binance", RouteProfileID: route.RouteProfileID}); err != nil {
			t.Fatal(err)
		}
	}
	return connection
}

type countingRouteBuilder struct {
	mu           sync.Mutex
	failRoute    domain.RouteID
	failErr      error
	buildHook    func(account.ConnectionSpec)
	builds       atomic.Int64
	privateCalls atomic.Int64
}

func (builder *countingRouteBuilder) BuildRoute(_ context.Context, snapshot account.ConnectionSpec, route account.RouteDefinition, _ *credential.Decoded) (account.ConnectionRoute, error) {
	builder.builds.Add(1)
	builder.mu.Lock()
	failRoute, failErr, hook := builder.failRoute, builder.failErr, builder.buildHook
	builder.mu.Unlock()
	if hook != nil {
		hook(snapshot)
	}
	handle := &offlineConnectionRoute{product: exchange.Product(route.Product), privateCalls: &builder.privateCalls}
	if route.Product == "spot" {
		handle.spot = &offlineSpotClient{privateCalls: &builder.privateCalls}
	} else {
		handle.perp = &offlinePerpClient{privateCalls: &builder.privateCalls}
	}
	if route.RouteID == failRoute {
		handle.probeErr = failErr
	}
	return handle, nil
}

func (builder *countingRouteBuilder) setFailRoute(routeID domain.RouteID, err error) {
	builder.mu.Lock()
	defer builder.mu.Unlock()
	builder.failRoute, builder.failErr = routeID, err
}

func (builder *countingRouteBuilder) setBuildHook(hook func(account.ConnectionSpec)) {
	builder.mu.Lock()
	defer builder.mu.Unlock()
	builder.buildHook = hook
}

type offlineConnectionRoute struct {
	product      exchange.Product
	spot         exchange.SpotClient
	perp         exchange.PerpClient
	probeErr     error
	privateCalls *atomic.Int64
	closed       atomic.Bool
}

func (route *offlineConnectionRoute) Probe(context.Context) error { return route.probeErr }
func (route *offlineConnectionRoute) Close() error                { route.closed.Store(true); return nil }
func (route *offlineConnectionRoute) SpotClient() (exchange.SpotClient, bool) {
	return route.spot, route.spot != nil
}
func (route *offlineConnectionRoute) PerpClient() (exchange.PerpClient, bool) {
	return route.perp, route.perp != nil
}
func (route *offlineConnectionRoute) OrderClient() (exchange.OrderREST, bool) {
	if route.spot != nil {
		return route.spot, true
	}
	if route.perp != nil {
		return route.perp, true
	}
	return nil, false
}
func (*offlineConnectionRoute) ExactLookupReady() bool { return true }

type offlineSpotClient struct {
	exchange.SpotClient
	privateCalls *atomic.Int64
}

func (client *offlineSpotClient) PlaceOrder(context.Context, exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error) {
	client.privateCalls.Add(1)
	return exchange.OrderAcknowledgement{}, errors.New("offline private call forbidden")
}
func (client *offlineSpotClient) CancelOrder(context.Context, exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error) {
	client.privateCalls.Add(1)
	return exchange.OrderAcknowledgement{}, errors.New("offline private call forbidden")
}
func (*offlineSpotClient) LookupExactOrder(context.Context, exchange.ExactOrderLookupRequest) (exchange.ExactOrderLookupResult, error) {
	return exchange.ExactOrderLookupResult{}, nil
}

type offlinePerpClient struct {
	exchange.PerpClient
	privateCalls *atomic.Int64
}

func (client *offlinePerpClient) PlaceOrder(context.Context, exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error) {
	client.privateCalls.Add(1)
	return exchange.OrderAcknowledgement{}, errors.New("offline private call forbidden")
}
func (client *offlinePerpClient) CancelOrder(context.Context, exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error) {
	client.privateCalls.Add(1)
	return exchange.OrderAcknowledgement{}, errors.New("offline private call forbidden")
}
func (*offlinePerpClient) LookupExactOrder(context.Context, exchange.ExactOrderLookupRequest) (exchange.ExactOrderLookupResult, error) {
	return exchange.ExactOrderLookupResult{}, nil
}

type countingPublicFactory struct {
	builds    atomic.Int64
	refreshes atomic.Int64
}

func (factory *countingPublicFactory) Build(_ context.Context, key routeprofile.Key) (publicconn.Connector, error) {
	factory.builds.Add(1)
	product, settlement := exchange.ProductSpot, ""
	if strings.Contains(key.RouteProfileID, ".perp.") {
		product, settlement = exchange.ProductPerp, "USDT"
	}
	return &offlinePublicConnector{instrument: platformInstrument(product, settlement), refreshes: &factory.refreshes}, nil
}

type offlinePublicConnector struct {
	instrument exchange.Instrument
	refreshes  *atomic.Int64
}

func (connector *offlinePublicConnector) Refresh(context.Context) ([]exchange.Instrument, error) {
	connector.refreshes.Add(1)
	return []exchange.Instrument{connector.instrument}, nil
}
func (*offlinePublicConnector) Close() error { return nil }

func platformInstrument(product exchange.Product, settlement string) exchange.Instrument {
	return exchange.Instrument{
		Identity: exchange.InstrumentIdentity{
			Venue: exchange.VenueBinance, Product: product, Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT",
			SettleAsset: settlement, NativeSymbol: "BTCUSDT", NativeID: "BTCUSDT",
		},
		Rules: exchange.InstrumentRules{
			PriceIncrement: decimal.RequireFromString("0.01"), QuantityIncrement: decimal.RequireFromString("0.001"), MinQuantity: decimal.RequireFromString("0.001"),
		},
	}
}

type denyNetworkTransport struct{}

func (denyNetworkTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("network access forbidden")
}

type integrationPool struct{ closed atomic.Bool }

func (*integrationPool) Ping(context.Context) error { return nil }
func (pool *integrationPool) Close()                { pool.closed.Store(true) }

type integrationListener struct{ closed atomic.Bool }

func (*integrationListener) Accept() (net.Conn, error) { return nil, errors.New("listener closed") }
func (listener *integrationListener) Close() error {
	listener.closed.Store(true)
	return nil
}
func (*integrationListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)} }

type integrationOrderCoordinator struct {
	mu       sync.Mutex
	accounts []domain.AccountID
}

func (coordinator *integrationOrderCoordinator) Start(_ context.Context, accountIDs []domain.AccountID) error {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	coordinator.accounts = append([]domain.AccountID(nil), accountIDs...)
	return nil
}
func (*integrationOrderCoordinator) Close(context.Context) error { return nil }
func (coordinator *integrationOrderCoordinator) startedAccounts() []domain.AccountID {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	return append([]domain.AccountID(nil), coordinator.accounts...)
}

func mustOperationalRef(t *testing.T, value string) secret.Ref {
	t.Helper()
	ref, err := secret.ParseRef(value)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

var _ account.ConnectionClientRoute = (*offlineConnectionRoute)(nil)
var _ health.Dependency = (*integrationPool)(nil)

func (pool *integrationPool) Healthy() bool { return !pool.closed.Load() }
