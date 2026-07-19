package factoryclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/sdk/lighter"
	"github.com/shopspring/decimal"
)

const openAPITestPrivateKey = "1111111111111111111111111111111111111111111111111111111111111111"
const openAPILighterPrivateKey = "01010101010101010101010101010101010101010101010101010101010101010101010101010101"

type noSendTransport struct {
	calls atomic.Int64
}

func (transport *noSendTransport) RoundTrip(*http.Request) (*http.Response, error) {
	transport.calls.Add(1)
	return &http.Response{
		StatusCode: http.StatusInternalServerError,
		Body:       io.NopCloser(strings.NewReader(`{"error":"unexpected network call"}`)),
		Header:     make(http.Header),
	}, nil
}

type openAPIRow struct {
	name    string
	product exchange.Product
	client  any
}

func openAPIRows(transport http.RoundTripper) []openAPIRow {
	settings := Settings{
		Endpoint:    "https://openapi.invalid",
		Environment: "demo",
		HTTPClient:  &http.Client{Transport: transport},
	}
	testnet := settings
	testnet.Environment = "testnet"
	return []openAPIRow{
		{name: "binance/spot", product: exchange.ProductSpot, client: NewBinanceSpot("", "", settings)},
		{name: "binance/perp", product: exchange.ProductPerp, client: NewBinanceUSDPerp("", "", settings)},
		{name: "okx/spot", product: exchange.ProductSpot, client: NewOKXSpot("", "", "", settings)},
		{name: "okx/perp", product: exchange.ProductPerp, client: NewOKXUSDTPerp("", "", "", settings)},
		{name: "lighter/spot", product: exchange.ProductSpot, client: NewLighterSpot(openAPILighterPrivateKey, 7, 2, testnet)},
		{name: "lighter/perp", product: exchange.ProductPerp, client: NewLighterPerp(openAPILighterPrivateKey, 7, 2, testnet)},
		{name: "hyperliquid/spot", product: exchange.ProductSpot, client: NewHyperliquidSpot(openAPITestPrivateKey, testnet)},
		{name: "hyperliquid/perp", product: exchange.ProductPerp, client: NewHyperliquidPerp(openAPITestPrivateKey, testnet)},
	}
}

func TestOpenAPIRestSurfaceMatrix(t *testing.T) {
	spotMethods := []string{
		"Instruments", "OrderBook", "Candles", "PublicTrades",
		"PlaceOrder", "CancelOrder", "OpenOrders", "OrderHistory", "Fills",
		"Balances", "SpotAccount",
	}
	perpMethods := []string{
		"Instruments", "OrderBook", "Candles", "PublicTrades",
		"PlaceOrder", "CancelOrder", "OpenOrders", "OrderHistory", "Fills",
		"Balances", "PerpAccount", "Positions",
		"FundingRate", "FundingRateHistory", "SetLeverage",
	}
	for _, row := range openAPIRows(&noSendTransport{}) {
		t.Run(row.name, func(t *testing.T) {
			methods := spotMethods
			if row.product == exchange.ProductPerp {
				methods = perpMethods
				if _, ok := row.client.(exchange.PerpClient); !ok {
					t.Fatalf("%T does not implement exchange.PerpClient", row.client)
				}
			} else if _, ok := row.client.(exchange.SpotClient); !ok {
				t.Fatalf("%T does not implement exchange.SpotClient", row.client)
			}
			typ := reflect.TypeOf(row.client)
			for _, method := range methods {
				if _, ok := typ.MethodByName(method); !ok {
					t.Errorf("%T is missing OpenAPI method %s", row.client, method)
				}
			}
		})
	}
}

func TestOpenAPIPlaceOrderRejectsInvalidNormalizedRequestBeforeNetwork(t *testing.T) {
	transport := &noSendTransport{}
	for _, row := range openAPIRows(transport) {
		for _, test := range []struct {
			name    string
			request exchange.PlaceOrderRequest
		}{
			{
				name: "limit policy on market order",
				request: exchange.PlaceOrderRequest{
					Instrument:    "BTC-USDT",
					ClientOrderID: "101",
					Side:          exchange.SideBuy,
					Type:          exchange.OrderTypeMarket,
					Quantity:      decimal.NewFromInt(1),
					LimitPolicy:   exchange.LimitPolicyResting,
				},
			},
			{
				name: "missing client order id",
				request: exchange.PlaceOrderRequest{
					Instrument: "BTC-USDT",
					Side:       exchange.SideBuy,
					Type:       exchange.OrderTypeMarket,
					Quantity:   decimal.NewFromInt(1),
				},
			},
		} {
			t.Run(row.name+"/"+test.name, func(t *testing.T) {
				var err error
				if row.product == exchange.ProductPerp {
					_, err = row.client.(exchange.PerpClient).PlaceOrder(context.Background(), test.request)
				} else {
					_, err = row.client.(exchange.SpotClient).PlaceOrder(context.Background(), test.request)
				}
				if !errors.Is(err, exchange.ErrInvalidRequest) {
					t.Fatalf("PlaceOrder error = %v, want ErrInvalidRequest", err)
				}
			})
		}
	}
	if got := transport.calls.Load(); got != 0 {
		t.Fatalf("invalid normalized requests made %d network calls", got)
	}
}

