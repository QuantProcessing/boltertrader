package factoryclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/shopspring/decimal"
)

const (
	testAsterCatalogUserAddress = "0xf39fd6e51aad88f6f4ce6ab8827279cfffb92266"
	testAsterCatalogPrivateKey  = "0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
)

func TestAsterCatalogLoaderUsesPublicExchangeInfoAndExactScopes(t *testing.T) {
	router := newAsterCatalogRouter()
	settings, scope := asterCatalogTestSettings(t, exchange.ProductSpot, "", router)

	snapshot, err := NewAsterInstrumentLoader(exchange.ProductSpot, settings).LoadInstruments(context.Background(), scope)
	if err != nil {
		t.Fatalf("LoadInstruments spot: %v", err)
	}
	if len(snapshot.Instruments) != 1 {
		t.Fatalf("spot instruments = %d, want 1", len(snapshot.Instruments))
	}
	spot := snapshot.Instruments[0]
	if spot.Identity.Venue != exchange.VenueAster ||
		spot.Identity.Product != exchange.ProductSpot ||
		spot.Identity.Symbol != "BTC-USDT" ||
		spot.Identity.NativeSymbol != "BTCUSDT" ||
		spot.Identity.SettleAsset != "" ||
		!spot.Rules.PriceIncrement.Equal(decimal.RequireFromString("0.1")) ||
		!spot.Rules.QuantityIncrement.Equal(decimal.RequireFromString("0.001")) ||
		!spot.Rules.MinQuantity.Equal(decimal.RequireFromString("0.001")) ||
		!spot.Rules.MinNotional.Valid ||
		!spot.Rules.MinNotional.Value.Equal(decimal.RequireFromString("5")) {
		t.Fatalf("spot instrument = %+v", spot)
	}

	settings, scope = asterCatalogTestSettings(t, exchange.ProductPerp, "USDT", router)
	snapshot, err = NewAsterInstrumentLoader(exchange.ProductPerp, settings).LoadInstruments(context.Background(), scope)
	if err != nil {
		t.Fatalf("LoadInstruments perp: %v", err)
	}
	if len(snapshot.Instruments) != 1 {
		t.Fatalf("perp instruments = %d, want 1", len(snapshot.Instruments))
	}
	perp := snapshot.Instruments[0]
	if perp.Identity.Product != exchange.ProductPerp ||
		perp.Identity.SettleAsset != "USDT" ||
		perp.Identity.NativeSymbol != "BTCUSDT" ||
		!perp.Encoding.ContractMultiplier.Valid ||
		!perp.Encoding.ContractMultiplier.Value.Equal(decimal.RequireFromString("1")) {
		t.Fatalf("perp instrument = %+v", perp)
	}

	router.mu.Lock()
	defer router.mu.Unlock()
	for _, request := range router.requests {
		if strings.Contains(request, "signature=") || strings.Contains(request, "timestamp=") || strings.Contains(request, testAsterCatalogPrivateKey) {
			t.Fatalf("public catalog request carried private material: %s", request)
		}
	}
}

func TestAsterCatalogLoaderSkipsUnsupportedRowsButFailsClosedOnMalformedSupportedRows(t *testing.T) {
	router := newAsterCatalogRouter()
	router.spotBody = `{"symbols":[
		{"symbol":"OLDUSDT","status":"BREAK","baseAsset":"OLD","quoteAsset":"USDT","filters":[]},
		{"symbol":"BTCUSDT","status":"TRADING","baseAsset":"BTC","quoteAsset":"USDT","filters":[{"filterType":"PRICE_FILTER","tickSize":"0"},{"filterType":"LOT_SIZE","stepSize":"0.001","minQty":"0.001"}]}
	]}`
	settings, scope := asterCatalogTestSettings(t, exchange.ProductSpot, "", router)
	_, err := NewAsterInstrumentLoader(exchange.ProductSpot, settings).LoadInstruments(context.Background(), scope)
	if err == nil {
		t.Fatal("malformed supported live spot row was accepted")
	}

	router = newAsterCatalogRouter()
	router.perpBody = `{"symbols":[
		{"symbol":"BTCUSD","contractType":"CURRENT_QUARTER","status":"TRADING","baseAsset":"BTC","quoteAsset":"USD","marginAsset":"USD","filters":[]},
		{"symbol":"BTCUSDT","contractType":"PERPETUAL","status":"TRADING","baseAsset":"BTC","quoteAsset":"USDT","marginAsset":"USDT","filters":[{"filterType":"PRICE_FILTER","tickSize":"0.1"},{"filterType":"LOT_SIZE","stepSize":"0.001","minQty":"0.001"},{"filterType":"MIN_NOTIONAL","notional":"5"}]}
	]}`
	settings, scope = asterCatalogTestSettings(t, exchange.ProductPerp, "USDT", router)
	snapshot, err := NewAsterInstrumentLoader(exchange.ProductPerp, settings).LoadInstruments(context.Background(), scope)
	if err != nil {
		t.Fatalf("unsupported delivery row should be skipped: %v", err)
	}
	if len(snapshot.Instruments) != 1 || snapshot.Instruments[0].Identity.NativeSymbol != "BTCUSDT" {
		t.Fatalf("filtered perp instruments = %+v", snapshot.Instruments)
	}
}

