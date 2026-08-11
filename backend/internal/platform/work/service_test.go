package work

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/backend/internal/platform/orders"
)

type commandGatewayFixture struct {
	calls   []orders.Command
	handoff error
}

type payloadProcessorFixture struct {
	calls    []orders.Command
	commands []orders.Command
	err      error
}

type planProducerFixture struct{ err error }

type dependencyReadinessFixture struct{ err error }

type blockingDependencyReadiness struct{}

type blockingWorkerReadinessStore struct{ *MemoryStore }

type blockingClaimStore struct{ *MemoryStore }

type ambiguousDeadlineClaimStore struct{ *MemoryStore }

var errAmbiguousClaimCommit = errors.New("ambiguous claim commit at deadline")

func (fixture dependencyReadinessFixture) Check(context.Context) error { return fixture.err }

func (blockingDependencyReadiness) Check(ctx context.Context) error {
	return waitForClaimContext(ctx)
}

func (store *blockingWorkerReadinessStore) GetWorker(ctx context.Context, _ string) (Registration, error) {
	return Registration{}, waitForClaimContext(ctx)
}

func (store *blockingClaimStore) ClaimWork(ctx context.Context, _ ClaimRequest, _ time.Time, _ time.Duration) (ClaimPage, error) {
	return ClaimPage{}, waitForClaimContext(ctx)
}

func (store *ambiguousDeadlineClaimStore) ClaimWork(ctx context.Context, _ ClaimRequest, _ time.Time, _ time.Duration) (ClaimPage, error) {
	<-ctx.Done()
	return ClaimPage{}, errAmbiguousClaimCommit
}

func waitForClaimContext(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(1500 * time.Millisecond):
		return errors.New("claim context was not canceled")
	}
}

type completionValidatorStore struct {
	*MemoryStore
	calls int
	err   error
}

func (store *completionValidatorStore) ValidateCompletion(context.Context, string, CompleteRequest, time.Time) error {
	store.calls++
	return store.err
}

func (producer planProducerFixture) Prepare(context.Context, IntentRecord) error { return producer.err }

func (processor *payloadProcessorFixture) Process(_ context.Context, parent orders.Command) ([]orders.Command, error) {
	processor.calls = append(processor.calls, parent)
	return processor.commands, processor.err
}

func (processor *payloadProcessorFixture) RecordHandoff(_ context.Context, command orders.Command, handoff orders.HandoffResult) (orders.Command, error) {
	if handoff.Outcome == orders.HandoffOutcomeUnknown {
		command.State = orders.StateUncertain
	} else if handoff.Outcome == orders.HandoffRejected || handoff.Outcome == orders.HandoffNotAttempted {
		command.State = orders.StateFailed
	} else {
		command.State = orders.StateSubmitted
	}
	return command, nil
}

func (gateway *commandGatewayFixture) Handoff(_ context.Context, command orders.Command) (orders.HandoffResult, error) {
	gateway.calls = append(gateway.calls, command)
	if gateway.handoff != nil {
		return orders.HandoffResult{Outcome: orders.HandoffOutcomeUnknown}, gateway.handoff
	}
	return orders.HandoffResult{Outcome: orders.HandoffAccepted}, nil
}

func TestServiceFencesWorkerGenerationLeaseAndFactsCursor(t *testing.T) {
	clock := time.Date(2026, 8, 4, 21, 0, 0, 0, time.UTC)
	service := New("contract-v1", 10*time.Second)
	service.SetClock(func() time.Time { return clock })

	registration := service.Register(RegistrationRequest{WorkerID: "worker-1", InstanceID: "instance-1", ProductID: "funding", Mode: "test", ContractVersion: "contract-v1"})
	if registration.Generation != 1 {
		t.Fatalf("generation=%d want 1", registration.Generation)
	}
	if err := service.ValidateWorker("worker-1", 1, "instance-1"); err != nil {
		t.Fatalf("ValidateWorker current generation: %v", err)
	}
	if err := service.ValidateWorker("worker-1", 2, "instance-1"); !errors.Is(err, ErrWorkerStale) {
		t.Fatalf("ValidateWorker stale err=%v want %v", err, ErrWorkerStale)
	}

	claims, err := service.Claim(ClaimRequest{WorkerID: "worker-1", WorkerGeneration: 1, InstanceID: "instance-1", MaxItems: 1})
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if len(claims) != 1 || claims[0].LeaseID == "" || claims[0].LeaseGeneration != 1 {
		t.Fatalf("claims=%+v", claims)
	}
	if got := service.Intent(claims[0].LeaseID, IntentRequest{WorkerID: "worker-1", WorkerGeneration: 1, WorkItemID: claims[0].WorkItemID, WorkRevision: claims[0].WorkRevision, LeaseGeneration: claims[0].LeaseGeneration, IdempotencyKey: "intent-1", IntentKind: "funding.open", FactsCursor: "stale-cursor"}); got["accepted"] != false {
		t.Fatalf("stale facts cursor accepted: %+v", got)
	}
	if got := service.Complete(claims[0].LeaseID, CompleteRequest{WorkerID: "worker-1", WorkerGeneration: 1, WorkItemID: claims[0].WorkItemID, WorkRevision: claims[0].WorkRevision, LeaseGeneration: claims[0].LeaseGeneration, IdempotencyKey: "complete-1", FactsCursor: claims[0].FactsCursor, Result: []byte(`{"ok":true}`)}); got["completed"] != true {
		t.Fatalf("complete result=%+v", got)
	}
	if got := service.Complete(claims[0].LeaseID, CompleteRequest{WorkerID: "worker-1", WorkerGeneration: 1, WorkItemID: claims[0].WorkItemID, WorkRevision: claims[0].WorkRevision, LeaseGeneration: claims[0].LeaseGeneration, IdempotencyKey: "complete-2", FactsCursor: claims[0].FactsCursor}); got["accepted"] != false {
		t.Fatalf("terminal lease reused: %+v", got)
	}
}

