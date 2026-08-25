package account

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantProcessing/boltertrader/credential"
	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/gateway/publicconn"
	gatewaysecret "github.com/QuantProcessing/boltertrader/gateway/secret"
	"github.com/QuantProcessing/boltertrader/gateway/store"
	"github.com/QuantProcessing/boltertrader/routeprofile"
	"github.com/QuantProcessing/boltertrader/secretstore"
)

var (
	ErrCandidateFailed      = errors.New("runtime candidate failed")
	ErrCandidateMismatch    = errors.New("runtime candidate fence mismatch")
	ErrRuntimeCleanupFailed = errors.New("runtime cleanup failed")
)

// Account recovery starts alongside the public market owners. A route probe may
// therefore share a constrained local proxy with several catalog bootstraps;
// keep its deadline aligned with the account HTTP client instead of rejecting
// a valid runtime during that initial burst.
const candidateCredentialProbeTimeout = 30 * time.Second

// CandidateFailure is the safe, persisted explanation for an atomic runtime
// candidate rejection. It never contains a venue message or credential
// material; callers may expose ReasonCode to an operator.
type CandidateFailure struct {
	RouteID    domain.RouteID
	ReasonCode string
}

func (failure CandidateFailure) Error() string {
	return ErrCandidateFailed.Error() + ": " + failure.ReasonCode
}

func (failure CandidateFailure) Unwrap() error { return ErrCandidateFailed }

func CandidateFailureReason(err error) string {
	var failure CandidateFailure
	if errors.As(err, &failure) && failure.ReasonCode != "" {
		return failure.ReasonCode
	}
	return "CANDIDATE_BUILD_FAILED"
}

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

type CandidateClientRoute interface {
	SpotClient() (exchange.SpotClient, bool)
	PerpClient() (exchange.PerpClient, bool)
	OrderClient() (exchange.OrderREST, bool)
	ExactLookupReady() bool
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
	LookupRotationFinalization(context.Context, store.RotationFinalizationRequest) (store.RotationFinalizationResult, error)
	FinalizeRotation(context.Context, store.RotationFinalizationRequest) (store.RotationFinalizationResult, error)
	RetireAccount(context.Context, store.RetirementRequest) (store.RetirementResult, error)
	ListRuntimeStateForRecovery(context.Context) ([]store.RuntimeState, error)
	IsSecretReferenceRetired(context.Context, secretstore.Scope, secretstore.OpaqueRef) (bool, error)
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

func (manager *Manager) SetRuntimeAccountObserver(observer RuntimeAccountObserver) error {
	if observer == nil {
		return ErrCandidateMismatch
	}
	manager.observerMu.Lock()
	defer manager.observerMu.Unlock()
	if manager.runtimeObserver != nil {
		return nil
	}
	manager.streamSupervision.Store(true)

	manager.groupMu.Lock()
	accountIDs := make([]domain.AccountID, 0, len(manager.groups))
	for accountID, runtime := range manager.groups {
		if runtime == nil || runtime.active.Load() == nil {
			continue
		}
		resetPrivateStreamRouteStates(runtime.activeGroup, true, "")
		accountIDs = append(accountIDs, accountID)
	}
	manager.groupMu.Unlock()
	sort.Slice(accountIDs, func(i, j int) bool { return accountIDs[i] < accountIDs[j] })
	attemptedAccountIDs := make([]domain.AccountID, 0, len(accountIDs))
	for _, accountID := range accountIDs {
		if err := observer.RegisterAccount(accountID); err != nil {
			attemptedAccountIDs = append(attemptedAccountIDs, accountID)
			for _, attemptedID := range attemptedAccountIDs {
				observer.UnregisterAccount(attemptedID)
			}
			return fmt.Errorf("runtime account supervision failed")
		}
		attemptedAccountIDs = append(attemptedAccountIDs, accountID)
	}
	manager.runtimeObserver = observer
	return nil
}

func (manager *Manager) notifyRuntimePromoted(accountID domain.AccountID) error {
	manager.observerMu.RLock()
	observer := manager.runtimeObserver
	manager.observerMu.RUnlock()
	if observer != nil {
		if err := observer.RegisterAccount(accountID); err != nil {
			return fmt.Errorf("runtime account supervision failed")
		}
	}
	return nil
}

func (manager *Manager) notifyRuntimeRetired(accountID domain.AccountID) {
	manager.observerMu.RLock()
	observer := manager.runtimeObserver
	manager.observerMu.RUnlock()
	if observer != nil {
		observer.UnregisterAccount(accountID)
	}
}

type RouteRuntime struct {
	Definition        store.RouteDefinition
	Handle            CandidateRoute
	PublicConnectorID string
	publicLease       publicconn.Lease
	closed            bool
}

type RouteView struct {
	RouteID         domain.RouteID
	Product         string
	SettlementAsset string
	RouteProfileID  string
	RequestedState  store.RouteState
	WriteReady      bool
}

type OrderRoutePurpose uint8

const (
	OrderRouteWrite OrderRoutePurpose = iota
	OrderRouteRecovery
	maxDynamicRouteSelectionAttempts = 3
)

type OrderRouteSelector struct {
	RouteID domain.RouteID
	Fence   domain.AccountFence
}

type SelectedOrderRoute struct {
	Dynamic    bool
	Fence      domain.AccountFence
	RouteID    domain.RouteID
	Instrument exchange.Instrument
	Status     Status
	Orders     exchange.OrderREST
	Admission  *GenerationAdmission
}

type RuntimeGroup struct {
	Snapshot       store.DesiredSnapshot
	Routes         map[domain.RouteID]*RouteRuntime
	privateStreams map[domain.RouteID]privateStreamState
	Admission      *GenerationAdmission
	State          GroupState
	cleanupMu      sync.Mutex
	cleanup        *runtimeCleanupAttempt
}

type privateStreamState struct {
	ready      bool
	reason     string
	snapshotID string
	sequence   uint64
}

type runtimeCleanupAttempt struct {
	done    chan struct{}
	err     error
	waiters int
}

type RoutingSnapshot struct {
	Fence  domain.AccountFence
	Routes map[domain.RouteID]RouteView
}

type accountRuntime struct {
	active      atomic.Pointer[RoutingSnapshot]
	activeGroup *RuntimeGroup
	candidate   *RuntimeGroup
	retiring    map[string]*RuntimeGroup
	retirement  *runtimeRetirement
}

type runtimeRetirement struct {
	operationID secretstore.OperationID
	digest      string
	result      store.RetirementResult
	groups      []*RuntimeGroup
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
		manager.groups[accountID] = &accountRuntime{retiring: make(map[string]*RuntimeGroup)}
	}
	return nil
}

func (manager *Manager) BuildCandidate(ctx context.Context, pending store.PendingSnapshot) (*RuntimeGroup, error) {
	return manager.buildCandidate(ctx, pending, false, true)
}

