package work

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/backend/internal/platform/ledger"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/orders"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/portfolio"
	fundingpolicy "github.com/QuantProcessing/boltertrader/backend/internal/products/funding/policy"
	"github.com/shopspring/decimal"
)

func TestFundingPayloadProcessorCreatesAndReplaysDurableOpenChildren(t *testing.T) {
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	payload := frozenOpenPayload("50")
	plans := NewMemoryFundingPlanStore()
	_, replay, err := plans.ReserveFundingPlan(context.Background(), openPlan(now, payload))
	if err != nil || replay {
		t.Fatalf("ReserveFundingPlan() replay=%v err=%v", replay, err)
	}
	commandService := orders.NewService(orders.NewMemoryStore(), nil, func() time.Time { return now })
	processor, err := NewFundingPayloadProcessor(commandService, plans, func() time.Time { return now.Add(time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	parent := fundingParent("funding.open", payload)
	first, err := processor.Process(context.Background(), parent)
	if err != nil || len(first) != 1 {
		t.Fatalf("Process()=%+v err=%v", first, err)
	}
	owned, err := commandService.ListOwned(context.Background(), parent.PrincipalID, parent.Mode, parent.ProductID)
	if err != nil || len(owned) != 2 {
		t.Fatalf("ListOwned()=%+v err=%v", owned, err)
	}
	for _, child := range owned {
		if child.CommandID == "" || child.CommandKind != "order.place" || child.LineageID != parent.LineageID || child.PayloadHash == parent.PayloadHash {
			t.Fatalf("child=%+v", child)
		}
		var normalized map[string]any
		if json.Unmarshal(child.Payload, &normalized) != nil || normalized["instrumentId"] == nil || normalized["quantity"] == nil || normalized["reduceOnly"] != false {
			t.Fatalf("normalized payload=%s", child.Payload)
		}
	}
	restarted, _ := NewFundingPayloadProcessor(commandService, plans, func() time.Time { return now.Add(2 * time.Second) })
	second, err := restarted.Process(context.Background(), parent)
	if err != nil || len(second) != 1 || second[0].CommandID != first[0].CommandID {
		t.Fatalf("replay=%+v first=%+v err=%v", second, first, err)
	}
}

func TestFundingExecutionIDsArePrincipalScopedForSharedLineage(t *testing.T) {
	now := time.Date(2026, 8, 6, 4, 45, 0, 0, time.UTC)
	processor := &FundingPayloadProcessor{executions: NewMemoryFundingExecutionStore(), now: func() time.Time { return now }}
	plan := FundingCommandPlan{PlanKey: "open:preview", PreviewHash: "preview-hash", IntentKind: "funding.open"}
	var ids []string
	for _, principal := range []string{"browser-a", "browser-b"} {
		execution, err := processor.reserveExecution(context.Background(), orders.Command{CommandID: "parent-" + principal, PrincipalID: principal, Mode: orders.ModeTest, ProductID: "funding", LineageID: "shared-lineage", CommandKind: "funding.open"}, plan, nil)
		if err != nil {
			t.Fatalf("principal %s reserveExecution() err=%v", principal, err)
		}
		ids = append(ids, execution.ExecutionID)
	}
	if ids[0] == ids[1] {
		t.Fatalf("principal-scoped execution IDs collided: %q", ids[0])
	}
}

func TestFundingPayloadProcessorAllowsConfiguredLiveWithoutGateway(t *testing.T) {
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	admission, err := fundingpolicy.NewAdmission(true, decimal.NewFromInt(250))
	if err != nil {
		t.Fatal(err)
	}
	payload := frozenOpenPayload("150")
	plan := openPlan(now, payload)
	plan.Mode = orders.ModeLive
	for index := range plan.Legs {
		plan.Legs[index].Notional = "150"
		plan.Legs[index].VenueEnvironment = "live"
	}
	plans := NewMemoryFundingPlanStore().WithAdmission(admission)
	if _, _, err := plans.ReserveFundingPlan(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	commands := orders.NewService(orders.NewMemoryStore(), nil, func() time.Time { return now })
	commands.SetLiveWritesEnabled(true)
	processor, err := NewFundingPayloadProcessor(commands, plans, func() time.Time { return now.Add(time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	processor.WithAdmission(admission)
	parent := fundingParent("funding.open", payload)
	parent.Mode = orders.ModeLive
	children, err := processor.Process(context.Background(), parent)
	if err != nil || len(children) != 1 || children[0].Mode != orders.ModeLive {
		t.Fatalf("Live children=%+v err=%v", children, err)
	}
}

func TestFundingPayloadProcessorRejectsUnfrozenOrUnsafePlans(t *testing.T) {
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	tests := map[string]struct {
		payload json.RawMessage
		plan    FundingCommandPlan
	}{
		"notional ceiling":    {payload: frozenOpenPayload("101"), plan: openPlan(now, frozenOpenPayload("101"))},
		"changed frozen body": {payload: frozenOpenPayload("49"), plan: openPlan(now, frozenOpenPayload("50"))},
		"expired plan": {payload: frozenOpenPayload("50"), plan: func() FundingCommandPlan {
			plan := openPlan(now, frozenOpenPayload("50"))
			plan.ExpiresAt = now.Add(time.Second)
			return plan
		}()},
		"unknown field": {payload: json.RawMessage(`{"previewId":"preview-1","opportunityId":"opportunity-1","longAccountId":"account-long","shortAccountId":"account-short","canonicalMarketId":"BTC-USDT-PERP","notionalPerLeg":"50","maxSlippageBps":"10","holdThroughSettlement":true,"rawOrder":{}}`), plan: openPlan(now, frozenOpenPayload("50"))},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			plans := NewMemoryFundingPlanStore()
			_, _, _ = plans.ReserveFundingPlan(context.Background(), test.plan)
			processor, _ := NewFundingPayloadProcessor(orders.NewService(orders.NewMemoryStore(), nil, func() time.Time { return now }), plans, func() time.Time { return now.Add(2 * time.Second) })
			if children, err := processor.Process(context.Background(), fundingParent("funding.open", test.payload)); err == nil || len(children) != 0 {
				t.Fatalf("Process()=%+v err=%v", children, err)
			}
		})
	}
}

func TestFundingPayloadProcessorEnforcesReduceOnlyCloseAndFrozenKinds(t *testing.T) {
	for name, test := range map[string]struct {
		kind    string
		payload json.RawMessage
	}{
		"open":       {"funding.open", frozenOpenPayload("50")},
		"close":      {"funding.close", json.RawMessage(`{"lineageId":"lineage_0001","reason":"operator"}`)},
		"compensate": {"funding.compensate", json.RawMessage(`{"lineageId":"lineage_0001","failedCommandId":"failed-1","survivingCommandId":"surviving-1","maxCompensationNotional":"25","reason":"hedge"}`)},
		"reconcile":  {"funding.reconcile", json.RawMessage(`{"lineageId":"lineage_0001","commandIds":["command-1","command-2"],"reason":"restart"}`)},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeFundingIntent(test.kind, test.payload); err != nil {
				t.Fatalf("decodeFundingIntent() err=%v", err)
			}
		})
	}
	for name, test := range map[string]struct {
		kind    string
		payload json.RawMessage
	}{
		"open":       {"funding.open", append(frozenOpenPayload("50"), []byte(` {}`)...)},
		"close":      {"funding.close", json.RawMessage(`{"lineageId":"lineage_0001","reason":"operator"} {}`)},
		"compensate": {"funding.compensate", json.RawMessage(`{"lineageId":"lineage_0001","failedCommandId":"failed-1","survivingCommandId":"surviving-1","maxCompensationNotional":"25","reason":"hedge"} {}`)},
		"reconcile":  {"funding.reconcile", json.RawMessage(`{"lineageId":"lineage_0001","commandIds":["command-1"],"reason":"restart"} {}`)},
	} {
		t.Run(name+" trailing JSON", func(t *testing.T) {
			if _, err := decodeFundingIntent(test.kind, test.payload); err == nil {
				t.Fatal("trailing JSON accepted")
			}
		})
	}
	intent, _ := decodeFundingIntent("funding.close", json.RawMessage(`{"lineageId":"lineage_0001","reason":"operator"}`))
	parent := fundingParent("funding.close", json.RawMessage(`{"lineageId":"lineage_0001","reason":"operator"}`))
	drafts := []FundingOrderDraft{{LegID: "long", AccountID: "account-long", ReservationID: "reservation-close", InstrumentID: "BTC-USDT", Side: "sell", Quantity: "1", OrderType: "market", ReduceOnly: false}}
	if err := validateFundingDrafts(parent, intent, drafts); err == nil {
		t.Fatal("non-reduce-only close plan accepted")
	}
}

func TestIntentResultUsesStoredPlanAndNeverRepeatsPartialHandoff(t *testing.T) {
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	payload := frozenOpenPayload("50")
	plans := NewMemoryFundingPlanStore()
	_, _, err := plans.ReserveFundingPlan(context.Background(), openPlan(now, payload))
	if err != nil {
		t.Fatal(err)
	}
	children := orders.NewService(orders.NewMemoryStore(), nil, func() time.Time { return now })
	processor, _ := NewFundingPayloadProcessor(children, plans, func() time.Time { return now.Add(time.Second) })
	gateway := &partialGatewayFixture{failAt: 1}
	store := NewMemoryStore()
	service := NewWithStoreGatewayAndProcessor("contract-v1", 10*time.Second, store, processor, gateway)
	service.SetClock(func() time.Time { return now })
	registration := service.Register(RegistrationRequest{WorkerID: "worker-1", InstanceID: "instance-1", ProductID: "funding", Mode: "test", ContractVersion: "contract-v1"})
	claims, _ := service.Claim(ClaimRequest{WorkerID: registration.WorkerID, WorkerGeneration: registration.Generation, InstanceID: registration.InstanceID, IdempotencyKey: "claim-1", MaxItems: 1})
	claim := claims[0]
	req := IntentRequest{WorkerID: registration.WorkerID, WorkerGeneration: registration.Generation, WorkItemID: claim.WorkItemID, WorkRevision: claim.WorkRevision, LeaseGeneration: claim.LeaseGeneration, IdempotencyKey: "economic-open-1", IntentKind: "funding.open", FactsCursor: claim.FactsCursor, AccountFences: claim.AccountFences, Payload: payload}
	first, err := service.IntentResult(claim.LeaseID, req)
	if err != nil || first["command"].(orders.Command).State != orders.StateUncertain || len(gateway.calls) != 1 {
		_, processErr := processor.Process(context.Background(), first["command"].(orders.Command))
		t.Fatalf("first=%+v calls=%d err=%v processErr=%v", first, len(gateway.calls), err, processErr)
	}
	if gateway.calls[0].CommandKind != "order.place" || gateway.calls[0].CommandID == first["command"].(orders.Command).CommandID {
		t.Fatalf("Gateway received parent or non-normalized command: %+v", gateway.calls[0])
	}
	commands := first["commands"].([]orders.Command)
	if len(commands) != 1 || commands[0].State != orders.StateUncertain {
		t.Fatalf("child states=%+v", commands)
	}
	storedFirst, _ := children.Get(context.Background(), commands[0].CommandID)
	if storedFirst.State != orders.StateUncertain {
		t.Fatalf("stored child state=%+v", storedFirst)
	}
	replay, err := service.IntentResult(claim.LeaseID, req)
	if err != nil || replay["command"].(orders.Command).State != orders.StateUncertain || len(gateway.calls) != 1 {
		t.Fatalf("replay=%+v calls=%d err=%v", replay, len(gateway.calls), err)
	}
}

func TestFundingHandoffPersistsGatewayEvidenceAndRejectsWithoutUncertainty(t *testing.T) {
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	payload := frozenOpenPayload("50")
	plans := NewMemoryFundingPlanStore()
	_, _, _ = plans.ReserveFundingPlan(context.Background(), openPlan(now, payload))
	commands := orders.NewService(orders.NewMemoryStore(), nil, func() time.Time { return now })
	executions := NewMemoryFundingExecutionStore()
	processor, _ := NewDurableFundingPayloadProcessor(commands, plans, executions, func() time.Time { return now.Add(time.Second) })
	positions := portfolio.NewProjection()
	ledgerService := ledger.NewService(ledger.NewMemoryStore(), func() time.Time { return now })
	processor.WithExecutionEvidence(positions, ledgerService)
	parent := fundingParent("funding.open", payload)
	ready, _ := processor.Process(context.Background(), parent)

	accepted, err := processor.RecordHandoff(context.Background(), ready[0], orders.HandoffResult{
		Outcome: orders.HandoffAccepted,
		Evidence: orders.ExecutionEvidence{CommandID: "gateway-command", GatewayOrderID: "gateway-order", ClientOrderID: "client-order", State: "partially_filled", ResourceVersion: 7, Fills: []orders.FillEvidence{{
			FillID: "fill-1", AccountID: ready[0].AccountID, InstrumentID: "BTC-USDT-LONG", Side: "buy", Quantity: "0.005", Price: "100", Fee: "0.01", FeeCurrency: "USDT", SettlementCurrency: "USDT", OccurredAt: now,
		}}},
	})
	if err != nil || accepted.State != orders.StateSubmitted || accepted.ExecutionEvidence.GatewayOrderID != "gateway-order" || accepted.ExecutionEvidence.ResourceVersion != 7 {
		t.Fatalf("accepted=%+v err=%v", accepted, err)
	}
	execution, _ := executions.GetFundingExecution(context.Background(), parent.PrincipalID, parent.Mode, parent.LineageID)
	if execution.Legs[0].ExecutionEvidence.GatewayOrderID != "gateway-order" {
		t.Fatalf("execution leg=%+v", execution.Legs[0])
	}
	if replay, replayErr := processor.RecordHandoff(context.Background(), ready[0], orders.HandoffResult{Outcome: orders.HandoffAccepted, Evidence: accepted.ExecutionEvidence}); replayErr != nil || replay.CommandID != accepted.CommandID {
		t.Fatalf("evidence replay=%+v err=%v", replay, replayErr)
	}
	if got := positions.List(portfolio.ModeTest, ready[0].AccountID, "funding"); len(got) != 1 || got[0].Quantity != "0.005" {
		t.Fatalf("positions=%+v", got)
	}
	entries, _ := ledgerService.List(context.Background(), ledger.Query{Mode: ledger.ModeTest, ProductID: "funding", LineageID: parent.LineageID, AccountID: ready[0].AccountID, InstrumentID: "BTC-USDT-LONG"})
	if len(entries) != 1 || entries[0].EntryID != "fill-1:fee" {
		t.Fatalf("ledger entries=%+v", entries)
	}

	ready, _ = processor.Process(context.Background(), parent)
	rejected, err := processor.RecordHandoff(context.Background(), ready[0], orders.HandoffResult{Outcome: orders.HandoffRejected})
	if err != nil || rejected.State != orders.StateFailed {
		t.Fatalf("rejected=%+v err=%v", rejected, err)
	}
}

func TestFundingReconcileReleasesSecondLegOnlyAfterAuthoritativeFirstFill(t *testing.T) {
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	payload := frozenOpenPayload("50")
	plans := NewMemoryFundingPlanStore()
	_, _, _ = plans.ReserveFundingPlan(context.Background(), openPlan(now, payload))
	children := orders.NewService(orders.NewMemoryStore(), nil, func() time.Time { return now })
	executions := NewMemoryFundingExecutionStore()
	processor, _ := NewDurableFundingPayloadProcessor(children, plans, executions, func() time.Time { return now.Add(time.Second) })
	parent := fundingParent("funding.open", payload)
	ready, err := processor.Process(context.Background(), parent)
	if err != nil || len(ready) != 1 {
		t.Fatalf("initial=%+v err=%v", ready, err)
	}
	first, err := processor.RecordHandoff(context.Background(), ready[0], orders.HandoffResult{Outcome: orders.HandoffAccepted})
	if err != nil || first.State != orders.StateSubmitted {
		t.Fatalf("first handoff=%+v err=%v", first, err)
	}
	owned, _ := children.ListOwned(context.Background(), parent.PrincipalID, parent.Mode, parent.ProductID)
	secondID := owned[0].CommandID
	if secondID == first.CommandID {
		secondID = owned[1].CommandID
	}
	reconcilePayload := json.RawMessage(`{"lineageId":"lineage_0001","commandIds":["` + first.CommandID + `","` + secondID + `"],"reason":"restart_before_fill"}`)
	reconcile := fundingParent("funding.reconcile", reconcilePayload)
	reconcile.CommandID = "command-reconcile-before-fill"
	ready, err = processor.Process(context.Background(), reconcile)
	if err != nil || len(ready) != 1 || ready[0].CommandID != first.CommandID || ready[0].State != orders.StateSubmitted {
		t.Fatalf("pre-fill reconcile=%+v err=%v", ready, err)
	}
	first, err = children.Transition(context.Background(), orders.TransitionRequest{CommandID: first.CommandID, State: orders.StateFilled})
	if err != nil {
		t.Fatal(err)
	}
	reconcilePayload = json.RawMessage(`{"lineageId":"lineage_0001","commandIds":["` + first.CommandID + `","` + secondID + `"],"reason":"authoritative_fill"}`)
	reconcile = fundingParent("funding.reconcile", reconcilePayload)
	reconcile.CommandID = "command-reconcile"
	ready, err = processor.Process(context.Background(), reconcile)
	if err != nil || len(ready) != 1 || ready[0].CommandID != secondID || ready[0].State != orders.StateAccepted {
		t.Fatalf("reconcile=%+v err=%v", ready, err)
	}
}

func TestFundingPartialCancelsBeforeOneBoundedReduceOnlyCompensation(t *testing.T) {
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	payload := frozenOpenPayload("50")
	plans := NewMemoryFundingPlanStore()
	_, _, _ = plans.ReserveFundingPlan(context.Background(), openPlan(now, payload))
	commands := orders.NewService(orders.NewMemoryStore(), nil, func() time.Time { return now })
	executions := NewMemoryFundingExecutionStore()
	processor, _ := NewDurableFundingPayloadProcessor(commands, plans, executions, func() time.Time { return now.Add(time.Second) })
	parent := fundingParent("funding.open", payload)
	ready, _ := processor.Process(context.Background(), parent)
	first, _ := processor.RecordHandoff(context.Background(), ready[0], orders.HandoffResult{Outcome: orders.HandoffAccepted})
	cancel, err := processor.RecordFundingPartial(context.Background(), parent.PrincipalID, parent.Mode, parent.LineageID, first.CommandID, "0.005", "facts-2")
	if err != nil || cancel.CommandKind != "order.cancel" {
		t.Fatalf("cancel=%+v err=%v", cancel, err)
	}
	if _, err := processor.RecordFundingCancellation(context.Background(), parent.PrincipalID, parent.Mode, parent.LineageID, cancel.CommandID, "25"); err == nil {
		t.Fatal("compensation created before authoritative cancellation")
	}
	cancel, err = commands.Transition(context.Background(), orders.TransitionRequest{CommandID: cancel.CommandID, State: orders.StateCanceled})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := processor.RecordFundingCancellation(context.Background(), parent.PrincipalID, parent.Mode, parent.LineageID, cancel.CommandID, "24"); err == nil {
		t.Fatal("compensation exceeded frozen notional cap")
	}
	compensation, err := processor.RecordFundingCancellation(context.Background(), parent.PrincipalID, parent.Mode, parent.LineageID, cancel.CommandID, "25")
	if err != nil || compensation.CommandKind != "order.place" {
		t.Fatalf("compensation=%+v err=%v", compensation, err)
	}
	var normalized map[string]any
	if json.Unmarshal(compensation.Payload, &normalized) != nil || normalized["reduceOnly"] != true || normalized["quantity"] != "0.005" || normalized["side"] != "sell" {
		t.Fatalf("compensation payload=%s", compensation.Payload)
	}
	replay, err := processor.RecordFundingCancellation(context.Background(), parent.PrincipalID, parent.Mode, parent.LineageID, cancel.CommandID, "25")
	if err != nil || replay.CommandID != compensation.CommandID {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	uncertain, err := processor.RecordHandoff(context.Background(), compensation, orders.HandoffResult{Outcome: orders.HandoffOutcomeUnknown})
	if err != nil || uncertain.State != orders.StateUncertain {
		t.Fatalf("uncertain=%+v err=%v", uncertain, err)
	}
	replay, err = processor.RecordFundingCancellation(context.Background(), parent.PrincipalID, parent.Mode, parent.LineageID, cancel.CommandID, "25")
	if err != nil || replay.CommandID != compensation.CommandID || replay.State != orders.StateUncertain {
		t.Fatalf("uncertain replay=%+v err=%v", replay, err)
	}
}

type partialGatewayFixture struct {
	calls  []orders.Command
	failAt int
}

func (gateway *partialGatewayFixture) Handoff(_ context.Context, command orders.Command) (orders.HandoffResult, error) {
	gateway.calls = append(gateway.calls, command)
	if len(gateway.calls) == gateway.failAt {
		return orders.HandoffResult{Outcome: orders.HandoffOutcomeUnknown}, errors.New("ambiguous handoff")
	}
	return orders.HandoffResult{Outcome: orders.HandoffAccepted}, nil
}

func frozenOpenPayload(notional string) json.RawMessage {
	return json.RawMessage(`{"previewId":"preview-1","opportunityId":"opportunity-1","longAccountId":"account-long","shortAccountId":"account-short","canonicalMarketId":"BTC-USDT-PERP","notionalPerLeg":"` + notional + `","maxSlippageBps":"10","holdThroughSettlement":true}`)
}

func openPlan(now time.Time, payload json.RawMessage) FundingCommandPlan {
	return FundingCommandPlan{
		PlanKey: "open:preview-1", PreviewHash: "preview-hash-1", PrincipalID: "platform:funding", Mode: orders.ModeTest,
		IntentKind: "funding.open", IntentPayload: payload, LineageID: "lineage_0001",
		Legs: []FundingOrderDraft{
			{LegID: "long", AccountID: "account-long", RuntimeGeneration: 1, Venue: "venue-long", VenueEnvironment: "test", ReservationID: "reservation-long", InstrumentID: "BTC-USDT-LONG", Side: "buy", Quantity: "0.01", Notional: "50", OrderType: "market"},
			{LegID: "short", AccountID: "account-short", RuntimeGeneration: 1, Venue: "venue-short", VenueEnvironment: "test", ReservationID: "reservation-short", InstrumentID: "BTC-USDT-SHORT", Side: "sell", Quantity: "0.01", Notional: "50", OrderType: "market"},
		},
		ExpiresAt: now.Add(30 * time.Second), CreatedAt: now,
	}
}

func fundingParent(kind string, payload json.RawMessage) orders.Command {
	return orders.Command{CommandID: "command-parent", PrincipalID: "platform:funding", AccountID: "account-long", ProductID: "funding", LineageID: "lineage_0001", Mode: orders.ModeTest, CommandKind: kind, State: orders.StateAccepted, Path: "/internal/v1/funding-work/leases/lease-1/command-intents", Payload: payload, PayloadHash: CanonicalRequestHash(payload)}
}
