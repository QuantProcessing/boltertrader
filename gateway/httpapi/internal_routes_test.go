package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
			"routes": []map[string]any{{"routeId": "perp-usdt", "product": "perp", "settlementAsset": "USDT", "routeProfileId": "binance.perp.usdt.demo.v1", "requestedState": "active"}},
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
	body := `{"operationId":"` + operationID + `","accountId":"acct_1","venue":"binance","nativeEnvironment":"demo","secretRef":"` + opaqueRef + `","desiredStateRevision":2,"runtimeGeneration":3,"credentialVersion":4,"requestedState":"active","routes":[{"routeId":"perp-usdt","product":"perp","settlementAsset":"USDT","routeProfileId":"binance.perp.usdt.demo.v1","requestedState":"active"}]}`
	path := "/internal/v1/accounts/desired/acct_1/operations/" + operationID

	for name, mutate := range map[string]func(*http.Request){
		"unauthenticated": func(*http.Request) {},
		"missing mode": func(request *http.Request) {
			request.Header.Set("Authorization", "Bearer gateway-adapter")
		},
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPut, path, strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			mutate(request)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code < 400 || len(authority.requests) != 0 {
				t.Fatalf("status=%d requests=%d body=%s", response.Code, len(authority.requests), response.Body.String())
			}
		})
	}

	invalid := httptest.NewRequest(http.MethodPut, path, strings.NewReader(strings.Replace(body, `"routes":`, `"secretMaterial":{"apiKey":"credential-canary"},"routes":`, 1)))
	invalid.Header.Set("Authorization", "Bearer gateway-adapter")
	invalid.Header.Set("Content-Type", "application/json")
	invalid.Header.Set("X-Bolter-Mode", "test")
	invalidResponse := httptest.NewRecorder()
	handler.ServeHTTP(invalidResponse, invalid)
	if invalidResponse.Code != http.StatusBadRequest || len(authority.requests) != 0 || strings.Contains(invalidResponse.Body.String(), "credential-canary") {
		t.Fatalf("invalid status=%d requests=%d body=%s", invalidResponse.Code, len(authority.requests), invalidResponse.Body.String())
	}

	request := httptest.NewRequest(http.MethodPut, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer gateway-adapter")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Bolter-Mode", "test")
	request.Header.Set("X-Request-ID", "platform-request-1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || len(authority.requests) != 1 {
		t.Fatalf("status=%d requests=%d body=%s", response.Code, len(authority.requests), response.Body.String())
	}
	received := authority.requests[0]
	if received.Operation != AuthorityApplyDesired || received.Mode != "test" || received.OperationID != operationID || received.AccountID != "acct_1" || received.DesiredApply == nil || received.DesiredApply.SecretRef != opaqueRef {
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
	body := `{"operationId":"` + operationID + `","accountId":"acct_1","desiredStateRevision":2,"runtimeGeneration":1,"credentialVersion":1}`
	request := httptest.NewRequest(http.MethodPut, "/internal/v1/accounts/desired/acct_1/retirements/"+operationID, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Bolter-Mode", "test")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || len(authority.requests) != 1 || authority.requests[0].Operation != AuthorityRetireDesired {
		t.Fatalf("status=%d requests=%+v body=%s", response.Code, authority.requests, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"tombstoned":true`) || strings.Contains(response.Body.String(), "secretRef") {
		t.Fatalf("retirement response is not explicit and redacted: %s", response.Body.String())
	}
}

func TestInternalAuthorityFinalizesRotationThroughStrictProofRoute(t *testing.T) {
	const operationID = "018f47a8-2578-7d3c-9d7d-5e4f4a445c40"
	completedAt := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	proof := RotationFinalizationProof{
		OperationID: operationID, AccountID: "acct_1",
		ActiveFence:       AccountFence{AccountID: "acct_1", DesiredStateRevision: 3, RuntimeGeneration: 3, CredentialVersion: 2},
		RetiringFence:     AccountFence{AccountID: "acct_1", DesiredStateRevision: 2, RuntimeGeneration: 2, CredentialVersion: 1},
		TombstoneRevision: 3, RetiringState: "retired", SecretReferenceState: "tombstoned", CompletedAt: completedAt,
	}
	authority := &fakeInternalAuthority{response: AuthorityResponse{Status: http.StatusOK, Body: proof}}
	handler, err := NewInternalHandler(InternalOptions{Authority: authority, Authenticate: func(*http.Request) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"operationId":"` + operationID + `","accountId":"acct_1","expectedActiveFence":{"accountId":"acct_1","desiredStateRevision":3,"runtimeGeneration":3,"credentialVersion":2},"expectedRetiringFence":{"accountId":"acct_1","desiredStateRevision":2,"runtimeGeneration":2,"credentialVersion":1}}`
	path := "/internal/v1/accounts/desired/acct_1/rotation-finalizations/" + operationID
	request := httptest.NewRequest(http.MethodPut, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Bolter-Mode", "test")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || len(authority.requests) != 1 {
		t.Fatalf("status=%d requests=%+v body=%s", response.Code, authority.requests, response.Body.String())
	}
	received := authority.requests[0]
	if received.Operation != AuthorityFinalizeRotation || received.Mode != "test" || received.AccountID != "acct_1" || received.OperationID != operationID || received.RotationFinalization == nil {
		t.Fatalf("authority request=%+v", received)
	}
	for _, forbidden := range []string{`"secretRef":`, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", `"namespace":`, `"provider":`, `"keyVersion":`} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("proof leaked %q: %s", forbidden, response.Body.String())
		}
	}
	if !strings.Contains(response.Body.String(), `"retiringState":"retired"`) || !strings.Contains(response.Body.String(), `"secretReferenceState":"tombstoned"`) {
		t.Fatalf("response is not terminal proof: %s", response.Body.String())
	}
}

func TestRotationFinalizationValidationRejectsIdentityFenceAndShapeConflicts(t *testing.T) {
	const operationID = "018f47a8-2578-7d3c-9d7d-5e4f4a445c40"
	valid := RotationFinalizationRequest{
		OperationID: operationID, AccountID: "acct_1",
		ExpectedActiveFence:   AccountFence{AccountID: "acct_1", DesiredStateRevision: 3, RuntimeGeneration: 3, CredentialVersion: 2},
		ExpectedRetiringFence: AccountFence{AccountID: "acct_1", DesiredStateRevision: 2, RuntimeGeneration: 2, CredentialVersion: 1},
	}
	if err := valid.validate("acct_1", operationID); err != nil {
		t.Fatalf("valid request: %v", err)
	}
	tests := map[string]func(*RotationFinalizationRequest){
		"path account": func(request *RotationFinalizationRequest) { request.AccountID = "acct_2" },
		"path operation": func(request *RotationFinalizationRequest) {
			request.OperationID = "018f47a8-2578-7d3c-9d7d-5e4f4a445c41"
		},
		"active account":      func(request *RotationFinalizationRequest) { request.ExpectedActiveFence.AccountID = "acct_2" },
		"retiring account":    func(request *RotationFinalizationRequest) { request.ExpectedRetiringFence.AccountID = "acct_2" },
		"revision gap":        func(request *RotationFinalizationRequest) { request.ExpectedActiveFence.DesiredStateRevision++ },
		"generation gap":      func(request *RotationFinalizationRequest) { request.ExpectedActiveFence.RuntimeGeneration++ },
		"credential gap":      func(request *RotationFinalizationRequest) { request.ExpectedActiveFence.CredentialVersion++ },
		"zero retiring fence": func(request *RotationFinalizationRequest) { request.ExpectedRetiringFence.RuntimeGeneration = 0 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			request := valid
			mutate(&request)
			if err := request.validate("acct_1", operationID); err == nil {
				t.Fatal("invalid request accepted")
			}
		})
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
		{http.MethodPut, "/internal/v1/accounts/desired/{accountId}/rotation-finalizations/{operationId}"},
		{http.MethodGet, "/internal/v1/accounts/runtime"},
		{http.MethodGet, "/internal/v1/accounts/runtime/{accountId}"},
		{http.MethodGet, "/internal/v1/accounts/runtime/{accountId}/events"},
		{http.MethodGet, "/internal/v1/catalog/venues"},
		{http.MethodGet, "/internal/v1/catalog/instruments"},
		{http.MethodGet, "/internal/v1/market-data"},
		{http.MethodGet, "/internal/v1/private-events"},
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

func TestDesiredApplyValidationRejectsModeConflictAndDuplicateRoutes(t *testing.T) {
	desired := DesiredApplyRequest{
		OperationID: "018f47a8-2578-7d3c-9d7d-5e4f4a445c33", AccountID: "acct_1", Venue: "binance",
		NativeEnvironment: "demo", SecretRef: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		DesiredStateRevision: 1, RuntimeGeneration: 1, CredentialVersion: 1, RequestedState: "active",
		Routes: []DesiredRoute{{RouteID: "perp-usdt", Product: "perp", SettlementAsset: "USDT", RouteProfileID: "binance.perp.usdt.demo.v1", RequestedState: "active"}},
	}
	if err := desired.validate("test", desired.AccountID, desired.OperationID); err != nil {
		t.Fatalf("valid desired apply: %v", err)
	}
	if err := desired.validate("live", desired.AccountID, desired.OperationID); err == nil {
		t.Fatal("live mode accepted a demo route profile")
	}
	desired.Routes = append(desired.Routes, desired.Routes[0])
	if err := desired.validate("test", desired.AccountID, desired.OperationID); err == nil {
		t.Fatal("duplicate route id accepted")
	}
}

func TestRuntimeSnapshotViewPublishesSafeOperationIDWithoutSecrets(t *testing.T) {
	view := RuntimeSnapshotView{
		OperationID: "018f47a8-2578-7d3c-9d7d-5e4f4a445c33", DesiredStateRevision: 1,
		RuntimeGeneration: 1, CredentialVersion: 1, RequestedState: "active",
		Routes: []RuntimeRouteView{{RouteID: "spot", Product: "spot", RouteProfileID: "binance.spot.demo.v1", RequestedState: "active", Ready: true}},
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"operationId":"018f47a8-2578-7d3c-9d7d-5e4f4a445c33"`) || strings.Contains(string(encoded), "secretRef") || strings.Contains(string(encoded), "provider") {
		t.Fatalf("runtime view=%s", encoded)
	}
}
