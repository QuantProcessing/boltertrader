package factoryclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/gorilla/websocket"
	"github.com/shopspring/decimal"
)

func TestBybitWebSocketExercisesEveryExposedMethod(t *testing.T) {
	wsURL, closeServer := newBybitScriptedWSServer(t)
	defer closeServer()
	transport := &noSendTransport{}
	settings := Settings{
		Endpoint:          "https://openapi.invalid",
		WebSocketEndpoint: wsURL,
		Environment:       "demo",
		HTTPClient:        &http.Client{Transport: transport},
	}

	spot := NewBybitSpot("key", "secret", settings)
	exerciseSpotWebSocketEveryMethod(t, spot.WebSocket(), "BTC-USDT", "11")
	exerciseBybitSpotStreams(t, spot.WebSocket())
	assertFactoryStringRedacts(t, spot, `exchange/factory.Client{venue:"bybit", product:"spot", credentials:redacted}`, "key", "secret")

	usdtPerp := NewBybitLinearPerp("key", "secret", "USDT", settings)
	exercisePerpWebSocketEveryMethod(t, usdtPerp.WebSocket(), "BTC-USDT", "21")
	exerciseBybitPerpStreams(t, usdtPerp.WebSocket())
	assertFactoryStringRedacts(t, usdtPerp, `exchange/factory.Client{venue:"bybit", product:"perp", credentials:redacted}`, "key", "secret")

	usdcPerp := NewBybitLinearPerp("key", "secret", "USDC", settings)
	exercisePerpWebSocketEveryMethod(t, usdcPerp.WebSocket(), "BTC-USDC", "31")
	assertFactoryStringRedacts(t, usdcPerp, `exchange/factory.Client{venue:"bybit", product:"perp", credentials:redacted}`, "key", "secret")

	if got := transport.calls.Load(); got != 0 {
		t.Fatalf("Bybit WebSocket order commands made %d REST calls", got)
	}
}

func TestBybitDemoWebSocketOrderCommandsUseRESTBridge(t *testing.T) {
	client := NewBybitSpot("key", "secret", Settings{
		Endpoint:    "https://openapi.invalid",
		Environment: "demo",
		HTTPClient:  &http.Client{Transport: bybitOpenAPIRouter{}},
	})

	placed, err := client.WebSocket().PlaceOrder(context.Background(), exchange.PlaceOrderRequest{
		Instrument:    "BTC-USDT",
		ClientOrderID: "401",
		Side:          exchange.SideBuy,
		Type:          exchange.OrderTypeLimit,
		Quantity:      decimal.NewFromInt(1),
		LimitPrice:    decimal.NewFromInt(99),
		LimitPolicy:   exchange.LimitPolicyResting,
	})
	if err != nil {
		t.Fatalf("PlaceOrder REST bridge: %v", err)
	}
	if placed.OrderID != "11" || placed.ClientOrderID != "401" {
		t.Fatalf("PlaceOrder REST bridge ack = %+v", placed)
	}

	canceled, err := client.WebSocket().CancelOrder(context.Background(), exchange.CancelOrderRequest{
		Instrument: "BTC-USDT",
		OrderID:    placed.OrderID,
	})
	if err != nil {
		t.Fatalf("CancelOrder REST bridge: %v", err)
	}
	if canceled.OrderID != placed.OrderID || canceled.State != exchange.AckCanceled {
		t.Fatalf("CancelOrder REST bridge ack = %+v", canceled)
	}
}

