package bybit

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	bybitsdk "github.com/QuantProcessing/boltertrader/sdk/bybit"
)

func TestBybitExecutionMassStatusPaginatesFillHistoryWithoutFalsePartial(t *testing.T) {
	const fillLimit = 100
	since := time.UnixMilli(1_700_000_000_000)
	until := since.Add(2 * time.Minute)
	var spotCalls atomic.Int32
	var linearCalls atomic.Int32
	var cursorCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v5/order/realtime":
			writeJSON(t, w, map[string]any{"retCode": 0, "retMsg": "OK", "result": map[string]any{"list": []any{}, "nextPageCursor": ""}})
		case "/v5/order/history":
			writeJSON(t, w, map[string]any{"retCode": 0, "retMsg": "OK", "result": map[string]any{"list": []any{}, "nextPageCursor": ""}})
		case "/v5/execution/list":
			category := r.URL.Query().Get("category")
			switch category {
			case "spot":
				spotCalls.Add(1)
			case "linear":
				linearCalls.Add(1)
			default:
				t.Errorf("execution category=%q, want spot or linear", category)
			}
			if got := r.URL.Query().Get("startTime"); got != strconv.FormatInt(since.UnixMilli(), 10) {
				t.Errorf("execution startTime=%q, want %d", got, since.UnixMilli())
			}
			if got := r.URL.Query().Get("endTime"); got != strconv.FormatInt(until.UnixMilli(), 10) {
				t.Errorf("execution endTime=%q, want %d", got, until.UnixMilli())
			}
			if got := r.URL.Query().Get("limit"); got != strconv.Itoa(fillLimit) {
				t.Errorf("execution limit=%q, want %d", got, fillLimit)
			}
			if r.URL.Query().Get("cursor") != "" {
				cursorCalls.Add(1)
				writeJSON(t, w, map[string]any{"retCode": 0, "retMsg": "OK", "result": map[string]any{"list": []any{}, "nextPageCursor": ""}})
				return
			}
			if category == "spot" {
				writeJSON(t, w, map[string]any{"retCode": 0, "retMsg": "OK", "result": map[string]any{
					"list":           []any{bybitExecutionFixture("spot-exec", "spot-order", "ETHUSDT", since.Add(time.Second))},
					"nextPageCursor": "",
				}})
				return
			}
			records := make([]any, 0, fillLimit)
			for i := 0; i < fillLimit; i++ {
				records = append(records, bybitExecutionFixture("exec-"+strconv.Itoa(i), "order-"+strconv.Itoa(i), "BTCUSDT", since.Add(time.Duration(i)*time.Millisecond)))
			}
			writeJSON(t, w, map[string]any{"retCode": 0, "retMsg": "OK", "result": map[string]any{"list": records, "nextPageCursor": "more"}})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.Error(w, "unexpected path", http.StatusNotFound)
		}
	}))
	defer server.Close()

	exec := newExecutionClient(
		bybitsdk.NewClient().WithCredentials("key", "secret").WithBaseURL(server.URL).WithHTTPClient(server.Client()),
		bybitTestProvider(),
		clock.NewSimulatedClock(until.Add(time.Minute)),
	)
	mass, err := exec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{
		AccountID:    AccountIDUnified,
		Since:        since,
		Until:        until,
		IncludeFills: true,
	})
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	if len(mass.FillReports) != fillLimit+1 {
		t.Fatalf("fill reports=%d, want one spot fill plus bounded linear page of %d", len(mass.FillReports), fillLimit)
	}
	if bybitHasWarning(mass.Warnings, "FILL_REPORTS_LIMIT_REACHED") {
		t.Fatalf("warnings=%+v, complete cursor traversal must not report saturation", mass.Warnings)
	}
	if mass.Partial {
		t.Fatal("mass status must be complete when the next cursor resolves below the hard limit")
	}
	if got := spotCalls.Load(); got != 1 {
		t.Fatalf("spot execution calls=%d, want exactly 1", got)
	}
	if got := linearCalls.Load(); got != 2 {
		t.Fatalf("linear execution calls=%d, want initial page plus one continuation", got)
	}
	if got := cursorCalls.Load(); got != 1 {
		t.Fatalf("execution cursor calls=%d, want one continuation page", got)
	}
}

