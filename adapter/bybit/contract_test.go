package bybit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/adapter/internal/runtimeaccept"
	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	btruntime "github.com/QuantProcessing/boltertrader/runtime"
	bybitsdk "github.com/QuantProcessing/boltertrader/sdk/bybit"
	"github.com/shopspring/decimal"
)

func TestBybitClientsImplementContractsAndCapabilities(t *testing.T) {
	provider := bybitTestProvider()
	clk := clock.NewSimulatedClock(time.Date(2026, 7, 6, 1, 0, 0, 0, time.UTC))
	rest := bybitsdk.NewClient().WithCredentials("key", "secret")

	var _ contract.MarketDataClient = newMarketDataClient(rest, nil, provider, clk)
	var _ contract.ExecutionClient = newExecutionClient(rest, provider, clk)
	var _ contract.AccountClient = newAccountClient(rest, provider, clk, []enums.InstrumentKind{enums.KindSpot, enums.KindPerp})
	var _ contract.AccountStateReporter = newAccountClient(rest, provider, clk, []enums.InstrumentKind{enums.KindSpot, enums.KindPerp})

	if caps := newAccountClient(rest, provider, clk, nil).Capabilities(); !caps.Reports.AccountStateSnapshots || !caps.Streaming.Account {
		t.Fatalf("account capabilities missing account-state/private stream support: %+v", caps)
	}
	if caps := newExecutionClient(rest, provider, clk).Capabilities(); !caps.Trading.Submit || !caps.Reports.OpenOrders {
		t.Fatalf("execution capabilities missing submit/open-order support: %+v", caps)
	}
}

func TestBybitAccountIDOverridePropagatesToClients(t *testing.T) {
	const accountID = "BYBIT-ALT"
	provider := bybitTestProvider()
	clk := clock.NewSimulatedClock(time.Date(2026, 7, 6, 1, 0, 0, 0, time.UTC))
	rest := bybitsdk.NewClient().WithCredentials("key", "secret")

	exec := newExecutionClient(rest, provider, clk, accountID)
	acct := newAccountClient(rest, provider, clk, []enums.InstrumentKind{enums.KindSpot, enums.KindPerp}, accountID)

	if exec.AccountID() != accountID || acct.AccountID() != accountID {
		t.Fatalf("account ids exec=%q acct=%q, want %q", exec.AccountID(), acct.AccountID(), accountID)
	}
}

func TestBybitAccountStateAcceptsUnifiedModesAndSharedAccountID(t *testing.T) {
	for _, tt := range []struct {
		name string
		mode bybitsdk.AccountMode
		code bybitsdk.UnifiedMarginStatus
	}{
		{name: "UTA1", mode: bybitsdk.AccountModeUTA1, code: bybitsdk.UnifiedMarginStatusUTA1},
		{name: "UTA1Pro", mode: bybitsdk.AccountModeUTA1, code: bybitsdk.UnifiedMarginStatusUTA1Pro},
		{name: "UTA2", mode: bybitsdk.AccountModeUTA2, code: bybitsdk.UnifiedMarginStatusUTA2},
		{name: "UTA2Pro", mode: bybitsdk.AccountModeUTA2, code: bybitsdk.UnifiedMarginStatusUTA2Pro},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := newBybitAccountServer(t, bybitAccountFixture{
				UnifiedMarginStatus: tt.code,
			})
			clk := clock.NewSimulatedClock(time.Date(2026, 7, 6, 1, 1, 0, 0, time.UTC))
			acct := newAccountClient(
				bybitsdk.NewClient().WithCredentials("key", "secret").WithBaseURL(server.URL).WithHTTPClient(server.Client()),
				bybitTestProvider(),
				clk,
				[]enums.InstrumentKind{enums.KindSpot, enums.KindPerp},
			)

			state, err := acct.AccountState(context.Background())
			if err != nil {
				t.Fatalf("AccountState: %v", err)
			}
			if state.AccountID != AccountIDUnified || state.Venue != VenueName || state.Type != model.AccountMargin {
				t.Fatalf("unexpected account identity/type: %+v", state)
			}
			if err := state.Validate(); err != nil {
				t.Fatalf("state invalid: %v", err)
			}
			if err := state.ModeInfo.ValidateVerified(); err != nil {
				t.Fatalf("mode info invalid: %v", err)
			}
			if state.ModeInfo.AccountMode != string(tt.mode) || !strings.Contains(state.ModeInfo.Source, "/v5/account/info") || !strings.Contains(state.ModeInfo.Source, "/v5/account/wallet-balance") {
				t.Fatalf("unexpected mode info: %+v", state.ModeInfo)
			}
			if AccountIDForKind(enums.KindSpot) != AccountIDForKind(enums.KindPerp) {
				t.Fatalf("spot/perp must share Bybit unified account id")
			}
			if len(state.Balances) == 0 || len(state.Margins) == 0 {
				t.Fatalf("expected balances and margin rows: %+v", state)
			}
			if got := bybitAvailableBalance(state, "USD"); !got.Equal(decimal.RequireFromString("900")) {
				t.Fatalf("unified USD available=%s, want 900 from account totalAvailableBalance", got)
			}
		})
	}
}