func (manager *Manager) buildCandidate(ctx context.Context, pending store.PendingSnapshot, lifecycleLocked, attachCandidate bool) (*RuntimeGroup, error) {
	if ctx == nil || manager.groupBuilder == nil {
		return nil, ErrCandidateFailed
	}
	manager.mu.RLock()
	closed := manager.closed
	manager.mu.RUnlock()
	if closed {
		return nil, ErrManagerClosed
	}
	if err := pending.DesiredSnapshot.Validate(); err != nil {
		return nil, ErrCandidateFailed
	}
	if manager.profileRegistry == nil || manager.profileRegistry.Validate(pending.DesiredSnapshot) != nil {
		return nil, ErrCandidateFailed
	}
	if manager.requirePublicConnectors && manager.publicConnectors == nil {
		return nil, ErrCandidateFailed
	}
	if manager.credentialResolver == nil {
		return nil, ErrCandidateFailed
	}
	if manager.runtimeStore != nil {
		retired, err := manager.runtimeStore.IsSecretReferenceRetired(ctx, secretstore.Scope{
			Namespace: secretstore.Namespace, KeyVersion: "v1",
		}, pending.SecretRef)
		if err != nil {
			return nil, ErrCandidateFailed
		}
		if retired {
			return nil, store.ErrRetiredSecretReference
		}
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
	manager.groupMu.Lock()
	ownership := manager.groups[pending.AccountID]
	manager.groupMu.Unlock()
	if ownership == nil {
		return nil, ErrCandidateFailed
	}
	group := &RuntimeGroup{
		Snapshot:       pending.DesiredSnapshot,
		Routes:         make(map[domain.RouteID]*RouteRuntime, len(pending.Routes)),
		privateStreams: make(map[domain.RouteID]privateStreamState, len(pending.Routes)),
		Admission:      newGenerationAdmission(pending.DesiredSnapshot),
		State:          GroupCandidate,
	}
	for _, definition := range pending.Routes {
		runtime := &RouteRuntime{Definition: definition}
		group.Routes[definition.RouteID] = runtime
		if definition.RequestedState == store.RouteDisabled {
			continue
		}
		state := privateStreamState{ready: !manager.streamSupervision.Load()}
		if !state.ready {
			state.reason = ReasonPrivateStreamStarting
		}
		group.privateStreams[definition.RouteID] = state
		if manager.publicConnectors != nil {
			key := routeprofile.Key{Venue: pending.Venue, RouteProfileID: definition.RouteProfileID}
			lease, err := manager.publicConnectors.Acquire(ctx, key)
			if err != nil || lease == nil {
				group.destroy()
				return nil, ErrCandidateFailed
			}
			runtime.publicLease = lease
			runtime.PublicConnectorID = lease.ConnectorID()
			if _, err := lease.Snapshot(); err != nil {
				if _, err := manager.publicConnectors.Refresh(ctx, key); err != nil {
					group.destroy()
					return nil, ErrCandidateFailed
				}
			}
		}
		handle, err := manager.groupBuilder.BuildRoute(ctx, pending.DesiredSnapshot, definition, decoded)
		if err != nil || handle == nil {
			group.destroy()
			return nil, ErrCandidateFailed
		}
		runtime.Handle = handle
	}
	// Credentials belong to the account, not to each configured route. Probing
	// every spot/settlement route made account creation both slow and brittle:
	// one credential was asked the same question several times before the
	// runtime could be promoted. Verify it once with a balance read,
	// preferring spot when present because it is the least product-specific
	// account endpoint. Route/private-stream readiness is observed separately
	// after promotion.
	probeRoute, probeDefinition := candidateCredentialProbe(group)
	if probeRoute == nil {
		group.destroy()
		return nil, ErrCandidateFailed
	}
	probeCtx, cancelProbe := context.WithTimeout(ctx, candidateCredentialProbeTimeout)
	err = probeRoute.Probe(probeCtx)
	cancelProbe()
	if err != nil {
		logCandidateProbeFailure(pending, probeDefinition, err)
		group.destroy()
		return nil, CandidateFailure{RouteID: probeDefinition.RouteID, ReasonCode: candidateProbeReason(err)}
	}
	if !lifecycleLocked {
		manager.closeMu.Lock()
		defer manager.closeMu.Unlock()
	}
	manager.mu.RLock()
	closed = manager.closed
	manager.mu.RUnlock()
	if closed {
		group.destroy()
		return nil, ErrManagerClosed
	}
	manager.groupMu.Lock()
	runtime := manager.groups[pending.AccountID]
	if runtime == nil || runtime != ownership || runtime.retirement != nil {
		manager.groupMu.Unlock()
		group.destroy()
		return nil, ErrCandidateFailed
	}
	if attachCandidate {
		if runtime.candidate != nil {
			runtime.candidate.destroy()
		}
		runtime.candidate = group
	}
	manager.groupMu.Unlock()
	return group, nil
}

func candidateCredentialProbe(group *RuntimeGroup) (CandidateRoute, store.RouteDefinition) {
	var fallback *RouteRuntime
	for _, definition := range group.Snapshot.Routes {
		runtime := group.Routes[definition.RouteID]
		if runtime == nil || runtime.Definition.RequestedState != store.RouteActive || runtime.Handle == nil {
			continue
		}
		if fallback == nil {
			fallback = runtime
		}
		if runtime.Definition.Product == string(exchange.ProductSpot) {
			return runtime.Handle, runtime.Definition
		}
	}
	if fallback == nil {
		return nil, store.RouteDefinition{}
	}
	return fallback.Handle, fallback.Definition
}

func candidateProbeReason(err error) string {
	var venueErr *exchange.Error
	if errors.As(err, &venueErr) {
		details := venueErr.Details()
		if details.Code == "33004" || details.Code == "-2015" {
			return "CREDENTIAL_EXPIRED"
		}
		switch venueErr.Kind() {
		case exchange.KindAuthentication:
			return "CREDENTIAL_INVALID"
		case exchange.KindPermission:
			return "CREDENTIAL_PERMISSION_DENIED"
		case exchange.KindRateLimit:
			return "VENUE_RATE_LIMITED"
		case exchange.KindTransport, exchange.KindDeadlineExceeded, exchange.KindCanceled:
			return "VENUE_UNAVAILABLE"
		case exchange.KindMalformedResponse:
			return "VENUE_RESPONSE_INVALID"
		}
	}
	return "CANDIDATE_BUILD_FAILED"
}

func logCandidateProbeFailure(pending store.PendingSnapshot, route store.RouteDefinition, err error) {
	attributes := []any{
		"account_id", pending.AccountID,
		"operation_id", pending.OperationID,
		"venue", pending.Venue,
		"route_id", route.RouteID,
	}
	var venueErr *exchange.Error
	if errors.As(err, &venueErr) {
		details := venueErr.Details()
		attributes = append(attributes, "kind", venueErr.Kind(), "venue_code", details.Code, "operation", details.Operation)
	} else {
		attributes = append(attributes, "kind", "unclassified")
	}
	slog.Warn("runtime candidate route probe failed", attributes...)
}

func (manager *Manager) PromoteCandidate(ctx context.Context, group *RuntimeGroup) (store.PromotionResult, error) {
	manager.closeMu.Lock()
	defer manager.closeMu.Unlock()
	manager.mu.RLock()
	closed := manager.closed
	manager.mu.RUnlock()
	if closed {
		return store.PromotionResult{}, ErrManagerClosed
	}
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
	if err := manager.notifyRuntimePromoted(group.Snapshot.AccountID); err != nil {
		return result, err
	}
	return result, nil
}

func (manager *Manager) FinalizeRotation(ctx context.Context, request store.RotationFinalizationRequest) (store.RotationFinalizationResult, error) {
	if ctx == nil || manager.runtimeStore == nil {
		return store.RotationFinalizationResult{}, ErrCandidateMismatch
	}
	if err := request.Validate(); err != nil {
		return store.RotationFinalizationResult{}, err
	}

	result, err := manager.runtimeStore.LookupRotationFinalization(ctx, request)
	if err == nil {
		if !rotationFinalizationMatches(request, result) {
			return store.RotationFinalizationResult{}, ErrCandidateMismatch
		}
		if err := manager.cleanupRetiringGeneration(ctx, request, false); err != nil {
			return store.RotationFinalizationResult{}, err
		}
		return result, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return store.RotationFinalizationResult{}, err
	}

	if err := manager.closeRetiringGeneration(ctx, request, true); err != nil {
		return store.RotationFinalizationResult{}, err
	}
	result, err = manager.runtimeStore.FinalizeRotation(ctx, request)
	if err != nil {
		return store.RotationFinalizationResult{}, err
	}
	if !rotationFinalizationMatches(request, result) {
		return store.RotationFinalizationResult{}, ErrCandidateMismatch
	}
	if err := manager.removeRetiringGeneration(ctx, request); err != nil {
		return store.RotationFinalizationResult{}, err
	}
	return result, nil
}

func (manager *Manager) cleanupRetiringGeneration(ctx context.Context, request store.RotationFinalizationRequest, requireActive bool) error {
	if err := manager.closeRetiringGeneration(ctx, request, requireActive); err != nil {
		return err
	}
	return manager.removeRetiringGeneration(ctx, request)
}

func (manager *Manager) closeRetiringGeneration(ctx context.Context, request store.RotationFinalizationRequest, requireActive bool) error {
	manager.groupMu.Lock()
	runtime := manager.groups[request.AccountID]
	if runtime == nil {
		manager.groupMu.Unlock()
		return nil
	}
	if requireActive {
		active := runtime.active.Load()
		if active == nil || runtime.activeGroup == nil || active.Fence != request.ExpectedActiveFence || runtime.activeGroup.Snapshot.Fence != request.ExpectedActiveFence {
			manager.groupMu.Unlock()
			return ErrStaleWriteFence
		}
	}
	group := runtime.retiring[request.ExpectedRetiringFence.RuntimeID]
	if group != nil && group.Snapshot.Fence != request.ExpectedRetiringFence {
		manager.groupMu.Unlock()
		return ErrStaleWriteFence
	}
	manager.groupMu.Unlock()
	if group == nil {
		return nil
	}
	return group.Admission.CloseAndWait(ctx)
}

func (manager *Manager) removeRetiringGeneration(ctx context.Context, request store.RotationFinalizationRequest) error {
	manager.groupMu.Lock()
	runtime := manager.groups[request.AccountID]
	if runtime == nil {
		manager.groupMu.Unlock()
		return nil
	}
	group := runtime.retiring[request.ExpectedRetiringFence.RuntimeID]
	if group == nil {
		manager.groupMu.Unlock()
		return nil
	}
	if group.Snapshot.Fence != request.ExpectedRetiringFence {
		manager.groupMu.Unlock()
		return ErrStaleWriteFence
	}
	manager.groupMu.Unlock()
	if err := group.destroyConfirmed(ctx); err != nil {
		return err
	}
	manager.groupMu.Lock()
	defer manager.groupMu.Unlock()
	runtime = manager.groups[request.AccountID]
	if runtime == nil {
		return nil
	}
	current := runtime.retiring[request.ExpectedRetiringFence.RuntimeID]
	if current == nil {
		return nil
	}
	if current != group || current.Snapshot.Fence != request.ExpectedRetiringFence {
		return ErrStaleWriteFence
	}
	delete(runtime.retiring, request.ExpectedRetiringFence.RuntimeID)
	return nil
}

func rotationFinalizationMatches(request store.RotationFinalizationRequest, result store.RotationFinalizationResult) bool {
	return result.OperationID == request.OperationID && result.AccountID == request.AccountID &&
		result.ActiveFence == request.ExpectedActiveFence && result.RetiringFence == request.ExpectedRetiringFence &&
		result.TombstoneRevision == request.ExpectedActiveFence.DesiredStateRevision &&
		result.RetiringState == "retired" && result.SecretReferenceState == "tombstoned" && !result.CompletedAt.IsZero()
}

func (manager *Manager) RetireRuntime(ctx context.Context, request store.RetirementRequest) (store.RetirementResult, error) {
	if ctx == nil || manager.runtimeStore == nil {
		return store.RetirementResult{}, ErrCandidateMismatch
	}
	if err := request.Validate(); err != nil {
		return store.RetirementResult{}, err
	}
	manager.closeMu.Lock()
	defer manager.closeMu.Unlock()

	manager.groupMu.Lock()
	runtime := manager.groups[request.AccountID]
	if runtime == nil {
		manager.groupMu.Unlock()
		return manager.replayRetirementWithoutLocalActive(ctx, request)
	}
	if runtime.retirement != nil {
		retirement := runtime.retirement
		digest, digestErr := request.CanonicalDigest()
		if digestErr != nil || retirement.operationID != request.OperationID || retirement.digest != digest {
			manager.groupMu.Unlock()
			return store.RetirementResult{}, store.ErrOperationConflict
		}
		manager.groupMu.Unlock()
		manager.notifyRuntimeRetired(request.AccountID)
		if err := manager.finishRuntimeRetirement(ctx, request.AccountID, runtime, retirement); err != nil {
			return store.RetirementResult{}, err
		}
		return retirement.result, nil
	}
	active := runtime.active.Load()
	if active == nil || runtime.activeGroup == nil {
		manager.groupMu.Unlock()
		return manager.replayRetirementWithoutLocalActive(ctx, request)
	}
	if active.Fence != request.ExpectedActiveFence {
		manager.groupMu.Unlock()
		return store.RetirementResult{}, ErrStaleWriteFence
	}
	groups := make([]*RuntimeGroup, 0, 2+len(runtime.retiring))
	seen := make(map[*RuntimeGroup]struct{}, 2+len(runtime.retiring))
	appendGroup := func(group *RuntimeGroup) {
		if group == nil {
			return
		}
		if _, exists := seen[group]; exists {
			return
		}
		seen[group] = struct{}{}
		groups = append(groups, group)
	}
	appendGroup(runtime.activeGroup)
	appendGroup(runtime.candidate)
	for _, retiring := range runtime.retiring {
		appendGroup(retiring)
	}
	manager.groupMu.Unlock()

	for _, group := range groups {
		if err := group.Admission.CloseAndWait(ctx); err != nil {
			return store.RetirementResult{}, err
		}
	}
	if manager.lifecycle == nil {
		return store.RetirementResult{}, fmt.Errorf("runtime lifecycle unavailable")
	}
	blockers, err := manager.lifecycle.Blockers(ctx, request.AccountID)
	if err != nil {
		return store.RetirementResult{}, fmt.Errorf("runtime blocker check failed")
	}
	if blockers.Any() {
		return store.RetirementResult{}, ErrRuntimeBlocked
	}
	result, err := manager.runtimeStore.RetireAccount(ctx, request)
	if err != nil {
		return store.RetirementResult{}, err
	}
	if !result.Tombstoned || result.OperationID != request.OperationID || result.AccountID != request.AccountID || result.TombstoneRevision != request.TombstoneRevision || result.CompletedAt.IsZero() {
		return store.RetirementResult{}, ErrCandidateMismatch
	}
	digest, err := request.CanonicalDigest()
	if err != nil {
		return store.RetirementResult{}, err
	}

	manager.groupMu.Lock()
	current := manager.groups[request.AccountID]
	if current == nil {
		manager.groupMu.Unlock()
		return result, nil
	}
	if current != runtime {
		manager.groupMu.Unlock()
		return store.RetirementResult{}, ErrCandidateMismatch
	}
	retirement := runtime.retirement
	if retirement != nil {
		if retirement.operationID != request.OperationID || retirement.digest != digest {
			manager.groupMu.Unlock()
			return store.RetirementResult{}, store.ErrOperationConflict
		}
	} else {
		retirement = &runtimeRetirement{
			operationID: request.OperationID,
			digest:      digest,
			result:      result,
			groups:      groups,
		}
		runtime.retirement = retirement
		runtime.active.Store(nil)
	}
	manager.groupMu.Unlock()
	manager.mu.Lock()
	delete(manager.runtimes, request.AccountID)
	manager.mu.Unlock()
	manager.notifyRuntimeRetired(request.AccountID)
	if err := manager.finishRuntimeRetirement(ctx, request.AccountID, runtime, retirement); err != nil {
		return store.RetirementResult{}, err
	}
	return retirement.result, nil
}

func (manager *Manager) replayRetirementWithoutLocalActive(ctx context.Context, request store.RetirementRequest) (store.RetirementResult, error) {
	states, err := manager.runtimeStore.ListRuntimeStateForRecovery(ctx)
	if err != nil {
		return store.RetirementResult{}, fmt.Errorf("runtime retirement state unavailable")
	}
	for index := range states {
		state := &states[index]
		if state.AccountID != request.AccountID {
			continue
		}
		if !state.Tombstoned {
			if state.Active == nil && state.Pending != nil && state.Pending.Fence == request.ExpectedActiveFence {
				// A pending-only snapshot has never owned the account's write
				// admission, so it cannot have venue inventory created through
				// this runtime. Tombstone it without asking a non-existent active
				// connector for positions. The durable store independently checks
				// that the account still has no active snapshot under its row lock.
				result, err := manager.runtimeStore.RetireAccount(ctx, request)
				if err != nil {
					return store.RetirementResult{}, err
				}
				if !result.Tombstoned || result.OperationID != request.OperationID || result.AccountID != request.AccountID ||
					result.TombstoneRevision != request.TombstoneRevision || result.CompletedAt.IsZero() {
					return store.RetirementResult{}, ErrCandidateMismatch
				}
				if err := manager.discardUnpromotedRuntime(ctx, request.AccountID); err != nil {
					return store.RetirementResult{}, err
				}
				return result, nil
			}
			if state.Active != nil && state.Active.Fence == request.ExpectedActiveFence && manager.lifecycle != nil {
				if _, err := manager.lifecycle.Blockers(ctx, request.AccountID); err != nil {
					return store.RetirementResult{}, fmt.Errorf("runtime blocker check failed")
				}
			}
			return store.RetirementResult{}, ErrRuntimeBlocked
		}
		result, err := manager.runtimeStore.RetireAccount(ctx, request)
		if err != nil {
			if errors.Is(err, store.ErrOperationConflict) {
				return store.RetirementResult{}, store.ErrOperationConflict
			}
			return store.RetirementResult{}, fmt.Errorf("runtime retirement replay failed")
		}
		if !result.Tombstoned || result.OperationID != request.OperationID || result.AccountID != request.AccountID ||
			result.TombstoneRevision != request.TombstoneRevision || result.CompletedAt.IsZero() {
			return store.RetirementResult{}, ErrCandidateMismatch
		}
		return result, nil
	}
	return store.RetirementResult{}, ErrRuntimeBlocked
}

func (manager *Manager) discardUnpromotedRuntime(ctx context.Context, accountID domain.AccountID) error {
	manager.groupMu.Lock()
	runtime := manager.groups[accountID]
	if runtime == nil {
		manager.groupMu.Unlock()
		return nil
	}
	if runtime.active.Load() != nil || runtime.activeGroup != nil || runtime.retirement != nil {
		manager.groupMu.Unlock()
		return ErrRuntimeBlocked
	}
	candidate := runtime.candidate
	manager.groupMu.Unlock()

	if candidate != nil {
		if err := candidate.Admission.CloseAndWait(ctx); err != nil {
			return err
		}
		if err := candidate.destroyConfirmed(ctx); err != nil {
			return err
		}
	}

	manager.groupMu.Lock()
	current := manager.groups[accountID]
	if current != runtime || current.active.Load() != nil || current.activeGroup != nil || current.retirement != nil || current.candidate != candidate {
		manager.groupMu.Unlock()
		return ErrCandidateMismatch
	}
	delete(manager.groups, accountID)
	manager.groupMu.Unlock()
	manager.mu.Lock()
	delete(manager.runtimes, accountID)
	manager.mu.Unlock()
	manager.notifyRuntimeRetired(accountID)
	return nil
}

func (manager *Manager) finishRuntimeRetirement(ctx context.Context, accountID domain.AccountID, runtime *accountRuntime, retirement *runtimeRetirement) error {
	for _, group := range retirement.groups {
		if err := group.destroyConfirmed(ctx); err != nil {
			return err
		}
	}

	manager.groupMu.Lock()
	defer manager.groupMu.Unlock()
	current := manager.groups[accountID]
	if current == nil {
		return nil
	}
	if current != runtime || current.retirement != retirement {
		return ErrCandidateMismatch
	}
	current.activeGroup = nil
	current.candidate = nil
	current.retiring = make(map[string]*RuntimeGroup)
	current.retirement = nil
	delete(manager.groups, accountID)
	return nil
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
		runtime.retiring[old.Snapshot.Fence.RuntimeID] = old
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
	if manager.streamSupervision.Load() {
		resetPrivateStreamRouteStates(group, true, "")
	}
	runtime.activeGroup = group
	runtime.candidate = nil
	runtime.active.Store(&RoutingSnapshot{Fence: active.Fence, Routes: routes})
	return manager.publishControlPlaneRuntime(active.DesiredSnapshot)
}

func (manager *Manager) publishControlPlaneRuntime(snapshot store.DesiredSnapshot) error {
	ref, err := gatewaysecret.ParseOpaqueRef(snapshot.SecretRef.RawForTrustedBoundary())
	if err != nil {
		return ErrCandidateMismatch
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	version := uint64(1)
	if current, ok := manager.runtimes[snapshot.AccountID]; ok {
		version = current.RuntimeVersion + 1
	}
	desired := DesiredRuntime{
		AccountID: snapshot.AccountID, Venue: snapshot.Venue, VenueEnvironment: snapshot.NativeEnvironment,
		SecretRef: ref, RuntimeID: snapshot.Fence.RuntimeID, RequestedState: RuntimeStateActive,
	}
	manager.runtimes[snapshot.AccountID] = appliedRuntime(desired, RuntimeStateActive, true, version)
	return nil
}

// RestoreActiveRuntime rebuilds an exact durable active generation without
// repeating its already-committed promotion.
func (manager *Manager) RestoreActiveRuntime(ctx context.Context, expected store.ActiveSnapshot) error {
	if ctx == nil || manager.runtimeStore == nil || expected.PromotedAt.IsZero() || expected.DesiredSnapshot.Validate() != nil {
		return fmt.Errorf("runtime active restore failed")
	}
	manager.closeMu.Lock()
	defer manager.closeMu.Unlock()
	if err := manager.restoreActiveRuntimeLocked(ctx, expected); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, ErrManagerClosed) {
			return ErrManagerClosed
		}
		// CandidateFailure contains only a route id and a stable, deliberately
		// safe reason code. Preserve it so a restart does not turn an actionable
		// credential failure into the opaque `runtime_not_promoted` state.
		var candidateFailure CandidateFailure
		if errors.As(err, &candidateFailure) {
			return candidateFailure
		}
		return fmt.Errorf("runtime active restore failed")
	}
	return nil
}

