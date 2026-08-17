package factoryclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	nadosdk "github.com/QuantProcessing/boltertrader/sdk/nado"
	"github.com/shopspring/decimal"
)

func TestNadoCandleMaxTimeOmitsZeroEnd(t *testing.T) {
	if got := nadoCandleMaxTime(time.Time{}); got != 0 {
		t.Fatalf("zero end max time = %d, want omitted zero", got)
	}
	end := time.Unix(1720000000, 0)
	if got := nadoCandleMaxTime(end); got != end.Unix() {
		t.Fatalf("non-zero end max time = %d, want %d", got, end.Unix())
	}
}

func TestNadoLocalMetadataPathsHonorContextBeforeIO(t *testing.T) {
	transport := &noSendTransport{}
	instrument := nadoTestInstrument(exchange.ProductPerp)
	settings := nadoCatalogTestSettings(transport, exchange.ProductPerp)
	catalog := exchange.NewMemoryCatalog(exchange.StaticInstrumentLoader{
		Snapshot: exchange.InstrumentSnapshot{
			Scope:       settings.CatalogScope,
			Instruments: []exchange.Instrument{instrument},
		},
	}, nil)
	if _, err := catalog.Refresh(context.Background(), settings.CatalogScope); err != nil {
		t.Fatalf("preload Nado catalog: %v", err)
	}
	settings.InstrumentCatalog = catalog
	catalogClient := NewNadoUSDT0Perp(testAsterPrivateKey, "default", settings)
	resolved, err := catalogClient.ResolveInstrument(context.Background(), instrument.Identity.Symbol)
	if err != nil {
		t.Fatalf("ResolveInstrument: %v", err)
	}

	noCatalogSettings := Settings{
		Endpoint:   settings.Endpoint,
		HTTPClient: &http.Client{Transport: transport},
	}
	noCatalogClient := NewNadoUSDT0Perp(testAsterPrivateKey, "default", noCatalogSettings)

	operations := []struct {
		name      string
		operation string
		call      func(context.Context) error
	}{
		{
			name:      "catalog SetLeverage",
			operation: "SetLeverage",
			call: func(ctx context.Context) error {
				_, err := catalogClient.SetLeverage(ctx, exchange.SetLeverageRequest{Instrument: resolved, Leverage: 3})
				return err
			},
		},
		{
			name:      "catalog Instruments",
			operation: "Instruments",
			call: func(ctx context.Context) error {
				_, err := catalogClient.Instruments(ctx)
				return err
			},
		},
		{
			name:      "no catalog SetLeverage",
			operation: "SetLeverage",
			call: func(ctx context.Context) error {
				_, err := noCatalogClient.SetLeverage(ctx, exchange.SetLeverageRequest{Instrument: instrument, Leverage: 3})
				return err
			},
		},
	}
	contexts := []struct {
		name string
		make func() (context.Context, context.CancelFunc)
		kind exchange.ErrorKind
		want error
	}{
		{
			name: "nil",
			make: func() (context.Context, context.CancelFunc) { return nil, func() {} },
			kind: exchange.KindInvalidRequest,
			want: exchange.ErrInvalidRequest,
		},
		{
			name: "canceled",
			make: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, func() {}
			},
			kind: exchange.KindCanceled,
			want: context.Canceled,
		},
		{
			name: "deadline",
			make: func() (context.Context, context.CancelFunc) {
				return context.WithDeadline(context.Background(), time.Unix(0, 0))
			},
			kind: exchange.KindDeadlineExceeded,
			want: context.DeadlineExceeded,
		},
	}

	for _, operation := range operations {
		for _, contextCase := range contexts {
			t.Run(operation.name+"/"+contextCase.name, func(t *testing.T) {
				ctx, cancel := contextCase.make()
				defer cancel()
				err := operation.call(ctx)
				if !errors.Is(err, contextCase.want) {
					t.Fatalf("error = %v, want %v", err, contextCase.want)
				}
				var normalized *exchange.Error
				if !errors.As(err, &normalized) {
					t.Fatalf("error type = %T, want *exchange.Error", err)
				}
				if normalized.Kind() != contextCase.kind {
					t.Fatalf("error kind = %q, want %q", normalized.Kind(), contextCase.kind)
				}
				details := normalized.Details()
				if details.Venue != exchange.VenueNado || details.Product != exchange.ProductPerp || details.Operation != operation.operation {
					t.Fatalf("error details = %+v, want Nado perp %s", details, operation.operation)
				}
			})
		}
	}
	if got := transport.calls.Load(); got != 0 {
		t.Fatalf("context boundary made %d SDK calls, want 0", got)
	}
}