func TestBybitAccountStateFailClosedForClassic(t *testing.T) {
	server := newBybitAccountServer(t, bybitAccountFixture{UnifiedMarginStatus: bybitsdk.UnifiedMarginStatusClassic})
	acct := newAccountClient(
		bybitsdk.NewClient().WithCredentials("key", "secret").WithBaseURL(server.URL).WithHTTPClient(server.Client()),
		bybitTestProvider(),
		clock.NewRealClock(),
		[]enums.InstrumentKind{enums.KindSpot, enums.KindPerp},
	)
	if _, err := acct.AccountState(context.Background()); err == nil {
		t.Fatal("classic account mode must fail closed")
	}
}

func TestBybitOrderAndFillConversion(t *testing.T) {
	inst := bybitTestProvider().All()[1]
	req := model.OrderRequest{
		AccountID:    AccountIDUnified,
		InstrumentID: inst.ID,
		ClientID:     "client-1",
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTX,
		Quantity:     decimal.RequireFromString("0.01"),
		Price:        decimal.RequireFromString("50000"),
		PositionSide: enums.PosNet,
		ReduceOnly:   true,
	}
	venue, err := orderRequestToBybit(req, inst)
	if err != nil {
		t.Fatalf("orderRequestToBybit: %v", err)
	}
	if venue.Category != "linear" || venue.Symbol != "BTCUSDT" || venue.TimeInForce != "PostOnly" || !venue.ReduceOnly {
		t.Fatalf("unexpected venue order: %+v", venue)
	}

	order := orderFromBybitRecord(bybitsdk.OrderRecord{
		OrderID: "42", OrderLinkID: "client-1", Symbol: "BTCUSDT", Side: "Buy", OrderType: "Limit",
		TimeInForce: "PostOnly", Qty: "0.01", Price: "50000", CumExecQty: "0.005", AvgPrice: "50001", OrderStatus: "PartiallyFilled",
	}, inst.ID, AccountIDUnified)
	if order.Status != enums.StatusPartiallyFilled || !order.FilledQty.Equal(decimal.RequireFromString("0.005")) {
		t.Fatalf("unexpected order: %+v", order)
	}

	fill := fillFromBybitExecution(bybitsdk.ExecutionRecord{
		ExecID: "trade-1", OrderID: "42", OrderLinkID: "client-1", Symbol: "BTCUSDT", Side: "Buy",
		ExecPrice: "50001", ExecQty: "0.005", ExecFee: "0.01", FeeCurrency: "USDT", IsMaker: true, ExecTime: "1783299600000",
	}, inst.ID, AccountIDUnified)
	if fill.AccountID != AccountIDUnified || fill.Liquidity != enums.LiqMaker || fill.Timestamp.IsZero() {
		t.Fatalf("unexpected fill: %+v", fill)
	}
}

func TestBybitRuntimeResyncUsesAccountStateFirst(t *testing.T) {
	server := newBybitAccountServer(t, bybitAccountFixture{UnifiedMarginStatus: bybitsdk.UnifiedMarginStatusUTA2})
	provider := bybitTestProvider()
	clk := clock.NewSimulatedClock(time.Date(2026, 7, 6, 1, 2, 0, 0, time.UTC))
	rest := bybitsdk.NewClient().WithCredentials("key", "secret").WithBaseURL(server.URL).WithHTTPClient(server.Client())
	acct := newAccountClient(rest, provider, clk, []enums.InstrumentKind{enums.KindSpot, enums.KindPerp})
	node := btruntime.NewNode(btruntime.Clients{Account: acct}, clk, AccountIDUnified, btruntime.WithAccountID(AccountIDUnified))

	report, err := node.Resync(context.Background())
	if err != nil {
		t.Fatalf("Resync: %v", err)
	}
	if report.AccountStatesApplied != 1 {
		t.Fatalf("account states applied=%d, want 1: %+v", report.AccountStatesApplied, report)
	}
	runtimeaccept.AssertAccountStateReady(t, node, AccountIDUnified, model.AccountMargin, enums.KindPerp)
}

