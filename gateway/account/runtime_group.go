package account

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/QuantProcessing/boltertrader/credential"
	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/gateway/publicconn"
)

var (
	ErrRuntimeCleanupFailed = errors.New("runtime cleanup failed")
)

type GroupState string

const (
	GroupPrepared  GroupState = "prepared"
	GroupActive    GroupState = "active"
	GroupDestroyed GroupState = "destroyed"
)

type ConnectionRoute interface {
	Probe(context.Context) error
	Close() error
}

type ConnectionClientRoute interface {
	SpotClient() (exchange.SpotClient, bool)
	PerpClient() (exchange.PerpClient, bool)
	OrderClient() (exchange.OrderREST, bool)
	ExactLookupReady() bool
}

type RuntimeGroupBuilder interface {
	BuildRoute(context.Context, ConnectionSpec, RouteDefinition, *credential.Decoded) (ConnectionRoute, error)
}

func (manager *Manager) SetRuntimeAccountObserver(observer RuntimeAccountObserver) error {
	if observer == nil {
		return errors.New("account observer is required")
	}
	manager.observerMu.Lock()
	defer manager.observerMu.Unlock()
	if manager.runtimeObserver != nil {
		return nil
	}

	manager.groupMu.Lock()
	accountIDs := make([]domain.AccountID, 0, len(manager.groups))
	for accountID, runtime := range manager.groups {
		if runtime == nil || runtime.active.Load() == nil {
			continue
		}
		runtime.supervisionRegistered = false
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
				manager.setSupervisionRegistered(attemptedID, false)
			}
			return fmt.Errorf("runtime account supervision failed")
		}
		manager.setSupervisionRegistered(accountID, true)
		attemptedAccountIDs = append(attemptedAccountIDs, accountID)
	}
	manager.runtimeObserver = observer
	return nil
}

func (manager *Manager) notifyAccountConnected(accountID domain.AccountID) error {
	manager.observerMu.RLock()
	observer := manager.runtimeObserver
	manager.observerMu.RUnlock()
	if observer != nil {
		if manager.supervisionIsRegistered(accountID) {
			return nil
		}
		if err := observer.RegisterAccount(accountID); err != nil {
			return fmt.Errorf("runtime account supervision failed")
		}
		manager.setSupervisionRegistered(accountID, true)
	}
	return nil
}

func (manager *Manager) supervisionIsRegistered(accountID domain.AccountID) bool {
	manager.groupMu.Lock()
	defer manager.groupMu.Unlock()
	runtime := manager.groups[accountID]
	return runtime != nil && runtime.active.Load() != nil && runtime.supervisionRegistered
}

func (manager *Manager) setSupervisionRegistered(accountID domain.AccountID, registered bool) {
	manager.groupMu.Lock()
	defer manager.groupMu.Unlock()
	if runtime := manager.groups[accountID]; runtime != nil && runtime.active.Load() != nil {
		runtime.supervisionRegistered = registered
	}
}

func (manager *Manager) notifyAccountDisconnected(accountID domain.AccountID) {
	manager.observerMu.RLock()
	observer := manager.runtimeObserver
	manager.observerMu.RUnlock()
	if observer != nil {
		observer.UnregisterAccount(accountID)
	}
}

type RouteRuntime struct {
	Definition        RouteDefinition
	Handle            ConnectionRoute
	PublicConnectorID string
	publicLease       publicconn.Lease
	closed            bool
}

type RouteView struct {
	RouteID         domain.RouteID
	Product         string
	SettlementAsset string
	RouteProfileID  string
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
	Snapshot  ConnectionSpec
	Routes    map[domain.RouteID]*RouteRuntime
	Admission *GenerationAdmission
	State     GroupState
	cleanupMu sync.Mutex
	cleanup   *runtimeCleanupAttempt
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
	active                atomic.Pointer[RoutingSnapshot]
	activeGroup           *RuntimeGroup
	supervisionRegistered bool
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

func newGenerationAdmission(snapshot ConnectionSpec) *GenerationAdmission {
	admission := NewGenerationAdmission()
	admission.fence = snapshot.Fence
	admission.routes = make(map[domain.RouteID]struct{}, len(snapshot.Routes))
	for _, route := range snapshot.Routes {
		admission.routes[route.RouteID] = struct{}{}
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
		clientRoute, ok := route.Handle.(ConnectionClientRoute)
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
		if definition.Product != product || definition.SettlementAsset != settlement {
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
func (manager *Manager) productClient(accountID domain.AccountID, product exchange.Product, settlement string) (ConnectionClientRoute, error) {
	if settlement == "" {
		return manager.activeProductClient(accountID, product)
	}
	manager.groupMu.Lock()
	defer manager.groupMu.Unlock()
	_, route, _, err := manager.activeRouteLocked(accountID, string(product), settlement)
	if err != nil {
		return nil, err
	}
	clientRoute, ok := route.Handle.(ConnectionClientRoute)
	if !ok {
		return nil, ErrProductMismatch
	}
	return clientRoute, nil
}

func (manager *Manager) activeProductClient(accountID domain.AccountID, product exchange.Product) (ConnectionClientRoute, error) {
	manager.groupMu.Lock()
	defer manager.groupMu.Unlock()
	runtime := manager.groups[accountID]
	if runtime == nil || runtime.activeGroup == nil || runtime.active.Load() == nil {
		return nil, ErrUnknownAccount
	}
	var matched ConnectionClientRoute
	for _, route := range runtime.activeGroup.Routes {
		if route.Definition.Product != string(product) {
			continue
		}
		clientRoute, ok := route.Handle.(ConnectionClientRoute)
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
	accountID   domain.AccountID
	venue       exchange.Venue
	environment exchange.Environment
	definition  RouteDefinition
	handle      ConnectionRoute
	publicLease publicconn.Lease
	admission   *GenerationAdmission
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
		result = append(result, activeRouteAccess{
			accountID: accountID, venue: exchange.Venue(group.Snapshot.Venue),
			environment: exchange.Environment(group.Snapshot.NativeEnvironment),
			definition:  route.Definition, handle: route.Handle, publicLease: route.publicLease,
			admission: group.Admission,
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
			if route.publicLease == nil {
				continue
			}
			if selector.RouteID != "" && selector.RouteID != routeID {
				continue
			}
			candidates = append(candidates, routeCandidate{
				routeID: routeID,
				route:   route,
				access: activeRouteAccess{
					accountID: accountID, venue: exchange.Venue(group.Snapshot.Venue),
					environment: exchange.Environment(group.Snapshot.NativeEnvironment),
					definition:  route.Definition, handle: route.Handle, publicLease: route.publicLease,
					admission: group.Admission,
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
		clientRoute, ok := matched.access.handle.(ConnectionClientRoute)
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
	return nil, ErrRuntimeBlocked
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
	// Captured route handles must stop accepting work even when cleanup comes
	// from process shutdown rather than an explicit account deletion.
	if err := group.Admission.CloseAndWait(ctx); err != nil {
		return err
	}
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
