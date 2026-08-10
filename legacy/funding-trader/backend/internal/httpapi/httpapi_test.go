package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/auth"
	"github.com/QuantProcessing/funding-trader/backend/internal/readiness"
)

func newTestServer(t *testing.T) http.Handler {
	t.Helper()
	return newTestServerWithLogger(t, nil)
}

func newTestServerWithLogger(t *testing.T, logger *slog.Logger) http.Handler {
	t.Helper()
	store := auth.NewMemoryStore()
	svc := auth.NewService(store, "test", time.Hour, false)
	if err := svc.Bootstrap(context.Background(), "owner@example.com", "first secure password"); err != nil {
		t.Fatal(err)
	}
	checks := readiness.StaticChecks(map[string]readiness.Component{
		"process":    {Ready: true},
		"database":   {Ready: true},
		"gateway":    {Ready: false, Reason: "not-initialized"},
		"publicData": {Ready: false, Reason: "not-initialized"},
		"accounts":   {Ready: false, Reason: "not-initialized"},
	})
	return New(ServerConfig{Mode: "test", Version: "test", Build: "test", Auth: svc, Readiness: checks, Logger: logger})
}

func newTestServerWithStore(t *testing.T) (http.Handler, *auth.MemoryStore) {
	t.Helper()
	store := auth.NewMemoryStore()
	svc := auth.NewService(store, "test", time.Hour, false)
	if err := svc.Bootstrap(context.Background(), "owner@example.com", "first secure password"); err != nil {
		t.Fatal(err)
	}
	checks := readiness.StaticChecks(map[string]readiness.Component{
		"process":    {Ready: true},
		"database":   {Ready: true},
		"gateway":    {Ready: false, Reason: "not-initialized"},
		"publicData": {Ready: false, Reason: "not-initialized"},
		"accounts":   {Ready: false, Reason: "not-initialized"},
	})
	return New(ServerConfig{Mode: "test", Version: "test", Build: "test", Auth: svc, Readiness: checks}), store
}

func newTestServerWithClock(t *testing.T, clock *time.Time) http.Handler {
	t.Helper()
	store := auth.NewMemoryStore()
	svc := auth.NewService(store, "test", time.Hour, false)
	svc.SetClock(func() time.Time { return *clock })
	if err := svc.Bootstrap(context.Background(), "owner@example.com", "first secure password"); err != nil {
		t.Fatal(err)
	}
	checks := readiness.StaticChecks(map[string]readiness.Component{
		"process":    {Ready: true},
		"database":   {Ready: true},
		"gateway":    {Ready: false, Reason: "not-initialized"},
		"publicData": {Ready: false, Reason: "not-initialized"},
		"accounts":   {Ready: false, Reason: "not-initialized"},
	})
	return New(ServerConfig{Mode: "test", Version: "test", Build: "test", Auth: svc, Readiness: checks})
}

