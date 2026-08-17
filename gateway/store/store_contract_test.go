package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/gateway/domain"
)

func TestCommandStoreContractDeclaresIdempotentHandoff(t *testing.T) {
	var _ CommandStore = (*fakeCommandStore)(nil)
}

type fakeCommandStore struct{}

func (*fakeCommandStore) ReserveCommand(context.Context, ReserveCommandRequest) (ReservedCommand, error) {
	return ReservedCommand{
		CommandID:      domain.CommandID("018f0000-0000-7000-8000-000000000001"),
		GatewayOrderID: domain.GatewayOrderID("018f0000-0000-7000-8000-000000000002"),
		ClientOrderID:  "1",
		State:          domain.StatePrepared,
		New:            true,
	}, nil
}

func (*fakeCommandStore) MarkDispatching(context.Context, MarkDispatchingRequest) (StoredCommand, error) {
	return StoredCommand{State: domain.StateDispatching}, nil
}

func (*fakeCommandStore) RecordVenueResult(context.Context, RecordVenueResultRequest) (StoredCommand, error) {
	return StoredCommand{State: domain.StateAccepted}, nil
}

func (*fakeCommandStore) LoadOrder(context.Context, LoadOrderRequest) (StoredCommand, error) {
	return StoredCommand{}, nil
}

func (*fakeCommandStore) LoadOrderByVenueIdentity(context.Context, LoadOrderByVenueIdentityRequest) (StoredCommand, error) {
	return StoredCommand{}, nil
}

func (*fakeCommandStore) ListOrders(context.Context, ListOrdersRequest) ([]StoredCommand, error) {
	return nil, nil
}

func (*fakeCommandStore) LoadRecoverableCommands(context.Context, LoadRecoverableCommandsRequest) ([]StoredCommand, error) {
	return []StoredCommand{{State: domain.StatePrepared}, {State: domain.StateDispatching}, {State: domain.StateUncertain}}, nil
}

func (*fakeCommandStore) RescheduleReconciliation(context.Context, RescheduleReconciliationRequest) (ReconciliationTask, error) {
	return ReconciliationTask{Reason: ReconciliationEvidenceUnconfirmed, Attempts: 1}, nil
}

func (*fakeCommandStore) ClaimDueReconciliationTasks(context.Context, ClaimDueReconciliationTasksRequest) ([]ReconciliationTask, error) {
	return []ReconciliationTask{{Reason: ReconciliationAmbiguousHandoff}}, nil
}

func (*fakeCommandStore) BeginOrderHandoff(_ context.Context, request BeginOrderHandoffRequest) (OrderHandoff, error) {
	return OrderHandoff{BeginOrderHandoffRequest: request, State: OrderHandoffStarted, StartedAt: time.Now()}, nil
}

func (*fakeCommandStore) ResolveOrderHandoff(_ context.Context, request ResolveOrderHandoffRequest) (OrderHandoff, error) {
	resolved := time.Now()
	return OrderHandoff{BeginOrderHandoffRequest: BeginOrderHandoffRequest{HandoffID: request.HandoffID}, State: request.State, ResolvedAt: &resolved}, nil
}

func TestReserveCommandRequestFreezesConflictScope(t *testing.T) {
	payload := json.RawMessage(`{"quantity":"1"}`)
	payloadHash, err := domain.CanonicalPayloadHash(payload)
	if err != nil {
		t.Fatal(err)
	}
	request := ReserveCommandRequest{
		PrincipalID:    domain.PrincipalID("principal-a"),
		AccountID:      domain.AccountID("binance-main"),
		InstrumentID:   domain.InstrumentID("binance:live:spot:BTC-USDT"),
		IdempotencyKey: domain.IdempotencyKey("strategy-a-1"),
		CorrelationID:  domain.EventID("018f0000-0000-7000-8000-000000000003"),
		PayloadHash:    payloadHash,
		Payload:        payload,
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("ReserveCommandRequest rejected: %v", err)
	}
	changed := request
	changed.PayloadHash = ""
	if err := changed.Validate(); err == nil {
		t.Fatal("ReserveCommandRequest accepted missing payload hash")
	}
	changed = request
	changed.Payload = nil
	if err := changed.Validate(); err == nil {
		t.Fatal("ReserveCommandRequest accepted missing durable payload")
	}
	changed = request
	changed.PayloadHash = "not-the-canonical-hash"
	if err := changed.Validate(); err == nil {
		t.Fatal("ReserveCommandRequest accepted payload/hash mismatch")
	}
}

func TestLoadOrderByVenueIdentityRequiresBoundedProductAndIdentity(t *testing.T) {
	request := LoadOrderByVenueIdentityRequest{
		AccountID:     "binance-main",
		Product:       "spot",
		ClientOrderID: "101",
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("valid client order identity rejected: %v", err)
	}
	request.ClientOrderID = ""
	request.VenueOrderID = "venue-1"
	if err := request.Validate(); err != nil {
		t.Fatalf("valid venue order identity rejected: %v", err)
	}
	request.Product = "future"
	if err := request.Validate(); err == nil {
		t.Fatal("unsupported product accepted")
	}
	request.Product = "spot"
	request.VenueOrderID = "venue-1\nsecret"
	if err := request.Validate(); err == nil {
		t.Fatal("control characters accepted in venue order identity")
	}
}

func TestReconciliationPlanFreezesSafeBoundedReasons(t *testing.T) {
	plan := ReconciliationPlan{
		TaskID:        domain.EventID("018f0000-0000-7000-8000-000000000021"),
		Reason:        ReconciliationAmbiguousHandoff,
		NextAttemptAt: time.Unix(1710000000, 0).UTC(),
	}
	if err := plan.Validate(); err != nil {
		t.Fatalf("valid reconciliation plan rejected: %v", err)
	}
	invalid := plan
	invalid.Reason = "venue said secret canary"
	if err := invalid.Validate(); err == nil {
		t.Fatal("free-form reconciliation reason accepted")
	}
	invalid = plan
	invalid.NextAttemptAt = time.Time{}
	if err := invalid.Validate(); err == nil {
		t.Fatal("reconciliation plan accepted zero next-attempt time")
	}

	due := ClaimDueReconciliationTasksRequest{
		AccountID:   domain.AccountID("binance-main"),
		Now:         time.Unix(1710000010, 0).UTC(),
		LeaseUntil:  time.Unix(1710000040, 0).UTC(),
		Limit:       10,
		MaxAttempts: 3,
	}
	if err := due.Validate(); err != nil {
		t.Fatalf("valid due-task request rejected: %v", err)
	}
	due.MaxAttempts = 0
	if err := due.Validate(); err == nil {
		t.Fatal("unbounded reconciliation request accepted")
	}
}
