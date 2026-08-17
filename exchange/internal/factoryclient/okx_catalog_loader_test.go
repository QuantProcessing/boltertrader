package factoryclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/shopspring/decimal"
)

type mutableOKXInstrumentLoader struct {
	snapshot exchange.InstrumentSnapshot
	err      error
}

func (loader *mutableOKXInstrumentLoader) LoadInstruments(context.Context, exchange.CatalogScope) (exchange.InstrumentSnapshot, error) {
	if loader.err != nil {
		return exchange.InstrumentSnapshot{}, loader.err
	}
	return loader.snapshot, nil
}

type okxCatalogRoundTripper func(*http.Request) (*http.Response, error)

func (fn okxCatalogRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestOKXInstrumentLoaderUsesPublicInstrumentsOnly(t *testing.T) {
	requests := make([]*http.Request, 0, 1)
	loader := NewOKXInstrumentLoader(exchange.ProductSpot, Settings{
		Endpoint: "https://okx.invalid",
		HTTPClient: &http.Client{Transport: okxCatalogRoundTripper(func(request *http.Request) (*http.Response, error) {
			requests = append(requests, request.Clone(request.Context()))
			if request.URL.Path != "/api/v5/public/instruments" || request.URL.Query().Get("instType") != okxSpotType {
				t.Fatalf("request = %s, want public SPOT instruments", request.URL.String())
			}
			for _, header := range []string{"OK-ACCESS-KEY", "OK-ACCESS-SIGN", "OK-ACCESS-TIMESTAMP", "OK-ACCESS-PASSPHRASE"} {
				if request.Header.Get(header) != "" {
					t.Fatalf("public instruments request leaked %s", header)
				}
			}
			return okxJSONResponse(`{"code":"0","msg":"","data":[
				{"instType":"SPOT","instId":"ETH-USDT","baseCcy":"ETH","quoteCcy":"USDT","tickSz":"0.01","lotSz":"0.0001","minSz":"0.0001","state":"live","instIdCode":1001},
				{"instType":"SPOT","instId":"TESTING3-USDT","baseCcy":"TESTING3","quoteCcy":"USDT","tickSz":"","lotSz":"","minSz":"","state":"preopen","instIdCode":1002}
			]}`), nil
		})},
	})

	snapshot, err := loader.LoadInstruments(context.Background(), exchange.CatalogScope{
		Venue:       exchange.VenueOKX,
		Environment: exchange.EnvironmentLive,
		Product:     exchange.ProductSpot,
	})
	if err != nil {
		t.Fatalf("LoadInstruments: %v", err)
	}
	if len(snapshot.Instruments) != 1 {
		t.Fatalf("instruments = %+v, want exactly one live row", snapshot.Instruments)
	}
	got := snapshot.Instruments[0]
	if got.Identity.Venue != exchange.VenueOKX || got.Identity.Product != exchange.ProductSpot ||
		got.Identity.Symbol != "ETH-USDT" || got.Identity.NativeSymbol != "ETH-USDT" || got.Identity.NativeID != "1001" {
		t.Fatalf("identity = %+v", got.Identity)
	}
	if !got.Rules.PriceIncrement.Equal(decimal.RequireFromString("0.01")) ||
		!got.Rules.QuantityIncrement.Equal(decimal.RequireFromString("0.0001")) ||
		!got.Rules.MinQuantity.Equal(decimal.RequireFromString("0.0001")) {
		t.Fatalf("rules = %+v", got.Rules)
	}
	if len(requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(requests))
	}
}