func TestNadoFillAllowsArchiveRowsWithoutTimestamp(t *testing.T) {
	fill, err := nadoMatchFill(nadosdk.Match{
		Digest:        "0x1111111111111111111111111111111111111111111111111111111111111111",
		BaseFilled:    "1000000000000000000",
		Fee:           "1000000000000000",
		SubmissionIdx: "1",
		Order: nadosdk.MatchOrder{
			PriceX18: "1000000000000000000",
			Amount:   "1000000000000000000",
		},
	}, nadoProduct{
		canonical:  "USDC-USDT0",
		quote:      "USDT0",
		marketType: nadosdk.MarketTypeSpot,
	})
	if err != nil {
		t.Fatalf("nadoMatchFill: %v", err)
	}
	if !fill.Time.IsZero() {
		t.Fatalf("missing archive timestamp fabricated as %s", fill.Time)
	}
}

type nadoRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn nadoRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestNadoOpenAPIFixtureFileCoversRESTMatrix(t *testing.T) {
	TestOpenAPINadoRESTExecutionMatrix(t)
}

func TestNadoMinSizeIsQuoteDenominatedMinNotional(t *testing.T) {
	client := NewNadoSpot(testAsterPrivateKey, "default", Settings{
		Endpoint:   "https://nado-fixture.invalid/v1",
		HTTPClient: &http.Client{Transport: newNadoOpenAPIRouter(t)},
	})
	instruments, err := client.Instruments(context.Background())
	if err != nil {
		t.Fatalf("Instruments: %v", err)
	}
	for _, instrument := range instruments {
		if instrument.Identity.Symbol != "ETH-USDT0" {
			continue
		}
		if !instrument.Rules.MinQuantity.Equal(instrument.Rules.QuantityIncrement) {
			t.Fatalf("MinQuantity = %s, want quantity increment %s", instrument.Rules.MinQuantity, instrument.Rules.QuantityIncrement)
		}
		if !instrument.Rules.MinNotional.Valid || !instrument.Rules.MinNotional.Value.Equal(decimal.NewFromInt(5)) {
			t.Fatalf("MinNotional = %+v, want 5 USDT0", instrument.Rules.MinNotional)
		}
		return
	}
	t.Fatal("ETH-USDT0 instrument not found")
}

func TestNadoInstrumentsStayWithinClientProductScope(t *testing.T) {
	ctx := context.Background()
	spot := NewNadoSpot(testAsterPrivateKey, "default", nadoCatalogSettings(t, exchange.ProductSpot, newNadoOpenAPIRouter(t)))
	spotInstruments, err := spot.Instruments(ctx)
	if err != nil {
		t.Fatalf("spot Instruments: %v", err)
	}
	if len(spotInstruments) != 1 || spotInstruments[0].Identity.Product != exchange.ProductSpot || spotInstruments[0].Identity.Symbol != "ETH-USDT0" {
		t.Fatalf("spot Instruments = %+v, want only spot scope", spotInstruments)
	}

	perp := NewNadoUSDT0Perp(testAsterPrivateKey, "default", nadoCatalogSettings(t, exchange.ProductPerp, newNadoOpenAPIRouter(t)))
	perpInstruments, err := perp.Instruments(ctx)
	if err != nil {
		t.Fatalf("perp Instruments: %v", err)
	}
	if len(perpInstruments) != 1 || perpInstruments[0].Identity.Product != exchange.ProductPerp || perpInstruments[0].Identity.Symbol != "ETH-PERP-USDT0" {
		t.Fatalf("perp Instruments = %+v, want only perp scope", perpInstruments)
	}
}

func TestNadoMarketOrderLimitPriceStaysOnVenueTick(t *testing.T) {
	tick := decimal.RequireFromString("0.0001")
	best := decimal.RequireFromString("1.0006")

	buy, err := nadoMarketOrderLimitPrice(best, tick, exchange.SideBuy)
	if err != nil {
		t.Fatalf("buy price: %v", err)
	}
	if want := decimal.RequireFromString("1.0507"); !buy.Equal(want) {
		t.Fatalf("buy price = %s, want %s", buy, want)
	}

	sell, err := nadoMarketOrderLimitPrice(best, tick, exchange.SideSell)
	if err != nil {
		t.Fatalf("sell price: %v", err)
	}
	if want := decimal.RequireFromString("0.9505"); !sell.Equal(want) {
		t.Fatalf("sell price = %s, want %s", sell, want)
	}
}

