package orders

import (
	"context"
	"errors"
	"testing"
	"time"
)

type reconciliationGatewayFixture struct {
	results []HandoffResult
	errors  []error
	calls   int
}

func (fixture *reconciliationGatewayFixture) Reconcile(context.Context, Command) (HandoffResult, error) {
	index := fixture.calls
	fixture.calls++
	if index < len(fixture.errors) && fixture.errors[index] != nil {
		return HandoffResult{}, fixture.errors[index]
	}
	return fixture.results[index], nil
}

type reconciliationDispatcherFixture struct{ commands *Service }

func (fixture reconciliationDispatcherFixture) RecordReconciliation(ctx context.Context, command Command, handoff HandoffResult) (Command, error) {
	if handoff.Outcome == HandoffRejected {
		return fixture.commands.Transition(ctx, TransitionRequest{CommandID: command.CommandID, State: StateFailed})
	}
	state := StateSubmitted
	switch handoff.Evidence.State {
	case "filled":
		state = StateFilled
	case "canceled":
		state = StateCanceled
	case "rejected":
		state = StateFailed
	}
	return fixture.commands.Transition(ctx, TransitionRequest{CommandID: command.CommandID, State: state, Evidence: handoff.Evidence})
}

func TestReconcilerAppliesAuthoritativeRejectionButRetainsNotAttempted(t *testing.T) {
	for _, test := range []struct {
		name    string
		outcome HandoffOutcome
		want    State
	}{
		{name: "rejected", outcome: HandoffRejected, want: StateFailed},
		{name: "not attempted", outcome: HandoffNotAttempted, want: StateUncertain},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, 8, 6, 1, 0, 0, 0, time.UTC)
			commands := NewService(NewMemoryStore(), nil, func() time.Time { return now })
			command, _, _ := commands.Create(context.Background(), CreateRequest{PrincipalID: "p", AccountID: "a", ProductID: "manual", LineageID: "l", IdempotencyKey: "i", Method: "POST", Path: "/commands", CommandKind: "manual.place", Mode: ModeTest, Payload: []byte(`{}`)})
			_, _ = commands.Transition(context.Background(), TransitionRequest{CommandID: command.CommandID, State: StateUncertain, Reason: ReconciliationAmbiguousHandoff, NextAttemptAt: now.Add(time.Second)})
			now = now.Add(time.Second)
			gateway := &reconciliationGatewayFixture{results: []HandoffResult{{Outcome: test.outcome}}}
			runner, _ := NewReconciler(commands, gateway, reconciliationDispatcherFixture{commands}, ModeTest, func() time.Time { return now })
			_ = runner.RunOnce(context.Background())
			current, _ := commands.Get(context.Background(), command.CommandID)
			if current.State != test.want {
				t.Fatalf("state=%s want=%s", current.State, test.want)
			}
		})
	}
}

func TestReconcilerSurvivesRestartAndAdvancesPartialToTerminalEvidence(t *testing.T) {
	now := time.Date(2026, 8, 6, 1, 0, 0, 0, time.UTC)
	commands := NewService(NewMemoryStore(), nil, func() time.Time { return now })
	command, _, err := commands.Create(context.Background(), CreateRequest{PrincipalID: "p", AccountID: "a", ProductID: "manual", LineageID: "l", IdempotencyKey: "i", Method: "POST", Path: "/commands", CommandKind: "manual.place", Mode: ModeTest, Payload: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	base := ExecutionEvidence{CommandID: "gateway-command", GatewayOrderID: "gateway-order", ClientOrderID: "client-order", State: "submitted", ResourceVersion: 1}
	if _, err := commands.Transition(context.Background(), TransitionRequest{CommandID: command.CommandID, State: StateSubmitted, Evidence: base}); err != nil {
		t.Fatal(err)
	}
	partial := base
	partial.State, partial.ResourceVersion = "partially_filled", 2
	partial.Fills = []FillEvidence{{FillID: "fill-1", AccountID: "a", InstrumentID: "BTC-USDT", Side: "buy", Quantity: "1", Price: "100", Fee: "0", FeeCurrency: "USDT", SettlementCurrency: "USDT", OccurredAt: now}}
	filled := partial
	filled.State, filled.ResourceVersion = "filled", 3
	gateway := &reconciliationGatewayFixture{results: []HandoffResult{{Outcome: HandoffAccepted, Evidence: partial}, {Outcome: HandoffAccepted, Evidence: filled}}}

	now = now.Add(5 * time.Second)
	runner, _ := NewReconciler(commands, gateway, reconciliationDispatcherFixture{commands}, ModeTest, func() time.Time { return now })
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	current, _ := commands.Get(context.Background(), command.CommandID)
	if current.State != StateSubmitted || current.ExecutionEvidence.ResourceVersion != 2 || len(current.ExecutionEvidence.Fills) != 1 {
		t.Fatalf("partial command=%+v", current)
	}

	now = now.Add(5 * time.Second)
	restarted, _ := NewReconciler(commands, gateway, reconciliationDispatcherFixture{commands}, ModeTest, func() time.Time { return now })
	if err := restarted.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	current, _ = commands.Get(context.Background(), command.CommandID)
	if current.State != StateFilled || current.ExecutionEvidence.ResourceVersion != 3 || gateway.calls != 2 {
		t.Fatalf("terminal command=%+v calls=%d", current, gateway.calls)
	}
	if due, _ := commands.ClaimDueReconciliation(context.Background(), ModeTest, now.Add(time.Hour), time.Minute, 10); len(due) != 0 {
		t.Fatalf("terminal reconciliation remained due: %+v", due)
	}
}

func TestReconcilerRetainsLookupErrorsWithBoundedBackoff(t *testing.T) {
	now := time.Date(2026, 8, 6, 1, 0, 0, 0, time.UTC)
	commands := NewService(NewMemoryStore(), nil, func() time.Time { return now })
	command, _, _ := commands.Create(context.Background(), CreateRequest{PrincipalID: "p", AccountID: "a", ProductID: "manual", LineageID: "l", IdempotencyKey: "i", Method: "POST", Path: "/commands", CommandKind: "manual.place", Mode: ModeTest, Payload: []byte(`{}`)})
	base := ExecutionEvidence{CommandID: "gateway-command", GatewayOrderID: "gateway-order", ClientOrderID: "client-order", State: "submitted", ResourceVersion: 1}
	_, _ = commands.Transition(context.Background(), TransitionRequest{CommandID: command.CommandID, State: StateSubmitted, Evidence: base})
	now = now.Add(5 * time.Second)
	gateway := &reconciliationGatewayFixture{errors: []error{errors.New("lookup unavailable")}}
	runner, _ := NewReconciler(commands, gateway, reconciliationDispatcherFixture{commands}, ModeTest, func() time.Time { return now })
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	current, _ := commands.Get(context.Background(), command.CommandID)
	if current.State != StateUncertain {
		t.Fatalf("lookup error command=%+v", current)
	}
	if due, _ := commands.ClaimDueReconciliation(context.Background(), ModeTest, now.Add(4*time.Second), time.Minute, 10); len(due) != 0 {
		t.Fatalf("backoff was not enforced: %+v", due)
	}
	if due, _ := commands.ClaimDueReconciliation(context.Background(), ModeTest, now.Add(5*time.Second), time.Minute, 10); len(due) != 1 || due[0].Reason != ReconciliationLookupFailed {
		t.Fatalf("lookup retry=%+v", due)
	}
}
