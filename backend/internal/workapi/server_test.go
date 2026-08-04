package workapi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	frozencontract "github.com/QuantProcessing/boltertrader/backend/internal/contract"
)

func TestWorkAuthAndStrictValidation(t *testing.T) {
	srv := newTestHandler(t)

	unauthorized := httptest.NewRequest(http.MethodGet, "/internal/v1/funding-work/status", nil)
	unauthorizedRec := httptest.NewRecorder()
	srv.ServeHTTP(unauthorizedRec, unauthorized)
	if unauthorizedRec.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d want 401", unauthorizedRec.Code)
	}

	authorized := httptest.NewRequest(http.MethodGet, "/internal/v1/funding-work/status", nil)
	authorized.Header.Set("Authorization", "Bearer test-worker-token")
	authorizedRec := httptest.NewRecorder()
	srv.ServeHTTP(authorizedRec, authorized)
	if authorizedRec.Code != http.StatusOK {
		t.Fatalf("code=%d want 200", authorizedRec.Code)
	}

	reg := httptest.NewRequest(http.MethodPost, "/internal/v1/funding-work/workers/registrations", bytes.NewBufferString(`{"workerId":"worker-1","instanceId":"instance-1","productId":"funding","mode":"test","contractVersion":"boltertrader-product-platform-upgrade/v4"}`))
	reg.Header.Set("Authorization", "Bearer test-worker-token")
	reg.Header.Set("Idempotency-Key", "register-worker-1")
	regRec := httptest.NewRecorder()
	srv.ServeHTTP(regRec, reg)
	if regRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d want 503", regRec.Code)
	}
}

func TestWorkRoutesMatchFrozenContract(t *testing.T) {
	contract, err := frozencontract.Load()
	if err != nil {
		t.Fatal(err)
	}
	var routes []string
	for _, listener := range contract.Listeners {
		if listener.ID == "platform-funding-work" {
			routes = append(routes, listener.Routes...)
		}
	}
	if len(routes) == 0 {
		t.Fatal("no work routes found")
	}
	if routes[0] != "GET /internal/v1/funding-work/status" {
		t.Fatalf("unexpected first route %q", routes[0])
	}
}
