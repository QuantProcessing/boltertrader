package spot

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/model"
	sdkspot "github.com/QuantProcessing/boltertrader/sdk/binance/spot"
)

func TestBinanceSpotMassStatusOwnsFrozenTypedCoverage(t *testing.T) {
	eth := testSpotInstrument()
	btc := instrumentFromSymbolInfo(&sdkspot.SymbolInfo{
		Symbol: "BTCUSDT", Status: "TRADING", BaseAsset: "BTC", QuoteAsset: "USDT",
		Filters: []map[string]any{{"filterType": "PRICE_FILTER", "tickSize": "0.1"}},
	})
	provider := testProvider(eth)
	provider.byID[btc.ID.String()] = btc
	provider.bySymbol[btc.VenueSymbol] = btc.ID
	provider.all = []*model.Instrument{eth, btc, eth}
	start := time.Date(2026, 7, 15, 1, 0, 0, 0, time.UTC)
	clk := clock.NewSimulatedClock(start)
	rest := testREST(func(r *http.Request) (string, int) {
		provider.mu.Lock()
		provider.byID = map[string]*model.Instrument{eth.ID.String(): eth}
		provider.bySymbol = map[string]model.InstrumentID{eth.VenueSymbol: eth.ID}
		provider.all = []*model.Instrument{eth}
		provider.mu.Unlock()
		clk.Advance(time.Minute)
		return `[{"symbol":"BTCUSDT","orderId":42,"clientOrderId":"c-btc","price":"100","origQty":"1","executedQty":"0","status":"NEW","timeInForce":"GTC","type":"LIMIT","side":"BUY"}]`, http.StatusOK
	})
	exec := newExecutionClient(rest, provider, clk)
	query := model.MassStatusQuery{Venue: venueName}

	mass, err := exec.GenerateExecutionMassStatus(context.Background(), query)
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	if mass.AccountID != AccountIDDefault {
		t.Fatalf("account=%q, want blank query normalized to %q", mass.AccountID, AccountIDDefault)
	}
	if mass.OpenOrdersCoverage.State != model.CoverageComplete || !mass.OpenOrdersCoverage.Scope.Through.Equal(start) {
		t.Fatalf("open coverage=%+v, want complete at request start %s", mass.OpenOrdersCoverage, start)
	}
	wantIDs := model.NormalizeInstrumentIDs([]model.InstrumentID{eth.ID, btc.ID})
	if got := mass.OpenOrdersCoverage.Scope.InstrumentIDs; len(got) != 2 || got[0] != wantIDs[0] || got[1] != wantIDs[1] {
		t.Fatalf("frozen ids=%v, want %v", got, wantIDs)
	}
	if report, ok := mass.OrderReports["42"]; !ok || report.Order.Request.InstrumentID != btc.ID {
		t.Fatalf("frozen response resolution report=%+v ok=%v, want instrument %s", report, ok, btc.ID)
	}
	if mass.FillsCoverage.State != model.CoverageNotRequested || mass.PositionsCoverage.State != model.CoverageNotRequested {
		t.Fatalf("optional coverage fills=%+v positions=%+v", mass.FillsCoverage, mass.PositionsCoverage)
	}
	if err := mass.ValidateFor(query); err != nil {
		t.Fatalf("typed mass status validation: %v", err)
	}
}

func TestBinanceSpotMassStatusMarksUnsupportedRequestedDomainsUnavailable(t *testing.T) {
	inst := testSpotInstrument()
	exec := newExecutionClient(testREST(func(*http.Request) (string, int) { return `[]`, http.StatusOK }), testProvider(inst), clock.NewSimulatedClock(time.Unix(100, 0)))
	query := model.MassStatusQuery{IncludeFills: true, IncludePositions: true}

	mass, err := exec.GenerateExecutionMassStatus(context.Background(), query)
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	if mass.FillsCoverage.State != model.CoverageUnavailable || !mass.FillsCoverage.Scope.IsZero() {
		t.Fatalf("fills coverage=%+v, want unavailable before request", mass.FillsCoverage)
	}
	if mass.PositionsCoverage.State != model.CoverageUnavailable || !mass.PositionsCoverage.Scope.IsZero() {
		t.Fatalf("positions coverage=%+v, want unavailable before request", mass.PositionsCoverage)
	}
	if err := mass.ValidateFor(query); err != nil {
		t.Fatalf("typed mass status validation: %v", err)
	}
}

func TestBinanceSpotMassStatusMarksAttemptedOpenOrdersUnavailable(t *testing.T) {
	inst := testSpotInstrument()
	start := time.Unix(200, 0)
	exec := newExecutionClient(testREST(func(*http.Request) (string, int) {
		return `{"code":-1000,"msg":"temporary failure"}`, http.StatusInternalServerError
	}), testProvider(inst), clock.NewSimulatedClock(start))
	query := model.MassStatusQuery{}

	mass, err := exec.GenerateExecutionMassStatus(context.Background(), query)
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	coverage := mass.OpenOrdersCoverage
	if coverage.State != model.CoverageUnavailable || !coverage.Scope.Through.Equal(start) ||
		len(coverage.Scope.InstrumentIDs) != 1 || coverage.Scope.InstrumentIDs[0] != inst.ID {
		t.Fatalf("attempted open-order coverage=%+v", coverage)
	}
	if err := mass.ValidateFor(query); err != nil {
		t.Fatalf("typed mass status validation: %v", err)
	}
}

func TestBinanceSpotMassStatusRejectsUnknownExplicitInstrumentBeforeIO(t *testing.T) {
	inst := testSpotInstrument()
	unknown := inst.ID
	unknown.Symbol += "-UNKNOWN"
	calls := 0
	exec := newExecutionClient(testREST(func(*http.Request) (string, int) {
		calls++
		return `[]`, http.StatusOK
	}), testProvider(inst), clock.NewRealClock())

	mass, err := exec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{InstrumentIDs: []model.InstrumentID{unknown}})
	if mass != nil || err == nil {
		t.Fatalf("mass=%+v err=%v, want unknown-selector error", mass, err)
	}
	if calls != 0 {
		t.Fatalf("network calls=%d, want zero before selector rejection", calls)
	}
}

func TestBinanceSpotMassStatusRejectsForeignVenueBeforeIO(t *testing.T) {
	inst := testSpotInstrument()
	calls := 0
	exec := newExecutionClient(testREST(func(*http.Request) (string, int) {
		calls++
		return `[]`, http.StatusOK
	}), testProvider(inst), clock.NewRealClock())

	mass, err := exec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{Venue: "OTHER"})
	if mass != nil || err == nil {
		t.Fatalf("mass=%+v err=%v, want foreign-venue error", mass, err)
	}
	if calls != 0 {
		t.Fatalf("network calls=%d, want zero before venue rejection", calls)
	}
}