func TestBybitExecutionMassStatusBatchesDerivativeOrderHistoryByInstrument(t *testing.T) {
	until := time.UnixMilli(1_700_000_000_000)
	start := until.Add(-7 * 24 * time.Hour)
	historyCalls := make(map[string]int)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v5/order/realtime":
			list := []any{}
			if r.URL.Query().Get("settleCoin") == bybitsdk.SettleCoinUSDT {
				list = append(list, bybitOrderFixture("realtime-order", "stale-realtime-client", "BTCUSDT", "Buy", 1, "New"))
			}
			writeJSON(t, w, map[string]any{"retCode": 0, "retMsg": "OK", "result": map[string]any{"list": list, "nextPageCursor": ""}})
		case "/v5/execution/list":
			if got := r.URL.Query().Get("category"); got != "linear" {
				t.Errorf("execution category=%q, want linear", got)
			}
			fill := func(execID, orderID, clientID, symbol string) map[string]any {
				record := bybitExecutionFixture(execID, orderID, symbol, until.Add(-time.Minute))
				record["orderLinkId"] = clientID
				return record
			}
			writeJSON(t, w, map[string]any{"retCode": 0, "retMsg": "OK", "result": map[string]any{
				"list": []any{
					fill("exec-long", "order-long", "client-long", "BTCUSDT"),
					fill("exec-short", "order-short", "client-short", "BTCUSDT"),
					fill("exec-realtime", "realtime-order", "realtime-client", "BTCUSDT"),
					fill("exec-usdc", "order-usdc", "client-usdc", "BTCPERP"),
				},
				"nextPageCursor": "",
			}})
		case "/v5/order/history":
			query := r.URL.Query()
			symbol := query.Get("symbol")
			historyCalls[symbol]++
			if got := query.Get("limit"); got != "50" {
				t.Errorf("history limit=%q, want 50", got)
			}
			if got := query.Get("startTime"); got != strconv.FormatInt(start.UnixMilli(), 10) {
				t.Errorf("history startTime=%q, want %d", got, start.UnixMilli())
			}
			if got := query.Get("endTime"); got != strconv.FormatInt(until.UnixMilli(), 10) {
				t.Errorf("history endTime=%q, want %d", got, until.UnixMilli())
			}
			if query.Get("orderId") != "" || query.Get("orderLinkId") != "" {
				t.Errorf("batched history unexpectedly filtered to one identity: %s", query.Encode())
			}
			switch symbol {
			case "BTCUSDT":
				if got := query.Get("settleCoin"); got != bybitsdk.SettleCoinUSDT {
					t.Errorf("BTCUSDT settleCoin=%q, want %s", got, bybitsdk.SettleCoinUSDT)
				}
				if query.Get("cursor") == "" {
					writeJSON(t, w, map[string]any{"retCode": 0, "retMsg": "OK", "result": map[string]any{
						"list": []any{
							bybitOrderFixture("order-long", "forged-client", "BTCUSDT", "Buy", 1, "Filled"),
							bybitOrderFixture("forged-order", "client-short", "BTCUSDT", "Sell", 2, "Filled"),
							bybitOrderFixture("unrelated-order", "unrelated-client", "BTCUSDT", "Buy", 9, "Filled"),
							bybitOrderFixture("order-long", "client-long", "BTCUSDT", "Buy", 1, "Filled"),
						},
						"nextPageCursor": "btc-next",
					}})
					return
				}
				if got := query.Get("cursor"); got != "btc-next" {
					t.Errorf("BTCUSDT cursor=%q, want btc-next", got)
				}
				writeJSON(t, w, map[string]any{"retCode": 0, "retMsg": "OK", "result": map[string]any{
					"list": []any{
						bybitOrderFixture("order-short", "client-short", "BTCUSDT", "Sell", 2, "Filled"),
						bybitOrderFixture("realtime-order", "realtime-client", "BTCUSDT", "Buy", 1, "Filled"),
					},
					"nextPageCursor": "",
				}})
			case "BTCPERP":
				if got := query.Get("settleCoin"); got != bybitsdk.SettleCoinUSDC {
					t.Errorf("BTCPERP settleCoin=%q, want %s", got, bybitsdk.SettleCoinUSDC)
				}
				if query.Get("cursor") != "" {
					http.Error(w, "history scanner did not stop after all USDC targets were found", http.StatusServiceUnavailable)
					return
				}
				writeJSON(t, w, map[string]any{"retCode": 0, "retMsg": "OK", "result": map[string]any{
					"list":           []any{bybitOrderFixture("order-usdc", "client-usdc", "BTCPERP", "Sell", 2, "Filled")},
					"nextPageCursor": "unused-usdc-next",
				}})
			default:
				t.Errorf("unexpected history symbol %q", symbol)
				http.Error(w, "unexpected symbol", http.StatusBadRequest)
			}
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.Error(w, "unexpected path", http.StatusNotFound)
		}
	}))
	defer server.Close()

	exec := newExecutionClient(
		bybitsdk.NewClient().WithCredentials("key", "secret").WithBaseURL(server.URL).WithHTTPClient(server.Client()),
		bybitTestProvider(),
		clock.NewSimulatedClock(until),
	).withCategories("linear")
	mass, err := exec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{
		AccountID:    AccountIDUnified,
		Until:        until,
		IncludeFills: true,
	})
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	if historyCalls["BTCUSDT"] != 2 || historyCalls["BTCPERP"] != 1 || len(historyCalls) != 2 {
		t.Fatalf("history calls=%v, want two BTCUSDT pages and one BTCPERP page", historyCalls)
	}
	if len(mass.OrderReports) != 4 {
		t.Fatalf("order reports=%+v, want one realtime plus three strictly matched history reports", mass.OrderReports)
	}
	if got := mass.OrderReports["order-long"].Order.Request.PositionSide; got != enums.PosLong {
		t.Fatalf("order-long position side=%s, want LONG", got)
	}
	if got := mass.OrderReports["order-short"].Order.Request.PositionSide; got != enums.PosShort {
		t.Fatalf("order-short position side=%s, want SHORT", got)
	}
	if got := mass.OrderReports["order-usdc"].Order.Request.PositionSide; got != enums.PosShort {
		t.Fatalf("order-usdc position side=%s, want SHORT", got)
	}
	if got := mass.OrderReports["realtime-order"].Order.Status; got != enums.StatusNew {
		t.Fatalf("realtime order status=%s, want existing realtime report to remain NEW", got)
	}
	if got := mass.OrderReports["realtime-order"].Order.Request.ClientID; got != "stale-realtime-client" {
		t.Fatalf("realtime order client id=%q, want existing realtime report to remain authoritative", got)
	}
	if _, ok := mass.OrderReports["forged-order"]; ok {
		t.Fatal("history record matching only client id passed strict dual-identity filtering")
	}
}

