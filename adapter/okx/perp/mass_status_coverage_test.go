package perp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/sdk/okx"
)

func TestOKXPerpMassStatusQueriesEveryPendingAlgoFamilyWithRequiredOrderType(t *testing.T) {
	inst := testOKXLinearInstrument(t)
	var gotRequests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v5/trade/orders-pending":
			gotRequests = append(gotRequests, "regular")
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
		case "/api/v5/trade/orders-algo-pending":
			ordType := r.URL.Query().Get("ordType")
			gotRequests = append(gotRequests, ordType)
			if ordType == "" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"code":"51000","msg":"Parameter ordType error","data":[]}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	rest := okx.NewClient().WithCredentials("key", "secret", "pass").WithBaseURL(server.URL)
	exec := newExecutionClient(rest, testOKXProvider(inst), clock.NewSimulatedClock(time.Unix(25, 0)), defaultDerivativeTdMode)

	mass, err := exec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{})
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	wantRequests := []string{"conditional,oco", "trigger", "move_order_stop", "iceberg", "twap", "smart_iceberg", "chase", "regular"}
	if strings.Join(gotRequests, "|") != strings.Join(wantRequests, "|") {
		t.Fatalf("pending-order requests=%q, want typed algos before regular snapshot %q", gotRequests, wantRequests)
	}
	if mass.OpenOrdersCoverage.State != model.CoverageComplete {
		t.Fatalf("open-order coverage=%+v warnings=%+v, want Complete", mass.OpenOrdersCoverage, mass.Warnings)
	}
}

func TestOKXPerpPendingAlgoTranslationDoesNotInventUnsupportedNeutralType(t *testing.T) {
	inst := testOKXLinearInstrument(t)
	order := orderFromPendingAlgo(&okx.AlgoOrder{
		InstId:  inst.VenueSymbol,
		AlgoId:  "twap-1",
		OrdType: "twap",
		State:   "live",
		Side:    "buy",
		PosSide: "net",
		Sz:      "1",
	}, frozenInstResolver{inst.VenueSymbol: inst.ID}, AccountIDDefault)
	if order.Request.Type != enums.TypeUnknown {
		t.Fatalf("unsupported OKX algo neutral type=%v, want TypeUnknown", order.Request.Type)
	}
}

