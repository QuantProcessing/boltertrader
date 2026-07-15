package perp

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
)

func TestPerpMassStatusRetainsSuccessfulRowsWhenAnotherInstrumentFails(t *testing.T) {
	first := mustPerpInstrument(t)
	second := *first
	second.ID.Symbol = "OTHER-USDT"
	second.VenueSymbol = "OTHERUSDT"
	provider := testProvider(first, &second)
	client := perpClientNoNetwork(t)
	client.WithHTTPClient(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Query().Get("symbol") == second.VenueSymbol {
			return &http.Response{StatusCode: http.StatusInternalServerError, Body: io.NopCloser(strings.NewReader(`{"code":-1000,"msg":"forced failure"}`)), Header: make(http.Header), Request: request}, nil
		}
		body := `[{"symbol":"ASTERUSDT","orderId":42,"clientOrderId":"retained-perp","status":"NEW","type":"LIMIT","side":"SELL","positionSide":"BOTH","timeInForce":"GTC","origQty":"1","price":"10","executedQty":"0","cumQty":"0","cumQuote":"0","avgPrice":"0","updateTime":1700000000000}]`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: request}, nil
	})})
	query := model.MassStatusQuery{AccountID: AccountIDDefault}
	mass, err := newExecutionClient(client, provider, clock.NewRealClock(), AccountIDDefault).GenerateExecutionMassStatus(context.Background(), query)
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	if len(mass.OrderReports) != 1 {
		t.Fatalf("retained reports=%+v, want one successful row", mass.OrderReports)
	}
	for _, report := range mass.OrderReports {
		if report.Order.VenueOrderID != "42" || report.Order.Request.InstrumentID != first.ID {
			t.Fatalf("retained report=%+v", report)
		}
	}
	coverage := mass.OpenOrdersCoverage
	if coverage.State != model.CoveragePartial || coverage.Scope.AccountID != AccountIDDefault || coverage.Scope.Through.IsZero() || len(coverage.Scope.InstrumentIDs) != 2 || !coverage.Scope.ContainsInstrument(first.ID) || !coverage.Scope.ContainsInstrument(second.ID) {
		t.Fatalf("coverage=%+v, want fully scoped Partial", coverage)
	}
	if err := mass.ValidateFor(query); err != nil {
		t.Fatalf("ValidateFor: %v", err)
	}
}

func TestPerpMassStatusFreezesSelectorAndEarliestRequestStart(t *testing.T) {
	start := time.Date(2026, 7, 15, 2, 0, 0, 0, time.UTC)
	clk := clock.NewSimulatedClock(start)
	first := mustPerpInstrument(t)
	second := *first
	second.ID.Symbol = "OTHER-USDT"
	second.VenueSymbol = "OTHERUSDT"
	provider := testProvider(first, &second)
	calls := 0
	client := perpClientNoNetwork(t)
	client.WithHTTPClient(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			provider.LoadSnapshot(nil)
			clk.Advance(time.Minute)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`[]`)), Header: make(http.Header), Request: request}, nil
	})})
	exec := newExecutionClient(client, provider, clk, AccountIDDefault)
	mass, err := exec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || mass.OpenOrdersCoverage.State != model.CoverageComplete {
		t.Fatalf("calls=%d coverage=%+v", calls, mass.OpenOrdersCoverage)
	}
	if !mass.OpenOrdersCoverage.Scope.Through.Equal(start) {
		t.Fatalf("through=%s, want earliest request start %s", mass.OpenOrdersCoverage.Scope.Through, start)
	}
	want := model.NormalizeInstrumentIDs([]model.InstrumentID{first.ID, second.ID})
	if got := mass.OpenOrdersCoverage.Scope.InstrumentIDs; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("frozen selector=%v, want %v", got, want)
	}
}

