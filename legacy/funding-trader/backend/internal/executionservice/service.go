package executionservice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/domain/execution"
	"github.com/shopspring/decimal"
)

const (
	submitMethod                 = "POST"
	submitPath                   = "/api/v1/orders"
	marketOrderReobserveAttempts = 6
	marketOrderReobserveInterval = 100 * time.Millisecond
)

type Service struct {
	store     Store
	source    FreshPreviewSource
	gateway   Gateway
	publisher Publisher
	repairer  RecoveryRepairer
	clock     Clock
	ids       IDGenerator
	wait      func(context.Context, time.Duration) error

	operationMu sync.Mutex
	mu          sync.RWMutex
	ready       bool
	recovering  bool
	degraded    bool
	reason      string
}

type Options struct {
	Store            Store
	Source           FreshPreviewSource
	Gateway          Gateway
	Publisher        Publisher
	RecoveryRepairer RecoveryRepairer
	Clock            Clock
	IDs              IDGenerator
	Wait             func(context.Context, time.Duration) error
	Ready            bool
}

func New(options Options) (*Service, error) {
	if options.Store == nil || options.Source == nil || options.Gateway == nil {
		return nil, errors.New("execution service dependencies are required")
	}
	clock := options.Clock
	if clock == nil {
		clock = systemClock{}
	}
	ids := options.IDs
	if ids == nil {
		ids = deterministicIDs{}
	}
	wait := options.Wait
	if wait == nil {
		wait = waitForContext
	}
	return &Service{
		store: options.Store, source: options.Source, gateway: options.Gateway,
		publisher: options.Publisher, repairer: options.RecoveryRepairer,
		clock: clock, ids: ids, wait: wait,
		ready: options.Ready, reason: readinessReason(options.Ready),
	}, nil
}

func (service *Service) Readiness() Readiness {
	service.mu.RLock()
	defer service.mu.RUnlock()
	return Readiness{
		Ready:      service.ready && !service.recovering,
		Recovering: service.recovering,
		Degraded:   service.degraded,
		Reason:     service.reason,
	}
}

func (service *Service) Preview(ctx context.Context, scope Scope, request PreviewRequest) (execution.PreviewResult, error) {
	if err := validateScope(scope); err != nil {
		return execution.PreviewResult{}, err
	}
	input, err := service.source.FreshPreview(ctx, scope, request)
	if err != nil {
		return execution.PreviewResult{}, safeError(err)
	}
	input, err = normalizePreviewInput(input, scope, request, service.clock.Now())
	if err != nil {
		return execution.PreviewResult{}, err
	}
	preview, err := execution.Preview(input)
	if err != nil {
		return execution.PreviewResult{}, safeError(err)
	}
	hash, err := stablePreviewHash(preview)
	if err != nil {
		return execution.PreviewResult{}, err
	}
	if err := service.store.SavePreview(ctx, preview, hash); err != nil {
		return execution.PreviewResult{}, mapStoreError(err)
	}
	return preview, nil
}

func (service *Service) Submit(ctx context.Context, scope Scope, request SubmitRequest, idempotencyKey, requestID string) (Result, error) {
	if err := validateScope(scope); err != nil {
		return Result{}, err
	}
	if request.Operation != execution.OperationOpen || strings.TrimSpace(request.PositionID) != "" {
		return Result{}, ErrPreviewMismatch
	}
	service.operationMu.Lock()
	defer service.operationMu.Unlock()
	return service.submit(ctx, scope, request, submitPath, idempotencyKey, requestID)
}

func (service *Service) Close(ctx context.Context, scope Scope, request SubmitRequest, idempotencyKey, requestID string) (Result, error) {
	if err := validateScope(scope); err != nil {
		return Result{}, err
	}
	if request.Operation != execution.OperationClose || strings.TrimSpace(request.PositionID) == "" {
		return Result{}, ErrPreviewMismatch
	}
	service.operationMu.Lock()
	defer service.operationMu.Unlock()
	return service.submit(ctx, scope, request, closePath(request.PositionID), idempotencyKey, requestID)
}

func (service *Service) submit(ctx context.Context, scope Scope, request SubmitRequest, path string, idempotencyKey, requestID string) (Result, error) {
	if err := service.requireReady(); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(idempotencyKey) == "" {
		return Result{}, errors.New("idempotency key is required")
	}
	if strings.TrimSpace(requestID) == "" {
		requestID = idempotencyKey
	}
	previewRequest := PreviewRequest{
		PreviewID: request.PreviewID, Operation: request.Operation, OpportunityID: request.OpportunityID, PositionID: request.PositionID,
		CapitalUSDT: request.CapitalUSDT, Leverage: request.Leverage,
	}
	requestHash, err := browserRequestHash(scope, submitMethod, path, previewRequest)
	if err != nil {
		return Result{}, err
	}
	existing, err := service.store.GetIdempotency(ctx, scope, submitMethod, path, idempotencyKey)
	if err == nil {
		if existing.RequestHash != requestHash {
			return Result{}, ErrIdempotencyConflict
		}
		stored, getErr := service.store.GetExecution(ctx, scope, existing.ExecutionID)
		if getErr != nil {
			return Result{}, mapStoreError(getErr)
		}
		if existing.State != "completed" && recoveryTerminal(stored.Execution) {
			return service.completeLinkedTerminal(ctx, scope, stored.Execution, true), nil
		}
		return idempotencyResult(stored.Execution, existing)
	}
	if !errors.Is(err, ErrNotFound) {
		return Result{}, mapStoreError(err)
	}
	autoClosePreview := request.Operation == execution.OperationClose &&
		strings.TrimSpace(previewRequest.PreviewID) == ""
	if autoClosePreview {
		previewRequest.PreviewID = automaticClosePreviewID(scope, path, idempotencyKey)
	}
	var stored StoredPreview
	if !autoClosePreview {
		receivedAt := request.ReceivedAt.UTC()
		if receivedAt.IsZero() {
			receivedAt = service.clock.Now().UTC()
		}
		stored, err = service.store.GetPreview(ctx, scope, request.PreviewID, receivedAt)
		if err != nil {
			return Result{}, mapStoreError(err)
		}
		if stored.Expired {
			return Result{}, ErrPreviewExpired
		}
	}
	input, err := service.source.FreshPreview(ctx, scope, previewRequest)
	if err != nil {
		return Result{}, safeError(err)
	}
	input, err = normalizePreviewInput(input, scope, previewRequest, service.clock.Now())
	if err != nil {
		return Result{}, err
	}
	fresh, err := execution.Preview(input)
	if err != nil {
		return Result{}, safeError(err)
	}
	if autoClosePreview {
		hash, hashErr := stablePreviewHash(fresh)
		if hashErr != nil {
			return Result{}, hashErr
		}
		if saveErr := service.store.SavePreview(ctx, fresh, hash); saveErr != nil {
			return Result{}, mapStoreError(saveErr)
		}
	}
	if autoClosePreview {
		stored, err = service.store.GetPreview(ctx, scope, previewRequest.PreviewID, service.clock.Now())
		if err != nil {
			return Result{}, mapStoreError(err)
		}
	}
	if err := validateStoredPreview(stored, fresh); err != nil {
		if errors.Is(err, ErrBlockedPreview) {
			return Result{SafeError: execution.ErrorInfo{Code: "PREVIEW_BLOCKED", SafeMessage: "preview is blocked"}}, err
		}
		return Result{}, err
	}
	approved := stored.Preview
	executionID := service.ids.NewExecutionID(scope, idempotencyKey, approved)
	exec, err := execution.NewExecution(executionID, approved)
	if err != nil {
		return Result{}, safeError(err)
	}
	reservation, err := service.store.ReserveExecution(ctx, scope, submitMethod, path, idempotencyKey, requestHash, exec.Snapshot())
	if err != nil {
		return Result{}, mapStoreError(err)
	}
	if reservation.Replayed {
		if recoveryTerminal(reservation.Execution) && reservation.State != "completed" {
			return service.completeLinkedTerminal(ctx, scope, reservation.Execution, true), nil
		}
		return reservationResult(reservation)
	}
	current := reservation.Execution
	version := normalizedVersion(reservation.Version)
	result := Result{Execution: current.Snapshot()}
	if err := current.Apply(execution.BeginSubmit(service.clock.Now())); err != nil {
		return Result{}, safeError(err)
	}
	version, committed, err := service.persist(ctx, scope, current, version, "begin_submit", requestID, nil)
	result = mergePublishResult(result, committed)
	if err != nil {
		return result, err
	}
	result.Execution = current.Snapshot()
	if err := service.executeNormalLegs(ctx, scope, &current, &version, requestID, &result); err != nil {
		result.Execution = current.Snapshot()
		if terminal, terminalErr := service.terminalReplayResult(ctx, scope, current.ID, false); terminalErr == nil {
			return terminal, nil
		}
		return result, err
	}
	result.Execution = current.Snapshot()
	body, _ := json.Marshal(current.Snapshot())
	if len(body) > 0 {
		if err := service.store.CompleteIdempotency(ctx, scope, submitMethod, path, idempotencyKey, 202, body); err != nil {
			result.Degraded = true
			result.Recoverable = true
			result.SafeError = execution.ErrorInfo{Code: "IDEMPOTENCY_COMPLETION_FAILED", SafeMessage: "execution response persistence failed", Retryable: true}
			result.ResponseCode = 202
			result.ResponseBody = body
			if recoveryTerminal(current) {
				return result, nil
			}
			return result, safeError(err)
		}
		result.ResponseCode = 202
		result.ResponseBody = body
	}
	return result, nil
}