func TestBybitExecutionMassStatusBoundsDerivativeOrderHistoryHydration(t *testing.T) {
	until := time.UnixMilli(1_700_000_000_000)
	var historyCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v5/order/realtime":
			writeJSON(t, w, map[string]any{"retCode": 0, "retMsg": "OK", "result": map[string]any{"list": []any{}, "nextPageCursor": ""}})
		case "/v5/execution/list":
			record := bybitExecutionFixture("exec-bounded", "missing-order", "BTCUSDT", until.Add(-time.Minute))
			record["orderLinkId"] = "missing-client"
			writeJSON(t, w, map[string]any{"retCode": 0, "retMsg": "OK", "result": map[string]any{"list": []any{record}, "nextPageCursor": ""}})
		case "/v5/order/history":
			call := int(historyCalls.Add(1))
			if call > 20 {
				http.Error(w, "history hydration exceeded its 1000-record bound", http.StatusServiceUnavailable)
				return
			}
			list := make([]any, 0, 50)
			for i := 0; i < 50; i++ {
				list = append(list, bybitOrderFixture(
					fmt.Sprintf("noise-order-%02d-%02d", call, i),
					fmt.Sprintf("noise-client-%02d-%02d", call, i),
					"BTCUSDT", "Buy", 9, "Filled",
				))
			}
			writeJSON(t, w, map[string]any{"retCode": 0, "retMsg": "OK", "result": map[string]any{
				"list":           list,
				"nextPageCursor": fmt.Sprintf("page-%02d", call+1),
			}})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.Error(w, "unexpected path", http.StatusNotFound)
		}
	}))
	defer server.Close()

	exec := newExecutionClient(
		bybitsdk.NewClient().WithCredentials("key", "secret").WithBaseURL(server.URL).WithHTTPClient(server.Client()),
		bybitTestProvider(),
		clock.NewSimulatedClock(until),
	).withCategories("linear")
	mass, err := exec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{
		AccountID:    AccountIDUnified,
		Until:        until,
		IncludeFills: true,
	})
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	if got := historyCalls.Load(); got != 20 {
		t.Fatalf("history calls=%d, want bounded 20 pages/1000 records", got)
	}
	if len(mass.FillReports) != 1 || len(mass.OrderReports) != 0 {
		t.Fatalf("mass=%+v, want unresolved fill retained for exact fallback", mass)
	}
	if !bybitHasWarning(mass.Warnings, "DERIVATIVE_ORDER_HISTORY_HYDRATION_LIMIT_REACHED") {
		t.Fatalf("warnings=%+v, want bounded-history warning", mass.Warnings)
	}
}