func TestBybitMassStatusQueriesLinearSettlementScopes(t *testing.T) {
	var settles []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v5/order/realtime" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("category"); got != "linear" {
			t.Fatalf("category=%q, want linear", got)
		}
		settle := r.URL.Query().Get("settleCoin")
		if settle == "" {
			t.Fatalf("settleCoin query is required: %s", r.URL.RawQuery)
		}
		settles = append(settles, settle)
		writeJSON(t, w, map[string]any{
			"retCode": 0,
			"retMsg":  "OK",
			"result":  map[string]any{"list": []any{}, "nextPageCursor": ""},
		})
	}))
	defer server.Close()

	rest := bybitsdk.NewClient().
		WithCredentials("key", "secret").
		WithBaseURL(server.URL).
		WithHTTPClient(server.Client())
	exec := newExecutionClient(rest, bybitTestProvider(), clock.NewSimulatedClock(time.Date(2026, 7, 6, 1, 3, 0, 0, time.UTC)))

	mass, err := exec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{AccountID: AccountIDUnified})
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	if len(settles) != 2 || settles[0] != bybitsdk.SettleCoinUSDT || settles[1] != bybitsdk.SettleCoinUSDC {
		t.Fatalf("settle queries=%v", settles)
	}
	if !mass.Partial || len(mass.Warnings) == 0 || mass.Warnings[0].Code != "bybit_spot_mass_status_symbol_scoped" {
		t.Fatalf("expected partial spot warning: %+v", mass)
	}
}

