package exchange

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestPlaceOrderRequestFreezesCommonMarketAndLimitFields(t *testing.T) {
	typ := reflect.TypeOf(PlaceOrderRequest{})
	got := make([]string, 0, typ.NumField())
	for index := 0; index < typ.NumField(); index++ {
		got = append(got, typ.Field(index).Name)
	}
	want := []string{
		"Instrument",
		"ClientOrderID",
		"Side",
		"Type",
		"Quantity",
		"LimitPrice",
		"LimitPolicy",
		"ReduceOnly",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PlaceOrderRequest fields = %v, want %v", got, want)
	}

	for _, forbidden := range []string{
		"TimeInForce", "PositionSide", "MarketID",
		"ContractCount", "Vault", "TriggerPrice", "CallbackRate",
	} {
		if _, ok := typ.FieldByName(forbidden); ok {
			t.Fatalf("PlaceOrderRequest unexpectedly exports %s", forbidden)
		}
	}

	request := PlaceOrderRequest{
		Instrument:  "BTC-USDT",
		Side:        SideBuy,
		Type:        OrderTypeLimit,
		Quantity:    decimal.RequireFromString("0.00000001"),
		LimitPrice:  decimal.RequireFromString("123456789.12345678"),
		LimitPolicy: LimitPolicyResting,
	}
	if request.Quantity.String() != "0.00000001" {
		t.Fatalf("quantity lost decimal precision: %s", request.Quantity)
	}
	if request.LimitPrice.String() != "123456789.12345678" {
		t.Fatalf("price lost decimal precision: %s", request.LimitPrice)
	}
}

func TestCancelOrderRequestFreezesPortableOrderIDFields(t *testing.T) {
	typ := reflect.TypeOf(CancelOrderRequest{})
	got := make([]string, 0, typ.NumField())
	for index := 0; index < typ.NumField(); index++ {
		got = append(got, typ.Field(index).Name)
	}
	want := []string{"Instrument", "OrderID"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CancelOrderRequest fields = %v, want portable fields %v", got, want)
	}
}