func (manager *Manager) restoreActiveRuntimeLocked(ctx context.Context, expected store.ActiveSnapshot) error {
	manager.mu.RLock()
	closed := manager.closed
	manager.mu.RUnlock()
	if closed {
		return ErrManagerClosed
	}
	states, err := manager.runtimeStore.ListRuntimeStateForRecovery(ctx)
	if err != nil {
		return err
	}
	var durable *store.ActiveSnapshot
	for index := range states {
		state := &states[index]
		if state.AccountID != expected.AccountID {
			continue
		}
		if state.Tombstoned || state.Active == nil || !sameDurableActive(*state.Active, expected) {
			return ErrCandidateMismatch
		}
		durable = state.Active
		break
	}
	if durable == nil {
		return ErrCandidateMismatch
	}

	manager.groupMu.Lock()
	runtime := manager.groups[durable.AccountID]
	if runtime != nil && runtime.activeGroup != nil && runtime.active.Load() != nil {
		active := runtime.active.Load()
		exact := active.Fence == durable.Fence && runtime.activeGroup.Snapshot.SnapshotID == durable.SnapshotID &&
			runtime.activeGroup.Snapshot.OperationID == durable.OperationID && runtime.activeGroup.Snapshot.Fence == durable.Fence
		supervisionHealthy := privateStreamRoutesReady(runtime.activeGroup)
		manager.groupMu.Unlock()
		if !exact {
			return ErrCandidateMismatch
		}
		if !supervisionHealthy {
			if err := manager.notifyRuntimePromoted(durable.AccountID); err != nil {
				return err
			}
		}
		return manager.markRuntimeRestoreReady(durable.DesiredSnapshot)
	}
	if runtime != nil && (runtime.activeGroup != nil || runtime.active.Load() != nil || runtime.retirement != nil) {
		manager.groupMu.Unlock()
		return ErrCandidateMismatch
	}
	manager.groupMu.Unlock()

	group, err := manager.buildCandidate(ctx, store.PendingSnapshot{
		DesiredSnapshot: durable.DesiredSnapshot,
		OperationState:  store.OperationApplied,
	}, true, false)
	if err != nil {
		return err
	}
	if err := manager.publishRestoredActive(group, *durable); err != nil {
		group.destroy()
		return err
	}
	if err := manager.notifyRuntimePromoted(durable.AccountID); err != nil {
		return err
	}
	return nil
}

