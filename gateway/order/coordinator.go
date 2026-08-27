package order

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/account"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/gateway/store"
	"github.com/shopspring/decimal"
)

func NewCoordinator(options Options) (*Coordinator, error) {
	if options.Store == nil {
		return nil, fmt.Errorf("order store is required")
	}
	if options.Router == nil {
		return nil, fmt.Errorf("order router is required")
	}
	if options.Identity == nil {
		return nil, fmt.Errorf("order identity generator is required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.ReconciliationDelay <= 0 {
		options.ReconciliationDelay = 5 * time.Second
	}
	if options.ReconciliationInterval <= 0 {
		options.ReconciliationInterval = 60 * time.Second
	}
	if options.ReconciliationLease <= 0 {
		options.ReconciliationLease = 30 * time.Second
	}
	if options.ReconciliationLimit <= 0 {
		options.ReconciliationLimit = 100
	}
	if options.MaxReconciliationAttempts <= 0 {
		options.MaxReconciliationAttempts = 20
	}
	idle := make(chan struct{})
	close(idle)
	return &Coordinator{
		store: options.Store, router: options.Router, publisher: options.Publisher,
		identity: options.Identity, hooks: options.Hooks, now: options.Now,
		reconciliationDelay:       options.ReconciliationDelay,
		reconciliationInterval:    options.ReconciliationInterval,
		reconciliationLease:       options.ReconciliationLease,
		reconciliationLimit:       options.ReconciliationLimit,
		maxReconciliationAttempts: options.MaxReconciliationAttempts,
		gates:                     make(map[domain.AccountID]*accountGate),
		reconciliationAccounts:    make(map[domain.AccountID]struct{}),
		idle:                      idle,
	}, nil
}

func (coordinator *Coordinator) Start(ctx context.Context, accountIDs []domain.AccountID) error {
	if ctx == nil {
		return fmt.Errorf("order coordinator start context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	coordinator.runnerMu.Lock()
	defer coordinator.runnerMu.Unlock()
	if coordinator.runnerStarted {
		return nil
	}
	coordinator.lifecycleMu.Lock()
	closing := coordinator.closing
	coordinator.lifecycleMu.Unlock()
	if closing {
		return ErrCoordinatorClosed
	}
	validated := make(map[domain.AccountID]struct{}, len(accountIDs))
	for _, accountID := range accountIDs {
		if err := accountID.Validate(); err != nil {
			return err
		}
		validated[accountID] = struct{}{}
	}
	for accountID := range validated {
		coordinator.reconciliationAccounts[accountID] = struct{}{}
	}
	ordered := coordinator.reconciliationAccountIDsLocked()
	for _, accountID := range ordered {
		// Recovery failures remain isolated in the per-account write gate.
		_, _ = coordinator.RecoverAccount(ctx, accountID)
	}
	loopCtx, cancel := context.WithCancel(context.Background())
	coordinator.runnerCancel = cancel
	coordinator.runnerDone = make(chan struct{})
	coordinator.runnerStarted = true
	go coordinator.reconciliationLoop(loopCtx, coordinator.runnerDone)
	return nil
}

func (coordinator *Coordinator) RegisterAccount(accountID domain.AccountID) error {
	if err := accountID.Validate(); err != nil {
		return err
	}
	if !coordinator.beginOperation() {
		return ErrCoordinatorClosed
	}
	defer coordinator.endOperation()
	coordinator.runnerMu.Lock()
	coordinator.reconciliationAccounts[accountID] = struct{}{}
	coordinator.runnerMu.Unlock()
	return nil
}

func (coordinator *Coordinator) UnregisterAccount(accountID domain.AccountID) {
	if err := accountID.Validate(); err != nil {
		return
	}
	coordinator.runnerMu.Lock()
	delete(coordinator.reconciliationAccounts, accountID)
	coordinator.runnerMu.Unlock()
}

func (coordinator *Coordinator) reconciliationLoop(ctx context.Context, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(coordinator.reconciliationInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, accountID := range coordinator.reconciliationAccountIDs() {
				if ctx.Err() != nil {
					return
				}
				_, _ = coordinator.ReconcileDue(ctx, accountID)
			}
		}
	}
}

func (coordinator *Coordinator) reconciliationAccountIDs() []domain.AccountID {
	coordinator.runnerMu.Lock()
	defer coordinator.runnerMu.Unlock()
	return coordinator.reconciliationAccountIDsLocked()
}

func (coordinator *Coordinator) reconciliationAccountIDsLocked() []domain.AccountID {
	accountIDs := make([]domain.AccountID, 0, len(coordinator.reconciliationAccounts))
	for accountID := range coordinator.reconciliationAccounts {
		accountIDs = append(accountIDs, accountID)
	}
	sort.Slice(accountIDs, func(i, j int) bool { return accountIDs[i] < accountIDs[j] })
	return accountIDs
}

func (coordinator *Coordinator) Place(ctx context.Context, request PlaceRequest) (Result, error) {
	if ctx == nil {
		return Result{}, fmt.Errorf("place order context is required")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if err := request.validateIdentity(); err != nil {
		return Result{}, err
	}
	if request.CorrelationID == "" {
		correlationID, err := coordinator.identity.EventID()
		if err != nil {
			return Result{}, fmt.Errorf("create order correlation failed")
		}
		request.CorrelationID = correlationID
	}
	if !coordinator.beginOperation() {
		return Result{}, ErrCoordinatorClosed
	}
	defer coordinator.endOperation()
	gate := coordinator.gate(request.AccountID)
	if !beginAccountWrite(gate) {
		return Result{}, ErrWriteNotReady
	}
	defer endAccountWrite(gate)

	route, err := coordinator.router.Route(ctx, request.AccountID, request.InstrumentID)
	if err != nil || route.validate() != nil || !route.WriteReady || route.CheckWriteFence == nil {
		return Result{}, ErrWriteNotReady
	}
	if route.RouteID != "" && route.RouteID != request.RouteID {
		return Result{}, ErrWriteNotReady
	}
	if request.Leverage < 0 || (route.Product != exchange.ProductPerp && request.Leverage != 0) {
		return Result{}, fmt.Errorf("%w: leverage is available only for perpetual products", exchange.ErrInvalidRequest)
	}
	payload, payloadHash, err := canonicalPlacePayload(request)
	if err != nil {
		return Result{}, err
	}
	preparedRequest := exchange.PlaceOrderRequest{
		Instrument: route.Instrument, Side: request.Side, Type: request.Type,
		Quantity: request.Quantity, LimitPrice: request.LimitPrice,
		LimitPolicy: request.LimitPolicy, ReduceOnly: request.ReduceOnly,
		// The gateway allocates the durable client order ID only after the
		// payload has passed local validation. Use a valid placeholder for this
		// I/O-free validation pass, then replace it with the reserved ID below.
		ClientOrderID: "1",
	}
	if err := preparedRequest.Validate(route.Product); err != nil {
		return Result{}, err
	}

	reserved, err := coordinator.store.ReserveCommand(ctx, store.ReserveCommandRequest{
		PrincipalID: request.PrincipalID, AccountID: request.AccountID, InstrumentID: request.InstrumentID,
		IdempotencyKey: request.IdempotencyKey, CorrelationID: request.CorrelationID,
		PayloadHash: payloadHash, Payload: payload,
	})
	if err != nil {
		return Result{}, fmt.Errorf("reserve order command failed")
	}
	result := resultFromReservation(reserved)
	if reserved.Conflict != store.ConflictNone {
		return result, ErrIdempotencyConflict
	}
	if !reserved.New {
		result.Replayed = true
		return result, nil
	}
	prepared := store.StoredCommand{
		CommandID: reserved.CommandID, GatewayOrderID: reserved.GatewayOrderID,
		AccountID: request.AccountID, InstrumentID: request.InstrumentID,
		ClientOrderID: reserved.ClientOrderID, State: domain.StatePrepared,
		ResourceVersion: reserved.ResourceVersion, Payload: payload,
	}
	if err := runHook(ctx, coordinator.hooks.AfterPrepared, prepared); err != nil {
		markAccountNotReady(gate)
		return result, err
	}
	preparedRequest.ClientOrderID = reserved.ClientOrderID
	result, err = coordinator.dispatch(ctx, route, prepared, preparedRequest, request.Leverage, request.RouteID, request.WriteFence)
	if err != nil {
		markAccountNotReady(gate)
	}
	return result, err
}

func (coordinator *Coordinator) Get(ctx context.Context, principalID domain.PrincipalID, gatewayOrderID domain.GatewayOrderID) (Result, error) {
	if ctx == nil {
		return Result{}, fmt.Errorf("get order context is required")
	}
	command, err := coordinator.store.LoadOrder(ctx, store.LoadOrderRequest{
		PrincipalID: principalID, GatewayOrderID: gatewayOrderID,
	})
	if err != nil {
		return Result{}, err
	}
	return resultFromStored(command), nil
}

func (coordinator *Coordinator) List(ctx context.Context, request ListRequest) ([]Result, error) {
	if ctx == nil {
		return nil, fmt.Errorf("list orders context is required")
	}
	commands, err := coordinator.store.ListOrders(ctx, store.ListOrdersRequest{
		PrincipalID: request.PrincipalID, AccountID: request.AccountID,
		InstrumentID: request.InstrumentID, OpenOnly: request.OpenOnly,
		Limit: request.Limit, Cursor: request.Cursor,
	})
	if err != nil {
		return nil, err
	}
	results := make([]Result, len(commands))
	for index, command := range commands {
		results[index] = resultFromStored(command)
	}
	return results, nil
}

func (coordinator *Coordinator) ObserveVenueOrder(ctx context.Context, observation VenueOrderObservation) (Result, error) {
	if ctx == nil {
		return Result{}, fmt.Errorf("observe venue order context is required")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if err := observation.validate(); err != nil {
		return Result{}, err
	}
	if !coordinator.beginOperation() {
		return Result{}, ErrCoordinatorClosed
	}
	defer coordinator.endOperation()

	lookup := store.LoadOrderByVenueIdentityRequest{
		AccountID:    observation.AccountID,
		Product:      string(observation.Product),
		VenueOrderID: observation.Order.OrderID,
	}
	if _, err := domain.ParseClientOrderID(observation.Order.ClientOrderID); err == nil {
		lookup.ClientOrderID = observation.Order.ClientOrderID
	}
	if lookup.ClientOrderID == "" && lookup.VenueOrderID == "" {
		return Result{}, store.ErrNotFound
	}
	command, err := coordinator.store.LoadOrderByVenueIdentity(ctx, lookup)
	if err != nil {
		return Result{}, err
	}
	if command.AccountID != observation.AccountID || command.InstrumentID != observation.InstrumentID {
		return Result{}, store.ErrNotFound
	}
	if command.VenueOrderID != "" && observation.Order.OrderID != "" &&
		command.VenueOrderID != observation.Order.OrderID {
		return Result{}, fmt.Errorf("venue order identity mismatch")
	}
	target, err := exactDomainState(exchange.ExactOrderLookupResult{
		Status: exchange.ExactOrderFound,
		Order:  observation.Order,
	})
	if err != nil {
		return Result{}, err
	}
	if terminalOrderState(command.State) {
		return resultFromStored(command), nil
	}
	updated, err := coordinator.transition(ctx, command, target, observation.Order.OrderID, "")
	if err != nil {
		return resultFromStored(command), err
	}
	return resultFromStored(updated), nil
}

func (coordinator *Coordinator) Cancel(ctx context.Context, request CancelRequest) (Result, error) {
	if ctx == nil {
		return Result{}, fmt.Errorf("cancel order context is required")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if err := request.PrincipalID.Validate(); err != nil {
		return Result{}, err
	}
	if err := request.GatewayOrderID.Validate(); err != nil {
		return Result{}, err
	}
	if !coordinator.beginOperation() {
		return Result{}, ErrCoordinatorClosed
	}
	defer coordinator.endOperation()
	command, err := coordinator.store.LoadOrder(ctx, store.LoadOrderRequest{
		PrincipalID: request.PrincipalID, GatewayOrderID: request.GatewayOrderID,
	})
	if err != nil {
		return Result{}, err
	}
	if command.State == domain.StateFilled || command.State == domain.StateCanceled || command.State == domain.StateRejected {
		return resultFromStored(command), nil
	}
	gate := coordinator.gate(command.AccountID)
	if !beginAccountWrite(gate) {
		return resultFromStored(command), ErrWriteNotReady
	}
	defer endAccountWrite(gate)
	route, err := coordinator.router.Route(ctx, command.AccountID, command.InstrumentID)
	if err != nil || route.validate() != nil || !route.WriteReady {
		return resultFromStored(command), ErrWriteNotReady
	}
	switch command.State {
	case domain.StatePrepared:
		updated, transitionErr := coordinator.transition(ctx, command, domain.StateRejected, "", "")
		return resultFromStored(updated), transitionErr
	case domain.StateDispatching, domain.StateUncertain:
		resolved, reconcileErr := coordinator.reconcile(ctx, route, command)
		if reconcileErr != nil || !resolved {
			markAccountNotReady(gate)
			return resultFromStored(command), ErrOutcomeUncertain
		}
		reloaded, loadErr := coordinator.store.LoadOrder(ctx, store.LoadOrderRequest{
			PrincipalID: request.PrincipalID, GatewayOrderID: request.GatewayOrderID,
		})
		if loadErr != nil {
			return resultFromStored(command), loadErr
		}
		return resultFromStored(reloaded), nil
	}
	if command.VenueOrderID == "" {
		updated, transitionErr := coordinator.transition(ctx, command, domain.StateUncertain, "", store.ReconciliationEvidenceUnconfirmed)
		markAccountNotReady(gate)
		if transitionErr != nil {
			return resultFromStored(command), transitionErr
		}
		return resultFromStored(updated), ErrOutcomeUncertain
	}
	uncertain, err := coordinator.transition(ctx, command, domain.StateUncertain, command.VenueOrderID, store.ReconciliationAmbiguousHandoff)
	if err != nil {
		markAccountNotReady(gate)
		return resultFromStored(command), ErrOutcomeUncertain
	}
	ack, cancelErr := route.Driver.CancelOrder(ctx, exchange.CancelOrderRequest{
		Instrument: route.Instrument, OrderID: command.VenueOrderID,
	})
	if cancelErr != nil || ack.Validate() != nil || ack.Operation != exchange.OrderOperationCancel ||
		ack.State != exchange.AckCanceled || ack.OrderID != command.VenueOrderID {
		markAccountNotReady(gate)
		return resultFromStored(uncertain), ErrOutcomeUncertain
	}
	updated, err := coordinator.transition(ctx, uncertain, domain.StateCanceled, command.VenueOrderID, "")
	if err != nil {
		markAccountNotReady(gate)
		return resultFromStored(uncertain), ErrOutcomeUncertain
	}
	return resultFromStored(updated), nil
}

func (coordinator *Coordinator) RecoverAccount(ctx context.Context, accountID domain.AccountID) (RecoveryReport, error) {
	report := RecoveryReport{AccountID: accountID}
	if ctx == nil {
		return report, fmt.Errorf("recover account context is required")
	}
	if err := accountID.Validate(); err != nil {
		return report, err
	}
	if !coordinator.beginOperation() {
		return report, ErrCoordinatorClosed
	}
	defer coordinator.endOperation()
	return coordinator.recoverAccount(ctx, accountID)
}

func (coordinator *Coordinator) recoverAccount(ctx context.Context, accountID domain.AccountID) (RecoveryReport, error) {
	report := RecoveryReport{AccountID: accountID}
	gate := coordinator.gate(accountID)
	beginAccountRecovery(gate)
	complete := false
	defer func() { finishAccountRecovery(gate, complete) }()

	commands, err := coordinator.store.LoadRecoverableCommands(ctx, store.LoadRecoverableCommandsRequest{AccountID: accountID, Limit: 1000})
	if err != nil {
		return report, fmt.Errorf("load account recovery commands failed")
	}
	report.Examined = len(commands)
	for _, command := range commands {
		resolved, recoverErr := coordinator.recoverCommand(ctx, command)
		if recoverErr != nil || !resolved {
			report.Unresolved++
			continue
		}
		report.Resolved++
	}
	if report.Unresolved != 0 {
		return report, ErrRecoveryIncomplete
	}
	complete = true
	report.WriteReady = true
	return report, nil
}

func (coordinator *Coordinator) ReconcileDue(ctx context.Context, accountID domain.AccountID) (ReconciliationReport, error) {
	report := ReconciliationReport{AccountID: accountID}
	if ctx == nil {
		return report, fmt.Errorf("reconcile due context is required")
	}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	if err := accountID.Validate(); err != nil {
		return report, err
	}
	if !coordinator.beginOperation() {
		return report, ErrCoordinatorClosed
	}
	defer coordinator.endOperation()

	now := coordinator.now().UTC()
	tasks, err := coordinator.store.ClaimDueReconciliationTasks(ctx, store.ClaimDueReconciliationTasksRequest{
		AccountID: accountID, Now: now, LeaseUntil: now.Add(coordinator.reconciliationLease),
		Limit: coordinator.reconciliationLimit, MaxAttempts: coordinator.maxReconciliationAttempts,
	})
	if err != nil {
		return report, fmt.Errorf("claim due reconciliation failed")
	}
	report.Claimed = len(tasks)
	for _, task := range tasks {
		resolved, reconcileErr := coordinator.recoverCommand(ctx, task.Command)
		if reconcileErr != nil || !resolved {
			report.Unresolved++
			continue
		}
		report.Resolved++
	}
	if report.Unresolved != 0 {
		return report, ErrRecoveryIncomplete
	}
	if report.Claimed == 0 {
		status := coordinator.AccountWriteStatus(accountID)
		if status.WriteReady {
			report.WriteReady = true
			return report, nil
		}
		recovery, recoveryErr := coordinator.recoverAccount(ctx, accountID)
		report.WriteReady = recovery.WriteReady
		report.Resolved += recovery.Resolved
		report.Unresolved += recovery.Unresolved
		if recoveryErr != nil {
			return report, recoveryErr
		}
		return report, nil
	}

	recovery, recoveryErr := coordinator.recoverAccount(ctx, accountID)
	report.WriteReady = recovery.WriteReady
	if recoveryErr != nil {
		report.Unresolved += recovery.Unresolved
		return report, recoveryErr
	}
	return report, nil
}

func (coordinator *Coordinator) AccountWriteStatus(accountID domain.AccountID) AccountWriteStatus {
	gate := coordinator.gate(accountID)
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if gate.ready && !gate.recovering {
		return AccountWriteStatus{WriteReady: true}
	}
	return AccountWriteStatus{Reason: ReasonRecoveryRequired}
}

// SuspendAccount closes the in-memory write gate immediately. It is used when
// private-stream continuity is no longer provable; only RecoverAccount may
// reopen the gate after exact REST reconciliation.
func (coordinator *Coordinator) SuspendAccount(accountID domain.AccountID) error {
	if err := accountID.Validate(); err != nil {
		return err
	}
	if !coordinator.beginOperation() {
		return ErrCoordinatorClosed
	}
	defer coordinator.endOperation()
	markAccountNotReady(coordinator.gate(accountID))
	return nil
}

func (coordinator *Coordinator) Shutdown(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("order coordinator shutdown context is required")
	}
	coordinator.lifecycleMu.Lock()
	coordinator.closing = true
	idle := coordinator.idle
	coordinator.lifecycleMu.Unlock()
	coordinator.runnerMu.Lock()
	cancel := coordinator.runnerCancel
	done := coordinator.runnerDone
	coordinator.runnerMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (coordinator *Coordinator) beginOperation() bool {
	coordinator.lifecycleMu.Lock()
	defer coordinator.lifecycleMu.Unlock()
	if coordinator.closing {
		return false
	}
	if coordinator.active == 0 {
		coordinator.idle = make(chan struct{})
	}
	coordinator.active++
	return true
}

func (coordinator *Coordinator) endOperation() {
	coordinator.lifecycleMu.Lock()
	defer coordinator.lifecycleMu.Unlock()
	coordinator.active--
	if coordinator.active == 0 {
		close(coordinator.idle)
	}
}

func (coordinator *Coordinator) recoverCommand(ctx context.Context, command store.StoredCommand) (bool, error) {
	payload, err := decodePlacePayload(command.Payload)
	if err != nil || payload.AccountID != command.AccountID || payload.InstrumentID != command.InstrumentID {
		return false, fmt.Errorf("recover durable order payload failed")
	}
	fence := domain.AccountFence{
		AccountID: payload.AccountID, RuntimeID: payload.RuntimeID,
	}
	var route Route
	if recoveryRouter, ok := coordinator.router.(FencedRecoveryRouter); ok {
		route, err = recoveryRouter.RouteForRecoveryCommand(ctx, command.AccountID, command.InstrumentID, payload.RouteID, fence)
	} else if recoveryRouter, ok := coordinator.router.(RecoveryRouter); ok {
		route, err = recoveryRouter.RouteForRecovery(ctx, command.AccountID, command.InstrumentID)
	} else {
		route, err = coordinator.router.Route(ctx, command.AccountID, command.InstrumentID)
	}
	if err != nil || route.validate() != nil || !route.WriteReady {
		return false, ErrWriteNotReady
	}
	quantity, _ := decimal.NewFromString(payload.Quantity)
	limitPrice := decimal.Zero
	if payload.LimitPrice != "" {
		limitPrice, _ = decimal.NewFromString(payload.LimitPrice)
	}
	request := exchange.PlaceOrderRequest{
		Instrument: route.Instrument, ClientOrderID: command.ClientOrderID,
		Side: payload.Side, Type: payload.Type, Quantity: quantity, LimitPrice: limitPrice,
		LimitPolicy: payload.LimitPolicy, ReduceOnly: payload.ReduceOnly,
	}
	if payload.Leverage < 0 || (route.Product != exchange.ProductPerp && payload.Leverage != 0) {
		return false, fmt.Errorf("recover durable order payload failed")
	}
	if err := request.Validate(route.Product); err != nil {
		return false, fmt.Errorf("recover durable order payload failed")
	}
	if command.State == domain.StatePrepared {
		writeFence := account.WriteFence{AccountID: payload.AccountID, RuntimeID: payload.RuntimeID}
		if route.CheckWriteFence == nil || validateWriteFence(writeFence, command.AccountID) != nil {
			return false, ErrWriteNotReady
		}
		result, dispatchErr := coordinator.dispatch(ctx, route, command, request, payload.Leverage, payload.RouteID, writeFence)
		return dispatchErr == nil && result.State != domain.StateUncertain, dispatchErr
	}
	return coordinator.reconcile(ctx, route, command)
}

func (coordinator *Coordinator) dispatch(ctx context.Context, route Route, prepared store.StoredCommand, request exchange.PlaceOrderRequest, leverage int, routeID domain.RouteID, fence account.WriteFence) (result Result, returnErr error) {
	if route.CheckWriteFence == nil || validateWriteFence(fence, prepared.AccountID) != nil {
		return resultFromStored(prepared), ErrWriteNotReady
	}
	var lease account.HandoffLease
	if route.Admission != nil {
		var err error
		lease, err = route.Admission.Acquire(domain.WriteFence{AccountFence: domain.AccountFence{
			AccountID: fence.AccountID, RuntimeID: fence.RuntimeID,
		}, RouteID: routeID})
		if err != nil {
			return resultFromStored(prepared), ErrWriteNotReady
		}
		defer lease.Release()
	}
	eventID, err := coordinator.identity.EventID()
	if err != nil {
		return resultFromStored(prepared), fmt.Errorf("allocate dispatch event failed")
	}
	dispatching, err := coordinator.store.MarkDispatching(ctx, store.MarkDispatchingRequest{CommandID: prepared.CommandID, EventID: eventID})
	if err != nil {
		return resultFromStored(prepared), fmt.Errorf("claim prepared order failed")
	}
	if err := coordinator.publish(ctx, dispatching); err != nil {
		return resultFromStored(dispatching), err
	}
	if err := runHook(ctx, coordinator.hooks.AfterDispatching, dispatching); err != nil {
		return resultFromStored(dispatching), err
	}
	if err := route.CheckWriteFence(fence); err != nil {
		return resultFromStored(dispatching), ErrWriteNotReady
	}
	if leverage > 0 {
		driver, ok := route.Driver.(LeverageDriver)
		if !ok {
			return resultFromStored(dispatching), fmt.Errorf("set leverage precondition: %w", exchange.ErrUnsupported)
		}
		if err := driver.SetLeverage(ctx, route.Instrument, leverage); err != nil {
			return resultFromStored(dispatching), err
		}
	}
	handoffID, err := coordinator.identity.EventID()
	if err != nil {
		return resultFromStored(dispatching), ErrOutcomeUncertain
	}
	if _, err := coordinator.store.BeginOrderHandoff(ctx, store.BeginOrderHandoffRequest{
		HandoffID: handoffID, CommandID: dispatching.CommandID, AccountID: dispatching.AccountID,
		RouteID: routeID, Fence: domain.AccountFence{
			AccountID: fence.AccountID, RuntimeID: fence.RuntimeID,
		},
	}); err != nil {
		return resultFromStored(dispatching), ErrOutcomeUncertain
	}
	handoffState := store.OrderHandoffUncertain
	defer func() {
		resolveCtx := context.WithoutCancel(ctx)
		if _, err := coordinator.store.ResolveOrderHandoff(resolveCtx, store.ResolveOrderHandoffRequest{HandoffID: handoffID, State: handoffState}); err != nil && returnErr == nil {
			returnErr = ErrOutcomeUncertain
		}
	}()

	ack, placeErr := route.Driver.PlaceOrder(ctx, request)
	if err := runHook(ctx, coordinator.hooks.AfterVenue, dispatching); err != nil {
		return resultFromStored(dispatching), err
	}
	states, venueOrderID, uncertain := classifyPlaceResult(route, request, ack, placeErr)
	// classifyPlaceResult collapses every venue outcome into a state, which is
	// the right shape for the ledger but leaves an operator with a bare
	// "rejected" and no way to learn why. The venue's own words are the only
	// account of the refusal that exists, so record them here; nothing else
	// downstream ever sees placeErr again.
	if placeErr != nil {
		slog.Error("gateway venue placement failed",
			"commandId", dispatching.CommandID, "accountId", dispatching.AccountID,
			"instrumentId", dispatching.InstrumentID, "routeId", routeID, "cause", placeErr)
	} else if ack.State == exchange.AckRejected {
		slog.Error("gateway venue rejected placement",
			"commandId", dispatching.CommandID, "accountId", dispatching.AccountID,
			"instrumentId", dispatching.InstrumentID, "routeId", routeID,
			"venueCode", ack.VenueCode, "venueMessage", ack.VenueMessage)
	}
	current := dispatching
	for _, nextState := range states {
		nextEventID, idErr := coordinator.identity.EventID()
		if idErr != nil {
			return resultFromStored(current), ErrOutcomeUncertain
		}
		var reconciliation *store.ReconciliationPlan
		if nextState == domain.StateUncertain {
			plan, planErr := coordinator.reconciliationPlan(store.ReconciliationAmbiguousHandoff)
			if planErr != nil {
				return resultFromStored(current), ErrOutcomeUncertain
			}
			reconciliation = &plan
		}
		updated, recordErr := coordinator.store.RecordVenueResult(ctx, store.RecordVenueResultRequest{
			CommandID: current.CommandID, VenueOrderID: venueOrderID, NextState: nextState,
			EventID: nextEventID, ResourceVersion: current.ResourceVersion,
			Reconciliation: reconciliation,
		})
		if recordErr != nil {
			return resultFromStored(current), ErrOutcomeUncertain
		}
		current = updated
		if err := runHook(ctx, coordinator.hooks.AfterCommitted, current); err != nil {
			return resultFromStored(current), err
		}
		if err := coordinator.publish(ctx, current); err != nil {
			return resultFromStored(current), err
		}
	}
	result = resultFromStored(current)
	if current.State == domain.StateRejected {
		result.VenueCode, result.VenueMessage = safeVenueRejection(ack, placeErr)
	}
	if uncertain || current.State == domain.StateUncertain {
		return result, ErrOutcomeUncertain
	}
	if current.State == domain.StateRejected {
		handoffState = store.OrderHandoffRejected
	} else {
		handoffState = store.OrderHandoffAccepted
	}
	return result, nil
}

func safeVenueRejection(ack exchange.OrderAcknowledgement, err error) (string, string) {
	code := strings.TrimSpace(ack.VenueCode)
	message := strings.TrimSpace(ack.VenueMessage)
	var venueErr *exchange.Error
	if errors.As(err, &venueErr) && venueErr.Kind() == exchange.KindVenueRejected {
		details := venueErr.Details()
		if code == "" {
			code = strings.TrimSpace(details.Code)
		}
		if message == "" {
			message = strings.TrimSpace(details.SafeMessage)
		}
	}
	return code, message
}

func (coordinator *Coordinator) reconcile(ctx context.Context, route Route, command store.StoredCommand) (bool, error) {
	lookup := exchange.ExactOrderLookupRequest{Instrument: &route.Instrument}
	if command.VenueOrderID != "" {
		lookup.VenueOrderID = command.VenueOrderID
	} else {
		lookup.ClientOrderID = command.ClientOrderID
	}
	evidence, err := route.Driver.LookupExactOrder(ctx, lookup)
	if err != nil {
		return coordinator.leaveUncertain(ctx, command, store.ReconciliationLookupFailed)
	}
	if evidence.validate() != nil {
		return coordinator.leaveUncertain(ctx, command, store.ReconciliationEvidenceInvalid)
	}
	switch evidence.Status {
	case exchange.ExactOrderUnconfirmed:
		return coordinator.leaveUncertain(ctx, command, store.ReconciliationEvidenceUnconfirmed)
	case exchange.ExactOrderNotFound:
		if command.State == domain.StateDispatching || command.State == domain.StateUncertain {
			updated, transitionErr := coordinator.transition(ctx, command, domain.StateRejected, "", "")
			return transitionErr == nil && updated.State == domain.StateRejected, transitionErr
		}
		return coordinator.leaveUncertain(ctx, command, store.ReconciliationEvidenceUnconfirmed)
	case exchange.ExactOrderFound:
		updated, transitionErr := coordinator.transition(ctx, command, evidence.State, evidence.Order.OrderID, "")
		return transitionErr == nil && updated.State != domain.StateUncertain, transitionErr
	default:
		return coordinator.leaveUncertain(ctx, command, store.ReconciliationEvidenceInvalid)
	}
}

func (coordinator *Coordinator) leaveUncertain(ctx context.Context, command store.StoredCommand, reason store.ReconciliationReason) (bool, error) {
	plan, err := coordinator.reconciliationPlan(reason)
	if err != nil {
		return false, ErrRecoveryIncomplete
	}
	if command.State == domain.StateUncertain {
		if _, err := coordinator.store.RescheduleReconciliation(ctx, store.RescheduleReconciliationRequest{
			GatewayOrderID: command.GatewayOrderID,
			Plan:           plan,
		}); err != nil {
			return false, ErrRecoveryIncomplete
		}
		return false, ErrRecoveryIncomplete
	}
	updated, err := coordinator.transition(ctx, command, domain.StateUncertain, command.VenueOrderID, reason)
	if err != nil {
		return false, err
	}
	return updated.State != domain.StateUncertain, ErrRecoveryIncomplete
}

func (coordinator *Coordinator) transition(ctx context.Context, command store.StoredCommand, target domain.OrderState, venueOrderID string, reason store.ReconciliationReason) (store.StoredCommand, error) {
	current := command
	for _, next := range transitionPath(current.State, target) {
		eventID, err := coordinator.identity.EventID()
		if err != nil {
			return current, fmt.Errorf("allocate order event failed")
		}
		var reconciliation *store.ReconciliationPlan
		if next == domain.StateUncertain {
			plan, planErr := coordinator.reconciliationPlan(reason)
			if planErr != nil {
				return current, planErr
			}
			reconciliation = &plan
		}
		updated, err := coordinator.store.RecordVenueResult(ctx, store.RecordVenueResultRequest{
			CommandID: current.CommandID, VenueOrderID: venueOrderID, NextState: next,
			EventID: eventID, ResourceVersion: current.ResourceVersion,
			Reconciliation: reconciliation,
		})
		if err != nil {
			return current, err
		}
		current = updated
		if err := coordinator.publish(ctx, current); err != nil {
			return current, err
		}
	}
	return current, nil
}

func (coordinator *Coordinator) reconciliationPlan(reason store.ReconciliationReason) (store.ReconciliationPlan, error) {
	taskID, err := coordinator.identity.EventID()
	if err != nil {
		return store.ReconciliationPlan{}, err
	}
	return store.ReconciliationPlan{
		TaskID:        taskID,
		Reason:        reason,
		NextAttemptAt: coordinator.now().Add(coordinator.reconciliationDelay).UTC(),
	}, nil
}

func classifyPlaceResult(route Route, request exchange.PlaceOrderRequest, ack exchange.OrderAcknowledgement, err error) ([]domain.OrderState, string, bool) {
	if err != nil {
		if errors.Is(err, exchange.ErrAmbiguousOutcome) || errors.Is(err, exchange.ErrMalformedResponse) ||
			errors.Is(err, exchange.ErrCanceled) || errors.Is(err, exchange.ErrDeadlineExceeded) {
			return []domain.OrderState{domain.StateUncertain}, ack.OrderID, true
		}
		return []domain.OrderState{domain.StateRejected}, ack.OrderID, false
	}
	if ack.Validate() != nil || ack.Venue != route.Instrument.Identity.Venue || ack.Product != route.Product ||
		ack.Operation != exchange.OrderOperationPlace || ack.NativeSymbol != route.Instrument.Identity.NativeSymbol ||
		(ack.ClientOrderID != "" && ack.ClientOrderID != request.ClientOrderID) {
		return []domain.OrderState{domain.StateUncertain}, ack.OrderID, true
	}
	switch ack.State {
	case exchange.AckAcceptedPending:
		return []domain.OrderState{domain.StateAccepted}, ack.OrderID, false
	case exchange.AckResting:
		return []domain.OrderState{domain.StateAccepted, domain.StateOpen}, ack.OrderID, false
	case exchange.AckPartiallyFilled:
		return []domain.OrderState{domain.StateAccepted, domain.StatePartiallyFilled}, ack.OrderID, false
	case exchange.AckImmediatelyFilled:
		return []domain.OrderState{domain.StateAccepted, domain.StateFilled}, ack.OrderID, false
	case exchange.AckRejected:
		return []domain.OrderState{domain.StateRejected}, ack.OrderID, false
	case exchange.AckAmbiguous:
		return []domain.OrderState{domain.StateUncertain}, ack.OrderID, true
	default:
		return []domain.OrderState{domain.StateUncertain}, ack.OrderID, true
	}
}

func transitionPath(current, target domain.OrderState) []domain.OrderState {
	if current == target {
		return nil
	}
	if current == domain.StateDispatching {
		switch target {
		case domain.StateOpen, domain.StatePartiallyFilled, domain.StateFilled:
			return []domain.OrderState{domain.StateAccepted, target}
		}
	}
	if current == domain.StateOpen && target == domain.StateAccepted {
		return nil
	}
	if current == domain.StatePartiallyFilled && (target == domain.StateAccepted || target == domain.StateOpen) {
		return nil
	}
	return []domain.OrderState{target}
}

func terminalOrderState(state domain.OrderState) bool {
	switch state {
	case domain.StateFilled, domain.StateCanceled, domain.StateRejected:
		return true
	default:
		return false
	}
}

func (coordinator *Coordinator) publish(ctx context.Context, command store.StoredCommand) error {
	if coordinator.publisher == nil {
		return nil
	}
	if err := coordinator.publisher.PublishCommitted(context.WithoutCancel(ctx), command); err != nil {
		return fmt.Errorf("publish committed order event failed")
	}
	return nil
}

func (coordinator *Coordinator) gate(accountID domain.AccountID) *accountGate {
	coordinator.gatesMu.Lock()
	defer coordinator.gatesMu.Unlock()
	gate := coordinator.gates[accountID]
	if gate == nil {
		gate = newAccountGate()
		coordinator.gates[accountID] = gate
	}
	return gate
}

func beginAccountWrite(gate *accountGate) bool {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if !gate.ready || gate.recovering {
		return false
	}
	gate.inFlight++
	return true
}

func endAccountWrite(gate *accountGate) {
	gate.mu.Lock()
	gate.inFlight--
	gate.cond.Broadcast()
	gate.mu.Unlock()
}

func markAccountNotReady(gate *accountGate) {
	gate.mu.Lock()
	gate.ready = false
	gate.mu.Unlock()
}

func beginAccountRecovery(gate *accountGate) {
	gate.mu.Lock()
	gate.recovering = true
	gate.ready = false
	for gate.inFlight > 0 {
		gate.cond.Wait()
	}
	gate.mu.Unlock()
}

func finishAccountRecovery(gate *accountGate, ready bool) {
	gate.mu.Lock()
	gate.ready = ready
	gate.recovering = false
	gate.cond.Broadcast()
	gate.mu.Unlock()
}

func resultFromReservation(command store.ReservedCommand) Result {
	return Result{
		CommandID: command.CommandID, GatewayOrderID: command.GatewayOrderID,
		ClientOrderID: command.ClientOrderID, State: command.State,
		ResourceVersion: command.ResourceVersion, VenueOrderID: command.VenueOrderID,
	}
}

func resultFromStored(command store.StoredCommand) Result {
	return Result{
		CommandID: command.CommandID, GatewayOrderID: command.GatewayOrderID,
		ClientOrderID: command.ClientOrderID, State: command.State,
		ResourceVersion: command.ResourceVersion, AccountID: command.AccountID,
		InstrumentID: command.InstrumentID, VenueOrderID: command.VenueOrderID,
	}
}

func runHook(ctx context.Context, hook func(context.Context, store.StoredCommand) error, command store.StoredCommand) error {
	if hook == nil {
		return nil
	}
	return hook(ctx, command)
}
