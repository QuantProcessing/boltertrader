package reconcile

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/cache"
	"github.com/QuantProcessing/boltertrader/runtime/journal"
	"github.com/shopspring/decimal"
)

type failOnceAppliedFillStore struct {
	cursor      Cursor
	commits     int
	recordCalls int
	records     map[string]model.Fill
}

func (s *failOnceAppliedFillStore) LoadCursor(context.Context, ScopeKey, ReportStream) (Cursor, error) {
	return s.cursor, nil
}
func (*failOnceAppliedFillStore) BeginPass(context.Context, PassHeader) error { return nil }
func (*failOnceAppliedFillStore) RecordFinding(context.Context, Finding) error {
	return nil
}
func (s *failOnceAppliedFillStore) CommitCursor(_ context.Context, cursor Cursor) error {
	s.cursor = cursor
	s.commits++
	return nil
}
func (*failOnceAppliedFillStore) LoadOpenFindings(context.Context, ScopeKey) ([]Finding, error) {
	return nil, nil
}
func (s *failOnceAppliedFillStore) RecordAppliedFill(_ context.Context, _ PassHeader, _ contract.EventMeta, fill model.Fill, _ time.Time) (string, error) {
	s.recordCalls++
	if s.recordCalls == 1 {
		return "applied-fill-record", errors.New("injected applied-event failure")
	}
	if s.records == nil {
		s.records = make(map[string]model.Fill)
	}
	s.records["applied-fill-record"] = fill
	return "applied-fill-record", nil
}
func (s *failOnceAppliedFillStore) LoadAppliedFills(_ context.Context, recordIDs []string) ([]AppliedFillDependency, error) {
	loaded := make([]AppliedFillDependency, 0, len(recordIDs))
	for _, recordID := range recordIDs {
		fill, ok := s.records[recordID]
		if !ok {
			return nil, errors.New("missing applied-fill record")
		}
		loaded = append(loaded, AppliedFillDependency{RecordID: recordID, Fill: fill})
	}
	return loaded, nil
}
func (s *failOnceAppliedFillStore) LoadAppliedFillReplay(context.Context, ScopeKey) ([]AppliedFillDependency, error) {
	loaded := make([]AppliedFillDependency, 0, len(s.records))
	for recordID, fill := range s.records {
		loaded = append(loaded, AppliedFillDependency{RecordID: recordID, Fill: fill})
	}
	return loaded, nil
}

type countingCursorStore struct {
	cursor  Cursor
	commits int
}

type emptyAppliedFillRecordStore struct {
	countingCursorStore
	recordCalls int
}

func (s *emptyAppliedFillRecordStore) RecordAppliedFill(context.Context, PassHeader, contract.EventMeta, model.Fill, time.Time) (string, error) {
	s.recordCalls++
	return "", nil
}

func (*emptyAppliedFillRecordStore) LoadAppliedFillReplay(context.Context, ScopeKey) ([]AppliedFillDependency, error) {
	return nil, nil
}

type malformedAppliedFillLoaderStore struct {
	countingCursorStore
	loaded []AppliedFillDependency
}

func (s *malformedAppliedFillLoaderStore) LoadAppliedFills(context.Context, []string) ([]AppliedFillDependency, error) {
	return append([]AppliedFillDependency(nil), s.loaded...), nil
}

type opaqueJournalStore struct {
	journal.Store
}

func (s *countingCursorStore) LoadCursor(context.Context, ScopeKey, ReportStream) (Cursor, error) {
	return s.cursor, nil
}
func (*countingCursorStore) BeginPass(context.Context, PassHeader) error { return nil }
func (*countingCursorStore) RecordFinding(context.Context, Finding) error {
	return nil
}
func (s *countingCursorStore) CommitCursor(_ context.Context, cursor Cursor) error {
	s.cursor = cursor
	s.commits++
	return nil
}
func (*countingCursorStore) LoadOpenFindings(context.Context, ScopeKey) ([]Finding, error) {
	return nil, nil
}

func TestAppliedFillReplayFailsClosedWithoutJournalEnumeration(t *testing.T) {
	store := NewJournalStateStore(&opaqueJournalStore{Store: journal.NewMemory()})
	if _, err := store.LoadAppliedFillReplay(context.Background(), ScopeKey{Venue: "T", AccountID: "acct"}); err == nil {
		t.Fatal("applied-fill replay silently succeeded without journal enumeration")
	}
}

