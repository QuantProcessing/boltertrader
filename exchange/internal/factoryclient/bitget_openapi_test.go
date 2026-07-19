package factoryclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/shopspring/decimal"
)

func TestOpenAPIBitgetRESTExecutionMatrix(t *testing.T) {
	ctx := context.Background()
	settings := Settings{Endpoint: "https://openapi.invalid", Environment: "demo", HTTPClient: &http.Client{Transport: bitgetOpenAPIRouter{}}}

	spot := NewBitgetSpot("key", "secret", "passphrase", settings)
	assertSpotReadMatrix(t, ctx, spot, "BTC-USDT")
	exerciseOpenAPIOrderBranches(t, ctx, exchange.ProductSpot, spot, "BTC-USDT")
	if _, err := spot.CancelOrder(ctx, exchange.CancelOrderRequest{Instrument: "BTC-USDT", OrderID: "11"}); err != nil {
		t.Fatalf("spot CancelOrder: %v", err)
	}
	assertSpotLifecycleMatrix(t, ctx, spot, "BTC-USDT")

	perp := NewBitgetPerp("key", "secret", "passphrase", "USDT-FUTURES", settings)
	assertPerpReadMatrix(t, ctx, perp, "BTC-USDT")
	exerciseOpenAPIOrderBranches(t, ctx, exchange.ProductPerp, perp, "BTC-USDT")
	if _, err := perp.CancelOrder(ctx, exchange.CancelOrderRequest{Instrument: "BTC-USDT", OrderID: "21"}); err != nil {
		t.Fatalf("perp CancelOrder: %v", err)
	}
	assertPerpLifecycleMatrixWithFundingRequest(t, ctx, perp, "BTC-USDT", exchange.FundingRateHistoryRequest{Instrument: "BTC-USDT", Limit: 10})
}

func TestBitgetBuildersSatisfyExchangeContracts(t *testing.T) {
	settings := Settings{
		Endpoint:          "https://openapi.invalid",
		WebSocketEndpoint: "wss://stream.invalid",
		Environment:       "demo",
		HTTPClient: &http.Client{Transport: openAPIRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			t.Fatalf("constructor performed network I/O: %s %s", request.Method, request.URL.String())
			return nil, nil
		})},
	}

	var _ exchange.SpotClient = NewBitgetSpot("key", "secret", "passphrase", settings)
	var _ exchange.PerpClient = NewBitgetPerp("key", "secret", "passphrase", "USDT-FUTURES", settings)
	var _ exchange.PerpClient = NewBitgetPerp("key", "secret", "passphrase", "USDC-FUTURES", settings)
}

func TestBitgetUSDCPerpUsesUSDCFuturesProductTypeAndMarginCoin(t *testing.T) {
	ctx := context.Background()
	var placeBody string
	var instrumentsCategory string
	client := NewBitgetPerp("key", "secret", "passphrase", "USDC-FUTURES", Settings{
		Endpoint:    "https://openapi.invalid",
		Environment: "demo",
		HTTPClient: &http.Client{Transport: openAPIRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			switch request.URL.Path {
			case "/api/v3/market/instruments":
				instrumentsCategory = request.URL.Query().Get("category")
				return bitgetData(`[{"symbol":"BTCUSDC","category":"USDC-FUTURES","baseCoin":"BTC","quoteCoin":"USDC","minOrderQty":"0.001","minOrderAmount":"5","pricePrecision":"1","quantityPrecision":"3","priceMultiplier":"0.1","quantityMultiplier":"0.001","status":"online"}]`), nil
			case "/api/v3/account/settings":
				return bitgetData(`{"accountMode":"unified","holdMode":"hedge_mode"}`), nil
			case "/api/v3/trade/place-order":
				body, _ := io.ReadAll(request.Body)
				placeBody = string(body)
				return bitgetData(`{"orderId":"11","clientOid":"401"}`), nil
			default:
				return bitgetData(`{}`), nil
			}
		})},
	})
	if _, err := client.Instruments(ctx); err != nil {
		t.Fatalf("Instruments: %v", err)
	}
	if instrumentsCategory != "USDC-FUTURES" {
		t.Fatalf("instrument category = %q, want USDC-FUTURES", instrumentsCategory)
	}
	_, err := client.PlaceOrder(ctx, exchange.PlaceOrderRequest{
		Instrument:    "BTC-USDC",
		ClientOrderID: "401",
		Side:          exchange.SideBuy,
		Type:          exchange.OrderTypeLimit,
		Quantity:      decimal.NewFromInt(1),
		LimitPrice:    decimal.NewFromInt(99),
		LimitPolicy:   exchange.LimitPolicyResting,
		ReduceOnly:    true,
	})
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	for _, want := range []string{`"category":"USDC-FUTURES"`, `"marginCoin":"USDC"`, `"posSide":"short"`} {
		if !strings.Contains(placeBody, want) {
			t.Fatalf("place body %s missing %s", placeBody, want)
		}
	}
	if strings.Contains(placeBody, `"reduceOnly"`) {
		t.Fatalf("hedge-mode place body assigns reduceOnly with posSide: %s", placeBody)
	}
}

