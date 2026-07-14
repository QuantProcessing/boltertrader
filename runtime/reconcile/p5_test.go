package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/cache"
	"github.com/QuantProcessing/boltertrader/runtime/exec"
	"github.com/QuantProcessing/boltertrader/runtime/journal"
	"github.com/QuantProcessing/boltertrader/runtime/runtimetest"
	"github.com/shopspring/decimal"
)

func TestReconciliationIDsDeterministicAcrossRuns(t *testing.T) {
	scope := ScopeKey{Venue: "T", AccountID: "acct"}
	stable := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	if got, want := PassID(scope, stable), PassID(scope, stable); got != want {
		t.Fatalf("pass id=%s, want %s", got, want)
	}
	fill := model.Fill{
		InstrumentID: btc,
		ClientID:     "c1",
		VenueOrderID: "v1",
		Side:         enums.SideBuy,
		Price:        d("100"),
		Quantity:     d("1"),
	}
	got := SyntheticTradeID("acct", fill, stable)
	// A different local start time must not affect the deterministic ID.
	laterStart := stable.Add(5 * time.Minute)
	want := SyntheticTradeID("acct", fill, stable)
	if got != want || got == SyntheticTradeID("acct", fill, laterStart) {
		t.Fatalf("stable synthetic id got=%s want=%s later=%s", got, want, SyntheticTradeID("acct", fill, laterStart))
	}
}

func TestJournalStateStoreCommitsAndLoadsCursor(t *testing.T) {
	ctx := context.Background()
	store := NewJournalStateStore(journal.NewMemory())
	scope := ScopeKey{Venue: "T", AccountID: "acct"}
	cursor := Cursor{
		Scope:              scope,
		Stream:             StreamOrders,
		LastSuccessfulPass: PassID(scope, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		LastReportID:       "report-1",
		LastVenueTime:      time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		LastLocalApplyTime: time.Date(2026, 1, 1, 0, 0, 1, 0, time.UTC),
		LookbackFloor:      time.Date(2025, 12, 31, 23, 0, 0, 0, time.UTC),
	}
	if err := store.CommitCursor(ctx, cursor); err != nil {
		t.Fatalf("commit: %v", err)
	}
	got, err := store.LoadCursor(ctx, scope, StreamOrders)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.LastReportID != cursor.LastReportID || got.LookbackFloor != cursor.LookbackFloor {
		t.Fatalf("cursor=%+v, want %+v", got, cursor)
	}
}

func TestReconcilerRequestsFillsFromStoredCursorWindow(t *testing.T) {
	ctx := context.Background()
	journalStore := journal.NewMemory()
	store := NewJournalStateStore(journalStore)
	scope := ScopeKey{Venue: "T", AccountID: "acct-1"}
	from := time.Date(2026, 7, 13, 1, 2, 3, 0, time.UTC)
	if err := store.CommitCursor(ctx, Cursor{
		Scope:              scope,
		Stream:             StreamOrders,
		LastSuccessfulPass: PassID(scope, from),
		LastVenueTime:      from,
		LastLocalApplyTime: from,
	}); err != nil {
		t.Fatalf("seed cursor: %v", err)
	}

	exec := &snapshotExec{
		mass:        model.NewExecutionMassStatus("T", "acct-1", from.Add(time.Minute)),
		fillHistory: true,
	}
	startedAt := time.Now()
	if _, err := New(nil, exec, cache.New()).
		WithAccountID("acct-1").
		WithStateStore(store).
		Run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}
	finishedAt := time.Now()

	if len(exec.queries) != 1 {
		t.Fatalf("queries=%+v, want one mass-status query", exec.queries)
	}
	query := exec.queries[0]
	if !query.IncludeFills {
		t.Fatal("mass-status query IncludeFills=false, want true")
	}
	wantSince := from.Add(-defaultCursorOverlap)
	if !query.Since.Equal(wantSince) {
		t.Fatalf("mass-status query since=%s, want cursor overlap start %s", query.Since, wantSince)
	}
	if query.Until.Before(startedAt) || query.Until.After(finishedAt) {
		t.Fatalf("mass-status query until=%s, want bounded by run [%s,%s]", query.Until, startedAt, finishedAt)
	}
}

func TestReconcilerDoesNotRequestUnsupportedFillHistory(t *testing.T) {
	exec := &snapshotExec{mass: model.NewExecutionMassStatus("T", "acct-1", time.Now())}

	if _, err := New(nil, exec, cache.New()).WithAccountID("acct-1").Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(exec.queries) != 1 {
		t.Fatalf("queries=%+v, want one mass-status query", exec.queries)
	}
	if exec.queries[0].IncludeFills {
		t.Fatal("mass-status query IncludeFills=true for execution client without fill-history capability")
	}
}

