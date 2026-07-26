package factoryclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
	binanceperp "github.com/QuantProcessing/boltertrader/sdk/binance/perp"
	"github.com/shopspring/decimal"
)

type mutableBinanceInstrumentLoader struct {
	snapshot exchange.InstrumentSnapshot
	err      error
}

func (loader *mutableBinanceInstrumentLoader) LoadInstruments(context.Context, exchange.CatalogScope) (exchange.InstrumentSnapshot, error) {
	if loader.err != nil {
		return exchange.InstrumentSnapshot{}, loader.err
	}
	return loader.snapshot, nil
}

type binanceCatalogRoundTripper func(*http.Request) (*http.Response, error)

func (fn binanceCatalogRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestBinanceCatalogMetadataObservesMemoryCatalogRefreshWithoutSDKFallback(t *testing.T) {
	ctx := context.Background()
	descriptor := exchange.SourceDescriptor{
		Name:              "binance-spot",
		Endpoint:          "https://binance.invalid",
		WebSocketEndpoint: "wss://binance.invalid/ws",
		Transport:         "test",
	}
	scope := exchange.CatalogScope{
		Venue:               exchange.VenueBinance,
		Environment:         exchange.EnvironmentDemo,
		Product:             exchange.ProductSpot,
		EndpointFingerprint: descriptor.Fingerprint(),
	}
	loader := &mutableBinanceInstrumentLoader{
		snapshot: exchange.InstrumentSnapshot{
			SchemaVersion: 1,
			Scope:         scope,
			Instruments: []exchange.Instrument{
				binanceCatalogTestInstrument(exchange.ProductSpot, "ETH", "USDT", "", decimal.RequireFromString("0.001")),
			},
		},
	}
	catalog := exchange.NewDefaultMemoryCatalog()
	if err := catalog.RegisterSource(scope, descriptor, loader); err != nil {
		t.Fatalf("RegisterSource: %v", err)
	}
	if _, err := catalog.Refresh(ctx, scope); err != nil {
		t.Fatalf("initial Refresh: %v", err)
	}

	httpCalls := 0
	client := NewBinanceSpot("key", "secret", Settings{
		Endpoint:          "https://binance.invalid",
		Environment:       "demo",
		InstrumentCatalog: catalog,
		CatalogScope:      scope,
		HTTPClient: &http.Client{Transport: binanceCatalogRoundTripper(func(*http.Request) (*http.Response, error) {
			httpCalls++
			return nil, errors.New("sdk discovery must not run in catalog mode")
		})},
	}).(*binanceSpotClient)

	rows, err := client.Instruments(ctx)
	if err != nil {
		t.Fatalf("initial Instruments: %v", err)
	}
	if len(rows) != 1 || !rows[0].Rules.QuantityIncrement.Equal(decimal.RequireFromString("0.001")) {
		t.Fatalf("initial catalog rows = %+v", rows)
	}

	loader.snapshot = exchange.InstrumentSnapshot{
		SchemaVersion: 1,
		Scope:         scope,
		Instruments: []exchange.Instrument{
			binanceCatalogTestInstrument(exchange.ProductSpot, "ETH", "USDT", "", decimal.RequireFromString("0.01")),
		},
	}
	if _, err := catalog.Refresh(ctx, scope); err != nil {
		t.Fatalf("second Refresh: %v", err)
	}
	rows, err = client.Instruments(ctx)
	if err != nil {
		t.Fatalf("refreshed Instruments: %v", err)
	}
	if len(rows) != 1 || !rows[0].Rules.QuantityIncrement.Equal(decimal.RequireFromString("0.01")) {
		t.Fatalf("refreshed catalog rows = %+v", rows)
	}
	if httpCalls != 0 {
		t.Fatalf("SDK discovery calls = %d, want 0 in catalog mode", httpCalls)
	}
}

func TestBinanceInstrumentLoaderUsesPublicExchangeInfoOnly(t *testing.T) {
	requests := make([]*http.Request, 0, 1)
	transport := binanceCatalogRoundTripper(func(request *http.Request) (*http.Response, error) {
		requests = append(requests, request.Clone(request.Context()))
		if request.URL.Path != "/api/v3/exchangeInfo" {
			t.Fatalf("path = %q, want public exchangeInfo", request.URL.Path)
		}
		if request.Header.Get("X-MBX-APIKEY") != "" || request.URL.RawQuery != "" {
			t.Fatalf("public catalog request leaked auth or query: header=%q rawQuery=%q", request.Header.Get("X-MBX-APIKEY"), request.URL.RawQuery)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(`{
				"symbols": [{
					"symbol": "ETHUSDT",
					"status": "TRADING",
					"baseAsset": "ETH",
					"quoteAsset": "USDT",
					"filters": [
						{"filterType":"PRICE_FILTER","tickSize":"0.01"},
						{"filterType":"LOT_SIZE","minQty":"0.001","stepSize":"0.001"},
						{"filterType":"MIN_NOTIONAL","minNotional":"5"}
					]
				}]
			}`)),
		}, nil
	})
	scope := exchange.CatalogScope{
		Venue:       exchange.VenueBinance,
		Environment: exchange.EnvironmentDemo,
		Product:     exchange.ProductSpot,
	}
	loader := NewBinanceInstrumentLoader(exchange.ProductSpot, Settings{
		Endpoint:    "https://binance.invalid",
		Environment: "demo",
		HTTPClient:  &http.Client{Transport: transport},
	})

	snapshot, err := loader.LoadInstruments(context.Background(), scope)
	if err != nil {
		t.Fatalf("LoadInstruments: %v", err)
	}
	if len(snapshot.Instruments) != 1 || snapshot.Instruments[0].Identity.NativeSymbol != "ETHUSDT" {
		t.Fatalf("snapshot = %+v", snapshot.Instruments)
	}
	if len(requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(requests))
	}
}

