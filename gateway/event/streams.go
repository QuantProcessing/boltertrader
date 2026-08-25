package event

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/account"
	"github.com/QuantProcessing/boltertrader/gateway/catalog"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/gateway/order"
	"github.com/QuantProcessing/boltertrader/gateway/store"
)

// errAccountRuntimeUnavailable marks an account that has no installed runtime
// group and therefore cannot carry a private stream yet. It is deliberately
// unexported and never reaches a caller outside this package: Start treats it
// as "skip this account", and every other path treats it as a plain error.
var errAccountRuntimeUnavailable = errors.New("account runtime is unavailable")

type StreamAccounts interface {
	Status(domain.AccountID) (account.Status, error)
	Instruments(domain.AccountID) (catalog.Snapshot, error)
	Resolve(context.Context, domain.AccountID, domain.InstrumentID) (exchange.Instrument, error)
	Spot(domain.AccountID) (exchange.SpotClient, error)
	Perp(domain.AccountID) (exchange.PerpClient, error)
	SetPrivateStreamState(domain.AccountID, bool, string) error
}

type atomicStreamRouteSource interface {
	SelectOrderRoute(context.Context, domain.AccountID, domain.InstrumentID, account.OrderRoutePurpose, account.OrderRouteSelector) (account.SelectedOrderRoute, error)
}

type privateStreamBarrierObserver interface {
	ObservePrivateStreamBarrier(domain.AccountID, string, uint64) error
}

type privateStreamRouteObserver interface {
	SetPrivateStreamRouteState(domain.WriteFence, bool, string) error
	ObservePrivateStreamRouteBarrier(domain.WriteFence, string, uint64) error
}

type StreamReconciler interface {
	SuspendAccount(domain.AccountID) error
	RecoverAccount(context.Context, domain.AccountID) (order.RecoveryReport, error)
	ObserveVenueOrder(context.Context, order.VenueOrderObservation) (order.Result, error)
}

type StreamOptions struct {
	Accounts           StreamAccounts
	Reconciler         StreamReconciler
	Hub                *Hub
	PrincipalID        domain.PrincipalID
	ReconnectBackoff   time.Duration
	RecoveryTimeout    time.Duration
	SubscriptionBuffer int
}

type FillPayload struct {
	AccountID    domain.AccountID    `json:"account_id"`
	InstrumentID domain.InstrumentID `json:"instrument_id"`
	Fill         exchange.Fill       `json:"fill"`
}

type BalancePayload struct {
	AccountID domain.AccountID `json:"account_id"`
	Balance   exchange.Balance `json:"balance"`
}

type PositionPayload struct {
	AccountID    domain.AccountID      `json:"account_id"`
	RouteID      domain.RouteID        `json:"route_id"`
	InstrumentID domain.InstrumentID   `json:"instrument_id"`
	PositionSlot exchange.PositionSlot `json:"position_slot"`
	Operation    PositionOperation     `json:"operation"`
	Position     exchange.Position     `json:"position"`
	Reason       string                `json:"reason,omitempty"`
}

type AccountHealthPayload struct {
	AccountID  domain.AccountID `json:"account_id"`
	ReadReady  bool             `json:"read_ready"`
	WriteReady bool             `json:"write_ready"`
	Reason     string           `json:"reason,omitempty"`
}

type StreamManager struct {
	accounts           StreamAccounts
	reconciler         StreamReconciler
	hub                *Hub
	positions          *PositionProjection
	principal          domain.PrincipalID
	reconnectBackoff   time.Duration
	recoveryTimeout    time.Duration
	subscriptionBuffer int

	mu               sync.Mutex
	versions         map[string]domain.ResourceVersion
	barrierSequences map[domain.AccountID]uint64
	routeBarriers    map[domain.WriteFence]uint64

	startMu       sync.Mutex
	lifecycleMu   sync.Mutex
	lifecycleOps  sync.WaitGroup
	runContext    context.Context
	cancel        context.CancelFunc
	runGeneration uint64
	wait          sync.WaitGroup
	registered    map[domain.AccountID]struct{}
	supervisors   map[domain.AccountID]*accountSupervisor
	transitions   map[domain.AccountID]*sync.Mutex
	started       bool
	closed        bool
	closeOnce     sync.Once
	closeDone     chan struct{}
}

type accountSupervisor struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func NewStreamManager(options StreamOptions) (*StreamManager, error) {
	if options.Accounts == nil || options.Reconciler == nil || options.Hub == nil {
		return nil, fmt.Errorf("stream manager dependencies are incomplete")
	}
	if err := options.PrincipalID.Validate(); err != nil {
		return nil, err
	}
	if options.ReconnectBackoff <= 0 {
		options.ReconnectBackoff = 5 * time.Second
	}
	if options.RecoveryTimeout <= 0 {
		options.RecoveryTimeout = 30 * time.Second
	}
	if options.SubscriptionBuffer <= 0 {
		options.SubscriptionBuffer = 64
	}
	positions, err := NewPositionProjection(options.Hub, time.Now)
	if err != nil {
		return nil, err
	}
	return &StreamManager{
		accounts: options.Accounts, reconciler: options.Reconciler, hub: options.Hub,
		positions: positions,
		principal: options.PrincipalID, reconnectBackoff: options.ReconnectBackoff,
		recoveryTimeout: options.RecoveryTimeout, subscriptionBuffer: options.SubscriptionBuffer,
		versions: make(map[string]domain.ResourceVersion), barrierSequences: make(map[domain.AccountID]uint64),
		routeBarriers: make(map[domain.WriteFence]uint64),
		registered:    make(map[domain.AccountID]struct{}), supervisors: make(map[domain.AccountID]*accountSupervisor),
		transitions: make(map[domain.AccountID]*sync.Mutex),
		closeDone:   make(chan struct{}),
	}, nil
}

func (manager *StreamManager) Positions() *PositionProjection {
	if manager == nil {
		return nil
	}
	return manager.positions
}