func TestLoginSessionMeLogoutContract(t *testing.T) {
	handler := newTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"email":"owner@example.com","password":"first secure password"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-Id", "req-123")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("login status = %d body=%s", res.Code, res.Body.String())
	}
	assertNoBareEnvironmentField(t, res.Body.Bytes())
	if got := res.Header().Get("X-Request-Id"); got != "req-123" {
		t.Fatalf("request id header = %q", got)
	}
	cookies := res.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode || cookies[0].Secure {
		t.Fatalf("bad session cookie: %+v", cookies)
	}
	var login struct {
		Data struct {
			Session struct {
				Authenticated bool   `json:"authenticated"`
				CSRFToken     string `json:"csrfToken"`
				ExpiresAt     string `json:"expiresAt"`
			} `json:"session"`
			Principal struct {
				PrincipalID string  `json:"principalId"`
				Mode        string  `json:"mode"`
				LastLoginAt *string `json:"lastLoginAt"`
			} `json:"principal"`
		} `json:"data"`
		Meta struct {
			RequestID string `json:"requestId"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &login); err != nil {
		t.Fatal(err)
	}
	if !login.Data.Session.Authenticated || login.Data.Session.CSRFToken == "" || login.Data.Principal.PrincipalID != auth.DefaultPrincipalID || login.Data.Principal.Mode != "test" || login.Meta.RequestID != "req-123" {
		t.Fatalf("bad login body: %+v", login)
	}
	if _, err := time.Parse(time.RFC3339, login.Data.Session.ExpiresAt); err != nil {
		t.Fatalf("expiresAt not RFC3339: %v", err)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.AddCookie(cookies[0])
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("me status = %d body=%s", res.Code, res.Body.String())
	}
	assertNoBareEnvironmentField(t, res.Body.Bytes())

	req = httptest.NewRequest(http.MethodGet, "/api/v1/auth/session", nil)
	req.AddCookie(cookies[0])
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("session status = %d body=%s", res.Code, res.Body.String())
	}
	var session struct {
		Data struct {
			Session struct {
				Authenticated bool   `json:"authenticated"`
				CSRFToken     string `json:"csrfToken"`
				ExpiresAt     string `json:"expiresAt"`
			} `json:"session"`
			Principal struct {
				PrincipalID string `json:"principalId"`
				Mode        string `json:"mode"`
			} `json:"principal"`
		} `json:"data"`
		Meta struct {
			RequestID string `json:"requestId"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	if !session.Data.Session.Authenticated || session.Data.Session.CSRFToken != login.Data.Session.CSRFToken || session.Data.Principal.PrincipalID != auth.DefaultPrincipalID || session.Data.Principal.Mode != "test" {
		t.Fatalf("bad session body: %+v", session)
	}
	if _, err := time.Parse(time.RFC3339, session.Data.Session.ExpiresAt); err != nil {
		t.Fatalf("session expiresAt not RFC3339: %v", err)
	}

	req = httptest.NewRequest(http.MethodDelete, "/api/v1/auth/session", nil)
	req.AddCookie(cookies[0])
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("logout missing csrf status = %d body=%s", res.Code, res.Body.String())
	}

	req = httptest.NewRequest(http.MethodDelete, "/api/v1/auth/session", nil)
	req.AddCookie(cookies[0])
	req.Header.Set("X-CSRF-Token", login.Data.Session.CSRFToken)
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("logout status = %d body=%s", res.Code, res.Body.String())
	}

	req = httptest.NewRequest(http.MethodDelete, "/api/v1/auth/session", nil)
	req.AddCookie(cookies[0])
	req.Header.Set("X-CSRF-Token", login.Data.Session.CSRFToken)
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("repeat logout status = %d body=%s", res.Code, res.Body.String())
	}
}

func TestPrivateV1RejectsInvalidServerMode(t *testing.T) {
	for _, mode := range []string{"", "demo", "testnet", "mainnet"} {
		t.Run(mode, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatalf("New accepted invalid product mode %q", mode)
				}
			}()
			_ = New(ServerConfig{
				Mode:      mode,
				Version:   "test",
				Build:     "test",
				Auth:      failingLoginAuth{},
				Readiness: readiness.StaticChecks(nil),
			})
		})
	}
}

func TestPrivateV1StatusAndMeExposeModeNotBareEnvironment(t *testing.T) {
	handler := newTestServer(t)
	cookie, _ := loginTestUser(t, handler)

	for _, path := range []string{"/api/v1/me", "/api/v1/auth/session", "/api/v1/system/status"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.AddCookie(cookie)
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, req)
			if res.Code != http.StatusOK {
				t.Fatalf("status = %d body=%s", res.Code, res.Body.String())
			}
			assertNoBareEnvironmentField(t, res.Body.Bytes())
			if !strings.Contains(res.Body.String(), `"mode":"test"`) {
				t.Fatalf("response missing configured mode: %s", res.Body.String())
			}
		})
	}
}

