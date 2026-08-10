package executionruntime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/domain/execution"
	"github.com/QuantProcessing/funding-trader/backend/internal/executionservice"
	"github.com/QuantProcessing/funding-trader/backend/internal/readmodel"
	"github.com/QuantProcessing/funding-trader/backend/internal/uistream"
	"github.com/shopspring/decimal"
)

func TestProjectionStoreProjectsAndBroadcastsCommittedPositionAndActivityEvents(t *testing.T) {
	ctx := context.Background()
	scope := readmodel.Scope{PrincipalID: "principal-1", Mode: "test"}
	projections := &fakeProjectionStore{
		positionEvent: readmodel.Event{
			ID: 101, Kind: readmodel.PositionUpsert, EntityID: "position-exec-open",
			Payload: json.RawMessage(`{"positionId":"position-exec-open"}`), OccurredAt: time.Unix(101, 0).UTC(),
		},
		activityEvent: readmodel.Event{
			ID: 102, Kind: readmodel.ActivityAppend, EntityID: "activity-1",
			Payload: json.RawMessage(`{"kind":"position.opened"}`), OccurredAt: time.Unix(102, 0).UTC(),
		},
	}
	stream := &fakeStoredBroadcaster{}
	store := NewProjectionStore(projections, projections, stream, "boot-projection")

	_, err := store.ApplyExecutionProjection(ctx, "ignored-by-projector", readmodel.PositionProjection{
		ID: "position-exec-open", Scope: scope, Symbol: "BTC-USDT", Status: "open",
		UpdatedAt: time.Unix(100, 0).UTC(), Payload: json.RawMessage(`{"positionId":"position-exec-open"}`),
	}, readmodel.ActivityProjection{
		ID: "activity-1", Scope: scope, Kind: "position.opened", PositionID: "position-exec-open",
		ExecutionID: "exec-open", OccurredAt: time.Unix(100, 0).UTC(), Payload: json.RawMessage(`{"kind":"position.opened"}`),
	})
	if err != nil {
		t.Fatalf("ApplyExecutionProjection: %v", err)
	}

	if len(projections.positions) != 1 || len(projections.activities) != 1 {
		t.Fatalf("projection store did not write exactly once: positions=%d activities=%d", len(projections.positions), len(projections.activities))
	}
	if projections.positionBootID != "boot-projection" || projections.activityBootID != "boot-projection" {
		t.Fatalf("boot id not enforced: position=%q activity=%q", projections.positionBootID, projections.activityBootID)
	}
	if len(stream.events) != 0 {
		t.Fatalf("projection events broadcast before orchestration: %#v", stream.events)
	}
	if err := store.BroadcastProjectionEvents([]readmodel.Event{
		projections.activityEvent,
		projections.positionEvent,
	}); err != nil {
		t.Fatalf("BroadcastProjectionEvents: %v", err)
	}
	if len(stream.events) != 2 {
		t.Fatalf("broadcasts = %d, want 2", len(stream.events))
	}
	if stream.events[0].Sequence != 101 || stream.events[0].Kind != string(readmodel.PositionUpsert) ||
		stream.events[1].Sequence != 102 || stream.events[1].Kind != string(readmodel.ActivityAppend) {
		t.Fatalf("wrong stored broadcasts: %#v", stream.events)
	}
	if projections.appendedUI {
		t.Fatal("projection store appended UI event instead of broadcasting committed read-model events")
	}
}

