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
	"sync"
	"sync/atomic"
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

	spotSettings := settings
	spotSettings.InstrumentCatalog, spotSettings.CatalogScope = bybitBitgetIdentityCatalog(t, exchange.VenueBybit, exchange.ProductSpot)
	spot := NewBybitSpot("key", "secret", spotSettings)
	spotInstrument := bybitBitgetTestInstrument(exchange.VenueBybit, exchange.ProductSpot, "BTC-USDT")
	exerciseSpotWebSocketEveryMethod(t, spot.WebSocket(), spotInstrument, "11")
	exerciseBybitSpotStreams(t, spot.WebSocket(), spotInstrument)
	assertFactoryStringRedacts(t, spot, `exchange/factory.Client{venue:"bybit", product:"spot", credentials:redacted}`, "key", "secret")

	usdtSettings := settings
	usdtSettings.InstrumentCatalog, usdtSettings.CatalogScope = bybitBitgetIdentityCatalog(t, exchange.VenueBybit, exchange.ProductPerp)
	usdtPerp := NewBybitLinearPerp("key", "secret", "USDT", usdtSettings)
	usdtInstrument := bybitBitgetTestInstrument(exchange.VenueBybit, exchange.ProductPerp, "BTC-USDT")
	exercisePerpWebSocketEveryMethod(t, usdtPerp.WebSocket(), usdtInstrument, "21")
	exerciseBybitPerpStreams(t, usdtPerp.WebSocket(), usdtInstrument)
	assertFactoryStringRedacts(t, usdtPerp, `exchange/factory.Client{venue:"bybit", product:"perp", credentials:redacted}`, "key", "secret")

	usdcPerp := NewBybitLinearPerp("key", "secret", "USDC", settings)
	exercisePerpWebSocketEveryMethod(t, usdcPerp.WebSocket(), bybitBitgetTestInstrument(exchange.VenueBybit, exchange.ProductPerp, "BTC-USDC"), "31")
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
		Instrument:    bybitBitgetTestInstrument(exchange.VenueBybit, exchange.ProductSpot, "BTC-USDT"),
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
		Instrument: bybitBitgetTestInstrument(exchange.VenueBybit, exchange.ProductSpot, "BTC-USDT"),
		OrderID:    placed.OrderID,
	})
	if err != nil {
		t.Fatalf("CancelOrder REST bridge: %v", err)
	}
	if canceled.OrderID != placed.OrderID || canceled.State != exchange.AckCanceled {
		t.Fatalf("CancelOrder REST bridge ack = %+v", canceled)
	}
}

func TestBybitPublicWebSocketSubscriptionCloseClosesDedicatedSDKClient(t *testing.T) {
	wsURL, waitClosed, closeServer := newBybitLifecycleWSServer(t)
	defer closeServer()
	settings := Settings{
		Endpoint:          "https://openapi.invalid",
		WebSocketEndpoint: wsURL,
		Environment:       "demo",
		HTTPClient:        &http.Client{Transport: bybitOpenAPIRouter{}},
	}
	client := NewBybitSpot("key", "secret", settings)
	instrument := bybitBitgetTestInstrument(exchange.VenueBybit, exchange.ProductSpot, "BTC-USDT")

	subscription, err := client.WebSocket().WatchOrderBook(context.Background(), exchange.WatchRequest{Instrument: instrument})
	if err != nil {
		t.Fatalf("WatchOrderBook: %v", err)
	}
	assertReceive(t, subscription.Events(), "book")
	if err := subscription.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := subscription.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	waitClosed("Bybit public socket")
}

func TestBybitPrivateWebSocketReusesAllInOneOrderTopicAcrossInstruments(t *testing.T) {
	wsURL, subscribeCount, closeServer := newBybitCountingPrivateWSServer(t)
	defer closeServer()
	settings := Settings{
		Endpoint:          "https://openapi.invalid",
		WebSocketEndpoint: wsURL,
		Environment:       "demo",
		HTTPClient:        &http.Client{Transport: bybitOpenAPIRouter{}},
	}
	client := NewBybitLinearPerp("key", "secret", "USDT", settings)
	btc := bybitBitgetTestInstrument(exchange.VenueBybit, exchange.ProductPerp, "BTC-USDT")
	eth := bybitBitgetTestInstrument(exchange.VenueBybit, exchange.ProductPerp, "ETH-USDT")

	first, err := client.WebSocket().WatchOrders(context.Background(), exchange.WatchRequest{Instrument: btc})
	if err != nil {
		t.Fatalf("first WatchOrders: %v", err)
	}
	defer first.Close()
	second, err := client.WebSocket().WatchOrders(context.Background(), exchange.WatchRequest{Instrument: eth})
	if err != nil {
		t.Fatalf("second WatchOrders: %v", err)
	}
	defer second.Close()

	if got := subscribeCount("order"); got != 1 {
		t.Fatalf("order subscriptions = %d, want 1 all-in-one Bybit private topic", got)
	}
}

