package bybit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	bybitsdk "github.com/QuantProcessing/boltertrader/sdk/bybit"
)

func TestAccountIDIsCanonicalUnifiedPool(t *testing.T) {
	if AccountIDUnified != "BYBIT-001" {
		t.Fatalf("AccountIDUnified=%q, want %q", AccountIDUnified, "BYBIT-001")
	}
	if AccountIDForKind(enums.KindSpot) != AccountIDUnified || AccountIDForKind(enums.KindPerp) != AccountIDUnified {
		t.Fatalf("Bybit unified account id must be shared across spot/perp")
	}
}

func TestNormalizeBybitCategories(t *testing.T) {
	got, err := normalizeBybitCategories(nil)
	if err != nil || !reflect.DeepEqual(got, []string{"spot", "linear"}) {
		t.Fatalf("default categories=%v err=%v", got, err)
	}

	got, err = normalizeBybitCategories([]string{" SPOT ", "linear", "spot"})
	if err != nil || !reflect.DeepEqual(got, []string{"spot", "linear"}) {
		t.Fatalf("normalized categories=%v err=%v", got, err)
	}

	for _, categories := range [][]string{{"inverse"}, {"option"}, {""}} {
		if got, err := normalizeBybitCategories(categories); err == nil {
			t.Fatalf("categories=%q normalized to %v, want explicit rejection", categories, got)
		}
	}
}

