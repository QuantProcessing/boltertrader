package httpapi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
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
