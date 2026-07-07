package perp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/contract/contracttest"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/sdk/okx"
	"github.com/shopspring/decimal"
)

// interface conformance
var (
	_ contract.ExecutionClient      = (*executionClient)(nil)
	_ contract.AccountClient        = (*accountClient)(nil)
	_ contract.AccountStateReporter = (*accountClient)(nil)
	_ contract.MarketDataClient     = (*marketDataClient)(nil)
)

func TestAccountIDOverridePropagatesToClients(t *testing.T) {
	const accountID = "OKX-ALT"
	provider := newInstrumentProvider()
	clk := clock.NewRealClock()

	exec := newExecutionClient(nil, provider, clk, "", accountID)
	acct := newAccountClient(nil, provider, clk, "", accountID)

	if exec.AccountID() != accountID || acct.AccountID() != accountID {
		t.Fatalf("account ids exec=%q acct=%q, want %q", exec.AccountID(), acct.AccountID(), accountID)
	}
}

func TestOKXPerpReportsRejectMismatchedAccountIDBeforeVenueRequest(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		t.Fatalf("unexpected venue request for mismatched account id: %s", r.URL.String())
	}))
	defer server.Close()

	inst := testOKXLinearInstrument(t)
	exec := newExecutionClient(
		okx.NewClient().WithCredentials("key", "secret", "passphrase").WithBaseURL(server.URL),
		testOKXProvider(inst),
		clock.NewRealClock(),
		"",
	)

	orders, err := exec.GenerateOrderStatusReports(context.Background(), model.OrderStatusReportQuery{AccountID: "OKX-OTHER", InstrumentID: inst.ID})
	if err != nil || len(orders) != 0 {
		t.Fatalf("mismatched account order reports=%+v err=%v, want empty nil", orders, err)
	}
	order, err := exec.GenerateOrderStatusReport(context.Background(), model.SingleOrderStatusQuery{AccountID: "OKX-OTHER", InstrumentID: inst.ID, ClientID: "client"})
	if err != nil || order != nil {
		t.Fatalf("mismatched account single order=%+v err=%v, want nil nil", order, err)
	}
	fills, err := exec.GenerateFillReports(context.Background(), model.FillReportQuery{AccountID: "OKX-OTHER", InstrumentID: inst.ID})
	if err != nil || len(fills) != 0 {
		t.Fatalf("mismatched account fill reports=%+v err=%v, want empty nil", fills, err)
	}
	positions, err := exec.GeneratePositionReports(context.Background(), model.PositionReportQuery{AccountID: "OKX-OTHER", InstrumentID: inst.ID})
	if err != nil || len(positions) != 0 {
		t.Fatalf("mismatched account position reports=%+v err=%v, want empty nil", positions, err)
	}
	mass, err := exec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{AccountID: "OKX-OTHER", IncludeFills: true, IncludePositions: true})
	if err != nil || mass == nil || mass.AccountID != "OKX-OTHER" || len(mass.OrderReports) != 0 || len(mass.FillReports) != 0 || len(mass.PositionReports) != 0 {
		t.Fatalf("mismatched account mass=%+v err=%v, want empty OKX-OTHER mass", mass, err)
	}
	if called {
		t.Fatal("mismatched account report crossed HTTP boundary")
	}
}

