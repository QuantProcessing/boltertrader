package work_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/backend/internal/platform/orders"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/work"
	"github.com/QuantProcessing/boltertrader/backend/internal/products/funding"
	"github.com/QuantProcessing/boltertrader/backend/internal/products/funding/calculation"
	fundingexecution "github.com/QuantProcessing/boltertrader/backend/internal/products/funding/execution"
	"github.com/QuantProcessing/boltertrader/backend/internal/products/funding/planning"
	"github.com/shopspring/decimal"
)

type integrationGateway struct{ calls []orders.Command }

func (gateway *integrationGateway) Handoff(_ context.Context, command orders.Command) (orders.HandoffResult, error) {
	gateway.calls = append(gateway.calls, command)
	return orders.HandoffResult{Outcome: orders.HandoffAccepted}, nil
}

func TestProductionFundingServiceDerivesPlansAndReplaysWithoutDuplicateGatewayCalls(t *testing.T) {
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	item, planningFact := integrationWork(t, now, "test", 50)
	store := work.NewMemoryStoreWithSnapshot([]work.WorkItem{item}, []work.Fact{planningFact})
	plans, executions := work.NewMemoryFundingPlanStore(), work.NewMemoryFundingExecutionStore()
	commands := orders.NewService(orders.NewMemoryStore(), nil, func() time.Time { return now.Add(time.Second) })
	processor, _ := work.NewDurableFundingPayloadProcessor(commands, plans, executions, func() time.Time { return now.Add(time.Second) })
	producer, _ := fundingexecution.NewPlanProducer(plans, executions)
	gateway := &integrationGateway{}
	service := work.NewWithStoreGatewayProcessorAndPlanProducer("contract-v1", 15*time.Second, store, processor, producer, gateway)
	service.SetClock(func() time.Time { return now.Add(time.Second) })
	registration := service.Register(work.RegistrationRequest{WorkerID: "worker-1", InstanceID: "instance-1", ProductID: "funding", Mode: "test", ContractVersion: "contract-v1"})
	claims, err := service.Claim(work.ClaimRequest{WorkerID: registration.WorkerID, WorkerGeneration: registration.Generation, InstanceID: registration.InstanceID, IdempotencyKey: "claim-1", MaxItems: 1})
	if err != nil || len(claims) != 1 {
		t.Fatalf("claims=%+v err=%v", claims, err)
	}
	claim := claims[0]
	workerClaim := funding.Claim{WorkItemID: item.WorkItemID, WorkKind: item.WorkKind, LineageID: item.LineageID, ProductID: item.ProductID, Mode: item.Mode, WorkRevision: item.WorkRevision, LeaseID: claim.LeaseID, LeaseGeneration: claim.LeaseGeneration, FactsCursor: item.FactsCursor, AccountFences: item.AccountFences, Payload: item.Payload}
	workerFact := funding.Fact{FactID: planningFact.FactID, Cursor: planningFact.Cursor, ProductID: planningFact.ProductID, Mode: planningFact.Mode, Kind: planningFact.Kind, Payload: planningFact.Payload, EffectiveAt: planningFact.EffectiveAt}
	outcome, err := (funding.PayloadProcessor{}).Process(context.Background(), funding.Work{Claim: workerClaim, Facts: []funding.Fact{workerFact}})
	if err != nil || len(outcome.Intents) != 1 {
		t.Fatalf("worker outcome=%+v err=%v", outcome, err)
	}
	request := work.IntentRequest{WorkerID: registration.WorkerID, WorkerGeneration: registration.Generation, WorkItemID: item.WorkItemID, WorkRevision: item.WorkRevision, LeaseGeneration: claim.LeaseGeneration, IdempotencyKey: "open-1", IntentKind: outcome.Intents[0].Kind, FactsCursor: item.FactsCursor, AccountFences: item.AccountFences, Payload: outcome.Intents[0].Payload}
	stale := request
	stale.IdempotencyKey, stale.AccountFences = "open-stale", json.RawMessage(`[{"accountId":"account-long","desiredStateRevision":11,"runtimeGeneration":5,"credentialVersion":3},{"accountId":"account-short","desiredStateRevision":13,"runtimeGeneration":7,"credentialVersion":5}]`)
	if _, err := service.IntentResult(claim.LeaseID, stale); err == nil || len(gateway.calls) != 0 {
		t.Fatalf("stale fence err=%v calls=%d", err, len(gateway.calls))
	}
	first, err := service.IntentResult(claim.LeaseID, request)
	if err != nil || len(gateway.calls) != 1 || gateway.calls[0].CommandKind != "order.place" {
		t.Fatalf("first=%+v calls=%+v err=%v", first, gateway.calls, err)
	}
	if _, err := service.IntentResult(claim.LeaseID, request); err != nil || len(gateway.calls) != 1 {
		t.Fatalf("replay calls=%d err=%v", len(gateway.calls), err)
	}
	restarted := work.NewWithStoreGatewayProcessorAndPlanProducer("contract-v1", 15*time.Second, store, processor, producer, gateway)
	restarted.SetClock(func() time.Time { return now.Add(2 * time.Second) })
	if _, err := restarted.IntentResult(claim.LeaseID, request); err != nil || len(gateway.calls) != 1 {
		t.Fatalf("restart replay calls=%d err=%v", len(gateway.calls), err)
	}
	children := first["commands"].([]orders.Command)
	filled, err := commands.Transition(context.Background(), orders.TransitionRequest{CommandID: children[0].CommandID, State: orders.StateFilled})
	if err != nil || filled.State != orders.StateFilled {
		t.Fatalf("fill=%+v err=%v", filled, err)
	}
	owned, _ := commands.ListOwned(context.Background(), "platform:funding", orders.ModeTest, "funding")
	reconcileIDs := []string{owned[0].CommandID, owned[1].CommandID}
	reconcilePayload, _ := json.Marshal(map[string]any{"lineageId": item.LineageID, "commandIds": reconcileIDs, "reason": "authoritative_fill"})
	reconcile := request
	reconcile.IdempotencyKey, reconcile.IntentKind, reconcile.Payload = "reconcile-1", "funding.reconcile", reconcilePayload
	if _, err := restarted.IntentResult(claim.LeaseID, reconcile); err != nil || len(gateway.calls) != 2 || gateway.calls[1].CommandID == gateway.calls[0].CommandID {
		t.Fatalf("reconcile calls=%+v err=%v", gateway.calls, err)
	}
}