func TestAdapterConfigPropagatesRESTRecvWindow(t *testing.T) {
	var gotRecvWindow string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v5/market/instruments-info":
			writeJSON(t, w, map[string]any{
				"retCode": 0,
				"retMsg":  "OK",
				"result": map[string]any{
					"category":       "spot",
					"list":           []any{},
					"nextPageCursor": "",
				},
			})
		case "/v5/user/query-api":
			gotRecvWindow = r.Header.Get("X-BAPI-RECV-WINDOW")
			writeJSON(t, w, map[string]any{
				"retCode": 0,
				"retMsg":  "OK",
				"result":  map[string]any{},
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	adapter, err := newWithRESTRecvWindow(context.Background(), Config{
		APIKey:      "key",
		APISecret:   "secret",
		Environment: bybitsdk.EnvironmentProfile{RESTBaseURL: server.URL},
		Categories:  []string{"spot"},
		HTTPClient:  server.Client(),
	}, 15000)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer adapter.Close()
	if _, err := adapter.rest.GetAPIKeyInfo(context.Background()); err != nil {
		t.Fatalf("GetAPIKeyInfo: %v", err)
	}
	if gotRecvWindow != "15000" {
		t.Fatalf("recv window=%q, want 15000", gotRecvWindow)
	}
}

func TestInstrumentFromBybitPreservesSpotAndSettlement(t *testing.T) {
	spot := instrumentFromBybit("spot", bybitsdk.Instrument{
		Symbol: "ETHUSDT", BaseCoin: "ETH", QuoteCoin: "USDT", Status: "Trading",
		PriceFilter:   bybitsdk.PriceFilter{TickSize: "0.01"},
		LotSizeFilter: bybitsdk.LotSizeFilter{BasePrecision: "0.0001", MinOrderQty: "0.001", MinNotionalValue: "5"},
	})
	if spot == nil || spot.ID != (model.InstrumentID{Venue: VenueName, Symbol: "ETH-USDT", Kind: enums.KindSpot}) {
		t.Fatalf("unexpected spot instrument: %+v", spot)
	}
	if spot.Settle != "USDT" {
		t.Fatalf("spot settle=%q", spot.Settle)
	}

	usdt := instrumentFromBybit("linear", bybitsdk.Instrument{
		Symbol: "BTCUSDT", BaseCoin: "BTC", QuoteCoin: "USDT", SettleCoin: bybitsdk.SettleCoinUSDT, Status: "Trading",
		PriceFilter:   bybitsdk.PriceFilter{TickSize: "0.1"},
		LotSizeFilter: bybitsdk.LotSizeFilter{QtyStep: "0.001", MinOrderQty: "0.001", MinNotionalValue: "5"},
	})
	if usdt == nil || usdt.ID.Symbol != "BTC-USDT" || usdt.ID.Kind != enums.KindPerp || usdt.Settle != bybitsdk.SettleCoinUSDT {
		t.Fatalf("unexpected USDT linear instrument: %+v", usdt)
	}

	usdc := instrumentFromBybit("linear", bybitsdk.Instrument{
		Symbol: "BTCPERP", BaseCoin: "BTC", QuoteCoin: "USDC", SettleCoin: bybitsdk.SettleCoinUSDC, Status: "Trading",
		PriceFilter:   bybitsdk.PriceFilter{TickSize: "0.1"},
		LotSizeFilter: bybitsdk.LotSizeFilter{QtyStep: "0.001", MinOrderQty: "0.001", MinNotionalValue: "5"},
	})
	if usdc == nil || usdc.ID.Symbol != "BTC-USDC" || usdc.ID.Kind != enums.KindPerp || usdc.Settle != bybitsdk.SettleCoinUSDC {
		t.Fatalf("unexpected USDC linear instrument: %+v", usdc)
	}
}

func TestInstrumentFromBybitRejectsUnsupportedSettlement(t *testing.T) {
	got := instrumentFromBybit("inverse", bybitsdk.Instrument{Symbol: "BTCUSD", BaseCoin: "BTC", QuoteCoin: "USD", SettleCoin: "BTC"})
	if got != nil {
		t.Fatalf("inverse instrument must be out of first-phase scope: %+v", got)
	}
}

func TestInstrumentFromBybitRejectsDatedLinearFutures(t *testing.T) {
	got := instrumentFromBybit("linear", bybitsdk.Instrument{
		Symbol:       "BTCUSDT-31JUL26",
		BaseCoin:     "BTC",
		QuoteCoin:    "USDT",
		SettleCoin:   bybitsdk.SettleCoinUSDT,
		Status:       "Trading",
		DeliveryTime: "1785456000000",
	})
	if got != nil {
		t.Fatalf("dated linear futures must not be modeled as perp: %+v", got)
	}
}

func TestInstrumentProviderLoadTracksDatedLinearFutureAsDeferred(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{
			"retCode": 0,
			"retMsg":  "OK",
			"result": map[string]any{
				"category": "linear",
				"list": []any{
					map[string]any{"symbol": "BTCUSDT", "baseCoin": "BTC", "quoteCoin": "USDT", "settleCoin": "USDT", "status": "Trading", "deliveryTime": "0"},
					map[string]any{"symbol": "BTCUSDT-31JUL26", "baseCoin": "BTC", "quoteCoin": "USDT", "settleCoin": "USDT", "status": "Trading", "deliveryTime": "1785456000000"},
				},
				"nextPageCursor": "",
			},
		})
	}))
	defer server.Close()

	provider := newInstrumentProvider()
	if err := provider.Load(context.Background(), bybitsdk.NewClient().WithBaseURL(server.URL).WithHTTPClient(server.Client()), "linear"); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !provider.isDeferred("linear", "BTCUSDT-31JUL26") {
		t.Fatal("dated linear future was not retained as explicitly deferred")
	}
	if _, ok := provider.ResolveVenueInstrument("BTCUSDT-31JUL26", enums.KindPerp, bybitsdk.SettleCoinUSDT); ok {
		t.Fatal("dated linear future must not enter supported perpetual registry")
	}
	if _, ok := provider.ResolveVenueInstrument("BTCUSDT", enums.KindPerp, bybitsdk.SettleCoinUSDT); !ok {
		t.Fatal("supported perpetual instrument was not loaded")
	}
}