func TestAsterClientUsesCatalogRefreshWithoutDirectMetadataFallback(t *testing.T) {
	router := newAsterCatalogRouter()
	catalog := exchange.NewDefaultMemoryCatalog()
	settings, scope := asterCatalogTestSettings(t, exchange.ProductSpot, "", router)
	settings.InstrumentCatalog = catalog
	settings.CatalogScope = scope
	if err := catalog.RegisterSource(scope, settings.SourceDescriptor, NewAsterInstrumentLoader(exchange.ProductSpot, settings)); err != nil {
		t.Fatalf("RegisterSource: %v", err)
	}
	client := NewAsterSpot(testAsterCatalogUserAddress, testAsterCatalogPrivateKey, testAsterCatalogUserAddress, settings)

	rows, err := client.Instruments(context.Background())
	if err != nil || len(rows) != 1 || rows[0].Identity.Symbol != "BTC-USDT" {
		t.Fatalf("initial Instruments rows=%+v err=%v", rows, err)
	}
	router.mu.Lock()
	router.spotBody = `{"symbols":[{"symbol":"ETHUSDT","status":"TRADING","baseAsset":"ETH","quoteAsset":"USDT","filters":[{"filterType":"PRICE_FILTER","tickSize":"0.01"},{"filterType":"LOT_SIZE","stepSize":"0.01","minQty":"0.01"},{"filterType":"MIN_NOTIONAL","minNotional":"5"}]}]}`
	router.mu.Unlock()

	rows, err = client.RefreshInstruments(context.Background())
	if err != nil || len(rows) != 1 || rows[0].Identity.Symbol != "ETH-USDT" {
		t.Fatalf("RefreshInstruments rows=%+v err=%v", rows, err)
	}
	resolved, err := client.ResolveInstrument(context.Background(), "ETH-USDT")
	if err != nil || resolved.Identity.NativeSymbol != "ETHUSDT" {
		t.Fatalf("ResolveInstrument after refresh = %+v err=%v", resolved, err)
	}
}

func TestAsterInstrumentCodecRejectsMismatchedNativeBeforeIO(t *testing.T) {
	router := newAsterCatalogRouter()
	settings, scope := asterCatalogTestSettings(t, exchange.ProductSpot, "", router)
	catalog := exchange.NewDefaultMemoryCatalog()
	settings.InstrumentCatalog = catalog
	settings.CatalogScope = scope
	if err := catalog.RegisterSource(scope, settings.SourceDescriptor, NewAsterInstrumentLoader(exchange.ProductSpot, settings)); err != nil {
		t.Fatalf("RegisterSource: %v", err)
	}
	client := NewAsterSpot(testAsterCatalogUserAddress, testAsterCatalogPrivateKey, testAsterCatalogUserAddress, settings)
	instrument, err := client.ResolveInstrument(context.Background(), "BTC-USDT")
	if err != nil {
		t.Fatalf("ResolveInstrument: %v", err)
	}
	instrument.Identity.NativeSymbol = "ETHUSDT"
	_, err = client.OrderBook(context.Background(), exchange.OrderBookRequest{Instrument: instrument, Limit: 5})
	if err == nil {
		t.Fatal("mismatched native symbol reached Aster transport")
	}
	router.mu.Lock()
	depthCalls := router.seen["GET /api/v3/depth"]
	router.mu.Unlock()
	if depthCalls != 0 {
		t.Fatalf("invalid instrument performed %d depth calls", depthCalls)
	}
}

func TestAsterWebSocketBackendsValidateInstrumentBeforeConnect(t *testing.T) {
	client := NewAsterSpot(testAsterCatalogUserAddress, testAsterCatalogPrivateKey, testAsterCatalogUserAddress, Settings{}).(*asterSpotClient)
	backend := &asterSpotWSBackend{client: client}
	bad := newInstrument(
		exchange.VenueAster,
		exchange.ProductSpot,
		"BTC-USDT",
		"BTC",
		"USDT",
		"",
		"ETHUSDT",
		"",
		decimal.RequireFromString("0.1"),
		decimal.RequireFromString("0.001"),
		decimal.RequireFromString("0.001"),
		exchange.OptionalDecimal{Value: decimal.RequireFromString("5"), Valid: true},
		oneContract(),
	)
	if _, err := backend.StartOrderBook(context.Background(), bad, streamCallbacks[exchange.BookEvent]{}); err == nil {
		t.Fatal("StartOrderBook accepted mismatched native symbol")
	}
	if backend.market != nil {
		t.Fatal("StartOrderBook connected before instrument validation")
	}
	if _, err := backend.StartOrders(context.Background(), bad, streamCallbacks[exchange.OrderEvent]{}); err == nil {
		t.Fatal("StartOrders accepted mismatched native symbol")
	}
	if backend.account != nil {
		t.Fatal("StartOrders connected before instrument validation")
	}
}