func TestBybitSingleOrderStatusFallsBackToOrderHistory(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if got := r.URL.Query().Get("category"); got != "spot" {
			t.Fatalf("category=%q, want spot", got)
		}
		if got := r.URL.Query().Get("symbol"); got != "ETHUSDT" {
			t.Fatalf("symbol=%q, want ETHUSDT", got)
		}
		if got := r.URL.Query().Get("orderLinkId"); got != "client-spot" {
			t.Fatalf("orderLinkId=%q, want client-spot", got)
		}
		switch r.URL.Path {
		case "/v5/order/realtime":
			writeJSON(t, w, map[string]any{
				"retCode": 0,
				"retMsg":  "OK",
				"result":  map[string]any{"list": []any{}, "nextPageCursor": ""},
			})
		case "/v5/order/history":
			writeJSON(t, w, map[string]any{
				"retCode": 0,
				"retMsg":  "OK",
				"result": map[string]any{"list": []any{
					map[string]any{
						"orderId":      "spot-order",
						"orderLinkId":  "client-spot",
						"symbol":       "ETHUSDT",
						"side":         "Buy",
						"orderType":    "Limit",
						"timeInForce":  "IOC",
						"qty":          "0.01",
						"price":        "100",
						"cumExecQty":   "0.01",
						"avgPrice":     "99.9",
						"orderStatus":  "Filled",
						"createdTime":  "1783299600000",
						"updatedTime":  "1783299601000",
						"cumExecFee":   "0.001",
						"closedPnl":    "0",
						"triggerPrice": "",
					},
				}, "nextPageCursor": ""},
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	rest := bybitsdk.NewClient().
		WithCredentials("key", "secret").
		WithBaseURL(server.URL).
		WithHTTPClient(server.Client())
	exec := newExecutionClient(rest, bybitTestProvider(), clock.NewSimulatedClock(time.Date(2026, 7, 6, 1, 4, 0, 0, time.UTC)))

	report, err := exec.GenerateOrderStatusReport(context.Background(), model.SingleOrderStatusQuery{
		AccountID:    AccountIDUnified,
		InstrumentID: model.InstrumentID{Venue: VenueName, Symbol: "ETH-USDT", Kind: enums.KindSpot},
		ClientID:     "client-spot",
		VenueOrderID: "spot-order",
	})
	if err != nil {
		t.Fatalf("GenerateOrderStatusReport: %v", err)
	}
	if report == nil || report.Order.Status != enums.StatusFilled || !report.Order.FilledQty.Equal(decimal.RequireFromString("0.01")) {
		t.Fatalf("unexpected report: %+v", report)
	}
	if strings.Join(paths, ",") != "/v5/order/realtime,/v5/order/history" {
		t.Fatalf("paths=%v", paths)
	}
}

func TestBybitScopedReportsPreserveSpotInstrumentForAmbiguousVenueSymbol(t *testing.T) {
	spotID := model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT", Kind: enums.KindSpot}
	provider := newInstrumentProvider()
	provider.LoadSnapshot([]*model.Instrument{
		instrumentFromBybit("spot", bybitsdk.Instrument{Symbol: "BTCUSDT", BaseCoin: "BTC", QuoteCoin: "USDT", Status: "Trading"}),
		instrumentFromBybit("linear", bybitsdk.Instrument{Symbol: "BTCUSDT", BaseCoin: "BTC", QuoteCoin: "USDT", SettleCoin: bybitsdk.SettleCoinUSDT, Status: "Trading"}),
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("category"); got != "spot" {
			t.Fatalf("category=%q, want spot", got)
		}
		if got := r.URL.Query().Get("symbol"); got != "BTCUSDT" {
			t.Fatalf("symbol=%q, want BTCUSDT", got)
		}
		switch r.URL.Path {
		case "/v5/order/realtime":
			writeJSON(t, w, map[string]any{
				"retCode": 0,
				"retMsg":  "OK",
				"result": map[string]any{"list": []any{
					map[string]any{
						"orderId":     "spot-order",
						"orderLinkId": "spot-client",
						"symbol":      "BTCUSDT",
						"side":        "Buy",
						"orderType":   "Limit",
						"timeInForce": "IOC",
						"qty":         "0.0001",
						"price":       "60000",
						"cumExecQty":  "0.0001",
						"avgPrice":    "59999",
						"orderStatus": "Filled",
					},
				}, "nextPageCursor": ""},
			})
		case "/v5/execution/list":
			writeJSON(t, w, map[string]any{
				"retCode": 0,
				"retMsg":  "OK",
				"result": map[string]any{"list": []any{
					map[string]any{
						"execId":      "spot-fill",
						"orderId":     "spot-order",
						"orderLinkId": "spot-client",
						"symbol":      "BTCUSDT",
						"side":        "Buy",
						"execPrice":   "59999",
						"execQty":     "0.0001",
						"execTime":    "1783299600000",
					},
				}, "nextPageCursor": ""},
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	rest := bybitsdk.NewClient().
		WithCredentials("key", "secret").
		WithBaseURL(server.URL).
		WithHTTPClient(server.Client())
	exec := newExecutionClient(rest, provider, clock.NewSimulatedClock(time.Date(2026, 7, 6, 1, 5, 0, 0, time.UTC)))

	report, err := exec.GenerateOrderStatusReport(context.Background(), model.SingleOrderStatusQuery{
		AccountID:    AccountIDUnified,
		InstrumentID: spotID,
		ClientID:     "spot-client",
		VenueOrderID: "spot-order",
	})
	if err != nil {
		t.Fatalf("GenerateOrderStatusReport: %v", err)
	}
	if report == nil || report.Order.Request.InstrumentID != spotID || report.Order.Status != enums.StatusFilled {
		t.Fatalf("unexpected scoped spot order report: %+v", report)
	}

	fills, err := exec.GenerateFillReports(context.Background(), model.FillReportQuery{
		AccountID:    AccountIDUnified,
		InstrumentID: spotID,
		ClientID:     "spot-client",
		VenueOrderID: "spot-order",
	})
	if err != nil {
		t.Fatalf("GenerateFillReports: %v", err)
	}
	if len(fills) != 1 || fills[0].Fill.InstrumentID != spotID {
		t.Fatalf("unexpected scoped spot fill reports: %+v", fills)
	}
}

func TestBybitReportsRejectMismatchedAccountIDBeforeVenueRequest(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		t.Fatalf("unexpected venue request for mismatched account id: %s", r.URL.String())
	}))
	defer server.Close()

	exec := newExecutionClient(
		bybitsdk.NewClient().
			WithCredentials("key", "secret").
			WithBaseURL(server.URL).
			WithHTTPClient(server.Client()),
		bybitTestProvider(),
		clock.NewSimulatedClock(time.Date(2026, 7, 6, 1, 6, 0, 0, time.UTC)),
	)
	spotID := model.InstrumentID{Venue: VenueName, Symbol: "ETH-USDT", Kind: enums.KindSpot}

	orders, err := exec.GenerateOrderStatusReports(context.Background(), model.OrderStatusReportQuery{AccountID: "BYBIT-OTHER", InstrumentID: spotID})
	if err != nil || len(orders) != 0 {
		t.Fatalf("mismatched account order reports=%+v err=%v, want empty nil", orders, err)
	}
	order, err := exec.GenerateOrderStatusReport(context.Background(), model.SingleOrderStatusQuery{AccountID: "BYBIT-OTHER", InstrumentID: spotID, ClientID: "client"})
	if err != nil || order != nil {
		t.Fatalf("mismatched account single order=%+v err=%v, want nil nil", order, err)
	}
	fills, err := exec.GenerateFillReports(context.Background(), model.FillReportQuery{AccountID: "BYBIT-OTHER", InstrumentID: spotID})
	if err != nil || len(fills) != 0 {
		t.Fatalf("mismatched account fill reports=%+v err=%v, want empty nil", fills, err)
	}
	positions, err := exec.GeneratePositionReports(context.Background(), model.PositionReportQuery{AccountID: "BYBIT-OTHER", InstrumentID: spotID})
	if err != nil || len(positions) != 0 {
		t.Fatalf("mismatched account position reports=%+v err=%v, want empty nil", positions, err)
	}
	mass, err := exec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{AccountID: "BYBIT-OTHER", IncludeFills: true, IncludePositions: true})
	if err != nil || mass == nil || mass.AccountID != "BYBIT-OTHER" || len(mass.OrderReports) != 0 || len(mass.FillReports) != 0 || len(mass.PositionReports) != 0 {
		t.Fatalf("mismatched account mass=%+v err=%v, want empty BYBIT-OTHER mass", mass, err)
	}
	if called {
		t.Fatal("mismatched account report crossed HTTP boundary")
	}
}

type bybitAccountFixture struct {
	UnifiedMarginStatus bybitsdk.UnifiedMarginStatus
}

func newBybitAccountServer(t *testing.T, fixture bybitAccountFixture) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v5/account/info":
			writeJSON(t, w, map[string]any{
				"retCode": 0,
				"retMsg":  "OK",
				"result": map[string]any{
					"unifiedMarginStatus": fixture.UnifiedMarginStatus,
					"marginMode":          "REGULAR_MARGIN",
					"spotHedgingStatus":   "OFF",
					"updatedTime":         "1783299600000",
				},
			})
		case "/v5/account/wallet-balance":
			writeJSON(t, w, map[string]any{
				"retCode": 0,
				"retMsg":  "OK",
				"result": map[string]any{"list": []any{map[string]any{
					"accountType":           "UNIFIED",
					"totalEquity":           "1000",
					"totalAvailableBalance": "900",
					"totalPerpUPL":          "5",
					"totalWalletBalance":    "995",
					"coin": []any{
						map[string]any{"coin": "USDT", "equity": "700", "walletBalance": "695", "locked": "10", "usdValue": "700", "unrealisedPnl": "5"},
						map[string]any{"coin": "USDC", "equity": "300", "walletBalance": "300", "locked": "0", "usdValue": "300"},
					},
				}}},
			})
		case "/v5/position/list":
			writeJSON(t, w, map[string]any{
				"retCode": 0,
				"retMsg":  "OK",
				"result": map[string]any{"list": []any{
					map[string]any{"symbol": "BTCUSDT", "side": "Buy", "size": "0.01", "avgPrice": "50000", "leverage": "2", "unrealisedPnl": "1"},
					map[string]any{"symbol": "BTCPERP", "side": "None", "size": "0", "avgPrice": "0", "leverage": "1", "unrealisedPnl": "0"},
				}},
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func bybitTestProvider() *instrumentProvider {
	provider := newInstrumentProvider()
	provider.LoadSnapshot([]*model.Instrument{
		instrumentFromBybit("spot", bybitsdk.Instrument{
			Symbol: "ETHUSDT", BaseCoin: "ETH", QuoteCoin: "USDT", Status: "Trading",
			PriceFilter:   bybitsdk.PriceFilter{TickSize: "0.01"},
			LotSizeFilter: bybitsdk.LotSizeFilter{BasePrecision: "0.0001", MinOrderQty: "0.001", MinNotionalValue: "5"},
		}),
		instrumentFromBybit("linear", bybitsdk.Instrument{
			Symbol: "BTCUSDT", BaseCoin: "BTC", QuoteCoin: "USDT", SettleCoin: bybitsdk.SettleCoinUSDT, Status: "Trading",
			PriceFilter:   bybitsdk.PriceFilter{TickSize: "0.1"},
			LotSizeFilter: bybitsdk.LotSizeFilter{QtyStep: "0.001", MinOrderQty: "0.001", MinNotionalValue: "5"},
		}),
		instrumentFromBybit("linear", bybitsdk.Instrument{
			Symbol: "BTCPERP", BaseCoin: "BTC", QuoteCoin: "USDC", SettleCoin: bybitsdk.SettleCoinUSDC, Status: "Trading",
			PriceFilter:   bybitsdk.PriceFilter{TickSize: "0.1"},
			LotSizeFilter: bybitsdk.LotSizeFilter{QtyStep: "0.001", MinOrderQty: "0.001", MinNotionalValue: "5"},
		}),
	})
	return provider
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatalf("write json: %v", err)
	}
}
