package perp

import (
	"context"
	"encoding/json"
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
	_ contract.ExecutionClient  = (*executionClient)(nil)
	_ contract.AccountClient    = (*accountClient)(nil)
	_ contract.MarketDataClient = (*marketDataClient)(nil)
)

func dd(s string) decimal.Decimal { return decimal.RequireFromString(s) }

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
		// Round-trip back. Market loses its (irrelevant) TIF.
		rt, rtif := ordTypeFromOKX(got)
		if rt != c.ot {
			t.Errorf("round-trip type %q -> %v, want %v", got, rt, c.ot)
		}
		if c.ot == enums.TypeLimit && rtif != c.tif {
			t.Errorf("round-trip TIF %q -> %v, want %v", got, rtif, c.tif)
		}
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
	if _, err := ordTypeToOKX(enums.TypeStopMarket, enums.TifGTC); err == nil {
		t.Error("stop-market (algo) should be ErrNotSupported in v1")
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
	events := execEventsFromOrder(&o, stubResolver{})
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
	if got := execEventsFromOrder(order, stubResolver{}); len(got) != 0 {
		t.Fatalf("inverse SWAP order produced %d events, want 0", len(got))
	}

	position := &okx.Position{
		InstId:   "BTC-USD-SWAP",
		InstType: "SWAP",
		PosSide:  "net",
		Pos:      "1",
	}
	if got := accountEventsFromPosition(position, stubResolver{}); len(got) != 0 {
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
	events := accountEventsFromPosition(&p, stubResolver{})
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
	account := newAccountClient(nil, nil, clock.NewRealClock(), "")
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
			Submit:       contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter golden, fake transport, or explicit live-read tests")},
			Cancel:       contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter golden, fake transport, or explicit live-read tests")},
			CancelAll:    contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter golden, fake transport, or explicit live-read tests")},
			Modify:       contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter golden, fake transport, or explicit live-read tests")},
			OpenOrders:   contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter golden, fake transport, or explicit live-read tests")},
			OrderReports: contracttest.CapabilityProbe{Support: contracttest.InventorySupported("covered by adapter golden, fake transport, or explicit live-read tests")},
		},
		Account: contracttest.AccountCapabilities{
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