func TestAsterRESTMatrixUsesResolvedCatalogInstruments(t *testing.T) {
	router := newAsterCatalogRouter()
	ctx := context.Background()

	spotSettings, spotScope := asterCatalogTestSettings(t, exchange.ProductSpot, "", router)
	spotCatalog := exchange.NewDefaultMemoryCatalog()
	spotSettings.InstrumentCatalog = spotCatalog
	spotSettings.CatalogScope = spotScope
	if err := spotCatalog.RegisterSource(spotScope, spotSettings.SourceDescriptor, NewAsterInstrumentLoader(exchange.ProductSpot, spotSettings)); err != nil {
		t.Fatalf("RegisterSource spot: %v", err)
	}
	spot := NewAsterSpot(testAsterCatalogUserAddress, testAsterCatalogPrivateKey, testAsterCatalogUserAddress, spotSettings)
	spotInstrument, err := spot.ResolveInstrument(ctx, "BTC-USDT")
	if err != nil {
		t.Fatalf("spot ResolveInstrument: %v", err)
	}
	if _, err := spot.OrderBook(ctx, exchange.OrderBookRequest{Instrument: spotInstrument, Limit: 5}); err != nil {
		t.Fatalf("spot OrderBook: %v", err)
	}
	if _, err := spot.PlaceOrder(ctx, exchange.PlaceOrderRequest{Instrument: spotInstrument, ClientOrderID: "101", Side: exchange.SideBuy, Type: exchange.OrderTypeLimit, Quantity: decimal.RequireFromString("1"), LimitPrice: decimal.RequireFromString("99"), LimitPolicy: exchange.LimitPolicyResting}); err != nil {
		t.Fatalf("spot PlaceOrder: %v", err)
	}
	if _, err := spot.CancelOrder(ctx, exchange.CancelOrderRequest{Instrument: spotInstrument, OrderID: "11"}); err != nil {
		t.Fatalf("spot CancelOrder: %v", err)
	}
	if page, err := spot.OpenOrders(ctx, exchange.OpenOrdersRequest{Instrument: &spotInstrument, Limit: 1}); err != nil || len(page.Orders) != 1 || page.Orders[0].NativeSymbol != "BTCUSDT" {
		t.Fatalf("spot OpenOrders page=%+v err=%v", page, err)
	}
	if page, err := spot.OrderHistory(ctx, exchange.OrderHistoryRequest{Instrument: &spotInstrument, Limit: 1}); err != nil || len(page.Orders) != 1 {
		t.Fatalf("spot OrderHistory page=%+v err=%v", page, err)
	}
	if page, err := spot.Fills(ctx, exchange.FillsRequest{Instrument: &spotInstrument, Limit: 1}); err != nil || len(page.Fills) != 1 || page.Fills[0].NativeSymbol != "BTCUSDT" {
		t.Fatalf("spot Fills page=%+v err=%v", page, err)
	}
	if balances, err := spot.Balances(ctx); err != nil || len(balances) != 1 {
		t.Fatalf("spot Balances rows=%+v err=%v", balances, err)
	}

	perpSettings, perpScope := asterCatalogTestSettings(t, exchange.ProductPerp, "USDT", router)
	perpCatalog := exchange.NewDefaultMemoryCatalog()
	perpSettings.InstrumentCatalog = perpCatalog
	perpSettings.CatalogScope = perpScope
	if err := perpCatalog.RegisterSource(perpScope, perpSettings.SourceDescriptor, NewAsterInstrumentLoader(exchange.ProductPerp, perpSettings)); err != nil {
		t.Fatalf("RegisterSource perp: %v", err)
	}
	perp := NewAsterUSDTPerp(testAsterCatalogUserAddress, testAsterCatalogPrivateKey, testAsterCatalogUserAddress, perpSettings)
	perpInstrument, err := perp.ResolveInstrument(ctx, "BTC-USDT")
	if err != nil {
		t.Fatalf("perp ResolveInstrument: %v", err)
	}
	if _, err := perp.PlaceOrder(ctx, exchange.PlaceOrderRequest{Instrument: perpInstrument, ClientOrderID: "201", Side: exchange.SideBuy, Type: exchange.OrderTypeLimit, Quantity: decimal.RequireFromString("1"), LimitPrice: decimal.RequireFromString("99"), LimitPolicy: exchange.LimitPolicyResting, ReduceOnly: true}); err != nil {
		t.Fatalf("perp PlaceOrder: %v", err)
	}
	if _, err := perp.CancelOrder(ctx, exchange.CancelOrderRequest{Instrument: perpInstrument, OrderID: "21"}); err != nil {
		t.Fatalf("perp CancelOrder: %v", err)
	}
	if rows, err := perp.Positions(ctx, exchange.PositionsRequest{Instrument: &perpInstrument}); err != nil || len(rows) != 1 || rows[0].NativeSymbol != "BTCUSDT" {
		t.Fatalf("perp Positions rows=%+v err=%v", rows, err)
	}
	if row, err := perp.FundingRate(ctx, exchange.FundingRateRequest{Instrument: perpInstrument}); err != nil || row.NativeSymbol != "BTCUSDT" {
		t.Fatalf("perp FundingRate row=%+v err=%v", row, err)
	}
	if _, err := perp.SetLeverage(ctx, exchange.SetLeverageRequest{Instrument: perpInstrument, Leverage: 5}); err != nil {
		t.Fatalf("perp SetLeverage: %v", err)
	}
}

