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
	mode       Mode
	now        func() time.Time
	interval   time.Duration
	lease      time.Duration
}

func NewReconciler(commands *Service, gateway ReconciliationGateway, dispatcher ReconciliationDispatcher, mode Mode, now func() time.Time) (*Reconciler, error) {
	if commands == nil || gateway == nil || dispatcher == nil || (mode != ModeTest && mode != ModeLive) {
		return nil, fmt.Errorf("reconciliation dependencies are required")
	}
	if now == nil {
		now = time.Now
	}
	return &Reconciler{commands: commands, gateway: gateway, dispatcher: dispatcher, mode: mode, now: now, interval: time.Second, lease: 15 * time.Second}, nil
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
	tasks, err := runner.commands.ClaimDueReconciliation(ctx, runner.mode, now, runner.lease, 20)
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
	_, err = runner.dispatcher.RecordReconciliation(ctx, command, handoff)
	if err != nil {
		_, _ = runner.commands.Transition(ctx, TransitionRequest{
			CommandID: command.CommandID, State: StateUncertain, Reason: ReconciliationEvidenceInvalid,
			NextAttemptAt: runner.now().UTC().Add(reconciliationBackoff(task.AttemptCount)), Evidence: command.ExecutionEvidence,
		})
	}
	return err
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