func TestNadoTerminalOrderUpdateAllowsOfficialZeroRemainingAmount(t *testing.T) {
	event, err := nadoOrderUpdateEvent(&nadosdk.OrderUpdate{
		Type:      "order_update",
		Timestamp: "1695081920633151000",
		ProductId: 5,
		Digest:    "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Amount:    "0",
		Reason:    nadosdk.OrderReasonFilled,
		Id:        "100",
	}, nadoProduct{
		canonical:  "USDC-USDT0",
		productID:  5,
		marketType: nadosdk.MarketTypeSpot,
	}, exchange.ProductSpot, "WatchOrders")
	if err != nil {
		t.Fatalf("terminal order update: %v", err)
	}
	if event.Order.Status != "filled" || !event.Order.Quantity.IsZero() || event.Order.Side != "" {
		t.Fatalf("terminal order event = %+v", event)
	}
	if want := time.Unix(0, 1695081920633151000).UTC(); !event.Order.UpdatedAt.Equal(want) {
		t.Fatalf("terminal order event time = %s, want %s", event.Order.UpdatedAt, want)
	}
}

func TestNadoPositionChangeAllowsOfficialFlatPosition(t *testing.T) {
	instrument := nadoTestInstrument(exchange.ProductPerp)
	event, err := nadoPositionEvent(&nadosdk.PositionChange{
		Type:         "position_change",
		Timestamp:    "1783641600",
		ProductId:    2,
		Amount:       "0",
		VQuoteAmount: "0",
		Reason:       nadosdk.PositionReasonMatchOrders,
	}, nadoProduct{
		canonical:  "ETH-PERP-USDT0",
		native:     "ETH-PERP_USDT0",
		productID:  2,
		marketType: nadosdk.MarketTypePerp,
		instrument: instrument,
	}, "WatchPositions")
	if err != nil {
		t.Fatalf("flat position update: %v", err)
	}
	if len(event.Positions) != 1 {
		t.Fatalf("flat position event = %+v, want one position", event)
	}
	position := event.Positions[0]
	if position.NativeSymbol != "ETH-PERP_USDT0" || position.NativeID != "2" || !position.Quantity.IsZero() || position.Side != "" || !position.EntryPrice.IsZero() {
		t.Fatalf("flat position = %+v, want zero quantity with no directional side", position)
	}
}