func TestAsterAccountWideLifecyclePreservesRawNativeSymbols(t *testing.T) {
	router := newAsterCatalogRouter()
	ctx := context.Background()

	spotSettings, spotScope := asterCatalogTestSettings(t, exchange.ProductSpot, "", router)
	spotCatalog := exchange.NewDefaultMemoryCatalog()
	spotSettings.InstrumentCatalog = spotCatalog
	spotSettings.CatalogScope = spotScope
	if err := spotCatalog.RegisterSource(spotScope, spotSettings.SourceDescriptor, NewAsterInstrumentLoader(exchange.ProductSpot, spotSettings)); err != nil {
		t.Fatalf("RegisterSource spot: %v", err)
	}
	spot := NewAsterSpot(testAsterCatalogUserAddress, testAsterCatalogPrivateKey, testAsterCatalogUserAddress, spotSettings)
	if page, err := spot.OpenOrders(ctx, exchange.OpenOrdersRequest{Limit: 10}); err != nil || len(page.Orders) != 1 || page.Orders[0].NativeSymbol != "BTCUSDT" {
		t.Fatalf("spot account-wide OpenOrders page=%+v err=%v", page, err)
	}
	if page, err := spot.OrderHistory(ctx, exchange.OrderHistoryRequest{Limit: 10}); err != nil || len(page.Orders) != 1 || page.Orders[0].NativeSymbol != "BTCUSDT" {
		t.Fatalf("spot account-wide OrderHistory page=%+v err=%v", page, err)
	}
	if page, err := spot.Fills(ctx, exchange.FillsRequest{Limit: 10}); err != nil || len(page.Fills) != 1 || page.Fills[0].NativeSymbol != "BTCUSDT" {
		t.Fatalf("spot account-wide Fills page=%+v err=%v", page, err)
	}

	perpSettings, perpScope := asterCatalogTestSettings(t, exchange.ProductPerp, "USDT", router)
	perpCatalog := exchange.NewDefaultMemoryCatalog()
	perpSettings.InstrumentCatalog = perpCatalog
	perpSettings.CatalogScope = perpScope
	if err := perpCatalog.RegisterSource(perpScope, perpSettings.SourceDescriptor, NewAsterInstrumentLoader(exchange.ProductPerp, perpSettings)); err != nil {
		t.Fatalf("RegisterSource perp: %v", err)
	}
	perp := NewAsterUSDTPerp(testAsterCatalogUserAddress, testAsterCatalogPrivateKey, testAsterCatalogUserAddress, perpSettings)
	if page, err := perp.OpenOrders(ctx, exchange.OpenOrdersRequest{Limit: 10}); err != nil || len(page.Orders) != 1 || page.Orders[0].NativeSymbol != "BTCUSDT" {
		t.Fatalf("perp account-wide OpenOrders page=%+v err=%v", page, err)
	}
	if page, err := perp.OrderHistory(ctx, exchange.OrderHistoryRequest{Limit: 10}); err != nil || len(page.Orders) != 1 || page.Orders[0].NativeSymbol != "BTCUSDT" {
		t.Fatalf("perp account-wide OrderHistory page=%+v err=%v", page, err)
	}
	if page, err := perp.Fills(ctx, exchange.FillsRequest{Limit: 10}); err != nil || len(page.Fills) != 1 || page.Fills[0].NativeSymbol != "BTCUSDT" {
		t.Fatalf("perp account-wide Fills page=%+v err=%v", page, err)
	}
	if rows, err := perp.Positions(ctx, exchange.PositionsRequest{}); err != nil || len(rows) != 1 || rows[0].NativeSymbol != "BTCUSDT" {
		t.Fatalf("perp account-wide Positions rows=%+v err=%v", rows, err)
	}
}

