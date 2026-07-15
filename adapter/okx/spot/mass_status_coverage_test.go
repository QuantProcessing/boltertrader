package spot

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/sdk/okx"
)

func TestOKXSpotMassStatusOwnsFrozenTypedCoverage(t *testing.T) {
	eth := testSpotInstrument()
	btc := instrumentFromOKX(&okx.Instrument{InstId: "BTC-USDT", InstType: instTypeSpot, BaseCcy: "BTC", QuoteCcy: "USDT", State: "live", TickSz: "0.1", LotSz: "0.0001", MinSz: "0.0001"})
	btc.ID.Symbol = "BTC-USDT-FROZEN"
	provider := testProvider(eth)
	provider.byID[btc.ID.String()] = btc
	provider.byInstID[btc.VenueSymbol] = btc.ID
	provider.all = []*model.Instrument{eth, btc, eth}
	start := time.Date(2026, 7, 15, 3, 0, 0, 0, time.UTC)
	clk := clock.NewSimulatedClock(start)
	calls := 0
	rest := testREST(func(r *http.Request) (string, int) {
		calls++
		if calls == 1 {
			provider.mu.Lock()
			provider.byID = map[string]*model.Instrument{eth.ID.String(): eth}
			provider.byInstID = map[string]model.InstrumentID{eth.VenueSymbol: eth.ID}
			provider.all = []*model.Instrument{eth}
			provider.mu.Unlock()
		}
		clk.Advance(time.Minute)
		switch r.URL.Path {
		case "/api/v5/trade/orders-pending":
			return `{"code":"0","msg":"","data":[{"instId":"BTC-USDT","instType":"SPOT","ordId":"42","clOrdId":"c-btc","state":"live","side":"buy","ordType":"limit","sz":"1","px":"100"}]}`, http.StatusOK
		case "/api/v5/trade/orders-algo-pending":
			return `{"code":"0","msg":"","data":[{"instId":"BTC-USDT","instType":"SPOT","algoId":"84","algoClOrdId":"a-btc","state":"live","side":"buy","ordType":"conditional","sz":"1","triggerPx":"90","orderPx":"100"}]}`, http.StatusOK
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
			return "", 0
		}
	})
	exec := newExecutionClient(rest, provider, clk, defaultSpotTdMode)
	query := model.MassStatusQuery{Venue: venueName}

	mass, err := exec.GenerateExecutionMassStatus(context.Background(), query)
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	if calls != 2 {
		t.Fatalf("open-order calls=%d, want regular+algo", calls)
	}
	if mass.AccountID != AccountIDDefault || mass.OpenOrdersCoverage.State != model.CoverageComplete || !mass.OpenOrdersCoverage.Scope.Through.Equal(start) {
		t.Fatalf("mass account=%q open coverage=%+v", mass.AccountID, mass.OpenOrdersCoverage)
	}
	wantIDs := model.NormalizeInstrumentIDs([]model.InstrumentID{eth.ID, btc.ID})
	if got := mass.OpenOrdersCoverage.Scope.InstrumentIDs; len(got) != 2 || got[0] != wantIDs[0] || got[1] != wantIDs[1] {
		t.Fatalf("frozen ids=%v, want %v", got, wantIDs)
	}
	if report, ok := mass.OrderReports["42"]; !ok || report.Order.Request.InstrumentID != btc.ID {
		t.Fatalf("frozen response resolution report=%+v ok=%v, want instrument %s", report, ok, btc.ID)
	}
	if report, ok := mass.OrderReports["84"]; !ok || report.Order.Request.InstrumentID != btc.ID {
		t.Fatalf("frozen algo response resolution report=%+v ok=%v, want instrument %s", report, ok, btc.ID)
	}
	if mass.FillsCoverage.State != model.CoverageNotRequested || mass.PositionsCoverage.State != model.CoverageNotRequested {
		t.Fatalf("optional coverage fills=%+v positions=%+v", mass.FillsCoverage, mass.PositionsCoverage)
	}
	if err := mass.ValidateFor(query); err != nil {
		t.Fatalf("typed mass status validation: %v", err)
	}
}

