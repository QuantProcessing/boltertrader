package reconcile

import (
	"context"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/cache"
	"github.com/QuantProcessing/boltertrader/runtime/journal"
	"github.com/shopspring/decimal"
)

func TestReconcileFillIdentityConflictBlocksWithoutApplyOrCursorCommit(t *testing.T) {
	at := time.Unix(100, 0)
	first := order("client-a", btc, "10", enums.StatusNew)
	first.Request.AccountID = "acct"
	first.Request.Side = enums.SideBuy
	first.VenueOrderID = "venue-a"
	second := order("client-b", btc, "10", enums.StatusNew)
	second.Request.AccountID = "acct"
	second.Request.Side = enums.SideBuy
	second.VenueOrderID = "venue-b"
	c := cache.New()
	c.UpsertOrder(first)
	c.UpsertOrder(second)

	mass := model.NewExecutionMassStatus("T", "acct", at)
	conflict := model.Fill{
		AccountID: "acct", InstrumentID: btc, ClientID: first.Request.ClientID, VenueOrderID: second.VenueOrderID,
		TradeID: "crossed-alias-trade", Side: enums.SideBuy, Price: d("100"), Quantity: d("1"), Timestamp: at,
	}
	if err := mass.AddFillReport(model.FillReport{Venue: "T", AccountID: "acct", Fill: conflict, ReportedAt: at}); err != nil {
		t.Fatalf("add fill: %v", err)
	}
	store := &countingCursorStore{}
	report, err := New(nil, &snapshotExec{mass: mass, fillHistory: true}, c).
		WithAccountID("acct").
		WithStateStore(store).
		Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !hasBlockingFindingCode(report.Findings, "FILL_ORDER_IDENTITY_CONFLICT") {
		t.Fatalf("findings=%+v, want blocking fill identity conflict", report.Findings)
	}
	if report.FillsApplied != 0 || report.OrdersMaterialized != 0 {
		t.Fatalf("report=%+v, conflicting fill must not apply or materialize", report)
	}
	for _, clientID := range []string{first.Request.ClientID, second.Request.ClientID} {
		got, ok := c.OrderForAccount("acct", clientID)
		if !ok || !got.FilledQty.IsZero() {
			t.Fatalf("order %s=(%+v,%v), want unchanged", clientID, got, ok)
		}
	}
	if store.commits != 0 || report.CursorsCommitted != 0 {
		t.Fatalf("cursor advanced despite identity conflict: store=%d report=%d", store.commits, report.CursorsCommitted)
	}
}

func TestReconcileOverlapRetainsFillOrderIdentityAfterLongTermIndexEviction(t *testing.T) {
	s := newOverlapScenario(t, "acct-overlap-identity", "ab")
	execClient := &snapshotExec{mass: s.mass("ab", 1, false), fillHistory: true}
	applications := 0
	r := New(nil, execClient, s.cache).
		WithAccountID(s.accountID).
		WithFillRetentionLimit(1).
		WithFillApplier(func(model.Fill, contract.EventMeta) FillApplyResult {
			applications++
			return FillApplyApplied
		})

	if report, err := r.Run(context.Background()); err != nil {
		t.Fatalf("initial pass: %v", err)
	} else if report.FillsApplied != 2 {
		t.Fatalf("initial report=%+v, want two applied fills", report)
	}

	at := s.base.Add(2 * time.Second)
	conflicting := model.Fill{
		AccountID:    s.accountID,
		InstrumentID: btc,
		ClientID:     "conflicting-client",
		VenueOrderID: "conflicting-venue-order",
		TradeID:      "outcome-overlap-trade-a",
		Side:         enums.SideBuy,
		Price:        d("100"),
		Quantity:     d("1"),
		Timestamp:    s.base,
	}
	conflictMass := model.NewExecutionMassStatus("T", s.accountID, at)
	if err := conflictMass.AddFillReport(model.FillReport{
		Venue: "T", AccountID: s.accountID, Fill: conflicting, ReportedAt: at,
	}); err != nil {
		t.Fatalf("add conflicting overlap fill: %v", err)
	}
	execClient.mass = conflictMass
	report, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("conflict pass: %v", err)
	}
	found := false
	for _, finding := range report.Findings {
		if finding.Code == "FILL_TRADE_IDENTITY_CONFLICT" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("findings=%+v, want retained overlap identity conflict", report.Findings)
	}
	if applications != 2 {
		t.Fatalf("applications=%d, conflicting overlap fill must not be applied", applications)
	}
}