func TestAsterLifecycleFiltersCompareRawNativeSymbols(t *testing.T) {
	router := newAsterCatalogRouter()
	ctx := context.Background()

	spotSettings, spotScope := asterCatalogTestSettings(t, exchange.ProductSpot, "", router)
	spotCatalog := exchange.NewDefaultMemoryCatalog()
	spotSettings.InstrumentCatalog = spotCatalog
	spotSettings.CatalogScope = spotScope
	if err := spotCatalog.RegisterSource(spotScope, spotSettings.SourceDescriptor, NewAsterInstrumentLoader(exchange.ProductSpot, spotSettings)); err != nil {
		t.Fatalf("RegisterSource spot: %v", err)
	}
	spot := NewAsterSpot(testAsterCatalogUserAddress, testAsterCatalogPrivateKey, testAsterCatalogUserAddress, spotSettings)
	spotOther := asterTestInstrument(exchange.ProductSpot, "ETH-USDT")
	if page, err := spot.OpenOrders(ctx, exchange.OpenOrdersRequest{Instrument: &spotOther, Limit: 10}); err != nil || len(page.Orders) != 0 {
		t.Fatalf("spot filtered OpenOrders page=%+v err=%v", page, err)
	}
	if page, err := spot.OrderHistory(ctx, exchange.OrderHistoryRequest{Instrument: &spotOther, Limit: 10}); err != nil || len(page.Orders) != 0 {
		t.Fatalf("spot filtered OrderHistory page=%+v err=%v", page, err)
	}
	if page, err := spot.Fills(ctx, exchange.FillsRequest{Instrument: &spotOther, Limit: 10}); err != nil || len(page.Fills) != 0 {
		t.Fatalf("spot filtered Fills page=%+v err=%v", page, err)
	}

	perpSettings, perpScope := asterCatalogTestSettings(t, exchange.ProductPerp, "USDT", router)
	perpCatalog := exchange.NewDefaultMemoryCatalog()
	perpSettings.InstrumentCatalog = perpCatalog
	perpSettings.CatalogScope = perpScope
	if err := perpCatalog.RegisterSource(perpScope, perpSettings.SourceDescriptor, NewAsterInstrumentLoader(exchange.ProductPerp, perpSettings)); err != nil {
		t.Fatalf("RegisterSource perp: %v", err)
	}
	perp := NewAsterUSDTPerp(testAsterCatalogUserAddress, testAsterCatalogPrivateKey, testAsterCatalogUserAddress, perpSettings)
	perpOther := asterTestInstrument(exchange.ProductPerp, "ETH-USDT")
	if page, err := perp.OpenOrders(ctx, exchange.OpenOrdersRequest{Instrument: &perpOther, Limit: 10}); err != nil || len(page.Orders) != 0 {
		t.Fatalf("perp filtered OpenOrders page=%+v err=%v", page, err)
	}
	if page, err := perp.OrderHistory(ctx, exchange.OrderHistoryRequest{Instrument: &perpOther, Limit: 10}); err != nil || len(page.Orders) != 0 {
		t.Fatalf("perp filtered OrderHistory page=%+v err=%v", page, err)
	}
	if page, err := perp.Fills(ctx, exchange.FillsRequest{Instrument: &perpOther, Limit: 10}); err != nil || len(page.Fills) != 0 {
		t.Fatalf("perp filtered Fills page=%+v err=%v", page, err)
	}
	if rows, err := perp.Positions(ctx, exchange.PositionsRequest{Instrument: &perpOther}); err != nil || len(rows) != 0 {
		t.Fatalf("perp filtered Positions rows=%+v err=%v", rows, err)
	}
}

func TestAsterPlaceOrderAckRejectsMismatchedResponseIdentity(t *testing.T) {
	router := newAsterCatalogRouter()
	ctx := context.Background()

	spotSettings, spotScope := asterCatalogTestSettings(t, exchange.ProductSpot, "", router)
	spotCatalog := exchange.NewDefaultMemoryCatalog()
	spotSettings.InstrumentCatalog = spotCatalog
	spotSettings.CatalogScope = spotScope
	if err := spotCatalog.RegisterSource(spotScope, spotSettings.SourceDescriptor, NewAsterInstrumentLoader(exchange.ProductSpot, spotSettings)); err != nil {
		t.Fatalf("RegisterSource spot: %v", err)
	}
	spot := NewAsterSpot(testAsterCatalogUserAddress, testAsterCatalogPrivateKey, testAsterCatalogUserAddress, spotSettings)
	spotInstrument, err := spot.ResolveInstrument(ctx, "BTC-USDT")
	if err != nil {
		t.Fatalf("spot ResolveInstrument: %v", err)
	}
	router.spotPlaceSymbol = "ETHUSDT"
	if _, err := spot.PlaceOrder(ctx, exchange.PlaceOrderRequest{Instrument: spotInstrument, ClientOrderID: "101", Side: exchange.SideBuy, Type: exchange.OrderTypeLimit, Quantity: decimal.RequireFromString("1"), LimitPrice: decimal.RequireFromString("99"), LimitPolicy: exchange.LimitPolicyResting}); !errors.Is(err, exchange.ErrMalformedResponse) {
		t.Fatalf("spot PlaceOrder symbol mismatch error = %v, want ErrMalformedResponse", err)
	}
	router.spotPlaceSymbol = "BTCUSDT"
	router.spotPlaceClientOrderID = "wrong-client"
	if _, err := spot.PlaceOrder(ctx, exchange.PlaceOrderRequest{Instrument: spotInstrument, ClientOrderID: "101", Side: exchange.SideBuy, Type: exchange.OrderTypeLimit, Quantity: decimal.RequireFromString("1"), LimitPrice: decimal.RequireFromString("99"), LimitPolicy: exchange.LimitPolicyResting}); !errors.Is(err, exchange.ErrMalformedResponse) {
		t.Fatalf("spot PlaceOrder client id mismatch error = %v, want ErrMalformedResponse", err)
	}

	router = newAsterCatalogRouter()
	perpSettings, perpScope := asterCatalogTestSettings(t, exchange.ProductPerp, "USDT", router)
	perpCatalog := exchange.NewDefaultMemoryCatalog()
	perpSettings.InstrumentCatalog = perpCatalog
	perpSettings.CatalogScope = perpScope
	if err := perpCatalog.RegisterSource(perpScope, perpSettings.SourceDescriptor, NewAsterInstrumentLoader(exchange.ProductPerp, perpSettings)); err != nil {
		t.Fatalf("RegisterSource perp: %v", err)
	}
	perp := NewAsterUSDTPerp(testAsterCatalogUserAddress, testAsterCatalogPrivateKey, testAsterCatalogUserAddress, perpSettings)
	perpInstrument, err := perp.ResolveInstrument(ctx, "BTC-USDT")
	if err != nil {
		t.Fatalf("perp ResolveInstrument: %v", err)
	}
	router.perpPlaceSymbol = "ETHUSDT"
	if _, err := perp.PlaceOrder(ctx, exchange.PlaceOrderRequest{Instrument: perpInstrument, ClientOrderID: "201", Side: exchange.SideBuy, Type: exchange.OrderTypeLimit, Quantity: decimal.RequireFromString("1"), LimitPrice: decimal.RequireFromString("99"), LimitPolicy: exchange.LimitPolicyResting, ReduceOnly: true}); !errors.Is(err, exchange.ErrMalformedResponse) {
		t.Fatalf("perp PlaceOrder symbol mismatch error = %v, want ErrMalformedResponse", err)
	}
	router.perpPlaceSymbol = "BTCUSDT"
	router.perpPlaceClientOrderID = "wrong-client"
	if _, err := perp.PlaceOrder(ctx, exchange.PlaceOrderRequest{Instrument: perpInstrument, ClientOrderID: "201", Side: exchange.SideBuy, Type: exchange.OrderTypeLimit, Quantity: decimal.RequireFromString("1"), LimitPrice: decimal.RequireFromString("99"), LimitPolicy: exchange.LimitPolicyResting, ReduceOnly: true}); !errors.Is(err, exchange.ErrMalformedResponse) {
		t.Fatalf("perp PlaceOrder client id mismatch error = %v, want ErrMalformedResponse", err)
	}
}

