package factoryclient

import (
	"context"
	"net/http"
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
	gate "github.com/QuantProcessing/boltertrader/sdk/gate"
	"github.com/shopspring/decimal"
)

func TestGateInstrumentLoaderLoadsSpotAndUSDTPerpCatalogMetadata(t *testing.T) {
	ctx := context.Background()
	router := &gateOpenAPIRouter{}
	settings := Settings{
		Endpoint:   "https://openapi.invalid",
		HTTPClient: &http.Client{Transport: router},
	}

	spotScope := exchange.CatalogScope{Venue: exchange.VenueGate, Environment: exchange.EnvironmentLive, Product: exchange.ProductSpot}
	spotSnapshot, err := NewGateInstrumentLoader(exchange.ProductSpot, settings).LoadInstruments(ctx, spotScope)
	if err != nil {
		t.Fatalf("spot LoadInstruments: %v", err)
	}
	if len(spotSnapshot.Instruments) != 1 {
		t.Fatalf("spot instruments = %d, want 1", len(spotSnapshot.Instruments))
	}
	spot := spotSnapshot.Instruments[0]
	if spot.Identity.NativeSymbol != "BTC_USDT" || spot.Identity.Symbol != "BTC-USDT" ||
		spot.Identity.SettleAsset != "" || spot.Identity.Venue != exchange.VenueGate ||
		spot.Identity.Product != exchange.ProductSpot {
		t.Fatalf("spot identity = %+v", spot.Identity)
	}
	if !spot.Rules.PriceIncrement.Equal(decimal.RequireFromString("0.1")) ||
		!spot.Rules.QuantityIncrement.Equal(decimal.RequireFromString("0.001")) ||
		!spot.Rules.MinQuantity.Equal(decimal.RequireFromString("0.001")) ||
		!spot.Rules.MinNotional.Valid ||
		!spot.Rules.MinNotional.Value.Equal(decimal.RequireFromString("5")) ||
		!spot.Encoding.ContractMultiplier.Valid ||
		!spot.Encoding.ContractMultiplier.Value.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("spot metadata = rules:%+v encoding:%+v", spot.Rules, spot.Encoding)
	}

	perpScope := exchange.CatalogScope{Venue: exchange.VenueGate, Environment: exchange.EnvironmentLive, Product: exchange.ProductPerp, Settlement: "USDT"}
	perpSnapshot, err := NewGateInstrumentLoader(exchange.ProductPerp, settings).LoadInstruments(ctx, perpScope)
	if err != nil {
		t.Fatalf("perp LoadInstruments: %v", err)
	}
	if len(perpSnapshot.Instruments) != 1 {
		t.Fatalf("perp instruments = %d, want 1", len(perpSnapshot.Instruments))
	}
	perp := perpSnapshot.Instruments[0]
	if perp.Identity.NativeSymbol != "BTC_USDT" || perp.Identity.Symbol != "BTC-USDT" ||
		perp.Identity.SettleAsset != "USDT" || perp.Identity.Venue != exchange.VenueGate ||
		perp.Identity.Product != exchange.ProductPerp {
		t.Fatalf("perp identity = %+v", perp.Identity)
	}
	if !perp.Rules.PriceIncrement.Equal(decimal.RequireFromString("0.1")) ||
		!perp.Rules.QuantityIncrement.Equal(decimal.RequireFromString("0.001")) ||
		!perp.Rules.MinQuantity.Equal(decimal.RequireFromString("0.001")) ||
		!perp.Encoding.ContractMultiplier.Valid ||
		!perp.Encoding.ContractMultiplier.Value.Equal(decimal.RequireFromString("0.001")) {
		t.Fatalf("perp metadata = rules:%+v encoding:%+v", perp.Rules, perp.Encoding)
	}
}

func TestGateCatalogBackedInstrumentsAndResolveUseRegisteredSource(t *testing.T) {
	ctx := context.Background()
	router := &gateOpenAPIRouter{}
	catalog := exchange.NewDefaultMemoryCatalog()
	settings := Settings{
		Endpoint:          "https://openapi.invalid",
		WebSocketEndpoint: "wss://ws.invalid/v4/ws/spot",
		HTTPClient:        &http.Client{Transport: router},
		InstrumentCatalog: catalog,
		CatalogScope: exchange.CatalogScope{
			Venue:       exchange.VenueGate,
			Environment: exchange.EnvironmentLive,
			Product:     exchange.ProductSpot,
		},
		SourceDescriptor: exchange.SourceDescriptor{Name: "gate-spot", Endpoint: "https://openapi.invalid", WebSocketEndpoint: "wss://ws.invalid/v4/ws/spot", Transport: "custom-http-client"},
	}
	settings.CatalogScope.EndpointFingerprint = settings.SourceDescriptor.Fingerprint()
	if err := catalog.RegisterSource(settings.CatalogScope, settings.SourceDescriptor, NewGateInstrumentLoader(exchange.ProductSpot, settings)); err != nil {
		t.Fatalf("RegisterSource: %v", err)
	}
	client := NewGateSpot("key", "secret", settings)

	instruments, err := client.Instruments(ctx)
	if err != nil {
		t.Fatalf("Instruments: %v", err)
	}
	if len(instruments) != 1 || instruments[0].Identity.NativeSymbol != "BTC_USDT" {
		t.Fatalf("instruments = %+v", instruments)
	}
	resolved, err := client.ResolveInstrument(ctx, "BTC_USDT")
	if err != nil {
		t.Fatalf("ResolveInstrument: %v", err)
	}
	if resolved.Identity.NativeSymbol != "BTC_USDT" {
		t.Fatalf("resolved = %+v", resolved)
	}
	if router.saw("GET /spot/currency_pairs/BTC_USDT?", "") {
		t.Fatalf("ResolveInstrument used SDK single-instrument fallback: %#v", router.seen)
	}
}

