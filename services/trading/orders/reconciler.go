package orders

import (
	"context"
	"fmt"
	"time"
)

type ReconciliationGateway interface {
	Reconcile(context.Context, Command) (HandoffResult, error)
}

type ReconciliationDispatcher interface {
	RecordReconciliation(context.Context, Command, HandoffResult) (Command, error)
}

type Reconciler struct {
	commands   *Service
	gateway    ReconciliationGateway
	dispatcher ReconciliationDispatcher
	now        func() time.Time
	interval   time.Duration
	lease      time.Duration
	onSettled  func(Command)
}

func NewReconciler(commands *Service, gateway ReconciliationGateway, dispatcher ReconciliationDispatcher, now func() time.Time) (*Reconciler, error) {
	if commands == nil || gateway == nil || dispatcher == nil {
		return nil, fmt.Errorf("reconciliation dependencies are required")
	}
	if now == nil {
		now = time.Now
	}
	return &Reconciler{commands: commands, gateway: gateway, dispatcher: dispatcher, now: now, interval: time.Second, lease: 15 * time.Second}, nil
}

// SetSettlementObserver registers a callback invoked whenever reconciliation
// carries a command into a terminal state. The reconciler, not the HTTP
// handler, is what learns a venue's final answer, so without this the audit
// trail stops at "submitted" and never records that the order filled or
// cancelled. The callback runs inline and must not block.
func (runner *Reconciler) SetSettlementObserver(observer func(Command)) {
	runner.onSettled = observer
}

func terminalState(state State) bool {
	return state == StateFilled || state == StateCanceled || state == StateFailed
}

func (runner *Reconciler) Run(ctx context.Context) {
	ticker := time.NewTicker(runner.interval)
	defer ticker.Stop()
	for {
		if err := runner.RunOnce(ctx); err != nil && ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (runner *Reconciler) RunOnce(ctx context.Context) error {
	now := runner.now().UTC()
	tasks, err := runner.commands.ClaimDueReconciliation(ctx, now, runner.lease, 20)
	if err != nil {
		return err
	}
	for _, task := range tasks {
		if err := runner.reconcile(ctx, task); err != nil && ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return nil
}

func (runner *Reconciler) reconcile(ctx context.Context, task ReconciliationTask) error {
	command, err := runner.commands.Get(ctx, task.CommandID)
	if err != nil {
		return err
	}
	handoff, lookupErr := runner.gateway.Reconcile(ctx, command)
	if lookupErr != nil || (handoff.Outcome != HandoffAccepted && handoff.Outcome != HandoffRejected) {
		_, err = runner.commands.Transition(ctx, TransitionRequest{
			CommandID: command.CommandID, State: StateUncertain, Reason: ReconciliationLookupFailed,
			NextAttemptAt: runner.now().UTC().Add(reconciliationBackoff(task.AttemptCount)), Evidence: handoff.Evidence,
		})
		return err
	}
	settled, err := runner.dispatcher.RecordReconciliation(ctx, command, handoff)
	if err != nil {
		_, _ = runner.commands.Transition(ctx, TransitionRequest{
			CommandID: command.CommandID, State: StateUncertain, Reason: ReconciliationEvidenceInvalid,
			NextAttemptAt: runner.now().UTC().Add(reconciliationBackoff(task.AttemptCount)), Evidence: command.ExecutionEvidence,
		})
		return err
	}
	if runner.onSettled != nil && terminalState(settled.State) {
		runner.onSettled(settled)
	}
	return nil
}

func reconciliationBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	backoff := 5 * time.Second
	for current := 1; current < attempt && backoff < time.Minute; current++ {
		backoff *= 2
	}
	if backoff > time.Minute {
		return time.Minute
	}
	return backoff
}
