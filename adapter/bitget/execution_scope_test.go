package bitget

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	bitgetsdk "github.com/QuantProcessing/boltertrader/sdk/bitget"
)

func TestBitgetUnscopedOrderReportsFanOutConfiguredCategoriesAndResolveExactInstrument(t *testing.T) {
	provider, ids := bitgetExecutionScopeProvider()
	var mu sync.Mutex
	calls := make(map[string]int)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/trade/unfilled-orders" {
			t.Errorf("unexpected path %s", r.URL.Path)
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		category := r.URL.Query().Get("category")
		mu.Lock()
		calls[category]++
		mu.Unlock()
		record := bitgetScopedOrderFixture(category)
		writeJSON(t, w, map[string]any{
			"code": "00000",
			"msg":  "success",
			"data": map[string]any{"list": []any{record}},
		})
	}))
	t.Cleanup(server.Close)

	exec := newExecutionClient(
		bitgetsdk.NewClient().WithCredentials("key", "secret", "pass").WithBaseURL(server.URL).WithHTTPClient(server.Client()),
		provider,
		clock.NewRealClock(),
	)
	reports, err := exec.GenerateOrderStatusReports(context.Background(), model.OrderStatusReportQuery{})
	if err != nil {
		t.Fatalf("GenerateOrderStatusReports: %v", err)
	}
	if len(reports) != 3 {
		t.Fatalf("unscoped order reports=%+v, want all three configured categories", reports)
	}
	wantByOrder := map[string]model.InstrumentID{
		"spot-order": ids.spot,
		"usdt-order": ids.usdt,
		"usdc-order": ids.usdc,
	}
	for _, report := range reports {
		want, ok := wantByOrder[report.Order.VenueOrderID]
		if !ok {
			t.Fatalf("unexpected order report %+v", report)
		}
		if got := report.Order.Request.InstrumentID; got != want {
			t.Fatalf("order %s instrument=%s, want exact category identity %s", report.Order.VenueOrderID, got, want)
		}
	}
	assertBitgetCategoryCalls(t, calls, map[string]int{
		"SPOT":                           1,
		bitgetsdk.ProductTypeUSDTFutures: 1,
		bitgetsdk.ProductTypeUSDCFutures: 1,
	})

	mu.Lock()
	clear(calls)
	mu.Unlock()
	scoped, err := exec.GenerateOrderStatusReports(context.Background(), model.OrderStatusReportQuery{InstrumentID: ids.usdc})
	if err != nil {
		t.Fatalf("GenerateOrderStatusReports scoped USDC: %v", err)
	}
	if len(scoped) != 1 || scoped[0].Order.Request.InstrumentID != ids.usdc {
		t.Fatalf("scoped USDC reports=%+v, want one exact report", scoped)
	}
	assertBitgetCategoryCalls(t, calls, map[string]int{bitgetsdk.ProductTypeUSDCFutures: 1})
}

func TestBitgetUnscopedSingleOrderReportFansOutAndRejectsIdentityConflict(t *testing.T) {
	provider, ids := bitgetExecutionScopeProvider()
	var conflict atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/trade/unfilled-orders" {
			t.Errorf("unexpected path %s", r.URL.Path)
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		category := r.URL.Query().Get("category")
		var records []any
		if category == bitgetsdk.ProductTypeUSDCFutures || (conflict.Load() && category == "SPOT") {
			record := bitgetScopedOrderFixture(category)
			record["orderId"] = "shared-order"
			record["clientOid"] = "shared-client"
			records = []any{record}
		}
		writeJSON(t, w, map[string]any{"code": "00000", "msg": "success", "data": map[string]any{"list": records}})
	}))
	t.Cleanup(server.Close)
	exec := newExecutionClient(
		bitgetsdk.NewClient().WithCredentials("key", "secret", "pass").WithBaseURL(server.URL).WithHTTPClient(server.Client()),
		provider,
		clock.NewRealClock(),
	)

	report, err := exec.GenerateOrderStatusReport(context.Background(), model.SingleOrderStatusQuery{
		ClientID:     "shared-client",
		VenueOrderID: "shared-order",
	})
	if err != nil {
		t.Fatalf("GenerateOrderStatusReport: %v", err)
	}
	if report == nil || report.Order.Request.InstrumentID != ids.usdc {
		t.Fatalf("unscoped single order=%+v, want exact USDC result", report)
	}

	conflict.Store(true)
	if _, err := exec.GenerateOrderStatusReport(context.Background(), model.SingleOrderStatusQuery{
		ClientID:     "shared-client",
		VenueOrderID: "shared-order",
	}); err == nil {
		t.Fatal("duplicate unscoped order identity across categories returned nil error")
	}
}