func TestOKXSpotMassStatusRetainsSuccessfulDomainWhenAnotherFails(t *testing.T) {
	inst := testSpotInstrument()
	for _, successfulDomain := range []string{"regular", "algo"} {
		t.Run(successfulDomain, func(t *testing.T) {
			start := time.Unix(50, 0)
			exec := newExecutionClient(testREST(func(r *http.Request) (string, int) {
				switch r.URL.Path {
				case "/api/v5/trade/orders-pending":
					if successfulDomain == "regular" {
						return `{"code":"0","msg":"","data":[{"instId":"ETH-USDT","instType":"SPOT","ordId":"42","clOrdId":"c-eth","state":"live","side":"buy","ordType":"limit","sz":"1","px":"100"}]}`, http.StatusOK
					}
					return `{"code":"50000","msg":"temporary regular failure","data":[]}`, http.StatusInternalServerError
				case "/api/v5/trade/orders-algo-pending":
					if successfulDomain == "algo" {
						return `{"code":"0","msg":"","data":[{"instId":"ETH-USDT","instType":"SPOT","algoId":"84","algoClOrdId":"a-eth","state":"live","side":"buy","ordType":"conditional","sz":"1","triggerPx":"90","orderPx":"100"}]}`, http.StatusOK
					}
					return `{"code":"50000","msg":"temporary algo failure","data":[]}`, http.StatusInternalServerError
				default:
					t.Fatalf("unexpected path %s", r.URL.Path)
					return "", 0
				}
			}), testProvider(inst), clock.NewSimulatedClock(start), defaultSpotTdMode)
			query := model.MassStatusQuery{InstrumentIDs: []model.InstrumentID{inst.ID}}

			mass, err := exec.GenerateExecutionMassStatus(context.Background(), query)
			if err != nil {
				t.Fatalf("GenerateExecutionMassStatus: %v", err)
			}
			coverage := mass.OpenOrdersCoverage
			if coverage.State != model.CoveragePartial || coverage.Scope.AccountID != AccountIDDefault || !coverage.Scope.Through.Equal(start) {
				t.Fatalf("coverage=%+v, want fully scoped Partial", coverage)
			}
			if got := coverage.Scope.InstrumentIDs; len(got) != 1 || got[0] != inst.ID {
				t.Fatalf("coverage ids=%v, want [%s]", got, inst.ID)
			}
			wantOrderID := "42"
			if successfulDomain == "algo" {
				wantOrderID = "84"
			}
			if report, ok := mass.OrderReports[wantOrderID]; !ok || report.Order.Request.InstrumentID != inst.ID {
				t.Fatalf("retained report=%+v ok=%t, want %s row", report, ok, successfulDomain)
			}
			if len(mass.OrderReports) != 1 {
				t.Fatalf("reports=%+v, want one retained successful-domain row", mass.OrderReports)
			}
			if err := mass.ValidateFor(query); err != nil {
				t.Fatalf("typed mass status validation: %v", err)
			}
		})
	}
}

func TestOKXSpotMassStatusMarksCappedPendingPagePartial(t *testing.T) {
	inst := testSpotInstrument()
	for _, saturated := range []string{"regular", "algo"} {
		t.Run(saturated, func(t *testing.T) {
			exec := newExecutionClient(testREST(func(r *http.Request) (string, int) {
				switch r.URL.Path {
				case "/api/v5/trade/orders-pending":
					if saturated == "regular" {
						return okxSpotPendingPage(inst.VenueSymbol, false), http.StatusOK
					}
				case "/api/v5/trade/orders-algo-pending":
					if saturated == "algo" {
						return okxSpotPendingPage(inst.VenueSymbol, true), http.StatusOK
					}
				}
				return `{"code":"0","msg":"","data":[]}`, http.StatusOK
			}), testProvider(inst), clock.NewSimulatedClock(time.Unix(300, 0)), defaultSpotTdMode)

			mass, err := exec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{})
			if err != nil {
				t.Fatalf("GenerateExecutionMassStatus: %v", err)
			}
			coverage := mass.OpenOrdersCoverage
			if coverage.State != model.CoveragePartial || coverage.Scope.AccountID != AccountIDDefault || coverage.Scope.ClientID != "" || !coverage.Scope.Through.Equal(time.Unix(300, 0)) {
				t.Fatalf("saturated %s coverage=%+v, want fully scoped Partial", saturated, coverage)
			}
			if got := coverage.Scope.InstrumentIDs; len(got) != 1 || got[0] != inst.ID {
				t.Fatalf("saturated %s coverage ids=%v, want [%s]", saturated, got, inst.ID)
			}
			if len(mass.OrderReports) != 100 {
				t.Fatalf("saturated %s reports=%d, want retained page of 100", saturated, len(mass.OrderReports))
			}
			if !hasOKXSaturationWarning(mass.Warnings, saturated) {
				t.Fatalf("saturated %s warnings=%+v, want saturation warning", saturated, mass.Warnings)
			}
		})
	}
}

