package spot

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/contract/contracttest"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	sdkspot "github.com/QuantProcessing/boltertrader/sdk/binance/spot"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"
)

var (
	_ contract.ExecutionClient      = (*executionClient)(nil)
	_ contract.AccountClient        = (*accountClient)(nil)
	_ contract.AccountStateReporter = (*accountClient)(nil)
	_ contract.MarketDataClient     = (*marketDataClient)(nil)
)

func TestAccountIDOverridePropagatesToClients(t *testing.T) {
	const accountID = "BINANCE-ALT"
	provider := newInstrumentProvider()
	clk := clock.NewRealClock()

	exec := newExecutionClient(nil, provider, clk, accountID)
	acct := newAccountClient(nil, provider, clk, accountID)

	if exec.AccountID() != accountID || acct.AccountID() != accountID {
		t.Fatalf("account ids exec=%q acct=%q, want %q", exec.AccountID(), acct.AccountID(), accountID)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func testSpotInstrument() *model.Instrument {
	return instrumentFromSymbolInfo(&sdkspot.SymbolInfo{
		Symbol:             "ETHUSDT",
		Status:             "TRADING",
		BaseAsset:          "ETH",
		QuoteAsset:         "USDT",
		BaseAssetPrecision: 8,
		QuotePrecision:     8,
		Filters: []map[string]any{
			{"filterType": "PRICE_FILTER", "tickSize": "0.01"},
			{"filterType": "LOT_SIZE", "stepSize": "0.0001", "minQty": "0.0001"},
			{"filterType": "MIN_NOTIONAL", "minNotional": "5"},
		},
	})
}

func testProvider(inst *model.Instrument) *instrumentProvider {
	p := newInstrumentProvider()
	p.byID[inst.ID.String()] = inst
	p.bySymbol[inst.VenueSymbol] = inst.ID
	p.all = []*model.Instrument{inst}
	return p
}

func testREST(handler func(*http.Request) (string, int)) *sdkspot.Client {
	return &sdkspot.Client{
		BaseURL:   "https://unit.test",
		APIKey:    "api-key",
		SecretKey: "secret",
		Logger:    zap.NewNop().Sugar(),
		HTTPClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			body, status := handler(r)
			if status == 0 {
				status = http.StatusOK
			}
			return &http.Response{
				StatusCode: status,
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     make(http.Header),
			}, nil
		})},
	}
}

func TestBinanceSpotInstrumentTranslation(t *testing.T) {
	inst := testSpotInstrument()
	if inst == nil {
		t.Fatal("instrumentFromSymbolInfo returned nil")
	}
	if inst.ID.Kind != enums.KindSpot {
		t.Fatalf("kind=%v, want SPOT", inst.ID.Kind)
	}
	if inst.Settle != "USDT" {
		t.Fatalf("settle=%q, want quote currency", inst.Settle)
	}
	if inst.PositionMode != model.NetOnly {
		t.Fatalf("spot position mode=%v, want NetOnly", inst.PositionMode)
	}

	contracttest.RunInstrumentParsing(t, testProvider(inst), []contracttest.InstrumentExpectation{{
		ID:          model.InstrumentID{Venue: "BINANCE", Symbol: "ETH-USDT", Kind: enums.KindSpot},
		PriceTick:   d("0.01"),
		SizeStep:    d("0.0001"),
		MinNotional: d("5"),
		VenueSymbol: "ETHUSDT",
		HasIntCode:  false,
		HasAssetIdx: false,
	}})
}

