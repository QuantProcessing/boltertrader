package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/secretstore"

	platformaccounts "github.com/QuantProcessing/boltertrader/backend/internal/platform/accounts"
)

type routeSecretWriter struct{ ref string }

func (writer routeSecretWriter) Put(context.Context, secretstore.OperationID, []byte) (string, error) {
	return writer.ref, nil
}

type routeAuditWriter struct{}

func (routeAuditWriter) Append(context.Context, platformaccounts.AuditRecord) error { return nil }

func TestAccountRoutesAreStrictCASFencedAndRedacted(t *testing.T) {
	service, err := platformaccounts.New(platformaccounts.Options{Store: platformaccounts.NewMemoryStore(), Secrets: routeSecretWriter{ref: "vault://must-not-return"}, Audit: routeAuditWriter{}, ID: func() string { return "acct_http" }})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	err = RegisterAccountRoutes(mux, AccountRoutesOptions{Service: service, Authenticate: func(*http.Request) (string, error) { return "browser-session", nil }, AuthorizeMutation: func(*http.Request) error { return nil }, Blockers: func(context.Context, string) (platformaccounts.Blockers, error) {
		return platformaccounts.Blockers{}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}

	unknown := httptest.NewRequest(http.MethodPost, "/api/v1/accounts", strings.NewReader(`{"venue":"binance","venueEnvironment":"testnet","displayName":"Primary","productIds":["funding"],"secretMaterial":{"apiKey":"canary"},"extra":true}`))
	unknown.Header.Set("Idempotency-Key", "create-1")
	unknownRec := httptest.NewRecorder()
	mux.ServeHTTP(unknownRec, unknown)
	if unknownRec.Code != http.StatusBadRequest {
		t.Fatalf("unknown status=%d body=%s", unknownRec.Code, unknownRec.Body.String())
	}

	create := httptest.NewRequest(http.MethodPost, "/api/v1/accounts", strings.NewReader(`{"venue":"binance","venueEnvironment":"testnet","displayName":"Primary","productIds":["funding"],"secretMaterial":{"apiKey":"credential-canary"}}`))
	create.Header.Set("Idempotency-Key", "create-1")
	created := httptest.NewRecorder()
	mux.ServeHTTP(created, create)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	if strings.Contains(created.Body.String(), "credential-canary") || strings.Contains(created.Body.String(), "vault://") || strings.Contains(created.Body.String(), "secretRef") {
		t.Fatalf("create leaked secret: %s", created.Body.String())
	}
	if created.Header().Get("ETag") != AccountETag(1) {
		t.Fatalf("etag=%q", created.Header().Get("ETag"))
	}

	rotate := httptest.NewRequest(http.MethodPost, "/api/v1/accounts/acct_http/rotate", strings.NewReader(`{"secretMaterial":{"apiKey":"replacement"}}`))
	rotate.Header.Set("If-Match", AccountETag(99))
	rotated := httptest.NewRecorder()
	mux.ServeHTTP(rotated, rotate)
	if rotated.Code != http.StatusConflict || !strings.Contains(rotated.Body.String(), "DESIRED_STATE_REVISION_CONFLICT") {
		t.Fatalf("rotate status=%d body=%s", rotated.Code, rotated.Body.String())
	}
}

func TestAccountRoutesRequireAuthHooks(t *testing.T) {
	service, _ := platformaccounts.New(platformaccounts.Options{Store: platformaccounts.NewMemoryStore(), Secrets: routeSecretWriter{ref: "ref"}, Audit: routeAuditWriter{}})
	mux := http.NewServeMux()
	_ = RegisterAccountRoutes(mux, AccountRoutesOptions{Service: service, Authenticate: func(*http.Request) (string, error) { return "", errors.New("denied") }, AuthorizeMutation: func(*http.Request) error { return nil }, Blockers: func(context.Context, string) (platformaccounts.Blockers, error) {
		return platformaccounts.Blockers{}, nil
	}})
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/accounts", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", response.Code)
	}
}

func TestTypedAccountCreateReplaysIdempotentlyAndRejectsChangedBody(t *testing.T) {
	service, _ := platformaccounts.New(platformaccounts.Options{Store: platformaccounts.NewMemoryStore(), Secrets: routeSecretWriter{ref: "opaque-ref"}, Audit: routeAuditWriter{}, ID: func() string { return "acct_typed" }})
	mux := http.NewServeMux()
	_ = RegisterAccountRoutes(mux, AccountRoutesOptions{Service: service, Authenticate: func(*http.Request) (string, error) { return "principal", nil }, AuthorizeMutation: func(*http.Request) error { return nil }, Blockers: func(context.Context, string) (platformaccounts.Blockers, error) {
		return platformaccounts.Blockers{}, nil
	}})
	body := `{"venue":"binance","displayName":"Primary","productPolicy":["manual"],"routes":[{"routeId":"perp-usdt","product":"perp","settlementAsset":"USDT","routeProfileId":"binance.perp.usdt.demo.v1","requestedState":"active"}],"credentialBundle":{"schemaVersion":1,"venue":"binance","authKind":"api_key_secret","metadata":{},"secrets":{"apiKey":"key-canary","apiSecret":"secret-canary"}}}`
	request := func(payload string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/accounts", strings.NewReader(payload))
		req.Header.Set("Idempotency-Key", " typed-create ")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, req)
		return response
	}
	for index := 0; index < 2; index++ {
		response := request(body)
		if response.Code != http.StatusCreated || strings.Contains(response.Body.String(), "canary") || strings.Contains(response.Body.String(), "opaque-ref") {
			t.Fatalf("replay %d status=%d body=%s", index, response.Code, response.Body.String())
		}
	}
	changed := strings.Replace(body, "Primary", "Changed", 1)
	response := request(changed)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "IDEMPOTENCY_CONFLICT") {
		t.Fatalf("changed status=%d body=%s", response.Code, response.Body.String())
	}
}
