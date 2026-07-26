package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
)

func TestHandlerAuthenticatesBeforeDisclosureAndPlacesStrictDecimalOrder(t *testing.T) {
	fixture := newHTTPFixture(t)
	body := `{"account_id":"acct-a","instrument_id":"binance:demo:spot:BTC-USDT","idempotency_key":"idem-1","side":"buy","type":"limit","quantity":"0.001","limit_price":"100","limit_policy":"resting"}`

	unauthorized := httptest.NewRecorder()
	fixture.handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, "/v1/orders", bytes.NewBufferString(body)))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d body=%s", unauthorized.Code, unauthorized.Body.String())
	}
	var unauthorizedEnvelope errorEnvelope
	if err := json.Unmarshal(unauthorized.Body.Bytes(), &unauthorizedEnvelope); err != nil {
		t.Fatal(err)
	}
	if unauthorizedEnvelope.Error.Code != "unauthorized" || unauthorizedEnvelope.Error.CorrelationID == "" {
		t.Fatalf("unauthorized envelope=%+v", unauthorizedEnvelope)
	}

	request := httptest.NewRequest(http.MethodPost, "/v1/orders", bytes.NewBufferString(body))
	request.Header.Set("Authorization", "Bearer test-token")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "idem-1")
	recorder := httptest.NewRecorder()
	fixture.handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("place status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if fixture.orders.place.PrincipalID == "" || fixture.orders.place.AccountID != "acct-a" ||
		fixture.orders.place.Quantity.String() != "0.001" {
		t.Fatalf("place request=%+v", fixture.orders.place)
	}
}

func TestHandlerRejectsMalformedBodiesAndUnsupportedOrForbiddenSurfacesBeforeBackendIO(t *testing.T) {
	fixture := newHTTPFixture(t)
	tests := []struct {
		name   string
		method string
		path   string
		body   string
		status int
	}{
		{"duplicate key", http.MethodPost, "/v1/orders", `{"account_id":"acct-a","account_id":"acct-b"}`, http.StatusBadRequest},
		{"unknown field", http.MethodPost, "/v1/orders", `{"unknown":true}`, http.StatusBadRequest},
		{"amend unsupported", http.MethodPatch, "/v1/orders/018f0000-0000-7000-8000-000000000002", `{}`, http.StatusNotImplemented},
		{"order book absent", http.MethodGet, "/v1/order-book", "", http.StatusNotFound},
		{"transfer absent", http.MethodPost, "/v1/transfers", `{}`, http.StatusNotFound},
		{"credentials absent", http.MethodPost, "/v1/accounts/acct-a/credentials", `{}`, http.StatusNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, bytes.NewBufferString(test.body))
			request.Header.Set("Authorization", "Bearer test-token")
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Idempotency-Key", "idem-1")
			recorder := httptest.NewRecorder()
			fixture.handler.ServeHTTP(recorder, request)
			if recorder.Code != test.status {
				t.Fatalf("status=%d want=%d body=%s", recorder.Code, test.status, recorder.Body.String())
			}
		})
	}
	if fixture.orders.placeCalls != 0 || fixture.orders.cancelCalls != 0 {
		t.Fatalf("invalid/unsupported requests reached order backend: %+v", fixture.orders)
	}
}

func TestHandlerRegistersReferenceOrderAndPrivateAccountRESTInventory(t *testing.T) {
	fixture := newHTTPFixture(t)
	tests := []struct {
		method string
		path   string
		status int
	}{
		{http.MethodGet, "/v1/health/live", 200},
		{http.MethodGet, "/v1/health/ready", 200},
		{http.MethodGet, "/v1/accounts", 200},
		{http.MethodGet, "/v1/accounts/acct-a", 200},
		{http.MethodGet, "/v1/capabilities", 200},
		{http.MethodGet, "/v1/instruments?account_id=acct-a&pagination.limit=10", 200},
		{http.MethodGet, "/v1/instruments/binance:demo:spot:BTC-USDT", 200},
		{http.MethodGet, "/v1/rules?account_id=acct-a", 200},
		{http.MethodGet, "/v1/risk-limits", 200},
		{http.MethodGet, "/v1/orders?pagination.limit=10", 200},
		{http.MethodGet, "/v1/open-orders?pagination.limit=10", 200},
		{http.MethodGet, "/v1/fills?account_id=acct-a&instrument_id=binance:demo:spot:BTC-USDT&pagination.limit=10", 200},
		{http.MethodGet, "/v1/balances?account_id=acct-a", 200},
	}
	for _, test := range tests {
		request := httptest.NewRequest(test.method, test.path, nil)
		request.Header.Set("Authorization", "Bearer test-token")
		recorder := httptest.NewRecorder()
		fixture.handler.ServeHTTP(recorder, request)
		if recorder.Code != test.status {
			t.Errorf("%s %s status=%d body=%s", test.method, test.path, recorder.Code, recorder.Body.String())
		}
	}
}