func TestNadoStreamTimestampAcceptsDocumentedUnits(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  time.Time
	}{
		{name: "seconds", value: "1783641600", want: time.Unix(1783641600, 0).UTC()},
		{name: "milliseconds", value: "1783641600300", want: time.UnixMilli(1783641600300).UTC()},
		{name: "nanoseconds", value: "1695081920633151000", want: time.Unix(0, 1695081920633151000).UTC()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := nadoUnixTime(tc.value, exchange.ProductSpot, "WatchOrders", "order timestamp")
			if err != nil {
				t.Fatalf("nadoUnixTime: %v", err)
			}
			if !got.Equal(tc.want) {
				t.Fatalf("time = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestNadoAmbiguousGatewayFailurePreservesSafeVenueCode(t *testing.T) {
	base := &nadoBase{meta: clientMeta{venue: exchange.VenueNado, product: exchange.ProductSpot}}
	ack := baseAck(base.meta, exchange.OrderOperationPlace, "ETH-USDT0", "", "101")
	err := base.mutationError(
		"PlaceOrder",
		nadosdk.NewGatewayApplicationError(2500, "sensitive upstream detail", "place_order"),
		&ack,
	)
	var exchangeErr *exchange.Error
	if !errors.As(err, &exchangeErr) {
		t.Fatalf("error=%T %v, want *exchange.Error", err, err)
	}
	if exchangeErr.Kind() != exchange.KindAmbiguousOutcome || exchangeErr.Details().Code != "2500" {
		t.Fatalf("error=%+v, want ambiguous outcome with code 2500", exchangeErr)
	}
	if ack.VenueCode != "2500" || strings.Contains(err.Error(), "sensitive upstream detail") {
		t.Fatalf("ack=%+v err=%v", ack, err)
	}
}

func TestNadoSubscriptionCloseTreatsDisconnectedSocketAsAlreadyClosed(t *testing.T) {
	calls := 0
	stop := nadoIdempotentUnsubscribe(func() error {
		calls++
		return errors.New("not connected")
	})
	if err := stop(); err != nil {
		t.Fatalf("idempotent unsubscribe: %v", err)
	}
	if calls != 1 {
		t.Fatalf("unsubscribe calls = %d, want 1", calls)
	}

	want := errors.New("unsubscribe failed")
	if err := nadoIdempotentUnsubscribe(func() error { return want })(); !errors.Is(err, want) {
		t.Fatalf("non-disconnect error = %v, want %v", err, want)
	}
}

func TestNadoPerpAccountDoesNotFabricateAvailable(t *testing.T) {
	client := NewNadoUSDT0Perp(testAsterPrivateKey, "default", Settings{Endpoint: "https://nado-fixture.invalid/v1", HTTPClient: &http.Client{Transport: newNadoOpenAPIRouter(t)}})
	account, err := client.PerpAccount(context.Background())
	if err != nil {
		t.Fatalf("PerpAccount: %v", err)
	}
	if account.Available.Valid {
		t.Fatalf("Nado Available was fabricated: %+v", account.Available)
	}
}

func TestNadoBalanceSnapshotRequiresCatalogProductMetadata(t *testing.T) {
	settings := nadoCatalogSettings(t, exchange.ProductSpot, nadoRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if nadoIsSubaccountInfoRequest(request) {
			return asterOpenAPIJSONResponse(`{"status":"success","data":{"subaccount":"default","exists":true,"spot_balances":[{"product_id":1,"balance":{"amount":"1000000000000000000"}},{"product_id":999,"balance":{"amount":"2000000000000000000"}}],"perp_balances":[],"healths":[]},"request_type":"subaccount_info"}`), nil
		}
		return newNadoOpenAPIRouter(t).RoundTrip(request)
	}))
	client := NewNadoSpot(testAsterPrivateKey, "default", settings)
	_, err := client.SpotAccount(context.Background())
	if !errors.Is(err, exchange.ErrMetadataResolution) {
		t.Fatalf("SpotAccount unknown balance product error = %v, want ErrMetadataResolution", err)
	}
}

func TestNadoBalanceSnapshotSkipsUnknownZeroBalanceProducts(t *testing.T) {
	settings := nadoCatalogSettings(t, exchange.ProductSpot, nadoRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if nadoIsSubaccountInfoRequest(request) {
			return asterOpenAPIJSONResponse(`{"status":"success","data":{"subaccount":"default","exists":true,"spot_balances":[{"product_id":1,"balance":{"amount":"1000000000000000000"}},{"product_id":999,"balance":{"amount":"0"}}],"perp_balances":[],"healths":[]},"request_type":"subaccount_info"}`), nil
		}
		return newNadoOpenAPIRouter(t).RoundTrip(request)
	}))
	client := NewNadoSpot(testAsterPrivateKey, "default", settings)
	account, err := client.SpotAccount(context.Background())
	if err != nil {
		t.Fatalf("SpotAccount: %v", err)
	}
	if len(account.Balances) != 1 || account.Balances[0].Asset != "ETH" {
		t.Fatalf("balances = %+v, want only known non-zero ETH balance", account.Balances)
	}
}

func TestNadoBalanceSnapshotUsesCatalogAssetMetadata(t *testing.T) {
	settings := nadoCatalogSettings(t, exchange.ProductSpot, nadoRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if nadoIsSubaccountInfoRequest(request) {
			return asterOpenAPIJSONResponse(`{"status":"success","data":{"subaccount":"default","exists":true,"spot_balances":[{"product_id":1,"balance":{"amount":"1000000000000000000"}}],"perp_balances":[],"healths":[]},"request_type":"subaccount_info"}`), nil
		}
		return newNadoOpenAPIRouter(t).RoundTrip(request)
	}))
	client := NewNadoSpot(testAsterPrivateKey, "default", settings)
	account, err := client.SpotAccount(context.Background())
	if err != nil {
		t.Fatalf("SpotAccount: %v", err)
	}
	if len(account.Balances) != 1 || account.Balances[0].Asset != "ETH" {
		t.Fatalf("balances = %+v, want product asset ETH from catalog metadata", account.Balances)
	}
}