func TestBitgetOpenOrdersRejectsMismatchedVenueIdentity(t *testing.T) {
	provider := newInstrumentProvider()
	inst := instrumentFromBitget(bitgetsdk.Instrument{
		Category: bitgetsdk.ProductTypeUSDTFutures, Symbol: "BTCUSDT", BaseCoin: "BTC", QuoteCoin: "USDT", Status: "online",
	})
	provider.LoadSnapshot([]*model.Instrument{inst})
	tests := []struct {
		name     string
		category string
		symbol   string
		wantErr  bool
	}{
		{name: "normalized exact identity", category: " usdt-futures ", symbol: " btcusdt "},
		{name: "missing category", category: "", symbol: "BTCUSDT", wantErr: true},
		{name: "wrong category", category: "SPOT", symbol: "BTCUSDT", wantErr: true},
		{name: "wrong symbol", category: bitgetsdk.ProductTypeUSDTFutures, symbol: "ETHUSDT", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, map[string]any{"code": "00000", "msg": "success", "data": map[string]any{"list": []any{
					map[string]any{"category": tc.category, "symbol": tc.symbol, "orderId": "order", "clientOid": "client", "side": "buy", "orderType": "limit", "qty": "1", "price": "100", "orderStatus": "live", "holdMode": "one_way_mode", "holdSide": "long"},
				}}})
			}))
			t.Cleanup(server.Close)
			exec := newExecutionClient(
				bitgetsdk.NewClient().WithCredentials("key", "secret", "pass").WithBaseURL(server.URL).WithHTTPClient(server.Client()),
				provider,
				clock.NewRealClock(),
			)
			orders, err := exec.OpenOrders(context.Background(), inst.ID)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("OpenOrders=%+v, want identity mismatch error", orders)
				}
				return
			}
			if err != nil || len(orders) != 1 || orders[0].Request.InstrumentID != inst.ID {
				t.Fatalf("OpenOrders=%+v err=%v, want normalized exact identity", orders, err)
			}
		})
	}
}

func TestBitgetUnscopedFillReportsFanOutWithGlobalLimit(t *testing.T) {
	provider, ids := bitgetExecutionScopeProvider()
	now := time.Date(2026, 7, 14, 3, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	calls := make(map[string]int)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/trade/fills" {
			t.Errorf("unexpected path %s", r.URL.Path)
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		category := r.URL.Query().Get("category")
		mu.Lock()
		calls[category]++
		mu.Unlock()
		var records []any
		switch category {
		case "SPOT":
			records = []any{bitgetCategoryFillFixture(category, "spot-fill", "spot-order", "BTCUSDT", now.Add(-3*time.Hour))}
		case bitgetsdk.ProductTypeUSDTFutures:
			records = []any{bitgetCategoryFillFixture(category, "usdt-fill", "usdt-order", "BTCUSDT", now.Add(-2*time.Hour))}
		case bitgetsdk.ProductTypeUSDCFutures:
			records = []any{bitgetCategoryFillFixture(category, "usdc-fill", "usdc-order", "BTCUSDT", now.Add(-time.Hour))}
		default:
			t.Errorf("unexpected category %q", category)
		}
		writeJSON(t, w, map[string]any{"code": "00000", "msg": "success", "data": map[string]any{"list": records}})
	}))
	t.Cleanup(server.Close)

	exec := newExecutionClient(
		bitgetsdk.NewClient().WithCredentials("key", "secret", "pass").WithBaseURL(server.URL).WithHTTPClient(server.Client()),
		provider,
		clock.NewSimulatedClock(now),
	)
	reports, err := exec.GenerateFillReports(context.Background(), model.FillReportQuery{Limit: 2})
	if err != nil {
		t.Fatalf("GenerateFillReports: %v", err)
	}
	if len(reports) != 2 {
		t.Fatalf("global-limit fill reports=%+v, want two newest across categories", reports)
	}
	if reports[0].Fill.InstrumentID != ids.usdc || reports[1].Fill.InstrumentID != ids.usdt {
		t.Fatalf("global-limit order/instrument resolution=%+v, want newest USDC then USDT", reports)
	}
	assertBitgetCategoryCalls(t, calls, map[string]int{
		"SPOT":                           1,
		bitgetsdk.ProductTypeUSDTFutures: 1,
		bitgetsdk.ProductTypeUSDCFutures: 1,
	})
}

