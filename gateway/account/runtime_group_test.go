package account

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/credential"
	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/catalog"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/gateway/publicconn"
	"github.com/QuantProcessing/boltertrader/gateway/store"
	"github.com/QuantProcessing/boltertrader/routeprofile"
	"github.com/QuantProcessing/boltertrader/secretstore"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

type candidateHandleStub struct {
	probeErr error
	closed   bool
}

func (handle *candidateHandleStub) Probe(context.Context) error { return handle.probeErr }
func (handle *candidateHandleStub) Close() error                { handle.closed = true; return nil }

type runtimeSpotClientStub struct{ exchange.SpotClient }
type runtimePerpClientStub struct {
	exchange.PerpClient
	positions    []exchange.Position
	positionsErr error
}

func (*runtimeSpotClientStub) LookupExactOrder(context.Context, exchange.ExactOrderLookupRequest) (exchange.ExactOrderLookupResult, error) {
	return exchange.ExactOrderLookupResult{}, nil
}
func (*runtimePerpClientStub) LookupExactOrder(context.Context, exchange.ExactOrderLookupRequest) (exchange.ExactOrderLookupResult, error) {
	return exchange.ExactOrderLookupResult{}, nil
}
func (client *runtimePerpClientStub) Positions(context.Context, exchange.PositionsRequest) ([]exchange.Position, error) {
	return append([]exchange.Position(nil), client.positions...), client.positionsErr
}

type candidateClientHandleStub struct {
	product exchange.Product
	spot    exchange.SpotClient
	perp    exchange.PerpClient
}

func (*candidateClientHandleStub) Probe(context.Context) error { return nil }
func (*candidateClientHandleStub) Close() error                { return nil }
func (handle *candidateClientHandleStub) SpotClient() (exchange.SpotClient, bool) {
	return handle.spot, handle.spot != nil
}
func (handle *candidateClientHandleStub) PerpClient() (exchange.PerpClient, bool) {
	return handle.perp, handle.perp != nil
}
func (handle *candidateClientHandleStub) OrderClient() (exchange.OrderREST, bool) {
	if handle.spot != nil {
		return handle.spot, true
	}
	if handle.perp != nil {
		return handle.perp, true
	}
	return nil, false
}
func (*candidateClientHandleStub) ExactLookupReady() bool { return true }

type candidateClientBuilderStub struct {
	failSnapshot uuid.UUID
	buildHook    func(store.DesiredSnapshot)
	buildCounts  map[uuid.UUID]int
}

func (builder candidateClientBuilderStub) BuildRoute(_ context.Context, snapshot store.DesiredSnapshot, route store.RouteDefinition, _ *credential.Decoded) (CandidateRoute, error) {
	if builder.buildCounts != nil {
		builder.buildCounts[snapshot.SnapshotID]++
	}
	if builder.buildHook != nil {
		builder.buildHook(snapshot)
	}
	if snapshot.SnapshotID == builder.failSnapshot {
		return nil, errors.New("candidate rebuild failed")
	}
	switch route.Product {
	case string(exchange.ProductSpot):
		return &candidateClientHandleStub{product: exchange.ProductSpot, spot: &runtimeSpotClientStub{}}, nil
	case string(exchange.ProductPerp):
		return &candidateClientHandleStub{product: exchange.ProductPerp, perp: &runtimePerpClientStub{}}, nil
	default:
		return nil, errors.New("unsupported product")
	}
}

type runtimePublicConnector struct{ instrument exchange.Instrument }

func (connector runtimePublicConnector) Refresh(context.Context) ([]exchange.Instrument, error) {
	return []exchange.Instrument{connector.instrument}, nil
}
func (runtimePublicConnector) Close() error { return nil }

type runtimePublicFactory struct {
	instruments map[routeprofile.Key]exchange.Instrument
}

func (factory runtimePublicFactory) Build(_ context.Context, key routeprofile.Key) (publicconn.Connector, error) {
	instrument, ok := factory.instruments[key]
	if !ok {
		return nil, errors.New("missing public connector")
	}
	return runtimePublicConnector{instrument: instrument}, nil
}

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

type failingCredentialResolver struct{ err error }

func (*failingCredentialResolver) Check(context.Context) error { return nil }
func (resolver *failingCredentialResolver) Resolve(context.Context, secretstore.OpaqueRef) ([]byte, error) {
	return nil, resolver.err
}

type runtimeObserverStub struct {
	mu           sync.Mutex
	registered   []domain.AccountID
	unregistered []domain.AccountID
	onRegister   func(domain.AccountID)
	onUnregister func(domain.AccountID)
	registerErr  error
	registerErrs map[domain.AccountID]error
}