func privateStreamRoutesReady(group *RuntimeGroup) bool {
	if group == nil || len(group.privateStreams) == 0 {
		return false
	}
	for _, state := range group.privateStreams {
		if !state.ready {
			return false
		}
	}
	return true
}

func sameDurableActive(actual, expected store.ActiveSnapshot) bool {
	if actual.SnapshotID != expected.SnapshotID || actual.OperationID != expected.OperationID ||
		actual.AccountID != expected.AccountID || actual.Fence != expected.Fence ||
		actual.RequestDigest != expected.RequestDigest {
		return false
	}
	actualDigest, actualErr := actual.DesiredSnapshot.CanonicalDigest()
	expectedDigest, expectedErr := expected.DesiredSnapshot.CanonicalDigest()
	return actualErr == nil && expectedErr == nil && actualDigest == expectedDigest
}

func (manager *Manager) publishRestoredActive(group *RuntimeGroup, active store.ActiveSnapshot) error {
	manager.groupMu.Lock()
	runtime := manager.groups[group.Snapshot.AccountID]
	if runtime == nil || runtime.retirement != nil || runtime.activeGroup != nil || runtime.active.Load() != nil {
		manager.groupMu.Unlock()
		return ErrCandidateMismatch
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
	if manager.streamSupervision.Load() {
		resetPrivateStreamRouteStates(group, true, "")
	}
	runtime.activeGroup = group
	runtime.active.Store(&RoutingSnapshot{Fence: active.Fence, Routes: routes})
	manager.groupMu.Unlock()
	return manager.publishControlPlaneRuntime(active.DesiredSnapshot)
}

func (manager *Manager) markRuntimeRestoreReady(snapshot store.DesiredSnapshot) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	runtime, ok := manager.runtimes[snapshot.AccountID]
	if !ok || runtime.Desired.Fence() != (WriteFence{
		AccountID: snapshot.AccountID, RuntimeID: snapshot.Fence.RuntimeID,
	}) {
		return ErrCandidateMismatch
	}
	runtime.Ready = true
	runtime.Reason = ""
	runtime.ObservedAt = time.Now().UTC()
	manager.runtimes[snapshot.AccountID] = runtime
	return nil
}