func TestFillPartialWarningKeepsNextQueryAtSafeCursorFloor(t *testing.T) {
	for _, warningCode := range []string{"FILL_REPORTS_PARTIAL", "FILL_REPORTS_LIMIT_REACHED"} {
		t.Run(warningCode, func(t *testing.T) {
			ctx := context.Background()
			store := NewJournalStateStore(journal.NewMemory())
			scope := ScopeKey{Venue: "T", AccountID: "acct"}
			from := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
			if err := store.CommitCursor(ctx, Cursor{
				Scope:              scope,
				Stream:             StreamOrders,
				LastSuccessfulPass: PassID(scope, from),
				LastVenueTime:      from,
				LastLocalApplyTime: from,
				LookbackFloor:      from,
			}); err != nil {
				t.Fatalf("seed cursor: %v", err)
			}

			partialAt := from.Add(time.Hour)
			partial := model.NewExecutionMassStatus("T", "acct", partialAt)
			partial.Warnings = append(partial.Warnings, model.ReportWarning{
				Code:    warningCode,
				Message: "fill history did not cover the full requested window",
			})
			exec := &snapshotExec{mass: partial}
			r := New(nil, exec, cache.New()).
				WithAccountID("acct").
				WithStateStore(store)
			report, err := r.Run(ctx)
			if err != nil {
				t.Fatalf("partial run: %v", err)
			}
			if !report.FillsPartial {
				t.Fatalf("partial report=%+v, want explicit fills-partial state", report)
			}
			cursor, err := store.LoadCursor(ctx, scope, StreamOrders)
			if err != nil {
				t.Fatalf("load partial cursor: %v", err)
			}
			if !cursor.FillsPartial {
				t.Fatalf("cursor=%+v, want explicit fills-partial state", cursor)
			}

			exec.mass = model.NewExecutionMassStatus("T", "acct", partialAt.Add(30*time.Minute))
			if _, err := r.Run(ctx); err != nil {
				t.Fatalf("retry run: %v", err)
			}
			if len(exec.queries) != 2 {
				t.Fatalf("queries=%+v, want two mass-status queries", exec.queries)
			}
			if got := exec.queries[1].Since; !got.Equal(from) {
				t.Fatalf("retry query since=%s, want prior safe fill floor %s", got, from)
			}
		})
	}
}

func TestRecoveredFillJournaledBeforeDependentCursorCommit(t *testing.T) {
	ctx := context.Background()
	c := cache.New()
	known := order("journal-fill", btc, "1", enums.StatusNew)
	known.Request.AccountID = "acct"
	c.UpsertOrder(known)

	generatedAt := time.Date(2026, 7, 13, 2, 0, 0, 0, time.UTC)
	mass := model.NewExecutionMassStatus("T", "acct", generatedAt)
	fill := model.Fill{
		AccountID:    "acct",
		InstrumentID: btc,
		ClientID:     known.Request.ClientID,
		VenueOrderID: known.VenueOrderID,
		TradeID:      "journal-fill-trade",
		Side:         enums.SideBuy,
		Price:        d("100"),
		Quantity:     d("1"),
		Timestamp:    generatedAt,
	}
	if err := mass.AddFillReport(model.FillReport{Venue: "T", AccountID: "acct", Fill: fill, ReportedAt: generatedAt}); err != nil {
		t.Fatalf("add fill: %v", err)
	}

	j := journal.NewMemory()
	if _, err := New(nil, &snapshotExec{mass: mass}, c).
		WithAccountID("acct").
		WithStateStore(NewJournalStateStore(j)).
		Run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}

	records := j.Records()
	appliedIndex, cursorIndex := -1, -1
	var appliedRecordID string
	var committed journal.ReconciliationCursor
	for i, record := range records {
		switch record.Type {
		case journal.RecordAppliedEvent:
			appliedIndex = i
			appliedRecordID = record.RecordID
		case journal.RecordReconciliationCursor:
			cursorIndex = i
			if err := json.Unmarshal(record.Payload, &committed); err != nil {
				t.Fatalf("decode cursor record: %v", err)
			}
		}
	}
	if appliedIndex < 0 {
		t.Fatalf("journal records=%+v, want applied-event record", records)
	}
	if cursorIndex < 0 || appliedIndex >= cursorIndex {
		t.Fatalf("record ordering applied=%d cursor=%d, want applied before cursor", appliedIndex, cursorIndex)
	}
	if len(committed.AppliedEventRecordIDs) != 1 || committed.AppliedEventRecordIDs[0] != appliedRecordID {
		t.Fatalf("cursor dependencies=%v, want [%s]", committed.AppliedEventRecordIDs, appliedRecordID)
	}
}

func TestCursorCommitRetryRetainsAppliedEventDependency(t *testing.T) {
	ctx := context.Background()
	c := cache.New()
	known := order("retry-fill", btc, "1", enums.StatusNew)
	known.Request.AccountID = "acct"
	c.UpsertOrder(known)

	generatedAt := time.Date(2026, 7, 13, 2, 30, 0, 0, time.UTC)
	mass := model.NewExecutionMassStatus("T", "acct", generatedAt)
	fill := model.Fill{
		AccountID:    "acct",
		InstrumentID: btc,
		ClientID:     known.Request.ClientID,
		VenueOrderID: known.VenueOrderID,
		TradeID:      "retry-fill-trade",
		Side:         enums.SideBuy,
		Price:        d("100"),
		Quantity:     d("1"),
		Timestamp:    generatedAt,
	}
	if err := mass.AddFillReport(model.FillReport{Venue: "T", AccountID: "acct", Fill: fill, ReportedAt: generatedAt}); err != nil {
		t.Fatalf("add fill: %v", err)
	}

	j := journal.NewMemory()
	store := &failOnceCursorStore{
		JournalStateStore: NewJournalStateStore(j),
		err:               errors.New("cursor commit failed once"),
	}
	r := New(nil, &snapshotExec{mass: mass}, c).
		WithAccountID("acct").
		WithStateStore(store)
	if _, err := r.Run(ctx); !errors.Is(err, store.err) {
		t.Fatalf("first run err=%v, want %v", err, store.err)
	}
	report, err := r.Run(ctx)
	if err != nil {
		t.Fatalf("retry run: %v", err)
	}
	if report.FillsApplied != 0 || report.FillsDuplicate != 1 {
		t.Fatalf("retry report=%+v, want prior application treated as duplicate", report)
	}

	var appliedRecordID string
	var committed journal.ReconciliationCursor
	for _, record := range j.Records() {
		switch record.Type {
		case journal.RecordAppliedEvent:
			appliedRecordID = record.RecordID
		case journal.RecordReconciliationCursor:
			if err := json.Unmarshal(record.Payload, &committed); err != nil {
				t.Fatalf("decode cursor: %v", err)
			}
		}
	}
	if appliedRecordID == "" {
		t.Fatal("applied-event record missing after retry")
	}
	if len(committed.AppliedEventRecordIDs) != 1 || committed.AppliedEventRecordIDs[0] != appliedRecordID {
		t.Fatalf("retry cursor dependencies=%v, want [%s]", committed.AppliedEventRecordIDs, appliedRecordID)
	}
}