func TestPrivateV1CrossModeSessionClearsSecureCookie(t *testing.T) {
	store := auth.NewMemoryStore()
	testAuth := auth.NewService(store, "test", time.Hour, false)
	if err := testAuth.Bootstrap(context.Background(), "owner@example.com", "first secure password"); err != nil {
		t.Fatal(err)
	}
	login, err := testAuth.Login(context.Background(), "owner@example.com", "first secure password", "req-test-login")
	if err != nil {
		t.Fatal(err)
	}
	liveAuth := auth.NewService(store, "live", time.Hour, true)
	liveHandler := New(ServerConfig{
		Mode:      "live",
		Version:   "test",
		Build:     "test",
		Auth:      liveAuth,
		Readiness: readiness.StaticChecks(nil),
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: login.Session.Token})
	res := httptest.NewRecorder()
	liveHandler.ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body=%s", res.Code, res.Body.String())
	}
	clears := res.Result().Cookies()
	if len(clears) != 1 ||
		clears[0].Name != sessionCookieName ||
		clears[0].Path != "/" ||
		!clears[0].HttpOnly ||
		!clears[0].Secure ||
		clears[0].SameSite != http.SameSiteStrictMode ||
		clears[0].MaxAge != -1 {
		t.Fatalf("bad clear cookie: %+v", clears)
	}
}

func TestLogoutCredentialBoundaryAndIdempotentReplay(t *testing.T) {
	clock := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	handler := newTestServerWithClock(t, &clock)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/auth/session", nil)
	req.Header.Set("X-CSRF-Token", "csrf")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("no cookie status = %d body=%s", res.Code, res.Body.String())
	}

	loginCookie, csrf := loginForLogoutTest(t, handler)

	req = httptest.NewRequest(http.MethodDelete, "/api/v1/auth/session", nil)
	req.AddCookie(loginCookie)
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("cookie no csrf status = %d body=%s", res.Code, res.Body.String())
	}

	req = httptest.NewRequest(http.MethodDelete, "/api/v1/auth/session", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "unknown-token"})
	req.Header.Set("X-CSRF-Token", "some-csrf")
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("unknown cookie status = %d body=%s", res.Code, res.Body.String())
	}

	req = httptest.NewRequest(http.MethodDelete, "/api/v1/auth/session", nil)
	req.AddCookie(loginCookie)
	req.Header.Set("X-CSRF-Token", "wrong")
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("active wrong csrf status = %d body=%s", res.Code, res.Body.String())
	}

	req = httptest.NewRequest(http.MethodDelete, "/api/v1/auth/session", nil)
	req.AddCookie(loginCookie)
	req.Header.Set("X-CSRF-Token", csrf)
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("first logout status = %d body=%s", res.Code, res.Body.String())
	}

	req = httptest.NewRequest(http.MethodDelete, "/api/v1/auth/session", nil)
	req.AddCookie(loginCookie)
	req.Header.Set("X-CSRF-Token", "wrong")
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("revoked wrong csrf status = %d body=%s", res.Code, res.Body.String())
	}

	req = httptest.NewRequest(http.MethodDelete, "/api/v1/auth/session", nil)
	req.AddCookie(loginCookie)
	req.Header.Set("X-CSRF-Token", csrf)
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("revoked matching csrf status = %d body=%s", res.Code, res.Body.String())
	}

	expiredCookie, expiredCSRF := loginForLogoutTest(t, handler)
	clock = clock.Add(2 * time.Hour)

	req = httptest.NewRequest(http.MethodDelete, "/api/v1/auth/session", nil)
	req.AddCookie(expiredCookie)
	req.Header.Set("X-CSRF-Token", "wrong")
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("expired wrong csrf status = %d body=%s", res.Code, res.Body.String())
	}

	req = httptest.NewRequest(http.MethodDelete, "/api/v1/auth/session", nil)
	req.AddCookie(expiredCookie)
	req.Header.Set("X-CSRF-Token", expiredCSRF)
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("expired matching csrf status = %d body=%s", res.Code, res.Body.String())
	}
}