func (manager *Manager) RestoreRuntimeState(ctx context.Context) error {
	if ctx == nil || manager.runtimeStore == nil {
		return ErrCandidateMismatch
	}
	states, err := manager.runtimeStore.ListRuntimeStateForRecovery(ctx)
	if err != nil {
		return err
	}
	for _, state := range states {
		if state.Tombstoned || state.Active == nil {
			continue
		}
		if err := manager.RestoreActiveRuntime(ctx, *state.Active); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			manager.recordRuntimeRestoreFailure(state.Active.DesiredSnapshot, err)
			continue
		}
	}
	for _, state := range states {
		if state.Tombstoned {
			continue
		}
		for _, retiring := range state.Retiring {
			group, err := manager.BuildCandidate(ctx, store.PendingSnapshot{DesiredSnapshot: retiring.DesiredSnapshot, OperationState: store.OperationApplied})
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				manager.recordRuntimeRestoreFailure(retiring.DesiredSnapshot, err)
				continue
			}
			if err := manager.installRestoredRetiring(ctx, group); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				group.destroy()
				manager.recordRuntimeRestoreFailure(retiring.DesiredSnapshot, err)
			}
		}
	}
	for _, state := range states {
		if state.Tombstoned || state.Pending == nil {
			continue
		}
		if _, err := manager.BuildCandidate(ctx, *state.Pending); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			manager.recordRuntimeRestoreFailure(state.Pending.DesiredSnapshot, err)
		}
	}
	return nil
}

