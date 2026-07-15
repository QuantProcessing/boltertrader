package runtimetest

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/shopspring/decimal"
)

var (
	fakeBTC = model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	fakeETH = model.InstrumentID{Venue: "TEST", Symbol: "ETH-USDT", Kind: enums.KindPerp}
)

func fakeOpenOrder(accountID, clientID string, id model.InstrumentID) model.Order {
	return model.Order{
		Request: model.OrderRequest{
			AccountID:    accountID,
			InstrumentID: id,
			ClientID:     clientID,
			Side:         enums.SideBuy,
			Type:         enums.TypeLimit,
			TIF:          enums.TifGTC,
			Quantity:     decimal.NewFromInt(1),
			Price:        decimal.NewFromInt(100),
		},
		VenueOrderID: "venue-" + clientID,
		Status:       enums.StatusNew,
	}
}

func fakeMassQuery() model.MassStatusQuery {
	return model.MassStatusQuery{
		Venue:     "TEST",
		AccountID: "acct-a",
		Until:     time.Unix(2_000, 0),
	}
}

func TestFakeExecMassStatusHonorsExplicitInstrumentSelector(t *testing.T) {
	exec := NewFakeExec()
	exec.SetAccountID("acct-a")
	exec.SetInstruments(fakeBTC, fakeETH)
	exec.SetOrderStatusReports(
		fakeOpenOrder("acct-a", "btc", fakeBTC),
		fakeOpenOrder("acct-a", "eth", fakeETH),
	)
	query := fakeMassQuery()
	query.InstrumentIDs = []model.InstrumentID{fakeETH, fakeETH}

	mass, err := exec.GenerateExecutionMassStatus(context.Background(), query)
	if err != nil {
		t.Fatalf("mass status: %v", err)
	}
	if got, want := mass.OpenOrdersCoverage.Scope.InstrumentIDs, []model.InstrumentID{fakeETH}; !slices.Equal(got, want) {
		t.Fatalf("coverage selector=%+v, want exact normalized intersection %+v", got, want)
	}
	if len(mass.OrderReports) != 1 {
		t.Fatalf("order reports=%d, want only the explicitly selected instrument", len(mass.OrderReports))
	}
	if report, ok := mass.OrderReports["venue-eth"]; !ok || report.Order.Request.InstrumentID != fakeETH {
		t.Fatalf("selected order report missing or wrong: %+v", mass.OrderReports)
	}
}

func TestFakeExecMassStatusDerivesVenueFromConfiguredInstruments(t *testing.T) {
	exec := NewFakeExec()
	exec.SetAccountID("acct-a")
	exec.SetInstruments(fakeBTC)

	if got := exec.Capabilities().Venue; got != fakeBTC.Venue {
		t.Fatalf("capability venue=%q, want configured instrument venue %q", got, fakeBTC.Venue)
	}
	mass, err := exec.GenerateExecutionMassStatus(context.Background(), fakeMassQuery())
	if err != nil {
		t.Fatalf("complete-empty mass status: %v", err)
	}
	if mass.Venue != fakeBTC.Venue {
		t.Fatalf("mass venue=%q, want %q", mass.Venue, fakeBTC.Venue)
	}
	if got, want := mass.OpenOrdersCoverage.Scope.InstrumentIDs, []model.InstrumentID{fakeBTC}; !slices.Equal(got, want) {
		t.Fatalf("coverage selector=%+v, want %+v", got, want)
	}
}

func TestFakeExecMassStatusPreservesCompleteEmptySelectorOwnership(t *testing.T) {
	exec := NewFakeExec()
	exec.SetAccountID("acct-a")
	configured := []model.InstrumentID{fakeETH, fakeBTC, fakeETH}
	exec.SetInstruments(configured...)
	configured[0] = model.InstrumentID{Venue: "MUTATED", Symbol: "MUTATED", Kind: enums.KindSpot}

	query := fakeMassQuery()
	query.InstrumentIDs = []model.InstrumentID{{Venue: "TEST", Symbol: "SOL-USDT", Kind: enums.KindPerp}}
	mass, err := exec.GenerateExecutionMassStatus(context.Background(), query)
	if err != nil {
		t.Fatalf("complete-empty mass status: %v", err)
	}
	if mass.OpenOrdersCoverage.State != model.CoverageComplete {
		t.Fatalf("open-order coverage=%s, want COMPLETE", mass.OpenOrdersCoverage.State)
	}
	ids := mass.OpenOrdersCoverage.Scope.InstrumentIDs
	if ids == nil || len(ids) != 0 {
		t.Fatalf("response must own a non-nil proven-empty selector, got %#v", ids)
	}
	query.InstrumentIDs[0] = fakeBTC
	if len(mass.OpenOrdersCoverage.Scope.InstrumentIDs) != 0 {
		t.Fatalf("response selector changed after query mutation: %+v", mass.OpenOrdersCoverage.Scope.InstrumentIDs)
	}
}