func TestBinancePerpInstrumentLoaderFiltersExactSettlementScope(t *testing.T) {
	loader := NewBinanceInstrumentLoader(exchange.ProductPerp, Settings{
		Endpoint:    "https://binance.invalid",
		Environment: "demo",
		HTTPClient: &http.Client{Transport: binanceCatalogRoundTripper(func(request *http.Request) (*http.Response, error) {
			if request.URL.Path != "/fapi/v1/exchangeInfo" {
				t.Fatalf("path = %q, want public USD-M exchangeInfo", request.URL.Path)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body: io.NopCloser(strings.NewReader(`{
					"symbols": [
						{
							"symbol": "ETHUSDT",
							"contractType": "PERPETUAL",
							"status": "TRADING",
							"baseAsset": "ETH",
							"quoteAsset": "USDT",
							"marginAsset": "USDT",
							"filters": [
								{"filterType":"PRICE_FILTER","tickSize":"0.01"},
								{"filterType":"LOT_SIZE","minQty":"0.001","stepSize":"0.001"},
								{"filterType":"MIN_NOTIONAL","notional":"5"}
							]
						},
						{
							"symbol": "BTCUSDC",
							"contractType": "PERPETUAL",
							"status": "TRADING",
							"baseAsset": "BTC",
							"quoteAsset": "USDC",
							"marginAsset": "USDC",
							"filters": [
								{"filterType":"PRICE_FILTER","tickSize":"0.1"},
								{"filterType":"LOT_SIZE","minQty":"0.001","stepSize":"0.001"},
								{"filterType":"MIN_NOTIONAL","notional":"5"}
							]
						}
					]
				}`)),
			}, nil
		})},
	})

	for _, test := range []struct {
		name       string
		settlement string
		wantNative string
	}{
		{name: "usdt", settlement: "USDT", wantNative: "ETHUSDT"},
		{name: "usdc", settlement: "USDC", wantNative: "BTCUSDC"},
	} {
		t.Run(test.name, func(t *testing.T) {
			scope := exchange.CatalogScope{
				Venue:       exchange.VenueBinance,
				Environment: exchange.EnvironmentDemo,
				Product:     exchange.ProductPerp,
				Settlement:  test.settlement,
			}
			snapshot, err := loader.LoadInstruments(context.Background(), scope)
			if err != nil {
				t.Fatalf("LoadInstruments: %v", err)
			}
			if len(snapshot.Instruments) != 1 {
				t.Fatalf("rows = %d, want 1: %+v", len(snapshot.Instruments), snapshot.Instruments)
			}
			got := snapshot.Instruments[0]
			if got.Identity.NativeSymbol != test.wantNative || got.Identity.SettleAsset != test.settlement {
				t.Fatalf("instrument identity = %+v, want native=%s settlement=%s", got.Identity, test.wantNative, test.settlement)
			}
		})
	}
}

