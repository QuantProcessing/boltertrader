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

type controlledCloseHandle struct {
	mu        sync.Mutex
	started   chan struct{}
	release   chan struct{}
	startOnce sync.Once
	errors    []error
	calls     int
}

func (handle *controlledCloseHandle) Probe(context.Context) error { return nil }

func (handle *controlledCloseHandle) Close() error {
	handle.mu.Lock()
	call := handle.calls
	handle.calls++
	handle.mu.Unlock()
	if handle.started != nil {
		handle.startOnce.Do(func() { close(handle.started) })
	}
	if handle.release != nil {
		<-handle.release
	}
	if call < len(handle.errors) {
		return handle.errors[call]
	}
	return nil
}

func (handle *controlledCloseHandle) callCount() int {
	handle.mu.Lock()
	defer handle.mu.Unlock()
	return handle.calls
}

type candidateBuilderStub struct {
	failRoute domain.RouteID
	handles   map[domain.RouteID]*candidateHandleStub
	decoded   *credential.Decoded
	buildHook func(store.DesiredSnapshot)
}

func (builder *candidateBuilderStub) BuildRoute(_ context.Context, snapshot store.DesiredSnapshot, route store.RouteDefinition, decoded *credential.Decoded) (CandidateRoute, error) {
	if builder.buildHook != nil {
		builder.buildHook(snapshot)
	}
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
	mu                    sync.Mutex
	next                  store.ActiveSnapshot
	states                []store.RuntimeState
	calls                 int
	retireCalls           int
	err                   error
	rotationRequest       *store.RotationFinalizationRequest
	rotationResult        *store.RotationFinalizationResult
	rotationLookupCalls   int
	rotationFinalizeCalls int
	rotationErr           error
	retireBarrier         *retireCallBarrier
}

type retireCallBarrier struct {
	mu      sync.Mutex
	want    int
	arrived int
	ready   chan struct{}
	release chan struct{}
}

func (barrier *retireCallBarrier) wait() {
	barrier.mu.Lock()
	barrier.arrived++
	if barrier.arrived == barrier.want {
		close(barrier.ready)
	}
	barrier.mu.Unlock()
	<-barrier.release
}

func (runtimeStore *runtimeStateStoreStub) LookupRotationFinalization(_ context.Context, request store.RotationFinalizationRequest) (store.RotationFinalizationResult, error) {
	runtimeStore.mu.Lock()
	defer runtimeStore.mu.Unlock()
	runtimeStore.rotationLookupCalls++
	if runtimeStore.rotationResult == nil || runtimeStore.rotationRequest == nil {
		return store.RotationFinalizationResult{}, store.ErrNotFound
	}
	if !sameRotationFinalizationRequest(*runtimeStore.rotationRequest, request) {
		return store.RotationFinalizationResult{}, store.ErrOperationConflict
	}
	return *runtimeStore.rotationResult, nil
}

func (runtimeStore *runtimeStateStoreStub) FinalizeRotation(_ context.Context, request store.RotationFinalizationRequest) (store.RotationFinalizationResult, error) {
	runtimeStore.mu.Lock()
	defer runtimeStore.mu.Unlock()
	runtimeStore.rotationFinalizeCalls++
	if runtimeStore.rotationErr != nil {
		return store.RotationFinalizationResult{}, runtimeStore.rotationErr
	}
	if runtimeStore.rotationResult != nil && runtimeStore.rotationRequest != nil {
		if !sameRotationFinalizationRequest(*runtimeStore.rotationRequest, request) {
			return store.RotationFinalizationResult{}, store.ErrOperationConflict
		}
		return *runtimeStore.rotationResult, nil
	}
	result := store.RotationFinalizationResult{
		OperationID: request.OperationID, AccountID: request.AccountID,
		ActiveFence: request.ExpectedActiveFence, RetiringFence: request.ExpectedRetiringFence,
		TombstoneRevision: request.ExpectedActiveFence.DesiredStateRevision,
		RetiringState:     "retired", SecretReferenceState: "tombstoned", CompletedAt: time.Now().UTC(),
	}
	requestCopy, resultCopy := request, result
	runtimeStore.rotationRequest, runtimeStore.rotationResult = &requestCopy, &resultCopy
	return result, nil
}