type failOnceCursorStore struct {
	*JournalStateStore
	err    error
	failed bool
}

func (s *failOnceCursorStore) CommitCursor(ctx context.Context, cursor Cursor) error {
	if !s.failed {
		s.failed = true
		return s.err
	}
	return s.JournalStateStore.CommitCursor(ctx, cursor)
}

func TestJournalStateStoreReplaysOpenFindingsFromJournal(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/reconcile.journal"
	j, err := journal.OpenFile(path, journal.FileOptions{})
	if err != nil {
		t.Fatalf("open file journal: %v", err)
	}
	scope := ScopeKey{Venue: "T", AccountID: "acct"}
	finding := Finding{
		ID:        "finding-1",
		PassID:    PassID(scope, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		Scope:     scope,
		Stream:    StreamFills,
		Severity:  FindingBlocking,
		Code:      "FILL_WITHOUT_ORDER",
		Message:   "missing order",
		Blocking:  true,
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 1, 0, time.UTC),
	}
	if err := NewJournalStateStore(j).RecordFinding(ctx, finding); err != nil {
		t.Fatalf("record finding: %v", err)
	}
	if err := j.Close(); err != nil {
		t.Fatalf("close journal: %v", err)
	}
	reopened, err := journal.OpenFile(path, journal.FileOptions{})
	if err != nil {
		t.Fatalf("reopen file journal: %v", err)
	}
	defer reopened.Close()
	replayed := NewJournalStateStore(reopened)
	open, err := replayed.LoadOpenFindings(ctx, scope)
	if err != nil {
		t.Fatalf("load findings: %v", err)
	}
	if len(open) != 1 || open[0].ID != finding.ID {
		t.Fatalf("open findings=%+v, want replayed finding", open)
	}
}

func TestReconcilerPassesAccountIDToMassStatusAndScope(t *testing.T) {
	c := cache.New()
	exec := &snapshotExec{}
	store := NewJournalStateStore(journal.NewMemory())
	r := New(nil, exec, c).WithAccountID("acct-1").WithStateStore(store)
	rep, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rep.CursorsCommitted != 1 {
		t.Fatalf("report=%+v, want cursor committed", rep)
	}
	if len(exec.queries) != 1 || exec.queries[0].AccountID != "acct-1" {
		t.Fatalf("queries=%+v, want account id acct-1", exec.queries)
	}
	cursor, err := store.LoadCursor(context.Background(), ScopeKey{Venue: "T", AccountID: "acct-1"}, StreamOrders)
	if err != nil {
		t.Fatalf("load cursor: %v", err)
	}
	if cursor.Scope.AccountID != "acct-1" {
		t.Fatalf("cursor scope=%+v, want account id acct-1", cursor.Scope)
	}
}

func TestAmbiguousSubmitResolvedByMassStatus(t *testing.T) {
	ctx := context.Background()
	c := cache.New()
	fake := runtimetest.NewFakeExec()
	fake.SetSubmitResult(nil, exec.ErrAmbiguousResult)
	clk := clock.NewSimulatedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	engine := exec.New(fake, c, clk, "reconcile")
	req := model.OrderRequest{
		InstrumentID: btc,
		ClientID:     "ambiguous-mass",
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     d("1"),
		Price:        d("100"),
	}
	if _, err := engine.Submit(ctx, req); !errors.Is(err, exec.ErrAmbiguousResult) {
		t.Fatalf("submit err=%v, want ambiguous", err)
	}
	if engine.InFlightCount() != 1 {
		t.Fatalf("in-flight=%d, want 1", engine.InFlightCount())
	}
	accepted := model.Order{Request: req, VenueOrderID: "venue-ambiguous", Status: enums.StatusNew, CreatedAt: clk.Now(), UpdatedAt: clk.Now()}
	fake.SetOrderStatusReports(accepted)
	r := New(nil, fake, c).WithInFlightResolver(engine)
	rep, err := r.Run(ctx)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rep.OrdersUpdated != 1 || engine.InFlightCount() != 0 {
		t.Fatalf("report=%+v in-flight=%d, want update and clear", rep, engine.InFlightCount())
	}
	if got, ok := c.Order(req.ClientID); !ok || got.Status != enums.StatusNew {
		t.Fatalf("cache order ok=%v order=%+v, want NEW", ok, got)
	}
}