func TestBitgetScopedFillReportsScanPastFilteredRawRows(t *testing.T) {
	provider := newInstrumentProvider()
	btc := instrumentFromBitget(bitgetsdk.Instrument{Category: "SPOT", Symbol: "BTCUSDT", BaseCoin: "BTC", QuoteCoin: "USDT", Status: "online"})
	eth := instrumentFromBitget(bitgetsdk.Instrument{Category: "SPOT", Symbol: "ETHUSDT", BaseCoin: "ETH", QuoteCoin: "USDT", Status: "online"})
	provider.LoadSnapshot([]*model.Instrument{btc, eth})
	now := time.Date(2026, 7, 14, 3, 30, 0, 0, time.UTC)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		data := map[string]any{
			"list":   []any{bitgetCategoryFillFixture("SPOT", "btc-fill", "btc-order", "BTCUSDT", now.Add(-time.Minute))},
			"cursor": "next",
		}
		if call == 2 {
			data = map[string]any{
				"list":   []any{bitgetCategoryFillFixture("SPOT", "eth-fill", "eth-order", "ETHUSDT", now.Add(-2*time.Minute))},
				"cursor": "",
			}
		}
		writeJSON(t, w, map[string]any{"code": "00000", "msg": "success", "data": data})
	}))
	t.Cleanup(server.Close)

	exec := newExecutionClient(
		bitgetsdk.NewClient().WithCredentials("key", "secret", "pass").WithBaseURL(server.URL).WithHTTPClient(server.Client()),
		provider,
		clock.NewSimulatedClock(now),
	)
	reports, err := exec.GenerateFillReports(context.Background(), model.FillReportQuery{InstrumentID: eth.ID, Limit: 1})
	if err != nil {
		t.Fatalf("GenerateFillReports: %v", err)
	}
	if len(reports) != 1 || reports[0].Fill.TradeID != "eth-fill" || reports[0].Fill.InstrumentID != eth.ID {
		t.Fatalf("reports=%+v, want filtered continuation-page ETH fill", reports)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("fill page calls=%d, want continuation scan after filtered raw row", got)
	}
}

func TestBitgetDirectFillReportsFailClosedWhenRawScanSaturatesBeforeMatch(t *testing.T) {
	provider := newInstrumentProvider()
	btc := instrumentFromBitget(bitgetsdk.Instrument{Category: "SPOT", Symbol: "BTCUSDT", BaseCoin: "BTC", QuoteCoin: "USDT", Status: "online"})
	eth := instrumentFromBitget(bitgetsdk.Instrument{Category: "SPOT", Symbol: "ETHUSDT", BaseCoin: "ETH", QuoteCoin: "USDT", Status: "online"})
	provider.LoadSnapshot([]*model.Instrument{btc, eth})
	now := time.Date(2026, 7, 14, 3, 45, 0, 0, time.UTC)
	records := make([]any, executionMassStatusFillLimit)
	for i := range records {
		records[i] = bitgetCategoryFillFixture("SPOT", "btc-fill-"+strconv.Itoa(i), "btc-order", "BTCUSDT", now.Add(-time.Duration(i)*time.Second))
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"code": "00000", "msg": "success", "data": map[string]any{"list": records, "cursor": "more"}})
	}))
	t.Cleanup(server.Close)

	exec := newExecutionClient(
		bitgetsdk.NewClient().WithCredentials("key", "secret", "pass").WithBaseURL(server.URL).WithHTTPClient(server.Client()),
		provider,
		clock.NewSimulatedClock(now),
	)
	reports, err := exec.GenerateFillReports(context.Background(), model.FillReportQuery{InstrumentID: eth.ID, Limit: 1})
	if err == nil {
		t.Fatalf("reports=%+v, want incomplete filtered history error", reports)
	}
}

