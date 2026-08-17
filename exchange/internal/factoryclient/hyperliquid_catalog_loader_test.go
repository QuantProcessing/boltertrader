package factoryclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	hyperliquidspot "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/spot"
	"github.com/shopspring/decimal"
)

type hyperliquidCatalogRoundTripper func(*http.Request) (*http.Response, error)

func (fn hyperliquidCatalogRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestHyperliquidInstrumentLoaderUsesPublicMetadataOnlyAndPreservesNativeIDs(t *testing.T) {
	ctx := context.Background()
	requestTypes := make([]string, 0, 3)
	transport := hyperliquidCatalogRoundTripper(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.URL.Path != "/info" {
			t.Fatalf("request = %s %s, want POST /info", request.Method, request.URL.Path)
		}
		if request.Header.Get("Authorization") != "" {
			t.Fatalf("public catalog request leaked authorization header")
		}
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if _, ok := payload["signature"]; ok {
			t.Fatalf("public catalog request leaked signed payload: %#v", payload)
		}
		if _, ok := payload["user"]; ok {
			t.Fatalf("public catalog request leaked account identity: %#v", payload)
		}
		requestType, _ := payload["type"].(string)
		requestTypes = append(requestTypes, requestType)
		switch requestType {
		case "spotMeta":
			return hyperliquidCatalogJSON(`{"tokens":[{"name":"USDC","szDecimals":6,"weiDecimals":6,"index":0,"isCanonical":true},{"name":"PURR","szDecimals":3,"weiDecimals":3,"index":1,"isCanonical":true}],"universe":[{"name":"@1","index":1,"tokens":[1,0],"isCanonical":true}]}`), nil
		case "metaAndAssetCtxs":
			return hyperliquidCatalogJSON(`[{"universe":[{"name":"BTC"}]},[{"funding":"0.0001","markPx":"100","oraclePx":"100","midPx":"100","premium":"0"}]]`), nil
		case "meta":
			return hyperliquidCatalogJSON(`{"universe":[{"name":"BTC","szDecimals":3,"maxLeverage":50}]}`), nil
		default:
			t.Fatalf("unexpected Hyperliquid catalog request type %q", requestType)
			return nil, nil
		}
	})
	settings := Settings{
		Endpoint:   "https://hyperliquid.invalid",
		HTTPClient: &http.Client{Transport: transport},
	}

	spotScope := exchange.CatalogScope{Venue: exchange.VenueHyperliquid, Environment: exchange.EnvironmentLive, Product: exchange.ProductSpot}
	spotSnapshot, err := NewHyperliquidInstrumentLoader(exchange.ProductSpot, settings).LoadInstruments(ctx, spotScope)
	if err != nil {
		t.Fatalf("spot LoadInstruments: %v", err)
	}
	if len(spotSnapshot.Instruments) != 1 {
		t.Fatalf("spot instruments=%d, want 1", len(spotSnapshot.Instruments))
	}
	spot := spotSnapshot.Instruments[0]
	if spot.Identity.Symbol != "PURR-USDC" || spot.Identity.NativeSymbol != "@1" || spot.Identity.NativeID != "10001" {
		t.Fatalf("spot identity = %+v", spot.Identity)
	}
	if !spot.Rules.QuantityIncrement.Equal(decimal.RequireFromString("0.001")) || spot.Encoding.QuantityScale.Value != 3 {
		t.Fatalf("spot precision = rules:%+v encoding:%+v", spot.Rules, spot.Encoding)
	}

	perpScope := exchange.CatalogScope{Venue: exchange.VenueHyperliquid, Environment: exchange.EnvironmentLive, Product: exchange.ProductPerp, Settlement: "USDC"}
	perpSnapshot, err := NewHyperliquidInstrumentLoader(exchange.ProductPerp, settings).LoadInstruments(ctx, perpScope)
	if err != nil {
		t.Fatalf("perp LoadInstruments: %v", err)
	}
	if len(perpSnapshot.Instruments) != 1 {
		t.Fatalf("perp instruments=%d, want 1", len(perpSnapshot.Instruments))
	}
	perp := perpSnapshot.Instruments[0]
	if perp.Identity.Symbol != "BTC-USDC" || perp.Identity.NativeSymbol != "BTC" || perp.Identity.NativeID != "0" || perp.Identity.SettleAsset != "USDC" {
		t.Fatalf("perp identity = %+v", perp.Identity)
	}
	if !perp.Rules.QuantityIncrement.Equal(decimal.RequireFromString("0.001")) || perp.Encoding.QuantityScale.Value != 3 {
		t.Fatalf("perp precision = rules:%+v encoding:%+v", perp.Rules, perp.Encoding)
	}

	gotTypes := strings.Join(requestTypes, ",")
	if gotTypes != "spotMeta,metaAndAssetCtxs,meta" {
		t.Fatalf("catalog request sequence = %s", gotTypes)
	}
}

