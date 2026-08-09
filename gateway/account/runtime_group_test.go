package account

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/credential"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/gateway/store"
	"github.com/QuantProcessing/boltertrader/secretstore"
	"github.com/google/uuid"
)

type candidateHandleStub struct {
	probeErr error
	closed   bool
}

func (handle *candidateHandleStub) Probe(context.Context) error { return handle.probeErr }
func (handle *candidateHandleStub) Close() error                { handle.closed = true; return nil }

type candidateBuilderStub struct {
	failRoute domain.RouteID
	handles   map[domain.RouteID]*candidateHandleStub
	decoded   *credential.Decoded
}

func (builder *candidateBuilderStub) BuildRoute(_ context.Context, _ store.DesiredSnapshot, route store.RouteDefinition, decoded *credential.Decoded) (CandidateRoute, error) {
	builder.decoded = decoded
	handle := &candidateHandleStub{}
	if route.RouteID == builder.failRoute {
		handle.probeErr = errors.New("authentication failed")
	}
	builder.handles[route.RouteID] = handle
	return handle, nil
}

type candidateResolverStub struct {
	calls int
	value []byte
}

type runtimeStateStoreStub struct {
	mu     sync.Mutex
	next   store.ActiveSnapshot
	states []store.RuntimeState
	calls  int
	err    error
}

func (runtimeStore *runtimeStateStoreStub) PromotePending(_ context.Context, request store.PromotionRequest) (store.PromotionResult, error) {
	runtimeStore.mu.Lock()
	defer runtimeStore.mu.Unlock()
	runtimeStore.calls++
	if runtimeStore.err != nil {
		return store.PromotionResult{}, runtimeStore.err
	}
	if runtimeStore.next.AccountID != request.AccountID || runtimeStore.next.OperationID != request.OperationID {
		return store.PromotionResult{}, store.ErrOperationConflict
	}
	return store.PromotionResult{Active: runtimeStore.next}, nil
}

func (runtimeStore *runtimeStateStoreStub) ListRuntimeStateForRecovery(context.Context) ([]store.RuntimeState, error) {
	return runtimeStore.states, nil
}

func (resolver *candidateResolverStub) Check(context.Context) error { return nil }
func (resolver *candidateResolverStub) Resolve(context.Context, secretstore.OpaqueRef) ([]byte, error) {
	resolver.calls++
	return append([]byte(nil), resolver.value...), nil
}

func TestCandidatePromotesSpotAndPerpAsOneGeneration(t *testing.T) {
	builder := &candidateBuilderStub{handles: map[domain.RouteID]*candidateHandleStub{}}
	manager := newRuntimeGroupTestManager(builder)
	pending := runtimeGroupPending(t)
	group, err := manager.BuildCandidate(context.Background(), pending)
	if err != nil {
		t.Fatal(err)
	}
	if got := manager.ActiveSnapshot(pending.AccountID); got != nil {
		t.Fatal("candidate became active before durable promotion")
	}
	if err := promoteGroup(manager, group); err != nil {
		t.Fatal(err)
	}
	active := manager.ActiveSnapshot(pending.AccountID)
	if active == nil || len(active.Routes) != 2 || active.Fence != pending.Fence {
		t.Fatalf("active=%+v", active)
	}
}