func exerciseSpotWebSocketEveryMethod(t *testing.T, ws exchange.SpotWebSocket, instrument, orderID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := ws.PlaceOrder(ctx, exchange.PlaceOrderRequest{
		Instrument:    instrument,
		ClientOrderID: "401",
		Side:          exchange.SideBuy,
		Type:          exchange.OrderTypeMarket,
		Quantity:      decimal.RequireFromString("1"),
	}); err != nil {
		t.Fatalf("PlaceOrder fallback: %v", err)
	}
	if _, err := ws.CancelOrder(ctx, exchange.CancelOrderRequest{Instrument: instrument, OrderID: orderID}); err != nil {
		t.Fatalf("CancelOrder fallback: %v", err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	assertCanceledWS(t, "WatchOrderBook", func() error {
		_, err := ws.WatchOrderBook(canceled, exchange.WatchRequest{Instrument: instrument})
		return err
	})
	assertCanceledWS(t, "WatchBBO", func() error {
		_, err := ws.WatchBBO(canceled, exchange.WatchRequest{Instrument: instrument})
		return err
	})
	assertCanceledWS(t, "WatchPublicTrades", func() error {
		_, err := ws.WatchPublicTrades(canceled, exchange.WatchRequest{Instrument: instrument})
		return err
	})
	assertCanceledWS(t, "WatchCandles", func() error {
		_, err := ws.WatchCandles(canceled, exchange.WatchCandlesRequest{Instrument: instrument, Interval: "1m"})
		return err
	})
	assertCanceledWS(t, "WatchOrders", func() error {
		_, err := ws.WatchOrders(canceled, exchange.WatchRequest{Instrument: instrument})
		return err
	})
	assertCanceledWS(t, "WatchFills", func() error {
		_, err := ws.WatchFills(canceled, exchange.WatchRequest{Instrument: instrument})
		return err
	})
	assertCanceledWS(t, "WatchBalances", func() error {
		_, err := ws.WatchBalances(canceled, exchange.WatchAccountRequest{})
		return err
	})
}

func exercisePerpWebSocketEveryMethod(t *testing.T, ws exchange.PerpWebSocket, instrument, orderID string) {
	t.Helper()
	exerciseSpotWebSocketEveryMethod(t, ws, instrument, orderID)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	assertCanceledWS(t, "WatchPositions", func() error {
		_, err := ws.WatchPositions(canceled, exchange.WatchRequest{Instrument: instrument})
		return err
	})
	assertCanceledWS(t, "WatchMarkPrice", func() error {
		_, err := ws.WatchMarkPrice(canceled, exchange.WatchRequest{Instrument: instrument})
		return err
	})
	assertCanceledWS(t, "WatchFundingRate", func() error {
		_, err := ws.WatchFundingRate(canceled, exchange.WatchRequest{Instrument: instrument})
		return err
	})
}

func assertCanceledWS(t *testing.T, operation string, run func() error) {
	t.Helper()
	err := run()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("%s error = %v, want context canceled", operation, err)
	}
}

func exerciseBybitSpotStreams(t *testing.T, ws exchange.SpotWebSocket) {
	t.Helper()
	book, err := ws.WatchOrderBook(context.Background(), exchange.WatchRequest{Instrument: "BTC-USDT"})
	if err != nil {
		t.Fatalf("WatchOrderBook: %v", err)
	}
	assertReceive(t, book.Events(), "book").Bids[0].Price.Equal(decimal.RequireFromString("99"))
	if err := book.Close(); err != nil {
		t.Fatalf("close book: %v", err)
	}

	bbo, err := ws.WatchBBO(context.Background(), exchange.WatchRequest{Instrument: "BTC-USDT"})
	if err != nil {
		t.Fatalf("WatchBBO: %v", err)
	}
	assertReceive(t, bbo.Events(), "bbo")
	_ = bbo.Close()

	trades, err := ws.WatchPublicTrades(context.Background(), exchange.WatchRequest{Instrument: "BTC-USDT"})
	if err != nil {
		t.Fatalf("WatchPublicTrades: %v", err)
	}
	assertReceive(t, trades.Events(), "public trades")
	_ = trades.Close()

	candles, err := ws.WatchCandles(context.Background(), exchange.WatchCandlesRequest{Instrument: "BTC-USDT", Interval: "1m"})
	if err != nil {
		t.Fatalf("WatchCandles: %v", err)
	}
	assertReceive(t, candles.Events(), "candles")
	_ = candles.Close()

	orders, err := ws.WatchOrders(context.Background(), exchange.WatchRequest{Instrument: "BTC-USDT"})
	if err != nil {
		t.Fatalf("WatchOrders: %v", err)
	}
	assertReceive(t, orders.Events(), "orders")
	_ = orders.Close()

	fills, err := ws.WatchFills(context.Background(), exchange.WatchRequest{Instrument: "BTC-USDT"})
	if err != nil {
		t.Fatalf("WatchFills: %v", err)
	}
	assertReceive(t, fills.Events(), "fills")
	_ = fills.Close()

	balances, err := ws.WatchBalances(context.Background(), exchange.WatchAccountRequest{})
	if err != nil {
		t.Fatalf("WatchBalances: %v", err)
	}
	assertReceive(t, balances.Events(), "balances")
	_ = balances.Close()
}

func exerciseBybitPerpStreams(t *testing.T, ws exchange.PerpWebSocket) {
	t.Helper()
	positions, err := ws.WatchPositions(context.Background(), exchange.WatchRequest{Instrument: "BTC-USDT"})
	if err != nil {
		t.Fatalf("WatchPositions: %v", err)
	}
	assertReceive(t, positions.Events(), "positions")
	_ = positions.Close()

	mark, err := ws.WatchMarkPrice(context.Background(), exchange.WatchRequest{Instrument: "BTC-USDT"})
	if err != nil {
		t.Fatalf("WatchMarkPrice: %v", err)
	}
	assertReceive(t, mark.Events(), "mark")
	_ = mark.Close()

	funding, err := ws.WatchFundingRate(context.Background(), exchange.WatchRequest{Instrument: "BTC-USDT"})
	if err != nil {
		t.Fatalf("WatchFundingRate: %v", err)
	}
	assertReceive(t, funding.Events(), "funding")
	_ = funding.Close()
}

func assertReceive[T any](t *testing.T, ch <-chan T, label string) T {
	t.Helper()
	select {
	case event := <-ch:
		return event
	case <-time.After(3 * time.Second):
		t.Fatalf("timeout waiting for %s event", label)
		var zero T
		return zero
	}
}

func newBybitScriptedWSServer(t *testing.T) (string, func()) {
	t.Helper()
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()
		_, payload, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var req bybitScriptedWSRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			t.Errorf("decode first request: %v", err)
			return
		}
		if req.Op == "auth" {
			_ = conn.WriteJSON(map[string]any{"op": "auth", "success": true, "retCode": 0})
			_, payload, err = conn.ReadMessage()
			if err != nil {
				return
			}
			if err := json.Unmarshal(payload, &req); err != nil {
				t.Errorf("decode authenticated request: %v", err)
				return
			}
		}
		if req.Op == "order.create" || req.Op == "order.cancel" {
			orderID := "41"
			orderLinkID := "401"
			if len(req.Args) > 0 {
				var arg struct {
					OrderID     string `json:"orderId"`
					OrderLinkID string `json:"orderLinkId"`
				}
				if err := json.Unmarshal(req.Args[0], &arg); err != nil {
					t.Errorf("decode trade arg: %v", err)
					return
				}
				if arg.OrderID != "" {
					orderID = arg.OrderID
				}
				if arg.OrderLinkID != "" {
					orderLinkID = arg.OrderLinkID
				}
			}
			_ = conn.WriteJSON(map[string]any{
				"reqId": req.TradeReqID, "retCode": 0, "retMsg": "OK", "op": req.Op,
				"data": map[string]string{"orderId": orderID, "orderLinkId": orderLinkID},
			})
			return
		}
		if req.Op == "subscribe" {
			_ = conn.WriteJSON(map[string]any{"op": "subscribe", "req_id": req.ReqID, "success": true})
		}
		if len(req.Args) == 0 {
			return
		}
		var topic string
		if err := json.Unmarshal(req.Args[0], &topic); err != nil {
			t.Errorf("decode subscription topic: %v", err)
			return
		}
		_ = conn.WriteMessage(websocket.TextMessage, []byte(bybitWSEvent(topic)))
		_, _, _ = conn.ReadMessage()
	}))
	return "ws" + strings.TrimPrefix(server.URL, "http"), server.Close
}