func TestHyperliquidSpotDiscoverySkipsNoncanonicalWhitespaceTokenMarket(t *testing.T) {
	var meta hyperliquidspot.SpotMeta
	if err := json.Unmarshal([]byte(`{
		"tokens":[
			{"name":"USDC","szDecimals":6,"index":0,"isCanonical":true},
			{"name":"PURR","szDecimals":3,"index":1,"isCanonical":true},
			{"name":"JPL ","szDecimals":3,"index":3,"isCanonical":false}
		],
		"universe":[
			{"name":"@1","index":1,"tokens":[1,0],"isCanonical":true},
			{"name":"@2","index":2,"tokens":[3,0],"isCanonical":false}
		]
	}`), &meta); err != nil {
		t.Fatal(err)
	}
	metas, err := hlSpotMetas("Instruments", &meta)
	if err != nil {
		t.Fatalf("noncanonical whitespace token polluted discovery: %v", err)
	}
	if len(metas) != 1 || metas["@1"].instrument.Identity.Symbol != "PURR-USDC" {
		t.Fatalf("spot metas = %+v, want only portable PURR-USDC", metas)
	}
}

func TestHyperliquidSpotCodecAcceptsCanonicalPairNativeCoin(t *testing.T) {
	instrument := hyperliquidCatalogInstrument(
		exchange.ProductSpot,
		"PURR-USDC",
		"PURR/USDC",
		"10000",
		"",
		3,
	)
	meta, err := hlMetaFromInstrument(exchange.ProductSpot, "test", instrument)
	if err != nil {
		t.Fatalf("canonical pair native coin: %v", err)
	}
	if meta.nativeCoin != "PURR/USDC" || meta.assetID != 10000 {
		t.Fatalf("meta = %+v", meta)
	}

	instrument.Identity.NativeSymbol = "PURR-USDC"
	if _, err := hlMetaFromInstrument(exchange.ProductSpot, "test", instrument); err == nil {
		t.Fatal("non-native canonical alias was accepted")
	}
}

