package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/services/authedge/session"
)

type testWriteAuthority struct{ ready bool }

func (authority testWriteAuthority) Ready(context.Context) (bool, string) {
	if authority.ready {
		return true, "ready"
	}
	return false, "writer_role_unavailable"
}

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

// The canonical write gate is the last line of defence before any mutation
// reaches storage: while the writer role is unavailable every mutating route is
// refused, but the two auth routes that let an operator see and end their own
// session stay reachable — otherwise a degraded deployment would also trap the
// operator in a session they cannot log out of.
func TestCanonicalWriteGateBlocksMutationsButLeavesAuthReachable(t *testing.T) {
	hash, err := session.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := session.New(session.Options{Username: "owner", PasswordHash: hash, Mode: "test", TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := New(Options{
		Sessions: sessions, Mode: "test", ContractVersion: ContractVersion, WriteAuthority: testWriteAuthority{},
		// The gate guards routes this listener forwards rather than serves, so
		// the probe below needs one mounted to have something to be refused.
		Downstream: func(mux *http.ServeMux) ([]string, error) {
			mux.HandleFunc("POST /api/v1/accounts", func(w http.ResponseWriter, _ *http.Request) {
				t.Error("downstream reached while canonical writes were unavailable")
				w.WriteHeader(http.StatusCreated)
			})
			return []string{"POST /api/v1/accounts"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	loginRequest := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"owner","password":"correct horse battery staple"}`))
	loginResponse := httptest.NewRecorder()
	handler.ServeHTTP(loginResponse, loginRequest)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("login=%d body=%s", loginResponse.Code, loginResponse.Body.String())
	}
	var loginBody struct {
		Data struct {
			Session struct {
				CSRFToken string `json:"csrfToken"`
			} `json:"session"`
		} `json:"data"`
	}
	if err := json.Unmarshal(loginResponse.Body.Bytes(), &loginBody); err != nil {
		t.Fatal(err)
	}
	login := loginResponse.Result().Cookies()[0]
	request := httptest.NewRequest(http.MethodPost, "/api/v1/accounts", strings.NewReader(`{"not":"decoded"}`))
	request.AddCookie(login)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "CANONICAL_WRITES_NOT_READY") {
		t.Fatalf("response=%d body=%s", response.Code, response.Body.String())
	}
	sessionRequest := httptest.NewRequest(http.MethodGet, "/api/v1/auth/session", nil)
	sessionRequest.AddCookie(login)
	sessionResponse := httptest.NewRecorder()
	handler.ServeHTTP(sessionResponse, sessionRequest)
	if sessionResponse.Code != http.StatusOK {
		t.Fatalf("session=%d body=%s", sessionResponse.Code, sessionResponse.Body.String())
	}
	logoutRequest := httptest.NewRequest(http.MethodDelete, "/api/v1/auth/session", nil)
	logoutRequest.AddCookie(login)
	logoutRequest.Header.Set("X-CSRF-Token", loginBody.Data.Session.CSRFToken)
	logoutResponse := httptest.NewRecorder()
	handler.ServeHTTP(logoutResponse, logoutRequest)
	if logoutResponse.Code != http.StatusNoContent {
		t.Fatalf("logout=%d body=%s", logoutResponse.Code, logoutResponse.Body.String())
	}
}
