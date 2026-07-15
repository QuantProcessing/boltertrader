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

func typedCoverageMass(
	query model.MassStatusQuery,
	ids []model.InstrumentID,
	openState, fillState, positionState model.CoverageState,
) *model.ExecutionMassStatus {
	mass := model.NewExecutionMassStatus("T", query.AccountID, query.Until)
	mass.ClientID = query.ClientID
	mass.OpenOrdersCoverage = typedSnapshotCoverage(openState, query, ids)
	if query.IncludeFills {
		mass.FillsCoverage = model.NewFillCoverage(fillState, query.AccountID, query.ClientID, ids, query.Since, query.Until)
	} else {
		mass.FillsCoverage = model.ReportCoverage{State: model.CoverageNotRequested}
	}
	if query.IncludePositions {
		mass.PositionsCoverage = typedSnapshotCoverage(positionState, query, ids)
	} else {
		mass.PositionsCoverage = model.ReportCoverage{State: model.CoverageNotRequested}
	}
	return mass
}

func typedSnapshotCoverage(state model.CoverageState, query model.MassStatusQuery, ids []model.InstrumentID) model.ReportCoverage {
	switch state {
	case model.CoverageUnknown, model.CoverageNotRequested:
		return model.ReportCoverage{State: state}
	default:
		return model.NewSnapshotCoverage(state, query.AccountID, query.ClientID, ids, query.Until)
	}
}

func accountOrder(accountID, clientID string, id model.InstrumentID) model.Order {
	o := order(clientID, id, "1", enums.StatusNew)
	o.Request.AccountID = accountID
	return o
}

func TestUnknownOrPartialOpenOrderCoverageCannotCloseMissingOrder(t *testing.T) {
	for _, state := range []model.CoverageState{model.CoveragePartial, model.CoverageUnavailable} {
		t.Run(state.String(), func(t *testing.T) {
			c := cache.New()
			c.UpsertOrder(accountOrder("acct", "missing", btc))
			exec := &snapshotExec{massFn: func(query model.MassStatusQuery) *model.ExecutionMassStatus {
				return typedCoverageMass(query, []model.InstrumentID{btc}, state, model.CoverageNotRequested, model.CoverageNotRequested)
			}}
			rep, err := New(nil, exec, c).WithAccountID("acct").Run(context.Background())
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if got, ok := c.Order("missing"); !ok || got.Status != enums.StatusNew {
				t.Fatalf("order=%+v ok=%v, incomplete coverage must not infer absence", got, ok)
			}
			if rep.OrdersClosedUnknown != 0 {
				t.Fatalf("OrdersClosedUnknown=%d, want 0", rep.OrdersClosedUnknown)
			}
			if verdict := rep.ActivationVerdict(); verdict.Safe {
				t.Fatalf("typed incomplete coverage unexpectedly activated: %+v", verdict)
			}
		})
	}
}

func TestDiagnosticWarningCannotAlterTypedCoverage(t *testing.T) {
	tests := []struct {
		code    string
		message string
	}{
		{code: "DIAGNOSTIC_ALPHA", message: "diagnostic only"},
		{code: "HISTORY_PAGE_NOTE", message: "lower-case partial history note"},
		{code: "VENUE_SCOPE_NOTE", message: "lower-case complete venue note"},
		{code: "OPEN_ORDERS_PARTIAL", message: "legacy-looking code with lower-case complete text"},
		{code: "FILL_REPORTS_LIMIT_REACHED", message: "legacy-looking code with lower-case partial text"},
		{code: "FILL_REPORTS_PARTIAL", message: "complete and partial remain diagnostic words"},
	}
	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			c := cache.New()
			c.UpsertOrder(accountOrder("acct", "missing", btc))
			exec := &snapshotExec{massFn: func(query model.MassStatusQuery) *model.ExecutionMassStatus {
				mass := typedCoverageMass(query, []model.InstrumentID{btc}, model.CoverageComplete, model.CoverageNotRequested, model.CoverageNotRequested)
				mass.Warnings = []model.ReportWarning{{Code: tt.code, Message: tt.message}}
				return mass
			}}
			rep, err := New(nil, exec, c).WithAccountID("acct").Run(context.Background())
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if got, ok := c.Order("missing"); !ok || got.Status != enums.StatusUnknown {
				t.Fatalf("order=%+v ok=%v, typed complete evidence should govern", got, ok)
			}
			if rep.OrdersClosedUnknown != 1 || rep.CursorsCommitted != 1 {
				t.Fatalf("warning changed absence/cursor result: closed=%d cursors=%d", rep.OrdersClosedUnknown, rep.CursorsCommitted)
			}
			if rep.Partial || rep.FillsPartial || rep.OpenOrdersCoverage.State != model.CoverageComplete {
				t.Fatalf("diagnostic changed typed coverage: passPartial=%v fillsPartial=%v coverage=%+v", rep.Partial, rep.FillsPartial, rep.OpenOrdersCoverage)
			}
			if len(rep.Warnings) != 1 || rep.Warnings[0].Code != tt.code || rep.Warnings[0].Message != tt.message {
				t.Fatalf("diagnostic warning not preserved verbatim: %+v", rep.Warnings)
			}
			if verdict := rep.ActivationVerdict(); !verdict.Safe {
				t.Fatalf("diagnostic warning %q changed typed activation verdict: %+v", tt.code, verdict)
			}
		})
	}
}

