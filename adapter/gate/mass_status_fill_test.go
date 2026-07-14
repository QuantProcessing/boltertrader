package gate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	gatesdk "github.com/QuantProcessing/boltertrader/sdk/gate"
)

func TestGateExecutionMassStatusIncludesBoundedFillsWhenRequested(t *testing.T) {
	provider := gateSpotTestProvider()
	since := time.UnixMilli(1_700_000_000_000)
	until := since.Add(2 * time.Second)
	const clientID = "keep-client"
	var fillCalls atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/spot/open_orders":
			writeJSON(t, w, []any{})
		case "/spot/my_trades":
			fillCalls.Add(1)
			if got := r.URL.Query().Get("currency_pair"); got != "" {
				t.Errorf("mass fill currency_pair=%q, want one unscoped product query", got)
			}
			if got := r.URL.Query().Get("limit"); got != "100" {
				t.Errorf("fill limit=%q, want 100", got)
			}
			writeJSON(t, w, []any{
				gateSpotFillFixture("inside", "inside-order", clientID, since.Add(time.Second)),
				gateSpotFillFixture("before", "before-order", clientID, since.Add(-time.Millisecond)),
				gateSpotFillFixture("after", "after-order", clientID, until.Add(time.Millisecond)),
				gateSpotFillFixture("other-client", "other-order", "other-client", since.Add(time.Second)),
			})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.Error(w, "unexpected path", http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	exec := newExecutionClient(
		gatesdk.NewClient().
			WithCredentials("key", "secret").
			WithBaseURL(server.URL).
			WithHTTPClient(server.Client()),
		provider,
		clock.NewSimulatedClock(until.Add(time.Minute)),
	).withScope([]enums.InstrumentKind{enums.KindSpot})
	lookback := 5 * time.Minute
	mass, err := exec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{
		AccountID:    AccountIDUnified,
		ClientID:     clientID,
		Since:        since,
		Until:        until,
		Lookback:     lookback,
		IncludeFills: true,
	})
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	if mass.AccountID != AccountIDUnified || mass.ClientID != clientID || mass.Lookback != lookback {
		t.Fatalf("mass query identity/bounds not preserved: %+v", mass)
	}
	reports := mass.FillReports["inside-order"]
	if len(mass.FillReports) != 1 || len(reports) != 1 {
		t.Fatalf("fill reports=%+v, want only the in-window/client-matched fill", mass.FillReports)
	}
	if report := reports[0]; report.AccountID != AccountIDUnified || report.Fill.AccountID != AccountIDUnified || report.Fill.ClientID != clientID || !report.Fill.Timestamp.Equal(since.Add(time.Second)) {
		t.Fatalf("unexpected bounded fill report: %+v", report)
	}
	if got := fillCalls.Load(); got != 1 {
		t.Fatalf("fill history calls=%d, want 1", got)
	}

	withoutFills, err := exec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{
		AccountID: AccountIDUnified,
		Since:     since,
		Until:     until,
	})
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus without fills: %v", err)
	}
	if len(withoutFills.FillReports) != 0 {
		t.Fatalf("fills returned without IncludeFills: %+v", withoutFills.FillReports)
	}
	if got := fillCalls.Load(); got != 1 {
		t.Fatalf("IncludeFills=false made a fill-history request; calls=%d", got)
	}

	wrongAccount, err := exec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{
		AccountID:    "GATE-OTHER",
		IncludeFills: true,
	})
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus for another account: %v", err)
	}
	if wrongAccount.AccountID != "GATE-OTHER" || len(wrongAccount.FillReports) != 0 {
		t.Fatalf("unexpected cross-account mass status: %+v", wrongAccount)
	}
	if got := fillCalls.Load(); got != 1 {
		t.Fatalf("cross-account mass status made a fill-history request; calls=%d", got)
	}
}