func TestPlaceOrderRequestValidation(t *testing.T) {
	valid := []struct {
		name    string
		product Product
		request PlaceOrderRequest
	}{
		{
			name:    "spot market",
			product: ProductSpot,
			request: PlaceOrderRequest{
				Instrument:    "BTC-USDT",
				ClientOrderID: "123",
				Side:          SideBuy,
				Type:          OrderTypeMarket,
				Quantity:      decimal.RequireFromString("0.001"),
			},
		},
		{
			name:    "perp reduce-only market",
			product: ProductPerp,
			request: PlaceOrderRequest{
				Instrument:    "BTC-USDT",
				ClientOrderID: "281474976710655",
				Side:          SideSell,
				Type:          OrderTypeMarket,
				Quantity:      decimal.RequireFromString("0.001"),
				ReduceOnly:    true,
			},
		},
		{
			name:    "spot resting limit",
			product: ProductSpot,
			request: PlaceOrderRequest{
				Instrument:    "BTC-USDT",
				ClientOrderID: "124",
				Side:          SideBuy,
				Type:          OrderTypeLimit,
				Quantity:      decimal.RequireFromString("0.001"),
				LimitPrice:    decimal.RequireFromString("65000"),
				LimitPolicy:   LimitPolicyResting,
			},
		},
		{
			name:    "spot ioc limit",
			product: ProductSpot,
			request: PlaceOrderRequest{
				Instrument:    "BTC-USDT",
				ClientOrderID: "125",
				Side:          SideBuy,
				Type:          OrderTypeLimit,
				Quantity:      decimal.RequireFromString("0.001"),
				LimitPrice:    decimal.RequireFromString("65000"),
				LimitPolicy:   LimitPolicyIOC,
			},
		},
		{
			name:    "perp post-only limit",
			product: ProductPerp,
			request: PlaceOrderRequest{
				Instrument:    "BTC-USDT",
				ClientOrderID: "126",
				Side:          SideSell,
				Type:          OrderTypeLimit,
				Quantity:      decimal.RequireFromString("0.001"),
				LimitPrice:    decimal.RequireFromString("65000"),
				LimitPolicy:   LimitPolicyPostOnly,
				ReduceOnly:    true,
			},
		},
	}
	for _, test := range valid {
		t.Run(test.name, func(t *testing.T) {
			if err := test.request.Validate(test.product); err != nil {
				t.Fatalf("Validate: %v", err)
			}
		})
	}

	baseMarket := PlaceOrderRequest{
		Instrument:    "BTC-USDT",
		ClientOrderID: "123",
		Side:          SideBuy,
		Type:          OrderTypeMarket,
		Quantity:      decimal.RequireFromString("0.001"),
	}
	baseLimit := PlaceOrderRequest{
		Instrument:    "BTC-USDT",
		ClientOrderID: "123",
		Side:          SideBuy,
		Type:          OrderTypeLimit,
		Quantity:      decimal.RequireFromString("0.001"),
		LimitPrice:    decimal.RequireFromString("65000"),
		LimitPolicy:   LimitPolicyResting,
	}
	invalid := []struct {
		name    string
		product Product
		request PlaceOrderRequest
	}{
		{name: "unknown product", product: Product("option"), request: baseMarket},
		{name: "missing instrument", product: ProductSpot, request: withPlaceOrder(baseMarket, func(request *PlaceOrderRequest) { request.Instrument = "" })},
		{name: "unknown side", product: ProductSpot, request: withPlaceOrder(baseMarket, func(request *PlaceOrderRequest) { request.Side = Side("hold") })},
		{name: "unknown type", product: ProductSpot, request: withPlaceOrder(baseMarket, func(request *PlaceOrderRequest) { request.Type = OrderType("stop") })},
		{name: "zero quantity", product: ProductSpot, request: withPlaceOrder(baseMarket, func(request *PlaceOrderRequest) { request.Quantity = decimal.Zero })},
		{name: "market price", product: ProductSpot, request: withPlaceOrder(baseMarket, func(request *PlaceOrderRequest) { request.LimitPrice = decimal.NewFromInt(1) })},
		{name: "market policy", product: ProductSpot, request: withPlaceOrder(baseMarket, func(request *PlaceOrderRequest) { request.LimitPolicy = LimitPolicyIOC })},
		{name: "spot reduce-only", product: ProductSpot, request: withPlaceOrder(baseMarket, func(request *PlaceOrderRequest) { request.ReduceOnly = true })},
		{name: "limit missing price", product: ProductSpot, request: withPlaceOrder(baseLimit, func(request *PlaceOrderRequest) { request.LimitPrice = decimal.Zero })},
		{name: "limit missing policy", product: ProductSpot, request: withPlaceOrder(baseLimit, func(request *PlaceOrderRequest) { request.LimitPolicy = "" })},
		{name: "limit unknown policy", product: ProductSpot, request: withPlaceOrder(baseLimit, func(request *PlaceOrderRequest) { request.LimitPolicy = LimitPolicy("fok") })},
		{name: "missing client id", product: ProductSpot, request: withPlaceOrder(baseMarket, func(request *PlaceOrderRequest) { request.ClientOrderID = "" })},
		{name: "client id zero", product: ProductSpot, request: withPlaceOrder(baseMarket, func(request *PlaceOrderRequest) { request.ClientOrderID = "0" })},
		{name: "client id leading zero", product: ProductSpot, request: withPlaceOrder(baseMarket, func(request *PlaceOrderRequest) { request.ClientOrderID = "0123" })},
		{name: "client id nonnumeric", product: ProductSpot, request: withPlaceOrder(baseMarket, func(request *PlaceOrderRequest) { request.ClientOrderID = "client-123" })},
		{name: "client id above common uint48 maximum", product: ProductSpot, request: withPlaceOrder(baseMarket, func(request *PlaceOrderRequest) { request.ClientOrderID = "281474976710656" })},
		{name: "client id overflow", product: ProductSpot, request: withPlaceOrder(baseMarket, func(request *PlaceOrderRequest) { request.ClientOrderID = "9223372036854775808" })},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			err := test.request.Validate(test.product)
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("Validate error = %v, want ErrInvalidRequest", err)
			}
		})
	}
}