func TestFakeExecMassStatusRejectsForeignVenueAndAccount(t *testing.T) {
	exec := NewFakeExec()
	exec.SetAccountID("acct-a")
	exec.SetInstruments(fakeBTC)
	exec.SetOrderStatusReports(fakeOpenOrder("acct-a", "btc", fakeBTC))

	tests := []struct {
		name  string
		query model.MassStatusQuery
		want  string
	}{
		{
			name: "venue",
			query: model.MassStatusQuery{
				Venue:     "OTHER",
				AccountID: "acct-a",
				Until:     time.Unix(2_000, 0),
			},
			want: "venue",
		},
		{
			name: "account",
			query: model.MassStatusQuery{
				Venue:     "TEST",
				AccountID: "acct-b",
				Until:     time.Unix(2_000, 0),
			},
			want: "account",
		},
		{
			name: "instrument venue",
			query: model.MassStatusQuery{
				Venue:         "TEST",
				AccountID:     "acct-a",
				InstrumentIDs: []model.InstrumentID{{Venue: "OTHER", Symbol: "BTC-USDT", Kind: enums.KindPerp}},
				Until:         time.Unix(2_000, 0),
			},
			want: "instrument",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := exec.GenerateExecutionMassStatus(context.Background(), tt.query); err == nil || !strings.Contains(strings.ToLower(err.Error()), tt.want) {
				t.Fatalf("error=%v, want an explicit %s mismatch", err, tt.want)
			}
		})
	}
}

func TestFakeExecMassStatusAppliesClientFilterWithoutChangingAuthority(t *testing.T) {
	exec := NewFakeExec()
	exec.SetAccountID("acct-a")
	exec.SetInstruments(fakeBTC)
	exec.SetOrderStatusReports(
		fakeOpenOrder("acct-a", "wanted", fakeBTC),
		fakeOpenOrder("acct-a", "foreign", fakeBTC),
	)
	query := fakeMassQuery()
	query.ClientID = "wanted"

	mass, err := exec.GenerateExecutionMassStatus(context.Background(), query)
	if err != nil {
		t.Fatalf("mass status: %v", err)
	}
	if mass.ClientID != query.ClientID || mass.OpenOrdersCoverage.Scope.ClientID != query.ClientID {
		t.Fatalf("client filter not frozen into response: mass=%q coverage=%q", mass.ClientID, mass.OpenOrdersCoverage.Scope.ClientID)
	}
	if len(mass.OrderReports) != 1 {
		t.Fatalf("order reports=%d, want exact client filtering", len(mass.OrderReports))
	}
	if _, ok := mass.OrderReports["venue-wanted"]; !ok {
		t.Fatalf("wanted client report missing: %+v", mass.OrderReports)
	}
}

func TestFakeExecMassStatusUnsupportedRequestedDomainsAreUnavailable(t *testing.T) {
	exec := NewFakeExec()
	exec.SetAccountID("acct-a")
	exec.SetInstruments(fakeBTC)
	query := fakeMassQuery()
	query.IncludeFills = true
	query.IncludePositions = true
	query.Since = time.Unix(1_000, 0)

	mass, err := exec.GenerateExecutionMassStatus(context.Background(), query)
	if err != nil {
		t.Fatalf("mass status: %v", err)
	}
	if coverage := mass.FillsCoverage; coverage.State != model.CoverageUnavailable || !coverage.Scope.IsZero() {
		t.Fatalf("fills coverage=%+v, want zero-scope UNAVAILABLE for unsupported fill history", coverage)
	}
	if coverage := mass.PositionsCoverage; coverage.State != model.CoverageUnavailable || coverage.Scope.IsZero() {
		t.Fatalf("positions coverage=%+v, want scoped UNAVAILABLE for the modeled account fallback", coverage)
	}
	if got, want := mass.PositionsCoverage.Scope.InstrumentIDs, []model.InstrumentID{fakeBTC}; !slices.Equal(got, want) {
		t.Fatalf("position fallback selector=%+v, want %+v", got, want)
	}
}

func TestFakeExecMassStatusUsesLocalRequestStartWatermark(t *testing.T) {
	exec := NewFakeExec()
	exec.SetAccountID("acct-a")
	exec.SetInstruments(fakeBTC)
	requestStartedAt := time.Unix(5_000, 0)
	exec.WithClock(clock.NewSimulatedClock(requestStartedAt))
	query := fakeMassQuery()
	query.Until = time.Unix(10, 0)

	mass, err := exec.GenerateExecutionMassStatus(context.Background(), query)
	if err != nil {
		t.Fatalf("mass status: %v", err)
	}
	if !mass.GeneratedAt.Equal(requestStartedAt) {
		t.Fatalf("generated at=%s, want injected request-start %s", mass.GeneratedAt, requestStartedAt)
	}
	if mass.GeneratedAt.Equal(query.Until) {
		t.Fatalf("old fill interval boundary %s was reused as the snapshot observation watermark", query.Until)
	}
	if got := mass.OpenOrdersCoverage.Scope.Through; !got.Equal(mass.GeneratedAt) {
		t.Fatalf("open-order through=%s, want captured request-start %s", got, mass.GeneratedAt)
	}
	if got := mass.PositionsCoverage; got.State != model.CoverageNotRequested || !got.Scope.IsZero() {
		t.Fatalf("omitted position coverage=%+v, want zero-scope NOT_REQUESTED", got)
	}
}

func TestFakeExecOrderStatusReportsUseInjectedClock(t *testing.T) {
	reportedAt := time.Unix(6_000, 0)
	exec := NewFakeExec().WithClock(clock.NewSimulatedClock(reportedAt))
	exec.SetOrderStatusReports(fakeOpenOrder("acct-a", "btc", fakeBTC))

	reports, err := exec.GenerateOrderStatusReports(context.Background(), model.OrderStatusReportQuery{AccountID: "acct-a"})
	if err != nil {
		t.Fatalf("order status reports: %v", err)
	}
	if len(reports) != 1 || !reports[0].ReportedAt.Equal(reportedAt) {
		t.Fatalf("reports=%+v, want injected reported-at %s", reports, reportedAt)
	}
}