func TestNadoAccountWideUnknownProductMetadataFailures(t *testing.T) {
	for _, test := range []struct {
		name      string
		product   exchange.Product
		transport http.RoundTripper
		call      func(context.Context, exchange.Product, Settings) error
	}{
		{
			name:      "open orders",
			product:   exchange.ProductSpot,
			transport: nadoUnknownOpenOrdersTransport(t),
			call: func(ctx context.Context, _ exchange.Product, settings Settings) error {
				_, err := NewNadoSpot(testAsterPrivateKey, "default", settings).OpenOrders(ctx, exchange.OpenOrdersRequest{})
				return err
			},
		},
		{
			name:      "order history",
			product:   exchange.ProductSpot,
			transport: nadoUnknownArchiveOrdersTransport(t),
			call: func(ctx context.Context, _ exchange.Product, settings Settings) error {
				_, err := NewNadoSpot(testAsterPrivateKey, "default", settings).OrderHistory(ctx, exchange.OrderHistoryRequest{})
				return err
			},
		},
		{
			name:      "fills",
			product:   exchange.ProductSpot,
			transport: nadoUnknownMatchesTransport(t),
			call: func(ctx context.Context, _ exchange.Product, settings Settings) error {
				_, err := NewNadoSpot(testAsterPrivateKey, "default", settings).Fills(ctx, exchange.FillsRequest{})
				return err
			},
		},
		{
			name:      "positions",
			product:   exchange.ProductPerp,
			transport: nadoUnknownPositionsTransport(t),
			call: func(ctx context.Context, _ exchange.Product, settings Settings) error {
				_, err := NewNadoUSDT0Perp(testAsterPrivateKey, "default", settings).Positions(ctx, exchange.PositionsRequest{})
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			settings := nadoCatalogSettings(t, test.product, test.transport)
			if err := test.call(context.Background(), test.product, settings); !errors.Is(err, exchange.ErrMetadataResolution) {
				t.Fatalf("%s error = %v, want ErrMetadataResolution", test.name, err)
			}
		})
	}
}

func TestNadoTargetedPositionsSkipKnownNonRequestedProducts(t *testing.T) {
	descriptor := exchange.SourceDescriptor{Name: "nado-targeted-positions", Endpoint: "https://nado-fixture.invalid/v1", Transport: "nado-targeted-positions"}
	scope := exchange.CatalogScope{
		Venue:               exchange.VenueNado,
		Environment:         exchange.EnvironmentLive,
		Product:             exchange.ProductPerp,
		Settlement:          "USDT0",
		EndpointFingerprint: descriptor.Fingerprint(),
	}
	requested := nadoTestInstrument(exchange.ProductPerp)
	other := nadoTestInstrument(exchange.ProductPerp)
	other.Identity.Symbol = "BTC-PERP-USDT0"
	other.Identity.NativeSymbol = "BTC-PERP_USDT0"
	other.Identity.NativeID = "22"
	other.Identity.BaseAsset = "BTC"
	catalog := exchange.NewDefaultMemoryCatalog()
	if err := catalog.RegisterSource(scope, descriptor, exchange.StaticInstrumentLoader{Snapshot: exchange.InstrumentSnapshot{
		Scope:       scope,
		Instruments: []exchange.Instrument{requested, other},
	}}); err != nil {
		t.Fatalf("RegisterSource: %v", err)
	}
	if _, err := catalog.Refresh(context.Background(), scope); err != nil {
		t.Fatalf("Refresh catalog: %v", err)
	}
	transport := nadoRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if nadoIsSubaccountInfoRequest(request) {
			return asterOpenAPIJSONResponse(`{"status":"success","data":{"subaccount":"default","exists":true,"spot_balances":[],"perp_balances":[{"product_id":2,"balance":{"amount":"1000000000000000000","v_quote_balance":"-99000000000000000000","last_cumulative_funding_x18":"0"}},{"product_id":22,"balance":{"amount":"2000000000000000000","v_quote_balance":"-180000000000000000000","last_cumulative_funding_x18":"0"}}],"healths":[{"assets":"100000000000000000000","liabilities":"0","health":"100000000000000000000"}]},"request_type":"subaccount_info"}`), nil
		}
		if request.URL.Path == "/v1/query" && request.URL.Query().Get("type") == "market_price" {
			if request.URL.Query().Get("product_id") != "2" {
				t.Fatalf("targeted Positions fetched mark price for non-requested product %s", request.URL.Query().Get("product_id"))
			}
			return asterOpenAPIJSONResponse(`{"status":"success","data":{"product_id":2,"mark_price_x18":"99000000000000000000","bid_x18":"98000000000000000000","ask_x18":"100000000000000000000"},"request_type":"market_price"}`), nil
		}
		return newNadoOpenAPIRouter(t).RoundTrip(request)
	})
	settings := Settings{
		Endpoint:          "https://nado-fixture.invalid/v1",
		HTTPClient:        &http.Client{Transport: transport},
		CatalogScope:      scope,
		InstrumentCatalog: catalog,
	}
	client := NewNadoUSDT0Perp(testAsterPrivateKey, "default", settings)
	positions, err := client.Positions(context.Background(), exchange.PositionsRequest{Instrument: &requested})
	if err != nil {
		t.Fatalf("targeted Positions: %v", err)
	}
	if len(positions) != 1 || positions[0].NativeID != "2" || positions[0].NativeSymbol != "ETH-PERP_USDT0" {
		t.Fatalf("positions = %+v, want only requested product 2", positions)
	}
}

