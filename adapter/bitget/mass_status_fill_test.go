package bitget

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	bitgetsdk "github.com/QuantProcessing/boltertrader/sdk/bitget"
)

func TestBitgetExecutionMassStatusIncludesBoundedFillsWhenRequested(t *testing.T) {
	provider := newInstrumentProvider()
	provider.LoadSnapshot([]*model.Instrument{
		instrumentFromBitget(bitgetsdk.Instrument{
			Category:  "SPOT",
			Symbol:    "ETHUSDT",
			BaseCoin:  "ETH",
			QuoteCoin: "USDT",
			Status:    "online",
		}),
	})
	since := time.UnixMilli(1_700_000_000_000)
	until := since.Add(2 * time.Second)
	const clientID = "keep-client"
	var fillCalls atomic.Int32
	var historyCalls atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v3/trade/unfilled-orders":
			writeJSON(t, w, map[string]any{
				"code": "00000",
				"msg":  "success",
				"data": map[string]any{"list": []any{}},
			})
		case "/api/v3/trade/fills":
			fillCalls.Add(1)
			if got := r.URL.Query().Get("category"); got != "SPOT" {
				t.Errorf("fill category=%q, want SPOT", got)
			}
			if got := r.URL.Query().Get("startTime"); got != strconv.FormatInt(since.UnixMilli(), 10) {
				t.Errorf("fill startTime=%q, want %d", got, since.UnixMilli())
			}
			if got := r.URL.Query().Get("endTime"); got != strconv.FormatInt(until.UnixMilli(), 10) {
				t.Errorf("fill endTime=%q, want %d", got, until.UnixMilli())
			}
			if got := r.URL.Query().Get("limit"); got != "100" {
				t.Errorf("fill page limit=%q, want venue maximum 100", got)
			}
			writeJSON(t, w, map[string]any{
				"code": "00000",
				"msg":  "success",
				"data": map[string]any{"list": []any{
					bitgetFillFixture("inside", "inside-order", clientID, since.Add(time.Second)),
					bitgetFillFixture("before", "before-order", clientID, since.Add(-time.Millisecond)),
					bitgetFillFixture("after", "after-order", clientID, until.Add(time.Millisecond)),
					bitgetFillFixture("other-client", "other-order", "other-client", since.Add(time.Second)),
				}},
			})
		case "/api/v3/trade/history-orders":
			historyCalls.Add(1)
			writeJSON(t, w, map[string]any{"code": "00000", "msg": "success", "data": map[string]any{"list": []any{}, "cursor": ""}})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.Error(w, "unexpected path", http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	exec := newExecutionClient(
		bitgetsdk.NewClient().
			WithCredentials("key", "secret", "pass").
			WithBaseURL(server.URL).
			WithHTTPClient(server.Client()),
		provider,
		clock.NewSimulatedClock(until.Add(time.Minute)),
	)
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
	if got := historyCalls.Load(); got != 0 {
		t.Fatalf("Spot fills triggered derivative order history; calls=%d", got)
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
}

func TestBitgetExecutionMassStatusBatchHydratesDerivativeFillOrders(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000)
	perp := instrumentFromBitget(bitgetsdk.Instrument{
		Category:  bitgetsdk.ProductTypeUSDTFutures,
		Symbol:    "BTCUSDT",
		BaseCoin:  "BTC",
		QuoteCoin: "USDT",
		Status:    "online",
	})
	provider := newInstrumentProvider()
	provider.LoadSnapshot([]*model.Instrument{perp})

	var historyCalls atomic.Int32
	var historyWindows [][2]int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v3/trade/unfilled-orders":
			writeJSON(t, w, map[string]any{"code": "00000", "msg": "success", "data": map[string]any{"list": []any{
				bitgetOrderFixture("order-existing", "client-existing", "long", "new"),
			}, "cursor": ""}})
		case "/api/v3/trade/fills":
			writeJSON(t, w, map[string]any{"code": "00000", "msg": "success", "data": map[string]any{"list": []any{
				bitgetDerivativeFillFixture("fill-1", "order-1", "client-1", now.Add(-time.Hour)),
				bitgetDerivativeFillFixture("fill-2", "order-2", "client-2", now.Add(-2*time.Hour)),
				bitgetDerivativeFillFixture("fill-3", "order-3", "client-3", now.Add(-3*time.Hour)),
				bitgetDerivativeFillFixture("fill-existing", "order-existing", "client-existing", now.Add(-4*time.Hour)),
			}, "cursor": ""}})
		case "/api/v3/trade/history-orders":
			historyCalls.Add(1)
			start, _ := strconv.ParseInt(r.URL.Query().Get("startTime"), 10, 64)
			end, _ := strconv.ParseInt(r.URL.Query().Get("endTime"), 10, 64)
			historyWindows = append(historyWindows, [2]int64{start, end})
			if got := r.URL.Query().Get("category"); got != bitgetsdk.ProductTypeUSDTFutures {
				t.Errorf("history category=%q, want %s", got, bitgetsdk.ProductTypeUSDTFutures)
			}
			if got := r.URL.Query().Get("symbol"); got != "BTCUSDT" {
				t.Errorf("history symbol=%q, want BTCUSDT", got)
			}
			writeJSON(t, w, map[string]any{"code": "00000", "msg": "success", "data": map[string]any{"list": []any{
				bitgetOrderFixture("order-1", "client-1", "long", "filled"),
				bitgetOrderFixture("order-2", "client-2", "short", "filled"),
				bitgetOrderFixture("order-3", "wrong-client", "long", "filled"),
				bitgetOrderFixture("order-existing", "client-existing", "short", "filled"),
				bitgetOrderFixture("unrelated", "unrelated-client", "long", "filled"),
			}, "cursor": ""}})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.Error(w, "unexpected path", http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	exec := newExecutionClient(
		bitgetsdk.NewClient().WithCredentials("key", "secret", "pass").WithBaseURL(server.URL).WithHTTPClient(server.Client()),
		provider,
		clock.NewSimulatedClock(now),
	)
	mass, err := exec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{AccountID: AccountIDUnified, IncludeFills: true})
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	if got := historyCalls.Load(); got != 3 {
		t.Fatalf("history calls=%d, want three 30-day windows for four same-instrument fills", got)
	}
	if len(historyWindows) != 3 {
		t.Fatalf("history windows=%v, want three", historyWindows)
	}
	sort.Slice(historyWindows, func(i, j int) bool { return historyWindows[i][0] < historyWindows[j][0] })
	if historyWindows[0][0] != now.Add(-90*24*time.Hour).UnixMilli() || historyWindows[2][1] != now.UnixMilli() {
		t.Fatalf("history windows=%v, want full retained 90-day range ending now", historyWindows)
	}
	for _, window := range historyWindows {
		if got := time.Duration(window[1]-window[0]) * time.Millisecond; got > 30*24*time.Hour {
			t.Fatalf("history window=%v spans %s, want at most 30 days", window, got)
		}
	}

	longReport, ok := mass.OrderReports["order-1"]
	if !ok || longReport.Order.Request.PositionSide != enums.PosLong {
		t.Fatalf("long historical order=%+v ok=%v, want hedge LONG", longReport, ok)
	}
	shortReport, ok := mass.OrderReports["order-2"]
	if !ok || shortReport.Order.Request.PositionSide != enums.PosShort {
		t.Fatalf("short historical order=%+v ok=%v, want hedge SHORT", shortReport, ok)
	}
	if _, ok := mass.OrderReports["order-3"]; ok {
		t.Fatalf("dual-identity mismatch was accepted: %+v", mass.OrderReports["order-3"])
	}
	if _, ok := mass.OrderReports["unrelated"]; ok {
		t.Fatalf("unrelated history order was added: %+v", mass.OrderReports["unrelated"])
	}
	existing := mass.OrderReports["order-existing"].Order
	if existing.Status != enums.StatusNew || existing.Request.PositionSide != enums.PosLong {
		t.Fatalf("existing mass order was overwritten by history: %+v", existing)
	}
}

func TestBitgetExecutionMassStatusFallsBackWhenDerivativeOrderHistoryIsUnavailable(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000)
	provider := newInstrumentProvider()
	provider.LoadSnapshot([]*model.Instrument{instrumentFromBitget(bitgetsdk.Instrument{
		Category:  bitgetsdk.ProductTypeUSDTFutures,
		Symbol:    "BTCUSDT",
		BaseCoin:  "BTC",
		QuoteCoin: "USDT",
		Status:    "online",
	})})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v3/trade/unfilled-orders":
			writeJSON(t, w, map[string]any{"code": "00000", "msg": "success", "data": map[string]any{"list": []any{}, "cursor": ""}})
		case "/api/v3/trade/fills":
			writeJSON(t, w, map[string]any{"code": "00000", "msg": "success", "data": map[string]any{"list": []any{
				bitgetDerivativeFillFixture("fill-1", "order-1", "client-1", now.Add(-time.Hour)),
			}, "cursor": ""}})
		case "/api/v3/trade/history-orders":
			writeJSON(t, w, map[string]any{"code": "40000", "msg": "history temporarily unavailable", "data": map[string]any{"list": []any{}, "cursor": ""}})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.Error(w, "unexpected path", http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	exec := newExecutionClient(
		bitgetsdk.NewClient().WithCredentials("key", "secret", "pass").WithBaseURL(server.URL).WithHTTPClient(server.Client()),
		provider,
		clock.NewSimulatedClock(now),
	)
	mass, err := exec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{AccountID: AccountIDUnified, IncludeFills: true})
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus must retain exact fallback after batch history failure: %v", err)
	}
	if len(mass.FillReports["order-1"]) != 1 || len(mass.OrderReports) != 0 {
		t.Fatalf("fallback mass=%+v, want fill retained and unresolved order absent", mass)
	}
	if !bitgetHasWarning(mass.Warnings, "ORDER_HISTORY_PREFETCH_FAILED") {
		t.Fatalf("warnings=%+v, want explicit batch-prefetch failure", mass.Warnings)
	}
}