func TestReconcilerRejectsUntypedMassStatus(t *testing.T) {
	exec := &snapshotExec{rawMass: true, massFn: func(query model.MassStatusQuery) *model.ExecutionMassStatus {
		mass := model.NewExecutionMassStatus("T", query.AccountID, query.Until)
		mass.Warnings = []model.ReportWarning{{Code: "OPEN_ORDERS_ONLY", Message: "must not authorize a legacy response"}}
		return mass
	}}

	_, err := New(nil, exec, cache.New()).WithAccountID("acct").Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "requested domain cannot be Unknown") {
		t.Fatalf("run error=%v, want strict typed coverage rejection", err)
	}
}

func TestCompleteCoverageCannotClosePostRequestLocalEntity(t *testing.T) {
	c := cache.New()
	c.UpsertOrder(accountOrder("acct", "before", btc))
	exec := &snapshotExec{massFn: func(query model.MassStatusQuery) *model.ExecutionMassStatus {
		c.UpsertOrder(accountOrder("acct", "after", btc))
		return typedCoverageMass(query, []model.InstrumentID{btc}, model.CoverageComplete, model.CoverageNotRequested, model.CoverageNotRequested)
	}}
	rep, err := New(nil, exec, c).WithAccountID("acct").Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got, _ := c.Order("before"); got.Status != enums.StatusUnknown {
		t.Fatalf("pre-request candidate status=%s, want UNKNOWN", got.Status)
	}
	if got, _ := c.Order("after"); got.Status != enums.StatusNew {
		t.Fatalf("post-request order status=%s, want NEW", got.Status)
	}
	if rep.OrdersClosedUnknown != 1 {
		t.Fatalf("OrdersClosedUnknown=%d, want 1", rep.OrdersClosedUnknown)
	}
}

func TestCompleteCoverageCannotRegressPostRequestCandidateUpdate(t *testing.T) {
	c := cache.New()
	c.UpsertOrder(accountOrder("acct", "updated", btc))
	exec := &snapshotExec{massFn: func(query model.MassStatusQuery) *model.ExecutionMassStatus {
		updated, _ := c.Order("updated")
		updated.Status = enums.StatusPartiallyFilled
		updated.FilledQty = d("0.5")
		c.UpsertOrder(updated)
		return typedCoverageMass(query, []model.InstrumentID{btc}, model.CoverageComplete, model.CoverageNotRequested, model.CoverageNotRequested)
	}}
	rep, err := New(nil, exec, c).WithAccountID("acct").Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got, _ := c.Order("updated"); got.Status != enums.StatusPartiallyFilled || !got.FilledQty.Equal(d("0.5")) {
		t.Fatalf("post-request candidate update regressed: %+v", got)
	}
	if rep.OrdersClosedUnknown != 0 {
		t.Fatalf("OrdersClosedUnknown=%d, want 0", rep.OrdersClosedUnknown)
	}
}

func TestCompleteOpenOrderCoverageClosesOnlyMatchingCandidateScope(t *testing.T) {
	c := cache.New()
	c.UpsertOrder(accountOrder("acct", "btc", btc))
	c.UpsertOrder(accountOrder("acct", "eth", eth))
	c.UpsertOrder(accountOrder("other", "other-btc", btc))
	exec := &snapshotExec{massFn: func(query model.MassStatusQuery) *model.ExecutionMassStatus {
		return typedCoverageMass(query, []model.InstrumentID{btc}, model.CoverageComplete, model.CoverageNotRequested, model.CoverageNotRequested)
	}}
	rep, err := New(nil, exec, c).WithAccountID("acct").Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got, _ := c.Order("btc"); got.Status != enums.StatusUnknown {
		t.Fatalf("matching candidate status=%s, want UNKNOWN", got.Status)
	}
	for _, clientID := range []string{"eth", "other-btc"} {
		if got, _ := c.Order(clientID); got.Status != enums.StatusNew {
			t.Fatalf("out-of-scope %s status=%s, want NEW", clientID, got.Status)
		}
	}
	if rep.OrdersClosedUnknown != 1 {
		t.Fatalf("OrdersClosedUnknown=%d, want 1", rep.OrdersClosedUnknown)
	}
}

func TestCompleteCoverageUsesResponseFrozenSelectorAfterRegistryMutation(t *testing.T) {
	c := cache.New()
	c.UpsertOrder(accountOrder("acct", "btc", btc))
	c.UpsertOrder(accountOrder("acct", "eth", eth))
	configured := []model.InstrumentID{btc}
	exec := &snapshotExec{massFn: func(query model.MassStatusQuery) *model.ExecutionMassStatus {
		mass := typedCoverageMass(query, configured, model.CoverageComplete, model.CoverageNotRequested, model.CoverageNotRequested)
		configured[0] = eth
		configured = append(configured, eth)
		return mass
	}}
	rep, err := New(nil, exec, c).WithAccountID("acct").Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got, _ := c.Order("btc"); got.Status != enums.StatusUnknown {
		t.Fatalf("frozen BTC candidate status=%s, want UNKNOWN", got.Status)
	}
	if got, _ := c.Order("eth"); got.Status != enums.StatusNew {
		t.Fatalf("post-construction selector mutation widened authority: %+v", got)
	}
	if len(rep.OpenOrdersCoverage.Scope.InstrumentIDs) != 1 || rep.OpenOrdersCoverage.Scope.InstrumentIDs[0] != btc {
		t.Fatalf("response selector mutated with source: %+v", rep.OpenOrdersCoverage.Scope.InstrumentIDs)
	}
}

