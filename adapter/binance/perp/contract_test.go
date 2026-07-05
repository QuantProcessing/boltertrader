package perp

import (
	"context"
	"encoding/json"
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
	sdkperp "github.com/QuantProcessing/boltertrader/sdk/binance/perp"
	"github.com/shopspring/decimal"
)

// interface conformance
var (
	_ contract.ExecutionClient      = (*executionClient)(nil)
	_ contract.AccountClient        = (*accountClient)(nil)
	_ contract.AccountStateReporter = (*accountClient)(nil)
	_ contract.MarketDataClient     = (*marketDataClient)(nil)
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// --- 1. Enum round-trip -----------------------------------------------------

func TestEnumRoundTrip(t *testing.T) {
	sides := []enums.OrderSide{enums.SideBuy, enums.SideSell}
	for _, s := range sides {
		v, err := sideToBinance(s)
		if err != nil {
			t.Fatalf("sideToBinance(%v): %v", s, err)
		}
		if got := sideFromBinance(v); got != s {
			t.Errorf("side round-trip: %v -> %q -> %v", s, v, got)
		}
	}

	types := []enums.OrderType{
		enums.TypeMarket, enums.TypeLimit, enums.TypeStopMarket,
		enums.TypeStopLimit, enums.TypeMarketIfTouched, enums.TypeLimitIfTouched,
		enums.TypeTrailingStopMarket,
	}
	for _, ot := range types {
		v, err := orderTypeToBinance(ot)
		if err != nil {
			t.Fatalf("orderTypeToBinance(%v): %v", ot, err)
		}
		if got := orderTypeFromBinance(v); got != ot {
			t.Errorf("type round-trip: %v -> %q -> %v", ot, v, got)
		}
	}

	tifs := []enums.TimeInForce{enums.TifGTC, enums.TifIOC, enums.TifFOK, enums.TifGTX}
	for _, tf := range tifs {
		v, err := tifToBinance(tf)
		if err != nil {
			t.Fatalf("tifToBinance(%v): %v", tf, err)
		}
		if got := tifFromBinance(v); got != tf {
			t.Errorf("tif round-trip: %v -> %q -> %v", tf, v, got)
		}
	}

	sidesP := []enums.PositionSide{enums.PosNet, enums.PosLong, enums.PosShort}
	for _, ps := range sidesP {
		if got := positionSideFromBinance(positionSideToBinance(ps)); got != ps {
			t.Errorf("posSide round-trip: %v -> %v", ps, got)
		}
	}
}

func TestEnumUnsupported(t *testing.T) {
	if _, err := sideToBinance(enums.SideUnknown); err == nil {
		t.Error("expected error for unknown side")
	}
	if _, err := orderTypeToBinance(enums.TypeUnknown); err == nil {
		t.Error("expected error for unknown type")
	}
	if _, err := tifToBinance(enums.TifUnknown); err == nil {
		t.Error("expected error for unknown TIF")
	}
}

// --- 2. Golden payload translation ------------------------------------------

const goldenOrderTradeUpdate = `{
  "e":"ORDER_TRADE_UPDATE","E":1700000000000,"T":1700000000000,
  "o":{
    "s":"BTCUSDT","c":"my-client-1","S":"BUY","o":"LIMIT","f":"GTC",
    "q":"0.010","p":"60000.0","ap":"60000.0","sp":"0","x":"TRADE","X":"FILLED",
    "i":123456789,"l":"0.010","z":"0.010","L":"60000.0","N":"USDT","n":"0.24",
    "T":1700000000123,"t":987654321,"m":false,"R":false,"ps":"BOTH","rp":"0"
  }
}`

func stubResolver(sym string) model.InstrumentID {
	return model.InstrumentID{Venue: venueName, Symbol: "BTC-USDT", Kind: enums.KindPerp}
}

func TestGoldenOrderTradeUpdateTranslation(t *testing.T) {
	var ev sdkperp.OrderUpdateEvent
	if err := json.Unmarshal([]byte(goldenOrderTradeUpdate), &ev); err != nil {
		t.Fatalf("unmarshal golden: %v", err)
	}
	events := execEventsFromOrderUpdate(&ev, stubResolver)
	if len(events) != 2 {
		t.Fatalf("expected OrderEvent+FillEvent, got %d events", len(events))
	}

	oe, ok := events[0].(contract.OrderEvent)
	if !ok {
		t.Fatalf("events[0] is %T, want OrderEvent", events[0])
	}
	if oe.Order.Status != enums.StatusFilled {
		t.Errorf("status=%v, want FILLED", oe.Order.Status)
	}
	if oe.Order.VenueOrderID != "123456789" {
		t.Errorf("venueOrderID=%q", oe.Order.VenueOrderID)
	}
	if !oe.Order.FilledQty.Equal(d("0.010")) {
		t.Errorf("filledQty=%s", oe.Order.FilledQty)
	}
	if oe.Order.Request.Side != enums.SideBuy {
		t.Errorf("side=%v", oe.Order.Request.Side)
	}

	fe, ok := events[1].(contract.FillEvent)
	if !ok {
		t.Fatalf("events[1] is %T, want FillEvent", events[1])
	}
	if !fe.Fill.Price.Equal(d("60000.0")) || !fe.Fill.Quantity.Equal(d("0.010")) {
		t.Errorf("fill px/qty = %s/%s", fe.Fill.Price, fe.Fill.Quantity)
	}
	if fe.Fill.Liquidity != enums.LiqTaker {
		t.Errorf("liquidity=%v, want TAKER (m=false)", fe.Fill.Liquidity)
	}
	if !fe.Fill.Fee.Equal(d("0.24")) || fe.Fill.FeeCurrency != "USDT" {
		t.Errorf("fee=%s %s", fe.Fill.Fee, fe.Fill.FeeCurrency)
	}
}

const goldenAccountUpdate = `{
  "e":"ACCOUNT_UPDATE","E":1700000000000,"T":1700000000000,
  "a":{"m":"ORDER","B":[{"a":"USDT","wb":"1000.5","cw":"950.0","bc":"0"}],
       "P":[{"s":"BTCUSDT","pa":"-0.5","ep":"60000.0","cr":"0","up":"-12.5","mt":"cross","iw":"0","ps":"BOTH"}]}
}`

func TestGoldenAccountUpdateTranslation(t *testing.T) {
	var ev sdkperp.AccountUpdateEvent
	if err := json.Unmarshal([]byte(goldenAccountUpdate), &ev); err != nil {
		t.Fatalf("unmarshal golden: %v", err)
	}
	events := accountEventsFromUpdate(&ev, stubResolver)
	if len(events) != 2 {
		t.Fatalf("expected BalanceEvent+PositionEvent, got %d", len(events))
	}
	be, ok := events[0].(contract.BalanceEvent)
	if !ok {
		t.Fatalf("events[0] is %T, want BalanceEvent", events[0])
	}
	if be.Balance.Currency != "USDT" || !be.Balance.Total.Equal(d("1000.5")) || !be.Balance.Free.Equal(d("950.0")) || !be.Balance.Available.Equal(d("950.0")) {
		t.Errorf("balance=%+v", be.Balance)
	}
	pe, ok := events[1].(contract.PositionEvent)
	if !ok {
		t.Fatalf("events[1] is %T, want PositionEvent", events[1])
	}
	// Signed quantity: short position must be negative.
	if !pe.Position.Quantity.Equal(d("-0.5")) {
		t.Errorf("position qty=%s, want -0.5 (signed short)", pe.Position.Quantity)
	}
	if !pe.Position.UnrealizedPnL.Equal(d("-12.5")) {
		t.Errorf("uPnL=%s", pe.Position.UnrealizedPnL)
	}
}

// --- 3. Instrument parsing --------------------------------------------------

func TestInstrumentParsing(t *testing.T) {
	si := &sdkperp.SymbolInfo{
		Symbol:            "BTCUSDT",
		ContractType:      "PERPETUAL",
		BaseAsset:         "BTC",
		QuoteAsset:        "USDT",
		MarginAsset:       "USDT",
		PricePrecision:    2,
		QuantityPrecision: 3,
		Filters: []map[string]any{
			{"filterType": "PRICE_FILTER", "tickSize": "0.10"},
			{"filterType": "LOT_SIZE", "stepSize": "0.001", "minQty": "0.001"},
			{"filterType": "MIN_NOTIONAL", "notional": "5"},
		},
	}
	inst := instrumentFromSymbolInfo(si)
	if inst == nil {
		t.Fatal("instrumentFromSymbolInfo returned nil")
	}

	provider := newInstrumentProvider()
	provider.byID[inst.ID.String()] = inst
	provider.bySymbol[inst.VenueSymbol] = inst.ID
	provider.all = []*model.Instrument{inst}

	contracttest.RunInstrumentParsing(t, provider, []contracttest.InstrumentExpectation{{
		ID:          model.InstrumentID{Venue: "BINANCE", Symbol: "BTC-USDT", Kind: enums.KindPerp},
		PriceTick:   d("0.10"),
		SizeStep:    d("0.001"),
		MinNotional: d("5"),
		VenueSymbol: "BTCUSDT",
		HasIntCode:  false, // Binance has no integer code
		HasAssetIdx: false, // Binance is symbol-keyed
	}})
}

func TestNonPerpetualSkipped(t *testing.T) {
	si := &sdkperp.SymbolInfo{Symbol: "BTCUSDT_240329", ContractType: "CURRENT_QUARTER"}
	if inst := instrumentFromSymbolInfo(si); inst != nil {
		t.Error("non-perpetual instrument should be skipped")
	}
}

func TestPerpCapabilitySuite(t *testing.T) {
	restOnly := newMarketDataClient(nil, nil, newInstrumentProvider(), clock.NewRealClock())
	acct := testPerpAccountClient()
	if caps := acct.Capabilities(); !caps.Reports.AccountStateSnapshots || caps.Streaming.AccountState {
		t.Fatalf("account state capability flags=%+v, want report snapshot true and stream false", caps)
	}
	contracttest.RunPerpCapabilitySuite(t, contracttest.PerpCapabilitySuite{
		Venue: "BINANCE",
		Market: contracttest.MarketCapabilities{
			OrderBook:       contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter golden, fake transport, or explicit live-read tests")},
			Bars:            contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter golden, fake transport, or explicit live-read tests")},
			SubscribeBook:   contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter golden, fake transport, or explicit live-read tests")},
			SubscribeQuotes: contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter golden, fake transport, or explicit live-read tests")},
			SubscribeTrades: contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter golden, fake transport, or explicit live-read tests")},
			Reconnect:       contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter golden, fake transport, or explicit live-read tests")},
			RESTOnlyStreams: contracttest.CapabilityProbe{Support: contracttest.Unsupported("REST-only Binance client has no market websocket"), Probe: func(ctx context.Context) error { return restOnly.SubscribeTrades(ctx, model.InstrumentID{}) }},
			RESTOnlyReconnect: contracttest.CapabilityProbe{Support: contracttest.Unsupported("REST-only Binance client has no market websocket"), Probe: func(ctx context.Context) error {
				return restOnly.Reconnect(ctx)
			}},
		},
		Execution: contracttest.ExecutionCapabilities{
			Submit:     contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter golden, fake transport, or explicit live-read tests")},
			Cancel:     contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter golden, fake transport, or explicit live-read tests")},
			CancelAll:  contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter golden, fake transport, or explicit live-read tests")},
			Modify:     contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter golden, fake transport, or explicit live-read tests")},
			OpenOrders: contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter golden, fake transport, or explicit live-read tests")},
			MassStatus: contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter golden, fake transport, or explicit live-read tests")},
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
				return state.ModeInfo.ValidateVerified()
			}},
			Balances:          contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter golden, fake transport, or explicit live-read tests")},
			Positions:         contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter golden, fake transport, or explicit live-read tests")},
			SetLeverage:       contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter golden, fake transport, or explicit live-read tests")},
			SetCrossMargin:    contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter golden, fake transport, or explicit live-read tests")},
			SetIsolatedMargin: contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter golden, fake transport, or explicit live-read tests")},
		},
	})
}

