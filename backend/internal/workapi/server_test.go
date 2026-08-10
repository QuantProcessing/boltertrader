package workapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	frozencontract "github.com/QuantProcessing/boltertrader/backend/internal/contract"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/work"
)

type testWriteAuthority struct{ ready bool }

func (authority testWriteAuthority) Ready(context.Context) (bool, string) {
	if authority.ready {
		return true, "ready"
	}
	return false, "writer_role_unavailable"
}

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

func TestWorkerKeyUsesPostgresSafeModeScope(t *testing.T) {
	testKey := (&handler{mode: "test"}).workerKey(" worker-a ")
	liveKey := (&handler{mode: "live"}).workerKey("worker-a")
	if testKey != "test:worker-a" || liveKey != "live:worker-a" {
		t.Fatalf("scoped worker keys test=%q live=%q", testKey, liveKey)
	}
	if strings.ContainsRune(testKey, '\x00') || strings.ContainsRune(liveKey, '\x00') || testKey == liveKey {
		t.Fatalf("worker keys are not PostgreSQL-safe and mode-scoped: test=%q live=%q", testKey, liveKey)
	}
}

func TestCanonicalWriteGateRejectsBeforeWorkDispatch(t *testing.T) {
	handler, err := New(Options{
		TokenDigest: sha256.Sum256([]byte("test-worker-token")), Mode: "test", ContractVersion: ContractVersion,
		WriteAuthority: testWriteAuthority{},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/funding-work/workers/registrations", strings.NewReader(`not-json`))
	request.Header.Set("Authorization", "Bearer test-worker-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "CANONICAL_WRITES_NOT_READY") {
		t.Fatalf("response=%d body=%s", response.Code, response.Body.String())
	}
}

func TestWorkerHeartbeatAndDrainUpdateReadiness(t *testing.T) {
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
	registration.Header.Set("Idempotency-Key", "registration-1")
	registrationResponse := httptest.NewRecorder()
	handler.ServeHTTP(registrationResponse, registration)
	if registrationResponse.Code != http.StatusCreated {
		t.Fatalf("registration=%d body=%s", registrationResponse.Code, registrationResponse.Body.String())
	}
	if len(readiness) != 0 {
		t.Fatalf("registration reported worker ready before heartbeat: %v", readiness)
	}
	heartbeat := httptest.NewRequest(http.MethodPut, "/internal/v1/funding-work/workers/worker-1/heartbeat", bytes.NewBufferString(`{"workerGeneration":1,"instanceId":"instance-1","state":"ready","activeLeaseIds":[],"lastAppliedFactsCursor":""}`))
	heartbeat.Header.Set("Authorization", "Bearer test-worker-token")
	heartbeat.Header.Set("X-Worker-Id", "worker-1")
	heartbeat.Header.Set("X-Worker-Generation", "1")
	heartbeat.Header.Set("X-Worker-Instance-Id", "instance-1")
	heartbeatResponse := httptest.NewRecorder()
	handler.ServeHTTP(heartbeatResponse, heartbeat)
	if heartbeatResponse.Code != http.StatusOK {
		t.Fatalf("heartbeat=%d body=%s", heartbeatResponse.Code, heartbeatResponse.Body.String())
	}
	drain := httptest.NewRequest(http.MethodPost, "/internal/v1/funding-work/workers/worker-1/drain", bytes.NewBufferString(`{"workerGeneration":1,"instanceId":"instance-1","idempotencyKey":"drain-1","reason":"shutdown"}`))
	drain.Header.Set("Authorization", "Bearer test-worker-token")
	drain.Header.Set("Idempotency-Key", "drain-1")
	drain.Header.Set("X-Worker-Id", "worker-1")
	drain.Header.Set("X-Worker-Generation", "1")
	drain.Header.Set("X-Worker-Instance-Id", "instance-1")
	drainResponse := httptest.NewRecorder()
	handler.ServeHTTP(drainResponse, drain)
	if drainResponse.Code != http.StatusAccepted {
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

func TestCommandIntentUsesFrozenStatusAndTypedLeaseErrors(t *testing.T) {
	srv := newTestHandler(t)
	claim := registerAndClaim(t, srv)
	validPayload := `{"previewId":"preview-1","opportunityId":"opportunity-1","longAccountId":"acct-long","shortAccountId":"acct-short","canonicalMarketId":"BTC-USDT-PERP","notionalPerLeg":"50","maxSlippageBps":"10","holdThroughSettlement":true}`

	staleBody := `{"workerId":"worker-a","workerGeneration":1,"workItemId":"` + claim.WorkItemID + `","workRevision":` + strconv.Itoa(claim.WorkRevision) + `,"leaseGeneration":999,"idempotencyKey":"intent-stale-1","intentKind":"funding.open","factsCursor":"` + claim.FactsCursor + `","accountFences":` + string(claim.AccountFences) + `,"payload":` + validPayload + `}`
	stale := httptest.NewRequest(http.MethodPost, "/internal/v1/funding-work/leases/"+claim.LeaseID+"/command-intents", bytes.NewBufferString(staleBody))
	setWorkerHeaders(stale, "intent-stale-1")
	staleResponse := httptest.NewRecorder()
	srv.ServeHTTP(staleResponse, stale)
	if staleResponse.Code != http.StatusConflict || !strings.Contains(staleResponse.Body.String(), "WORK_LEASE_STALE") {
		t.Fatalf("stale status=%d body=%s", staleResponse.Code, staleResponse.Body.String())
	}

	body := `{"workerId":"worker-a","workerGeneration":1,"workItemId":"` + claim.WorkItemID + `","workRevision":` + strconv.Itoa(claim.WorkRevision) + `,"leaseGeneration":` + strconv.Itoa(claim.LeaseGeneration) + `,"idempotencyKey":"intent-ok-1","intentKind":"funding.open","factsCursor":"` + claim.FactsCursor + `","accountFences":` + string(claim.AccountFences) + `,"payload":` + validPayload + `}`
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/funding-work/leases/"+claim.LeaseID+"/command-intents", bytes.NewBufferString(body))
	setWorkerHeaders(request, "intent-ok-1")
	response := httptest.NewRecorder()
	srv.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("intent status=%d want 200 body=%s", response.Code, response.Body.String())
	}
}

func TestWorkServiceErrorsMapToFrozenHTTPContract(t *testing.T) {
	tests := []struct {
		err       error
		status    int
		code      string
		retryable bool
	}{
		{work.ErrLeaseExpired, http.StatusConflict, "WORK_LEASE_EXPIRED", false},
		{work.ErrLeaseStale, http.StatusConflict, "WORK_LEASE_STALE", false},
		{work.ErrWorkRevisionConflict, http.StatusConflict, "WORK_REVISION_CONFLICT", true},
		{work.ErrAuthorizationStale, http.StatusConflict, "WORK_AUTHORIZATION_STALE", true},
		{work.ErrAttemptsExhausted, http.StatusConflict, "WORK_ATTEMPTS_EXHAUSTED", false},
		{work.ErrIdempotencyConflict, http.StatusConflict, "IDEMPOTENCY_CONFLICT", false},
		{work.ErrWorkerStale, http.StatusConflict, "WORKER_GENERATION_STALE", false},
		{work.ErrWorkerNotReady, http.StatusServiceUnavailable, "WORKER_NOT_READY", true},
		{work.ErrDependencyUnavailable, http.StatusServiceUnavailable, "WORKER_NOT_READY", true},
		{work.ErrWorkNotFound, http.StatusNotFound, "WORK_NOT_FOUND", false},
	}
	for _, test := range tests {
		t.Run(test.code, func(t *testing.T) {
			status, code, retryable := workHTTPError(fmt.Errorf("store operation: %w", test.err))
			if status != test.status || code != test.code || retryable != test.retryable {
				t.Fatalf("mapping=(%d,%q,%t) want (%d,%q,%t)", status, code, retryable, test.status, test.code, test.retryable)
			}
		})
	}
}

func TestWorkAPIScopesWorkerIdentityByMode(t *testing.T) {
	now := time.Date(2026, 8, 5, 2, 30, 0, 0, time.UTC)
	fences := json.RawMessage(`[{"accountId":"account-long","desiredStateRevision":1,"runtimeGeneration":1,"credentialVersion":1},{"accountId":"account-short","desiredStateRevision":1,"runtimeGeneration":1,"credentialVersion":1}]`)
	store := work.NewMemoryStoreWithSnapshot([]work.WorkItem{
		{
			WorkItemID:    "work-test",
			WorkKind:      "open",
			LineageID:     "lineage-test",
			WorkRevision:  1,
			ProductID:     "funding",
			Mode:          "test",
			Status:        "open",
			FactsCursor:   "cursor-test",
			AccountFences: fences,
			Payload:       json.RawMessage(`{}`),
			MaxAttempts:   5,
			NotBefore:     now,
			CreatedAt:     now,
			UpdatedAt:     now,
		},
		{
			WorkItemID:    "work-live",
			WorkKind:      "open",
			LineageID:     "lineage-live",
			WorkRevision:  1,
			ProductID:     "funding",
			Mode:          "live",
			Status:        "open",
			FactsCursor:   "cursor-live",
			AccountFences: fences,
			Payload:       json.RawMessage(`{}`),
			MaxAttempts:   5,
			NotBefore:     now,
			CreatedAt:     now,
			UpdatedAt:     now,
		},
	}, nil)
	service := work.NewWithStore(ContractVersion, 10*time.Second, store)
	service.SetClock(func() time.Time { return now })
	testHandler, err := New(Options{TokenDigest: sha256.Sum256([]byte("test-worker-token")), Mode: "test", ContractVersion: ContractVersion, Service: service})
	if err != nil {
		t.Fatal(err)
	}
	liveHandler, err := New(Options{TokenDigest: sha256.Sum256([]byte("live-worker-token")), Mode: "live", ContractVersion: ContractVersion, Service: service})
	if err != nil {
		t.Fatal(err)
	}

	type registrationEnvelope struct {
		Data work.Registration `json:"data"`
	}
	register := func(handler http.Handler, auth, mode, idempotency string) {
		request := httptest.NewRequest(http.MethodPost, "/internal/v1/funding-work/workers/registrations", strings.NewReader(`{"workerId":"worker-a","instanceId":"instance-a","productId":"funding","mode":"`+mode+`","contractVersion":"boltertrader-product-platform-upgrade/v4"}`))
		request.Header.Set("Authorization", auth)
		request.Header.Set("Idempotency-Key", idempotency)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusCreated {
			t.Fatalf("%s registration status=%d body=%s", mode, response.Code, response.Body.String())
		}
		var envelope registrationEnvelope
		if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Data.WorkerID != "worker-a" || envelope.Data.Mode != mode || envelope.Data.Generation != 1 {
			t.Fatalf("%s registration=%+v", mode, envelope.Data)
		}
		heartbeat := httptest.NewRequest(http.MethodPut, "/internal/v1/funding-work/workers/worker-a/heartbeat", strings.NewReader(`{"workerGeneration":1,"instanceId":"instance-a","state":"ready","activeLeaseIds":[],"lastAppliedFactsCursor":""}`))
		heartbeat.Header.Set("Authorization", auth)
		heartbeat.Header.Set("X-Worker-Id", "worker-a")
		heartbeat.Header.Set("X-Worker-Generation", "1")
		heartbeat.Header.Set("X-Worker-Instance-Id", "instance-a")
		heartbeatResponse := httptest.NewRecorder()
		handler.ServeHTTP(heartbeatResponse, heartbeat)
		if heartbeatResponse.Code != http.StatusOK {
			t.Fatalf("%s heartbeat status=%d body=%s", mode, heartbeatResponse.Code, heartbeatResponse.Body.String())
		}
	}
	register(testHandler, "Bearer test-worker-token", "test", "register-test")
	register(liveHandler, "Bearer live-worker-token", "live", "register-live")

	type claimEnvelope struct {
		Data struct {
			WorkItem *work.WorkItem `json:"workItem"`
			Lease    *work.Lease    `json:"lease"`
		} `json:"data"`
	}
	claim := func(handler http.Handler, auth, mode, idempotency string) {
		request := httptest.NewRequest(http.MethodPost, "/internal/v1/funding-work/claims", strings.NewReader(`{"workerId":"worker-a","workerGeneration":1,"instanceId":"instance-a","idempotencyKey":"`+idempotency+`","afterCursor":"","waitSeconds":0,"maxItems":1}`))
		request.Header.Set("Authorization", auth)
		request.Header.Set("Idempotency-Key", idempotency)
		request.Header.Set("X-Worker-Id", "worker-a")
		request.Header.Set("X-Worker-Generation", "1")
		request.Header.Set("X-Worker-Instance-Id", "instance-a")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s claim status=%d body=%s", mode, response.Code, response.Body.String())
		}
		var envelope claimEnvelope
		if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Data.WorkItem == nil || envelope.Data.Lease == nil || envelope.Data.WorkItem.Mode != mode {
			t.Fatalf("%s claim=%+v", mode, envelope.Data)
		}
	}
	claim(testHandler, "Bearer test-worker-token", "test", "claim-test")
	claim(liveHandler, "Bearer live-worker-token", "live", "claim-live")
}

type testClaim struct {
	WorkItemID      string          `json:"workItemId"`
	WorkRevision    int             `json:"workRevision"`
	LeaseID         string          `json:"leaseId"`
	LeaseGeneration int             `json:"leaseGeneration"`
	FactsCursor     string          `json:"factsCursor"`
	AccountFences   json.RawMessage `json:"accountFences"`
}

func registerAndClaim(t *testing.T, srv http.Handler) testClaim {
	t.Helper()
	registration := httptest.NewRequest(http.MethodPost, "/internal/v1/funding-work/workers/registrations", bytes.NewBufferString(`{"workerId":"worker-a","instanceId":"instance-a","productId":"funding","mode":"test","contractVersion":"boltertrader-product-platform-upgrade/v4"}`))
	registration.Header.Set("Authorization", "Bearer test-worker-token")
	registration.Header.Set("Idempotency-Key", "registration-a")
	registrationResponse := httptest.NewRecorder()
	srv.ServeHTTP(registrationResponse, registration)
	if registrationResponse.Code != http.StatusCreated {
		t.Fatalf("registration=%d body=%s", registrationResponse.Code, registrationResponse.Body.String())
	}
	heartbeat := httptest.NewRequest(http.MethodPut, "/internal/v1/funding-work/workers/worker-a/heartbeat", bytes.NewBufferString(`{"workerGeneration":1,"instanceId":"instance-a","state":"ready","activeLeaseIds":[],"lastAppliedFactsCursor":""}`))
	heartbeat.Header.Set("Authorization", "Bearer test-worker-token")
	heartbeat.Header.Set("X-Worker-Id", "worker-a")
	heartbeat.Header.Set("X-Worker-Generation", "1")
	heartbeat.Header.Set("X-Worker-Instance-Id", "instance-a")
	heartbeatResponse := httptest.NewRecorder()
	srv.ServeHTTP(heartbeatResponse, heartbeat)
	if heartbeatResponse.Code != http.StatusOK {
		t.Fatalf("heartbeat=%d body=%s", heartbeatResponse.Code, heartbeatResponse.Body.String())
	}
	claimRequest := httptest.NewRequest(http.MethodPost, "/internal/v1/funding-work/claims", bytes.NewBufferString(`{"workerId":"worker-a","workerGeneration":1,"instanceId":"instance-a","idempotencyKey":"claim-a","afterCursor":"","waitSeconds":0,"maxItems":1}`))
	setWorkerHeaders(claimRequest, "claim-a")
	claimResponse := httptest.NewRecorder()
	srv.ServeHTTP(claimResponse, claimRequest)
	if claimResponse.Code != http.StatusOK {
		t.Fatalf("claim=%d body=%s", claimResponse.Code, claimResponse.Body.String())
	}
	var envelope struct {
		Data struct {
			WorkItem *work.WorkItem `json:"workItem"`
			Lease    *work.Lease    `json:"lease"`
		} `json:"data"`
	}
	if err := json.NewDecoder(claimResponse.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.WorkItem == nil || envelope.Data.Lease == nil {
		t.Fatalf("claim data=%+v", envelope.Data)
	}
	return testClaim{WorkItemID: envelope.Data.WorkItem.WorkItemID, WorkRevision: envelope.Data.WorkItem.WorkRevision, LeaseID: envelope.Data.Lease.LeaseID, LeaseGeneration: envelope.Data.Lease.LeaseGeneration, FactsCursor: envelope.Data.WorkItem.FactsCursor, AccountFences: envelope.Data.WorkItem.AccountFences}
}

func setWorkerHeaders(request *http.Request, idempotencyKey string) {
	request.Header.Set("Authorization", "Bearer test-worker-token")
	request.Header.Set("Idempotency-Key", idempotencyKey)
	request.Header.Set("X-Worker-Id", "worker-a")
	request.Header.Set("X-Worker-Generation", "1")
	request.Header.Set("X-Worker-Instance-Id", "instance-a")
}