func TestMassStatusOrderEvidenceMustMatchPendingCommand(t *testing.T) {
	tests := []struct {
		name       string
		state      exec.InFlightState
		command    journal.CommandType
		intentReq  func(model.OrderRequest) model.OrderRequest
		confirming func(model.Order) model.Order
	}{
		{
			name:    "cancel",
			state:   exec.InFlightPendingCancel,
			command: journal.CommandCancel,
			intentReq: func(req model.OrderRequest) model.OrderRequest {
				return req
			},
			confirming: func(order model.Order) model.Order {
				order.Status = enums.StatusCanceled
				return order
			},
		},
		{
			name:    "modify",
			state:   exec.InFlightPendingModify,
			command: journal.CommandModify,
			intentReq: func(req model.OrderRequest) model.OrderRequest {
				req.Price = d("101")
				req.Quantity = d("2")
				return req
			},
			confirming: func(order model.Order) model.Order {
				order.Request.Price = d("101")
				order.Request.Quantity = d("2")
				return order
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			c := cache.New()
			fake := runtimetest.NewFakeExec()
			clk := clock.NewSimulatedClock(time.Unix(100, 0))
			engine := exec.New(fake, c, clk, "acct").WithAccountID("acct")
			order := model.Order{
				Request: model.OrderRequest{
					AccountID: "acct", InstrumentID: btc, ClientID: "mass-" + tt.name,
					Side: enums.SideBuy, Type: enums.TypeLimit, TIF: enums.TifGTC,
					Price: d("100"), Quantity: d("1"),
				},
				VenueOrderID: "venue-mass-" + tt.name,
				Status:       enums.StatusNew,
				UpdatedAt:    time.Unix(100, 0),
			}
			c.UpsertOrder(order)
			intentReq := tt.intentReq(order.Request)
			inflight := exec.NewInFlightJournal()
			inflight.TrackIntent(journal.CommandIntent{
				RecordID: "mass-intent-" + tt.name, CommandID: "mass-command-" + tt.name, Type: tt.command,
				ClientID: intentReq.ClientID, VenueOrderID: order.VenueOrderID, AccountID: intentReq.AccountID,
				InstrumentID: intentReq.InstrumentID, Side: intentReq.Side, Price: intentReq.Price, Quantity: intentReq.Quantity,
			}, tt.state)
			engine.WithInFlightJournal(inflight)
			r := New(nil, fake, c).WithAccountID("acct").WithInFlightResolver(engine)

			unchanged := order
			unchanged.UpdatedAt = time.Unix(101, 0)
			fake.SetOrderStatusReports(unchanged)
			if _, err := r.Run(ctx); err != nil {
				t.Fatalf("unchanged run: %v", err)
			}
			if got := engine.InFlightCount(); got != 1 {
				t.Fatalf("in-flight=%d after unchanged mass-status order, want pending %s", got, tt.name)
			}

			confirmed := tt.confirming(order)
			confirmed.UpdatedAt = time.Unix(102, 0)
			fake.SetOrderStatusReports(confirmed)
			if _, err := r.Run(ctx); err != nil {
				t.Fatalf("confirming run: %v", err)
			}
			if got := engine.InFlightCount(); got != 0 {
				t.Fatalf("in-flight=%d after confirming mass-status order, want resolved", got)
			}
		})
	}
}

func TestAmbiguousSubmitResolvedByFillOnlyMassStatus(t *testing.T) {
	ctx := context.Background()
	c := cache.New()
	fake := runtimetest.NewFakeExec()
	fake.SetSubmitResult(nil, exec.ErrAmbiguousResult)
	clk := clock.NewSimulatedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	engine := exec.New(fake, c, clk, "reconcile")
	req := model.OrderRequest{
		InstrumentID: btc,
		ClientID:     "ambiguous-fill-only",
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     d("1"),
		Price:        d("100"),
	}
	if _, err := engine.Submit(ctx, req); !errors.Is(err, exec.ErrAmbiguousResult) {
		t.Fatalf("submit err=%v, want ambiguous", err)
	}
	if engine.InFlightCount() != 1 {
		t.Fatalf("in-flight=%d, want 1", engine.InFlightCount())
	}

	generatedAt := time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC)
	mass := model.NewExecutionMassStatus("T", "reconcile", generatedAt)
	fill := model.Fill{
		InstrumentID: btc,
		ClientID:     req.ClientID,
		VenueOrderID: "venue-fill-only",
		TradeID:      "fill-only-trade",
		Side:         enums.SideBuy,
		Price:        d("100"),
		Quantity:     d("1"),
		Timestamp:    generatedAt,
	}
	if err := mass.AddFillReport(model.FillReport{Venue: "T", AccountID: "reconcile", Fill: fill, ReportedAt: generatedAt}); err != nil {
		t.Fatalf("add fill: %v", err)
	}
	r := New(nil, &snapshotExec{mass: mass}, c).WithInFlightResolver(engine).WithAccountID("reconcile")
	rep, err := r.Run(ctx)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rep.FillsApplied != 1 || engine.InFlightCount() != 0 {
		t.Fatalf("report=%+v in-flight=%d, want fill applied and in-flight cleared", rep, engine.InFlightCount())
	}
	got, ok := c.Order(req.ClientID)
	if !ok || got.Status != enums.StatusFilled || got.VenueOrderID != fill.VenueOrderID {
		t.Fatalf("cache order ok=%v order=%+v, want FILLED with venue id", ok, got)
	}
}