func TestBinancePerpAccountStateTranslation(t *testing.T) {
	acct := testPerpAccountClient()

	state, err := acct.AccountState(context.Background())
	if err != nil {
		t.Fatalf("AccountState: %v", err)
	}
	if state.AccountID != model.AccountIDBinanceUSDM || state.Type != model.AccountMargin || state.BaseCurrency != "USD" {
		t.Fatalf("account state identity=%+v", state)
	}
	if state.ModeInfo.AccountMode != "USD-M" || state.ModeInfo.PositionMode != "hedge" || state.ModeInfo.MarginMode != "multi_assets" {
		t.Fatalf("mode info=%+v", state.ModeInfo)
	}
	if !state.ModeInfo.Verified || len(state.ModeInfo.ProductScope) != 1 || state.ModeInfo.ProductScope[0] != enums.KindPerp {
		t.Fatalf("mode verification/scope=%+v", state.ModeInfo)
	}
	if state.TsEvent.UnixMilli() != 1700000000000 {
		t.Fatalf("TsEvent=%s, want REST updateTime", state.TsEvent)
	}
	if len(state.Balances) != 1 || state.Balances[0].Currency != "USDT" || !state.Balances[0].Free.Equal(d("900")) || !state.Balances[0].Total.Equal(d("1000")) {
		t.Fatalf("balances=%+v", state.Balances)
	}
	if len(state.Margins) != 2 {
		t.Fatalf("margins len=%d, want asset+position margins: %+v", len(state.Margins), state.Margins)
	}
	if state.Margins[0].InstrumentID != nil || !state.Margins[0].Initial.Equal(d("50")) || !state.Margins[0].Maintenance.Equal(d("10")) {
		t.Fatalf("asset margin=%+v", state.Margins[0])
	}
	if state.Margins[1].InstrumentID == nil || *state.Margins[1].InstrumentID != (model.InstrumentID{Venue: venueName, Symbol: "BTC-USDT", Kind: enums.KindPerp}) {
		t.Fatalf("position margin instrument=%+v", state.Margins[1].InstrumentID)
	}
	if !state.Margins[1].Initial.Equal(d("25")) || !state.Margins[1].Maintenance.Equal(d("5")) {
		t.Fatalf("position margin=%+v", state.Margins[1])
	}
	if err := state.Validate(); err != nil {
		t.Fatalf("account state validate: %v", err)
	}
	if err := state.ModeInfo.ValidateVerified(); err != nil {
		t.Fatalf("mode validate: %v", err)
	}
}