func TestAsterCancelOrderAckRejectsMismatchedResponseIdentity(t *testing.T) {
	router := newAsterCatalogRouter()
	ctx := context.Background()

	spotSettings, spotScope := asterCatalogTestSettings(t, exchange.ProductSpot, "", router)
	spotCatalog := exchange.NewDefaultMemoryCatalog()
	spotSettings.InstrumentCatalog = spotCatalog
	spotSettings.CatalogScope = spotScope
	if err := spotCatalog.RegisterSource(spotScope, spotSettings.SourceDescriptor, NewAsterInstrumentLoader(exchange.ProductSpot, spotSettings)); err != nil {
		t.Fatalf("RegisterSource spot: %v", err)
	}
	spot := NewAsterSpot(testAsterCatalogUserAddress, testAsterCatalogPrivateKey, testAsterCatalogUserAddress, spotSettings)
	spotInstrument, err := spot.ResolveInstrument(ctx, "BTC-USDT")
	if err != nil {
		t.Fatalf("spot ResolveInstrument: %v", err)
	}
	router.spotCancelSymbol = "ETHUSDT"
	if _, err := spot.CancelOrder(ctx, exchange.CancelOrderRequest{Instrument: spotInstrument, OrderID: "11"}); !errors.Is(err, exchange.ErrMalformedResponse) {
		t.Fatalf("spot CancelOrder symbol mismatch error = %v, want ErrMalformedResponse", err)
	}
	router.spotCancelSymbol = "BTCUSDT"
	router.spotCancelOrderID = 12
	if _, err := spot.CancelOrder(ctx, exchange.CancelOrderRequest{Instrument: spotInstrument, OrderID: "11"}); !errors.Is(err, exchange.ErrMalformedResponse) {
		t.Fatalf("spot CancelOrder order id mismatch error = %v, want ErrMalformedResponse", err)
	}

	router = newAsterCatalogRouter()
	perpSettings, perpScope := asterCatalogTestSettings(t, exchange.ProductPerp, "USDT", router)
	perpCatalog := exchange.NewDefaultMemoryCatalog()
	perpSettings.InstrumentCatalog = perpCatalog
	perpSettings.CatalogScope = perpScope
	if err := perpCatalog.RegisterSource(perpScope, perpSettings.SourceDescriptor, NewAsterInstrumentLoader(exchange.ProductPerp, perpSettings)); err != nil {
		t.Fatalf("RegisterSource perp: %v", err)
	}
	perp := NewAsterUSDTPerp(testAsterCatalogUserAddress, testAsterCatalogPrivateKey, testAsterCatalogUserAddress, perpSettings)
	perpInstrument, err := perp.ResolveInstrument(ctx, "BTC-USDT")
	if err != nil {
		t.Fatalf("perp ResolveInstrument: %v", err)
	}
	router.perpCancelSymbol = "ETHUSDT"
	if _, err := perp.CancelOrder(ctx, exchange.CancelOrderRequest{Instrument: perpInstrument, OrderID: "21"}); !errors.Is(err, exchange.ErrMalformedResponse) {
		t.Fatalf("perp CancelOrder symbol mismatch error = %v, want ErrMalformedResponse", err)
	}
	router.perpCancelSymbol = "BTCUSDT"
	router.perpCancelOrderID = 22
	if _, err := perp.CancelOrder(ctx, exchange.CancelOrderRequest{Instrument: perpInstrument, OrderID: "21"}); !errors.Is(err, exchange.ErrMalformedResponse) {
		t.Fatalf("perp CancelOrder order id mismatch error = %v, want ErrMalformedResponse", err)
	}
}

