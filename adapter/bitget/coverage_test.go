package bitget

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/model"
	bitgetsdk "github.com/QuantProcessing/boltertrader/sdk/bitget"
)

func TestBitgetExecutionMassStatusOwnsCompleteEmptyAllDomainCoverage(t *testing.T) {
	now := time.Date(2026, 7, 15, 14, 0, 0, 0, time.UTC)
	since := now.Add(-time.Hour)
	provider := bitgetTestProvider()
	clk := clock.NewSimulatedClock(now)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clk.Advance(time.Second)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v3/trade/unfilled-orders", "/api/v3/trade/fills", "/api/v3/position/current-position":
			writeJSON(t, w, map[string]any{"code": "00000", "msg": "success", "data": map[string]any{"list": []any{}, "cursor": ""}})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	exec := newExecutionClient(bitgetsdk.NewClient().WithCredentials("key", "secret", "pass").WithBaseURL(server.URL).WithHTTPClient(server.Client()), provider, clk)
	configured := provider.All()
	query := model.MassStatusQuery{
		InstrumentIDs:    []model.InstrumentID{configured[2].ID, configured[0].ID, configured[1].ID, configured[0].ID},
		Until:            now,
		Lookback:         time.Hour,
		IncludeFills:     true,
		IncludePositions: true,
	}
	mass, err := exec.GenerateExecutionMassStatus(context.Background(), query)
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	if err := mass.ValidateFor(query); err != nil {
		t.Fatalf("typed coverage validation: %v; mass=%+v", err, mass)
	}
	assertBitgetCompleteCoverage(t, "open", mass.OpenOrdersCoverage, 3)
	assertBitgetCompleteCoverage(t, "fills", mass.FillsCoverage, 3)
	assertBitgetCompleteCoverage(t, "positions", mass.PositionsCoverage, 2)
	if !mass.OpenOrdersCoverage.Scope.Through.Equal(now) || !mass.PositionsCoverage.Scope.Through.After(now) {
		t.Fatalf("snapshot watermarks open=%s positions=%s", mass.OpenOrdersCoverage.Scope.Through, mass.PositionsCoverage.Scope.Through)
	}
	if !mass.FillsCoverage.Scope.From.Equal(since) || !mass.FillsCoverage.Scope.Through.Equal(now) {
		t.Fatalf("fill interval=%s..%s, want %s..%s", mass.FillsCoverage.Scope.From, mass.FillsCoverage.Scope.Through, since, now)
	}
	provider.LoadSnapshot(nil)
	if len(mass.OpenOrdersCoverage.Scope.InstrumentIDs) != 3 {
		t.Fatalf("provider mutation altered frozen selector: %+v", mass.OpenOrdersCoverage.Scope.InstrumentIDs)
	}
	if err := mass.ValidateFor(query); err != nil {
		t.Fatalf("provider mutation altered coverage validity: %v", err)
	}
}

func TestBitgetExecutionMassStatusMarksRequestedDomainsUnavailableBeforeIO(t *testing.T) {
	query := model.MassStatusQuery{IncludeFills: true, IncludePositions: true}
	mass, err := newExecutionClient(nil, bitgetTestProvider(), clock.NewSimulatedClock(time.Date(2026, 7, 15, 15, 0, 0, 0, time.UTC))).GenerateExecutionMassStatus(context.Background(), query)
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	if err := mass.ValidateFor(query); err != nil {
		t.Fatalf("typed unavailable coverage validation: %v; mass=%+v", err, mass)
	}
	for name, coverage := range map[string]model.ReportCoverage{"open": mass.OpenOrdersCoverage, "fills": mass.FillsCoverage, "positions": mass.PositionsCoverage} {
		if coverage.State != model.CoverageUnavailable || !coverage.Scope.IsZero() {
			t.Fatalf("%s coverage=%+v, want pre-I/O unavailable with zero scope", name, coverage)
		}
	}
}

func assertBitgetCompleteCoverage(t *testing.T, name string, coverage model.ReportCoverage, count int) {
	t.Helper()
	if coverage.State != model.CoverageComplete || coverage.Scope.InstrumentIDs == nil || len(coverage.Scope.InstrumentIDs) != count {
		t.Fatalf("%s coverage=%+v, want complete owned %d-instrument selector", name, coverage, count)
	}
}
