package bitget

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	bitgetsdk "github.com/QuantProcessing/boltertrader/sdk/bitget"
	"github.com/shopspring/decimal"
)

func TestAccountIDIsCanonicalUnifiedPool(t *testing.T) {
	if AccountIDUnified != model.AccountIDBitgetDefault {
		t.Fatalf("AccountIDUnified=%q", AccountIDUnified)
	}
	if AccountIDForKind(enums.KindSpot) != AccountIDUnified || AccountIDForKind(enums.KindPerp) != AccountIDUnified {
		t.Fatalf("Bitget unified account id must be shared across spot/perp")
	}
}

func TestInstrumentFromBitgetPreservesSpotAndSettlement(t *testing.T) {
	spot := instrumentFromBitget(bitgetsdk.Instrument{
		Category: "SPOT", Symbol: "ETHUSDT", BaseCoin: "ETH", QuoteCoin: "USDT", Status: "online",
		PricePrecision: "2", QuantityPrecision: "4", MinOrderQty: "0.001", MinOrderAmount: "5",
	})
	if spot == nil || spot.ID != (model.InstrumentID{Venue: VenueName, Symbol: "ETH-USDT", Kind: enums.KindSpot}) {
		t.Fatalf("unexpected spot instrument: %+v", spot)
	}
	if spot.Settle != "USDT" || !spot.PriceTick.Equal(mustDecimal("0.01")) || !spot.SizeStep.Equal(mustDecimal("0.0001")) {
		t.Fatalf("unexpected spot precision/settle: %+v", spot)
	}

	usdt := instrumentFromBitget(bitgetsdk.Instrument{
		Category: bitgetsdk.ProductTypeUSDTFutures, Symbol: "BTCUSDT", BaseCoin: "BTC", QuoteCoin: "USDT", Status: "online",
		PricePrecision: "1", QuantityPrecision: "3", MinOrderQty: "0.001", MinOrderAmount: "5",
	})
	if usdt == nil || usdt.ID.Symbol != "BTC-USDT" || usdt.ID.Kind != enums.KindPerp || usdt.Settle != "USDT" {
		t.Fatalf("unexpected USDT perp instrument: %+v", usdt)
	}

	usdc := instrumentFromBitget(bitgetsdk.Instrument{
		Category: bitgetsdk.ProductTypeUSDCFutures, Symbol: "BTCPERP", BaseCoin: "BTC", QuoteCoin: "USDC", Status: "online",
		PricePrecision: "1", QuantityPrecision: "3", MinOrderQty: "0.001", MinOrderAmount: "5",
	})
	if usdc == nil || usdc.ID.Symbol != "BTC-USDC" || usdc.ID.Kind != enums.KindPerp || usdc.Settle != "USDC" {
		t.Fatalf("unexpected USDC perp instrument: %+v", usdc)
	}
}

func TestInstrumentFromBitgetRejectsUnsupportedSettlement(t *testing.T) {
	got := instrumentFromBitget(bitgetsdk.Instrument{Category: "COIN-FUTURES", Symbol: "BTCUSD", BaseCoin: "BTC", QuoteCoin: "USD"})
	if got != nil {
		t.Fatalf("coin-margined instrument must be out of first-phase scope: %+v", got)
	}
}

func TestInstrumentProviderIndexesNeutralAndVenueSymbols(t *testing.T) {
	provider := newInstrumentProvider()
	provider.LoadSnapshot([]*model.Instrument{
		instrumentFromBitget(bitgetsdk.Instrument{Category: "SPOT", Symbol: "ETHUSDT", BaseCoin: "ETH", QuoteCoin: "USDT"}),
		instrumentFromBitget(bitgetsdk.Instrument{Category: bitgetsdk.ProductTypeUSDTFutures, Symbol: "BTCUSDT", BaseCoin: "BTC", QuoteCoin: "USDT"}),
		instrumentFromBitget(bitgetsdk.Instrument{Category: bitgetsdk.ProductTypeUSDCFutures, Symbol: "BTCPERP", BaseCoin: "BTC", QuoteCoin: "USDC"}),
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
		instrumentFromBitget(bitgetsdk.Instrument{Category: "SPOT", Symbol: "BTCUSDT", BaseCoin: "BTC", QuoteCoin: "USDT"}),
		instrumentFromBitget(bitgetsdk.Instrument{Category: bitgetsdk.ProductTypeUSDTFutures, Symbol: "BTCUSDT", BaseCoin: "BTC", QuoteCoin: "USDT"}),
		instrumentFromBitget(bitgetsdk.Instrument{Category: "SPOT", Symbol: "BTCUSDC", BaseCoin: "BTC", QuoteCoin: "USDC"}),
		instrumentFromBitget(bitgetsdk.Instrument{Category: bitgetsdk.ProductTypeUSDCFutures, Symbol: "BTCUSDC", BaseCoin: "BTC", QuoteCoin: "USDC"}),
	})

	spot, ok := provider.ResolveVenueInstrument("BTCUSDT", enums.KindSpot, "")
	if !ok || spot != (model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT", Kind: enums.KindSpot}) {
		t.Fatalf("spot resolve=%+v ok=%v", spot, ok)
	}
	usdt, ok := provider.ResolveVenueInstrument("BTCUSDT", enums.KindPerp, "USDT")
	if !ok || usdt != (model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT", Kind: enums.KindPerp}) {
		t.Fatalf("USDT perp resolve=%+v ok=%v", usdt, ok)
	}
	usdc, ok := provider.ResolveVenueInstrument("BTCUSDC", enums.KindPerp, "USDC")
	if !ok || usdc != (model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDC", Kind: enums.KindPerp}) {
		t.Fatalf("USDC perp resolve=%+v ok=%v", usdc, ok)
	}
}