func TestGatePerpPlaceOrderRequiresCatalogMultiplierBeforeTransport(t *testing.T) {
	ctx := context.Background()
	router := &gateOpenAPIRouter{}
	client := NewGateUSDTPerp("key", "secret", Settings{
		Endpoint:   "https://openapi.invalid",
		HTTPClient: &http.Client{Transport: router},
	})
	inst := gateTestInstrument(exchange.ProductPerp)
	inst.Encoding.ContractMultiplier = exchange.OptionalDecimal{}

	_, err := client.PlaceOrder(ctx, exchange.PlaceOrderRequest{
		Instrument:    inst,
		ClientOrderID: "101",
		Side:          exchange.SideBuy,
		Type:          exchange.OrderTypeLimit,
		Quantity:      decimal.RequireFromString("0.05"),
		LimitPrice:    decimal.NewFromInt(100),
		LimitPolicy:   exchange.LimitPolicyResting,
	})
	assertGateErrorKind(t, err, exchange.KindMetadataResolution)
	if router.saw("GET /futures/usdt/contracts/BTC_USDT?", "") || len(router.placeBodies) != 0 {
		t.Fatalf("missing catalog multiplier reached Gate transport: seen=%#v bodies=%#v", router.seen, router.placeBodies)
	}
}

func TestGateCatalogFiltersUnsupportedRowsBeforeValidation(t *testing.T) {
	spotRows := []gate.CurrencyPair{
		{ID: "", Base: "", Quote: "", TradeStatus: "untradable"},
		{ID: "BTC_USDT", Base: "BTC", Quote: "USDT", MinBaseAmount: "0.001", MinQuoteAmount: "5", AmountPrecision: 3, Precision: 1, TradeStatus: "tradable"},
	}
	spot, err := gateSpotCatalogInstruments(spotRows)
	if err != nil {
		t.Fatalf("spot catalog instruments: %v", err)
	}
	if len(spot) != 1 || spot[0].Identity.NativeSymbol != "BTC_USDT" || spot[0].Identity.Symbol != "BTC-USDT" {
		t.Fatalf("spot instruments = %+v", spot)
	}

	perpRows := []gate.Contract{
		{Name: "", Status: "delisted", OrderSizeMin: -1, OrderPriceRound: "not-a-decimal", QuantoMultiplier: "0"},
		{Name: "BTC_USDT", Status: "trading", OrderSizeMin: 1, OrderPriceRound: "0.1", QuantoMultiplier: "0.001"},
	}
	perp, err := gatePerpCatalogInstruments(perpRows, gate.SettleUSDT)
	if err != nil {
		t.Fatalf("perp catalog instruments: %v", err)
	}
	if len(perp) != 1 || perp[0].Identity.NativeSymbol != "BTC_USDT" || perp[0].Identity.Symbol != "BTC-USDT" {
		t.Fatalf("perp instruments = %+v", perp)
	}
}

func TestGateCatalogSourceSkipsFuturesRowsWithoutPositiveMinimumSize(t *testing.T) {
	rows := gateCatalogTradableFuturesRows([]gate.Contract{
		{Name: "BNX_USDT", Status: "trading", OrderSizeMin: 0, OrderPriceRound: "0.0001", QuantoMultiplier: "1"},
		{Name: "BTC_USDT", Status: "trading", OrderSizeMin: 1, OrderPriceRound: "0.1", QuantoMultiplier: "0.001"},
		{Name: "OLD_USDT", Status: "delisted", OrderSizeMin: 1, OrderPriceRound: "0.1", QuantoMultiplier: "0.001"},
	})
	if len(rows) != 1 || rows[0].Name != "BTC_USDT" {
		t.Fatalf("tradable futures rows = %+v, want only BTC_USDT", rows)
	}
}