type bybitScriptedWSRequest struct {
	ReqID      string            `json:"req_id"`
	TradeReqID string            `json:"reqId"`
	Op         string            `json:"op"`
	Args       []json.RawMessage `json:"args"`
}

func bybitWSEvent(topic string) string {
	switch {
	case strings.HasPrefix(topic, "orderbook."):
		return `{"topic":"` + topic + `","type":"snapshot","ts":1720000000000,"data":{"s":"BTCUSDT","b":[["99","1"]],"a":[["101","2"]],"u":1,"seq":1}}`
	case strings.HasPrefix(topic, "tickers."):
		return `{"topic":"` + topic + `","ts":1720000000000,"data":{"symbol":"BTCUSDT","lastPrice":"100","markPrice":"100","fundingRate":"0.0001","nextFundingTime":"1720003600000","time":"1720000000000"}}`
	case strings.HasPrefix(topic, "publicTrade."):
		return `{"topic":"` + topic + `","data":[{"i":"t1","s":"BTCUSDT","p":"100","v":"0.1","S":"Buy","T":1720000000000}]}`
	case strings.HasPrefix(topic, "kline."):
		return `{"topic":"` + topic + `","data":[{"start":1720000000000,"open":"100","high":"101","low":"99","close":"100.5","volume":"3"}]}`
	case topic == "order":
		return `{"topic":"order","data":[{"orderId":"11","orderLinkId":"101","symbol":"BTCUSDT","side":"Buy","orderType":"Limit","timeInForce":"GTC","price":"99","qty":"1","cumExecQty":"0","orderStatus":"New","createdTime":"1720000000000","updatedTime":"1720000001000"}]}`
	case topic == "execution":
		return `{"topic":"execution","data":[{"execId":"e1","orderId":"11","orderLinkId":"101","symbol":"BTCUSDT","side":"Buy","execPrice":"99","execQty":"1","execFee":"0.01","feeCurrency":"USDT","execTime":"1720000000000"}]}`
	case topic == "wallet":
		return `{"topic":"wallet","data":[{"accountType":"UNIFIED","coin":[{"coin":"USDT","equity":"100","walletBalance":"100","locked":"1"}]}]}`
	case topic == "position":
		return `{"topic":"position","data":[{"symbol":"BTCUSDT","side":"Buy","size":"1","avgPrice":"99","unrealisedPnl":"1","liqPrice":"50","leverage":"5"}]}`
	default:
		return `{"topic":"` + topic + `","data":[]}`
	}
}

