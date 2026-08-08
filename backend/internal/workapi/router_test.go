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
	handler, err := New(Options{TokenDigest: sha256.Sum256([]byte("test-worker-token")), Mode: "test", ContractVersion: ContractVersion, WorkerValidator: acceptingWorkerValidator{}})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct{ path, body string }{
		{path: "/internal/v1/funding-work/leases/lease-1/command-intents", body: `{"workerId":"worker-a","workerGeneration":1,"workItemId":"work-1","workRevision":1,"leaseGeneration":1,"idempotencyKey":"intent-1","intentKind":"funding.open","factsCursor":"cursor-1","accountFences":{},"payload":{"unexpected":true}}`},
		{path: "/internal/v1/funding-work/leases/lease-1/complete", body: `{"workerId":"worker-a","workerGeneration":1,"workItemId":"work-1","workRevision":1,"leaseGeneration":1,"idempotencyKey":"complete-1","factsCursor":"cursor-1","result":{"unexpected":true}}`},
		{path: "/internal/v1/funding-work/leases/lease-1/fail", body: `{"workerId":"worker-a","workerGeneration":1,"workItemId":"work-1","workRevision":1,"leaseGeneration":1,"idempotencyKey":"fail-1","factsCursor":"cursor-1","errorCode":"FAILED","retryable":false,"details":{"unexpected":true}}`},
	}
	for _, test := range tests {
		request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
		request.Header.Set("Authorization", "Bearer test-worker-token")
		switch {
		case strings.Contains(test.body, `"idempotencyKey":"intent-1"`):
			request.Header.Set("Idempotency-Key", "intent-1")
		case strings.Contains(test.body, `"idempotencyKey":"complete-1"`):
			request.Header.Set("Idempotency-Key", "complete-1")
		case strings.Contains(test.body, `"idempotencyKey":"fail-1"`):
			request.Header.Set("Idempotency-Key", "fail-1")
		}
		request.Header.Set("X-Worker-Id", "worker-a")
		request.Header.Set("X-Worker-Generation", "1")
		request.Header.Set("X-Worker-Instance-Id", "instance-a")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "VALIDATION_ERROR") {
			t.Fatalf("%s status=%d body=%s", test.path, response.Code, response.Body.String())
		}
	}
}

func TestWorkRoutesRequireIdempotencyHeadersAndWorkerFencingHeaders(t *testing.T) {
	handler := newTestHandler(t)
	register := httptest.NewRequest(http.MethodPost, "/internal/v1/funding-work/workers/registrations", strings.NewReader(`{"workerId":"worker-a","instanceId":"instance-a","productId":"funding","mode":"test","contractVersion":"boltertrader-product-platform-upgrade/v4"}`))
	register.Header.Set("Authorization", "Bearer test-worker-token")
	register.Header.Set("Idempotency-Key", "registration-1")
	registered := httptest.NewRecorder()
	handler.ServeHTTP(registered, register)
	if registered.Code != http.StatusCreated {
		t.Fatalf("registration status=%d body=%s", registered.Code, registered.Body.String())
	}

	tests := []struct {
		name    string
		method  string
		path    string
		body    string
		headers map[string]string
		want    int
		code    string
	}{
		{name: "registration requires idempotency", method: http.MethodPost, path: "/internal/v1/funding-work/workers/registrations", body: `{"workerId":"worker-b","instanceId":"instance-b","productId":"funding","mode":"test","contractVersion":"boltertrader-product-platform-upgrade/v4"}`, want: http.StatusBadRequest, code: "IDEMPOTENCY_REQUIRED"},
		{name: "heartbeat requires worker headers", method: http.MethodPut, path: "/internal/v1/funding-work/workers/worker-a/heartbeat", body: `{"workerGeneration":1,"instanceId":"instance-a","state":"ready","activeLeaseIds":[],"lastAppliedFactsCursor":"cursor-1"}`, want: http.StatusBadRequest, code: "WORKER_FENCE_REQUIRED"},
		{name: "readiness requires worker headers", method: http.MethodGet, path: "/internal/v1/funding-work/workers/worker-a/readiness", want: http.StatusBadRequest, code: "WORKER_FENCE_REQUIRED"},
		{name: "drain rejects idempotency mismatch", method: http.MethodPost, path: "/internal/v1/funding-work/workers/worker-a/drain", body: `{"workerGeneration":1,"instanceId":"instance-a","idempotencyKey":"drain-body","reason":"shutdown"}`, headers: map[string]string{"Idempotency-Key": "drain-header", "X-Worker-Id": "worker-a", "X-Worker-Generation": "1", "X-Worker-Instance-Id": "instance-a"}, want: http.StatusConflict, code: "IDEMPOTENCY_CONFLICT"},
		{name: "claim requires maxItems one", method: http.MethodPost, path: "/internal/v1/funding-work/claims", body: `{"workerId":"worker-a","workerGeneration":1,"instanceId":"instance-a","idempotencyKey":"claim-1","afterCursor":"","waitSeconds":0,"maxItems":2}`, headers: map[string]string{"Idempotency-Key": "claim-1", "X-Worker-Id": "worker-a", "X-Worker-Generation": "1", "X-Worker-Instance-Id": "instance-a"}, want: http.StatusBadRequest, code: "VALIDATION_ERROR"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			request.Header.Set("Authorization", "Bearer test-worker-token")
			for key, value := range test.headers {
				request.Header.Set(key, value)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.want || !strings.Contains(response.Body.String(), test.code) {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.want, response.Body.String())
			}
		})
	}
}

func TestWorkBearerAuthorizationUsesStrictSyntax(t *testing.T) {
	handler := newTestHandler(t)
	for _, auth := range []string{"Bearer  test-worker-token", "Bearer\ttest-worker-token", "Bearer test-worker-token trailing", " bearer test-worker-token"} {
		request := httptest.NewRequest(http.MethodGet, "/internal/v1/funding-work/status", nil)
		request.Header.Set("Authorization", auth)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized || strings.Contains(response.Body.String(), "test-worker-token") {
			t.Fatalf("auth %q status=%d body=%s", auth, response.Code, response.Body.String())
		}
	}
}

type rejectingWorkerValidator struct{}

func (rejectingWorkerValidator) ValidateWorker(string, int, string) error {
	return errTestWorkerStale{}
}

type errTestWorkerStale struct{}

func (errTestWorkerStale) Error() string { return "stale worker" }

func TestWorkAPIMapsWorkerValidatorFailureToFrozenStaleError(t *testing.T) {
	handler, err := New(Options{TokenDigest: sha256.Sum256([]byte("test-worker-token")), Mode: "test", ContractVersion: ContractVersion, WorkerValidator: rejectingWorkerValidator{}})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/internal/v1/funding-work/workers/worker-a/readiness", nil)
	request.Header.Set("Authorization", "Bearer test-worker-token")
	request.Header.Set("X-Worker-Id", "worker-a")
	request.Header.Set("X-Worker-Generation", "1")
	request.Header.Set("X-Worker-Instance-Id", "instance-a")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "WORKER_GENERATION_STALE") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

type acceptingWorkerValidator struct{}

func (acceptingWorkerValidator) ValidateWorker(string, int, string) error { return nil }
