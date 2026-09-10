package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/gateway/store"
)

func TestAcceptanceHistorySurvivesUncertaintyAndReload(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	db := NewStore(pool)
	registerTestAccount(t, ctx, db)
	request := reserveRequest(t, json.RawMessage(`{"quantity":"0.001"}`))
	reserved, err := db.ReserveCommand(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	ids := domain.NewIdentityGenerator(domain.IdentityGeneratorOptions{})
	nextID := func() domain.EventID {
		t.Helper()
		id, err := ids.EventID()
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	current, err := db.MarkDispatching(ctx, store.MarkDispatchingRequest{CommandID: reserved.CommandID, EventID: nextID()})
	if err != nil || current.AcceptanceObserved {
		t.Fatalf("dispatching=%+v err=%v", current, err)
	}
	for _, state := range []domain.OrderState{domain.StateAccepted, domain.StateOpen, domain.StateUncertain} {
		transition := store.RecordVenueResultRequest{CommandID: current.CommandID, NextState: state, EventID: nextID(), ResourceVersion: current.ResourceVersion}
		if state == domain.StateUncertain {
			transition.Reconciliation = &store.ReconciliationPlan{TaskID: nextID(), Reason: store.ReconciliationEvidenceUnconfirmed, NextAttemptAt: time.Now().Add(-time.Minute)}
		}
		current, err = db.RecordVenueResult(ctx, transition)
		if err != nil || !current.AcceptanceObserved {
			t.Fatalf("transition=%+v err=%v", current, err)
		}
	}
	pool.Reset()
	db = NewStore(pool)
	loaded, err := db.LoadOrder(ctx, store.LoadOrderRequest{PrincipalID: request.PrincipalID, GatewayOrderID: current.GatewayOrderID})
	if err != nil || loaded.State != domain.StateUncertain || !loaded.AcceptanceObserved || loaded.VenueOrderID != "" {
		t.Fatalf("reload=%+v err=%v", loaded, err)
	}
	byKey, err := db.LoadOrderByIdempotencyKey(ctx, request.PrincipalID, request.IdempotencyKey)
	if err != nil || byKey.GatewayOrderID != current.GatewayOrderID || byKey.AccountID != request.AccountID || byKey.InstrumentID != request.InstrumentID || !byKey.AcceptanceObserved {
		t.Fatalf("lookup original key=%+v err=%v", byKey, err)
	}
	if _, err := db.LoadOrderByIdempotencyKey(ctx, "other-principal", request.IdempotencyKey); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross principal key lookup err=%v", err)
	}
	if _, err := db.LoadOrderByIdempotencyKey(ctx, request.PrincipalID, "absent-key"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("absent key lookup err=%v", err)
	}
	recoverable, err := db.LoadRecoverableCommands(ctx, store.LoadRecoverableCommandsRequest{AccountID: request.AccountID, Limit: 10})
	if err != nil || len(recoverable) != 1 || !recoverable[0].AcceptanceObserved {
		t.Fatalf("recoverable=%+v err=%v", recoverable, err)
	}
	now := time.Now()
	tasks, err := db.ClaimDueReconciliationTasks(ctx, store.ClaimDueReconciliationTasksRequest{AccountID: request.AccountID, Now: now, LeaseUntil: now.Add(time.Minute), Limit: 10, MaxAttempts: 10})
	if err != nil || len(tasks) != 1 || !tasks[0].Command.AcceptanceObserved {
		t.Fatalf("claimed=%+v err=%v", tasks, err)
	}
	listed, err := db.ListOrders(ctx, store.ListOrdersRequest{PrincipalID: request.PrincipalID, AccountID: request.AccountID, Limit: 10})
	if err != nil || len(listed) != 1 || !listed[0].AcceptanceObserved {
		t.Fatalf("listed=%+v err=%v", listed, err)
	}
	byVenue, err := db.LoadOrderByVenueIdentity(ctx, store.LoadOrderByVenueIdentityRequest{AccountID: request.AccountID, Product: "spot", ClientOrderID: current.ClientOrderID})
	if err != nil || !byVenue.AcceptanceObserved {
		t.Fatalf("by venue identity=%+v err=%v", byVenue, err)
	}
	task, err := db.RescheduleReconciliation(ctx, store.RescheduleReconciliationRequest{GatewayOrderID: current.GatewayOrderID, Plan: store.ReconciliationPlan{TaskID: nextID(), Reason: store.ReconciliationEvidenceUnconfirmed, NextAttemptAt: now.Add(time.Minute)}})
	if err != nil || !task.Command.AcceptanceObserved {
		t.Fatalf("rescheduled=%+v err=%v", task, err)
	}
}