func TestNadoTargetedPositionsRejectUnknownProduct(t *testing.T) {
	settings := nadoCatalogSettings(t, exchange.ProductPerp, nadoUnknownPositionsTransport(t))
	client := NewNadoUSDT0Perp(testAsterPrivateKey, "default", settings)
	instrument := nadoTestInstrument(exchange.ProductPerp)
	_, err := client.Positions(context.Background(), exchange.PositionsRequest{Instrument: &instrument})
	if !errors.Is(err, exchange.ErrMetadataResolution) {
		t.Fatalf("targeted Positions unknown product error = %v, want ErrMetadataResolution", err)
	}
}

func TestNadoPositionsSkipUnknownZeroQuantityProducts(t *testing.T) {
	transport := nadoRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if nadoIsSubaccountInfoRequest(request) {
			return asterOpenAPIJSONResponse(`{"status":"success","data":{"subaccount":"default","exists":true,"spot_balances":[],"perp_balances":[{"product_id":999,"balance":{"amount":"0","v_quote_balance":"0","last_cumulative_funding_x18":"0"}}],"healths":[]},"request_type":"subaccount_info"}`), nil
		}
		return newNadoOpenAPIRouter(t).RoundTrip(request)
	})
	settings := nadoCatalogSettings(t, exchange.ProductPerp, transport)
	client := NewNadoUSDT0Perp(testAsterPrivateKey, "default", settings)
	positions, err := client.Positions(context.Background(), exchange.PositionsRequest{})
	if err != nil {
		t.Fatalf("Positions: %v", err)
	}
	if len(positions) != 0 {
		t.Fatalf("positions = %+v, want no open positions", positions)
	}
}

func TestNadoAccountWideKnownProductRowsUseRawIdentities(t *testing.T) {
	settings := nadoCatalogSettings(t, exchange.ProductSpot, newNadoOpenAPIRouter(t))
	client := NewNadoSpot(testAsterPrivateKey, "default", settings)
	orders, err := client.OpenOrders(context.Background(), exchange.OpenOrdersRequest{})
	if err != nil {
		t.Fatalf("OpenOrders: %v", err)
	}
	if len(orders.Orders) != 1 || orders.Orders[0].NativeSymbol != "ETH_USDT0" || orders.Orders[0].NativeID != "1" {
		t.Fatalf("OpenOrders = %+v, want raw catalog identity", orders.Orders)
	}
	history, err := client.OrderHistory(context.Background(), exchange.OrderHistoryRequest{})
	if err != nil {
		t.Fatalf("OrderHistory: %v", err)
	}
	if len(history.Orders) != 1 || history.Orders[0].NativeSymbol != "ETH_USDT0" || history.Orders[0].NativeID != "1" {
		t.Fatalf("OrderHistory = %+v, want raw catalog identity", history.Orders)
	}
	fills, err := client.Fills(context.Background(), exchange.FillsRequest{})
	if err != nil {
		t.Fatalf("Fills: %v", err)
	}
	if len(fills.Fills) != 1 || fills.Fills[0].NativeSymbol != "ETH_USDT0" || fills.Fills[0].NativeID != "1" {
		t.Fatalf("Fills = %+v, want raw catalog identity", fills.Fills)
	}
}

