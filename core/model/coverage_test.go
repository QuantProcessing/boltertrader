package model

import (
	"slices"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/shopspring/decimal"
)

func coverageTestIDs() []InstrumentID {
	return []InstrumentID{
		{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindPerp},
		{Venue: "T", Symbol: "ETH-USDT", Kind: enums.KindPerp},
	}
}

func validCoverageMass(query MassStatusQuery) *ExecutionMassStatus {
	through := query.Until
	if through.IsZero() {
		through = time.Unix(200, 0)
	}
	from := query.Since
	if from.IsZero() && query.Lookback > 0 {
		from = through.Add(-query.Lookback)
	}
	accountID := query.AccountID
	if accountID == "" {
		accountID = "acct"
	}
	ids := query.InstrumentIDs
	if ids == nil {
		ids = coverageTestIDs()
	}
	mass := NewExecutionMassStatus("T", accountID, through)
	mass.ClientID = query.ClientID
	mass.OpenOrdersCoverage = NewSnapshotCoverage(CoverageComplete, accountID, query.ClientID, ids, through.Add(-time.Second))
	if query.IncludeFills {
		mass.FillsCoverage = NewFillCoverage(CoverageComplete, accountID, query.ClientID, ids, from, through)
	} else {
		mass.FillsCoverage = ReportCoverage{State: CoverageNotRequested}
	}
	if query.IncludePositions {
		mass.PositionsCoverage = NewSnapshotCoverage(CoverageComplete, accountID, query.ClientID, ids, through.Add(-time.Second))
	} else {
		mass.PositionsCoverage = ReportCoverage{State: CoverageNotRequested}
	}
	return mass
}

func TestCoverageSelectorNormalizationAndOwnership(t *testing.T) {
	input := []InstrumentID{
		{Venue: " T ", Symbol: " ETH-USDT ", Kind: enums.KindPerp},
		{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindPerp},
		{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindPerp},
	}
	want := coverageTestIDs()
	got := NormalizeInstrumentIDs(input)
	if !slices.Equal(got, want) {
		t.Fatalf("NormalizeInstrumentIDs=%+v, want %+v", got, want)
	}
	input[0].Symbol = "MUTATED"
	if !slices.Equal(got, want) {
		t.Fatalf("normalized selector aliases input: %+v", got)
	}

	coverage := NewSnapshotCoverage(CoverageComplete, "acct", "client", got, time.Unix(10, 0))
	got[0].Symbol = "MUTATED-AGAIN"
	if !slices.Equal(coverage.Scope.InstrumentIDs, want) {
		t.Fatalf("coverage selector aliases constructor input: %+v", coverage.Scope.InstrumentIDs)
	}
	mass := NewExecutionMassStatus("T", "acct", time.Unix(10, 0))
	mass.OpenOrdersCoverage = coverage
	clone := mass.Clone()
	clone.OpenOrdersCoverage.Scope.InstrumentIDs[0].Symbol = "CLONE-MUTATION"
	if !slices.Equal(mass.OpenOrdersCoverage.Scope.InstrumentIDs, want) {
		t.Fatalf("coverage selector aliases clone: %+v", mass.OpenOrdersCoverage.Scope.InstrumentIDs)
	}

	empty := NormalizeInstrumentIDs([]InstrumentID{})
	if empty == nil || len(empty) != 0 {
		t.Fatalf("empty selector=%v, want non-nil empty", empty)
	}
	if NormalizeInstrumentIDs(nil) != nil {
		t.Fatal("nil selector must remain nil")
	}
}