type asterCatalogRouter struct {
	mu                     sync.Mutex
	seen                   map[string]int
	requests               []string
	spotBody               string
	perpBody               string
	spotPlaceSymbol        string
	spotPlaceClientOrderID string
	spotCancelSymbol       string
	spotCancelOrderID      int64
	perpPlaceSymbol        string
	perpPlaceClientOrderID string
	perpCancelSymbol       string
	perpCancelOrderID      int64
}

func newAsterCatalogRouter() *asterCatalogRouter {
	return &asterCatalogRouter{
		seen: make(map[string]int),
		spotBody: `{"symbols":[
			{"symbol":"BTCUSDT","status":"TRADING","baseAsset":"BTC","quoteAsset":"USDT","filters":[{"filterType":"PRICE_FILTER","tickSize":"0.1"},{"filterType":"LOT_SIZE","stepSize":"0.001","minQty":"0.001"},{"filterType":"MIN_NOTIONAL","minNotional":"5"}]},
			{"symbol":"DELISTUSDT","status":"HALT","baseAsset":"DELIST","quoteAsset":"USDT","filters":[]}
		]}`,
		perpBody: `{"symbols":[
			{"symbol":"BTCUSDT","contractType":"PERPETUAL","status":"TRADING","baseAsset":"BTC","quoteAsset":"USDT","marginAsset":"USDT","filters":[{"filterType":"PRICE_FILTER","tickSize":"0.1"},{"filterType":"LOT_SIZE","stepSize":"0.001","minQty":"0.001"},{"filterType":"MIN_NOTIONAL","notional":"5"}]},
			{"symbol":"ETHUSDC","contractType":"PERPETUAL","status":"TRADING","baseAsset":"ETH","quoteAsset":"USDC","marginAsset":"USDC","filters":[{"filterType":"PRICE_FILTER","tickSize":"0.1"},{"filterType":"LOT_SIZE","stepSize":"0.001","minQty":"0.001"}]}
		]}`,
	}
}