func TestOKXPerpMassStatusOwnsFrozenTypedCoverage(t *testing.T) {
	btc := testOKXLinearInstrument(t)
	eth := instrumentFromOKX(&okx.Instrument{InstId: "ETH-USDT-SWAP", InstType: instTypeSwap, BaseCcy: "ETH", QuoteCcy: "USDT", SettleCcy: "USDT", CtVal: "1", TickSz: "0.1", LotSz: "1", MinSz: "1"})
	eth.ID.Symbol = "ETH-USDT-FROZEN"
	provider := testOKXProvider(btc)
	provider.byID[eth.ID.String()] = eth
	provider.byInstID[eth.VenueSymbol] = eth.ID
	provider.all = []*model.Instrument{eth, btc, eth}
	start := time.Date(2026, 7, 15, 4, 0, 0, 0, time.UTC)
	clk := clock.NewSimulatedClock(start)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			provider.mu.Lock()
			provider.byID = map[string]*model.Instrument{btc.ID.String(): btc}
			provider.byInstID = map[string]model.InstrumentID{btc.VenueSymbol: btc.ID}
			provider.all = []*model.Instrument{btc}
			provider.mu.Unlock()
		}
		clk.Advance(time.Minute)
		switch r.URL.Path {
		case "/api/v5/trade/orders-pending":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"ETH-USDT-SWAP","instType":"SWAP","ordId":"42","clOrdId":"c-eth","state":"live","side":"buy","posSide":"net","ordType":"limit","sz":"1","px":"100"}]}`))
		case "/api/v5/trade/orders-algo-pending":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"ETH-USDT-SWAP","instType":"SWAP","algoId":"84","algoClOrdId":"a-eth","state":"live","side":"buy","posSide":"net","ordType":"conditional","sz":"1","triggerPx":"90","orderPx":"100"}]}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	rest := okx.NewClient().WithCredentials("key", "secret", "pass").WithBaseURL(server.URL)
	exec := newExecutionClient(rest, provider, clk, defaultDerivativeTdMode)
	query := model.MassStatusQuery{Venue: venueName}

	mass, err := exec.GenerateExecutionMassStatus(context.Background(), query)
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	if calls != 1+len(perpPendingAlgoOrderTypes) {
		t.Fatalf("open-order calls=%d, want regular plus %d typed algo families", calls, len(perpPendingAlgoOrderTypes))
	}
	if mass.AccountID != AccountIDDefault || mass.OpenOrdersCoverage.State != model.CoverageComplete || !mass.OpenOrdersCoverage.Scope.Through.Equal(start) {
		t.Fatalf("mass account=%q open coverage=%+v", mass.AccountID, mass.OpenOrdersCoverage)
	}
	wantIDs := model.NormalizeInstrumentIDs([]model.InstrumentID{btc.ID, eth.ID})
	if got := mass.OpenOrdersCoverage.Scope.InstrumentIDs; len(got) != 2 || got[0] != wantIDs[0] || got[1] != wantIDs[1] {
		t.Fatalf("frozen ids=%v, want %v", got, wantIDs)
	}
	if report, ok := mass.OrderReports["42"]; !ok || report.Order.Request.InstrumentID != eth.ID {
		t.Fatalf("frozen response resolution report=%+v ok=%v, want instrument %s", report, ok, eth.ID)
	}
	if report, ok := mass.OrderReports["84"]; !ok || report.Order.Request.InstrumentID != eth.ID {
		t.Fatalf("frozen algo response resolution report=%+v ok=%v, want instrument %s", report, ok, eth.ID)
	}
	if mass.FillsCoverage.State != model.CoverageNotRequested || mass.PositionsCoverage.State != model.CoverageNotRequested {
		t.Fatalf("optional coverage fills=%+v positions=%+v", mass.FillsCoverage, mass.PositionsCoverage)
	}
	if err := mass.ValidateFor(query); err != nil {
		t.Fatalf("typed mass status validation: %v", err)
	}
}

func TestOKXPerpMassStatusMarksOneFailedAlgoFamilyPartial(t *testing.T) {
	inst := testOKXLinearInstrument(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v5/trade/orders-pending":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
		case "/api/v5/trade/orders-algo-pending":
			switch r.URL.Query().Get("ordType") {
			case string(okx.AlgoOrderTypeTrigger):
				_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"BTC-USDT-SWAP","instType":"SWAP","algoId":"84","algoClOrdId":"a-btc","state":"live","side":"buy","posSide":"net","ordType":"trigger","sz":"1","triggerPx":"90","orderPx":"-1"}]}`))
			case string(okx.AlgoOrderTypeTWAP):
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"code":"50000","msg":"temporary twap failure","data":[]}`))
			default:
				_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
			}
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	rest := okx.NewClient().WithCredentials("key", "secret", "pass").WithBaseURL(server.URL)
	exec := newExecutionClient(rest, testOKXProvider(inst), clock.NewSimulatedClock(time.Unix(40, 0)), defaultDerivativeTdMode)

	mass, err := exec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{})
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	if mass.OpenOrdersCoverage.State != model.CoveragePartial {
		t.Fatalf("coverage=%+v, want Partial", mass.OpenOrdersCoverage)
	}
	if _, ok := mass.OrderReports["84"]; !ok {
		t.Fatalf("successful trigger row was not retained: %+v", mass.OrderReports)
	}
	if !hasOKXPerpWarningCode(mass.Warnings, "ALGO_TWAP_UNAVAILABLE") {
		t.Fatalf("warnings=%+v, want ALGO_TWAP_UNAVAILABLE", mass.Warnings)
	}
}

func TestOKXPerpMassStatusRetainsSuccessfulDomainWhenAnotherFails(t *testing.T) {
	inst := testOKXLinearInstrument(t)
	for _, successfulDomain := range []string{"regular", "algo"} {
		t.Run(successfulDomain, func(t *testing.T) {
			start := time.Unix(50, 0)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v5/trade/orders-pending":
					if successfulDomain == "regular" {
						_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"BTC-USDT-SWAP","instType":"SWAP","ordId":"42","clOrdId":"c-btc","state":"live","side":"buy","posSide":"net","ordType":"limit","sz":"1","px":"100"}]}`))
						return
					}
				case "/api/v5/trade/orders-algo-pending":
					if successfulDomain == "algo" {
						_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"BTC-USDT-SWAP","instType":"SWAP","algoId":"84","algoClOrdId":"a-btc","state":"live","side":"buy","posSide":"net","ordType":"conditional","sz":"1","triggerPx":"90","orderPx":"100"}]}`))
						return
					}
				default:
					t.Fatalf("unexpected path %s", r.URL.Path)
				}
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"code":"50000","msg":"temporary failure","data":[]}`))
			}))
			defer server.Close()
			rest := okx.NewClient().WithCredentials("key", "secret", "pass").WithBaseURL(server.URL)
			exec := newExecutionClient(rest, testOKXProvider(inst), clock.NewSimulatedClock(start), defaultDerivativeTdMode)
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