func TestAmbiguousSubmitResolvedByVenueOnlyFillMassStatus(t *testing.T) {
	ctx := context.Background()
	c := cache.New()
	fake := runtimetest.NewFakeExec()
	fake.SetSubmitResult(nil, exec.ErrAmbiguousResult)
	clk := clock.NewSimulatedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	engine := exec.New(fake, c, clk, "reconcile")
	req := model.OrderRequest{
		InstrumentID: btc,
		ClientID:     "ambiguous-venue-fill",
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     d("1"),
		Price:        d("100"),
	}
	if _, err := engine.Submit(ctx, req); !errors.Is(err, exec.ErrAmbiguousResult) {
		t.Fatalf("submit err=%v, want ambiguous", err)
	}
	if engine.InFlightCount() != 1 {
		t.Fatalf("in-flight=%d, want 1", engine.InFlightCount())
	}

	generatedAt := time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC)
	mass := model.NewExecutionMassStatus("T", "reconcile", generatedAt)
	fill := model.Fill{
		InstrumentID: btc,
		VenueOrderID: "venue-only-fill",
		TradeID:      "venue-only-trade",
		Side:         enums.SideBuy,
		Price:        d("100"),
		Quantity:     d("1"),
		Timestamp:    generatedAt,
	}
	if err := mass.AddFillReport(model.FillReport{Venue: "T", AccountID: "reconcile", Fill: fill, ReportedAt: generatedAt}); err != nil {
		t.Fatalf("add fill: %v", err)
	}
	r := New(nil, &snapshotExec{mass: mass}, c).WithInFlightResolver(engine).WithAccountID("reconcile")
	rep, err := r.Run(ctx)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rep.FillsApplied != 1 || rep.OrdersMaterialized != 0 || engine.InFlightCount() != 0 {
		t.Fatalf("report=%+v in-flight=%d, want fill applied to original order and in-flight cleared", rep, engine.InFlightCount())
	}
	got, ok := c.Order(req.ClientID)
	if !ok || got.Status != enums.StatusFilled || got.VenueOrderID != fill.VenueOrderID {
		t.Fatalf("cache order ok=%v order=%+v, want original client order FILLED with venue id", ok, got)
	}
	if _, ok := c.Order("external-" + fill.VenueOrderID + "-" + fill.TradeID); ok {
		t.Fatal("venue-only fill was incorrectly materialized as external order")
	}
}

func TestVenueOnlyFillAfterReplayMaterializesOriginalClientOrder(t *testing.T) {
	ctx := context.Background()
	j := journal.NewMemory()
	fake := runtimetest.NewFakeExec()
	fake.SetSubmitResult(nil, exec.ErrAmbiguousResult)
	clk := clock.NewSimulatedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	firstCache := cache.New()
	first := exec.New(fake, firstCache, clk, "reconcile").WithJournal(j)
	req := model.OrderRequest{
		InstrumentID: btc,
		ClientID:     "replayed-venue-fill",
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     d("1"),
		Price:        d("100"),
	}
	if _, err := first.Submit(ctx, req); !errors.Is(err, exec.ErrAmbiguousResult) {
		t.Fatalf("submit err=%v, want ambiguous", err)
	}

	replayCache := cache.New()
	replayed := exec.New(runtimetest.NewFakeExec(), replayCache, clk, "reconcile").WithJournal(j)
	if err := replayed.ReplayOpenIntents(ctx); err != nil {
		t.Fatalf("replay open intents: %v", err)
	}
	if replayed.InFlightCount() != 1 {
		t.Fatalf("in-flight=%d, want replayed intent", replayed.InFlightCount())
	}

	generatedAt := time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC)
	mass := model.NewExecutionMassStatus("T", "reconcile", generatedAt)
	fill := model.Fill{
		InstrumentID: btc,
		VenueOrderID: "venue-replayed-fill",
		TradeID:      "replayed-fill-trade",
		Side:         enums.SideBuy,
		Price:        d("100"),
		Quantity:     d("1"),
		Timestamp:    generatedAt,
	}
	if err := mass.AddFillReport(model.FillReport{Venue: "T", AccountID: "reconcile", Fill: fill, ReportedAt: generatedAt}); err != nil {
		t.Fatalf("add fill: %v", err)
	}
	rep, err := New(nil, &snapshotExec{mass: mass}, replayCache).
		WithInFlightResolver(replayed).
		WithAccountID("reconcile").
		Run(ctx)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rep.FillsApplied != 1 || rep.OrdersMaterialized != 1 || replayed.InFlightCount() != 0 {
		t.Fatalf("report=%+v in-flight=%d, want materialized original order and cleared in-flight", rep, replayed.InFlightCount())
	}
	got, ok := replayCache.Order(req.ClientID)
	if !ok || got.Status != enums.StatusFilled || got.VenueOrderID != fill.VenueOrderID {
		t.Fatalf("cache order ok=%v order=%+v, want replayed original client order FILLED", ok, got)
	}
	open, err := j.OpenIntents(ctx)
	if err != nil {
		t.Fatalf("open intents: %v", err)
	}
	if len(open) != 0 {
		t.Fatalf("open intents=%+v, want durable resolution", open)
	}
}