func (router *asterCatalogRouter) RoundTrip(request *http.Request) (*http.Response, error) {
	key := request.Method + " " + request.URL.Path
	router.mu.Lock()
	router.seen[key]++
	router.requests = append(router.requests, request.URL.String())
	spotBody := router.spotBody
	perpBody := router.perpBody
	spotPlaceSymbol := asterDefaultValue(router.spotPlaceSymbol, "BTCUSDT")
	spotPlaceClientOrderID := router.spotPlaceClientOrderID
	spotCancelSymbol := asterDefaultValue(router.spotCancelSymbol, "BTCUSDT")
	spotCancelOrderID := router.spotCancelOrderID
	if spotCancelOrderID == 0 {
		spotCancelOrderID = 11
	}
	perpPlaceSymbol := asterDefaultValue(router.perpPlaceSymbol, "BTCUSDT")
	perpPlaceClientOrderID := router.perpPlaceClientOrderID
	perpCancelSymbol := asterDefaultValue(router.perpCancelSymbol, "BTCUSDT")
	perpCancelOrderID := router.perpCancelOrderID
	if perpCancelOrderID == 0 {
		perpCancelOrderID = 21
	}
	router.mu.Unlock()
	switch request.URL.Path {
	case "/api/v3/exchangeInfo":
		return asterCatalogJSONResponse(spotBody), nil
	case "/fapi/v3/exchangeInfo":
		return asterCatalogJSONResponse(perpBody), nil
	case "/api/v3/depth":
		return asterCatalogJSONResponse(`{"lastUpdateId":7,"bids":[["99","1"]],"asks":[["101","2"]]}`), nil
	case "/api/v3/order":
		if request.Method == http.MethodDelete {
			return asterCatalogJSONResponse(`{"symbol":"` + spotCancelSymbol + `","orderId":` + strconv.FormatInt(spotCancelOrderID, 10) + `,"clientOrderId":"cancel","status":"CANCELED"}`), nil
		}
		clientID := request.URL.Query().Get("newClientOrderId")
		if spotPlaceClientOrderID != "" {
			clientID = spotPlaceClientOrderID
		}
		return asterCatalogJSONResponse(`{"symbol":"` + spotPlaceSymbol + `","orderId":11,"clientOrderId":"` + clientID + `","time":1720000000000,"price":"99","origQty":"1","executedQty":"0","cumQuote":"0","status":"NEW","timeInForce":"GTC","type":"LIMIT","side":"BUY"}`), nil
	case "/api/v3/openOrders":
		return asterCatalogJSONResponse(`[{"symbol":"BTCUSDT","orderId":11,"clientOrderId":"101","price":"99","origQty":"1","executedQty":"0","status":"NEW","timeInForce":"GTC","type":"LIMIT","side":"BUY"}]`), nil
	case "/api/v3/allOrders":
		return asterCatalogJSONResponse(`[{"symbol":"BTCUSDT","orderId":10,"clientOrderId":"100","price":"99","origQty":"1","executedQty":"1","cumQuote":"99","status":"FILLED","timeInForce":"GTC","type":"LIMIT","side":"BUY"}]`), nil
	case "/api/v3/userTrades":
		return asterCatalogJSONResponse(`[{"symbol":"BTCUSDT","id":1,"orderId":10,"price":"99","qty":"1","commission":"0.001","commissionAsset":"BTC","time":1720000000000,"buyer":true,"maker":false}]`), nil
	case "/api/v3/account":
		return asterCatalogJSONResponse(`{"balances":[{"asset":"USDT","free":"100","locked":"2"}]}`), nil
	case "/fapi/v3/order":
		if request.Method == http.MethodDelete {
			return asterCatalogJSONResponse(`{"symbol":"` + perpCancelSymbol + `","orderId":` + strconv.FormatInt(perpCancelOrderID, 10) + `,"clientOrderId":"201","price":"99","origQty":"1","executedQty":"0","avgPrice":"0","status":"CANCELED","timeInForce":"GTC","type":"LIMIT","side":"BUY","positionSide":"BOTH","reduceOnly":true,"updateTime":1720000000000}`), nil
		}
		clientID := request.URL.Query().Get("newClientOrderId")
		if perpPlaceClientOrderID != "" {
			clientID = perpPlaceClientOrderID
		}
		return asterCatalogJSONResponse(`{"symbol":"` + perpPlaceSymbol + `","orderId":21,"clientOrderId":"` + clientID + `","price":"99","origQty":"1","executedQty":"0","cumQty":"0","cumQuote":"0","avgPrice":"0","status":"NEW","timeInForce":"GTC","type":"LIMIT","side":"BUY","positionSide":"BOTH","reduceOnly":true,"updateTime":1720000000000}`), nil
	case "/fapi/v3/openOrders":
		return asterCatalogJSONResponse(`[{"symbol":"BTCUSDT","orderId":21,"clientOrderId":"201","price":"99","origQty":"1","executedQty":"0","avgPrice":"0","status":"NEW","timeInForce":"GTC","type":"LIMIT","side":"BUY","positionSide":"BOTH","reduceOnly":true,"updateTime":1720000000000}]`), nil
	case "/fapi/v3/allOrders":
		return asterCatalogJSONResponse(`[{"symbol":"BTCUSDT","orderId":20,"clientOrderId":"200","price":"99","origQty":"1","executedQty":"1","avgPrice":"99","status":"FILLED","timeInForce":"GTC","type":"LIMIT","side":"BUY","positionSide":"BOTH","reduceOnly":true,"updateTime":1720000000000}]`), nil
	case "/fapi/v3/userTrades":
		return asterCatalogJSONResponse(`[{"symbol":"BTCUSDT","id":2,"orderId":20,"price":"99","qty":"1","quoteQty":"99","commission":"0.01","commissionAsset":"USDT","time":1720000000000,"side":"BUY","maker":false,"positionSide":"BOTH"}]`), nil
	case "/fapi/v3/positionRisk":
		return asterCatalogJSONResponse(`[{"symbol":"BTCUSDT","positionAmt":"1","entryPrice":"99","markPrice":"100","unRealizedProfit":"1","liquidationPrice":"50","leverage":"5","isolatedMargin":"0","positionSide":"BOTH"}]`), nil
	case "/fapi/v3/balance":
		return asterCatalogJSONResponse(`[{"asset":"USDT","balance":"100","availableBalance":"90"}]`), nil
	case "/fapi/v3/accountWithJoinMargin":
		return asterCatalogJSONResponse(`{"totalMarginBalance":"100","availableBalance":"90","totalInitialMargin":"10","totalUnrealizedProfit":"1","assets":[{"asset":"USDT","walletBalance":"100","availableBalance":"90"}]}`), nil
	case "/fapi/v3/premiumIndex":
		return asterCatalogJSONResponse(`{"symbol":"BTCUSDT","markPrice":"100","lastFundingRate":"0.0001","nextFundingTime":1720003600000,"time":1720000000000}`), nil
	case "/fapi/v3/leverage":
		return asterCatalogJSONResponse(`{"symbol":"BTCUSDT","leverage":5,"maxNotionalValue":"1000000"}`), nil
	default:
		return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader(`{"code":-1,"msg":"unexpected Aster route"}`)), Header: make(http.Header)}, nil
	}
}

func asterCatalogJSONResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func asterDefaultValue(value string, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func asterCatalogTestSettings(t *testing.T, product exchange.Product, settlement string, router *asterCatalogRouter) (Settings, exchange.CatalogScope) {
	t.Helper()
	endpoint := "https://aster-catalog.invalid"
	wsEndpoint := "wss://aster-catalog.invalid/ws"
	descriptor := exchange.SourceDescriptor{
		Name:              "aster-" + string(product),
		Endpoint:          endpoint,
		WebSocketEndpoint: wsEndpoint,
		Transport:         "test-transport",
	}
	scope := exchange.CatalogScope{
		Venue:               exchange.VenueAster,
		Environment:         exchange.EnvironmentLive,
		Product:             product,
		Settlement:          settlement,
		EndpointFingerprint: descriptor.Fingerprint(),
	}
	return Settings{
		Endpoint:          endpoint,
		WebSocketEndpoint: wsEndpoint,
		HTTPClient:        &http.Client{Transport: router},
		SourceDescriptor:  descriptor,
	}, scope
}