func dd(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func ptrString(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

// stubResolver implements instResolver for translation tests.
type stubResolver struct{}

func (stubResolver) resolveInstID(instID string) model.InstrumentID {
	return model.InstrumentID{Venue: venueName, Symbol: instIDToNeutral(instID), Kind: enums.KindPerp}
}

// --- 1. Enum round-trip — the hard part: ordType folds TIF -------------------

func TestOrdTypeTifFolding(t *testing.T) {
	cases := []struct {
		ot  enums.OrderType
		tif enums.TimeInForce
		okx string
	}{
		{enums.TypeMarket, enums.TifUnknown, "market"},
		{enums.TypeMarket, enums.TifIOC, "optimal_limit_ioc"},
		{enums.TypeLimit, enums.TifGTC, "limit"},
		{enums.TypeLimit, enums.TifIOC, "ioc"},
		{enums.TypeLimit, enums.TifFOK, "fok"},
		{enums.TypeLimit, enums.TifGTX, "post_only"},
	}
	for _, c := range cases {
		got, err := ordTypeToOKX(c.ot, c.tif)
		if err != nil {
			t.Fatalf("ordTypeToOKX(%v,%v): %v", c.ot, c.tif, err)
		}
		if got != c.okx {
			t.Errorf("ordTypeToOKX(%v,%v)=%q, want %q", c.ot, c.tif, got, c.okx)
		}
		// Round-trip back. Market loses its (irrelevant) TIF except IOC.
		rt, rtif := ordTypeFromOKX(got)
		if rt != c.ot {
			t.Errorf("round-trip type %q -> %v, want %v", got, rt, c.ot)
		}
		if (c.ot == enums.TypeLimit || c.okx == "optimal_limit_ioc") && rtif != c.tif {
			t.Errorf("round-trip TIF %q -> %v, want %v", got, rtif, c.tif)
		}
	}
}

func TestOrdTypeRejectsMarketFOK(t *testing.T) {
	if _, err := ordTypeToOKX(enums.TypeMarket, enums.TifFOK); err == nil {
		t.Fatal("Market+FOK should be rejected like NautilusTrader")
	}
}

func TestSideAndStatusMapping(t *testing.T) {
	if sideFromOKX("buy") != enums.SideBuy || sideFromOKX("sell") != enums.SideSell {
		t.Error("side mapping wrong")
	}
	if statusFromOKX("live") != enums.StatusNew || statusFromOKX("partially_filled") != enums.StatusPartiallyFilled {
		t.Error("status mapping wrong")
	}
	if statusFromOKX("filled") != enums.StatusFilled || statusFromOKX("canceled") != enums.StatusCanceled {
		t.Error("terminal status mapping wrong")
	}
}

func TestUnsupportedEnums(t *testing.T) {
	if _, err := sideToOKX(enums.SideUnknown); err == nil {
		t.Error("unknown side should error")
	}
	if _, err := regularOrdTypeToOKX(enums.TypeStopMarket, enums.TifGTC); err == nil {
		t.Error("stop-market must not be routed to regular order endpoint")
	}
}

// --- 2. Golden ws order push -> typed events --------------------------------

const goldenOrder = `{
  "instId":"BTC-USDT-SWAP","instType":"SWAP","ordId":"312269865356374016","clOrdId":"c-okx-1",
  "side":"buy","posSide":"net","ordType":"limit","state":"filled",
  "sz":"1","px":"60000","accFillSz":"1","avgPx":"60000",
  "fillPx":"60000","fillSz":"1","fillTime":"1700000000123","tradeId":"99",
  "execType":"T","fee":"-0.03","feeCcy":"USDT","uTime":"1700000000123"
}`

func TestGoldenOrderTranslation(t *testing.T) {
	var o okx.Order
	if err := json.Unmarshal([]byte(goldenOrder), &o); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	events := execEventsFromOrder(&o, stubResolver{}, model.AccountIDOKXDefault)
	if len(events) != 2 {
		t.Fatalf("want OrderEvent+FillEvent, got %d", len(events))
	}
	oe := events[0].(contract.OrderEvent)
	if oe.Order.Status != enums.StatusFilled || oe.Order.VenueOrderID != "312269865356374016" {
		t.Errorf("order event wrong: %+v", oe.Order)
	}
	if oe.Order.Request.InstrumentID.Symbol != "BTC-USDT" {
		t.Errorf("instId not mapped to neutral symbol: %s", oe.Order.Request.InstrumentID.Symbol)
	}
	if oe.Order.Request.AccountID != model.AccountIDOKXDefault {
		t.Fatalf("order account_id=%q", oe.Order.Request.AccountID)
	}
	fe := events[1].(contract.FillEvent)
	if !fe.Fill.Price.Equal(dd("60000")) || !fe.Fill.Quantity.Equal(dd("1")) {
		t.Errorf("fill px/qty wrong: %s/%s", fe.Fill.Price, fe.Fill.Quantity)
	}
	if fe.Fill.Liquidity != enums.LiqTaker {
		t.Errorf("execType T should be taker, got %v", fe.Fill.Liquidity)
	}
	// Fee is reported negative by OKX; adapter stores the magnitude.
	if !fe.Fill.Fee.Equal(dd("0.03")) {
		t.Errorf("fee=%s, want 0.03 (abs)", fe.Fill.Fee)
	}
	if fe.Fill.AccountID != model.AccountIDOKXDefault {
		t.Fatalf("fill account_id=%q", fe.Fill.AccountID)
	}
}

func TestPrivateEventTranslationSkipsUnsupportedInverseSwap(t *testing.T) {
	order := &okx.Order{
		InstId:    "BTC-USD-SWAP",
		InstType:  "SWAP",
		OrdId:     "inverse-order",
		Side:      "buy",
		OrdType:   "limit",
		State:     "filled",
		Sz:        "1",
		AccFillSz: "1",
		FillSz:    "1",
	}
	if got := execEventsFromOrder(order, stubResolver{}, model.AccountIDOKXDefault); len(got) != 0 {
		t.Fatalf("inverse SWAP order produced %d events, want 0", len(got))
	}

	position := &okx.Position{
		InstId:   "BTC-USD-SWAP",
		InstType: "SWAP",
		PosSide:  "net",
		Pos:      "1",
	}
	if got := accountEventsFromPosition(position, stubResolver{}, model.AccountIDOKXDefault); len(got) != 0 {
		t.Fatalf("inverse SWAP position produced %d events, want 0", len(got))
	}
}

// --- 3. Signed short position -----------------------------------------------

const goldenShortPosition = `{
  "instId":"ETH-USDT-SWAP","instType":"SWAP","posSide":"short","pos":"5",
  "avgPx":"3000","markPx":"2950","upl":"250","lever":"10","uTime":"1700000000000"
}`

func TestShortPositionSignedNegative(t *testing.T) {
	var p okx.Position
	if err := json.Unmarshal([]byte(goldenShortPosition), &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	events := accountEventsFromPosition(&p, stubResolver{}, model.AccountIDOKXDefault)
	pe := events[0].(contract.PositionEvent)
	// OKX reports pos as a positive magnitude with posSide=short; the model must
	// carry a SIGNED quantity (negative for short).
	if !pe.Position.Quantity.Equal(dd("-5")) {
		t.Errorf("short qty=%s, want -5 (signed)", pe.Position.Quantity)
	}
	if pe.Position.Side != enums.PosShort {
		t.Errorf("side=%v, want SHORT", pe.Position.Side)
	}
	if !pe.Position.UnrealizedPnL.Equal(dd("250")) {
		t.Errorf("uPnL=%s, want 250", pe.Position.UnrealizedPnL)
	}
	if pe.Position.AccountID != model.AccountIDOKXDefault {
		t.Fatalf("position account_id=%q", pe.Position.AccountID)
	}
}

// --- 4. Instrument parsing — InstIdCode is the OKX divergence ----------------

func TestInstrumentParsing_PopulatesIntCode(t *testing.T) {
	code := int64(123456)
	in := &okx.Instrument{
		InstId: "BTC-USDT-SWAP", InstType: "SWAP", BaseCcy: "BTC", QuoteCcy: "USDT",
		SettleCcy: "USDT", TickSz: "0.1", LotSz: "0.01", MinSz: "0.01", InstIdCode: &code,
	}
	inst := instrumentFromOKX(in)
	if inst == nil {
		t.Fatal("nil instrument")
	}
	provider := newInstrumentProvider()
	provider.byID[inst.ID.String()] = inst
	provider.byInstID[inst.VenueSymbol] = inst.ID
	provider.all = []*model.Instrument{inst}

	contracttest.RunInstrumentParsing(t, provider, []contracttest.InstrumentExpectation{{
		ID:          model.InstrumentID{Venue: "OKX", Symbol: "BTC-USDT", Kind: enums.KindPerp},
		PriceTick:   dd("0.1"),
		SizeStep:    dd("0.01"),
		MinNotional: decimal.Zero,
		VenueSymbol: "BTC-USDT-SWAP",
		HasIntCode:  true,  // OKX populates VenueIntCode — the divergence
		HasAssetIdx: false, // OKX is not asset-index keyed
	}})
}

func TestNonSwapSkipped(t *testing.T) {
	if instrumentFromOKX(&okx.Instrument{InstId: "BTC-USDT", InstType: "SPOT"}) != nil {
		t.Error("non-SWAP instrument should be skipped")
	}
}

func TestInstrumentParsing_SkipsInverseCoinMarginedSwap(t *testing.T) {
	in := &okx.Instrument{
		InstId: "BTC-USD-SWAP", InstType: "SWAP", BaseCcy: "BTC", QuoteCcy: "USD",
		SettleCcy: "BTC", TickSz: "0.1", LotSz: "1", MinSz: "1",
	}
	if instrumentFromOKX(in) != nil {
		t.Fatal("coin-margined inverse SWAP should be excluded from OKX perp first-phase support")
	}
}

func TestInstrumentParsing_SkipsNonUSDTSettlement(t *testing.T) {
	in := &okx.Instrument{
		InstId: "BTC-USDT-SWAP", InstType: "SWAP", BaseCcy: "BTC", QuoteCcy: "USDT",
		SettleCcy: "BTC", TickSz: "0.1", LotSz: "1", MinSz: "1",
	}
	if instrumentFromOKX(in) != nil {
		t.Fatal("SWAP with non-USDT settlement should be excluded")
	}
}

func TestDerivativeTdModeDefaultsAndValidation(t *testing.T) {
	got, err := normalizeDerivativeTdMode("")
	if err != nil {
		t.Fatalf("default tdMode: %v", err)
	}
	if got != "cross" {
		t.Fatalf("default tdMode=%q, want cross", got)
	}
	got, err = normalizeDerivativeTdMode("isolated")
	if err != nil {
		t.Fatalf("isolated tdMode: %v", err)
	}
	if got != "isolated" {
		t.Fatalf("tdMode=%q, want isolated", got)
	}
	if _, err := normalizeDerivativeTdMode("cash"); err == nil {
		t.Fatal("cash tdMode must not be accepted by perp adapter")
	}
}

func TestSubmitUsesConfiguredTdMode(t *testing.T) {
	inst := testOKXLinearInstrument(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v5/trade/order" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var req okx.OrderRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.TdMode != "isolated" {
			t.Fatalf("tdMode=%q, want isolated", req.TdMode)
		}
		if req.InstId != inst.VenueSymbol {
			t.Fatalf("instId=%q, want %q", req.InstId, inst.VenueSymbol)
		}
		_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"ordId":"venue-order-1","clOrdId":"client-1","sCode":"0"}]}`))
	}))
	defer server.Close()

	rest := okx.NewClient().WithCredentials("key", "secret", "passphrase").WithBaseURL(server.URL)
	exec := newExecutionClient(rest, testOKXProvider(inst), clock.NewRealClock(), "isolated")
	got, err := exec.Submit(context.Background(), model.OrderRequest{
		InstrumentID: inst.ID,
		ClientID:     "client-1",
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     dd("1"),
		Price:        dd("100"),
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if got.VenueOrderID != "venue-order-1" {
		t.Fatalf("venue order id=%q", got.VenueOrderID)
	}
}

func TestSCodeRejectWrapsVenueRejected(t *testing.T) {
	err := checkSCode([]okx.OrderId{{SCode: "51008", SMsg: "insufficient balance"}})
	if !errors.Is(err, contract.ErrVenueRejected) {
		t.Fatalf("sCode error=%v, want contract.ErrVenueRejected", err)
	}
}

func TestSubmitConditionalOrdersUseAlgoEndpoint(t *testing.T) {
	inst := testOKXLinearInstrument(t)
	cases := []struct {
		name         string
		req          model.OrderRequest
		wantOrdType  string
		wantTrigger  string
		wantOrderPx  string
		wantCallback string
		wantAlgoID   string
	}{
		{
			name: "stop market",
			req: model.OrderRequest{
				InstrumentID: inst.ID, ClientID: "c-stop-market", Side: enums.SideSell,
				Type: enums.TypeStopMarket, Quantity: dd("1"), TriggerPrice: dd("59000"), ReduceOnly: true,
			},
			wantOrdType: "trigger", wantTrigger: "59000", wantOrderPx: "-1", wantAlgoID: "algo-1",
		},
		{
			name: "limit if touched",
			req: model.OrderRequest{
				InstrumentID: inst.ID, ClientID: "c-lit", Side: enums.SideBuy,
				Type: enums.TypeLimitIfTouched, Quantity: dd("2"), Price: dd("60100"), TriggerPrice: dd("60000"),
			},
			wantOrdType: "trigger", wantTrigger: "60000", wantOrderPx: "60100", wantAlgoID: "algo-2",
		},
		{
			name: "trailing stop market",
			req: model.OrderRequest{
				InstrumentID: inst.ID, ClientID: "c-trailing", Side: enums.SideSell,
				Type: enums.TypeTrailingStopMarket, Quantity: dd("3"), ActivationPrice: dd("60500"), TrailingOffsetBps: dd("25"),
			},
			wantOrdType: "move_order_stop", wantCallback: "0.0025", wantAlgoID: "algo-3",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/api/v5/trade/order-algo" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				var req okx.AlgoOrderRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Fatalf("decode request: %v", err)
				}
				if req.InstId != inst.VenueSymbol || req.TdMode != "cross" || req.OrdType != tc.wantOrdType {
					t.Fatalf("unexpected algo request: %+v", req)
				}
				if req.AlgoClOrdId == nil || *req.AlgoClOrdId != tc.req.ClientID {
					t.Fatalf("algoClOrdId=%v, want %q", req.AlgoClOrdId, tc.req.ClientID)
				}
				if got := ptrString(req.TriggerPx); got != tc.wantTrigger {
					t.Fatalf("triggerPx=%q, want %q", got, tc.wantTrigger)
				}
				if got := ptrString(req.OrderPx); got != tc.wantOrderPx {
					t.Fatalf("orderPx=%q, want %q", got, tc.wantOrderPx)
				}
				if got := ptrString(req.CallbackRatio); got != tc.wantCallback {
					t.Fatalf("callbackRatio=%q, want %q", got, tc.wantCallback)
				}
				_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"algoId":"` + tc.wantAlgoID + `","algoClOrdId":"` + tc.req.ClientID + `","sCode":"0"}]}`))
			}))
			defer server.Close()

			rest := okx.NewClient().WithCredentials("key", "secret", "passphrase").WithBaseURL(server.URL)
			exec := newExecutionClient(rest, testOKXProvider(inst), clock.NewRealClock(), "cross")
			got, err := exec.Submit(context.Background(), tc.req)
			if err != nil {
				t.Fatalf("Submit: %v", err)
			}
			if got.VenueOrderID != tc.wantAlgoID || got.Request.ClientID != tc.req.ClientID {
				t.Fatalf("order=%+v", got)
			}
		})
	}
}

