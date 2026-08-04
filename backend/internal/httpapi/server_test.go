package httpapi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	frozencontract "github.com/QuantProcessing/boltertrader/backend/internal/contract"
)

func TestBrowserAuthAndStrictLoginValidation(t *testing.T) {
	srv := newTestHandler(t)

	unauthorized := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	unauthorizedRec := httptest.NewRecorder()
	srv.ServeHTTP(unauthorizedRec, unauthorized)
	if unauthorizedRec.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d want 401", unauthorizedRec.Code)
	}

	bad := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBufferString(`{"username":"owner","password":"wrong"}`))
	badRec := httptest.NewRecorder()
	srv.ServeHTTP(badRec, bad)
	if badRec.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d want 401", badRec.Code)
	}

	good := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBufferString(`{"username":"owner","password":"correct horse battery staple"}`))
	goodRec := httptest.NewRecorder()
	srv.ServeHTTP(goodRec, good)
	if goodRec.Code != http.StatusOK {
		t.Fatalf("code=%d want 200", goodRec.Code)
	}
	cookies := goodRec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected session cookie")
	}

	meReq := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	for _, cookie := range cookies {
		meReq.AddCookie(cookie)
	}
	meRec := httptest.NewRecorder()
	srv.ServeHTTP(meRec, meReq)
	if meRec.Code != http.StatusOK {
		t.Fatalf("code=%d want 200", meRec.Code)
	}
}

func TestBrowserRoutesMatchFrozenContract(t *testing.T) {
	contract, err := frozencontract.Load()
	if err != nil {
		t.Fatal(err)
	}
	var routes []string
	for _, listener := range contract.Listeners {
		if listener.ID == "platform-browser" {
			routes = append(routes, listener.Routes...)
		}
	}
	if len(routes) == 0 {
		t.Fatal("no browser routes found")
	}
	if routes[0] != "POST /api/v1/auth/login" {
		t.Fatalf("unexpected first route %q", routes[0])
	}
	if routes[len(routes)-1] != "GET /api/v1/realtime/stream" {
		t.Fatalf("unexpected last route %q", routes[len(routes)-1])
	}
}