func (manager *StreamManager) Start(ctx context.Context, accountIDs []domain.AccountID) error {
	if ctx == nil {
		return fmt.Errorf("stream manager context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	seen := make(map[domain.AccountID]struct{}, len(accountIDs))
	for _, accountID := range accountIDs {
		if err := accountID.Validate(); err != nil {
			return err
		}
		if _, exists := seen[accountID]; exists {
			continue
		}
		seen[accountID] = struct{}{}
	}

	manager.startMu.Lock()
	defer manager.startMu.Unlock()
	manager.lifecycleMu.Lock()
	if manager.closed {
		manager.lifecycleMu.Unlock()
		return ErrClosed
	}
	if manager.started {
		manager.lifecycleMu.Unlock()
		return nil
	}
	for accountID := range seen {
		manager.registered[accountID] = struct{}{}
	}
	runContext, cancel := context.WithCancel(ctx)
	manager.runContext = runContext
	manager.cancel = cancel
	manager.runGeneration++
	runGeneration := manager.runGeneration
	manager.started = true
	registered := make([]domain.AccountID, 0, len(manager.registered))
	for accountID := range manager.registered {
		registered = append(registered, accountID)
	}
	manager.lifecycleMu.Unlock()
	for _, accountID := range registered {
		startErr := manager.startAccount(accountID, runGeneration)
		if startErr == nil {
			continue
		}
		if !errors.Is(startErr, errAccountRuntimeUnavailable) {
			manager.rollbackStart(runGeneration, cancel)
			return startErr
		}
		// An account whose runtime group is not installed yet cannot host a
		// private stream, but that is a provisioning state rather than a stream
		// fault: AccountIDs() reports every desired runtime, including ones the
		// account manager has not promoted. Aborting the whole start for one of
		// them means a single half-provisioned account keeps the process from
		// booting at all, while every healthy account loses its stream too. The
		// reconciler already suspended this account's writes above, and
		// notifyRuntimePromoted registers the stream once the runtime lands, so
		// skipping is both safe and self-healing. Same isolation the order
		// coordinator applies to its own per-account recovery failures.
		slog.Warn("private stream not installed at startup; account writes suspended",
			"account_id", string(accountID), "reason", "account_runtime_unavailable")
	}
	return nil
}

func (manager *StreamManager) RegisterAccount(accountID domain.AccountID) error {
	if err := accountID.Validate(); err != nil {
		return err
	}
	transition := manager.accountTransition(accountID)
	transition.Lock()
	defer transition.Unlock()

	manager.lifecycleMu.Lock()
	if manager.closed {
		manager.lifecycleMu.Unlock()
		return ErrClosed
	}
	manager.registered[accountID] = struct{}{}
	if !manager.started {
		manager.lifecycleMu.Unlock()
		return nil
	}
	runGeneration := manager.runGeneration
	runContext := manager.runContext
	supervisor := manager.supervisors[accountID]
	manager.lifecycleOps.Add(1)
	if supervisor != nil {
		supervisor.cancel()
	}
	manager.lifecycleMu.Unlock()
	defer manager.lifecycleOps.Done()

	if supervisor != nil {
		<-supervisor.done
		manager.lifecycleMu.Lock()
		if manager.supervisors[accountID] == supervisor {
			delete(manager.supervisors, accountID)
		}
		manager.lifecycleMu.Unlock()
	}
	return manager.prepareAndInstallAccount(accountID, runGeneration, runContext)
}

func (manager *StreamManager) UnregisterAccount(accountID domain.AccountID) {
	if err := accountID.Validate(); err != nil {
		return
	}
	transition := manager.accountTransition(accountID)
	transition.Lock()
	defer transition.Unlock()

	manager.lifecycleMu.Lock()
	if manager.closed {
		manager.lifecycleMu.Unlock()
		return
	}
	delete(manager.registered, accountID)
	supervisor := manager.supervisors[accountID]
	manager.lifecycleOps.Add(1)
	if supervisor != nil {
		supervisor.cancel()
	}
	manager.lifecycleMu.Unlock()
	defer manager.lifecycleOps.Done()

	if supervisor != nil {
		<-supervisor.done
		manager.lifecycleMu.Lock()
		if manager.supervisors[accountID] == supervisor {
			delete(manager.supervisors, accountID)
		}
		manager.lifecycleMu.Unlock()
	}
	manager.positions.RemoveAccount(accountID)
}

func (manager *StreamManager) accountTransition(accountID domain.AccountID) *sync.Mutex {
	manager.lifecycleMu.Lock()
	defer manager.lifecycleMu.Unlock()
	transition := manager.transitions[accountID]
	if transition == nil {
		transition = &sync.Mutex{}
		manager.transitions[accountID] = transition
	}
	return transition
}

func (manager *StreamManager) startAccount(accountID domain.AccountID, runGeneration uint64) error {
	transition := manager.accountTransition(accountID)
	transition.Lock()
	defer transition.Unlock()

	manager.lifecycleMu.Lock()
	if manager.closed {
		manager.lifecycleMu.Unlock()
		return ErrClosed
	}
	if !manager.started || manager.runGeneration != runGeneration {
		manager.lifecycleMu.Unlock()
		return nil
	}
	if _, registered := manager.registered[accountID]; !registered {
		manager.lifecycleMu.Unlock()
		return nil
	}
	if manager.supervisors[accountID] != nil {
		manager.lifecycleMu.Unlock()
		return nil
	}
	runContext := manager.runContext
	manager.lifecycleOps.Add(1)
	manager.lifecycleMu.Unlock()
	defer manager.lifecycleOps.Done()
	return manager.prepareAndInstallAccount(accountID, runGeneration, runContext)
}

func (manager *StreamManager) prepareAndInstallAccount(
	accountID domain.AccountID,
	runGeneration uint64,
	runContext context.Context,
) error {
	if err := runContext.Err(); err != nil {
		return err
	}
	if err := manager.reconciler.SuspendAccount(accountID); err != nil {
		return fmt.Errorf("suspend account before private stream failed")
	}
	if err := manager.positions.MarkSyncing(manager.principal, accountID); err != nil {
		return fmt.Errorf("mark position projection syncing failed")
	}
	if err := manager.accounts.SetPrivateStreamState(accountID, false, account.ReasonPrivateStreamStarting); err != nil {
		if errors.Is(err, account.ErrUnknownAccount) {
			return errAccountRuntimeUnavailable
		}
		return fmt.Errorf("mark private stream starting failed")
	}

	manager.lifecycleMu.Lock()
	defer manager.lifecycleMu.Unlock()
	if manager.closed {
		return ErrClosed
	}
	if !manager.started || manager.runGeneration != runGeneration || manager.runContext != runContext {
		return nil
	}
	if _, registered := manager.registered[accountID]; !registered {
		return nil
	}
	if manager.supervisors[accountID] != nil {
		return nil
	}
	if err := runContext.Err(); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(runContext)
	supervisor := &accountSupervisor{cancel: cancel, done: make(chan struct{})}
	manager.supervisors[accountID] = supervisor
	manager.wait.Add(1)
	go func() {
		defer manager.wait.Done()
		defer close(supervisor.done)
		manager.superviseAccount(ctx, accountID)
	}()
	return nil
}

func (manager *StreamManager) rollbackStart(runGeneration uint64, cancel context.CancelFunc) {
	cancel()
	manager.lifecycleMu.Lock()
	if manager.runGeneration != runGeneration {
		manager.lifecycleMu.Unlock()
		return
	}
	manager.started = false
	manager.runContext = nil
	manager.cancel = nil
	supervisors := make(map[domain.AccountID]*accountSupervisor, len(manager.supervisors))
	for accountID, supervisor := range manager.supervisors {
		supervisors[accountID] = supervisor
		supervisor.cancel()
	}
	manager.lifecycleMu.Unlock()
	for _, supervisor := range supervisors {
		<-supervisor.done
	}
	manager.lifecycleMu.Lock()
	for accountID, supervisor := range supervisors {
		if manager.supervisors[accountID] == supervisor {
			delete(manager.supervisors, accountID)
		}
	}
	manager.lifecycleMu.Unlock()
}

func (manager *StreamManager) Close() {
	if manager == nil {
		return
	}
	manager.closeOnce.Do(func() {
		manager.lifecycleMu.Lock()
		manager.closed = true
		manager.started = false
		if manager.cancel != nil {
			manager.cancel()
		}
		for _, supervisor := range manager.supervisors {
			supervisor.cancel()
		}
		manager.lifecycleMu.Unlock()
		manager.lifecycleOps.Wait()
		manager.wait.Wait()
		close(manager.closeDone)
	})
	<-manager.closeDone
}

func (manager *StreamManager) superviseAccount(ctx context.Context, accountID domain.AccountID) {
	if routes, ok := manager.accounts.(atomicStreamRouteSource); ok {
		if manager.superviseDynamicAccount(ctx, accountID, routes) {
			return
		}
	}
	manager.superviseStaticAccount(ctx, accountID)
}

func (manager *StreamManager) superviseStaticAccount(ctx context.Context, accountID domain.AccountID) {
	for {
		if ctx.Err() != nil {
			return
		}
		streams, err := manager.openAccountStreams(ctx, accountID)
		if err != nil {
			logPrivateStreamConnectFailure(accountID, "", err)
			manager.unavailable(accountID, "private_stream_connect_failed", true)
			if !waitContext(ctx, manager.reconnectBackoff) {
				return
			}
			continue
		}

		recoveryContext, cancel := context.WithTimeout(ctx, manager.recoveryTimeout)
		report, recoveryErr := manager.reconciler.RecoverAccount(recoveryContext, accountID)
		cancel()
		if recoveryErr != nil || !report.WriteReady {
			streams.close()
			manager.unavailable(accountID, "private_stream_recovery_failed", true)
			if !waitContext(ctx, manager.reconnectBackoff) {
				return
			}
			continue
		}
		if err := manager.activatePrivateStream(ctx, accountID, streams); err != nil {
			streams.close()
			if ctx.Err() != nil {
				return
			}
			manager.unavailable(accountID, account.ReasonPrivateStreamGap, true)
			if !waitContext(ctx, manager.reconnectBackoff) {
				return
			}
			continue
		}
		manager.publishHealth(accountID)
		_ = manager.positions.MarkLive(manager.principal, accountID)

		select {
		case <-ctx.Done():
			streams.close()
			return
		case <-streams.faults:
			streams.close()
			if !waitContext(ctx, manager.reconnectBackoff) {
				return
			}
		}
	}
}

type dynamicRouteFault struct {
	routeID domain.RouteID
	streams *accountStreams
}

type dynamicRouteActivation struct {
	routeID domain.RouteID
	streams *accountStreams
	err     error
}

type dynamicRouteReconnect struct {
	routeID domain.RouteID
	streams *accountStreams
	ok      bool
}

func (manager *StreamManager) superviseDynamicAccount(
	ctx context.Context,
	accountID domain.AccountID,
	routes atomicStreamRouteSource,
) bool {
	observer, ok := manager.accounts.(privateStreamRouteObserver)
	if !ok {
		return false
	}
	for {
		if ctx.Err() != nil {
			return true
		}
		snapshot, err := manager.accounts.Instruments(accountID)
		if err != nil || len(snapshot.Instruments) == 0 {
			manager.unavailable(accountID, "private_stream_connect_failed", true)
			if !waitContext(ctx, manager.reconnectBackoff) {
				return true
			}
			continue
		}
		plan, dynamic, err := manager.selectDynamicStreamPlan(ctx, routes, accountID, snapshot)
		if !dynamic {
			return false
		}
		if err != nil {
			manager.unavailable(accountID, "private_stream_connect_failed", true)
			if !waitContext(ctx, manager.reconnectBackoff) {
				return true
			}
			continue
		}
		manager.runDynamicAccount(ctx, accountID, plan, observer)
		return true
	}
}

func (manager *StreamManager) runDynamicAccount(
	ctx context.Context,
	accountID domain.AccountID,
	plan dynamicStreamPlan,
	observer privateStreamRouteObserver,
) {
	routeIDs := make([]domain.RouteID, 0, len(plan.routes))
	for routeID := range plan.routes {
		routeIDs = append(routeIDs, routeID)
	}
	sort.Slice(routeIDs, func(i, j int) bool { return routeIDs[i] < routeIDs[j] })
	faults := make(chan dynamicRouteFault, len(routeIDs)*4)
	activations := make(chan dynamicRouteActivation, len(routeIDs)*4)
	reconnects := make(chan dynamicRouteReconnect)

	for ctx.Err() == nil {
		sessions := make(map[domain.RouteID]*accountStreams, len(routeIDs))
		initializationFailed := false
		for _, routeID := range routeIDs {
			fence := domain.WriteFence{AccountFence: plan.generation, RouteID: routeID}
			if err := observer.SetPrivateStreamRouteState(fence, true, ""); err != nil {
				initializationFailed = true
				break
			}
			streams, err := manager.openDynamicRouteStreams(ctx, accountID, routeID, plan.routes[routeID], faults)
			if err != nil {
				logPrivateStreamConnectFailure(accountID, routeID, err)
				manager.dynamicRouteUnavailable(accountID, fence, "private_stream_connect_failed", true, observer)
				initializationFailed = true
				break
			}
			sessions[routeID] = streams
		}
		if initializationFailed {
			closeDynamicRouteSessions(sessions)
			if !waitContext(ctx, manager.reconnectBackoff) {
				return
			}
			continue
		}

		recoveryContext, cancel := context.WithTimeout(ctx, manager.recoveryTimeout)
		report, recoveryErr := manager.reconciler.RecoverAccount(recoveryContext, accountID)
		cancel()
		if recoveryErr != nil || !report.WriteReady {
			closeDynamicRouteSessions(sessions)
			for _, routeID := range routeIDs {
				manager.dynamicRouteUnavailable(accountID, domain.WriteFence{
					AccountFence: plan.generation, RouteID: routeID,
				}, "private_stream_recovery_failed", true, observer)
			}
			if !waitContext(ctx, manager.reconnectBackoff) {
				return
			}
			continue
		}
		for routeID, streams := range sessions {
			manager.waitDynamicRouteActivation(ctx, routeID, streams, activations)
		}
		retrying := make(map[domain.RouteID]bool, len(routeIDs))

		for len(sessions)+len(retrying) > 0 {
			select {
			case <-ctx.Done():
				closeDynamicRouteSessions(sessions)
				return
			case activation := <-activations:
				if sessions[activation.routeID] != activation.streams || activation.err != nil {
					continue
				}
				fence := domain.WriteFence{AccountFence: plan.generation, RouteID: activation.routeID}
				if err := manager.observeDynamicRouteBarrier(observer, fence); err != nil {
					activation.streams.close()
					delete(sessions, activation.routeID)
					manager.dynamicRouteUnavailable(accountID, fence, "private_stream_barrier_failed", true, observer)
					retrying[activation.routeID] = true
					manager.startDynamicRouteReconnect(
						ctx, accountID, fence, plan.routes[activation.routeID], faults, observer, reconnects,
					)
					continue
				}
				manager.publishHealth(accountID)
				_ = manager.positions.MarkLive(manager.principal, accountID)
			case fault := <-faults:
				if sessions[fault.routeID] != fault.streams {
					continue
				}
				fault.streams.close()
				delete(sessions, fault.routeID)
				fence := domain.WriteFence{AccountFence: plan.generation, RouteID: fault.routeID}
				manager.dynamicRouteUnavailable(accountID, fence, account.ReasonPrivateStreamGap, true, observer)
				retrying[fault.routeID] = true
				manager.startDynamicRouteReconnect(
					ctx, accountID, fence, plan.routes[fault.routeID], faults, observer, reconnects,
				)
			case reconnect := <-reconnects:
				if !retrying[reconnect.routeID] {
					if reconnect.streams != nil {
						reconnect.streams.close()
					}
					continue
				}
				delete(retrying, reconnect.routeID)
				if !reconnect.ok || reconnect.streams == nil {
					closeDynamicRouteSessions(sessions)
					return
				}
				sessions[reconnect.routeID] = reconnect.streams
				manager.waitDynamicRouteActivation(ctx, reconnect.routeID, reconnect.streams, activations)
			}
		}
		return
	}
}

func (manager *StreamManager) startDynamicRouteReconnect(
	ctx context.Context,
	accountID domain.AccountID,
	fence domain.WriteFence,
	selected []selectedStreamRoute,
	faults chan<- dynamicRouteFault,
	observer privateStreamRouteObserver,
	results chan<- dynamicRouteReconnect,
) {
	go func() {
		streams, ok := manager.reconnectDynamicRoute(ctx, accountID, fence, selected, faults, observer)
		result := dynamicRouteReconnect{routeID: fence.RouteID, streams: streams, ok: ok}
		select {
		case results <- result:
		case <-ctx.Done():
			if streams != nil {
				streams.close()
			}
		}
	}()
}

func (manager *StreamManager) reconnectDynamicRoute(
	ctx context.Context,
	accountID domain.AccountID,
	fence domain.WriteFence,
	selected []selectedStreamRoute,
	faults chan<- dynamicRouteFault,
	observer privateStreamRouteObserver,
) (*accountStreams, bool) {
	for waitContext(ctx, manager.reconnectBackoff) {
		if err := observer.SetPrivateStreamRouteState(fence, true, ""); err != nil {
			return nil, false
		}
		streams, err := manager.openDynamicRouteStreams(ctx, accountID, fence.RouteID, selected, faults)
		if err != nil {
			logPrivateStreamConnectFailure(accountID, fence.RouteID, err)
			manager.dynamicRouteUnavailable(accountID, fence, "private_stream_connect_failed", true, observer)
			continue
		}
		recoveryContext, cancel := context.WithTimeout(ctx, manager.recoveryTimeout)
		report, recoveryErr := manager.reconciler.RecoverAccount(recoveryContext, accountID)
		cancel()
		if recoveryErr != nil || !report.WriteReady {
			streams.close()
			manager.dynamicRouteUnavailable(accountID, fence, "private_stream_recovery_failed", true, observer)
			continue
		}
		return streams, true
	}
	return nil, false
}

func logPrivateStreamConnectFailure(accountID domain.AccountID, routeID domain.RouteID, err error) {
	attributes := []any{"account_id", accountID}
	if routeID != "" {
		attributes = append(attributes, "route_id", routeID)
	}
	var venueErr *exchange.Error
	if errors.As(err, &venueErr) {
		details := venueErr.Details()
		attributes = append(attributes, "kind", venueErr.Kind(), "venue", details.Venue,
			"product", details.Product, "operation", details.Operation, "venue_code", details.Code)
	} else {
		attributes = append(attributes, "kind", "unclassified")
	}
	slog.Warn("private stream connection failed", attributes...)
}

func (manager *StreamManager) waitDynamicRouteActivation(
	ctx context.Context,
	routeID domain.RouteID,
	streams *accountStreams,
	results chan<- dynamicRouteActivation,
) {
	go func() {
		result := dynamicRouteActivation{routeID: routeID, streams: streams}
		select {
		case <-ctx.Done():
			result.err = ctx.Err()
		case <-streams.faults:
			result.err = fmt.Errorf("private stream fault before activation")
		case <-streams.activated:
		}
		select {
		case results <- result:
		case <-ctx.Done():
		}
	}()
}

func (manager *StreamManager) observeDynamicRouteBarrier(observer privateStreamRouteObserver, fence domain.WriteFence) error {
	snapshotID, err := manager.hub.identity.EventID()
	if err != nil {
		return err
	}
	manager.mu.Lock()
	manager.routeBarriers[fence]++
	sequence := manager.routeBarriers[fence]
	manager.mu.Unlock()
	return observer.ObservePrivateStreamRouteBarrier(fence, string(snapshotID), sequence)
}

func (manager *StreamManager) dynamicRouteUnavailable(
	accountID domain.AccountID,
	fence domain.WriteFence,
	reason string,
	resync bool,
	observer privateStreamRouteObserver,
) {
	slog.Warn("private stream route unavailable; account writes suspended",
		"account_id", string(accountID), "route_id", string(fence.RouteID),
		"account_fence", fence.AccountFence, "reason", reason, "resync", resync)
	_ = manager.reconciler.SuspendAccount(accountID)
	_ = observer.SetPrivateStreamRouteState(fence, false, reason)
	_ = manager.positions.MarkStale(manager.principal, accountID, reason)
	manager.publishHealth(accountID)
	if resync {
		_, _ = manager.hub.PublishResync(manager.principal, accountID, reason)
	}
}

func closeDynamicRouteSessions(sessions map[domain.RouteID]*accountStreams) {
	for _, streams := range sessions {
		streams.close()
	}
}

func (manager *StreamManager) activatePrivateStream(ctx context.Context, accountID domain.AccountID, streams *accountStreams) error {
	if err := manager.accounts.SetPrivateStreamState(accountID, true, ""); err != nil {
		return err
	}
	observer, requiresBarrier := manager.accounts.(privateStreamBarrierObserver)
	if !requiresBarrier {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-streams.faults:
		return fmt.Errorf("private stream fault before activation")
	case <-streams.activated:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-streams.faults:
		return fmt.Errorf("private stream fault before barrier")
	default:
	}
	snapshotID, err := manager.hub.identity.EventID()
	if err != nil {
		return err
	}
	manager.mu.Lock()
	manager.barrierSequences[accountID]++
	sequence := manager.barrierSequences[accountID]
	manager.mu.Unlock()
	return observer.ObservePrivateStreamBarrier(accountID, string(snapshotID), sequence)
}

func (manager *StreamManager) unavailable(accountID domain.AccountID, reason string, resync bool) {
	slog.Warn("private stream unavailable; account writes suspended",
		"account_id", string(accountID), "reason", reason, "resync", resync)
	_ = manager.reconciler.SuspendAccount(accountID)
	_ = manager.accounts.SetPrivateStreamState(accountID, false, reason)
	_ = manager.positions.MarkStale(manager.principal, accountID, reason)
	manager.publishHealth(accountID)
	if resync {
		_, _ = manager.hub.PublishResync(manager.principal, accountID, reason)
	}
}

func (manager *StreamManager) publishHealth(accountID domain.AccountID) {
	status, err := manager.accounts.Status(accountID)
	if err != nil {
		return
	}
	payload := AccountHealthPayload{
		AccountID: accountID, ReadReady: status.ReadReady, WriteReady: status.WriteReady, Reason: status.Reason,
	}
	manager.publish(accountID, KindAccountHealth, "account", time.Time{}, payload)
}

// accountStreams owns one account's private subscriptions and the two signals a
// supervisor waits on: faults, and activated.
//
// activated closes once every route in this session has a live private stream.
// Liveness here is the exchange layer's own SubscriptionActive status, not the
// arrival of a balance snapshot. EventSnapshot states something about a
// payload's baseline, which is a different question: five of the nine adapters
// only ever emit EventDelta, and Binance cannot emit a snapshot at all for a
// quiet account, because its account-position stream fires only when a balance
// changes. A barrier keyed on it therefore never opens, and the route stays
// private_stream_starting forever. SubscriptionActive is emitted uniformly for
// every venue once the backend's subscribe has succeeded, which is the question
// this barrier is actually asking.
type accountStreams struct {
	cancel             context.CancelFunc
	faults             chan struct{}
	activated          chan struct{}
	mu                 sync.Mutex
	closers            []func() error
	activationMu       sync.Mutex
	pendingActivations map[domain.RouteID]struct{}
	once               sync.Once
	faultOnce          sync.Once
	onFault            func()
}

func (streams *accountStreams) expectActivations(routes map[domain.RouteID]struct{}) {
	streams.activationMu.Lock()
	defer streams.activationMu.Unlock()
	streams.pendingActivations = make(map[domain.RouteID]struct{}, len(routes))
	for routeID := range routes {
		streams.pendingActivations[routeID] = struct{}{}
	}
}

func (streams *accountStreams) observeActivation(routeID domain.RouteID) {
	streams.activationMu.Lock()
	defer streams.activationMu.Unlock()
	if _, pending := streams.pendingActivations[routeID]; !pending {
		return
	}
	delete(streams.pendingActivations, routeID)
	if len(streams.pendingActivations) == 0 {
		close(streams.activated)
	}
}

func (streams *accountStreams) add(close func() error) {
	streams.mu.Lock()
	defer streams.mu.Unlock()
	streams.closers = append(streams.closers, close)
}

func (streams *accountStreams) fault() {
	streams.faultOnce.Do(func() {
		if streams.onFault != nil {
			streams.onFault()
		}
	})
	select {
	case streams.faults <- struct{}{}:
	default:
	}
}

func (streams *accountStreams) close() {
	streams.once.Do(func() {
		streams.cancel()
		streams.mu.Lock()
		closers := append([]func() error(nil), streams.closers...)
		streams.mu.Unlock()
		for index := len(closers) - 1; index >= 0; index-- {
			_ = closers[index]()
		}
	})
}

func (manager *StreamManager) openAccountStreams(ctx context.Context, accountID domain.AccountID) (*accountStreams, error) {
	status, err := manager.accounts.Status(accountID)
	if err != nil {
		return nil, err
	}
	snapshot, err := manager.accounts.Instruments(accountID)
	if err != nil || len(snapshot.Instruments) == 0 {
		return nil, fmt.Errorf("private stream instruments unavailable")
	}
	streamContext, cancel := context.WithCancel(ctx)
	streams := &accountStreams{
		cancel: cancel, faults: make(chan struct{}, 1), activated: make(chan struct{}),
		onFault: func() {
			manager.unavailable(accountID, account.ReasonPrivateStreamGap, true)
		},
	}
	closeOnError := func(err error) (*accountStreams, error) {
		streams.close()
		return nil, err
	}
	if routes, dynamic := manager.accounts.(atomicStreamRouteSource); dynamic {
		if err := manager.openDynamicAccountStreams(streamContext, streams, routes, accountID, snapshot); err != nil {
			return closeOnError(err)
		}
		return streams, nil
	}
	staticRoute := domain.RouteID("static")
	streams.expectActivations(map[domain.RouteID]struct{}{staticRoute: {}})

	switch status.Product {
	case exchange.ProductSpot:
		client, clientErr := manager.accounts.Spot(accountID)
		if clientErr != nil {
			return closeOnError(clientErr)
		}
		socket := client.WebSocket()
		catalogItems := make([]exchange.AccountInstrument, 0, len(snapshot.Instruments))
		for _, public := range snapshot.Instruments {
			instrument, resolveErr := manager.accounts.Resolve(streamContext, accountID, public.ID)
			if resolveErr != nil {
				return closeOnError(resolveErr)
			}
			catalogItems = append(catalogItems, exchange.AccountInstrument{InstrumentID: string(public.ID), Instrument: instrument})
		}
		if err := manager.openAccountEventStream(streamContext, streams, socket, accountID, staticRoute, status.Product, catalogItems); err != nil {
			return closeOnError(err)
		}
	case exchange.ProductPerp:
		client, clientErr := manager.accounts.Perp(accountID)
		if clientErr != nil {
			return closeOnError(clientErr)
		}
		socket := client.WebSocket()
		catalogItems := make([]exchange.AccountInstrument, 0, len(snapshot.Instruments))
		for _, public := range snapshot.Instruments {
			instrument, resolveErr := manager.accounts.Resolve(streamContext, accountID, public.ID)
			if resolveErr != nil {
				return closeOnError(resolveErr)
			}
			catalogItems = append(catalogItems, exchange.AccountInstrument{InstrumentID: string(public.ID), Instrument: instrument})
		}
		if err := manager.openAccountEventStream(streamContext, streams, socket, accountID, staticRoute, status.Product, catalogItems); err != nil {
			return closeOnError(err)
		}
	default:
		return closeOnError(fmt.Errorf("private stream product unsupported"))
	}
	return streams, nil
}

type selectedStreamRoute struct {
	instrumentID domain.InstrumentID
	selection    account.SelectedOrderRoute
}

type dynamicStreamPlan struct {
	generation domain.AccountFence
	routes     map[domain.RouteID][]selectedStreamRoute
}

func (manager *StreamManager) selectDynamicStreamPlan(
	ctx context.Context,
	routes atomicStreamRouteSource,
	accountID domain.AccountID,
	snapshot catalog.Snapshot,
) (dynamicStreamPlan, bool, error) {
	plan := dynamicStreamPlan{routes: make(map[domain.RouteID][]selectedStreamRoute)}
	for _, public := range snapshot.Instruments {
		route, err := routes.SelectOrderRoute(ctx, accountID, public.ID, account.OrderRouteRecovery, account.OrderRouteSelector{})
		if err != nil {
			return dynamicStreamPlan{}, true, fmt.Errorf("private stream route selection failed")
		}
		if !route.Dynamic {
			return dynamicStreamPlan{}, false, nil
		}
		if route.Orders == nil || route.RouteID.Validate() != nil || route.Fence.Validate() != nil ||
			route.Fence.AccountID != accountID || route.Status.AccountID != accountID {
			return dynamicStreamPlan{}, true, fmt.Errorf("private stream route selection failed")
		}
		if plan.generation == (domain.AccountFence{}) {
			plan.generation = route.Fence
		} else if plan.generation != route.Fence {
			return dynamicStreamPlan{}, true, fmt.Errorf("private stream generation changed")
		}
		switch route.Instrument.Identity.Product {
		case exchange.ProductSpot:
			if _, ok := route.Orders.(interface{ WebSocket() exchange.SpotWebSocket }); !ok {
				return dynamicStreamPlan{}, true, fmt.Errorf("private stream spot client unavailable")
			}
		case exchange.ProductPerp:
			if _, ok := route.Orders.(interface{ WebSocket() exchange.PerpWebSocket }); !ok {
				return dynamicStreamPlan{}, true, fmt.Errorf("private stream perp client unavailable")
			}
		default:
			return dynamicStreamPlan{}, true, fmt.Errorf("private stream product unsupported")
		}
		plan.routes[route.RouteID] = append(plan.routes[route.RouteID], selectedStreamRoute{
			instrumentID: public.ID,
			selection:    route,
		})
	}
	if plan.generation == (domain.AccountFence{}) || len(plan.routes) == 0 {
		return dynamicStreamPlan{}, true, fmt.Errorf("private stream instruments unavailable")
	}
	return plan, true, nil
}

func (manager *StreamManager) openDynamicAccountStreams(
	ctx context.Context,
	streams *accountStreams,
	routes atomicStreamRouteSource,
	accountID domain.AccountID,
	snapshot catalog.Snapshot,
) error {
	plan, dynamic, err := manager.selectDynamicStreamPlan(ctx, routes, accountID, snapshot)
	if err != nil || !dynamic {
		return fmt.Errorf("private stream route selection failed")
	}
	balanceRoutes := make(map[domain.RouteID]struct{}, len(plan.routes))
	for routeID := range plan.routes {
		balanceRoutes[routeID] = struct{}{}
	}
	streams.expectActivations(balanceRoutes)
	for routeID, selections := range plan.routes {
		if err := manager.openSelectedAccountRoute(ctx, streams, accountID, routeID, selections); err != nil {
			return err
		}
	}
	return nil
}

func (manager *StreamManager) openDynamicRouteStreams(
	ctx context.Context,
	accountID domain.AccountID,
	routeID domain.RouteID,
	selected []selectedStreamRoute,
	faults chan<- dynamicRouteFault,
) (*accountStreams, error) {
	streamContext, cancel := context.WithCancel(ctx)
	var streams *accountStreams
	streams = &accountStreams{
		cancel: cancel, faults: make(chan struct{}, 1), activated: make(chan struct{}),
		onFault: func() {
			select {
			case faults <- dynamicRouteFault{routeID: routeID, streams: streams}:
			default:
			}
		},
	}
	streams.expectActivations(map[domain.RouteID]struct{}{routeID: {}})
	closeOnError := func(err error) (*accountStreams, error) {
		streams.close()
		return nil, err
	}
	if len(selected) == 0 {
		return closeOnError(fmt.Errorf("private stream route selection failed"))
	}
	if err := manager.openSelectedAccountRoute(streamContext, streams, accountID, routeID, selected); err != nil {
		return closeOnError(err)
	}
	return streams, nil
}

func (manager *StreamManager) openSelectedAccountRoute(ctx context.Context, streams *accountStreams, accountID domain.AccountID, routeID domain.RouteID, selected []selectedStreamRoute) error {
	if len(selected) == 0 {
		return fmt.Errorf("private stream route selection failed")
	}
	first := selected[0].selection
	catalogItems := make([]exchange.AccountInstrument, 0, len(selected))
	for _, item := range selected {
		if item.selection.RouteID != routeID || item.selection.Instrument.Identity.Product != first.Instrument.Identity.Product {
			return fmt.Errorf("private stream route selection failed")
		}
		catalogItems = append(catalogItems, exchange.AccountInstrument{InstrumentID: string(item.instrumentID), Instrument: item.selection.Instrument})
	}
	var socket any
	switch first.Instrument.Identity.Product {
	case exchange.ProductSpot:
		socket = first.Orders.(interface{ WebSocket() exchange.SpotWebSocket }).WebSocket()
	case exchange.ProductPerp:
		socket = first.Orders.(interface{ WebSocket() exchange.PerpWebSocket }).WebSocket()
	default:
		return fmt.Errorf("private stream product unsupported")
	}
	return manager.openAccountEventStream(ctx, streams, socket, accountID, routeID, first.Instrument.Identity.Product, catalogItems)
}

func (manager *StreamManager) openAccountEventStream(ctx context.Context, streams *accountStreams, socket any, accountID domain.AccountID, routeID domain.RouteID, product exchange.Product, catalogItems []exchange.AccountInstrument) error {
	accountSocket, ok := socket.(exchange.AccountWebSocket)
	if len(catalogItems) == 0 {
		return fmt.Errorf("private account event stream unavailable")
	}
	if !ok {
		return fmt.Errorf("private account event stream unavailable")
	}
	indexer, ok := socket.(exchange.AccountInstrumentIndexer)
	if !ok || indexer.SetAccountInstruments(catalogItems) != nil {
		return fmt.Errorf("private account instrument index unavailable")
	}
	identity := catalogItems[0].Instrument.Identity
	stream, err := accountSocket.WatchAccountEvents(ctx, exchange.WatchAccountEventsRequest{
		Route: exchange.AccountRoute{
			AccountID: string(accountID), RouteID: string(routeID), Venue: identity.Venue,
			Environment: exchange.EnvironmentLive, Product: product, Settlement: identity.SettleAsset,
			NativeFamily: accountNativeFamily(identity),
		},
		Options: exchange.WatchOptions{Buffer: manager.subscriptionBuffer},
	})
	if err != nil {
		return err
	}
	monitorSubscription(ctx, streams, stream, func(event exchange.AccountEvent) error {
		instrumentID := domain.InstrumentID(event.InstrumentID)
		switch event.Kind {
		case exchange.AccountEventOrder:
			if event.Order == nil || instrumentID.Validate() != nil {
				return fmt.Errorf("private account order event is invalid")
			}
			_, observeErr := manager.reconciler.ObserveVenueOrder(ctx, order.VenueOrderObservation{AccountID: accountID, InstrumentID: instrumentID, Product: product, Order: event.Order.Order})
			if errors.Is(observeErr, store.ErrNotFound) {
				return nil
			}
			return observeErr
		case exchange.AccountEventFill:
			if event.Fill == nil || instrumentID.Validate() != nil {
				return fmt.Errorf("private account fill event is invalid")
			}
			return manager.publishFill(accountID, instrumentID, *event.Fill)
		case exchange.AccountEventBalance:
			if event.Balance == nil {
				return fmt.Errorf("private account balance event is invalid")
			}
			return manager.publishBalances(accountID, *event.Balance)
		case exchange.AccountEventPosition:
			if event.Position == nil || instrumentID.Validate() != nil {
				return fmt.Errorf("private account position event is invalid")
			}
			return manager.publishPositions(accountID, routeID, instrumentID, *event.Position)
		case exchange.AccountEventResyncRequired:
			return fmt.Errorf("private account stream requires resync")
		case exchange.AccountEventStreamStatus:
			if event.Status != nil && event.Status.State == exchange.SubscriptionGap {
				return fmt.Errorf("private account stream gap")
			}
		}
		return nil
	}, func() { streams.observeActivation(routeID) }, "account.events", accountID)
	return nil
}

func accountNativeFamily(identity exchange.InstrumentIdentity) string {
	if identity.Venue == exchange.VenueBybit && identity.Product == exchange.ProductPerp {
		return "linear"
	}
	if identity.Venue == exchange.VenueOKX && identity.Product == exchange.ProductPerp {
		return "SWAP"
	}
	family := string(identity.Product)
	if identity.SettleAsset != "" {
		family += ":" + identity.SettleAsset
	}
	return family
}

func (manager *StreamManager) openSpotInstrument(
	ctx context.Context,
	streams *accountStreams,
	socket exchange.SpotWebSocket,
	accountID domain.AccountID,
	instrumentID domain.InstrumentID,
	instrument exchange.Instrument,
) error {
	request := exchange.WatchRequest{
		Instrument: instrument, Options: exchange.WatchOptions{Buffer: manager.subscriptionBuffer},
	}
	orders, err := socket.WatchOrders(ctx, request)
	if err != nil {
		return err
	}
	monitorSubscription(ctx, streams, orders, func(event exchange.OrderEvent) error {
		_, observeErr := manager.reconciler.ObserveVenueOrder(ctx, order.VenueOrderObservation{
			AccountID: accountID, InstrumentID: instrumentID,
			Product: instrument.Identity.Product, Order: event.Order,
		})
		if errors.Is(observeErr, store.ErrNotFound) {
			return nil
		}
		return observeErr
	}, nil, "orders", accountID)
	fills, err := socket.WatchFills(ctx, request)
	if err != nil {
		return err
	}
	monitorSubscription(ctx, streams, fills, func(event exchange.FillEvent) error {
		return manager.publishFill(accountID, instrumentID, event)
	}, nil, "fills", accountID)
	return nil
}

func (manager *StreamManager) openPerpInstrument(
	ctx context.Context,
	streams *accountStreams,
	socket exchange.PerpWebSocket,
	accountID domain.AccountID,
	routeID domain.RouteID,
	instrumentID domain.InstrumentID,
	instrument exchange.Instrument,
) error {
	if err := manager.openSpotInstrument(ctx, streams, socket, accountID, instrumentID, instrument); err != nil {
		return err
	}
	positions, err := socket.WatchPositions(ctx, exchange.WatchRequest{
		Instrument: instrument, Options: exchange.WatchOptions{Buffer: manager.subscriptionBuffer},
	})
	if err != nil {
		return err
	}
	monitorSubscription(ctx, streams, positions, func(event exchange.PositionEvent) error {
		return manager.publishPositions(accountID, routeID, instrumentID, event)
	}, nil, "positions", accountID)
	return nil
}

func (manager *StreamManager) openBalances(
	ctx context.Context,
	streams *accountStreams,
	socket exchange.SpotWebSocket,
	accountID domain.AccountID,
	routeID domain.RouteID,
) error {
	balances, err := socket.WatchBalances(ctx, exchange.WatchAccountRequest{
		Options: exchange.WatchOptions{Buffer: manager.subscriptionBuffer},
	})
	if err != nil {
		return err
	}
	monitorSubscription(ctx, streams, balances, func(event exchange.BalanceEvent) error {
		return manager.publishBalances(accountID, event)
	}, func() {
		streams.observeActivation(routeID)
	}, "balances", accountID)
	return nil
}

// monitorSubscription drains one subscription until ctx ends or it faults.
//
// onActive, when supplied, fires each time the exchange layer reports the
// subscription live. That report is the only venue-neutral evidence the gateway
// gets that a private stream is up: it is emitted uniformly once the backend's
// subscribe succeeds, whereas the events themselves arrive only when the venue
// has something to say.
func monitorSubscription[T any](
	ctx context.Context,
	streams *accountStreams,
	subscription exchange.Subscription[T],
	onEvent func(T) error,
	onActive func(),
	kind string,
	accountID domain.AccountID,
) {
	streams.add(subscription.Close)
	go func() {
		events := subscription.Events()
		statuses := subscription.Status()
		errorsChannel := subscription.Errors()
		for {
			select {
			case <-ctx.Done():
				return
			case event, open := <-events:
				if !open {
					faultSubscription(streams, kind, accountID, "events_closed", nil)
					return
				}
				if err := onEvent(event); err != nil {
					faultSubscription(streams, kind, accountID, "event_apply_failed", err)
					return
				}
			case status, open := <-statuses:
				if !open {
					statuses = nil
					continue
				}
				if status.State == exchange.SubscriptionGap || status.State == exchange.SubscriptionClosed ||
					status.Phase == exchange.GapStarted {
					faultSubscription(streams, kind, accountID, fmt.Sprintf("status_%s_%s", status.State, status.Phase), nil)
					return
				}
				if status.State == exchange.SubscriptionActive && onActive != nil {
					onActive()
				}
			case streamErr, open := <-errorsChannel:
				if !open {
					errorsChannel = nil
					continue
				}
				if streamErr != nil {
					faultSubscription(streams, kind, accountID, "stream_error", streamErr)
					return
				}
			}
			if events == nil && statuses == nil && errorsChannel == nil {
				faultSubscription(streams, kind, accountID, "all_channels_closed", nil)
				return
			}
		}
	}()
}

// faultSubscription names the cause before tripping the account's write gate.
// The fault channel carries only a signal, and every one of the five ways a
// subscription can die converges on the same suspend, so without this line an
// operator sees writes start failing with no way to tell a venue disconnect
// from a gateway-side apply error.
func faultSubscription(streams *accountStreams, kind string, accountID domain.AccountID, cause string, err error) {
	attributes := []any{"account_id", string(accountID), "subscription", kind, "cause", cause}
	if err != nil {
		attributes = append(attributes, "error", err)
	}
	slog.Warn("private stream subscription faulted", attributes...)
	streams.fault()
}

func (manager *StreamManager) publishFill(accountID domain.AccountID, instrumentID domain.InstrumentID, event exchange.FillEvent) error {
	resource := event.Fill.FillID
	if resource == "" {
		resource = event.Fill.OrderID + ":" + event.Fill.NativeSymbol
	}
	return manager.publish(accountID, KindFill, resource, event.Fill.Time, FillPayload{
		AccountID: accountID, InstrumentID: instrumentID, Fill: event.Fill,
	})
}

func (manager *StreamManager) publishBalances(accountID domain.AccountID, event exchange.BalanceEvent) error {
	for _, balance := range event.Balances {
		if err := manager.publish(accountID, KindBalance, balance.Asset, event.Time, BalancePayload{
			AccountID: accountID, Balance: balance,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (manager *StreamManager) publishPositions(accountID domain.AccountID, routeID domain.RouteID, instrumentID domain.InstrumentID, event exchange.PositionEvent) error {
	return manager.positions.Apply(manager.principal, accountID, routeID, instrumentID, event)
}

func (manager *StreamManager) publish(accountID domain.AccountID, kind Kind, resource string, occurredAt time.Time, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	eventID, err := manager.hub.identity.EventID()
	if err != nil {
		return err
	}
	version := manager.nextVersion(accountID, kind, resource)
	_, err = manager.hub.Publish(PublishRequest{
		PrincipalID: manager.principal, AccountID: accountID, EventID: eventID,
		ResourceVersion: version, Kind: kind, OccurredAt: occurredAt, Payload: encoded,
	})
	return err
}

func (manager *StreamManager) nextVersion(accountID domain.AccountID, kind Kind, resource string) domain.ResourceVersion {
	key := string(accountID) + "\x00" + string(kind) + "\x00" + resource
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.versions[key]++
	return manager.versions[key]
}

func waitContext(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