func TestMutationReplaySurvivesServiceRecreationAndRejectsConflicts(t *testing.T) {
	clock := time.Date(2026, 8, 4, 23, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	service := NewWithStore("contract-v1", 10*time.Second, store)
	service.SetClock(func() time.Time { return clock })
	if _, err := service.RegisterResult(RegistrationRequest{WorkerID: "worker-1", InstanceID: "instance-1", ProductID: "funding", Mode: "test", ContractVersion: "contract-v1", IdempotencyKey: "register-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Heartbeat("worker-1", HeartbeatRequest{WorkerGeneration: 1, InstanceID: "instance-1", State: "ready"}); err != nil {
		t.Fatal(err)
	}
	claims, err := service.Claim(ClaimRequest{WorkerID: "worker-1", WorkerGeneration: 1, InstanceID: "instance-1", IdempotencyKey: "claim-1", MaxItems: 1})
	if err != nil || len(claims) != 1 {
		t.Fatalf("Claim() claims=%+v err=%v", claims, err)
	}
	claim := claims[0]
	request := IntentRequest{WorkerID: "worker-1", WorkerGeneration: 1, WorkItemID: claim.WorkItemID, WorkRevision: claim.WorkRevision, LeaseGeneration: claim.LeaseGeneration, IdempotencyKey: "intent-1", IntentKind: "funding.open", FactsCursor: claim.FactsCursor, AccountFences: claim.AccountFences, Payload: json.RawMessage(`{"quantity":"1","market":"BTC-USDT"}`)}
	first, err := service.IntentResult(claim.LeaseID, request)
	if err != nil {
		t.Fatalf("IntentResult() first err=%v", err)
	}

	restarted := NewWithStore("contract-v1", 10*time.Second, store)
	restarted.SetClock(func() time.Time { return clock.Add(time.Second) })
	reordered := request
	reordered.Payload = json.RawMessage(`{ "market": "BTC-USDT", "quantity": "1" }`)
	replay, err := restarted.IntentResult(claim.LeaseID, reordered)
	if err != nil || !reflect.DeepEqual(replay, first) {
		t.Fatalf("replay=%+v first=%+v err=%v", replay, first, err)
	}
	reordered.Payload = json.RawMessage(`{"market":"ETH-USDT","quantity":"1"}`)
	if _, err := restarted.IntentResult(claim.LeaseID, reordered); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflicting replay err=%v want %v", err, ErrIdempotencyConflict)
	}
}

func TestIntentCreatesSharedCommandAndReplaysGatewayHandoffState(t *testing.T) {
	clock := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	gateway := &commandGatewayFixture{}
	processor := &payloadProcessorFixture{commands: []orders.Command{{CommandID: "command-child-long", ProductID: "funding", LineageID: "lineage_0001", Mode: orders.ModeTest, State: orders.StateAccepted, Payload: json.RawMessage(`{"instrumentId":"BTC-USDT","side":"buy","quantity":"1","orderType":"market"}`)}}}
	service := NewWithStoreGatewayAndProcessor("contract-v1", 10*time.Second, store, processor, gateway)
	service.SetClock(func() time.Time { return clock })
	registration := service.Register(RegistrationRequest{WorkerID: "worker-1", InstanceID: "instance-1", ProductID: "funding", Mode: "test", ContractVersion: "contract-v1"})
	claims, err := service.Claim(ClaimRequest{WorkerID: registration.WorkerID, WorkerGeneration: registration.Generation, InstanceID: registration.InstanceID, IdempotencyKey: "claim-1", MaxItems: 1})
	if err != nil || len(claims) != 1 {
		t.Fatalf("Claim()=%+v err=%v", claims, err)
	}
	claim := claims[0]
	req := IntentRequest{WorkerID: registration.WorkerID, WorkerGeneration: registration.Generation, WorkItemID: claim.WorkItemID, WorkRevision: claim.WorkRevision, LeaseGeneration: claim.LeaseGeneration, IdempotencyKey: "funding-economic-1", IntentKind: "funding.open", FactsCursor: claim.FactsCursor, AccountFences: claim.AccountFences, Payload: json.RawMessage(`{"longAccountId":"account-long","shortAccountId":"account-short","notionalPerLeg":"50"}`)}
	first, err := service.IntentResult(claim.LeaseID, req)
	if err != nil {
		t.Fatal(err)
	}
	command, ok := first["command"].(orders.Command)
	if !ok || command.CommandID == "" || command.ProductID != "funding" || command.AccountID != "account-long" || command.State != orders.StateSubmitted {
		t.Fatalf("command=%+v", first["command"])
	}
	replay, err := service.IntentResult(claim.LeaseID, req)
	if err != nil || replay["command"].(orders.Command).CommandID != command.CommandID || len(processor.calls) != 1 || len(gateway.calls) != 1 || gateway.calls[0].CommandID != "command-child-long" {
		t.Fatalf("replay=%+v calls=%d err=%v", replay, len(gateway.calls), err)
	}
}

func TestIntentPreservesUncertainCommandWithoutRepeatingAmbiguousHandoff(t *testing.T) {
	clock := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	gateway := &commandGatewayFixture{handoff: errors.New("ambiguous handoff")}
	processor := &payloadProcessorFixture{commands: []orders.Command{{CommandID: "command-child-close", ProductID: "funding", LineageID: "lineage_0001", Mode: orders.ModeTest, State: orders.StateAccepted, Payload: json.RawMessage(`{"instrumentId":"BTC-USDT","side":"sell","quantity":"1","orderType":"market","reduceOnly":true}`)}}}
	service := NewWithStoreGatewayAndProcessor("contract-v1", 10*time.Second, store, processor, gateway)
	service.SetClock(func() time.Time { return clock })
	registration := service.Register(RegistrationRequest{WorkerID: "worker-1", InstanceID: "instance-1", ProductID: "funding", Mode: "test", ContractVersion: "contract-v1"})
	claims, _ := service.Claim(ClaimRequest{WorkerID: registration.WorkerID, WorkerGeneration: registration.Generation, InstanceID: registration.InstanceID, IdempotencyKey: "claim-1", MaxItems: 1})
	claim := claims[0]
	req := IntentRequest{WorkerID: registration.WorkerID, WorkerGeneration: registration.Generation, WorkItemID: claim.WorkItemID, WorkRevision: claim.WorkRevision, LeaseGeneration: claim.LeaseGeneration, IdempotencyKey: "funding-economic-1", IntentKind: "funding.close", FactsCursor: claim.FactsCursor, AccountFences: claim.AccountFences, Payload: json.RawMessage(`{"lineageId":"lineage_0001","reason":"operator"}`)}
	first, err := service.IntentResult(claim.LeaseID, req)
	if err != nil || first["command"].(orders.Command).State != orders.StateUncertain {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	replay, err := service.IntentResult(claim.LeaseID, req)
	if err != nil || replay["command"].(orders.Command).State != orders.StateUncertain || len(gateway.calls) != 1 {
		t.Fatalf("replay=%+v calls=%d err=%v", replay, len(gateway.calls), err)
	}
}

func TestIntentFailsClosedBeforeGatewayWithoutPayloadProcessor(t *testing.T) {
	clock := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	gateway := &commandGatewayFixture{}
	service := NewWithStoreAndGateway("contract-v1", 10*time.Second, store, gateway)
	service.SetClock(func() time.Time { return clock })
	registration := service.Register(RegistrationRequest{WorkerID: "worker-1", InstanceID: "instance-1", ProductID: "funding", Mode: "test", ContractVersion: "contract-v1"})
	claims, _ := service.Claim(ClaimRequest{WorkerID: registration.WorkerID, WorkerGeneration: registration.Generation, InstanceID: registration.InstanceID, IdempotencyKey: "claim-1", MaxItems: 1})
	claim := claims[0]
	req := IntentRequest{WorkerID: registration.WorkerID, WorkerGeneration: registration.Generation, WorkItemID: claim.WorkItemID, WorkRevision: claim.WorkRevision, LeaseGeneration: claim.LeaseGeneration, IdempotencyKey: "funding-economic-1", IntentKind: "funding.open", FactsCursor: claim.FactsCursor, AccountFences: claim.AccountFences, Payload: json.RawMessage(`{"longAccountId":"account-long","shortAccountId":"account-short","notionalPerLeg":"50"}`)}
	result, err := service.IntentResult(claim.LeaseID, req)
	if err != nil || result["command"].(orders.Command).State != orders.StateFailed || len(gateway.calls) != 0 {
		t.Fatalf("result=%+v Gateway calls=%d err=%v", result, len(gateway.calls), err)
	}
}

func TestIntentDeterministicProcessorFailureIsFailedBeforeGateway(t *testing.T) {
	store := NewMemoryStore()
	gateway := &commandGatewayFixture{}
	processor := &payloadProcessorFixture{err: errors.New("invalid frozen plan")}
	service := NewWithStoreGatewayAndProcessor("contract-v1", 10*time.Second, store, processor, gateway)
	registration := service.Register(RegistrationRequest{WorkerID: "worker-1", InstanceID: "instance-1", ProductID: "funding", Mode: "test", ContractVersion: "contract-v1"})
	claims, _ := service.Claim(ClaimRequest{WorkerID: registration.WorkerID, WorkerGeneration: registration.Generation, InstanceID: registration.InstanceID, IdempotencyKey: "claim-1", MaxItems: 1})
	claim := claims[0]
	result, err := service.IntentResult(claim.LeaseID, IntentRequest{WorkerID: registration.WorkerID, WorkerGeneration: registration.Generation, WorkItemID: claim.WorkItemID, WorkRevision: claim.WorkRevision, LeaseGeneration: claim.LeaseGeneration, IdempotencyKey: "intent-1", IntentKind: "funding.open", FactsCursor: claim.FactsCursor, AccountFences: claim.AccountFences, Payload: json.RawMessage(`{}`)})
	if err != nil || result["command"].(orders.Command).State != orders.StateFailed || len(gateway.calls) != 0 {
		t.Fatalf("result=%+v gateway calls=%d err=%v", result, len(gateway.calls), err)
	}
}

func TestIntentDeterministicPlanFailureIsFailedBeforeGateway(t *testing.T) {
	store := NewMemoryStore()
	gateway := &commandGatewayFixture{}
	processor := &payloadProcessorFixture{}
	service := NewWithStoreGatewayProcessorAndPlanProducer("contract-v1", 10*time.Second, store, processor, planProducerFixture{err: errors.New("accounting projection failed")}, gateway)
	registration := service.Register(RegistrationRequest{WorkerID: "worker-1", InstanceID: "instance-1", ProductID: "funding", Mode: "test", ContractVersion: "contract-v1"})
	claims, _ := service.Claim(ClaimRequest{WorkerID: registration.WorkerID, WorkerGeneration: registration.Generation, InstanceID: registration.InstanceID, IdempotencyKey: "claim-1", MaxItems: 1})
	claim := claims[0]
	result, err := service.IntentResult(claim.LeaseID, IntentRequest{WorkerID: registration.WorkerID, WorkerGeneration: registration.Generation, WorkItemID: claim.WorkItemID, WorkRevision: claim.WorkRevision, LeaseGeneration: claim.LeaseGeneration, IdempotencyKey: "intent-1", IntentKind: "funding.open", FactsCursor: claim.FactsCursor, AccountFences: claim.AccountFences, Payload: json.RawMessage(`{}`)})
	if err != nil || result["command"].(orders.Command).State != orders.StateFailed || len(processor.calls) != 0 || len(gateway.calls) != 0 {
		t.Fatalf("result=%+v processor calls=%d gateway calls=%d err=%v", result, len(processor.calls), len(gateway.calls), err)
	}
}

func TestCompleteRequiresOwnedCommandsTerminalAndSuccessful(t *testing.T) {
	for _, test := range []struct {
		name       string
		processor  *payloadProcessorFixture
		handoffErr error
		wantState  orders.State
	}{
		{name: "submitted", processor: &payloadProcessorFixture{commands: []orders.Command{{CommandID: "child-submitted", ProductID: "funding", LineageID: "lineage_0001", Mode: orders.ModeTest, State: orders.StateAccepted}}}, wantState: orders.StateSubmitted},
		{name: "uncertain", processor: &payloadProcessorFixture{commands: []orders.Command{{CommandID: "child-uncertain", ProductID: "funding", LineageID: "lineage_0001", Mode: orders.ModeTest, State: orders.StateAccepted}}}, handoffErr: errors.New("ambiguous handoff"), wantState: orders.StateUncertain},
		{name: "failed", processor: &payloadProcessorFixture{err: errors.New("invalid plan")}, wantState: orders.StateFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := NewMemoryStore()
			service := NewWithStoreGatewayAndProcessor("contract-v1", 10*time.Second, store, test.processor, &commandGatewayFixture{handoff: test.handoffErr})
			registration := service.Register(RegistrationRequest{WorkerID: "worker-1", InstanceID: "instance-1", ProductID: "funding", Mode: "test", ContractVersion: "contract-v1"})
			claims, _ := service.Claim(ClaimRequest{WorkerID: registration.WorkerID, WorkerGeneration: registration.Generation, InstanceID: registration.InstanceID, IdempotencyKey: "claim-1", MaxItems: 1})
			claim := claims[0]
			intent, err := service.IntentResult(claim.LeaseID, IntentRequest{WorkerID: registration.WorkerID, WorkerGeneration: registration.Generation, WorkItemID: claim.WorkItemID, WorkRevision: claim.WorkRevision, LeaseGeneration: claim.LeaseGeneration, IdempotencyKey: "intent-1", IntentKind: "funding.open", FactsCursor: claim.FactsCursor, AccountFences: claim.AccountFences, Payload: json.RawMessage(`{}`)})
			if err != nil || intent["command"].(orders.Command).State != test.wantState {
				t.Fatalf("intent=%+v err=%v", intent, err)
			}
			complete := CompleteRequest{WorkerID: registration.WorkerID, WorkerGeneration: registration.Generation, WorkItemID: claim.WorkItemID, WorkRevision: claim.WorkRevision, LeaseGeneration: claim.LeaseGeneration, IdempotencyKey: "complete-1", FactsCursor: claim.FactsCursor, Result: json.RawMessage(`{"outcome":"completed","commandIds":[],"finalFactsCursor":"cursor_0001","summaryCode":"DONE"}`)}
			if _, err := service.CompleteResult(claim.LeaseID, complete); !errors.Is(err, ErrDependencyUnavailable) {
				t.Fatalf("CompleteResult() error=%v, want dependency unavailable", err)
			}
			if test.wantState == orders.StateFailed {
				failed, err := service.FailResult(claim.LeaseID, FailRequest{WorkerID: registration.WorkerID, WorkerGeneration: registration.Generation, WorkItemID: claim.WorkItemID, WorkRevision: claim.WorkRevision, LeaseGeneration: claim.LeaseGeneration, IdempotencyKey: "fail-1", FactsCursor: claim.FactsCursor, ErrorCode: "PLAN_INVALID", Retryable: true, Details: json.RawMessage(`{}`)})
				if err != nil || failed["status"] != "retry_wait" {
					t.Fatalf("retryable failure=%+v err=%v", failed, err)
				}
			}
		})
	}
}

func TestCompleteAcceptsFullyTerminalOwnedCommands(t *testing.T) {
	store := NewMemoryStore()
	processor := &payloadProcessorFixture{commands: []orders.Command{{CommandID: "child-filled", ProductID: "funding", LineageID: "lineage_0001", Mode: orders.ModeTest, State: orders.StateAccepted}}}
	service := NewWithStoreGatewayAndProcessor("contract-v1", 10*time.Second, store, processor, &commandGatewayFixture{})
	registration := service.Register(RegistrationRequest{WorkerID: "worker-1", InstanceID: "instance-1", ProductID: "funding", Mode: "test", ContractVersion: "contract-v1"})
	claims, _ := service.Claim(ClaimRequest{WorkerID: registration.WorkerID, WorkerGeneration: registration.Generation, InstanceID: registration.InstanceID, IdempotencyKey: "claim-1", MaxItems: 1})
	claim := claims[0]
	intent, err := service.IntentResult(claim.LeaseID, IntentRequest{WorkerID: registration.WorkerID, WorkerGeneration: registration.Generation, WorkItemID: claim.WorkItemID, WorkRevision: claim.WorkRevision, LeaseGeneration: claim.LeaseGeneration, IdempotencyKey: "intent-1", IntentKind: "funding.open", FactsCursor: claim.FactsCursor, AccountFences: claim.AccountFences, Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	parent := intent["command"].(orders.Command)
	store.mu.Lock()
	parent.State = orders.StateFilled
	store.commands[parent.CommandID] = parent
	store.mu.Unlock()
	complete := CompleteRequest{WorkerID: registration.WorkerID, WorkerGeneration: registration.Generation, WorkItemID: claim.WorkItemID, WorkRevision: claim.WorkRevision, LeaseGeneration: claim.LeaseGeneration, IdempotencyKey: "complete-1", FactsCursor: claim.FactsCursor, Result: json.RawMessage(`{"outcome":"completed","commandIds":[],"finalFactsCursor":"cursor_0001","summaryCode":"DONE"}`)}
	_, err = service.CompleteResult(claim.LeaseID, complete)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CompleteResult(claim.LeaseID, complete); err != nil {
		t.Fatalf("completion replay: %v", err)
	}
}

func TestClaimFencesModeAndReclaimsExpiredLeaseWithMonotonicGeneration(t *testing.T) {
	clock := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	service := NewWithStore("contract-v1", time.Second, store)
	service.SetClock(func() time.Time { return clock })
	live := service.Register(RegistrationRequest{WorkerID: "worker-live", InstanceID: "instance-live", ProductID: "funding", Mode: "live", ContractVersion: "contract-v1"})
	if claims, err := service.Claim(ClaimRequest{WorkerID: live.WorkerID, WorkerGeneration: live.Generation, InstanceID: live.InstanceID, IdempotencyKey: "claim-live", MaxItems: 1}); err != nil || len(claims) != 0 {
		t.Fatalf("cross-mode Claim() claims=%+v err=%v", claims, err)
	}
	testWorker := service.Register(RegistrationRequest{WorkerID: "worker-test", InstanceID: "instance-test", ProductID: "funding", Mode: "test", ContractVersion: "contract-v1"})
	first, err := service.Claim(ClaimRequest{WorkerID: testWorker.WorkerID, WorkerGeneration: testWorker.Generation, InstanceID: testWorker.InstanceID, IdempotencyKey: "claim-test-1", MaxItems: 1})
	if err != nil || len(first) != 1 {
		t.Fatalf("first Claim()=%+v err=%v", first, err)
	}
	clock = clock.Add(31 * time.Second)
	if _, err := service.Heartbeat(testWorker.WorkerID, HeartbeatRequest{WorkerGeneration: testWorker.Generation, InstanceID: testWorker.InstanceID, State: "ready"}); err != nil {
		t.Fatal(err)
	}
	if claims, err := service.Claim(ClaimRequest{WorkerID: testWorker.WorkerID, WorkerGeneration: testWorker.Generation, InstanceID: testWorker.InstanceID, IdempotencyKey: "claim-test-too-early", MaxItems: 1}); err != nil || len(claims) != 0 {
		t.Fatalf("backoff Claim() claims=%+v err=%v", claims, err)
	}
	clock = clock.Add(time.Second)
	if _, err := service.Heartbeat(testWorker.WorkerID, HeartbeatRequest{WorkerGeneration: testWorker.Generation, InstanceID: testWorker.InstanceID, State: "ready"}); err != nil {
		t.Fatal(err)
	}
	second, err := service.Claim(ClaimRequest{WorkerID: testWorker.WorkerID, WorkerGeneration: testWorker.Generation, InstanceID: testWorker.InstanceID, IdempotencyKey: "claim-test-2", MaxItems: 1})
	if err != nil || len(second) != 1 || second[0].LeaseGeneration <= first[0].LeaseGeneration || second[0].Attempt != 2 {
		t.Fatalf("reclaimed Claim()=%+v first=%+v err=%v", second, first, err)
	}
}

func TestClaimHonorsNotBeforeBeforeClaimingFutureWork(t *testing.T) {
	clock := time.Date(2026, 8, 5, 2, 0, 0, 0, time.UTC)
	store := NewMemoryStoreWithSnapshot([]WorkItem{
		{
			WorkItemID:    "work-future",
			WorkKind:      "open",
			LineageID:     "lineage-future",
			WorkRevision:  1,
			ProductID:     "funding",
			Mode:          "test",
			Status:        "open",
			FactsCursor:   "cursor-future",
			AccountFences: json.RawMessage(`[{"accountId":"account-long","desiredStateRevision":1,"runtimeGeneration":1,"credentialVersion":1},{"accountId":"account-short","desiredStateRevision":1,"runtimeGeneration":1,"credentialVersion":1}]`),
			Payload:       json.RawMessage(`{}`),
			MaxAttempts:   5,
			NotBefore:     clock.Add(time.Second),
			CreatedAt:     clock,
			UpdatedAt:     clock,
		},
	}, nil)
	service := NewWithStore("contract-v1", 10*time.Second, store)
	service.SetClock(func() time.Time { return clock })
	registration := service.Register(RegistrationRequest{WorkerID: "worker-1", InstanceID: "instance-1", ProductID: "funding", Mode: "test", ContractVersion: "contract-v1"})

	if claims, err := service.Claim(ClaimRequest{WorkerID: registration.WorkerID, WorkerGeneration: registration.Generation, InstanceID: registration.InstanceID, IdempotencyKey: "claim-too-early", MaxItems: 1}); err != nil || len(claims) != 0 {
		t.Fatalf("early Claim()=%+v err=%v", claims, err)
	}

	clock = clock.Add(time.Second)
	if _, err := service.Heartbeat(registration.WorkerID, HeartbeatRequest{WorkerGeneration: registration.Generation, InstanceID: registration.InstanceID, State: "ready"}); err != nil {
		t.Fatal(err)
	}
	claims, err := service.Claim(ClaimRequest{WorkerID: registration.WorkerID, WorkerGeneration: registration.Generation, InstanceID: registration.InstanceID, IdempotencyKey: "claim-on-time", MaxItems: 1})
	if err != nil || len(claims) != 1 || claims[0].WorkItemID != "work-future" || claims[0].Attempt != 1 {
		t.Fatalf("on-time Claim()=%+v err=%v", claims, err)
	}
}

func TestIntentRejectsChangedAccountFenceBeforeRecordingIdempotency(t *testing.T) {
	service := New("contract-v1", 10*time.Second)
	registration := service.Register(RegistrationRequest{WorkerID: "worker-1", InstanceID: "instance-1", ProductID: "funding", Mode: "test", ContractVersion: "contract-v1"})
	claims, err := service.Claim(ClaimRequest{WorkerID: registration.WorkerID, WorkerGeneration: registration.Generation, InstanceID: registration.InstanceID, IdempotencyKey: "claim-1", MaxItems: 1})
	if err != nil || len(claims) != 1 {
		t.Fatalf("Claim()=%+v err=%v", claims, err)
	}
	claim := claims[0]
	req := IntentRequest{WorkerID: registration.WorkerID, WorkerGeneration: registration.Generation, WorkItemID: claim.WorkItemID, WorkRevision: claim.WorkRevision, LeaseGeneration: claim.LeaseGeneration, IdempotencyKey: "intent-1", IntentKind: "funding.open", FactsCursor: claim.FactsCursor, AccountFences: json.RawMessage(`[{"accountId":"changed","runtimeGeneration":2}]`), Payload: json.RawMessage(`{}`)}
	if _, err := service.IntentResult(claim.LeaseID, req); !errors.Is(err, ErrAuthorizationStale) {
		t.Fatalf("changed fence err=%v want %v", err, ErrAuthorizationStale)
	}
	req.AccountFences = claim.AccountFences
	if _, err := service.IntentResult(claim.LeaseID, req); err != nil {
		t.Fatalf("same key after rejected pre-idempotency request err=%v", err)
	}
}

func TestIntentCommandsPreserveBrowserPrincipalAndScopeIdempotency(t *testing.T) {
	now := time.Date(2026, 8, 6, 4, 45, 0, 0, time.UTC)
	store := NewMemoryStore()
	fences := json.RawMessage(`[{"accountId":"long","desiredStateRevision":4,"runtimeGeneration":2,"credentialVersion":3},{"accountId":"short","desiredStateRevision":5,"runtimeGeneration":3,"credentialVersion":4}]`)
	for index, principal := range []string{"browser-a", "browser-b"} {
		workID := "work-" + principal
		leaseID := "lease-" + principal
		store.workItems[workID] = WorkItem{WorkItemID: workID, PrincipalID: principal, WorkKind: "open", LineageID: "shared-lineage", WorkRevision: 1, ProductID: "funding", Mode: "test", Status: "leased", FactsCursor: "cursor-" + principal, AccountFences: fences, Payload: json.RawMessage(`{}`), MaxAttempts: 5, CreatedAt: now, UpdatedAt: now}
		store.leases[leaseID] = Lease{LeaseID: leaseID, LeaseGeneration: 1, WorkItemID: workID, WorkRevision: 1, WorkerID: "worker-funding", WorkerGeneration: 1, FactsCursor: "cursor-" + principal, Status: "active", ExpiresAt: now.Add(time.Minute), CreatedAt: now, UpdatedAt: now}
		record, err := store.RecordIntent(context.Background(), leaseID, IntentRequest{WorkerID: "worker-funding", WorkerGeneration: 1, WorkItemID: workID, WorkRevision: 1, LeaseGeneration: 1, IdempotencyKey: "shared-intent", IntentKind: "funding.open", FactsCursor: "cursor-" + principal, AccountFences: fences, Payload: json.RawMessage(`{}`)}, now.Add(time.Duration(index)*time.Second))
		if err != nil {
			t.Fatalf("principal %s RecordIntent() err=%v", principal, err)
		}
		if record.Command.PrincipalID != principal {
			t.Fatalf("principal %s command owner=%q", principal, record.Command.PrincipalID)
		}
		if record.Command.LineageID != "shared-lineage" {
			t.Fatalf("principal %s lineage=%q", principal, record.Command.LineageID)
		}
	}
	if len(store.commands) != 2 {
		t.Fatalf("principal-scoped commands=%d want 2", len(store.commands))
	}
}

func TestAccountFencesRequireCompleteDuplicateFreeExactSet(t *testing.T) {
	valid := json.RawMessage(`[{"accountId":"long","desiredStateRevision":4,"runtimeGeneration":2,"credentialVersion":3},{"accountId":"short","desiredStateRevision":5,"runtimeGeneration":3,"credentialVersion":4}]`)
	if _, err := DecodeAccountFences(valid); err != nil {
		t.Fatalf("valid fences rejected: %v", err)
	}
	for name, encoded := range map[string]json.RawMessage{
		"object":        json.RawMessage(`{}`),
		"null":          json.RawMessage(`null`),
		"empty":         json.RawMessage(`[]`),
		"omitted":       nil,
		"incomplete":    json.RawMessage(`[{"accountId":"long","desiredStateRevision":4,"runtimeGeneration":2,"credentialVersion":3}]`),
		"duplicate":     json.RawMessage(`[{"accountId":"long","desiredStateRevision":4,"runtimeGeneration":2,"credentialVersion":3},{"accountId":"long","desiredStateRevision":5,"runtimeGeneration":3,"credentialVersion":4}]`),
		"missing fence": json.RawMessage(`[{"accountId":"long","runtimeGeneration":2,"credentialVersion":3},{"accountId":"short","desiredStateRevision":5,"runtimeGeneration":3,"credentialVersion":4}]`),
		"unknown field": json.RawMessage(`[{"accountId":"long","desiredStateRevision":4,"runtimeGeneration":2,"credentialVersion":3,"mode":"test"},{"accountId":"short","desiredStateRevision":5,"runtimeGeneration":3,"credentialVersion":4}]`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeAccountFences(encoded); !errors.Is(err, ErrAuthorizationStale) {
				t.Fatalf("DecodeAccountFences(%s) error=%v", encoded, err)
			}
		})
	}
}

func TestCanonicalJSONPreservesLargeIntegersAndJSONBNumericSemantics(t *testing.T) {
	left := json.RawMessage(`{"quantity":"1.00","sequence":9007199254740992,"ratio":1.0}`)
	reordered := json.RawMessage(`{"ratio":1,"sequence":9007199254740992,"quantity":"1.00"}`)
	if !EqualJSON(left, reordered) || CanonicalRequestHash(left) != CanonicalRequestHash(reordered) {
		t.Fatal("equivalent JSONB values did not canonicalize equally")
	}
	largeIntegerChanged := json.RawMessage(`{"quantity":"1.00","sequence":9007199254740993,"ratio":1}`)
	if EqualJSON(left, largeIntegerChanged) || CanonicalRequestHash(left) == CanonicalRequestHash(largeIntegerChanged) {
		t.Fatal("distinct large integers collapsed during JSON normalization")
	}
	decimalTextChanged := json.RawMessage(`{"quantity":"1.0","sequence":9007199254740992,"ratio":1}`)
	if EqualJSON(left, decimalTextChanged) {
		t.Fatal("decimal textual values lost their exact string semantics")
	}
}

func TestRetryableFailuresBackOffAndFifthAttemptRequiresManualIntervention(t *testing.T) {
	clock := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	service := New("contract-v1", 30*time.Second)
	service.SetClock(func() time.Time { return clock })
	registration := service.Register(RegistrationRequest{WorkerID: "worker-1", InstanceID: "instance-1", ProductID: "funding", Mode: "test", ContractVersion: "contract-v1"})
	for attempt, delay := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 0} {
		claims, err := service.Claim(ClaimRequest{WorkerID: registration.WorkerID, WorkerGeneration: registration.Generation, InstanceID: registration.InstanceID, IdempotencyKey: "claim-" + string(rune('1'+attempt)), MaxItems: 1})
		if err != nil || len(claims) != 1 || claims[0].Attempt != attempt+1 {
			t.Fatalf("attempt %d Claim()=%+v err=%v", attempt+1, claims, err)
		}
		claim := claims[0]
		result, err := service.FailResult(claim.LeaseID, FailRequest{WorkerID: registration.WorkerID, WorkerGeneration: registration.Generation, WorkItemID: claim.WorkItemID, WorkRevision: claim.WorkRevision, LeaseGeneration: claim.LeaseGeneration, IdempotencyKey: "fail-" + string(rune('1'+attempt)), FactsCursor: claim.FactsCursor, ErrorCode: "RETRY", Retryable: true, Details: json.RawMessage(`{}`)})
		if err != nil {
			t.Fatalf("attempt %d FailResult() err=%v", attempt+1, err)
		}
		if attempt == 4 {
			if result["status"] != "manual_intervention" {
				t.Fatalf("fifth result=%+v", result)
			}
			break
		}
		if result["status"] != "retry_wait" {
			t.Fatalf("attempt %d result=%+v", attempt+1, result)
		}
		clock = clock.Add(delay)
		if _, err := service.Heartbeat(registration.WorkerID, HeartbeatRequest{WorkerGeneration: registration.Generation, InstanceID: registration.InstanceID, State: "ready"}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestServiceReadinessUsesInjectedClockAndHeartbeatTTL(t *testing.T) {
	clock := time.Date(2026, 8, 4, 22, 0, 0, 0, time.UTC)
	service := New("contract-v1", time.Second)
	service.SetClock(func() time.Time { return clock })
	if _, err := service.RegisterResult(RegistrationRequest{WorkerID: "worker-1", InstanceID: "instance-1", ProductID: "funding", Mode: "test", ContractVersion: "contract-v1", IdempotencyKey: "register-readiness"}); err != nil {
		t.Fatal(err)
	}
	if ready := service.Readiness("worker-1"); ready.CanClaim || ready.FreshHeartbeat || ready.State != "starting" {
		t.Fatalf("registration readiness=%+v", ready)
	}
	if _, err := service.Heartbeat("worker-1", HeartbeatRequest{WorkerGeneration: 1, InstanceID: "instance-1", State: "starting"}); err != nil {
		t.Fatalf("starting heartbeat: %v", err)
	}
	if ready := service.Readiness("worker-1"); ready.CanClaim || ready.FreshHeartbeat {
		t.Fatalf("non-ready heartbeat readiness=%+v", ready)
	}
	if _, err := service.Heartbeat("worker-1", HeartbeatRequest{WorkerGeneration: 1, InstanceID: "instance-1", State: "ready"}); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if ready := service.Readiness("worker-1"); !ready.CanClaim || ready.State != "ready" {
		t.Fatalf("heartbeat readiness=%+v", ready)
	}
	clock = clock.Add(2 * time.Second)
	if ready := service.Readiness("worker-1"); ready.CanClaim || ready.FreshHeartbeat {
		t.Fatalf("expired readiness=%+v", ready)
	}
}

func TestServiceReadinessAndClaimsFailClosedWhenDependenciesAreUnavailable(t *testing.T) {
	service := New("contract-v1", time.Minute)
	service.SetDependencyReadinessEvaluator(dependencyReadinessFixture{err: errors.New("database unavailable")})
	registration := service.Register(RegistrationRequest{WorkerID: "worker-1", InstanceID: "instance-1", ProductID: "funding", Mode: "test", ContractVersion: "contract-v1"})

	ready := service.Readiness(registration.WorkerID)
	if ready.CanClaim || ready.DependenciesReady {
		t.Fatalf("Readiness()=%+v, want dependencies to fail closed", ready)
	}
	if status := service.Status(); status["ready"] != false {
		t.Fatalf("Status()=%+v, want ready=false", status)
	}
	_, err := service.Claim(ClaimRequest{WorkerID: registration.WorkerID, WorkerGeneration: registration.Generation, InstanceID: registration.InstanceID, IdempotencyKey: "claim-1", MaxItems: 1})
	if !errors.Is(err, ErrDependencyUnavailable) {
		t.Fatalf("Claim() error=%v, want %v", err, ErrDependencyUnavailable)
	}
}

func TestEmptyClaimDoesNotReserveIdempotencyKey(t *testing.T) {
	store := NewMemoryStoreWithSnapshot(nil, nil)
	service := NewWithStore("contract-v1", time.Minute, store)
	registration, err := service.RegisterResult(RegistrationRequest{
		WorkerID: "worker-empty", InstanceID: "instance-empty", ProductID: "funding", Mode: "test",
		ContractVersion: "contract-v1", IdempotencyKey: "register-empty",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Heartbeat(registration.WorkerID, HeartbeatRequest{WorkerGeneration: registration.Generation, InstanceID: registration.InstanceID, State: "ready"}); err != nil {
		t.Fatal(err)
	}

	request := ClaimRequest{WorkerID: registration.WorkerID, WorkerGeneration: registration.Generation, InstanceID: registration.InstanceID, IdempotencyKey: "claim-empty", MaxItems: 1}
	page, err := service.ClaimPage(request)
	if err != nil || page.WorkItem != nil || page.Lease != nil {
		t.Fatalf("empty claim=%+v err=%v", page, err)
	}
	store.mu.Lock()
	_, remembered := store.operations["claim\x00"+request.IdempotencyKey]
	store.mu.Unlock()
	if remembered {
		t.Fatal("empty claim reserved a canonical idempotency operation")
	}
}

func TestClaimPageWaitsForWorkThatBecomesAvailable(t *testing.T) {
	store := NewMemoryStoreWithSnapshot(nil, nil)
	service := NewWithStore("contract-v1", time.Minute, store)
	registration, err := service.RegisterResult(RegistrationRequest{
		WorkerID: "worker-wait", InstanceID: "instance-wait", ProductID: "funding", Mode: "test",
		ContractVersion: "contract-v1", IdempotencyKey: "register-wait",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Heartbeat(registration.WorkerID, HeartbeatRequest{WorkerGeneration: registration.Generation, InstanceID: registration.InstanceID, State: "ready"}); err != nil {
		t.Fatal(err)
	}

	go func() {
		time.Sleep(100 * time.Millisecond)
		now := time.Now().UTC()
		store.mu.Lock()
		store.workItems["work-later"] = WorkItem{
			WorkItemID: "work-later", WorkKind: "open", LineageID: "lineage-later", WorkRevision: 1,
			ProductID: "funding", Mode: "test", Status: "open", FactsCursor: "cursor-later",
			AccountFences: json.RawMessage(`[{"accountId":"account-long","desiredStateRevision":1,"runtimeGeneration":1,"credentialVersion":1},{"accountId":"account-short","desiredStateRevision":1,"runtimeGeneration":1,"credentialVersion":1}]`),
			Payload:       json.RawMessage(`{}`), MaxAttempts: 5, NotBefore: now, CreatedAt: now, UpdatedAt: now,
		}
		store.mu.Unlock()
	}()

	started := time.Now()
	page, err := service.ClaimPage(ClaimRequest{
		WorkerID: registration.WorkerID, WorkerGeneration: registration.Generation, InstanceID: registration.InstanceID,
		IdempotencyKey: "claim-wait", WaitSeconds: 1, MaxItems: 1,
	})
	elapsed := time.Since(started)
	if err != nil || page.WorkItem == nil || page.Lease == nil || page.WorkItem.WorkItemID != "work-later" {
		t.Fatalf("long-poll claim=%+v elapsed=%s err=%v", page, elapsed, err)
	}
	if elapsed < 100*time.Millisecond || elapsed >= time.Second {
		t.Fatalf("long-poll elapsed=%s, want work before the one-second deadline", elapsed)
	}
}

func TestClaimPageWaitDeadlineBoundsDependencyReadiness(t *testing.T) {
	store, registration := readyEmptyStore(t)
	service := NewWithStore("contract-v1", time.Minute, store)
	service.SetDependencyReadinessEvaluator(blockingDependencyReadiness{})
	assertClaimWaitDeadline(t, service, registration, "claim-blocked-dependency")
}

func TestClaimPageWaitDeadlineBoundsWorkerReadiness(t *testing.T) {
	store, registration := readyEmptyStore(t)
	service := NewWithStore("contract-v1", time.Minute, &blockingWorkerReadinessStore{MemoryStore: store})
	assertClaimWaitDeadline(t, service, registration, "claim-blocked-readiness")
}

func TestClaimPageWaitDeadlineBoundsStoreCall(t *testing.T) {
	store, registration := readyEmptyStore(t)
	service := NewWithStore("contract-v1", time.Minute, &blockingClaimStore{MemoryStore: store})
	assertClaimWaitDeadline(t, service, registration, "claim-blocked-store")
}

func TestClaimPageWaitDeadlineReturnsEmptyOnlyWhileIdle(t *testing.T) {
	store, registration := readyEmptyStore(t)
	service := NewWithStore("contract-v1", time.Minute, store)
	started := time.Now()
	page, err := service.ClaimPage(ClaimRequest{
		WorkerID: registration.WorkerID, WorkerGeneration: registration.Generation, InstanceID: registration.InstanceID,
		IdempotencyKey: "claim-idle-deadline", AfterCursor: "cursor-before-wait", WaitSeconds: 1, MaxItems: 1,
	})
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("idle claim error=%v elapsed=%s", err, elapsed)
	}
	if page.WorkItem != nil || page.Lease != nil || page.NextCursor != "cursor-before-wait" {
		t.Fatalf("idle claim page=%+v", page)
	}
	if elapsed < 900*time.Millisecond || elapsed >= 1250*time.Millisecond {
		t.Fatalf("idle claim elapsed=%s, want the one-second wait deadline", elapsed)
	}
}

func TestClaimPagePropagatesAmbiguousStoreErrorAtWaitDeadline(t *testing.T) {
	store, registration := readyEmptyStore(t)
	service := NewWithStore("contract-v1", time.Minute, &ambiguousDeadlineClaimStore{MemoryStore: store})
	started := time.Now()
	_, err := service.ClaimPage(ClaimRequest{
		WorkerID: registration.WorkerID, WorkerGeneration: registration.Generation, InstanceID: registration.InstanceID,
		IdempotencyKey: "claim-ambiguous-deadline", WaitSeconds: 1, MaxItems: 1,
	})
	elapsed := time.Since(started)
	if !errors.Is(err, errAmbiguousClaimCommit) {
		t.Fatalf("ambiguous claim error=%v elapsed=%s", err, elapsed)
	}
	if elapsed < 900*time.Millisecond || elapsed >= 1250*time.Millisecond {
		t.Fatalf("ambiguous claim elapsed=%s, want the one-second wait deadline", elapsed)
	}
}

func readyEmptyStore(t *testing.T) (*MemoryStore, Registration) {
	t.Helper()
	store := NewMemoryStoreWithSnapshot(nil, nil)
	service := NewWithStore("contract-v1", time.Minute, store)
	registration, err := service.RegisterResult(RegistrationRequest{
		WorkerID: "worker-blocked", InstanceID: "instance-blocked", ProductID: "funding", Mode: "test",
		ContractVersion: "contract-v1", IdempotencyKey: "register-blocked",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Heartbeat(registration.WorkerID, HeartbeatRequest{WorkerGeneration: registration.Generation, InstanceID: registration.InstanceID, State: "ready"}); err != nil {
		t.Fatal(err)
	}
	return store, registration
}

func assertClaimWaitDeadline(t *testing.T, service *Service, registration Registration, idempotencyKey string) {
	t.Helper()
	parent, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	started := time.Now()
	page, err := service.ClaimPageContext(parent, ClaimRequest{
		WorkerID: registration.WorkerID, WorkerGeneration: registration.Generation, InstanceID: registration.InstanceID,
		IdempotencyKey: idempotencyKey, AfterCursor: "cursor-before-wait", WaitSeconds: 1, MaxItems: 1,
	})
	elapsed := time.Since(started)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("bounded claim error=%v page=%+v elapsed=%s", err, page, elapsed)
	}
	if elapsed < 900*time.Millisecond || elapsed >= 1250*time.Millisecond {
		t.Fatalf("bounded claim elapsed=%s, want the one-second wait deadline", elapsed)
	}
}

func TestCompleteValidatesStoreAuthorityWithoutGatewayWiring(t *testing.T) {
	validatorErr := errors.New("completion authority incomplete")
	store := &completionValidatorStore{MemoryStore: NewMemoryStore(), err: validatorErr}
	service := NewWithStore("contract-v1", time.Minute, store)
	registration := service.Register(RegistrationRequest{WorkerID: "worker-1", InstanceID: "instance-1", ProductID: "funding", Mode: "test", ContractVersion: "contract-v1"})
	claims, err := service.Claim(ClaimRequest{WorkerID: registration.WorkerID, WorkerGeneration: registration.Generation, InstanceID: registration.InstanceID, IdempotencyKey: "claim-1", MaxItems: 1})
	if err != nil || len(claims) != 1 {
		t.Fatalf("Claim()=%+v error=%v", claims, err)
	}
	claim := claims[0]
	_, err = service.CompleteResult(claim.LeaseID, CompleteRequest{WorkerID: registration.WorkerID, WorkerGeneration: registration.Generation, WorkItemID: claim.WorkItemID, WorkRevision: claim.WorkRevision, LeaseGeneration: claim.LeaseGeneration, IdempotencyKey: "complete-1", FactsCursor: claim.FactsCursor, Result: json.RawMessage(`{"outcome":"completed"}`)})
	if !errors.Is(err, validatorErr) || store.calls != 1 {
		t.Fatalf("CompleteResult() error=%v validator calls=%d", err, store.calls)
	}
}

func TestFundingV1RequiresExactlyTwoAccountFences(t *testing.T) {
	if FundingV1AccountFenceCount != 2 {
		t.Fatalf("FundingV1AccountFenceCount=%d", FundingV1AccountFenceCount)
	}
	fence := `{"accountId":"account-a","desiredStateRevision":1,"runtimeGeneration":1,"credentialVersion":1}`
	for _, encoded := range []string{"[" + fence + "]", "[" + fence + "," + strings.Replace(fence, "account-a", "account-b", 1) + "," + strings.Replace(fence, "account-a", "account-c", 1) + "]"} {
		if _, err := DecodeAccountFences(json.RawMessage(encoded)); !errors.Is(err, ErrAuthorizationStale) {
			t.Fatalf("DecodeAccountFences(%s) error=%v", encoded, err)
		}
	}
}