func sameRotationFinalizationRequest(first, second store.RotationFinalizationRequest) bool {
	firstDigest, firstErr := first.CanonicalDigest()
	secondDigest, secondErr := second.CanonicalDigest()
	return firstErr == nil && secondErr == nil && first.OperationID == second.OperationID && firstDigest == secondDigest
}

func (runtimeStore *runtimeStateStoreStub) RetireAccount(_ context.Context, request store.RetirementRequest) (store.RetirementResult, error) {
	runtimeStore.mu.Lock()
	runtimeStore.retireCalls++
	err := runtimeStore.err
	barrier := runtimeStore.retireBarrier
	runtimeStore.mu.Unlock()
	if barrier != nil {
		barrier.wait()
	}
	if err != nil {
		return store.RetirementResult{}, err
	}
	return store.RetirementResult{
		OperationID: request.OperationID, AccountID: request.AccountID, TombstoneRevision: request.TombstoneRevision,
		Tombstoned: true, CompletedAt: time.Now(),
	}, nil
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

func (runtimeStore *runtimeStateStoreStub) IsSecretReferenceRetired(context.Context, secretstore.Scope, secretstore.OpaqueRef) (bool, error) {
	return false, nil
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
	routeID, admission, err := manager.ActiveRoute(pending.AccountID, "perp", "USDT")
	if err != nil || routeID != "perp-usdt" || admission != group.Admission {
		t.Fatalf("active route=%q admission=%p err=%v", routeID, admission, err)
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

func TestRetirementClosesAdmissionUntilLeaseReleaseAndFencesWrites(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	builder := &candidateBuilderStub{handles: map[domain.RouteID]*candidateHandleStub{}}
	manager := newRuntimeGroupTestManager(builder)
	active := runtimeGroupPending(t)
	group, err := manager.BuildCandidate(context.Background(), active)
	if err != nil || promoteGroup(manager, group) != nil {
		t.Fatalf("seed active err=%v", err)
	}
	lease, err := group.Admission.Acquire(domain.WriteFence{AccountFence: active.Fence, RouteID: "spot"})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()

	operationID, _ := secretstore.ParseOperationID("018f47a8-2578-7d3c-9d7d-5e4f4a445c46")
	done := make(chan error, 1)
	go func() {
		_, retireErr := manager.RetireRuntime(ctx, store.RetirementRequest{
			OperationID: operationID, AccountID: active.AccountID, ExpectedActiveFence: active.Fence,
			TombstoneRevision: active.Fence.DesiredStateRevision + 1,
			Scope:             secretstore.Scope{Mode: secretstore.ModeTest, Namespace: "boltertrader/test", KeyVersion: "v1"},
		})
		done <- retireErr
	}()

	deadline := time.Now().Add(time.Second)
	for {
		probe, probeErr := group.Admission.Acquire(domain.WriteFence{AccountFence: active.Fence, RouteID: "spot"})
		if errors.Is(probeErr, ErrRuntimeBlocked) {
			break
		}
		if probeErr != nil {
			t.Fatalf("probe admission err=%v", probeErr)
		}
		probe.Release()
		if time.Now().After(deadline) {
			t.Fatal("retirement did not close admission")
		}
	}
	select {
	case err := <-done:
		t.Fatalf("retirement crossed in-flight handoff: %v", err)
	default:
	}

	lease.Release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatalf("wait for retirement: %v", ctx.Err())
	}
	if manager.ActiveSnapshot(active.AccountID) != nil {
		t.Fatal("retired runtime remained published")
	}
	if !errors.Is(manager.CheckWriteFence(WriteFence{
		AccountID: active.AccountID, DesiredStateRevision: active.Fence.DesiredStateRevision,
		RuntimeGeneration: active.Fence.RuntimeGeneration, CredentialVersion: active.Fence.CredentialVersion,
	}), ErrStaleWriteFence) {
		t.Fatal("retired generation still passed the write fence")
	}
	for routeID, handle := range builder.handles {
		if !handle.closed {
			t.Fatalf("route %s was not closed", routeID)
		}
	}
}

func TestRetirementRemainsFailClosedWhileVenueBlockersExist(t *testing.T) {
	builder := &candidateBuilderStub{handles: map[domain.RouteID]*candidateHandleStub{}}
	manager := newRuntimeGroupTestManager(builder)
	active := runtimeGroupPending(t)
	group, err := manager.BuildCandidate(context.Background(), active)
	if err != nil || promoteGroup(manager, group) != nil {
		t.Fatalf("seed active err=%v", err)
	}
	manager.lifecycle.(*fakeRuntimeLifecycle).blockers = RuntimeBlockers{OpenOrders: 1}
	operationID, _ := secretstore.ParseOperationID("018f47a8-2578-7d3c-9d7d-5e4f4a445c47")
	_, err = manager.RetireRuntime(context.Background(), store.RetirementRequest{
		OperationID: operationID, AccountID: active.AccountID, ExpectedActiveFence: active.Fence,
		TombstoneRevision: active.Fence.DesiredStateRevision + 1,
		Scope:             secretstore.Scope{Mode: secretstore.ModeTest, Namespace: "boltertrader/test", KeyVersion: "v1"},
	})
	if !errors.Is(err, ErrRuntimeBlocked) {
		t.Fatalf("retirement blocker err=%v", err)
	}
	if manager.ActiveSnapshot(active.AccountID) == nil {
		t.Fatal("blocked retirement destroyed reconciliation runtime")
	}
	if _, err := group.Admission.Acquire(domain.WriteFence{AccountFence: active.Fence, RouteID: "spot"}); !errors.Is(err, ErrRuntimeBlocked) {
		t.Fatalf("blocked retirement reopened write admission: %v", err)
	}
	if manager.runtimeStore.(*runtimeStateStoreStub).retireCalls != 0 {
		t.Fatal("blocked retirement reached durable tombstone transaction")
	}
}

func TestRetirementCloseFailureRetainsOwnershipForExactReplay(t *testing.T) {
	builder := &candidateBuilderStub{handles: map[domain.RouteID]*candidateHandleStub{}}
	manager := newRuntimeGroupTestManager(builder)
	active := runtimeGroupPending(t)
	group, err := manager.BuildCandidate(context.Background(), active)
	if err != nil || promoteGroup(manager, group) != nil {
		t.Fatalf("seed active err=%v", err)
	}
	handle := &controlledCloseHandle{errors: []error{errors.New("close failed")}}
	group.Routes["spot"].Handle = handle
	operationID, _ := secretstore.ParseOperationID("018f47a8-2578-7d3c-9d7d-5e4f4a445c48")
	request := store.RetirementRequest{
		OperationID: operationID, AccountID: active.AccountID, ExpectedActiveFence: active.Fence,
		TombstoneRevision: active.Fence.DesiredStateRevision + 1,
		Scope:             secretstore.Scope{Mode: secretstore.ModeTest, Namespace: "boltertrader/test", KeyVersion: "v1"},
	}

	if _, err := manager.RetireRuntime(context.Background(), request); !errors.Is(err, ErrRuntimeCleanupFailed) {
		t.Fatalf("first retirement err=%v", err)
	}
	if manager.ActiveSnapshot(active.AccountID) != nil {
		t.Fatal("durably retired runtime remained published after cleanup failure")
	}
	manager.groupMu.Lock()
	retained := manager.groups[active.AccountID]
	manager.groupMu.Unlock()
	if retained == nil || retained.activeGroup != group || group.State == GroupDestroyed {
		t.Fatalf("cleanup failure lost runtime ownership: runtime=%p state=%q", retained, group.State)
	}
	if _, err := group.Admission.Acquire(domain.WriteFence{AccountFence: active.Fence, RouteID: "spot"}); !errors.Is(err, ErrRuntimeBlocked) {
		t.Fatalf("cleanup failure reopened writes: %v", err)
	}
	if calls := manager.runtimeStore.(*runtimeStateStoreStub).retireCalls; calls != 1 {
		t.Fatalf("durable retirement calls=%d want=1", calls)
	}

	result, err := manager.RetireRuntime(context.Background(), request)
	if err != nil || !result.Tombstoned {
		t.Fatalf("replayed retirement=%+v err=%v", result, err)
	}
	if calls := handle.callCount(); calls != 2 {
		t.Fatalf("route close calls=%d want=2", calls)
	}
	if calls := manager.runtimeStore.(*runtimeStateStoreStub).retireCalls; calls != 1 {
		t.Fatalf("exact replay repeated durable transition: calls=%d", calls)
	}
	manager.groupMu.Lock()
	_, retainedAfterReplay := manager.groups[active.AccountID]
	manager.groupMu.Unlock()
	if retainedAfterReplay || group.State != GroupDestroyed {
		t.Fatalf("successful replay retained=%t state=%q", retainedAfterReplay, group.State)
	}
}

func TestConcurrentRetirementBeforeLocalCleanupConvergesOnOneOwnershipRecord(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	builder := &candidateBuilderStub{handles: map[domain.RouteID]*candidateHandleStub{}}
	manager := newRuntimeGroupTestManager(builder)
	active := runtimeGroupPending(t)
	group, err := manager.BuildCandidate(ctx, active)
	if err != nil || promoteGroup(manager, group) != nil {
		t.Fatalf("seed active err=%v", err)
	}
	handles := make(map[domain.RouteID]*controlledCloseHandle, len(group.Routes))
	for routeID := range group.Routes {
		handle := &controlledCloseHandle{}
		handles[routeID] = handle
		group.Routes[routeID].Handle = handle
	}
	barrier := &retireCallBarrier{want: 2, ready: make(chan struct{}), release: make(chan struct{})}
	manager.runtimeStore.(*runtimeStateStoreStub).retireBarrier = barrier
	operationID, _ := secretstore.ParseOperationID("018f47a8-2578-7d3c-9d7d-5e4f4a445c49")
	request := store.RetirementRequest{
		OperationID: operationID, AccountID: active.AccountID, ExpectedActiveFence: active.Fence,
		TombstoneRevision: active.Fence.DesiredStateRevision + 1,
		Scope:             secretstore.Scope{Mode: secretstore.ModeTest, Namespace: "boltertrader/test", KeyVersion: "v1"},
	}
	type retirementCall struct {
		result store.RetirementResult
		err    error
	}
	calls := []chan retirementCall{make(chan retirementCall, 1), make(chan retirementCall, 1)}
	for _, call := range calls {
		go func(result chan<- retirementCall) {
			retired, err := manager.RetireRuntime(ctx, request)
			result <- retirementCall{result: retired, err: err}
		}(call)
	}
	select {
	case <-barrier.ready:
	case <-ctx.Done():
		t.Fatalf("concurrent retirements did not reach durable store: %v", ctx.Err())
	}
	close(barrier.release)
	for index, call := range calls {
		select {
		case retired := <-call:
			if retired.err != nil || !retired.result.Tombstoned {
				t.Fatalf("retirement %d result=%+v err=%v", index+1, retired.result, retired.err)
			}
		case <-ctx.Done():
			t.Fatalf("wait for retirement %d: %v", index+1, ctx.Err())
		}
	}
	for routeID, handle := range handles {
		if calls := handle.callCount(); calls != 1 {
			t.Fatalf("route %s close calls=%d want=1", routeID, calls)
		}
	}
	manager.groupMu.Lock()
	_, retained := manager.groups[active.AccountID]
	manager.groupMu.Unlock()
	if retained {
		t.Fatal("successful concurrent retirement retained local ownership")
	}
}

func TestRotationFinalizationWaitsForExactRetiringLeaseAndDestroysOnlyThatGeneration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	manager, groups := runtimeRotationGroups(t, 3)
	retiring, otherRetiring, active := groups[1], groups[0], groups[2]
	retiring.Admission.reopen()
	lease, err := retiring.Admission.Acquire(domain.WriteFence{AccountFence: retiring.Snapshot.Fence, RouteID: "spot"})
	if err != nil {
		t.Fatal(err)
	}
	request := rotationRuntimeRequest(active, retiring)
	done := make(chan error, 1)
	go func() {
		_, finalizeErr := manager.FinalizeRotation(ctx, request)
		done <- finalizeErr
	}()
	waitForClosedAdmission(t, retiring)
	select {
	case err := <-done:
		t.Fatalf("rotation finalization crossed in-flight retiring lease: %v", err)
	default:
	}
	lease.Release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatalf("wait for rotation finalization: %v", ctx.Err())
	}
	if retiring.State != GroupDestroyed {
		t.Fatalf("exact retiring state=%q", retiring.State)
	}
	if otherRetiring.State != GroupRetiring || active.State != GroupActive {
		t.Fatalf("other retiring=%q active=%q", otherRetiring.State, active.State)
	}
	manager.groupMu.Lock()
	runtime := manager.groups[active.Snapshot.AccountID]
	_, exactExists := runtime.retiring[retiring.Snapshot.Fence.RuntimeGeneration]
	_, otherExists := runtime.retiring[otherRetiring.Snapshot.Fence.RuntimeGeneration]
	manager.groupMu.Unlock()
	if exactExists || !otherExists {
		t.Fatalf("retiring membership exact=%t other=%t", exactExists, otherExists)
	}
}

