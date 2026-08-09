package account

import (
	"context"
	"errors"
	"testing"

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
	material  []byte
}

func (builder *candidateBuilderStub) BuildRoute(_ context.Context, _ store.DesiredSnapshot, route store.RouteDefinition, material []byte) (CandidateRoute, error) {
	builder.material = append(builder.material[:0], material...)
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
	if err := manager.PublishPromoted(context.Background(), group, pending.Fence); err != nil {
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
	if err := manager.PublishPromoted(context.Background(), group, first.Fence); err != nil {
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
	resolver := &candidateResolverStub{value: []byte("credential-bundle-canary")}
	manager.credentialResolver = resolver
	if _, err := manager.BuildCandidate(context.Background(), runtimeGroupPending(t)); err != nil {
		t.Fatal(err)
	}
	if resolver.calls != 1 {
		t.Fatalf("resolver calls=%d want=1", resolver.calls)
	}
	if string(builder.material) != "credential-bundle-canary" {
		t.Fatal("route builder did not receive decoded bundle bytes")
	}
}

func TestPromotionWaitsForOldGenerationHandoffLease(t *testing.T) {
	builder := &candidateBuilderStub{handles: map[domain.RouteID]*candidateHandleStub{}}
	manager := newRuntimeGroupTestManager(builder)
	first := runtimeGroupPending(t)
	active, err := manager.BuildCandidate(context.Background(), first)
	if err != nil || manager.PublishPromoted(context.Background(), active, first.Fence) != nil {
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
	go func() { done <- manager.PublishPromoted(context.Background(), candidate, next.Fence) }()
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

func newRuntimeGroupTestManager(builder RuntimeGroupBuilder) *Manager {
	return &Manager{
		entries: map[domain.AccountID]*entry{}, runtimes: map[domain.AccountID]RuntimeAccount{},
		groups: map[domain.AccountID]*accountRuntime{}, groupBuilder: builder,
		profileRegistry: NewProfileRegistry(nil), runtimeMode: RuntimeModeTest,
	}
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
