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

	frozencontract "github.com/QuantProcessing/boltertrader/backend/internal/contract"
	platformaccounts "github.com/QuantProcessing/boltertrader/backend/internal/platform/accounts"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/session"
)

type correlationAudit struct {
	records []platformaccounts.AuditRecord
}

func (audit *correlationAudit) Append(_ context.Context, record platformaccounts.AuditRecord) error {
	audit.records = append(audit.records, record)
	return nil
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

func TestGeneratedRequestIDIsStableAcrossAccountAuditAndResponse(t *testing.T) {
	hash, err := session.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := session.New(session.Options{Username: "owner", PasswordHash: hash, Mode: "test", TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	login, err := sessions.Login(context.Background(), "owner", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	audit := &correlationAudit{}
	accountService, err := platformaccounts.New(platformaccounts.Options{
		Store: platformaccounts.NewMemoryStore(), Secrets: &routeSecretWriter{ref: "opaque-ref"}, Audit: audit,
		ID: func() string { return "acct-correlation" },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := accountService.Create(context.Background(), platformaccounts.CreateRequest{Venue: "binance", VenueEnvironment: platformaccounts.EnvironmentDemo, DisplayName: "Seed", ProductIDs: []string{"manual"}, SecretMaterial: []byte(`{"apiKey":"seed"}`)}, "setup"); err != nil {
		t.Fatal(err)
	}
	audit.records = nil
	handler, err := New(Options{
		Sessions: sessions, Mode: "test", ContractVersion: ContractVersion,
		Accounts: &AccountRoutesOptions{
			Service: accountService, Authenticate: SessionAuthenticator(sessions), AuthorizeMutation: SessionMutationAuthorizer(sessions),
			Blockers: func(context.Context, string) (platformaccounts.Blockers, error) {
				return platformaccounts.Blockers{}, nil
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPatch, "/api/v1/accounts/acct-correlation", strings.NewReader(`{"displayName":"Correlation","productIds":["manual"]}`))
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: login.Session.Token})
	request.Header.Set("X-CSRF-Token", login.Session.CSRFToken)
	request.Header.Set("If-Match", AccountETag(1))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("update=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Meta struct {
			RequestID string `json:"requestId"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Meta.RequestID == "" || len(audit.records) != 1 || audit.records[0].RequestID != body.Meta.RequestID {
		t.Fatalf("response requestId=%q audit=%+v", body.Meta.RequestID, audit.records)
	}
}