func TestRotationFinalizationWrongFenceAndMalformedProofDoNotCloseOrDestroyGeneration(t *testing.T) {
	t.Run("wrong active fence", func(t *testing.T) {
		manager, groups := runtimeRotationGroups(t, 2)
		retiring, active := groups[0], groups[1]
		retiring.Admission.reopen()
		request := rotationRuntimeRequest(active, retiring)
		request.ExpectedActiveFence.DesiredStateRevision++
		request.ExpectedActiveFence.RuntimeGeneration++
		request.ExpectedActiveFence.CredentialVersion++
		request.ExpectedRetiringFence.DesiredStateRevision++
		request.ExpectedRetiringFence.RuntimeGeneration++
		request.ExpectedRetiringFence.CredentialVersion++
		if _, err := manager.FinalizeRotation(context.Background(), request); !errors.Is(err, ErrStaleWriteFence) {
			t.Fatalf("wrong fence err=%v", err)
		}
		lease, err := retiring.Admission.Acquire(domain.WriteFence{AccountFence: retiring.Snapshot.Fence, RouteID: "spot"})
		if err != nil {
			t.Fatalf("wrong fence closed exact retiring admission: %v", err)
		}
		lease.Release()
		if retiring.State != GroupRetiring {
			t.Fatalf("wrong fence destroyed retiring group: %q", retiring.State)
		}
	})

	t.Run("malformed durable proof", func(t *testing.T) {
		manager, groups := runtimeRotationGroups(t, 2)
		retiring, active := groups[0], groups[1]
		request := rotationRuntimeRequest(active, retiring)
		bad := store.RotationFinalizationResult{
			OperationID: request.OperationID, AccountID: request.AccountID,
			ActiveFence: request.ExpectedActiveFence, RetiringFence: request.ExpectedRetiringFence,
			TombstoneRevision: request.ExpectedActiveFence.DesiredStateRevision,
			RetiringState:     "retired", SecretReferenceState: "not-tombstoned", CompletedAt: time.Now().UTC(),
		}
		runtimeStore := manager.runtimeStore.(*runtimeStateStoreStub)
		runtimeStore.rotationRequest, runtimeStore.rotationResult = &request, &bad
		if _, err := manager.FinalizeRotation(context.Background(), request); !errors.Is(err, ErrCandidateMismatch) {
			t.Fatalf("malformed proof err=%v", err)
		}
		if retiring.State != GroupRetiring {
			t.Fatalf("malformed proof destroyed retiring group: %q", retiring.State)
		}
	})
}