func TestHandlerRejectsHostileJSONAndPaginationWithoutBackendIO(t *testing.T) {
	fixture := newHTTPFixture(t)
	valid := `{"account_id":"acct-a","instrument_id":"binance:demo:spot:BTC-USDT","idempotency_key":"idem-1","side":"buy","type":"market","quantity":"0.001"}`
	tests := []struct {
		name        string
		contentType string
		body        []byte
	}{
		{name: "missing content type", body: []byte(valid)},
		{name: "wrong content type", contentType: "text/plain", body: []byte(valid)},
		{name: "duplicate nested key", contentType: "application/json", body: []byte(`{"account_id":"acct-a","instrument_id":"binance:demo:spot:BTC-USDT","idempotency_key":"idem-1","side":"buy","type":"market","quantity":"0.001","extra":{"x":1,"x":2}}`)},
		{name: "trailing value", contentType: "application/json", body: []byte(valid + `{}`)},
		{name: "invalid unicode", contentType: "application/json", body: append([]byte(`{"account_id":"`), 0xff, '"', '}')},
		{name: "oversized body", contentType: "application/json", body: []byte(`{"account_id":"` + strings.Repeat("a", int(defaultBodyLimit)) + `"}`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/v1/orders", bytes.NewReader(test.body))
			request.Header.Set("Authorization", "Bearer test-token")
			request.Header.Set("Idempotency-Key", "idem-1")
			if test.contentType != "" {
				request.Header.Set("Content-Type", test.contentType)
			}
			recorder := httptest.NewRecorder()
			fixture.handler.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/orders?pagination.limit=501", nil)
	request.Header.Set("Authorization", "Bearer test-token")
	recorder := httptest.NewRecorder()
	fixture.handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("pagination status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if fixture.orders.placeCalls != 0 || fixture.orders.listCalls != 0 {
		t.Fatalf("hostile request reached backend: %+v", fixture.orders)
	}
}

func TestHandlerErrorEnvelopeNeverReflectsNativeErrorMaterial(t *testing.T) {
	fixture := newHTTPFixture(t)
	const canary = "venue-message-secret-canary"
	fixture.orders.placeErr = exchange.NewError(exchange.KindVenueRejected, exchange.ErrorDetails{
		Operation: "PlaceOrder", Code: "51000", SafeMessage: canary,
	})
	body := `{"account_id":"acct-a","instrument_id":"binance:demo:spot:BTC-USDT","idempotency_key":"idem-1","side":"buy","type":"market","quantity":"0.001"}`
	request := httptest.NewRequest(http.MethodPost, "/v1/orders", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer test-token")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "idem-1")
	recorder := httptest.NewRecorder()
	fixture.handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), canary) || strings.Contains(recorder.Body.String(), "51000") {
		t.Fatalf("error reflected native material: %s", recorder.Body.String())
	}
	var envelope errorEnvelope
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != "venue_rejected" || envelope.Error.CorrelationID == "" {
		t.Fatalf("error envelope=%+v", envelope)
	}
	if envelope.Error.CorrelationID != string(fixture.orders.place.CorrelationID) {
		t.Fatalf("response correlation=%q place correlation=%q",
			envelope.Error.CorrelationID, fixture.orders.place.CorrelationID)
	}
}

func TestHandlerAuthenticatesBeforeMissingResourceDisclosure(t *testing.T) {
	fixture := newHTTPFixture(t)
	request := httptest.NewRequest(http.MethodGet, "/v1/accounts/does-not-exist", nil)
	recorder := httptest.NewRecorder()
	fixture.handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestHandlerUsesStableErrorEnvelopeForUnknownRouteAndMethod(t *testing.T) {
	fixture := newHTTPFixture(t)
	tests := []struct {
		method string
		path   string
		status int
		code   string
	}{
		{http.MethodGet, "/v1/unknown", http.StatusNotFound, "not_found"},
		{http.MethodPost, "/v1/accounts", http.StatusMethodNotAllowed, "method_not_allowed"},
	}
	for _, test := range tests {
		request := httptest.NewRequest(test.method, test.path, nil)
		request.Header.Set("Authorization", "Bearer test-token")
		recorder := httptest.NewRecorder()
		fixture.handler.ServeHTTP(recorder, request)
		if recorder.Code != test.status {
			t.Errorf("%s %s status=%d body=%s", test.method, test.path, recorder.Code, recorder.Body.String())
			continue
		}
		if recorder.Header().Get("Content-Type") != "application/json" {
			t.Errorf("%s %s content type=%q", test.method, test.path, recorder.Header().Get("Content-Type"))
		}
		var envelope errorEnvelope
		if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
			t.Errorf("%s %s body=%q: %v", test.method, test.path, recorder.Body.String(), err)
			continue
		}
		if envelope.Error.Code != test.code || envelope.Error.CorrelationID == "" {
			t.Errorf("%s %s envelope=%+v", test.method, test.path, envelope)
		}
	}
}

func TestHandlerIdempotencyConflictReturnsExistingCommandAndRequestCorrelation(t *testing.T) {
	fixture := newHTTPFixture(t)
	fixture.orders.placeResult = order.Result{
		CommandID:      "018f0000-0000-7000-8000-000000000001",
		GatewayOrderID: "018f0000-0000-7000-8000-000000000002",
		ClientOrderID:  "1", State: domain.StatePrepared,
	}
	fixture.orders.placeErr = order.ErrIdempotencyConflict
	body := `{"account_id":"acct-a","instrument_id":"binance:demo:spot:BTC-USDT","idempotency_key":"idem-conflict","side":"buy","type":"market","quantity":"0.001"}`
	request := httptest.NewRequest(http.MethodPost, "/v1/orders", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer test-token")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "idem-conflict")
	recorder := httptest.NewRecorder()

	fixture.handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Error             errorBody        `json:"error"`
		ExistingCommandID domain.CommandID `json:"existing_command_id"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.ExistingCommandID != fixture.orders.placeResult.CommandID ||
		response.Error.Code != "idempotency_conflict" ||
		response.Error.CorrelationID != string(fixture.orders.place.CorrelationID) {
		t.Fatalf("conflict response=%+v place=%+v", response, fixture.orders.place)
	}
}

type httpFixture struct {
	handler  http.Handler
	accounts *fakeHTTPAccounts
	orders   *fakeHTTPOrders
}

func newHTTPFixture(t *testing.T) httpFixture {
	t.Helper()
	instrument := exchange.Instrument{
		Identity: exchange.InstrumentIdentity{
			Venue: exchange.VenueBinance, Product: exchange.ProductSpot, Symbol: "BTC-USDT",
			BaseAsset: "BTC", QuoteAsset: "USDT", NativeSymbol: "BTCUSDT", NativeID: "BTCUSDT",
		},
	}
	public := catalog.Instrument{
		ID: "binance:demo:spot:BTC-USDT", Venue: exchange.VenueBinance,
		Environment: exchange.EnvironmentDemo, Product: exchange.ProductSpot,
		Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT",
	}
	accounts := &fakeHTTPAccounts{
		resource:   account.Resource{AccountID: "acct-a", Venue: exchange.VenueBinance, Product: exchange.ProductSpot, Environment: exchange.EnvironmentDemo, ReadReady: true, WriteReady: true},
		status:     account.Status{Resource: account.Resource{AccountID: "acct-a", Venue: exchange.VenueBinance, Product: exchange.ProductSpot, Environment: exchange.EnvironmentDemo, ReadReady: true, WriteReady: true}},
		instrument: instrument, public: public,
	}
	references := &fakeHTTPReferences{accounts: accounts}
	orders := &fakeHTTPOrders{}
	monitor := health.NewMonitor()
	monitor.SetPostgresHealthy(true)
	handler, err := NewHandler(Options{
		Monitor: monitor, Accounts: accounts, References: references, Orders: orders,
		Events:  newTestEventHub(t),
		Auditor: &fakeHTTPAuditor{}, Identity: domain.NewIdentityGenerator(domain.IdentityGeneratorOptions{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	ref, _ := secret.ParseRef("env://TOKEN")
	middleware, _ := auth.NewBearerMiddleware(context.Background(), secret.StaticProvider{ref.RawForProvider(): "test-token"}, ref)
	return httpFixture{handler: middleware.Wrap(handler), accounts: accounts, orders: orders}
}

func newTestEventHub(t *testing.T) *gatewayevent.Hub {
	t.Helper()
	hub, err := gatewayevent.NewHub(gatewayevent.Options{
		Identity: domain.NewIdentityGenerator(domain.IdentityGeneratorOptions{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(hub.Close)
	return hub
}

type fakeHTTPOrders struct {
	place       order.PlaceRequest
	placeCalls  int
	placeErr    error
	placeResult order.Result
	listCalls   int
	cancelCalls int
}

func (orders *fakeHTTPOrders) Place(_ context.Context, request order.PlaceRequest) (order.Result, error) {
	orders.place, orders.placeCalls = request, orders.placeCalls+1
	if orders.placeErr != nil {
		return orders.placeResult, orders.placeErr
	}
	return order.Result{
		CommandID:      "018f0000-0000-7000-8000-000000000001",
		GatewayOrderID: "018f0000-0000-7000-8000-000000000002",
		ClientOrderID:  "1", State: domain.StateOpen, ResourceVersion: 2,
	}, nil
}

func (*fakeHTTPOrders) Get(context.Context, domain.PrincipalID, domain.GatewayOrderID) (order.Result, error) {
	return order.Result{}, nil
}

func (orders *fakeHTTPOrders) List(context.Context, order.ListRequest) ([]order.Result, error) {
	orders.listCalls++
	return []order.Result{}, nil
}

func (orders *fakeHTTPOrders) Cancel(context.Context, order.CancelRequest) (order.Result, error) {
	orders.cancelCalls++
	return order.Result{}, nil
}

type fakeHTTPAccounts struct {
	resource   account.Resource
	status     account.Status
	instrument exchange.Instrument
	public     catalog.Instrument
	capability capability.Row
	spot       exchange.SpotClient
	perp       exchange.PerpClient
}

func (accounts *fakeHTTPAccounts) Accounts() []account.Resource {
	return []account.Resource{accounts.resource}
}
func (accounts *fakeHTTPAccounts) Status(accountID domain.AccountID) (account.Status, error) {
	if accountID != accounts.resource.AccountID {
		return account.Status{}, account.ErrUnknownAccount
	}
	return accounts.status, nil
}
func (accounts *fakeHTTPAccounts) Capability(domain.AccountID) (capability.Row, error) {
	if accounts.capability.RowID != "" {
		return accounts.capability, nil
	}
	return capability.StaticMatrix().Rows[0], nil
}
func (accounts *fakeHTTPAccounts) Instruments(domain.AccountID) (catalog.Snapshot, error) {
	return catalog.Snapshot{Instruments: []catalog.Instrument{accounts.public}}, nil
}
func (accounts *fakeHTTPAccounts) Resolve(context.Context, domain.AccountID, domain.InstrumentID) (exchange.Instrument, error) {
	return accounts.instrument, nil
}
func (accounts *fakeHTTPAccounts) Spot(domain.AccountID) (exchange.SpotClient, error) {
	if accounts.spot != nil {
		return accounts.spot, nil
	}
	return fakeHTTPSpotClient{}, nil
}
func (accounts *fakeHTTPAccounts) Perp(domain.AccountID) (exchange.PerpClient, error) {
	if accounts.perp != nil {
		return accounts.perp, nil
	}
	return nil, account.ErrProductMismatch
}

type fakeHTTPSpotClient struct{ exchange.SpotClient }

func (fakeHTTPSpotClient) Fills(context.Context, exchange.FillsRequest) (exchange.FillPage, error) {
	return exchange.FillPage{}, nil
}
func (fakeHTTPSpotClient) Balances(context.Context) ([]exchange.Balance, error) {
	return []exchange.Balance{}, nil
}
func (fakeHTTPSpotClient) SpotAccount(context.Context) (exchange.SpotAccount, error) {
	return exchange.SpotAccount{}, nil
}

type fakeHTTPReferences struct{ accounts *fakeHTTPAccounts }

func (references *fakeHTTPReferences) Accounts() []account.Resource {
	return references.accounts.Accounts()
}
func (*fakeHTTPReferences) Capabilities(domain.AccountID) ([]reference.AccountCapability, error) {
	return []reference.AccountCapability{}, nil
}
func (references *fakeHTTPReferences) Instruments(domain.AccountID) ([]catalog.Instrument, error) {
	return []catalog.Instrument{references.accounts.public}, nil
}
func (references *fakeHTTPReferences) Rules(domain.AccountID) ([]reference.InstrumentRules, error) {
	return []reference.InstrumentRules{}, nil
}
func (*fakeHTTPReferences) RiskLimits(domain.AccountID) reference.RiskLimitReference {
	return reference.RiskLimitReference{Available: false, Reason: reference.ReasonRiskLimitsNotNormalized}
}