func (manager *Manager) installRestoredRetiring(ctx context.Context, group *RuntimeGroup) error {
	if err := group.Admission.CloseAndWait(ctx); err != nil {
		return err
	}
	manager.groupMu.Lock()
	defer manager.groupMu.Unlock()
	runtime := manager.groups[group.Snapshot.AccountID]
	if runtime == nil || runtime.candidate != group || runtime.retirement != nil {
		return ErrCandidateMismatch
	}
	if existing := runtime.retiring[group.Snapshot.Fence.RuntimeID]; existing != nil {
		return ErrCandidateMismatch
	}
	group.State = GroupRetiring
	runtime.candidate = nil
	runtime.retiring[group.Snapshot.Fence.RuntimeID] = group
	return nil
}

func (manager *Manager) recordRuntimeRestoreFailure(snapshot store.DesiredSnapshot, cause error) {
	reason := CandidateFailureReason(cause)
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if runtime, ok := manager.runtimes[snapshot.AccountID]; ok {
		runtime.Ready = false
		runtime.Reason = reason
		runtime.ObservedAt = time.Now().UTC()
		manager.runtimes[snapshot.AccountID] = runtime
		return
	}
	ref, err := gatewaysecret.ParseOpaqueRef(snapshot.SecretRef.RawForTrustedBoundary())
	if err != nil {
		return
	}
	desired := DesiredRuntime{
		AccountID: snapshot.AccountID, Venue: snapshot.Venue, VenueEnvironment: snapshot.NativeEnvironment,
		SecretRef: ref, RuntimeID: snapshot.Fence.RuntimeID, RequestedState: RuntimeStateActive,
	}
	runtime := appliedRuntime(desired, RuntimeStateActive, false, 1)
	runtime.Reason = reason
	manager.runtimes[snapshot.AccountID] = runtime
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
	routeID, _, group, err := manager.activeRouteLocked(accountID, product, settlement)
	if err != nil {
		return "", nil, ErrRuntimeBlocked
	}
	return routeID, group.Admission, nil
}

func (manager *Manager) ActiveRouteClient(accountID domain.AccountID, product, settlement string) (domain.RouteID, exchange.OrderREST, *GenerationAdmission, error) {
	manager.groupMu.Lock()
	routeID, route, group, err := manager.activeRouteLocked(accountID, product, settlement)
	manager.groupMu.Unlock()
	if err == nil {
		clientRoute, ok := route.Handle.(CandidateClientRoute)
		if !ok {
			return "", nil, nil, ErrRuntimeBlocked
		}
		orders, ok := clientRoute.OrderClient()
		if !ok || orders == nil {
			return "", nil, nil, ErrRuntimeBlocked
		}
		return routeID, orders, group.Admission, nil
	}

	// Static accounts remain supported for migration-only callers. They have no
	// runtime route or generation admission.
	switch exchange.Product(product) {
	case exchange.ProductSpot:
		client, clientErr := manager.Spot(accountID)
		if clientErr != nil {
			return "", nil, nil, clientErr
		}
		return "", client, nil, nil
	case exchange.ProductPerp:
		client, clientErr := manager.Perp(accountID)
		if clientErr != nil {
			return "", nil, nil, clientErr
		}
		return "", client, nil, nil
	default:
		return "", nil, nil, ErrProductMismatch
	}
}

func (manager *Manager) activeRouteLocked(accountID domain.AccountID, product, settlement string) (domain.RouteID, *RouteRuntime, *RuntimeGroup, error) {
	runtime := manager.groups[accountID]
	if runtime == nil || runtime.activeGroup == nil || runtime.active.Load() == nil {
		return "", nil, nil, ErrRuntimeBlocked
	}
	var matched domain.RouteID
	var matchedRoute *RouteRuntime
	for routeID, route := range runtime.activeGroup.Routes {
		definition := route.Definition
		if definition.RequestedState != store.RouteActive || definition.Product != product || definition.SettlementAsset != settlement {
			continue
		}
		if matched != "" {
			return "", nil, nil, ErrRuntimeBlocked
		}
		matched, matchedRoute = routeID, route
	}
	if matched == "" || matchedRoute == nil {
		return "", nil, nil, ErrRuntimeBlocked
	}
	return matched, matchedRoute, runtime.activeGroup, nil
}

// productClient resolves the client handle a caller should use for one
// product. A named settlement selects exactly one active route; an empty one
// falls back to the whole-account lookup, which by design refuses to guess
// when the account runs more than one route for the product.
func (manager *Manager) productClient(accountID domain.AccountID, product exchange.Product, settlement string) (CandidateClientRoute, error) {
	if settlement == "" {
		return manager.activeProductClient(accountID, product)
	}
	manager.groupMu.Lock()
	defer manager.groupMu.Unlock()
	_, route, _, err := manager.activeRouteLocked(accountID, string(product), settlement)
	if err != nil {
		return nil, err
	}
	clientRoute, ok := route.Handle.(CandidateClientRoute)
	if !ok {
		return nil, ErrProductMismatch
	}
	return clientRoute, nil
}

func (manager *Manager) activeProductClient(accountID domain.AccountID, product exchange.Product) (CandidateClientRoute, error) {
	manager.groupMu.Lock()
	defer manager.groupMu.Unlock()
	runtime := manager.groups[accountID]
	if runtime == nil || runtime.activeGroup == nil || runtime.active.Load() == nil {
		return nil, ErrUnknownAccount
	}
	var matched CandidateClientRoute
	for _, route := range runtime.activeGroup.Routes {
		if route.Definition.RequestedState != store.RouteActive || route.Definition.Product != string(product) {
			continue
		}
		clientRoute, ok := route.Handle.(CandidateClientRoute)
		if !ok || matched != nil {
			return nil, ErrProductMismatch
		}
		matched = clientRoute
	}
	if matched == nil {
		return nil, ErrProductMismatch
	}
	return matched, nil
}

