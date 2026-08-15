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
	"github.com/QuantProcessing/boltertrader/gateway/store"
)

func TestHandlerAuthenticatesBeforeDisclosureAndPlacesStrictDecimalOrder(t *testing.T) {
	fixture := newHTTPFixture(t)
	body := `{"account_id":"acct-a","route_id":"spot","instrument_id":"binance:demo:spot:BTC-USDT","idempotency_key":"idem-1","side":"buy","type":"limit","quantity":"0.001","limit_price":"100","limit_policy":"resting","desired_state_revision":1,"runtime_generation":2,"credential_version":3}`

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
		fixture.orders.place.RouteID != "spot" ||
		fixture.orders.place.Quantity.String() != "0.001" || fixture.orders.place.WriteFence != (account.WriteFence{
		AccountID: "acct-a", DesiredStateRevision: 1, RuntimeGeneration: 2, CredentialVersion: 3,
	}) {
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
		// An order that does not name its route is refused here rather than
		// forwarded: the coordinator validates the route before it resolves
		// one, so an omitted route_id surfaces as an unclassified failure --
		// a 500 -- instead of the caller error it is.
		{"missing route", http.MethodPost, "/v1/orders", `{"account_id":"acct-a","instrument_id":"binance:demo:spot:BTC-USDT","idempotency_key":"idem-1","side":"buy","type":"market","quantity":"0.001"}`, http.StatusBadRequest},
		{"amend unsupported", http.MethodPatch, "/v1/orders/018f0000-0000-7000-8000-000000000002", `{}`, http.StatusNotImplemented},
		{"order book requires scope", http.MethodGet, "/v1/order-book", "", http.StatusBadRequest},
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

func TestHandlerAccountReadinessIncludesCoordinatorWriteGate(t *testing.T) {
	fixture := newHTTPFixture(t)
	fixture.orders.writeBlocked = true
	fixture.orders.writeReason = order.ReasonRecoveryRequired

	for _, path := range []string{"/v1/accounts", "/v1/accounts/acct-a"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer test-token")
		recorder := httptest.NewRecorder()
		fixture.handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET %s status=%d body=%s", path, recorder.Code, recorder.Body.String())
		}
		if !strings.Contains(recorder.Body.String(), `"write_ready":false`) ||
			!strings.Contains(recorder.Body.String(), `"reason":"order_recovery_required"`) {
			t.Fatalf("GET %s did not expose coordinator gate: %s", path, recorder.Body.String())
		}
	}
}

func TestHandlerRejectsHostileJSONAndPaginationWithoutBackendIO(t *testing.T) {
	fixture := newHTTPFixture(t)
	valid := `{"account_id":"acct-a","route_id":"spot","instrument_id":"binance:demo:spot:BTC-USDT","idempotency_key":"idem-1","side":"buy","type":"market","quantity":"0.001"}`
	tests := []struct {
		name        string
		contentType string
		body        []byte
	}{
		{name: "missing content type", body: []byte(valid)},
		{name: "wrong content type", contentType: "text/plain", body: []byte(valid)},
		{name: "duplicate nested key", contentType: "application/json", body: []byte(`{"account_id":"acct-a","route_id":"spot","instrument_id":"binance:demo:spot:BTC-USDT","idempotency_key":"idem-1","side":"buy","type":"market","quantity":"0.001","extra":{"x":1,"x":2}}`)},
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
	body := `{"account_id":"acct-a","route_id":"spot","instrument_id":"binance:demo:spot:BTC-USDT","idempotency_key":"idem-1","side":"buy","type":"market","quantity":"0.001"}`
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
	body := `{"account_id":"acct-a","route_id":"spot","instrument_id":"binance:demo:spot:BTC-USDT","idempotency_key":"idem-conflict","side":"buy","type":"market","quantity":"0.001"}`
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

func TestHandlerListFillsByGatewayOrderIDFiltersNativeOrderAndHidesVenueOrderID(t *testing.T) {
	fixture := newHTTPFixture(t)
	gatewayOrderID := domain.GatewayOrderID("018f0000-0000-7000-8000-000000000099")
	fixture.orders.getResult = order.Result{
		GatewayOrderID: gatewayOrderID,
		AccountID:      "acct-a",
		InstrumentID:   "binance:demo:spot:BTC-USDT",
		VenueOrderID:   "venue-2",
	}
	fixture.accounts.spot = recordingSpotClient{page: exchange.FillPage{
		Fills: []exchange.Fill{
			{OrderID: "venue-1", FillID: "fill-1"},
			{OrderID: "venue-2", FillID: "fill-2"},
		},
		Page: exchange.PageInfo{Cursor: "fill-2", HasMoreKnown: false, HasMore: false},
	}}

	request := httptest.NewRequest(http.MethodGet, "/v1/fills?account_id=acct-a&instrument_id=binance:demo:spot:BTC-USDT&gateway_order_id="+string(gatewayOrderID), nil)
	request.Header.Set("Authorization", "Bearer test-token")
	recorder := httptest.NewRecorder()

	fixture.handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if fixture.orders.getCalls != 1 || fixture.orders.getPrincipal == "" || fixture.orders.getGatewayOrderID != gatewayOrderID {
		t.Fatalf("order ownership lookup principal=%q gateway=%q calls=%d", fixture.orders.getPrincipal, fixture.orders.getGatewayOrderID, fixture.orders.getCalls)
	}
	var response struct {
		Items []struct {
			GatewayOrderID domain.GatewayOrderID `json:"gateway_order_id"`
			Fill           exchange.Fill         `json:"fill"`
		} `json:"items"`
		NextCursor string `json:"next_cursor,omitempty"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Items) != 1 || response.Items[0].GatewayOrderID != gatewayOrderID ||
		response.Items[0].Fill.FillID != "fill-2" || response.Items[0].Fill.OrderID != "" || response.NextCursor != "" {
		t.Fatalf("response=%+v body=%s", response, recorder.Body.String())
	}
}

func TestHandlerListFillsByGatewayOrderIDAllowsInstrumentScopeWithoutAccountID(t *testing.T) {
	fixture := newHTTPFixture(t)
	gatewayOrderID := domain.GatewayOrderID("018f0000-0000-7000-8000-000000000099")
	fixture.orders.getResult = order.Result{
		GatewayOrderID: gatewayOrderID,
		AccountID:      "acct-a",
		InstrumentID:   "binance:demo:spot:BTC-USDT",
		VenueOrderID:   "venue-2",
	}
	fixture.accounts.spot = recordingSpotClient{page: exchange.FillPage{
		Fills: []exchange.Fill{{OrderID: "venue-2", FillID: "fill-2"}},
	}}

	request := httptest.NewRequest(http.MethodGet, "/v1/fills?instrument_id=binance:demo:spot:BTC-USDT&gateway_order_id="+string(gatewayOrderID), nil)
	request.Header.Set("Authorization", "Bearer test-token")
	recorder := httptest.NewRecorder()

	fixture.handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	ordinary := httptest.NewRequest(http.MethodGet, "/v1/fills?instrument_id=binance:demo:spot:BTC-USDT", nil)
	ordinary.Header.Set("Authorization", "Bearer test-token")
	ordinaryRecorder := httptest.NewRecorder()
	fixture.handler.ServeHTTP(ordinaryRecorder, ordinary)
	if ordinaryRecorder.Code != http.StatusBadRequest {
		t.Fatalf("ordinary instrument-only status=%d body=%s", ordinaryRecorder.Code, ordinaryRecorder.Body.String())
	}
}

func TestHandlerListFillsByGatewayOrderIDFailsClosedOnMissingVenueOrScopeMismatch(t *testing.T) {
	tests := []struct {
		name       string
		result     order.Result
		requestURL string
		wantStatus int
	}{
		{
			name: "missing venue order id",
			result: order.Result{
				GatewayOrderID: "018f0000-0000-7000-8000-000000000099",
				AccountID:      "acct-a",
				InstrumentID:   "binance:demo:spot:BTC-USDT",
			},
			requestURL: "/v1/fills?account_id=acct-a&instrument_id=binance:demo:spot:BTC-USDT&gateway_order_id=018f0000-0000-7000-8000-000000000099",
			wantStatus: http.StatusNotFound,
		},
		{
			name: "account mismatch",
			result: order.Result{
				GatewayOrderID: "018f0000-0000-7000-8000-000000000099",
				AccountID:      "acct-b",
				InstrumentID:   "binance:demo:spot:BTC-USDT",
				VenueOrderID:   "venue-2",
			},
			requestURL: "/v1/fills?account_id=acct-a&instrument_id=binance:demo:spot:BTC-USDT&gateway_order_id=018f0000-0000-7000-8000-000000000099",
			wantStatus: http.StatusNotFound,
		},
		{
			name: "instrument mismatch",
			result: order.Result{
				GatewayOrderID: "018f0000-0000-7000-8000-000000000099",
				AccountID:      "acct-a",
				InstrumentID:   "binance:demo:spot:ETH-USDT",
				VenueOrderID:   "venue-2",
			},
			requestURL: "/v1/fills?account_id=acct-a&instrument_id=binance:demo:spot:BTC-USDT&gateway_order_id=018f0000-0000-7000-8000-000000000099",
			wantStatus: http.StatusNotFound,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newHTTPFixture(t)
			fixture.orders.getResult = test.result
			fixture.accounts.spot = recordingSpotClient{page: exchange.FillPage{Fills: []exchange.Fill{{OrderID: "venue-2", FillID: "fill-2"}}}}

			request := httptest.NewRequest(http.MethodGet, test.requestURL, nil)
			request.Header.Set("Authorization", "Bearer test-token")
			recorder := httptest.NewRecorder()

			fixture.handler.ServeHTTP(recorder, request)

			if recorder.Code != test.wantStatus {
				t.Fatalf("status=%d want=%d body=%s", recorder.Code, test.wantStatus, recorder.Body.String())
			}
		})
	}
}

func TestHandlerListFillsByGatewayOrderIDNotFoundDoesNotQueryVenue(t *testing.T) {
	fixture := newHTTPFixture(t)
	fixture.orders.getErr = store.ErrNotFound
	spot := &countingSpotClient{}
	fixture.accounts.spot = spot

	request := httptest.NewRequest(http.MethodGet, "/v1/fills?gateway_order_id=018f0000-0000-7000-8000-000000000099", nil)
	request.Header.Set("Authorization", "Bearer test-token")
	recorder := httptest.NewRecorder()

	fixture.handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if spot.fillsCalls != 0 {
		t.Fatalf("venue fills calls=%d, want 0", spot.fillsCalls)
	}
}

func TestHandlerListFillsNextCursorRequiresKnownHasMore(t *testing.T) {
	fixture := newHTTPFixture(t)
	fixture.accounts.spot = recordingSpotClient{page: exchange.FillPage{
		Fills: []exchange.Fill{{FillID: "fill-1"}},
		Page:  exchange.PageInfo{Cursor: "native-next", HasMoreKnown: false, HasMore: false},
	}}

	request := httptest.NewRequest(http.MethodGet, "/v1/fills?account_id=acct-a&instrument_id=binance:demo:spot:BTC-USDT&pagination.limit=10", nil)
	request.Header.Set("Authorization", "Bearer test-token")
	recorder := httptest.NewRecorder()

	fixture.handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "next_cursor") {
		t.Fatalf("unexpected next_cursor: %s", recorder.Body.String())
	}
}

func TestHandlerListFillsProjectsSettlementAssetFromResolvedInstrument(t *testing.T) {
	fixture := newHTTPFixture(t)
	fixture.accounts.spot = recordingSpotClient{page: exchange.FillPage{
		Fills: []exchange.Fill{{FillID: "fill-1", FeeAsset: "BNB"}},
	}}

	request := httptest.NewRequest(http.MethodGet, "/v1/fills?account_id=acct-a&instrument_id=binance:demo:spot:BTC-USDT", nil)
	request.Header.Set("Authorization", "Bearer test-token")
	recorder := httptest.NewRecorder()
	fixture.handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Items []struct {
			SettlementAsset string `json:"settlement_asset"`
		} `json:"items"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Items) != 1 || response.Items[0].SettlementAsset != "USDT" {
		t.Fatalf("response=%+v body=%s", response, recorder.Body.String())
	}
}

func TestHandlerListFillsProjectsSettlementAssetWithoutInstrumentFilter(t *testing.T) {
	fixture := newHTTPFixture(t)
	fixture.accounts.spot = recordingSpotClient{page: exchange.FillPage{
		Fills: []exchange.Fill{{FillID: "fill-1", NativeSymbol: "BTCUSDT", FeeAsset: "BNB"}},
	}}
	request := httptest.NewRequest(http.MethodGet, "/v1/fills?account_id=acct-a", nil)
	request.Header.Set("Authorization", "Bearer test-token")
	recorder := httptest.NewRecorder()
	fixture.handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Items []struct {
			InstrumentID    domain.InstrumentID `json:"instrument_id"`
			SettlementAsset string              `json:"settlement_asset"`
		} `json:"items"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Items) != 1 || response.Items[0].InstrumentID != "binance:demo:spot:BTC-USDT" || response.Items[0].SettlementAsset != "USDT" {
		t.Fatalf("response=%+v body=%s", response, recorder.Body.String())
	}
}

func TestHandlerListFillsRejectsMissingSettlementAuthority(t *testing.T) {
	fixture := newHTTPFixture(t)
	fixture.accounts.instrument.Identity.QuoteAsset = ""
	fixture.accounts.spot = recordingSpotClient{page: exchange.FillPage{Fills: []exchange.Fill{{FillID: "fill-1"}}}}
	request := httptest.NewRequest(http.MethodGet, "/v1/fills?account_id=acct-a&instrument_id=binance:demo:spot:BTC-USDT", nil)
	request.Header.Set("Authorization", "Bearer test-token")
	recorder := httptest.NewRecorder()
	fixture.handler.ServeHTTP(recorder, request)
	if recorder.Code == http.StatusOK {
		t.Fatalf("missing settlement authority accepted: %s", recorder.Body.String())
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

func TestGatewayWriteGateRejectsPublicMutationsBeforeDecode(t *testing.T) {
	fixture := newHTTPFixture(t)
	handler := NewWriteGate(func() bool { return false }).Wrap(fixture.handler)
	for _, request := range []struct{ method, path string }{
		{http.MethodPost, "/v1/orders"},
		{http.MethodPatch, "/v1/orders/not-an-id"},
		{http.MethodDelete, "/v1/orders/not-an-id"},
		{http.MethodPut, "/v1/accounts/not-an-id/instruments/not-an-id/leverage"},
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(request.method, request.path, strings.NewReader("{")))
		if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "GATEWAY_WRITES_NOT_READY") {
			t.Fatalf("%s %s: status=%d body=%s", request.method, request.path, recorder.Code, recorder.Body.String())
		}
	}
}

type fakeHTTPOrders struct {
	place             order.PlaceRequest
	placeCalls        int
	placeErr          error
	placeResult       order.Result
	getCalls          int
	getPrincipal      domain.PrincipalID
	getGatewayOrderID domain.GatewayOrderID
	getErr            error
	getResult         order.Result
	listCalls         int
	cancelCalls       int
	writeBlocked      bool
	writeReason       string
}

func (orders *fakeHTTPOrders) AccountWriteStatus(domain.AccountID) order.AccountWriteStatus {
	return order.AccountWriteStatus{WriteReady: !orders.writeBlocked, Reason: orders.writeReason}
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

func (orders *fakeHTTPOrders) Get(_ context.Context, principalID domain.PrincipalID, gatewayOrderID domain.GatewayOrderID) (order.Result, error) {
	orders.getCalls++
	orders.getPrincipal = principalID
	orders.getGatewayOrderID = gatewayOrderID
	if orders.getErr != nil {
		return order.Result{}, orders.getErr
	}
	if orders.getResult.GatewayOrderID == "" {
		return order.Result{}, store.ErrNotFound
	}
	return orders.getResult, nil
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
	resource    account.Resource
	status      account.Status
	instrument  exchange.Instrument
	public      catalog.Instrument
	instruments []catalog.Instrument
	resolved    map[domain.InstrumentID]exchange.Instrument
	capability  capability.Row
	spot        exchange.SpotClient
	perp        exchange.PerpClient
	// routes and routePerps describe an account that runs more than one route
	// of the same product — the shape bybit takes when it settles perpetuals in
	// both USDT and USDC. Fixtures that leave them empty have a single route,
	// and every accessor below falls back to the single-route fields.
	routes     []account.Resource
	routePerps map[string]exchange.PerpClient
	// routeCapabilities is keyed by settlement asset, the only thing that tells
	// two routes of one perp account apart.
	routeCapabilities map[string]capability.Row
}

func (accounts *fakeHTTPAccounts) Accounts() []account.Resource {
	if len(accounts.routes) > 0 {
		return accounts.routes
	}
	return []account.Resource{accounts.resource}
}
func (accounts *fakeHTTPAccounts) Status(accountID domain.AccountID) (account.Status, error) {
	if accountID != accounts.resource.AccountID {
		return account.Status{}, account.ErrUnknownAccount
	}
	return accounts.status, nil
}
func (accounts *fakeHTTPAccounts) Capability(domain.AccountID) (capability.Row, error) {
	// The real manager reports one capability row per route and refuses to
	// collapse two of them into a single answer. A fixture that quietly
	// returned the first row would hide the exact failure this package
	// regressed on, so it refuses here too.
	if len(accounts.routes) > 1 {
		return capability.Row{}, account.ErrProductMismatch
	}
	return accounts.capabilityRow(), nil
}

// RouteCapability answers per route when routeCapabilities names one, and
// otherwise falls back to the account-wide row so the single-route fixtures
// keep behaving exactly as they did.
func (accounts *fakeHTTPAccounts) RouteCapability(_ domain.AccountID, _, settlement string) (capability.Row, error) {
	if row, ok := accounts.routeCapabilities[settlement]; ok {
		return row, nil
	}
	return accounts.capabilityRow(), nil
}

func (accounts *fakeHTTPAccounts) capabilityRow() capability.Row {
	if accounts.capability.RowID != "" {
		return accounts.capability
	}
	return capability.StaticMatrix().Rows[0]
}
func (accounts *fakeHTTPAccounts) Instruments(domain.AccountID) (catalog.Snapshot, error) {
	if len(accounts.instruments) > 0 {
		return catalog.Snapshot{Instruments: accounts.instruments}, nil
	}
	return catalog.Snapshot{Instruments: []catalog.Instrument{accounts.public}}, nil
}
func (accounts *fakeHTTPAccounts) Resolve(_ context.Context, _ domain.AccountID, instrumentID domain.InstrumentID) (exchange.Instrument, error) {
	if accounts.resolved != nil {
		if instrument, ok := accounts.resolved[instrumentID]; ok {
			return instrument, nil
		}
		return exchange.Instrument{}, account.ErrUnknownAccount
	}
	return accounts.instrument, nil
}
func (accounts *fakeHTTPAccounts) RouteStatus(accountID domain.AccountID, product, settlement string) (account.Status, error) {
	if len(accounts.routes) == 0 {
		return accounts.Status(accountID)
	}
	for _, route := range accounts.routes {
		if route.AccountID != accountID || string(route.Product) != product || route.Settlement != settlement {
			continue
		}
		return account.Status{Resource: route, ClientReady: true}, nil
	}
	return account.Status{}, account.ErrProductMismatch
}
func (accounts *fakeHTTPAccounts) SpotRoute(domain.AccountID, string) (exchange.SpotClient, error) {
	if accounts.spot != nil {
		return accounts.spot, nil
	}
	return fakeHTTPSpotClient{}, nil
}
func (accounts *fakeHTTPAccounts) PerpRoute(_ domain.AccountID, settlement string) (exchange.PerpClient, error) {
	if client, ok := accounts.routePerps[settlement]; ok {
		return client, nil
	}
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

type recordingSpotClient struct {
	exchange.SpotClient
	page exchange.FillPage
	err  error
}

func (client recordingSpotClient) Fills(context.Context, exchange.FillsRequest) (exchange.FillPage, error) {
	if client.err != nil {
		return exchange.FillPage{}, client.err
	}
	return client.page, nil
}
func (recordingSpotClient) Balances(context.Context) ([]exchange.Balance, error) {
	return []exchange.Balance{}, nil
}
func (recordingSpotClient) SpotAccount(context.Context) (exchange.SpotAccount, error) {
	return exchange.SpotAccount{}, nil
}

type countingSpotClient struct {
	exchange.SpotClient
	fillsCalls int
}

func (client *countingSpotClient) Fills(context.Context, exchange.FillsRequest) (exchange.FillPage, error) {
	client.fillsCalls++
	return exchange.FillPage{}, nil
}
func (*countingSpotClient) Balances(context.Context) ([]exchange.Balance, error) {
	return []exchange.Balance{}, nil
}
func (*countingSpotClient) SpotAccount(context.Context) (exchange.SpotAccount, error) {
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
