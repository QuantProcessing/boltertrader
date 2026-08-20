package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/credential"
	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/account"
	"github.com/QuantProcessing/boltertrader/gateway/auth"
	"github.com/QuantProcessing/boltertrader/gateway/catalog"
	"github.com/QuantProcessing/boltertrader/gateway/config"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	gatewayevent "github.com/QuantProcessing/boltertrader/gateway/event"
	"github.com/QuantProcessing/boltertrader/gateway/health"
	"github.com/QuantProcessing/boltertrader/gateway/httpapi"
	"github.com/QuantProcessing/boltertrader/gateway/observability"
	"github.com/QuantProcessing/boltertrader/gateway/order"
	"github.com/QuantProcessing/boltertrader/gateway/process"
	"github.com/QuantProcessing/boltertrader/gateway/publicconn"
	"github.com/QuantProcessing/boltertrader/gateway/reference"
	"github.com/QuantProcessing/boltertrader/gateway/secret"
	"github.com/QuantProcessing/boltertrader/gateway/store"
	storepostgres "github.com/QuantProcessing/boltertrader/gateway/store/postgres"
	"github.com/QuantProcessing/boltertrader/routeprofile"
	"github.com/QuantProcessing/boltertrader/secretstore"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

const (
	credentialCanary     = "BT_TEST_SECRET_CANARY_4f0a9d"
	mockProviderIdentity = "/private/tmp/platform-provider-identity"
	rawOpaqueRef         = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
)

func TestPlatformRuntimeZeroAccountProcessIsReady(t *testing.T) {
	harness := newPlatformHarness(t)
	if ready, active, routeReady := harness.manager.RuntimeReadiness(); !ready || active != 0 || routeReady != 0 {
		t.Fatalf("zero-account readiness=(%t,%d,%d)", ready, active, routeReady)
	}

	authRef := mustOperationalRef(t, "env://PLATFORM_TEST_TOKEN")
	dsnRef := mustOperationalRef(t, "env://PLATFORM_TEST_DSN")
	configFile := config.File{
		Server:   config.ServerConfig{Listen: "127.0.0.1:1", AuthTokenRef: authRef},
		Postgres: config.PostgresConfig{DSNRef: dsnRef},
	}
	pool := &integrationPool{}
	orders := &integrationOrderCoordinator{}
	service, err := process.Start(t.Context(), configFile, process.Dependencies{
		CheckCredentialResolver: harness.resolver.Check,
		ResolveSecret: func(_ context.Context, ref secret.Ref) (string, error) {
			switch ref.RawForProvider() {
			case authRef.RawForProvider():
				return "test-token", nil
			case dsnRef.RawForProvider():
				return "postgres://local-test", nil
			default:
				return "", errors.New("unknown operational reference")
			}
		},
		ConnectPostgres:          func(context.Context, string) (process.PostgresPool, error) { return pool, nil },
		ValidateDatabaseIdentity: func(context.Context, process.PostgresPool) error { return nil },
		VerifyDatabaseAuthority: func(context.Context, process.PostgresPool) (storepostgres.AuthorityResult, error) {
			return storepostgres.AuthorityResult{WriterMembership: true}, nil
		},
		BuildAccountManager: func(context.Context) (process.AccountManager, error) { return harness.manager, nil },
		RestoreAccountManager: func(ctx context.Context, _ process.PostgresPool, manager process.AccountManager) error {
			return manager.(*account.Manager).RestoreRuntimeState(ctx)
		},
		BuildOrderCoordinator: func(context.Context, process.PostgresPool, process.AccountManager) (process.OrderCoordinator, error) {
			return orders, nil
		},
		Listen: func(string, string) (net.Listener, error) { return &integrationListener{}, nil },
	})
	if err != nil {
		t.Fatalf("process.Start: %v", err)
	}
	t.Cleanup(func() { _ = service.Shutdown(context.Background()) })
	if readiness := service.Health().Readiness(); !readiness.Ready {
		t.Fatalf("process readiness=%+v", readiness)
	}
	if got := orders.startedAccounts(); len(got) != 0 {
		t.Fatalf("order recovery accounts=%v", got)
	}
	if checks := harness.resolver.checkCalls.Load(); checks != 1 {
		t.Fatalf("credential resolver preflight calls=%d, want 1", checks)
	}
}

func TestPlatformRuntimeAbsentRouteDeniesOrderBeforePrivateIO(t *testing.T) {
	harness := newPlatformHarness(t)
	pending := platformSnapshot(t, "route-denial", "account-route", 1, 1, rawOpaqueRef, spotRoute())
	harness.promote(t, pending)
	router, err := order.NewAccountRouter(harness.manager)
	if err != nil {
		t.Fatal(err)
	}
	_, err = router.Route(t.Context(), pending.AccountID, "binance:live:perp:BTC-USDT")
	if err == nil {
		t.Fatal("absent Perp route was accepted")
	}
	if calls := harness.builder.privateCalls.Load(); calls != 0 {
		t.Fatalf("absent route performed %d private calls", calls)
	}
}

