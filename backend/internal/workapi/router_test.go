package workapi

import (
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestHandler(t *testing.T) http.Handler {
	t.Helper()
	handler, err := New(Options{TokenDigest: sha256.Sum256([]byte("test-worker-token")), Mode: "test", ContractVersion: ContractVersion})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return handler
}

func TestWorkListenerRequiresScopedBearerAndMountsOnlyWorkRoutes(t *testing.T) {
	handler := newTestHandler(t)
	tests := []struct {
		name string
		path string
		auth string
		want int
		code string
	}{
		{name: "missing", path: "/internal/v1/funding-work/status", want: http.StatusUnauthorized, code: "WORKER_AUTH_REQUIRED"},
		{name: "wrong", path: "/internal/v1/funding-work/status", auth: "Bearer live-worker-token", want: http.StatusUnauthorized, code: "WORKER_AUTH_REQUIRED"},
		{name: "valid", path: "/internal/v1/funding-work/status", auth: "Bearer test-worker-token", want: http.StatusOK},
		{name: "browser absent", path: "/api/v1/system/status", auth: "Bearer test-worker-token", want: http.StatusNotFound},
		{name: "gateway absent", path: "/internal/v1/accounts/desired", auth: "Bearer test-worker-token", want: http.StatusNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			request.Header.Set("Authorization", test.auth)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status = %d want %d body=%s", response.Code, test.want, response.Body.String())
			}
			if test.code != "" && !strings.Contains(response.Body.String(), test.code) {
				t.Fatalf("body missing %q: %s", test.code, response.Body.String())
			}
		})
	}
}

func TestRegistrationSkeletonRejectsUnknownFieldsBeforeServiceHandoff(t *testing.T) {
	handler := newTestHandler(t)
	tests := []struct {
		name string
		body string
		want int
	}{
		{name: "unknown field", body: `{"workerId":"worker-a","instanceId":"instance-a","productId":"funding","mode":"test","contractVersion":"boltertrader-product-platform-upgrade/v4","token":"must-not-be-accepted"}`, want: http.StatusBadRequest},
		{name: "wrong mode", body: `{"workerId":"worker-a","instanceId":"instance-a","productId":"funding","mode":"live","contractVersion":"boltertrader-product-platform-upgrade/v4"}`, want: http.StatusForbidden},
		{name: "valid registration", body: `{"workerId":"worker-a","instanceId":"instance-a","productId":"funding","mode":"test","contractVersion":"boltertrader-product-platform-upgrade/v4"}`, want: http.StatusCreated},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/internal/v1/funding-work/workers/registrations", strings.NewReader(test.body))
			request.Header.Set("Authorization", "Bearer test-worker-token")
			request.Header.Set("Idempotency-Key", "registration-1")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status = %d want %d body=%s", response.Code, test.want, response.Body.String())
			}
		})
	}
}

func TestWorkerTerminalAndIntentPayloadsRejectUnknownNestedFields(t *testing.T) {
	handler := newTestHandler(t)
	tests := []struct{ path, body string }{
		{path: "/internal/v1/funding-work/leases/lease-1/command-intents", body: `{"workerId":"worker-a","workerGeneration":1,"workItemId":"work-1","workRevision":1,"leaseGeneration":1,"idempotencyKey":"intent-1","intentKind":"funding.open","factsCursor":"cursor-1","accountFences":{},"payload":{"unexpected":true}}`},
		{path: "/internal/v1/funding-work/leases/lease-1/complete", body: `{"workerId":"worker-a","workerGeneration":1,"workItemId":"work-1","workRevision":1,"leaseGeneration":1,"idempotencyKey":"complete-1","factsCursor":"cursor-1","result":{"unexpected":true}}`},
		{path: "/internal/v1/funding-work/leases/lease-1/fail", body: `{"workerId":"worker-a","workerGeneration":1,"workItemId":"work-1","workRevision":1,"leaseGeneration":1,"idempotencyKey":"fail-1","factsCursor":"cursor-1","errorCode":"FAILED","retryable":false,"details":{"unexpected":true}}`},
	}
	for _, test := range tests {
		request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
		request.Header.Set("Authorization", "Bearer test-worker-token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "VALIDATION_ERROR") {
			t.Fatalf("%s status=%d body=%s", test.path, response.Code, response.Body.String())
		}
	}
}