func TestBinanceSpotDepthTranslation(t *testing.T) {
	inst := testSpotInstrument()
	var gotQuery url.Values
	rest := testREST(func(r *http.Request) (string, int) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v3/depth" {
			t.Fatalf("request=%s %s, want GET /api/v3/depth", r.Method, r.URL.Path)
		}
		gotQuery = r.URL.Query()
		return `{"lastUpdateId":42,"bids":[["3000.01","0.5"]],"asks":[["3000.02","0.7"]]}`, 200
	})
	market := newMarketDataClient(rest, nil, testProvider(inst), clock.NewRealClock())

	book, err := market.OrderBook(context.Background(), inst.ID, 5)
	if err != nil {
		t.Fatalf("OrderBook: %v", err)
	}
	if gotQuery.Get("symbol") != "ETHUSDT" || gotQuery.Get("limit") != "5" {
		t.Fatalf("query=%s, want symbol ETHUSDT limit 5", gotQuery.Encode())
	}
	if book.Sequence != 42 || !book.Bids[0].Price.Equal(d("3000.01")) || !book.Asks[0].Quantity.Equal(d("0.7")) {
		t.Fatalf("book=%+v", book)
	}
}

func TestBinanceSpotSubmitOrderRequestTranslation(t *testing.T) {
	inst := testSpotInstrument()
	var submit url.Values
	rest := testREST(func(r *http.Request) (string, int) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v3/order" {
			t.Fatalf("request=%s %s, want POST /api/v3/order", r.Method, r.URL.Path)
		}
		submit = r.URL.Query()
		return `{"orderId":555,"clientOrderId":"c-spot-1","symbol":"ETHUSDT","status":"NEW","side":"BUY","type":"LIMIT","timeInForce":"GTC","origQty":"0.0100","price":"3000.01","executedQty":"0","cummulativeQuoteQty":"0"}`, 200
	})
	exec := newExecutionClient(rest, testProvider(inst), clock.NewRealClock())

	order, err := exec.Submit(context.Background(), model.OrderRequest{
		InstrumentID: inst.ID,
		ClientID:     "c-spot-1",
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     d("0.0100"),
		Price:        d("3000.01"),
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if submit.Get("symbol") != "ETHUSDT" || submit.Get("side") != "BUY" || submit.Get("type") != "LIMIT" {
		t.Fatalf("submit query=%s", submit.Encode())
	}
	if submit.Get("reduceOnly") != "" || submit.Get("positionSide") != "" {
		t.Fatalf("spot submit leaked derivative-only fields: %s", submit.Encode())
	}
	if submit.Get("newOrderRespType") != "FULL" {
		t.Fatalf("newOrderRespType=%q, want FULL", submit.Get("newOrderRespType"))
	}
	if order.VenueOrderID != "555" || order.Request.PositionSide != enums.PosNet || order.Request.ReduceOnly {
		t.Fatalf("order=%+v", order)
	}
}

func TestBinanceSpotSubmitImmediateFillEmitsFillEvent(t *testing.T) {
	inst := testSpotInstrument()
	rest := testREST(func(r *http.Request) (string, int) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v3/order" {
			t.Fatalf("request=%s %s, want POST /api/v3/order", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("newOrderRespType"); got != "FULL" {
			t.Fatalf("newOrderRespType=%q, want FULL", got)
		}
		return `{
			"orderId":556,
			"clientOrderId":"c-spot-fill",
			"symbol":"ETHUSDT",
			"status":"FILLED",
			"side":"BUY",
			"type":"LIMIT",
			"timeInForce":"IOC",
			"origQty":"0.0100",
			"price":"3001.00",
			"executedQty":"0.0100",
			"cummulativeQuoteQty":"30.01",
			"transactTime":1700000000123,
			"fills":[
				{"price":"3001.00","qty":"0.0100","commission":"0.003","commissionAsset":"BNB","tradeId":789}
			]
		}`, 200
	})
	exec := newExecutionClient(rest, testProvider(inst), clock.NewRealClock())

	order, err := exec.Submit(context.Background(), model.OrderRequest{
		InstrumentID: inst.ID,
		ClientID:     "c-spot-fill",
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifIOC,
		Quantity:     d("0.0100"),
		Price:        d("3001.00"),
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if order.Status != enums.StatusFilled || !order.FilledQty.Equal(d("0.0100")) {
		t.Fatalf("order=%+v, want filled order", order)
	}

	var got []contract.ExecEvent
	for i := 0; i < 2; i++ {
		select {
		case env := <-exec.Events():
			if env.Source != contract.SourceAdapterREST || !env.Flags.Has(contract.EventFlagSynthetic) {
				t.Fatalf("event %d source=%s flags=%d, want REST synthetic", i, env.Source, env.Flags)
			}
			got = append(got, env.Payload)
		default:
			t.Fatalf("event %d missing; got=%#v", i, got)
		}
	}
	if _, ok := got[0].(contract.OrderEvent); !ok {
		t.Fatalf("event[0]=%T, want OrderEvent", got[0])
	}
	fillEvent, ok := got[1].(contract.FillEvent)
	if !ok {
		t.Fatalf("event[1]=%T, want FillEvent", got[1])
	}
	if fillEvent.Fill.ClientID != "c-spot-fill" ||
		fillEvent.Fill.VenueOrderID != "556" ||
		fillEvent.Fill.TradeID != "789" ||
		!fillEvent.Fill.Price.Equal(d("3001.00")) ||
		!fillEvent.Fill.Quantity.Equal(d("0.0100")) ||
		fillEvent.Fill.FeeCurrency != "BNB" ||
		fillEvent.Fill.AccountID != model.AccountIDBinanceDefault {
		t.Fatalf("fill event=%+v", fillEvent.Fill)
	}
}

func TestBinanceSpotSubmitTouchedOrdersUseTakeProfitTypes(t *testing.T) {
	inst := testSpotInstrument()
	cases := []struct {
		name        string
		req         model.OrderRequest
		wantType    string
		wantTIF     string
		wantPrice   string
		wantTrigger string
	}{
		{
			name: "market if touched",
			req: model.OrderRequest{
				InstrumentID: inst.ID,
				ClientID:     "c-mit",
				Side:         enums.SideSell,
				Type:         enums.TypeMarketIfTouched,
				Quantity:     d("0.0100"),
				TriggerPrice: d("3100.00"),
			},
			wantType: "TAKE_PROFIT", wantTrigger: "3100",
		},
		{
			name: "limit if touched",
			req: model.OrderRequest{
				InstrumentID: inst.ID,
				ClientID:     "c-lit",
				Side:         enums.SideSell,
				Type:         enums.TypeLimitIfTouched,
				TIF:          enums.TifGTC,
				Quantity:     d("0.0100"),
				Price:        d("3099.50"),
				TriggerPrice: d("3100.00"),
			},
			wantType: "TAKE_PROFIT_LIMIT", wantTIF: "GTC", wantPrice: "3099.5", wantTrigger: "3100",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var submit url.Values
			rest := testREST(func(r *http.Request) (string, int) {
				if r.Method != http.MethodPost || r.URL.Path != "/api/v3/order" {
					t.Fatalf("request=%s %s, want POST /api/v3/order", r.Method, r.URL.Path)
				}
				submit = r.URL.Query()
				return `{"orderId":555,"clientOrderId":"` + tc.req.ClientID + `","symbol":"ETHUSDT","status":"NEW","side":"SELL","type":"` + tc.wantType + `","timeInForce":"` + tc.wantTIF + `","origQty":"0.0100","price":"` + tc.wantPrice + `","executedQty":"0","cummulativeQuoteQty":"0"}`, 200
			})
			exec := newExecutionClient(rest, testProvider(inst), clock.NewRealClock())

			if _, err := exec.Submit(context.Background(), tc.req); err != nil {
				t.Fatalf("Submit: %v", err)
			}
			if submit.Get("type") != tc.wantType || submit.Get("stopPrice") != tc.wantTrigger {
				t.Fatalf("submit query=%s", submit.Encode())
			}
			if submit.Get("timeInForce") != tc.wantTIF {
				t.Fatalf("timeInForce=%q, want %q", submit.Get("timeInForce"), tc.wantTIF)
			}
		})
	}
}

func TestBinanceSpotRejectsDerivativeOrderFields(t *testing.T) {
	inst := testSpotInstrument()
	exec := newExecutionClient(testREST(func(r *http.Request) (string, int) {
		t.Fatalf("spot derivative-field rejection must happen before REST request: %s", r.URL.String())
		return `{}`, 500
	}), testProvider(inst), clock.NewRealClock())

	for name, req := range map[string]model.OrderRequest{
		"reduce_only": {
			InstrumentID: inst.ID,
			ClientID:     "c-reduce-only",
			Side:         enums.SideSell,
			Type:         enums.TypeMarket,
			Quantity:     d("0.01"),
			ReduceOnly:   true,
		},
		"position_side": {
			InstrumentID: inst.ID,
			ClientID:     "c-position-side",
			Side:         enums.SideBuy,
			Type:         enums.TypeMarket,
			Quantity:     d("0.01"),
			PositionSide: enums.PosLong,
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := exec.Submit(context.Background(), req)
			if !errors.Is(err, contract.ErrNotSupported) {
				t.Fatalf("Submit err=%v, want ErrNotSupported", err)
			}
		})
	}
}

func TestBinanceSpotCancelOrderTranslation(t *testing.T) {
	inst := testSpotInstrument()
	var cancel url.Values
	rest := testREST(func(r *http.Request) (string, int) {
		if r.Method != http.MethodDelete || r.URL.Path != "/api/v3/order" {
			t.Fatalf("request=%s %s, want DELETE /api/v3/order", r.Method, r.URL.Path)
		}
		cancel = r.URL.Query()
		return `{"orderId":555,"clientOrderId":"c-spot-1","symbol":"ETHUSDT","status":"CANCELED","side":"BUY","type":"LIMIT","timeInForce":"GTC","origQty":"0.0100","price":"3000.01","executedQty":"0","cummulativeQuoteQty":"0"}`, 200
	})
	exec := newExecutionClient(rest, testProvider(inst), clock.NewRealClock())

	if err := exec.Cancel(context.Background(), inst.ID, "555"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if cancel.Get("symbol") != "ETHUSDT" || cancel.Get("orderId") != "555" {
		t.Fatalf("cancel query=%s", cancel.Encode())
	}
}

func TestBinanceSpotOpenOrdersTranslation(t *testing.T) {
	inst := testSpotInstrument()
	rest := testREST(func(r *http.Request) (string, int) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v3/openOrders" {
			t.Fatalf("request=%s %s, want GET /api/v3/openOrders", r.Method, r.URL.Path)
		}
		return `[{"orderId":777,"clientOrderId":"c-open","symbol":"ETHUSDT","status":"NEW","side":"SELL","type":"LIMIT","timeInForce":"GTC","origQty":"0.0200","price":"3200.00","executedQty":"0","cummulativeQuoteQty":"0"}]`, 200
	})
	exec := newExecutionClient(rest, testProvider(inst), clock.NewRealClock())

	mass, err := exec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{})
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	if len(mass.OrderReports) != 1 {
		t.Fatalf("orders len=%d", len(mass.OrderReports))
	}
	report := mass.OrderReports["777"]
	order := report.Order
	if order.Request.InstrumentID != inst.ID || order.VenueOrderID != "777" || order.Request.Side != enums.SideSell {
		t.Fatalf("order=%+v", order)
	}
	if !order.Request.Quantity.Equal(d("0.0200")) || !order.Request.Price.Equal(d("3200.00")) {
		t.Fatalf("order qty/price=%s/%s, want 0.0200/3200.00", order.Request.Quantity, order.Request.Price)
	}
}