func TestOpenAPICancelOrderRejectsNonPortableOrderIDBeforeNetwork(t *testing.T) {
	transport := &noSendTransport{}
	for _, row := range openAPIRows(transport) {
		for _, orderID := range []string{"", "0", "01", "not-an-order-id", "9223372036854775808"} {
			t.Run(row.name+"/"+orderID, func(t *testing.T) {
				request := exchange.CancelOrderRequest{
					Instrument: "BTC-USDT",
					OrderID:    orderID,
				}
				var err error
				if row.product == exchange.ProductPerp {
					_, err = row.client.(exchange.PerpClient).CancelOrder(context.Background(), request)
				} else {
					_, err = row.client.(exchange.SpotClient).CancelOrder(context.Background(), request)
				}
				if !errors.Is(err, exchange.ErrInvalidRequest) {
					t.Fatalf("CancelOrder error = %v, want ErrInvalidRequest", err)
				}
			})
		}
	}
	if got := transport.calls.Load(); got != 0 {
		t.Fatalf("invalid cancel requests made %d network calls", got)
	}
}

func TestOpenAPIRestEveryMethodHonorsContextBoundary(t *testing.T) {
	transport := &noSendTransport{}
	for _, row := range openAPIRows(transport) {
		t.Run(row.name, func(t *testing.T) {
			if row.product == exchange.ProductSpot {
				client := row.client.(exchange.SpotClient)
				assertOpenAPIErrors(t, map[string]error{
					"Instruments":  errorFromSlice(client.Instruments(nil)),
					"OrderBook":    errorFromValue(client.OrderBook(nil, exchange.OrderBookRequest{})),
					"Candles":      errorFromValue(client.Candles(nil, exchange.CandlesRequest{})),
					"PublicTrades": errorFromValue(client.PublicTrades(nil, exchange.PublicTradesRequest{})),
					"PlaceOrder":   errorFromValue(client.PlaceOrder(nil, exchange.PlaceOrderRequest{})),
					"CancelOrder":  errorFromValue(client.CancelOrder(nil, exchange.CancelOrderRequest{})),
					"OpenOrders":   errorFromValue(client.OpenOrders(nil, exchange.OpenOrdersRequest{})),
					"OrderHistory": errorFromValue(client.OrderHistory(nil, exchange.OrderHistoryRequest{})),
					"Fills":        errorFromValue(client.Fills(nil, exchange.FillsRequest{})),
					"Balances":     errorFromSlice(client.Balances(nil)),
					"SpotAccount":  errorFromValue(client.SpotAccount(nil)),
				})
				return
			}
			client := row.client.(exchange.PerpClient)
			assertOpenAPIErrors(t, map[string]error{
				"Instruments":        errorFromSlice(client.Instruments(nil)),
				"OrderBook":          errorFromValue(client.OrderBook(nil, exchange.OrderBookRequest{})),
				"Candles":            errorFromValue(client.Candles(nil, exchange.CandlesRequest{})),
				"PublicTrades":       errorFromValue(client.PublicTrades(nil, exchange.PublicTradesRequest{})),
				"PlaceOrder":         errorFromValue(client.PlaceOrder(nil, exchange.PlaceOrderRequest{})),
				"CancelOrder":        errorFromValue(client.CancelOrder(nil, exchange.CancelOrderRequest{})),
				"OpenOrders":         errorFromValue(client.OpenOrders(nil, exchange.OpenOrdersRequest{})),
				"OrderHistory":       errorFromValue(client.OrderHistory(nil, exchange.OrderHistoryRequest{})),
				"Fills":              errorFromValue(client.Fills(nil, exchange.FillsRequest{})),
				"Balances":           errorFromSlice(client.Balances(nil)),
				"PerpAccount":        errorFromValue(client.PerpAccount(nil)),
				"Positions":          errorFromSlice(client.Positions(nil, exchange.PositionsRequest{})),
				"FundingRate":        errorFromValue(client.FundingRate(nil, exchange.FundingRateRequest{})),
				"FundingRateHistory": errorFromValue(client.FundingRateHistory(nil, exchange.FundingRateHistoryRequest{})),
				"SetLeverage":        errorFromValue(client.SetLeverage(nil, exchange.SetLeverageRequest{})),
			})
		})
	}
	if got := transport.calls.Load(); got != 0 {
		t.Fatalf("nil-context OpenAPI calls made %d network calls", got)
	}
}

