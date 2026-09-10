package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/services/trading/orders"
)

func TestPostgresCommandTransitionsMergeEvidenceAndFenceTerminalState(t *testing.T) {
	database, reopen := openIntegrationDatabase(t)
	second := reopen()
	database.SetMaxOpenConns(4)
	second.SetMaxOpenConns(4)
	stores := []*CommandStore{NewCommandStore(database), NewCommandStore(second)}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	now := time.Now().UTC().Truncate(time.Microsecond)
	command := orders.Command{CommandID: "c", PrincipalID: "p", AccountID: "a", ProductID: "manual", LineageID: "l", IdempotencyKey: "k", Method: "POST", Path: "/commands", CommandKind: "manual.place", State: orders.StateAccepted, Payload: json.RawMessage(`{}`), Blockers: []orders.Blocker{}, CreatedAt: now, UpdatedAt: now}
	if _, _, err := stores[0].Create(ctx, command); err != nil {
		t.Fatal(err)
	}
	const writers = 12
	start, done := make(chan struct{}), make(chan error, writers)
	for i := range writers {
		go func() {
			<-start
			_, err := stores[i%len(stores)].Transition(ctx, orders.TransitionRequest{
				CommandID: "c", State: orders.StateSubmitted, Reason: orders.ReconciliationAwaitingTerminal, NextAttemptAt: now.Add(time.Minute),
				Evidence: orders.ExecutionEvidence{CommandID: "gc", GatewayOrderID: "go", ClientOrderID: "1", State: "open", ResourceVersion: 2, Fills: []orders.FillEvidence{{FillID: fmt.Sprintf("f-%d", i)}}},
			}, now)
			done <- err
		}()
	}
	close(start)
	for range writers {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	store := NewCommandStore(reopen())
	current, err := store.Get(ctx, "c")
	if err != nil || len(current.ExecutionEvidence.Fills) != writers {
		t.Fatalf("persisted command=%+v err=%v", current, err)
	}
	if _, err := store.Transition(ctx, orders.TransitionRequest{CommandID: "c", State: orders.StateFilled}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := stores[1].Transition(ctx, orders.TransitionRequest{CommandID: "c", State: orders.StateSubmitted, Reason: orders.ReconciliationAwaitingTerminal, NextAttemptAt: now.Add(time.Minute)}, now); !errors.Is(err, orders.ErrInvalidTransition) {
		t.Fatalf("terminal regression err=%v", err)
	}
	current, err = store.Get(ctx, "c")
	if err != nil || current.State != orders.StateFilled || len(current.ExecutionEvidence.Fills) != writers {
		t.Fatalf("terminal command=%+v err=%v", current, err)
	}
	tasks, err := store.ClaimDue(ctx, now.Add(time.Hour), now.Add(2*time.Hour), 10)
	if err != nil || len(tasks) != 0 {
		t.Fatalf("terminal tasks=%+v err=%v", tasks, err)
	}
}