func TestSetLeverageUsesConfiguredTdMode(t *testing.T) {
	inst := testOKXLinearInstrument(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v5/account/set-leverage" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var req okx.SetLeverage
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.MgnMode != "isolated" {
			t.Fatalf("mgnMode=%q, want isolated", req.MgnMode)
		}
		if req.InstId != inst.VenueSymbol {
			t.Fatalf("instId=%q, want %q", req.InstId, inst.VenueSymbol)
		}
		_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"BTC-USDT-SWAP","lever":5,"mgnMode":"isolated"}]}`))
	}))
	defer server.Close()

	rest := okx.NewClient().WithCredentials("key", "secret", "passphrase").WithBaseURL(server.URL)
	account := newAccountClient(rest, testOKXProvider(inst), clock.NewRealClock(), "isolated")
	if err := account.SetLeverage(context.Background(), inst.ID, dd("5")); err != nil {
		t.Fatalf("SetLeverage: %v", err)
	}
}

func TestPerpCapabilitySuite(t *testing.T) {
	restOnly := newMarketDataClient(nil, nil, newInstrumentProvider(), clock.NewRealClock())
	account := testOKXAccountClient(t, "isolated")
	if caps := account.Capabilities(); !caps.Reports.AccountStateSnapshots || caps.Streaming.AccountState {
		t.Fatalf("account state capability flags=%+v, want report snapshot true and stream false", caps)
	}
	contracttest.RunPerpCapabilitySuite(t, contracttest.PerpCapabilitySuite{
		Venue: "OKX",
		Market: contracttest.MarketCapabilities{
			OrderBook:       contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter golden, fake transport, or explicit live-read tests")},
			Bars:            contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter golden, fake transport, or explicit live-read tests")},
			SubscribeBook:   contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter golden, fake transport, or explicit live-read tests")},
			SubscribeQuotes: contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter golden, fake transport, or explicit live-read tests")},
			SubscribeTrades: contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter golden, fake transport, or explicit live-read tests")},
			Reconnect:       contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter golden, fake transport, or explicit live-read tests")},
			RESTOnlyStreams: contracttest.CapabilityProbe{Support: contracttest.Unsupported("REST-only OKX client has no market websocket"), Probe: func(ctx context.Context) error { return restOnly.SubscribeTrades(ctx, model.InstrumentID{}) }},
			RESTOnlyReconnect: contracttest.CapabilityProbe{Support: contracttest.Unsupported("REST-only OKX client has no market websocket"), Probe: func(ctx context.Context) error {
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
				state, err := account.AccountState(ctx)
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
			Balances:    contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter golden, fake transport, or explicit live-read tests")},
			Positions:   contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter golden, fake transport, or explicit live-read tests")},
			SetLeverage: contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter golden, fake transport, or explicit live-read tests")},
			SetCrossMargin: contracttest.CapabilityProbe{Support: contracttest.Unsupported("OKX margin mode is per-order TdMode"), Probe: func(ctx context.Context) error {
				return account.SetMarginMode(ctx, model.InstrumentID{}, "cross")
			}},
			SetIsolatedMargin: contracttest.CapabilityProbe{Support: contracttest.Unsupported("OKX margin mode is per-order TdMode"), Probe: func(ctx context.Context) error {
				return account.SetMarginMode(ctx, model.InstrumentID{}, "isolated")
			}},
		},
	})
}

func TestOKXPerpAccountStateTranslation(t *testing.T) {
	account := testOKXAccountClient(t, "isolated")

	state, err := account.AccountState(context.Background())
	if err != nil {
		t.Fatalf("AccountState: %v", err)
	}
	if state.AccountID != model.AccountIDOKXDefault || state.Venue != venueName || state.Type != model.AccountMargin || state.BaseCurrency != usdtSettlement {
		t.Fatalf("account state identity=%+v", state)
	}
	if !state.Reported || state.EventID == "" || state.TsInit.IsZero() {
		t.Fatalf("account state envelope incomplete: %+v", state)
	}
	if state.TsEvent.UnixMilli() != 1700000000002 {
		t.Fatalf("TsEvent=%s, want latest position uTime", state.TsEvent)
	}
	if len(state.Balances) != 1 || state.Balances[0].Currency != "USDT" || !state.Balances[0].Free.Equal(dd("900")) || !state.Balances[0].Total.Equal(dd("1000")) {
		t.Fatalf("balances=%+v", state.Balances)
	}
	if len(state.Margins) != 2 {
		t.Fatalf("margins len=%d, want asset+position margins: %+v", len(state.Margins), state.Margins)
	}
	if state.Margins[0].InstrumentID != nil || !state.Margins[0].Initial.Equal(dd("50")) || !state.Margins[0].Maintenance.Equal(dd("10")) {
		t.Fatalf("asset margin=%+v", state.Margins[0])
	}
	if state.Margins[1].InstrumentID == nil || *state.Margins[1].InstrumentID != (model.InstrumentID{Venue: venueName, Symbol: "BTC-USDT", Kind: enums.KindPerp}) {
		t.Fatalf("position margin instrument=%+v", state.Margins[1].InstrumentID)
	}
	if !state.Margins[1].Initial.Equal(dd("25")) || !state.Margins[1].Maintenance.Equal(dd("5")) {
		t.Fatalf("position margin=%+v", state.Margins[1])
	}
	if err := state.Validate(); err != nil {
		t.Fatalf("account state validate: %v", err)
	}
}

func testOKXLinearInstrument(t *testing.T) *model.Instrument {
	t.Helper()
	code := int64(123456)
	inst := instrumentFromOKX(&okx.Instrument{
		InstId: "BTC-USDT-SWAP", InstType: "SWAP", BaseCcy: "BTC", QuoteCcy: "USDT",
		SettleCcy: "USDT", TickSz: "0.1", LotSz: "1", MinSz: "1", InstIdCode: &code,
	})
	if inst == nil {
		t.Fatal("test instrument was filtered")
	}
	return inst
}

func testOKXProvider(inst *model.Instrument) *instrumentProvider {
	provider := newInstrumentProvider()
	provider.byID[inst.ID.String()] = inst
	provider.byInstID[inst.VenueSymbol] = inst.ID
	provider.all = []*model.Instrument{inst}
	return provider
}

func testOKXAccountClient(t *testing.T, tdMode string) *accountClient {
	t.Helper()
	inst := testOKXLinearInstrument(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method=%s, want GET", r.Method)
		}
		switch r.URL.Path {
		case "/api/v5/account/balance":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"uTime":"1700000000000","details":[{"ccy":"USDT","eq":"1000","availBal":"900","imr":"50","mmr":"10","uTime":"1700000000001"}]}]}`))
		case "/api/v5/account/positions":
			if got := r.URL.Query().Get("instType"); got != instTypeSwap {
				t.Fatalf("positions instType=%q, want %s", got, instTypeSwap)
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"BTC-USDT-SWAP","instType":"SWAP","ccy":"USDT","pos":"1","posSide":"net","avgPx":"60000","markPx":"60100","upl":"100","lever":"5","imr":"25","mmr":"5","mgnMode":"isolated","uTime":"1700000000002"}]}`))
		case "/api/v5/account/config":
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"acctLv":"2","posMode":"long_short_mode","ctIsoMode":"automatic","mgnIsoMode":"automatic","settleCcy":"USDT"}]}`))
		default:
			t.Fatalf("unexpected account state request: %s", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	rest := okx.NewClient().
		WithCredentials("key", "secret", "passphrase").
		WithBaseURL(server.URL)
	return newAccountClient(rest, testOKXProvider(inst), clock.NewRealClock(), tdMode)
}
