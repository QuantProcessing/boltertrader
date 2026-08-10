package funding

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/backend/internal/workapi"
	"github.com/shopspring/decimal"
)

func TestClientRegistersClaimsAndDrainsWithScopedBearer(t *testing.T) {
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-worker-token" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		seen = append(seen, r.Method+" "+r.URL.Path)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode %s: %v", r.URL.Path, err)
		}
		if _, forbidden := body["token"]; forbidden {
			t.Fatalf("request leaked token in body: %+v", body)
		}
		switch r.URL.Path {
		case "/internal/v1/funding-work/workers/registrations":
			if body["workerId"] != "worker-a" || body["productId"] != "funding" || body["contractVersion"] == "" {
				t.Fatalf("registration body=%+v", body)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"workerGeneration":2}}`))
		case "/internal/v1/funding-work/claims":
			if body["workerGeneration"] != float64(2) || body["maxItems"] != float64(1) {
				t.Fatalf("claim body=%+v", body)
			}
			_, _ = w.Write([]byte(`{"data":{"workItem":{"workItemId":"work-1","workRevision":1,"factsCursor":"cursor-1","accountFences":[],"payload":{},"attempt":1},"lease":{"leaseId":"lease-1","leaseGeneration":1,"expiresAt":"2026-08-05T00:00:30Z"},"nextCursor":"cursor-1"}}`))
		case "/internal/v1/funding-work/workers/worker-a/drain":
			if body["reason"] != "shutdown" {
				t.Fatalf("drain body=%+v", body)
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"data":{"draining":true}}`))
		default:
			t.Fatalf("unexpected route %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client, err := NewClient(Config{BaseURL: server.URL, Token: "test-worker-token", WorkerID: "worker-a", InstanceID: "instance-a", Mode: "test", ContractVersion: "boltertrader-product-platform-upgrade/v4"})
	if err != nil {
		t.Fatal(err)
	}
	generation, err := client.Register(context.Background())
	if err != nil || generation != 2 {
		t.Fatalf("Register generation=%d err=%v", generation, err)
	}
	claims, err := client.Claim(context.Background(), ClaimOptions{WorkerGeneration: generation, MaxItems: 1, IdempotencyKey: "claim-1"})
	if err != nil || len(claims) != 1 || claims[0].LeaseID != "lease-1" || claims[0].ExpiresAt.IsZero() {
		t.Fatalf("Claim=%+v err=%v", claims, err)
	}
	if err := client.Drain(context.Background(), DrainOptions{WorkerGeneration: generation, IdempotencyKey: "drain-1", Reason: "shutdown"}); err != nil {
		t.Fatalf("Drain err=%v", err)
	}
	if strings.Join(seen, ",") != "POST /internal/v1/funding-work/workers/registrations,POST /internal/v1/funding-work/claims,POST /internal/v1/funding-work/workers/worker-a/drain" {
		t.Fatalf("routes = %v", seen)
	}
}

func TestPassiveStatusUsesOnlyGET(t *testing.T) {
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer test-worker-token" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"data":{"mode":"test"}}`))
	}))
	defer server.Close()
	client, err := NewClient(Config{BaseURL: server.URL, Token: "test-worker-token", WorkerID: "worker-a", InstanceID: "instance-a", Mode: "test", ContractVersion: workContractVersion})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.PassiveStatus(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(seen, ","); got != "GET /internal/v1/funding-work/status" {
		t.Fatalf("requests=%s", got)
	}
}

func TestClientRejectsForbiddenWorkerEnvironment(t *testing.T) {
	for _, env := range []string{"BT_GATEWAY_URL", "BT_GATEWAY_TOKEN_FILE", "GATEWAY_AUTH_TOKEN_FILE", "BT_LIVE_PRIVATE_WRITES_ENABLED", "DATABASE_URL", "BT_SECRET_MASTER_KEY_FILE", "TRADING_SECRET"} {
		t.Setenv(env, "must-not-be-visible")
		if _, err := NewClient(Config{BaseURL: "http://127.0.0.1:1", Token: "test-worker-token", WorkerID: "worker-a", InstanceID: "instance-a", Mode: "test", ContractVersion: "boltertrader-product-platform-upgrade/v4"}); err == nil {
			t.Fatalf("NewClient accepted forbidden env %s", env)
		}
		t.Setenv(env, "")
	}
}