func (observer *runtimeObserverStub) RegisterAccount(accountID domain.AccountID) error {
	if observer.onRegister != nil {
		observer.onRegister(accountID)
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	observer.registered = append(observer.registered, accountID)
	if err := observer.registerErrs[accountID]; err != nil {
		return err
	}
	return observer.registerErr
}

func (observer *runtimeObserverStub) UnregisterAccount(accountID domain.AccountID) {
	if observer.onUnregister != nil {
		observer.onUnregister(accountID)
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	observer.unregistered = append(observer.unregistered, accountID)
}

func (observer *runtimeObserverStub) registrations() []domain.AccountID {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	return append([]domain.AccountID(nil), observer.registered...)
}

func (observer *runtimeObserverStub) unregistrations() []domain.AccountID {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	return append([]domain.AccountID(nil), observer.unregistered...)
}

type blockingPublicLease struct {
	started    chan struct{}
	release    chan struct{}
	instrument exchange.Instrument
	once       sync.Once
}

func (*blockingPublicLease) ConnectorID() string                 { return "blocking-public" }
func (*blockingPublicLease) Snapshot() (catalog.Snapshot, error) { return catalog.Snapshot{}, nil }
func (*blockingPublicLease) Status() catalog.Status              { return catalog.Status{ReadReady: true} }
func (*blockingPublicLease) Release()                            {}
func (lease *blockingPublicLease) Resolve(context.Context, domain.InstrumentID) (exchange.Instrument, error) {
	lease.once.Do(func() { close(lease.started) })
	<-lease.release
	return lease.instrument, nil
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
	retireRequest         *store.RetirementRequest
	retireResult          *store.RetirementResult
	updateStateOnRetire   bool
}

type retireCallBarrier struct {
	mu      sync.Mutex
	want    int
	arrived int
	ready   chan struct{}
	release chan struct{}
}

type runtimeLifecycleStub struct {
	mu       sync.Mutex
	blockers RuntimeBlockers
	calls    int
}

func (*runtimeLifecycleStub) Validate(context.Context, DesiredRuntime) error { return nil }
func (*runtimeLifecycleStub) Activate(context.Context, DesiredRuntime) error { return nil }
func (*runtimeLifecycleStub) Deactivate(context.Context, domain.AccountID) error {
	return nil
}
func (lifecycle *runtimeLifecycleStub) Blockers(context.Context, domain.AccountID) (RuntimeBlockers, error) {
	lifecycle.mu.Lock()
	defer lifecycle.mu.Unlock()
	lifecycle.calls++
	return lifecycle.blockers, nil
}

func (lifecycle *runtimeLifecycleStub) blockerCalls() int {
	lifecycle.mu.Lock()
	defer lifecycle.mu.Unlock()
	return lifecycle.calls
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
	replayRequest := runtimeStore.retireRequest
	replayResult := runtimeStore.retireResult
	runtimeStore.mu.Unlock()
	if barrier != nil {
		barrier.wait()
	}
	if err != nil {
		return store.RetirementResult{}, err
	}
	if replayRequest != nil && replayResult != nil {
		requestDigest, requestErr := request.CanonicalDigest()
		replayDigest, replayErr := replayRequest.CanonicalDigest()
		if requestErr != nil || replayErr != nil || request.OperationID != replayRequest.OperationID || requestDigest != replayDigest {
			return store.RetirementResult{}, store.ErrOperationConflict
		}
		return *replayResult, nil
	}
	result := store.RetirementResult{
		OperationID: request.OperationID, AccountID: request.AccountID, TombstoneRevision: request.TombstoneRevision,
		Tombstoned: true, CompletedAt: time.Now(),
	}
	runtimeStore.mu.Lock()
	requestCopy, resultCopy := request, result
	runtimeStore.retireRequest, runtimeStore.retireResult = &requestCopy, &resultCopy
	if runtimeStore.updateStateOnRetire {
		for index := range runtimeStore.states {
			if runtimeStore.states[index].AccountID == request.AccountID {
				runtimeStore.states[index].Active = nil
				runtimeStore.states[index].Pending = nil
				runtimeStore.states[index].Retiring = nil
				runtimeStore.states[index].Tombstoned = true
				runtimeStore.states[index].TombstoneRevision = request.TombstoneRevision
			}
		}
	}
	runtimeStore.mu.Unlock()
	return result, nil
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
	runtimeStore.mu.Lock()
	defer runtimeStore.mu.Unlock()
	return append([]store.RuntimeState(nil), runtimeStore.states...), nil
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

func TestPromotedRuntimeExposesActiveRouteClientsAndResources(t *testing.T) {
	manager := newRuntimeGroupTestManager(candidateClientBuilderStub{})
	registry, err := publicconn.NewRegistry(runtimePublicFactory{instruments: map[routeprofile.Key]exchange.Instrument{
		{Venue: "binance", RouteProfileID: "binance.spot.v1"}:      accountTestInstrument(exchange.VenueBinance, exchange.ProductSpot, "BTC-USDT", "BTCUSDT", ""),
		{Venue: "binance", RouteProfileID: "binance.perp.usdt.v1"}: accountTestInstrument(exchange.VenueBinance, exchange.ProductPerp, "BTC-USDT", "BTCUSDT", "USDT"),
	}}, routeprofile.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	manager.publicConnectors = registry
	pending := runtimeGroupPending(t)
	group, err := manager.BuildCandidate(context.Background(), pending)
	if err != nil {
		t.Fatal(err)
	}
	if err := promoteGroup(manager, group); err != nil {
		t.Fatal(err)
	}

	resources := manager.Accounts()
	if len(resources) != 2 {
		t.Fatalf("resources=%+v", resources)
	}
	products := map[exchange.Product]bool{}
	for _, resource := range resources {
		products[resource.Product] = resource.ReadReady && resource.WriteReady
	}
	if !products[exchange.ProductSpot] || !products[exchange.ProductPerp] {
		t.Fatalf("route resources=%+v", resources)
	}
	capabilities, err := manager.Capabilities(pending.AccountID)
	if err != nil || len(capabilities) != 2 || capabilities[0].Product != "perp" || capabilities[1].Product != "spot" {
		t.Fatalf("capabilities=%+v err=%v", capabilities, err)
	}
	if client, err := manager.Spot(pending.AccountID); err != nil || client == nil {
		t.Fatalf("spot client=%v err=%v", client, err)
	}
	if client, err := manager.Perp(pending.AccountID); err != nil || client == nil {
		t.Fatalf("perp client=%v err=%v", client, err)
	}
	routeID, orders, admission, err := manager.ActiveRouteClient(pending.AccountID, "perp", "USDT")
	if err != nil || routeID != "perp-usdt" || orders == nil || admission != group.Admission {
		t.Fatalf("route=%q orders=%v admission=%p err=%v", routeID, orders, admission, err)
	}
}

func TestRuntimePositionBlockersUsesAuthoritativeActivePerpRoutesAndRedactsFailures(t *testing.T) {
	manager := newRuntimeGroupTestManager(candidateClientBuilderStub{})
	pending := runtimeGroupPending(t)
	group, err := manager.BuildCandidate(context.Background(), pending)
	if err != nil || promoteGroup(manager, group) != nil {
		t.Fatalf("promote runtime err=%v", err)
	}
	clientRoute := group.Routes["perp-usdt"].Handle.(CandidateClientRoute)
	perp, ok := clientRoute.PerpClient()
	if !ok {
		t.Fatal("perp client unavailable")
	}
	client := perp.(*runtimePerpClientStub)
	client.positions = []exchange.Position{
		{NativeSymbol: "BTCUSDT", Quantity: decimal.NewFromInt(1)},
		{NativeSymbol: "ETHUSDT", Quantity: decimal.Zero},
	}
	positions, err := manager.RuntimePositionBlockers(context.Background(), pending.AccountID)
	if err != nil || positions != 1 {
		t.Fatalf("position blockers=%d err=%v", positions, err)
	}

	client.positionsErr = errors.New("position-secret-canary")
	if _, err := manager.RuntimePositionBlockers(context.Background(), pending.AccountID); err == nil || strings.Contains(err.Error(), "position-secret-canary") {
		t.Fatalf("position blocker error=%v", err)
	}
}

func TestPromotedRuntimePrivateStreamBarrierControlsDynamicWriteReadiness(t *testing.T) {
	manager := newRuntimeGroupTestManager(candidateClientBuilderStub{})
	registry, err := publicconn.NewRegistry(runtimePublicFactory{instruments: map[routeprofile.Key]exchange.Instrument{
		{Venue: "binance", RouteProfileID: "binance.spot.v1"}:      accountTestInstrument(exchange.VenueBinance, exchange.ProductSpot, "BTC-USDT", "BTCUSDT", ""),
		{Venue: "binance", RouteProfileID: "binance.perp.usdt.v1"}: accountTestInstrument(exchange.VenueBinance, exchange.ProductPerp, "BTC-USDT", "BTCUSDT", "USDT"),
	}}, routeprofile.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	manager.publicConnectors = registry
	pending := runtimeGroupPending(t)
	group, err := manager.BuildCandidate(context.Background(), pending)
	if err != nil {
		t.Fatal(err)
	}
	if err := promoteGroup(manager, group); err != nil {
		t.Fatal(err)
	}

	if err := manager.SetPrivateStreamState(pending.AccountID, false, ReasonPrivateStreamGap); err != nil {
		t.Fatal(err)
	}
	if status, err := manager.RouteStatus(pending.AccountID, "spot", ""); err != nil || status.WriteReady || status.Reason != ReasonPrivateStreamGap {
		t.Fatalf("gapped route status=%+v err=%v", status, err)
	}
	if status, err := manager.RecoveryStatus(pending.AccountID); err != nil || !status.WriteReady || status.Reason != "" {
		t.Fatalf("recovery status=%+v err=%v", status, err)
	}
	if err := manager.SetPrivateStreamState(pending.AccountID, true, ""); err != nil {
		t.Fatal(err)
	}
	if status, err := manager.Status(pending.AccountID); err != nil || status.WriteReady || status.Reason != ReasonPrivateStreamStarting {
		t.Fatalf("reconnected-before-barrier status=%+v err=%v", status, err)
	}
	if err := manager.ObservePrivateStreamBarrier(pending.AccountID, "snapshot-1", 42); err != nil {
		t.Fatal(err)
	}
	if status, err := manager.Status(pending.AccountID); err != nil || !status.WriteReady || status.Reason != "" {
		t.Fatalf("barrier-complete status=%+v err=%v", status, err)
	}
}

func TestPromotedRuntimePrivateStreamReadinessIsolatedByGenerationAndRoute(t *testing.T) {
	manager := newRuntimeGroupTestManager(candidateClientBuilderStub{})
	registry, err := publicconn.NewRegistry(runtimePublicFactory{instruments: map[routeprofile.Key]exchange.Instrument{
		{Venue: "binance", RouteProfileID: "binance.spot.v1"}:      accountTestInstrument(exchange.VenueBinance, exchange.ProductSpot, "BTC-USDT", "BTCUSDT", ""),
		{Venue: "binance", RouteProfileID: "binance.perp.usdt.v1"}: accountTestInstrument(exchange.VenueBinance, exchange.ProductPerp, "BTC-USDT", "BTCUSDT", "USDT"),
	}}, routeprofile.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	manager.publicConnectors = registry
	manager.streamSupervision.Store(true)
	pending := runtimeGroupPending(t)
	group, err := manager.BuildCandidate(context.Background(), pending)
	if err != nil || promoteGroup(manager, group) != nil {
		t.Fatalf("promote runtime: %v", err)
	}
	spotFence := domain.WriteFence{AccountFence: pending.Fence, RouteID: "spot"}
	perpFence := domain.WriteFence{AccountFence: pending.Fence, RouteID: "perp-usdt"}

	if err := manager.SetPrivateStreamRouteState(spotFence, true, ""); err != nil {
		t.Fatal(err)
	}
	if err := manager.ObservePrivateStreamRouteBarrier(spotFence, "spot-snapshot", 1); err != nil {
		t.Fatal(err)
	}
	if status, err := manager.RouteStatus(pending.AccountID, "spot", ""); err != nil || !status.WriteReady || status.Reason != "" {
		t.Fatalf("spot status after its barrier=%+v err=%v", status, err)
	}
	if status, err := manager.RouteStatus(pending.AccountID, "perp", "USDT"); err != nil || status.WriteReady || status.Reason != ReasonPrivateStreamStarting {
		t.Fatalf("perp status before its barrier=%+v err=%v", status, err)
	}

	if err := manager.SetPrivateStreamRouteState(perpFence, true, ""); err != nil {
		t.Fatal(err)
	}
	if err := manager.ObservePrivateStreamRouteBarrier(perpFence, "perp-snapshot", 1); err != nil {
		t.Fatal(err)
	}
	if err := manager.SetPrivateStreamRouteState(spotFence, false, ReasonPrivateStreamGap); err != nil {
		t.Fatal(err)
	}
	if status, err := manager.RouteStatus(pending.AccountID, "spot", ""); err != nil || status.WriteReady || status.Reason != ReasonPrivateStreamGap {
		t.Fatalf("spot status after gap=%+v err=%v", status, err)
	}
	if status, err := manager.RouteStatus(pending.AccountID, "perp", "USDT"); err != nil || !status.WriteReady || status.Reason != "" {
		t.Fatalf("unrelated perp status after spot gap=%+v err=%v", status, err)
	}

	stale := spotFence
	stale.RuntimeGeneration--
	if err := manager.SetPrivateStreamRouteState(stale, false, ReasonPrivateStreamGap); !errors.Is(err, ErrStaleWriteFence) {
		t.Fatalf("stale stream callback err=%v", err)
	}
	if status, err := manager.RouteStatus(pending.AccountID, "perp", "USDT"); err != nil || !status.WriteReady {
		t.Fatalf("stale generation callback changed unrelated route status=%+v err=%v", status, err)
	}
}

func TestPromotedRuntimeResolvesSharedPublicCatalog(t *testing.T) {
	manager := newRuntimeGroupTestManager(candidateClientBuilderStub{})
	registry, err := publicconn.NewRegistry(runtimePublicFactory{instruments: map[routeprofile.Key]exchange.Instrument{
		{Venue: "binance", RouteProfileID: "binance.spot.v1"}:      accountTestInstrument(exchange.VenueBinance, exchange.ProductSpot, "BTC-USDT", "BTCUSDT", ""),
		{Venue: "binance", RouteProfileID: "binance.perp.usdt.v1"}: accountTestInstrument(exchange.VenueBinance, exchange.ProductPerp, "BTC-USDT", "BTCUSDT", "USDT"),
	}}, routeprofile.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	manager.publicConnectors = registry
	pending := runtimeGroupPending(t)
	group, err := manager.BuildCandidate(context.Background(), pending)
	if err != nil {
		t.Fatal(err)
	}
	if err := promoteGroup(manager, group); err != nil {
		t.Fatal(err)
	}

	snapshot, err := manager.Instruments(pending.AccountID)
	if err != nil || len(snapshot.Instruments) != 2 {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	for _, public := range snapshot.Instruments {
		resolved, resolveErr := manager.Resolve(context.Background(), pending.AccountID, public.ID)
		if resolveErr != nil || resolved.Identity.NativeSymbol != "BTCUSDT" {
			t.Fatalf("instrument=%s resolved=%+v err=%v", public.ID, resolved, resolveErr)
		}
	}
}

func TestSelectOrderRouteKeepsActiveAndRecoveryGenerationTuplesCoherent(t *testing.T) {
	manager := newRuntimeGroupTestManager(candidateClientBuilderStub{})
	registry, err := publicconn.NewRegistry(runtimePublicFactory{instruments: map[routeprofile.Key]exchange.Instrument{
		{Venue: "binance", RouteProfileID: "binance.spot.v1"}:      accountTestInstrument(exchange.VenueBinance, exchange.ProductSpot, "BTC-USDT", "BTCUSDT", ""),
		{Venue: "binance", RouteProfileID: "binance.perp.usdt.v1"}: accountTestInstrument(exchange.VenueBinance, exchange.ProductPerp, "BTC-USDT", "BTCUSDT", "USDT"),
	}}, routeprofile.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	manager.publicConnectors = registry
	first := runtimeGroupPending(t)
	firstGroup, err := manager.BuildCandidate(context.Background(), first)
	if err != nil || promoteGroup(manager, firstGroup) != nil {
		t.Fatalf("promote first: %v", err)
	}

	selected, err := manager.SelectOrderRoute(context.Background(), first.AccountID, "binance:live:spot:BTC-USDT", OrderRouteWrite, OrderRouteSelector{})
	if err != nil || !selected.Dynamic || selected.Fence != first.Fence || selected.RouteID != "spot" || selected.Admission != firstGroup.Admission {
		t.Fatalf("first selection=%+v err=%v", selected, err)
	}
	firstOrders, _ := firstGroup.Routes["spot"].Handle.(CandidateClientRoute).OrderClient()
	if selected.Orders != firstOrders {
		t.Fatal("first selection mixed the active client generation")
	}

	second := runtimeGroupPending(t)
	second.SnapshotID = uuid.MustParse("018f47a8-2578-7d3c-9d7d-5e4f4a445c36")
	second.OperationID, _ = secretstore.ParseOperationID("018f47a8-2578-7d3c-9d7d-5e4f4a445c34")
	second.Fence.DesiredStateRevision++
	second.Fence.RuntimeGeneration++
	second.Fence.CredentialVersion++
	secondGroup, err := manager.BuildCandidate(context.Background(), second)
	if err != nil || promoteGroup(manager, secondGroup) != nil {
		t.Fatalf("promote second: %v", err)
	}

	active, err := manager.SelectOrderRoute(context.Background(), second.AccountID, "binance:live:spot:BTC-USDT", OrderRouteWrite, OrderRouteSelector{})
	if err != nil || active.Fence != second.Fence || active.Admission != secondGroup.Admission {
		t.Fatalf("active selection=%+v err=%v", active, err)
	}
	recovery, err := manager.SelectOrderRoute(context.Background(), first.AccountID, "binance:live:spot:BTC-USDT", OrderRouteRecovery, OrderRouteSelector{RouteID: "spot", Fence: first.Fence})
	if err != nil || recovery.Fence != first.Fence || recovery.Admission != firstGroup.Admission || recovery.Orders != firstOrders {
		t.Fatalf("recovery selection=%+v err=%v", recovery, err)
	}
	if lease, err := recovery.Admission.Acquire(domain.WriteFence{AccountFence: first.Fence, RouteID: "spot"}); err == nil {
		lease.Release()
		t.Fatal("retiring generation reopened write admission")
	}
}

func TestSelectOrderRouteDoesNotHoldGroupLockAcrossResolveAndRetriesGenerationChange(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	manager := newRuntimeGroupTestManager(candidateClientBuilderStub{})
	first := runtimeGroupPending(t)
	firstGroup, err := manager.BuildCandidate(ctx, first)
	if err != nil || promoteGroup(manager, firstGroup) != nil {
		t.Fatalf("promote first: %v", err)
	}
	lease := &blockingPublicLease{
		started: make(chan struct{}), release: make(chan struct{}),
		instrument: accountTestInstrument(exchange.VenueBinance, exchange.ProductSpot, "BTC-USDT", "BTCUSDT", ""),
	}
	firstGroup.Routes["spot"].publicLease = lease

	type selectionResult struct {
		selected SelectedOrderRoute
		err      error
	}
	selected := make(chan selectionResult, 1)
	go func() {
		result, selectErr := manager.SelectOrderRoute(ctx, first.AccountID, "binance:live:spot:BTC-USDT", OrderRouteWrite, OrderRouteSelector{})
		selected <- selectionResult{selected: result, err: selectErr}
	}()
	select {
	case <-lease.started:
	case <-ctx.Done():
		t.Fatalf("resolve did not start: %v", ctx.Err())
	}

	ownershipDone := make(chan error, 1)
	go func() { ownershipDone <- manager.EnsureAccountOwnership("account-b") }()
	select {
	case err := <-ownershipDone:
		if err != nil {
			t.Fatalf("unrelated ownership operation: %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("unrelated group operation blocked behind public resolve")
	}

	second := runtimeGroupPending(t)
	second.SnapshotID = uuid.MustParse("018f47a8-2578-7d3c-9d7d-5e4f4a445c36")
	second.OperationID, _ = secretstore.ParseOperationID("018f47a8-2578-7d3c-9d7d-5e4f4a445c34")
	second.Fence.DesiredStateRevision++
	second.Fence.RuntimeGeneration++
	second.Fence.CredentialVersion++
	secondGroup, err := manager.BuildCandidate(ctx, second)
	if err != nil || promoteGroup(manager, secondGroup) != nil {
		t.Fatalf("promote second while resolve blocks: %v", err)
	}
	secondRelease := make(chan struct{})
	close(secondRelease)
	secondGroup.Routes["spot"].publicLease = &blockingPublicLease{
		started: make(chan struct{}), release: secondRelease,
		instrument: accountTestInstrument(exchange.VenueBinance, exchange.ProductSpot, "BTC-USDT", "BTCUSDT", ""),
	}
	close(lease.release)
	select {
	case result := <-selected:
		if result.err != nil || result.selected.Fence != second.Fence || result.selected.Admission != secondGroup.Admission {
			t.Fatalf("selection after generation change=%+v err=%v", result.selected, result.err)
		}
	case <-ctx.Done():
		t.Fatalf("wait for stale selection: %v", ctx.Err())
	}
}

func TestRuntimeObserverRegistersExistingAndNewGenerationsBeforeWritesResume(t *testing.T) {
	manager := newRuntimeGroupTestManager(candidateClientBuilderStub{})
	registry, err := publicconn.NewRegistry(runtimePublicFactory{instruments: map[routeprofile.Key]exchange.Instrument{
		{Venue: "binance", RouteProfileID: "binance.spot.v1"}:      accountTestInstrument(exchange.VenueBinance, exchange.ProductSpot, "BTC-USDT", "BTCUSDT", ""),
		{Venue: "binance", RouteProfileID: "binance.perp.usdt.v1"}: accountTestInstrument(exchange.VenueBinance, exchange.ProductPerp, "BTC-USDT", "BTCUSDT", "USDT"),
	}}, routeprofile.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	manager.publicConnectors = registry
	first := runtimeGroupPending(t)
	firstGroup, err := manager.BuildCandidate(context.Background(), first)
	if err != nil || promoteGroup(manager, firstGroup) != nil {
		t.Fatalf("promote first: %v", err)
	}

	observer := &runtimeObserverStub{}
	if err := manager.SetRuntimeAccountObserver(observer); err != nil {
		t.Fatal(err)
	}
	if got := observer.registrations(); len(got) != 1 || got[0] != first.AccountID {
		t.Fatalf("initial registrations=%v", got)
	}
	if status, err := manager.Status(first.AccountID); err != nil || status.WriteReady || status.Reason != ReasonPrivateStreamStarting {
		t.Fatalf("supervised initial status=%+v err=%v", status, err)
	}
	if err := manager.ObservePrivateStreamBarrier(first.AccountID, "snapshot-1", 1); err != nil {
		t.Fatal(err)
	}

	second := runtimeGroupPending(t)
	second.SnapshotID = uuid.MustParse("018f47a8-2578-7d3c-9d7d-5e4f4a445c36")
	second.OperationID, _ = secretstore.ParseOperationID("018f47a8-2578-7d3c-9d7d-5e4f4a445c34")
	second.Fence.DesiredStateRevision++
	second.Fence.RuntimeGeneration++
	second.Fence.CredentialVersion++
	secondGroup, err := manager.BuildCandidate(context.Background(), second)
	if err != nil || promoteGroup(manager, secondGroup) != nil {
		t.Fatalf("promote second: %v", err)
	}
	if got := observer.registrations(); len(got) != 2 || got[1] != second.AccountID {
		t.Fatalf("generation registrations=%v", got)
	}
	if status, err := manager.Status(second.AccountID); err != nil || status.WriteReady || status.Reason != ReasonPrivateStreamStarting {
		t.Fatalf("new generation status=%+v err=%v", status, err)
	}
}

func TestRuntimeObserverBootstrapBlocksAllAccountsAndRollsBackForRetry(t *testing.T) {
	manager := newRuntimeGroupTestManager(candidateClientBuilderStub{})
	registry, err := publicconn.NewRegistry(runtimePublicFactory{instruments: map[routeprofile.Key]exchange.Instrument{
		{Venue: "binance", RouteProfileID: "binance.spot.v1"}:      accountTestInstrument(exchange.VenueBinance, exchange.ProductSpot, "BTC-USDT", "BTCUSDT", ""),
		{Venue: "binance", RouteProfileID: "binance.perp.usdt.v1"}: accountTestInstrument(exchange.VenueBinance, exchange.ProductPerp, "BTC-USDT", "BTCUSDT", "USDT"),
	}}, routeprofile.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	manager.publicConnectors = registry
	first := runtimeGroupPending(t)
	firstGroup, err := manager.BuildCandidate(context.Background(), first)
	if err != nil || promoteGroup(manager, firstGroup) != nil {
		t.Fatalf("promote first: %v", err)
	}
	second := runtimeGroupPending(t)
	second.AccountID = "account-b"
	second.Fence.AccountID = second.AccountID
	second.SnapshotID = uuid.MustParse("018f47a8-2578-7d3c-9d7d-5e4f4a445c46")
	second.OperationID, _ = secretstore.ParseOperationID("018f47a8-2578-7d3c-9d7d-5e4f4a445c47")
	secondGroup, err := manager.BuildCandidate(context.Background(), second)
	if err != nil || promoteGroup(manager, secondGroup) != nil {
		t.Fatalf("promote second: %v", err)
	}

	observer := &runtimeObserverStub{registerErrs: map[domain.AccountID]error{"account-b": errors.New("observer-secret-canary")}}
	observer.onRegister = func(domain.AccountID) {
		for _, accountID := range []domain.AccountID{first.AccountID, second.AccountID} {
			status, statusErr := manager.Status(accountID)
			if statusErr != nil || status.WriteReady || status.Reason != ReasonPrivateStreamStarting {
				t.Errorf("status during bootstrap account=%s status=%+v err=%v", accountID, status, statusErr)
			}
		}
	}
	if err := manager.SetRuntimeAccountObserver(observer); err == nil || strings.Contains(err.Error(), "observer-secret-canary") {
		t.Fatalf("observer bootstrap err=%v", err)
	}
	if got := observer.unregistrations(); len(got) != 2 || got[0] != first.AccountID || got[1] != second.AccountID {
		t.Fatalf("bootstrap cleanup=%v", got)
	}
	manager.observerMu.RLock()
	installed := manager.runtimeObserver
	manager.observerMu.RUnlock()
	if installed != nil {
		t.Fatal("failed observer bootstrap remained installed")
	}

	observer.mu.Lock()
	delete(observer.registerErrs, second.AccountID)
	observer.mu.Unlock()
	if err := manager.SetRuntimeAccountObserver(observer); err != nil {
		t.Fatalf("observer retry: %v", err)
	}
	if got := observer.registrations(); len(got) != 4 || got[2] != first.AccountID || got[3] != second.AccountID {
		t.Fatalf("bootstrap registrations after retry=%v", got)
	}
}

func TestPromotionReportsRuntimeObserverFailureAndKeepsWritesBlocked(t *testing.T) {
	manager := newRuntimeGroupTestManager(candidateClientBuilderStub{})
	observer := &runtimeObserverStub{registerErr: errors.New("observer-secret-canary")}
	if err := manager.SetRuntimeAccountObserver(observer); err != nil {
		t.Fatal(err)
	}
	pending := runtimeGroupPending(t)
	group, err := manager.BuildCandidate(context.Background(), pending)
	if err != nil {
		t.Fatal(err)
	}
	manager.runtimeStore.(*runtimeStateStoreStub).next = store.ActiveSnapshot{DesiredSnapshot: group.Snapshot, PromotedAt: time.Now()}
	result, err := manager.PromoteCandidate(context.Background(), group)
	if err == nil || strings.Contains(err.Error(), "observer-secret-canary") {
		t.Fatalf("promotion result=%+v err=%v", result, err)
	}
	status, statusErr := manager.RouteStatus(pending.AccountID, string(exchange.ProductSpot), "")
	manager.groupMu.Lock()
	privateReason := manager.groups[pending.AccountID].activeGroup.privateStreams["spot"].reason
	manager.groupMu.Unlock()
	if statusErr != nil || status.WriteReady || privateReason != ReasonPrivateStreamStarting {
		t.Fatalf("status after observer failure=%+v err=%v", status, statusErr)
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

func TestCandidateLosingManagerCloseRaceIsDestroyedAndNotInstalled(t *testing.T) {
	built := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	builder := &candidateBuilderStub{
		handles: map[domain.RouteID]*candidateHandleStub{},
		buildHook: func(store.DesiredSnapshot) {
			once.Do(func() { close(built) })
			<-release
		},
	}
	manager := newRuntimeGroupTestManager(builder)
	pending := runtimeGroupPending(t)
	result := make(chan error, 1)
	go func() {
		_, err := manager.BuildCandidate(context.Background(), pending)
		result <- err
	}()
	<-built
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-result; !errors.Is(err, ErrManagerClosed) {
		t.Fatalf("BuildCandidate error=%v, want ErrManagerClosed", err)
	}
	if active := manager.ActiveSnapshot(pending.AccountID); active != nil {
		t.Fatalf("closed manager published active snapshot: %+v", active)
	}
	for routeID, handle := range builder.handles {
		if !handle.closed {
			t.Fatalf("route %s was not closed", routeID)
		}
	}
}

func TestCandidateLosingAccountOwnershipRaceIsDestroyedAndNotInstalled(t *testing.T) {
	builder := &candidateBuilderStub{handles: map[domain.RouteID]*candidateHandleStub{}}
	manager := newRuntimeGroupTestManager(builder)
	active := runtimeGroupPending(t)
	activeGroup, err := manager.BuildCandidate(context.Background(), active)
	if err != nil || promoteGroup(manager, activeGroup) != nil {
		t.Fatalf("seed active err=%v", err)
	}

	entered := make(chan struct{})
	resume := make(chan struct{})
	var once sync.Once
	builder.buildHook = func(store.DesiredSnapshot) {
		once.Do(func() {
			close(entered)
			<-resume
		})
	}
	next := runtimeGroupPending(t)
	next.SnapshotID = uuid.MustParse("018f47a8-2578-7d3c-9d7d-5e4f4a445c36")
	next.Fence.DesiredStateRevision++
	next.Fence.RuntimeGeneration++
	result := make(chan error, 1)
	go func() {
		_, buildErr := manager.BuildCandidate(context.Background(), next)
		result <- buildErr
	}()
	<-entered

	operationID, _ := secretstore.ParseOperationID("018f47a8-2578-7d3c-9d7d-5e4f4a445c50")
	if _, err := manager.RetireRuntime(context.Background(), store.RetirementRequest{
		OperationID: operationID, AccountID: active.AccountID, ExpectedActiveFence: active.Fence,
		TombstoneRevision: active.Fence.DesiredStateRevision + 1,
		Scope:             secretstore.Scope{Namespace: secretstore.Namespace, KeyVersion: "v1"},
	}); err != nil {
		t.Fatal(err)
	}
	manager.groupMu.Lock()
	retained := manager.groups[active.AccountID]
	manager.groupMu.Unlock()
	if retained != nil {
		t.Fatal("retirement retained account ownership")
	}

	close(resume)
	if err := <-result; !errors.Is(err, ErrCandidateFailed) {
		t.Fatalf("BuildCandidate error=%v, want ErrCandidateFailed", err)
	}
	manager.groupMu.Lock()
	retained = manager.groups[active.AccountID]
	manager.groupMu.Unlock()
	if retained != nil {
		t.Fatal("candidate reinstalled retired account ownership")
	}
	for routeID, handle := range builder.handles {
		if !handle.closed {
			t.Fatalf("route %s was not closed", routeID)
		}
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

func TestRetirementWithoutLocalActiveRuntimeChecksDurableActiveBlockers(t *testing.T) {
	for name, blockers := range map[string]RuntimeBlockers{
		"open orders": {OpenOrders: 1},
		"positions":   {Positions: 1},
	} {
		t.Run(name, func(t *testing.T) {
			active := runtimeGroupPending(t)
			manager := newRuntimeGroupTestManager(candidateClientBuilderStub{})
			lifecycle := &runtimeLifecycleStub{blockers: blockers}
			manager.lifecycle = lifecycle
			runtimeStore := manager.runtimeStore.(*runtimeStateStoreStub)
			durable := store.ActiveSnapshot{DesiredSnapshot: active.DesiredSnapshot, PromotedAt: time.Now().UTC()}
			runtimeStore.states = []store.RuntimeState{{AccountID: active.AccountID, Active: &durable}}
			operationID, _ := secretstore.ParseOperationID("018f47a8-2578-7d3c-9d7d-5e4f4a445c49")
			_, err := manager.RetireRuntime(context.Background(), store.RetirementRequest{
				OperationID: operationID, AccountID: active.AccountID, ExpectedActiveFence: active.Fence,
				TombstoneRevision: active.Fence.DesiredStateRevision + 1,
				Scope:             secretstore.Scope{Namespace: secretstore.Namespace, KeyVersion: "v1"},
			})
			if !errors.Is(err, ErrRuntimeBlocked) {
				t.Fatalf("retirement with blockers err=%v", err)
			}
			if lifecycle.blockerCalls() != 1 || runtimeStore.retireCalls != 0 {
				t.Fatalf("blocker calls=%d durable retire calls=%d", lifecycle.blockerCalls(), runtimeStore.retireCalls)
			}
		})
	}
}

func TestRetirementAfterFailedRestoreChecksDurableActiveBlockers(t *testing.T) {
	active := runtimeGroupPending(t)
	manager := newRuntimeGroupTestManager(candidateClientBuilderStub{failSnapshot: active.SnapshotID})
	lifecycle := &runtimeLifecycleStub{blockers: RuntimeBlockers{OpenOrders: 1}}
	manager.lifecycle = lifecycle
	runtimeStore := manager.runtimeStore.(*runtimeStateStoreStub)
	durable := store.ActiveSnapshot{DesiredSnapshot: active.DesiredSnapshot, PromotedAt: time.Now().UTC()}
	runtimeStore.states = []store.RuntimeState{{AccountID: active.AccountID, Active: &durable}}
	if err := manager.RestoreRuntimeState(context.Background()); err != nil {
		t.Fatal(err)
	}
	manager.groupMu.Lock()
	local := manager.groups[active.AccountID]
	manager.groupMu.Unlock()
	if local == nil || local.active.Load() != nil {
		t.Fatalf("failed restore local runtime=%+v", local)
	}
	operationID, _ := secretstore.ParseOperationID("018f47a8-2578-7d3c-9d7d-5e4f4a445c49")
	_, err := manager.RetireRuntime(context.Background(), store.RetirementRequest{
		OperationID: operationID, AccountID: active.AccountID, ExpectedActiveFence: active.Fence,
		TombstoneRevision: active.Fence.DesiredStateRevision + 1,
		Scope:             secretstore.Scope{Namespace: secretstore.Namespace, KeyVersion: "v1"},
	})
	if !errors.Is(err, ErrRuntimeBlocked) || lifecycle.blockerCalls() != 1 || runtimeStore.retireCalls != 0 {
		t.Fatalf("failed-restore retirement err=%v blocker calls=%d retire calls=%d", err, lifecycle.blockerCalls(), runtimeStore.retireCalls)
	}
}

func TestRetirementWithoutLocalRuntimeAllowsOnlyExactDurableReplay(t *testing.T) {
	active := runtimeGroupPending(t)
	manager := newRuntimeGroupTestManager(candidateClientBuilderStub{})
	lifecycle := &runtimeLifecycleStub{blockers: RuntimeBlockers{OpenOrders: 1, Positions: 1}}
	manager.lifecycle = lifecycle
	runtimeStore := manager.runtimeStore.(*runtimeStateStoreStub)
	operationID, _ := secretstore.ParseOperationID("018f47a8-2578-7d3c-9d7d-5e4f4a445c49")
	request := store.RetirementRequest{
		OperationID: operationID, AccountID: active.AccountID, ExpectedActiveFence: active.Fence,
		TombstoneRevision: active.Fence.DesiredStateRevision + 1,
		Scope:             secretstore.Scope{Namespace: secretstore.Namespace, KeyVersion: "v1"},
	}
	result := store.RetirementResult{
		OperationID: request.OperationID, AccountID: request.AccountID, TombstoneRevision: request.TombstoneRevision,
		Tombstoned: true, CompletedAt: time.Now().UTC(),
	}
	runtimeStore.states = []store.RuntimeState{{
		AccountID: active.AccountID, Tombstoned: true, TombstoneRevision: request.TombstoneRevision,
	}}
	runtimeStore.retireRequest, runtimeStore.retireResult = &request, &result

	replayed, err := manager.RetireRuntime(context.Background(), request)
	if err != nil || replayed != result {
		t.Fatalf("exact retirement replay=%+v err=%v", replayed, err)
	}
	if lifecycle.blockerCalls() != 0 || runtimeStore.retireCalls != 1 {
		t.Fatalf("exact replay blocker calls=%d retire calls=%d", lifecycle.blockerCalls(), runtimeStore.retireCalls)
	}
	conflict := request
	conflict.OperationID, _ = secretstore.ParseOperationID("018f47a8-2578-7d3c-9d7d-5e4f4a445c48")
	if _, err := manager.RetireRuntime(context.Background(), conflict); !errors.Is(err, store.ErrOperationConflict) {
		t.Fatalf("conflicting retirement replay err=%v", err)
	}
}

func TestRetirementSerializesCandidateBuildAndPromotionLifecycle(t *testing.T) {
	newRetiringManager := func(t *testing.T) (*Manager, store.PendingSnapshot, store.RetirementRequest, *retireCallBarrier) {
		t.Helper()
		manager := newRuntimeGroupTestManager(candidateClientBuilderStub{})
		active := runtimeGroupPending(t)
		group, err := manager.BuildCandidate(context.Background(), active)
		if err != nil || promoteGroup(manager, group) != nil {
			t.Fatalf("seed active: %v", err)
		}
		barrier := &retireCallBarrier{want: 1, ready: make(chan struct{}), release: make(chan struct{})}
		manager.runtimeStore.(*runtimeStateStoreStub).retireBarrier = barrier
		operationID, _ := secretstore.ParseOperationID("018f47a8-2578-7d3c-9d7d-5e4f4a445c49")
		request := store.RetirementRequest{
			OperationID: operationID, AccountID: active.AccountID, ExpectedActiveFence: active.Fence,
			TombstoneRevision: active.Fence.DesiredStateRevision + 1,
			Scope:             secretstore.Scope{Namespace: secretstore.Namespace, KeyVersion: "v1"},
		}
		return manager, active, request, barrier
	}

	t.Run("build", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		manager, active, request, barrier := newRetiringManager(t)
		retired := make(chan error, 1)
		go func() { _, err := manager.RetireRuntime(ctx, request); retired <- err }()
		select {
		case <-barrier.ready:
		case <-ctx.Done():
			t.Fatalf("retirement did not reach durable store: %v", ctx.Err())
		}
		next := active
		next.SnapshotID = uuid.MustParse("018f47a8-2578-7d3c-9d7d-5e4f4a445c36")
		next.OperationID, _ = secretstore.ParseOperationID("018f47a8-2578-7d3c-9d7d-5e4f4a445c34")
		next.Fence.DesiredStateRevision++
		next.Fence.RuntimeGeneration++
		built := make(chan error, 1)
		go func() { _, err := manager.BuildCandidate(ctx, next); built <- err }()
		select {
		case err := <-built:
			t.Fatalf("candidate build crossed retirement lifecycle: %v", err)
		case <-time.After(100 * time.Millisecond):
		}
		close(barrier.release)
		if err := <-retired; err != nil {
			t.Fatalf("retirement: %v", err)
		}
		if err := <-built; !errors.Is(err, ErrCandidateFailed) {
			t.Fatalf("post-retirement build err=%v", err)
		}
	})

	t.Run("promotion", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		manager, active, request, barrier := newRetiringManager(t)
		next := active
		next.SnapshotID = uuid.MustParse("018f47a8-2578-7d3c-9d7d-5e4f4a445c36")
		next.OperationID, _ = secretstore.ParseOperationID("018f47a8-2578-7d3c-9d7d-5e4f4a445c34")
		next.Fence.DesiredStateRevision++
		next.Fence.RuntimeGeneration++
		candidate, err := manager.BuildCandidate(ctx, next)
		if err != nil {
			t.Fatal(err)
		}
		runtimeStore := manager.runtimeStore.(*runtimeStateStoreStub)
		runtimeStore.mu.Lock()
		runtimeStore.next = store.ActiveSnapshot{DesiredSnapshot: candidate.Snapshot, PromotedAt: time.Now()}
		runtimeStore.mu.Unlock()
		retired := make(chan error, 1)
		go func() { _, err := manager.RetireRuntime(ctx, request); retired <- err }()
		select {
		case <-barrier.ready:
		case <-ctx.Done():
			t.Fatalf("retirement did not reach durable store: %v", ctx.Err())
		}
		promoted := make(chan error, 1)
		go func() { _, err := manager.PromoteCandidate(ctx, candidate); promoted <- err }()
		select {
		case err := <-promoted:
			t.Fatalf("promotion crossed retirement lifecycle: %v", err)
		case <-time.After(100 * time.Millisecond):
		}
		close(barrier.release)
		if err := <-retired; err != nil {
			t.Fatalf("retirement: %v", err)
		}
		if err := <-promoted; !errors.Is(err, ErrCandidateMismatch) {
			t.Fatalf("post-retirement promotion err=%v", err)
		}
	})
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
			Scope:             secretstore.Scope{Namespace: secretstore.Namespace, KeyVersion: "v1"},
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
	if ids := manager.AccountIDs(); len(ids) != 0 {
		t.Fatalf("retired runtime remained in recovery inventory: %v", ids)
	}
	if _, err := manager.Runtime(active.AccountID); !errors.Is(err, ErrUnknownAccount) {
		t.Fatalf("retired control-plane runtime err=%v", err)
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
		Scope:             secretstore.Scope{Namespace: secretstore.Namespace, KeyVersion: "v1"},
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
	var firstUnregister sync.Once
	observer := &runtimeObserverStub{onUnregister: func(accountID domain.AccountID) {
		if accountID != active.AccountID {
			t.Fatalf("unregistered account=%s", accountID)
		}
		firstUnregister.Do(func() {
			if calls := handle.callCount(); calls != 0 {
				t.Fatalf("route close calls=%d before stream supervision stopped", calls)
			}
		})
	}}
	if err := manager.SetRuntimeAccountObserver(observer); err != nil {
		t.Fatal(err)
	}
	operationID, _ := secretstore.ParseOperationID("018f47a8-2578-7d3c-9d7d-5e4f4a445c48")
	request := store.RetirementRequest{
		OperationID: operationID, AccountID: active.AccountID, ExpectedActiveFence: active.Fence,
		TombstoneRevision: active.Fence.DesiredStateRevision + 1,
		Scope:             secretstore.Scope{Namespace: secretstore.Namespace, KeyVersion: "v1"},
	}

	if _, err := manager.RetireRuntime(context.Background(), request); !errors.Is(err, ErrRuntimeCleanupFailed) {
		t.Fatalf("first retirement err=%v", err)
	}
	if got := observer.unregistrations(); len(got) != 1 || got[0] != active.AccountID {
		t.Fatalf("unregistrations after failed cleanup=%v", got)
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
	if got := observer.unregistrations(); len(got) != 2 || got[1] != active.AccountID {
		t.Fatalf("unregistrations after cleanup replay=%v", got)
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
	barrier := &retireCallBarrier{want: 1, ready: make(chan struct{}), release: make(chan struct{})}
	runtimeStore := manager.runtimeStore.(*runtimeStateStoreStub)
	runtimeStore.retireBarrier = barrier
	durable := store.ActiveSnapshot{DesiredSnapshot: active.DesiredSnapshot, PromotedAt: time.Now().UTC()}
	runtimeStore.states = []store.RuntimeState{{AccountID: active.AccountID, Active: &durable}}
	runtimeStore.updateStateOnRetire = true
	operationID, _ := secretstore.ParseOperationID("018f47a8-2578-7d3c-9d7d-5e4f4a445c49")
	request := store.RetirementRequest{
		OperationID: operationID, AccountID: active.AccountID, ExpectedActiveFence: active.Fence,
		TombstoneRevision: active.Fence.DesiredStateRevision + 1,
		Scope:             secretstore.Scope{Namespace: secretstore.Namespace, KeyVersion: "v1"},
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

func TestRestoreRehydratesRetiringGenerationForExactRecoveryWithAdmissionClosed(t *testing.T) {
	retiring := runtimeGroupPending(t).DesiredSnapshot
	active := runtimeGroupPending(t).DesiredSnapshot
	active.SnapshotID = uuid.MustParse("018f47a8-2578-7d3c-9d7d-5e4f4a445c56")
	active.OperationID, _ = secretstore.ParseOperationID("018f47a8-2578-7d3c-9d7d-5e4f4a445c55")
	active.SecretRef, _ = secretstore.ParseOpaqueRef("AgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgI")
	active.Fence.DesiredStateRevision++
	active.Fence.RuntimeGeneration++
	active.Fence.CredentialVersion++
	builtSnapshots := make([]uuid.UUID, 0, len(active.Routes))
	builder := candidateClientBuilderStub{buildHook: func(snapshot store.DesiredSnapshot) {
		builtSnapshots = append(builtSnapshots, snapshot.SnapshotID)
	}}
	manager := newRuntimeGroupTestManager(builder)
	registry, err := publicconn.NewRegistry(runtimePublicFactory{instruments: map[routeprofile.Key]exchange.Instrument{
		{Venue: "binance", RouteProfileID: "binance.spot.v1"}:      accountTestInstrument(exchange.VenueBinance, exchange.ProductSpot, "BTC-USDT", "BTCUSDT", ""),
		{Venue: "binance", RouteProfileID: "binance.perp.usdt.v1"}: accountTestInstrument(exchange.VenueBinance, exchange.ProductPerp, "BTC-USDT", "BTCUSDT", "USDT"),
	}}, routeprofile.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	manager.publicConnectors = registry
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
	if got := manager.AccountIDs(); len(got) != 1 || got[0] != active.AccountID {
		t.Fatalf("restored account IDs=%v, want %s", got, active.AccountID)
	}
	controlPlane, err := manager.Runtime(active.AccountID)
	if err != nil || controlPlane.Desired.Fence() != (WriteFence{
		AccountID: active.AccountID, DesiredStateRevision: active.Fence.DesiredStateRevision,
		RuntimeGeneration: active.Fence.RuntimeGeneration, CredentialVersion: active.Fence.CredentialVersion,
	}) || controlPlane.State != RuntimeStateActive || !controlPlane.Ready {
		t.Fatalf("restored control-plane runtime=%+v err=%v", controlPlane, err)
	}
	if len(builtSnapshots) != len(active.Routes)+len(retiring.Routes) {
		t.Fatalf("route builds=%d want=%d", len(builtSnapshots), len(active.Routes)+len(retiring.Routes))
	}
	manager.groupMu.Lock()
	localRetiring := len(manager.groups[active.AccountID].retiring)
	manager.groupMu.Unlock()
	if localRetiring != 1 {
		t.Fatalf("restore rehydrated %d retiring groups", localRetiring)
	}
	recovery, err := manager.SelectOrderRoute(
		context.Background(), active.AccountID, "binance:live:spot:BTC-USDT", OrderRouteRecovery,
		OrderRouteSelector{RouteID: "spot", Fence: retiring.Fence},
	)
	if err != nil || recovery.Fence != retiring.Fence || recovery.RouteID != "spot" {
		t.Fatalf("restored retiring recovery=%+v err=%v", recovery, err)
	}
	if lease, acquireErr := recovery.Admission.Acquire(domain.WriteFence{AccountFence: retiring.Fence, RouteID: "spot"}); acquireErr == nil {
		lease.Release()
		t.Fatal("restored retiring generation admitted writes")
	}
	request := store.RotationFinalizationRequest{
		OperationID: active.OperationID, AccountID: active.AccountID,
		ExpectedActiveFence: active.Fence, ExpectedRetiringFence: retiring.Fence,
		Scope: secretstore.Scope{Namespace: secretstore.Namespace, KeyVersion: "v1"},
	}
	result, err := manager.FinalizeRotation(context.Background(), request)
	if err != nil || result.OperationID != active.OperationID || result.RetiringFence != retiring.Fence {
		t.Fatalf("restart finalization=%+v err=%v", result, err)
	}
	if runtimeStore.rotationFinalizeCalls != 1 {
		t.Fatal("restart finalization did not reach durable store")
	}
}

func TestRestorePendingFailurePreservesActiveAndContinuesOtherAccounts(t *testing.T) {
	firstActive := runtimeGroupPending(t)
	firstPending := runtimeGroupPending(t)
	firstPending.SnapshotID = uuid.MustParse("018f47a8-2578-7d3c-9d7d-5e4f4a445c36")
	firstPending.OperationID, _ = secretstore.ParseOperationID("018f47a8-2578-7d3c-9d7d-5e4f4a445c34")
	firstPending.Fence.DesiredStateRevision++
	firstPending.Fence.RuntimeGeneration++

	secondActive := runtimeGroupPending(t)
	secondActive.AccountID = "account-b"
	secondActive.Fence.AccountID = secondActive.AccountID
	secondActive.SnapshotID = uuid.MustParse("018f47a8-2578-7d3c-9d7d-5e4f4a445c46")
	secondActive.OperationID, _ = secretstore.ParseOperationID("018f47a8-2578-7d3c-9d7d-5e4f4a445c47")
	builder := candidateClientBuilderStub{failSnapshot: firstPending.SnapshotID}
	manager := newRuntimeGroupTestManager(builder)
	manager.runtimeStore.(*runtimeStateStoreStub).states = []store.RuntimeState{
		{
			AccountID: firstActive.AccountID,
			Active:    &store.ActiveSnapshot{DesiredSnapshot: firstActive.DesiredSnapshot, PromotedAt: time.Now()},
			Pending:   &firstPending,
		},
		{
			AccountID: secondActive.AccountID,
			Active:    &store.ActiveSnapshot{DesiredSnapshot: secondActive.DesiredSnapshot, PromotedAt: time.Now()},
		},
	}

	if err := manager.RestoreRuntimeState(context.Background()); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if active := manager.ActiveSnapshot(firstActive.AccountID); active == nil || active.Fence != firstActive.Fence {
		t.Fatalf("first active after pending failure=%+v", active)
	}
	if active := manager.ActiveSnapshot(secondActive.AccountID); active == nil || active.Fence != secondActive.Fence {
		t.Fatalf("second active after other account failure=%+v", active)
	}
	firstRuntime, err := manager.Runtime(firstActive.AccountID)
	if err != nil || firstRuntime.Ready || firstRuntime.Reason != ErrCandidateFailed.Error() {
		t.Fatalf("failed pending readiness=%+v err=%v", firstRuntime, err)
	}
	secondRuntime, err := manager.Runtime(secondActive.AccountID)
	if err != nil || !secondRuntime.Ready || secondRuntime.Reason != "" {
		t.Fatalf("valid account readiness=%+v err=%v", secondRuntime, err)
	}
}

func TestRestoreActiveRuntimeRebuildsExactDurableGenerationWithoutPromotion(t *testing.T) {
	active := runtimeGroupPending(t)
	pending := runtimeGroupPending(t)
	pending.SnapshotID = uuid.MustParse("018f47a8-2578-7d3c-9d7d-5e4f4a445c36")
	pending.OperationID, _ = secretstore.ParseOperationID("018f47a8-2578-7d3c-9d7d-5e4f4a445c34")
	pending.Fence.DesiredStateRevision++
	pending.Fence.RuntimeGeneration++
	buildCounts := make(map[uuid.UUID]int)
	builder := &candidateClientBuilderStub{failSnapshot: active.SnapshotID, buildCounts: buildCounts}
	manager := newRuntimeGroupTestManager(builder)
	durable := store.ActiveSnapshot{DesiredSnapshot: active.DesiredSnapshot, PromotedAt: time.Now().UTC()}
	runtimeStore := manager.runtimeStore.(*runtimeStateStoreStub)
	runtimeStore.states = []store.RuntimeState{{AccountID: active.AccountID, Active: &durable, Pending: &pending}}
	observer := &runtimeObserverStub{}
	if err := manager.SetRuntimeAccountObserver(observer); err != nil {
		t.Fatal(err)
	}
	if err := manager.RestoreRuntimeState(context.Background()); err != nil {
		t.Fatal(err)
	}
	if manager.ActiveSnapshot(active.AccountID) != nil {
		t.Fatal("transient active rebuild failure published a runtime")
	}
	manager.groupMu.Lock()
	pendingCandidate := manager.groups[active.AccountID].candidate
	manager.groupMu.Unlock()
	if pendingCandidate == nil || pendingCandidate.Snapshot.SnapshotID != pending.SnapshotID {
		t.Fatalf("restored pending candidate=%+v", pendingCandidate)
	}

	builder.failSnapshot = uuid.Nil
	if err := manager.RestoreActiveRuntime(context.Background(), durable); err != nil {
		t.Fatalf("exact active replay: %v", err)
	}
	published := manager.ActiveSnapshot(active.AccountID)
	if published == nil || published.Fence != active.Fence {
		t.Fatalf("restored active=%+v", published)
	}
	manager.groupMu.Lock()
	preservedPending := manager.groups[active.AccountID].candidate
	manager.groupMu.Unlock()
	if preservedPending != pendingCandidate {
		t.Fatal("active replay replaced the restored pending candidate")
	}
	if runtimeStore.calls != 0 {
		t.Fatalf("active replay issued %d durable promotions", runtimeStore.calls)
	}
	if got := observer.registrations(); len(got) != 1 || got[0] != active.AccountID {
		t.Fatalf("observer registrations=%v", got)
	}
	manager.groupMu.Lock()
	privateStream := manager.groups[active.AccountID].activeGroup.privateStreams["spot"]
	manager.groupMu.Unlock()
	if privateStream.ready || privateStream.reason != ReasonPrivateStreamStarting {
		t.Fatalf("restored supervision state=%+v", privateStream)
	}
	runtime, err := manager.Runtime(active.AccountID)
	if err != nil || !runtime.Ready || runtime.Reason != "" {
		t.Fatalf("restored runtime=%+v err=%v", runtime, err)
	}
	version := runtime.RuntimeVersion
	builds := buildCounts[active.SnapshotID]
	if err := manager.RestoreActiveRuntime(context.Background(), durable); err != nil {
		t.Fatalf("incomplete supervision replay: %v", err)
	}
	if got := observer.registrations(); len(got) != 2 {
		t.Fatalf("incomplete supervision was not retried: registrations=%v", got)
	}
	for _, routeID := range []domain.RouteID{"spot", "perp-usdt"} {
		fence := domain.WriteFence{AccountFence: active.Fence, RouteID: routeID}
		if err := manager.ObservePrivateStreamRouteBarrier(fence, string(routeID)+"-snapshot", 1); err != nil {
			t.Fatalf("observe %s barrier: %v", routeID, err)
		}
	}
	if err := manager.RestoreActiveRuntime(context.Background(), durable); err != nil {
		t.Fatalf("healthy exact active replay: %v", err)
	}
	if got := observer.registrations(); len(got) != 2 {
		t.Fatalf("healthy exact replay restarted supervision: registrations=%v", got)
	}
	runtime, err = manager.Runtime(active.AccountID)
	if err != nil || runtime.RuntimeVersion != version || buildCounts[active.SnapshotID] != builds || runtimeStore.calls != 0 {
		t.Fatalf("idempotent replay runtime=%+v builds=%d promotions=%d err=%v", runtime, buildCounts[active.SnapshotID], runtimeStore.calls, err)
	}
}

func TestRestoreActiveRuntimeRejectsNonDurableGenerationAndRedactsBuildFailure(t *testing.T) {
	active := runtimeGroupPending(t)
	builder := &candidateClientBuilderStub{failSnapshot: active.SnapshotID}
	manager := newRuntimeGroupTestManager(builder)
	durable := store.ActiveSnapshot{DesiredSnapshot: active.DesiredSnapshot, PromotedAt: time.Now().UTC()}
	manager.runtimeStore.(*runtimeStateStoreStub).states = []store.RuntimeState{{AccountID: active.AccountID, Active: &durable}}
	manager.credentialResolver = &failingCredentialResolver{err: errors.New("credential-secret-canary")}
	if err := manager.RestoreActiveRuntime(context.Background(), durable); err == nil || strings.Contains(err.Error(), "credential-secret-canary") {
		t.Fatalf("redacted active restore err=%v", err)
	}

	nonDurable := durable
	nonDurable.SnapshotID = uuid.MustParse("018f47a8-2578-7d3c-9d7d-5e4f4a445c99")
	if err := manager.RestoreActiveRuntime(context.Background(), nonDurable); err == nil {
		t.Fatal("non-durable active generation was restored")
	}
}

func TestRestoreActiveRuntimeSerializesAgainstRetirement(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	active := runtimeGroupPending(t)
	manager := newRuntimeGroupTestManager(candidateClientBuilderStub{})
	durable := store.ActiveSnapshot{DesiredSnapshot: active.DesiredSnapshot, PromotedAt: time.Now().UTC()}
	runtimeStore := manager.runtimeStore.(*runtimeStateStoreStub)
	group, err := manager.BuildCandidate(ctx, active)
	if err != nil || promoteGroup(manager, group) != nil {
		t.Fatalf("seed active: %v", err)
	}
	runtimeStore.states = []store.RuntimeState{{AccountID: active.AccountID, Active: &durable}}
	runtimeStore.updateStateOnRetire = true
	barrier := &retireCallBarrier{want: 1, ready: make(chan struct{}), release: make(chan struct{})}
	runtimeStore.retireBarrier = barrier
	operationID, _ := secretstore.ParseOperationID("018f47a8-2578-7d3c-9d7d-5e4f4a445c49")
	request := store.RetirementRequest{
		OperationID: operationID, AccountID: active.AccountID, ExpectedActiveFence: active.Fence,
		TombstoneRevision: active.Fence.DesiredStateRevision + 1,
		Scope:             secretstore.Scope{Namespace: secretstore.Namespace, KeyVersion: "v1"},
	}
	retired := make(chan error, 1)
	go func() { _, err := manager.RetireRuntime(ctx, request); retired <- err }()
	select {
	case <-barrier.ready:
	case <-ctx.Done():
		t.Fatalf("retirement did not reach durable store: %v", ctx.Err())
	}
	restored := make(chan error, 1)
	go func() { restored <- manager.RestoreActiveRuntime(ctx, durable) }()
	select {
	case err := <-restored:
		t.Fatalf("active restore crossed retirement: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(barrier.release)
	if err := <-retired; err != nil {
		t.Fatalf("retirement: %v", err)
	}
	if err := <-restored; err == nil {
		t.Fatal("active generation restored after durable retirement")
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
		Scope: secretstore.Scope{Namespace: secretstore.Namespace, KeyVersion: "v1"},
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
		profileRegistry:    NewProfileRegistry(nil),
		allowLiveWrites:    true,
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
		AccountID: fence.AccountID, Venue: "binance", NativeEnvironment: "live", SecretRef: ref,
		Fence: fence, RequestedState: store.DesiredActive,
		Routes: []store.RouteDefinition{
			{RouteID: "spot", Product: "spot", RouteProfileID: "binance.spot.v1", RequestedState: store.RouteActive},
			{RouteID: "perp-usdt", Product: "perp", SettlementAsset: "USDT", RouteProfileID: "binance.perp.usdt.v1", RequestedState: store.RouteActive},
		},
	}, OperationState: store.OperationCandidateReady}
}