func TestOpaqueJournalAllowsReconciliationWithoutRecoveredFills(t *testing.T) {
	store := NewJournalStateStore(&opaqueJournalStore{Store: journal.NewMemory()})
	exec := &snapshotExec{
		fillHistory: true,
		mass:        model.NewExecutionMassStatus("T", "acct", time.Unix(100, 0)),
	}
	if _, err := New(nil, exec, cache.New()).WithAccountID("acct").WithStateStore(store).Run(context.Background()); err != nil {
		t.Fatalf("empty reconciliation with a public journal.Store should remain compatible: %v", err)
	}
}

func TestOpaqueJournalRejectsRecoveredFillBeforeBusinessApplication(t *testing.T) {
	at := time.Unix(100, 0)
	known := order("opaque-fill", btc, "1", enums.StatusNew)
	known.Request.AccountID = "acct"
	known.Request.Side = enums.SideBuy
	known.VenueOrderID = "venue-opaque-fill"
	fill := model.Fill{
		AccountID: "acct", InstrumentID: btc, ClientID: known.Request.ClientID,
		VenueOrderID: known.VenueOrderID, TradeID: "opaque-trade", Side: enums.SideBuy,
		Price: d("100"), Quantity: d("1"), Timestamp: at,
	}
	mass := model.NewExecutionMassStatus("T", "acct", at)
	if err := mass.AddFillReport(model.FillReport{Venue: "T", AccountID: "acct", Fill: fill, ReportedAt: at}); err != nil {
		t.Fatalf("add fill: %v", err)
	}
	c := cache.New()
	c.UpsertOrder(known)
	applied := 0
	store := NewJournalStateStore(&opaqueJournalStore{Store: journal.NewMemory()})
	_, err := New(nil, &snapshotExec{fillHistory: true, mass: mass}, c).
		WithAccountID("acct").
		WithStateStore(store).
		WithFillApplier(func(model.Fill, contract.EventMeta) FillApplyResult {
			applied++
			return FillApplyApplied
		}).
		Run(context.Background())
	if err == nil {
		t.Fatal("recovered fill unexpectedly applied without crash-replay capability")
	}
	if applied != 0 {
		t.Fatalf("business applications=%d, want fail closed before mutation", applied)
	}
}

func TestAppliedFillRecorderEmptyRecordIDFailsClosedAfterBusinessApplication(t *testing.T) {
	at := time.Unix(150, 0)
	known := order("empty-record-id", btc, "1", enums.StatusNew)
	known.Request.AccountID = "acct"
	known.Request.Side = enums.SideBuy
	known.VenueOrderID = "venue-empty-record-id"
	fill := model.Fill{
		AccountID: "acct", InstrumentID: btc, ClientID: known.Request.ClientID,
		VenueOrderID: known.VenueOrderID, TradeID: "empty-record-id-trade", Side: enums.SideBuy,
		Price: d("100"), Quantity: d("1"), Timestamp: at,
	}
	mass := model.NewExecutionMassStatus("T", "acct", at)
	if err := mass.AddFillReport(model.FillReport{Venue: "T", AccountID: "acct", Fill: fill, ReportedAt: at}); err != nil {
		t.Fatalf("add fill: %v", err)
	}
	c := cache.New()
	c.UpsertOrder(known)
	store := &emptyAppliedFillRecordStore{}
	applied := 0
	r := New(nil, &snapshotExec{fillHistory: true, mass: mass}, c).
		WithAccountID("acct").
		WithStateStore(store).
		WithFillApplier(func(model.Fill, contract.EventMeta) FillApplyResult {
			applied++
			return FillApplyApplied
		})

	if _, err := r.Run(context.Background()); err == nil {
		t.Fatal("empty applied-fill record ID unexpectedly allowed cursor progress")
	}
	if applied != 1 {
		t.Fatalf("business applications=%d, want the already accepted fill retained for durability retry", applied)
	}
	if store.commits != 0 {
		t.Fatalf("cursor commits=%d, want none without an exact durable dependency", store.commits)
	}
	if len(r.pending) != 1 {
		t.Fatalf("pending fills=%d, want the applied fill retained for retry", len(r.pending))
	}
}