func TestGateCatalogMalformedSupportedRowsFailClosed(t *testing.T) {
	if _, err := gateSpotCatalogInstruments([]gate.CurrencyPair{{
		ID: "BTC_USDT", Base: "BTC", Quote: "USDT", MinBaseAmount: "not-a-decimal", AmountPrecision: 3, Precision: 1, TradeStatus: "tradable",
	}}); err == nil {
		t.Fatal("malformed supported spot row unexpectedly succeeded")
	} else {
		assertGateErrorKind(t, err, exchange.KindMalformedResponse)
	}

	if _, err := gatePerpCatalogInstruments([]gate.Contract{{
		Name: "BTC_USDT", Status: "trading", OrderSizeMin: 1, OrderPriceRound: "not-a-decimal", QuantoMultiplier: "0.001",
	}}, gate.SettleUSDT); err == nil {
		t.Fatal("malformed supported futures row unexpectedly succeeded")
	} else {
		assertGateErrorKind(t, err, exchange.KindMalformedResponse)
	}
}

func TestGateCatalogRequiresPositivePriceQuantityAndMinimums(t *testing.T) {
	for _, row := range []gate.CurrencyPair{
		{ID: "BTC_USDT", Base: "BTC", Quote: "USDT", MinBaseAmount: "0", AmountPrecision: 3, Precision: 1, TradeStatus: "tradable"},
		{ID: "BTC_USDT", Base: "BTC", Quote: "USDT", MinBaseAmount: "0.001", MinQuoteAmount: "0", AmountPrecision: 3, Precision: 1, TradeStatus: "tradable"},
	} {
		if _, err := gateSpotCatalogInstruments([]gate.CurrencyPair{row}); err == nil {
			t.Fatalf("non-positive spot row unexpectedly succeeded: %+v", row)
		} else {
			assertGateErrorKind(t, err, exchange.KindMalformedResponse)
		}
	}

	for _, row := range []gate.Contract{
		{Name: "BTC_USDT", Status: "trading", OrderSizeMin: 0, OrderPriceRound: "0.1", QuantoMultiplier: "0.001"},
		{Name: "BTC_USDT", Status: "trading", OrderSizeMin: 1, OrderPriceRound: "0", QuantoMultiplier: "0.001"},
		{Name: "BTC_USDT", Status: "trading", OrderSizeMin: 1, OrderPriceRound: "0.1", QuantoMultiplier: "0"},
	} {
		if _, err := gatePerpCatalogInstruments([]gate.Contract{row}, gate.SettleUSDT); err == nil {
			t.Fatalf("non-positive futures row unexpectedly succeeded: %+v", row)
		} else {
			assertGateErrorKind(t, err, exchange.KindMalformedResponse)
		}
	}
}

func gateTestInstrument(product exchange.Product) exchange.Instrument {
	settle := ""
	if product == exchange.ProductPerp {
		settle = "USDT"
	}
	multiplier := exchange.OptionalDecimal{Value: decimal.NewFromInt(1), Valid: true}
	if product == exchange.ProductPerp {
		multiplier = exchange.OptionalDecimal{Value: decimal.RequireFromString("0.001"), Valid: true}
	}
	return newInstrument(
		exchange.VenueGate,
		product,
		"BTC_USDT",
		"BTC",
		"USDT",
		settle,
		"BTC_USDT",
		"",
		decimal.RequireFromString("0.1"),
		multiplier.Value,
		multiplier.Value,
		exchange.OptionalDecimal{Value: decimal.NewFromInt(5), Valid: product == exchange.ProductSpot},
		multiplier,
	)
}

func gateInstrumentPtr(instrument exchange.Instrument) *exchange.Instrument {
	return &instrument
}

func gateTestSettings(product exchange.Product, transport http.RoundTripper) Settings {
	catalog := exchange.NewDefaultMemoryCatalog()
	scope := exchange.CatalogScope{
		Venue:       exchange.VenueGate,
		Environment: exchange.EnvironmentLive,
		Product:     product,
	}
	if product == exchange.ProductPerp {
		scope.Settlement = "USDT"
	}
	descriptor := exchange.SourceDescriptor{
		Name:              "gate-" + string(product),
		Endpoint:          "https://openapi.invalid",
		WebSocketEndpoint: "wss://ws.invalid/v4/ws/spot",
		Transport:         "custom-http-client",
	}
	scope.EndpointFingerprint = descriptor.Fingerprint()
	loader := exchange.StaticInstrumentLoader{Snapshot: exchange.InstrumentSnapshot{
		SchemaVersion: 1,
		Scope:         scope,
		Instruments:   []exchange.Instrument{gateTestInstrument(product)},
	}}
	if err := catalog.RegisterSource(scope, descriptor, loader); err != nil {
		panic(err)
	}
	return Settings{
		Endpoint:          descriptor.Endpoint,
		WebSocketEndpoint: descriptor.WebSocketEndpoint,
		HTTPClient:        &http.Client{Transport: transport},
		InstrumentCatalog: catalog,
		CatalogScope:      scope,
		SourceDescriptor:  descriptor,
	}
}