func TestTypedPositionReconciliationUsesFrozenCoverageAfterCapabilitiesMutation(t *testing.T) {
	c := cache.New()
	c.UpsertPosition(model.Position{AccountID: "acct", InstrumentID: btc, Side: enums.PosNet, Quantity: d("1")})
	exec := &snapshotExec{
		positions: true,
		products:  []contract.ProductCapability{{Kind: enums.KindPerp, Trading: true, Account: true}},
	}
	exec.massFn = func(query model.MassStatusQuery) *model.ExecutionMassStatus {
		mass := typedCoverageMass(query, []model.InstrumentID{btc}, model.CoverageComplete, model.CoverageNotRequested, model.CoverageComplete)
		if err := mass.AddPositionReport(model.PositionReport{Venue: "T", AccountID: "acct", Position: model.Position{
			AccountID: "acct", InstrumentID: btc, Side: enums.PosNet,
		}}); err != nil {
			t.Fatalf("add position report: %v", err)
		}
		exec.products = []contract.ProductCapability{{Kind: enums.KindSpot, Trading: true, Account: true}}
		return mass
	}
	rep, err := New(nil, exec, c).WithAccountID("acct").Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !hasFindingCode(rep.Findings, "POSITION_MISMATCH") {
		t.Fatalf("findings=%+v, frozen typed selector must ignore post-response capability mutation", rep.Findings)
	}
}

func TestTypedRuntimeRejectsBlankProducerAccount(t *testing.T) {
	exec := &snapshotExec{massFn: func(query model.MassStatusQuery) *model.ExecutionMassStatus {
		mass := typedCoverageMass(query, []model.InstrumentID{btc}, model.CoverageComplete, model.CoverageNotRequested, model.CoverageNotRequested)
		mass.AccountID = ""
		return mass
	}}
	if _, err := New(nil, exec, cache.New()).WithAccountID("acct").Run(context.Background()); err == nil {
		t.Fatal("typed runtime accepted a producer response with a blank account")
	}
}

func TestTypedSnapshotFillInferenceRequiresMatchingFillCoverage(t *testing.T) {
	start := time.Unix(10_000, 0)
	tests := []struct {
		name        string
		fillHistory bool
		fillState   model.CoverageState
		fillIDs     []model.InstrumentID
		updatedAt   func(model.MassStatusQuery) time.Time
		cursor      Cursor
	}{
		{
			name:        "not requested",
			fillHistory: false,
			fillState:   model.CoverageNotRequested,
			fillIDs:     []model.InstrumentID{btc},
			updatedAt:   func(query model.MassStatusQuery) time.Time { return query.Until },
		},
		{
			name:        "unavailable",
			fillHistory: true,
			fillState:   model.CoverageUnavailable,
			fillIDs:     []model.InstrumentID{btc},
			updatedAt:   func(query model.MassStatusQuery) time.Time { return query.Until },
		},
		{
			name:        "selector mismatch",
			fillHistory: true,
			fillState:   model.CoverageComplete,
			fillIDs:     []model.InstrumentID{eth},
			updatedAt:   func(query model.MassStatusQuery) time.Time { return query.Until },
		},
		{
			name:        "outside interval",
			fillHistory: true,
			fillState:   model.CoverageComplete,
			fillIDs:     []model.InstrumentID{btc},
			updatedAt:   func(query model.MassStatusQuery) time.Time { return query.Since.Add(-time.Second) },
			cursor: Cursor{
				Scope: ScopeKey{Venue: "T", AccountID: "acct"}, Stream: StreamOrders,
				FillInstrumentIDs: []model.InstrumentID{btc}, LastVenueTime: start.Add(-10 * time.Minute),
				LookbackFloor: start.Add(-time.Hour),
			},
		},
		{
			name:        "missing venue event time",
			fillHistory: true,
			fillState:   model.CoverageComplete,
			fillIDs:     []model.InstrumentID{btc},
			updatedAt:   func(model.MassStatusQuery) time.Time { return time.Time{} },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := cache.New()
			known := accountOrder("acct", "progress", btc)
			known.Request.Quantity = d("2")
			known.Request.Side = enums.SideBuy
			c.UpsertOrder(known)

			store := &countingCursorStore{cursor: tt.cursor}
			exec := &snapshotExec{fillHistory: tt.fillHistory}
			exec.massFn = func(query model.MassStatusQuery) *model.ExecutionMassStatus {
				mass := typedCoverageMass(query, []model.InstrumentID{btc}, model.CoverageComplete, tt.fillState, model.CoverageNotRequested)
				if query.IncludeFills {
					mass.FillsCoverage = model.NewFillCoverage(tt.fillState, query.AccountID, query.ClientID, tt.fillIDs, query.Since, query.Until)
				}
				snapshot := known
				snapshot.Status = enums.StatusPartiallyFilled
				snapshot.FilledQty = d("1")
				snapshot.AvgFillPrice = d("100")
				snapshot.UpdatedAt = tt.updatedAt(query)
				if err := mass.AddOrderReport(model.OrderStatusReport{Venue: "T", AccountID: "acct", Order: snapshot}); err != nil {
					t.Fatalf("add order report: %v", err)
				}
				return mass
			}

			var applied []model.Fill
			rep, err := New(nil, exec, c).
				WithAccountID("acct").
				WithClock(clock.NewSimulatedClock(start)).
				WithStateStore(store).
				WithFillApplier(func(fill model.Fill, _ contract.EventMeta) FillApplyResult {
					applied = append(applied, fill)
					return FillApplyApplied
				}).
				Run(context.Background())
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if len(applied) != 0 || rep.FillsInferred != 0 {
				t.Fatalf("applied=%+v report=%+v, typed inference escaped fill coverage", applied, rep)
			}
			if !hasFindingCode(rep.Findings, "ORDER_PROGRESS_WITHOUT_FILL") {
				t.Fatalf("findings=%+v, uncovered progress must remain blocking", rep.Findings)
			}
		})
	}
}