func TestBitgetExecutionMassStatusPropagatesDerivativeOrderHistoryCancellation(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000)
	provider := newInstrumentProvider()
	provider.LoadSnapshot([]*model.Instrument{instrumentFromBitget(bitgetsdk.Instrument{
		Category:  bitgetsdk.ProductTypeUSDTFutures,
		Symbol:    "BTCUSDT",
		BaseCoin:  "BTC",
		QuoteCoin: "USDT",
		Status:    "online",
	})})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v3/trade/unfilled-orders":
			writeJSON(t, w, map[string]any{"code": "00000", "msg": "success", "data": map[string]any{"list": []any{}, "cursor": ""}})
		case "/api/v3/trade/fills":
			writeJSON(t, w, map[string]any{"code": "00000", "msg": "success", "data": map[string]any{"list": []any{
				bitgetDerivativeFillFixture("fill-1", "order-1", "client-1", now.Add(-time.Hour)),
			}, "cursor": ""}})
		case "/api/v3/trade/history-orders":
			<-r.Context().Done()
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.Error(w, "unexpected path", http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	exec := newExecutionClient(
		bitgetsdk.NewClient().WithCredentials("key", "secret", "pass").WithBaseURL(server.URL).WithHTTPClient(server.Client()),
		provider,
		clock.NewSimulatedClock(now),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := exec.GenerateExecutionMassStatus(ctx, model.MassStatusQuery{AccountID: AccountIDUnified, IncludeFills: true})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("GenerateExecutionMassStatus error=%v, want context deadline exceeded", err)
	}
}

