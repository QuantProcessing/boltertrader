package workapi

import (
	"bytes"
	"crypto/sha256"
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
	if regRec.Code != http.StatusCreated {
		t.Fatalf("code=%d want 201", regRec.Code)
	}
}

func TestWorkerRegistrationAndDrainUpdateReadiness(t *testing.T) {
	var readiness []bool
	handler, err := New(Options{
		TokenDigest: sha256.Sum256([]byte("test-worker-token")), Mode: "test", ContractVersion: ContractVersion,
		OnReadiness: func(ready bool) { readiness = append(readiness, ready) },
	})
	if err != nil {
		t.Fatal(err)
	}
	registration := httptest.NewRequest(http.MethodPost, "/internal/v1/funding-work/workers/registrations", bytes.NewBufferString(`{"workerId":"worker-1","instanceId":"instance-1","productId":"funding","mode":"test","contractVersion":"boltertrader-product-platform-upgrade/v4"}`))
	registration.Header.Set("Authorization", "Bearer test-worker-token")
	registrationResponse := httptest.NewRecorder()
	handler.ServeHTTP(registrationResponse, registration)
	if registrationResponse.Code != http.StatusCreated {
		t.Fatalf("registration=%d body=%s", registrationResponse.Code, registrationResponse.Body.String())
	}
	drain := httptest.NewRequest(http.MethodPost, "/internal/v1/funding-work/workers/worker-1/drain", bytes.NewBufferString(`{"workerGeneration":1,"instanceId":"instance-1","idempotencyKey":"drain-1","reason":"shutdown"}`))
	drain.Header.Set("Authorization", "Bearer test-worker-token")
	drainResponse := httptest.NewRecorder()
	handler.ServeHTTP(drainResponse, drain)
	if drainResponse.Code != http.StatusOK {
		t.Fatalf("drain=%d body=%s", drainResponse.Code, drainResponse.Body.String())
	}
	if len(readiness) != 2 || !readiness[0] || readiness[1] {
		t.Fatalf("readiness transitions=%v", readiness)
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