func TestHyperliquidCatalogMetasObserveCatalogRefreshWithoutSDKFallback(t *testing.T) {
	ctx := context.Background()
	scope := exchange.CatalogScope{Venue: exchange.VenueHyperliquid, Environment: exchange.EnvironmentLive, Product: exchange.ProductSpot}
	descriptor := exchange.SourceDescriptor{Name: "hyperliquid-spot-test", Endpoint: "https://hyperliquid.invalid", Transport: "test"}
	scope.EndpointFingerprint = descriptor.Fingerprint()
	loader := &hyperliquidMutableLoader{snapshot: exchange.InstrumentSnapshot{
		SchemaVersion: 1,
		Scope:         scope,
		LoadedAt:      time.Unix(1, 0),
		Instruments: []exchange.Instrument{
			hyperliquidCatalogInstrument(exchange.ProductSpot, "PURR-USDC", "@1", "10001", "", 3),
		},
	}}
	catalog := exchange.NewDefaultMemoryCatalog()
	if err := catalog.RegisterSource(scope, descriptor, loader); err != nil {
		t.Fatalf("RegisterSource: %v", err)
	}
	meta := clientMeta{venue: exchange.VenueHyperliquid, product: exchange.ProductSpot, catalog: catalog, catalogScope: scope}
	if _, err := catalog.Refresh(ctx, scope); err != nil {
		t.Fatalf("initial Refresh: %v", err)
	}
	metas, err := hlCatalogMetas(ctx, meta, "Instruments", exchange.ProductSpot)
	if err != nil {
		t.Fatalf("initial hlCatalogMetas: %v", err)
	}
	if metas["@1"].instrument.Rules.QuantityIncrement.String() != "0.001" {
		t.Fatalf("initial quantity increment = %s", metas["@1"].instrument.Rules.QuantityIncrement)
	}
	loader.snapshot.Instruments[0] = hyperliquidCatalogInstrument(exchange.ProductSpot, "PURR-USDC", "@1", "10001", "", 4)
	if _, err := catalog.Refresh(ctx, scope); err != nil {
		t.Fatalf("second Refresh: %v", err)
	}
	metas, err = hlCatalogMetas(ctx, meta, "Instruments", exchange.ProductSpot)
	if err != nil {
		t.Fatalf("refreshed hlCatalogMetas: %v", err)
	}
	if metas["@1"].instrument.Rules.QuantityIncrement.String() != "0.0001" {
		t.Fatalf("refreshed quantity increment = %s", metas["@1"].instrument.Rules.QuantityIncrement)
	}
}

func hyperliquidCatalogJSON(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func hyperliquidStaticTestMeta(t *testing.T, instrument exchange.Instrument) clientMeta {
	t.Helper()
	scope := exchange.CatalogScope{
		Venue:       instrument.Identity.Venue,
		Environment: exchange.EnvironmentLive,
		Product:     instrument.Identity.Product,
		Settlement:  instrument.Identity.SettleAsset,
	}
	descriptor := exchange.SourceDescriptor{
		Name:      "hyperliquid-static-test",
		Endpoint:  "https://hyperliquid.invalid",
		Transport: "static-test",
	}
	scope.EndpointFingerprint = descriptor.Fingerprint()
	catalog := exchange.NewDefaultMemoryCatalog()
	if err := catalog.RegisterSource(scope, descriptor, exchange.StaticInstrumentLoader{Snapshot: exchange.InstrumentSnapshot{
		SchemaVersion: 1,
		Scope:         scope,
		Instruments:   []exchange.Instrument{instrument},
		LoadedAt:      time.Unix(1, 0),
	}}); err != nil {
		t.Fatalf("RegisterSource: %v", err)
	}
	return clientMeta{venue: instrument.Identity.Venue, product: instrument.Identity.Product, catalog: catalog, catalogScope: scope}
}

type hyperliquidMutableLoader struct {
	snapshot exchange.InstrumentSnapshot
}

func (loader *hyperliquidMutableLoader) LoadInstruments(context.Context, exchange.CatalogScope) (exchange.InstrumentSnapshot, error) {
	return loader.snapshot, nil
}

func hyperliquidCatalogInstrument(product exchange.Product, symbol, nativeSymbol, nativeID, settlement string, quantityScale int) exchange.Instrument {
	baseAsset, ok := strings.CutSuffix(symbol, "-USDC")
	if !ok {
		baseAsset = symbol
	}
	instrument := newInstrument(
		exchange.VenueHyperliquid,
		product,
		symbol,
		baseAsset,
		"USDC",
		settlement,
		nativeSymbol,
		nativeID,
		decimal.RequireFromString("0.01"),
		decimal.New(1, int32(-quantityScale)),
		decimal.New(1, int32(-quantityScale)),
		exchange.OptionalDecimal{Value: hyperliquidMinNotional, Valid: true},
		oneContract(),
	)
	hlApplyScaleMetadata(&instrument, 2, quantityScale, 6)
	return instrument
}
