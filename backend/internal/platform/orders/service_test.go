package orders

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

type reservationGate struct{ calls int }

func (gate *reservationGate) Consume(_ context.Context, request ReservationClaim) error {
	gate.calls++
	if request.ReservationID == "denied" {
		return errors.New("reservation denied")
	}
	return nil
}

func TestServiceReplaysCanonicalEconomicCommandAndRejectsConflict(t *testing.T) {
	gate := &reservationGate{}
	service := NewService(NewMemoryStore(), gate, time.Now)
	request := CreateRequest{
		PrincipalID: "principal-a", Mode: ModeTest, AccountID: "account-a", ProductID: "funding",
		LineageID: "lineage-a", ReservationID: "reservation-a", IdempotencyKey: "open-a",
		Method: "POST", Path: "/api/v1/commands", CommandKind: "funding.open",
		Payload: json.RawMessage(`{"quantity":"1","side":"buy"}`),
	}
	first, replay, err := service.Create(context.Background(), request)
	if err != nil || replay {
		t.Fatalf("first Create = %+v replay=%v err=%v", first, replay, err)
	}
	reordered := request
	reordered.Payload = json.RawMessage(`{"side":"buy","quantity":"1"}`)
	second, replay, err := service.Create(context.Background(), reordered)
	if err != nil || !replay || second.CommandID != first.CommandID || gate.calls != 1 {
		t.Fatalf("replay = %+v replay=%v calls=%d err=%v", second, replay, gate.calls, err)
	}
	changed := request
	changed.Payload = json.RawMessage(`{"quantity":"2","side":"buy"}`)
	conflict, _, err := service.Create(context.Background(), changed)
	if !errors.Is(err, ErrIdempotencyConflict) || conflict.CommandID != first.CommandID || gate.calls != 1 {
		t.Fatalf("conflict = %+v calls=%d err=%v", conflict, gate.calls, err)
	}
}

func TestServicePersistsUncertainReconciliationUntilTerminalEvidence(t *testing.T) {
	store := NewMemoryStore()
	service := NewService(store, nil, time.Now)
	command, _, err := service.Create(context.Background(), CreateRequest{
		PrincipalID: "principal-a", Mode: ModeTest, AccountID: "account-a", ProductID: "manual",
		LineageID: "lineage-a", IdempotencyKey: "order-a", Method: "POST", Path: "/api/v1/commands",
		CommandKind: "manual.place", Payload: json.RawMessage(`{"side":"buy"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	uncertain, err := service.Transition(context.Background(), TransitionRequest{
		CommandID: command.CommandID, State: StateUncertain, Reason: ReconciliationAmbiguousHandoff,
		NextAttemptAt: time.Now().Add(time.Second),
	})
	if err != nil || uncertain.State != StateUncertain {
		t.Fatalf("uncertain = %+v, err=%v", uncertain, err)
	}
	due, err := service.ClaimDueReconciliation(context.Background(), ModeTest, time.Now().Add(2*time.Second), time.Minute, 10)
	if err != nil || len(due) != 1 || due[0].CommandID != command.CommandID {
		t.Fatalf("due = %+v, err=%v", due, err)
	}
	if _, err := service.Transition(context.Background(), TransitionRequest{CommandID: command.CommandID, State: StateFilled}); err != nil {
		t.Fatal(err)
	}
	due, err = service.ClaimDueReconciliation(context.Background(), ModeTest, time.Now().Add(2*time.Minute), time.Minute, 10)
	if err != nil || len(due) != 0 {
		t.Fatalf("terminal due = %+v, err=%v", due, err)
	}
}

func TestServiceDeniesLiveCommandsUnlessExplicitlyEnabled(t *testing.T) {
	service := NewService(NewMemoryStore(), nil, time.Now)
	_, _, err := service.Create(context.Background(), CreateRequest{
		PrincipalID: "principal-a", Mode: ModeLive, AccountID: "account-a", ProductID: "manual",
		LineageID: "lineage-a", IdempotencyKey: "live-a", Method: "POST", Path: "/api/v1/commands",
		CommandKind: "manual.place", Payload: json.RawMessage(`{"side":"buy"}`),
	})
	if !errors.Is(err, ErrLiveWriteDisabled) {
		t.Fatalf("Create error = %v, want ErrLiveWriteDisabled", err)
	}
	service.SetLiveWritesEnabled(true)
	if _, _, err := service.Create(context.Background(), CreateRequest{
		PrincipalID: "principal-a", Mode: ModeLive, AccountID: "account-a", ProductID: "manual",
		LineageID: "lineage-a", IdempotencyKey: "live-b", Method: "POST", Path: "/api/v1/commands",
		CommandKind: "manual.place", Payload: json.RawMessage(`{"side":"buy"}`),
	}); err != nil {
		t.Fatal(err)
	}
}

func TestServiceRejectsTerminalStateRegression(t *testing.T) {
	service := NewService(NewMemoryStore(), nil, time.Now)
	command, _, err := service.Create(context.Background(), CreateRequest{PrincipalID: "p", Mode: ModeTest, AccountID: "a", ProductID: "manual", LineageID: "l", IdempotencyKey: "k", Method: "POST", Path: "/api/v1/commands", CommandKind: "manual.place", Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Transition(context.Background(), TransitionRequest{CommandID: command.CommandID, State: StateFilled}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Transition(context.Background(), TransitionRequest{CommandID: command.CommandID, State: StateSubmitted}); err == nil {
		t.Fatal("terminal command regressed to submitted")
	}
}

func TestServiceFencesCommandReadsByPrincipalAndMode(t *testing.T) {
	service := NewService(NewMemoryStore(), nil, time.Now)
	command, _, err := service.Create(context.Background(), CreateRequest{PrincipalID: "p", Mode: ModeTest, AccountID: "a", ProductID: "manual", LineageID: "l", IdempotencyKey: "k", Method: "POST", Path: "/api/v1/commands", CommandKind: "manual.place", Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetOwned(context.Background(), "other", ModeTest, command.CommandID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-principal read err=%v", err)
	}
	if _, err := service.GetOwned(context.Background(), "p", ModeLive, command.CommandID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-mode read err=%v", err)
	}
}
