package factoryclient

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/shopspring/decimal"
)

type mutableBybitBitgetInstrumentLoader struct {
	snapshot exchange.InstrumentSnapshot
	err      error
}

func (loader *mutableBybitBitgetInstrumentLoader) LoadInstruments(context.Context, exchange.CatalogScope) (exchange.InstrumentSnapshot, error) {
	if loader.err != nil {
		return exchange.InstrumentSnapshot{}, loader.err
	}
	return loader.snapshot, nil
}

type bybitBitgetCatalogRoundTripper func(*http.Request) (*http.Response, error)

func (fn bybitBitgetCatalogRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestBybitInstrumentLoaderUsesPublicInstrumentsOnlyAndFiltersSettlement(t *testing.T) {
	loader := NewBybitInstrumentLoader(exchange.ProductPerp, Settings{
		Endpoint: "https://bybit.invalid",
		HTTPClient: &http.Client{Transport: bybitBitgetCatalogRoundTripper(func(request *http.Request) (*http.Response, error) {
			if request.URL.Path != "/v5/market/instruments-info" ||
				request.URL.Query().Get("category") != "linear" ||
				request.URL.Query().Get("limit") == "" {
				t.Fatalf("request = %s, want public linear instruments", request.URL.String())
			}
			if request.Header.Get("X-BAPI-API-KEY") != "" || request.Header.Get("X-BAPI-SIGN") != "" {
				t.Fatalf("public Bybit instruments request leaked auth headers")
			}
			return openAPIJSONResponse(`{"retCode":0,"retMsg":"OK","result":{"category":"linear","list":[
				{"symbol":"ETHUSDT","contractType":"LinearPerpetual","baseCoin":"ETH","quoteCoin":"USDT","settleCoin":"USDT","status":"Trading","priceFilter":{"tickSize":"0.01"},"lotSizeFilter":{"qtyStep":"0.001","minOrderQty":"0.001","minNotionalValue":"5"}},
				{"symbol":"BTCPERP","contractType":"LinearPerpetual","baseCoin":"BTC","quoteCoin":"USDC","settleCoin":"USDC","status":"Trading","priceFilter":{"tickSize":"0.1"},"lotSizeFilter":{"qtyStep":"0.001","minOrderQty":"0.001","minNotionalValue":"5"}},
				{"symbol":"BTCUSDT-31JUL26","contractType":"LinearFutures","baseCoin":"BTC","quoteCoin":"USDT","settleCoin":"USDT","status":"Trading","priceFilter":{"tickSize":"0.1"},"lotSizeFilter":{"qtyStep":"0.001","minOrderQty":"0.001","minNotionalValue":"5"}}
			],"nextPageCursor":""},"time":1720000000000}`), nil
		})},
	})

	for _, test := range []struct {
		name       string
		settlement string
		wantSymbol string
		wantNative string
	}{
		{name: "usdt", settlement: "USDT", wantSymbol: "ETH-USDT", wantNative: "ETHUSDT"},
		{name: "usdc", settlement: "USDC", wantSymbol: "BTC-USDC", wantNative: "BTCPERP"},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot, err := loader.LoadInstruments(context.Background(), exchange.CatalogScope{
				Venue:       exchange.VenueBybit,
				Environment: exchange.EnvironmentLive,
				Product:     exchange.ProductPerp,
				Settlement:  test.settlement,
			})
			if err != nil {
				t.Fatalf("LoadInstruments: %v", err)
			}
			if len(snapshot.Instruments) != 1 {
				t.Fatalf("instruments = %+v, want exactly one %s row", snapshot.Instruments, test.settlement)
			}
			got := snapshot.Instruments[0]
			if got.Identity.Symbol != test.wantSymbol || got.Identity.NativeSymbol != test.wantNative || got.Identity.SettleAsset != test.settlement {
				t.Fatalf("identity = %+v, want symbol=%s native=%s settlement=%s", got.Identity, test.wantSymbol, test.wantNative, test.settlement)
			}
		})
	}
}