func TestProjectingPublisherCommitsProjectionBeforePublishingExecutionEvents(t *testing.T) {
	ctx := context.Background()
	exec := projectionExecution("exec-open", execution.StateHedged)
	payload, err := json.Marshal(exec)
	if err != nil {
		t.Fatal(err)
	}
	inner := &recordingPublisher{}
	projections := &fakeProjectionStore{
		positionEvent: readmodel.Event{ID: 201, Kind: readmodel.PositionUpsert, EntityID: "position-exec-open", Payload: json.RawMessage(`{"positionId":"position-exec-open"}`), OccurredAt: time.Unix(201, 0).UTC()},
		activityEvent: readmodel.Event{ID: 202, Kind: readmodel.ActivityAppend, EntityID: "activity-1", Payload: json.RawMessage(`{"kind":"position.opened"}`), OccurredAt: time.Unix(202, 0).UTC()},
	}
	stream := &fakeStoredBroadcaster{}
	store := NewProjectionStore(projections, projections, stream, "boot-projection")
	publisher := NewProjectingPublisher(inner, store)

	err = publisher.Publish(ctx, []executionservice.CommittedEvent{
		{ID: "11", Kind: string(readmodel.ExecutionUpsert), EntityID: exec.ID, Payload: payload, Occurred: time.Unix(11, 0).UTC()},
		{ID: "12", Kind: string(readmodel.AuditAppend), EntityID: exec.ID, Payload: json.RawMessage(`{"audit":true}`), Occurred: time.Unix(12, 0).UTC()},
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if len(inner.events) != 2 || inner.events[0].ID != "11" || inner.events[1].ID != "12" {
		t.Fatalf("inner publisher did not receive original events: %#v", inner.events)
	}
	if len(stream.events) != 2 {
		t.Fatalf("projection broadcasts = %d, want 2", len(stream.events))
	}
	if len(projections.positions) != 1 {
		t.Fatalf("projection not applied before inner publish")
	}
}

func TestProjectingPublisherFailsSafeOnMalformedOrMismatchedExecutionEvent(t *testing.T) {
	ctx := context.Background()
	store := NewProjectionStore(&fakeProjectionStore{}, &fakeProjectionStore{}, &fakeStoredBroadcaster{}, "boot-projection")
	publisher := NewProjectingPublisher(&recordingPublisher{}, store)

	err := publisher.Publish(ctx, []executionservice.CommittedEvent{{
		ID: "11", Kind: string(readmodel.ExecutionUpsert), EntityID: "exec-bad",
		Payload: json.RawMessage(`{"id":`), Occurred: time.Unix(11, 0).UTC(),
	}})
	if err == nil {
		t.Fatal("expected malformed execution event to fail safe")
	}

	exec := projectionExecution("exec-open", execution.StateHedged)
	exec.ID = "different"
	payload, marshalErr := json.Marshal(exec)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	err = publisher.Publish(ctx, []executionservice.CommittedEvent{{
		ID: "12", Kind: string(readmodel.ExecutionUpsert), EntityID: "exec-open",
		Payload: payload, Occurred: time.Unix(12, 0).UTC(),
	}})
	if err == nil {
		t.Fatal("expected mismatched execution event to fail safe")
	}
}

func TestProjectingPublisherDoesNotBroadcastExecutionWhenProjectionCommitFails(t *testing.T) {
	ctx := context.Background()
	exec := projectionExecution("exec-open", execution.StateHedged)
	payload, err := json.Marshal(exec)
	if err != nil {
		t.Fatal(err)
	}
	inner := &recordingPublisher{}
	projections := &fakeProjectionStore{err: errors.New("projection transaction failed")}
	publisher := NewProjectingPublisher(
		inner,
		NewProjectionStore(projections, projections, &fakeStoredBroadcaster{}, "boot-projection"),
	)

	err = publisher.Publish(ctx, []executionservice.CommittedEvent{{
		ID: "11", Kind: string(readmodel.ExecutionUpsert), EntityID: exec.ID,
		Payload: payload, Occurred: time.Unix(11, 0).UTC(),
	}})

	if err == nil {
		t.Fatal("expected projection commit failure")
	}
	if len(inner.events) != 0 {
		t.Fatalf("execution was broadcast before projection commit: %#v", inner.events)
	}
}

func TestProjectingPublisherKeepsDurableProjectionWhenBroadcastFails(t *testing.T) {
	ctx := context.Background()
	exec := projectionExecution("exec-open", execution.StateHedged)
	payload, err := json.Marshal(exec)
	if err != nil {
		t.Fatal(err)
	}
	projections := &fakeProjectionStore{}
	publisher := NewProjectingPublisher(&recordingPublisher{err: errors.New("stream down")}, NewProjectionStore(projections, projections, &fakeStoredBroadcaster{}, "boot-projection"))

	err = publisher.Publish(ctx, []executionservice.CommittedEvent{{
		ID: "11", Kind: string(readmodel.ExecutionUpsert), EntityID: exec.ID,
		Payload: payload, Occurred: time.Unix(11, 0).UTC(),
	}})
	if err == nil {
		t.Fatal("expected inner publish error")
	}
	if len(projections.positions) != 1 {
		t.Fatalf("durable projection was not committed before broadcast failure: %#v", projections.positions)
	}
}

func TestProjectingPublisherRepairRebuildsTheRequestedExecutionScope(t *testing.T) {
	ctx := context.Background()
	exec := projectionExecution("exec-open", execution.StateHedged)
	projections := &fakeProjectionStore{
		executions: []execution.Execution{exec},
		positionEvent: readmodel.Event{
			ID: 301, Kind: readmodel.PositionUpsert, EntityID: "position-exec-open",
			Payload: json.RawMessage(`{"positionId":"position-exec-open"}`), OccurredAt: time.Unix(301, 0).UTC(),
		},
		activityEvent: readmodel.Event{
			ID: 302, Kind: readmodel.ActivityAppend, EntityID: "activity-1",
			Payload: json.RawMessage(`{"kind":"position.opened"}`), OccurredAt: time.Unix(302, 0).UTC(),
		},
	}
	stream := &fakeStoredBroadcaster{}
	publisher := NewProjectingPublisher(
		&recordingPublisher{},
		NewProjectionStore(projections, projections, stream, "boot-projection"),
	)

	if err := publisher.Repair(ctx, executionservice.Scope{
		PrincipalID: exec.PrincipalID,
		Mode:        exec.Mode,
	}); err != nil {
		t.Fatalf("Repair: %v", err)
	}
	if len(projections.positions) != 1 || len(projections.activities) != 1 {
		t.Fatalf("repair projections: positions=%d activities=%d", len(projections.positions), len(projections.activities))
	}
	if projections.positions[0].Scope != (readmodel.Scope{PrincipalID: exec.PrincipalID, Mode: exec.Mode}) {
		t.Fatalf("repair scope = %+v", projections.positions[0].Scope)
	}
	if len(stream.events) != 2 || stream.events[0].Sequence != 301 || stream.events[1].Sequence != 302 {
		t.Fatalf("repair broadcasts = %#v", stream.events)
	}
}

func projectionExecution(id string, state execution.State) execution.Execution {
	at := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	return execution.Execution{
		ID:                   id,
		PrincipalID:          "principal-1",
		Mode:                 "test",
		Operation:            execution.OperationOpen,
		OpportunityID:        "opp-btc",
		LineageID:            id,
		State:                state,
		CreatedAt:            at,
		UpdatedAt:            at.Add(time.Minute),
		CapitalUSDT:          decimal.RequireFromString("100"),
		Leverage:             3,
		ExpectedNetAprPct:    decimal.RequireFromString("18.5"),
		InitialLongQuantity:  decimal.RequireFromString("0.12"),
		InitialShortQuantity: decimal.RequireFromString("0.12"),
		PlannedLegs: []execution.PlannedLeg{
			{LegIdentity: execution.LegIdentity{AccountID: "acct-long", Venue: "okx", InstrumentID: "okx-btc", VenueEnvironment: "demo"}, Role: execution.LegRoleLong, Side: execution.SideBuy, Symbol: "BTC-USDT", GatewayInstrumentID: "BTC-USDT-SWAP", Notional: decimal.RequireFromString("300"), Quantity: decimal.RequireFromString("0.12")},
			{LegIdentity: execution.LegIdentity{AccountID: "acct-short", Venue: "bybit", InstrumentID: "bybit-btc", VenueEnvironment: "testnet"}, Role: execution.LegRoleShort, Side: execution.SideSell, Symbol: "BTC-USDT", GatewayInstrumentID: "BTCUSDT", Notional: decimal.RequireFromString("300"), Quantity: decimal.RequireFromString("0.12")},
		},
		Legs: []execution.Leg{
			{LegIdentity: execution.LegIdentity{AccountID: "acct-long", Venue: "okx", InstrumentID: "okx-btc", VenueEnvironment: "demo"}, Role: execution.LegRoleLong, Filled: decimal.RequireFromString("0.12")},
			{LegIdentity: execution.LegIdentity{AccountID: "acct-short", Venue: "bybit", InstrumentID: "bybit-btc", VenueEnvironment: "testnet"}, Role: execution.LegRoleShort, Filled: decimal.RequireFromString("0.12")},
		},
	}
}

type fakeProjectionStore struct {
	executions     []execution.Execution
	positions      []readmodel.PositionProjection
	activities     []readmodel.ActivityProjection
	positionEvent  readmodel.Event
	activityEvent  readmodel.Event
	positionBootID string
	activityBootID string
	appendedUI     bool
	err            error
}

func (store *fakeProjectionStore) ListExecutions(context.Context, readmodel.Scope) ([]execution.Execution, error) {
	return append([]execution.Execution(nil), store.executions...), nil
}

func (store *fakeProjectionStore) ApplyExecutionProjection(
	_ context.Context,
	bootID string,
	position readmodel.PositionProjection,
	activity readmodel.ActivityProjection,
) ([]readmodel.Event, error) {
	if store.err != nil {
		return nil, store.err
	}
	store.positionBootID = bootID
	store.activityBootID = bootID
	store.positions = append(store.positions, position)
	store.activities = append(store.activities, activity)
	return []readmodel.Event{store.positionEvent, store.activityEvent}, nil
}

type fakeStoredBroadcaster struct {
	events []uistream.StoredEvent
}

func (broadcaster *fakeStoredBroadcaster) BroadcastStored(event uistream.StoredEvent) error {
	broadcaster.events = append(broadcaster.events, event)
	return nil
}

type recordingPublisher struct {
	events []executionservice.CommittedEvent
	err    error
}

func (publisher *recordingPublisher) Publish(_ context.Context, events []executionservice.CommittedEvent) error {
	publisher.events = append(publisher.events, events...)
	return publisher.err
}
