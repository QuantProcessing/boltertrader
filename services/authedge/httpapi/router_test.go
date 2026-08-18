package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/services/authedge/session"
)

func newTestHandler(t *testing.T) http.Handler {
	t.Helper()
	return newTestHandlerWith(t, nil)
}

func newTestHandlerWith(t *testing.T, downstream func(*http.ServeMux) ([]string, error)) http.Handler {
	t.Helper()
	passwordHash, err := session.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("session.HashPassword() error = %v", err)
	}
	sessions, err := session.New(session.Options{Username: "owner", PasswordHash: passwordHash, TTL: time.Hour})
	if err != nil {
		t.Fatalf("session.New() error = %v", err)
	}
	handler, err := New(Options{Sessions: sessions, ContractVersion: ContractVersion, Downstream: downstream})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return handler
}

func TestLoginSessionCSRFAndLogout(t *testing.T) {
	handler := newTestHandler(t)
	loginRequest := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"owner","password":"correct horse battery staple"}`))
	loginRequest.Header.Set("X-Request-Id", "req-login")
	loginResponse := httptest.NewRecorder()
	handler.ServeHTTP(loginResponse, loginRequest)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("login status = %d body=%s", loginResponse.Code, loginResponse.Body.String())
	}
	cookies := loginResponse.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != SessionCookieName || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("login cookies = %#v", cookies)
	}
	var login struct {
		Data struct {
			Session struct {
				CSRFToken string `json:"csrfToken"`
			} `json:"session"`
		} `json:"data"`
		Meta struct {
			RequestID string `json:"requestId"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(loginResponse.Body.Bytes(), &login); err != nil {
		t.Fatalf("decode login: %v", err)
	}
	if login.Data.Session.CSRFToken == "" || login.Meta.RequestID != "req-login" {
		t.Fatalf("login payload = %#v", login)
	}

	meRequest := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	meRequest.AddCookie(cookies[0])
	meResponse := httptest.NewRecorder()
	handler.ServeHTTP(meResponse, meRequest)
	if meResponse.Code != http.StatusOK {
		t.Fatalf("me status = %d body=%s", meResponse.Code, meResponse.Body.String())
	}

	logoutWithoutCSRF := httptest.NewRequest(http.MethodDelete, "/api/v1/auth/session", nil)
	logoutWithoutCSRF.AddCookie(cookies[0])
	logoutResponse := httptest.NewRecorder()
	handler.ServeHTTP(logoutResponse, logoutWithoutCSRF)
	if logoutResponse.Code != http.StatusForbidden {
		t.Fatalf("logout without csrf status = %d body=%s", logoutResponse.Code, logoutResponse.Body.String())
	}

	logoutRequest := httptest.NewRequest(http.MethodDelete, "/api/v1/auth/session", nil)
	logoutRequest.AddCookie(cookies[0])
	logoutRequest.Header.Set("X-CSRF-Token", login.Data.Session.CSRFToken)
	logoutResponse = httptest.NewRecorder()
	handler.ServeHTTP(logoutResponse, logoutRequest)
	if logoutResponse.Code != http.StatusNoContent {
		t.Fatalf("logout status = %d body=%s", logoutResponse.Code, logoutResponse.Body.String())
	}
}

func TestBrowserRouterIsStrictAndDoesNotMountWorkRoutes(t *testing.T) {
	handler := newTestHandler(t)
	tests := []struct {
		name   string
		method string
		path   string
		body   string
		want   int
	}{
		// `mode` was never a login field, and now it is not a concept either.
		// A client still sending it must be told so rather than logged in on a
		// request the server only half understood.
		{name: "unknown login field", method: http.MethodPost, path: "/api/v1/auth/login", body: `{"username":"owner","password":"correct horse battery staple","mode":"live"}`, want: http.StatusBadRequest},
		{name: "wrong login method", method: http.MethodGet, path: "/api/v1/auth/login", want: http.StatusMethodNotAllowed},
		{name: "gateway route absent", method: http.MethodGet, path: "/internal/v1/accounts/desired", want: http.StatusNotFound},
		{name: "unversioned route absent", method: http.MethodGet, path: "/system/status", want: http.StatusNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status = %d want %d body=%s", response.Code, test.want, response.Body.String())
			}
		})
	}
}

// Everything past the four auth routes is mounted, not intercepted: the
// downstream sees the request with its own authorization still to do. The one
// thing this listener guarantees it is a request id, because a downstream that
// minted its own would log a different one than the browser was told.
func TestDownstreamRoutesAreMountedAndCarryARequestID(t *testing.T) {
	var seen []string
	handler := newTestHandlerWith(t, func(mux *http.ServeMux) ([]string, error) {
		mux.HandleFunc("GET /api/v1/system/readiness", func(w http.ResponseWriter, r *http.Request) {
			seen = append(seen, r.Header.Get("X-Request-Id"))
			w.WriteHeader(http.StatusOK)
		})
		return []string{"GET /api/v1/system/readiness"}, nil
	})

	// No cookie: reaching the handler is the assertion. Deciding who may read
	// readiness belongs to whatever mounted the route.
	anonymous := httptest.NewRequest(http.MethodGet, "/api/v1/system/readiness", nil)
	anonymousResponse := httptest.NewRecorder()
	handler.ServeHTTP(anonymousResponse, anonymous)
	if anonymousResponse.Code != http.StatusOK {
		t.Fatalf("status = %d want 200 body=%s", anonymousResponse.Code, anonymousResponse.Body.String())
	}

	carried := httptest.NewRequest(http.MethodGet, "/api/v1/system/readiness", nil)
	carried.Header.Set("X-Request-Id", "req-from-browser")
	handler.ServeHTTP(httptest.NewRecorder(), carried)

	if len(seen) != 2 {
		t.Fatalf("downstream reached %d times, want 2", len(seen))
	}
	if seen[0] == "" {
		t.Error("downstream saw no request id on a request that arrived without one")
	}
	if seen[1] != "req-from-browser" {
		t.Errorf("downstream saw request id %q, want the one the caller sent", seen[1])
	}
}