func TestBitgetInstrumentLoaderUsesPublicInstrumentsOnlyAndFiltersSettlement(t *testing.T) {
	requests := make([]*http.Request, 0, 2)
	loader := NewBitgetInstrumentLoader(exchange.ProductPerp, "USDC-FUTURES", Settings{
		Endpoint: "https://bitget.invalid",
		HTTPClient: &http.Client{Transport: bybitBitgetCatalogRoundTripper(func(request *http.Request) (*http.Response, error) {
			requests = append(requests, request.Clone(request.Context()))
			if request.URL.Path != "/api/v3/market/instruments" ||
				request.URL.Query().Get("category") != "USDC-FUTURES" {
				t.Fatalf("request = %s, want public USDC-FUTURES instruments", request.URL.String())
			}
			for _, header := range []string{"ACCESS-KEY", "ACCESS-SIGN", "ACCESS-PASSPHRASE"} {
				if request.Header.Get(header) != "" {
					t.Fatalf("public Bitget instruments request leaked %s", header)
				}
			}
			return bitgetData(`[
				{"symbol":"ETHUSDT","category":"USDT-FUTURES","baseCoin":"ETH","quoteCoin":"USDT","minOrderQty":"0.001","minOrderAmount":"5","priceMultiplier":"0.01","quantityMultiplier":"0.001","status":"online"},
				{"symbol":"BTCPERP","category":"USDC-FUTURES","baseCoin":"BTC","quoteCoin":"USDC","minOrderQty":"0.001","minOrderAmount":"5","priceMultiplier":"0.1","quantityMultiplier":"0.001","status":"online"}
			]`), nil
		})},
	})

	snapshot, err := loader.LoadInstruments(context.Background(), exchange.CatalogScope{
		Venue:       exchange.VenueBitget,
		Environment: exchange.EnvironmentLive,
		Product:     exchange.ProductPerp,
		Settlement:  "USDC",
	})
	if err != nil {
		t.Fatalf("LoadInstruments: %v", err)
	}
	if len(snapshot.Instruments) != 1 {
		t.Fatalf("instruments = %+v, want exactly one USDC row", snapshot.Instruments)
	}
	got := snapshot.Instruments[0]
	if got.Identity.Symbol != "BTC-USDC" || got.Identity.NativeSymbol != "BTCPERP" || got.Identity.SettleAsset != "USDC" {
		t.Fatalf("identity = %+v", got.Identity)
	}
	if len(requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(requests))
	}
}

func TestBitgetInstrumentLoaderFallsBackToPublishedPrecisions(t *testing.T) {
	loader := NewBitgetInstrumentLoader(exchange.ProductSpot, "", Settings{
		Endpoint: "https://bitget.invalid",
		HTTPClient: &http.Client{Transport: bybitBitgetCatalogRoundTripper(func(*http.Request) (*http.Response, error) {
			return bitgetData(`[
				{"symbol":"GOATUSDT","category":"spot","baseCoin":"GOAT","quoteCoin":"USDT","minOrderQty":"0.01","minOrderAmount":"5","pricePrecision":"5","quantityPrecision":"2","priceMultiplier":"","quantityMultiplier":"","status":"online"}
			]`), nil
		})},
	})

	snapshot, err := loader.LoadInstruments(context.Background(), exchange.CatalogScope{
		Venue:       exchange.VenueBitget,
		Environment: exchange.EnvironmentLive,
		Product:     exchange.ProductSpot,
	})
	if err != nil {
		t.Fatalf("LoadInstruments: %v", err)
	}
	if len(snapshot.Instruments) != 1 {
		t.Fatalf("instruments = %+v, want one Spot row", snapshot.Instruments)
	}
	got := snapshot.Instruments[0]
	if !got.Rules.PriceIncrement.Equal(decimal.RequireFromString("0.00001")) {
		t.Fatalf("price increment = %s", got.Rules.PriceIncrement)
	}
	if !got.Rules.QuantityIncrement.Equal(decimal.RequireFromString("0.01")) {
		t.Fatalf("quantity increment = %s", got.Rules.QuantityIncrement)
	}
}

