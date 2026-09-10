package orders

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
	wakeups := make(chan string, 64)
	watchDone := make(chan struct{})
	if watcher, ok := runner.gateway.(interface {
		WatchAccountActivity(context.Context, func(string)) error
	}); ok {
		go func() {
			defer close(watchDone)
			for ctx.Err() == nil {
				_ = watcher.WatchAccountActivity(ctx, func(account string) {
					select {
					case wakeups <- account:
					default:
					}
				})
				timer := time.NewTimer(time.Second)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
		}()
	} else {
		close(watchDone)
	}
	defer func() { <-watchDone }()

	ticker := time.NewTicker(runner.interval)
	defer ticker.Stop()
	for {
		if err := runner.RunOnce(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Error("command reconciliation failed", "cause", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case account := <-wakeups:
			if err := runner.commands.ExpediteAccount(ctx, account); err != nil && ctx.Err() == nil {
				slog.Error("account reconciliation wakeup failed", "cause", err)
			}
		}
	}
}

func (runner *Reconciler) RunOnce(ctx context.Context) error {
	var failures []error
	// Claim only the task we can work on now. Leasing a batch up front lets
	// slow reads consume the leases of tasks that have not even started.
	for range 20 {
		now := runner.now().UTC()
		tasks, err := runner.commands.ClaimDueReconciliation(ctx, now, runner.lease, 1)
		if err != nil {
			return errors.Join(append(failures, err)...)
		}
		if len(tasks) == 0 {
			break
		}
		if err := runner.reconcile(ctx, tasks[0]); err != nil {
			failures = append(failures, err)
			if ctx.Err() != nil {
				break
			}
		}
	}

	return errors.Join(failures...)
}

func (runner *Reconciler) reconcile(ctx context.Context, task ReconciliationTask) error {
	command, err := runner.commands.Get(ctx, task.CommandID)
	if err != nil {
		return err
	}
	if terminalState(command.State) {
		return nil
	}
	if task.Reason == ReconciliationAwaitingHandoff {
		if command.State != StateAccepted {
			return nil // the sender replaced this stale task with its own schedule
		}
		settled, err := runner.commands.Transition(ctx, TransitionRequest{CommandID: command.CommandID,
			ExpectedState: StateAccepted, State: StateFailed, Blockers: []Blocker{{Code: HandoffNotStarted}}})
		if errors.Is(err, ErrInvalidTransition) {
			return nil
		} // a sender won the race
		if err == nil && runner.onSettled != nil {
			runner.onSettled(settled)
		}
		return err
	}
	lookupCtx, cancel := context.WithTimeout(ctx, runner.lease/2)
	handoff, lookupErr := runner.gateway.Reconcile(lookupCtx, command)
	cancel()
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
