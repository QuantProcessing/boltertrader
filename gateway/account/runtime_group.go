package account

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/QuantProcessing/boltertrader/credential"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/gateway/store"
	"github.com/QuantProcessing/boltertrader/secretstore"
)

var (
	ErrCandidateFailed   = errors.New("runtime candidate failed")
	ErrCandidateMismatch = errors.New("runtime candidate fence mismatch")
)

type GroupState string

const (
	GroupCandidate GroupState = "candidate"
	GroupActive    GroupState = "active"
	GroupRetiring  GroupState = "retiring"
	GroupDestroyed GroupState = "destroyed"
)

type CandidateRoute interface {
	Probe(context.Context) error
	Close() error
}

type RuntimeGroupBuilder interface {
	BuildRoute(context.Context, store.DesiredSnapshot, store.RouteDefinition, *credential.Decoded) (CandidateRoute, error)
}

type ScopedCredentialResolver interface {
	Check(context.Context) error
	Resolve(context.Context, secretstore.OpaqueRef) ([]byte, error)
}

type RuntimeStateStore interface {
	PromotePending(context.Context, store.PromotionRequest) (store.PromotionResult, error)
	ListRuntimeStateForRecovery(context.Context) ([]store.RuntimeState, error)
}

func (manager *Manager) SetRuntimeStateStore(runtimeStore RuntimeStateStore) error {
	if runtimeStore == nil {
		return ErrCandidateMismatch
	}
	manager.groupMu.Lock()
	defer manager.groupMu.Unlock()
	manager.runtimeStore = runtimeStore
	return nil
}

type RouteRuntime struct {
	Definition store.RouteDefinition
	Handle     CandidateRoute
}

type RouteView struct {
	RouteID         domain.RouteID
	Product         string
	SettlementAsset string
	RouteProfileID  string
	RequestedState  store.RouteState
	WriteReady      bool
}

type RuntimeGroup struct {
	Snapshot  store.DesiredSnapshot
	Routes    map[domain.RouteID]*RouteRuntime
	Admission *GenerationAdmission
	State     GroupState
}

type RoutingSnapshot struct {
	Fence  domain.AccountFence
	Routes map[domain.RouteID]RouteView
}

type accountRuntime struct {
	active      atomic.Pointer[RoutingSnapshot]
	activeGroup *RuntimeGroup
	candidate   *RuntimeGroup
	retiring    map[uint64]*RuntimeGroup
}

type GenerationAdmission struct {
	mu       sync.Mutex
	closed   bool
	inflight int
	drained  chan struct{}
	fence    domain.AccountFence
	routes   map[domain.RouteID]struct{}
}

func NewGenerationAdmission() *GenerationAdmission {
	return &GenerationAdmission{drained: make(chan struct{})}
}

type HandoffLease interface{ Release() }

type handoffLease struct {
	release func()
}

func (lease *handoffLease) Release() { lease.release() }

func newGenerationAdmission(snapshot store.DesiredSnapshot) *GenerationAdmission {
	admission := NewGenerationAdmission()
	admission.fence = snapshot.Fence
	admission.routes = make(map[domain.RouteID]struct{}, len(snapshot.Routes))
	for _, route := range snapshot.Routes {
		if route.RequestedState == store.RouteActive {
			admission.routes[route.RouteID] = struct{}{}
		}
	}
	return admission
}

func (admission *GenerationAdmission) Acquire(fence domain.WriteFence) (HandoffLease, error) {
	admission.mu.Lock()
	defer admission.mu.Unlock()
	if admission.closed || fence.AccountFence != admission.fence {
		return nil, ErrRuntimeBlocked
	}
	if _, ok := admission.routes[fence.RouteID]; !ok {
		return nil, ErrRuntimeBlocked
	}
	admission.inflight++
	var once sync.Once
	return &handoffLease{release: func() {
		once.Do(func() {
			admission.mu.Lock()
			admission.inflight--
			if admission.closed && admission.inflight == 0 {
				close(admission.drained)
			}
			admission.mu.Unlock()
		})
	}}, nil
}

