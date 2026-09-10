package postgres

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/services/trading/orders"
)

func TestPostgresAccountWakeupPreservesOwnershipHandoffAndLeases(t *testing.T) {
	database, _ := openIntegrationDatabase(t)
	store := NewCommandStore(database)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Microsecond)
	future := now.Add(time.Hour)
	for _, item := range []struct {
		id, account string
		reason      orders.ReconciliationReason
	}{
		{"waiting", "a", orders.ReconciliationAwaitingTerminal},
		{"missing-fill", "a", orders.ReconciliationEvidenceInvalid},
		{"other", "b", orders.ReconciliationAwaitingTerminal},
		{"handoff", "a", orders.ReconciliationAmbiguousHandoff},
		{"leased", "a", orders.ReconciliationAwaitingTerminal},
	} {
		command := orders.Command{CommandID: item.id, PrincipalID: "p", AccountID: item.account, ProductID: "manual", LineageID: item.id, IdempotencyKey: item.id, Method: "POST", Path: "/commands", CommandKind: "manual.place", State: orders.StateAccepted, Payload: json.RawMessage(`{}`), Blockers: []orders.Blocker{}, CreatedAt: now, UpdatedAt: now}
		if _, _, err := store.Create(ctx, command); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Transition(ctx, orders.TransitionRequest{CommandID: item.id, State: orders.StateSubmitted, Reason: item.reason, NextAttemptAt: future}, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.ExecContext(ctx, `UPDATE reconciliation_tasks SET lease_until=$1 WHERE command_id='leased'`, future); err != nil {
		t.Fatal(err)
	}
	if err := store.ExpediteAccount(ctx, "a", now); err != nil {
		t.Fatal(err)
	}
	tasks, err := store.ClaimDue(ctx, now, now.Add(time.Minute), 10)
	if err != nil {
		t.Fatal(err)
	}
	claimed := map[string]bool{}
	for _, task := range tasks {
		claimed[task.CommandID] = true
	}
	if len(tasks) != 2 || !claimed["waiting"] || !claimed["missing-fill"] {
		t.Fatalf("unexpected due tasks: %+v", tasks)
	}
	for _, id := range []string{"other", "handoff"} {
		var next time.Time
		if err := database.QueryRowContext(ctx, `SELECT next_attempt_at FROM reconciliation_tasks WHERE command_id=$1`, id).Scan(&next); err != nil || !next.Equal(future) {
			t.Fatalf("%s window changed: %s, err=%v", id, next, err)
		}
	}
	var lease time.Time
	if err := database.QueryRowContext(ctx, `SELECT lease_until FROM reconciliation_tasks WHERE command_id='leased'`).Scan(&lease); err != nil || !lease.Equal(future) {
		t.Fatalf("lease changed: %s, err=%v", lease, err)
	}
}
