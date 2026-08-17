package factoryclient

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/sdk/lighter"
)

type mutableLighterInstrumentLoader struct {
	snapshot exchange.InstrumentSnapshot
	err      error
}

func (loader *mutableLighterInstrumentLoader) LoadInstruments(context.Context, exchange.CatalogScope) (exchange.InstrumentSnapshot, error) {
	if loader.err != nil {
		return exchange.InstrumentSnapshot{}, loader.err
	}
	return loader.snapshot, nil
}

func TestLighterBuildMetasAcceptsZeroMarketID(t *testing.T) {
	response := &lighter.OrderBookDetailsResponse{
		Code: 200,
		OrderBookDetails: []*lighter.OrderBookDetail{
			{
				Symbol:                 "ETH",
				MarketId:               0,
				MarketType:             lighterPerp,
				MinBaseAmount:          "0.001",
				MinQuoteAmount:         "5",
				SizeDecimals:           3,
				PriceDecimals:          2,
				SupportedQuoteDecimals: 6,
			},
		},
	}

	metas, byID, err := lighterBuildMetas(response, exchange.ProductPerp, lighterPerp)
	if err != nil {
		t.Fatalf("lighterBuildMetas: %v", err)
	}
	if got := metas["ETH-USDC"].marketID; got != 0 {
		t.Fatalf("symbol market id = %d, want 0", got)
	}
	if got := byID[0].instrument.Identity.Symbol; got != "ETH-USDC" {
		t.Fatalf("id 0 symbol = %q, want ETH-USDC", got)
	}
}

func TestLighterBuildMetasSeparatesCanonicalAndNativeSymbols(t *testing.T) {
	tests := []struct {
		name       string
		product    exchange.Product
		marketType string
		response   *lighter.OrderBookDetailsResponse
		wantSymbol string
	}{
		{
			name:       "spot slash symbol",
			product:    exchange.ProductSpot,
			marketType: lighterSpot,
			response: &lighter.OrderBookDetailsResponse{Code: 200, SpotOrderBookDetails: []*lighter.OrderBookDetail{{
				Symbol: "ETH/USDC", MarketId: 2048, MarketType: lighterSpot,
				MinBaseAmount: "0.001", MinQuoteAmount: "5", SizeDecimals: 3, PriceDecimals: 2, SupportedQuoteDecimals: 6,
			}}},
			wantSymbol: "ETH-USDC",
		},
		{
			name:       "perp base-only symbol",
			product:    exchange.ProductPerp,
			marketType: lighterPerp,
			response: &lighter.OrderBookDetailsResponse{Code: 200, OrderBookDetails: []*lighter.OrderBookDetail{{
				Symbol: "BTC", MarketId: 1, MarketType: lighterPerp,
				MinBaseAmount: "0.001", MinQuoteAmount: "5", SizeDecimals: 3, PriceDecimals: 2, SupportedQuoteDecimals: 6,
			}}},
			wantSymbol: "BTC-USDC",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			metas, _, err := lighterBuildMetas(test.response, test.product, test.marketType)
			if err != nil {
				t.Fatalf("lighterBuildMetas: %v", err)
			}
			meta, ok := metas[test.wantSymbol]
			if !ok {
				t.Fatalf("canonical metadata key %q is missing", test.wantSymbol)
			}
			identity := meta.instrument.Identity
			if identity.Symbol != test.wantSymbol || identity.NativeSymbol == identity.Symbol {
				t.Fatalf("symbols = canonical %q native %q, want distinct canonical %q", identity.Symbol, identity.NativeSymbol, test.wantSymbol)
			}
		})
	}
}

func TestLighterBuildMetasPublishesEncodingScales(t *testing.T) {
	response := &lighter.OrderBookDetailsResponse{
		Code: 200,
		OrderBookDetails: []*lighter.OrderBookDetail{
			{
				Symbol:                 "BTC",
				MarketId:               1,
				MarketType:             lighterPerp,
				MinBaseAmount:          "0.001",
				MinQuoteAmount:         "5",
				SizeDecimals:           4,
				PriceDecimals:          2,
				SupportedQuoteDecimals: 6,
			},
		},
	}

	metas, _, err := lighterBuildMetas(response, exchange.ProductPerp, lighterPerp)
	if err != nil {
		t.Fatalf("lighterBuildMetas: %v", err)
	}
	encoding := metas["BTC-USDC"].instrument.Encoding
	if !encoding.PriceScale.Valid || encoding.PriceScale.Value != 100 {
		t.Fatalf("price scale = %+v, want 100", encoding.PriceScale)
	}
	if !encoding.QuantityScale.Valid || encoding.QuantityScale.Value != 10000 {
		t.Fatalf("quantity scale = %+v, want 10000", encoding.QuantityScale)
	}
	if !encoding.QuoteScale.Valid || encoding.QuoteScale.Value != 1000000 {
		t.Fatalf("quote scale = %+v, want 1000000", encoding.QuoteScale)
	}
}