func TestInstrumentProviderResolvesNormalizedCategoryAndSymbolExactly(t *testing.T) {
	provider := newInstrumentProvider()
	provider.LoadSnapshot([]*model.Instrument{
		instrumentFromBitget(bitgetsdk.Instrument{Category: "SPOT", Symbol: "BTCUSDT", BaseCoin: "BTC", QuoteCoin: "USDT"}),
		instrumentFromBitget(bitgetsdk.Instrument{Category: bitgetsdk.ProductTypeUSDTFutures, Symbol: "BTCUSDT", BaseCoin: "BTC", QuoteCoin: "USDT"}),
	})

	spotWant := model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT", Kind: enums.KindSpot}
	spot, ok := provider.ResolveVenueCategorySymbol("spot", " btcusdt ")
	if !ok || spot != spotWant {
		t.Fatalf("normalized spot resolve=%+v ok=%v, want %+v", spot, ok, spotWant)
	}
	perpWant := model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT", Kind: enums.KindPerp}
	perp, ok := provider.ResolveVenueCategorySymbol("usdt-futures", "BTCUSDT")
	if !ok || perp != perpWant {
		t.Fatalf("normalized USDT perp resolve=%+v ok=%v, want %+v", perp, ok, perpWant)
	}
	if ambiguous, ok := provider.ResolveVenueSymbol("BTCUSDT"); ok || ambiguous != (model.InstrumentID{}) {
		t.Fatalf("symbol-only resolution must fail closed for spot/perp collision: %+v ok=%v", ambiguous, ok)
	}
	if ambiguous := provider.resolveVenueSymbol("BTCUSDT"); ambiguous != (model.InstrumentID{}) {
		t.Fatalf("internal symbol-only resolution must not guess a product: %+v", ambiguous)
	}

	// REST report resolution and WS category resolution must produce the same
	// neutral identifiers for an explicitly scoped instrument.
	restSpot, restSpotOK := provider.ResolveVenueInstrument("BTCUSDT", enums.KindSpot, "")
	restPerp, restPerpOK := provider.ResolveVenueInstrument("BTCUSDT", enums.KindPerp, "USDT")
	if !restSpotOK || restSpot != spot || !restPerpOK || restPerp != perp {
		t.Fatalf("REST/WS identity mismatch: restSpot=%+v wsSpot=%+v restPerp=%+v wsPerp=%+v", restSpot, spot, restPerp, perp)
	}
}

func TestInstrumentProviderCategoryResolutionFailsClosed(t *testing.T) {
	provider := newInstrumentProvider()
	provider.LoadSnapshot([]*model.Instrument{
		instrumentFromBitget(bitgetsdk.Instrument{Category: "SPOT", Symbol: "BTCUSDT", BaseCoin: "BTC", QuoteCoin: "USDT"}),
	})

	for _, tc := range []struct {
		category string
		symbol   string
	}{
		{category: bitgetsdk.ProductTypeUSDTFutures, symbol: "BTCUSDT"}, // valid but out of configured scope
		{category: "COIN-FUTURES", symbol: "BTCUSDT"},                   // unsupported category
		{category: "", symbol: "BTCUSDT"},                               // category is mandatory for UTA private records
		{category: "SPOT", symbol: "ETHUSDT"},                           // unknown instrument
	} {
		if got, ok := provider.ResolveVenueCategorySymbol(tc.category, tc.symbol); ok || got != (model.InstrumentID{}) {
			t.Fatalf("resolve(%q,%q)=%+v ok=%v, want fail-closed zero ID", tc.category, tc.symbol, got, ok)
		}
	}

	if got := provider.resolveVenueSymbol("NOT-LOADED"); got != (model.InstrumentID{}) {
		t.Fatalf("unknown symbol must not synthesize a perpetual ID: %+v", got)
	}
}

