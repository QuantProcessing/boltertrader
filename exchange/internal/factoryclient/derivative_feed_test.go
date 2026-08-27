package factoryclient

import (
	"context"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/shopspring/decimal"
)

func TestDerivativeFeedEmitsCanonicalPatchAndUsesSharedRESTGate(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	scope := exchange.CatalogScope{Venue: exchange.VenueBybit, Product: exchange.ProductPerp, Environment: exchange.EnvironmentLive, Settlement: "USDT"}
	loader := staticDerivativeLoader{snapshot: DerivativeSnapshot{SchemaVersion: 1, Scope: scope, ObservedAt: now, Entries: []DerivativeSnapshotEntry{{
		Symbol: "BTC-USDT", NativeSymbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT",
		Rate: decimal.RequireFromString("0.0001"), RateValid: true,
		MarkPrice:         exchange.OptionalDecimal{Value: decimal.NewFromInt(78000), Valid: true},
		FundingObservedAt: now.Add(-time.Minute), MarkObservedAt: now,
	}}}}
	requests := &recordingMarketRequests{}
	feed := NewDerivativeFeed(scope, []string{"bybit.perp.usdt.v1"}, "linear", loader, canonicalResolver{}, Settings{
		MarketRequests:     requests,
		MarketCapabilities: []exchange.MarketCapability{{Venue: exchange.VenueBybit, NativeMarketFamily: "linear", Dataset: exchange.DatasetFunding, Transport: exchange.TransportREST, Granularity: exchange.GranularityAllMarket, NominalCadence: 15 * time.Second, RateLimitOperation: "linear_tickers"}},
	})
	patch, err := feed.Bootstrap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if patch.Kind != exchange.EventSnapshot || patch.NativeFamily != "linear" || patch.Fields&exchange.DerivativeFieldFunding == 0 || len(patch.Facts) != 1 {
		t.Fatalf("canonical patch=%+v", patch)
	}
	fact := patch.Facts[0]
	if fact.InstrumentID != "bybit:live:perp:USDT:BTC-USDT" || fact.RouteProfileID != "bybit.perp.usdt.v1" || !fact.MarkObservedAt.Equal(now) || !fact.FundingObservedAt.Equal(now.Add(-time.Minute)) {
		t.Fatalf("canonical fact=%+v", fact)
	}
	if requests.calls != 1 || requests.operation != "linear_tickers" || requests.collapseKey != "linear" {
		t.Fatalf("market requests=%+v", requests)
	}
}