type activeRouteAccess struct {
	accountID           domain.AccountID
	venue               exchange.Venue
	environment         exchange.Environment
	definition          store.RouteDefinition
	handle              CandidateRoute
	publicLease         publicconn.Lease
	admission           *GenerationAdmission
	runtimeGeneration   uint64
	privateStreamReady  bool
	privateStreamReason string
}

func (manager *Manager) activeRouteAccesses(accountID domain.AccountID) ([]activeRouteAccess, error) {
	manager.groupMu.Lock()
	defer manager.groupMu.Unlock()
	runtime := manager.groups[accountID]
	if runtime == nil || runtime.activeGroup == nil || runtime.active.Load() == nil {
		return nil, ErrUnknownAccount
	}
	group := runtime.activeGroup
	result := make([]activeRouteAccess, 0, len(group.Routes))
	for _, route := range group.Routes {
		if route.Definition.RequestedState != store.RouteActive {
			continue
		}
		privateStream := group.privateStreams[route.Definition.RouteID]
		result = append(result, activeRouteAccess{
			accountID: accountID, venue: exchange.Venue(group.Snapshot.Venue),
			environment: exchange.Environment(group.Snapshot.NativeEnvironment),
			definition:  route.Definition, handle: route.Handle, publicLease: route.publicLease,
			admission: group.Admission, runtimeGeneration: group.Snapshot.Fence.RuntimeGeneration,
			privateStreamReady: privateStream.ready, privateStreamReason: privateStream.reason,
		})
	}
	if len(result) == 0 {
		return nil, ErrRuntimeBlocked
	}
	sort.Slice(result, func(i, j int) bool { return result[i].definition.RouteID < result[j].definition.RouteID })
	return result, nil
}

// RuntimePositionBlockers reads venue-authoritative positions from every
// active perpetual route and rejects evidence gathered across a generation
// change.
func (manager *Manager) RuntimePositionBlockers(ctx context.Context, accountID domain.AccountID) (int, error) {
	if ctx == nil || accountID.Validate() != nil {
		return 0, fmt.Errorf("runtime position blocker lookup failed")
	}
	if err := ctx.Err(); err != nil {
		return 0, fmt.Errorf("runtime position blocker lookup failed")
	}

	manager.groupMu.Lock()
	runtime := manager.groups[accountID]
	if runtime == nil || runtime.retirement != nil || runtime.activeGroup == nil {
		manager.groupMu.Unlock()
		return 0, ErrUnknownAccount
	}
	group := runtime.activeGroup
	active := runtime.active.Load()
	if active == nil || active.Fence != group.Snapshot.Fence {
		manager.groupMu.Unlock()
		return 0, fmt.Errorf("runtime position blocker lookup failed")
	}
	clients := make([]exchange.PerpAccountREST, 0, len(group.Routes))
	for _, route := range group.Routes {
		if route.Definition.RequestedState != store.RouteActive || route.Definition.Product != string(exchange.ProductPerp) {
			continue
		}
		clientRoute, ok := route.Handle.(CandidateClientRoute)
		if !ok {
			manager.groupMu.Unlock()
			return 0, fmt.Errorf("runtime position blocker lookup failed")
		}
		client, ok := clientRoute.PerpClient()
		if !ok || client == nil {
			manager.groupMu.Unlock()
			return 0, fmt.Errorf("runtime position blocker lookup failed")
		}
		clients = append(clients, client)
	}
	manager.groupMu.Unlock()

	positions := 0
	for _, client := range clients {
		rows, err := client.Positions(ctx, exchange.PositionsRequest{})
		if err != nil {
			return 0, fmt.Errorf("runtime position blocker lookup failed")
		}
		for _, row := range rows {
			if !row.Quantity.IsZero() {
				positions++
			}
		}
	}

	manager.groupMu.Lock()
	current := manager.groups[accountID]
	consistent := current == runtime && current != nil && current.retirement == nil &&
		current.activeGroup == group && current.active.Load() != nil && current.active.Load().Fence == group.Snapshot.Fence
	manager.groupMu.Unlock()
	if !consistent {
		return 0, fmt.Errorf("runtime position blocker lookup failed")
	}
	return positions, nil
}