func TestInstrumentProviderIndexesNeutralAndVenueSymbols(t *testing.T) {
	provider := newInstrumentProvider()
	provider.LoadSnapshot([]*model.Instrument{
		instrumentFromBybit("spot", bybitsdk.Instrument{Symbol: "ETHUSDT", BaseCoin: "ETH", QuoteCoin: "USDT"}),
		instrumentFromBybit("linear", bybitsdk.Instrument{Symbol: "BTCUSDT", BaseCoin: "BTC", QuoteCoin: "USDT", SettleCoin: bybitsdk.SettleCoinUSDT}),
		instrumentFromBybit("linear", bybitsdk.Instrument{Symbol: "BTCPERP", BaseCoin: "BTC", QuoteCoin: "USDC", SettleCoin: bybitsdk.SettleCoinUSDC}),
	})

	id, ok := provider.ResolveVenueSymbol("BTCPERP")
	if !ok || id != (model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDC", Kind: enums.KindPerp}) {
		t.Fatalf("resolve venue symbol=%+v ok=%v", id, ok)
	}
	if _, ok := provider.Instrument(id); !ok {
		t.Fatalf("expected provider to return %s", id)
	}
	if got := provider.All(); len(got) != 3 {
		t.Fatalf("provider all len=%d", len(got))
	}
}

func TestInstrumentProviderResolvesVenueSymbolByKindAndSettlement(t *testing.T) {
	provider := newInstrumentProvider()
	provider.LoadSnapshot([]*model.Instrument{
		instrumentFromBybit("spot", bybitsdk.Instrument{Symbol: "BTCUSDT", BaseCoin: "BTC", QuoteCoin: "USDT"}),
		instrumentFromBybit("linear", bybitsdk.Instrument{Symbol: "BTCUSDT", BaseCoin: "BTC", QuoteCoin: "USDT", SettleCoin: bybitsdk.SettleCoinUSDT}),
		instrumentFromBybit("spot", bybitsdk.Instrument{Symbol: "BTCUSDC", BaseCoin: "BTC", QuoteCoin: "USDC"}),
		instrumentFromBybit("linear", bybitsdk.Instrument{Symbol: "BTCUSDC", BaseCoin: "BTC", QuoteCoin: "USDC", SettleCoin: bybitsdk.SettleCoinUSDC}),
	})

	spot, ok := provider.ResolveVenueInstrument("BTCUSDT", enums.KindSpot, "")
	if !ok || spot != (model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT", Kind: enums.KindSpot}) {
		t.Fatalf("spot resolve=%+v ok=%v", spot, ok)
	}
	usdt, ok := provider.ResolveVenueInstrument("BTCUSDT", enums.KindPerp, bybitsdk.SettleCoinUSDT)
	if !ok || usdt != (model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT", Kind: enums.KindPerp}) {
		t.Fatalf("USDT perp resolve=%+v ok=%v", usdt, ok)
	}
	usdc, ok := provider.ResolveVenueInstrument("BTCUSDC", enums.KindPerp, bybitsdk.SettleCoinUSDC)
	if !ok || usdc != (model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDC", Kind: enums.KindPerp}) {
		t.Fatalf("USDC perp resolve=%+v ok=%v", usdc, ok)
	}
}

func TestCapabilityRowsSplitSettlementCategories(t *testing.T) {
	rows := CapabilityRows()
	want := map[string]string{
		"Spot cash":             "unsupported",
		"USDT-linear Perp/SWAP": "account snapshot",
		"USDC-linear Perp/SWAP": "account snapshot",
	}
	for _, row := range rows {
		if row.Venue != VenueName || !row.AccountStateSnapshot {
			t.Fatalf("unexpected row: %+v", row)
		}
		if strings.EqualFold(strings.TrimSpace(row.FillReports), "unsupported") {
			t.Fatalf("capability row still reports implemented fill history as unsupported: %+v", row)
		}
		if !strings.Contains(strings.ToLower(row.MassStatus), "fill") {
			t.Fatalf("mass-status capability omits implemented fills: %+v", row)
		}
		if positionReports, ok := want[row.Product]; ok {
			if row.PositionReports != positionReports {
				t.Fatalf("product=%s position reports=%q, want %q", row.Product, row.PositionReports, positionReports)
			}
			delete(want, row.Product)
		}
	}
	for product := range want {
		t.Fatalf("missing capability row for %s", product)
	}
}