func TestAppliedFillLoaderMustReturnExactCursorDependencies(t *testing.T) {
	fill := model.Fill{
		AccountID: "acct", InstrumentID: btc, ClientID: "dependency-client",
		VenueOrderID: "dependency-venue", TradeID: "dependency-trade",
		Side: enums.SideBuy, Price: d("100"), Quantity: d("1"), Timestamp: time.Unix(200, 0),
	}
	tests := []struct {
		name   string
		loaded []AppliedFillDependency
	}{
		{name: "missing", loaded: nil},
		{name: "extra", loaded: []AppliedFillDependency{{RecordID: "required", Fill: fill}, {RecordID: "extra", Fill: fill}}},
		{name: "duplicate", loaded: []AppliedFillDependency{{RecordID: "required", Fill: fill}, {RecordID: "required", Fill: fill}}},
		{name: "empty id", loaded: []AppliedFillDependency{{RecordID: "", Fill: fill}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &malformedAppliedFillLoaderStore{loaded: tt.loaded}
			r := New(nil, nil, cache.New()).WithStateStore(store)
			err := r.seedAppliedFillDependencies(context.Background(), ScopeKey{Venue: "T", AccountID: "acct"}, Cursor{
				AppliedEventRecordIDs: []string{"required"},
			})
			if err == nil {
				t.Fatal("malformed applied-fill dependency set was accepted")
			}
			if len(r.fills) != 0 || len(r.overlapFills) != 0 || len(r.fillIdentities) != 0 {
				t.Fatalf("malformed dependency mutated replay state: fills=%d overlap=%d identities=%d", len(r.fills), len(r.overlapFills), len(r.fillIdentities))
			}
		})
	}
}

func TestPendingAppliedFillFlushesBeforeCursorAdvance(t *testing.T) {
	at := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	known := order("pending-fill", btc, "2", enums.StatusNew)
	known.Request.AccountID = "acct"
	known.Request.Side = enums.SideBuy
	known.Request.PositionSide = enums.PosNet
	known.Request.Price = d("100")
	known.VenueOrderID = "venue-pending-fill"
	fill := model.Fill{
		AccountID: "acct", InstrumentID: btc, ClientID: known.Request.ClientID,
		VenueOrderID: known.VenueOrderID, TradeID: "pending-trade", Side: enums.SideBuy,
		Price: d("100"), Quantity: d("1"), Timestamp: at,
	}
	withFill := model.NewExecutionMassStatus("T", "acct", at)
	if err := withFill.AddFillReport(model.FillReport{Venue: "T", AccountID: "acct", Fill: fill, ReportedAt: at}); err != nil {
		t.Fatalf("add fill: %v", err)
	}
	empty := model.NewExecutionMassStatus("T", "acct", at.Add(time.Minute))
	calls := 0
	exec := &snapshotExec{fillHistory: true, massFn: func(model.MassStatusQuery) *model.ExecutionMassStatus {
		calls++
		if calls == 1 {
			clone := withFill.Clone()
			return &clone
		}
		clone := empty.Clone()
		return &clone
	}}
	c := cache.New()
	c.UpsertOrder(known)
	store := &failOnceAppliedFillStore{}
	r := New(nil, exec, c).WithAccountID("acct").WithStateStore(store)

	if _, err := r.Run(context.Background()); err == nil {
		t.Fatal("first run unexpectedly succeeded")
	}
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatalf("retry run: %v", err)
	}
	if store.recordCalls != 2 {
		t.Fatalf("applied-event writes=%d, want failed write plus independent retry", store.recordCalls)
	}
	if store.commits != 1 {
		t.Fatalf("cursor commits=%d, want one successful retry commit", store.commits)
	}
	if len(store.cursor.AppliedEventRecordIDs) != 1 || store.cursor.AppliedEventRecordIDs[0] != "applied-fill-record" {
		t.Fatalf("cursor dependencies=%v, want durable pending fill dependency", store.cursor.AppliedEventRecordIDs)
	}
}

func TestCursorWatermarkDoesNotExceedRequestedFillWindow(t *testing.T) {
	var missedAt time.Time
	calls := 0
	exec := &snapshotExec{fillHistory: true, massFn: func(query model.MassStatusQuery) *model.ExecutionMassStatus {
		calls++
		if calls == 1 {
			missedAt = query.Until.Add(time.Millisecond)
			// Ensure the second query's wall-clock Until covers the deliberately
			// missed fill, which sits just beyond this first requested boundary.
			time.Sleep(3 * time.Millisecond)
			return model.NewExecutionMassStatus("T", "acct", query.Until.Add(2*time.Millisecond))
		}
		mass := model.NewExecutionMassStatus("T", "acct", query.Until.Add(time.Millisecond))
		if !missedAt.Before(query.Since) && !missedAt.After(query.Until) {
			fill := model.Fill{
				AccountID: "acct", InstrumentID: btc, ClientID: "watermark",
				VenueOrderID: "venue-watermark", TradeID: "watermark-trade",
				Side: enums.SideBuy, Price: d("100"), Quantity: d("1"), Timestamp: missedAt,
			}
			if err := mass.AddFillReport(model.FillReport{Venue: "T", AccountID: "acct", Fill: fill, ReportedAt: missedAt}); err != nil {
				t.Fatalf("add fill: %v", err)
			}
		}
		return mass
	}}
	c := cache.New()
	known := order("watermark", btc, "1", enums.StatusNew)
	known.Request.AccountID = "acct"
	known.Request.Side = enums.SideBuy
	known.VenueOrderID = "venue-watermark"
	c.UpsertOrder(known)
	r := New(nil, exec, c).
		WithAccountID("acct").
		WithStateStore(NewJournalStateStore(journal.NewMemory()))

	if _, err := r.Run(context.Background()); err != nil {
		t.Fatalf("first run: %v", err)
	}
	report, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if report.FillsApplied != 1 {
		t.Fatalf("second report=%+v, fill immediately after prior query Until was skipped", report)
	}
	if got := exec.queries[1].Since; got.After(missedAt) {
		t.Fatalf("second query Since=%s advanced past missed fill at %s", got, missedAt)
	}
}