func TestFillCursorSelectorResetClearsOldTemporalFloor(t *testing.T) {
	start := time.Unix(20_000, 0)
	store := &countingCursorStore{cursor: Cursor{
		Scope: ScopeKey{Venue: "T", AccountID: "acct"}, Stream: StreamOrders,
		FillInstrumentIDs: []model.InstrumentID{btc},
		LastVenueTime:     start.Add(-10 * time.Minute),
		LookbackFloor:     start.Add(-time.Hour),
	}}
	exec := &snapshotExec{fillHistory: true, massFn: func(query model.MassStatusQuery) *model.ExecutionMassStatus {
		return typedCoverageMass(query, []model.InstrumentID{eth}, model.CoverageComplete, model.CoverageComplete, model.CoverageNotRequested)
	}}
	rep, err := New(nil, exec, cache.New()).
		WithAccountID("acct").
		WithClock(clock.NewSimulatedClock(start)).
		WithStateStore(store).
		Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !rep.FillsPartial || !store.cursor.FillsPartial {
		t.Fatalf("report=%+v cursor=%+v, selector change must reset fail-closed", rep, store.cursor)
	}
	if !store.cursor.LastVenueTime.IsZero() || !store.cursor.LookbackFloor.IsZero() {
		t.Fatalf("selector reset retained old temporal authority: %+v", store.cursor)
	}
	if len(store.cursor.FillInstrumentIDs) != 1 || store.cursor.FillInstrumentIDs[0] != eth {
		t.Fatalf("reset selector=%+v, want ETH", store.cursor.FillInstrumentIDs)
	}
}

func TestFillCursorResetRejectsAnotherSelectorChange(t *testing.T) {
	start := time.Unix(20_000, 0)
	store := &countingCursorStore{cursor: Cursor{
		Scope: ScopeKey{Venue: "T", AccountID: "acct"}, Stream: StreamOrders,
		FillInstrumentIDs: []model.InstrumentID{btc, eth},
		LastVenueTime:     start.Add(-10 * time.Minute),
		LookbackFloor:     start.Add(-time.Hour),
	}}
	call := 0
	exec := &snapshotExec{fillHistory: true, massFn: func(query model.MassStatusQuery) *model.ExecutionMassStatus {
		call++
		ids := []model.InstrumentID{btc}
		if call == 2 {
			ids = []model.InstrumentID{eth}
		}
		return typedCoverageMass(query, ids, model.CoverageComplete, model.CoverageComplete, model.CoverageNotRequested)
	}}
	r := New(nil, exec, cache.New()).
		WithAccountID("acct").
		WithClock(clock.NewSimulatedClock(start)).
		WithStateStore(store)
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatalf("first run: %v", err)
	}
	second, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if !second.FillsPartial || !store.cursor.FillsPartial {
		t.Fatalf("second=%+v cursor=%+v, second selector change must reset again", second, store.cursor)
	}
	if !store.cursor.LastVenueTime.IsZero() || !store.cursor.LookbackFloor.IsZero() {
		t.Fatalf("second selector reset advanced stale temporal authority: %+v", store.cursor)
	}
	if len(store.cursor.FillInstrumentIDs) != 1 || store.cursor.FillInstrumentIDs[0] != eth {
		t.Fatalf("second reset selector=%+v, want ETH", store.cursor.FillInstrumentIDs)
	}
}

func TestPartialFillCursorCannotAuthorizeStrictQuerySubset(t *testing.T) {
	query := model.MassStatusQuery{InstrumentIDs: []model.InstrumentID{btc, eth}}
	cursor := Cursor{FillsPartial: true, FillInstrumentIDs: []model.InstrumentID{btc}}
	coverage := model.NewFillCoverage(model.CoverageComplete, "acct", "", []model.InstrumentID{btc}, time.Time{}, time.Unix(2, 0))
	if fillCursorScopeCompatible(query, cursor, coverage) {
		t.Fatal("partial reset made a strict query subset advanceable")
	}
}

