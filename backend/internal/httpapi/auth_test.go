package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/backend/internal/platform/session"
)

func TestAuthRouteHookStrictLoginSessionAndCSRF(t *testing.T) {
	hash, _ := session.HashPassword("correct horse battery staple")
	sessions, err := session.New(session.Options{Username: "owner", PasswordHash: hash, Mode: "test", TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	if err := RegisterAuthRoutes(mux, AuthRoutesOptions{Sessions: sessions}); err != nil {
		t.Fatal(err)
	}

	unknown := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"owner","password":"correct horse battery staple","mode":"live"}`))
	unknownRec := httptest.NewRecorder()
	mux.ServeHTTP(unknownRec, unknown)
	if unknownRec.Code != http.StatusBadRequest {
		t.Fatalf("unknown login status=%d", unknownRec.Code)
	}

	login := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"owner","password":"correct horse battery staple"}`))
	loginRec := httptest.NewRecorder()
	mux.ServeHTTP(loginRec, login)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", loginRec.Code, loginRec.Body.String())
	}
	cookies := loginRec.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookies=%#v", cookies)
	}

	logout := httptest.NewRequest(http.MethodDelete, "/api/v1/auth/session", nil)
	logout.AddCookie(cookies[0])
	logoutRec := httptest.NewRecorder()
	mux.ServeHTTP(logoutRec, logout)
	if logoutRec.Code != http.StatusForbidden {
		t.Fatalf("logout without csrf status=%d", logoutRec.Code)
	}
}