func TestBitgetDirectFillReportsRejectPartitionSaturationDespiteGlobalMatchCount(t *testing.T) {
	provider := newInstrumentProvider()
	spot := instrumentFromBitget(bitgetsdk.Instrument{Category: "SPOT", Symbol: "ETHUSDT", BaseCoin: "ETH", QuoteCoin: "USDT", Status: "online"})
	perp := instrumentFromBitget(bitgetsdk.Instrument{Category: bitgetsdk.ProductTypeUSDTFutures, Symbol: "BTCUSDT", BaseCoin: "BTC", QuoteCoin: "USDT", Status: "online"})
	provider.LoadSnapshot([]*model.Instrument{spot, perp})
	now := time.Date(2026, 7, 14, 3, 50, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		category := r.URL.Query().Get("category")
		if category == bitgetsdk.ProductTypeUSDTFutures {
			writeJSON(t, w, map[string]any{"code": "00000", "msg": "success", "data": map[string]any{
				"list": []any{bitgetCategoryFillFixtureWithClient(category, "perp-keep", "perp-order", "keep", "BTCUSDT", now.Add(-2*time.Hour))},
			}})
			return
		}
		page := 0
		if cursor := r.URL.Query().Get("cursor"); cursor != "" {
			parsed, err := strconv.Atoi(cursor)
			if err != nil {
				t.Fatalf("unexpected spot cursor %q", cursor)
			}
			page = parsed
		}
		records := make([]any, 100)
		for i := range records {
			clientID := "other"
			if page == 0 && i == 0 {
				clientID = "keep"
			}
			records[i] = bitgetCategoryFillFixtureWithClient("SPOT", "spot-"+strconv.Itoa(page)+"-"+strconv.Itoa(i), "spot-order", clientID, "ETHUSDT", now.Add(-time.Duration(page*100+i)*time.Second))
		}
		next := "more"
		if page < 9 {
			next = strconv.Itoa(page + 1)
		}
		writeJSON(t, w, map[string]any{"code": "00000", "msg": "success", "data": map[string]any{"list": records, "cursor": next}})
	}))
	t.Cleanup(server.Close)

	exec := newExecutionClient(
		bitgetsdk.NewClient().WithCredentials("key", "secret", "pass").WithBaseURL(server.URL).WithHTTPClient(server.Client()),
		provider,
		clock.NewSimulatedClock(now),
	)
	reports, err := exec.GenerateFillReports(context.Background(), model.FillReportQuery{ClientID: "keep", Limit: 2})
	if err == nil {
		t.Fatalf("reports=%+v, want saturated Spot partition to fail closed even though USDT supplies the second global match", reports)
	}
}

func TestBitgetScopedFillReportsSkipOtherSymbolsInConfiguredCategory(t *testing.T) {
	provider := newInstrumentProvider()
	eth := instrumentFromBitget(bitgetsdk.Instrument{Category: "SPOT", Symbol: "ETHUSDT", BaseCoin: "ETH", QuoteCoin: "USDT", Status: "online"})
	sol := instrumentFromBitget(bitgetsdk.Instrument{Category: "SPOT", Symbol: "SOLUSDT", BaseCoin: "SOL", QuoteCoin: "USDT", Status: "online"})
	provider.LoadSnapshot([]*model.Instrument{eth, sol})
	now := time.Date(2026, 7, 14, 3, 30, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{"code": "00000", "msg": "success", "data": map[string]any{"list": []any{
			bitgetCategoryFillFixture(" spot ", "sol-fill", "sol-order", "SOLUSDT", now.Add(-time.Minute)),
			bitgetCategoryFillFixture(" spot ", "eth-fill", "eth-order", " ethusdt ", now.Add(-2*time.Minute)),
		}}})
	}))
	t.Cleanup(server.Close)
	exec := newExecutionClient(
		bitgetsdk.NewClient().WithCredentials("key", "secret", "pass").WithBaseURL(server.URL).WithHTTPClient(server.Client()),
		provider,
		clock.NewSimulatedClock(now),
	)
	reports, err := exec.GenerateFillReports(context.Background(), model.FillReportQuery{InstrumentID: eth.ID})
	if err != nil {
		t.Fatalf("GenerateFillReports scoped ETH: %v", err)
	}
	if len(reports) != 1 || reports[0].Fill.InstrumentID != eth.ID || reports[0].Fill.TradeID != "eth-fill" {
		t.Fatalf("scoped ETH reports=%+v, want only ETH fill", reports)
	}
}

