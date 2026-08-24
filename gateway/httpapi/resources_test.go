package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/account"
	"github.com/QuantProcessing/boltertrader/gateway/auth"
	"github.com/QuantProcessing/boltertrader/gateway/capability"
	"github.com/QuantProcessing/boltertrader/gateway/catalog"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	gatewayevent "github.com/QuantProcessing/boltertrader/gateway/event"
	"github.com/QuantProcessing/boltertrader/gateway/health"
	"github.com/QuantProcessing/boltertrader/gateway/order"
	"github.com/QuantProcessing/boltertrader/gateway/reference"
	"github.com/QuantProcessing/boltertrader/gateway/secret"
	"github.com/shopspring/decimal"
)

func TestHandlerPositionSnapshotReturnsCompleteProjectionAndCursor(t *testing.T) {
	instrumentID := domain.InstrumentID("binance:live:perp:USDT:BTC-USDT")
	resource := account.Resource{
		AccountID: "acct-p", Venue: exchange.VenueBinance, Product: exchange.ProductPerp,
		Environment: exchange.EnvironmentLive, Settlement: "USDT", ReadReady: true, WriteReady: true,
	}
	accounts := &fakeHTTPAccounts{
		resource: resource, status: account.Status{Resource: resource},
		instrument: exchange.Instrument{Identity: exchange.InstrumentIdentity{
			Venue: exchange.VenueBinance, Product: exchange.ProductPerp, Symbol: "BTC-USDT",
			BaseAsset: "BTC", QuoteAsset: "USDT", SettleAsset: "USDT", NativeSymbol: "BTCUSDT", NativeID: "BTCUSDT",
		}},
		public: catalog.Instrument{
			ID: instrumentID, Venue: exchange.VenueBinance, Environment: exchange.EnvironmentLive,
			Product: exchange.ProductPerp, Settlement: "USDT", Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT",
		},
		perp: &fakeHTTPPerpClient{positions: []exchange.Position{{
			NativeSymbol: "BTCUSDT", NativeID: "BTCUSDT", Side: exchange.SideBuy,
			Quantity: decimal.RequireFromString("0.1"),
		}}},
		capability: capability.StaticMatrix().Rows[1],
	}
	hub := newTestEventHub(t)
	projection, err := gatewayevent.NewPositionProjection(hub, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	monitor := health.NewMonitor()
	monitor.SetPostgresHealthy(true)
	handler, err := NewHandler(Options{
		Monitor: monitor, Accounts: accounts, References: &fakeHTTPReferences{accounts: accounts},
		Orders: &fakeHTTPOrders{}, Events: hub, Positions: projection, Auditor: &fakeHTTPAuditor{},
		Identity: domain.NewIdentityGenerator(domain.IdentityGeneratorOptions{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	ref, _ := secret.ParseRef("env://TOKEN")
	middleware, err := auth.NewBearerMiddleware(context.Background(), secret.StaticProvider{ref.RawForProvider(): "test-token"}, ref)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/position-snapshot?account_id=acct-p", nil)
	request.Header.Set("Authorization", "Bearer test-token")
	response := httptest.NewRecorder()
	middleware.Wrap(handler).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var envelope struct {
		Data gatewayevent.PositionSnapshot `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Data.Items) != 1 || envelope.Data.Items[0].InstrumentID != instrumentID || envelope.Data.Cursor == "" {
		t.Fatalf("snapshot=%+v", envelope.Data)
	}
}

func TestHandlerPerpPrivateResourcesUseResolvedInstrumentAndCapabilityGates(t *testing.T) {
	instrumentID := domain.InstrumentID("binance:live:perp:USDT:BTC-USDT")
	instrument := exchange.Instrument{
		Identity: exchange.InstrumentIdentity{
			Venue: exchange.VenueBinance, Product: exchange.ProductPerp,
			Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT",
			SettleAsset: "USDT", NativeSymbol: "BTCUSDT", NativeID: "BTCUSDT",
		},
	}
	public := catalog.Instrument{
		ID: instrumentID, Venue: exchange.VenueBinance, Environment: exchange.EnvironmentLive,
		Product: exchange.ProductPerp, Settlement: "USDT", Symbol: "BTC-USDT",
		BaseAsset: "BTC", QuoteAsset: "USDT",
	}
	perp := &fakeHTTPPerpClient{positions: []exchange.Position{{
		NativeSymbol: "BTCUSDT", NativeID: "BTCUSDT", Quantity: decimal.NewFromInt(1),
	}}}
	accounts := &fakeHTTPAccounts{
		resource: account.Resource{
			AccountID: "acct-p", Venue: exchange.VenueBinance, Product: exchange.ProductPerp,
			Environment: exchange.EnvironmentLive, Settlement: "USDT", ReadReady: true, WriteReady: true,
		},
		status: account.Status{Resource: account.Resource{
			AccountID: "acct-p", Venue: exchange.VenueBinance, Product: exchange.ProductPerp,
			Environment: exchange.EnvironmentLive, Settlement: "USDT", ReadReady: true, WriteReady: true,
		}},
		instrument: instrument, public: public, capability: capability.StaticMatrix().Rows[1], perp: perp,
	}
	references := &fakeHTTPReferences{accounts: accounts}
	monitor := health.NewMonitor()
	monitor.SetPostgresHealthy(true)
	handler, err := NewHandler(Options{
		Monitor: monitor, Accounts: accounts, References: references, Orders: &fakeHTTPOrders{},
		Events:  newTestEventHub(t),
		Auditor: &fakeHTTPAuditor{}, Identity: domain.NewIdentityGenerator(domain.IdentityGeneratorOptions{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	ref, _ := secret.ParseRef("env://TOKEN")
	middleware, _ := auth.NewBearerMiddleware(context.Background(), secret.StaticProvider{ref.RawForProvider(): "test-token"}, ref)
	authenticated := middleware.Wrap(handler)

	tests := []struct {
		method string
		path   string
		body   string
		status int
	}{
		{http.MethodGet, "/v1/fills?account_id=acct-p&instrument_id=" + string(instrumentID), "", http.StatusOK},
		{http.MethodGet, "/v1/balances?account_id=acct-p", "", http.StatusOK},
		{http.MethodGet, "/v1/positions?account_id=acct-p&instrument_id=" + string(instrumentID), "", http.StatusOK},
		{http.MethodGet, "/v1/funding-rates?account_id=acct-p&instrument_id=" + string(instrumentID), "", http.StatusOK},
		{http.MethodPut, "/v1/accounts/acct-p/instruments/" + string(instrumentID) + "/leverage", `{"leverage":5}`, http.StatusNotImplemented},
	}
	for _, test := range tests {
		request := httptest.NewRequest(test.method, test.path, bytes.NewBufferString(test.body))
		request.Header.Set("Authorization", "Bearer test-token")
		if test.body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		recorder := httptest.NewRecorder()
		authenticated.ServeHTTP(recorder, request)
		if recorder.Code != test.status {
			t.Errorf("%s %s status=%d body=%s", test.method, test.path, recorder.Code, recorder.Body.String())
		}
	}
	if perp.fillsCalls != 1 || perp.balanceCalls != 1 || perp.positionCalls != 1 ||
		perp.fundingCalls != 1 || perp.leverageCalls != 0 {
		t.Fatalf("perp method calls=%+v", perp)
	}
	if perp.lastInstrument.Identity.NativeSymbol != "BTCUSDT" {
		t.Fatalf("resolved instrument=%+v", perp.lastInstrument.Identity)
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/positions?account_id=acct-p", nil)
	request.Header.Set("Authorization", "Bearer test-token")
	recorder := httptest.NewRecorder()
	authenticated.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("unscoped positions status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var positions collection[positionResource]
	if err := json.NewDecoder(recorder.Body).Decode(&positions); err != nil {
		t.Fatalf("decode unscoped positions: %v", err)
	}
	if len(positions.Items) != 1 || positions.Items[0].InstrumentID != instrumentID {
		t.Fatalf("unscoped positions=%+v, want mapped instrument %s", positions.Items, instrumentID)
	}
}

// TestHandlerServesEveryRouteOfAMultiRouteAccount pins the shape bybit takes:
// one set of credentials settling perpetuals in both USDT and USDC, which the
// account manager reports as two resources sharing a single account id. Reading
// such an account by id used to be impossible — every lookup stopped at the
// first route it found and then rejected the request as ambiguous, so balances,
// positions, fills and order books all answered 400 no matter which of the two
// routes was meant.
func TestHandlerServesEveryRouteOfAMultiRouteAccount(t *testing.T) {
	const accountID = domain.AccountID("acct-multi")
	usdtInstrumentID := domain.InstrumentID("bybit:live:perp:USDT:SIREN-USDT")
	usdcInstrumentID := domain.InstrumentID("bybit:live:perp:USDC:SIREN-USDC")
	route := func(settlement string) account.Resource {
		return account.Resource{
			AccountID: accountID, Venue: exchange.VenueBybit, Product: exchange.ProductPerp,
			Environment: exchange.EnvironmentLive, Settlement: settlement, ReadReady: true, WriteReady: true,
		}
	}
	resolve := func(settlement, symbol, native string) exchange.Instrument {
		return exchange.Instrument{Identity: exchange.InstrumentIdentity{
			Venue: exchange.VenueBybit, Product: exchange.ProductPerp, Symbol: symbol,
			BaseAsset: "SIREN", QuoteAsset: settlement, SettleAsset: settlement,
			NativeSymbol: native, NativeID: native,
		}}
	}
	list := func(id domain.InstrumentID, settlement, symbol string) catalog.Instrument {
		return catalog.Instrument{
			ID: id, Venue: exchange.VenueBybit, Environment: exchange.EnvironmentLive,
			Product: exchange.ProductPerp, Settlement: settlement, Symbol: symbol,
			BaseAsset: "SIREN", QuoteAsset: settlement,
		}
	}
	const venueOrderID = "venue-order-1"
	usdt := &fakeHTTPPerpClient{
		venueOrderID: venueOrderID,
		positions:    []exchange.Position{{NativeSymbol: "SIRENUSDT", NativeID: "SIRENUSDT"}},
	}
	usdc := &fakeHTTPPerpClient{
		venueOrderID: venueOrderID,
		positions:    []exchange.Position{{NativeSymbol: "SIRENPERP", NativeID: "SIRENPERP"}},
	}
	accounts := &fakeHTTPAccounts{
		resource: route("USDT"),
		status:   account.Status{Resource: route("USDT")},
		routes:   []account.Resource{route("USDT"), route("USDC")},
		// No fallback client: a lookup that loses the settlement now fails
		// loudly instead of quietly answering from whichever route was first.
		routePerps: map[string]exchange.PerpClient{"USDT": usdt, "USDC": usdc},
		instruments: []catalog.Instrument{
			list(usdtInstrumentID, "USDT", "SIREN-USDT"),
			list(usdcInstrumentID, "USDC", "SIREN-USDC"),
		},
		resolved: map[domain.InstrumentID]exchange.Instrument{
			usdtInstrumentID: resolve("USDT", "SIREN-USDT", "SIRENUSDT"),
			usdcInstrumentID: resolve("USDC", "SIREN-USDC", "SIRENPERP"),
		},
		capability: capability.StaticMatrix().Rows[1],
	}
	orders := &fakeHTTPOrders{getResult: order.Result{
		CommandID: "018f0000-0000-7000-8000-000000000001", GatewayOrderID: "018f0000-0000-7000-8000-000000000002",
		AccountID: accountID, InstrumentID: usdtInstrumentID, VenueOrderID: venueOrderID,
		State: domain.StateFilled, ResourceVersion: 3,
	}}
	monitor := health.NewMonitor()
	monitor.SetPostgresHealthy(true)
	handler, err := NewHandler(Options{
		Monitor: monitor, Accounts: accounts, References: &fakeHTTPReferences{accounts: accounts}, Orders: orders,
		Events:  newTestEventHub(t),
		Auditor: &fakeHTTPAuditor{}, Identity: domain.NewIdentityGenerator(domain.IdentityGeneratorOptions{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	ref, _ := secret.ParseRef("env://TOKEN")
	middleware, _ := auth.NewBearerMiddleware(context.Background(), secret.StaticProvider{ref.RawForProvider(): "test-token"}, ref)
	authenticated := middleware.Wrap(handler)
	get := func(t *testing.T, path string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer test-token")
		recorder := httptest.NewRecorder()
		authenticated.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET %s status=%d body=%s", path, recorder.Code, recorder.Body.String())
		}
		return recorder
	}

	balances := decodeCollection[balanceResource](t, get(t, "/v1/balances?account_id=acct-multi"))
	if len(balances.Items) != 2 || usdt.balanceCalls != 1 || usdc.balanceCalls != 1 {
		t.Fatalf("balances=%d usdt=%d usdc=%d, want one from each route", len(balances.Items), usdt.balanceCalls, usdc.balanceCalls)
	}
	if balances.Items[0].AvailableMargin != "80" || balances.Items[1].AvailableMargin != "80" {
		t.Fatalf("available margins=%q/%q, want normalized account available", balances.Items[0].AvailableMargin, balances.Items[1].AvailableMargin)
	}

	positions := decodeCollection[positionResource](t, get(t, "/v1/positions?account_id=acct-multi"))
	if usdt.positionCalls != 1 || usdc.positionCalls != 1 {
		t.Fatalf("position calls usdt=%d usdc=%d, want one from each route", usdt.positionCalls, usdc.positionCalls)
	}
	seen := map[domain.InstrumentID]bool{}
	for _, item := range positions.Items {
		seen[item.InstrumentID] = true
	}
	if !seen[usdtInstrumentID] || !seen[usdcInstrumentID] {
		t.Fatalf("positions=%+v, want both settlements mapped back to their instruments", positions.Items)
	}

	// The by-order path is the one the reconciler walks: it knows the gateway
	// order id and nothing else, and must recover the route from the order's
	// own instrument id rather than from the account.
	fills := decodeCollection[fillResource](t, get(t, "/v1/fills?gateway_order_id=018f0000-0000-7000-8000-000000000002"))
	if usdt.fillsCalls != 1 || usdc.fillsCalls != 0 {
		t.Fatalf("fills calls usdt=%d usdc=%d, want only the order's own route", usdt.fillsCalls, usdc.fillsCalls)
	}
	if len(fills.Items) != 1 || fills.Items[0].InstrumentID != usdtInstrumentID || fills.Items[0].SettlementAsset != "USDT" {
		t.Fatalf("fills=%+v, want one USDT-settled fill", fills.Items)
	}

	// An instrument filter names one of the two books. Asking the other about
	// a symbol it does not list is not a narrower question but a wrong one, so
	// the USDC route must not be consulted at all.
	scoped := decodeCollection[fillResource](t, get(t, "/v1/fills?account_id=acct-multi&instrument_id="+string(usdtInstrumentID)))
	if usdt.fillsCalls != 2 || usdc.fillsCalls != 0 {
		t.Fatalf("scoped fills calls usdt=%d usdc=%d, want only the named instrument's route", usdt.fillsCalls, usdc.fillsCalls)
	}
	if len(scoped.Items) != 1 || scoped.Items[0].InstrumentID != usdtInstrumentID {
		t.Fatalf("scoped fills=%+v, want the USDT instrument", scoped.Items)
	}

	get(t, "/v1/funding-rates?account_id=acct-multi&instrument_id="+string(usdcInstrumentID))
	if usdc.fundingCalls != 1 || usdt.fundingCalls != 0 {
		t.Fatalf("funding calls usdt=%d usdc=%d, want only the named instrument's route", usdt.fundingCalls, usdc.fundingCalls)
	}

	// Public market data resolves its route the same way, from the instrument
	// rather than from the account, so a two-route account can be charted.
	get(t, "/v1/order-book?account_id=acct-multi&instrument_id="+string(usdcInstrumentID))
	if usdc.orderBookCalls != 1 || usdt.orderBookCalls != 0 {
		t.Fatalf("order book calls usdt=%d usdc=%d, want only the named instrument's route", usdt.orderBookCalls, usdc.orderBookCalls)
	}
}

func decodeCollection[T any](t *testing.T, recorder *httptest.ResponseRecorder) collection[T] {
	t.Helper()
	var decoded collection[T]
	if err := json.NewDecoder(recorder.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode %T: %v", decoded, err)
	}
	return decoded
}

func TestHandlerRejectsPerpOnlyResourcesForSpotBeforeClientIO(t *testing.T) {
	fixture := newHTTPFixture(t)
	for _, path := range []string{
		"/v1/positions?account_id=acct-a",
		"/v1/funding-rates?account_id=acct-a&instrument_id=binance:live:spot:BTC-USDT",
	} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer test-token")
		recorder := httptest.NewRecorder()
		fixture.handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadRequest && recorder.Code != http.StatusNotImplemented {
			t.Errorf("%s status=%d body=%s", path, recorder.Code, recorder.Body.String())
		}
	}
}

type fakeHTTPPerpClient struct {
	exchange.PerpClient
	fillsCalls     int
	balanceCalls   int
	positionCalls  int
	fundingCalls   int
	leverageCalls  int
	orderBookCalls int
	leverageErr    error
	lastInstrument exchange.Instrument
	positions      []exchange.Position
	// venueOrderID is the order this client's fills belong to. listFills drops
	// fills whose order id does not match the order it was asked about, so a
	// fixture exercising the by-order path has to name one; the empty default
	// leaves the account-scoped path unchanged.
	venueOrderID string
}

func (client *fakeHTTPPerpClient) Fills(_ context.Context, request exchange.FillsRequest) (exchange.FillPage, error) {
	client.fillsCalls++
	client.lastInstrument = *request.Instrument
	return exchange.FillPage{Fills: []exchange.Fill{{
		FillID: "fill-1", OrderID: client.venueOrderID,
		NativeSymbol: request.Instrument.Identity.NativeSymbol,
	}}}, nil
}

func (client *fakeHTTPPerpClient) OrderBook(_ context.Context, request exchange.OrderBookRequest) (exchange.OrderBook, error) {
	client.orderBookCalls++
	client.lastInstrument = request.Instrument
	return exchange.OrderBook{Instrument: request.Instrument}, nil
}

func (client *fakeHTTPPerpClient) Balances(context.Context) ([]exchange.Balance, error) {
	client.balanceCalls++
	return []exchange.Balance{{Asset: "USDT", Total: decimal.NewFromInt(100)}}, nil
}

func (client *fakeHTTPPerpClient) PerpAccount(context.Context) (exchange.PerpAccount, error) {
	client.balanceCalls++
	return exchange.PerpAccount{
		Balances: []exchange.Balance{
			{Asset: "OLD-ZERO-ASSET"},
			{Asset: "USDT", Total: decimal.NewFromInt(100)},
		},
		Available: exchange.OptionalDecimal{Value: decimal.NewFromInt(80), Valid: true},
	}, nil
}

func (client *fakeHTTPPerpClient) Positions(_ context.Context, request exchange.PositionsRequest) ([]exchange.Position, error) {
	client.positionCalls++
	if request.Instrument == nil {
		return append([]exchange.Position(nil), client.positions...), nil
	}
	client.lastInstrument = *request.Instrument
	return []exchange.Position{{NativeSymbol: request.Instrument.Identity.NativeSymbol}}, nil
}

func (client *fakeHTTPPerpClient) FundingRate(_ context.Context, request exchange.FundingRateRequest) (exchange.FundingRate, error) {
	client.fundingCalls++
	client.lastInstrument = request.Instrument
	return exchange.FundingRate{NativeSymbol: request.Instrument.Identity.NativeSymbol}, nil
}

func (client *fakeHTTPPerpClient) SetLeverage(_ context.Context, request exchange.SetLeverageRequest) (exchange.Leverage, error) {
	client.leverageCalls++
	client.lastInstrument = request.Instrument
	if client.leverageErr != nil {
		return exchange.Leverage{}, client.leverageErr
	}
	return exchange.Leverage{NativeSymbol: request.Instrument.Identity.NativeSymbol, Effective: request.Leverage}, nil
}

var _ reference.Registry = (*fakeHTTPAccounts)(nil)
