package perp

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/model"
	sdkperp "github.com/QuantProcessing/boltertrader/sdk/binance/perp"
)

func TestBinancePerpMassStatusOwnsFrozenTypedCoverage(t *testing.T) {
	btc := rejectionTestInstrument()
	eth := instrumentFromSymbolInfo(&sdkperp.SymbolInfo{
		Symbol: "ETHUSDT", ContractType: "PERPETUAL", BaseAsset: "ETH", QuoteAsset: "USDT", MarginAsset: "USDT",
		Filters: []map[string]any{{"filterType": "PRICE_FILTER", "tickSize": "0.1"}},
	})
	provider := rejectionTestProvider(btc)
	provider.byID[eth.ID.String()] = eth
	provider.bySymbol[eth.VenueSymbol] = eth.ID
	provider.all = []*model.Instrument{eth, btc, eth}
	start := time.Date(2026, 7, 15, 2, 0, 0, 0, time.UTC)
	clk := clock.NewSimulatedClock(start)
	calls := 0
	rest := sdkperp.NewClient().WithRateLimiter(nil)
	rest.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		body := `[]`
		if calls == 1 {
			provider.mu.Lock()
			provider.byID = map[string]*model.Instrument{btc.ID.String(): btc}
			provider.bySymbol = map[string]model.InstrumentID{btc.VenueSymbol: btc.ID}
			provider.all = []*model.Instrument{btc}
			provider.mu.Unlock()
			body = `[{"orderId":42,"clientOrderId":"c-eth","symbol":"ETHUSDT","status":"NEW","side":"BUY","origQty":"1","price":"100","timeInForce":"GTC","type":"LIMIT"}]`
		}
		clk.Advance(time.Minute)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	exec := newExecutionClient(rest, provider, clk)
	query := model.MassStatusQuery{Venue: venueName}

	mass, err := exec.GenerateExecutionMassStatus(context.Background(), query)
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	if calls != 2 {
		t.Fatalf("open-order calls=%d, want regular+algo", calls)
	}
	if mass.OpenOrdersCoverage.State != model.CoverageComplete || !mass.OpenOrdersCoverage.Scope.Through.Equal(start) {
		t.Fatalf("open coverage=%+v, want complete at earliest request start %s", mass.OpenOrdersCoverage, start)
	}
	wantIDs := model.NormalizeInstrumentIDs([]model.InstrumentID{btc.ID, eth.ID})
	if got := mass.OpenOrdersCoverage.Scope.InstrumentIDs; len(got) != 2 || got[0] != wantIDs[0] || got[1] != wantIDs[1] {
		t.Fatalf("frozen ids=%v, want %v", got, wantIDs)
	}
	if report, ok := mass.OrderReports["42"]; !ok || report.Order.Request.InstrumentID != eth.ID {
		t.Fatalf("frozen response resolution report=%+v ok=%v, want instrument %s", report, ok, eth.ID)
	}
	if mass.FillsCoverage.State != model.CoverageNotRequested || mass.PositionsCoverage.State != model.CoverageNotRequested {
		t.Fatalf("optional coverage fills=%+v positions=%+v", mass.FillsCoverage, mass.PositionsCoverage)
	}
	if err := mass.ValidateFor(query); err != nil {
		t.Fatalf("typed mass status validation: %v", err)
	}
}

func TestBinancePerpMassStatusRetainsSuccessfulDomainWhenAnotherFails(t *testing.T) {
	inst := rejectionTestInstrument()
	for _, successfulDomain := range []string{"regular", "algo"} {
		t.Run(successfulDomain, func(t *testing.T) {
			start := time.Unix(50, 0)
			rest := sdkperp.NewClient().WithRateLimiter(nil)
			rest.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				var body string
				status := http.StatusOK
				switch r.URL.Path {
				case "/fapi/v1/openOrders":
					if successfulDomain == "regular" {
						body = `[{"orderId":42,"clientOrderId":"c-btc","symbol":"BTCUSDT","status":"NEW","side":"BUY","origQty":"1","price":"100","timeInForce":"GTC","type":"LIMIT"}]`
					} else {
						status, body = http.StatusInternalServerError, `{"code":-1000,"msg":"temporary regular failure"}`
					}
				case "/fapi/v1/openAlgoOrders":
					if successfulDomain == "algo" {
						body = `[{"algoId":84,"clientAlgoId":"a-btc","symbol":"BTCUSDT","algoStatus":"NEW","side":"BUY","orderType":"STOP_MARKET","quantity":"1","triggerPrice":"90","timeInForce":"GTC","positionSide":"BOTH"}]`
					} else {
						status, body = http.StatusInternalServerError, `{"code":-1000,"msg":"temporary algo failure"}`
					}
				default:
					t.Fatalf("unexpected path %s", r.URL.Path)
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})}
			exec := newExecutionClient(rest, rejectionTestProvider(inst), clock.NewSimulatedClock(start))
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

func TestBinancePerpMassStatusMarksUnsupportedRequestedDomainsUnavailable(t *testing.T) {
	inst := rejectionTestInstrument()
	rest := sdkperp.NewClient().WithRateLimiter(nil)
	rest.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`[]`)), Header: make(http.Header)}, nil
	})}
	exec := newExecutionClient(rest, rejectionTestProvider(inst), clock.NewSimulatedClock(time.Unix(100, 0)))
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

func TestBinancePerpMassStatusMarksAttemptedOpenOrdersUnavailable(t *testing.T) {
	inst := rejectionTestInstrument()
	start := time.Unix(200, 0)
	rest := sdkperp.NewClient().WithRateLimiter(nil)
	rest.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusInternalServerError, Body: io.NopCloser(strings.NewReader(`{"code":-1000,"msg":"temporary failure"}`)), Header: make(http.Header)}, nil
	})}
	exec := newExecutionClient(rest, rejectionTestProvider(inst), clock.NewSimulatedClock(start))
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

func TestBinancePerpMassStatusRejectsUnknownExplicitInstrumentBeforeIO(t *testing.T) {
	inst := rejectionTestInstrument()
	unknown := inst.ID
	unknown.Symbol += "-UNKNOWN"
	calls := 0
	rest := sdkperp.NewClient().WithRateLimiter(nil)
	rest.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`[]`)), Header: make(http.Header)}, nil
	})}
	exec := newExecutionClient(rest, rejectionTestProvider(inst), clock.NewRealClock())

	mass, err := exec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{InstrumentIDs: []model.InstrumentID{unknown}})
	if mass != nil || err == nil {
		t.Fatalf("mass=%+v err=%v, want unknown-selector error", mass, err)
	}
	if calls != 0 {
		t.Fatalf("network calls=%d, want zero before selector rejection", calls)
	}
}

func TestBinancePerpMassStatusRejectsForeignVenueBeforeIO(t *testing.T) {
	inst := rejectionTestInstrument()
	calls := 0
	rest := sdkperp.NewClient().WithRateLimiter(nil)
	rest.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`[]`)), Header: make(http.Header)}, nil
	})}
	exec := newExecutionClient(rest, rejectionTestProvider(inst), clock.NewRealClock())

	mass, err := exec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{Venue: "OTHER"})
	if mass != nil || err == nil {
		t.Fatalf("mass=%+v err=%v, want foreign-venue error", mass, err)
	}
	if calls != 0 {
		t.Fatalf("network calls=%d, want zero before venue rejection", calls)
	}
}