func TestBitgetFillReportsSplitThirtyDayWindowsDeduplicateAndRejectBeyondHistory(t *testing.T) {
	provider := newInstrumentProvider()
	provider.LoadSnapshot([]*model.Instrument{instrumentFromBitget(bitgetsdk.Instrument{
		Category: "SPOT", Symbol: "ETHUSDT", BaseCoin: "ETH", QuoteCoin: "USDT", Status: "online",
	})})
	now := time.Date(2026, 7, 14, 4, 0, 0, 0, time.UTC)
	since := now.Add(-60 * 24 * time.Hour)
	boundary := now.Add(-30 * 24 * time.Hour)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/trade/fills" {
			t.Errorf("unexpected path %s", r.URL.Path)
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		calls.Add(1)
		startMillis, err := strconv.ParseInt(r.URL.Query().Get("startTime"), 10, 64)
		if err != nil {
			t.Errorf("invalid startTime: %v", err)
		}
		endMillis, err := strconv.ParseInt(r.URL.Query().Get("endTime"), 10, 64)
		if err != nil {
			t.Errorf("invalid endTime: %v", err)
		}
		start := time.UnixMilli(startMillis)
		end := time.UnixMilli(endMillis)
		if end.Sub(start) > 30*24*time.Hour {
			t.Errorf("fill window=%s, exceeds 30 days", end.Sub(start))
		}
		records := []any{bitgetCategoryFillFixture("SPOT", "boundary", "boundary-order", "ETHUSDT", boundary)}
		if end.Equal(now) {
			records = append(records, bitgetCategoryFillFixture("SPOT", "newer", "newer-order", "ETHUSDT", now.Add(-time.Hour)))
		} else {
			records = append(records, bitgetCategoryFillFixture("SPOT", "older", "older-order", "ETHUSDT", since.Add(time.Hour)))
		}
		writeJSON(t, w, map[string]any{"code": "00000", "msg": "success", "data": map[string]any{"list": records}})
	}))
	t.Cleanup(server.Close)

	exec := newExecutionClient(
		bitgetsdk.NewClient().WithCredentials("key", "secret", "pass").WithBaseURL(server.URL).WithHTTPClient(server.Client()),
		provider,
		clock.NewSimulatedClock(now),
	)
	reports, err := exec.GenerateFillReports(context.Background(), model.FillReportQuery{Since: since, Until: now, Limit: 10})
	if err != nil {
		t.Fatalf("GenerateFillReports: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("fill window calls=%d, want two 30-day requests", got)
	}
	if len(reports) != 3 {
		t.Fatalf("deduplicated reports=%+v, want boundary/newer/older exactly once", reports)
	}

	before := calls.Load()
	_, err = exec.GenerateFillReports(context.Background(), model.FillReportQuery{Since: now.Add(-91 * 24 * time.Hour), Until: now})
	if err == nil {
		t.Fatal("GenerateFillReports beyond Bitget's 90-day history returned nil error")
	}
	if got := calls.Load(); got != before {
		t.Fatalf("unsupported history query crossed HTTP boundary: calls=%d, want %d", got, before)
	}
}

func TestBitgetFillReportsStopAfterNewerWindowsSatisfyLimit(t *testing.T) {
	provider := newInstrumentProvider()
	btc := instrumentFromBitget(bitgetsdk.Instrument{Category: "SPOT", Symbol: "BTCUSDT", BaseCoin: "BTC", QuoteCoin: "USDT", Status: "online"})
	eth := instrumentFromBitget(bitgetsdk.Instrument{Category: "SPOT", Symbol: "ETHUSDT", BaseCoin: "ETH", QuoteCoin: "USDT", Status: "online"})
	provider.LoadSnapshot([]*model.Instrument{btc, eth})
	now := time.Date(2026, 7, 14, 4, 15, 0, 0, time.UTC)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		if call == 1 {
			writeJSON(t, w, map[string]any{"code": "00000", "msg": "success", "data": map[string]any{"list": []any{
				bitgetCategoryFillFixture("SPOT", "newest-eth", "eth-order", "ETHUSDT", now.Add(-time.Hour)),
			}}})
			return
		}
		writeJSON(t, w, map[string]any{"code": "00000", "msg": "success", "data": map[string]any{
			"list":   bitgetSaturatedOtherSymbolFillRows(now.Add(-31*24*time.Hour), "BTCUSDT"),
			"cursor": "more",
		}})
	}))
	t.Cleanup(server.Close)

	exec := newExecutionClient(
		bitgetsdk.NewClient().WithCredentials("key", "secret", "pass").WithBaseURL(server.URL).WithHTTPClient(server.Client()),
		provider,
		clock.NewSimulatedClock(now),
	)
	reports, limitReached, unsafeIncomplete, err := exec.generateFillReports(context.Background(), model.FillReportQuery{
		InstrumentID: eth.ID,
		Since:        now.Add(-60 * 24 * time.Hour),
		Until:        now,
		Limit:        1,
	})
	if err != nil {
		t.Fatalf("generateFillReports: %v", err)
	}
	if !limitReached {
		t.Fatal("generateFillReports did not report the global limit after skipping an older window")
	}
	if unsafeIncomplete {
		t.Fatal("generateFillReports marked the satisfied newest-window limit as unsafe")
	}
	if len(reports) != 1 || reports[0].Fill.TradeID != "newest-eth" {
		t.Fatalf("reports=%+v, want newest ETH fill", reports)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("fill window calls=%d, want older windows skipped after newest limit was satisfied", got)
	}
}