func loginForLogoutTest(t *testing.T, handler http.Handler) (*http.Cookie, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"email":"owner@example.com","password":"first secure password"}`))
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("login status = %d body=%s", res.Code, res.Body.String())
	}
	var body struct {
		Data struct {
			Session struct {
				CSRFToken string `json:"csrfToken"`
			} `json:"session"`
		} `json:"data"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	cookies := res.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %+v", cookies)
	}
	return cookies[0], body.Data.Session.CSRFToken
}

func TestAuthErrorsMalformedJSONAndReadinessSplit(t *testing.T) {
	handler := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("unauth me status = %d", res.Code)
	}
	var errBody struct {
		Error struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			RequestID string `json:"requestId"`
			Retryable bool   `json:"retryable"`
		} `json:"error"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &errBody); err != nil {
		t.Fatal(err)
	}
	if errBody.Error.Code == "" || errBody.Error.RequestID == "" {
		t.Fatalf("bad error envelope: %+v", errBody)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"email":"owner@example.com","password":"x","extra":1}`))
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d body=%s", res.Code, res.Body.String())
	}

	for name, payload := range map[string]string{
		"trailing garbage": `{"email":"owner@example.com","password":"x"} garbage`,
		"second value":     `{"email":"owner@example.com","password":"x"} {"email":"owner@example.com","password":"x"}`,
	} {
		req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(payload))
		res = httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d body=%s", name, res.Code, res.Body.String())
		}
		var malformed struct {
			Error struct {
				Code      string `json:"code"`
				RequestID string `json:"requestId"`
			} `json:"error"`
		}
		if err := json.Unmarshal(res.Body.Bytes(), &malformed); err != nil {
			t.Fatalf("%s error envelope decode: %v", name, err)
		}
		if malformed.Error.Code != "bad_request" || malformed.Error.RequestID == "" {
			t.Fatalf("%s bad error envelope: %+v", name, malformed)
		}
	}

	req = httptest.NewRequest(http.MethodGet, "/health/live", nil)
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("live status = %d", res.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("ready status = %d body=%s", res.Code, res.Body.String())
	}
	for _, name := range []string{"maintenance", "process", "database", "gateway", "publicData", "accounts"} {
		if !strings.Contains(res.Body.String(), `"`+name+`"`) {
			t.Fatalf("ready body missing component %s: %s", name, res.Body.String())
		}
	}
}

func TestLoginInfrastructureErrorReturnsRetryableServiceError(t *testing.T) {
	rawErr := errors.New("postgres://user:raw-secret@db/funding_trader_test unavailable")
	handler := New(ServerConfig{
		Mode:    "test",
		Version: "test",
		Build:   "test",
		Auth:    failingLoginAuth{err: rawErr},
		Readiness: readiness.StaticChecks(map[string]readiness.Component{
			"process":    {Ready: true},
			"database":   {Ready: true},
			"gateway":    {Ready: false},
			"publicData": {Ready: false},
			"accounts":   {Ready: false},
		}),
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"email":"owner@example.com","password":"first secure password"}`))
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body=%s", res.Code, res.Body.String())
	}
	if strings.Contains(res.Body.String(), "raw-secret") || strings.Contains(res.Body.String(), "postgres://") {
		t.Fatalf("response leaked raw infrastructure error: %s", res.Body.String())
	}
	var body struct {
		Error struct {
			Code      string `json:"code"`
			Retryable bool   `json:"retryable"`
		} `json:"error"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "service_unavailable" || !body.Error.Retryable {
		t.Fatalf("bad error envelope: %+v", body)
	}
}

func TestRequestLoggingSanitizesRequestIDAndExcludesSecrets(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	handler := newTestServerWithLogger(t, logger)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login?password=query-secret&csrfToken=query-csrf", strings.NewReader(`{"email":"owner@example.com","password":"body-secret","csrfToken":"body-csrf"}`))
	req.Header.Set("X-Request-Id", strings.Repeat("x", 200)+"\nsecret")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if got := res.Header().Get("X-Request-Id"); len(got) > 64 || strings.Contains(got, "\n") || strings.Contains(got, "secret") {
		t.Fatalf("request id not sanitized: %q", got)
	}
	out := logs.String()
	for _, forbidden := range []string{"query-secret", "query-csrf", "body-secret", "body-csrf", "ft_session", "csrfToken", strings.Repeat("x", 80)} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("log leaked %q: %s", forbidden, out)
		}
	}
	for _, required := range []string{"requestId", "method", "path", "status", "duration"} {
		if !strings.Contains(out, required) {
			t.Fatalf("log missing %s: %s", required, out)
		}
	}
}

