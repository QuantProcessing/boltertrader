package orders

import (
	"context"
	"testing"
	"time"
)

func TestAccountWakeupPreservesHandoffWindowsAndOtherAccounts(t *testing.T) {
	now := time.Now().UTC()
	store := NewMemoryStore()
	service := newLiveService(store, func() time.Time { return now })
	create := func(account, key string) Command {
		c, _, err := service.Create(t.Context(), CreateRequest{PrincipalID: "p", AccountID: account, ProductID: "manual", LineageID: key, IdempotencyKey: key, Method: "POST", Path: "/commands", CommandKind: "manual.place", Payload: []byte(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	waiting := create("a", "waiting")
	other := create("b", "other")
	handoff := create("a", "handoff")
	for _, c := range []Command{waiting, other} {
		if _, err := service.Transition(t.Context(), TransitionRequest{CommandID: c.CommandID, State: StateSubmitted, Reason: ReconciliationAwaitingTerminal, NextAttemptAt: now.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := service.BeginExecution(t.Context(), handoff.CommandID); err != nil {
		t.Fatal(err)
	}
	if err := service.ExpediteAccount(t.Context(), "a"); err != nil {
		t.Fatal(err)
	}
	tasks, err := service.ClaimDueReconciliation(t.Context(), now, time.Minute, 10)
	if err != nil || len(tasks) != 1 || tasks[0].CommandID != waiting.CommandID {
		t.Fatalf("tasks=%+v err=%v", tasks, err)
	}
}

type wakeupGateway struct {
	ready  chan func(string)
	result HandoffResult
}

func (g *wakeupGateway) WatchAccountActivity(ctx context.Context, notify func(string)) error {
	g.ready <- notify
	<-ctx.Done()
	return ctx.Err()
}
func (g *wakeupGateway) Reconcile(context.Context, Command) (HandoffResult, error) {
	return g.result, nil
}
func TestReconcilerWakesOnVenueEventBeforePollingDeadline(t *testing.T) {
	now := time.Now().UTC()
	service := newLiveService(NewMemoryStore(), func() time.Time { return now })
	c, _, err := service.Create(t.Context(), CreateRequest{PrincipalID: "p", AccountID: "a", ProductID: "manual", LineageID: "l", IdempotencyKey: "i", Method: "POST", Path: "/commands", CommandKind: "manual.place", Payload: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	evidence := ExecutionEvidence{CommandID: "gateway-command", GatewayOrderID: "order", ClientOrderID: "client", State: "submitted", ResourceVersion: 1}
	_, err = service.Transition(t.Context(), TransitionRequest{CommandID: c.CommandID, State: StateSubmitted, Reason: ReconciliationAwaitingTerminal, NextAttemptAt: now.Add(time.Hour), Evidence: evidence})
	if err != nil {
		t.Fatal(err)
	}
	evidence.State = "filled"
	evidence.ResourceVersion = 2
	gateway := &wakeupGateway{ready: make(chan func(string), 1), result: HandoffResult{Outcome: HandoffAccepted, Evidence: evidence}}
	runner, _ := NewReconciler(service, gateway, reconciliationDispatcherFixture{service}, func() time.Time { return now })
	runner.interval = time.Hour
	settled := make(chan Command, 1)
	runner.SetSettlementObserver(func(c Command) { settled <- c })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); runner.Run(ctx) }()
	select {
	case notify := <-gateway.ready:
		notify("a")
	case <-time.After(time.Second):
		t.Fatal("watcher not started")
	}
	select {
	case c := <-settled:
		if c.State != StateFilled {
			t.Fatal(c.State)
		}
	case <-time.After(time.Second):
		t.Fatal("venue event waited for polling")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watcher leaked")
	}
}