func TestBitgetExecutionMassStatusQueriesEachFillCategoryOnce(t *testing.T) {
	provider := newInstrumentProvider()
	provider.LoadSnapshot([]*model.Instrument{
		instrumentFromBitget(bitgetsdk.Instrument{Category: "SPOT", Symbol: "ETHUSDT", BaseCoin: "ETH", QuoteCoin: "USDT", Status: "online"}),
		instrumentFromBitget(bitgetsdk.Instrument{Category: "SPOT", Symbol: "SOLUSDT", BaseCoin: "SOL", QuoteCoin: "USDT", Status: "online"}),
	})
	var fillCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v3/trade/unfilled-orders":
			writeJSON(t, w, map[string]any{"code": "00000", "msg": "success", "data": map[string]any{"list": []any{}}})
		case "/api/v3/trade/fills":
			fillCalls.Add(1)
			writeJSON(t, w, map[string]any{"code": "00000", "msg": "success", "data": map[string]any{"list": []any{
				bitgetFillFixtureForSymbol("eth-fill", "eth-order", "client", "ETHUSDT", time.UnixMilli(1_700_000_000_000)),
				bitgetFillFixtureForSymbol("sol-fill", "sol-order", "client", "SOLUSDT", time.UnixMilli(1_700_000_000_001)),
			}}})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.Error(w, "unexpected path", http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	exec := newExecutionClient(
		bitgetsdk.NewClient().WithCredentials("key", "secret", "pass").WithBaseURL(server.URL).WithHTTPClient(server.Client()),
		provider,
		clock.NewRealClock(),
	)
	mass, err := exec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{AccountID: AccountIDUnified, IncludeFills: true})
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	if got := fillCalls.Load(); got != 1 {
		t.Fatalf("SPOT fill-history calls=%d, want one category-wide call", got)
	}
	if len(mass.FillReports) != 2 || len(mass.FillReports["eth-order"]) != 1 || len(mass.FillReports["sol-order"]) != 1 {
		t.Fatalf("category-wide fills were lost or duplicated: %+v", mass.FillReports)
	}
	if got := mass.FillReports["eth-order"][0].Fill.InstrumentID.Symbol; got != "ETH-USDT" {
		t.Fatalf("ETH fill instrument=%q, want ETH-USDT", got)
	}
	if got := mass.FillReports["sol-order"][0].Fill.InstrumentID.Symbol; got != "SOL-USDT" {
		t.Fatalf("SOL fill instrument=%q, want SOL-USDT", got)
	}
}