func TestOKXPerpMassStatusMarksCappedPendingPagePartial(t *testing.T) {
	inst := testOKXLinearInstrument(t)
	for _, saturated := range []string{"regular", "algo"} {
		t.Run(saturated, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v5/trade/orders-pending":
					if saturated == "regular" {
						_, _ = w.Write([]byte(okxPerpPendingPage(inst.VenueSymbol, false)))
						return
					}
				case "/api/v5/trade/orders-algo-pending":
					if saturated == "algo" && r.URL.Query().Get("ordType") == string(okx.AlgoOrderTypeTrigger) {
						_, _ = w.Write([]byte(okxPerpPendingPage(inst.VenueSymbol, true)))
						return
					}
				}
				_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
			}))
			defer server.Close()
			rest := okx.NewClient().WithCredentials("key", "secret", "pass").WithBaseURL(server.URL)
			exec := newExecutionClient(rest, testOKXProvider(inst), clock.NewSimulatedClock(time.Unix(300, 0)), defaultDerivativeTdMode)

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
			if !hasOKXPerpSaturationWarning(mass.Warnings, saturated) {
				t.Fatalf("saturated %s warnings=%+v, want saturation warning", saturated, mass.Warnings)
			}
		})
	}
}

func TestOKXPerpMassStatusUsesPerFamilyPageCaps(t *testing.T) {
	inst := testOKXLinearInstrument(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v5/trade/orders-pending":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
		case "/api/v5/trade/orders-algo-pending":
			switch r.URL.Query().Get("ordType") {
			case "conditional,oco":
				_, _ = w.Write([]byte(okxPerpAlgoPendingPage(inst.VenueSymbol, "conditional", "conditional", 60)))
			case string(okx.AlgoOrderTypeTrigger):
				_, _ = w.Write([]byte(okxPerpAlgoPendingPage(inst.VenueSymbol, "trigger", "trigger", 60)))
			default:
				_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
			}
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	rest := okx.NewClient().WithCredentials("key", "secret", "pass").WithBaseURL(server.URL)
	exec := newExecutionClient(rest, testOKXProvider(inst), clock.NewSimulatedClock(time.Unix(350, 0)), defaultDerivativeTdMode)

	mass, err := exec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{})
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	if mass.OpenOrdersCoverage.State != model.CoverageComplete || len(mass.OrderReports) != 120 {
		t.Fatalf("coverage=%+v reports=%d, want Complete with two independent 60-row pages", mass.OpenOrdersCoverage, len(mass.OrderReports))
	}
}

func okxPerpPendingPage(instID string, algo bool) string {
	if algo {
		return okxPerpAlgoPendingPage(instID, "conditional", "a", 100)
	}
	rows := make([]string, 100)
	for i := range rows {
		rows[i] = fmt.Sprintf(`{"instId":%q,"instType":"SWAP","ordId":%q,"clOrdId":%q,"state":"live","side":"buy","posSide":"net","ordType":"limit","sz":"1","px":"100"}`, instID, fmt.Sprintf("r-%d", i), fmt.Sprintf("rc-%d", i))
	}
	return `{"code":"0","msg":"","data":[` + strings.Join(rows, ",") + `]}`
}

func okxPerpAlgoPendingPage(instID, orderType, prefix string, count int) string {
	rows := make([]string, count)
	for i := range rows {
		rows[i] = fmt.Sprintf(`{"instId":%q,"instType":"SWAP","algoId":%q,"algoClOrdId":%q,"state":"live","side":"buy","posSide":"net","ordType":%q,"sz":"1","triggerPx":"90","orderPx":"100"}`, instID, fmt.Sprintf("%s-%d", prefix, i), fmt.Sprintf("%sc-%d", prefix, i), orderType)
	}
	return `{"code":"0","msg":"","data":[` + strings.Join(rows, ",") + `]}`
}