func TestBybitExecutionMassStatusWarnsWhenDerivativeOrderHistoryHydrationIsUnavailable(t *testing.T) {
	until := time.UnixMilli(1_700_000_000_000)
	var historyCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v5/order/realtime":
			writeJSON(t, w, map[string]any{"retCode": 0, "retMsg": "OK", "result": map[string]any{"list": []any{}, "nextPageCursor": ""}})
		case "/v5/execution/list":
			record := bybitExecutionFixture("exec-fallback", "order-fallback", "BTCUSDT", until.Add(-time.Minute))
			record["orderLinkId"] = "client-fallback"
			writeJSON(t, w, map[string]any{"retCode": 0, "retMsg": "OK", "result": map[string]any{
				"list":           []any{record},
				"nextPageCursor": "",
			}})
		case "/v5/order/history":
			historyCalls.Add(1)
			http.Error(w, "temporary history outage", http.StatusServiceUnavailable)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.Error(w, "unexpected path", http.StatusNotFound)
		}
	}))
	defer server.Close()

	exec := newExecutionClient(
		bybitsdk.NewClient().WithCredentials("key", "secret").WithBaseURL(server.URL).WithHTTPClient(server.Client()),
		bybitTestProvider(),
		clock.NewSimulatedClock(until),
	).withCategories("linear")
	mass, err := exec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{
		AccountID:    AccountIDUnified,
		Until:        until,
		IncludeFills: true,
	})
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	if historyCalls.Load() != 1 {
		t.Fatalf("history calls=%d, want one batched attempt", historyCalls.Load())
	}
	if len(mass.FillReports) != 1 || len(mass.OrderReports) != 0 {
		t.Fatalf("mass=%+v, want fill preserved and missing order left for exact fallback", mass)
	}
	if mass.Partial {
		t.Fatal("optional order-history hydration failure must not mark the authoritative open-order/fill snapshot partial")
	}
	if !bybitHasWarning(mass.Warnings, "DERIVATIVE_ORDER_HISTORY_HYDRATION_UNAVAILABLE") {
		t.Fatalf("warnings=%+v, want explicit hydration warning", mass.Warnings)
	}
}

func TestBybitExecutionMassStatusFailsClosedOnMatchedHistoricalPositionIndex(t *testing.T) {
	until := time.UnixMilli(1_700_000_000_000)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v5/order/realtime":
			writeJSON(t, w, map[string]any{"retCode": 0, "retMsg": "OK", "result": map[string]any{"list": []any{}, "nextPageCursor": ""}})
		case "/v5/execution/list":
			record := bybitExecutionFixture("exec-invalid-leg", "order-invalid-leg", "BTCUSDT", until.Add(-time.Minute))
			record["orderLinkId"] = "client-invalid-leg"
			writeJSON(t, w, map[string]any{"retCode": 0, "retMsg": "OK", "result": map[string]any{"list": []any{record}, "nextPageCursor": ""}})
		case "/v5/order/history":
			writeJSON(t, w, map[string]any{"retCode": 0, "retMsg": "OK", "result": map[string]any{
				"list":           []any{bybitOrderFixture("order-invalid-leg", "client-invalid-leg", "BTCUSDT", "Buy", 9, "Filled")},
				"nextPageCursor": "",
			}})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.Error(w, "unexpected path", http.StatusNotFound)
		}
	}))
	defer server.Close()

	exec := newExecutionClient(
		bybitsdk.NewClient().WithCredentials("key", "secret").WithBaseURL(server.URL).WithHTTPClient(server.Client()),
		bybitTestProvider(),
		clock.NewSimulatedClock(until),
	).withCategories("linear")
	_, err := exec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{
		AccountID:    AccountIDUnified,
		Until:        until,
		IncludeFills: true,
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported positionIdx 9") {
		t.Fatalf("GenerateExecutionMassStatus error=%v, want matched invalid hedge leg to fail closed", err)
	}
}

func TestBybitExecutionMassStatusDoesNotQueryOrderHistoryForSpotFills(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v5/order/realtime":
			writeJSON(t, w, map[string]any{"retCode": 0, "retMsg": "OK", "result": map[string]any{"list": []any{}, "nextPageCursor": ""}})
		case "/v5/execution/list":
			writeJSON(t, w, map[string]any{"retCode": 0, "retMsg": "OK", "result": map[string]any{
				"list":           []any{bybitExecutionFixture("spot-exec", "spot-order", "ETHUSDT", time.UnixMilli(1_700_000_000_000))},
				"nextPageCursor": "",
			}})
		case "/v5/order/history":
			t.Error("spot fill triggered derivative order-history hydration")
			http.Error(w, "unexpected history", http.StatusBadRequest)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.Error(w, "unexpected path", http.StatusNotFound)
		}
	}))
	defer server.Close()

	exec := newExecutionClient(
		bybitsdk.NewClient().WithCredentials("key", "secret").WithBaseURL(server.URL).WithHTTPClient(server.Client()),
		bybitTestProvider(),
		clock.NewRealClock(),
	).withCategories("spot")
	mass, err := exec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{AccountID: AccountIDUnified, IncludeFills: true})
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	if len(mass.FillReports) != 1 || len(mass.OrderReports) != 0 {
		t.Fatalf("mass=%+v, want one spot fill and no synthesized order", mass)
	}
}