func TestRotationFinalizationStoreFailureLeavesExactGroupClosedAndUndestroyed(t *testing.T) {
	manager, groups := runtimeRotationGroups(t, 2)
	retiring, active := groups[0], groups[1]
	retiring.Admission.reopen()
	runtimeStore := manager.runtimeStore.(*runtimeStateStoreStub)
	runtimeStore.rotationErr = store.ErrHandoffDrainBlocked
	_, err := manager.FinalizeRotation(context.Background(), rotationRuntimeRequest(active, retiring))
	if !errors.Is(err, store.ErrHandoffDrainBlocked) {
		t.Fatalf("finalization err=%v", err)
	}
	if retiring.State != GroupRetiring {
		t.Fatalf("failed finalization destroyed group: %q", retiring.State)
	}
	if _, err := retiring.Admission.Acquire(domain.WriteFence{AccountFence: retiring.Snapshot.Fence, RouteID: "spot"}); !errors.Is(err, ErrRuntimeBlocked) {
		t.Fatalf("failed finalization reopened admission: %v", err)
	}
}

func TestRotationFinalizationDurableReplayCleansLocalGroupWithoutSecondCommit(t *testing.T) {
	manager, groups := runtimeRotationGroups(t, 2)
	retiring, active := groups[0], groups[1]
	request := rotationRuntimeRequest(active, retiring)
	runtimeStore := manager.runtimeStore.(*runtimeStateStoreStub)
	result := store.RotationFinalizationResult{
		OperationID: request.OperationID, AccountID: request.AccountID,
		ActiveFence: request.ExpectedActiveFence, RetiringFence: request.ExpectedRetiringFence,
		TombstoneRevision: request.ExpectedActiveFence.DesiredStateRevision,
		RetiringState:     "retired", SecretReferenceState: "tombstoned", CompletedAt: time.Now().UTC(),
	}
	runtimeStore.rotationRequest, runtimeStore.rotationResult = &request, &result
	replayed, err := manager.FinalizeRotation(context.Background(), request)
	if err != nil || replayed != result {
		t.Fatalf("replay=%+v err=%v", replayed, err)
	}
	if runtimeStore.rotationLookupCalls != 1 || runtimeStore.rotationFinalizeCalls != 0 || retiring.State != GroupDestroyed {
		t.Fatalf("lookup=%d finalize=%d retiring=%q", runtimeStore.rotationLookupCalls, runtimeStore.rotationFinalizeCalls, retiring.State)
	}
}