func TestOKXPerpInstrumentLoaderFiltersUSDTLiveSwapAndEncodesContracts(t *testing.T) {
	loader := NewOKXInstrumentLoader(exchange.ProductPerp, Settings{
		Endpoint: "https://okx.invalid",
		HTTPClient: &http.Client{Transport: okxCatalogRoundTripper(func(request *http.Request) (*http.Response, error) {
			if request.URL.Path != "/api/v5/public/instruments" || request.URL.Query().Get("instType") != okxSwapType {
				t.Fatalf("request = %s, want public SWAP instruments", request.URL.String())
			}
			return okxJSONResponse(`{"code":"0","msg":"","data":[
				{"instType":"SWAP","instId":"ETH-USDT-SWAP","baseCcy":"","quoteCcy":"","settleCcy":"USDT","ctValCcy":"ETH","ctVal":"0.01","tickSz":"0.1","lotSz":"1","minSz":"1","state":"live","instIdCode":2001},
				{"instType":"SWAP","instId":"BTC-USDC-SWAP","settleCcy":"USDC","ctValCcy":"BTC","ctVal":"0.01","tickSz":"0.1","lotSz":"1","minSz":"1","state":"live","instIdCode":2002},
				{"instType":"SWAP","instId":"TEST-USDT-SWAP","settleCcy":"USDT","ctValCcy":"TEST","ctVal":"","tickSz":"","lotSz":"","minSz":"","state":"preopen","instIdCode":2003}
			]}`), nil
		})},
	})

	snapshot, err := loader.LoadInstruments(context.Background(), exchange.CatalogScope{
		Venue:       exchange.VenueOKX,
		Environment: exchange.EnvironmentLive,
		Product:     exchange.ProductPerp,
		Settlement:  "USDT",
	})
	if err != nil {
		t.Fatalf("LoadInstruments: %v", err)
	}
	if len(snapshot.Instruments) != 1 {
		t.Fatalf("instruments = %+v, want only live USDT SWAP", snapshot.Instruments)
	}
	got := snapshot.Instruments[0]
	if got.Identity.Symbol != "ETH-USDT-SWAP" || got.Identity.NativeSymbol != "ETH-USDT-SWAP" ||
		got.Identity.NativeID != "2001" || got.Identity.SettleAsset != "USDT" {
		t.Fatalf("identity = %+v", got.Identity)
	}
	if !got.Encoding.ContractMultiplier.Valid || !got.Encoding.ContractMultiplier.Value.Equal(decimal.RequireFromString("0.01")) {
		t.Fatalf("contract multiplier = %+v", got.Encoding.ContractMultiplier)
	}
	if !got.Rules.QuantityIncrement.Equal(decimal.RequireFromString("0.01")) ||
		!got.Rules.MinQuantity.Equal(decimal.RequireFromString("0.01")) {
		t.Fatalf("contract-adjusted rules = %+v", got.Rules)
	}
	meta, err := okxContractMetaFromInstrument(got)
	if err != nil {
		t.Fatalf("okxContractMetaFromInstrument: %v", err)
	}
	if !meta.contractIncrement.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("contract increment = %s, want 1", meta.contractIncrement)
	}
}

func TestOKXInstrumentLoaderFailsClosedForMalformedLiveSupportedRows(t *testing.T) {
	_, err := NewOKXInstrumentLoader(exchange.ProductPerp, Settings{
		Endpoint: "https://okx.invalid",
		HTTPClient: &http.Client{Transport: okxCatalogRoundTripper(func(*http.Request) (*http.Response, error) {
			return okxJSONResponse(`{"code":"0","msg":"","data":[
				{"instType":"SWAP","instId":"ETH-USDT-SWAP","settleCcy":"USDT","ctValCcy":"ETH","ctVal":"0.01","tickSz":"","lotSz":"1","minSz":"1","state":"live","instIdCode":2001}
			]}`), nil
		})},
	}).LoadInstruments(context.Background(), exchange.CatalogScope{
		Venue:       exchange.VenueOKX,
		Environment: exchange.EnvironmentLive,
		Product:     exchange.ProductPerp,
		Settlement:  "USDT",
	})
	if err == nil {
		t.Fatal("malformed live supported OKX SWAP row was skipped instead of failing closed")
	}
}

func TestOKXSpotInstrumentLoaderFailsClosedWhenNativePartsMismatchMetadata(t *testing.T) {
	_, err := NewOKXInstrumentLoader(exchange.ProductSpot, Settings{
		Endpoint: "https://okx.invalid",
		HTTPClient: &http.Client{Transport: okxCatalogRoundTripper(func(*http.Request) (*http.Response, error) {
			return okxJSONResponse(`{"code":"0","msg":"","data":[
				{"instType":"SPOT","instId":"ETH-USDT","baseCcy":"BTC","quoteCcy":"USDT","tickSz":"0.01","lotSz":"0.0001","minSz":"0.0001","state":"live","instIdCode":1001}
			]}`), nil
		})},
	}).LoadInstruments(context.Background(), exchange.CatalogScope{
		Venue:       exchange.VenueOKX,
		Environment: exchange.EnvironmentLive,
		Product:     exchange.ProductSpot,
	})
	if err == nil {
		t.Fatal("live OKX SPOT row with instId/baseCcy mismatch was published")
	}
}