func (service *Service) Get(ctx context.Context, scope Scope, id string) (execution.Execution, error) {
	if err := validateScope(scope); err != nil {
		return execution.Execution{}, err
	}
	stored, err := service.store.GetExecution(ctx, scope, id)
	if err != nil {
		return execution.Execution{}, mapStoreError(err)
	}
	return stored.Execution.Snapshot(), nil
}

func (service *Service) Reconcile(ctx context.Context, scope Scope, id string, requestID string) (Result, error) {
	if err := validateScope(scope); err != nil {
		return Result{}, err
	}
	service.operationMu.Lock()
	defer service.operationMu.Unlock()
	stored, err := service.store.GetExecution(ctx, scope, id)
	if err != nil {
		return Result{}, mapStoreError(err)
	}
	version := normalizedVersion(stored.Version)
	result := Result{Execution: stored.Execution.Snapshot()}
	err = service.reconcileExecution(ctx, scope, &stored.Execution, &version, requestID, &result, false)
	result.Execution = stored.Execution.Snapshot()
	return result, err
}

func (service *Service) Recover(ctx context.Context, scope Scope, requestID string) (Result, error) {
	if err := validateScope(scope); err != nil {
		return Result{}, err
	}
	needsAuthoritativeRepair, err := service.startRecovery()
	if err != nil {
		return Result{}, err
	}
	service.operationMu.Lock()
	defer service.operationMu.Unlock()
	success := false
	clearDegraded := false
	defer func() {
		if success {
			service.finishRecovery(clearDegraded)
		}
	}()
	authoritativeRepaired := false
	if needsAuthoritativeRepair && service.repairer != nil {
		if err := service.repairer.Repair(ctx, scope); err != nil {
			service.markRecoveryFailed("recovery repair failed")
			return Result{}, safeError(err)
		}
		authoritativeRepaired = true
	}
	stored, err := service.store.ListNonTerminal(ctx, scope)
	if err != nil {
		service.markRecoveryFailed("recovery failed")
		return Result{}, mapStoreError(err)
	}
	var combined Result
	unresolved := false
	for _, item := range stored {
		version := normalizedVersion(item.Version)
		if err := service.reconcileExecution(ctx, scope, &item.Execution, &version, requestID, &combined, true); err != nil {
			service.markRecoveryFailed("recovery failed")
			combined.Execution = item.Execution.Snapshot()
			return combined, err
		}
		combined.Execution = item.Execution.Snapshot()
		if recoveryTerminal(item.Execution) {
			combined = service.mergeTerminalRepair(ctx, scope, item.Execution, combined)
		}
		if !recoveryTerminal(item.Execution) {
			unresolved = true
		}
	}
	terminalIncomplete, err := service.store.ListTerminalWithIncompleteIdempotency(ctx, scope)
	if err != nil {
		service.markRecoveryFailed("recovery failed")
		return combined, mapStoreError(err)
	}
	for _, item := range terminalIncomplete {
		if !recoveryTerminal(item.Execution) {
			unresolved = true
			continue
		}
		combined.Execution = item.Execution.Snapshot()
		combined = service.mergeTerminalRepair(ctx, scope, item.Execution, combined)
	}
	if unresolved {
		service.markRecoveryFailed("manual recovery required")
		return combined, nil
	}
	clearDegraded = authoritativeRepaired && !combined.Degraded && !combined.Recoverable
	success = true
	return combined, nil
}

func (service *Service) executeNormalLegs(ctx context.Context, scope Scope, exec *execution.Execution, version *int64, requestID string, result *Result) error {
	for _, role := range []execution.LegRole{execution.LegRoleLong, execution.LegRoleShort} {
		plan, ok := plannedLeg(exec, role)
		if !ok {
			return errors.New("planned leg is missing")
		}
		gatewayID := gatewayIdentity(exec.ID, role)
		outcome, err := service.placeAndResolve(ctx, plan, role, gatewayID, false)
		result.GatewayWrites = append(result.GatewayWrites, gatewayID)
		if err != nil {
			return err
		}
		if err := service.applyOutcome(ctx, scope, exec, version, requestID, outcome, false, result); err != nil {
			return err
		}
		if exec.State == execution.StateUncertain || exec.State == execution.StateManualInterventionRequired {
			return nil
		}
		if exec.State == execution.StateFailed {
			return nil
		}
		if role == execution.LegRoleLong && !legFilled(exec, execution.LegRoleLong) {
			refreshed, err := service.refreshLegFillBeforeCancel(ctx, scope, exec, version, requestID, result, plan, gatewayID)
			if err != nil {
				return err
			}
			if refreshed {
				continue
			}
			if !legNeedsCancel(exec, execution.LegRoleLong) {
				return nil
			}
			if err := service.cancelUnsafeRemainder(ctx, scope, exec, version, requestID, result, plan, gatewayID); err != nil {
				return err
			}
			if exec.State == execution.StateUncertain || exec.State == execution.StateManualInterventionRequired {
				return nil
			}
			if legFilled(exec, execution.LegRoleLong) {
				continue
			}
			return service.compensateIfNeeded(ctx, scope, exec, version, requestID, result)
		}
		if exec.State == execution.StatePartiallyHedged {
			if legNeedsCancel(exec, role) {
				if err := service.cancelUnsafeRemainder(ctx, scope, exec, version, requestID, result, plan, gatewayID); err != nil {
					return err
				}
				if exec.State == execution.StateUncertain || exec.State == execution.StateManualInterventionRequired {
					return nil
				}
			}
			return service.compensateIfNeeded(ctx, scope, exec, version, requestID, result)
		}
	}
	return nil
}