func withPlaceOrder(request PlaceOrderRequest, mutate func(*PlaceOrderRequest)) PlaceOrderRequest {
	mutate(&request)
	return request
}

func TestNormalizedModelsDoNotExposeNativeRepresentations(t *testing.T) {
	value := struct {
		Instrument Instrument
		Book       OrderBook
		Candles    CandlePage
		Order      Order
		Fills      FillPage
		Spot       SpotAccount
		Perp       PerpAccount
		Positions  []Position
	}{
		Instrument: Instrument{Symbol: "BTC-USDT"},
		Book:       OrderBook{Instrument: "BTC-USDT"},
		Candles:    CandlePage{Page: PageInfo{Limit: 100}},
		Order:      Order{Instrument: "BTC-USDT"},
		Fills:      FillPage{Page: PageInfo{Limit: 100}},
		Spot:       SpotAccount{},
		Perp:       PerpAccount{},
		Positions:  []Position{{Instrument: "BTC-USDT"}},
	}

	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(string(data))
	for _, forbidden := range []string{
		"market_id", "marketid", "contract_count", "contractcount",
		"sdk", "native", "raw_response", "rawresponse",
	} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("normalized JSON leaks %q: %s", forbidden, data)
		}
	}
}

func TestModelsCarryMatrixRequiredNormalizedFields(t *testing.T) {
	instrument := Instrument{
		Symbol:            "BTC-USDT",
		PriceIncrement:    decimal.RequireFromString("0.10"),
		QuantityIncrement: decimal.RequireFromString("0.0001"),
		MinQuantity:       decimal.RequireFromString("0.001"),
		MinNotional: OptionalDecimal{
			Value: decimal.RequireFromString("5"),
			Valid: true,
		},
	}
	if !instrument.MinNotional.Valid {
		t.Fatal("instrument must preserve optional min-notional truth")
	}

	candle := Candle{Complete: true}
	if !candle.Complete {
		t.Fatal("candle completeness flag was lost")
	}

	balance := Balance{
		Asset:     "USDT",
		Available: decimal.RequireFromString("8"),
		Locked:    decimal.RequireFromString("2"),
		Total:     decimal.RequireFromString("10"),
	}
	if !balance.Available.Add(balance.Locked).Equal(balance.Total) {
		t.Fatal("spot balance cannot represent free/locked/total")
	}

	perp := PerpAccount{
		Equity:        OptionalDecimal{Value: decimal.RequireFromString("100"), Valid: true},
		Available:     OptionalDecimal{Value: decimal.RequireFromString("80"), Valid: true},
		MarginUsed:    OptionalDecimal{Value: decimal.RequireFromString("20"), Valid: true},
		UnrealizedPnL: OptionalDecimal{Value: decimal.RequireFromString("-1.25"), Valid: true},
	}
	if !perp.UnrealizedPnL.Valid {
		t.Fatal("perp account must preserve optional PnL truth")
	}

	position := Position{
		Instrument:       "BTC-USDT",
		Quantity:         decimal.RequireFromString("-0.5"),
		EntryPrice:       decimal.RequireFromString("100"),
		MarkPrice:        decimal.RequireFromString("90"),
		LiquidationPrice: OptionalDecimal{Value: decimal.RequireFromString("150"), Valid: true},
		Leverage:         OptionalDecimal{Value: decimal.RequireFromString("10"), Valid: true},
		MarginUsed:       OptionalDecimal{Value: decimal.RequireFromString("5"), Valid: true},
	}
	if !position.Quantity.IsNegative() {
		t.Fatal("position quantity must preserve its sign")
	}

	order := Order{
		AverageFillPrice: OptionalDecimal{
			Value: decimal.RequireFromString("99.5"),
			Valid: true,
		},
	}
	if !order.AverageFillPrice.Valid {
		t.Fatal("order must preserve optional average fill price")
	}

	fill := Fill{ClientOrderID: "client-1", Liquidity: LiquidityMaker}
	if fill.Liquidity != LiquidityMaker {
		t.Fatal("fill must preserve maker/taker liquidity")
	}

	orderType := Order{
		Type:        OrderTypeMarket,
		LimitPolicy: "",
		ReduceOnly:  true,
	}
	if orderType.Type != OrderTypeMarket || !orderType.ReduceOnly {
		t.Fatal("order must preserve normalized type and reduce-only intent")
	}

	publicTrade := PublicTrade{
		Instrument: "BTC-USDT",
		TradeID:    "77",
		Side:       SideBuy,
		Price:      decimal.RequireFromString("65000"),
		Quantity:   decimal.RequireFromString("0.01"),
		Time:       time.UnixMilli(1700000000000).UTC(),
	}
	if publicTrade.TradeID == "" || !publicTrade.Price.IsPositive() || !publicTrade.Quantity.IsPositive() {
		t.Fatal("public trade must preserve normalized execution identity")
	}

	funding := FundingRate{
		Instrument: "BTC-USDT",
		Rate:       decimal.RequireFromString("0.0001"),
		MarkPrice:  OptionalDecimal{Value: decimal.RequireFromString("65000"), Valid: true},
		ObservedAt: time.UnixMilli(1700000000000).UTC(),
	}
	if funding.Instrument == "" || !funding.MarkPrice.Valid || funding.ObservedAt.IsZero() {
		t.Fatal("funding rate must preserve instrument, applicable time, and optional mark")
	}

	leverage := Leverage{Instrument: "BTC-USDT", Effective: 5}
	if leverage.Effective != 5 {
		t.Fatal("leverage must report the effective value")
	}
}

