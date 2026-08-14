package servicetoken

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/services/trading/app"
)

func testToken() []byte { return []byte(strings.Repeat("t", 32)) }

func TestNewRejectsWeakConfiguration(t *testing.T) {
	if _, err := New([]byte(strings.Repeat("t", 31)), "test"); err == nil {
		t.Fatal("short token accepted")
	}
	if _, err := New(testToken(), "paper"); err == nil {
		t.Fatal("unknown mode accepted")
	}
	if _, err := New(testToken(), "live"); err != nil {
		t.Fatalf("live mode rejected: %v", err)
	}
}

// A browser can reach the service's routes but cannot hold the service token.
// Every request without it must be refused before any handler sees it.
func TestWrapRequiresServiceToken(t *testing.T) {
	authenticator, err := New(testToken(), "test")
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
	authenticator, err := New(testToken(), "test")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/positions", nil)
	request.Header.Set("Authorization", "bearer "+string(testToken()))
	request.Header.Set(PrincipalHeader, "principal-1")
	request.Header.Set(ModeHeader, "test")

	var principal, mode string
	var identityErr error
	authenticator.Wrap(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		principal, mode, identityErr = authenticator.Principal(r)
	})).ServeHTTP(httptest.NewRecorder(), request)

	if identityErr != nil || principal != "principal-1" || mode != "test" {
		t.Fatalf("principal=%q mode=%q err=%v", principal, mode, identityErr)
	}
}

// The identity headers are authority only because the token gates them. A
// request that carries them without the token must not merely be refused — the
// headers must be gone, so that nothing downstream of a future misconfigured
// proxy can read them as identity.
func TestWrapStripsIdentityHeadersFromUntrustedRequests(t *testing.T) {
	authenticator, err := New(testToken(), "test")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/positions", nil)
	request.Header.Set(PrincipalHeader, "attacker")
	request.Header.Set(ModeHeader, "live")

	authenticator.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("unauthenticated request reached the handler")
	})).ServeHTTP(httptest.NewRecorder(), request)

	if request.Header.Get(PrincipalHeader) != "" || request.Header.Get(ModeHeader) != "" {
		t.Fatalf("identity headers survived: principal=%q mode=%q", request.Header.Get(PrincipalHeader), request.Header.Get(ModeHeader))
	}
}

// A valid token with an unusable identity reaches the handler, which then
// answers 401 for its own reasons. What must not happen is the identity being
// honoured anyway.
func TestPrincipalRejectsUnusableIdentity(t *testing.T) {
	authenticator, err := New(testToken(), "test")
	if err != nil {
		t.Fatal(err)
	}
	for name, headers := range map[string]map[string]string{
		"absent":       {},
		"blank":        {PrincipalHeader: "   ", ModeHeader: "test"},
		"missing mode": {PrincipalHeader: "principal-1"},
		"crossed mode": {PrincipalHeader: "principal-1", ModeHeader: "live"},
	} {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/reservations", nil)
		request.Header.Set("Authorization", "Bearer "+string(testToken()))
		for key, value := range headers {
			request.Header.Set(key, value)
		}
		served := false
		authenticator.Wrap(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			served = true
			if _, _, err := authenticator.Principal(r); err == nil {
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
	authenticator, err := New(testToken(), "test")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/positions", nil)
	request.Header.Set(PrincipalHeader, "attacker")
	request.Header.Set(ModeHeader, "test")
	if _, _, err := authenticator.Principal(request); err == nil {
		t.Fatal("header identity honoured without the middleware")
	}
}

func TestAuthenticatorRedactsToken(t *testing.T) {
	authenticator, err := New(testToken(), "test")
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
	authenticator, err := New(testToken(), "test")
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