func TestProductionFundingServiceLiveIntentMakesNoGatewayCall(t *testing.T) {
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	item, fact := integrationWork(t, now, "live", 50)
	store := work.NewMemoryStoreWithSnapshot([]work.WorkItem{item}, []work.Fact{fact})
	plans, executions := work.NewMemoryFundingPlanStore(), work.NewMemoryFundingExecutionStore()
	commands := orders.NewService(orders.NewMemoryStore(), nil, time.Now)
	processor, _ := work.NewDurableFundingPayloadProcessor(commands, plans, executions, time.Now)
	producer, _ := fundingexecution.NewPlanProducer(plans, executions)
	gateway := &integrationGateway{}
	service := work.NewWithStoreGatewayProcessorAndPlanProducer("contract-v1", time.Minute, store, processor, producer, gateway)
	service.SetClock(func() time.Time { return now.Add(time.Second) })
	registration := service.Register(work.RegistrationRequest{WorkerID: "worker-live", InstanceID: "instance-live", ProductID: "funding", Mode: "live", ContractVersion: "contract-v1"})
	claims, _ := service.Claim(work.ClaimRequest{WorkerID: registration.WorkerID, WorkerGeneration: registration.Generation, InstanceID: registration.InstanceID, IdempotencyKey: "claim-live", MaxItems: 1})
	claim := claims[0]
	_, err := service.IntentResult(claim.LeaseID, work.IntentRequest{WorkerID: registration.WorkerID, WorkerGeneration: registration.Generation, WorkItemID: item.WorkItemID, WorkRevision: item.WorkRevision, LeaseGeneration: claim.LeaseGeneration, IdempotencyKey: "live-1", IntentKind: "funding.open", FactsCursor: item.FactsCursor, AccountFences: item.AccountFences, Payload: item.Payload})
	if err == nil || len(gateway.calls) != 0 {
		t.Fatalf("live err=%v calls=%d", err, len(gateway.calls))
	}
}

func TestFundingWorkerRejectsOversizedTestPlanBeforePlatformIntent(t *testing.T) {
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	item, planningFact := integrationWork(t, now, "test", 101)
	workerClaim := funding.Claim{WorkItemID: item.WorkItemID, WorkKind: item.WorkKind, LineageID: item.LineageID, ProductID: item.ProductID, Mode: item.Mode, WorkRevision: item.WorkRevision, FactsCursor: item.FactsCursor, AccountFences: item.AccountFences, Payload: item.Payload}
	workerFact := funding.Fact{FactID: planningFact.FactID, Cursor: planningFact.Cursor, ProductID: planningFact.ProductID, Mode: planningFact.Mode, Kind: planningFact.Kind, Payload: planningFact.Payload, EffectiveAt: planningFact.EffectiveAt}
	if outcome, err := (funding.PayloadProcessor{}).Process(context.Background(), funding.Work{Claim: workerClaim, Facts: []funding.Fact{workerFact}}); err == nil || len(outcome.Intents) != 0 {
		t.Fatalf("oversized outcome=%+v err=%v", outcome, err)
	}
}