func TestBinancePerpDiscoveryFailsClosedForMalformedSupportedUSDRows(t *testing.T) {
	for _, test := range []struct {
		name   string
		symbol binanceperp.SymbolInfo
	}{
		{
			name:   "supported quote with mismatched margin asset",
			symbol: binancePerpCatalogSymbol("ETHUSDT", "ETH", "USDT", "USDC"),
		},
		{
			name:   "supported quote with noncanonical native symbol",
			symbol: binancePerpCatalogSymbol("ETH-USDT", "ETH", "USDT", "USDT"),
		},
		{
			name:   "supported quote with malformed base metadata",
			symbol: binancePerpCatalogSymbol("ethUSDT", "eth", "USDT", "USDT"),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := binancePerpInstrumentsFromExchangeInfo(&binanceperp.ExchangeInfoResponse{
				Symbols: []binanceperp.SymbolInfo{test.symbol},
			})
			if err == nil {
				t.Fatal("malformed supported Binance USD-M row was skipped instead of failing closed")
			}
		})
	}
}

func TestBinancePerpDiscoverySkipsUnsupportedNonUSDQuotes(t *testing.T) {
	rows, err := binancePerpInstrumentsFromExchangeInfo(&binanceperp.ExchangeInfoResponse{
		Symbols: []binanceperp.SymbolInfo{
			binancePerpCatalogSymbol("ETHBTC", "ETH", "BTC", "BTC"),
			binancePerpCatalogSymbol("测试测试USDT", "测试测试", "USDT", "USDT"),
			binancePerpCatalogSymbol("ETHUSDT", "ETH", "USDT", "USDT"),
		},
	})
	if err != nil {
		t.Fatalf("row outside the portable USD-M subset caused discovery failure: %v", err)
	}
	if len(rows) != 1 || rows[0].Identity.NativeSymbol != "ETHUSDT" {
		t.Fatalf("rows = %+v, want only supported ETHUSDT", rows)
	}
}

func TestBinanceExactInstrumentCodecsRejectAliasesBeforeIO(t *testing.T) {
	spot := binanceSpotETHUSDTInstrument()
	native, canonical, err := binanceSpotSymbols(spot, "test")
	if err != nil {
		t.Fatalf("spot codec: %v", err)
	}
	if native != "ETHUSDT" || canonical.Identity.Symbol != "ETH-USDT" {
		t.Fatalf("spot native=%q canonical=%+v", native, canonical.Identity)
	}

	perp := binancePerpETHUSDTInstrument()
	canonical, native, err = binancePerpRequestSymbols(perp)
	if err != nil {
		t.Fatalf("perp codec: %v", err)
	}
	if native != "ETHUSDT" || canonical.Identity.SettleAsset != "USDT" {
		t.Fatalf("perp native=%q canonical=%+v", native, canonical.Identity)
	}

	spotAlias := spot
	spotAlias.Identity.NativeSymbol = "ETH-USDT"
	if _, _, err := binanceSpotSymbols(spotAlias, "test"); !errors.Is(err, exchange.ErrInvalidRequest) {
		t.Fatalf("spot alias error = %v, want invalid request", err)
	}
	perpAlias := perp
	perpAlias.Identity.SettleAsset = "USDC"
	if _, _, err := binancePerpRequestSymbols(perpAlias); err == nil {
		t.Fatal("perp settlement alias was accepted")
	}
}

func binancePerpCatalogSymbol(symbol, base, quote, margin string) binanceperp.SymbolInfo {
	return binanceperp.SymbolInfo{
		Symbol:       symbol,
		ContractType: "PERPETUAL",
		Status:       "TRADING",
		BaseAsset:    base,
		QuoteAsset:   quote,
		MarginAsset:  margin,
		Filters: []map[string]interface{}{
			{"filterType": "PRICE_FILTER", "tickSize": "0.01"},
			{"filterType": "LOT_SIZE", "minQty": "0.001", "stepSize": "0.001"},
			{"filterType": "MIN_NOTIONAL", "notional": "5"},
		},
	}
}

func binanceSpotETHUSDTInstrument() exchange.Instrument {
	return binanceCatalogTestInstrument(exchange.ProductSpot, "ETH", "USDT", "", decimal.RequireFromString("0.001"))
}

func binancePerpETHUSDTInstrument() exchange.Instrument {
	return binanceCatalogTestInstrument(exchange.ProductPerp, "ETH", "USDT", "USDT", decimal.RequireFromString("0.001"))
}

func binanceCatalogTestInstrument(product exchange.Product, base, quote, settlement string, quantityIncrement decimal.Decimal) exchange.Instrument {
	if product == exchange.ProductPerp && settlement == "" {
		settlement = quote
	}
	return newInstrument(
		exchange.VenueBinance,
		product,
		base+"-"+quote,
		base,
		quote,
		settlement,
		base+quote,
		"",
		decimal.RequireFromString("0.01"),
		quantityIncrement,
		quantityIncrement,
		exchange.OptionalDecimal{Value: decimal.RequireFromString("5"), Valid: true},
		oneContract(),
	)
}
