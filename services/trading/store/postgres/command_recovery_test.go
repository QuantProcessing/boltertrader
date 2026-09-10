package postgres

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/services/trading/orders"
)

func recoveryCommand(now time.Time) orders.Command {
	return orders.Command{CommandID: "c", PrincipalID: "p", AccountID: "a", ProductID: "manual", LineageID: "l", IdempotencyKey: "k", Method: "POST", Path: "/commands", CommandKind: "manual.place", State: orders.StateAccepted, Payload: []byte(`{}`), Blockers: []orders.Blocker{}, CreatedAt: now, UpdatedAt: now}
}

func TestPostgresCommandRecoveryIsAtomicAndHasOneSender(t *testing.T) {
	database, reopen := openIntegrationDatabase(t)
	database.SetMaxOpenConns(4)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	store := NewCommandStore(database)
	command := recoveryCommand(now)
	if _, _, err := store.Create(ctx, command); err != nil {
		t.Fatal(err)
	}
	// Initial recovery exists even if the service dies before BeginExecution.
	var reason string
	if err := database.QueryRow(`SELECT reason FROM reconciliation_tasks WHERE command_id='c'`).Scan(&reason); err != nil || reason != string(orders.ReconciliationAwaitingHandoff) {
		t.Fatalf("reason=%s err=%v", reason, err)
	}
	second := reopen()
	second.SetMaxOpenConns(4)
	services := []*orders.Service{orders.NewService(store, func() time.Time { return now }), orders.NewService(NewCommandStore(second), func() time.Time { return now })}
	var wg sync.WaitGroup
	winners := make(chan bool, 12)
	for i := range 12 {
		wg.Go(func() {
			_, won, err := services[i%2].BeginExecution(ctx, command.CommandID)
			if err != nil {
				t.Error(err)
			}
			winners <- won
		})
	}
	wg.Wait()
	close(winners)
	count := 0
	for won := range winners {
		if won {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("senders=%d", count)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	restarted := orders.NewService(NewCommandStore(reopen()), func() time.Time { return now.Add(time.Minute) })
	current, won, err := restarted.BeginExecution(ctx, command.CommandID)
	if err != nil || won || current.State != orders.StateUncertain {
		t.Fatalf("restart=%+v won=%v err=%v", current, won, err)
	}
	tasks, err := restarted.ClaimDueReconciliation(ctx, now.Add(time.Minute), time.Minute, 10)
	if err != nil || len(tasks) != 1 || tasks[0].Reason != orders.ReconciliationAmbiguousHandoff {
		t.Fatalf("tasks=%+v err=%v", tasks, err)
	}
}

func TestPostgresRecoveryTaskFailureRollsBackCommand(t *testing.T) {
	database, _ := openIntegrationDatabase(t)
	ctx := context.Background()
	if _, err := database.Exec(`ALTER TABLE reconciliation_tasks ADD CHECK (false)`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := NewCommandStore(database).Create(ctx, recoveryCommand(time.Now())); err == nil {
		t.Fatal("command committed without recovery task")
	}
	var count int
	if err := database.QueryRow(`SELECT count(*) FROM commands`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("commands=%d err=%v", count, err)
	}
}

func TestCommandRecoveryMigrationPreservesLegacyAmbiguity(t *testing.T) {
	database, reopen := openIntegrationDatabaseAtVersion(t, 3)
	ctx := context.Background()
	now := time.Now().UTC()
	store := NewCommandStore(database)
	for i, state := range []orders.State{orders.StateAccepted, orders.StateSubmitted, orders.StateUncertain, orders.StateFilled} {
		command := recoveryCommand(now)
		command.CommandID, command.IdempotencyKey, command.State = fmt.Sprint(i), fmt.Sprint(i), state
		if _, _, err := store.Create(ctx, command); err != nil {
			t.Fatal(err)
		}
	}
	// Model the old binary, which did not create an initial task.
	if _, err := database.Exec(`DELETE FROM reconciliation_tasks`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, database); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, database); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	store = NewCommandStore(reopen())
	legacy, err := store.Get(ctx, "0")
	if err != nil || legacy.State != orders.StateUncertain || len(legacy.Blockers) != 0 {
		t.Fatalf("legacy=%+v err=%v", legacy, err)
	}
	tasks, err := store.ClaimDue(ctx, now.Add(time.Minute), now.Add(2*time.Minute), 10)
	if err != nil || len(tasks) != 3 {
		t.Fatalf("tasks=%+v err=%v", tasks, err)
	}
	for _, task := range tasks {
		if task.Reason == orders.ReconciliationAwaitingHandoff {
			t.Fatalf("legacy dispatch declared unsent: %+v", task)
		}
	}
}