func TestPlatformRuntimeTwoAccountsSharePublicConnectorButNotPrivateRoutes(t *testing.T) {
	harness := newPlatformHarness(t)
	first := platformSnapshot(t, "shared-first", "account-one", 1, 1, rawOpaqueRef, spotRoute())
	second := platformSnapshot(t, "shared-second", "account-two", 1, 1, alternateOpaqueRef().RawForTrustedBoundary(), spotRoute())
	firstGroup := harness.promote(t, first)
	secondGroup := harness.promote(t, second)

	if builds := harness.publicFactory.builds.Load(); builds != 1 {
		t.Fatalf("public connector builds=%d, want 1", builds)
	}
	if refreshes := harness.publicFactory.refreshes.Load(); refreshes != 1 {
		t.Fatalf("public connector refreshes=%d, want 1", refreshes)
	}
	if private := harness.builder.builds.Load(); private != 2 {
		t.Fatalf("private route groups=%d, want 2", private)
	}
	if firstGroup == secondGroup || firstGroup.Admission == secondGroup.Admission {
		t.Fatal("two accounts shared a private runtime generation")
	}
	if got := harness.manager.AccountIDs(); len(got) != 2 || got[0] != first.AccountID || got[1] != second.AccountID {
		t.Fatalf("account IDs=%v", got)
	}
}

func TestPlatformRuntimeSpotAndPerpPromoteAtomically(t *testing.T) {
	harness := newPlatformHarness(t)
	pending := platformSnapshot(t, "atomic", "account-atomic", 1, 1, rawOpaqueRef, spotRoute(), perpRoute())
	harness.store.register(pending.DesiredSnapshot)
	group, err := harness.manager.BuildCandidate(t.Context(), pending)
	if err != nil {
		t.Fatal(err)
	}
	if active := harness.manager.ActiveSnapshot(pending.AccountID); active != nil {
		t.Fatalf("candidate published before durable promotion: %+v", active)
	}
	if _, err := harness.manager.PromoteCandidate(t.Context(), group); err != nil {
		t.Fatal(err)
	}
	active := harness.manager.ActiveSnapshot(pending.AccountID)
	if active == nil || active.Fence != pending.Fence || len(active.Routes) != 2 {
		t.Fatalf("active snapshot=%+v", active)
	}
	for _, routeID := range []domain.RouteID{"spot", "perp-usdt"} {
		if _, ok := active.Routes[routeID]; !ok {
			t.Fatalf("atomic generation missing route %s: %+v", routeID, active.Routes)
		}
	}
}

func TestPlatformRuntimeFailedCandidateKeepsActiveGeneration(t *testing.T) {
	harness := newPlatformHarness(t)
	active := platformSnapshot(t, "active", "account-stable", 1, 1, rawOpaqueRef, spotRoute(), perpRoute())
	harness.promote(t, active)
	before := harness.manager.ActiveSnapshot(active.AccountID)

	harness.builder.setFailRoute("spot", errors.New("authentication failed: "+credentialCanary+" "+mockProviderIdentity))
	next := platformSnapshot(t, "failed", active.AccountID, 2, 2, alternateOpaqueRef().RawForTrustedBoundary(), spotRoute(), perpRoute())
	harness.store.register(next.DesiredSnapshot)
	if _, err := harness.manager.BuildCandidate(t.Context(), next); !errors.Is(err, account.ErrCandidateFailed) {
		t.Fatalf("failed candidate err=%v", err)
	}
	after := harness.manager.ActiveSnapshot(active.AccountID)
	if before == nil || after == nil || before.Fence != after.Fence || len(after.Routes) != 2 {
		t.Fatalf("active changed across failed candidate: before=%+v after=%+v", before, after)
	}
}

func TestPlatformRuntimeRejectsInvalidCandidatesBeforeResolverOrNetwork(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*store.PendingSnapshot, *platformHarness)
		wantErr error
	}{
		// There used to be a "wrong mode" case ahead of this one, feeding a
		// `binance.spot.live.v1` profile to a test-mode manager. Its whole point
		// was the mode disagreement; with one mode left it named an unregistered
		// profile and nothing else, which is what this case already covers.
		{name: "wrong profile", mutate: func(pending *store.PendingSnapshot, _ *platformHarness) {
			pending.Routes[0].RouteProfileID = "binance.profile.not-trusted.v1"
		}},
		{name: "malformed opaque ref", mutate: func(pending *store.PendingSnapshot, _ *platformHarness) {
			pending.SecretRef = secretstore.OpaqueRef("raw-ref-canary-invalid")
		}},
		{name: "retired opaque ref", wantErr: store.ErrRetiredSecretReference, mutate: func(pending *store.PendingSnapshot, harness *platformHarness) {
			harness.store.setRetired(pending.SecretRef)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newPlatformHarness(t)
			pending := platformSnapshot(t, "invalid-"+strings.ReplaceAll(test.name, " ", "-"), "account-invalid", 1, 1, rawOpaqueRef, spotRoute())
			test.mutate(&pending, harness)
			_, err := harness.manager.BuildCandidate(t.Context(), pending)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("err=%v, want %v", err, test.wantErr)
				}
			} else if !errors.Is(err, account.ErrCandidateFailed) {
				t.Fatalf("err=%v, want candidate failure", err)
			}
			if calls := harness.resolver.resolveCalls.Load(); calls != 0 {
				t.Fatalf("resolver calls=%d", calls)
			}
			if builds := harness.publicFactory.builds.Load(); builds != 0 {
				t.Fatalf("public connector builds=%d", builds)
			}
			if builds := harness.builder.builds.Load(); builds != 0 {
				t.Fatalf("private route builds=%d", builds)
			}
		})
	}
}