func TestBinanceSpotReportsRejectMismatchedAccountIDBeforeVenueRequest(t *testing.T) {
	called := false
	inst := testSpotInstrument()
	rest := testREST(func(r *http.Request) (string, int) {
		called = true
		t.Fatalf("unexpected venue request for mismatched account id: %s", r.URL.String())
		return "", 0
	})
	exec := newExecutionClient(rest, testProvider(inst), clock.NewRealClock())

	orders, err := exec.GenerateOrderStatusReports(context.Background(), model.OrderStatusReportQuery{AccountID: "BINANCE-OTHER", InstrumentID: inst.ID})
	if err != nil || len(orders) != 0 {
		t.Fatalf("mismatched account order reports=%+v err=%v, want empty nil", orders, err)
	}
	order, err := exec.GenerateOrderStatusReport(context.Background(), model.SingleOrderStatusQuery{AccountID: "BINANCE-OTHER", InstrumentID: inst.ID, ClientID: "client"})
	if err != nil || order != nil {
		t.Fatalf("mismatched account single order=%+v err=%v, want nil nil", order, err)
	}
	fills, err := exec.GenerateFillReports(context.Background(), model.FillReportQuery{AccountID: "BINANCE-OTHER", InstrumentID: inst.ID})
	if err != nil || len(fills) != 0 {
		t.Fatalf("mismatched account fill reports=%+v err=%v, want empty nil", fills, err)
	}
	positions, err := exec.GeneratePositionReports(context.Background(), model.PositionReportQuery{AccountID: "BINANCE-OTHER", InstrumentID: inst.ID})
	if err != nil || len(positions) != 0 {
		t.Fatalf("mismatched account position reports=%+v err=%v, want empty nil", positions, err)
	}
	mass, err := exec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{AccountID: "BINANCE-OTHER", IncludeFills: true, IncludePositions: true})
	if err != nil || mass == nil || mass.AccountID != "BINANCE-OTHER" || len(mass.OrderReports) != 0 || len(mass.FillReports) != 0 || len(mass.PositionReports) != 0 {
		t.Fatalf("mismatched account mass=%+v err=%v, want empty BINANCE-OTHER mass", mass, err)
	}
	if called {
		t.Fatal("mismatched account report crossed HTTP boundary")
	}
}