func TestOneFailedRouteDestroysWholeCandidateAndPreservesActive(t *testing.T) {
	builder := &candidateBuilderStub{handles: map[domain.RouteID]*candidateHandleStub{}}
	manager := newRuntimeGroupTestManager(builder)
	first := runtimeGroupPending(t)
	group, err := manager.BuildCandidate(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	if err := promoteGroup(manager, group); err != nil {
		t.Fatal(err)
	}
	before := manager.ActiveSnapshot(first.AccountID)

	builder.failRoute = "perp-usdt"
	next := runtimeGroupPending(t)
	next.SnapshotID = uuid.MustParse("018f47a8-2578-7d3c-9d7d-5e4f4a445c36")
	next.Fence.RuntimeGeneration++
	next.Fence.DesiredStateRevision++
	if _, err := manager.BuildCandidate(context.Background(), next); !errors.Is(err, ErrCandidateFailed) {
		t.Fatalf("err=%v", err)
	}
	after := manager.ActiveSnapshot(first.AccountID)
	if after == nil || after.Fence != before.Fence {
		t.Fatalf("before=%+v after=%+v", before, after)
	}
	if !builder.handles["spot"].closed || !builder.handles["perp-usdt"].closed {
		t.Fatal("failed candidate handles were not all destroyed")
	}
}

func TestCandidateResolvesCredentialBundleOnceForAllRoutes(t *testing.T) {
	builder := &candidateBuilderStub{handles: map[domain.RouteID]*candidateHandleStub{}}
	manager := newRuntimeGroupTestManager(builder)
	resolver := &candidateResolverStub{value: []byte(`{"schemaVersion":1,"venue":"binance","authKind":"api_key_secret","metadata":{},"secrets":{"apiKey":"key-canary","apiSecret":"secret-canary"}}`)}
	manager.credentialResolver = resolver
	if _, err := manager.BuildCandidate(context.Background(), runtimeGroupPending(t)); err != nil {
		t.Fatal(err)
	}
	if resolver.calls != 1 {
		t.Fatalf("resolver calls=%d want=1", resolver.calls)
	}
	if builder.decoded == nil || builder.decoded.AuthKind != credential.AuthAPIKeySecret {
		t.Fatalf("route builder decoded=%+v", builder.decoded)
	}
	credentials, ok := builder.decoded.Credentials.(*credential.APIKeySecret)
	if !ok || credentials.APIKey != "" || credentials.APISecret != "" {
		t.Fatalf("decoded credentials were not cleared after construction: %+v", builder.decoded)
	}
}

func TestPromotionWaitsForOldGenerationHandoffLease(t *testing.T) {
	builder := &candidateBuilderStub{handles: map[domain.RouteID]*candidateHandleStub{}}
	manager := newRuntimeGroupTestManager(builder)
	first := runtimeGroupPending(t)
	active, err := manager.BuildCandidate(context.Background(), first)
	if err != nil || promoteGroup(manager, active) != nil {
		t.Fatalf("seed active err=%v", err)
	}
	lease, err := active.Admission.Acquire(domain.WriteFence{AccountFence: first.Fence, RouteID: "spot"})
	if err != nil {
		t.Fatal(err)
	}
	next := runtimeGroupPending(t)
	next.SnapshotID = uuid.MustParse("018f47a8-2578-7d3c-9d7d-5e4f4a445c36")
	next.Fence.DesiredStateRevision++
	next.Fence.RuntimeGeneration++
	candidate, err := manager.BuildCandidate(context.Background(), next)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- promoteGroup(manager, candidate) }()
	select {
	case err := <-done:
		t.Fatalf("promotion crossed in-flight handoff: %v", err)
	default:
	}
	lease.Release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestPromotionFailureReopensActiveGenerationAdmission(t *testing.T) {
	builder := &candidateBuilderStub{handles: map[domain.RouteID]*candidateHandleStub{}}
	manager := newRuntimeGroupTestManager(builder)
	first := runtimeGroupPending(t)
	active, err := manager.BuildCandidate(context.Background(), first)
	if err != nil || promoteGroup(manager, active) != nil {
		t.Fatalf("seed active err=%v", err)
	}

	next := runtimeGroupPending(t)
	next.OperationID, _ = secretstore.ParseOperationID("018f47a8-2578-7d3c-9d7d-5e4f4a445c44")
	next.SnapshotID = uuid.MustParse("018f47a8-2578-7d3c-9d7d-5e4f4a445c45")
	next.Fence.DesiredStateRevision++
	next.Fence.RuntimeGeneration++
	candidate, err := manager.BuildCandidate(context.Background(), next)
	if err != nil {
		t.Fatal(err)
	}
	runtimeStore := manager.runtimeStore.(*runtimeStateStoreStub)
	runtimeStore.err = store.ErrPromotionFenceConflict
	if _, err := manager.PromoteCandidate(context.Background(), candidate); !errors.Is(err, store.ErrPromotionFenceConflict) {
		t.Fatalf("promotion err=%v", err)
	}
	lease, err := active.Admission.Acquire(domain.WriteFence{AccountFence: first.Fence, RouteID: "spot"})
	if err != nil {
		t.Fatalf("active admission remained closed after failed promotion: %v", err)
	}
	lease.Release()
}

func newRuntimeGroupTestManager(builder RuntimeGroupBuilder) *Manager {
	return &Manager{
		entries: map[domain.AccountID]*entry{}, runtimes: map[domain.AccountID]RuntimeAccount{},
		groups: map[domain.AccountID]*accountRuntime{}, groupBuilder: builder,
		profileRegistry: NewProfileRegistry(nil), runtimeMode: RuntimeModeTest,
		credentialResolver: &candidateResolverStub{value: []byte(`{"schemaVersion":1,"venue":"binance","authKind":"api_key_secret","metadata":{},"secrets":{"apiKey":"key","apiSecret":"secret"}}`)},
		runtimeStore:       &runtimeStateStoreStub{},
	}
}

func promoteGroup(manager *Manager, group *RuntimeGroup) error {
	runtimeStore := manager.runtimeStore.(*runtimeStateStoreStub)
	runtimeStore.mu.Lock()
	runtimeStore.next = store.ActiveSnapshot{DesiredSnapshot: group.Snapshot, PromotedAt: time.Now()}
	runtimeStore.mu.Unlock()
	_, err := manager.PromoteCandidate(context.Background(), group)
	return err
}

func runtimeGroupPending(t *testing.T) store.PendingSnapshot {
	t.Helper()
	op, _ := secretstore.ParseOperationID("018f47a8-2578-7d3c-9d7d-5e4f4a445c33")
	ref, _ := secretstore.ParseOpaqueRef("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	fence := domain.AccountFence{AccountID: "account-a", DesiredStateRevision: 2, RuntimeGeneration: 2, CredentialVersion: 1}
	return store.PendingSnapshot{DesiredSnapshot: store.DesiredSnapshot{
		SnapshotID: uuid.MustParse("018f47a8-2578-7d3c-9d7d-5e4f4a445c35"), OperationID: op,
		AccountID: fence.AccountID, Venue: "binance", NativeEnvironment: "demo", SecretRef: ref,
		Fence: fence, RequestedState: store.DesiredActive,
		Routes: []store.RouteDefinition{
			{RouteID: "spot", Product: "spot", RouteProfileID: "binance.spot.demo.v1", RequestedState: store.RouteActive},
			{RouteID: "perp-usdt", Product: "perp", SettlementAsset: "USDT", RouteProfileID: "binance.perp.usdt.demo.v1", RequestedState: store.RouteActive},
		},
	}, OperationState: store.OperationCandidateReady}
}