func exerciseSpotWebSocketEveryMethod(t *testing.T, ws exchange.SpotWebSocket, instrument exchange.Instrument, orderID string) {
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

func exercisePerpWebSocketEveryMethod(t *testing.T, ws exchange.PerpWebSocket, instrument exchange.Instrument, orderID string) {
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

func exerciseBybitSpotStreams(t *testing.T, ws exchange.SpotWebSocket, instrument exchange.Instrument) {
	t.Helper()
	publicCloses := bybitScriptedWSClosedConnections(t)
	book, err := ws.WatchOrderBook(context.Background(), exchange.WatchRequest{Instrument: instrument})
	if err != nil {
		t.Fatalf("WatchOrderBook: %v", err)
	}
	assertReceive(t, book.Events(), "book").Bids[0].Price.Equal(decimal.RequireFromString("99"))
	if err := book.Close(); err != nil {
		t.Fatalf("close book: %v", err)
	}
	publicCloses.expectNext("book")

	bbo, err := ws.WatchBBO(context.Background(), exchange.WatchRequest{Instrument: instrument})
	if err != nil {
		t.Fatalf("WatchBBO: %v", err)
	}
	assertReceive(t, bbo.Events(), "bbo")
	_ = bbo.Close()
	publicCloses.expectNext("bbo")

	trades, err := ws.WatchPublicTrades(context.Background(), exchange.WatchRequest{Instrument: instrument})
	if err != nil {
		t.Fatalf("WatchPublicTrades: %v", err)
	}
	assertReceive(t, trades.Events(), "public trades")
	_ = trades.Close()
	publicCloses.expectNext("public trades")

	candles, err := ws.WatchCandles(context.Background(), exchange.WatchCandlesRequest{Instrument: instrument, Interval: "1m"})
	if err != nil {
		t.Fatalf("WatchCandles: %v", err)
	}
	assertReceive(t, candles.Events(), "candles")
	_ = candles.Close()
	publicCloses.expectNext("candles")

	orders, err := ws.WatchOrders(context.Background(), exchange.WatchRequest{Instrument: instrument})
	if err != nil {
		t.Fatalf("WatchOrders: %v", err)
	}
	assertReceive(t, orders.Events(), "orders")
	_ = orders.Close()

	fills, err := ws.WatchFills(context.Background(), exchange.WatchRequest{Instrument: instrument})
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

func exerciseBybitPerpStreams(t *testing.T, ws exchange.PerpWebSocket, instrument exchange.Instrument) {
	t.Helper()
	publicCloses := bybitScriptedWSClosedConnections(t)
	positions, err := ws.WatchPositions(context.Background(), exchange.WatchRequest{Instrument: instrument})
	if err != nil {
		t.Fatalf("WatchPositions: %v", err)
	}
	assertReceive(t, positions.Events(), "positions")
	_ = positions.Close()

	mark, err := ws.WatchMarkPrice(context.Background(), exchange.WatchRequest{Instrument: instrument})
	if err != nil {
		t.Fatalf("WatchMarkPrice: %v", err)
	}
	assertReceive(t, mark.Events(), "mark")
	_ = mark.Close()
	publicCloses.expectNext("mark")

	funding, err := ws.WatchFundingRate(context.Background(), exchange.WatchRequest{Instrument: instrument})
	if err != nil {
		t.Fatalf("WatchFundingRate: %v", err)
	}
	assertReceive(t, funding.Events(), "funding")
	_ = funding.Close()
	publicCloses.expectNext("funding")
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

type scriptedWSConnectionTracker struct {
	closed chan struct{}
	count  *atomic.Int64
}

var bybitScriptedWSConnections atomic.Pointer[scriptedWSConnectionTracker]

func bybitScriptedWSClosedConnections(t *testing.T) *scriptedWSConnectionTracker {
	t.Helper()
	tracker := bybitScriptedWSConnections.Load()
	if tracker == nil {
		t.Fatal("Bybit scripted WS tracker is not installed")
	}
	return tracker
}

func (tracker *scriptedWSConnectionTracker) expectNext(label string) {
	select {
	case <-tracker.closed:
		return
	case <-time.After(3 * time.Second):
		panic(fmt.Sprintf("timeout waiting for %s websocket close; closed=%d", label, tracker.count.Load()))
	}
}

func newBybitScriptedWSServer(t *testing.T) (string, func()) {
	t.Helper()
	upgrader := websocket.Upgrader{}
	tracker := &scriptedWSConnectionTracker{closed: make(chan struct{}, 64), count: &atomic.Int64{}}
	bybitScriptedWSConnections.Store(tracker)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		publicConnection := false
		defer func() {
			_ = conn.Close()
			if publicConnection {
				tracker.count.Add(1)
				tracker.closed <- struct{}{}
			}
		}()
		_, payload, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var req bybitScriptedWSRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			t.Errorf("decode first request: %v", err)
			return
		}
		authenticated := req.Op == "auth"
		if authenticated {
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
			publicConnection = !authenticated
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
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
		_, _, _ = conn.ReadMessage()
	}))
	return "ws" + strings.TrimPrefix(server.URL, "http"), func() {
		server.Close()
		bybitScriptedWSConnections.Store(nil)
	}
}

func newBybitLifecycleWSServer(t *testing.T) (string, func(string), func()) {
	t.Helper()
	upgrader := websocket.Upgrader{}
	closed := make(chan struct{})
	var closeOnce sync.Once
	var mu sync.Mutex
	var conns []*websocket.Conn
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		mu.Lock()
		conns = append(conns, conn)
		mu.Unlock()
		defer conn.Close()
		for {
			_, payload, err := conn.ReadMessage()
			if err != nil {
				if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) || strings.Contains(err.Error(), "close") {
					closeOnce.Do(func() { close(closed) })
				}
				return
			}
			var req bybitScriptedWSRequest
			if err := json.Unmarshal(payload, &req); err != nil {
				t.Errorf("decode request: %v", err)
				return
			}
			if req.Op != "subscribe" || len(req.Args) == 0 {
				continue
			}
			var topic string
			if err := json.Unmarshal(req.Args[0], &topic); err != nil {
				t.Errorf("decode topic: %v", err)
				return
			}
			_ = conn.WriteJSON(map[string]any{"op": "subscribe", "req_id": req.ReqID, "success": true})
			_ = conn.WriteMessage(websocket.TextMessage, []byte(bybitWSEvent(topic)))
		}
	}))
	waitClosed := func(label string) {
		t.Helper()
		select {
		case <-closed:
		case <-time.After(500 * time.Millisecond):
			t.Fatalf("%s did not close after subscription stop", label)
		}
	}
	closeServer := func() {
		mu.Lock()
		for _, conn := range conns {
			_ = conn.Close()
		}
		mu.Unlock()
		server.Close()
	}
	return "ws" + strings.TrimPrefix(server.URL, "http"), waitClosed, closeServer
}