func (service *Service) placeAndResolve(ctx context.Context, plan execution.PlannedLeg, role execution.LegRole, gatewayID string, compensation bool) (legOutcome, error) {
	result, err := service.gateway.PlaceOrder(ctx, GatewayOrderRequest{
		GatewayID: gatewayID, IdempotencyKey: gatewayID, AccountID: plan.AccountID,
		InstrumentID: plan.GatewayInstrumentID, Side: plan.Side, Quantity: plan.Quantity,
		ReduceOnly: plan.ReduceOnly || compensation, Role: role,
	})
	if err != nil {
		return legOutcome{}, safeError(err)
	}
	if result.GatewayID == "" {
		result.GatewayID = gatewayID
	}
	if result.Outcome == GatewayAmbiguous {
		observed, fills, observeErr := service.observeOrder(ctx, plan, result.GatewayID, orderLookupID(result.GatewayID, result.OrderID))
		if observeErr != nil || !observed.Found {
			return legOutcome{role: role, gatewayID: result.GatewayID, orderID: observedOrderID(result.OrderID, observed), outcome: GatewayAmbiguous, err: result.Error, compensation: compensation}, nil
		}
		return legOutcomeFromObservation(role, plan, observed, fills, compensation), nil
	}
	if result.Outcome == GatewayRejected {
		return legOutcome{role: role, gatewayID: result.GatewayID, orderID: result.OrderID, outcome: GatewayRejected, err: result.Error, compensation: compensation}, nil
	}
	observed, fills, observeErr := service.observeOrder(ctx, plan, result.GatewayID, orderLookupID(result.GatewayID, result.OrderID))
	if observeErr != nil {
		return legOutcome{role: role, gatewayID: result.GatewayID, orderID: observedOrderID(result.OrderID, observed), outcome: GatewayAmbiguous, err: execution.ErrorInfo{Code: "RECONCILE_UNCERTAIN", SafeMessage: "gateway reconciliation is uncertain", Retryable: true}, compensation: compensation}, nil
	}
	if !observed.Found {
		return legOutcome{
			role: role, gatewayID: result.GatewayID, orderID: result.OrderID, outcome: GatewayAmbiguous,
			err:          execution.ErrorInfo{Code: "ORDER_NOT_OBSERVED_AFTER_ACCEPTANCE", SafeMessage: "accepted order was not observed after placement", Retryable: true},
			compensation: compensation,
		}, nil
	}
	return legOutcomeFromObservation(role, plan, observed, fills, compensation), nil
}

func (service *Service) observeOrder(ctx context.Context, plan execution.PlannedLeg, gatewayID, lookupID string) (OrderObservation, []FillObservation, error) {
	lookupID = orderLookupID(gatewayID, lookupID)
	observed, err := service.gateway.GetOrder(ctx, plan.AccountID, lookupID)
	if err != nil {
		return OrderObservation{}, nil, err
	}
	if !observed.Found {
		return observed, nil, nil
	}
	observed.GatewayID = gatewayID
	fills, err := service.gateway.Fills(ctx, plan.AccountID, plan.GatewayInstrumentID, lookupID)
	if err != nil {
		return observed, nil, err
	}
	return observed, fills, nil
}