func assertOpenAPIErrors(t *testing.T, results map[string]error) {
	t.Helper()
	for operation, err := range results {
		if err == nil {
			t.Errorf("%s accepted a nil context", operation)
		}
	}
}

func errorFromValue[T any](_ T, err error) error {
	return err
}

func errorFromSlice[T any](_ []T, err error) error {
	return err
}

type nativeOrderShape struct {
	orderType string
	tif       string
	hasPrice  bool
	reduce    bool
	clientID  string
}

func TestOpenAPIOrderParameterMatrix(t *testing.T) {
	requests := []struct {
		name   string
		kind   exchange.OrderType
		policy exchange.LimitPolicy
	}{
		{name: "market", kind: exchange.OrderTypeMarket},
		{name: "limit-resting", kind: exchange.OrderTypeLimit, policy: exchange.LimitPolicyResting},
		{name: "limit-ioc", kind: exchange.OrderTypeLimit, policy: exchange.LimitPolicyIOC},
		{name: "limit-post-only", kind: exchange.OrderTypeLimit, policy: exchange.LimitPolicyPostOnly},
	}
	rows := []struct {
		name      string
		product   exchange.Product
		translate func(exchange.PlaceOrderRequest) nativeOrderShape
	}{
		{
			name: "binance/spot", product: exchange.ProductSpot,
			translate: func(req exchange.PlaceOrderRequest) nativeOrderShape {
				row := binanceSpotPlaceParams("BTCUSDT", "BUY", req)
				return nativeOrderShape{orderType: row.Type, tif: row.TimeInForce, hasPrice: row.Price != "", clientID: row.NewClientOrderID}
			},
		},
		{
			name: "binance/perp", product: exchange.ProductPerp,
			translate: func(req exchange.PlaceOrderRequest) nativeOrderShape {
				row := binancePerpPlaceParams("BTCUSDT", req)
				return nativeOrderShape{orderType: row.Type, tif: row.TimeInForce, hasPrice: row.Price != "", reduce: row.ReduceOnly, clientID: row.NewClientOrderID}
			},
		},
		{
			name: "okx/spot", product: exchange.ProductSpot,
			translate: func(req exchange.PlaceOrderRequest) nativeOrderShape {
				orderType, price := okxOrderRequestShape(req)
				return nativeOrderShape{orderType: orderType, hasPrice: price != nil, clientID: req.ClientOrderID}
			},
		},
		{
			name: "okx/perp", product: exchange.ProductPerp,
			translate: func(req exchange.PlaceOrderRequest) nativeOrderShape {
				orderType, price := okxOrderRequestShape(req)
				return nativeOrderShape{orderType: orderType, hasPrice: price != nil, reduce: req.ReduceOnly, clientID: req.ClientOrderID}
			},
		},
		{
			name: "lighter/spot", product: exchange.ProductSpot,
			translate: func(req exchange.PlaceOrderRequest) nativeOrderShape {
				row := lighterPlaceRequest(lighterMarketMeta{marketID: 1}, req, 100, 200, 101, 0)
				return lighterNativeOrderShape(row)
			},
		},
		{
			name: "lighter/perp", product: exchange.ProductPerp,
			translate: func(req exchange.PlaceOrderRequest) nativeOrderShape {
				row := lighterPlaceRequest(lighterMarketMeta{marketID: 1}, req, 100, 200, 101, 0)
				return lighterNativeOrderShape(row)
			},
		},
		{
			name: "hyperliquid/spot", product: exchange.ProductSpot,
			translate: func(req exchange.PlaceOrderRequest) nativeOrderShape {
				return hyperliquidNativeOrderShape(req)
			},
		},
		{
			name: "hyperliquid/perp", product: exchange.ProductPerp,
			translate: func(req exchange.PlaceOrderRequest) nativeOrderShape {
				shape := hyperliquidNativeOrderShape(req)
				shape.reduce = req.ReduceOnly
				return shape
			},
		},
	}

	for _, row := range rows {
		for _, request := range requests {
			t.Run(row.name+"/"+request.name, func(t *testing.T) {
				req := exchange.PlaceOrderRequest{
					Instrument:    "BTC-USDT",
					ClientOrderID: "101",
					Side:          exchange.SideBuy,
					Type:          request.kind,
					Quantity:      decimal.NewFromInt(2),
					LimitPolicy:   request.policy,
				}
				if request.kind == exchange.OrderTypeLimit {
					req.LimitPrice = decimal.NewFromInt(100)
				}
				if row.product == exchange.ProductPerp {
					req.ReduceOnly = true
				}
				if err := req.Validate(row.product); err != nil {
					t.Fatalf("valid matrix request: %v", err)
				}
				shape := row.translate(req)
				assertNativeOrderShape(t, row.name, req, shape)
			})
		}
	}
}

