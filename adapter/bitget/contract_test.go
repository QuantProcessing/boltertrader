package bitget

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/adapter/internal/runtimeaccept"
	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	btruntime "github.com/QuantProcessing/boltertrader/runtime"
	bitgetsdk "github.com/QuantProcessing/boltertrader/sdk/bitget"
	"github.com/shopspring/decimal"
)

func TestBitgetClientsImplementContractsAndCapabilities(t *testing.T) {
	provider := bitgetTestProvider()
	clk := clock.NewSimulatedClock(time.Date(2026, 7, 6, 2, 0, 0, 0, time.UTC))
	rest := bitgetsdk.NewClient().WithCredentials("key", "secret", "pass")

	var _ contract.MarketDataClient = newMarketDataClient(rest, nil, provider, clk)
	var _ contract.ExecutionClient = newExecutionClient(rest, provider, clk)
	var _ contract.AccountClient = newAccountClient(rest, provider, clk, []enums.InstrumentKind{enums.KindSpot, enums.KindPerp})
	var _ contract.AccountStateReporter = newAccountClient(rest, provider, clk, []enums.InstrumentKind{enums.KindSpot, enums.KindPerp})

	if caps := newAccountClient(rest, provider, clk, nil).Capabilities(); !caps.Reports.AccountStateSnapshots || !caps.Streaming.Account {
		t.Fatalf("account capabilities missing account-state/private stream support: %+v", caps)
	}
}

func TestBitgetAccountIDOverridePropagatesToClients(t *testing.T) {
	const accountID = "BITGET-ALT"
	provider := bitgetTestProvider()
	clk := clock.NewSimulatedClock(time.Date(2026, 7, 6, 2, 0, 0, 0, time.UTC))
	rest := bitgetsdk.NewClient().WithCredentials("key", "secret", "pass")

	exec := newExecutionClient(rest, provider, clk, accountID)
	acct := newAccountClient(rest, provider, clk, []enums.InstrumentKind{enums.KindSpot, enums.KindPerp}, accountID)

	if exec.AccountID() != accountID || acct.AccountID() != accountID {
		t.Fatalf("account ids exec=%q acct=%q, want %q", exec.AccountID(), acct.AccountID(), accountID)
	}
}