func testPerpAccountClient() *accountClient {
	inst := instrumentFromSymbolInfo(&sdkperp.SymbolInfo{
		Symbol: "BTCUSDT", ContractType: "PERPETUAL", BaseAsset: "BTC", QuoteAsset: "USDT", MarginAsset: "USDT",
		Filters: []map[string]any{{"filterType": "PRICE_FILTER", "tickSize": "0.10"}},
	})
	provider := newInstrumentProvider()
	provider.byID[inst.ID.String()] = inst
	provider.bySymbol[inst.VenueSymbol] = inst.ID
	rest := sdkperp.NewClient().WithCredentials("k", "s").WithHTTPClient(&http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			var body string
			switch r.URL.Path {
			case "/fapi/v2/account":
				body = `{
					"updateTime":1700000000000,
					"totalWalletBalance":"1000",
					"assets":[
						{"asset":"USDT","walletBalance":"1000","availableBalance":"900","initialMargin":"50","maintMargin":"10","updateTime":1700000000000},
						{"asset":"","walletBalance":"0","availableBalance":"0","initialMargin":"0","maintMargin":"0","updateTime":0}
					],
					"positions":[
						{"symbol":"BTCUSDT","initialMargin":"25","maintMargin":"5","unrealizedProfit":"2","leverage":"20","entryPrice":"60000","positionSide":"BOTH","positionAmt":"0.1","updateTime":1700000000001},
						{"symbol":"","initialMargin":"0","maintMargin":"0","unrealizedProfit":"0","leverage":"0","entryPrice":"0","positionSide":"BOTH","positionAmt":"0","updateTime":0}
					]
				}`
			case "/fapi/v1/positionSide/dual":
				body = `{"dualSidePosition":true}`
			case "/fapi/v1/multiAssetsMargin":
				body = `{"multiAssetsMargin":true}`
			default:
				return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader(`{"code":-1,"msg":"unexpected path"}`)), Header: make(http.Header)}, nil
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		}),
	})
	return newAccountClient(rest, provider, clock.NewRealClock())
}