func TestConcurrentRotationFinalizationReplayWaitsForSingleConfirmedCleanup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	manager, groups := runtimeRotationGroups(t, 2)
	retiring, active := groups[0], groups[1]
	handle := &controlledCloseHandle{started: make(chan struct{}), release: make(chan struct{})}
	retiring.Routes["spot"].Handle = handle
	request := rotationRuntimeRequest(active, retiring)

	first := make(chan error, 1)
	go func() {
		_, err := manager.FinalizeRotation(ctx, request)
		first <- err
	}()
	select {
	case <-handle.started:
	case <-ctx.Done():
		t.Fatalf("first cleanup did not reach route close: %v", ctx.Err())
	}

	second := make(chan error, 1)
	go func() {
		_, err := manager.FinalizeRotation(ctx, request)
		second <- err
	}()
	waitForCleanupWaiter(t, ctx, retiring)
	select {
	case err := <-second:
		close(handle.release)
		t.Fatalf("replay returned before cleanup completed: %v", err)
	default:
	}
	close(handle.release)
	for index, result := range []<-chan error{first, second} {
		select {
		case err := <-result:
			if err != nil {
				t.Fatalf("finalization %d: %v", index+1, err)
			}
		case <-ctx.Done():
			t.Fatalf("wait for finalization %d: %v", index+1, ctx.Err())
		}
	}
	if calls := handle.callCount(); calls != 1 {
		t.Fatalf("route close calls=%d want=1", calls)
	}
	if retiring.State != GroupDestroyed {
		t.Fatalf("retiring state=%q", retiring.State)
	}
}