func TestNewNormalizesAndDeduplicatesConfiguredCategories(t *testing.T) {
	var (
		mu         sync.Mutex
		categories []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/market/instruments" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		category := r.URL.Query().Get("category")
		categories = append(categories, category)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		otherCategory := "SPOT"
		if category == "SPOT" {
			otherCategory = bitgetsdk.ProductTypeUSDTFutures
		}
		matching := map[string]any{"category": category, "symbol": category + "-MATCH", "baseCoin": category, "quoteCoin": "USDT", "status": "online"}
		mismatched := map[string]any{"category": otherCategory, "symbol": category + "-OUT-OF-SCOPE", "baseCoin": "OUT", "quoteCoin": "USDT", "status": "online"}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": "00000", "msg": "success", "data": []any{matching, mismatched}})
	}))
	defer server.Close()

	adapter, err := New(context.Background(), Config{
		Environment: bitgetsdk.EnvironmentProfile{
			RESTBaseURL:  server.URL,
			PublicWSURL:  "ws://127.0.0.1/public",
			PrivateWSURL: "ws://127.0.0.1/private",
		},
		Categories: []string{" spot ", "usdt-futures", "SPOT", " usdc-futures "},
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer adapter.Close()

	mu.Lock()
	got := append([]string(nil), categories...)
	mu.Unlock()
	want := []string{"SPOT", bitgetsdk.ProductTypeUSDTFutures, bitgetsdk.ProductTypeUSDCFutures}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("instrument categories=%v, want normalized deduplicated %v", got, want)
	}
	if got := len(adapter.provider.All()); got != len(want) {
		t.Fatalf("loaded instruments=%d, want only the %d in-scope category matches", got, len(want))
	}
}

func TestNewRejectsUnsupportedConfiguredCategoryBeforeIO(t *testing.T) {
	for _, category := range []string{"MARGIN", "coin-futures", "", "   ", "OPTIONS"} {
		t.Run(category, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				http.Error(w, "unexpected request", http.StatusInternalServerError)
			}))
			defer server.Close()

			adapter, err := New(context.Background(), Config{
				Environment: bitgetsdk.EnvironmentProfile{RESTBaseURL: server.URL},
				Categories:  []string{category},
				HTTPClient:  server.Client(),
			})
			if err == nil {
				if adapter != nil {
					_ = adapter.Close()
				}
				t.Fatalf("New category %q succeeded, want fail-closed error", category)
			}
			if got := requests.Load(); got != 0 {
				t.Fatalf("invalid category %q issued %d HTTP requests", category, got)
			}
		})
	}
}

func TestNormalizeBitgetCategoriesDefaultsEmptyList(t *testing.T) {
	got, err := normalizeBitgetCategories(nil)
	if err != nil {
		t.Fatalf("normalize defaults: %v", err)
	}
	want := []string{"SPOT", bitgetsdk.ProductTypeUSDTFutures, bitgetsdk.ProductTypeUSDCFutures}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("default categories=%v, want %v", got, want)
	}
}

func TestCapabilityRowsSplitSettlementCategories(t *testing.T) {
	rows := CapabilityRows()
	want := map[string]bool{"Spot cash": false, "USDT-linear Perp/SWAP": false, "USDC-linear Perp/SWAP": false}
	for _, row := range rows {
		if row.Venue != VenueName || !row.AccountStateSnapshot {
			t.Fatalf("unexpected row: %+v", row)
		}
		if strings.EqualFold(strings.TrimSpace(row.FillReports), "unsupported") {
			t.Fatalf("capability row still reports implemented fill history as unsupported: %+v", row)
		}
		if !strings.Contains(strings.ToLower(row.MassStatus), "fill") || !strings.Contains(strings.ToLower(row.MassStatus), "position") {
			t.Fatalf("mass-status capability omits implemented fills/positions: %+v", row)
		}
		if _, ok := want[row.Product]; ok {
			want[row.Product] = true
		}
	}
	for product, seen := range want {
		if !seen {
			t.Fatalf("missing capability row for %s", product)
		}
	}
}

func mustDecimal(s string) decimal.Decimal {
	d, err := decimal.NewFromString(s)
	if err != nil {
		panic(err)
	}
	return d
}