func TestSecurityAuditsForUnauthenticatedAndCSRFOutcomes(t *testing.T) {
	handler, store := newTestServerWithStore(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("unauth status = %d", res.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"email":"owner@example.com","password":"first secure password"}`))
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("login status = %d", res.Code)
	}
	cookie := res.Result().Cookies()[0]

	req = httptest.NewRequest(http.MethodDelete, "/api/v1/auth/session", nil)
	req.AddCookie(cookie)
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("missing csrf status = %d", res.Code)
	}

	req = httptest.NewRequest(http.MethodDelete, "/api/v1/auth/session", nil)
	req.AddCookie(cookie)
	req.Header.Set("X-CSRF-Token", "csrf-secret-wrong")
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("invalid csrf status = %d", res.Code)
	}

	seen := map[string]bool{}
	for _, event := range store.AuditEvents() {
		seen[event.Action+":"+event.Outcome] = true
		joined := event.Action + event.Outcome + event.RequestID + event.Email
		for _, forbidden := range []string{"first secure password", "csrf-secret-wrong", cookie.Value} {
			if strings.Contains(joined, forbidden) {
				t.Fatalf("audit leaked secret %q in %+v", forbidden, event)
			}
		}
	}
	for _, key := range []string{"auth.session:unauthenticated", "auth.csrf:missing", "auth.csrf:invalid"} {
		if !seen[key] {
			t.Fatalf("missing audit outcome %s in %+v", key, store.AuditEvents())
		}
	}
}

func loginTestUser(t *testing.T, handler http.Handler) (*http.Cookie, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"email":"owner@example.com","password":"first secure password"}`))
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("login status = %d body=%s", res.Code, res.Body.String())
	}
	var body struct {
		Data struct {
			Session struct {
				CSRFToken string `json:"csrfToken"`
			} `json:"session"`
		} `json:"data"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	cookies := res.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %+v", cookies)
	}
	return cookies[0], body.Data.Session.CSRFToken
}

func assertNoBareEnvironmentField(t *testing.T, payload []byte) {
	t.Helper()
	var walk func(any, string)
	walk = func(value any, path string) {
		switch typed := value.(type) {
		case map[string]any:
			for key, child := range typed {
				if key == "environment" {
					t.Fatalf("bare environment field at %s in %s", path, string(payload))
				}
				walk(child, path+"."+key)
			}
		case []any:
			for i, child := range typed {
				walk(child, fmt.Sprintf("%s[%d]", path, i))
			}
		}
	}
	var decoded any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	walk(decoded, "$")
}

type failingLoginAuth struct {
	err error
}

func (f failingLoginAuth) Login(context.Context, string, string, string) (auth.LoginResult, error) {
	return auth.LoginResult{}, f.err
}

func (f failingLoginAuth) Authenticate(context.Context, string) (auth.Principal, error) {
	return auth.Principal{}, auth.ErrInvalidSession
}

func (f failingLoginAuth) CurrentSession(context.Context, string) (auth.CurrentSessionResult, error) {
	return auth.CurrentSessionResult{}, auth.ErrInvalidSession
}

func (f failingLoginAuth) RequireCSRF(context.Context, string, string) error {
	return auth.ErrInvalidSession
}

func (f failingLoginAuth) Logout(context.Context, string, string, string) error {
	return auth.ErrInvalidSession
}

func (f failingLoginAuth) AuditSecurityOutcome(context.Context, string, string, string, string) error {
	return nil
}

func (f failingLoginAuth) CookieSecure() bool {
	return false
}