func TestPostRequestReobservationOfFrozenFindingVersionRemainsOpen(t *testing.T) {
	ctx := context.Background()
	start := time.Unix(30_000, 0)
	scope := ScopeKey{Venue: "T", AccountID: "acct"}
	store := NewJournalStateStore(journal.NewMemory())
	condition := positionKeyString(positionKey{"acct", btc.String(), enums.PosNet})
	before := Finding{
		ID: "same-condition", PassID: "before", Scope: scope, Stream: StreamPositions,
		Severity: FindingBlocking, Code: "POSITION_MISMATCH", Blocking: true,
		Message:   "position " + condition + " local quantity 1 differs from authoritative quantity 0",
		CreatedAt: start.Add(-time.Second),
	}
	if err := store.RecordFinding(ctx, before); err != nil {
		t.Fatalf("record initial finding: %v", err)
	}
	c := cache.New()
	c.UpsertPosition(model.Position{AccountID: "acct", InstrumentID: btc, Side: enums.PosNet, Quantity: d("1")})
	exec := &snapshotExec{positions: true}
	exec.massFn = func(query model.MassStatusQuery) *model.ExecutionMassStatus {
		reobserved := before
		reobserved.PassID = "after-request"
		reobserved.CreatedAt = start.Add(time.Second)
		reobserved.Message = "position " + condition + " local quantity 1 differs from authoritative quantity 2"
		if err := store.RecordFinding(ctx, reobserved); err != nil {
			t.Fatalf("record post-request reobservation: %v", err)
		}
		mass := typedCoverageMass(query, []model.InstrumentID{btc}, model.CoverageComplete, model.CoverageNotRequested, model.CoverageComplete)
		if err := mass.AddPositionReport(model.PositionReport{Venue: "T", AccountID: "acct", Position: model.Position{
			AccountID: "acct", InstrumentID: btc, Side: enums.PosNet, Quantity: d("1"),
		}}); err != nil {
			t.Fatalf("add matching position: %v", err)
		}
		return mass
	}
	rep, err := New(nil, exec, c).
		WithAccountID("acct").
		WithClock(clock.NewSimulatedClock(start)).
		WithStateStore(store).
		Run(ctx)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	open, err := store.LoadOpenFindings(ctx, scope)
	if err != nil {
		t.Fatalf("load findings: %v", err)
	}
	if len(open) != 1 || open[0].ID != before.ID || open[0].PassID != "after-request" {
		t.Fatalf("open=%+v, newer same-ID reobservation must survive old snapshot resolution", open)
	}
	if rep.ActivationVerdict().Safe {
		t.Fatalf("report=%+v, newer same-ID finding must remain blocking", rep)
	}
}

func TestSnapshotObservationWatermarkIsIndependentOfVenueEventTime(t *testing.T) {
	start := time.Unix(100, 0)
	store := &countingCursorStore{}
	exec := &snapshotExec{massFn: func(query model.MassStatusQuery) *model.ExecutionMassStatus {
		mass := typedCoverageMass(query, []model.InstrumentID{btc}, model.CoverageComplete, model.CoverageNotRequested, model.CoverageNotRequested)
		venueOrder := accountOrder("acct", "venue-time", btc)
		venueOrder.UpdatedAt = start.Add(100 * 365 * 24 * time.Hour)
		if err := mass.AddOrderReport(model.OrderStatusReport{
			Venue: "T", AccountID: "acct", Order: venueOrder, ReportedAt: start.Add(-100 * 365 * 24 * time.Hour),
		}); err != nil {
			t.Fatalf("add order report: %v", err)
		}
		return mass
	}}
	rep, err := New(nil, exec, cache.New()).
		WithAccountID("acct").
		WithClock(clock.NewSimulatedClock(start)).
		WithStateStore(store).
		Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !rep.OpenOrdersCoverage.Scope.Through.Equal(start) || !store.cursor.LastVenueTime.Equal(start) {
		t.Fatalf("coverage=%+v cursor=%+v, want local request-start %s", rep.OpenOrdersCoverage, store.cursor, start)
	}
}

func TestCompleteEmptyPositionCoverageSuppressesFallbackAndRequery(t *testing.T) {
	start := time.Unix(100, 0)
	acct := &snapshotAccount{
		accountID:       "acct",
		accountState:    authoritativeSnapshot("acct", start),
		positionReports: true,
		products:        []contract.ProductCapability{{Kind: enums.KindPerp, Account: true}},
	}
	exec := &snapshotExec{positions: true, massFn: func(query model.MassStatusQuery) *model.ExecutionMassStatus {
		return typedCoverageMass(query, []model.InstrumentID{btc}, model.CoverageComplete, model.CoverageNotRequested, model.CoverageComplete)
	}}
	rep, err := newReconcilerWithAccountCapabilities(acct, exec, cache.New()).WithAccountID("acct").WithClock(clock.NewSimulatedClock(start)).Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if acct.positionCalls != 0 {
		t.Fatalf("Positions calls=%d, complete-empty execution evidence must suppress fallback", acct.positionCalls)
	}
	if rep.PositionsCoverage.State != model.CoverageComplete || !rep.PositionsCoverage.Scope.Through.Equal(start) {
		t.Fatalf("positions coverage=%+v, want complete at %s", rep.PositionsCoverage, start)
	}
}

func TestFallbackCompleteEmptyCapturesOwnRequestStartWatermark(t *testing.T) {
	start := time.Unix(100, 0)
	clk := clock.NewSimulatedClock(start)
	acct := &snapshotAccount{
		accountID:       "acct",
		accountState:    authoritativeSnapshot("acct", start),
		positionReports: true,
		products:        []contract.ProductCapability{{Kind: enums.KindPerp, Account: true}},
	}
	exec := &snapshotExec{positions: true, massFn: func(query model.MassStatusQuery) *model.ExecutionMassStatus {
		mass := typedCoverageMass(query, []model.InstrumentID{btc}, model.CoverageComplete, model.CoverageNotRequested, model.CoveragePartial)
		clk.Advance(5 * time.Second)
		return mass
	}}
	rep, err := newReconcilerWithAccountCapabilities(acct, exec, cache.New()).WithAccountID("acct").WithClock(clk).Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if acct.positionCalls != 1 {
		t.Fatalf("Positions calls=%d, want 1", acct.positionCalls)
	}
	want := start.Add(5 * time.Second)
	if rep.PositionsCoverage.State != model.CoverageComplete || !rep.PositionsCoverage.Scope.Through.Equal(want) {
		t.Fatalf("positions coverage=%+v, want complete fallback watermark %s", rep.PositionsCoverage, want)
	}
}

