package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeInternalAuthority struct {
	requests []AuthorityRequest
	response AuthorityResponse
	err      error
}

func (authority *fakeInternalAuthority) Execute(_ context.Context, request AuthorityRequest) (AuthorityResponse, error) {
	authority.requests = append(authority.requests, request)
	return authority.response, authority.err
}

func TestInternalAuthorityAppliesDesiredThroughStrictOperationIdentity(t *testing.T) {
	const operationID = "018f47a8-2578-7d3c-9d7d-5e4f4a445c33"
	const opaqueRef = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	authority := &fakeInternalAuthority{response: AuthorityResponse{
		Status: http.StatusAccepted,
		Body: map[string]any{
			"accountId": "acct_1", "requestedState": "active",
			"routes": []map[string]any{{"routeId": "perp-usdt", "product": "perp", "settlementAsset": "USDT", "routeProfileId": "binance.perp.usdt.v1", "requestedState": "active"}},
		},
	}}
	handler, err := NewInternalHandler(InternalOptions{
		Authority: authority,
		Authenticate: func(r *http.Request) error {
			if r.Header.Get("Authorization") != "Bearer gateway-adapter" {
				return errors.New("wrong audience")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"operationId":"` + operationID + `","accountId":"acct_1","runtimeId":"018f47a8-2578-7d3c-9d7d-5e4f4a445e11","venue":"binance","nativeEnvironment":"live","secretRef":"` + opaqueRef + `","requestedState":"active","routes":[{"routeId":"perp-usdt","product":"perp","settlementAsset":"USDT","routeProfileId":"binance.perp.usdt.v1","requestedState":"active"}]}`
	path := "/internal/v1/accounts/desired/acct_1/operations/" + operationID

	// A caller the audience check rejects must not reach the authority at all.
	// This used to be one of two such callers: the other authenticated correctly
	// but carried no mode attestation header, and the route refused it before it
	// could name which mode it meant. There is one mode left, so the header is
	// gone and authentication is the whole of what stands here.
	unauthenticated := httptest.NewRequest(http.MethodPut, path, strings.NewReader(body))
	unauthenticated.Header.Set("Content-Type", "application/json")
	unauthenticatedResponse := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticatedResponse, unauthenticated)
	if unauthenticatedResponse.Code < 400 || len(authority.requests) != 0 {
		t.Fatalf("unauthenticated status=%d requests=%d body=%s", unauthenticatedResponse.Code, len(authority.requests), unauthenticatedResponse.Body.String())
	}

	invalid := httptest.NewRequest(http.MethodPut, path, strings.NewReader(strings.Replace(body, `"routes":`, `"secretMaterial":{"apiKey":"credential-canary"},"routes":`, 1)))
	invalid.Header.Set("Authorization", "Bearer gateway-adapter")
	invalid.Header.Set("Content-Type", "application/json")
	invalidResponse := httptest.NewRecorder()
	handler.ServeHTTP(invalidResponse, invalid)
	if invalidResponse.Code != http.StatusBadRequest || len(authority.requests) != 0 || strings.Contains(invalidResponse.Body.String(), "credential-canary") {
		t.Fatalf("invalid status=%d requests=%d body=%s", invalidResponse.Code, len(authority.requests), invalidResponse.Body.String())
	}

	request := httptest.NewRequest(http.MethodPut, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer gateway-adapter")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Request-ID", "platform-request-1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || len(authority.requests) != 1 {
		t.Fatalf("status=%d requests=%d body=%s", response.Code, len(authority.requests), response.Body.String())
	}
	received := authority.requests[0]
	if received.Operation != AuthorityApplyDesired || received.OperationID != operationID || received.AccountID != "acct_1" || received.DesiredApply == nil || received.DesiredApply.SecretRef != opaqueRef {
		t.Fatalf("authority request=%+v", received)
	}
	if strings.Contains(response.Body.String(), "secretRef") || strings.Contains(response.Body.String(), opaqueRef) {
		t.Fatalf("response leaked opaque reference: %s", response.Body.String())
	}
}

func TestInternalAuthorityRetirementReturnsExplicitTombstoneTruth(t *testing.T) {
	const operationID = "018f47a8-2578-7d3c-9d7d-5e4f4a445c34"
	authority := &fakeInternalAuthority{response: AuthorityResponse{
		Status: http.StatusOK,
		Body:   RuntimeView{AccountID: "acct_1", Retiring: []RuntimeSnapshotView{}, Tombstoned: true},
	}}
	handler, err := NewInternalHandler(InternalOptions{Authority: authority, Authenticate: func(*http.Request) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"operationId":"` + operationID + `","accountId":"acct_1","runtimeId":"018f47a8-2578-7d3c-9d7d-5e4f4a445e11"}`
	request := httptest.NewRequest(http.MethodPut, "/internal/v1/accounts/desired/acct_1/retirements/"+operationID, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || len(authority.requests) != 1 || authority.requests[0].Operation != AuthorityRetireDesired {
		t.Fatalf("status=%d requests=%+v body=%s", response.Code, authority.requests, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"tombstoned":true`) || strings.Contains(response.Body.String(), "secretRef") {
		t.Fatalf("retirement response is not explicit and redacted: %s", response.Body.String())
	}
}

func TestInternalAuthorityDoesNotExposeCredentialRotation(t *testing.T) {
	authority := new(fakeInternalAuthority)
	handler, err := NewInternalHandler(InternalOptions{Authority: authority, Authenticate: func(*http.Request) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPut,
		"/internal/v1/accounts/desired/acct_1/rotation-finalizations/018f47a8-2578-7d3c-9d7d-5e4f4a445c40",
		strings.NewReader(`{}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("status=%d", response.Code)
	}
	if len(authority.requests) != 0 {
		t.Fatalf("rotation reached authority: %+v", authority.requests)
	}
}

func TestInternalAuthorityRejectsLegacyDesiredMutationRoutes(t *testing.T) {
	authority := new(fakeInternalAuthority)
	handler, err := NewInternalHandler(InternalOptions{Authority: authority, Authenticate: func(*http.Request) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	requests := []*http.Request{
		httptest.NewRequest(http.MethodPost, "/internal/v1/accounts/desired", strings.NewReader(`{}`)),
		httptest.NewRequest(http.MethodPut, "/internal/v1/accounts/desired/acct_1", strings.NewReader(`{}`)),
		httptest.NewRequest(http.MethodPost, "/internal/v1/accounts/desired/acct_1/validate", strings.NewReader(`{}`)),
		httptest.NewRequest(http.MethodPost, "/internal/v1/accounts/desired/acct_1/rotate", strings.NewReader(`{}`)),
		httptest.NewRequest(http.MethodPost, "/internal/v1/accounts/desired/acct_1/disable", strings.NewReader(`{}`)),
		httptest.NewRequest(http.MethodPost, "/internal/v1/accounts/desired/acct_1/drain", strings.NewReader(`{}`)),
		httptest.NewRequest(http.MethodDelete, "/internal/v1/accounts/desired/acct_1", strings.NewReader(`{}`)),
	}
	for _, request := range requests {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound && response.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s status=%d", request.Method, request.URL.Path, response.Code)
		}
	}
	if len(authority.requests) != 0 {
		t.Fatalf("legacy routes reached authority: %+v", authority.requests)
	}
}

func TestInternalAuthorityRouteInventoryIsFrozen(t *testing.T) {
	want := []Route{
		{http.MethodGet, "/internal/v1/runtime/status"},
		{http.MethodGet, "/internal/v1/runtime/readiness"},
		{http.MethodGet, "/internal/v1/runtime/open-orders"},
		{http.MethodGet, "/internal/v1/runtime/positions"},
		{http.MethodGet, "/internal/v1/runtime/uncertain-commands"},
		{http.MethodGet, "/internal/v1/accounts/desired"},
		{http.MethodGet, "/internal/v1/accounts/desired/{accountId}"},
		{http.MethodPut, "/internal/v1/accounts/desired/{accountId}/operations/{operationId}"},
		{http.MethodPut, "/internal/v1/accounts/desired/{accountId}/retirements/{operationId}"},
		{http.MethodGet, "/internal/v1/accounts/runtime"},
		{http.MethodGet, "/internal/v1/accounts/runtime/{accountId}"},
		{http.MethodGet, "/internal/v1/accounts/runtime/{accountId}/events"},
		{http.MethodGet, "/internal/v1/catalog/venues"},
		{http.MethodGet, "/internal/v1/catalog/instruments"},
		{http.MethodGet, "/internal/v1/market-data"},
		{http.MethodGet, "/internal/v1/candles"},
		{http.MethodGet, "/internal/v1/public/ws"},
		{http.MethodGet, "/internal/v1/private-events"},
		{http.MethodGet, "/internal/v1/funding-board"},
		{http.MethodGet, "/internal/v1/funding-history"},
	}
	got := InternalRoutes()
	if len(got) != len(want) {
		t.Fatalf("internal routes=%v want=%v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("internal route[%d]=%v want=%v", index, got[index], want[index])
		}
	}
}

// TestDesiredApplyValidationRejectsUnreachableEnvironmentAndDuplicateRoutes keeps
// both halves of what used to be a mode-conflict test.
//
// The first half once proved a live request would not carry a demo route
// profile. Modes are gone and profiles no longer name an environment, but the
// request still does — it is persisted beside the account and re-read on every
// restart — and only live endpoints exist. A request naming demo describes a
// runtime nothing can dial, so it is refused at the edge rather than stored.
func TestDesiredApplyValidationRejectsUnreachableEnvironmentAndDuplicateRoutes(t *testing.T) {
	desired := DesiredApplyRequest{
		OperationID: "018f47a8-2578-7d3c-9d7d-5e4f4a445c33", AccountID: "acct_1", Venue: "binance",
		NativeEnvironment: "live", SecretRef: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		RuntimeID: "018f47a8-2578-7d3c-9d7d-5e4f4a445e11", RequestedState: "active",
		Routes: []DesiredRoute{{RouteID: "perp-usdt", Product: "perp", SettlementAsset: "USDT", RouteProfileID: "binance.perp.usdt.v1", RequestedState: "active"}},
	}
	if err := desired.validate(desired.AccountID, desired.OperationID); err != nil {
		t.Fatalf("valid desired apply: %v", err)
	}
	unreachable := desired
	unreachable.NativeEnvironment = "demo"
	if err := unreachable.validate(desired.AccountID, desired.OperationID); err == nil {
		t.Fatal("desired apply accepted endpoints that no longer exist")
	}
	desired.Routes = append(desired.Routes, desired.Routes[0])
	if err := desired.validate(desired.AccountID, desired.OperationID); err == nil {
		t.Fatal("duplicate route id accepted")
	}
}

func TestRuntimeSnapshotViewPublishesSafeOperationIDWithoutSecrets(t *testing.T) {
	view := RuntimeSnapshotView{
		OperationID: "018f47a8-2578-7d3c-9d7d-5e4f4a445c33", RuntimeID: "018f47a8-2578-7d3c-9d7d-5e4f4a445e11", RequestedState: "active",
		Routes: []RuntimeRouteView{{RouteID: "spot", Product: "spot", RouteProfileID: "binance.spot.v1", RequestedState: "active", Ready: true}},
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"operationId":"018f47a8-2578-7d3c-9d7d-5e4f4a445c33"`) || strings.Contains(string(encoded), "secretRef") || strings.Contains(string(encoded), "provider") {
		t.Fatalf("runtime view=%s", encoded)
	}
}

func TestGatewayWriteGateRejectsInternalPUTBeforeDecodeAndDispatch(t *testing.T) {
	authority := &fakeInternalAuthority{}
	handler, err := NewInternalHandler(InternalOptions{
		Authority: authority, Authenticate: func(*http.Request) error { return nil }, WriteReady: func() bool { return false },
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/internal/v1/accounts/desired/a/operations/op",
		"/internal/v1/accounts/desired/a/retirements/op",
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPut, path, strings.NewReader("{")))
		if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "GATEWAY_WRITES_NOT_READY") {
			t.Fatalf("path=%s status=%d body=%s", path, recorder.Code, recorder.Body.String())
		}
	}
	if len(authority.requests) != 0 {
		t.Fatalf("authority dispatched %d requests", len(authority.requests))
	}
}
