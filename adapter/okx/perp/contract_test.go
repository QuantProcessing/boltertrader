package perp

import (
	"encoding/json"
	"testing"

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