func TestIncompletePositionFallbackDoesNotMergePartialRows(t *testing.T) {
	start := time.Unix(100, 0)
	partial := model.Position{AccountID: "acct", InstrumentID: btc, Quantity: d("1"), Side: enums.PosNet}
	authoritative := model.Position{AccountID: "acct", InstrumentID: btc, Quantity: d("2"), Side: enums.PosNet}
	acct := &snapshotAccount{
		accountID:       "acct",
		accountState:    authoritativeSnapshot("acct", start),
		positionReports: true,
		products:        []contract.ProductCapability{{Kind: enums.KindPerp, Account: true}},
		positions:       []model.Position{authoritative},
	}
	exec := &snapshotExec{positions: true, massFn: func(query model.MassStatusQuery) *model.ExecutionMassStatus {
		mass := typedCoverageMass(query, []model.InstrumentID{btc}, model.CoverageComplete, model.CoverageNotRequested, model.CoveragePartial)
		if err := mass.AddPositionReport(model.PositionReport{Venue: "T", AccountID: "acct", Position: partial}); err != nil {
			t.Fatalf("add partial position: %v", err)
		}
		return mass
	}}
	rep, err := newReconcilerWithAccountCapabilities(acct, exec, cache.New()).WithAccountID("acct").WithClock(clock.NewSimulatedClock(start)).Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if acct.positionCalls != 1 {
		t.Fatalf("Positions calls=%d, want 1", acct.positionCalls)
	}
	if rep.PositionsCoverage.State != model.CoverageComplete {
		t.Fatalf("positions coverage=%+v, want Complete", rep.PositionsCoverage)
	}
	for _, finding := range rep.Findings {
		if finding.Code == "POSITION_REPORT_AMBIGUOUS" {
			t.Fatalf("fallback merged partial and authoritative rows: %+v", finding)
		}
	}
}

func TestPartialPositionCoverageUsesOnlyExplicitRows(t *testing.T) {
	c := cache.New()
	c.UpsertPosition(model.Position{AccountID: "acct", InstrumentID: btc, Side: enums.PosNet, Quantity: d("1")})
	c.UpsertPosition(model.Position{AccountID: "acct", InstrumentID: eth, Side: enums.PosNet, Quantity: d("2")})
	exec := &snapshotExec{positions: true, massFn: func(query model.MassStatusQuery) *model.ExecutionMassStatus {
		mass := typedCoverageMass(query, []model.InstrumentID{btc, eth}, model.CoverageComplete, model.CoverageNotRequested, model.CoveragePartial)
		if err := mass.AddPositionReport(model.PositionReport{Venue: "T", AccountID: "acct", Position: model.Position{
			AccountID: "acct", InstrumentID: btc, Side: enums.PosNet,
		}}); err != nil {
			t.Fatalf("add explicit flat position: %v", err)
		}
		return mass
	}}
	rep, err := New(nil, exec, c).WithAccountID("acct").Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	var btcMismatch, ethMismatch bool
	for _, finding := range rep.Findings {
		if finding.Code != "POSITION_MISMATCH" {
			continue
		}
		btcMismatch = btcMismatch || strings.Contains(finding.Message, btc.String())
		ethMismatch = ethMismatch || strings.Contains(finding.Message, eth.String())
	}
	if !btcMismatch || ethMismatch {
		t.Fatalf("findings=%+v, want mismatch only for explicitly reported BTC row", rep.Findings)
	}
}