func TestBitgetInstrumentLoaderAcceptsCaseDistinctNativeSymbols(t *testing.T) {
	loader := NewBitgetInstrumentLoader(exchange.ProductSpot, "", Settings{
		Endpoint: "https://bitget.invalid",
		HTTPClient: &http.Client{Transport: bybitBitgetCatalogRoundTripper(func(*http.Request) (*http.Response, error) {
			return bitgetData(`[
				{"symbol":"RPBRUSDT","category":"spot","baseCoin":"rPBR","quoteCoin":"USDT","minOrderQty":"0.0001","minOrderAmount":"5","pricePrecision":"2","quantityPrecision":"4","status":"online"}
			]`), nil
		})},
	})

	snapshot, err := loader.LoadInstruments(context.Background(), exchange.CatalogScope{
		Venue:       exchange.VenueBitget,
		Environment: exchange.EnvironmentLive,
		Product:     exchange.ProductSpot,
	})
	if err != nil {
		t.Fatalf("LoadInstruments: %v", err)
	}
	if len(snapshot.Instruments) != 1 {
		t.Fatalf("instruments = %+v, want one tokenized Spot row", snapshot.Instruments)
	}
	got := snapshot.Instruments[0]
	if got.Identity.Symbol != "RPBR-USDT" ||
		got.Identity.NativeSymbol != "RPBRUSDT" ||
		got.Identity.BaseAsset != "rPBR" {
		t.Fatalf("identity = %+v", got.Identity)
	}
}

func TestBitgetInstrumentLoaderSkipsNonCryptoSpotProducts(t *testing.T) {
	loader := NewBitgetInstrumentLoader(exchange.ProductSpot, "", Settings{
		Endpoint: "https://bitget.invalid",
		HTTPClient: &http.Client{Transport: bybitBitgetCatalogRoundTripper(func(*http.Request) (*http.Response, error) {
			return bitgetData(`[
				{"symbol":"BTCUSDT","category":"spot","baseCoin":"BTC","quoteCoin":"USDT","symbolType":"crypto","minOrderQty":"0.0001","minOrderAmount":"5","pricePrecision":"2","quantityPrecision":"4","status":"online"},
				{"symbol":"PRESPAXUSDT","category":"spot","baseCoin":"preSPCX","quoteCoin":"USDT","symbolType":"stock","minOrderQty":"0.0001","minOrderAmount":"1","pricePrecision":"2","quantityPrecision":"4","status":"online"}
			]`), nil
		})},
	})

	snapshot, err := loader.LoadInstruments(context.Background(), exchange.CatalogScope{
		Venue:       exchange.VenueBitget,
		Environment: exchange.EnvironmentLive,
		Product:     exchange.ProductSpot,
	})
	if err != nil {
		t.Fatalf("LoadInstruments: %v", err)
	}
	if len(snapshot.Instruments) != 1 ||
		snapshot.Instruments[0].Identity.NativeSymbol != "BTCUSDT" {
		t.Fatalf("instruments = %+v, want only crypto Spot row", snapshot.Instruments)
	}
}

