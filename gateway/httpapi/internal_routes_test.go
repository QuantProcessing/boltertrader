package httpapi

import (
	"context"
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

func TestInternalAuthorityDoesNotExposeCredentialRotation(t *testing.T) {
	authority := new(fakeInternalAuthority)
	handler, err := NewInternalHandler(InternalOptions{Authority: authority, Authenticate: func(*http.Request) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPut, "/internal/v1/accounts/desired/account-a/rotation/operation-a",
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
	requests := []*http.Request{}
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
		{http.MethodGet, "/internal/v1/accounts/runtime"},
		{http.MethodGet, "/internal/v1/accounts/runtime/{accountId}"},
		{http.MethodGet, "/internal/v1/accounts/runtime/{accountId}/events"},
		{http.MethodGet, "/internal/v1/catalog/venues"},
		{http.MethodGet, "/internal/v1/catalog/instruments"},
		{http.MethodGet, "/internal/v1/market-data"},
		{http.MethodGet, "/internal/v1/candles"},
		{http.MethodGet, "/internal/v1/market/ws"},
		{http.MethodGet, "/internal/v1/account/ws"},
		{http.MethodGet, "/internal/v1/market-facts"},
		{http.MethodGet, "/internal/v1/fresh-bbo"},
		{http.MethodGet, "/internal/v1/private-events"},
		{http.MethodGet, "/internal/v1/funding-board"},
		{http.MethodGet, "/internal/v1/funding-history"},
		{http.MethodGet, "/internal/v1/venues/connectivity"},
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

func TestGatewayWriteGateRejectsInternalPUTBeforeDecodeAndDispatch(t *testing.T) {
	authority := &fakeInternalAuthority{}
	handler, err := NewInternalHandler(InternalOptions{
		Authority: authority, Authenticate: func(*http.Request) error { return nil }, WriteReady: func() bool { return false },
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{} {
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