func (admission *GenerationAdmission) CloseAndWait(ctx context.Context) error {
	admission.mu.Lock()
	if !admission.closed {
		admission.closed = true
		if admission.inflight == 0 {
			close(admission.drained)
		}
	}
	drained := admission.drained
	admission.mu.Unlock()
	select {
	case <-drained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (admission *GenerationAdmission) reopen() {
	admission.mu.Lock()
	defer admission.mu.Unlock()
	if !admission.closed {
		return
	}
	admission.closed = false
	admission.drained = make(chan struct{})
}

func (manager *Manager) EnsureAccountOwnership(accountID domain.AccountID) error {
	if err := accountID.Validate(); err != nil {
		return err
	}
	manager.groupMu.Lock()
	defer manager.groupMu.Unlock()
	if manager.groups == nil {
		manager.groups = make(map[domain.AccountID]*accountRuntime)
	}
	if _, exists := manager.groups[accountID]; !exists {
		manager.groups[accountID] = &accountRuntime{retiring: make(map[uint64]*RuntimeGroup)}
	}
	return nil
}

func (manager *Manager) BuildCandidate(ctx context.Context, pending store.PendingSnapshot) (*RuntimeGroup, error) {
	if ctx == nil || manager.groupBuilder == nil {
		return nil, ErrCandidateFailed
	}
	if err := pending.DesiredSnapshot.Validate(); err != nil {
		return nil, ErrCandidateFailed
	}
	if manager.profileRegistry == nil || manager.profileRegistry.Validate(manager.runtimeMode, pending.DesiredSnapshot) != nil {
		return nil, ErrCandidateFailed
	}
	if manager.credentialResolver == nil {
		return nil, ErrCandidateFailed
	}
	material, err := manager.credentialResolver.Resolve(ctx, pending.SecretRef)
	if err != nil || len(material) == 0 {
		clear(material)
		return nil, ErrCandidateFailed
	}
	decoded, err := credential.DefaultRegistry().Decode(pending.Venue, material)
	clear(material)
	if err != nil {
		return nil, ErrCandidateFailed
	}
	defer decoded.Clear()
	if err := manager.EnsureAccountOwnership(pending.AccountID); err != nil {
		return nil, err
	}
	group := &RuntimeGroup{
		Snapshot:  pending.DesiredSnapshot,
		Routes:    make(map[domain.RouteID]*RouteRuntime, len(pending.Routes)),
		Admission: newGenerationAdmission(pending.DesiredSnapshot),
		State:     GroupCandidate,
	}
	for _, definition := range pending.Routes {
		runtime := &RouteRuntime{Definition: definition}
		group.Routes[definition.RouteID] = runtime
		if definition.RequestedState == store.RouteDisabled {
			continue
		}
		handle, err := manager.groupBuilder.BuildRoute(ctx, pending.DesiredSnapshot, definition, decoded)
		if err != nil || handle == nil {
			group.destroy()
			return nil, ErrCandidateFailed
		}
		runtime.Handle = handle
		if err := handle.Probe(ctx); err != nil {
			group.destroy()
			return nil, ErrCandidateFailed
		}
	}
	manager.groupMu.Lock()
	runtime := manager.groups[pending.AccountID]
	if runtime.candidate != nil {
		runtime.candidate.destroy()
	}
	runtime.candidate = group
	manager.groupMu.Unlock()
	return group, nil
}

func (manager *Manager) PromoteCandidate(ctx context.Context, group *RuntimeGroup) (store.PromotionResult, error) {
	if group == nil || group.State != GroupCandidate || manager.runtimeStore == nil {
		return store.PromotionResult{}, ErrCandidateMismatch
	}
	manager.groupMu.Lock()
	runtime := manager.groups[group.Snapshot.AccountID]
	if runtime == nil || runtime.candidate != group {
		manager.groupMu.Unlock()
		return store.PromotionResult{}, ErrCandidateMismatch
	}
	old := runtime.activeGroup
	manager.groupMu.Unlock()
	if old != nil {
		if err := old.Admission.CloseAndWait(ctx); err != nil {
			old.Admission.reopen()
			return store.PromotionResult{}, err
		}
	}
	expected := domain.AccountFence{}
	if old != nil {
		expected = old.Snapshot.Fence
	}
	result, err := manager.runtimeStore.PromotePending(ctx, store.PromotionRequest{
		AccountID: group.Snapshot.AccountID, OperationID: group.Snapshot.OperationID,
		ExpectedActiveFence: expected,
	})
	if err != nil {
		if old != nil {
			old.Admission.reopen()
		}
		return store.PromotionResult{}, err
	}
	if result.Active.SnapshotID != group.Snapshot.SnapshotID || result.Active.OperationID != group.Snapshot.OperationID || result.Active.Fence != group.Snapshot.Fence {
		return store.PromotionResult{}, ErrCandidateMismatch
	}
	if err := manager.publishApplied(group, result.Active); err != nil {
		return store.PromotionResult{}, err
	}
	return result, nil
}

func (manager *Manager) publishApplied(group *RuntimeGroup, active store.ActiveSnapshot) error {
	manager.groupMu.Lock()
	defer manager.groupMu.Unlock()
	runtime := manager.groups[group.Snapshot.AccountID]
	if runtime == nil || runtime.candidate != group {
		return ErrCandidateMismatch
	}
	old := runtime.activeGroup
	if old != nil {
		old.State = GroupRetiring
		runtime.retiring[old.Snapshot.Fence.RuntimeGeneration] = old
	}
	routes := make(map[domain.RouteID]RouteView, len(group.Routes))
	for routeID, route := range group.Routes {
		routes[routeID] = RouteView{
			RouteID: routeID, Product: route.Definition.Product,
			SettlementAsset: route.Definition.SettlementAsset,
			RouteProfileID:  route.Definition.RouteProfileID,
			RequestedState:  route.Definition.RequestedState,
			WriteReady:      route.Handle != nil,
		}
	}
	group.State = GroupActive
	runtime.activeGroup = group
	runtime.candidate = nil
	runtime.active.Store(&RoutingSnapshot{Fence: active.Fence, Routes: routes})
	return nil
}

func (manager *Manager) RestoreRuntimeState(ctx context.Context) error {
	if manager.runtimeStore == nil {
		return ErrCandidateMismatch
	}
	states, err := manager.runtimeStore.ListRuntimeStateForRecovery(ctx)
	if err != nil {
		return err
	}
	for _, state := range states {
		if state.Active == nil {
			continue
		}
		candidate, err := manager.BuildCandidate(ctx, store.PendingSnapshot{DesiredSnapshot: state.Active.DesiredSnapshot, OperationState: store.OperationApplied})
		if err != nil {
			return err
		}
		if err := manager.publishApplied(candidate, *state.Active); err != nil {
			return err
		}
	}
	for _, state := range states {
		if state.Pending == nil {
			continue
		}
		if _, err := manager.BuildCandidate(ctx, *state.Pending); err != nil {
			return err
		}
	}
	return nil
}

func (manager *Manager) ActiveSnapshot(accountID domain.AccountID) *RoutingSnapshot {
	manager.groupMu.Lock()
	runtime := manager.groups[accountID]
	manager.groupMu.Unlock()
	if runtime == nil {
		return nil
	}
	snapshot := runtime.active.Load()
	if snapshot == nil {
		return nil
	}
	routes := make(map[domain.RouteID]RouteView, len(snapshot.Routes))
	for routeID, route := range snapshot.Routes {
		routes[routeID] = route
	}
	return &RoutingSnapshot{Fence: snapshot.Fence, Routes: routes}
}

func (manager *Manager) ActiveRoute(accountID domain.AccountID, product, settlement string) (domain.RouteID, *GenerationAdmission, error) {
	manager.groupMu.Lock()
	defer manager.groupMu.Unlock()
	runtime := manager.groups[accountID]
	if runtime == nil || runtime.activeGroup == nil || runtime.active.Load() == nil {
		return "", nil, ErrRuntimeBlocked
	}
	var matched domain.RouteID
	for routeID, route := range runtime.activeGroup.Routes {
		definition := route.Definition
		if definition.RequestedState != store.RouteActive || definition.Product != product || definition.SettlementAsset != settlement {
			continue
		}
		if matched != "" {
			return "", nil, ErrRuntimeBlocked
		}
		matched = routeID
	}
	if matched == "" {
		return "", nil, ErrRuntimeBlocked
	}
	return matched, runtime.activeGroup.Admission, nil
}

func (group *RuntimeGroup) destroy() {
	for _, route := range group.Routes {
		if route.Handle != nil {
			_ = route.Handle.Close()
		}
	}
	group.State = GroupDestroyed
}