func TestBybitExecutionMassStatusHonorsConfiguredCategoryScope(t *testing.T) {
	var orderCalls atomic.Int32
	var fillCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v5/order/realtime":
			orderCalls.Add(1)
		case "/v5/execution/list":
			fillCalls.Add(1)
		default:
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		if got := r.URL.Query().Get("category"); got != "spot" {
			t.Errorf("category=%q, want spot-only scope", got)
		}
		writeJSON(t, w, map[string]any{"retCode": 0, "retMsg": "OK", "result": map[string]any{"list": []any{}, "nextPageCursor": ""}})
	}))
	defer server.Close()

	exec := newExecutionClient(
		bybitsdk.NewClient().WithCredentials("key", "secret").WithBaseURL(server.URL).WithHTTPClient(server.Client()),
		bybitTestProvider(),
		clock.NewRealClock(),
	).withCategories("spot")
	mass, err := exec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{
		AccountID:    AccountIDUnified,
		IncludeFills: true,
	})
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	if len(mass.OrderReports) != 0 || len(mass.FillReports) != 0 {
		t.Fatalf("unexpected reports: %+v", mass)
	}
	if orderCalls.Load() != 1 || fillCalls.Load() != 1 {
		t.Fatalf("order calls=%d fill calls=%d, want one spot call each", orderCalls.Load(), fillCalls.Load())
	}
	capabilities := exec.Capabilities()
	products := capabilities.Products
	if len(products) != 1 || products[0].Kind != enums.KindSpot {
		t.Fatalf("products=%+v, want spot-only capability", products)
	}
	if capabilities.Reports.PositionReports {
		t.Fatal("spot-only execution capability must not advertise derivative position reports")
	}
	account := newAccountClient(nil, bybitTestProvider(), clock.NewRealClock(), []enums.InstrumentKind{enums.KindSpot})
	defer account.Close()
	if account.Capabilities().Reports.PositionReports {
		t.Fatal("spot-only account capability must not advertise derivative position reports")
	}
}

func TestBybitWildcardReportsFanOutConfiguredCategories(t *testing.T) {
	for _, tc := range []struct {
		name       string
		categories []string
		wantOrders map[string]int
		wantFills  map[string]int
	}{
		{
			name:       "default unified scope",
			wantOrders: map[string]int{"spot/": 1, "linear/USDT": 1, "linear/USDC": 1},
			wantFills:  map[string]int{"spot": 1, "linear": 1},
		},
		{
			name:       "spot-only scope",
			categories: []string{"spot"},
			wantOrders: map[string]int{"spot/": 1},
			wantFills:  map[string]int{"spot": 1},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			orderCalls := make(map[string]int)
			fillCalls := make(map[string]int)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				category := r.URL.Query().Get("category")
				switch r.URL.Path {
				case "/v5/order/realtime":
					orderCalls[category+"/"+r.URL.Query().Get("settleCoin")]++
				case "/v5/execution/list":
					fillCalls[category]++
				default:
					http.Error(w, "unexpected path", http.StatusNotFound)
					return
				}
				writeJSON(t, w, map[string]any{"retCode": 0, "retMsg": "OK", "result": map[string]any{"list": []any{}, "nextPageCursor": ""}})
			}))
			defer server.Close()

			exec := newExecutionClient(
				bybitsdk.NewClient().WithCredentials("key", "secret").WithBaseURL(server.URL).WithHTTPClient(server.Client()),
				bybitTestProvider(),
				clock.NewRealClock(),
			)
			if len(tc.categories) != 0 {
				exec.withCategories(tc.categories...)
			}
			if _, err := exec.GenerateOrderStatusReports(context.Background(), model.OrderStatusReportQuery{AccountID: AccountIDUnified}); err != nil {
				t.Fatalf("GenerateOrderStatusReports: %v", err)
			}
			if _, err := exec.GenerateFillReports(context.Background(), model.FillReportQuery{AccountID: AccountIDUnified}); err != nil {
				t.Fatalf("GenerateFillReports: %v", err)
			}
			if len(orderCalls) != len(tc.wantOrders) || len(fillCalls) != len(tc.wantFills) {
				t.Fatalf("order calls=%v want=%v; fill calls=%v want=%v", orderCalls, tc.wantOrders, fillCalls, tc.wantFills)
			}
			for scope, want := range tc.wantOrders {
				if orderCalls[scope] != want {
					t.Fatalf("order scope=%s calls=%d, want %d", scope, orderCalls[scope], want)
				}
			}
			for category, want := range tc.wantFills {
				if fillCalls[category] != want {
					t.Fatalf("fill category=%s calls=%d, want %d", category, fillCalls[category], want)
				}
			}
		})
	}
}