func TestRESTPageRequestShapesRemainBoundedAndTyped(t *testing.T) {
	assertFields := func(name string, value any, want []string) {
		t.Helper()
		typ := reflect.TypeOf(value)
		got := make([]string, 0, typ.NumField())
		for index := 0; index < typ.NumField(); index++ {
			got = append(got, typ.Field(index).Name)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s fields = %v, want %v", name, got, want)
		}
	}
	assertFields("PublicTradesRequest", PublicTradesRequest{}, []string{"Instrument", "Limit"})
	assertFields("OrderHistoryRequest", OrderHistoryRequest{}, []string{"Instrument", "Start", "End", "Cursor", "Limit"})
	assertFields("FundingRateRequest", FundingRateRequest{}, []string{"Instrument"})
	assertFields("FundingRateHistoryRequest", FundingRateHistoryRequest{}, []string{"Instrument", "Start", "End", "Cursor", "Limit"})
	assertFields("SetLeverageRequest", SetLeverageRequest{}, []string{"Instrument", "Leverage"})
}

func TestPaginationNeverClaimsCompleteHistory(t *testing.T) {
	typ := reflect.TypeOf(PageInfo{})
	if _, ok := typ.FieldByName("Complete"); ok {
		t.Fatal("PageInfo must not claim complete history")
	}
	for _, required := range []string{
		"Cursor", "Limit", "WindowStart", "WindowEnd", "HasMoreKnown", "HasMore",
	} {
		if _, ok := typ.FieldByName(required); !ok {
			t.Fatalf("PageInfo missing %s", required)
		}
	}

	page := PageInfo{
		Cursor:       "opaque-cursor",
		Limit:        100,
		WindowStart:  time.Unix(100, 0).UTC(),
		WindowEnd:    time.Unix(200, 0).UTC(),
		HasMoreKnown: false,
	}
	if page.HasMore {
		t.Fatal("unknown pagination must not invent HasMore")
	}
}