func TestOKXCatalogMetadataObservesMemoryCatalogRefreshWithoutSDKFallback(t *testing.T) {
	ctx := context.Background()
	descriptor := exchange.SourceDescriptor{
		Name:              "okx-usdt-perp",
		Endpoint:          "https://okx.invalid",
		WebSocketEndpoint: "wss://okx.invalid/ws",
		Transport:         "test",
	}
	scope := exchange.CatalogScope{
		Venue:               exchange.VenueOKX,
		Environment:         exchange.EnvironmentLive,
		Product:             exchange.ProductPerp,
		Settlement:          "USDT",
		EndpointFingerprint: descriptor.Fingerprint(),
	}
	loader := &mutableOKXInstrumentLoader{
		snapshot: exchange.InstrumentSnapshot{
			SchemaVersion: 1,
			Scope:         scope,
			LoadedAt:      time.Unix(1, 0),
			Instruments: []exchange.Instrument{
				okxCatalogTestInstrument(exchange.ProductPerp, "ETH", "USDT", "1001", decimal.RequireFromString("0.01")),
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
	client := NewOKXUSDTPerp("key", "secret", "passphrase", Settings{
		Endpoint:          "https://okx.invalid",
		InstrumentCatalog: catalog,
		CatalogScope:      scope,
		HTTPClient: &http.Client{Transport: okxCatalogRoundTripper(func(*http.Request) (*http.Response, error) {
			httpCalls++
			return nil, errors.New("SDK discovery must not run in catalog mode")
		})},
	}).(*okxPerpClient)

	rows, err := client.Instruments(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("initial Instruments rows=%d err=%v", len(rows), err)
	}
	if !rows[0].Rules.QuantityIncrement.Equal(decimal.RequireFromString("0.01")) {
		t.Fatalf("initial quantity increment = %s", rows[0].Rules.QuantityIncrement)
	}
	loader.snapshot = exchange.InstrumentSnapshot{
		SchemaVersion: 1,
		Scope:         scope,
		LoadedAt:      time.Unix(2, 0),
		Instruments: []exchange.Instrument{
			okxCatalogTestInstrument(exchange.ProductPerp, "ETH", "USDT", "1002", decimal.RequireFromString("0.02")),
		},
	}
	if _, err := catalog.Refresh(ctx, scope); err != nil {
		t.Fatalf("second Refresh: %v", err)
	}
	rows, err = client.Instruments(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("refreshed Instruments rows=%d err=%v", len(rows), err)
	}
	if rows[0].Identity.NativeID != "1002" || !rows[0].Rules.QuantityIncrement.Equal(decimal.RequireFromString("0.02")) {
		t.Fatalf("refreshed row = %+v", rows[0])
	}
	if httpCalls != 0 {
		t.Fatalf("SDK discovery calls = %d, want 0 in catalog mode", httpCalls)
	}
}

func TestOKXCatalogInstrumentCodeLoaderUsesSnapshotNativeID(t *testing.T) {
	instrument := okxCatalogTestInstrument(exchange.ProductSpot, "ETH", "USDT", "1001", decimal.RequireFromString("0.0001"))
	code, err := okxSpotInstrumentCodeLoader(clientMeta{})(context.Background(), "WatchOrders", instrument)
	if err != nil {
		t.Fatalf("instrument code loader: %v", err)
	}
	if code != 1001 {
		t.Fatalf("code = %d, want 1001", code)
	}

	instrument.Identity.NativeID = ""
	if _, err := okxSpotInstrumentCodeLoader(clientMeta{})(context.Background(), "WatchOrders", instrument); err == nil {
		t.Fatal("missing NativeID was accepted")
	}
}

func okxJSONResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func okxCatalogTestInstrument(product exchange.Product, base, quote, nativeID string, quantityIncrement decimal.Decimal) exchange.Instrument {
	native := base + "-" + quote
	settlement := ""
	contract := oneContract()
	minQuantity := quantityIncrement
	if product == exchange.ProductPerp {
		native += "-SWAP"
		settlement = quote
		contract = exchange.OptionalDecimal{Value: decimal.RequireFromString("0.01"), Valid: true}
		minQuantity = quantityIncrement
	}
	return newInstrument(
		exchange.VenueOKX,
		product,
		native,
		base,
		quote,
		settlement,
		native,
		nativeID,
		decimal.RequireFromString("0.1"),
		quantityIncrement,
		minQuantity,
		exchange.OptionalDecimal{},
		contract,
	)
}