// --- 4. Submit synchrony (fake transport) -----------------------------------

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSubmitSynchrony(t *testing.T) {
	const ack = `{"orderId":555,"clientOrderId":"c-1","symbol":"BTCUSDT","status":"NEW",
		"side":"BUY","type":"LIMIT","origQty":"0.010","price":"60000","executedQty":"0","avgPrice":"0"}`

	rest := sdkperp.NewClient().WithCredentials("k", "s").WithHTTPClient(&http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(strings.NewReader(ack)),
				Header:     make(http.Header),
			}, nil
		}),
	})

	inst := instrumentFromSymbolInfo(&sdkperp.SymbolInfo{
		Symbol: "BTCUSDT", ContractType: "PERPETUAL", BaseAsset: "BTC", QuoteAsset: "USDT", MarginAsset: "USDT",
		Filters: []map[string]any{{"filterType": "PRICE_FILTER", "tickSize": "0.10"}},
	})
	provider := newInstrumentProvider()
	provider.byID[inst.ID.String()] = inst
	provider.bySymbol[inst.VenueSymbol] = inst.ID

	exec := newExecutionClient(rest, provider, clock.NewRealClock())

	req := model.OrderRequest{
		InstrumentID: inst.ID,
		ClientID:     "c-1",
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     d("0.010"),
		Price:        d("60000"),
	}
	contracttest.RunSubmitSynchrony(t, exec, req)
}