func TestLighterMetaDecimalsUsesSupportedValueThenLegacyValue(t *testing.T) {
	tests := []struct {
		name     string
		primary  uint8
		fallback uint8
		want     uint8
		wantErr  bool
	}{
		{name: "supported value", primary: 6, fallback: 3, want: 6},
		{name: "legacy value", primary: 0, fallback: 3, want: 3},
		{name: "zero is valid", primary: 0, fallback: 0, want: 0},
		{name: "supported value exceeds bound", primary: 19, fallback: 3, wantErr: true},
		{name: "legacy value exceeds bound", primary: 0, fallback: 19, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := lighterMetaDecimals(test.primary, test.fallback)
			if test.wantErr {
				if err == nil {
					t.Fatal("lighterMetaDecimals expected an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("lighterMetaDecimals: %v", err)
			}
			if got != test.want {
				t.Fatalf("lighterMetaDecimals = %d, want %d", got, test.want)
			}
		})
	}
}

func TestLighterCatalogMetadataObservesMemoryCatalogRefresh(t *testing.T) {
	ctx := context.Background()
	descriptor := exchange.SourceDescriptor{
		Name:              "lighter-live",
		Endpoint:          "https://openapi.invalid",
		WebSocketEndpoint: "wss://ws.invalid",
		Transport:         "openapi",
	}
	scope := exchange.CatalogScope{
		Venue:               exchange.VenueLighter,
		Environment:         exchange.EnvironmentLive,
		Product:             exchange.ProductSpot,
		EndpointFingerprint: descriptor.Fingerprint(),
	}
	loader := &mutableLighterInstrumentLoader{
		snapshot: exchange.InstrumentSnapshot{
			SchemaVersion: 1,
			Scope:         scope,
			Instruments:   []exchange.Instrument{lighterTestInstrument(exchange.ProductSpot, "ETH-USDC", "ETH-USDC", "7")},
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
	client := NewLighterSpot(lighterOpenAPILighterPrivateKey, 7, 2, Settings{
		Endpoint:          "https://openapi.invalid",
		InstrumentCatalog: catalog,
		CatalogScope:      scope,
		HTTPClient: &http.Client{Transport: lighterOpenAPIRoundTripFunc(func(*http.Request) (*http.Response, error) {
			httpCalls++
			return nil, errors.New("sdk discovery must not run in catalog mode")
		})},
	}).(*lighterSpotClient)

	meta, err := client.lighterMeta(ctx, "test", exchange.ProductSpot, lighterSpot, lighterTestInstrument(exchange.ProductSpot, "ETH-USDC", "ETH-USDC", "7"))
	if err != nil {
		t.Fatalf("initial catalog metadata: %v", err)
	}
	if meta.marketID != 7 {
		t.Fatalf("initial market id = %d, want 7", meta.marketID)
	}

	loader.snapshot = exchange.InstrumentSnapshot{
		SchemaVersion: 1,
		Scope:         scope,
		Instruments:   []exchange.Instrument{lighterTestInstrument(exchange.ProductSpot, "ETH-USDC", "ETH-USDC", "8")},
	}
	if _, err := catalog.Refresh(ctx, scope); err != nil {
		t.Fatalf("second Refresh: %v", err)
	}
	if _, err := client.lighterMeta(ctx, "test", exchange.ProductSpot, lighterSpot, lighterTestInstrument(exchange.ProductSpot, "ETH-USDC", "ETH-USDC", "7")); err == nil {
		t.Fatal("old native id resolved after catalog refresh")
	}
	meta, err = client.lighterMeta(ctx, "test", exchange.ProductSpot, lighterSpot, lighterTestInstrument(exchange.ProductSpot, "ETH-USDC", "ETH-USDC", "8"))
	if err != nil {
		t.Fatalf("refreshed catalog metadata: %v", err)
	}
	if meta.marketID != 8 {
		t.Fatalf("refreshed market id = %d, want 8", meta.marketID)
	}
	if httpCalls != 0 {
		t.Fatalf("SDK discovery calls = %d, want 0 in catalog mode", httpCalls)
	}
}

func TestLighterCatalogMetadataReturnsCatalogErrorWithoutSDKFallback(t *testing.T) {
	ctx := context.Background()
	scope := exchange.CatalogScope{
		Venue:       exchange.VenueLighter,
		Environment: exchange.EnvironmentLive,
		Product:     exchange.ProductSpot,
	}
	catalog := exchange.NewMemoryCatalog(exchange.StaticInstrumentLoader{
		Snapshot: exchange.InstrumentSnapshot{
			SchemaVersion: 1,
			Scope:         scope,
			Instruments:   []exchange.Instrument{lighterTestInstrument(exchange.ProductPerp, "ETH-USDC", "ETH-USDC", "7")},
		},
	}, nil)
	httpCalls := 0
	client := NewLighterSpot(lighterOpenAPILighterPrivateKey, 7, 2, Settings{
		Endpoint:          "https://openapi.invalid",
		InstrumentCatalog: catalog,
		CatalogScope:      scope,
		HTTPClient: &http.Client{Transport: lighterOpenAPIRoundTripFunc(func(*http.Request) (*http.Response, error) {
			httpCalls++
			return nil, errors.New("sdk discovery must not run in catalog mode")
		})},
	}).(*lighterSpotClient)

	if _, err := client.lighterMetas(ctx, "test", exchange.ProductSpot, lighterSpot); err == nil {
		t.Fatal("catalog metadata resolved from invalid snapshot")
	}
	if httpCalls != 0 {
		t.Fatalf("SDK discovery calls = %d, want 0 after catalog error", httpCalls)
	}
}

func TestLighterPositionsResolveMarketIDsFromCatalogSnapshot(t *testing.T) {
	ctx := context.Background()
	descriptor := exchange.SourceDescriptor{
		Name:              "lighter-live",
		Endpoint:          "https://openapi.invalid",
		WebSocketEndpoint: "wss://ws.invalid",
		Transport:         "openapi",
	}
	scope := exchange.CatalogScope{
		Venue:               exchange.VenueLighter,
		Environment:         exchange.EnvironmentLive,
		Product:             exchange.ProductPerp,
		EndpointFingerprint: descriptor.Fingerprint(),
	}
	loader := &mutableLighterInstrumentLoader{
		snapshot: exchange.InstrumentSnapshot{
			SchemaVersion: 1,
			Scope:         scope,
			Instruments:   []exchange.Instrument{lighterTestInstrument(exchange.ProductPerp, "ETH-USDC", "ETH-USDC", "7")},
		},
	}
	catalog := exchange.NewDefaultMemoryCatalog()
	if err := catalog.RegisterSource(scope, descriptor, loader); err != nil {
		t.Fatalf("RegisterSource: %v", err)
	}
	if _, err := catalog.Refresh(ctx, scope); err != nil {
		t.Fatalf("initial Refresh: %v", err)
	}

	marketID := 7
	httpCalls := 0
	client := NewLighterPerp(lighterOpenAPILighterPrivateKey, 7, 2, Settings{
		Endpoint:          "https://openapi.invalid",
		InstrumentCatalog: catalog,
		CatalogScope:      scope,
		HTTPClient: &http.Client{Transport: lighterOpenAPIRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			httpCalls++
			switch request.URL.Path {
			case "/api/v1/account":
				return lighterOpenAPIJSONResponse(`{"code":200,"message":"","accounts":[{"index":7,"account_index":7,"account_trading_mode":0,"available_balance":"90","collateral":"100","cross_initial_margin_requirement":"10","positions":[{"market_id":` + strconv.Itoa(marketID) + `,"symbol":"ETH-USDC","sign":1,"position":"1","avg_entry_price":"100","position_value":"105","unrealized_pnl":"5","liquidation_price":"50","allocated_margin":"20"}],"assets":[{"symbol":"USDC","balance":"90","locked_balance":"10"}]}]}`), nil
			default:
				return nil, errors.New("unexpected SDK discovery route in catalog mode: " + request.URL.Path)
			}
		})},
	}).(*lighterPerpClient)

	positions, err := client.Positions(ctx, exchange.PositionsRequest{})
	if err != nil {
		t.Fatalf("initial Positions: %v", err)
	}
	if len(positions) != 1 || positions[0].NativeID != "7" || positions[0].NativeSymbol != "ETH-USDC" {
		t.Fatalf("initial positions = %+v, want catalog native id 7", positions)
	}

	loader.snapshot = exchange.InstrumentSnapshot{
		SchemaVersion: 1,
		Scope:         scope,
		Instruments:   []exchange.Instrument{lighterTestInstrument(exchange.ProductPerp, "ETH-USDC", "ETH-USDC", "8")},
	}
	if _, err := catalog.Refresh(ctx, scope); err != nil {
		t.Fatalf("second Refresh: %v", err)
	}
	marketID = 8
	positions, err = client.Positions(ctx, exchange.PositionsRequest{})
	if err != nil {
		t.Fatalf("refreshed Positions: %v", err)
	}
	if len(positions) != 1 || positions[0].NativeID != "8" {
		t.Fatalf("refreshed positions = %+v, want catalog native id 8", positions)
	}
	if httpCalls != 2 {
		t.Fatalf("account HTTP calls = %d, want 2 account reads only", httpCalls)
	}
}