func TestCoverageValidateForStateSpecificScopeShapes(t *testing.T) {
	from := time.Unix(100, 0)
	through := time.Unix(200, 0)
	query := MassStatusQuery{
		Venue: "T", AccountID: "acct", ClientID: "client", InstrumentIDs: coverageTestIDs(),
		Since: from, Until: through, IncludeFills: true, IncludePositions: true,
	}
	tests := []struct {
		name    string
		mutate  func(*ExecutionMassStatus, *MassStatusQuery)
		wantErr bool
	}{
		{name: "complete"},
		{name: "partial with full scope", mutate: func(m *ExecutionMassStatus, _ *MassStatusQuery) { m.OpenOrdersCoverage.State = CoveragePartial }},
		{name: "unavailable before request", mutate: func(m *ExecutionMassStatus, _ *MassStatusQuery) {
			m.OpenOrdersCoverage = ReportCoverage{State: CoverageUnavailable}
		}},
		{name: "unavailable after attempt", mutate: func(m *ExecutionMassStatus, _ *MassStatusQuery) { m.OpenOrdersCoverage.State = CoverageUnavailable }},
		{name: "complete empty selector", mutate: func(m *ExecutionMassStatus, _ *MassStatusQuery) {
			m.OpenOrdersCoverage.Scope.InstrumentIDs = []InstrumentID{}
		}},
		{name: "unknown requested", mutate: func(m *ExecutionMassStatus, _ *MassStatusQuery) { m.OpenOrdersCoverage = ReportCoverage{} }, wantErr: true},
		{name: "unknown with scope", mutate: func(m *ExecutionMassStatus, _ *MassStatusQuery) { m.OpenOrdersCoverage.State = CoverageUnknown }, wantErr: true},
		{name: "open orders not requested", mutate: func(m *ExecutionMassStatus, _ *MassStatusQuery) {
			m.OpenOrdersCoverage = ReportCoverage{State: CoverageNotRequested}
		}, wantErr: true},
		{name: "complete nil selector", mutate: func(m *ExecutionMassStatus, _ *MassStatusQuery) { m.OpenOrdersCoverage.Scope.InstrumentIDs = nil }, wantErr: true},
		{name: "hybrid unavailable", mutate: func(m *ExecutionMassStatus, _ *MassStatusQuery) {
			m.OpenOrdersCoverage = ReportCoverage{State: CoverageUnavailable, Scope: CoverageScope{AccountID: "acct"}}
		}, wantErr: true},
		{name: "snapshot from set", mutate: func(m *ExecutionMassStatus, _ *MassStatusQuery) { m.PositionsCoverage.Scope.From = from }, wantErr: true},
		{name: "fill inverted interval", mutate: func(m *ExecutionMassStatus, _ *MassStatusQuery) {
			m.FillsCoverage.Scope.From = through.Add(time.Second)
		}, wantErr: true},
		{name: "fills not requested while included", mutate: func(m *ExecutionMassStatus, _ *MassStatusQuery) {
			m.FillsCoverage = ReportCoverage{State: CoverageNotRequested}
		}, wantErr: true},
		{name: "positions not requested while included", mutate: func(m *ExecutionMassStatus, _ *MassStatusQuery) {
			m.PositionsCoverage = ReportCoverage{State: CoverageNotRequested}
		}, wantErr: true},
		{name: "fills omitted", mutate: func(m *ExecutionMassStatus, q *MassStatusQuery) {
			q.IncludeFills = false
			m.FillsCoverage = ReportCoverage{State: CoverageNotRequested}
		}},
		{name: "positions omitted", mutate: func(m *ExecutionMassStatus, q *MassStatusQuery) {
			q.IncludePositions = false
			m.PositionsCoverage = ReportCoverage{State: CoverageNotRequested}
		}},
		{name: "blank query account normalizes", mutate: func(_ *ExecutionMassStatus, q *MassStatusQuery) { q.AccountID = "" }},
		{name: "query account mismatch", mutate: func(_ *ExecutionMassStatus, q *MassStatusQuery) { q.AccountID = "other" }, wantErr: true},
		{name: "blank query venue normalizes", mutate: func(_ *ExecutionMassStatus, q *MassStatusQuery) { q.Venue = "" }},
		{name: "query venue mismatch", mutate: func(_ *ExecutionMassStatus, q *MassStatusQuery) { q.Venue = "OTHER" }, wantErr: true},
		{name: "response venue missing", mutate: func(m *ExecutionMassStatus, _ *MassStatusQuery) { m.Venue = "" }, wantErr: true},
		{name: "response client mismatch", mutate: func(m *ExecutionMassStatus, _ *MassStatusQuery) { m.ClientID = "other" }, wantErr: true},
		{name: "coverage account mismatch", mutate: func(m *ExecutionMassStatus, _ *MassStatusQuery) { m.OpenOrdersCoverage.Scope.AccountID = "other" }, wantErr: true},
		{name: "coverage client mismatch", mutate: func(m *ExecutionMassStatus, _ *MassStatusQuery) { m.OpenOrdersCoverage.Scope.ClientID = "other" }, wantErr: true},
		{name: "coverage selector venue mismatch", mutate: func(m *ExecutionMassStatus, _ *MassStatusQuery) {
			m.OpenOrdersCoverage.Scope.InstrumentIDs[0].Venue = "OTHER"
		}, wantErr: true},
		{name: "query selector venue mismatch", mutate: func(_ *ExecutionMassStatus, q *MassStatusQuery) {
			q.InstrumentIDs[0].Venue = "OTHER"
		}, wantErr: true},
		{name: "coverage selector wider than query", mutate: func(m *ExecutionMassStatus, _ *MassStatusQuery) {
			m.OpenOrdersCoverage.Scope.InstrumentIDs = append(m.OpenOrdersCoverage.Scope.InstrumentIDs, InstrumentID{Venue: "T", Symbol: "SOL-USDT", Kind: enums.KindPerp})
		}, wantErr: true},
		{name: "selector not normalized", mutate: func(m *ExecutionMassStatus, _ *MassStatusQuery) {
			m.OpenOrdersCoverage.Scope.InstrumentIDs[0], m.OpenOrdersCoverage.Scope.InstrumentIDs[1] = m.OpenOrdersCoverage.Scope.InstrumentIDs[1], m.OpenOrdersCoverage.Scope.InstrumentIDs[0]
		}, wantErr: true},
		{name: "fill from mismatch", mutate: func(m *ExecutionMassStatus, _ *MassStatusQuery) { m.FillsCoverage.Scope.From = from.Add(time.Second) }, wantErr: true},
		{name: "fill through mismatch", mutate: func(m *ExecutionMassStatus, _ *MassStatusQuery) {
			m.FillsCoverage.Scope.Through = through.Add(-time.Second)
		}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := query
			q.InstrumentIDs = append([]InstrumentID(nil), query.InstrumentIDs...)
			mass := validCoverageMass(q)
			if tt.mutate != nil {
				tt.mutate(mass, &q)
			}
			err := mass.ValidateFor(q)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateFor err=%v wantErr=%v mass=%+v query=%+v", err, tt.wantErr, mass, q)
			}
		})
	}
}

