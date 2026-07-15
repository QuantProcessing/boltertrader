package reconcile

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/cache"
	"github.com/QuantProcessing/boltertrader/runtime/journal"
)

type overlapScenario struct {
	t         *testing.T
	accountID string
	base      time.Time
	cache     *cache.Cache
	orders    map[rune]model.Order
}

func newOverlapScenario(t *testing.T, accountID, allIDs string) *overlapScenario {
	t.Helper()
	s := &overlapScenario{
		t:         t,
		accountID: accountID,
		base:      time.Date(2026, 7, 14, 3, 0, 0, 0, time.UTC),
		cache:     cache.New(),
		orders:    make(map[rune]model.Order, len(allIDs)),
	}
	for _, id := range allIDs {
		known := order("outcome-overlap-"+string(id), btc, "1", enums.StatusNew)
		known.Request.AccountID = accountID
		known.Request.Side = enums.SideBuy
		s.orders[id] = known
		s.cache.UpsertOrder(known)
	}
	return s
}

func (s *overlapScenario) mass(ids string, pass int, fillsPartial bool) *model.ExecutionMassStatus {
	s.t.Helper()
	generatedAt := s.base.Add(time.Duration(pass) * time.Second)
	mass := model.NewExecutionMassStatus("T", s.accountID, generatedAt)
	if fillsPartial {
		mass.Warnings = append(mass.Warnings, model.ReportWarning{
			Code:    "HISTORY_PAGE_NOTE",
			Message: "fixture diagnostic accompanies typed partial coverage",
		})
	}
	for _, id := range ids {
		known := s.orders[id]
		if err := mass.AddOrderReport(model.OrderStatusReport{
			Venue:     "T",
			AccountID: s.accountID,
			Order:     known,
		}); err != nil {
			s.t.Fatalf("add order report %q: %v", id, err)
		}
		fill := model.Fill{
			AccountID:    s.accountID,
			InstrumentID: btc,
			ClientID:     known.Request.ClientID,
			VenueOrderID: known.VenueOrderID,
			TradeID:      "outcome-overlap-trade-" + string(id),
			Side:         enums.SideBuy,
			Price:        d("100"),
			Quantity:     d("1"),
			Timestamp:    s.base,
		}
		if err := mass.AddFillReport(model.FillReport{
			Venue:      "T",
			AccountID:  s.accountID,
			Fill:       fill,
			ReportedAt: s.base,
		}); err != nil {
			s.t.Fatalf("add fill report %q: %v", id, err)
		}
	}
	return mass
}

func TestPartialFillPassRetainsPriorFullOverlap(t *testing.T) {
	s := newOverlapScenario(t, "acct-overlap-partial", "abcde")
	exec := &snapshotExec{mass: s.mass("abcde", 1, false), fillHistory: true}
	applications := 0
	r := New(nil, exec, s.cache).
		WithAccountID(s.accountID).
		WithClock(clock.NewSimulatedClock(s.base)).
		WithFillRetentionLimit(2).
		WithFillApplier(func(model.Fill, contract.EventMeta) FillApplyResult {
			applications++
			return FillApplyApplied
		})

	if _, err := r.Run(context.Background()); err != nil {
		t.Fatalf("full pass: %v", err)
	}
	exec.mass = s.mass("de", 2, true)
	exec.fillState = model.CoveragePartial
	partial, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("partial pass: %v", err)
	}
	if !partial.FillsPartial {
		t.Fatalf("partial report was not classified as fill-partial: %+v", partial)
	}
	exec.mass = s.mass("abcde", 3, false)
	exec.fillState = model.CoverageComplete
	retry, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("safe-floor retry: %v", err)
	}
	if retry.FillsApplied != 0 || retry.FillsDuplicate != 5 || applications != 5 {
		t.Fatalf("safe-floor retry=%+v applications=%d, want all five prior fills deduplicated", retry, applications)
	}
}

func TestFailedFillPassRetainsUnprocessedPriorOverlap(t *testing.T) {
	s := newOverlapScenario(t, "acct-overlap-failed-prefix", "abdefgh")
	exec := &snapshotExec{mass: s.mass("defgh", 1, false), fillHistory: true}
	applications := 0
	failB := false
	r := New(nil, exec, s.cache).
		WithAccountID(s.accountID).
		WithClock(clock.NewSimulatedClock(s.base)).
		WithFillRetentionLimit(2).
		WithFillApplier(func(fill model.Fill, _ contract.EventMeta) FillApplyResult {
			if failB && strings.HasSuffix(fill.TradeID, "-b") {
				return FillApplyUnmatched
			}
			applications++
			return FillApplyApplied
		})

	if _, err := r.Run(context.Background()); err != nil {
		t.Fatalf("initial full pass: %v", err)
	}
	failB = true
	exec.mass = s.mass("abdefgh", 2, false)
	if _, err := r.Run(context.Background()); err == nil {
		t.Fatal("mid-report application failure returned nil error")
	}
	failB = false
	exec.mass = s.mass("defgh", 3, false)
	retry, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("retry after failed pass: %v", err)
	}
	if retry.FillsApplied != 0 || retry.FillsDuplicate != 5 || applications != 6 {
		t.Fatalf("retry=%+v applications=%d, want prior five fills deduplicated after one new prefix fill", retry, applications)
	}
}

type overlapFailOnceCursorStore struct {
	cursor   Cursor
	failNext bool
}

func (s *overlapFailOnceCursorStore) LoadCursor(_ context.Context, scope ScopeKey, stream ReportStream) (Cursor, error) {
	if s.cursor.Scope == scope && s.cursor.Stream == stream {
		return s.cursor, nil
	}
	return Cursor{}, nil
}