func TestBinanceSpotAccountBalancesTranslation(t *testing.T) {
	inst := testSpotInstrument()
	rest := testREST(func(r *http.Request) (string, int) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v3/account" {
			t.Fatalf("request=%s %s, want GET /api/v3/account", r.Method, r.URL.Path)
		}
		return `{"updateTime":1700000000000,"accountType":"SPOT","balances":[{"asset":"USDT","free":"100.5","locked":"2.25"},{"asset":"ETH","free":"0.3","locked":"0.1"}]}`, 200
	})
	acct := newAccountClient(rest, testProvider(inst), clock.NewRealClock())

	bals, err := acct.Balances(context.Background())
	if err != nil {
		t.Fatalf("Balances: %v", err)
	}
	if len(bals) != 2 {
		t.Fatalf("balances len=%d", len(bals))
	}
	if bals[0].Currency != "USDT" || !bals[0].Available.Equal(d("100.5")) || !bals[0].Locked.Equal(d("2.25")) || !bals[0].Total.Equal(d("102.75")) {
		t.Fatalf("balance[0]=%+v", bals[0])
	}
	if !bals[0].Free.Equal(d("100.5")) {
		t.Fatalf("balance[0].Free=%s, want 100.5", bals[0].Free)
	}
	if !bals[0].CashInvariantOK() || !bals[1].CashInvariantOK() {
		t.Fatalf("spot balances must satisfy cash invariant: %+v", bals)
	}
	state, err := acct.AccountState(context.Background())
	if err != nil {
		t.Fatalf("AccountState: %v", err)
	}
	if state.AccountID != model.AccountIDBinanceDefault || state.Venue != venueName || state.Type != model.AccountCash {
		t.Fatalf("account state identity/type=%+v", state)
	}
	if !state.Reported || state.EventID == "" || state.TsInit.IsZero() {
		t.Fatalf("account state envelope incomplete: %+v", state)
	}
	if state.TsEvent.UnixMilli() != 1700000000000 {
		t.Fatalf("TsEvent=%s, want REST updateTime", state.TsEvent)
	}
	if got := state.Balances[0].Free; !got.Equal(d("100.5")) {
		t.Fatalf("state balance free=%s, want 100.5", got)
	}
	pos, err := acct.Positions(context.Background())
	if err != nil {
		t.Fatalf("Positions: %v", err)
	}
	if len(pos) != 0 {
		t.Fatalf("spot positions len=%d, want 0", len(pos))
	}
	if err := acct.SetLeverage(context.Background(), inst.ID, d("2")); !errors.Is(err, contract.ErrNotSupported) {
		t.Fatalf("SetLeverage err=%v, want ErrNotSupported", err)
	}
	if err := acct.SetMarginMode(context.Background(), inst.ID, "cross"); !errors.Is(err, contract.ErrNotSupported) {
		t.Fatalf("SetMarginMode err=%v, want ErrNotSupported", err)
	}
}