func (service *Service) applyOutcome(ctx context.Context, scope Scope, exec *execution.Execution, version *int64, requestID string, outcome legOutcome, compensation bool, result *Result) error {
	if outcome.outcome == GatewayRejected {
		if compensation {
			if err := exec.Apply(execution.CompensationRejectedBeforeAcceptance(outcome.gatewayID, outcome.orderID, outcome.role, outcome.quantity, safeGatewayError(outcome.err), service.clock.Now())); err != nil {
				return safeError(err)
			}
			var err error
			*version, *result, err = service.persistIntoResult(ctx, scope, *exec, *version, "compensation_rejected", requestID, *result)
			return err
		}
		info := safeGatewayError(outcome.err)
		if err := exec.Apply(execution.Event{
			Type: execution.EventLegRejected, GatewayID: outcome.gatewayID, Role: outcome.role,
			OrderID: outcome.orderID, Error: info.SafeMessage, SafeError: info, At: service.clock.Now(),
		}); err != nil {
			return safeError(err)
		}
		var err error
		*version, *result, err = service.persistIntoResult(ctx, scope, *exec, *version, "leg_rejected", requestID, *result)
		return err
	}
	if outcome.outcome == GatewayAmbiguous {
		info := safeGatewayError(outcome.err)
		event := execution.Event{
			Type: execution.EventLegUncertain, GatewayID: outcome.gatewayID, Role: outcome.role,
			OrderID: outcome.orderID, Error: info.SafeMessage, SafeError: info, At: service.clock.Now(),
		}
		if compensation {
			event = execution.CompensationUncertainCode(outcome.gatewayID, info, outcome.role, outcome.quantity, service.clock.Now())
			event.OrderID = outcome.orderID
		}
		if err := exec.Apply(event); err != nil {
			return safeError(err)
		}
		var err error
		*version, *result, err = service.persistIntoResult(ctx, scope, *exec, *version, transitionKind("leg_uncertain", compensation), requestID, *result)
		return err
	}
	if compensation {
		if err := exec.Apply(execution.CompensationAccepted(outcome.gatewayID, outcome.orderID, outcome.role, outcome.quantity, service.clock.Now())); err != nil {
			return safeError(err)
		}
		var err error
		*version, *result, err = service.persistIntoResult(ctx, scope, *exec, *version, "compensation_accepted", requestID, *result)
		if err != nil {
			return err
		}
	} else {
		if err := exec.Apply(execution.LegAccepted(outcome.gatewayID, outcome.role, outcome.orderID, service.clock.Now())); err != nil {
			return safeError(err)
		}
		var err error
		*version, *result, err = service.persistIntoResult(ctx, scope, *exec, *version, "leg_accepted", requestID, *result)
		if err != nil {
			return err
		}
	}
	if outcome.quantity.IsPositive() {
		for _, event := range fillEvents(outcome, service.clock.Now()) {
			if err := exec.Apply(event); err != nil {
				return safeError(err)
			}
			var err error
			*version, *result, err = service.persistIntoResult(ctx, scope, *exec, *version, transitionKind("leg_fill", compensation), requestID, *result)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (service *Service) refreshLegFillBeforeCancel(ctx context.Context, scope Scope, exec *execution.Execution, version *int64, requestID string, result *Result, plan execution.PlannedLeg, gatewayID string) (bool, error) {
	if !legNeedsCancel(exec, plan.Role) {
		return false, nil
	}
	for attempt := 0; attempt < marketOrderReobserveAttempts; attempt++ {
		if attempt > 0 {
			if err := service.wait(ctx, marketOrderReobserveInterval); err != nil {
				return false, err
			}
		}
		observed, fills, err := service.observeOrder(ctx, plan, gatewayID, cancelOrderLookupID(exec, gatewayID))
		if err != nil || !observed.Found {
			continue
		}
		outcome := legOutcomeFromObservation(plan.Role, plan, observed, fills, false)
		if outcome.outcome != GatewayAccepted {
			return false, nil
		}
		if !outcome.quantity.IsPositive() {
			continue
		}
		alreadyFilled := decimal.Zero
		for _, leg := range exec.Legs {
			if leg.Role == plan.Role {
				alreadyFilled = leg.Filled
				break
			}
		}
		for _, event := range fillEventsAfter(outcome, alreadyFilled, service.clock.Now()) {
			if err := exec.Apply(event); err != nil {
				return false, safeError(err)
			}
			var persistErr error
			*version, *result, persistErr = service.persistIntoResult(ctx, scope, *exec, *version, "pre_cancel_observed_fill", requestID, *result)
			if persistErr != nil {
				return false, persistErr
			}
		}
		if outcome.status == execution.LegStatusFilled {
			promoted, err := service.promoteStatusOnlyFilled(ctx, scope, exec, version, requestID, gatewayID, outcome, "pre_cancel_observed_fill", result)
			if err != nil {
				return false, err
			}
			if promoted || legFilled(exec, plan.Role) {
				return true, nil
			}
		}
	}
	return legFilled(exec, plan.Role), nil
}

func waitForContext(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		return nil
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (service *Service) compensateIfNeeded(ctx context.Context, scope Scope, exec *execution.Execution, version *int64, requestID string, result *Result) error {
	if !exec.DirectionalExposure.IsPositive() || exec.Compensation != nil {
		return nil
	}
	target, ok := compensationTarget(exec)
	if !ok {
		return nil
	}
	plan, ok := plannedLeg(exec, target.Role)
	if !ok {
		return nil
	}
	plan.Side = opposite(plan.Side)
	plan.Quantity = exec.DirectionalExposure
	plan.ReduceOnly = true
	gatewayID := gatewayIdentity(exec.ID, execution.LegRoleCompensation)
	outcome, err := service.placeAndResolve(ctx, plan, target.Role, gatewayID, true)
	result.GatewayWrites = append(result.GatewayWrites, gatewayID)
	if err != nil {
		return err
	}
	if outcome.quantity.IsZero() {
		outcome.quantity = exec.DirectionalExposure
	}
	return service.applyOutcome(ctx, scope, exec, version, requestID, outcome, true, result)
}

func (service *Service) reconcileExecution(ctx context.Context, scope Scope, exec *execution.Execution, version *int64, requestID string, result *Result, continueMissingLeg bool) error {
	if exec.State == execution.StateSubmitting && len(exec.Legs) == 0 {
		if err := exec.Apply(execution.LegUncertain(gatewayIdentity(exec.ID, execution.LegRoleLong), execution.LegRoleLong, "restart before gateway handoff completed", service.clock.Now())); err != nil {
			return safeError(err)
		}
		exec.ManualIntervention = true
		exec.ManualInterventionReason = "restart before gateway handoff completed"
		exec.State = execution.StateManualInterventionRequired
		var err error
		*version, *result, err = service.persistIntoResult(ctx, scope, *exec, *version, "recover_unsubmitted", requestID, *result)
		return err
	}
	for _, leg := range exec.Legs {
		plan, ok := plannedLeg(exec, leg.Role)
		if leg.Role == execution.LegRoleCompensation && exec.Compensation != nil {
			plan, ok = plannedLeg(exec, exec.Compensation.TargetRole)
			plan.Side = opposite(plan.Side)
			plan.Quantity = exec.Compensation.Quantity
			plan.ReduceOnly = true
		}
		if !ok || strings.TrimSpace(leg.GatewayID) == "" {
			return ErrServiceUnavailable
		}
		observed, fills, err := service.observeOrder(ctx, plan, leg.GatewayID, legOrderLookupID(leg))
		if err != nil {
			return safeError(err)
		}
		var outcome legOutcome
		if observed.Found {
			outcome = legOutcomeFromObservation(leg.Role, plan, observed, fills, leg.Role == execution.LegRoleCompensation)
		} else if replayRejectedCloseLeg(exec, leg) {
			outcome, err = service.placeAndResolve(ctx, plan, leg.Role, leg.GatewayID, false)
			result.GatewayWrites = append(result.GatewayWrites, leg.GatewayID)
			if err != nil {
				return err
			}
		} else {
			return ErrServiceUnavailable
		}
		if err := service.applyRecoveryStatus(ctx, scope, exec, version, requestID, leg, outcome, result); err != nil {
			return err
		}
		if _, err := service.promoteStatusOnlyFilled(ctx, scope, exec, version, requestID, leg.GatewayID, outcome, "reconcile_status", result); err != nil {
			return err
		}
		if outcome.quantity.IsZero() || !outcome.quantity.GreaterThan(leg.Filled) {
			continue
		}
		for _, event := range fillEventsAfter(outcome, leg.Filled, service.clock.Now()) {
			event.OrderID = outcome.orderID
			if err := exec.Apply(event); err != nil {
				return safeError(err)
			}
			var persistErr error
			*version, *result, persistErr = service.persistIntoResult(ctx, scope, *exec, *version, "reconcile_fill", requestID, *result)
			if persistErr != nil {
				return persistErr
			}
		}
	}
	if continueMissingLeg {
		if err := service.continueMissingSecondLeg(ctx, scope, exec, version, requestID, result); err != nil {
			return err
		}
	}
	return nil
}

func replayRejectedCloseLeg(exec *execution.Execution, leg execution.Leg) bool {
	if exec.Operation != execution.OperationClose || !leg.ReduceOnly || !leg.Filled.IsZero() {
		return false
	}
	if leg.Role != execution.LegRoleLong && leg.Role != execution.LegRoleShort {
		return false
	}
	return leg.Status == execution.LegStatusRejected || leg.Status == execution.LegStatusUncertain
}

func (service *Service) continueMissingSecondLeg(ctx context.Context, scope Scope, exec *execution.Execution, version *int64, requestID string, result *Result) error {
	if (exec.Operation != execution.OperationOpen && exec.Operation != execution.OperationClose) ||
		exec.ManualIntervention ||
		exec.Compensation != nil ||
		hasLeg(exec, execution.LegRoleShort) ||
		!legFilled(exec, execution.LegRoleLong) {
		return nil
	}
	plan, ok := plannedLeg(exec, execution.LegRoleShort)
	if !ok {
		return nil
	}
	gatewayID := gatewayIdentity(exec.ID, execution.LegRoleShort)
	observed, err := service.gateway.GetOrder(ctx, plan.AccountID, gatewayID)
	if err != nil {
		return safeError(err)
	}
	var outcome legOutcome
	if observed.Found {
		observed.GatewayID = gatewayID
		fills, fillsErr := service.gateway.Fills(ctx, plan.AccountID, plan.GatewayInstrumentID, gatewayID)
		if fillsErr != nil {
			return safeError(fillsErr)
		}
		outcome = legOutcomeFromObservation(execution.LegRoleShort, plan, observed, fills, false)
	} else {
		var placeErr error
		outcome, placeErr = service.placeAndResolve(ctx, plan, execution.LegRoleShort, gatewayID, false)
		result.GatewayWrites = append(result.GatewayWrites, gatewayID)
		if placeErr != nil {
			return placeErr
		}
	}
	if err := service.applyOutcome(ctx, scope, exec, version, requestID, outcome, false, result); err != nil {
		return err
	}
	if exec.Operation != execution.OperationOpen || exec.State != execution.StatePartiallyHedged {
		return nil
	}
	if legNeedsCancel(exec, execution.LegRoleShort) {
		if err := service.cancelUnsafeRemainder(ctx, scope, exec, version, requestID, result, plan, gatewayID); err != nil {
			return err
		}
		if exec.State == execution.StateUncertain || exec.State == execution.StateManualInterventionRequired {
			return nil
		}
	}
	return service.compensateIfNeeded(ctx, scope, exec, version, requestID, result)
}

func (service *Service) cancelUnsafeRemainder(ctx context.Context, scope Scope, exec *execution.Execution, version *int64, requestID string, result *Result, plan execution.PlannedLeg, gatewayID string) error {
	cancel, err := service.gateway.CancelOrder(ctx, GatewayCancelRequest{AccountID: plan.AccountID, GatewayID: cancelOrderLookupID(exec, gatewayID)})
	if err != nil || cancel.Outcome == GatewayAmbiguous {
		refreshed, refreshErr := service.refreshLegFillBeforeCancel(ctx, scope, exec, version, requestID, result, plan, gatewayID)
		if refreshErr != nil {
			return refreshErr
		}
		if refreshed {
			return nil
		}
		info := execution.ErrorInfo{Code: "CANCEL_UNCERTAIN", SafeMessage: "order cancellation is uncertain", Retryable: true}
		if err == nil {
			info = safeGatewayError(cancel.Error)
		}
		if applyErr := exec.Apply(execution.LegUncertain(gatewayID, plan.Role, info.SafeMessage, service.clock.Now())); applyErr != nil {
			return safeError(applyErr)
		}
		exec.ManualIntervention = true
		exec.ManualInterventionReason = "cancel uncertain: " + info.SafeMessage
		exec.State = execution.StateManualInterventionRequired
		var persistErr error
		*version, *result, persistErr = service.persistIntoResult(ctx, scope, *exec, *version, "cancel_uncertain", requestID, *result)
		return persistErr
	}
	if cancel.Outcome == GatewayRejected {
		refreshed, refreshErr := service.refreshLegFillBeforeCancel(ctx, scope, exec, version, requestID, result, plan, gatewayID)
		if refreshErr != nil {
			return refreshErr
		}
		if refreshed {
			return nil
		}
		if applyErr := exec.Apply(execution.LegUncertain(gatewayID, plan.Role, "order cancellation is uncertain", service.clock.Now())); applyErr != nil {
			return safeError(applyErr)
		}
		exec.ManualIntervention = true
		exec.ManualInterventionReason = "cancel rejected: " + safeGatewayError(cancel.Error).SafeMessage
		exec.State = execution.StateManualInterventionRequired
		var persistErr error
		*version, *result, persistErr = service.persistIntoResult(ctx, scope, *exec, *version, "cancel_rejected", requestID, *result)
		return persistErr
	}
	observed, fills, observeErr := service.observeOrder(ctx, plan, gatewayID, cancelOrderLookupID(exec, gatewayID))
	if observeErr != nil || !observed.Found {
		info := execution.ErrorInfo{Code: "CANCEL_RECONCILE_UNCERTAIN", SafeMessage: "order cancellation is unconfirmed", Retryable: true}
		if applyErr := exec.Apply(execution.LegUncertain(gatewayID, plan.Role, info.SafeMessage, service.clock.Now())); applyErr != nil {
			return safeError(applyErr)
		}
		exec.ManualIntervention = true
		exec.ManualInterventionReason = "cancel accepted but final order status is unconfirmed"
		exec.State = execution.StateManualInterventionRequired
		var persistErr error
		*version, *result, persistErr = service.persistIntoResult(ctx, scope, *exec, *version, "cancel_reconcile_uncertain", requestID, *result)
		return persistErr
	}
	if observed.Status != execution.LegStatusCanceled {
		outcome := legOutcomeFromObservation(plan.Role, plan, observed, fills, false)
		if outcome.outcome == GatewayAmbiguous {
			if applyErr := exec.Apply(execution.LegUncertain(gatewayID, plan.Role, safeGatewayError(outcome.err).SafeMessage, service.clock.Now())); applyErr != nil {
				return safeError(applyErr)
			}
			exec.ManualIntervention = true
			exec.ManualInterventionReason = "cancel accepted but final order status is unconfirmed"
			exec.State = execution.StateManualInterventionRequired
			var persistErr error
			*version, *result, persistErr = service.persistIntoResult(ctx, scope, *exec, *version, "cancel_reconcile_uncertain", requestID, *result)
			return persistErr
		}
		if outcome.quantity.IsPositive() {
			alreadyFilled := decimal.Zero
			for _, leg := range exec.Legs {
				if leg.Role == plan.Role {
					alreadyFilled = leg.Filled
					break
				}
			}
			for _, event := range fillEventsAfter(outcome, alreadyFilled, service.clock.Now()) {
				if applyErr := exec.Apply(event); applyErr != nil {
					return safeError(applyErr)
				}
				var persistErr error
				*version, *result, persistErr = service.persistIntoResult(ctx, scope, *exec, *version, "cancel_observed_fill", requestID, *result)
				if persistErr != nil {
					return persistErr
				}
			}
		}
		if outcome.status == execution.LegStatusFilled {
			if _, err := service.promoteStatusOnlyFilled(ctx, scope, exec, version, requestID, gatewayID, outcome, "cancel_observed_fill", result); err != nil {
				return err
			}
			return nil
		}
		if applyErr := exec.Apply(execution.LegUncertain(gatewayID, plan.Role, "cancel accepted but order is not confirmed canceled", service.clock.Now())); applyErr != nil {
			return safeError(applyErr)
		}
		exec.ManualIntervention = true
		exec.ManualInterventionReason = "cancel accepted but order is not confirmed canceled"
		exec.State = execution.StateManualInterventionRequired
		var persistErr error
		*version, *result, persistErr = service.persistIntoResult(ctx, scope, *exec, *version, "cancel_reconcile_uncertain", requestID, *result)
		return persistErr
	}
	if applyErr := exec.Apply(execution.LegCanceled(gatewayID, service.clock.Now())); applyErr != nil {
		return safeError(applyErr)
	}
	var persistErr error
	*version, *result, persistErr = service.persistIntoResult(ctx, scope, *exec, *version, "leg_canceled", requestID, *result)
	return persistErr
}

func (service *Service) applyRecoveryStatus(ctx context.Context, scope Scope, exec *execution.Execution, version *int64, requestID string, leg execution.Leg, outcome legOutcome, result *Result) error {
	if leg.Status == outcome.status || outcome.status == "" {
		return nil
	}
	switch outcome.status {
	case execution.LegStatusRejected:
		if err := exec.Apply(execution.LegRejectedCode(leg.GatewayID, safeGatewayError(outcome.err), service.clock.Now())); err != nil {
			return safeError(err)
		}
	case execution.LegStatusCanceled:
		if err := exec.Apply(execution.LegCanceled(leg.GatewayID, service.clock.Now())); err != nil {
			return safeError(err)
		}
	case execution.LegStatusUncertain:
		if err := exec.Apply(execution.LegUncertain(leg.GatewayID, leg.Role, "gateway outcome is uncertain", service.clock.Now())); err != nil {
			return safeError(err)
		}
	default:
		return nil
	}
	var err error
	*version, *result, err = service.persistIntoResult(ctx, scope, *exec, *version, "reconcile_status", requestID, *result)
	return err
}

func (service *Service) promoteStatusOnlyFilled(ctx context.Context, scope Scope, exec *execution.Execution, version *int64, requestID, gatewayID string, outcome legOutcome, kind string, result *Result) (bool, error) {
	if outcome.status != execution.LegStatusFilled || !outcome.quantity.IsPositive() {
		return false, nil
	}
	for _, leg := range exec.Legs {
		if leg.GatewayID != gatewayID {
			continue
		}
		if leg.Status != execution.LegStatusPartial ||
			!leg.Filled.IsPositive() ||
			!leg.Filled.Equal(outcome.quantity) {
			return false, nil
		}
		if err := exec.Apply(execution.LegFilled(gatewayID, leg.Filled, service.clock.Now())); err != nil {
			return false, safeError(err)
		}
		var persistErr error
		*version, *result, persistErr = service.persistIntoResult(ctx, scope, *exec, *version, kind, requestID, *result)
		if persistErr != nil {
			return false, persistErr
		}
		return true, nil
	}
	return false, nil
}

func (service *Service) persistIntoResult(ctx context.Context, scope Scope, exec execution.Execution, version int64, kind, requestID string, result Result) (int64, Result, error) {
	next, committed, err := service.persist(ctx, scope, exec, version, kind, requestID, nil)
	result = mergePublishResult(result, committed)
	result.Execution = exec.Snapshot()
	return next, result, err
}

func (service *Service) persist(ctx context.Context, scope Scope, exec execution.Execution, version int64, kind, requestID string, payload json.RawMessage) (int64, publishResult, error) {
	events, err := service.store.PersistTransition(ctx, PersistTransitionInput{
		Scope: scope, Execution: exec.Snapshot(), ExpectedVersion: version,
		Kind: kind, RequestID: requestID, Payload: payload, OccurredAt: service.clock.Now(),
	})
	if err != nil {
		return version, publishResult{}, mapStoreError(err)
	}
	result := publishResult{}
	if len(events) > 0 && service.publisher != nil {
		if err := service.publisher.Publish(ctx, events); err != nil {
			result.degraded = true
			result.recoverable = true
			service.markDegraded("event publishing degraded")
		}
	}
	for _, event := range events {
		result.publishedIDs = append(result.publishedIDs, event.ID)
	}
	return version + 1, result, nil
}

func (service *Service) requireReady() error {
	service.mu.RLock()
	defer service.mu.RUnlock()
	if service.recovering {
		return ErrRecoveryInProgress
	}
	if !service.ready {
		return ErrNotReady
	}
	return nil
}

func (service *Service) startRecovery() (bool, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.recovering {
		return false, ErrRecoveryInProgress
	}
	needsAuthoritativeRepair := !service.ready || service.degraded
	service.recovering = true
	service.ready = false
	service.reason = "recovery in progress"
	return needsAuthoritativeRepair, nil
}

func (service *Service) finishRecovery(clearDegraded bool) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.recovering = false
	service.ready = true
	if clearDegraded {
		service.degraded = false
	}
	if service.reason == "recovery in progress" || clearDegraded {
		service.reason = ""
	}
}

func (service *Service) markRecoveryFailed(reason string) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.recovering = false
	service.ready = false
	service.degraded = true
	service.reason = reason
}

func (service *Service) markDegraded(reason string) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.degraded = true
	service.reason = reason
}

type legOutcome struct {
	role         execution.LegRole
	gatewayID    string
	orderID      string
	outcome      GatewayOutcome
	quantity     decimal.Decimal
	filled       bool
	status       execution.LegStatus
	err          execution.ErrorInfo
	fills        []FillObservation
	compensation bool
}

type publishResult struct {
	degraded     bool
	recoverable  bool
	publishedIDs []string
}

func normalizePreviewInput(input execution.PreviewInput, scope Scope, request PreviewRequest, now time.Time) (execution.PreviewInput, error) {
	if input.PrincipalID != "" && input.PrincipalID != scope.PrincipalID {
		return execution.PreviewInput{}, ErrPreviewMismatch
	}
	if input.Mode != "" && input.Mode != scope.Mode {
		return execution.PreviewInput{}, ErrPreviewMismatch
	}
	if input.PreviewID != "" && input.PreviewID != request.PreviewID {
		return execution.PreviewInput{}, ErrPreviewMismatch
	}
	if input.Operation != "" && request.Operation != "" && input.Operation != request.Operation {
		return execution.PreviewInput{}, ErrPreviewMismatch
	}
	if input.OpportunityID != "" && request.OpportunityID != "" && input.OpportunityID != request.OpportunityID {
		return execution.PreviewInput{}, ErrPreviewMismatch
	}
	if input.PositionID != "" && request.PositionID != "" && input.PositionID != request.PositionID {
		return execution.PreviewInput{}, ErrPreviewMismatch
	}
	if request.CapitalUSDT.IsPositive() && input.CapitalUSDT.IsPositive() && !input.CapitalUSDT.Equal(request.CapitalUSDT) {
		return execution.PreviewInput{}, ErrPreviewMismatch
	}
	if request.Leverage != 0 && input.Leverage != 0 && input.Leverage != request.Leverage {
		return execution.PreviewInput{}, ErrPreviewMismatch
	}
	input.PrincipalID = scope.PrincipalID
	input.Mode = scope.Mode
	input.PreviewID = request.PreviewID
	input.Operation = request.Operation
	input.OpportunityID = request.OpportunityID
	input.PositionID = request.PositionID
	if request.CapitalUSDT.IsPositive() {
		input.CapitalUSDT = request.CapitalUSDT
	}
	if request.Leverage != 0 {
		input.Leverage = request.Leverage
	}
	if input.PreviewedAt.IsZero() {
		input.PreviewedAt = now.UTC()
	}
	return input, nil
}

func validateStoredPreview(stored StoredPreview, fresh execution.PreviewResult) error {
	if stored.Expired {
		return ErrPreviewExpired
	}
	storedHash, err := stablePreviewHash(stored.Preview)
	if err != nil {
		return err
	}
	if stored.RequestHash != storedHash {
		return ErrPreviewMismatch
	}
	if !stored.Preview.CanSubmit || stored.Preview.Blocked() || fresh.Blocked() {
		return ErrBlockedPreview
	}
	if !samePreviewIntent(stored.Preview, fresh) ||
		!freshWarningsWerePreviouslyShown(stored.Preview, fresh) ||
		!markDriftWithinPolicy(stored.Preview, fresh) {
		return ErrPreviewMismatch
	}
	return nil
}

func samePreviewIntent(stored, fresh execution.PreviewResult) bool {
	if stored.PreviewID != fresh.PreviewID ||
		stored.PrincipalID != fresh.PrincipalID ||
		stored.Mode != fresh.Mode ||
		stored.Operation != fresh.Operation ||
		stored.OpportunityID != fresh.OpportunityID ||
		stored.PositionID != fresh.PositionID ||
		stored.ParentExecutionID != fresh.ParentExecutionID ||
		stored.LineageID != fresh.LineageID ||
		!stored.CapitalUSDT.Equal(fresh.CapitalUSDT) ||
		stored.Leverage != fresh.Leverage ||
		!stored.MaxSlippagePct.Equal(fresh.MaxSlippagePct) ||
		len(stored.PlannedLegs) != len(fresh.PlannedLegs) ||
		len(stored.PlannedLegs) != 2 {
		return false
	}
	for index := range stored.PlannedLegs {
		if !samePlannedLegIntent(stored.PlannedLegs[index], fresh.PlannedLegs[index]) {
			return false
		}
	}
	return true
}

func samePlannedLegIntent(stored, fresh execution.PlannedLeg) bool {
	return stored.Role == fresh.Role &&
		stored.AccountID == fresh.AccountID &&
		stored.Venue == fresh.Venue &&
		stored.VenueEnvironment == fresh.VenueEnvironment &&
		stored.InstrumentID == fresh.InstrumentID &&
		stored.GatewayInstrumentID == fresh.GatewayInstrumentID &&
		stored.Symbol == fresh.Symbol &&
		stored.Side == fresh.Side &&
		stored.ReduceOnly == fresh.ReduceOnly &&
		stored.Quantity.Equal(fresh.Quantity)
}

func freshWarningsWerePreviouslyShown(stored, fresh execution.PreviewResult) bool {
	storedCodes := make(map[execution.ReasonCode]struct{}, len(stored.Warnings))
	for _, warning := range stored.Warnings {
		storedCodes[warning.Code] = struct{}{}
	}
	for _, warning := range fresh.Warnings {
		if _, ok := storedCodes[warning.Code]; !ok {
			return false
		}
	}
	return true
}

func markDriftWithinPolicy(stored, fresh execution.PreviewResult) bool {
	for index := range stored.PlannedLegs {
		storedLeg := stored.PlannedLegs[index]
		freshLeg := fresh.PlannedLegs[index]
		if !storedLeg.MarkPrice.IsPositive() || !freshLeg.MarkPrice.IsPositive() {
			return false
		}
		adverse := decimal.Zero
		switch storedLeg.Side {
		case execution.SideBuy:
			adverse = freshLeg.MarkPrice.Sub(storedLeg.MarkPrice)
		case execution.SideSell:
			adverse = storedLeg.MarkPrice.Sub(freshLeg.MarkPrice)
		default:
			return false
		}
		if !adverse.IsPositive() {
			continue
		}
		adversePct := adverse.Div(storedLeg.MarkPrice).Mul(decimal.NewFromInt(100))
		if adversePct.GreaterThan(stored.MaxSlippagePct) {
			return false
		}
	}
	return true
}

func previewHash(preview execution.PreviewResult) (string, error) {
	body, err := json.Marshal(preview)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func stablePreviewHash(preview execution.PreviewResult) (string, error) {
	preview.PreviewedAt = time.Time{}
	preview.ExpiresAt = time.Time{}
	return previewHash(preview)
}

func browserRequestHash(scope Scope, method string, path string, request PreviewRequest) (string, error) {
	body, err := json.Marshal(struct {
		Method  string         `json:"method"`
		Path    string         `json:"path"`
		Scope   Scope          `json:"scope"`
		Request PreviewRequest `json:"request"`
	}{Method: method, Path: path, Scope: scope, Request: request})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func automaticClosePreviewID(scope Scope, path, idempotencyKey string) string {
	body := strings.Join([]string{
		scope.PrincipalID,
		scope.Mode,
		submitMethod,
		path,
		idempotencyKey,
	}, "\x00")
	sum := sha256.Sum256([]byte(body))
	return "close-preview-" + hex.EncodeToString(sum[:16])
}

func closePath(positionID string) string {
	return "/api/v1/positions/" + url.PathEscape(positionID) + "/close"
}

func validateScope(scope Scope) error {
	if strings.TrimSpace(scope.PrincipalID) == "" ||
		(scope.Mode != "test" && scope.Mode != "live") {
		return ErrInvalidScope
	}
	return nil
}

func idempotencyResult(exec execution.Execution, record IdempotencyRecord) (Result, error) {
	if record.State == "completed" && record.ResponseCode >= 100 && len(record.ResponseBody) > 0 && json.Valid(record.ResponseBody) {
		return Result{Execution: exec.Snapshot(), Replayed: true, ResponseCode: record.ResponseCode, ResponseBody: record.ResponseBody}, nil
	}
	if recoveryTerminal(exec) {
		return terminalCompletionResult(exec, true), nil
	}
	return Result{
		Execution: exec.Snapshot(), Replayed: true, Recoverable: true,
		SafeError: execution.ErrorInfo{Code: "EXECUTION_IN_PROGRESS", SafeMessage: "execution is already in progress", Retryable: true},
	}, ErrExecutionInProgress
}

func reservationResult(reservation IdempotencyReservation) (Result, error) {
	return idempotencyResult(reservation.Execution, IdempotencyRecord{
		ExecutionID: reservation.Execution.ID, RequestHash: "", State: reservation.State,
		ResponseCode: reservation.ResponseCode, ResponseBody: reservation.ResponseBody,
	})
}

func (service *Service) terminalReplayResult(ctx context.Context, scope Scope, executionID string, replayed bool) (Result, error) {
	stored, err := service.store.GetExecution(ctx, scope, executionID)
	if err != nil {
		return Result{}, mapStoreError(err)
	}
	if !recoveryTerminal(stored.Execution) {
		return Result{}, ErrExecutionInProgress
	}
	return service.completeLinkedTerminal(ctx, scope, stored.Execution, replayed), nil
}

func terminalCompletionResult(exec execution.Execution, replayed bool) Result {
	body, _ := json.Marshal(exec.Snapshot())
	return Result{
		Execution:    exec.Snapshot(),
		Replayed:     replayed,
		Degraded:     true,
		Recoverable:  true,
		SafeError:    execution.ErrorInfo{Code: "IDEMPOTENCY_COMPLETION_FAILED", SafeMessage: "execution response persistence failed", Retryable: true},
		ResponseCode: 202,
		ResponseBody: body,
	}
}

func (service *Service) completeLinkedTerminal(ctx context.Context, scope Scope, exec execution.Execution, replayed bool) Result {
	result, _ := service.completeLinkedTerminalWithError(ctx, scope, exec, replayed)
	return result
}

func (service *Service) completeLinkedTerminalWithError(ctx context.Context, scope Scope, exec execution.Execution, replayed bool) (Result, error) {
	result := terminalCompletionResult(exec, replayed)
	if len(result.ResponseBody) == 0 || !json.Valid(result.ResponseBody) {
		return result, errors.New("terminal execution response is invalid")
	}
	if err := service.store.CompleteLinkedIdempotency(ctx, scope, exec.ID, result.ResponseCode, result.ResponseBody); err != nil {
		result.SafeError = execution.ErrorInfo{Code: "IDEMPOTENCY_COMPLETION_FAILED", SafeMessage: "execution response persistence failed", Retryable: true}
		result.Degraded = true
		result.Recoverable = true
		return result, err
	}
	return result, nil
}

func (service *Service) mergeTerminalRepair(ctx context.Context, scope Scope, exec execution.Execution, result Result) Result {
	repaired, repairErr := service.completeLinkedTerminalWithError(ctx, scope, exec, false)
	if result.ResponseCode == 0 {
		result.ResponseCode = repaired.ResponseCode
	}
	if len(result.ResponseBody) == 0 {
		result.ResponseBody = repaired.ResponseBody
	}
	if repairErr != nil {
		if repaired.Degraded {
			result.Degraded = true
		}
		if repaired.Recoverable {
			result.Recoverable = true
		}
		if result.SafeError.Code == "" {
			result.SafeError = repaired.SafeError
		}
	}
	return result
}

func gatewayIdentity(executionID string, role execution.LegRole) string {
	return executionID + ":" + string(role)
}

func plannedLeg(exec *execution.Execution, role execution.LegRole) (execution.PlannedLeg, bool) {
	for _, leg := range exec.PlannedLegs {
		if leg.Role == role {
			return leg, true
		}
	}
	return execution.PlannedLeg{}, false
}

func legFilled(exec *execution.Execution, role execution.LegRole) bool {
	for _, leg := range exec.Legs {
		if leg.Role == role {
			return leg.Status == execution.LegStatusFilled
		}
	}
	return false
}

func hasLeg(exec *execution.Execution, role execution.LegRole) bool {
	for _, leg := range exec.Legs {
		if leg.Role == role {
			return true
		}
	}
	return false
}

func recoveryTerminal(exec execution.Execution) bool {
	if exec.ManualIntervention {
		return false
	}
	switch exec.State {
	case execution.StateHedged, execution.StateClosed, execution.StateFailed:
		return true
	case execution.StateManualInterventionRequired:
		return false
	default:
		return false
	}
}

func legNeedsCancel(exec *execution.Execution, role execution.LegRole) bool {
	for _, leg := range exec.Legs {
		if leg.Role != role {
			continue
		}
		return leg.Status == execution.LegStatusWorking || leg.Status == execution.LegStatusPartial
	}
	return false
}

func compensationTarget(exec *execution.Execution) (execution.Leg, bool) {
	for _, leg := range exec.Legs {
		if leg.Role != execution.LegRoleCompensation && leg.Filled.IsPositive() {
			return leg, true
		}
	}
	return execution.Leg{}, false
}

func legOutcomeFromObservation(role execution.LegRole, plan execution.PlannedLeg, observed OrderObservation, fills []FillObservation, compensation bool) legOutcome {
	quantity := cumulativeFill(fills)
	if observed.Status == execution.LegStatusRejected {
		return legOutcome{role: role, gatewayID: observed.GatewayID, orderID: observed.OrderID, outcome: GatewayRejected, quantity: quantity, status: observed.Status, err: observed.Error, compensation: compensation}
	}
	if observed.Status == execution.LegStatusPartial && len(fills) == 0 {
		return legOutcome{
			role: role, gatewayID: observed.GatewayID, orderID: observed.OrderID,
			outcome: GatewayAmbiguous, status: observed.Status,
			err:          execution.ErrorInfo{Code: "PARTIAL_FILL_UNOBSERVABLE", SafeMessage: "partial fill quantity is unavailable", Retryable: true},
			compensation: compensation,
		}
	}
	filled := observed.Status == execution.LegStatusFilled
	if observed.Status == execution.LegStatusPartial {
		filled = false
	}
	if filled && quantity.IsPositive() && quantity.LessThan(plan.Quantity) {
		return legOutcome{
			role: role, gatewayID: observed.GatewayID, orderID: observed.OrderID,
			outcome: GatewayAmbiguous, status: observed.Status,
			err:          execution.ErrorInfo{Code: "FILLED_QUANTITY_UNOBSERVABLE", SafeMessage: "filled order quantity is incomplete", Retryable: true},
			compensation: compensation,
		}
	}
	if filled && len(fills) == 0 && quantity.IsZero() {
		quantity = plan.Quantity
	}
	return legOutcome{
		role: role, gatewayID: observed.GatewayID, orderID: observed.OrderID,
		outcome: GatewayAccepted, quantity: quantity, filled: filled,
		status: observed.Status,
		fills:  fills, compensation: compensation,
	}
}

func orderLookupID(gatewayID, orderID string) string {
	if strings.TrimSpace(orderID) != "" {
		return orderID
	}
	return gatewayID
}

func observedOrderID(fallback string, observed OrderObservation) string {
	if observed.Found && strings.TrimSpace(observed.OrderID) != "" {
		return observed.OrderID
	}
	return fallback
}

func legOrderLookupID(leg execution.Leg) string {
	if strings.TrimSpace(leg.GatewayOrderID) != "" {
		return leg.GatewayOrderID
	}
	return orderLookupID(leg.GatewayID, leg.OrderID)
}

func cancelOrderLookupID(exec *execution.Execution, gatewayID string) string {
	if exec != nil {
		for _, leg := range exec.Legs {
			if leg.GatewayID == gatewayID {
				return legOrderLookupID(leg)
			}
		}
	}
	return gatewayID
}

func cumulativeFill(fills []FillObservation) decimal.Decimal {
	total := decimal.Zero
	for _, fill := range fills {
		if fill.Quantity.IsPositive() {
			total = total.Add(fill.Quantity)
		}
	}
	return total
}

func safeGatewayError(info execution.ErrorInfo) execution.ErrorInfo {
	if info.Code == "" && info.SafeMessage == "" {
		return execution.ErrorInfo{Code: "GATEWAY_ERROR", SafeMessage: "gateway error"}
	}
	if info.SafeMessage == "" {
		info.SafeMessage = "gateway error"
	}
	return info
}

func fillEvents(outcome legOutcome, at time.Time) []execution.Event {
	return fillEventsAfter(outcome, decimal.Zero, at)
}

func fillEventsAfter(outcome legOutcome, alreadyFilled decimal.Decimal, at time.Time) []execution.Event {
	if len(outcome.fills) == 0 {
		if outcome.filled {
			return []execution.Event{execution.LegFilled(outcome.gatewayID, outcome.quantity, at)}
		}
		return []execution.Event{execution.LegPartial(outcome.gatewayID, outcome.quantity, at)}
	}
	events := make([]execution.Event, 0, len(outcome.fills))
	cumulative := decimal.Zero
	for index, fill := range outcome.fills {
		cumulative = cumulative.Add(fill.Quantity)
		if !cumulative.GreaterThan(alreadyFilled) {
			continue
		}
		eventAt := fill.OccurredAt
		if eventAt.IsZero() {
			eventAt = at
		}
		fillQuantity := fill.Quantity
		prior := cumulative.Sub(fill.Quantity)
		if prior.LessThan(alreadyFilled) {
			fillQuantity = cumulative.Sub(alreadyFilled)
		}
		if outcome.filled && index == len(outcome.fills)-1 {
			events = append(events, execution.LegFilledDeltaDetail(
				fill.FillID, outcome.gatewayID, fill.GatewayFillID, cumulative,
				fillQuantity, fill.Price, fill.Fee, eventAt,
			))
			continue
		}
		events = append(events, execution.LegPartialFillDetail(
			fill.FillID, outcome.gatewayID, fill.GatewayFillID, cumulative,
			fillQuantity, fill.Price, fill.Fee, eventAt,
		))
	}
	return events
}

func mapStoreError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrIdempotencyConflict):
		return ErrIdempotencyConflict
	case errors.Is(err, ErrNotFound):
		return ErrNotFound
	default:
		return safeError(err)
	}
}