func TestFailedOrUnprovenPositionFallbackPreservesIncompleteState(t *testing.T) {
	start := time.Unix(100, 0)
	tests := []struct {
		name          string
		account       *snapshotAccount
		mutateMass    func(*model.ExecutionMassStatus)
		wantPositions int
	}{
		{
			name: "request error",
			account: &snapshotAccount{
				accountID:       "acct",
				accountState:    authoritativeSnapshot("acct", start),
				positionReports: true,
				products:        []contract.ProductCapability{{Kind: enums.KindPerp, Account: true}},
				positionErr:     errors.New("positions unavailable"),
			},
			wantPositions: 1,
		},
		{
			name:          "capability cannot prove selector",
			account:       &snapshotAccount{accountID: "acct", accountState: authoritativeSnapshot("acct", start), positionReports: true},
			wantPositions: 0,
		},
		{
			name: "returned row outside selector",
			account: &snapshotAccount{
				accountID:       "acct",
				accountState:    authoritativeSnapshot("acct", start),
				positionReports: true,
				products:        []contract.ProductCapability{{Kind: enums.KindPerp, Account: true}},
				positions:       []model.Position{{AccountID: "acct", InstrumentID: eth, Side: enums.PosNet, Quantity: d("1")}},
			},
			wantPositions: 1,
		},
		{
			name: "fallback clock precedes execution observation",
			account: &snapshotAccount{
				accountID:       "acct",
				accountState:    authoritativeSnapshot("acct", start),
				positionReports: true,
				products:        []contract.ProductCapability{{Kind: enums.KindPerp, Account: true}},
			},
			mutateMass: func(mass *model.ExecutionMassStatus) {
				mass.PositionsCoverage.Scope.Through = start.Add(time.Second)
			},
			wantPositions: 0,
		},
		{
			name: "account provider mismatch on complete empty result",
			account: &snapshotAccount{
				accountID:       "other",
				accountState:    authoritativeSnapshot("acct", start),
				positionReports: true,
				products:        []contract.ProductCapability{{Kind: enums.KindPerp, Account: true}},
			},
			wantPositions: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exec := &snapshotExec{positions: true, massFn: func(query model.MassStatusQuery) *model.ExecutionMassStatus {
				mass := typedCoverageMass(query, []model.InstrumentID{btc}, model.CoverageComplete, model.CoverageNotRequested, model.CoveragePartial)
				if tt.mutateMass != nil {
					tt.mutateMass(mass)
				}
				return mass
			}}
			rep, err := newReconcilerWithAccountCapabilities(tt.account, exec, cache.New()).WithAccountID("acct").WithClock(clock.NewSimulatedClock(start)).Run(context.Background())
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if tt.account.positionCalls != tt.wantPositions {
				t.Fatalf("Positions calls=%d, want %d", tt.account.positionCalls, tt.wantPositions)
			}
			if rep.PositionsCoverage.State != model.CoveragePartial || rep.PositionsCoverage.Scope.InstrumentIDs[0] != btc {
				t.Fatalf("positions coverage=%+v, want original partial BTC scope", rep.PositionsCoverage)
			}
			if rep.ActivationVerdict().Safe {
				t.Fatalf("incomplete fallback unexpectedly activated: %+v", rep)
			}
		})
	}
}

func TestNotRequestedPositionsNeverFallback(t *testing.T) {
	acct := &snapshotAccount{accountState: authoritativeSnapshot("acct", time.Unix(1, 0))}
	exec := &snapshotExec{massFn: func(query model.MassStatusQuery) *model.ExecutionMassStatus {
		if query.IncludePositions {
			t.Fatal("positions unexpectedly requested")
		}
		return typedCoverageMass(query, []model.InstrumentID{btc}, model.CoverageComplete, model.CoverageNotRequested, model.CoverageNotRequested)
	}}
	rep, err := newReconcilerWithAccountCapabilities(acct, exec, cache.New()).WithAccountID("acct").Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if acct.positionCalls != 0 || rep.PositionsCoverage.State != model.CoverageNotRequested {
		t.Fatalf("account calls=%d positions coverage=%+v", acct.positionCalls, rep.PositionsCoverage)
	}
}

func TestIncompleteFillCoverageDoesNotCommitFullCursor(t *testing.T) {
	start := time.Unix(100, 0)
	store := &countingCursorStore{}
	exec := &snapshotExec{fillHistory: true, massFn: func(query model.MassStatusQuery) *model.ExecutionMassStatus {
		return typedCoverageMass(query, []model.InstrumentID{btc}, model.CoverageComplete, model.CoveragePartial, model.CoverageNotRequested)
	}}
	rep, err := New(nil, exec, cache.New()).
		WithAccountID("acct").
		WithClock(clock.NewSimulatedClock(start)).
		WithStateStore(store).
		Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if store.commits != 0 || rep.CursorsCommitted != 0 {
		t.Fatalf("incomplete fill coverage committed cursor: store=%d report=%d", store.commits, rep.CursorsCommitted)
	}
	if !rep.FillsPartial || rep.ActivationVerdict().Safe {
		t.Fatalf("report=%+v, want typed incomplete fill fail-closed", rep)
	}
}

func TestCompleteFillCoverageCommitsOnlyMatchingInterval(t *testing.T) {
	start := time.Unix(200, 0)
	store := &countingCursorStore{cursor: Cursor{
		Scope: ScopeKey{Venue: "T", AccountID: "acct"}, Stream: StreamOrders,
		FillInstrumentIDs: []model.InstrumentID{btc},
		LastVenueTime:     start.Add(-10 * time.Minute), LookbackFloor: start.Add(-time.Hour),
	}}
	var query model.MassStatusQuery
	exec := &snapshotExec{fillHistory: true, massFn: func(got model.MassStatusQuery) *model.ExecutionMassStatus {
		query = got
		return typedCoverageMass(got, []model.InstrumentID{btc}, model.CoverageComplete, model.CoverageComplete, model.CoverageNotRequested)
	}}
	rep, err := New(nil, exec, cache.New()).
		WithAccountID("acct").
		WithClock(clock.NewSimulatedClock(start)).
		WithStateStore(store).
		Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if store.commits != 1 || rep.CursorsCommitted != 1 {
		t.Fatalf("cursor commits store=%d report=%d, want 1", store.commits, rep.CursorsCommitted)
	}
	if !store.cursor.LastVenueTime.Equal(query.Until) || !store.cursor.LookbackFloor.Equal(query.Since) {
		t.Fatalf("cursor=%+v query=%+v, want exact complete fill interval", store.cursor, query)
	}
	if len(store.cursor.FillInstrumentIDs) != 1 || store.cursor.FillInstrumentIDs[0] != btc {
		t.Fatalf("cursor selector=%+v, want BTC", store.cursor.FillInstrumentIDs)
	}
}

