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
	_ contract.ExecutionClient  = (*executionClient)(nil)
	_ contract.AccountClient    = (*accountClient)(nil)
	_ contract.MarketDataClient = (*marketDataClient)(nil)
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
		enums.TypeStopLimit, enums.TypeTakeProfitMarket, enums.TypeTakeProfitLimit,
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
	if be.Balance.Currency != "USDT" || !be.Balance.Total.Equal(d("1000.5")) {
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
