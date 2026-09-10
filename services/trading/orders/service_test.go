package orders

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// newLiveService arms the operator switch, which every test that writes a
// command now has to do. The switch used to admit a test-mode command with no
// arming at all, so most of these tests never touched it; with one mode left
// the switch is the only gate and it starts closed.
func newLiveService(store Store, now func() time.Time) *Service {
	service := NewService(store, now)
	service.SetLiveWritesEnabled(true)
	return service
}

func TestServiceReplaysCanonicalEconomicCommandAndRejectsConflict(t *testing.T) {
	service := newLiveService(NewMemoryStore(), time.Now)
	request := CreateRequest{
		PrincipalID: "principal-a", AccountID: "account-a", ProductID: "funding",
		LineageID: "lineage-a", IdempotencyKey: "open-a",
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
	if err != nil || !replay || second.CommandID != first.CommandID {
		t.Fatalf("replay = %+v replay=%v err=%v", second, replay, err)
	}
	changed := request
	changed.Payload = json.RawMessage(`{"quantity":"2","side":"buy"}`)
	conflict, _, err := service.Create(context.Background(), changed)
	if !errors.Is(err, ErrIdempotencyConflict) || conflict.CommandID != first.CommandID {
		t.Fatalf("conflict = %+v err=%v", conflict, err)
	}
}

func TestListOwnedFiltersPrincipalAndProduct(t *testing.T) {
	service := newLiveService(NewMemoryStore(), time.Now)
	for _, request := range []CreateRequest{
		{PrincipalID: "principal-a", AccountID: "account-a", ProductID: "manual", LineageID: "lineage-a", IdempotencyKey: "a", Method: "POST", Path: "/api/v1/commands", CommandKind: "manual.place", Payload: json.RawMessage(`{}`)},
		{PrincipalID: "principal-a", AccountID: "account-a", ProductID: "funding", LineageID: "lineage-b", IdempotencyKey: "b", Method: "POST", Path: "/api/v1/commands", CommandKind: "funding.open", Payload: json.RawMessage(`{}`)},
		{PrincipalID: "principal-b", AccountID: "account-b", ProductID: "manual", LineageID: "lineage-c", IdempotencyKey: "c", Method: "POST", Path: "/api/v1/commands", CommandKind: "manual.place", Payload: json.RawMessage(`{}`)},
	} {
		if _, _, err := service.Create(context.Background(), request); err != nil {
			t.Fatal(err)
		}
	}
	items, err := service.ListOwned(context.Background(), "principal-a", "manual")
	if err != nil || len(items) != 1 || items[0].ProductID != "manual" || items[0].PrincipalID != "principal-a" {
		t.Fatalf("ListOwned()=%+v err=%v", items, err)
	}
}

func TestServicePersistsUncertainReconciliationUntilTerminalEvidence(t *testing.T) {
	store := NewMemoryStore()
	service := newLiveService(store, time.Now)
	command, _, err := service.Create(context.Background(), CreateRequest{
		PrincipalID: "principal-a", AccountID: "account-a", ProductID: "manual",
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
	due, err := service.ClaimDueReconciliation(context.Background(), time.Now().Add(2*time.Second), time.Minute, 10)
	if err != nil || len(due) != 1 || due[0].CommandID != command.CommandID {
		t.Fatalf("due = %+v, err=%v", due, err)
	}
	if _, err := service.Transition(context.Background(), TransitionRequest{CommandID: command.CommandID, State: StateFilled}); err != nil {
		t.Fatal(err)
	}
	due, err = service.ClaimDueReconciliation(context.Background(), time.Now().Add(2*time.Minute), time.Minute, 10)
	if err != nil || len(due) != 0 {
		t.Fatalf("terminal due = %+v, err=%v", due, err)
	}
}

// TestServiceDeniesCommandsUnlessLiveWritesEnabled is the one test here that
// must build its service unarmed: it owns the switch itself.
func TestServiceDeniesCommandsUnlessLiveWritesEnabled(t *testing.T) {
	service := NewService(NewMemoryStore(), time.Now)
	_, _, err := service.Create(context.Background(), CreateRequest{
		PrincipalID: "principal-a", AccountID: "account-a", ProductID: "manual",
		LineageID: "lineage-a", IdempotencyKey: "live-a", Method: "POST", Path: "/api/v1/commands",
		CommandKind: "manual.place", Payload: json.RawMessage(`{"side":"buy"}`),
	})
	if !errors.Is(err, ErrLiveWriteDisabled) {
		t.Fatalf("Create error = %v, want ErrLiveWriteDisabled", err)
	}
	service.SetLiveWritesEnabled(true)
	if _, _, err := service.Create(context.Background(), CreateRequest{
		PrincipalID: "principal-a", AccountID: "account-a", ProductID: "manual",
		LineageID: "lineage-a", IdempotencyKey: "live-b", Method: "POST", Path: "/api/v1/commands",
		CommandKind: "manual.place", Payload: json.RawMessage(`{"side":"buy"}`),
	}); err != nil {
		t.Fatal(err)
	}
}

func TestServiceRejectsTerminalStateRegression(t *testing.T) {
	service := newLiveService(NewMemoryStore(), time.Now)
	command, _, err := service.Create(context.Background(), CreateRequest{PrincipalID: "p", AccountID: "a", ProductID: "manual", LineageID: "l", IdempotencyKey: "k", Method: "POST", Path: "/api/v1/commands", CommandKind: "manual.place", Payload: json.RawMessage(`{}`)})
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

// TestServiceFencesCommandReadsByPrincipalAndMode had a second case: the right
// principal reading its own command under the other mode, which GetOwned
// refused. The read fenced on principal and mode together; with one mode left
// the principal is the whole fence, so the clause went from GetOwned and the
// case went from here.
func TestServiceFencesCommandReadsByPrincipal(t *testing.T) {
	service := newLiveService(NewMemoryStore(), time.Now)
	command, _, err := service.Create(context.Background(), CreateRequest{PrincipalID: "p", AccountID: "a", ProductID: "manual", LineageID: "l", IdempotencyKey: "k", Method: "POST", Path: "/api/v1/commands", CommandKind: "manual.place", Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetOwned(context.Background(), "other", command.CommandID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-principal read err=%v", err)
	}
	if _, err := service.GetOwned(context.Background(), "p", command.CommandID); err != nil {
		t.Fatalf("owner read err=%v", err)
	}
}

func TestFreshServicesGenerateDistinctCommandIDs(t *testing.T) {
	store := NewMemoryStore()
	create := func(service *Service, key string) Command {
		t.Helper()
		item, replay, err := service.Create(context.Background(), CreateRequest{
			PrincipalID: "principal-a", AccountID: "account-a", ProductID: "manual", LineageID: "lineage-a",
			IdempotencyKey: key, Method: "POST", Path: "/api/v1/commands", CommandKind: "manual.place", Payload: json.RawMessage(`{}`),
		})
		if err != nil || replay {
			t.Fatalf("Create() item=%+v replay=%v err=%v", item, replay, err)
		}
		return item
	}
	first := create(newLiveService(store, time.Now), "first")
	second := create(newLiveService(store, time.Now), "second")
	if first.CommandID == second.CommandID {
		t.Fatalf("fresh services generated colliding command id %q", first.CommandID)
	}
}