func TestBybitBitgetCatalogMetadataObservesRefreshWithoutSDKFallback(t *testing.T) {
	for _, test := range []struct {
		name   string
		client func(exchange.InstrumentCatalog, exchange.CatalogScope, http.RoundTripper) exchange.SpotClient
		venue  exchange.Venue
	}{
		{
			name: "bybit",
			client: func(catalog exchange.InstrumentCatalog, scope exchange.CatalogScope, transport http.RoundTripper) exchange.SpotClient {
				return NewBybitSpot("key", "secret", Settings{Endpoint: "https://catalog.invalid", InstrumentCatalog: catalog, CatalogScope: scope, HTTPClient: &http.Client{Transport: transport}})
			},
			venue: exchange.VenueBybit,
		},
		{
			name: "bitget",
			client: func(catalog exchange.InstrumentCatalog, scope exchange.CatalogScope, transport http.RoundTripper) exchange.SpotClient {
				return NewBitgetSpot("key", "secret", "passphrase", Settings{Endpoint: "https://catalog.invalid", InstrumentCatalog: catalog, CatalogScope: scope, HTTPClient: &http.Client{Transport: transport}})
			},
			venue: exchange.VenueBitget,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			descriptor := exchange.SourceDescriptor{Name: string(test.venue) + "-spot", Endpoint: "https://catalog.invalid", WebSocketEndpoint: "wss://catalog.invalid/ws", Transport: "test"}
			scope := exchange.CatalogScope{Venue: test.venue, Environment: exchange.EnvironmentLive, Product: exchange.ProductSpot, EndpointFingerprint: descriptor.Fingerprint()}
			loader := &mutableBybitBitgetInstrumentLoader{snapshot: exchange.InstrumentSnapshot{
				SchemaVersion: 1,
				Scope:         scope,
				Instruments: []exchange.Instrument{
					bybitBitgetCatalogTestInstrument(test.venue, exchange.ProductSpot, "ETH", "USDT", "", decimal.RequireFromString("0.001")),
				},
			}}
			catalog := exchange.NewDefaultMemoryCatalog()
			if err := catalog.RegisterSource(scope, descriptor, loader); err != nil {
				t.Fatalf("RegisterSource: %v", err)
			}
			if _, err := catalog.Refresh(ctx, scope); err != nil {
				t.Fatalf("initial Refresh: %v", err)
			}
			httpCalls := 0
			client := test.client(catalog, scope, bybitBitgetCatalogRoundTripper(func(*http.Request) (*http.Response, error) {
				httpCalls++
				return nil, errors.New("sdk discovery must not run in catalog mode")
			}))

			rows, err := client.Instruments(ctx)
			if err != nil || len(rows) != 1 {
				t.Fatalf("initial Instruments rows=%d err=%v", len(rows), err)
			}
			loader.snapshot = exchange.InstrumentSnapshot{
				SchemaVersion: 1,
				Scope:         scope,
				Instruments: []exchange.Instrument{
					bybitBitgetCatalogTestInstrument(test.venue, exchange.ProductSpot, "ETH", "USDT", "", decimal.RequireFromString("0.01")),
				},
			}
			if _, err := catalog.Refresh(ctx, scope); err != nil {
				t.Fatalf("second Refresh: %v", err)
			}
			rows, err = client.Instruments(ctx)
			if err != nil || len(rows) != 1 {
				t.Fatalf("refreshed Instruments rows=%d err=%v", len(rows), err)
			}
			if !rows[0].Rules.QuantityIncrement.Equal(decimal.RequireFromString("0.01")) {
				t.Fatalf("refreshed quantity increment = %s", rows[0].Rules.QuantityIncrement)
			}
			if httpCalls != 0 {
				t.Fatalf("SDK discovery calls = %d, want 0 in catalog mode", httpCalls)
			}
		})
	}
}

