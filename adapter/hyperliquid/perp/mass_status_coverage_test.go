package perp

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/adapter/hyperliquid/internal/instruments"
	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
)

func TestHyperliquidPerpMassStatusRetainsSuccessfulDexRowsWhenAnotherDexFails(t *testing.T) {
	provider := testProvider(t)
	rest := testREST(func(_ *http.Request, body []byte) (string, int) {
		var request map[string]any
		if err := json.Unmarshal(body, &request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if request["dex"] == "testdex" {
			return `{"error":"forced HIP-3 failure"}`, http.StatusInternalServerError
		}
		return `[{"coin":"BTC","side":"B","limitPx":"65000","sz":"0.01","oid":555,"timestamp":1700000000000,"origSz":"0.01","reduceOnly":false,"orderType":"Limit","tif":"Gtc"}]`, http.StatusOK
	})
	exec := newExecutionClient(rest, provider, clock.NewRealClock(), AccountIDDefault)
	query := model.MassStatusQuery{AccountID: AccountIDDefault}
	mass, err := exec.GenerateExecutionMassStatus(context.Background(), query)
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	if len(mass.OrderReports) != 1 {
		t.Fatalf("retained order reports=%+v, want one successful standard-dex row", mass.OrderReports)
	}
	for _, report := range mass.OrderReports {
		if report.Order.VenueOrderID != "555" || report.Order.Request.InstrumentID != testPerpID() {
			t.Fatalf("retained report=%+v", report)
		}
	}
	expectedIDs := make([]model.InstrumentID, 0)
	for _, inst := range provider.All() {
		if inst != nil && inst.ID.Kind == enums.KindPerp {
			expectedIDs = append(expectedIDs, inst.ID)
		}
	}
	coverage := mass.OpenOrdersCoverage
	if coverage.State != model.CoveragePartial || coverage.Scope.AccountID != AccountIDDefault || coverage.Scope.Through.IsZero() || len(coverage.Scope.InstrumentIDs) != len(expectedIDs) {
		t.Fatalf("coverage=%+v, want fully scoped Partial", coverage)
	}
	for _, id := range expectedIDs {
		if !coverage.Scope.ContainsInstrument(id) {
			t.Fatalf("coverage selector %v omitted %s", coverage.Scope.InstrumentIDs, id)
		}
	}
	if err := mass.ValidateFor(query); err != nil {
		t.Fatalf("ValidateFor: %v", err)
	}
}

func TestHyperliquidPerpMassStatusClientIDFilterIsTypedPartial(t *testing.T) {
	rest := testREST(func(*http.Request, []byte) (string, int) { return `[]`, http.StatusOK })
	query := model.MassStatusQuery{AccountID: AccountIDDefault, ClientID: "client-filter"}
	mass, err := newExecutionClient(rest, testProvider(t), clock.NewRealClock(), AccountIDDefault).GenerateExecutionMassStatus(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	if mass.OpenOrdersCoverage.State != model.CoveragePartial || mass.OpenOrdersCoverage.Scope.ClientID != query.ClientID || mass.OpenOrdersCoverage.Scope.IsZero() {
		t.Fatalf("coverage=%+v, want scoped Partial for nonblank client filter", mass.OpenOrdersCoverage)
	}
	if err := mass.ValidateFor(query); err != nil {
		t.Fatalf("ValidateFor: %v", err)
	}
}

func TestHyperliquidPerpMassStatusUsesEarliestDexRequestAndFrozenRegistry(t *testing.T) {
	start := time.Date(2026, 7, 15, 2, 0, 0, 0, time.UTC)
	clk := clock.NewSimulatedClock(start)
	provider := testProvider(t)
	calls := 0
	rest := testREST(func(*http.Request, []byte) (string, int) {
		calls++
		clk.Advance(time.Minute)
		return `[]`, http.StatusOK
	})
	exec := newExecutionClient(rest, provider, clk, AccountIDDefault)
	mass, err := exec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{})
	if err != nil {
		t.Fatal(err)
	}
	exec.provider = instruments.NewRegistry()
	if calls != 2 || mass.OpenOrdersCoverage.State != model.CoverageComplete || len(mass.OpenOrdersCoverage.Scope.InstrumentIDs) != 3 {
		t.Fatalf("calls=%d coverage=%+v", calls, mass.OpenOrdersCoverage)
	}
	if !mass.OpenOrdersCoverage.Scope.Through.Equal(start) {
		t.Fatalf("through=%s, want earliest request start %s", mass.OpenOrdersCoverage.Scope.Through, start)
	}
}

func TestHyperliquidPerpMassStatusDistinguishesEmptyFromPreIOUnavailable(t *testing.T) {
	start := time.Date(2026, 7, 15, 3, 0, 0, 0, time.UTC)
	clk := clock.NewSimulatedClock(start)
	provider := testProvider(t)
	exec := newExecutionClient(nil, provider, clk, AccountIDDefault)
	requested := model.MassStatusQuery{AccountID: AccountIDDefault, ClientID: "client-scope", IncludeFills: true, IncludePositions: true}
	unavailable, err := exec.GenerateExecutionMassStatus(context.Background(), requested)
	if err != nil {
		t.Fatal(err)
	}
	if unavailable.OpenOrdersCoverage.State != model.CoverageUnavailable || unavailable.FillsCoverage.State != model.CoverageUnavailable || unavailable.PositionsCoverage.State != model.CoverageUnavailable || !unavailable.OpenOrdersCoverage.Scope.IsZero() {
		t.Fatalf("pre-I/O coverage=%+v/%+v/%+v", unavailable.OpenOrdersCoverage, unavailable.FillsCoverage, unavailable.PositionsCoverage)
	}
	wantPositionIDs := make([]model.InstrumentID, 0)
	for _, inst := range provider.All() {
		if inst != nil && inst.ID.Kind == enums.KindPerp {
			wantPositionIDs = append(wantPositionIDs, inst.ID)
		}
	}
	wantPositionIDs = model.NormalizeInstrumentIDs(wantPositionIDs)
	positionScope := unavailable.PositionsCoverage.Scope
	if positionScope.AccountID != AccountIDDefault || positionScope.ClientID != requested.ClientID ||
		positionScope.InstrumentIDs == nil || !slices.Equal(positionScope.InstrumentIDs, wantPositionIDs) ||
		!positionScope.Through.Equal(start) {
		t.Fatalf("positions scope=%+v, want frozen account/client/standard+HIP-3 selector at %s", positionScope, start)
	}
	if err := unavailable.ValidateFor(requested); err != nil {
		t.Fatalf("pre-I/O validation: %v", err)
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
		t.Fatalf("empty validation: %v", err)
	}
	failed := testREST(func(*http.Request, []byte) (string, int) { return `{}`, http.StatusInternalServerError })
	attempted, err := newExecutionClient(failed, testProvider(t), clk, AccountIDDefault).GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{AccountID: AccountIDDefault})
	if err != nil {
		t.Fatal(err)
	}
	if attempted.OpenOrdersCoverage.State != model.CoverageUnavailable || attempted.OpenOrdersCoverage.Scope.IsZero() {
		t.Fatalf("attempted coverage=%+v, want scoped Unavailable", attempted.OpenOrdersCoverage)
	}
}

func TestHyperliquidPerpMassStatusRejectsMismatchedScopeBeforeIO(t *testing.T) {
	called := false
	rest := testREST(func(*http.Request, []byte) (string, int) {
		called = true
		return `[]`, http.StatusOK
	})
	provider := testProvider(t)
	exec := newExecutionClient(rest, provider, clock.NewRealClock(), AccountIDDefault)
	id := testPerpID()
	wrongKind := id
	wrongKind.Kind = enums.KindSpot
	unknown := id
	unknown.Symbol = "UNKNOWN-USDC"

	for name, query := range map[string]model.MassStatusQuery{
		"account":            {AccountID: "HYPERLIQUID-OTHER"},
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