func integrationWork(t *testing.T, now time.Time, mode string, notional int64) (work.WorkItem, work.Fact) {
	t.Helper()
	leg := func(account string, generation int, venue, instrument, rate string) calculation.LegFact {
		price := decimal.NewFromInt(100)
		return calculation.LegFact{AccountFence: calculation.AccountFence{AccountID: account, RuntimeGeneration: generation}, Venue: venue, VenueEnvironment: "test", InstrumentID: instrument, Symbol: "BTCUSDT", Current: calculation.FundingPoint{Rate: decimal.RequireFromString(rate), ObservedAt: now.Add(-time.Second), FundingTime: now, NextFunding: now.Add(8 * time.Hour)}, History: []calculation.FundingPoint{{Rate: decimal.RequireFromString(rate)}}, MarketObservedAt: now.Add(-time.Second), MarkPrice: price, BBO: calculation.Quote{BidPrice: price, BidSize: decimal.NewFromInt(10), AskPrice: price, AskSize: decimal.NewFromInt(10)}, Depth: calculation.Depth{Bids: []calculation.Level{{Price: price, Quantity: decimal.NewFromInt(10)}}, Asks: []calculation.Level{{Price: price, Quantity: decimal.NewFromInt(10)}}}, QuantityIncrement: decimal.RequireFromString("0.001"), MinQuantity: decimal.RequireFromString("0.001"), MinNotional: decimal.NewFromInt(5), TakerFeeRate: decimal.RequireFromString("0.0001")}
	}
	input := calculation.Input{Mode: "test", ProductID: "funding", OpportunityID: "opportunity-1", ObservedAt: now, RequestedNotionalPerLeg: decimal.NewFromInt(notional), Long: leg("account-long", 4, "venue-a", "btc-a", "0.0001"), Short: leg("account-short", 7, "venue-b", "btc-b", "0.0011")}
	reservedNotional, currency := input.RequestedNotionalPerLeg.String(), "USDT"
	authorities := []planning.ReservationAuthority{{ReservationID: "reservation-long", Revision: now.Add(-time.Minute), PrincipalID: "platform:funding", Mode: mode, ProductID: "funding", Kind: "open", AccountID: "account-long", Venue: "venue-a", VenueEnvironment: "test", InstrumentID: "btc-a", Notional: &reservedNotional, Currency: &currency, Status: "held"}, {ReservationID: "reservation-short", Revision: now.Add(-time.Minute), PrincipalID: "platform:funding", Mode: mode, ProductID: "funding", Kind: "open", AccountID: "account-short", Venue: "venue-b", VenueEnvironment: "test", InstrumentID: "btc-b", Notional: &reservedNotional, Currency: &currency, Status: "held"}}
	planningFact := planning.NewPlanningFact(input, "platform:funding", "reservation-long", "reservation-short", authorities, nil)
	input = planningFact.Input
	plan, _ := calculation.Compute(input)
	payload, _ := json.Marshal(planning.OpenIntent{PreviewID: plan.PreviewID, OpportunityID: plan.OpportunityID, LongAccountID: "account-long", ShortAccountID: "account-short", CanonicalMarketID: plan.RouteID, NotionalPerLeg: decimal.NewFromInt(notional).String(), MaxSlippageBps: "10", HoldThroughSettlement: true})
	planningPayload, _ := json.Marshal(planningFact)
	fences := json.RawMessage(`[{"accountId":"account-long","desiredStateRevision":11,"runtimeGeneration":4,"credentialVersion":3},{"accountId":"account-short","desiredStateRevision":13,"runtimeGeneration":7,"credentialVersion":5}]`)
	item := work.WorkItem{WorkItemID: "work-integration", WorkKind: "open", LineageID: "lineage-integration", WorkRevision: 1, ProductID: "funding", Mode: mode, Status: "open", FactsCursor: "cursor-integration", AccountFences: fences, Payload: payload, MaxAttempts: 5, NotBefore: now, CreatedAt: now, UpdatedAt: now}
	fact := work.Fact{FactID: "fact-planning", Cursor: item.FactsCursor, ProductID: "funding", Mode: mode, Kind: planning.FactKind, Payload: planningPayload, EffectiveAt: now}
	return item, fact
}
