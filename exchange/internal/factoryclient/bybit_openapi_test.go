package factoryclient

import (
	"context"
	"net/http"
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
)

func TestOpenAPIBybitRESTExecutionMatrix(t *testing.T) {
	ctx := context.Background()
	settings := Settings{Endpoint: "https://openapi.invalid", Environment: "testnet", HTTPClient: &http.Client{Transport: bybitOpenAPIRouter{}}}

	spot := NewBybitSpot("key", "secret", settings)
	assertSpotReadMatrix(t, ctx, spot, "BTC-USDT")
	exerciseOpenAPIOrderBranches(t, ctx, exchange.ProductSpot, spot, "BTC-USDT")
	if _, err := spot.CancelOrder(ctx, exchange.CancelOrderRequest{Instrument: "BTC-USDT", OrderID: "11"}); err != nil {
		t.Fatalf("spot CancelOrder: %v", err)
	}
	assertSpotLifecycleMatrix(t, ctx, spot, "BTC-USDT")

	perp := NewBybitLinearPerp("key", "secret", "USDT", settings)
	assertPerpReadMatrix(t, ctx, perp, "BTC-USDT")
	exerciseOpenAPIOrderBranches(t, ctx, exchange.ProductPerp, perp, "BTC-USDT")
	if _, err := perp.CancelOrder(ctx, exchange.CancelOrderRequest{Instrument: "BTC-USDT", OrderID: "21"}); err != nil {
		t.Fatalf("perp CancelOrder: %v", err)
	}
	assertPerpLifecycleMatrix(t, ctx, perp, "BTC-USDT")
}

func TestBybitBuildersSatisfyExchangeContracts(t *testing.T) {
	settings := Settings{
		Endpoint:          "https://openapi.invalid",
		WebSocketEndpoint: "wss://stream.invalid",
		Environment:       "demo",
		HTTPClient: &http.Client{Transport: openAPIRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			t.Fatalf("constructor performed network I/O: %s %s", request.Method, request.URL.String())
			return nil, nil
		})},
	}

	var _ exchange.SpotClient = NewBybitSpot("key", "secret", settings)
	var _ exchange.PerpClient = NewBybitLinearPerp("key", "secret", "USDT", settings)
	var _ exchange.PerpClient = NewBybitLinearPerp("key", "secret", "USDC", settings)
}

func TestBybitMalformedNumericFieldsReturnMalformedResponse(t *testing.T) {
	client := NewBybitSpot("key", "secret", Settings{
		Endpoint:    "https://openapi.invalid",
		Environment: "demo",
		HTTPClient: &http.Client{Transport: openAPIRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			return bybitData(`{"category":"spot","list":[{"symbol":"BTCUSDT","baseCoin":"BTC","quoteCoin":"USDT","status":"Trading","priceFilter":{"tickSize":"bad"},"lotSizeFilter":{"basePrecision":"0.001","qtyStep":"0.001","minOrderQty":"0.001"}}]}`), nil
		})},
	})
	_, err := client.Instruments(context.Background())
	assertExchangeErrorKind(t, err, exchange.KindMalformedResponse)
}

func TestBybitCancelAcceptsOfficialUUIDOrderID(t *testing.T) {
	const orderID = "cf55eb56-0853-4d3f-945e-17ddd6059a89"
	client := NewBybitLinearPerp("key", "secret", "USDT", Settings{
		Endpoint:    "https://openapi.invalid",
		Environment: "demo",
		HTTPClient:  &http.Client{Transport: bybitOpenAPIRouter{}},
	})

	ack, err := client.CancelOrder(context.Background(), exchange.CancelOrderRequest{
		Instrument: "BTC-USDT",
		OrderID:    orderID,
	})
	if err != nil {
		t.Fatalf("CancelOrder UUID: %v", err)
	}
	if ack.OrderID != orderID || ack.State != exchange.AckCanceled {
		t.Fatalf("CancelOrder UUID ack=%+v", ack)
	}
}