func TestSubmitConditionalOrdersUseAlgoEndpoint(t *testing.T) {
	inst := instrumentFromSymbolInfo(&sdkperp.SymbolInfo{
		Symbol: "BTCUSDT", ContractType: "PERPETUAL", BaseAsset: "BTC", QuoteAsset: "USDT", MarginAsset: "USDT",
		Filters: []map[string]any{{"filterType": "PRICE_FILTER", "tickSize": "0.10"}},
	})
	provider := newInstrumentProvider()
	provider.byID[inst.ID.String()] = inst
	provider.bySymbol[inst.VenueSymbol] = inst.ID

	cases := []struct {
		name         string
		req          model.OrderRequest
		wantType     string
		wantPrice    string
		wantTrigger  string
		wantActivate string
		wantCallback string
		wantVenueID  string
		wantClientID string
		wantTIF      string
	}{
		{
			name: "stop market",
			req: model.OrderRequest{
				InstrumentID: inst.ID, ClientID: "c-stop-market", Side: enums.SideSell,
				Type: enums.TypeStopMarket, TIF: enums.TifGTC, Quantity: d("0.010"),
				TriggerPrice: d("59000"), ReduceOnly: true,
			},
			wantType: "STOP_MARKET", wantTrigger: "59000", wantVenueID: "901", wantClientID: "c-stop-market", wantTIF: "GTC",
		},
		{
			name: "limit if touched",
			req: model.OrderRequest{
				InstrumentID: inst.ID, ClientID: "c-lit", Side: enums.SideBuy,
				Type: enums.TypeLimitIfTouched, TIF: enums.TifGTC, Quantity: d("0.020"),
				Price: d("60100"), TriggerPrice: d("60000"),
			},
			wantType: "TAKE_PROFIT", wantPrice: "60100", wantTrigger: "60000", wantVenueID: "902", wantClientID: "c-lit", wantTIF: "GTC",
		},
		{
			name: "trailing stop market",
			req: model.OrderRequest{
				InstrumentID: inst.ID, ClientID: "c-trailing", Side: enums.SideSell,
				Type: enums.TypeTrailingStopMarket, TIF: enums.TifGTC, Quantity: d("0.030"),
				ActivationPrice: d("60500"), TrailingOffsetBps: d("25"),
			},
			wantType: "TRAILING_STOP_MARKET", wantActivate: "60500", wantCallback: "0.25", wantVenueID: "903", wantClientID: "c-trailing", wantTIF: "GTC",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rest := sdkperp.NewClient().WithCredentials("k", "s").WithHTTPClient(&http.Client{
				Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					if r.Method != http.MethodPost || r.URL.Path != "/fapi/v1/algoOrder" {
						t.Fatalf("request=%s %s, want POST /fapi/v1/algoOrder", r.Method, r.URL.Path)
					}
					q := r.URL.Query()
					if q.Get("symbol") != "BTCUSDT" || q.Get("algoType") != "CONDITIONAL" || q.Get("type") != tc.wantType {
						t.Fatalf("query=%s", q.Encode())
					}
					if q.Get("clientAlgoId") != tc.wantClientID {
						t.Fatalf("clientAlgoId=%q, want %q", q.Get("clientAlgoId"), tc.wantClientID)
					}
					if q.Get("timeInForce") != tc.wantTIF {
						t.Fatalf("timeInForce=%q, want %q", q.Get("timeInForce"), tc.wantTIF)
					}
					if q.Get("price") != tc.wantPrice {
						t.Fatalf("price=%q, want %q", q.Get("price"), tc.wantPrice)
					}
					if q.Get("triggerPrice") != tc.wantTrigger {
						t.Fatalf("triggerPrice=%q, want %q", q.Get("triggerPrice"), tc.wantTrigger)
					}
					if q.Get("activatePrice") != tc.wantActivate {
						t.Fatalf("activatePrice=%q, want %q", q.Get("activatePrice"), tc.wantActivate)
					}
					if q.Get("callbackRate") != tc.wantCallback {
						t.Fatalf("callbackRate=%q, want %q", q.Get("callbackRate"), tc.wantCallback)
					}
					body := `{"algoId":` + tc.wantVenueID + `,"clientAlgoId":"` + tc.wantClientID + `","symbol":"BTCUSDT","algoStatus":"NEW","side":"` + q.Get("side") + `","orderType":"` + tc.wantType + `","quantity":"` + q.Get("quantity") + `"}`
					return &http.Response{
						StatusCode: 200,
						Body:       io.NopCloser(strings.NewReader(body)),
						Header:     make(http.Header),
					}, nil
				}),
			})
			exec := newExecutionClient(rest, provider, clock.NewRealClock())

			order, err := exec.Submit(context.Background(), tc.req)
			if err != nil {
				t.Fatalf("Submit: %v", err)
			}
			if order.VenueOrderID != tc.wantVenueID || order.Request.ClientID != tc.wantClientID {
				t.Fatalf("order=%+v", order)
			}
		})
	}
}