func TestNadoMutationRejectsMalformedResponseDigest(t *testing.T) {
	router := newNadoOpenAPIRouter(t)
	client := NewNadoSpot(testAsterPrivateKey, "default", Settings{
		Endpoint: "https://nado-fixture.invalid/v1",
		HTTPClient: &http.Client{Transport: nadoRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			response, err := router.RoundTrip(request)
			if err != nil || request.URL.Path != "/v1/execute" {
				return response, err
			}
			body, err := io.ReadAll(response.Body)
			if err != nil {
				return nil, err
			}
			_ = response.Body.Close()
			body = []byte(strings.ReplaceAll(string(body), "0x1111111111111111111111111111111111111111111111111111111111111111", "0xAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"))
			response.Body = io.NopCloser(strings.NewReader(string(body)))
			response.ContentLength = int64(len(body))
			return response, nil
		})},
	})
	_, err := client.PlaceOrder(context.Background(), exchange.PlaceOrderRequest{Instrument: nadoTestInstrument(exchange.ProductSpot), ClientOrderID: "101", Side: exchange.SideBuy, Type: exchange.OrderTypeLimit, Quantity: decimal.NewFromInt(1), LimitPrice: decimal.NewFromInt(99), LimitPolicy: exchange.LimitPolicyResting})
	if !errors.Is(err, exchange.ErrMalformedResponse) {
		t.Fatalf("PlaceOrder malformed digest error = %v, want ErrMalformedResponse", err)
	}
}

func nadoCatalogSettings(t *testing.T, product exchange.Product, router http.RoundTripper) Settings {
	t.Helper()
	endpoint := "https://nado-fixture.invalid/v1"
	descriptor := exchange.SourceDescriptor{Name: "nado-openapi-" + string(product), Endpoint: endpoint, WebSocketEndpoint: "wss://nado-fixture.invalid/ws", Transport: "nado-openapi-fixture"}
	scope := exchange.CatalogScope{Venue: exchange.VenueNado, Environment: exchange.EnvironmentLive, Product: product, EndpointFingerprint: descriptor.Fingerprint()}
	settings := Settings{Endpoint: endpoint, WebSocketEndpoint: descriptor.WebSocketEndpoint, HTTPClient: &http.Client{Transport: router}, SourceDescriptor: descriptor, CatalogScope: scope}
	catalog := exchange.NewDefaultMemoryCatalog()
	settings.InstrumentCatalog = catalog
	if err := catalog.RegisterSource(scope, descriptor, NewNadoInstrumentLoader(product, settings)); err != nil {
		t.Fatalf("RegisterSource Nado %s: %v", product, err)
	}
	if _, err := catalog.Refresh(context.Background(), scope); err != nil {
		t.Fatalf("Refresh Nado catalog %s: %v", product, err)
	}
	for _, peerProduct := range []exchange.Product{exchange.ProductSpot, exchange.ProductPerp} {
		if peerProduct == product {
			continue
		}
		peerScope := scope
		peerScope.Product = peerProduct
		if err := catalog.RegisterSource(peerScope, descriptor, NewNadoInstrumentLoader(peerProduct, settings)); err != nil {
			t.Fatalf("RegisterSource Nado %s: %v", peerProduct, err)
		}
		if _, err := catalog.Refresh(context.Background(), peerScope); err != nil {
			t.Fatalf("Refresh Nado catalog %s: %v", peerProduct, err)
		}
	}
	return settings
}

func nadoIsSubaccountInfoRequest(request *http.Request) bool {
	if request.URL.Path != "/v1/query" {
		return false
	}
	if request.URL.Query().Get("type") == "subaccount_info" {
		return true
	}
	if request.Body == nil {
		return false
	}
	body := nadoReadAndRestoreBody(request)
	var payload map[string]any
	_ = json.Unmarshal(body, &payload)
	value, _ := payload["type"].(string)
	return value == "subaccount_info"
}

func nadoReadAndRestoreBody(request *http.Request) []byte {
	if request.Body == nil {
		return nil
	}
	body, _ := io.ReadAll(request.Body)
	request.Body = io.NopCloser(bytes.NewReader(body))
	return body
}