func okxSpotPendingPage(instID string, algo bool) string {
	rows := make([]string, 100)
	for i := range rows {
		if algo {
			rows[i] = fmt.Sprintf(`{"instId":%q,"instType":"SPOT","algoId":%q,"algoClOrdId":%q,"state":"live","side":"buy","ordType":"conditional","sz":"1","triggerPx":"90","orderPx":"100"}`, instID, fmt.Sprintf("a-%d", i), fmt.Sprintf("ac-%d", i))
		} else {
			rows[i] = fmt.Sprintf(`{"instId":%q,"instType":"SPOT","ordId":%q,"clOrdId":%q,"state":"live","side":"buy","ordType":"limit","sz":"1","px":"100"}`, instID, fmt.Sprintf("r-%d", i), fmt.Sprintf("rc-%d", i))
		}
	}
	return `{"code":"0","msg":"","data":[` + strings.Join(rows, ",") + `]}`
}

func hasOKXSaturationWarning(warnings []model.ReportWarning, saturated string) bool {
	want := strings.ToUpper(saturated) + "_ORDERS_SATURATED"
	for _, warning := range warnings {
		if warning.Code == want {
			return true
		}
	}
	return false
}

func TestOKXSpotMassStatusMarksUnsupportedRequestedDomainsUnavailable(t *testing.T) {
	inst := testSpotInstrument()
	exec := newExecutionClient(testREST(func(r *http.Request) (string, int) {
		return `{"code":"0","msg":"","data":[]}`, http.StatusOK
	}), testProvider(inst), clock.NewSimulatedClock(time.Unix(100, 0)), defaultSpotTdMode)
	query := model.MassStatusQuery{IncludeFills: true, IncludePositions: true}

	mass, err := exec.GenerateExecutionMassStatus(context.Background(), query)
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	if mass.FillsCoverage.State != model.CoverageUnavailable || !mass.FillsCoverage.Scope.IsZero() ||
		mass.PositionsCoverage.State != model.CoverageUnavailable || !mass.PositionsCoverage.Scope.IsZero() {
		t.Fatalf("unsupported coverage fills=%+v positions=%+v", mass.FillsCoverage, mass.PositionsCoverage)
	}
	if err := mass.ValidateFor(query); err != nil {
		t.Fatalf("typed mass status validation: %v", err)
	}
}

func TestOKXSpotMassStatusMarksAttemptedOpenOrdersUnavailable(t *testing.T) {
	inst := testSpotInstrument()
	start := time.Unix(200, 0)
	exec := newExecutionClient(testREST(func(*http.Request) (string, int) {
		return `{"code":"50000","msg":"temporary failure","data":[]}`, http.StatusInternalServerError
	}), testProvider(inst), clock.NewSimulatedClock(start), defaultSpotTdMode)
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

func TestOKXSpotMassStatusRejectsUnknownExplicitInstrumentBeforeIO(t *testing.T) {
	inst := testSpotInstrument()
	unknown := inst.ID
	unknown.Symbol += "-UNKNOWN"
	calls := 0
	exec := newExecutionClient(testREST(func(*http.Request) (string, int) {
		calls++
		return `{"code":"0","msg":"","data":[]}`, http.StatusOK
	}), testProvider(inst), clock.NewRealClock(), defaultSpotTdMode)

	mass, err := exec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{InstrumentIDs: []model.InstrumentID{unknown}})
	if mass != nil || err == nil {
		t.Fatalf("mass=%+v err=%v, want unknown-selector error", mass, err)
	}
	if calls != 0 {
		t.Fatalf("network calls=%d, want zero before selector rejection", calls)
	}
}

func TestOKXSpotMassStatusRejectsForeignVenueBeforeIO(t *testing.T) {
	inst := testSpotInstrument()
	calls := 0
	exec := newExecutionClient(testREST(func(*http.Request) (string, int) {
		calls++
		return `{"code":"0","msg":"","data":[]}`, http.StatusOK
	}), testProvider(inst), clock.NewRealClock(), defaultSpotTdMode)

	mass, err := exec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{Venue: "OTHER"})
	if mass != nil || err == nil {
		t.Fatalf("mass=%+v err=%v, want foreign-venue error", mass, err)
	}
	if calls != 0 {
		t.Fatalf("network calls=%d, want zero before venue rejection", calls)
	}
}