func TestDurableDuplicateFillResolvesReplayedInFlightIntent(t *testing.T) {
	ctx := context.Background()
	generatedAt := time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC)
	fill := model.Fill{
		AccountID:    "acct",
		InstrumentID: btc,
		ClientID:     "durable-duplicate",
		VenueOrderID: "venue-durable-duplicate",
		TradeID:      "durable-duplicate-trade",
		Side:         enums.SideBuy,
		Price:        d("100"),
		Quantity:     d("1"),
		Timestamp:    generatedAt,
	}
	mass := model.NewExecutionMassStatus("T", "acct", generatedAt)
	if err := mass.AddFillReport(model.FillReport{Venue: "T", AccountID: "acct", Fill: fill, ReportedAt: generatedAt}); err != nil {
		t.Fatalf("add fill: %v", err)
	}
	source := &snapshotExec{mass: mass, fillHistory: true}
	state := NewJournalStateStore(journal.NewMemory())
	seedOrder := model.Order{
		Request: model.OrderRequest{
			AccountID: "acct", InstrumentID: btc, ClientID: fill.ClientID, Side: fill.Side,
			Type: enums.TypeLimit, TIF: enums.TifGTC, Quantity: fill.Quantity, Price: fill.Price,
		},
		VenueOrderID: fill.VenueOrderID,
		Status:       enums.StatusNew,
	}
	firstCache := cache.New()
	firstCache.UpsertOrder(seedOrder)
	firstReport, err := New(nil, source, firstCache).
		WithAccountID("acct").
		WithStateStore(state).
		Run(ctx)
	if err != nil || firstReport.FillsApplied != 1 {
		t.Fatalf("first run report=%+v err=%v, want durable applied fill", firstReport, err)
	}

	replayCache := cache.New()
	replayCache.UpsertOrder(seedOrder)
	inflight := exec.NewInFlightJournal()
	inflight.TrackIntent(journal.CommandIntent{
		RecordID: "replayed-intent", CommandID: "replayed-command", Type: journal.CommandSubmit,
		ClientID: fill.ClientID, VenueOrderID: fill.VenueOrderID, AccountID: fill.AccountID,
		InstrumentID: fill.InstrumentID, Side: fill.Side, Quantity: fill.Quantity,
	}, exec.InFlightSubmitted)
	engine := exec.New(runtimetest.NewFakeExec(), replayCache, clock.NewSimulatedClock(generatedAt), "acct").
		WithAccountID("acct").
		WithInFlightJournal(inflight)
	secondReport, err := New(nil, source, replayCache).
		WithAccountID("acct").
		WithStateStore(state).
		WithInFlightResolver(engine).
		Run(ctx)
	if err != nil {
		t.Fatalf("replay run: %v", err)
	}
	if secondReport.FillsDuplicate != 1 || engine.InFlightCount() != 0 {
		t.Fatalf("replay report=%+v in-flight=%d, want durable duplicate to resolve replayed intent", secondReport, engine.InFlightCount())
	}
}

func TestFillReportMaterializesExternalOrder(t *testing.T) {
	c := cache.New()
	generatedAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mass := model.NewExecutionMassStatus("T", "", generatedAt)
	fill := model.Fill{
		InstrumentID: btc,
		VenueOrderID: "external-venue",
		TradeID:      "external-trade",
		Side:         enums.SideBuy,
		Price:        d("100"),
		Quantity:     d("2"),
		Timestamp:    generatedAt,
	}
	if err := mass.AddFillReport(model.FillReport{Venue: "T", Fill: fill, ReportedAt: generatedAt}); err != nil {
		t.Fatalf("add fill: %v", err)
	}
	r := New(nil, &snapshotExec{mass: mass}, c)
	rep, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rep.OrdersMaterialized != 1 || rep.FillsApplied != 1 {
		t.Fatalf("report=%+v, want materialized=1 fills=1", rep)
	}
	order, ok := c.Order("external-external-venue-external-trade")
	if !ok {
		t.Fatal("materialized external order missing")
	}
	if order.Status != enums.StatusFilled || !order.FilledQty.Equal(d("2")) {
		t.Fatalf("order=%+v, want filled qty 2", order)
	}
}

func TestFillReportMaterializesExternalOrderWithAccountScope(t *testing.T) {
	c := cache.New()
	generatedAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mass := model.NewExecutionMassStatus("T", "acct-a", generatedAt)
	fill := model.Fill{
		InstrumentID: btc,
		VenueOrderID: "external-venue",
		TradeID:      "external-trade",
		Side:         enums.SideBuy,
		Price:        d("100"),
		Quantity:     d("2"),
		Timestamp:    generatedAt,
	}
	if err := mass.AddFillReport(model.FillReport{Venue: "T", AccountID: "acct-a", Fill: fill, ReportedAt: generatedAt}); err != nil {
		t.Fatalf("add fill: %v", err)
	}
	r := New(nil, &snapshotExec{mass: mass}, c).WithAccountID("acct-a")
	rep, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rep.OrdersMaterialized != 1 || rep.FillsApplied != 1 {
		t.Fatalf("report=%+v, want materialized=1 fills=1", rep)
	}
	order, ok := c.Order("external-acct-a-external-venue-external-trade")
	if !ok {
		t.Fatal("account-scoped materialized external order missing")
	}
	if order.Request.AccountID != "acct-a" || order.Status != enums.StatusFilled || !order.FilledQty.Equal(d("2")) {
		t.Fatalf("order=%+v, want acct-a filled qty 2", order)
	}
}

func TestDuplicateTradeIDIgnored(t *testing.T) {
	c := cache.New()
	c.UpsertOrder(order("known", btc, "10", enums.StatusNew))
	generatedAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mass := model.NewExecutionMassStatus("T", "", generatedAt)
	fill := model.Fill{
		InstrumentID: btc,
		ClientID:     "known",
		VenueOrderID: "v-known",
		TradeID:      "dup-trade",
		Side:         enums.SideBuy,
		Price:        d("100"),
		Quantity:     d("1"),
		Timestamp:    generatedAt,
	}
	for i := 0; i < 2; i++ {
		if err := mass.AddFillReport(model.FillReport{Venue: "T", Fill: fill, ReportedAt: generatedAt}); err != nil {
			t.Fatalf("add fill: %v", err)
		}
	}
	r := New(nil, &snapshotExec{mass: mass}, c)
	rep, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rep.FillsApplied != 1 || rep.FillsDuplicate != 1 {
		t.Fatalf("report=%+v, want applied=1 duplicate=1", rep)
	}
	got, _ := c.Order("known")
	if !got.FilledQty.Equal(d("1")) {
		t.Fatalf("filled=%s, want 1", got.FilledQty)
	}
}

