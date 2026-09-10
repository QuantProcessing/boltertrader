package httpapi

import (
	"context"
	"errors"
	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/account"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type accountRegistrationStub struct {
	addErr         error
	added, removed int
}

func (*accountRegistrationStub) List(context.Context) ([]account.Registration, error) {
	return []account.Registration{}, nil
}
func (*accountRegistrationStub) Get(context.Context, domain.AccountID) (account.Registration, error) {
	return account.Registration{}, account.ErrRegistrationNotFound
}
func (stub *accountRegistrationStub) Add(_ context.Context, request account.AddRegistration, key string) (account.Registration, error) {
	stub.added++
	if key == "" {
		return account.Registration{}, account.ErrInvalidRegistration
	}
	return account.Registration{ID: "acct-one", Venue: request.Venue, DisplayName: request.DisplayName, ValidatedAt: time.Now(), CreatedAt: time.Now()}, stub.addErr
}
func (stub *accountRegistrationStub) Remove(context.Context, domain.AccountID) error {
	stub.removed++
	return nil
}
func TestAccountRegistrationHTTPHasOnlyAddViewDelete(t *testing.T) {
	stub := &accountRegistrationStub{}
	handler, err := NewInternalHandler(InternalOptions{Accounts: stub, Authority: &fakeInternalAuthority{}, Authenticate: func(r *http.Request) error {
		if r.Header.Get("Authorization") != "Bearer offline-service" {
			return errors.New("login required")
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	const body = `{"venue":"binance","displayName":"Personal","credentialBundle":{"schemaVersion":1,"venue":"binance","authKind":"api_key_secret","metadata":{},"secrets":{"apiKey":"offline-canary-key","apiSecret":"offline-canary-secret"}}}`
	send := func(method, path, payload string, authenticated bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(payload))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", "new-account")
		if authenticated {
			r.Header.Set("Authorization", "Bearer offline-service")
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := send("POST", "/internal/v1/accounts", body, false); w.Code != 401 || stub.added != 0 {
		t.Fatal("unauthenticated registration reached service")
	}
	w := send("POST", "/internal/v1/accounts", body, true)
	if w.Code != 201 || !strings.Contains(w.Body.String(), `"routeId":"spot"`) || strings.Contains(w.Body.String(), "offline-canary") {
		t.Fatalf("registration response: %d %s", w.Code, w.Body.String())
	}
	if w := send("DELETE", "/internal/v1/accounts/acct-one", "", true); w.Code != 204 || stub.removed != 1 {
		t.Fatal("deletion still requires blockers or a revision")
	}
	for _, path := range []string{"/internal/v1/accounts/acct-one/disable", "/internal/v1/accounts/acct-one/rotate", "/internal/v1/accounts/desired/acct-one/operations/one"} {
		if w := send("POST", path, "{}", true); w.Code < 400 {
			t.Fatalf("removed workflow still mounted: %s", path)
		}
	}
	stub.addErr = exchange.NewError(exchange.KindPermission, exchange.ErrorDetails{Code: "-2015", SafeMessage: "Invalid API-key, IP, or permissions for action."})
	w = send("POST", "/internal/v1/accounts", body, true)
	if w.Code != 502 || !strings.Contains(w.Body.String(), "-2015") || !strings.Contains(w.Body.String(), "permissions for action") || strings.Contains(w.Body.String(), "offline-canary") {
		t.Fatalf("venue failure not preserved safely: %s", w.Body.String())
	}
}
