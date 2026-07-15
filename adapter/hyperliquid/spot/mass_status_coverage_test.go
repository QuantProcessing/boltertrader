package spot

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/adapter/hyperliquid/internal/instruments"
	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
)

func TestHyperliquidSpotMassStatusClientIDFilterIsTypedPartial(t *testing.T) {
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

func TestHyperliquidSpotMassStatusOwnsSelectorAndRequestStart(t *testing.T) {
	start := time.Date(2026, 7, 15, 2, 0, 0, 0, time.UTC)
	clk := clock.NewSimulatedClock(start)
	provider := testProvider(t)
	rest := testREST(func(*http.Request, []byte) (string, int) {
		clk.Advance(time.Minute)
		return `[]`, http.StatusOK
	})
	exec := newExecutionClient(rest, provider, clk, AccountIDDefault)
	id := testSpotID()
	query := model.MassStatusQuery{InstrumentIDs: []model.InstrumentID{id, id}}
	mass, err := exec.GenerateExecutionMassStatus(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	exec.provider = instruments.NewRegistry()
	query.InstrumentIDs[0].Symbol = "MUTATED"
	if mass.OpenOrdersCoverage.State != model.CoverageComplete || len(mass.OpenOrdersCoverage.Scope.InstrumentIDs) != 1 || mass.OpenOrdersCoverage.Scope.InstrumentIDs[0] != id {
		t.Fatalf("coverage=%+v", mass.OpenOrdersCoverage)
	}
	if !mass.OpenOrdersCoverage.Scope.Through.Equal(start) {
		t.Fatalf("through=%s, want %s", mass.OpenOrdersCoverage.Scope.Through, start)
	}
}

func TestHyperliquidSpotMassStatusDistinguishesEmptyFromPreIOUnavailable(t *testing.T) {
	clk := clock.NewSimulatedClock(time.Date(2026, 7, 15, 3, 0, 0, 0, time.UTC))
	exec := newExecutionClient(nil, testProvider(t), clk, AccountIDDefault)
	requested := model.MassStatusQuery{AccountID: AccountIDDefault, IncludeFills: true, IncludePositions: true}
	unavailable, err := exec.GenerateExecutionMassStatus(context.Background(), requested)
	if err != nil {
		t.Fatal(err)
	}
	if unavailable.OpenOrdersCoverage.State != model.CoverageUnavailable || unavailable.FillsCoverage.State != model.CoverageUnavailable || unavailable.PositionsCoverage.State != model.CoverageUnavailable || !unavailable.OpenOrdersCoverage.Scope.IsZero() {
		t.Fatalf("pre-I/O coverage=%+v/%+v/%+v", unavailable.OpenOrdersCoverage, unavailable.FillsCoverage, unavailable.PositionsCoverage)
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

func TestHyperliquidSpotMassStatusRejectsMismatchedScopeBeforeIO(t *testing.T) {
	called := false
	rest := testREST(func(*http.Request, []byte) (string, int) {
		called = true
		return `[]`, http.StatusOK
	})
	provider := testProvider(t)
	exec := newExecutionClient(rest, provider, clock.NewRealClock(), AccountIDDefault)
	id := testSpotID()
	wrongKind := id
	wrongKind.Kind = enums.KindPerp
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