func TestBybitBitgetLoadersFailClosedForMalformedSupportedRows(t *testing.T) {
	t.Run("bybit native symbol mismatch", func(t *testing.T) {
		_, err := NewBybitInstrumentLoader(exchange.ProductSpot, Settings{
			Endpoint: "https://bybit.invalid",
			HTTPClient: &http.Client{Transport: bybitBitgetCatalogRoundTripper(func(*http.Request) (*http.Response, error) {
				return openAPIJSONResponse(`{"retCode":0,"retMsg":"OK","result":{"category":"spot","list":[
					{"symbol":"ETHUSDT","baseCoin":"BTC","quoteCoin":"USDT","settleCoin":"","status":"Trading","priceFilter":{"tickSize":"0.01"},"lotSizeFilter":{"basePrecision":"0.001","minOrderQty":"0.001","minOrderAmt":"5"}}
				],"nextPageCursor":""},"time":1720000000000}`), nil
			})},
		}).LoadInstruments(context.Background(), exchange.CatalogScope{Venue: exchange.VenueBybit, Environment: exchange.EnvironmentLive, Product: exchange.ProductSpot})
		if err == nil {
			t.Fatal("malformed supported Bybit row was published")
		}
	})

	t.Run("bitget missing increment", func(t *testing.T) {
		_, err := NewBitgetInstrumentLoader(exchange.ProductSpot, "", Settings{
			Endpoint: "https://bitget.invalid",
			HTTPClient: &http.Client{Transport: bybitBitgetCatalogRoundTripper(func(*http.Request) (*http.Response, error) {
				return bitgetData(`[{"symbol":"ETHUSDT","category":"spot","baseCoin":"ETH","quoteCoin":"USDT","minOrderQty":"0.001","priceMultiplier":"","quantityMultiplier":"","status":"online"}]`), nil
			})},
		}).LoadInstruments(context.Background(), exchange.CatalogScope{Venue: exchange.VenueBitget, Environment: exchange.EnvironmentLive, Product: exchange.ProductSpot})
		if err == nil {
			t.Fatal("malformed supported Bitget row was published")
		}
	})
}

func bybitBitgetCatalogTestInstrument(venue exchange.Venue, product exchange.Product, base, quote, settle string, quantityIncrement decimal.Decimal) exchange.Instrument {
	native := base + quote
	if product == exchange.ProductPerp && quote == "USDC" {
		native = base + "PERP"
	}
	return newInstrument(
		venue,
		product,
		base+"-"+quote,
		base,
		quote,
		settle,
		native,
		"",
		decimal.RequireFromString("0.01"),
		quantityIncrement,
		quantityIncrement,
		exchange.OptionalDecimal{Value: decimal.NewFromInt(5), Valid: true},
		oneContract(),
	)
}

func bybitBitgetTestInstrument(venue exchange.Venue, product exchange.Product, symbol string) exchange.Instrument {
	parts := strings.Split(symbol, "-")
	settle := ""
	if product == exchange.ProductPerp {
		settle = parts[1]
	}
	return bybitBitgetCatalogTestInstrument(venue, product, parts[0], parts[1], settle, decimal.RequireFromString("0.001"))
}

func bybitBitgetTestInstrumentPtr(venue exchange.Venue, product exchange.Product, symbol string) *exchange.Instrument {
	instrument := bybitBitgetTestInstrument(venue, product, symbol)
	return &instrument
}

func bybitBitgetStaticCatalogSettings(t *testing.T, settings Settings, venue exchange.Venue, product exchange.Product, symbol string) Settings {
	t.Helper()
	instrument := bybitBitgetTestInstrument(venue, product, symbol)
	descriptor := exchange.SourceDescriptor{
		Name:              string(venue) + "-" + string(product) + "-openapi-fixture",
		Endpoint:          settings.Endpoint,
		WebSocketEndpoint: settings.WebSocketEndpoint,
		Transport:         "test",
	}
	scope := exchange.CatalogScope{
		Venue:               venue,
		Environment:         exchange.EnvironmentLive,
		Product:             product,
		Settlement:          instrument.Identity.SettleAsset,
		EndpointFingerprint: descriptor.Fingerprint(),
	}
	loader := exchange.StaticInstrumentLoader{Snapshot: exchange.InstrumentSnapshot{
		SchemaVersion: 1,
		Scope:         scope,
		Instruments:   []exchange.Instrument{instrument},
	}}
	catalog := exchange.NewDefaultMemoryCatalog()
	if err := catalog.RegisterSource(scope, descriptor, loader); err != nil {
		t.Fatalf("RegisterSource: %v", err)
	}
	if _, err := catalog.Refresh(context.Background(), scope); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	settings.InstrumentCatalog = catalog
	settings.CatalogScope = scope
	return settings
}