func nadoUnknownOpenOrdersTransport(t *testing.T) http.RoundTripper {
	t.Helper()
	router := newNadoOpenAPIRouter(t)
	return nadoRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := nadoReadAndRestoreBody(request)
		response, err := router.RoundTrip(request)
		if err != nil || request.URL.Path != "/v1/query" {
			return response, err
		}
		if !bytes.Contains(body, []byte(`"type":"orders"`)) {
			return response, nil
		}
		return asterOpenAPIJSONResponse(`{"status":"success","data":{"sender":"sender","product_orders":[{"product_id":999,"orders":[{"product_id":999,"sender":"sender","price_x18":"99000000000000000000","amount":"1000000000000000000","expiration":"4000000000","nonce":"1","unfilled_amount":"1000000000000000000","digest":"0x1111111111111111111111111111111111111111111111111111111111111111","placed_at":1720000000000,"appendix":"0","order_type":"limit"}]}]},"request_type":"orders"}`), nil
	})
}

func nadoUnknownArchiveOrdersTransport(t *testing.T) http.RoundTripper {
	t.Helper()
	router := newNadoOpenAPIRouter(t)
	return nadoRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := nadoReadAndRestoreBody(request)
		response, err := router.RoundTrip(request)
		if err != nil || request.URL.Path != "/v1" {
			return response, err
		}
		if !bytes.Contains(body, []byte(`"orders"`)) {
			return response, nil
		}
		return asterOpenAPIJSONResponse(`{"orders":[{"digest":"0x1111111111111111111111111111111111111111111111111111111111111111","subaccount":"default","product_id":999,"submission_idx":"1","last_fill_submission_idx":"1","amount":"1000000000000000000","price_x18":"99000000000000000000","base_filled":"1000000000000000000","quote_filled":"99000000000000000000","fee":"1000000000000000","builder_fee":"0","closed_amount":"0","realized_pnl":"0","closed_net_entry":"0","closed_margin":"0","first_fill_timestamp":"1720000000","last_fill_timestamp":"1720000000","expiration":"4000000000","nonce":"1","appendix":"0"}]}`), nil
	})
}

func nadoUnknownMatchesTransport(t *testing.T) http.RoundTripper {
	t.Helper()
	router := newNadoOpenAPIRouter(t)
	return nadoRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := nadoReadAndRestoreBody(request)
		response, err := router.RoundTrip(request)
		if err != nil || request.URL.Path != "/v1" {
			return response, err
		}
		if !bytes.Contains(body, []byte(`"matches"`)) {
			return response, nil
		}
		return asterOpenAPIJSONResponse(`{"matches":[{"digest":"0x1111111111111111111111111111111111111111111111111111111111111111","order":{"sender":"sender","priceX18":"99000000000000000000","amount":"1000000000000000000","expiration":"4000000000","nonce":"1","appendix":"0"},"base_filled":"1000000000000000000","quote_filled":"99000000000000000000","fee":"1000000000000000","sequencer_fee":"0","submission_idx":"1","timestamp":"1720000000","pre_balance":{"base":{"spot":{"product_id":999,"balance":{"amount":"0"}}}},"post_balance":{"base":{"spot":{"product_id":999,"balance":{"amount":"1000000000000000000"}}}}}],"txs":[]}`), nil
	})
}

func nadoUnknownPositionsTransport(t *testing.T) http.RoundTripper {
	t.Helper()
	router := newNadoOpenAPIRouter(t)
	return nadoRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if nadoIsSubaccountInfoRequest(request) {
			return asterOpenAPIJSONResponse(`{"status":"success","data":{"subaccount":"default","exists":true,"spot_balances":[],"perp_balances":[{"product_id":999,"balance":{"amount":"1000000000000000000","v_quote_balance":"-99000000000000000000","last_cumulative_funding_x18":"0"}}],"healths":[{"assets":"100000000000000000000","liabilities":"0","health":"100000000000000000000"}]},"request_type":"subaccount_info"}`), nil
		}
		return router.RoundTrip(request)
	})
}

func TestNadoCancelAcceptsNativeLowercaseDigest(t *testing.T) {
	client := NewNadoSpot(testAsterPrivateKey, "default", Settings{Endpoint: "https://nado-fixture.invalid/v1", HTTPClient: &http.Client{Transport: newNadoOpenAPIRouter(t)}})
	ack, err := client.CancelOrder(context.Background(), exchange.CancelOrderRequest{Instrument: nadoTestInstrument(exchange.ProductSpot), OrderID: "0x1111111111111111111111111111111111111111111111111111111111111111"})
	if err != nil {
		t.Fatalf("CancelOrder native digest: %v", err)
	}
	if ack.OrderID != "0x1111111111111111111111111111111111111111111111111111111111111111" {
		t.Fatalf("CancelOrder ack digest = %q", ack.OrderID)
	}
}