func TestCompleteFillCoverageCannotAdvanceCursorForStrictQuerySubset(t *testing.T) {
	query := model.MassStatusQuery{InstrumentIDs: []model.InstrumentID{btc, eth}}
	coverage := model.NewFillCoverage(model.CoverageComplete, "acct", "", []model.InstrumentID{btc}, time.Unix(1, 0), time.Unix(2, 0))
	if fillCursorScopeCompatible(query, Cursor{}, coverage) {
		t.Fatal("strict subset fill coverage was accepted for a wider query cursor")
	}
}

func TestCompleteFillCoverageResetsBeforeAdvancingChangedSelector(t *testing.T) {
	start := time.Unix(200, 0)
	store := &countingCursorStore{cursor: Cursor{
		Scope: ScopeKey{Venue: "T", AccountID: "acct"}, Stream: StreamOrders,
		FillInstrumentIDs: []model.InstrumentID{btc, eth},
		LastVenueTime:     start.Add(-10 * time.Minute),
	}}
	queries := make([]model.MassStatusQuery, 0, 2)
	exec := &snapshotExec{fillHistory: true, massFn: func(query model.MassStatusQuery) *model.ExecutionMassStatus {
		queries = append(queries, query)
		return typedCoverageMass(query, []model.InstrumentID{btc}, model.CoverageComplete, model.CoverageComplete, model.CoverageNotRequested)
	}}
	r := New(nil, exec, cache.New()).
		WithAccountID("acct").
		WithClock(clock.NewSimulatedClock(start)).
		WithStateStore(store)

	first, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if !first.FillsPartial || first.ActivationVerdict().Safe {
		t.Fatalf("first report=%+v, changed selector must fail closed", first)
	}
	if !store.cursor.FillsPartial || !store.cursor.LastVenueTime.IsZero() || !store.cursor.LookbackFloor.IsZero() {
		t.Fatalf("reset cursor retained prior temporal authority: %+v", store.cursor)
	}
	if len(store.cursor.FillInstrumentIDs) != 1 || store.cursor.FillInstrumentIDs[0] != btc {
		t.Fatalf("reset selector=%+v, want BTC", store.cursor.FillInstrumentIDs)
	}

	second, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if second.FillsPartial || !second.ActivationVerdict().Safe {
		t.Fatalf("second report=%+v, full reset interval should restore continuity", second)
	}
	if store.cursor.FillsPartial || !store.cursor.LastVenueTime.Equal(queries[1].Until) {
		t.Fatalf("second cursor=%+v query=%+v, want complete advance", store.cursor, queries[1])
	}
	if !queries[1].Since.IsZero() {
		t.Fatalf("reset query Since=%s, want zero lookback floor", queries[1].Since)
	}
}

func TestPostRequestFindingCannotBeResolvedByOlderCoverage(t *testing.T) {
	start := time.Unix(100, 0)
	store := NewJournalStateStore(journal.NewMemory())
	postRequest := Finding{
		ID: "post-request-position", PassID: "concurrent", Scope: ScopeKey{Venue: "T", AccountID: "acct"},
		Stream: StreamPositions, Severity: FindingBlocking, Code: "POSITION_MISMATCH", Blocking: true,
		Message:   "position " + positionKeyString(positionKey{"acct", btc.String(), enums.PosNet}) + " local quantity 1 differs from authoritative quantity 0",
		CreatedAt: start,
	}
	exec := &snapshotExec{positions: true, massFn: func(query model.MassStatusQuery) *model.ExecutionMassStatus {
		if err := store.RecordFinding(context.Background(), postRequest); err != nil {
			t.Fatalf("record concurrent finding: %v", err)
		}
		return typedCoverageMass(query, []model.InstrumentID{btc}, model.CoverageComplete, model.CoverageNotRequested, model.CoverageComplete)
	}}
	rep, err := New(nil, exec, cache.New()).
		WithAccountID("acct").
		WithClock(clock.NewSimulatedClock(start)).
		WithStateStore(store).
		Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	open, err := store.LoadOpenFindings(context.Background(), postRequest.Scope)
	if err != nil {
		t.Fatalf("load open findings: %v", err)
	}
	if len(open) != 1 || open[0].ID != postRequest.ID {
		t.Fatalf("post-request finding was resolved by older coverage: %+v", open)
	}
	if rep.ActivationVerdict().Safe {
		t.Fatalf("post-request blocking finding missing from final report: %+v", rep)
	}
}

func TestReconcilerUsesInjectedClockForMassStatusQuery(t *testing.T) {
	start := time.Unix(1234, 0)
	exec := &snapshotExec{massFn: func(query model.MassStatusQuery) *model.ExecutionMassStatus {
		if !query.Until.Equal(start) {
			t.Fatalf("query Until=%s, want injected clock %s", query.Until, start)
		}
		if query.Venue != "T" {
			t.Fatalf("query Venue=%q, want T", query.Venue)
		}
		return typedCoverageMass(query, []model.InstrumentID{}, model.CoverageComplete, model.CoverageNotRequested, model.CoverageNotRequested)
	}}
	if _, err := New(nil, exec, cache.New()).WithAccountID("acct").WithClock(clock.NewSimulatedClock(start)).Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
}
