package factoryclient

import (
	"net/http"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/shopspring/decimal"
)

const (
	testAsterUserAddress = "0xf39fd6e51aad88f6f4ce6ab8827279cfffb92266"
	testAsterPrivateKey  = "0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
)

func asterOpenAPITestSettings(t *testing.T, router http.RoundTripper, product exchange.Product, settlement string) Settings {
	t.Helper()
	endpoint := "https://aster-fixture.invalid"
	descriptor := exchange.SourceDescriptor{
		Name:              "aster-openapi-" + string(product),
		Endpoint:          endpoint,
		WebSocketEndpoint: "wss://aster-fixture.invalid/ws",
		Transport:         "aster-openapi-fixture",
	}
	scope := exchange.CatalogScope{
		Venue:               exchange.VenueAster,
		Environment:         exchange.EnvironmentLive,
		Product:             product,
		Settlement:          settlement,
		EndpointFingerprint: descriptor.Fingerprint(),
	}
	settings := Settings{
		Endpoint:          endpoint,
		WebSocketEndpoint: descriptor.WebSocketEndpoint,
		HTTPClient:        &http.Client{Transport: router},
		SourceDescriptor:  descriptor,
		CatalogScope:      scope,
	}
	catalog := exchange.NewDefaultMemoryCatalog()
	settings.InstrumentCatalog = catalog
	if err := catalog.RegisterSource(scope, descriptor, NewAsterInstrumentLoader(product, settings)); err != nil {
		t.Fatalf("RegisterSource Aster %s: %v", product, err)
	}
	return settings
}

func asterTestInstrument(product exchange.Product, symbol string) exchange.Instrument {
	parts := strings.Split(symbol, "-")
	base, quote := parts[0], parts[1]
	settlement := ""
	if product == exchange.ProductPerp {
		settlement = quote
	}
	return newInstrument(
		exchange.VenueAster,
		product,
		symbol,
		base,
		quote,
		settlement,
		strings.ReplaceAll(symbol, "-", ""),
		"",
		decimal.RequireFromString("0.1"),
		decimal.RequireFromString("0.001"),
		decimal.RequireFromString("0.001"),
		exchange.OptionalDecimal{Value: decimal.RequireFromString("5"), Valid: true},
		oneContract(),
	)
}