func TestBybitWildcardExactOrderQueryDoesNotFanOutLinearSettlements(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v5/order/realtime" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		calls.Add(1)
		if got := r.URL.Query().Get("category"); got != "linear" {
			t.Fatalf("category=%q, want linear", got)
		}
		if got := r.URL.Query().Get("settleCoin"); got != "" {
			t.Fatalf("settleCoin=%q, exact order id must not fan out settlements", got)
		}
		writeJSON(t, w, map[string]any{"retCode": 0, "retMsg": "OK", "result": map[string]any{"list": []any{map[string]any{
			"orderId": "order-1", "orderLinkId": "client-1", "symbol": "BTCUSDT", "side": "Buy", "orderType": "Limit",
			"timeInForce": "IOC", "price": "100", "qty": "0.001", "cumExecQty": "0.001", "orderStatus": "Filled",
		}}, "nextPageCursor": ""}})
	}))
	defer server.Close()

	exec := newExecutionClient(
		bybitsdk.NewClient().WithCredentials("key", "secret").WithBaseURL(server.URL).WithHTTPClient(server.Client()),
		bybitTestProvider(),
		clock.NewRealClock(),
	).withCategories("linear")
	reports, err := exec.GenerateOrderStatusReports(context.Background(), model.OrderStatusReportQuery{
		AccountID:    AccountIDUnified,
		VenueOrderID: "order-1",
	})
	if err != nil {
		t.Fatalf("GenerateOrderStatusReports: %v", err)
	}
	if len(reports) != 1 || reports[0].Order.Request.InstrumentID != (model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT", Kind: enums.KindPerp}) {
		t.Fatalf("reports=%+v, want one exact USDT-perp order", reports)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls=%d, want one category-scoped exact-id query", calls.Load())
	}
}

func TestBybitMassStatusClientIDDoesNotDuplicateLinearSettlements(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v5/order/realtime" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		calls.Add(1)
		if got := r.URL.Query().Get("settleCoin"); got != "" {
			t.Fatalf("settleCoin=%q, exact client id must not fan out settlements", got)
		}
		writeJSON(t, w, map[string]any{"retCode": 0, "retMsg": "OK", "result": map[string]any{"list": []any{map[string]any{
			"orderId": "order-1", "orderLinkId": "client-1", "symbol": "BTCUSDT", "side": "Buy", "orderType": "Limit",
			"timeInForce": "IOC", "price": "100", "qty": "0.001", "cumExecQty": "0.001", "orderStatus": "Filled",
		}}, "nextPageCursor": ""}})
	}))
	defer server.Close()

	exec := newExecutionClient(
		bybitsdk.NewClient().WithCredentials("key", "secret").WithBaseURL(server.URL).WithHTTPClient(server.Client()),
		bybitTestProvider(),
		clock.NewRealClock(),
	).withCategories("linear")
	mass, err := exec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{AccountID: AccountIDUnified, ClientID: "client-1"})
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	if len(mass.OrderReports) != 1 {
		t.Fatalf("order reports=%+v, want one exact client-id order", mass.OrderReports)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls=%d, want one category-scoped client-id query", calls.Load())
	}
}

func TestBybitWildcardFillReportsAppliesGlobalLimit(t *testing.T) {
	base := time.UnixMilli(1_700_000_000_000)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v5/execution/list" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		var records []any
		switch r.URL.Query().Get("category") {
		case "spot":
			records = []any{
				bybitExecutionFixture("spot-new", "spot-order", "ETHUSDT", base.Add(3*time.Second)),
				bybitExecutionFixture("spot-old", "spot-order", "ETHUSDT", base.Add(time.Second)),
			}
		case "linear":
			records = []any{
				bybitExecutionFixture("linear-new", "linear-order", "BTCUSDT", base.Add(4*time.Second)),
				bybitExecutionFixture("linear-old", "linear-order", "BTCUSDT", base.Add(2*time.Second)),
			}
		default:
			t.Fatalf("unexpected category %q", r.URL.Query().Get("category"))
		}
		writeJSON(t, w, map[string]any{"retCode": 0, "retMsg": "OK", "result": map[string]any{"list": records, "nextPageCursor": ""}})
	}))
	defer server.Close()

	exec := newExecutionClient(
		bybitsdk.NewClient().WithCredentials("key", "secret").WithBaseURL(server.URL).WithHTTPClient(server.Client()),
		bybitTestProvider(),
		clock.NewRealClock(),
	)
	reports, err := exec.GenerateFillReports(context.Background(), model.FillReportQuery{AccountID: AccountIDUnified, Limit: 2})
	if err != nil {
		t.Fatalf("GenerateFillReports: %v", err)
	}
	if len(reports) != 2 || reports[0].Fill.TradeID != "linear-new" || reports[1].Fill.TradeID != "spot-new" {
		t.Fatalf("reports=%+v, want the two newest fills across configured categories", reports)
	}
}

func TestBybitGenerateFillReportsFailsClosedOnUnknownVenueSymbol(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v5/execution/list" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		writeJSON(t, w, map[string]any{"retCode": 0, "retMsg": "OK", "result": map[string]any{
			"list":           []any{bybitExecutionFixture("unknown-fill", "unknown-order", "NEWUSDT", time.UnixMilli(1_700_000_000_000))},
			"nextPageCursor": "",
		}})
	}))
	defer server.Close()

	exec := newExecutionClient(
		bybitsdk.NewClient().WithCredentials("key", "secret").WithBaseURL(server.URL).WithHTTPClient(server.Client()),
		bybitTestProvider(),
		clock.NewRealClock(),
	).withCategories("linear")
	_, err := exec.GenerateFillReports(context.Background(), model.FillReportQuery{AccountID: AccountIDUnified})
	if err == nil || !strings.Contains(err.Error(), "unknown fill instrument") {
		t.Fatalf("GenerateFillReports error=%v, want unknown fill instrument", err)
	}
}