func TestReconcileCrossedOrderAliasesBlockWithoutMutationOrCursorCommit(t *testing.T) {
	at := time.Unix(150, 0)
	first := order("client-a", btc, "10", enums.StatusNew)
	first.Request.AccountID = "acct"
	first.Request.Side = enums.SideBuy
	first.VenueOrderID = "venue-a"
	second := order("client-b", btc, "10", enums.StatusNew)
	second.Request.AccountID = "acct"
	second.Request.Side = enums.SideBuy
	second.VenueOrderID = "venue-b"
	c := cache.New()
	c.UpsertOrder(first)
	c.UpsertOrder(second)

	crossed := first
	crossed.VenueOrderID = second.VenueOrderID
	crossed.UpdatedAt = at
	mass := model.NewExecutionMassStatus("T", "acct", at)
	if err := mass.AddOrderReport(model.OrderStatusReport{Venue: "T", AccountID: "acct", Order: crossed, ReportedAt: at}); err != nil {
		t.Fatalf("add order report: %v", err)
	}
	store := &countingCursorStore{}
	report, err := New(nil, &snapshotExec{mass: mass}, c).
		WithAccountID("acct").
		WithStateStore(store).
		Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !hasBlockingFindingCode(report.Findings, "ORDER_IDENTITY_CONFLICT") {
		t.Fatalf("findings=%+v, want blocking order identity conflict", report.Findings)
	}
	if report.OrdersUpdated != 0 || report.OrdersExternal != 0 || report.OrdersClosedUnknown != 0 {
		t.Fatalf("report=%+v, crossed aliases must not mutate or classify orders", report)
	}
	for _, want := range []model.Order{first, second} {
		got, ok := c.OrderByClientIDForAccount("acct", want.Request.ClientID)
		if !ok || got.VenueOrderID != want.VenueOrderID || got.Status != want.Status {
			t.Fatalf("order %s=(%+v,%v), want unchanged %+v", want.Request.ClientID, got, ok, want)
		}
	}
	if store.commits != 0 || report.CursorsCommitted != 0 {
		t.Fatalf("cursor advanced despite order identity conflict: store=%d report=%d", store.commits, report.CursorsCommitted)
	}
}

func TestReconcileTypedVenueKeysDoNotKeepMissingClientCollisionOpen(t *testing.T) {
	at := time.Unix(200, 0)
	local := order("shared", btc, "2", enums.StatusNew)
	local.Request.AccountID = "acct"
	local.VenueOrderID = "venue-local"
	reported := order("reported-client", btc, "2", enums.StatusNew)
	reported.Request.AccountID = "acct"
	reported.VenueOrderID = "shared"
	c := cache.New()
	c.UpsertOrder(local)

	mass := model.NewExecutionMassStatus("T", "acct", at)
	if err := mass.AddOrderReport(model.OrderStatusReport{Venue: "T", AccountID: "acct", Order: reported, ReportedAt: at}); err != nil {
		t.Fatalf("add order report: %v", err)
	}
	if _, err := New(nil, &snapshotExec{mass: mass}, c).WithAccountID("acct").Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	got, ok := c.OrderByClientIDForAccount("acct", local.Request.ClientID)
	if !ok || got.Status != enums.StatusUnknown {
		t.Fatalf("missing local order=(%+v,%v), venue-ID/client-ID collision kept it open", got, ok)
	}
}

