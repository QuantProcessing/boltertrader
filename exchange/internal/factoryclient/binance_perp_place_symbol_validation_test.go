package factoryclient

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
	binanceperp "github.com/QuantProcessing/boltertrader/sdk/binance/perp"
	"github.com/shopspring/decimal"
)

func TestBinancePerpRESTPlaceOrderRejectsMalformedInstrumentBeforeSend(t *testing.T) {
	sends := 0
	client := NewBinanceUSDPerp("key", "secret", Settings{
		Endpoint:    "https://openapi.invalid",
		Environment: "demo",
		HTTPClient: &http.Client{Transport: openAPIRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			sends++
			return openAPIJSONResponse(`{"symbol":"","orderId":1,"clientOrderId":"1001","status":"NEW","type":"MARKET","side":"BUY","origQty":"1","executedQty":"0","avgPrice":"0","positionSide":"BOTH","reduceOnly":false}`), nil
		})},
	})

	_, err := client.PlaceOrder(context.Background(), binancePerpMalformedSymbolPlaceRequest())
	if !errors.Is(err, exchange.ErrInvalidRequest) {
		t.Fatalf("PlaceOrder error = %v, want ErrInvalidRequest", err)
	}
	if sends != 0 {
		t.Fatalf("PlaceOrder sent %d HTTP request(s), want no send", sends)
	}
}

func TestBinancePerpWSPlaceOrderRejectsMalformedInstrumentBeforeSend(t *testing.T) {
	api := &binancePerpNoSendPrivateAPI{}
	backend := newBinancePerpPrivateWSBackendForTest(api, &binancePerpNoSendAccountWS{}, "key", "secret")

	_, err := backend.PlaceOrder(context.Background(), binancePerpMalformedSymbolPlaceRequest())
	if !errors.Is(err, exchange.ErrInvalidRequest) {
		t.Fatalf("PlaceOrder error = %v, want ErrInvalidRequest", err)
	}
	if api.connectCalls != 0 {
		t.Fatalf("PlaceOrder opened API websocket %d time(s), want no connection", api.connectCalls)
	}
	if api.placeCalls != 0 {
		t.Fatalf("PlaceOrder sent %d websocket place request(s), want no send", api.placeCalls)
	}
}

func binancePerpMalformedSymbolPlaceRequest() exchange.PlaceOrderRequest {
	return exchange.PlaceOrderRequest{
		Instrument:    "eth-USDT",
		ClientOrderID: "1001",
		Side:          exchange.SideBuy,
		Type:          exchange.OrderTypeMarket,
		Quantity:      decimal.NewFromInt(1),
	}
}

type binancePerpNoSendPrivateAPI struct {
	connectCalls int
	placeCalls   int
}

func (api *binancePerpNoSendPrivateAPI) Connect() error {
	api.connectCalls++
	return nil
}

func (api *binancePerpNoSendPrivateAPI) Close() {}

func (api *binancePerpNoSendPrivateAPI) PlaceOrderWS(string, string, binanceperp.PlaceOrderParams, string) (*binanceperp.OrderResponse, error) {
	api.placeCalls++
	return &binanceperp.OrderResponse{}, nil
}

func (api *binancePerpNoSendPrivateAPI) CancelOrderWS(string, string, binanceperp.CancelOrderParams, string) (*binanceperp.OrderResponse, error) {
	return &binanceperp.OrderResponse{}, nil
}

type binancePerpNoSendAccountWS struct{}

func (ws *binancePerpNoSendAccountWS) Connect() error { return nil }
func (ws *binancePerpNoSendAccountWS) Close()         {}
func (ws *binancePerpNoSendAccountWS) SubscribeOrderUpdate(func(*binanceperp.OrderUpdateEvent)) {
}
func (ws *binancePerpNoSendAccountWS) SubscribeAccountUpdate(func(*binanceperp.AccountUpdateEvent)) {
}
func (ws *binancePerpNoSendAccountWS) SetReconnectHooks(func(error), func()) {}