func TestBybitGenerateFillReportsSkipsUnsupportedDatedLinearFuture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v5/execution/list" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		writeJSON(t, w, map[string]any{"retCode": 0, "retMsg": "OK", "result": map[string]any{
			"list": []any{
				bybitExecutionFixture("dated-fill", "dated-order", "BTCUSDT-31JUL26", time.UnixMilli(1_700_000_000_000)),
				bybitExecutionFixture("perp-fill", "perp-order", "BTCUSDT", time.UnixMilli(1_700_000_000_001)),
			},
			"nextPageCursor": "",
		}})
	}))
	defer server.Close()

	provider := bybitTestProvider()
	exec := newExecutionClient(
		bybitsdk.NewClient().WithCredentials("key", "secret").WithBaseURL(server.URL).WithHTTPClient(server.Client()),
		provider,
		clock.NewRealClock(),
	).withCategories("linear")
	reports, err := exec.GenerateFillReports(context.Background(), model.FillReportQuery{AccountID: AccountIDUnified})
	if err != nil {
		t.Fatalf("GenerateFillReports: %v", err)
	}
	if len(reports) != 1 || reports[0].Fill.TradeID != "perp-fill" {
		t.Fatalf("reports=%+v, want only supported perpetual fill", reports)
	}
}

func TestBybitScopedFillReportFailsClosedOnDeferredSymbolMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"retCode": 0, "retMsg": "OK", "result": map[string]any{
			"list":           []any{bybitExecutionFixture("dated-fill", "dated-order", "BTCUSDT-31JUL26", time.UnixMilli(1_700_000_000_000))},
			"nextPageCursor": "",
		}})
	}))
	defer server.Close()

	provider := bybitTestProvider()
	provider.markDeferred("linear", "BTCUSDT-31JUL26")
	exec := newExecutionClient(
		bybitsdk.NewClient().WithCredentials("key", "secret").WithBaseURL(server.URL).WithHTTPClient(server.Client()),
		provider,
		clock.NewRealClock(),
	)
	_, err := exec.GenerateFillReports(context.Background(), model.FillReportQuery{
		AccountID:    AccountIDUnified,
		InstrumentID: model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT", Kind: enums.KindPerp},
	})
	if err == nil || !strings.Contains(err.Error(), "unknown fill instrument") {
		t.Fatalf("GenerateFillReports error=%v, want scoped symbol mismatch", err)
	}
}

func TestBybitExecutionMassStatusSkipsKnownDeferredDatedFutureFill(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v5/order/realtime":
			writeJSON(t, w, map[string]any{"retCode": 0, "retMsg": "OK", "result": map[string]any{"list": []any{}, "nextPageCursor": ""}})
		case "/v5/order/history":
			writeJSON(t, w, map[string]any{"retCode": 0, "retMsg": "OK", "result": map[string]any{"list": []any{}, "nextPageCursor": ""}})
		case "/v5/execution/list":
			list := []any{}
			if r.URL.Query().Get("category") == "linear" {
				list = []any{
					bybitExecutionFixture("dated-fill", "dated-order", "BTCUSDT-31JUL26", time.UnixMilli(1_700_000_000_000)),
					bybitExecutionFixture("perp-fill", "perp-order", "BTCUSDT", time.UnixMilli(1_700_000_000_001)),
				}
			}
			writeJSON(t, w, map[string]any{"retCode": 0, "retMsg": "OK", "result": map[string]any{"list": list, "nextPageCursor": ""}})
		default:
			http.Error(w, "unexpected path", http.StatusNotFound)
		}
	}))
	defer server.Close()

	provider := bybitTestProvider()
	provider.markDeferred("linear", "BTCUSDT-31JUL26")
	exec := newExecutionClient(
		bybitsdk.NewClient().WithCredentials("key", "secret").WithBaseURL(server.URL).WithHTTPClient(server.Client()),
		provider,
		clock.NewRealClock(),
	)
	mass, err := exec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{AccountID: AccountIDUnified, IncludeFills: true})
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	var reports []model.FillReport
	for _, grouped := range mass.FillReports {
		reports = append(reports, grouped...)
	}
	if len(reports) != 1 || reports[0].Fill.TradeID != "perp-fill" {
		t.Fatalf("fill reports=%+v, want only supported perpetual fill", mass.FillReports)
	}
}