func TestBinanceSpotUserDataOrderUpdateTranslation(t *testing.T) {
	const golden = `{
		"e":"executionReport","E":1700000000000,"s":"ETHUSDT","c":"c-fill","S":"BUY","o":"LIMIT","f":"GTC",
		"q":"0.0100","p":"3000.00","x":"TRADE","X":"FILLED","i":987,"l":"0.0100","z":"0.0100",
		"L":"3000.00","n":"0.003","N":"BNB","T":1700000000123,"t":456,"m":false
	}`
	var ev sdkspot.ExecutionReportEvent
	if err := json.Unmarshal([]byte(golden), &ev); err != nil {
		t.Fatalf("unmarshal golden: %v", err)
	}

	events := execEventsFromExecutionReport(&ev, func(sym string) model.InstrumentID {
		if sym != "ETHUSDT" {
			t.Fatalf("resolve symbol=%q", sym)
		}
		return model.InstrumentID{Venue: venueName, Symbol: "ETH-USDT", Kind: enums.KindSpot}
	}, model.AccountIDBinanceDefault)
	if len(events) != 2 {
		t.Fatalf("events len=%d, want order+fill", len(events))
	}
	oe, ok := events[0].(contract.OrderEvent)
	if !ok {
		t.Fatalf("events[0]=%T, want OrderEvent", events[0])
	}
	if oe.Order.Request.PositionSide != enums.PosNet || oe.Order.Request.ReduceOnly {
		t.Fatalf("spot order leaked derivative fields: %+v", oe.Order.Request)
	}
	if oe.Order.Request.AccountID != model.AccountIDBinanceDefault {
		t.Fatalf("order account_id=%q", oe.Order.Request.AccountID)
	}
	fe, ok := events[1].(contract.FillEvent)
	if !ok {
		t.Fatalf("events[1]=%T, want FillEvent", events[1])
	}
	if fe.Fill.TradeID != "456" || !fe.Fill.Price.Equal(d("3000.00")) || fe.Fill.FeeCurrency != "BNB" {
		t.Fatalf("fill=%+v", fe.Fill)
	}
	if fe.Fill.AccountID != model.AccountIDBinanceDefault {
		t.Fatalf("fill account_id=%q", fe.Fill.AccountID)
	}
}