func TestOrderProgressFindingCannotBeResolvedByCrossNamespaceFill(t *testing.T) {
	firstAt := time.Unix(300, 0)
	orderA := order("client-a", btc, "2", enums.StatusNew)
	orderA.Request.AccountID = "acct"
	orderA.Request.Side = enums.SideBuy
	orderA.VenueOrderID = "shared"
	orderB := order("shared", btc, "2", enums.StatusNew)
	orderB.Request.AccountID = "acct"
	orderB.Request.Side = enums.SideBuy
	orderB.VenueOrderID = ""
	c := cache.New()
	c.UpsertOrder(orderA)
	c.UpsertOrder(orderB)

	snapshotB := orderB
	snapshotB.Status = enums.StatusPartiallyFilled
	snapshotB.FilledQty = d("1")
	snapshotB.AvgFillPrice = decimal.Zero // prevent inferred fill; evidence is intentionally absent.
	snapshotB.UpdatedAt = firstAt
	firstMass := model.NewExecutionMassStatus("T", "acct", firstAt)
	if err := firstMass.AddOrderReport(model.OrderStatusReport{Venue: "T", AccountID: "acct", Order: snapshotB, ReportedAt: firstAt}); err != nil {
		t.Fatalf("add first order report: %v", err)
	}
	execClient := &snapshotExec{mass: firstMass, fillHistory: true}
	store := NewJournalStateStore(journal.NewMemory())
	r := New(nil, execClient, c).WithAccountID("acct").WithStateStore(store)
	first, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if !hasBlockingFindingCode(first.Findings, "ORDER_PROGRESS_WITHOUT_FILL") {
		t.Fatalf("first findings=%+v, want missing fill finding", first.Findings)
	}

	secondAt := firstAt.Add(time.Second)
	snapshotB.UpdatedAt = secondAt
	secondMass := model.NewExecutionMassStatus("T", "acct", secondAt)
	if err := secondMass.AddOrderReport(model.OrderStatusReport{Venue: "T", AccountID: "acct", Order: snapshotB, ReportedAt: secondAt}); err != nil {
		t.Fatalf("add second order report: %v", err)
	}
	fillA := model.Fill{
		AccountID: "acct", InstrumentID: btc, ClientID: orderA.Request.ClientID, VenueOrderID: orderA.VenueOrderID,
		TradeID: "fill-for-order-a", Side: enums.SideBuy, Price: d("100"), Quantity: d("1"), Timestamp: secondAt,
	}
	if err := secondMass.AddFillReport(model.FillReport{Venue: "T", AccountID: "acct", Fill: fillA, ReportedAt: secondAt}); err != nil {
		t.Fatalf("add cross-namespace fill: %v", err)
	}
	execClient.mass = secondMass
	second, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if !hasBlockingFindingCode(second.Findings, "ORDER_PROGRESS_WITHOUT_FILL") {
		t.Fatalf("second findings=%+v, order A venue alias incorrectly resolved order B client finding", second.Findings)
	}
}

func TestExternalFillMaterializesOnlyAfterRuntimeIdentityGuardAccepts(t *testing.T) {
	at := time.Unix(400, 0)
	fill := model.Fill{
		AccountID: "acct", InstrumentID: btc, VenueOrderID: "external-venue", TradeID: "external-trade",
		Side: enums.SideBuy, Price: d("100"), Quantity: d("1"), Timestamp: at,
	}
	mass := model.NewExecutionMassStatus("T", "acct", at)
	if err := mass.AddFillReport(model.FillReport{Venue: "T", AccountID: "acct", Fill: fill, ReportedAt: at}); err != nil {
		t.Fatalf("add fill: %v", err)
	}
	c := cache.New()
	guardCalled := false
	report, err := New(nil, &snapshotExec{mass: mass, fillHistory: true}, c).
		WithAccountID("acct").
		WithFillApplier(func(candidate model.Fill, _ contract.EventMeta) FillApplyResult {
			guardCalled = true
			if candidate.ClientID != "" {
				t.Fatalf("generated client alias %q was committed before runtime guard", candidate.ClientID)
			}
			if _, found := c.OrderByVenueOrderIDForAccount("acct", fill.VenueOrderID); found {
				t.Fatal("external order was cached before runtime guard")
			}
			return FillApplyApplied
		}).
		Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !guardCalled || report.OrdersMaterialized != 1 || report.FillsApplied != 1 {
		t.Fatalf("guard=%v report=%+v, want accepted materialization", guardCalled, report)
	}
	if got, found := c.OrderByVenueOrderIDForAccount("acct", fill.VenueOrderID); !found || got.Request.ClientID == "" {
		t.Fatalf("materialized order=(%+v,%v), want generated client alias after guard", got, found)
	}
}

func hasBlockingFindingCode(findings []Finding, code string) bool {
	for _, finding := range findings {
		if finding.Code == code && finding.Blocking {
			return true
		}
	}
	return false
}