func TestBybitGenerateFillReportsDoesNotTreatFundingAsFill(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		funding := bybitExecutionFixture("funding", "", "UNKNOWNUSDT", time.UnixMilli(1_700_000_000_000))
		funding["execType"] = "Funding"
		writeJSON(t, w, map[string]any{"retCode": 0, "retMsg": "OK", "result": map[string]any{
			"list": []any{
				funding,
				bybitExecutionFixture("trade", "order", "BTCUSDT", time.UnixMilli(1_700_000_000_001)),
			},
			"nextPageCursor": "",
		}})
	}))
	defer server.Close()

	exec := newExecutionClient(
		bybitsdk.NewClient().WithCredentials("key", "secret").WithBaseURL(server.URL).WithHTTPClient(server.Client()),
		bybitTestProvider(),
		clock.NewRealClock(),
	).withCategories("linear")
	reports, err := exec.GenerateFillReports(context.Background(), model.FillReportQuery{AccountID: AccountIDUnified})
	if err != nil {
		t.Fatalf("GenerateFillReports: %v", err)
	}
	if len(reports) != 1 || reports[0].Fill.TradeID != "trade" {
		t.Fatalf("reports=%+v, want only ordinary trade", reports)
	}
}

func TestBybitBoundedFillReportsDoesNotLetFundingConsumeTradeLimit(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Query().Get("cursor") == "next" {
			writeJSON(t, w, map[string]any{"retCode": 0, "retMsg": "OK", "result": map[string]any{
				"list":           []any{bybitExecutionFixture("trade", "order", "BTCUSDT", time.UnixMilli(1_700_000_000_001))},
				"nextPageCursor": "",
			}})
			return
		}
		funding := bybitExecutionFixture("funding", "", "UNKNOWNUSDT", time.UnixMilli(1_700_000_000_000))
		funding["execType"] = "Funding"
		writeJSON(t, w, map[string]any{"retCode": 0, "retMsg": "OK", "result": map[string]any{
			"list":           []any{funding},
			"nextPageCursor": "next",
		}})
	}))
	defer server.Close()

	exec := newExecutionClient(
		bybitsdk.NewClient().WithCredentials("key", "secret").WithBaseURL(server.URL).WithHTTPClient(server.Client()),
		bybitTestProvider(),
		clock.NewRealClock(),
	).withCategories("linear")
	reports, err := exec.GenerateFillReports(context.Background(), model.FillReportQuery{AccountID: AccountIDUnified, Limit: 1})
	if err != nil {
		t.Fatalf("GenerateFillReports: %v", err)
	}
	if len(reports) != 1 || reports[0].Fill.TradeID != "trade" {
		t.Fatalf("reports=%+v, want one Trade after skipped Funding", reports)
	}
	if calls.Load() != 3 {
		t.Fatalf("calls=%d, want bounded retry plus continuation", calls.Load())
	}
}

func TestBybitGenerateFillReportsFailsClosedOnUnsupportedExecutionType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		settlement := bybitExecutionFixture("settlement", "order", "BTCUSDT", time.UnixMilli(1_700_000_000_000))
		settlement["execType"] = "Settle"
		writeJSON(t, w, map[string]any{"retCode": 0, "retMsg": "OK", "result": map[string]any{
			"list":           []any{settlement},
			"nextPageCursor": "",
		}})
	}))
	defer server.Close()

	exec := newExecutionClient(
		bybitsdk.NewClient().WithCredentials("key", "secret").WithBaseURL(server.URL).WithHTTPClient(server.Client()),
		bybitTestProvider(),
		clock.NewRealClock(),
	).withCategories("linear")
	_, err := exec.GenerateFillReports(context.Background(), model.FillReportQuery{AccountID: AccountIDUnified})
	if err == nil || !strings.Contains(err.Error(), "unsupported execution type") {
		t.Fatalf("GenerateFillReports error=%v, want unsupported execution type", err)
	}
}

func bybitExecutionFixture(execID, orderID, symbol string, timestamp time.Time) map[string]any {
	return map[string]any{
		"execType":    "Trade",
		"execId":      execID,
		"orderId":     orderID,
		"orderLinkId": "client",
		"symbol":      symbol,
		"side":        "Buy",
		"execPrice":   "100",
		"execQty":     "0.01",
		"execTime":    strconv.FormatInt(timestamp.UnixMilli(), 10),
	}
}

func bybitOrderFixture(orderID, clientID, symbol, side string, positionIdx int, status string) map[string]any {
	return map[string]any{
		"orderId":     orderID,
		"orderLinkId": clientID,
		"symbol":      symbol,
		"side":        side,
		"positionIdx": positionIdx,
		"orderType":   "Limit",
		"timeInForce": "GTC",
		"price":       "100",
		"qty":         "0.01",
		"cumExecQty":  "0.01",
		"avgPrice":    "100",
		"orderStatus": status,
		"createdTime": "1700000000000",
		"updatedTime": "1700000001000",
		"reduceOnly":  false,
	}
}

func bybitHasWarning(warnings []model.ReportWarning, code string) bool {
	for _, warning := range warnings {
		if warning.Code == code {
			return true
		}
	}
	return false
}