func TestDerivativeFeedProjectsOneNativeSnapshotAcrossRoutes(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	scope := exchange.CatalogScope{Venue: exchange.VenueBybit, Product: exchange.ProductPerp, Environment: exchange.EnvironmentLive}
	loader := staticDerivativeLoader{snapshot: DerivativeSnapshot{SchemaVersion: 1, Scope: scope, ObservedAt: now, Entries: []DerivativeSnapshotEntry{
		{Symbol: "BTC-USDT", NativeSymbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT", Rate: decimal.RequireFromString("0.0001"), RateValid: true},
		{Symbol: "BTC-USDC", NativeSymbol: "BTCPERP", BaseAsset: "BTC", QuoteAsset: "USDC", Rate: decimal.RequireFromString("0.0002"), RateValid: true},
	}}}
	feed := NewDerivativeFeed(scope, []string{"bybit.perp.usdt.v1", "bybit.perp.usdc.v1"}, "linear", loader, multiRouteResolver{}, Settings{})
	patch, err := feed.Bootstrap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(patch.Facts) != 2 {
		t.Fatalf("facts=%d want 2 route projections", len(patch.Facts))
	}
	routes := map[string]bool{}
	for _, fact := range patch.Facts {
		routes[fact.RouteProfileID] = true
	}
	if !routes["bybit.perp.usdt.v1"] || !routes["bybit.perp.usdc.v1"] {
		t.Fatalf("routes=%v", routes)
	}
}

func TestDerivativeFeedCachesCanonicalResolutionForRealtimeDeltas(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	scope := exchange.CatalogScope{Venue: exchange.VenueBybit, Product: exchange.ProductPerp, Environment: exchange.EnvironmentLive, Settlement: "USDT"}
	native := exchange.InstrumentIdentity{Venue: exchange.VenueBybit, Product: exchange.ProductPerp, Symbol: "BTC-USDT", NativeSymbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT", SettleAsset: "USDT"}
	loader := staticDerivativeLoader{snapshot: DerivativeSnapshot{SchemaVersion: 1, Scope: scope, ObservedAt: now, Entries: []DerivativeSnapshotEntry{{
		Symbol: native.Symbol, NativeSymbol: native.NativeSymbol, BaseAsset: native.BaseAsset, QuoteAsset: native.QuoteAsset,
		MarkPrice: exchange.OptionalDecimal{Value: decimal.NewFromInt(78_000), Valid: true}, MarkObservedAt: now,
	}}}}
	resolver := &countingCanonicalResolver{}
	feed := NewDerivativeFeed(scope, []string{"bybit.perp.usdt.v1"}, "linear", loader, resolver, Settings{}).(*derivativeMarketFeed)
	if _, err := feed.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if resolver.calls != 1 {
		t.Fatalf("bootstrap resolver calls=%d want=1", resolver.calls)
	}
	for index := 1; index <= 3; index++ {
		projected, err := feed.project(context.Background(), nativeDerivativePatch{
			Kind: nativeDerivativePatchDelta, Scope: scope, ObservedAt: now.Add(time.Duration(index) * time.Second),
			Fields: nativeDerivativeFieldMarkPrice,
			Facts:  []nativeDerivativeFact{{Instrument: native, MarkPrice: exchange.OptionalDecimal{Value: decimal.NewFromInt(int64(78_000 + index)), Valid: true}, MarkObservedAt: now.Add(time.Duration(index) * time.Second)}},
		})
		if err != nil || len(projected.Facts) != 1 {
			t.Fatalf("delta %d projected=%+v err=%v", index, projected, err)
		}
	}
	if resolver.calls != 1 {
		t.Fatalf("realtime deltas rebuilt the catalog resolution %d times", resolver.calls)
	}
}

type staticDerivativeLoader struct{ snapshot DerivativeSnapshot }

func (loader staticDerivativeLoader) LoadDerivativeSnapshot(context.Context, exchange.CatalogScope) (DerivativeSnapshot, error) {
	return loader.snapshot, nil
}

type canonicalResolver struct{}

func (canonicalResolver) ResolveDerivativeInstrument(_ context.Context, route, nativeSymbol, nativeID string) (exchange.DerivativeFact, bool) {
	if route != "bybit.perp.usdt.v1" || nativeSymbol != "BTCUSDT" {
		return exchange.DerivativeFact{}, false
	}
	return exchange.DerivativeFact{
		InstrumentID: "bybit:live:perp:USDT:BTC-USDT", RouteProfileID: route, Environment: exchange.EnvironmentLive,
		Instrument: exchange.InstrumentIdentity{Venue: exchange.VenueBybit, Product: exchange.ProductPerp, Symbol: "BTC-USDT", NativeSymbol: nativeSymbol, NativeID: nativeID, BaseAsset: "BTC", QuoteAsset: "USDT", SettleAsset: "USDT"},
	}, true
}

type countingCanonicalResolver struct{ calls int }

func (resolver *countingCanonicalResolver) ResolveDerivativeInstrument(_ context.Context, route, nativeSymbol, nativeID string) (exchange.DerivativeFact, bool) {
	resolver.calls++
	return canonicalResolver{}.ResolveDerivativeInstrument(context.Background(), route, nativeSymbol, nativeID)
}

type multiRouteResolver struct{}

func (multiRouteResolver) ResolveDerivativeInstrument(_ context.Context, route, nativeSymbol, nativeID string) (exchange.DerivativeFact, bool) {
	wanted := map[string]string{"bybit.perp.usdt.v1": "BTCUSDT", "bybit.perp.usdc.v1": "BTCPERP"}[route]
	if wanted == "" || wanted != nativeSymbol {
		return exchange.DerivativeFact{}, false
	}
	settlement := "USDT"
	if route == "bybit.perp.usdc.v1" {
		settlement = "USDC"
	}
	return exchange.DerivativeFact{
		InstrumentID:   "bybit:live:perp:" + settlement + ":BTC-" + settlement,
		RouteProfileID: route, Environment: exchange.EnvironmentLive,
		Instrument: exchange.InstrumentIdentity{Venue: exchange.VenueBybit, Product: exchange.ProductPerp, Symbol: "BTC-" + settlement, NativeSymbol: nativeSymbol, NativeID: nativeID, BaseAsset: "BTC", QuoteAsset: settlement, SettleAsset: settlement},
	}, true
}

type recordingMarketRequests struct {
	calls       int
	operation   string
	collapseKey string
}

func (requests *recordingMarketRequests) Do(ctx context.Context, _ exchange.Venue, operation, collapseKey string, call func(context.Context) (any, error)) (any, error) {
	requests.calls++
	requests.operation, requests.collapseKey = operation, collapseKey
	return call(ctx)
}