func TestSameTradeIDOnDifferentOrdersWarnsAndSkipsConflictingDuplicate(t *testing.T) {
	c := cache.New()
	c.UpsertOrder(order("known-a", btc, "1", enums.StatusNew))
	c.UpsertOrder(order("known-b", btc, "1", enums.StatusNew))
	generatedAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mass := model.NewExecutionMassStatus("T", "acct", generatedAt)
	for _, fill := range []model.Fill{
		{InstrumentID: btc, ClientID: "known-a", VenueOrderID: "v-known-a", TradeID: "shared-trade", Side: enums.SideBuy, Price: d("100"), Quantity: d("1"), Timestamp: generatedAt},
		{InstrumentID: btc, ClientID: "known-b", VenueOrderID: "v-known-b", TradeID: "shared-trade", Side: enums.SideBuy, Price: d("101"), Quantity: d("1"), Timestamp: generatedAt},
	} {
		if err := mass.AddFillReport(model.FillReport{Venue: "T", AccountID: "acct", Fill: fill, ReportedAt: generatedAt}); err != nil {
			t.Fatalf("add fill: %v", err)
		}
	}
	r := New(nil, &snapshotExec{mass: mass}, c)
	rep, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rep.FillsApplied != 1 || rep.FillsDuplicate != 1 {
		t.Fatalf("report=%+v, want one fill applied and conflicting identity skipped", rep)
	}
	if !hasFindingCode(rep.Findings, "FILL_TRADE_IDENTITY_CONFLICT") {
		t.Fatalf("findings=%+v, want observable trade identity conflict", rep.Findings)
	}
	if len(rep.Warnings) != 1 || rep.Warnings[0].Code != "FILL_TRADE_IDENTITY_CONFLICT" {
		t.Fatalf("warnings=%+v, want one trade identity conflict warning", rep.Warnings)
	}
	first, _ := c.Order("known-a")
	if first.Status != enums.StatusFilled || !first.FilledQty.Equal(d("1")) {
		t.Fatalf("first order=%+v, want filled qty 1", first)
	}
	second, _ := c.Order("known-b")
	if second.Status == enums.StatusFilled || !second.FilledQty.IsZero() {
		t.Fatalf("conflicting second order=%+v, must not receive duplicate venue trade", second)
	}
}

func TestFillReportsComputeWeightedAveragePrice(t *testing.T) {
	c := cache.New()
	c.UpsertOrder(order("known", btc, "2", enums.StatusNew))
	generatedAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mass := model.NewExecutionMassStatus("T", "acct", generatedAt)
	for _, fill := range []model.Fill{
		{InstrumentID: btc, ClientID: "known", VenueOrderID: "v-known", TradeID: "fill-1", Side: enums.SideBuy, Price: d("100"), Quantity: d("1"), Timestamp: generatedAt},
		{InstrumentID: btc, ClientID: "known", VenueOrderID: "v-known", TradeID: "fill-2", Side: enums.SideBuy, Price: d("200"), Quantity: d("1"), Timestamp: generatedAt.Add(time.Second)},
	} {
		if err := mass.AddFillReport(model.FillReport{Venue: "T", AccountID: "acct", Fill: fill, ReportedAt: generatedAt}); err != nil {
			t.Fatalf("add fill: %v", err)
		}
	}
	rep, err := New(nil, &snapshotExec{mass: mass}, c).Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rep.FillsApplied != 2 {
		t.Fatalf("report=%+v, want two fills", rep)
	}
	got, _ := c.Order("known")
	if got.Status != enums.StatusFilled || !got.FilledQty.Equal(d("2")) || !got.AvgFillPrice.Equal(d("150")) {
		t.Fatalf("order=%+v, want FILLED qty 2 avg 150", got)
	}
}

func TestFillReportWinsOverSamePassOrderReport(t *testing.T) {
	c := cache.New()
	generatedAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mass := model.NewExecutionMassStatus("T", "", generatedAt)
	orderReport := order("same-pass", btc, "2", enums.StatusNew)
	if err := mass.AddOrderReport(model.OrderStatusReport{Venue: "T", Order: orderReport, ReportedAt: generatedAt}); err != nil {
		t.Fatalf("add order: %v", err)
	}
	fill := model.Fill{
		InstrumentID: btc,
		ClientID:     "same-pass",
		VenueOrderID: "v-same-pass",
		TradeID:      "same-pass-trade",
		Side:         enums.SideBuy,
		Price:        d("100"),
		Quantity:     d("1"),
		Timestamp:    generatedAt,
	}
	if err := mass.AddFillReport(model.FillReport{Venue: "T", Fill: fill, ReportedAt: generatedAt}); err != nil {
		t.Fatalf("add fill: %v", err)
	}
	rep, err := New(nil, &snapshotExec{mass: mass}, c).Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rep.OrdersExternal != 1 || rep.FillsApplied != 1 {
		t.Fatalf("report=%+v, want external order and applied fill", rep)
	}
	got, ok := c.Order("same-pass")
	if !ok {
		t.Fatal("order missing")
	}
	if got.Status != enums.StatusPartiallyFilled || !got.FilledQty.Equal(d("1")) {
		t.Fatalf("order=%+v, want PARTIALLY_FILLED qty 1", got)
	}
}

