package bybit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/model"
	bybitsdk "github.com/QuantProcessing/boltertrader/sdk/bybit"
)

func TestBybitExecutionMassStatusOwnsCompleteEmptyAllDomainCoverage(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	since := now.Add(-time.Hour)
	provider := bybitTestProvider()
	clk := clock.NewSimulatedClock(now)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clk.Advance(time.Second)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v5/order/realtime", "/v5/execution/list":
			writeJSON(t, w, map[string]any{"retCode": 0, "retMsg": "OK", "result": map[string]any{"list": []any{}, "nextPageCursor": ""}})
		case "/v5/position/list":
			writeJSON(t, w, map[string]any{"retCode": 0, "retMsg": "OK", "result": map[string]any{"list": []any{}}})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	exec := newExecutionClient(bybitsdk.NewClient().WithCredentials("key", "secret").WithBaseURL(server.URL).WithHTTPClient(server.Client()), provider, clk)
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
	assertBybitCompleteCoverage(t, "open", mass.OpenOrdersCoverage, 3)
	assertBybitCompleteCoverage(t, "fills", mass.FillsCoverage, 3)
	assertBybitCompleteCoverage(t, "positions", mass.PositionsCoverage, 2)
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

func TestBybitExecutionMassStatusMarksRequestedDomainsUnavailableBeforeIO(t *testing.T) {
	query := model.MassStatusQuery{IncludeFills: true, IncludePositions: true}
	mass, err := newExecutionClient(nil, bybitTestProvider(), clock.NewSimulatedClock(time.Date(2026, 7, 15, 13, 0, 0, 0, time.UTC))).GenerateExecutionMassStatus(context.Background(), query)
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

func assertBybitCompleteCoverage(t *testing.T, name string, coverage model.ReportCoverage, count int) {
	t.Helper()
	if coverage.State != model.CoverageComplete || coverage.Scope.InstrumentIDs == nil || len(coverage.Scope.InstrumentIDs) != count {
		t.Fatalf("%s coverage=%+v, want complete owned %d-instrument selector", name, coverage, count)
	}
}
