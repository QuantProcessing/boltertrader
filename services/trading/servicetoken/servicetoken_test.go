package servicetoken

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/services/trading/app"
)

func testToken() []byte { return []byte(strings.Repeat("t", 32)) }

// New also used to take the mode this process runs in and reject anything but
// "test" or "live", which is why this test once had three cases. Token length
// is the whole configuration now.
func TestNewRejectsWeakConfiguration(t *testing.T) {
	if _, err := New([]byte(strings.Repeat("t", 31))); err == nil {
		t.Fatal("short token accepted")
	}
	if _, err := New(testToken()); err != nil {
		t.Fatalf("32-byte token rejected: %v", err)
	}
}

// A browser can reach the service's routes but cannot hold the service token.
// Every request without it must be refused before any handler sees it.
func TestWrapRequiresServiceToken(t *testing.T) {
	authenticator, err := New(testToken())
	if err != nil {
		t.Fatal(err)
	}
	for _, header := range []string{"", "Bearer", "Basic " + string(testToken()), "Bearer wrong", "Bearer " + string(testToken()) + " extra"} {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/positions", nil)
		if header != "" {
			request.Header.Set("Authorization", header)
		}
		recorder := httptest.NewRecorder()
		served := false
		authenticator.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { served = true })).ServeHTTP(recorder, request)
		if served {
			t.Fatalf("header %q reached the handler", header)
		}
		if recorder.Code != http.StatusUnauthorized || recorder.Header().Get("WWW-Authenticate") != "Bearer" {
			t.Fatalf("header %q status=%d challenge=%q", header, recorder.Code, recorder.Header().Get("WWW-Authenticate"))
		}
	}
}

func TestWrapAcceptsTrustedCallerIdentity(t *testing.T) {
	authenticator, err := New(testToken())
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/positions", nil)
	request.Header.Set("Authorization", "bearer "+string(testToken()))
	request.Header.Set(PrincipalHeader, "principal-1")

	var principal string
	var identityErr error
	authenticator.Wrap(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		principal, identityErr = authenticator.Principal(r)
	})).ServeHTTP(httptest.NewRecorder(), request)

	if identityErr != nil || principal != "principal-1" {
		t.Fatalf("principal=%q err=%v", principal, identityErr)
	}
}

// The identity header is authority only because the token gates it. A request
// that carries it without the token must not merely be refused — the header
// must be gone, so that nothing downstream of a future misconfigured proxy can
// read it as identity.
func TestWrapStripsIdentityHeaderFromUntrustedRequests(t *testing.T) {
	authenticator, err := New(testToken())
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/positions", nil)
	request.Header.Set(PrincipalHeader, "attacker")

	authenticator.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("unauthenticated request reached the handler")
	})).ServeHTTP(httptest.NewRecorder(), request)

	if request.Header.Get(PrincipalHeader) != "" {
		t.Fatalf("identity header survived: principal=%q", request.Header.Get(PrincipalHeader))
	}
}

// A valid token with an unusable identity reaches the handler, which then
// answers 401 for its own reasons. What must not happen is the identity being
// honoured anyway.
func TestPrincipalRejectsUnusableIdentity(t *testing.T) {
	authenticator, err := New(testToken())
	if err != nil {
		t.Fatal(err)
	}
	// Two more cases lived here: a principal with no mode header, and one whose
	// mode disagreed with this process. Both were about the second identity
	// header, and a principal that named a mode is now just a principal.
	for name, headers := range map[string]map[string]string{
		"absent": {},
		"blank":  {PrincipalHeader: "   "},
	} {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/reservations", nil)
		request.Header.Set("Authorization", "Bearer "+string(testToken()))
		for key, value := range headers {
			request.Header.Set(key, value)
		}
		served := false
		authenticator.Wrap(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			served = true
			if _, err := authenticator.Principal(r); err == nil {
				t.Fatalf("%s: identity accepted", name)
			}
			if err := authenticator.AuthorizeMutation(r); err == nil {
				t.Fatalf("%s: mutation authorized", name)
			}
			if _, err := authenticator.Authenticate(r); err == nil {
				t.Fatalf("%s: audit access authorized", name)
			}
		})).ServeHTTP(httptest.NewRecorder(), request)
		if !served {
			t.Fatalf("%s: request was refused before the handler", name)
		}
	}
}

// Handlers read identity from the request context, never from a header. A
// request that never passed through Wrap therefore has no identity at all, even
// if it carries the headers.
func TestPrincipalIgnoresHeadersOutsideWrap(t *testing.T) {
	authenticator, err := New(testToken())
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/positions", nil)
	request.Header.Set(PrincipalHeader, "attacker")
	if _, err := authenticator.Principal(request); err == nil {
		t.Fatal("header identity honoured without the middleware")
	}
}

func TestAuthenticatorRedactsToken(t *testing.T) {
	authenticator, err := New(testToken())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := authenticator.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	for _, rendered := range []string{authenticator.String(), authenticator.GoString(), string(encoded)} {
		if !strings.Contains(rendered, "redacted") || strings.Contains(rendered, string(testToken())) {
			t.Fatalf("token leaked in %q", rendered)
		}
	}
}

// The three functions exist to be injected into the trading assembly. Asserting
// the shapes here means P2-4d's wiring cannot discover a mismatch at the point
// where it is hardest to change.
func TestAuthenticatorFitsTheTradingAssembly(t *testing.T) {
	authenticator, err := New(testToken())
	if err != nil {
		t.Fatal(err)
	}
	dependencies := app.Dependencies{
		Principal:         authenticator.Principal,
		Authenticate:      authenticator.Authenticate,
		AuthorizeMutation: authenticator.AuthorizeMutation,
	}
	if dependencies.Principal == nil || dependencies.Authenticate == nil || dependencies.AuthorizeMutation == nil {
		t.Fatal("identity dependencies are unset")
	}
}