func TestBinanceSpotUserDataBalanceUpdateTranslation(t *testing.T) {
	ev := sdkspot.AccountPositionEvent{
		EventTime: 1700000000000,
		Balances: []struct {
			Asset  string `json:"a"`
			Free   string `json:"f"`
			Locked string `json:"l"`
		}{
			{Asset: "USDT", Free: "99", Locked: "1"},
			{Asset: "ETH", Free: "0.2", Locked: "0.05"},
		},
	}

	events := accountEventsFromAccountPosition(&ev, model.AccountIDBinanceDefault)
	if len(events) != 2 {
		t.Fatalf("events len=%d", len(events))
	}
	be, ok := events[0].(contract.BalanceEvent)
	if !ok {
		t.Fatalf("events[0]=%T, want BalanceEvent", events[0])
	}
	if be.Balance.Currency != "USDT" || !be.Balance.Total.Equal(d("100")) || !be.Balance.Free.Equal(d("99")) || !be.Balance.Available.Equal(d("99")) || !be.Balance.Locked.Equal(d("1")) {
		t.Fatalf("balance event=%+v", be.Balance)
	}
	if be.Balance.AccountID != model.AccountIDBinanceDefault {
		t.Fatalf("balance account_id=%q", be.Balance.AccountID)
	}
}

