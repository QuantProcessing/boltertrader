package factoryclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/shopspring/decimal"
)

func TestNadoInstrumentLoaderUsesPublicMetadataOnlyAndPreservesNativeIdentity(t *testing.T) {
	settings := nadoCatalogTestSettings(newNadoOpenAPIRouter(t), exchange.ProductSpot)
	spotSnapshot, err := NewNadoInstrumentLoader(exchange.ProductSpot, settings).LoadInstruments(context.Background(), settings.CatalogScope)
	if err != nil {
		t.Fatalf("Nado spot LoadInstruments: %v", err)
	}
	if len(spotSnapshot.Instruments) != 1 {
		t.Fatalf("Nado spot instruments = %d, want 1", len(spotSnapshot.Instruments))
	}
	spot := spotSnapshot.Instruments[0]
	if spot.Identity.Symbol != "ETH-USDT0" ||
		spot.Identity.NativeSymbol != "ETH_USDT0" ||
		spot.Identity.NativeID != "1" ||
		spot.Identity.BaseAsset != "ETH" ||
		spot.Identity.QuoteAsset != "USDT0" ||
		spot.Identity.SettleAsset != "USDT0" {
		t.Fatalf("Nado spot identity = %+v", spot.Identity)
	}
	if !spot.Rules.PriceIncrement.Equal(decimal.RequireFromString("0.1")) ||
		!spot.Rules.QuantityIncrement.Equal(decimal.RequireFromString("0.001")) ||
		!spot.Rules.MinQuantity.Equal(decimal.RequireFromString("0.001")) ||
		!spot.Rules.MinNotional.Valid ||
		!spot.Rules.MinNotional.Value.Equal(decimal.NewFromInt(5)) {
		t.Fatalf("Nado spot rules = %+v", spot.Rules)
	}

	settings = nadoCatalogTestSettings(newNadoOpenAPIRouter(t), exchange.ProductPerp)
	perpSnapshot, err := NewNadoInstrumentLoader(exchange.ProductPerp, settings).LoadInstruments(context.Background(), settings.CatalogScope)
	if err != nil {
		t.Fatalf("Nado perp LoadInstruments: %v", err)
	}
	if len(perpSnapshot.Instruments) != 1 {
		t.Fatalf("Nado perp instruments = %d, want 1", len(perpSnapshot.Instruments))
	}
	perp := perpSnapshot.Instruments[0]
	if perp.Identity.Symbol != "ETH-PERP-USDT0" ||
		perp.Identity.NativeSymbol != "ETH-PERP_USDT0" ||
		perp.Identity.NativeID != "2" ||
		perp.Identity.BaseAsset != "ETH" ||
		perp.Identity.QuoteAsset != "USDT0" ||
		perp.Identity.SettleAsset != "USDT0" {
		t.Fatalf("Nado perp identity = %+v", perp.Identity)
	}
}

func TestNadoOrderInputRejectsNonrepresentableX18BeforeIO(t *testing.T) {
	var executeCalls atomic.Int32
	var metadataCalls atomic.Int32
	baseRouter := newNadoOpenAPIRouter(t)
	router := nadoRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/v1/execute" {
			executeCalls.Add(1)
		}
		if request.URL.Path == "/v2/pairs" ||
			(request.URL.Path == "/v1/query" && (request.URL.Query().Get("type") == "all_products" || request.URL.Query().Get("type") == "symbols")) {
			metadataCalls.Add(1)
		}
		return baseRouter.RoundTrip(request)
	})
	settings := nadoOpenAPITestSettings(t, router, exchange.ProductSpot)
	client := NewNadoSpot(testAsterPrivateKey, "default", settings)
	instrument, err := client.ResolveInstrument(context.Background(), "ETH_USDT0")
	if err != nil {
		t.Fatalf("ResolveInstrument: %v", err)
	}
	metadataCalls.Store(0)
	instrument.Rules.PriceIncrement = decimal.RequireFromString("0.0000000000000000001")
	_, err = client.PlaceOrder(context.Background(), exchange.PlaceOrderRequest{
		Instrument:    instrument,
		ClientOrderID: "101",
		Side:          exchange.SideBuy,
		Type:          exchange.OrderTypeLimit,
		Quantity:      decimal.RequireFromString("1.001"),
		LimitPrice:    decimal.RequireFromString("99.0000000000000000001"),
		LimitPolicy:   exchange.LimitPolicyResting,
	})
	if !errors.Is(err, exchange.ErrInvalidRequest) {
		t.Fatalf("PlaceOrder error = %v, want ErrInvalidRequest", err)
	}
	if executeCalls.Load() != 0 {
		t.Fatalf("Nado execute calls = %d, want 0 before x18 rejection", executeCalls.Load())
	}
	if metadataCalls.Load() != 0 {
		t.Fatalf("Nado metadata calls after catalog preload = %d, want 0", metadataCalls.Load())
	}
}