func TestCumulativeSnapshotProgressUsesCanonicalOrderIdentity(t *testing.T) {
	c := cache.New()
	generatedAt := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	existing := order("canonical-progress", btc, "2", enums.StatusNew)
	existing.Request.AccountID = "acct"
	c.UpsertOrder(existing)

	snapshot := existing
	snapshot.VenueOrderID = ""
	snapshot.Status = enums.StatusPartiallyFilled
	snapshot.FilledQty = d("1")
	mass := model.NewExecutionMassStatus("T", "acct", generatedAt)
	if err := mass.AddOrderReport(model.OrderStatusReport{
		Venue: "T", AccountID: "acct", Order: snapshot, ReportedAt: generatedAt,
	}); err != nil {
		t.Fatalf("add order: %v", err)
	}
	fill := model.Fill{
		AccountID: "acct", InstrumentID: btc, ClientID: existing.Request.ClientID,
		VenueOrderID: existing.VenueOrderID, TradeID: "canonical-progress-trade",
		Side: enums.SideBuy, Price: d("100"), Quantity: d("1"), Timestamp: generatedAt,
	}
	if err := mass.AddFillReport(model.FillReport{Venue: "T", AccountID: "acct", Fill: fill, ReportedAt: generatedAt}); err != nil {
		t.Fatalf("add fill: %v", err)
	}

	rep, err := New(nil, &snapshotExec{mass: mass}, c).WithAccountID("acct").Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rep.FillsApplied != 1 {
		t.Fatalf("report=%+v, want one applied fill", rep)
	}
	got, ok := c.Order(existing.Request.ClientID)
	if !ok {
		t.Fatal("order missing")
	}
	if got.Status != enums.StatusPartiallyFilled || !got.FilledQty.Equal(d("1")) {
		t.Fatalf("order=%+v, cumulative snapshot and same fill must produce PARTIALLY_FILLED qty 1", got)
	}
}

func TestPartialScopeFailureKeepsMissingOrderOpenAndCommitsPartialCursor(t *testing.T) {
	c := cache.New()
	c.UpsertOrder(order("missing", btc, "1", enums.StatusNew))
	generatedAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mass := model.NewExecutionMassStatus("T", "", generatedAt)
	mass.Partial = true
	store := NewJournalStateStore(journal.NewMemory())
	r := New(nil, &snapshotExec{mass: mass}, c).WithStateStore(store)
	rep, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !rep.Partial || rep.FillsPartial || len(rep.Findings) == 0 || rep.CursorsCommitted != 1 {
		t.Fatalf("report=%+v, want partial finding and committed partial cursor", rep)
	}
	if got, ok := c.Order("missing"); !ok || got.Status != enums.StatusNew {
		t.Fatalf("missing order ok=%v order=%+v, want still NEW", ok, got)
	}
	cursor, err := store.LoadCursor(context.Background(), ScopeKey{Venue: "T"}, StreamOrders)
	if err != nil {
		t.Fatalf("load cursor: %v", err)
	}
	if !cursor.Partial {
		t.Fatalf("cursor=%+v, want partial", cursor)
	}
	if cursor.FillsPartial {
		t.Fatalf("cursor=%+v, order ambiguity must not imply partial fill history", cursor)
	}
}

func TestCursorCommitFailureReturnsError(t *testing.T) {
	c := cache.New()
	mass := model.NewExecutionMassStatus("T", "", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fail := errors.New("journal commit failed")
	r := New(nil, &snapshotExec{mass: mass}, c).WithStateStore(failingStateStore{err: fail})
	if _, err := r.Run(context.Background()); !errors.Is(err, fail) {
		t.Fatalf("run err=%v, want %v", err, fail)
	}
}

func TestAccountPositionMismatchIsAuditedWithoutOverwrite(t *testing.T) {
	c := cache.New()
	c.UpsertPosition(model.Position{InstrumentID: btc, Side: enums.PosNet, Quantity: d("1")})
	acct := &snapshotAccount{
		positionReports: true,
		positions:       []model.Position{{InstrumentID: btc, Side: enums.PosNet, Quantity: d("2")}},
	}
	rep, err := New(acct, nil, c).Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rep.PositionOverwrites != 0 || !hasFindingCode(rep.Findings, "POSITION_MISMATCH") {
		t.Fatalf("report=%+v, want audited mismatch without overwrite", rep)
	}
	if got, ok := c.Position(btc, enums.PosNet); !ok || !got.Quantity.Equal(d("1")) {
		t.Fatalf("cache position=%+v ok=%v, want original quantity 1", got, ok)
	}
	if rep.ActivationVerdict().Safe {
		t.Fatalf("report=%+v, position mismatch must block activation", rep)
	}
}

type failingStateStore struct{ err error }

func (s failingStateStore) LoadCursor(context.Context, ScopeKey, ReportStream) (Cursor, error) {
	return Cursor{}, nil
}
func (s failingStateStore) BeginPass(context.Context, PassHeader) error  { return nil }
func (s failingStateStore) RecordFinding(context.Context, Finding) error { return nil }
func (s failingStateStore) CommitCursor(context.Context, Cursor) error   { return s.err }
func (s failingStateStore) LoadOpenFindings(context.Context, ScopeKey) ([]Finding, error) {
	return nil, nil
}

func BenchmarkReconcileMassStatus(b *testing.B) {
	for _, size := range []int{100, 1000, 10000} {
		b.Run(decimal.NewFromInt(int64(size)).String(), func(b *testing.B) {
			generatedAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			mass := model.NewExecutionMassStatus("T", "", generatedAt)
			for i := 0; i < size; i++ {
				o := order("order-"+decimal.NewFromInt(int64(i)).String(), btc, "1", enums.StatusNew)
				if err := mass.AddOrderReport(model.OrderStatusReport{Venue: "T", Order: o, ReportedAt: generatedAt}); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				c := cache.New()
				r := New(nil, &snapshotExec{mass: mass}, c)
				if _, err := r.Run(context.Background()); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