func TestBitgetFillReportsFailClosedWhenOlderSaturatedWindowLeavesLimitUnsatisfied(t *testing.T) {
	provider := newInstrumentProvider()
	btc := instrumentFromBitget(bitgetsdk.Instrument{Category: "SPOT", Symbol: "BTCUSDT", BaseCoin: "BTC", QuoteCoin: "USDT", Status: "online"})
	eth := instrumentFromBitget(bitgetsdk.Instrument{Category: "SPOT", Symbol: "ETHUSDT", BaseCoin: "ETH", QuoteCoin: "USDT", Status: "online"})
	provider.LoadSnapshot([]*model.Instrument{btc, eth})
	now := time.Date(2026, 7, 14, 4, 20, 0, 0, time.UTC)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		if call == 1 {
			writeJSON(t, w, map[string]any{"code": "00000", "msg": "success", "data": map[string]any{"list": []any{
				bitgetCategoryFillFixture("SPOT", "newer-btc", "btc-order", "BTCUSDT", now.Add(-time.Hour)),
			}}})
			return
		}
		writeJSON(t, w, map[string]any{"code": "00000", "msg": "success", "data": map[string]any{
			"list":   bitgetSaturatedOtherSymbolFillRows(now.Add(-31*24*time.Hour), "BTCUSDT"),
			"cursor": "more",
		}})
	}))
	t.Cleanup(server.Close)

	exec := newExecutionClient(
		bitgetsdk.NewClient().WithCredentials("key", "secret", "pass").WithBaseURL(server.URL).WithHTTPClient(server.Client()),
		provider,
		clock.NewSimulatedClock(now),
	)
	reports, err := exec.GenerateFillReports(context.Background(), model.FillReportQuery{
		InstrumentID: eth.ID,
		Since:        now.Add(-60 * 24 * time.Hour),
		Until:        now,
		Limit:        1,
	})
	if err == nil {
		t.Fatalf("reports=%+v, want older saturated window to fail closed while target limit remains unsatisfied", reports)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("fill window calls=%d, want newer and saturated older window", got)
	}
}

func TestBitgetFillReportWindowsAcceptMillisecondHistoryFloor(t *testing.T) {
	now := time.Date(2026, 7, 14, 4, 0, 0, 750_000, time.UTC)
	venueNow := time.UnixMilli(now.UnixMilli())
	historyFloor := venueNow.Add(-bitgetFillHistory)
	exec := newExecutionClient(bitgetsdk.NewClient(), newInstrumentProvider(), clock.NewSimulatedClock(now))

	windows, err := exec.fillReportWindows(historyFloor, venueNow)
	if err != nil {
		t.Fatalf("fillReportWindows rejected venue-precision 90-day floor: %v", err)
	}
	if len(windows) != 3 {
		t.Fatalf("windows=%+v, want three 30-day windows", windows)
	}
	if got := windows[len(windows)-1].since; !got.Equal(historyFloor) {
		t.Fatalf("oldest window start=%s, want %s", got, historyFloor)
	}
	for _, window := range windows {
		if window.since.Nanosecond()%int(time.Millisecond) != 0 || window.until.Nanosecond()%int(time.Millisecond) != 0 {
			t.Fatalf("window=%+v, want millisecond-aligned venue boundaries", window)
		}
	}
}