func TestBitgetSpotMarketBuyConvertsBaseQuantityToQuoteQuantity(t *testing.T) {
	var placeBody string
	client := NewBitgetSpot("key", "secret", "passphrase", Settings{
		Endpoint:    "https://openapi.invalid",
		Environment: "demo",
		HTTPClient: &http.Client{Transport: openAPIRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			switch request.URL.Path {
			case "/api/v3/market/orderbook":
				return bitgetData(`{"b":[["99","1"]],"a":[["101","2"]],"ts":"1720000000000"}`), nil
			case "/api/v3/trade/place-order":
				body, _ := io.ReadAll(request.Body)
				placeBody = string(body)
				return bitgetData(`{"orderId":"11","clientOid":"401"}`), nil
			default:
				return bitgetData(`{}`), nil
			}
		})},
	})
	_, err := client.PlaceOrder(t.Context(), exchange.PlaceOrderRequest{
		Instrument:    "BTC-USDT",
		ClientOrderID: "401",
		Side:          exchange.SideBuy,
		Type:          exchange.OrderTypeMarket,
		Quantity:      decimal.NewFromInt(1),
	})
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	if !strings.Contains(placeBody, `"qty":"101"`) {
		t.Fatalf("place body %s missing quote quantity", placeBody)
	}
}

func TestBitgetCancelAcceptsVenueSizedOrderID(t *testing.T) {
	const orderID = "1462622650495291392"
	var cancelBody string
	client := NewBitgetSpot("key", "secret", "passphrase", Settings{
		Endpoint: "https://openapi.invalid",
		HTTPClient: &http.Client{Transport: openAPIRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			body, _ := io.ReadAll(request.Body)
			cancelBody = string(body)
			return bitgetData(`{"orderId":"` + orderID + `"}`), nil
		})},
	})
	if _, err := client.CancelOrder(t.Context(), exchange.CancelOrderRequest{Instrument: "BTC-USDT", OrderID: orderID}); err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}
	if !strings.Contains(cancelBody, `"orderId":"`+orderID+`"`) {
		t.Fatalf("cancel body %s missing venue order id", cancelBody)
	}
}

func TestBitgetMalformedNumericFieldsReturnMalformedResponse(t *testing.T) {
	client := NewBitgetSpot("key", "secret", "passphrase", Settings{
		Endpoint:    "https://openapi.invalid",
		Environment: "demo",
		HTTPClient: &http.Client{Transport: openAPIRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			return bitgetData(`[{"symbol":"BTCUSDT","category":"spot","baseCoin":"BTC","quoteCoin":"USDT","minOrderQty":"not-a-decimal","pricePrecision":"1","quantityPrecision":"3","status":"online"}]`), nil
		})},
	})
	_, err := client.Instruments(context.Background())
	assertExchangeErrorKind(t, err, exchange.KindMalformedResponse)
}

func assertExchangeErrorKind(t *testing.T, err error, want exchange.ErrorKind) {
	t.Helper()
	var exchangeErr *exchange.Error
	if !errors.As(err, &exchangeErr) {
		t.Fatalf("error = %v, want exchange error kind %s", err, want)
	}
	if exchangeErr.Kind() != want {
		t.Fatalf("error kind = %s, want %s: %v", exchangeErr.Kind(), want, err)
	}
}