func TestBinanceSpotContractCapabilities(t *testing.T) {
	inst := testSpotInstrument()
	provider := testProvider(inst)
	restOnly := newMarketDataClient(nil, nil, provider, clock.NewRealClock())
	acct := newAccountClient(testREST(func(r *http.Request) (string, int) {
		return `{"balances":[]}`, 200
	}), provider, clock.NewRealClock())
	if caps := acct.Capabilities(); !caps.Reports.AccountStateSnapshots || caps.Streaming.AccountState {
		t.Fatalf("account state capability flags=%+v, want report snapshot true and stream false", caps)
	}

	contracttest.RunSpotCapabilitySuite(t, contracttest.SpotCapabilitySuite{
		Venue: "BINANCE",
		Market: contracttest.MarketCapabilities{
			OrderBook:       contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter fixture and demo data tests")},
			Bars:            contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter fixture and demo data tests")},
			SubscribeBook:   contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter fixture and demo data tests")},
			SubscribeQuotes: contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter fixture and demo data tests")},
			SubscribeTrades: contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter fixture and demo data tests")},
			Reconnect:       contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter fixture and demo data tests")},
			RESTOnlyStreams: contracttest.CapabilityProbe{Support: contracttest.Unsupported("REST-only Binance Spot client has no market websocket"), Probe: func(ctx context.Context) error {
				return restOnly.SubscribeTrades(ctx, inst.ID)
			}},
			RESTOnlyReconnect: contracttest.CapabilityProbe{Support: contracttest.Unsupported("REST-only Binance Spot client has no market websocket"), Probe: func(ctx context.Context) error {
				return restOnly.Reconnect(ctx)
			}},
		},
		Execution: contracttest.ExecutionCapabilities{
			Submit:     contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter fixture and demo exec tests")},
			Cancel:     contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter fixture and demo exec tests")},
			CancelAll:  contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter fixture and demo exec tests")},
			Modify:     contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter fixture and demo exec tests")},
			OpenOrders: contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter fixture and demo exec tests")},
			MassStatus: contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter fixture and demo exec tests")},
		},
		Account: contracttest.AccountCapabilities{
			AccountState: contracttest.CapabilityProbe{Support: contracttest.Supported(), Probe: func(ctx context.Context) error {
				state, err := acct.AccountState(ctx)
				if err != nil {
					return err
				}
				if err := state.Validate(); err != nil {
					return err
				}
				if !state.Reported || state.EventID == "" || state.TsEvent.IsZero() || state.TsInit.IsZero() {
					return errors.New("account state envelope incomplete")
				}
				return nil
			}},
			Balances: contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter fixture and demo account tests")},
			Positions: contracttest.CapabilityProbe{Support: contracttest.Supported(), Probe: func(ctx context.Context) error {
				positions, err := acct.Positions(ctx)
				if err != nil {
					return err
				}
				if len(positions) != 0 {
					return errors.New("spot account returned positions")
				}
				return nil
			}},
			SetLeverage: contracttest.CapabilityProbe{Support: contracttest.Unsupported("spot cash account has no leverage"), Probe: func(ctx context.Context) error {
				return acct.SetLeverage(ctx, inst.ID, d("2"))
			}},
			SetCrossMargin: contracttest.CapabilityProbe{Support: contracttest.Unsupported("spot cash account has no cross margin mode"), Probe: func(ctx context.Context) error {
				return acct.SetMarginMode(ctx, inst.ID, "cross")
			}},
			SetIsolatedMargin: contracttest.CapabilityProbe{Support: contracttest.Unsupported("spot cash account has no isolated margin mode"), Probe: func(ctx context.Context) error {
				return acct.SetMarginMode(ctx, inst.ID, "isolated")
			}},
		},
	})
}