func TestCursorOverlapRecoversLateVisibleVenueTimestamp(t *testing.T) {
	var lateFillAt time.Time
	calls := 0
	exec := &snapshotExec{fillHistory: true, massFn: func(query model.MassStatusQuery) *model.ExecutionMassStatus {
		calls++
		mass := model.NewExecutionMassStatus("T", "acct", query.Until)
		if calls == 1 {
			lateFillAt = query.Until.Add(-time.Millisecond)
			return mass
		}
		if !lateFillAt.Before(query.Since) && !lateFillAt.After(query.Until) {
			fill := model.Fill{
				AccountID: "acct", InstrumentID: btc, ClientID: "late-visible",
				VenueOrderID: "venue-late-visible", TradeID: "late-visible-trade",
				Side: enums.SideBuy, Price: d("100"), Quantity: d("1"), Timestamp: lateFillAt,
			}
			if err := mass.AddFillReport(model.FillReport{Venue: "T", AccountID: "acct", Fill: fill, ReportedAt: lateFillAt}); err != nil {
				t.Fatalf("add fill: %v", err)
			}
		}
		return mass
	}}
	c := cache.New()
	known := order("late-visible", btc, "1", enums.StatusNew)
	known.Request.AccountID = "acct"
	known.Request.Side = enums.SideBuy
	known.VenueOrderID = "venue-late-visible"
	c.UpsertOrder(known)
	r := New(nil, exec, c).
		WithAccountID("acct").
		WithStateStore(NewJournalStateStore(journal.NewMemory()))

	if _, err := r.Run(context.Background()); err != nil {
		t.Fatalf("first run: %v", err)
	}
	report, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if report.FillsApplied != 1 {
		t.Fatalf("second report=%+v, late-visible fill before prior Until was skipped", report)
	}
	if got := exec.queries[1].Since; got.After(lateFillAt) {
		t.Fatalf("second query Since=%s advanced past late-visible fill at %s", got, lateFillAt)
	}
}

func TestSyntheticTradeIDUsesFillTimestampAcrossPasses(t *testing.T) {
	fillAt := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	fill := model.Fill{
		AccountID: "acct", InstrumentID: btc, ClientID: "synthetic",
		VenueOrderID: "venue-synthetic", Side: enums.SideBuy,
		Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(1), Timestamp: fillAt,
	}
	first := SyntheticTradeID("acct", fill, fillAt.Add(time.Minute))
	second := SyntheticTradeID("acct", fill, fillAt.Add(2*time.Minute))
	if first != second {
		t.Fatalf("same timestamped fill produced pass-dependent ids %q and %q", first, second)
	}
}

func TestBlockingFillFindingRetainsPriorCursor(t *testing.T) {
	at := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	mass := model.NewExecutionMassStatus("T", "acct", at)
	fill := model.Fill{
		AccountID: "acct", VenueOrderID: "unknown-order", TradeID: "blocking-trade",
		Side: enums.SideBuy, Price: d("100"), Quantity: d("1"), Timestamp: at,
	}
	if err := mass.AddFillReport(model.FillReport{Venue: "T", AccountID: "acct", Fill: fill, ReportedAt: at}); err != nil {
		t.Fatalf("add fill: %v", err)
	}
	store := &countingCursorStore{}
	report, err := New(nil, &snapshotExec{mass: mass, fillHistory: true}, cache.New()).
		WithAccountID("acct").
		WithStateStore(store).
		Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(report.Findings) != 1 || !report.Findings[0].Blocking {
		t.Fatalf("report findings=%+v, want blocking unmatched-fill finding", report.Findings)
	}
	if store.commits != 0 || report.CursorsCommitted != 0 {
		t.Fatalf("cursor advanced despite blocking finding: store=%d report=%d", store.commits, report.CursorsCommitted)
	}
}