func TestClientAcceptsFrozenPlatformWorkBaseURL(t *testing.T) {
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/internal/v1/funding-work/workers/registrations":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"data":{"workerGeneration":2}}`))
		case "/internal/v1/funding-work/claims":
			_, _ = w.Write([]byte(`{"data":{"nextCursor":"cursor-1"}}`))
		case "/internal/v1/funding-work/workers/worker-a/drain":
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"data":{"draining":true}}`))
		default:
			t.Fatalf("unexpected route %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client, err := NewClient(Config{BaseURL: server.URL + "/internal/v1/funding-work", Token: "test-worker-token", WorkerID: "worker-a", InstanceID: "instance-a", Mode: "test", ContractVersion: "boltertrader-product-platform-upgrade/v4"})
	if err != nil {
		t.Fatalf("NewClient base path error=%v", err)
	}
	if _, err := client.Register(context.Background()); err != nil {
		t.Fatalf("Register err=%v", err)
	}
	if _, err := client.ClaimPage(context.Background(), ClaimOptions{WorkerGeneration: 2, IdempotencyKey: "claim-1", MaxItems: 1}); err != nil {
		t.Fatalf("ClaimPage err=%v", err)
	}
	if err := client.Drain(context.Background(), DrainOptions{WorkerGeneration: 2, IdempotencyKey: "drain-1", Reason: "shutdown"}); err != nil {
		t.Fatalf("Drain err=%v", err)
	}
	if got := strings.Join(seen, ","); got != "POST /internal/v1/funding-work/workers/registrations,POST /internal/v1/funding-work/claims,POST /internal/v1/funding-work/workers/worker-a/drain" {
		t.Fatalf("routes = %v", seen)
	}
}

func TestClientRejectsUnsupportedPlatformWorkBaseURLForms(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	for _, baseURL := range []string{
		server.URL + "/internal/v1/funding-work/../orders",
		server.URL + "/internal/v1/funding-work?query=1",
		server.URL + "/internal/v1/funding-work#fragment",
	} {
		if _, err := NewClient(Config{BaseURL: baseURL, Token: "test-worker-token", WorkerID: "worker-a", InstanceID: "instance-a", Mode: "test", ContractVersion: "boltertrader-product-platform-upgrade/v4"}); err == nil {
			t.Fatalf("NewClient accepted %s", baseURL)
		}
	}
}

func TestClientAcceptsFrozenPlatformWorkBaseURLWithoutDuplicatingPath(t *testing.T) {
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	config := Config{BaseURL: server.URL + "/internal/v1/funding-work", Token: "test-worker-token", WorkerID: "worker-a", InstanceID: "instance-a", Mode: "test", ContractVersion: "boltertrader-product-platform-upgrade/v4"}
	config.HTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		path = request.URL.Path
		return &http.Response{StatusCode: http.StatusCreated, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":{"workerGeneration":2}}`))}, nil
	})}
	client, err := NewClient(config)
	if err != nil {
		t.Fatalf("NewClient frozen base URL: %v", err)
	}
	if _, err := client.Register(context.Background()); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if path != "/internal/v1/funding-work/workers/registrations" {
		t.Fatalf("registration path=%q", path)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestConfigFromEnvironmentReadsTokenFileAndRequiresWorkerScope(t *testing.T) {
	tokenFile := t.TempDir() + "/worker.token"
	if err := os.WriteFile(tokenFile, []byte("test-worker-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BT_PLATFORM_WORK_URL", "http://127.0.0.1:18092")
	t.Setenv("BT_FUNDING_WORKER_TOKEN_FILE", tokenFile)
	t.Setenv("BT_FUNDING_WORKER_ID", "worker-a")
	t.Setenv("BT_FUNDING_WORKER_INSTANCE_ID", "instance-a")
	t.Setenv("BT_FUNDING_WORK_CONTRACT_VERSION", "boltertrader-product-platform-upgrade/v4")
	t.Setenv("FT_MODE", "test")
	config, err := ConfigFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if config.Token != "test-worker-token" || config.WorkerID != "worker-a" || config.Mode != "test" {
		t.Fatalf("config=%+v", config)
	}
}

func TestMigrationValidationProfileRequiresTestAndNoLiveWrites(t *testing.T) {
	if err := (Config{StartupProfile: "migration-validation", Mode: "test"}).ValidateStartupProfile(); err != nil {
		t.Fatalf("test validation profile: %v", err)
	}
	for _, config := range []Config{
		{StartupProfile: "migration-validation", Mode: "live"},
		{StartupProfile: "migration-validation", Mode: "test", LiveEnabled: true},
		{StartupProfile: "unknown", Mode: "test"},
	} {
		if err := config.ValidateStartupProfile(); err == nil {
			t.Fatalf("accepted config %+v", config)
		}
	}
}

func TestConfigFromEnvironmentDefaultsWorkerIDPerMode(t *testing.T) {
	tokenFile := t.TempDir() + "/worker.token"
	if err := os.WriteFile(tokenFile, []byte("test-worker-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BT_FUNDING_WORKER_TOKEN_FILE", tokenFile)
	t.Setenv("BT_FUNDING_WORKER_ID", "")

	t.Setenv("FT_MODE", "test")
	testConfig, err := ConfigFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FT_MODE", "live")
	liveConfig, err := ConfigFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if testConfig.WorkerID != "funding-default-test" || liveConfig.WorkerID != "funding-default-live" || testConfig.WorkerID == liveConfig.WorkerID {
		t.Fatalf("test worker=%q live worker=%q", testConfig.WorkerID, liveConfig.WorkerID)
	}
}

func TestFundingWorkerAdmissionIsExplicitModeScopedAndBounded(t *testing.T) {
	defaultAdmission, err := (Config{Mode: "live"}).Admission()
	if err != nil || defaultAdmission.LiveEnabled() {
		t.Fatalf("default admission=%+v err=%v", defaultAdmission, err)
	}
	configured, err := (Config{Mode: "live", LiveEnabled: true, LiveMaxNotional: "75"}).Admission()
	if err != nil || !configured.Allows("live", decimal.NewFromInt(75)) || configured.Allows("live", decimal.NewFromInt(76)) {
		t.Fatalf("configured admission=%+v err=%v", configured, err)
	}
	if _, err := (Config{Mode: "test", LiveEnabled: true, LiveMaxNotional: "75"}).Admission(); err == nil {
		t.Fatal("cross-mode Funding Live configuration was accepted")
	}
	if _, err := (Config{Mode: "live", LiveEnabled: true}).Admission(); err == nil {
		t.Fatal("Funding Live configuration without a maximum was accepted")
	}
}

func TestConfigFromEnvironmentRejectsInvalidFundingLiveBoolean(t *testing.T) {
	tokenFile := t.TempDir() + "/worker.token"
	if err := os.WriteFile(tokenFile, []byte("test-worker-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BT_FUNDING_WORKER_TOKEN_FILE", tokenFile)
	t.Setenv("BT_FUNDING_LIVE_ENABLED", "sometimes")
	if _, err := ConfigFromEnvironment(); err == nil {
		t.Fatal("invalid Funding Live boolean was accepted")
	}
}

func TestClientTalksToRealWorkHandlerWithIdempotencyAndFencingHeaders(t *testing.T) {
	t.Setenv("BT_GATEWAY_URL", "")
	handler := newRealWorkHandler(t)
	server := httptest.NewServer(handler)
	defer server.Close()

	client, err := NewClient(Config{BaseURL: server.URL, Token: "test-worker-token", WorkerID: "worker-a", InstanceID: "instance-a", Mode: "test", ContractVersion: "boltertrader-product-platform-upgrade/v4"})
	if err != nil {
		t.Fatal(err)
	}

	generation, err := client.Register(context.Background())
	if err != nil {
		t.Fatalf("Register err=%v", err)
	}
	if generation != 1 {
		t.Fatalf("generation=%d want 1", generation)
	}
	registration, err := client.Heartbeat(context.Background(), HeartbeatOptions{WorkerGeneration: generation, State: "ready", ActiveLeaseIDs: []string{}, LastAppliedFactsCursor: ""})
	if err != nil || registration.generation() != generation {
		t.Fatalf("Heartbeat registration=%+v err=%v", registration, err)
	}
	readiness, err := client.Readiness(context.Background(), generation)
	if err != nil || !readiness.CanClaim || readiness.WorkerGeneration != generation {
		t.Fatalf("Readiness=%+v err=%v", readiness, err)
	}

	claims, err := client.Claim(context.Background(), ClaimOptions{WorkerGeneration: generation, IdempotencyKey: "claim-real-1", MaxItems: 1})
	if err != nil {
		t.Fatalf("Claim err=%v", err)
	}
	if len(claims) != 1 || claims[0].WorkItemID == "" || claims[0].LeaseID == "" || claims[0].LeaseGeneration == 0 {
		t.Fatalf("claims=%+v", claims)
	}
	replayedClaims, err := client.Claim(context.Background(), ClaimOptions{WorkerGeneration: generation, IdempotencyKey: "claim-real-1", MaxItems: 1})
	if err != nil || len(replayedClaims) != 1 || replayedClaims[0].LeaseID != claims[0].LeaseID || replayedClaims[0].WorkItemID != claims[0].WorkItemID {
		t.Fatalf("claim replay=%+v err=%v first=%+v", replayedClaims, err, claims)
	}
	lease := LeaseRef{WorkerGeneration: generation, WorkItemID: claims[0].WorkItemID, WorkRevision: claims[0].WorkRevision, LeaseID: claims[0].LeaseID, LeaseGeneration: claims[0].LeaseGeneration}
	_, err = client.Facts(context.Background(), lease, "unknown-cursor", 500)
	var resnapshotErr *HTTPError
	if !errors.As(err, &resnapshotErr) || resnapshotErr.Code != "RESNAPSHOT_REQUIRED" || !resnapshotErr.Retryable {
		t.Fatalf("unknown cursor error=%v", err)
	}
	snapshot, err := client.Snapshot(context.Background(), lease)
	if err != nil || snapshot.ResnapshotRequired || snapshot.NextCursor == "" {
		t.Fatalf("Snapshot=%+v err=%v", snapshot, err)
	}
	page, err := client.Facts(context.Background(), lease, claims[0].FactsCursor, 100)
	if err != nil {
		t.Fatalf("Facts=%+v err=%v", page, err)
	}
	renewed, err := client.Renew(context.Background(), lease, "renew-real-1")
	if err != nil || renewed.LeaseGeneration != lease.LeaseGeneration || renewed.ExpiresAt.IsZero() {
		t.Fatalf("Renew=%+v err=%v", renewed, err)
	}
	replayedRenewal, err := client.Renew(context.Background(), lease, "renew-real-1")
	if err != nil || replayedRenewal.LeaseGeneration != renewed.LeaseGeneration || !replayedRenewal.ExpiresAt.Equal(renewed.ExpiresAt) {
		t.Fatalf("renew replay=%+v err=%v first=%+v", replayedRenewal, err, renewed)
	}
	lease.LeaseGeneration = renewed.LeaseGeneration
	intentPayload := json.RawMessage(`{"previewId":"preview-1","opportunityId":"opportunity-1","longAccountId":"acct-long","shortAccountId":"acct-short","canonicalMarketId":"BTC-USDT-PERP","notionalPerLeg":"50","maxSlippageBps":"10","holdThroughSettlement":true}`)
	intent, err := client.CommandIntent(context.Background(), IntentOptions{LeaseRef: lease, IdempotencyKey: "intent-real-1", IntentKind: "funding.open", FactsCursor: claims[0].FactsCursor, AccountFences: claims[0].AccountFences, Payload: intentPayload})
	if err != nil || !intent.Accepted {
		t.Fatalf("CommandIntent=%+v err=%v", intent, err)
	}
	replayedIntent, err := client.CommandIntent(context.Background(), IntentOptions{LeaseRef: lease, IdempotencyKey: "intent-real-1", IntentKind: "funding.open", FactsCursor: claims[0].FactsCursor, AccountFences: claims[0].AccountFences, Payload: intentPayload})
	if err != nil || !replayedIntent.Accepted {
		t.Fatalf("CommandIntent replay=%+v err=%v", replayedIntent, err)
	}
	result := json.RawMessage(`{"outcome":"completed","commandIds":["command-1"],"finalFactsCursor":"` + claims[0].FactsCursor + `","summaryCode":"OPENED"}`)
	completed, err := client.Complete(context.Background(), CompleteOptions{LeaseRef: lease, IdempotencyKey: "complete-real-1", FactsCursor: claims[0].FactsCursor, Result: result})
	var dependencyErr *HTTPError
	if !errors.As(err, &dependencyErr) || dependencyErr.Code != "WORKER_NOT_READY" || !dependencyErr.Retryable || completed.Completed {
		t.Fatalf("Complete with nonterminal command=%+v err=%v", completed, err)
	}
	_, err = client.Readiness(context.Background(), generation+1)
	var staleErr *HTTPError
	if !errors.As(err, &staleErr) || staleErr.Code != "WORKER_GENERATION_STALE" {
		t.Fatalf("stale generation error=%v", err)
	}

	if err := client.Drain(context.Background(), DrainOptions{WorkerGeneration: generation, IdempotencyKey: "drain-real-1", Reason: "shutdown"}); err != nil {
		t.Fatalf("Drain err=%v", err)
	}
}

func TestClientReturnsTypedHTTPErrorFromRealWorkHandler(t *testing.T) {
	handler := newRealWorkHandler(t)
	server := httptest.NewServer(handler)
	defer server.Close()

	client, err := NewClient(Config{BaseURL: server.URL, Token: "wrong-token", WorkerID: "worker-a", InstanceID: "instance-a", Mode: "test", ContractVersion: "boltertrader-product-platform-upgrade/v4"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Register(context.Background())
	if err == nil {
		t.Fatal("Register succeeded with wrong token")
	}
	httpErr, ok := err.(*HTTPError)
	if !ok {
		t.Fatalf("error type=%T want *HTTPError: %v", err, err)
	}
	if httpErr.StatusCode != http.StatusUnauthorized || httpErr.Code != "WORKER_AUTH_REQUIRED" || httpErr.RequestID == "" || httpErr.Retryable {
		t.Fatalf("http error=%+v", httpErr)
	}
	if strings.Contains(httpErr.Error(), "wrong-token") {
		t.Fatalf("error leaked token: %v", httpErr)
	}
}

func TestClientValidatesRequiredIdempotencyBeforeHTTP(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()
	client, err := NewClient(Config{BaseURL: server.URL, Token: "test-worker-token", WorkerID: "worker-a", InstanceID: "instance-a", Mode: "test", ContractVersion: "boltertrader-product-platform-upgrade/v4"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Claim(context.Background(), ClaimOptions{WorkerGeneration: 1, MaxItems: 1})
	if err == nil || !strings.Contains(err.Error(), "idempotency key is required") {
		t.Fatalf("Claim err=%v", err)
	}
	if called {
		t.Fatal("client performed HTTP request before validating idempotency key")
	}
}

func newRealWorkHandler(t *testing.T) http.Handler {
	t.Helper()
	handler, err := workapi.New(workapi.Options{TokenDigest: sha256.Sum256([]byte("test-worker-token")), Mode: "test", ContractVersion: workapi.ContractVersion})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}