func (manager *Manager) SelectOrderRoute(
	ctx context.Context,
	accountID domain.AccountID,
	instrumentID domain.InstrumentID,
	purpose OrderRoutePurpose,
	selector OrderRouteSelector,
) (SelectedOrderRoute, error) {
	if ctx == nil {
		return SelectedOrderRoute{}, fmt.Errorf("order route context is required")
	}
	if err := ctx.Err(); err != nil {
		return SelectedOrderRoute{}, err
	}
	if err := accountID.Validate(); err != nil {
		return SelectedOrderRoute{}, err
	}
	if err := instrumentID.Validate(); err != nil {
		return SelectedOrderRoute{}, err
	}
	if purpose != OrderRouteWrite && purpose != OrderRouteRecovery {
		return SelectedOrderRoute{}, ErrRuntimeBlocked
	}
	if selector.RouteID != "" && selector.RouteID.Validate() != nil {
		return SelectedOrderRoute{}, ErrRuntimeBlocked
	}
	if selector.Fence != (domain.AccountFence{}) && selector.Fence.Validate() != nil {
		return SelectedOrderRoute{}, ErrRuntimeBlocked
	}

	manager.mu.RLock()
	_, static := manager.entries[accountID]
	closed := manager.closed
	manager.mu.RUnlock()
	if closed {
		return SelectedOrderRoute{}, ErrManagerClosed
	}
	if static {
		return manager.selectStaticOrderRoute(ctx, accountID, instrumentID, purpose)
	}

	for attempt := 0; attempt < maxDynamicRouteSelectionAttempts; attempt++ {
		type routeCandidate struct {
			routeID domain.RouteID
			route   *RouteRuntime
			access  activeRouteAccess
		}
		manager.groupMu.Lock()
		runtime := manager.groups[accountID]
		if runtime == nil {
			manager.groupMu.Unlock()
			return SelectedOrderRoute{}, ErrUnknownAccount
		}
		group, err := selectRuntimeGroup(runtime, purpose, selector.Fence)
		if err != nil {
			manager.groupMu.Unlock()
			return SelectedOrderRoute{}, err
		}
		candidates := make([]routeCandidate, 0, len(group.Routes))
		for routeID, route := range group.Routes {
			if route.Definition.RequestedState != store.RouteActive || route.publicLease == nil {
				continue
			}
			if selector.RouteID != "" && selector.RouteID != routeID {
				continue
			}
			privateStream := group.privateStreams[routeID]
			candidates = append(candidates, routeCandidate{
				routeID: routeID,
				route:   route,
				access: activeRouteAccess{
					accountID: accountID, venue: exchange.Venue(group.Snapshot.Venue),
					environment: exchange.Environment(group.Snapshot.NativeEnvironment),
					definition:  route.Definition, handle: route.Handle, publicLease: route.publicLease,
					admission: group.Admission, runtimeGeneration: group.Snapshot.Fence.RuntimeGeneration,
					privateStreamReady: privateStream.ready, privateStreamReason: privateStream.reason,
				},
			})
		}
		manager.groupMu.Unlock()
		sort.Slice(candidates, func(i, j int) bool { return candidates[i].routeID < candidates[j].routeID })

		var matched *routeCandidate
		var instrument exchange.Instrument
		for index := range candidates {
			candidate := &candidates[index]
			resolved, resolveErr := candidate.access.publicLease.Resolve(ctx, instrumentID)
			if resolveErr != nil {
				continue
			}
			if string(resolved.Identity.Venue) != group.Snapshot.Venue ||
				string(resolved.Identity.Product) != candidate.access.definition.Product ||
				resolved.Identity.SettleAsset != candidate.access.definition.SettlementAsset {
				return SelectedOrderRoute{}, ErrRuntimeBlocked
			}
			if matched != nil {
				return SelectedOrderRoute{}, ErrRuntimeBlocked
			}
			matched, instrument = candidate, resolved
		}
		if matched == nil {
			return SelectedOrderRoute{}, ErrRuntimeBlocked
		}
		clientRoute, ok := matched.access.handle.(CandidateClientRoute)
		if !ok {
			return SelectedOrderRoute{}, ErrRuntimeBlocked
		}
		orders, ok := clientRoute.OrderClient()
		if !ok || orders == nil {
			return SelectedOrderRoute{}, ErrRuntimeBlocked
		}
		status := manager.dynamicRouteStatus(matched.access, purpose == OrderRouteWrite)

		manager.groupMu.Lock()
		current := manager.groups[accountID]
		if current == nil || current != runtime {
			manager.groupMu.Unlock()
			return SelectedOrderRoute{}, ErrRuntimeBlocked
		}
		currentGroup, currentErr := selectRuntimeGroup(current, purpose, selector.Fence)
		if currentErr != nil || currentGroup != group || currentGroup.Snapshot.Fence != group.Snapshot.Fence ||
			currentGroup.Admission != matched.access.admission || currentGroup.Routes[matched.routeID] != matched.route {
			manager.groupMu.Unlock()
			if purpose == OrderRouteWrite && selector.Fence == (domain.AccountFence{}) {
				continue
			}
			return SelectedOrderRoute{}, ErrRuntimeBlocked
		}
		manager.groupMu.Unlock()
		return SelectedOrderRoute{
			Dynamic: true, Fence: group.Snapshot.Fence, RouteID: matched.access.definition.RouteID,
			Instrument: instrument, Status: status,
			Orders: orders, Admission: group.Admission,
		}, nil
	}
	return SelectedOrderRoute{}, ErrRuntimeBlocked
}

func (manager *Manager) selectStaticOrderRoute(
	ctx context.Context,
	accountID domain.AccountID,
	instrumentID domain.InstrumentID,
	purpose OrderRoutePurpose,
) (SelectedOrderRoute, error) {
	instrument, err := manager.Resolve(ctx, accountID, instrumentID)
	if err != nil {
		return SelectedOrderRoute{}, err
	}
	var status Status
	if purpose == OrderRouteRecovery {
		status, err = manager.RecoveryStatus(accountID)
	} else {
		status, err = manager.Status(accountID)
	}
	if err != nil {
		return SelectedOrderRoute{}, err
	}
	var orders exchange.OrderREST
	switch instrument.Identity.Product {
	case exchange.ProductSpot:
		orders, err = manager.Spot(accountID)
	case exchange.ProductPerp:
		orders, err = manager.Perp(accountID)
	default:
		err = ErrProductMismatch
	}
	if err != nil || orders == nil {
		return SelectedOrderRoute{}, ErrRuntimeBlocked
	}
	return SelectedOrderRoute{Instrument: instrument, Status: status, Orders: orders}, nil
}

func selectRuntimeGroup(runtime *accountRuntime, purpose OrderRoutePurpose, fence domain.AccountFence) (*RuntimeGroup, error) {
	active := runtime.active.Load()
	if purpose == OrderRouteWrite || fence == (domain.AccountFence{}) {
		if active == nil || runtime.activeGroup == nil || runtime.activeGroup.Snapshot.Fence != active.Fence {
			return nil, ErrRuntimeBlocked
		}
		if fence != (domain.AccountFence{}) && active.Fence != fence {
			return nil, ErrRuntimeBlocked
		}
		return runtime.activeGroup, nil
	}
	if active != nil && runtime.activeGroup != nil && active.Fence == fence && runtime.activeGroup.Snapshot.Fence == fence {
		return runtime.activeGroup, nil
	}
	retiring := runtime.retiring[fence.RuntimeID]
	if retiring == nil || retiring.Snapshot.Fence != fence {
		return nil, ErrRuntimeBlocked
	}
	return retiring, nil
}

func (group *RuntimeGroup) destroy() {
	for _, route := range group.Routes {
		if route.Handle != nil {
			_ = route.Handle.Close()
		}
		if route.publicLease != nil {
			route.publicLease.Release()
			route.publicLease = nil
		}
	}
	group.State = GroupDestroyed
}

func (group *RuntimeGroup) destroyConfirmed(ctx context.Context) error {
	group.cleanupMu.Lock()
	if group.State == GroupDestroyed {
		group.cleanupMu.Unlock()
		return nil
	}
	if group.cleanup != nil {
		attempt := group.cleanup
		attempt.waiters++
		group.cleanupMu.Unlock()
		var err error
		select {
		case <-attempt.done:
			err = attempt.err
		case <-ctx.Done():
			err = ctx.Err()
		}
		group.cleanupMu.Lock()
		attempt.waiters--
		group.cleanupMu.Unlock()
		return err
	}
	attempt := &runtimeCleanupAttempt{done: make(chan struct{})}
	group.cleanup = attempt
	group.cleanupMu.Unlock()

	failed := false
	for _, route := range group.Routes {
		if route.Handle != nil && !route.closed {
			if err := route.Handle.Close(); err != nil {
				failed = true
				continue
			}
			route.closed = true
		}
		if route.publicLease != nil {
			route.publicLease.Release()
			route.publicLease = nil
		}
	}
	if failed {
		attempt.err = ErrRuntimeCleanupFailed
	}

	group.cleanupMu.Lock()
	if attempt.err == nil {
		group.State = GroupDestroyed
	}
	group.cleanup = nil
	close(attempt.done)
	group.cleanupMu.Unlock()
	return attempt.err
}