func assertFactoryStringRedacts(t *testing.T, value any, want string, secrets ...string) {
	t.Helper()
	got := fmt.Sprintf("%s", value)
	if got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	if fmt.Sprintf("%#v", value) != want {
		t.Fatalf("GoString() = %q, want %q", fmt.Sprintf("%#v", value), want)
	}
	for _, secret := range secrets {
		if strings.Contains(got, secret) || strings.Contains(fmt.Sprintf("%#v", value), secret) {
			t.Fatalf("redacted string leaked secret %q: %s", secret, got)
		}
	}
}

type bybitWSRestRouter struct{}

func (bybitWSRestRouter) RoundTrip(request *http.Request) (*http.Response, error) {
	body := ""
	if request.Body != nil {
		payload, _ := io.ReadAll(request.Body)
		body = string(payload)
	}
	orderID := "11"
	if strings.Contains(body, `"orderId":"`) {
		orderID = betweenWS(body, `"orderId":"`, `"`)
	}
	clientOrderID := "401"
	if strings.Contains(body, `"orderLinkId":"`) {
		clientOrderID = betweenWS(body, `"orderLinkId":"`, `"`)
	}
	switch request.URL.Path {
	case "/v5/order/create":
		return jsonHTTP(`{"retCode":0,"retMsg":"OK","result":{"orderId":"` + orderID + `","orderLinkId":"` + clientOrderID + `"}}`), nil
	case "/v5/order/cancel":
		return jsonHTTP(`{"retCode":0,"retMsg":"OK","result":{"orderId":"` + orderID + `","orderLinkId":"` + clientOrderID + `"}}`), nil
	default:
		return jsonHTTP(`{"retCode":10001,"retMsg":"unexpected bybit ws rest route"}`), nil
	}
}

func jsonHTTP(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body))}
}

func betweenWS(s, prefix, suffix string) string {
	start := strings.Index(s, prefix)
	if start < 0 {
		return ""
	}
	start += len(prefix)
	end := strings.Index(s[start:], suffix)
	if end < 0 {
		return s[start:]
	}
	return s[start : start+end]
}