func TestCoverageValidateForRejectsReportsOutsideFrozenScope(t *testing.T) {
	query := MassStatusQuery{
		Venue: "T", AccountID: "acct", ClientID: "client", InstrumentIDs: coverageTestIDs(),
		Since: time.Unix(100, 0), Until: time.Unix(200, 0), IncludeFills: true, IncludePositions: true,
	}
	tests := []struct {
		name string
		add  func(*testing.T, *ExecutionMassStatus)
	}{
		{name: "order account", add: func(t *testing.T, mass *ExecutionMassStatus) {
			t.Helper()
			err := mass.AddOrderReport(OrderStatusReport{Venue: "T", AccountID: "other", Order: Order{Request: OrderRequest{
				AccountID: "other", ClientID: "client", InstrumentID: coverageTestIDs()[0], Quantity: decimal.NewFromInt(1),
			}}})
			if err != nil {
				t.Fatalf("add order: %v", err)
			}
		}},
		{name: "fill client", add: func(t *testing.T, mass *ExecutionMassStatus) {
			t.Helper()
			err := mass.AddFillReport(FillReport{Venue: "T", AccountID: "acct", Fill: Fill{
				AccountID: "acct", ClientID: "other", InstrumentID: coverageTestIDs()[0],
				TradeID: "trade", Price: decimal.NewFromInt(1), Quantity: decimal.NewFromInt(1),
			}})
			if err != nil {
				t.Fatalf("add fill: %v", err)
			}
		}},
		{name: "position instrument", add: func(t *testing.T, mass *ExecutionMassStatus) {
			t.Helper()
			err := mass.AddPositionReport(PositionReport{Venue: "T", AccountID: "acct", Position: Position{
				AccountID: "acct", InstrumentID: InstrumentID{Venue: "T", Symbol: "SOL-USDT", Kind: enums.KindPerp},
			}})
			if err != nil {
				t.Fatalf("add position: %v", err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mass := validCoverageMass(query)
			tt.add(t, mass)
			if err := mass.ValidateFor(query); err == nil {
				t.Fatal("ValidateFor accepted report outside frozen coverage scope")
			}
		})
	}
}

func TestCoverageValidateForRejectsConflictingReportEnvelopeIdentity(t *testing.T) {
	query := MassStatusQuery{
		Venue: "T", AccountID: "acct", ClientID: "client", InstrumentIDs: coverageTestIDs(),
		Since: time.Unix(100, 0), Until: time.Unix(200, 0), IncludeFills: true, IncludePositions: true,
	}
	tests := []struct {
		name string
		add  func(*testing.T, *ExecutionMassStatus)
	}{
		{name: "order payload account", add: func(t *testing.T, mass *ExecutionMassStatus) {
			t.Helper()
			if err := mass.AddOrderReport(OrderStatusReport{Venue: "T", AccountID: "acct", Order: Order{Request: OrderRequest{
				AccountID: "other", ClientID: "client", InstrumentID: coverageTestIDs()[0], Quantity: decimal.NewFromInt(1),
			}}}); err != nil {
				t.Fatalf("add order: %v", err)
			}
		}},
		{name: "order report venue", add: func(t *testing.T, mass *ExecutionMassStatus) {
			t.Helper()
			if err := mass.AddOrderReport(OrderStatusReport{Venue: "OTHER", AccountID: "acct", Order: Order{Request: OrderRequest{
				AccountID: "acct", ClientID: "client", InstrumentID: coverageTestIDs()[0], Quantity: decimal.NewFromInt(1),
			}}}); err != nil {
				t.Fatalf("add order: %v", err)
			}
		}},
		{name: "fill payload account", add: func(t *testing.T, mass *ExecutionMassStatus) {
			t.Helper()
			if err := mass.AddFillReport(FillReport{Venue: "T", AccountID: "acct", Fill: Fill{
				AccountID: "other", ClientID: "client", InstrumentID: coverageTestIDs()[0],
				Price: decimal.NewFromInt(1), Quantity: decimal.NewFromInt(1), Timestamp: query.Since,
			}}); err != nil {
				t.Fatalf("add fill: %v", err)
			}
		}},
		{name: "fill report venue", add: func(t *testing.T, mass *ExecutionMassStatus) {
			t.Helper()
			if err := mass.AddFillReport(FillReport{Venue: "OTHER", AccountID: "acct", Fill: Fill{
				AccountID: "acct", ClientID: "client", InstrumentID: coverageTestIDs()[0],
				Price: decimal.NewFromInt(1), Quantity: decimal.NewFromInt(1), Timestamp: query.Since,
			}}); err != nil {
				t.Fatalf("add fill: %v", err)
			}
		}},
		{name: "position payload account", add: func(t *testing.T, mass *ExecutionMassStatus) {
			t.Helper()
			if err := mass.AddPositionReport(PositionReport{Venue: "T", AccountID: "acct", Position: Position{
				AccountID: "other", InstrumentID: coverageTestIDs()[0],
			}}); err != nil {
				t.Fatalf("add position: %v", err)
			}
		}},
		{name: "position report venue", add: func(t *testing.T, mass *ExecutionMassStatus) {
			t.Helper()
			if err := mass.AddPositionReport(PositionReport{Venue: "OTHER", AccountID: "acct", Position: Position{
				AccountID: "acct", InstrumentID: coverageTestIDs()[0],
			}}); err != nil {
				t.Fatalf("add position: %v", err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mass := validCoverageMass(query)
			tt.add(t, mass)
			if err := mass.ValidateFor(query); err == nil {
				t.Fatal("ValidateFor accepted conflicting report envelope identity")
			}
		})
	}
}

func TestCoverageValidateForBoundsFillEventTimeToCoverageInterval(t *testing.T) {
	query := MassStatusQuery{
		Venue: "T", AccountID: "acct", ClientID: "client", InstrumentIDs: coverageTestIDs(),
		Since: time.Unix(100, 0), Until: time.Unix(200, 0), IncludeFills: true,
	}
	for _, at := range []time.Time{time.Time{}, query.Since.Add(-time.Nanosecond), query.Until.Add(time.Nanosecond)} {
		mass := validCoverageMass(query)
		if err := mass.AddFillReport(FillReport{Venue: "T", AccountID: "acct", Fill: Fill{
			AccountID: "acct", ClientID: "client", InstrumentID: coverageTestIDs()[0],
			Price: decimal.NewFromInt(1), Quantity: decimal.NewFromInt(1), Timestamp: at,
		}}); err != nil {
			t.Fatalf("add fill at %s: %v", at, err)
		}
		if err := mass.ValidateFor(query); err == nil {
			t.Fatalf("ValidateFor accepted fill event time %s outside [%s,%s]", at, query.Since, query.Until)
		}
	}

	for _, at := range []time.Time{query.Since, query.Until} {
		mass := validCoverageMass(query)
		if err := mass.AddFillReport(FillReport{Venue: "T", AccountID: "acct", Fill: Fill{
			AccountID: "acct", ClientID: "client", InstrumentID: coverageTestIDs()[0],
			Price: decimal.NewFromInt(1), Quantity: decimal.NewFromInt(1), Timestamp: at,
		}}); err != nil {
			t.Fatalf("add fill at %s: %v", at, err)
		}
		if err := mass.ValidateFor(query); err != nil {
			t.Fatalf("ValidateFor rejected inclusive fill boundary %s: %v", at, err)
		}
	}
}

func TestCoverageValidateForUsesLookbackAsEffectiveFillInterval(t *testing.T) {
	through := time.Unix(200, 0)
	query := MassStatusQuery{
		AccountID: "acct", InstrumentIDs: coverageTestIDs(), Until: through,
		Lookback: time.Hour, IncludeFills: true,
	}
	mass := validCoverageMass(query)
	if err := mass.ValidateFor(query); err != nil {
		t.Fatalf("ValidateFor lookback: %v", err)
	}
	mass.FillsCoverage.Scope.From = through.Add(-30 * time.Minute)
	if err := mass.ValidateFor(query); err == nil {
		t.Fatal("ValidateFor accepted fill interval different from effective lookback")
	}
}

func TestCoverageValidateForRequiresZeroFillStartForUnboundedQuery(t *testing.T) {
	query := MassStatusQuery{
		Venue: "T", AccountID: "acct", InstrumentIDs: coverageTestIDs(),
		Until: time.Unix(200, 0), IncludeFills: true,
	}
	mass := validCoverageMass(query)
	mass.FillsCoverage.Scope.From = time.Unix(100, 0)
	if err := mass.ValidateFor(query); err == nil {
		t.Fatal("ValidateFor accepted a narrowed nonzero fill start for an unbounded query")
	}
}