func TestPerpMassStatusDistinguishesEmptyFromPreIOUnavailable(t *testing.T) {
	clk := clock.NewSimulatedClock(time.Date(2026, 7, 15, 3, 0, 0, 0, time.UTC))
	inst := mustPerpInstrument(t)
	exec := newExecutionClient(nil, testProvider(inst), clk, AccountIDDefault)
	requested := model.MassStatusQuery{AccountID: AccountIDDefault, IncludeFills: true, IncludePositions: true}
	unavailable, err := exec.GenerateExecutionMassStatus(context.Background(), requested)
	if err != nil {
		t.Fatal(err)
	}
	if unavailable.OpenOrdersCoverage.State != model.CoverageUnavailable || unavailable.FillsCoverage.State != model.CoverageUnavailable || unavailable.PositionsCoverage.State != model.CoverageUnavailable ||
		!unavailable.OpenOrdersCoverage.Scope.IsZero() || !unavailable.FillsCoverage.Scope.IsZero() || !unavailable.PositionsCoverage.Scope.IsZero() {
		t.Fatalf("pre-I/O coverage=%+v/%+v/%+v", unavailable.OpenOrdersCoverage, unavailable.FillsCoverage, unavailable.PositionsCoverage)
	}
	if err := unavailable.ValidateFor(requested); err != nil {
		t.Fatalf("pre-I/O coverage validation: %v", err)
	}
	emptyQuery := requested
	emptyQuery.InstrumentIDs = []model.InstrumentID{}
	empty, err := exec.GenerateExecutionMassStatus(context.Background(), emptyQuery)
	if err != nil {
		t.Fatal(err)
	}
	if empty.OpenOrdersCoverage.State != model.CoverageComplete || empty.FillsCoverage.State != model.CoverageComplete || empty.PositionsCoverage.State != model.CoverageComplete || empty.OpenOrdersCoverage.Scope.InstrumentIDs == nil {
		t.Fatalf("empty coverage=%+v/%+v/%+v", empty.OpenOrdersCoverage, empty.FillsCoverage, empty.PositionsCoverage)
	}
	if err := empty.ValidateFor(emptyQuery); err != nil {
		t.Fatalf("empty coverage validation: %v", err)
	}
	failedClient := perpClientNoNetwork(t)
	failedClient.WithHTTPClient(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusInternalServerError, Body: io.NopCloser(strings.NewReader(`{"code":-1000,"msg":"internal"}`)), Header: make(http.Header), Request: request}, nil
	})})
	attempted, err := newExecutionClient(failedClient, testProvider(inst), clk, AccountIDDefault).GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{AccountID: AccountIDDefault})
	if err != nil {
		t.Fatal(err)
	}
	if attempted.OpenOrdersCoverage.State != model.CoverageUnavailable || attempted.OpenOrdersCoverage.Scope.IsZero() {
		t.Fatalf("attempted coverage=%+v, want scoped Unavailable", attempted.OpenOrdersCoverage)
	}
}

func TestPerpMassStatusMarksOptionalDomainsNotRequested(t *testing.T) {
	client := perpClientNoNetwork(t)
	client.WithHTTPClient(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`[]`)), Header: make(http.Header), Request: request}, nil
	})})
	inst := mustPerpInstrument(t)
	query := model.MassStatusQuery{AccountID: AccountIDDefault}
	mass, err := newExecutionClient(client, testProvider(inst), clock.NewRealClock(), AccountIDDefault).GenerateExecutionMassStatus(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	if mass.OpenOrdersCoverage.State != model.CoverageComplete || mass.FillsCoverage.State != model.CoverageNotRequested || mass.PositionsCoverage.State != model.CoverageNotRequested {
		t.Fatalf("coverage=%+v/%+v/%+v", mass.OpenOrdersCoverage, mass.FillsCoverage, mass.PositionsCoverage)
	}
	if err := mass.ValidateFor(query); err != nil {
		t.Fatalf("ValidateFor: %v", err)
	}
}

func TestPerpMassStatusRejectsMismatchedScopeBeforeIO(t *testing.T) {
	called := false
	client := perpClientNoNetwork(t)
	client.WithHTTPClient(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		called = true
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`[]`)), Header: make(http.Header), Request: request}, nil
	})})
	inst := mustPerpInstrument(t)
	exec := newExecutionClient(client, testProvider(inst), clock.NewRealClock(), AccountIDDefault)
	id := inst.ID
	wrongKind := id
	wrongKind.Kind = enums.KindSpot
	unknown := id
	unknown.Symbol = "UNKNOWN-USDT"

	for name, query := range map[string]model.MassStatusQuery{
		"account":            {AccountID: "ASTER-OTHER"},
		"venue":              {Venue: "OTHER"},
		"instrument venue":   {InstrumentIDs: []model.InstrumentID{{Venue: "OTHER", Symbol: id.Symbol, Kind: id.Kind}}},
		"instrument kind":    {InstrumentIDs: []model.InstrumentID{wrongKind}},
		"unknown instrument": {InstrumentIDs: []model.InstrumentID{unknown}},
	} {
		t.Run(name, func(t *testing.T) {
			mass, err := exec.GenerateExecutionMassStatus(context.Background(), query)
			if err == nil || mass != nil {
				t.Fatalf("mass=%+v err=%v, want nil fail-closed error", mass, err)
			}
		})
	}
	if called {
		t.Fatal("invalid mass-status scope crossed the venue I/O boundary")
	}
}