func TestPlatformRuntimeRestorePublishesActiveBeforePending(t *testing.T) {
	harness := newPlatformHarness(t)
	active := platformSnapshot(t, "restore-active", "account-restore", 4, 4, rawOpaqueRef, spotRoute(), perpRoute())
	pending := platformSnapshot(t, "restore-pending", active.AccountID, 5, 5, alternateOpaqueRef().RawForTrustedBoundary(), spotRoute(), perpRoute())
	harness.store.setStates([]store.RuntimeState{{
		AccountID: active.AccountID,
		Active:    &store.ActiveSnapshot{DesiredSnapshot: active.DesiredSnapshot, PromotedAt: time.Now().Add(-time.Minute)},
		Pending:   &pending,
	}})
	var observed atomic.Bool
	harness.builder.setBuildHook(func(snapshot store.DesiredSnapshot) {
		if snapshot.SnapshotID != pending.SnapshotID {
			return
		}
		published := harness.manager.ActiveSnapshot(active.AccountID)
		if published != nil && published.Fence == active.Fence && len(published.Routes) == 2 {
			observed.Store(true)
		}
	})
	if err := harness.manager.RestoreRuntimeState(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !observed.Load() {
		t.Fatal("pending rebuild began before durable active publication")
	}
	if restored := harness.manager.ActiveSnapshot(active.AccountID); restored == nil || restored.Fence != active.Fence {
		t.Fatalf("restored active=%+v", restored)
	}
}

// The harness never arms the operator switch, and that is now the whole reason
// the fence refuses. It used to be half the reason: the test built a live-mode
// manager on purpose, because every other test here ran in test mode and wrote
// past this fence for free. Nothing writes for free any more, so "live" is no
// longer a property this test has to arrange.
func TestPlatformRuntimeLiveWritesRemainDenied(t *testing.T) {
	harness := newPlatformHarness(t)
	pending := platformSnapshot(t, "live-denied", "account-live", 1, 1, rawOpaqueRef, store.RouteDefinition{
		RouteID: "spot", Product: "spot", RouteProfileID: "binance.spot.v1", RequestedState: store.RouteActive,
	})
	harness.promote(t, pending)
	fence := account.WriteFence{
		AccountID: pending.AccountID, DesiredStateRevision: pending.Fence.DesiredStateRevision,
		RuntimeGeneration: pending.Fence.RuntimeGeneration, CredentialVersion: pending.Fence.CredentialVersion,
	}
	if err := harness.manager.CheckWriteFence(fence); !errors.Is(err, account.ErrLiveWriteDisabled) {
		t.Fatalf("live write fence err=%v", err)
	}
	status, err := harness.manager.RouteStatus(pending.AccountID, "spot", "")
	if err != nil || status.WriteReady || status.Reason != account.ErrLiveWriteDisabled.Error() {
		t.Fatalf("live route status=%+v err=%v", status, err)
	}
	router, err := order.NewAccountRouter(harness.manager)
	if err != nil {
		t.Fatal(err)
	}
	route, err := router.Route(t.Context(), pending.AccountID, "binance:live:spot:BTC-USDT")
	if err != nil || route.WriteReady {
		t.Fatalf("live order route=%+v err=%v", route, err)
	}
	if calls := harness.builder.privateCalls.Load(); calls != 0 {
		t.Fatalf("denied live route performed %d private calls", calls)
	}
}

func TestPlatformRuntimeArtifactsExcludeCredentialCanariesAndProviderIdentity(t *testing.T) {
	credentials := newFilesystemCredentials(t)
	activeRef := credentials.put(t, "redaction-active", credentialCanary)
	rotatedRef := credentials.put(t, "redaction-rotated", credentialCanary+"-rotated")
	harness := newPlatformHarnessWithResolver(t, credentials.resolver)
	active := platformSnapshot(t, "redaction-active", "account-redacted", 1, 1, activeRef.RawForTrustedBoundary(), spotRoute())
	harness.promote(t, active)
	harness.builder.setFailRoute("spot", errors.New("native failure "+credentialCanary+" "+activeRef.RawForTrustedBoundary()+" "+credentials.stateDir))
	failed := platformSnapshot(t, "redaction-failed", active.AccountID, 2, 2, rotatedRef.RawForTrustedBoundary(), spotRoute())
	harness.store.register(failed.DesiredSnapshot)
	_, candidateErr := harness.manager.BuildCandidate(t.Context(), failed)
	if !errors.Is(candidateErr, account.ErrCandidateFailed) {
		t.Fatalf("candidate err=%v", candidateErr)
	}
	if current := harness.manager.ActiveSnapshot(active.AccountID); current == nil || current.Fence != active.Fence {
		t.Fatalf("failed candidate replaced active runtime: %+v", current)
	}

	harness.builder.setFailRoute("", nil)
	candidate, err := harness.manager.BuildCandidate(t.Context(), failed)
	if err != nil {
		t.Fatalf("rebuild candidate: %v", err)
	}
	if _, err := harness.manager.PromoteCandidate(t.Context(), candidate); err != nil {
		t.Fatalf("promote rotated candidate: %v", err)
	}
	activeState := store.ActiveSnapshot{DesiredSnapshot: failed.DesiredSnapshot, PromotedAt: time.Now().UTC()}
	retiringState := store.ActiveSnapshot{DesiredSnapshot: active.DesiredSnapshot, PromotedAt: time.Now().Add(-time.Minute).UTC()}
	harness.store.setStates([]store.RuntimeState{{
		AccountID: active.AccountID,
		Active:    &activeState,
		Retiring:  []store.ActiveSnapshot{retiringState},
	}})
	harness.restart(t)
	if err := harness.manager.RestoreRuntimeState(t.Context()); err != nil {
		t.Fatalf("restore runtime: %v", err)
	}
	if restored := harness.manager.ActiveSnapshot(active.AccountID); restored == nil || restored.Fence != failed.Fence {
		t.Fatalf("restored active runtime=%+v", restored)
	}
	if err := credentials.writer.Delete(t.Context(), credentials.scope, activeRef); err != nil {
		t.Fatalf("delete retired credential: %v", err)
	}
	if _, err := credentials.resolver.Resolve(t.Context(), activeRef); !errors.Is(err, secretstore.ErrSecretNotFound) {
		t.Fatalf("resolve deleted credential err=%v", err)
	}

	runtimesJSON := mustJSON(t, harness.manager.Runtimes())
	resourcesJSON := mustJSON(t, harness.manager.Accounts())
	activeJSON := mustJSON(t, harness.manager.ActiveSnapshot(active.AccountID))
	metrics := observability.NewMetrics()
	states := make([]observability.AccountState, 0, len(harness.manager.Accounts()))
	for _, resource := range harness.manager.Accounts() {
		states = append(states, observability.AccountState{
			Venue: string(resource.Venue), Product: string(resource.Product), Environment: string(resource.Environment),
			ReadReady: resource.ReadReady, WriteReady: resource.WriteReady,
		})
	}
	metrics.SetAccountSnapshot(states)
	metricsResponse := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(metricsResponse, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	var logOutput bytes.Buffer
	logger := observability.NewSafeLogger(&logOutput)
	logger.Error(candidateErr.Error(), credentials.stateDir, activeRef.RawForTrustedBoundary())
	if err := logger.Sync(); err != nil {
		t.Fatal(err)
	}

	accountsResponse := captureAccountsHTTPArtifact(t, harness)
	auditJSON := captureAuditHTTPArtifact(t, harness)
	artifacts := map[string][]byte{
		"runtime.json":                runtimesJSON,
		"resources.json":              resourcesJSON,
		"active.json":                 activeJSON,
		"gateway.log":                 logOutput.Bytes(),
		"metrics.txt":                 metricsResponse.Body.Bytes(),
		"audit-capture.json":          auditJSON,
		"accounts-http-response.json": accountsResponse,
	}
	dir := t.TempDir()
	for name, data := range artifacts {
		if len(data) == 0 {
			t.Fatalf("artifact %s is empty", name)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for name := range artifacts {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		assertExcludesSecrets(t, "artifact "+name, data, credentials.forbidden(activeRef, rotatedRef)...)
	}
	assertDirectoryExcludesSecrets(t, credentials.stateDir, credentials.forbidden(activeRef, rotatedRef)...)
}

func captureAccountsHTTPArtifact(t *testing.T, harness *platformHarness) []byte {
	t.Helper()
	handler, _ := newArtifactHTTPHandler(t, harness)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/accounts", nil)
	request.Header.Set("Authorization", "Bearer artifact-test-token")
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("accounts HTTP status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	return append([]byte(nil), recorder.Body.Bytes()...)
}

func captureAuditHTTPArtifact(t *testing.T, harness *platformHarness) []byte {
	t.Helper()
	handler, auditor := newArtifactHTTPHandler(t, harness)
	request := httptest.NewRequest(http.MethodPatch, "/v1/orders/018f0000-0000-7000-8000-000000000002", nil)
	request.Header.Set("Authorization", "Bearer artifact-test-token")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotImplemented {
		t.Fatalf("audited HTTP status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(auditor.records) != 1 {
		t.Fatalf("audit records=%d want=1", len(auditor.records))
	}
	record := auditor.records[0]
	if record.Action != store.AuditAmendOrder || record.Outcome != store.AuditUnsupported {
		t.Fatalf("audit record=%+v", record)
	}
	if err := record.Validate(); err != nil {
		t.Fatalf("captured audit record: %v", err)
	}
	return mustJSON(t, record)
}

func newArtifactHTTPHandler(t *testing.T, harness *platformHarness) (http.Handler, *artifactAuditCapture) {
	t.Helper()
	references, err := reference.NewService(harness.manager)
	if err != nil {
		t.Fatal(err)
	}
	identity := domain.NewIdentityGenerator(domain.IdentityGeneratorOptions{})
	events, err := gatewayevent.NewHub(gatewayevent.Options{Identity: identity})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(events.Close)
	auditor := &artifactAuditCapture{}
	monitor := health.NewMonitor()
	monitor.SetPostgresHealthy(true)
	handler, err := httpapi.NewHandler(httpapi.Options{
		Monitor: monitor, Accounts: harness.manager, References: references,
		Orders: artifactOrderService{}, Events: events, Auditor: auditor, Identity: identity,
	})
	if err != nil {
		t.Fatal(err)
	}
	ref := mustOperationalRef(t, "env://PLATFORM_ARTIFACT_TOKEN")
	middleware, err := auth.NewBearerMiddleware(t.Context(), secret.StaticProvider{
		ref.RawForProvider(): "artifact-test-token",
	}, ref)
	if err != nil {
		t.Fatal(err)
	}
	return middleware.Wrap(handler), auditor
}

type artifactAuditCapture struct {
	records []store.AuditRecord
}

func (capture *artifactAuditCapture) AppendAudit(_ context.Context, record store.AuditRecord) error {
	capture.records = append(capture.records, record)
	return nil
}

type artifactOrderService struct{}

func (artifactOrderService) Place(context.Context, order.PlaceRequest) (order.Result, error) {
	return order.Result{}, errors.New("unexpected order placement")
}

func (artifactOrderService) Get(context.Context, domain.PrincipalID, domain.GatewayOrderID) (order.Result, error) {
	return order.Result{}, errors.New("unexpected order read")
}

func (artifactOrderService) List(context.Context, order.ListRequest) ([]order.Result, error) {
	return nil, errors.New("unexpected order list")
}

func (artifactOrderService) Cancel(context.Context, order.CancelRequest) (order.Result, error) {
	return order.Result{}, errors.New("unexpected order cancellation")
}

func (artifactOrderService) AccountWriteStatus(domain.AccountID) order.AccountWriteStatus {
	return order.AccountWriteStatus{}
}

type filesystemCredentials struct {
	writer   secretstore.CredentialWriter
	resolver *countingCredentialResolver
	scope    secretstore.Scope
	stateDir string
}

func newFilesystemCredentials(t *testing.T) *filesystemCredentials {
	t.Helper()
	stateDir := filepath.Join(t.TempDir(), "secret-store")
	key := make([]byte, 32)
	for index := range key {
		key[index] = byte(index + 1)
	}
	provider, err := secretstore.NewFilesystemProvider(secretstore.FilesystemConfig{
		StateDir: stateDir,
		KeySource: secretstore.StaticKeySource{
			Key: key,
		},
	})
	clear(key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(provider.Close)
	scope := secretstore.Scope{Namespace: secretstore.Namespace, KeyVersion: "v1"}
	scopedResolver, err := secret.NewScopedCredentialResolver(provider.Resolver(), scope)
	if err != nil {
		t.Fatal(err)
	}
	return &filesystemCredentials{
		writer:   provider.Writer(),
		resolver: &countingCredentialResolver{delegate: scopedResolver},
		scope:    scope,
		stateDir: stateDir,
	}
}

func (credentials *filesystemCredentials) put(t *testing.T, seed, secretValue string) secretstore.OpaqueRef {
	t.Helper()
	operation, err := secretstore.ParseOperationID(uuid.NewSHA1(uuid.NameSpaceOID, []byte("credential-"+seed)).String())
	if err != nil {
		t.Fatal(err)
	}
	material := []byte(`{"schemaVersion":1,"venue":"binance","authKind":"api_key_secret","metadata":{},"secrets":{"apiKey":"key-` + secretValue + `","apiSecret":"secret-` + secretValue + `"}}`)
	defer clear(material)
	ref, err := credentials.writer.Put(t.Context(), credentials.scope, operation, material)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func (credentials *filesystemCredentials) forbidden(refs ...secretstore.OpaqueRef) []string {
	forbidden := []string{credentialCanary, credentials.stateDir}
	for _, ref := range refs {
		forbidden = append(forbidden, ref.RawForTrustedBoundary())
	}
	return forbidden
}

func assertDirectoryExcludesSecrets(t *testing.T, root string, forbidden ...string) {
	t.Helper()
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		assertExcludesSecrets(t, "provider file "+relative, data, forbidden...)
		assertExcludesSecrets(t, "provider filename "+relative, []byte(relative), forbidden...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func assertExcludesSecrets(t *testing.T, source string, data []byte, forbidden ...string) {
	t.Helper()
	for _, value := range forbidden {
		if value != "" && bytes.Contains(data, []byte(value)) {
			t.Fatalf("%s leaked sensitive material", source)
		}
	}
}

type platformHarness struct {
	manager        *account.Manager
	store          *memoryRuntimeStore
	resolver       *countingCredentialResolver
	builder        *countingRouteBuilder
	publicFactory  *countingPublicFactory
	publicRegistry *publicconn.Registry
	catalog        *catalog.Service
}

func newPlatformHarness(t *testing.T) *platformHarness {
	t.Helper()
	resolver := &countingCredentialResolver{payload: []byte(`{"schemaVersion":1,"venue":"binance","authKind":"api_key_secret","metadata":{},"secrets":{"apiKey":"key-` + credentialCanary + `","apiSecret":"secret-` + credentialCanary + `"}}`)}
	return newPlatformHarnessWithResolver(t, resolver)
}

func newPlatformHarnessWithResolver(t *testing.T, resolver *countingCredentialResolver) *platformHarness {
	t.Helper()
	service, err := catalog.NewService(time.Minute, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	publicFactory := &countingPublicFactory{}
	publicRegistry, err := publicconn.NewRegistry(publicFactory, routeprofile.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	builder := &countingRouteBuilder{}
	runtimeStore := newMemoryRuntimeStore()
	harness := &platformHarness{
		store: runtimeStore, resolver: resolver, builder: builder, publicFactory: publicFactory,
		publicRegistry: publicRegistry, catalog: service,
	}
	harness.manager = harness.buildManager(t)
	t.Cleanup(func() {
		_ = harness.manager.Close()
		_ = publicRegistry.Close()
	})
	return harness
}

func (harness *platformHarness) buildManager(t *testing.T) *account.Manager {
	t.Helper()
	manager, err := account.NewManager(context.Background(), nil, account.Options{
		GatewayCatalog: harness.catalog, HTTPClient: &http.Client{Transport: denyNetworkTransport{}},
		RuntimeGroupBuilder: harness.builder, CredentialResolver: harness.resolver, RuntimeStore: harness.store,
		PublicConnectors: harness.publicRegistry, RequirePublicConnectors: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func (harness *platformHarness) restart(t *testing.T) {
	t.Helper()
	if err := harness.manager.Close(); err != nil {
		t.Fatalf("close manager before restart: %v", err)
	}
	publicRegistry, err := publicconn.NewRegistry(harness.publicFactory, routeprofile.DefaultRegistry())
	if err != nil {
		t.Fatalf("rebuild public connector registry: %v", err)
	}
	harness.publicRegistry = publicRegistry
	harness.manager = harness.buildManager(t)
}

func (harness *platformHarness) promote(t *testing.T, pending store.PendingSnapshot) *account.RuntimeGroup {
	t.Helper()
	harness.store.register(pending.DesiredSnapshot)
	group, err := harness.manager.BuildCandidate(t.Context(), pending)
	if err != nil {
		t.Fatalf("BuildCandidate: %v", err)
	}
	if _, err := harness.manager.PromoteCandidate(t.Context(), group); err != nil {
		t.Fatalf("PromoteCandidate: %v", err)
	}
	return group
}

func platformSnapshot(t *testing.T, seed string, accountID domain.AccountID, revision, generation uint64, rawRef string, routes ...store.RouteDefinition) store.PendingSnapshot {
	t.Helper()
	ref, err := secretstore.ParseOpaqueRef(rawRef)
	if err != nil {
		t.Fatal(err)
	}
	operation, err := secretstore.ParseOperationID(uuid.NewSHA1(uuid.NameSpaceOID, []byte("operation-"+seed)).String())
	if err != nil {
		t.Fatal(err)
	}
	fence := domain.AccountFence{AccountID: accountID, DesiredStateRevision: revision, RuntimeGeneration: generation, CredentialVersion: generation}
	return store.PendingSnapshot{DesiredSnapshot: store.DesiredSnapshot{
		SnapshotID: uuid.NewSHA1(uuid.NameSpaceOID, []byte("snapshot-"+seed)), OperationID: operation,
		AccountID: accountID, Venue: "binance", NativeEnvironment: "live", SecretRef: ref,
		Fence: fence, RequestedState: store.DesiredActive, Routes: routes, CreatedAt: time.Now().UTC(),
	}, OperationState: store.OperationCandidateReady}
}

func spotRoute() store.RouteDefinition {
	return store.RouteDefinition{RouteID: "spot", Product: "spot", RouteProfileID: "binance.spot.v1", RequestedState: store.RouteActive}
}

func perpRoute() store.RouteDefinition {
	return store.RouteDefinition{RouteID: "perp-usdt", Product: "perp", SettlementAsset: "USDT", RouteProfileID: "binance.perp.usdt.v1", RequestedState: store.RouteActive}
}

func alternateOpaqueRef() secretstore.OpaqueRef {
	ref, err := secretstore.ParseOpaqueRef("AgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgI")
	if err != nil {
		panic(err)
	}
	return ref
}

type memoryRuntimeStore struct {
	mu       sync.Mutex
	pending  map[secretstore.OperationID]store.DesiredSnapshot
	active   map[domain.AccountID]store.ActiveSnapshot
	states   []store.RuntimeState
	retired  map[secretstore.OpaqueRef]bool
	promotes int
}

func newMemoryRuntimeStore() *memoryRuntimeStore {
	return &memoryRuntimeStore{
		pending: make(map[secretstore.OperationID]store.DesiredSnapshot),
		active:  make(map[domain.AccountID]store.ActiveSnapshot),
		retired: make(map[secretstore.OpaqueRef]bool),
	}
}

func (runtimeStore *memoryRuntimeStore) register(snapshot store.DesiredSnapshot) {
	runtimeStore.mu.Lock()
	defer runtimeStore.mu.Unlock()
	runtimeStore.pending[snapshot.OperationID] = snapshot
}

func (runtimeStore *memoryRuntimeStore) PromotePending(_ context.Context, request store.PromotionRequest) (store.PromotionResult, error) {
	runtimeStore.mu.Lock()
	defer runtimeStore.mu.Unlock()
	pending, ok := runtimeStore.pending[request.OperationID]
	if !ok || pending.AccountID != request.AccountID {
		return store.PromotionResult{}, store.ErrOperationConflict
	}
	current, hasActive := runtimeStore.active[request.AccountID]
	if (!hasActive && request.ExpectedActiveFence != (domain.AccountFence{})) || (hasActive && request.ExpectedActiveFence != current.Fence) {
		return store.PromotionResult{}, store.ErrPromotionFenceConflict
	}
	active := store.ActiveSnapshot{DesiredSnapshot: pending, PromotedAt: time.Now().UTC()}
	result := store.PromotionResult{Active: active}
	if hasActive {
		retiring := current
		result.Retiring = &retiring
	}
	runtimeStore.active[request.AccountID] = active
	runtimeStore.promotes++
	return result, nil
}

func (*memoryRuntimeStore) LookupRotationFinalization(context.Context, store.RotationFinalizationRequest) (store.RotationFinalizationResult, error) {
	return store.RotationFinalizationResult{}, store.ErrNotFound
}

func (*memoryRuntimeStore) FinalizeRotation(context.Context, store.RotationFinalizationRequest) (store.RotationFinalizationResult, error) {
	return store.RotationFinalizationResult{}, store.ErrOperationConflict
}

func (*memoryRuntimeStore) RetireAccount(context.Context, store.RetirementRequest) (store.RetirementResult, error) {
	return store.RetirementResult{}, store.ErrOperationConflict
}

func (runtimeStore *memoryRuntimeStore) ListRuntimeStateForRecovery(context.Context) ([]store.RuntimeState, error) {
	runtimeStore.mu.Lock()
	defer runtimeStore.mu.Unlock()
	return append([]store.RuntimeState(nil), runtimeStore.states...), nil
}

func (runtimeStore *memoryRuntimeStore) IsSecretReferenceRetired(_ context.Context, _ secretstore.Scope, ref secretstore.OpaqueRef) (bool, error) {
	runtimeStore.mu.Lock()
	defer runtimeStore.mu.Unlock()
	return runtimeStore.retired[ref], nil
}

func (runtimeStore *memoryRuntimeStore) setRetired(ref secretstore.OpaqueRef) {
	runtimeStore.mu.Lock()
	defer runtimeStore.mu.Unlock()
	runtimeStore.retired[ref] = true
}

func (runtimeStore *memoryRuntimeStore) setStates(states []store.RuntimeState) {
	runtimeStore.mu.Lock()
	defer runtimeStore.mu.Unlock()
	runtimeStore.states = append([]store.RuntimeState(nil), states...)
}

type countingCredentialResolver struct {
	payload      []byte
	delegate     account.ScopedCredentialResolver
	checkCalls   atomic.Int64
	resolveCalls atomic.Int64
}

func (resolver *countingCredentialResolver) Check(ctx context.Context) error {
	resolver.checkCalls.Add(1)
	if resolver.delegate != nil {
		return resolver.delegate.Check(ctx)
	}
	return nil
}

func (resolver *countingCredentialResolver) Resolve(ctx context.Context, ref secretstore.OpaqueRef) ([]byte, error) {
	resolver.resolveCalls.Add(1)
	if resolver.delegate != nil {
		return resolver.delegate.Resolve(ctx, ref)
	}
	return append([]byte(nil), resolver.payload...), nil
}

type countingRouteBuilder struct {
	mu           sync.Mutex
	failRoute    domain.RouteID
	failErr      error
	buildHook    func(store.DesiredSnapshot)
	builds       atomic.Int64
	privateCalls atomic.Int64
}

func (builder *countingRouteBuilder) BuildRoute(_ context.Context, snapshot store.DesiredSnapshot, route store.RouteDefinition, _ *credential.Decoded) (account.CandidateRoute, error) {
	builder.builds.Add(1)
	builder.mu.Lock()
	failRoute, failErr, hook := builder.failRoute, builder.failErr, builder.buildHook
	builder.mu.Unlock()
	if hook != nil {
		hook(snapshot)
	}
	handle := &offlineCandidateRoute{product: exchange.Product(route.Product), privateCalls: &builder.privateCalls}
	if route.Product == "spot" {
		handle.spot = &offlineSpotClient{privateCalls: &builder.privateCalls}
	} else {
		handle.perp = &offlinePerpClient{privateCalls: &builder.privateCalls}
	}
	if route.RouteID == failRoute {
		handle.probeErr = failErr
	}
	return handle, nil
}

func (builder *countingRouteBuilder) setFailRoute(routeID domain.RouteID, err error) {
	builder.mu.Lock()
	defer builder.mu.Unlock()
	builder.failRoute, builder.failErr = routeID, err
}

func (builder *countingRouteBuilder) setBuildHook(hook func(store.DesiredSnapshot)) {
	builder.mu.Lock()
	defer builder.mu.Unlock()
	builder.buildHook = hook
}

type offlineCandidateRoute struct {
	product      exchange.Product
	spot         exchange.SpotClient
	perp         exchange.PerpClient
	probeErr     error
	privateCalls *atomic.Int64
	closed       atomic.Bool
}

func (route *offlineCandidateRoute) Probe(context.Context) error { return route.probeErr }
func (route *offlineCandidateRoute) Close() error                { route.closed.Store(true); return nil }
func (route *offlineCandidateRoute) SpotClient() (exchange.SpotClient, bool) {
	return route.spot, route.spot != nil
}
func (route *offlineCandidateRoute) PerpClient() (exchange.PerpClient, bool) {
	return route.perp, route.perp != nil
}
func (route *offlineCandidateRoute) OrderClient() (exchange.OrderREST, bool) {
	if route.spot != nil {
		return route.spot, true
	}
	if route.perp != nil {
		return route.perp, true
	}
	return nil, false
}
func (*offlineCandidateRoute) ExactLookupReady() bool { return true }

type offlineSpotClient struct {
	exchange.SpotClient
	privateCalls *atomic.Int64
}

func (client *offlineSpotClient) PlaceOrder(context.Context, exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error) {
	client.privateCalls.Add(1)
	return exchange.OrderAcknowledgement{}, errors.New("offline private call forbidden")
}
func (client *offlineSpotClient) CancelOrder(context.Context, exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error) {
	client.privateCalls.Add(1)
	return exchange.OrderAcknowledgement{}, errors.New("offline private call forbidden")
}
func (*offlineSpotClient) LookupExactOrder(context.Context, exchange.ExactOrderLookupRequest) (exchange.ExactOrderLookupResult, error) {
	return exchange.ExactOrderLookupResult{}, nil
}

type offlinePerpClient struct {
	exchange.PerpClient
	privateCalls *atomic.Int64
}

func (client *offlinePerpClient) PlaceOrder(context.Context, exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error) {
	client.privateCalls.Add(1)
	return exchange.OrderAcknowledgement{}, errors.New("offline private call forbidden")
}
func (client *offlinePerpClient) CancelOrder(context.Context, exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error) {
	client.privateCalls.Add(1)
	return exchange.OrderAcknowledgement{}, errors.New("offline private call forbidden")
}
func (*offlinePerpClient) LookupExactOrder(context.Context, exchange.ExactOrderLookupRequest) (exchange.ExactOrderLookupResult, error) {
	return exchange.ExactOrderLookupResult{}, nil
}

type countingPublicFactory struct {
	builds    atomic.Int64
	refreshes atomic.Int64
}

func (factory *countingPublicFactory) Build(_ context.Context, key routeprofile.Key) (publicconn.Connector, error) {
	factory.builds.Add(1)
	product, settlement := exchange.ProductSpot, ""
	if strings.Contains(key.RouteProfileID, ".perp.") {
		product, settlement = exchange.ProductPerp, "USDT"
	}
	return &offlinePublicConnector{instrument: platformInstrument(product, settlement), refreshes: &factory.refreshes}, nil
}

type offlinePublicConnector struct {
	instrument exchange.Instrument
	refreshes  *atomic.Int64
}

func (connector *offlinePublicConnector) Refresh(context.Context) ([]exchange.Instrument, error) {
	connector.refreshes.Add(1)
	return []exchange.Instrument{connector.instrument}, nil
}
func (*offlinePublicConnector) Close() error { return nil }

func platformInstrument(product exchange.Product, settlement string) exchange.Instrument {
	return exchange.Instrument{
		Identity: exchange.InstrumentIdentity{
			Venue: exchange.VenueBinance, Product: product, Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT",
			SettleAsset: settlement, NativeSymbol: "BTCUSDT", NativeID: "BTCUSDT",
		},
		Rules: exchange.InstrumentRules{
			PriceIncrement: decimal.RequireFromString("0.01"), QuantityIncrement: decimal.RequireFromString("0.001"), MinQuantity: decimal.RequireFromString("0.001"),
		},
	}
}

type denyNetworkTransport struct{}

func (denyNetworkTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("network access forbidden")
}

type integrationPool struct{ closed atomic.Bool }

func (*integrationPool) Ping(context.Context) error { return nil }
func (pool *integrationPool) Close()                { pool.closed.Store(true) }

type integrationListener struct{ closed atomic.Bool }

func (*integrationListener) Accept() (net.Conn, error) { return nil, errors.New("listener closed") }
func (listener *integrationListener) Close() error {
	listener.closed.Store(true)
	return nil
}
func (*integrationListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)} }

type integrationOrderCoordinator struct {
	mu       sync.Mutex
	accounts []domain.AccountID
}

func (coordinator *integrationOrderCoordinator) Start(_ context.Context, accountIDs []domain.AccountID) error {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	coordinator.accounts = append([]domain.AccountID(nil), accountIDs...)
	return nil
}
func (*integrationOrderCoordinator) Close(context.Context) error { return nil }
func (coordinator *integrationOrderCoordinator) startedAccounts() []domain.AccountID {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	return append([]domain.AccountID(nil), coordinator.accounts...)
}

func mustOperationalRef(t *testing.T, value string) secret.Ref {
	t.Helper()
	ref, err := secret.ParseRef(value)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

var _ account.CandidateClientRoute = (*offlineCandidateRoute)(nil)
var _ health.Dependency = (*integrationPool)(nil)

func (pool *integrationPool) Healthy() bool { return !pool.closed.Load() }
