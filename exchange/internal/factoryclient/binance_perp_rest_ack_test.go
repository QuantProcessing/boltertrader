package factoryclient

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/shopspring/decimal"
)

func TestBinancePerpRESTMarketNewAcknowledgementIsAcceptedPending(t *testing.T) {
	client := NewBinanceUSDPerp("key", "secret", Settings{
		Endpoint:    "https://openapi.invalid",
		Environment: "demo",
		HTTPClient: &http.Client{Transport: openAPIRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			return openAPIJSONResponse(`{
				"symbol":"ETHUSDT",
				"orderId":42,
				"clientOrderId":"1001",
				"price":"0",
				"origQty":"0.027",
				"executedQty":"0",
				"cumQty":"0",
				"cumQuote":"0",
				"avgPrice":"0",
				"status":"NEW",
				"timeInForce":"",
				"type":"MARKET",
				"side":"BUY",
				"positionSide":"BOTH",
				"reduceOnly":false,
				"updateTime":1720000000000
			}`), nil
		})},
	})

	ack, err := client.PlaceOrder(context.Background(), exchange.PlaceOrderRequest{
		Instrument:    "ETH-USDT",
		ClientOrderID: "1001",
		Side:          exchange.SideBuy,
		Type:          exchange.OrderTypeMarket,
		Quantity:      decimal.RequireFromString("0.027"),
	})
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	if ack.State != exchange.AckAcceptedPending ||
		ack.OrderType != exchange.OrderTypeMarket ||
		ack.OrderID != "42" ||
		!ack.FilledQuantity.IsZero() {
		t.Fatalf("market NEW acknowledgement = %+v", ack)
	}
	if err := ack.Validate(); err != nil {
		t.Fatalf("market NEW acknowledgement validation: %v", err)
	}
}

func TestBinancePerpRESTRejectsAcknowledgementOrderTypeMismatch(t *testing.T) {
	client := NewBinanceUSDPerp("key", "secret", Settings{
		Endpoint:    "https://openapi.invalid",
		Environment: "demo",
		HTTPClient: &http.Client{Transport: openAPIRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			return openAPIJSONResponse(`{
				"symbol":"ETHUSDT",
				"orderId":43,
				"clientOrderId":"1002",
				"price":"0",
				"origQty":"0.027",
				"executedQty":"0",
				"avgPrice":"0",
				"status":"NEW",
				"type":"MARKET",
				"side":"BUY",
				"positionSide":"BOTH",
				"reduceOnly":false
			}`), nil
		})},
	})

	_, err := client.PlaceOrder(context.Background(), exchange.PlaceOrderRequest{
		Instrument:    "ETH-USDT",
		ClientOrderID: "1002",
		Side:          exchange.SideBuy,
		Type:          exchange.OrderTypeLimit,
		Quantity:      decimal.RequireFromString("0.027"),
		LimitPrice:    decimal.NewFromInt(1800),
		LimitPolicy:   exchange.LimitPolicyResting,
	})
	if !errors.Is(err, exchange.ErrMalformedResponse) {
		t.Fatalf("PlaceOrder order-type mismatch error = %v, want malformed response", err)
	}
}
