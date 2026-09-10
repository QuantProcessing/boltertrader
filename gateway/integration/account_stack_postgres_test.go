package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/credential"
	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/account"
	"github.com/QuantProcessing/boltertrader/gateway/auth"
	"github.com/QuantProcessing/boltertrader/gateway/catalog"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	gatewayevent "github.com/QuantProcessing/boltertrader/gateway/event"
	"github.com/QuantProcessing/boltertrader/gateway/health"
	gatewayhttp "github.com/QuantProcessing/boltertrader/gateway/httpapi"
	"github.com/QuantProcessing/boltertrader/gateway/order"
	"github.com/QuantProcessing/boltertrader/gateway/publicconn"
	"github.com/QuantProcessing/boltertrader/gateway/reference"
	"github.com/QuantProcessing/boltertrader/gateway/secret"
	gatewaypostgres "github.com/QuantProcessing/boltertrader/gateway/store/postgres"
	"github.com/QuantProcessing/boltertrader/routeprofile"
	"github.com/QuantProcessing/boltertrader/secretstore"
	edgehttp "github.com/QuantProcessing/boltertrader/services/authedge/httpapi"
	"github.com/QuantProcessing/boltertrader/services/authedge/proxy"
	"github.com/QuantProcessing/boltertrader/services/authedge/session"
	edgepostgres "github.com/QuantProcessing/boltertrader/services/authedge/store/postgres"
	"github.com/QuantProcessing/boltertrader/services/trading/accountpositions"
	tradinggateway "github.com/QuantProcessing/boltertrader/services/trading/gateway"
	tradinghttp "github.com/QuantProcessing/boltertrader/services/trading/httpapi"
	tradingorders "github.com/QuantProcessing/boltertrader/services/trading/orders"
	"github.com/QuantProcessing/boltertrader/services/trading/servicetoken"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/shopspring/decimal"
)

const stackToken = "offline-stack-service-token-at-least-32-bytes"
const stackAccountBody = `{"venue":"binance","displayName":"Personal","credentialBundle":{"schemaVersion":1,"venue":"binance","authKind":"api_key_secret","metadata":{},"secrets":{"apiKey":"offline-stack-key","apiSecret":"offline-stack-secret"}}}`