func lighterNativeOrderShape(row lighter.CreateOrderRequest) nativeOrderShape {
	orderType := "limit"
	if row.OrderType == lighter.OrderTypeMarket {
		orderType = "market"
	}
	tif := "resting"
	switch row.TimeInForce {
	case lighter.OrderTimeInForceImmediateOrCancel:
		tif = "ioc"
	case lighter.OrderTimeInForcePostOnly:
		tif = "post_only"
	}
	return nativeOrderShape{
		orderType: orderType,
		tif:       tif,
		hasPrice:  row.Price != 0,
		reduce:    row.ReduceOnly == 1,
		clientID:  strconvFormatInt(row.ClientOrderId),
	}
}

func hyperliquidNativeOrderShape(req exchange.PlaceOrderRequest) nativeOrderShape {
	orderType := "limit"
	tif := strings.ToLower(string(hlLimitTIF(req.LimitPolicy)))
	if req.Type == exchange.OrderTypeMarket {
		orderType = "market"
		tif = "ioc"
	}
	return nativeOrderShape{
		orderType: orderType,
		tif:       tif,
		hasPrice:  req.Type == exchange.OrderTypeLimit,
		clientID:  hlNativeClientOrderID(req.ClientOrderID),
	}
}

func assertNativeOrderShape(t *testing.T, venue string, req exchange.PlaceOrderRequest, shape nativeOrderShape) {
	t.Helper()
	if req.Type == exchange.OrderTypeMarket {
		if shape.orderType != "MARKET" && shape.orderType != "market" {
			t.Errorf("%s market mapped to %q", venue, shape.orderType)
		}
		lighterMarket := venue == "lighter/spot" || venue == "lighter/perp"
		if shape.hasPrice && !lighterMarket {
			t.Errorf("%s market retained a public limit price", venue)
		}
		if !shape.hasPrice && lighterMarket {
			t.Errorf("%s market omitted the venue-required protected price", venue)
		}
		if venue != "okx/spot" && venue != "okx/perp" && !strings.EqualFold(shape.tif, "ioc") && shape.tif != "" {
			t.Errorf("%s market tif = %q, want IOC or omitted native market TIF", venue, shape.tif)
		}
	} else {
		if shape.orderType == "MARKET" || shape.orderType == "market" {
			t.Errorf("%s limit mapped to market", venue)
		}
		if !shape.hasPrice {
			t.Errorf("%s limit omitted price", venue)
		}
		switch req.LimitPolicy {
		case exchange.LimitPolicyIOC:
			if shape.orderType != "ioc" && !strings.EqualFold(shape.tif, "ioc") {
				t.Errorf("%s IOC mapping = type %q tif %q", venue, shape.orderType, shape.tif)
			}
		case exchange.LimitPolicyPostOnly:
			if shape.orderType != "post_only" && shape.orderType != "LIMIT_MAKER" &&
				!strings.EqualFold(shape.tif, "post_only") && !strings.EqualFold(shape.tif, "gtx") &&
				!strings.EqualFold(shape.tif, "alo") {
				t.Errorf("%s post-only mapping = type %q tif %q", venue, shape.orderType, shape.tif)
			}
		}
	}
	if shape.reduce != (req.ReduceOnly && strings.HasSuffix(venue, "/perp")) {
		t.Errorf("%s reduce-only mapping = %v", venue, shape.reduce)
	}
	if strings.HasPrefix(venue, "hyperliquid/") {
		if shape.clientID != "0x00000000000000000000000000000065" {
			t.Errorf("%s native client id = %q", venue, shape.clientID)
		}
	} else if shape.clientID != req.ClientOrderID {
		t.Errorf("%s client id = %q, want %q", venue, shape.clientID, req.ClientOrderID)
	}
}

func strconvFormatInt(value int64) string {
	if value == 0 {
		return ""
	}
	return decimal.NewFromInt(value).String()
}