func newBybitCountingPrivateWSServer(t *testing.T) (string, func(string) int64, func()) {
	t.Helper()
	upgrader := websocket.Upgrader{}
	var counts sync.Map
	var mu sync.Mutex
	var conns []*websocket.Conn
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		mu.Lock()
		conns = append(conns, conn)
		mu.Unlock()
		defer conn.Close()
		for {
			_, payload, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var req bybitScriptedWSRequest
			if err := json.Unmarshal(payload, &req); err != nil {
				t.Errorf("decode request: %v", err)
				return
			}
			switch req.Op {
			case "auth":
				_ = conn.WriteJSON(map[string]any{"op": "auth", "success": true, "retCode": 0})
			case "subscribe":
				if len(req.Args) == 0 {
					t.Errorf("subscribe missing topic")
					return
				}
				var topic string
				if err := json.Unmarshal(req.Args[0], &topic); err != nil {
					t.Errorf("decode topic: %v", err)
					return
				}
				value, _ := counts.LoadOrStore(topic, &atomic.Int64{})
				value.(*atomic.Int64).Add(1)
				_ = conn.WriteJSON(map[string]any{"op": "subscribe", "req_id": req.ReqID, "success": true})
			}
		}
	}))
	subscribeCount := func(topic string) int64 {
		value, ok := counts.Load(topic)
		if !ok {
			return 0
		}
		return value.(*atomic.Int64).Load()
	}
	closeServer := func() {
		mu.Lock()
		for _, conn := range conns {
			_ = conn.Close()
		}
		mu.Unlock()
		server.Close()
	}
	return "ws" + strings.TrimPrefix(server.URL, "http"), subscribeCount, closeServer
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