func TestGateExecutionMassStatusQueriesFillHistoryOncePerProduct(t *testing.T) {
	provider := newInstrumentProvider()
	provider.LoadSnapshot([]*model.Instrument{
		instrumentFromGateSpot(gatesdk.CurrencyPair{ID: "ETH_USDT", Base: "ETH", Quote: "USDT", TradeStatus: "tradable", AmountPrecision: 4, Precision: 2, MinBaseAmount: "0.001", MinQuoteAmount: "5"}),
		instrumentFromGateSpot(gatesdk.CurrencyPair{ID: "BTC_USDT", Base: "BTC", Quote: "USDT", TradeStatus: "tradable", AmountPrecision: 6, Precision: 2, MinBaseAmount: "0.00001", MinQuoteAmount: "5"}),
		instrumentFromGateContract(gatesdk.SettleUSDT, gatesdk.Contract{Name: "BTC_USDT", Status: "trading", QuantoMultiplier: "0.0001", OrderPriceRound: "0.1", OrderSizeMin: 1}),
		instrumentFromGateContract(gatesdk.SettleUSDT, gatesdk.Contract{Name: "ETH_USDT", Status: "trading", QuantoMultiplier: "0.001", OrderPriceRound: "0.01", OrderSizeMin: 1}),
	})
	var spotCalls atomic.Int32
	var futuresCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/futures/usdt/accounts":
			writeJSON(t, w, map[string]any{"user": 42, "position_mode": "single"})
		case "/spot/open_orders", "/futures/usdt/orders":
			writeJSON(t, w, []any{})
		case "/spot/my_trades":
			spotCalls.Add(1)
			if got := r.URL.Query().Get("currency_pair"); got != "" {
				t.Errorf("spot currency_pair=%q, want unscoped", got)
			}
			writeJSON(t, w, []any{})
		case "/futures/usdt/my_trades":
			futuresCalls.Add(1)
			if got := r.URL.Query().Get("contract"); got != "" {
				t.Errorf("futures contract=%q, want unscoped", got)
			}
			writeJSON(t, w, []any{})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.Error(w, "unexpected path", http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	exec := newExecutionClient(
		gatesdk.NewClient().WithCredentials("key", "secret").WithBaseURL(server.URL).WithHTTPClient(server.Client()),
		provider,
		clock.NewRealClock(),
	).withScope([]enums.InstrumentKind{enums.KindSpot, enums.KindPerp})
	if _, err := exec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{AccountID: AccountIDUnified, IncludeFills: true}); err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	if got := spotCalls.Load(); got != 1 {
		t.Fatalf("spot fill-history calls=%d, want 1", got)
	}
	if got := futuresCalls.Load(); got != 1 {
		t.Fatalf("futures fill-history calls=%d, want 1", got)
	}
}

func TestGateExecutionMassStatusWarnsWhenFillPageReachesLimit(t *testing.T) {
	const fillPageLimit = 100
	provider := gateSpotTestProvider()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/spot/open_orders":
			writeJSON(t, w, []any{})
		case "/spot/my_trades":
			records := make([]any, 0, fillPageLimit)
			records = append(records, gateSpotFillFixture("keep", "keep-order", "keep-client", time.UnixMilli(1_700_000_000_000)))
			for i := 1; i < fillPageLimit; i++ {
				records = append(records, gateSpotFillFixture("other-"+strconv.Itoa(i), "other-order-"+strconv.Itoa(i), "other-client", time.UnixMilli(1_700_000_000_000+int64(i))))
			}
			writeJSON(t, w, records)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.Error(w, "unexpected path", http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	exec := newExecutionClient(
		gatesdk.NewClient().WithCredentials("key", "secret").WithBaseURL(server.URL).WithHTTPClient(server.Client()),
		provider,
		clock.NewRealClock(),
	).withScope([]enums.InstrumentKind{enums.KindSpot})
	mass, err := exec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{
		AccountID:    AccountIDUnified,
		ClientID:     "keep-client",
		IncludeFills: true,
	})
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	if len(mass.FillReports) != 1 {
		t.Fatalf("filtered fill reports=%d, want 1", len(mass.FillReports))
	}
	if !gateHasWarning(mass.Warnings, "FILL_REPORTS_LIMIT_REACHED") {
		t.Fatalf("warnings=%+v, want FILL_REPORTS_LIMIT_REACHED", mass.Warnings)
	}
}

func gateSpotFillFixture(tradeID, orderID, clientID string, timestamp time.Time) map[string]any {
	return map[string]any{
		"id":             tradeID,
		"text":           "t-" + clientID,
		"currency_pair":  "ETH_USDT",
		"order_id":       orderID,
		"side":           "buy",
		"role":           "taker",
		"amount":         "0.01",
		"price":          "1000",
		"fee":            "-0.01",
		"fee_currency":   "USDT",
		"create_time_ms": strconv.FormatInt(timestamp.UnixMilli(), 10),
	}
}

func gateHasWarning(warnings []model.ReportWarning, code string) bool {
	for _, warning := range warnings {
		if warning.Code == code {
			return true
		}
	}
	return false
}