// --- 5. Modify: recover side via GetOrder, then amend -----------------------

// modifyTestExec wires an executionClient whose REST transport answers GetOrder
// (GET) with the resting order and ModifyOrder (PUT) with the amended one,
// recording the PUT query so the test can assert what was sent.
func modifyTestExec(t *testing.T, existing, amended string, putQuery *url.Values, methods *[]string) (*executionClient, model.InstrumentID) {
	t.Helper()
	rest := sdkperp.NewClient().WithCredentials("k", "s").WithHTTPClient(&http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			*methods = append(*methods, r.Method)
			body := existing
			if r.Method == http.MethodPut {
				*putQuery = r.URL.Query()
				body = amended
			}
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     make(http.Header),
			}, nil
		}),
	})
	inst := instrumentFromSymbolInfo(&sdkperp.SymbolInfo{
		Symbol: "BTCUSDT", ContractType: "PERPETUAL", BaseAsset: "BTC", QuoteAsset: "USDT", MarginAsset: "USDT",
		Filters: []map[string]any{{"filterType": "PRICE_FILTER", "tickSize": "0.10"}},
	})
	provider := newInstrumentProvider()
	provider.byID[inst.ID.String()] = inst
	provider.bySymbol[inst.VenueSymbol] = inst.ID
	return newExecutionClient(rest, provider, clock.NewRealClock()), inst.ID
}