func waitForCleanupWaiter(t *testing.T, ctx context.Context, group *RuntimeGroup) {
	t.Helper()
	for {
		group.cleanupMu.Lock()
		waiters := 0
		if group.cleanup != nil {
			waiters = group.cleanup.waiters
		}
		group.cleanupMu.Unlock()
		if waiters > 0 {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("cleanup replay did not enter waiter path: %v", ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
}

func TestRotationFinalizationCloseFailureRetainsGroupForReplayRetry(t *testing.T) {
	manager, groups := runtimeRotationGroups(t, 2)
	retiring, active := groups[0], groups[1]
	handle := &controlledCloseHandle{errors: []error{errors.New("close failed")}}
	retiring.Routes["spot"].Handle = handle
	request := rotationRuntimeRequest(active, retiring)

	if _, err := manager.FinalizeRotation(context.Background(), request); !errors.Is(err, ErrRuntimeCleanupFailed) {
		t.Fatalf("first finalization err=%v", err)
	}
	manager.groupMu.Lock()
	retained := manager.groups[active.Snapshot.AccountID].retiring[retiring.Snapshot.Fence.RuntimeGeneration] == retiring
	manager.groupMu.Unlock()
	if !retained || retiring.State != GroupRetiring {
		t.Fatalf("failed cleanup retained=%t state=%q", retained, retiring.State)
	}
	if _, err := manager.FinalizeRotation(context.Background(), request); err != nil {
		t.Fatalf("replayed finalization: %v", err)
	}
	if calls := handle.callCount(); calls != 2 {
		t.Fatalf("route close calls=%d want=2", calls)
	}
	manager.groupMu.Lock()
	_, retained = manager.groups[active.Snapshot.AccountID].retiring[retiring.Snapshot.Fence.RuntimeGeneration]
	manager.groupMu.Unlock()
	if retained || retiring.State != GroupDestroyed {
		t.Fatalf("successful retry retained=%t state=%q", retained, retiring.State)
	}
}

func TestRotationFinalizationAfterRestartWithoutLocalRetiringGroupUsesDurableStore(t *testing.T) {
	retiring := runtimeGroupPending(t).DesiredSnapshot
	active := runtimeGroupPending(t).DesiredSnapshot
	active.SnapshotID = uuid.MustParse("018f47a8-2578-7d3c-9d7d-5e4f4a445c56")
	active.OperationID, _ = secretstore.ParseOperationID("018f47a8-2578-7d3c-9d7d-5e4f4a445c55")
	active.SecretRef, _ = secretstore.ParseOpaqueRef("AgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgI")
	active.Fence.DesiredStateRevision++
	active.Fence.RuntimeGeneration++
	active.Fence.CredentialVersion++
	builtSnapshots := make([]uuid.UUID, 0, len(active.Routes))
	builder := &candidateBuilderStub{handles: map[domain.RouteID]*candidateHandleStub{}}
	builder.buildHook = func(snapshot store.DesiredSnapshot) {
		builtSnapshots = append(builtSnapshots, snapshot.SnapshotID)
	}
	manager := newRuntimeGroupTestManager(builder)
	runtimeStore := manager.runtimeStore.(*runtimeStateStoreStub)
	runtimeStore.states = []store.RuntimeState{{
		AccountID: active.AccountID,
		Active:    &store.ActiveSnapshot{DesiredSnapshot: active, PromotedAt: time.Now().UTC()},
		Retiring:  []store.ActiveSnapshot{{DesiredSnapshot: retiring, PromotedAt: time.Now().Add(-time.Minute).UTC()}},
	}}
	if err := manager.RestoreRuntimeState(context.Background()); err != nil {
		t.Fatal(err)
	}
	published := manager.ActiveSnapshot(active.AccountID)
	if published == nil || published.Fence != active.Fence {
		t.Fatalf("restored active=%+v", published)
	}
	for _, snapshotID := range builtSnapshots {
		if snapshotID != active.SnapshotID {
			t.Fatalf("restore rehydrated non-active snapshot %s", snapshotID)
		}
	}
	if len(builtSnapshots) != len(active.Routes) {
		t.Fatalf("active route builds=%d want=%d", len(builtSnapshots), len(active.Routes))
	}
	manager.groupMu.Lock()
	localRetiring := len(manager.groups[active.AccountID].retiring)
	manager.groupMu.Unlock()
	if localRetiring != 0 {
		t.Fatalf("restore rehydrated %d retiring groups", localRetiring)
	}
	request := store.RotationFinalizationRequest{
		OperationID: active.OperationID, AccountID: active.AccountID,
		ExpectedActiveFence: active.Fence, ExpectedRetiringFence: retiring.Fence,
		Scope: secretstore.Scope{Mode: secretstore.ModeTest, Namespace: "boltertrader/test", KeyVersion: "v1"},
	}
	result, err := manager.FinalizeRotation(context.Background(), request)
	if err != nil || result.OperationID != active.OperationID || result.RetiringFence != retiring.Fence {
		t.Fatalf("restart finalization=%+v err=%v", result, err)
	}
	if runtimeStore.rotationFinalizeCalls != 1 {
		t.Fatal("restart finalization did not reach durable store")
	}
}

func waitForClosedAdmission(t *testing.T, group *RuntimeGroup) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		lease, err := group.Admission.Acquire(domain.WriteFence{AccountFence: group.Snapshot.Fence, RouteID: "spot"})
		if errors.Is(err, ErrRuntimeBlocked) {
			return
		}
		if err != nil {
			t.Fatalf("admission probe: %v", err)
		}
		lease.Release()
	}
	t.Fatal("retiring admission did not close")
}

func runtimeRotationGroups(t *testing.T, count int) (*Manager, []*RuntimeGroup) {
	t.Helper()
	manager := newRuntimeGroupTestManager(&candidateBuilderStub{handles: map[domain.RouteID]*candidateHandleStub{}})
	base := runtimeGroupPending(t)
	operationIDs := []string{
		"018f47a8-2578-7d3c-9d7d-5e4f4a445c50", "018f47a8-2578-7d3c-9d7d-5e4f4a445c51", "018f47a8-2578-7d3c-9d7d-5e4f4a445c52",
	}
	snapshotIDs := []string{
		"018f47a8-2578-7d3c-9d7d-5e4f4a445c60", "018f47a8-2578-7d3c-9d7d-5e4f4a445c61", "018f47a8-2578-7d3c-9d7d-5e4f4a445c62",
	}
	groups := make([]*RuntimeGroup, 0, count)
	for index := 0; index < count; index++ {
		pending := base
		pending.OperationID, _ = secretstore.ParseOperationID(operationIDs[index])
		pending.SnapshotID = uuid.MustParse(snapshotIDs[index])
		pending.Fence.DesiredStateRevision += uint64(index)
		pending.Fence.RuntimeGeneration += uint64(index)
		pending.Fence.CredentialVersion += uint64(index)
		group, err := manager.BuildCandidate(context.Background(), pending)
		if err != nil || promoteGroup(manager, group) != nil {
			t.Fatalf("generation %d promotion err=%v", index, err)
		}
		groups = append(groups, group)
	}
	return manager, groups
}

func rotationRuntimeRequest(active, retiring *RuntimeGroup) store.RotationFinalizationRequest {
	return store.RotationFinalizationRequest{
		OperationID: active.Snapshot.OperationID, AccountID: active.Snapshot.AccountID,
		ExpectedActiveFence: active.Snapshot.Fence, ExpectedRetiringFence: retiring.Snapshot.Fence,
		Scope: secretstore.Scope{Mode: secretstore.ModeTest, Namespace: "boltertrader/test", KeyVersion: "v1"},
	}
}

func TestRestorePublishesDurableActiveBeforeRebuildingPending(t *testing.T) {
	active := runtimeGroupPending(t)
	pending := runtimeGroupPending(t)
	pending.SnapshotID = uuid.MustParse("018f47a8-2578-7d3c-9d7d-5e4f4a445c36")
	pending.OperationID, _ = secretstore.ParseOperationID("018f47a8-2578-7d3c-9d7d-5e4f4a445c34")
	pending.Fence.DesiredStateRevision++
	pending.Fence.RuntimeGeneration++

	builder := &candidateBuilderStub{handles: map[domain.RouteID]*candidateHandleStub{}}
	manager := newRuntimeGroupTestManager(builder)
	manager.runtimeStore.(*runtimeStateStoreStub).states = []store.RuntimeState{{
		AccountID: active.AccountID,
		Active:    &store.ActiveSnapshot{DesiredSnapshot: active.DesiredSnapshot, PromotedAt: time.Now()},
		Pending:   &pending,
	}}
	builder.buildHook = func(snapshot store.DesiredSnapshot) {
		if snapshot.SnapshotID != pending.SnapshotID {
			return
		}
		published := manager.ActiveSnapshot(active.AccountID)
		if published == nil || published.Fence != active.Fence {
			t.Fatalf("active snapshot was not published before pending rebuild: %+v", published)
		}
	}

	if err := manager.RestoreRuntimeState(context.Background()); err != nil {
		t.Fatal(err)
	}
	published := manager.ActiveSnapshot(active.AccountID)
	if published == nil || published.Fence != active.Fence {
		t.Fatalf("restored active=%+v", published)
	}
}

func newRuntimeGroupTestManager(builder RuntimeGroupBuilder) *Manager {
	return &Manager{
		entries: map[domain.AccountID]*entry{}, runtimes: map[domain.AccountID]RuntimeAccount{},
		groups: map[domain.AccountID]*accountRuntime{}, groupBuilder: builder,
		profileRegistry: NewProfileRegistry(nil), runtimeMode: RuntimeModeTest,
		lifecycle:          &fakeRuntimeLifecycle{},
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
