package httpapi

import (
	"context"
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

func TestInternalAuthorityCreateRequiresAdapterAuthCASAndStrictOpaqueReference(t *testing.T) {
	authority := &fakeInternalAuthority{response: AuthorityResponse{
		Body:                 DesiredAccount{AccountID: "acct_1", Venue: "example", VenueEnvironment: "testnet", SecretRef: "opaque_ref_1", DesiredStateRevision: 1, RuntimeGeneration: 1, CredentialVersion: 1, RequestedState: "creating"},
		DesiredStateRevision: 1,
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
	body := `{"accountId":"acct_1","venue":"example","venueEnvironment":"testnet","secretRef":"opaque_ref_1","desiredStateRevision":1,"runtimeGeneration":1,"credentialVersion":1,"requestedState":"creating"}`

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, "/internal/v1/accounts/desired", strings.NewReader(body)))
	if unauthorized.Code != http.StatusUnauthorized || len(authority.requests) != 0 {
		t.Fatalf("unauthorized status=%d requests=%d", unauthorized.Code, len(authority.requests))
	}

	missingCAS := httptest.NewRequest(http.MethodPost, "/internal/v1/accounts/desired", strings.NewReader(body))
	missingCAS.Header.Set("Authorization", "Bearer gateway-adapter")
	missingCAS.Header.Set("Content-Type", "application/json")
	missingCASResponse := httptest.NewRecorder()
	handler.ServeHTTP(missingCASResponse, missingCAS)
	if missingCASResponse.Code != http.StatusPreconditionRequired || len(authority.requests) != 0 {
		t.Fatalf("missing CAS status=%d requests=%d body=%s", missingCASResponse.Code, len(authority.requests), missingCASResponse.Body.String())
	}

	invalid := httptest.NewRequest(http.MethodPost, "/internal/v1/accounts/desired", strings.NewReader(strings.Replace(body, `"requestedState":"creating"`, `"requestedState":"creating","secretMaterial":{"apiKey":"credential-canary"}`, 1)))
	invalid.Header.Set("Authorization", "Bearer gateway-adapter")
	invalid.Header.Set("Content-Type", "application/json")
	invalid.Header.Set("If-None-Match", "*")
	invalidResponse := httptest.NewRecorder()
	handler.ServeHTTP(invalidResponse, invalid)
	if invalidResponse.Code != http.StatusBadRequest || len(authority.requests) != 0 || strings.Contains(invalidResponse.Body.String(), "credential-canary") {
		t.Fatalf("invalid status=%d requests=%d body=%s", invalidResponse.Code, len(authority.requests), invalidResponse.Body.String())
	}

	request := httptest.NewRequest(http.MethodPost, "/internal/v1/accounts/desired", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer gateway-adapter")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("If-None-Match", "*")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || response.Header().Get("ETag") != `"desired-state-revision-1"` || len(authority.requests) != 1 {
		t.Fatalf("create status=%d etag=%q requests=%d body=%s", response.Code, response.Header().Get("ETag"), len(authority.requests), response.Body.String())
	}
	if authority.requests[0].Operation != AuthorityCreateDesired || authority.requests[0].Desired == nil || authority.requests[0].Desired.SecretRef != "opaque_ref_1" {
		t.Fatalf("authority request=%+v", authority.requests[0])
	}
}

func TestInternalAuthorityExistingMutationsRequireStrongIfMatchAndRedactFailures(t *testing.T) {
	authority := &fakeInternalAuthority{err: errors.New("vault://sensitive/path credential-canary")}
	handler, err := NewInternalHandler(InternalOptions{
		Authority:    authority,
		Authenticate: func(*http.Request) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, value := range []string{"", "*", `W/"desired-state-revision-2"`, `"desired-state-revision-2", "desired-state-revision-3"`, `"desired-state-revision-0"`} {
		request := httptest.NewRequest(http.MethodPost, "/internal/v1/accounts/desired/acct_1/validate", strings.NewReader(`{"desiredStateRevision":2,"runtimeGeneration":1,"credentialVersion":1}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("If-Match", value)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusPreconditionRequired || len(authority.requests) != 0 {
			t.Fatalf("If-Match %q status=%d requests=%d body=%s", value, response.Code, len(authority.requests), response.Body.String())
		}
	}

	request := httptest.NewRequest(http.MethodPost, "/internal/v1/accounts/desired/acct_1/validate", strings.NewReader(`{"desiredStateRevision":2,"runtimeGeneration":1,"credentialVersion":1}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("If-Match", `"desired-state-revision-2"`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError || len(authority.requests) != 1 {
		t.Fatalf("valid mutation status=%d requests=%d body=%s", response.Code, len(authority.requests), response.Body.String())
	}
	if strings.Contains(response.Body.String(), "vault://") || strings.Contains(response.Body.String(), "credential-canary") {
		t.Fatalf("authority error leaked sensitive detail: %s", response.Body.String())
	}
	if authority.requests[0].ExpectedRevision != 2 || authority.requests[0].Operation != AuthorityValidateDesired {
		t.Fatalf("authority request=%+v", authority.requests[0])
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
		{http.MethodPost, "/internal/v1/accounts/desired"},
		{http.MethodGet, "/internal/v1/accounts/desired/{accountId}"},
		{http.MethodPut, "/internal/v1/accounts/desired/{accountId}"},
		{http.MethodPost, "/internal/v1/accounts/desired/{accountId}/validate"},
		{http.MethodPost, "/internal/v1/accounts/desired/{accountId}/rotate"},
		{http.MethodPost, "/internal/v1/accounts/desired/{accountId}/disable"},
		{http.MethodPost, "/internal/v1/accounts/desired/{accountId}/drain"},
		{http.MethodDelete, "/internal/v1/accounts/desired/{accountId}"},
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

func TestInternalAuthorityDeleteRejectsBodiesBeforeAuthority(t *testing.T) {
	authority := new(fakeInternalAuthority)
	handler, err := NewInternalHandler(InternalOptions{Authority: authority, Authenticate: func(*http.Request) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodDelete, "/internal/v1/accounts/desired/acct_1", strings.NewReader(`{"force":true}`))
	request.Header.Set("If-Match", `"desired-state-revision-2"`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || len(authority.requests) != 0 {
		t.Fatalf("delete status=%d requests=%d body=%s", response.Code, len(authority.requests), response.Body.String())
	}
}

func TestDesiredMutationValidationRejectsUnfrozenStatesAndFences(t *testing.T) {
	base := DesiredMutation{DesiredStateRevision: 2, RuntimeGeneration: 1, CredentialVersion: 1, RequestedState: "disabled"}
	if err := base.validate(AuthorityDisableDesired); err != nil {
		t.Fatalf("valid disable: %v", err)
	}
	invalidState := base
	invalidState.RequestedState = "force_enabled"
	if err := invalidState.validate(AuthorityDisableDesired); err == nil {
		t.Fatal("accepted unfrozen requested state")
	}
	invalidFence := DesiredMutation{
		SecretRef: "opaque_ref_2", DesiredStateRevision: 2, RuntimeGeneration: 1, CredentialVersion: 1,
		RotationFence: &RotationFence{RotationFenceID: "rotation_1", State: "armed", FromCredentialVersion: 1, ToCredentialVersion: 1, FromRuntimeGeneration: 1, ToRuntimeGeneration: 2},
	}
	if err := invalidFence.validate(AuthorityRotateDesired); err == nil {
		t.Fatal("accepted non-advancing credential rotation fence")
	}
}