func nadoOpenAPITestSettings(t *testing.T, router http.RoundTripper, product exchange.Product) Settings {
	t.Helper()
	settings := nadoCatalogTestSettings(router, product)
	catalog := exchange.NewDefaultMemoryCatalog()
	settings.InstrumentCatalog = catalog
	if err := catalog.RegisterSource(settings.CatalogScope, settings.SourceDescriptor, NewNadoInstrumentLoader(product, settings)); err != nil {
		t.Fatalf("RegisterSource Nado %s: %v", product, err)
	}
	return settings
}

func nadoCatalogTestSettings(router http.RoundTripper, product exchange.Product) Settings {
	endpoint := "https://nado-fixture.invalid/v1"
	descriptor := exchange.SourceDescriptor{
		Name:              "nado-openapi-" + string(product),
		Endpoint:          endpoint,
		WebSocketEndpoint: "wss://nado-fixture.invalid/ws",
		Transport:         "nado-openapi-fixture",
	}
	settlement := ""
	if product == exchange.ProductPerp {
		settlement = "USDT0"
	}
	scope := exchange.CatalogScope{
		Venue:               exchange.VenueNado,
		Environment:         exchange.EnvironmentTestnet,
		Product:             product,
		Settlement:          settlement,
		EndpointFingerprint: descriptor.Fingerprint(),
	}
	return Settings{
		Environment:       "testnet",
		Endpoint:          endpoint,
		WebSocketEndpoint: descriptor.WebSocketEndpoint,
		HTTPClient:        &http.Client{Transport: router},
		SourceDescriptor:  descriptor,
		CatalogScope:      scope,
		InstrumentCatalog: exchange.NewDefaultMemoryCatalog(),
	}
}

func TestNadoInstrumentLoaderFailsClosedForMalformedSupportedRows(t *testing.T) {
	router := nadoRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		response, err := newNadoOpenAPIRouter(t).RoundTrip(request)
		if err != nil || request.URL.Path != "/v1/query" || request.URL.Query().Get("type") != "symbols" {
			return response, err
		}
		body, err := io.ReadAll(response.Body)
		if err != nil {
			return nil, err
		}
		_ = response.Body.Close()
		body = []byte(strings.Replace(string(body), `"price_increment_x18": "100000000000000000"`, `"price_increment_x18": "not-x18"`, 1))
		response.Body = io.NopCloser(strings.NewReader(string(body)))
		response.ContentLength = int64(len(body))
		return response, nil
	})
	settings := nadoCatalogTestSettings(router, exchange.ProductSpot)
	_, err := NewNadoInstrumentLoader(exchange.ProductSpot, settings).LoadInstruments(context.Background(), settings.CatalogScope)
	if !errors.Is(err, exchange.ErrMalformedResponse) {
		t.Fatalf("LoadInstruments error = %v, want ErrMalformedResponse", err)
	}
}