func TestBitgetExecutionMassStatusPaginatesFillHistoryWithoutFalsePartial(t *testing.T) {
	const fillPageLimit = 100
	var fillCalls atomic.Int32
	provider := newInstrumentProvider()
	provider.LoadSnapshot([]*model.Instrument{
		instrumentFromBitget(bitgetsdk.Instrument{Category: "SPOT", Symbol: "ETHUSDT", BaseCoin: "ETH", QuoteCoin: "USDT", Status: "online"}),
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v3/trade/unfilled-orders":
			writeJSON(t, w, map[string]any{"code": "00000", "msg": "success", "data": map[string]any{"list": []any{}}})
		case "/api/v3/trade/fills":
			fillCalls.Add(1)
			if r.URL.Query().Get("cursor") == "next" {
				writeJSON(t, w, map[string]any{"code": "00000", "msg": "success", "data": map[string]any{
					"list":   []any{bitgetFillFixture("last", "last-order", "keep-client", time.UnixMilli(1_700_000_001_000))},
					"cursor": "",
				}})
				return
			}
			records := make([]any, 0, fillPageLimit)
			records = append(records, bitgetFillFixture("keep", "keep-order", "keep-client", time.UnixMilli(1_700_000_000_000)))
			for i := 1; i < fillPageLimit; i++ {
				records = append(records, bitgetFillFixture("other-"+strconv.Itoa(i), "other-order-"+strconv.Itoa(i), "other-client", time.UnixMilli(1_700_000_000_000+int64(i))))
			}
			writeJSON(t, w, map[string]any{"code": "00000", "msg": "success", "data": map[string]any{"list": records, "cursor": "next"}})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.Error(w, "unexpected path", http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	exec := newExecutionClient(
		bitgetsdk.NewClient().WithCredentials("key", "secret", "pass").WithBaseURL(server.URL).WithHTTPClient(server.Client()),
		provider,
		clock.NewRealClock(),
	)
	mass, err := exec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{
		AccountID:    AccountIDUnified,
		ClientID:     "keep-client",
		IncludeFills: true,
	})
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	if len(mass.FillReports) != 2 {
		t.Fatalf("filtered fill report groups=%d, want first and continuation-page matches", len(mass.FillReports))
	}
	if bitgetHasWarning(mass.Warnings, "FILL_REPORTS_LIMIT_REACHED") {
		t.Fatalf("warnings=%+v, complete cursor traversal must not report saturation", mass.Warnings)
	}
	if fillCalls.Load() != 2 {
		t.Fatalf("fill calls=%d, want initial and continuation pages", fillCalls.Load())
	}
}

func TestBitgetGenerateFillReportsFailsClosedOnUnknownVenueSymbol(t *testing.T) {
	provider := newInstrumentProvider()
	spot := instrumentFromBitget(bitgetsdk.Instrument{
		Category: "SPOT", Symbol: "ETHUSDT", BaseCoin: "ETH", QuoteCoin: "USDT", Status: "online",
	})
	provider.LoadSnapshot([]*model.Instrument{spot})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/trade/fills" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		writeJSON(t, w, map[string]any{"code": "00000", "msg": "success", "data": map[string]any{"list": []any{
			bitgetFillFixtureForSymbol("unknown-fill", "unknown-order", "client", "NEWUSDT", time.UnixMilli(1_700_000_000_000)),
		}}})
	}))
	t.Cleanup(server.Close)

	exec := newExecutionClient(
		bitgetsdk.NewClient().WithCredentials("key", "secret", "pass").WithBaseURL(server.URL).WithHTTPClient(server.Client()),
		provider,
		clock.NewRealClock(),
	)
	_, err := exec.GenerateFillReports(context.Background(), model.FillReportQuery{
		AccountID:    AccountIDUnified,
		InstrumentID: spot.ID,
	})
	if err == nil || !strings.Contains(err.Error(), "unknown fill instrument") {
		t.Fatalf("GenerateFillReports error=%v, want unknown fill instrument", err)
	}
}