func (*overlapFailOnceCursorStore) BeginPass(context.Context, PassHeader) error  { return nil }
func (*overlapFailOnceCursorStore) RecordFinding(context.Context, Finding) error { return nil }
func (s *overlapFailOnceCursorStore) CommitCursor(_ context.Context, cursor Cursor) error {
	if s.failNext {
		s.failNext = false
		return errors.New("injected cursor commit failure")
	}
	s.cursor = cursor
	return nil
}
func (*overlapFailOnceCursorStore) LoadOpenFindings(context.Context, ScopeKey) ([]Finding, error) {
	return nil, nil
}

func TestCursorCommitFailureRetainsPriorOverlapAcrossEmptyReport(t *testing.T) {
	s := newOverlapScenario(t, "acct-overlap-cursor-failure", "abcde")
	exec := &snapshotExec{mass: s.mass("abcde", 1, false), fillHistory: true}
	store := &overlapFailOnceCursorStore{}
	applications := 0
	r := New(nil, exec, s.cache).
		WithAccountID(s.accountID).
		WithClock(clock.NewSimulatedClock(s.base)).
		WithStateStore(store).
		WithFillRetentionLimit(2).
		WithFillApplier(func(model.Fill, contract.EventMeta) FillApplyResult {
			applications++
			return FillApplyApplied
		})

	if _, err := r.Run(context.Background()); err != nil {
		t.Fatalf("initial full pass: %v", err)
	}
	store.failNext = true
	exec.mass = s.mass("", 2, false)
	if _, err := r.Run(context.Background()); err == nil {
		t.Fatal("empty pass cursor failure returned nil error")
	}
	exec.mass = s.mass("abcde", 3, false)
	retry, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("retry from old cursor: %v", err)
	}
	if retry.FillsApplied != 0 || retry.FillsDuplicate != 5 || applications != 5 {
		t.Fatalf("retry=%+v applications=%d, want old-cursor overlap fully deduplicated", retry, applications)
	}
}

func TestOverlapRetentionCapacityFailsClosedWithoutForgettingAppliedPrefix(t *testing.T) {
	s := newOverlapScenario(t, "acct-overlap-capacity", "abc")
	exec := &snapshotExec{mass: s.mass("abc", 1, false), fillHistory: true}
	applications := 0
	r := New(nil, exec, s.cache).
		WithAccountID(s.accountID).
		WithClock(clock.NewSimulatedClock(s.base)).
		WithFillRetentionLimit(2).
		WithFillApplier(func(model.Fill, contract.EventMeta) FillApplyResult {
			applications++
			return FillApplyApplied
		})
	r.overlapLimit = 2

	if _, err := r.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "retention capacity 2 exhausted") {
		t.Fatalf("first over-capacity pass error = %v", err)
	}
	if applications != 2 || len(r.overlapFills) != 2 {
		t.Fatalf("first over-capacity pass applications=%d overlap=%d, want retained applied prefix of 2", applications, len(r.overlapFills))
	}
	if _, err := r.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "retention capacity 2 exhausted") {
		t.Fatalf("retry over-capacity pass error = %v", err)
	}
	if applications != 2 || len(r.overlapFills) != 2 {
		t.Fatalf("retry applications=%d overlap=%d, applied prefix was forgotten", applications, len(r.overlapFills))
	}
}

func TestPartialCursorRetainsFullOverlapDependenciesAcrossCompactionAndRestart(t *testing.T) {
	s := newOverlapScenario(t, "acct-overlap-partial-restart", "abcde")
	full := s.mass("abcde", 1, false)
	exec := &snapshotExec{mass: full, fillHistory: true}
	memory := journal.NewMemoryWithRetention(1)
	applications := 0
	r := New(nil, exec, s.cache).
		WithAccountID(s.accountID).
		WithClock(clock.NewSimulatedClock(s.base)).
		WithStateStore(NewJournalStateStore(memory)).
		WithFillRetentionLimit(2).
		WithFillApplier(func(model.Fill, contract.EventMeta) FillApplyResult {
			applications++
			return FillApplyApplied
		})

	if _, err := r.Run(context.Background()); err != nil {
		t.Fatalf("initial full pass: %v", err)
	}
	exec.mass = s.mass("de", 2, true)
	exec.fillState = model.CoveragePartial
	if report, err := r.Run(context.Background()); err != nil {
		t.Fatalf("partial pass: %v", err)
	} else if !report.FillsPartial {
		t.Fatalf("partial report was not classified as fill-partial: %+v", report)
	}

	restartCache := cache.New()
	for _, known := range s.orders {
		restartCache.UpsertOrder(known)
	}
	restartExec := &snapshotExec{mass: full, fillHistory: true}
	restarted := New(nil, restartExec, restartCache).
		WithAccountID(s.accountID).
		WithClock(clock.NewSimulatedClock(s.base)).
		WithStateStore(NewJournalStateStore(memory)).
		WithFillRetentionLimit(2).
		WithFillApplier(func(model.Fill, contract.EventMeta) FillApplyResult {
			applications++
			return FillApplyApplied
		})
	retry, err := restarted.Run(context.Background())
	if err != nil {
		t.Fatalf("restart safe-floor retry: %v", err)
	}
	if retry.FillsApplied != 0 || retry.FillsDuplicate != 5 || applications != 5 {
		t.Fatalf("restart retry=%+v applications=%d, want all compacted overlap fills replayed as duplicates", retry, applications)
	}
}