func TestModifyRecoversSideAndAmends(t *testing.T) {
	const existing = `{"orderId":555,"clientOrderId":"c-1","symbol":"BTCUSDT","status":"NEW",
		"side":"SELL","type":"LIMIT","origQty":"0.010","price":"60000","executedQty":"0","avgPrice":"0"}`
	const amended = `{"orderId":555,"clientOrderId":"c-1","symbol":"BTCUSDT","status":"NEW",
		"side":"SELL","type":"LIMIT","origQty":"0.020","price":"61000","executedQty":"0","avgPrice":"0"}`

	var put url.Values
	var methods []string
	exec, instID := modifyTestExec(t, existing, amended, &put, &methods)

	got, err := exec.Modify(context.Background(), instID, "555", d("61000"), d("0.020"))
	if err != nil {
		t.Fatalf("modify: %v", err)
	}

	// GetOrder (GET) must precede ModifyOrder (PUT).
	if len(methods) != 2 || methods[0] != http.MethodGet || methods[1] != http.MethodPut {
		t.Fatalf("request sequence=%v, want [GET PUT]", methods)
	}
	// The recovered side and amended values must reach the amend request.
	if put.Get("side") != "SELL" {
		t.Errorf("amend side=%q, want SELL", put.Get("side"))
	}
	if put.Get("orderId") != "555" || put.Get("price") != "61000" || put.Get("quantity") != "0.02" {
		t.Errorf("amend params orderId=%q price=%q quantity=%q", put.Get("orderId"), put.Get("price"), put.Get("quantity"))
	}
	// The returned order reflects the venue's amended response.
	if got.VenueOrderID != "555" || got.Request.Side != enums.SideSell {
		t.Errorf("order venueID=%q side=%v", got.VenueOrderID, got.Request.Side)
	}
}

// TestModifyKeepsZeroFieldsFromExisting: a zero newPrice is read back from the
// resting order so Binance's both-fields-required amend still succeeds.
func TestModifyKeepsZeroFieldsFromExisting(t *testing.T) {
	const existing = `{"orderId":555,"clientOrderId":"c-1","symbol":"BTCUSDT","status":"NEW",
		"side":"BUY","type":"LIMIT","origQty":"0.010","price":"60000","executedQty":"0","avgPrice":"0"}`
	const amended = `{"orderId":555,"clientOrderId":"c-1","symbol":"BTCUSDT","status":"NEW",
		"side":"BUY","type":"LIMIT","origQty":"0.050","price":"60000","executedQty":"0","avgPrice":"0"}`

	var put url.Values
	var methods []string
	exec, instID := modifyTestExec(t, existing, amended, &put, &methods)

	// Only change quantity; price is zero and must fall back to the resting 60000.
	if _, err := exec.Modify(context.Background(), instID, "555", decimal.Zero, d("0.050")); err != nil {
		t.Fatalf("modify: %v", err)
	}
	if put.Get("price") != "60000" {
		t.Errorf("amend price=%q, want 60000 (kept from existing)", put.Get("price"))
	}
	if put.Get("quantity") != "0.05" {
		t.Errorf("amend quantity=%q, want 0.05", put.Get("quantity"))
	}
}
