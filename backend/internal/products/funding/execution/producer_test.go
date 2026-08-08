package fundingexecution

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/backend/internal/platform/ledger"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/orders"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/portfolio"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/work"
	"github.com/QuantProcessing/boltertrader/backend/internal/products/funding/accounting"
	"github.com/QuantProcessing/boltertrader/backend/internal/products/funding/calculation"
	"github.com/QuantProcessing/boltertrader/backend/internal/products/funding/planning"
	"github.com/shopspring/decimal"
)

func TestPlanProducerDerivesAndReplaysOpenFromAuthoritativeFacts(t *testing.T) {
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	input := producerInput(now)
	planningFact := producerPlanningFact(input)
	input = planningFact.Input
	computed, _ := calculation.Compute(input)
	request := planning.OpenIntent{PreviewID: computed.PreviewID, OpportunityID: computed.OpportunityID, LongAccountID: "account-long", ShortAccountID: "account-short", CanonicalMarketID: computed.RouteID, NotionalPerLeg: "50", MaxSlippageBps: "10", HoldThroughSettlement: true}
	payload, _ := json.Marshal(request)
	factPayload, _ := json.Marshal(planningFact)
	fences := json.RawMessage(`[{"accountId":"account-long","desiredStateRevision":11,"runtimeGeneration":4,"credentialVersion":3},{"accountId":"account-short","desiredStateRevision":13,"runtimeGeneration":7,"credentialVersion":5}]`)
	record := work.IntentRecord{Command: orders.Command{CommandID: "parent-1", PrincipalID: "platform:funding", ProductID: "funding", Mode: orders.ModeTest, LineageID: "lineage-1", CommandKind: "funding.open", Payload: payload, CreatedAt: now}, WorkPayload: payload, AccountFences: fences, Facts: []work.Fact{{Kind: planning.FactKind, Payload: factPayload}}}
	plans := work.NewMemoryFundingPlanStore()
	executions := work.NewMemoryFundingExecutionStore()
	producer, _ := NewPlanProducer(plans, executions)
	if err := producer.Prepare(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if err := producer.Prepare(context.Background(), record); err != nil {
		t.Fatalf("replay: %v", err)
	}
	stored, err := plans.GetFundingPlan(context.Background(), "platform:funding", orders.ModeTest, "open:"+computed.PreviewID)
	if err != nil || len(stored.Legs) != 2 || stored.Legs[0].ReduceOnly || stored.Legs[1].ReduceOnly || stored.PreviewHash != computed.NormalizedHash {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
	changed := record
	changed.AccountFences = json.RawMessage(`[{"accountId":"account-long","desiredStateRevision":11,"runtimeGeneration":5,"credentialVersion":3},{"accountId":"account-short","desiredStateRevision":13,"runtimeGeneration":7,"credentialVersion":5}]`)
	if err := producer.Prepare(context.Background(), changed); err == nil {
		t.Fatal("changed account fence was accepted")
	}
}

func TestPlanProducerPreservesRequestedNotionalWhenExecutableQuantityRoundsDown(t *testing.T) {
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	input := producerInput(now)
	input.Long.MarkPrice = decimal.NewFromInt(101)
	input.Long.BBO.BidPrice, input.Long.BBO.AskPrice = input.Long.MarkPrice, input.Long.MarkPrice
	input.Long.Depth.Bids[0].Price, input.Long.Depth.Asks[0].Price = input.Long.MarkPrice, input.Long.MarkPrice
	input.Short.MarkPrice = decimal.NewFromInt(103)
	input.Short.BBO.BidPrice, input.Short.BBO.AskPrice = input.Short.MarkPrice, input.Short.MarkPrice
	input.Short.Depth.Bids[0].Price, input.Short.Depth.Asks[0].Price = input.Short.MarkPrice, input.Short.MarkPrice
	planningFact := producerPlanningFact(input)
	input = planningFact.Input
	computed, err := calculation.Compute(input)
	if err != nil || !computed.CanSubmit || computed.Legs[0].Notional.Equal(input.RequestedNotionalPerLeg) || computed.Legs[1].Notional.Equal(input.RequestedNotionalPerLeg) {
		t.Fatalf("computed=%+v err=%v", computed, err)
	}
	request := planning.OpenIntent{PreviewID: computed.PreviewID, OpportunityID: computed.OpportunityID, LongAccountID: "account-long", ShortAccountID: "account-short", CanonicalMarketID: computed.RouteID, NotionalPerLeg: "50", MaxSlippageBps: "10", HoldThroughSettlement: true}
	payload, _ := json.Marshal(request)
	factPayload, _ := json.Marshal(planningFact)
	record := work.IntentRecord{
		Command:       orders.Command{CommandID: "parent-rounded", PrincipalID: "platform:funding", ProductID: "funding", Mode: orders.ModeTest, LineageID: "lineage-rounded", CommandKind: "funding.open", Payload: payload, CreatedAt: now},
		WorkPayload:   payload,
		AccountFences: json.RawMessage(`[{"accountId":"account-long","desiredStateRevision":11,"runtimeGeneration":4,"credentialVersion":3},{"accountId":"account-short","desiredStateRevision":13,"runtimeGeneration":7,"credentialVersion":5}]`),
		Facts:         []work.Fact{{Kind: planning.FactKind, Payload: factPayload}},
	}
	plans := work.NewMemoryFundingPlanStore()
	producer, _ := NewPlanProducer(plans, work.NewMemoryFundingExecutionStore())
	if err := producer.Prepare(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	stored, err := plans.GetFundingPlan(context.Background(), "platform:funding", orders.ModeTest, "open:"+computed.PreviewID)
	if err != nil || !work.EqualJSON(stored.IntentPayload, payload) {
		t.Fatalf("stored payload=%s requested=%s err=%v", stored.IntentPayload, payload, err)
	}
}

func TestPlanProducerDerivesReduceOnlyCloseAndCompensation(t *testing.T) {
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	plans := work.NewMemoryFundingPlanStore()
	executions := work.NewMemoryFundingExecutionStore()
	producer, _ := NewPlanProducer(plans, executions)
	openDrafts := []work.FundingOrderDraft{
		{LegID: "long", AccountID: "account-long", RuntimeGeneration: 4, Venue: "venue-a", VenueEnvironment: "test", ReservationID: "open-long", InstrumentID: "btc-a", Side: "buy", Quantity: "0.5", Notional: "50", OrderType: "market"},
		{LegID: "short", AccountID: "account-short", RuntimeGeneration: 7, Venue: "venue-b", VenueEnvironment: "test", ReservationID: "open-short", InstrumentID: "btc-b", Side: "sell", Quantity: "0.5", Notional: "50", OrderType: "market"},
	}
	_, _, err := executions.ReserveFundingExecution(context.Background(), work.FundingExecution{ExecutionID: "funding:lineage-1", PrincipalID: "platform:funding", Mode: orders.ModeTest, LineageID: "lineage-1", ParentCommandID: "parent-open", PlanKey: "open:preview-1", PreviewHash: "hash", IntentKind: "funding.open", State: work.FundingExecutionHedged, Legs: []work.FundingExecutionLeg{{Role: "long", Draft: openDrafts[0], CommandID: "command-long", CommandState: orders.StateFilled, FilledQuantity: "0.25"}, {Role: "short", Draft: openDrafts[1], CommandID: "command-short", CommandState: orders.StateFilled}}, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	facts := []work.Fact{
		{Kind: "funding.unwind-reservation", AccountID: "account-long", Payload: unwindAuthorityPayload(t, now, "close-long", "account-long", "venue-a", "btc-a", "0.5"), EffectiveAt: now},
		{Kind: "funding.unwind-reservation", AccountID: "account-short", Payload: unwindAuthorityPayload(t, now, "close-short", "account-short", "venue-b", "btc-b", "0.5"), EffectiveAt: now},
	}
	closePayload, _ := json.Marshal(work.FundingCloseIntent{LineageID: "lineage-1", Reason: "operator", RequestedAt: now})
	closeRecord := work.IntentRecord{Command: orders.Command{CommandID: "parent-close", PrincipalID: "platform:funding", ProductID: "funding", Mode: orders.ModeTest, LineageID: "lineage-1", CommandKind: "funding.close", Payload: closePayload, CreatedAt: now}, WorkPayload: closePayload, Facts: facts}
	if err := producer.Prepare(context.Background(), closeRecord); err != nil {
		t.Fatal(err)
	}
	closePlan, _ := plans.GetFundingPlan(context.Background(), "platform:funding", orders.ModeTest, "close:lineage-1")
	if len(closePlan.Legs) != 2 || !closePlan.Legs[0].ReduceOnly || !closePlan.Legs[1].ReduceOnly || closePlan.Legs[0].Side != "sell" || closePlan.Legs[1].Side != "buy" {
		t.Fatalf("close plan=%+v", closePlan)
	}
	incomplete := closeRecord
	incomplete.Facts = []work.Fact{{Kind: "funding.unwind-reservation", AccountID: "account-long", Payload: json.RawMessage(`{"reservationId":"close-long"}`)}}
	if err := producer.Prepare(context.Background(), incomplete); err == nil {
		t.Fatal("close accepted incomplete unwind authority")
	}
	compPayload := json.RawMessage(`{"lineageId":"lineage-1","failedCommandId":"command-short","survivingCommandId":"command-long","maxCompensationNotional":"25","reason":"partial"}`)
	compRecord := work.IntentRecord{Command: orders.Command{CommandID: "parent-comp", PrincipalID: "platform:funding", ProductID: "funding", Mode: orders.ModeTest, LineageID: "lineage-1", CommandKind: "funding.compensate", Payload: compPayload, CreatedAt: now}, WorkPayload: compPayload, Facts: facts}
	if err := producer.Prepare(context.Background(), compRecord); err != nil {
		t.Fatal(err)
	}
	compPlan, _ := plans.GetFundingPlan(context.Background(), "platform:funding", orders.ModeTest, "compensate:lineage-1:command-short:command-long")
	if len(compPlan.Legs) != 1 || !compPlan.Legs[0].ReduceOnly || compPlan.Legs[0].Quantity != "0.25" || compPlan.Legs[0].Notional != "25" {
		t.Fatalf("compensation plan=%+v", compPlan)
	}
}

func unwindAuthorityPayload(t *testing.T, now time.Time, id, account, venue, instrument, quantity string) json.RawMessage {
	t.Helper()
	expires := now.Add(time.Minute)
	value := planning.ReservationAuthority{ReservationID: id, Revision: now.Add(-time.Second), PrincipalID: "browser-a", Mode: "test", ProductID: "funding", Kind: "close", AccountID: account, Venue: venue, VenueEnvironment: "test", InstrumentID: instrument, Quantity: &quantity, Status: "held", ExpiresAt: &expires}
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestPlanProducerFailsClosedBeforeSupersededAccountingCanDoublePnL(t *testing.T) {
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	ledgerService := ledger.NewService(ledger.NewMemoryStore(), func() time.Time { return now })
	portfolioProjection := portfolio.NewProjection()
	adapter := accounting.NewPlatformAdapter(ledgerService, portfolioProjection)
	producer, _ := NewPlanProducer(work.NewMemoryFundingPlanStore(), work.NewMemoryFundingExecutionStore())
	producer.WithAccounting(adapter)
	base := accounting.Fact{PrincipalID: "principal-a", FactID: "fill-1", Revision: 1, Kind: accounting.KindFill, ProductID: "funding", Mode: "test", LineageID: "lineage-1", PositionID: "position-1", AccountID: "account-long", InstrumentID: "btc-a", CommandID: "command-long", EffectiveAt: now, Side: accounting.SideBuy, Quantity: decimal.NewFromInt(1), Price: decimal.NewFromInt(100), Fee: decimal.NewFromInt(1), FeeAsset: "USDT", SettlementAsset: "USDT"}
	fundingFact := accounting.Fact{PrincipalID: "principal-a", FactID: "funding-1", Revision: 1, Kind: accounting.KindFunding, ProductID: "funding", Mode: "test", LineageID: "lineage-1", PositionID: "position-1", AccountID: "account-long", InstrumentID: "btc-a", EffectiveAt: now.Add(time.Second), Amount: decimal.NewFromInt(2), Asset: "USDT"}
	watermark := accounting.Fact{PrincipalID: "principal-a", FactID: "watermark-1", Revision: 1, Kind: accounting.KindWatermark, ProductID: "funding", Mode: "test", LineageID: "lineage-1", PositionID: "position-1", AccountID: "account-long", InstrumentID: "btc-a", EffectiveAt: now.Add(2 * time.Second), Completeness: accounting.CompletenessComplete, Verified: true}
	residual := accounting.Fact{PrincipalID: "principal-a", FactID: "residual-1", Revision: 1, Kind: accounting.KindResidual, ProductID: "funding", Mode: "test", LineageID: "lineage-1", PositionID: "position-1", AccountID: "account-long", InstrumentID: "btc-a", EffectiveAt: now.Add(3 * time.Second), Residual: accounting.Residual{BlockerClass: "open_order"}}
	baseFacts := []accounting.Fact{base, fundingFact, watermark, residual}
	projection, err := accounting.Rebuild(baseFacts)
	if err != nil || projection.Residual.BlockerClass != "open_order" || projection.VerificationStatus != "residual_exposure" {
		t.Fatalf("base projection=%+v err=%v", projection, err)
	}
	corrected := base
	corrected.FactID, corrected.SupersedesFactID, corrected.Revision, corrected.Quantity = "fill-2", base.FactID, 2, decimal.NewFromInt(2)
	encode := func(fact accounting.Fact) work.Fact {
		payload, _ := json.Marshal(fact)
		return work.Fact{Kind: "funding.accounting.funding", Payload: payload}
	}
	encodedBase := make([]work.Fact, 0, len(baseFacts))
	for _, fact := range baseFacts {
		encodedBase = append(encodedBase, encode(fact))
	}
	baseRecord := work.IntentRecord{Command: orders.Command{PrincipalID: "principal-a", CommandKind: "funding.reconcile"}, Facts: encodedBase}
	if err := producer.Prepare(context.Background(), baseRecord); err != nil {
		t.Fatal(err)
	}
	correctedChain := append(append([]work.Fact(nil), encodedBase...), encode(corrected))
	if err := producer.Prepare(context.Background(), work.IntentRecord{Command: orders.Command{PrincipalID: "principal-a", CommandKind: "funding.reconcile"}, Facts: correctedChain}); err == nil {
		t.Fatal("superseded accounting chain mutated non-replaceable Platform sinks")
	}
	restarted, _ := NewPlanProducer(work.NewMemoryFundingPlanStore(), work.NewMemoryFundingExecutionStore())
	restarted.WithAccounting(adapter)
	if err := restarted.Prepare(context.Background(), baseRecord); err != nil {
		t.Fatalf("immutable replay after restart: %v", err)
	}
	snapshot, err := ledgerService.PnL(context.Background(), ledger.Query{PrincipalID: "principal-a", Mode: ledger.ModeTest, ProductID: "funding", LineageID: "lineage-1", AccountID: "account-long", InstrumentID: "btc-a"})
	positions := portfolioProjection.List("principal-a", portfolio.ModeTest, "account-long", "funding")
	if err != nil || snapshot.Funding != "2" || snapshot.Fees != "1" || snapshot.NetPnL != "1" || len(positions) != 1 || positions[0].Quantity != "1" {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
}

func TestPlanProducerValidatesAllAccountingScopesBeforeAnySinkMutation(t *testing.T) {
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	clean := accounting.Fact{PrincipalID: "principal-a", FactID: "clean-funding", Revision: 1, Kind: accounting.KindFunding, ProductID: "funding", Mode: "test", LineageID: "lineage-clean", PositionID: "position-clean", AccountID: "account-clean", InstrumentID: "btc-clean", EffectiveAt: now, Amount: decimal.NewFromInt(4), Asset: "USDT"}
	badBase := accounting.Fact{PrincipalID: "principal-a", FactID: "bad-funding-1", Revision: 1, Kind: accounting.KindFunding, ProductID: "funding", Mode: "test", LineageID: "lineage-bad", PositionID: "position-bad", AccountID: "account-bad", InstrumentID: "btc-bad", EffectiveAt: now, Amount: decimal.NewFromInt(2), Asset: "USDT"}
	badSuccessor := badBase
	badSuccessor.FactID, badSuccessor.SupersedesFactID, badSuccessor.Revision, badSuccessor.Amount = "bad-funding-2", badBase.FactID, 2, decimal.NewFromInt(3)
	encode := func(fact accounting.Fact) work.Fact {
		payload, _ := json.Marshal(fact)
		return work.Fact{Kind: "funding.accounting.funding", Payload: payload}
	}
	batches := [][]work.Fact{{encode(clean), encode(badBase), encode(badSuccessor)}, {encode(badSuccessor), encode(badBase), encode(clean)}}
	for index, facts := range batches {
		ledgerService := ledger.NewService(ledger.NewMemoryStore(), func() time.Time { return now })
		portfolioProjection := portfolio.NewProjection()
		producer, _ := NewPlanProducer(work.NewMemoryFundingPlanStore(), work.NewMemoryFundingExecutionStore())
		producer.WithAccounting(accounting.NewPlatformAdapter(ledgerService, portfolioProjection))
		if err := producer.Prepare(context.Background(), work.IntentRecord{Command: orders.Command{PrincipalID: "principal-a", CommandKind: "funding.reconcile"}, Facts: facts}); err == nil {
			t.Fatalf("order %d: mixed invalid scopes accepted", index)
		}
		snapshot, err := ledgerService.PnL(context.Background(), ledger.Query{PrincipalID: "principal-a", Mode: ledger.ModeTest, ProductID: "funding", LineageID: "lineage-clean", AccountID: "account-clean", InstrumentID: "btc-clean"})
		if err != nil || snapshot.Funding != "0" || snapshot.NetPnL != "0" || len(portfolioProjection.List("principal-a", portfolio.ModeTest, "", "funding")) != 0 {
			t.Fatalf("order %d mutated sinks: snapshot=%+v err=%v", index, snapshot, err)
		}
	}
}

func TestPlanProducerReplaysMidBatchAccountingFailureWithoutDuplicateEffects(t *testing.T) {
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	ledgerService := ledger.NewService(ledger.NewMemoryStore(), func() time.Time { return now })
	injected := errors.New("injected accounting sink failure")
	ledgerSink := &failOnceLedger{LedgerSink: ledgerService, entryID: "fill-2:fee", err: injected}
	portfolioProjection := portfolio.NewProjection()
	producer, _ := NewPlanProducer(work.NewMemoryFundingPlanStore(), work.NewMemoryFundingExecutionStore())
	producer.WithAccounting(accounting.NewPlatformAdapter(ledgerSink, portfolioProjection))

	first := accounting.Fact{PrincipalID: "principal-a", FactID: "fill-1", Revision: 1, Kind: accounting.KindFill, ProductID: "funding", Mode: "test", LineageID: "lineage-1", PositionID: "position-1", AccountID: "account-long", InstrumentID: "btc-a", CommandID: "command-long", EffectiveAt: now, Side: accounting.SideBuy, Quantity: decimal.NewFromInt(1), Price: decimal.NewFromInt(100), Fee: decimal.NewFromInt(1), FeeAsset: "USDT", SettlementAsset: "USDT"}
	second := first
	second.FactID, second.Revision, second.EffectiveAt = "fill-2", 2, now.Add(time.Second)
	encode := func(fact accounting.Fact) work.Fact {
		payload, _ := json.Marshal(fact)
		return work.Fact{Kind: "funding.accounting.fill", Payload: payload}
	}
	record := work.IntentRecord{Command: orders.Command{PrincipalID: "principal-a", CommandKind: "funding.reconcile"}, Facts: []work.Fact{encode(first), encode(second)}}

	if err := producer.Prepare(context.Background(), record); !errors.Is(err, injected) {
		t.Fatalf("first Prepare error=%v", err)
	}
	positions := portfolioProjection.List("principal-a", portfolio.ModeTest, "account-long", "funding")
	partial, err := ledgerService.PnL(context.Background(), ledger.Query{PrincipalID: "principal-a", Mode: ledger.ModeTest, ProductID: "funding", LineageID: "lineage-1", AccountID: "account-long", InstrumentID: "btc-a"})
	if err != nil || len(positions) != 1 || positions[0].Quantity != "2" || partial.Fees != "1" {
		t.Fatalf("failed attempt was not observable and replayable: positions=%+v snapshot=%+v err=%v", positions, partial, err)
	}

	if err := producer.Prepare(context.Background(), record); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if err := producer.Prepare(context.Background(), record); err != nil {
		t.Fatalf("stable replay: %v", err)
	}
	positions = portfolioProjection.List("principal-a", portfolio.ModeTest, "account-long", "funding")
	complete, err := ledgerService.PnL(context.Background(), ledger.Query{PrincipalID: "principal-a", Mode: ledger.ModeTest, ProductID: "funding", LineageID: "lineage-1", AccountID: "account-long", InstrumentID: "btc-a"})
	entries, listErr := ledgerService.List(context.Background(), ledger.Query{PrincipalID: "principal-a", Mode: ledger.ModeTest, ProductID: "funding", LineageID: "lineage-1", AccountID: "account-long", InstrumentID: "btc-a"})
	if err != nil || listErr != nil || len(positions) != 1 || positions[0].Quantity != "2" || complete.Fees != "2" || complete.NetPnL != "-2" || len(entries) != 2 {
		t.Fatalf("replay duplicated or omitted effects: positions=%+v snapshot=%+v entries=%+v err=%v listErr=%v", positions, complete, entries, err, listErr)
	}
}

type failOnceLedger struct {
	accounting.LedgerSink
	entryID string
	err     error
	failed  bool
}

func (sink *failOnceLedger) Record(ctx context.Context, entry ledger.Entry) error {
	if entry.EntryID == sink.entryID && !sink.failed {
		sink.failed = true
		return sink.err
	}
	return sink.LedgerSink.Record(ctx, entry)
}

func producerInput(now time.Time) calculation.Input {
	leg := func(account string, generation int, venue, instrument, rate string) calculation.LegFact {
		price := decimal.NewFromInt(100)
		return calculation.LegFact{AccountFence: calculation.AccountFence{AccountID: account, RuntimeGeneration: generation}, Venue: venue, VenueEnvironment: "test", InstrumentID: instrument, Symbol: "BTCUSDT", Current: calculation.FundingPoint{Rate: decimal.RequireFromString(rate), ObservedAt: now.Add(-time.Second), FundingTime: now, NextFunding: now.Add(8 * time.Hour)}, History: []calculation.FundingPoint{{Rate: decimal.RequireFromString(rate)}}, MarketObservedAt: now.Add(-time.Second), MarkPrice: price, BBO: calculation.Quote{BidPrice: price, BidSize: decimal.NewFromInt(10), AskPrice: price, AskSize: decimal.NewFromInt(10)}, Depth: calculation.Depth{Bids: []calculation.Level{{Price: price, Quantity: decimal.NewFromInt(10)}}, Asks: []calculation.Level{{Price: price, Quantity: decimal.NewFromInt(10)}}}, QuantityIncrement: decimal.RequireFromString("0.001"), MinQuantity: decimal.RequireFromString("0.001"), MinNotional: decimal.NewFromInt(5), TakerFeeRate: decimal.RequireFromString("0.0001")}
	}
	return calculation.Input{Mode: "test", ProductID: "funding", OpportunityID: "opportunity-1", ObservedAt: now, RequestedNotionalPerLeg: decimal.NewFromInt(50), Long: leg("account-long", 4, "venue-a", "btc-a", "0.0001"), Short: leg("account-short", 7, "venue-b", "btc-b", "0.0011")}
}

func producerPlanningFact(input calculation.Input) planning.PlanningFact {
	updated := input.ObservedAt.Add(-time.Minute)
	notional, currency := input.RequestedNotionalPerLeg.String(), "USDT"
	authorities := []planning.ReservationAuthority{
		{ReservationID: "reservation-long", Revision: updated, PrincipalID: "platform:funding", Mode: input.Mode, ProductID: "funding", Kind: "open", AccountID: input.Long.AccountFence.AccountID, Venue: input.Long.Venue, VenueEnvironment: input.Long.VenueEnvironment, InstrumentID: input.Long.InstrumentID, Notional: &notional, Currency: &currency, Status: "held"},
		{ReservationID: "reservation-short", Revision: updated, PrincipalID: "platform:funding", Mode: input.Mode, ProductID: "funding", Kind: "open", AccountID: input.Short.AccountFence.AccountID, Venue: input.Short.Venue, VenueEnvironment: input.Short.VenueEnvironment, InstrumentID: input.Short.InstrumentID, Notional: &notional, Currency: &currency, Status: "held"},
	}
	return planning.NewPlanningFact(input, "platform:funding", "reservation-long", "reservation-short", authorities, nil)
}