func bitgetFillFixture(execID, orderID, clientID string, timestamp time.Time) map[string]any {
	return bitgetFillFixtureForSymbol(execID, orderID, clientID, "ETHUSDT", timestamp)
}

func bitgetFillFixtureForSymbol(execID, orderID, clientID, symbol string, timestamp time.Time) map[string]any {
	return map[string]any{
		"category":  "SPOT",
		"execId":    execID,
		"orderId":   orderID,
		"clientOid": clientID,
		"symbol":    symbol,
		"side":      "buy",
		"execPrice": "1000",
		"execQty":   "0.01",
		"execTime":  strconv.FormatInt(timestamp.UnixMilli(), 10),
	}
}

func bitgetDerivativeFillFixture(execID, orderID, clientID string, timestamp time.Time) map[string]any {
	return map[string]any{
		"category":  bitgetsdk.ProductTypeUSDTFutures,
		"execId":    execID,
		"orderId":   orderID,
		"clientOid": clientID,
		"symbol":    "BTCUSDT",
		"side":      "buy",
		"execPrice": "50000",
		"execQty":   "0.01",
		"execTime":  strconv.FormatInt(timestamp.UnixMilli(), 10),
	}
}

func bitgetOrderFixture(orderID, clientID, posSide, status string) map[string]any {
	return map[string]any{
		"orderId":     orderID,
		"clientOid":   clientID,
		"category":    bitgetsdk.ProductTypeUSDTFutures,
		"symbol":      "BTCUSDT",
		"side":        "buy",
		"orderType":   "limit",
		"timeInForce": "gtc",
		"price":       "50000",
		"qty":         "0.01",
		"filledQty":   "0.01",
		"avgPrice":    "50000",
		"orderStatus": status,
		"holdMode":    "hedge_mode",
		"posSide":     posSide,
		"cTime":       "1799990000000",
		"uTime":       "1800000000000",
	}
}

func bitgetHasWarning(warnings []model.ReportWarning, code string) bool {
	for _, warning := range warnings {
		if warning.Code == code {
			return true
		}
	}
	return false
}