func TestBitgetPositionReportsMassStatusAndCapabilitiesHonorConfiguredScope(t *testing.T) {
	provider := newInstrumentProvider()
	spot := instrumentFromBitget(bitgetsdk.Instrument{Category: "SPOT", Symbol: "ETHUSDT", BaseCoin: "ETH", QuoteCoin: "USDT", Status: "online"})
	usdc := instrumentFromBitget(bitgetsdk.Instrument{Category: bitgetsdk.ProductTypeUSDCFutures, Symbol: "ETHPERP", BaseCoin: "ETH", QuoteCoin: "USDC", Status: "online"})
	provider.LoadSnapshot([]*model.Instrument{spot, usdc})
	var mu sync.Mutex
	calls := make(map[string]int)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		category := r.URL.Query().Get("category")
		mu.Lock()
		calls[r.URL.Path+"|"+category]++
		mu.Unlock()
		switch r.URL.Path {
		case "/api/v3/trade/unfilled-orders":
			writeJSON(t, w, map[string]any{"code": "00000", "msg": "success", "data": map[string]any{"list": []any{}}})
		case "/api/v3/position/current-position":
			if category != bitgetsdk.ProductTypeUSDCFutures {
				t.Errorf("position category=%q, want only configured USDC futures", category)
			}
			writeJSON(t, w, map[string]any{"code": "00000", "msg": "success", "data": map[string]any{"list": []any{
				map[string]any{"category": category, "symbol": "ETHPERP", "posSide": "long", "holdMode": "one_way_mode", "qty": "1", "avgPrice": "100", "markPrice": "101"},
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
	positions, err := exec.GeneratePositionReports(context.Background(), model.PositionReportQuery{})
	if err != nil {
		t.Fatalf("GeneratePositionReports: %v", err)
	}
	if len(positions) != 1 || positions[0].Position.InstrumentID != usdc.ID {
		t.Fatalf("position reports=%+v, want exact configured USDC instrument", positions)
	}
	positionCalls := calls["/api/v3/position/current-position|"+bitgetsdk.ProductTypeUSDCFutures]
	spotPositions, err := exec.GeneratePositionReports(context.Background(), model.PositionReportQuery{InstrumentID: spot.ID})
	if err != nil || len(spotPositions) != 0 {
		t.Fatalf("scoped Spot positions=%+v err=%v, want empty nil", spotPositions, err)
	}
	if got := calls["/api/v3/position/current-position|"+bitgetsdk.ProductTypeUSDCFutures]; got != positionCalls {
		t.Fatalf("scoped Spot position report made derivative request: calls=%d, want %d", got, positionCalls)
	}

	mass, err := exec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{IncludePositions: true})
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	if len(mass.PositionReports) != 1 {
		t.Fatalf("mass position reports=%+v, want configured USDC position", mass.PositionReports)
	}
	caps := exec.Capabilities()
	if !caps.Reports.PositionReports || !bitgetHasProductKind(caps.Products, enums.KindSpot) || !bitgetHasProductKind(caps.Products, enums.KindPerp) {
		t.Fatalf("mixed-scope capabilities=%+v", caps)
	}
	if !caps.Reports.SingleOrderStatus || caps.Reports.OrderHistory {
		t.Fatalf("mixed-scope report capabilities=%+v, want exact single-order status without closed-order history", caps.Reports)
	}

	spotProvider := newInstrumentProvider()
	spotProvider.LoadSnapshot([]*model.Instrument{spot})
	spotOnly := newExecutionClient(nil, spotProvider, clock.NewRealClock())
	spotCaps := spotOnly.Capabilities()
	if spotCaps.Reports.PositionReports || len(spotCaps.Products) != 1 || spotCaps.Products[0].Kind != enums.KindSpot {
		t.Fatalf("spot-only capabilities=%+v, want no derivative position reports", spotCaps)
	}
	if !spotCaps.Reports.SingleOrderStatus || spotCaps.Reports.OrderHistory {
		t.Fatalf("spot-only report capabilities=%+v, want exact single-order status without closed-order history", spotCaps.Reports)
	}
}

func TestBitgetPositionReportsFailClosedForKnownInvalidQuantity(t *testing.T) {
	tests := []struct {
		name     string
		quantity map[string]any
	}{
		{name: "malformed quantity", quantity: map[string]any{"qty": "not-a-number"}},
		{name: "missing quantity", quantity: map[string]any{"qty": "", "total": "", "size": ""}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := bitgetAccountScopeProvider(bitgetsdk.ProductTypeUSDTFutures)
			record := map[string]any{
				"symbol":   "BTCUSDT",
				"category": bitgetsdk.ProductTypeUSDTFutures,
				"posSide":  "long",
			}
			for key, value := range tt.quantity {
				record[key] = value
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeJSON(t, w, map[string]any{"code": "00000", "msg": "success", "data": map[string]any{"list": []any{record}}})
			}))
			defer server.Close()

			exec := newExecutionClient(
				bitgetsdk.NewClient().WithCredentials("key", "secret", "pass").WithBaseURL(server.URL).WithHTTPClient(server.Client()),
				provider,
				clock.NewRealClock(),
			)
			reports, err := exec.GeneratePositionReports(context.Background(), model.PositionReportQuery{})
			if err == nil || reports != nil {
				t.Fatalf("GeneratePositionReports returned reports=%+v err=%v, want fail-closed nil snapshot", reports, err)
			}
			for _, want := range []string{"quantity", "BTCUSDT"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("GeneratePositionReports error=%q, want context %q", err, want)
				}
			}
		})
	}
}