// This is a real HTTP/service/database integration. Only venue clients are fake;
// no SDK client is constructed and the manager transport refuses all network I/O.
func TestAccountStackRealPostgresLoginRestorePositionsAndDelete(t *testing.T) {
	gatewayPool := accountStackPool(t, "gateway")
	if err := gatewaypostgres.Migrate(t.Context(), gatewayPool); err != nil {
		t.Fatal(err)
	}
	authPool := accountStackPool(t, "auth")
	authDB := stdlib.OpenDB(*authPool.Config().ConnConfig)
	t.Cleanup(func() { _ = authDB.Close() })
	if err := edgepostgres.Migrate(t.Context(), authDB); err != nil {
		t.Fatal(err)
	}
	hash, err := session.HashPassword("offline-password")
	if err != nil {
		t.Fatal(err)
	}
	identities := edgepostgres.NewIdentityStore(authDB)
	if err := identities.Upsert(t.Context(), session.Identity{PrincipalID: session.DefaultPrincipalID, Username: "owner", PasswordHash: hash}); err != nil {
		t.Fatal(err)
	}
	sessions, err := session.New(session.Options{Username: "owner", PasswordHash: hash, TTL: time.Hour, Store: edgepostgres.NewSessionStore(authDB), Identities: identities})
	if err != nil {
		t.Fatal(err)
	}

	venue := &accountStackVenue{}
	stack := newAccountStack(t, gatewayPool, venue, sessions)
	if body := stack.request(t, "POST", "/api/v1/accounts", stackAccountBody, "add-one", false, 401); strings.Contains(body, "offline-stack-secret") {
		t.Fatal("secret reflected before login")
	}
	stack.login(t)
	stack.request(t, "POST", "/api/v1/accounts", stackAccountBody, "add-one", false, 403)
	if venue.probes.Load() != 0 {
		t.Fatal("unauthenticated/CSRF request reached venue")
	}

	venue.setError(exchange.NewError(exchange.KindPermission, exchange.ErrorDetails{Venue: exchange.VenueBinance, Code: "-2015", SafeMessage: "API key access denied"}))
	failed := stack.request(t, "POST", "/api/v1/accounts", stackAccountBody, "denied", true, 502)
	if !strings.Contains(failed, "-2015") {
		t.Fatal("venue error code lost through proxy")
	}
	if rows, err := stack.accounts.List(t.Context()); err != nil || len(rows) != 0 {
		t.Fatalf("failed registration persisted: %v", err)
	}
	venue.setError(nil)
	probesBefore := venue.probes.Load()
	body := stack.request(t, "POST", "/api/v1/accounts", stackAccountBody, "add-one", true, 201)
	var added struct {
		Data struct {
			Account struct {
				AccountID string `json:"accountId"`
			} `json:"account"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &added); err != nil || added.Data.Account.AccountID == "" {
		t.Fatalf("account response missing identity: %v", err)
	}
	id := added.Data.Account.AccountID
	stack.request(t, "POST", "/api/v1/accounts", stackAccountBody, "add-one", true, 201)
	if venue.probes.Load() != probesBefore+1 {
		t.Fatal("zero balance failed validation or replay performed another balance query")
	}

	venue.setPosition("0.25")
	assertStackPosition(t, stack.positions(t), id, "0.25")
	venue.setPosition("0.10")
	assertStackPosition(t, stack.positions(t), id, "0.1")
	venue.setError(errors.New("venue temporarily unavailable"))
	unavailable := stack.positions(t)
	if len(unavailable.Accounts) != 1 || unavailable.Accounts[0].Freshness != accountpositions.FreshnessUnavailable || unavailable.Summary.StaleAccountCount != 1 {
		t.Fatalf("unavailable venue reported as flat: %+v", unavailable)
	}
	venue.setError(nil)
	venue.setPosition("0")
	flat := stack.positions(t)
	if len(flat.Items) != 0 || len(flat.Accounts) != 1 || flat.Accounts[0].Freshness != accountpositions.FreshnessLive {
		t.Fatalf("venue flat state not reflected: %+v", flat)
	}
	venue.setPosition("0.40")

	// Recreate all Gateway components and database connections. Credentials and
	// the existing browser login are recovered from their separate SQL schemas.
	csrf := stack.csrf
	jar := stack.client.Jar
	stack.close()
	poolConfig := gatewayPool.Config()
	gatewayPool.Close()
	restoredPool, err := pgxpool.NewWithConfig(t.Context(), poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restoredPool.Close)
	if err := authDB.Close(); err != nil {
		t.Fatal(err)
	}
	authDB = stdlib.OpenDB(*authPool.Config().ConnConfig)
	t.Cleanup(func() { _ = authDB.Close() })
	sessions, err = session.New(session.Options{Username: "owner", PasswordHash: hash, TTL: time.Hour, Store: edgepostgres.NewSessionStore(authDB), Identities: edgepostgres.NewIdentityStore(authDB)})
	if err != nil {
		t.Fatal(err)
	}
	beforeRestore := venue.probes.Load()
	stack = newAccountStack(t, restoredPool, venue, sessions)
	stack.csrf = csrf
	stack.client.Jar = jar
	stack.request(t, "GET", "/api/v1/auth/session", "", "", false, 200)
	if failures := stack.accounts.Restore(t.Context()); len(failures) != 0 {
		t.Fatalf("restore failed: %v", failures)
	}
	stack.refreshCatalog(t)
	if venue.probes.Load() != beforeRestore {
		t.Fatal("restart introduced a new balance-validation gate")
	}
	assertStackPosition(t, stack.positions(t), id, "0.4")

	stack.request(t, "DELETE", "/api/v1/accounts/"+id, "", "", true, 204)
	empty := stack.positions(t)
	if len(empty.Accounts) != 0 || len(empty.Items) != 0 {
		t.Fatalf("deleted account remains visible: %+v", empty)
	}
	if venue.writes.Load() != 0 {
		t.Fatal("registration, position read or deletion issued a venue trade")
	}
	if _, err := stack.accounts.Get(t.Context(), domain.AccountID(id)); !errors.Is(err, account.ErrRegistrationNotFound) {
		t.Fatalf("deleted credential remains readable: %v", err)
	}
	if stack.manager.ActiveSnapshot(domain.AccountID(id)) != nil {
		t.Fatal("deleted account still routable")
	}
	stack.request(t, "DELETE", "/api/v1/auth/session", "", "", true, 204)
	stack.request(t, "GET", "/api/v1/accounts", "", "", false, 401)
}

type accountStack struct {
	manager                *account.Manager
	accounts               *account.Registrations
	registry               *publicconn.Registry
	edge, trading, gateway *httptest.Server
	client                 *http.Client
	csrf                   string
	closeOnce              sync.Once
	orders                 *order.Coordinator
	hub                    *gatewayevent.Hub
}

func newAccountStack(t *testing.T, pool *pgxpool.Pool, venue *accountStackVenue, sessions *session.Service) *accountStack {
	t.Helper()
	stack := &accountStack{}
	cat, err := catalog.NewService(time.Minute, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	stack.registry, err = publicconn.NewRegistry(&countingPublicFactory{}, routeprofile.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	stack.manager, err = account.NewManager(t.Context(), nil, account.Options{GatewayCatalog: cat, HTTPClient: &http.Client{Transport: denyNetworkTransport{}}, RuntimeGroupBuilder: venue, PublicConnectors: stack.registry, RequirePublicConnectors: true})
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := secretstore.NewCipher(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	stack.accounts, err = account.NewRegistrations(gatewaypostgres.NewRegistrationStore(pool, cipher), stack.manager)
	if err != nil {
		t.Fatal(err)
	}
	identity := domain.NewIdentityGenerator(domain.IdentityGeneratorOptions{})
	router, err := order.NewAccountRouter(stack.manager)
	if err != nil {
		t.Fatal(err)
	}
	store := gatewaypostgres.NewStore(pool)
	stack.orders, err = order.NewCoordinator(order.Options{Store: store, Router: router, Identity: identity})
	if err != nil {
		t.Fatal(err)
	}
	stack.hub, err = gatewayevent.NewHub(gatewayevent.Options{Identity: identity})
	if err != nil {
		t.Fatal(err)
	}
	positions, err := gatewayevent.NewPositionProjection(stack.hub, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := reference.NewService(stack.manager)
	if err != nil {
		t.Fatal(err)
	}
	public, err := gatewayhttp.NewHandler(gatewayhttp.Options{Monitor: health.NewMonitor(), Accounts: stack.manager, References: refs, Orders: stack.orders, Events: stack.hub, Positions: positions, Auditor: store, Identity: identity})
	if err != nil {
		t.Fatal(err)
	}
	ref, err := secret.ParseRef("env://OFFLINE_STACK_TOKEN")
	if err != nil {
		t.Fatal(err)
	}
	bearer, err := auth.NewBearerMiddleware(t.Context(), secret.StaticProvider{"env://OFFLINE_STACK_TOKEN": stackToken}, ref)
	if err != nil {
		t.Fatal(err)
	}
	internal, err := gatewayhttp.NewInternalHandler(gatewayhttp.InternalOptions{Accounts: stack.accounts, Authority: stackUnusedAuthority{}, Authenticate: func(r *http.Request) error {
		if _, ok := auth.Principal(r.Context()); !ok {
			return errors.New("service identity missing")
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	gatewayMux := http.NewServeMux()
	gatewayMux.Handle("/internal/", internal)
	gatewayMux.Handle("/", public)
	stack.gateway = httptest.NewServer(bearer.Wrap(gatewayMux))
	adapter, err := tradinggateway.NewClient(stack.gateway.URL, false, []byte(stackToken), false, stack.gateway.Client())
	if err != nil {
		t.Fatal(err)
	}
	currentPositions, err := accountpositions.NewService(adapter, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	tradingAuth, err := servicetoken.New([]byte(stackToken))
	if err != nil {
		t.Fatal(err)
	}
	activity, err := tradinghttp.NewActivityLog(32, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	tradingMux := http.NewServeMux()
	err = tradinghttp.RegisterBrowserProjectionRoutes(tradingMux, tradinghttp.BrowserProjectionRoutesOptions{Runtimes: adapter, Balances: adapter, Positions: adapter, PortfolioPositions: currentPositions, Commands: tradingorders.NewService(tradingorders.NewMemoryStore(), time.Now), Activity: activity, Principal: tradingAuth.Principal})
	if err != nil {
		t.Fatal(err)
	}
	stack.trading = httptest.NewServer(tradingAuth.Wrap(tradingMux))
	gatewayURL, _ := url.Parse(stack.gateway.URL)
	tradingURL, _ := url.Parse(stack.trading.URL)
	forwarder, err := proxy.New(proxy.Options{Gateway: proxy.Upstream{BaseURL: gatewayURL, Token: stackToken}, Trading: proxy.Upstream{BaseURL: tradingURL, Token: stackToken}, Sessions: sessions})
	if err != nil {
		t.Fatal(err)
	}
	edge, err := edgehttp.New(edgehttp.Options{Sessions: sessions, ContractVersion: edgehttp.ContractVersion, Downstream: forwarder.Register})
	if err != nil {
		t.Fatal(err)
	}
	stack.edge = httptest.NewServer(edge)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	stack.client = &http.Client{Jar: jar, Timeout: 5 * time.Second}
	t.Cleanup(stack.close)
	return stack
}
func (stack *accountStack) close() {
	stack.closeOnce.Do(func() {
		stack.edge.Close()
		stack.trading.Close()
		stack.gateway.Close()
		_ = stack.orders.Shutdown(context.Background())
		_ = stack.manager.Close()
		_ = stack.registry.Close()
		stack.hub.Close()
	})
}
func (stack *accountStack) refreshCatalog(t *testing.T) {
	t.Helper()
	for _, route := range account.RegistrationRoutes("binance") {
		if _, err := stack.registry.Refresh(t.Context(), routeprofile.Key{Venue: "binance", RouteProfileID: route.RouteProfileID}); err != nil {
			t.Fatal(err)
		}
	}
}
func (stack *accountStack) login(t *testing.T) {
	t.Helper()
	body := stack.request(t, "POST", "/api/v1/auth/login", `{"username":"owner","password":"offline-password"}`, "", false, 200)
	var result struct {
		Data session.Result `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		t.Fatal(err)
	}
	stack.csrf = result.Data.Session.CSRFToken
	if stack.csrf == "" {
		t.Fatal("login did not return CSRF token")
	}
}
func (stack *accountStack) request(t *testing.T, method, path, payload, key string, csrf bool, status int) string {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, stack.edge.URL+path, strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	if csrf {
		req.Header.Set("X-CSRF-Token", stack.csrf)
	}
	res, err := stack.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	for _, canary := range []string{"offline-stack-key", "offline-stack-secret", stackToken, "offline-password"} {
		if strings.Contains(string(body), canary) {
			t.Fatal("credential leaked in HTTP response")
		}
	}
	if res.StatusCode != status {
		t.Fatalf("%s %s: status=%d want=%d body=%s", method, path, res.StatusCode, status, body)
	}
	return string(body)
}
func (stack *accountStack) positions(t *testing.T) accountpositions.Snapshot {
	t.Helper()
	if len(stack.manager.AccountIDs()) > 0 {
		stack.refreshCatalog(t)
	}
	body := stack.request(t, "GET", "/api/v1/portfolio/positions", "", "", false, 200)
	var result struct {
		Data accountpositions.Snapshot `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		t.Fatal(err)
	}
	return result.Data
}
func assertStackPosition(t *testing.T, snapshot accountpositions.Snapshot, id, quantity string) {
	t.Helper()
	if len(snapshot.Items) != 1 || snapshot.Items[0].AccountID != id || snapshot.Items[0].Quantity != quantity || snapshot.Items[0].Freshness != accountpositions.FreshnessLive {
		t.Fatalf("current venue positions=%+v", snapshot)
	}
}

type stackUnusedAuthority struct{}

func (stackUnusedAuthority) Execute(context.Context, gatewayhttp.AuthorityRequest) (gatewayhttp.AuthorityResponse, error) {
	return gatewayhttp.AuthorityResponse{}, errors.New("unexpected internal authority request")
}

type accountStackVenue struct {
	mu             sync.Mutex
	quantity       string
	err            error
	probes, writes atomic.Int64
}

func (venue *accountStackVenue) balances(context.Context) ([]exchange.Balance, error) {
	venue.probes.Add(1)
	venue.mu.Lock()
	defer venue.mu.Unlock()
	return []exchange.Balance{}, venue.err
}
func (venue *accountStackVenue) setPosition(quantity string) {
	venue.mu.Lock()
	defer venue.mu.Unlock()
	venue.quantity = quantity
}
func (venue *accountStackVenue) setError(err error) {
	venue.mu.Lock()
	defer venue.mu.Unlock()
	venue.err = err
}
func (venue *accountStackVenue) BuildRoute(_ context.Context, _ account.ConnectionSpec, route account.RouteDefinition, _ *credential.Decoded) (account.ConnectionRoute, error) {
	return &stackConnectionRoute{venue: venue, product: route.Product}, nil
}

type stackConnectionRoute struct {
	venue   *accountStackVenue
	product string
}

func (route *stackConnectionRoute) Probe(ctx context.Context) error {
	_, err := route.venue.balances(ctx)
	return err
}
func (*stackConnectionRoute) Close() error { return nil }
func (route *stackConnectionRoute) SpotClient() (exchange.SpotClient, bool) {
	return &stackSpot{venue: route.venue}, route.product == "spot"
}
func (route *stackConnectionRoute) PerpClient() (exchange.PerpClient, bool) {
	return &stackPerp{venue: route.venue}, route.product == "perp"
}
func (route *stackConnectionRoute) OrderClient() (exchange.OrderREST, bool) {
	if route.product == "spot" {
		return &stackSpot{venue: route.venue}, true
	}
	return &stackPerp{venue: route.venue}, true
}
func (*stackConnectionRoute) ExactLookupReady() bool { return true }

type stackSpot struct {
	exchange.SpotClient
	venue *accountStackVenue
}

func (client *stackSpot) Balances(ctx context.Context) ([]exchange.Balance, error) {
	return client.venue.balances(ctx)
}
func (client *stackSpot) PlaceOrder(context.Context, exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error) {
	client.venue.writes.Add(1)
	return exchange.OrderAcknowledgement{}, errors.New("unexpected venue write")
}
func (client *stackSpot) CancelOrder(context.Context, exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error) {
	client.venue.writes.Add(1)
	return exchange.OrderAcknowledgement{}, errors.New("unexpected venue write")
}

type stackPerp struct {
	exchange.PerpClient
	venue *accountStackVenue
}

func (client *stackPerp) Positions(context.Context, exchange.PositionsRequest) ([]exchange.Position, error) {
	client.venue.mu.Lock()
	defer client.venue.mu.Unlock()
	if client.venue.err != nil {
		return nil, client.venue.err
	}
	if client.venue.quantity == "" || client.venue.quantity == "0" {
		return []exchange.Position{}, nil
	}
	return []exchange.Position{{NativeSymbol: "BTCUSDT", NativeID: "BTCUSDT", PositionSlot: "net", Side: exchange.SideBuy, Quantity: decimal.RequireFromString(client.venue.quantity), EntryPrice: decimal.RequireFromString("50000"), MarkPrice: decimal.RequireFromString("50100"), UnrealizedPnL: decimal.RequireFromString("25")}}, nil
}
func (client *stackPerp) PlaceOrder(context.Context, exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error) {
	client.venue.writes.Add(1)
	return exchange.OrderAcknowledgement{}, errors.New("unexpected venue write")
}
func (client *stackPerp) CancelOrder(context.Context, exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error) {
	client.venue.writes.Add(1)
	return exchange.OrderAcknowledgement{}, errors.New("unexpected venue write")
}

func accountStackPool(t *testing.T, suffix string) *pgxpool.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("account stack requires explicit isolated PostgreSQL")
	}
	dsn := os.Getenv("GATEWAY_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("GATEWAY_TEST_POSTGRES_DSN is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid test PostgreSQL configuration")
	}
	admin, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("account_stack_%s_%d", suffix, time.Now().UnixNano())
	if _, err = admin.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = admin.Exec(cleanup, `DROP SCHEMA `+schema+` CASCADE`)
		admin.Close()
	})
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}