func TestBitgetAccountStateRequiresUnifiedAndSharedAccountID(t *testing.T) {
	server := newBitgetAccountServer(t, bitgetAccountFixture{AccountMode: "unified"})
	clk := clock.NewSimulatedClock(time.Date(2026, 7, 6, 2, 1, 0, 0, time.UTC))
	acct := newAccountClient(
		bitgetsdk.NewClient().WithCredentials("key", "secret", "pass").WithBaseURL(server.URL).WithHTTPClient(server.Client()),
		bitgetTestProvider(),
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
	if state.ModeInfo.AccountMode != "UNIFIED" || state.ModeInfo.PositionMode != "single_hold" || state.ModeInfo.CollateralMode != "union" {
		t.Fatalf("unexpected mode info: %+v", state.ModeInfo)
	}
	if AccountIDForKind(enums.KindSpot) != AccountIDForKind(enums.KindPerp) {
		t.Fatalf("spot/perp must share Bitget unified account id")
	}
	if len(state.Balances) == 0 || len(state.Margins) == 0 {
		t.Fatalf("expected balances and margins: %+v", state)
	}
}

func TestBitgetAccountStateAcceptsHybridUTAAccountMode(t *testing.T) {
	server := newBitgetAccountServer(t, bitgetAccountFixture{AccountMode: "hybrid"})
	clk := clock.NewSimulatedClock(time.Date(2026, 7, 6, 2, 1, 0, 0, time.UTC))
	acct := newAccountClient(
		bitgetsdk.NewClient().WithCredentials("key", "secret", "pass").WithBaseURL(server.URL).WithHTTPClient(server.Client()),
		bitgetTestProvider(),
		clk,
		[]enums.InstrumentKind{enums.KindSpot, enums.KindPerp},
	)

	state, err := acct.AccountState(context.Background())
	if err != nil {
		t.Fatalf("AccountState: %v", err)
	}
	if state.ModeInfo.AccountMode != "HYBRID" {
		t.Fatalf("account mode=%q, want HYBRID", state.ModeInfo.AccountMode)
	}
	if err := state.ModeInfo.ValidateVerified(); err != nil {
		t.Fatalf("hybrid mode info invalid: %v", err)
	}
}

func TestBitgetAccountStateFailClosedForClassicAndUnknown(t *testing.T) {
	for _, mode := range []string{"classic", "", "portfolio"} {
		server := newBitgetAccountServer(t, bitgetAccountFixture{AccountMode: mode})
		acct := newAccountClient(
			bitgetsdk.NewClient().WithCredentials("key", "secret", "pass").WithBaseURL(server.URL).WithHTTPClient(server.Client()),
			bitgetTestProvider(),
			clock.NewRealClock(),
			[]enums.InstrumentKind{enums.KindSpot, enums.KindPerp},
		)
		if _, err := acct.AccountState(context.Background()); err == nil {
			t.Fatalf("account mode %q must fail closed", mode)
		}
	}
}

func TestBitgetOrderAndFillConversion(t *testing.T) {
	inst := bitgetTestProvider().All()[1]
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
	venue, err := orderRequestToBitget(req, inst)
	if err != nil {
		t.Fatalf("orderRequestToBitget: %v", err)
	}
	if venue.Category != bitgetsdk.ProductTypeUSDTFutures || venue.Symbol != "BTCUSDT" || venue.TimeInForce != "post_only" {
		t.Fatalf("unexpected venue order: %+v", venue)
	}
	if venue.PosSide != "short" {
		t.Fatalf("reduce-only buy posSide=%q, want short", venue.PosSide)
	}
	if venue.TradeSide != "close" {
		t.Fatalf("reduce-only buy tradeSide=%q, want close", venue.TradeSide)
	}
	if venue.ReduceOnly != "" {
		t.Fatalf("hybrid UTA close reduceOnly=%q, want omitted", venue.ReduceOnly)
	}
	closeLongReq := req
	closeLongReq.Side = enums.SideSell
	closeLongReq.ReduceOnly = true
	closeLongVenue, err := orderRequestToBitget(closeLongReq, inst)
	if err != nil {
		t.Fatalf("close long orderRequestToBitget: %v", err)
	}
	if closeLongVenue.PosSide != "long" {
		t.Fatalf("reduce-only sell posSide=%q, want long", closeLongVenue.PosSide)
	}
	if closeLongVenue.TradeSide != "close" {
		t.Fatalf("reduce-only sell tradeSide=%q, want close", closeLongVenue.TradeSide)
	}
	if closeLongVenue.ReduceOnly != "" {
		t.Fatalf("hybrid UTA close reduceOnly=%q, want omitted", closeLongVenue.ReduceOnly)
	}
	openReq := req
	openReq.ReduceOnly = false
	openVenue, err := orderRequestToBitget(openReq, inst)
	if err != nil {
		t.Fatalf("open orderRequestToBitget: %v", err)
	}
	if openVenue.TradeSide != "" {
		t.Fatalf("UTA opening perp tradeSide=%q, want omitted", openVenue.TradeSide)
	}
	if openVenue.PosSide != "long" {
		t.Fatalf("opening buy posSide=%q, want long", openVenue.PosSide)
	}
	order := orderFromBitgetRecord(bitgetsdk.OrderRecord{
		OrderID: "42", ClientOID: "client-1", Symbol: "BTCUSDT", Category: bitgetsdk.ProductTypeUSDTFutures, Side: "buy", OrderType: "limit",
		TimeInForce: "post_only", Qty: "0.01", Price: "50000", FilledQty: "0.005", AvgPrice: "50001", OrderStatus: "partially_filled",
	}, inst.ID, AccountIDUnified)
	if order.Status != enums.StatusPartiallyFilled || !order.FilledQty.Equal(decimal.RequireFromString("0.005")) {
		t.Fatalf("unexpected order: %+v", order)
	}
	fill := fillFromBitget(bitgetsdk.FillRecord{
		ExecID: "trade-1", OrderID: "42", ClientOID: "client-1", Symbol: "BTCUSDT", Side: "buy",
		ExecPrice: "50001", ExecQty: "0.005", ExecTime: "1783299600000",
		FeeDetail: []bitgetsdk.FeeDetail{{FeeCoin: "USDT", Fee: "0.01"}},
	}, inst.ID, AccountIDUnified)
	if fill.AccountID != AccountIDUnified || fill.FeeCurrency != "USDT" || fill.Timestamp.IsZero() {
		t.Fatalf("unexpected fill: %+v", fill)
	}
}

func TestBitgetRuntimeResyncUsesAccountStateFirst(t *testing.T) {
	server := newBitgetAccountServer(t, bitgetAccountFixture{AccountMode: "unified"})
	provider := bitgetTestProvider()
	clk := clock.NewSimulatedClock(time.Date(2026, 7, 6, 2, 2, 0, 0, time.UTC))
	rest := bitgetsdk.NewClient().WithCredentials("key", "secret", "pass").WithBaseURL(server.URL).WithHTTPClient(server.Client())
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

func TestBitgetScopedReportsPreserveSpotInstrumentForAmbiguousVenueSymbol(t *testing.T) {
	spotID := model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT", Kind: enums.KindSpot}
	provider := newInstrumentProvider()
	provider.LoadSnapshot([]*model.Instrument{
		instrumentFromBitget(bitgetsdk.Instrument{Category: "SPOT", Symbol: "BTCUSDT", BaseCoin: "BTC", QuoteCoin: "USDT", Status: "online"}),
		instrumentFromBitget(bitgetsdk.Instrument{Category: bitgetsdk.ProductTypeUSDTFutures, Symbol: "BTCUSDT", BaseCoin: "BTC", QuoteCoin: "USDT", Status: "online"}),
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("category"); got != "SPOT" {
			t.Fatalf("category=%q, want SPOT", got)
		}
		switch r.URL.Path {
		case "/api/v3/trade/order-info":
			if got := r.URL.Query().Get("symbol"); got != "BTCUSDT" {
				t.Fatalf("symbol=%q, want BTCUSDT", got)
			}
			writeJSON(t, w, map[string]any{"code": "00000", "msg": "success", "data": map[string]any{
				"orderId":     "spot-order",
				"clientOid":   "spot-client",
				"symbol":      "BTCUSDT",
				"category":    "SPOT",
				"side":        "buy",
				"orderType":   "limit",
				"timeInForce": "ioc",
				"qty":         "0.0001",
				"price":       "60000",
				"filledQty":   "0.0001",
				"avgPrice":    "59999",
				"orderStatus": "filled",
			}})
		case "/api/v3/trade/fills":
			writeJSON(t, w, map[string]any{"code": "00000", "msg": "success", "data": map[string]any{"list": []any{
				map[string]any{
					"execId":    "spot-fill",
					"orderId":   "spot-order",
					"clientOid": "spot-client",
					"symbol":    "BTCUSDT",
					"side":      "buy",
					"execPrice": "59999",
					"execQty":   "0.0001",
					"execTime":  "1783299600000",
				},
			}}})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	rest := bitgetsdk.NewClient().
		WithCredentials("key", "secret", "pass").
		WithBaseURL(server.URL).
		WithHTTPClient(server.Client())
	exec := newExecutionClient(rest, provider, clock.NewSimulatedClock(time.Date(2026, 7, 6, 2, 3, 0, 0, time.UTC)))

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

func TestBitgetReportsRejectMismatchedAccountIDBeforeVenueRequest(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		t.Fatalf("unexpected venue request for mismatched account id: %s", r.URL.String())
	}))
	defer server.Close()

	exec := newExecutionClient(
		bitgetsdk.NewClient().
			WithCredentials("key", "secret", "pass").
			WithBaseURL(server.URL).
			WithHTTPClient(server.Client()),
		bitgetTestProvider(),
		clock.NewSimulatedClock(time.Date(2026, 7, 6, 2, 4, 0, 0, time.UTC)),
	)
	spotID := model.InstrumentID{Venue: VenueName, Symbol: "ETH-USDT", Kind: enums.KindSpot}

	orders, err := exec.GenerateOrderStatusReports(context.Background(), model.OrderStatusReportQuery{AccountID: "BITGET-OTHER", InstrumentID: spotID})
	if err != nil || len(orders) != 0 {
		t.Fatalf("mismatched account order reports=%+v err=%v, want empty nil", orders, err)
	}
	order, err := exec.GenerateOrderStatusReport(context.Background(), model.SingleOrderStatusQuery{AccountID: "BITGET-OTHER", InstrumentID: spotID, ClientID: "client"})
	if err != nil || order != nil {
		t.Fatalf("mismatched account single order=%+v err=%v, want nil nil", order, err)
	}
	fills, err := exec.GenerateFillReports(context.Background(), model.FillReportQuery{AccountID: "BITGET-OTHER", InstrumentID: spotID})
	if err != nil || len(fills) != 0 {
		t.Fatalf("mismatched account fill reports=%+v err=%v, want empty nil", fills, err)
	}
	positions, err := exec.GeneratePositionReports(context.Background(), model.PositionReportQuery{AccountID: "BITGET-OTHER", InstrumentID: spotID})
	if err != nil || len(positions) != 0 {
		t.Fatalf("mismatched account position reports=%+v err=%v, want empty nil", positions, err)
	}
	mass, err := exec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{AccountID: "BITGET-OTHER", IncludeFills: true, IncludePositions: true})
	if err != nil || mass == nil || mass.AccountID != "BITGET-OTHER" || len(mass.OrderReports) != 0 || len(mass.FillReports) != 0 || len(mass.PositionReports) != 0 {
		t.Fatalf("mismatched account mass=%+v err=%v, want empty BITGET-OTHER mass", mass, err)
	}
	if called {
		t.Fatal("mismatched account report crossed HTTP boundary")
	}
}

type bitgetAccountFixture struct {
	AccountMode string
}

func newBitgetAccountServer(t *testing.T, fixture bitgetAccountFixture) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v3/account/info":
			writeJSON(t, w, map[string]any{"code": "00000", "msg": "success", "data": map[string]any{"userId": "u1", "permType": "uta", "permissions": []string{"spot", "contract"}}})
		case "/api/v3/account/settings":
			writeJSON(t, w, map[string]any{"code": "00000", "msg": "success", "data": map[string]any{
				"accountMode":  fixture.AccountMode,
				"assetMode":    "union",
				"accountLevel": "trader",
				"holdMode":     "single_hold",
				"symbolSettings": []any{
					map[string]any{"symbol": "BTCUSDT", "category": bitgetsdk.ProductTypeUSDTFutures, "marginMode": "crossed"},
				},
			}})
		case "/api/v3/account/assets":
			writeJSON(t, w, map[string]any{"code": "00000", "msg": "success", "data": map[string]any{
				"accountEquity":    "1000",
				"usdtEquity":       "700",
				"available":        "900",
				"unrealizedPL":     "5",
				"unionTotalMargin": "10",
				"assets": []any{
					map[string]any{"coin": "USDT", "available": "690", "frozen": "10", "equity": "700", "usdtValue": "700"},
					map[string]any{"coin": "USDC", "available": "300", "frozen": "0", "equity": "300", "usdtValue": "300"},
				},
			}})
		case "/api/v3/position/current-position":
			writeJSON(t, w, map[string]any{"code": "00000", "msg": "success", "data": map[string]any{"list": []any{
				map[string]any{"symbol": "BTCUSDT", "category": bitgetsdk.ProductTypeUSDTFutures, "posSide": "long", "qty": "0.01", "avgPrice": "50000", "markPrice": "50001", "leverage": "2", "unrealizedPL": "1", "updatedTime": "1783299600000"},
			}}})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func bitgetTestProvider() *instrumentProvider {
	provider := newInstrumentProvider()
	provider.LoadSnapshot([]*model.Instrument{
		instrumentFromBitget(bitgetsdk.Instrument{Category: "SPOT", Symbol: "ETHUSDT", BaseCoin: "ETH", QuoteCoin: "USDT", Status: "online", PricePrecision: "2", QuantityPrecision: "4"}),
		instrumentFromBitget(bitgetsdk.Instrument{Category: bitgetsdk.ProductTypeUSDTFutures, Symbol: "BTCUSDT", BaseCoin: "BTC", QuoteCoin: "USDT", Status: "online", PricePrecision: "1", QuantityPrecision: "3"}),
		instrumentFromBitget(bitgetsdk.Instrument{Category: bitgetsdk.ProductTypeUSDCFutures, Symbol: "BTCPERP", BaseCoin: "BTC", QuoteCoin: "USDC", Status: "online", PricePrecision: "1", QuantityPrecision: "3"}),
	})
	return provider
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatalf("write json: %v", err)
	}
}