type bitgetExecutionScopeIDs struct {
	spot model.InstrumentID
	usdt model.InstrumentID
	usdc model.InstrumentID
}

func bitgetExecutionScopeProvider() (*instrumentProvider, bitgetExecutionScopeIDs) {
	spot := instrumentFromBitget(bitgetsdk.Instrument{Category: "SPOT", Symbol: "BTCUSDT", BaseCoin: "BTC", QuoteCoin: "USDT", Status: "online"})
	usdt := instrumentFromBitget(bitgetsdk.Instrument{Category: bitgetsdk.ProductTypeUSDTFutures, Symbol: "BTCUSDT", BaseCoin: "BTC", QuoteCoin: "USDT", Status: "online"})
	usdc := instrumentFromBitget(bitgetsdk.Instrument{Category: bitgetsdk.ProductTypeUSDCFutures, Symbol: "BTCUSDT", BaseCoin: "BTC", QuoteCoin: "USDC", Status: "online"})
	provider := newInstrumentProvider()
	provider.LoadSnapshot([]*model.Instrument{spot, usdt, usdc})
	return provider, bitgetExecutionScopeIDs{spot: spot.ID, usdt: usdt.ID, usdc: usdc.ID}
}

func bitgetScopedOrderFixture(category string) map[string]any {
	orderID := map[string]string{
		"SPOT":                           "spot-order",
		bitgetsdk.ProductTypeUSDTFutures: "usdt-order",
		bitgetsdk.ProductTypeUSDCFutures: "usdc-order",
	}[category]
	record := map[string]any{
		"category": category, "orderId": orderID, "clientOid": orderID + "-client", "symbol": "BTCUSDT",
		"side": "buy", "orderType": "limit", "timeInForce": "gtc", "qty": "1", "price": "100", "orderStatus": "live",
	}
	if category != "SPOT" {
		record["holdMode"] = "one_way_mode"
		record["holdSide"] = "long"
	}
	return record
}

func bitgetCategoryFillFixture(category, execID, orderID, symbol string, timestamp time.Time) map[string]any {
	return bitgetCategoryFillFixtureWithClient(category, execID, orderID, "client", symbol, timestamp)
}

func bitgetCategoryFillFixtureWithClient(category, execID, orderID, clientID, symbol string, timestamp time.Time) map[string]any {
	return map[string]any{
		"category": category, "execId": execID, "orderId": orderID, "clientOid": clientID, "symbol": symbol,
		"side": "buy", "execPrice": "100", "execQty": "1", "execTime": strconv.FormatInt(timestamp.UnixMilli(), 10),
	}
}

func bitgetSaturatedOtherSymbolFillRows(start time.Time, symbol string) []any {
	records := make([]any, executionMassStatusFillLimit)
	for i := range records {
		records[i] = bitgetCategoryFillFixture("SPOT", "other-"+strconv.Itoa(i), "other-order", symbol, start.Add(-time.Duration(i)*time.Second))
	}
	return records
}

func assertBitgetCategoryCalls(t *testing.T, got, want map[string]int) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("category calls=%v, want %v", got, want)
	}
	for category, wantCount := range want {
		if got[category] != wantCount {
			t.Fatalf("category %s calls=%d, want %d (all=%v)", category, got[category], wantCount, got)
		}
	}
}

func bitgetHasProductKind(products []contract.ProductCapability, kind enums.InstrumentKind) bool {
	for _, product := range products {
		if product.Kind == kind {
			return true
		}
	}
	return false
}