func hasOKXPerpSaturationWarning(warnings []model.ReportWarning, saturated string) bool {
	want := strings.ToUpper(saturated) + "_ORDERS_SATURATED"
	if saturated == "algo" {
		want = "ALGO_TRIGGER_ORDERS_SATURATED"
	}
	for _, warning := range warnings {
		if warning.Code == want {
			return true
		}
	}
	return false
}

func hasOKXPerpWarningCode(warnings []model.ReportWarning, code string) bool {
	for _, warning := range warnings {
		if warning.Code == code {
			return true
		}
	}
	return false
}

func TestOKXPerpMassStatusMarksUnsupportedRequestedDomainsUnavailable(t *testing.T) {
	inst := testOKXLinearInstrument(t)
	start := time.Unix(100, 0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
	}))
	defer server.Close()
	rest := okx.NewClient().WithCredentials("key", "secret", "pass").WithBaseURL(server.URL)
	exec := newExecutionClient(rest, testOKXProvider(inst), clock.NewSimulatedClock(start), defaultDerivativeTdMode)
	query := model.MassStatusQuery{
		ClientID:         "client-scope",
		InstrumentIDs:    []model.InstrumentID{inst.ID, inst.ID},
		IncludeFills:     true,
		IncludePositions: true,
	}

	mass, err := exec.GenerateExecutionMassStatus(context.Background(), query)
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	if mass.FillsCoverage.State != model.CoverageUnavailable || !mass.FillsCoverage.Scope.IsZero() {
		t.Fatalf("unsupported coverage fills=%+v positions=%+v", mass.FillsCoverage, mass.PositionsCoverage)
	}
	positions := mass.PositionsCoverage
	if positions.State != model.CoverageUnavailable ||
		positions.Scope.AccountID != AccountIDDefault ||
		positions.Scope.ClientID != query.ClientID ||
		positions.Scope.InstrumentIDs == nil ||
		len(positions.Scope.InstrumentIDs) != 1 || positions.Scope.InstrumentIDs[0] != inst.ID ||
		!positions.Scope.From.IsZero() || !positions.Scope.Through.Equal(start) {
		t.Fatalf("unsupported position coverage=%+v, want frozen account/client/instrument scope at %s", positions, start)
	}
	if err := mass.ValidateFor(query); err != nil {
		t.Fatalf("typed mass status validation: %v", err)
	}
}

func TestOKXPerpMassStatusMarksAttemptedOpenOrdersUnavailable(t *testing.T) {
	inst := testOKXLinearInstrument(t)
	start := time.Unix(200, 0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"code":"50000","msg":"temporary failure","data":[]}`))
	}))
	defer server.Close()
	rest := okx.NewClient().WithCredentials("key", "secret", "pass").WithBaseURL(server.URL)
	exec := newExecutionClient(rest, testOKXProvider(inst), clock.NewSimulatedClock(start), defaultDerivativeTdMode)
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

func TestOKXPerpMassStatusRejectsUnknownExplicitInstrumentBeforeIO(t *testing.T) {
	inst := testOKXLinearInstrument(t)
	unknown := inst.ID
	unknown.Symbol += "-UNKNOWN"
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
	}))
	defer server.Close()
	rest := okx.NewClient().WithCredentials("key", "secret", "pass").WithBaseURL(server.URL)
	exec := newExecutionClient(rest, testOKXProvider(inst), clock.NewRealClock(), defaultDerivativeTdMode)

	mass, err := exec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{InstrumentIDs: []model.InstrumentID{unknown}})
	if mass != nil || err == nil {
		t.Fatalf("mass=%+v err=%v, want unknown-selector error", mass, err)
	}
	if calls != 0 {
		t.Fatalf("network calls=%d, want zero before selector rejection", calls)
	}
}

func TestOKXPerpMassStatusRejectsForeignVenueBeforeIO(t *testing.T) {
	inst := testOKXLinearInstrument(t)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
	}))
	defer server.Close()
	rest := okx.NewClient().WithCredentials("key", "secret", "pass").WithBaseURL(server.URL)
	exec := newExecutionClient(rest, testOKXProvider(inst), clock.NewRealClock(), defaultDerivativeTdMode)

	mass, err := exec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{Venue: "OTHER"})
	if mass != nil || err == nil {
		t.Fatalf("mass=%+v err=%v, want foreign-venue error", mass, err)
	}
	if calls != 0 {
		t.Fatalf("network calls=%d, want zero before venue rejection", calls)
	}
}