func safeError(err error) error {
	if err == nil {
		return nil
	}
	for _, sentinel := range []error{
		ErrNotReady, ErrPreviewConflict, ErrPreviewExpired, ErrPreviewMismatch,
		ErrBlockedPreview, ErrIdempotencyConflict, ErrNotFound, ErrRecoveryInProgress,
		ErrExecutionInProgress,
	} {
		if errors.Is(err, sentinel) {
			return fmt.Errorf("%w", sentinel)
		}
	}
	return ErrServiceUnavailable
}

func transitionKind(base string, compensation bool) string {
	if compensation {
		return "compensation_" + base
	}
	return base
}

func opposite(side execution.Side) execution.Side {
	if side == execution.SideBuy {
		return execution.SideSell
	}
	return execution.SideBuy
}

func mergePublishResult(result Result, published publishResult) Result {
	result.Degraded = result.Degraded || published.degraded
	result.Recoverable = result.Recoverable || published.recoverable
	result.PublishedIDs = append(result.PublishedIDs, published.publishedIDs...)
	return result
}

func normalizedVersion(version int64) int64 {
	if version < 1 {
		return 1
	}
	return version
}

func first(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

func firstOperation(value, fallback execution.Operation) execution.Operation {
	if value != "" {
		return value
	}
	return fallback
}

func readinessReason(ready bool) string {
	if ready {
		return ""
	}
	return "recovery required"
}

type systemClock struct{}

func (systemClock) Now() time.Time {
	return time.Now().UTC()
}

type deterministicIDs struct{}

func (deterministicIDs) NewExecutionID(scope Scope, key string, preview execution.PreviewResult) string {
	body, _ := json.Marshal(execution.NewCommandPayload("", preview, ""))
	sum := sha256.Sum256([]byte(scope.PrincipalID + "\x00" + scope.Mode + "\x00" + key + "\x00" + string(body)))
	return "exec_" + hex.EncodeToString(sum[:8])
}
