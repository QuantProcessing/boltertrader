package factoryclient

import (
	"encoding/json"
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
)

func TestBitgetWebSocketExercisesEveryExposedMethod(t *testing.T) {
	wsURL, closeServer := newBitgetScriptedWSServer(t)
	defer closeServer()
	transport := &bitgetWSAuxTransport{}
	settings := Settings{
		Endpoint:          "https://openapi.invalid",
		WebSocketEndpoint: wsURL,
		HTTPClient:        &http.Client{Transport: transport},
	}
	spotSettings := settings
	spotSettings.InstrumentCatalog, spotSettings.CatalogScope = bybitBitgetIdentityCatalog(t, exchange.VenueBitget, exchange.ProductSpot)
	spot := NewBitgetSpot("key", "secret", "passphrase", spotSettings)
	spotInstrument := bybitBitgetTestInstrument(exchange.VenueBitget, exchange.ProductSpot, "BTC-USDT")
	exerciseSpotWebSocketEveryMethod(t, spot.WebSocket(), spotInstrument, "11")
	exerciseBitgetSpotStreams(t, spot.WebSocket(), spotInstrument)
	assertFactoryStringRedacts(t, spot, `exchange/factory.Client{venue:"bitget", product:"spot", credentials:redacted}`, "key", "secret", "passphrase")

	usdtSettings := settings
	usdtSettings.InstrumentCatalog, usdtSettings.CatalogScope = bybitBitgetIdentityCatalog(t, exchange.VenueBitget, exchange.ProductPerp)
	usdtPerp := NewBitgetPerp("key", "secret", "passphrase", "USDT-FUTURES", usdtSettings)
	usdtInstrument := bybitBitgetTestInstrument(exchange.VenueBitget, exchange.ProductPerp, "BTC-USDT")
	exercisePerpWebSocketEveryMethod(t, usdtPerp.WebSocket(), usdtInstrument, "21")
	exerciseBitgetPerpStreams(t, usdtPerp.WebSocket(), usdtInstrument)
	assertFactoryStringRedacts(t, usdtPerp, `exchange/factory.Client{venue:"bitget", product:"perp", credentials:redacted}`, "key", "secret", "passphrase")

	usdcPerp := NewBitgetPerp("key", "secret", "passphrase", "USDC-FUTURES", settings)
	usdcInstrument := bybitBitgetTestInstrument(exchange.VenueBitget, exchange.ProductPerp, "BTC-USDC")
	exercisePerpWebSocketEveryMethod(t, usdcPerp.WebSocket(), usdcInstrument, "31")
	exerciseBitgetPerpStreams(t, usdcPerp.WebSocket(), usdcInstrument)
	assertFactoryStringRedacts(t, usdcPerp, `exchange/factory.Client{venue:"bitget", product:"perp", credentials:redacted}`, "key", "secret", "passphrase")

	if got := transport.calls.Load(); got != 3 {
		t.Fatalf("Bitget WebSocket order commands made %d auxiliary REST calls, want 3", got)
	}
}

func TestBitgetPublicWebSocketSubscriptionCloseClosesDedicatedSDKClient(t *testing.T) {
	wsURL, waitClosed, closeServer := newBitgetLifecycleWSServer(t)
	defer closeServer()
	settings := Settings{
		Endpoint:          "https://openapi.invalid",
		WebSocketEndpoint: wsURL,
		HTTPClient:        &http.Client{Transport: bitgetOpenAPIRouter{}},
	}
	client := NewBitgetSpot("key", "secret", "passphrase", settings)
	instrument := bybitBitgetTestInstrument(exchange.VenueBitget, exchange.ProductSpot, "BTC-USDT")

	subscription, err := client.WebSocket().WatchBBO(t.Context(), exchange.WatchRequest{Instrument: instrument})
	if err != nil {
		t.Fatalf("WatchBBO: %v", err)
	}
	assertReceive(t, subscription.Events(), "bbo")
	if err := subscription.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := subscription.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	waitClosed("Bitget public socket")
}

type bitgetWSAuxTransport struct {
	calls atomic.Int64
}

func (transport *bitgetWSAuxTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.calls.Add(1)
	switch request.URL.Path {
	case "/api/v3/market/orderbook":
		return bitgetData(`{"b":[["99","1"]],"a":[["101","2"]],"ts":"1720000000000"}`), nil
	case "/api/v3/account/settings":
		return bitgetData(`{"accountMode":"unified","holdMode":"hedge_mode"}`), nil
	default:
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Body:       io.NopCloser(strings.NewReader(`{"error":"unexpected REST call"}`)),
			Header:     make(http.Header),
		}, nil
	}
}

func exerciseBitgetSpotStreams(t *testing.T, ws exchange.SpotWebSocket, instrument exchange.Instrument) {
	t.Helper()
	publicCloses := bitgetScriptedWSClosedConnections(t)
	book, err := ws.WatchOrderBook(t.Context(), exchange.WatchRequest{Instrument: instrument})
	if err != nil {
		t.Fatalf("WatchOrderBook: %v", err)
	}
	select {
	case streamErr := <-book.Errors():
		t.Fatalf("WatchOrderBook control acknowledgement reached data handler: %v", streamErr)
	case <-book.Events():
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for Bitget order book event")
	}
	_ = book.Close()
	publicCloses.expectNext("book")

	bbo, err := ws.WatchBBO(t.Context(), exchange.WatchRequest{Instrument: instrument})
	if err != nil {
		t.Fatalf("WatchBBO: %v", err)
	}
	assertReceive(t, bbo.Events(), "bbo")
	_ = bbo.Close()
	publicCloses.expectNext("bbo")

	trades, err := ws.WatchPublicTrades(t.Context(), exchange.WatchRequest{Instrument: instrument})
	if err != nil {
		t.Fatalf("WatchPublicTrades: %v", err)
	}
	assertReceive(t, trades.Events(), "public trades")
	_ = trades.Close()
	publicCloses.expectNext("public trades")

	candles, err := ws.WatchCandles(t.Context(), exchange.WatchCandlesRequest{Instrument: instrument, Interval: "1m"})
	if err != nil {
		t.Fatalf("WatchCandles: %v", err)
	}
	assertReceive(t, candles.Events(), "candles")
	_ = candles.Close()
	publicCloses.expectNext("candles")

	orders, err := ws.WatchOrders(t.Context(), exchange.WatchRequest{Instrument: instrument})
	if err != nil {
		t.Fatalf("WatchOrders: %v", err)
	}
	assertReceive(t, orders.Events(), "orders")
	_ = orders.Close()

	fills, err := ws.WatchFills(t.Context(), exchange.WatchRequest{Instrument: instrument})
	if err != nil {
		t.Fatalf("WatchFills: %v", err)
	}
	assertReceive(t, fills.Events(), "fills")
	_ = fills.Close()

	balances, err := ws.WatchBalances(t.Context(), exchange.WatchAccountRequest{})
	if err != nil {
		t.Fatalf("WatchBalances: %v", err)
	}
	assertReceive(t, balances.Events(), "balances")
	_ = balances.Close()
}

func exerciseBitgetPerpStreams(t *testing.T, ws exchange.PerpWebSocket, instrument exchange.Instrument) {
	t.Helper()
	publicCloses := bitgetScriptedWSClosedConnections(t)
	positions, err := ws.WatchPositions(t.Context(), exchange.WatchRequest{Instrument: instrument})
	if err != nil {
		t.Fatalf("WatchPositions: %v", err)
	}
	assertReceive(t, positions.Events(), "positions")
	_ = positions.Close()

	mark, err := ws.WatchMarkPrice(t.Context(), exchange.WatchRequest{Instrument: instrument})
	if err != nil {
		t.Fatalf("WatchMarkPrice: %v", err)
	}
	assertReceive(t, mark.Events(), "mark")
	_ = mark.Close()
	publicCloses.expectNext("mark")

	funding, err := ws.WatchFundingRate(t.Context(), exchange.WatchRequest{Instrument: instrument})
	if err != nil {
		t.Fatalf("WatchFundingRate: %v", err)
	}
	assertReceive(t, funding.Events(), "funding")
	_ = funding.Close()
	publicCloses.expectNext("funding")
}

var bitgetScriptedWSConnections atomic.Pointer[scriptedWSConnectionTracker]

func bitgetScriptedWSClosedConnections(t *testing.T) *scriptedWSConnectionTracker {
	t.Helper()
	tracker := bitgetScriptedWSConnections.Load()
	if tracker == nil {
		t.Fatal("Bitget scripted WS tracker is not installed")
	}
	return tracker
}

func newBitgetScriptedWSServer(t *testing.T) (string, func()) {
	t.Helper()
	upgrader := websocket.Upgrader{}
	tracker := &scriptedWSConnectionTracker{closed: make(chan struct{}, 64), count: &atomic.Int64{}}
	bitgetScriptedWSConnections.Store(tracker)
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
		authenticated := false
		for {
			_, payload, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if string(payload) == "ping" {
				_ = conn.WriteMessage(websocket.TextMessage, []byte("pong"))
				continue
			}
			var req struct {
				Op       string `json:"op"`
				ID       string `json:"id"`
				Category string `json:"category"`
				Topic    string `json:"topic"`
				Args     []struct {
					InstType string `json:"instType"`
					Topic    string `json:"topic"`
					Symbol   string `json:"symbol"`
					Interval string `json:"interval"`
					OrderID  string `json:"orderId"`
				} `json:"args"`
			}
			_ = json.Unmarshal(payload, &req)
			switch req.Op {
			case "login":
				authenticated = true
				_ = conn.WriteJSON(map[string]any{"event": "login", "code": "0"})
			case "trade":
				orderID := "ws-11"
				if req.Topic == "cancel-order" && len(req.Args) > 0 {
					orderID = req.Args[0].OrderID
				}
				_ = conn.WriteJSON(map[string]any{"event": "trade", "id": req.ID, "category": req.Category, "topic": req.Topic, "code": "0", "msg": "success", "args": []map[string]string{{"orderId": orderID, "clientOid": "401"}}})
				if !authenticated {
					return
				}
			case "subscribe":
				if len(req.Args) == 0 {
					return
				}
				publicConnection = !authenticated
				arg := req.Args[0]
				if !bitgetExpectedV3WSArg(arg.InstType, arg.Topic, arg.Symbol) {
					_ = conn.WriteJSON(map[string]any{"event": "error", "code": "30001", "msg": "unexpected subscription wire", "arg": arg})
					return
				}
				_ = conn.WriteJSON(map[string]any{"event": "subscribe", "code": "0", "arg": arg})
				_ = conn.WriteMessage(websocket.TextMessage, []byte(bitgetWSEvent(arg.InstType, arg.Topic, arg.Symbol, arg.Interval)))
			case "unsubscribe":
				if !authenticated {
					return
				}
				_ = conn.WriteJSON(map[string]any{"event": "unsubscribe", "code": "0", "args": req.Args})
			}
		}
	}))
	return "ws" + strings.TrimPrefix(server.URL, "http"), func() {
		server.Close()
		bitgetScriptedWSConnections.Store(nil)
	}
}

func newBitgetLifecycleWSServer(t *testing.T) (string, func(string), func()) {
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
			if string(payload) == "ping" {
				_ = conn.WriteMessage(websocket.TextMessage, []byte("pong"))
				continue
			}
			var req struct {
				Op   string `json:"op"`
				Args []struct {
					InstType string `json:"instType"`
					Topic    string `json:"topic"`
					Symbol   string `json:"symbol"`
					Interval string `json:"interval"`
				} `json:"args"`
			}
			if err := json.Unmarshal(payload, &req); err != nil {
				t.Errorf("decode request: %v", err)
				return
			}
			if req.Op != "subscribe" || len(req.Args) == 0 {
				continue
			}
			arg := req.Args[0]
			_ = conn.WriteJSON(map[string]any{"event": "subscribe", "code": "0", "arg": arg})
			_ = conn.WriteMessage(websocket.TextMessage, []byte(bitgetWSEvent(arg.InstType, arg.Topic, arg.Symbol, arg.Interval)))
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

func bitgetExpectedV3WSArg(instType, topic, symbol string) bool {
	switch topic {
	case "books5", "ticker", "publicTrade", "kline":
		return (instType == "spot" || instType == "usdt-futures" || instType == "usdc-futures") && symbol != ""
	case "order", "fill", "account", "position":
		return instType == "UTA" && symbol == ""
	default:
		return false
	}
}

func bitgetWSEvent(instType, topic, symbol, interval string) string {
	arg := `{"instType":"` + instType + `","topic":"` + topic + `","symbol":"` + symbol + `"}`
	if symbol == "" || symbol == "default" {
		symbol = "BTCUSDT"
	}
	switch topic {
	case "books5":
		return `{"arg":` + arg + `,"action":"snapshot","data":[{"b":[["99","1"]],"a":[["101","2"]],"seq":2,"pseq":1,"ts":"1720000000000"}]}`
	case "ticker":
		return `{"arg":` + arg + `,"ts":1720000000000,"data":[{"symbol":"` + symbol + `","bid1Price":"99","bid1Size":"1","ask1Price":"101","ask1Size":"2","markPrice":"100","fundingRate":"0.0001","nextFundingTime":"1720003600000"}]}`
	case "publicTrade":
		return `{"arg":` + arg + `,"data":[{"i":"t1","p":"100","v":"0.1","S":"buy","T":"1720000000000","L":"plus"}]}`
	case "kline":
		_ = interval
		return `{"arg":` + arg + `,"data":[{"start":"1720000000000","open":"100","high":"101","low":"99","close":"100.5","volume":"3","turnover":"300"}]}`
	case "order":
		return `{"arg":` + arg + `,"data":[{"orderId":"11","clientOid":"101","symbol":"` + symbol + `","side":"buy","orderType":"limit","timeInForce":"gtc","price":"99","qty":"1","filledQty":"0","orderStatus":"live","cTime":"1720000000000","uTime":"1720000001000"}]}`
	case "fill":
		return `{"arg":` + arg + `,"data":[{"orderId":"11","clientOid":"101","execId":"e1","symbol":"` + symbol + `","side":"buy","execPrice":"99","execQty":"1","feeDetail":[{"feeCoin":"USDT","fee":"0.01"}],"execTime":"1720000000000"}]}`
	case "account":
		return `{"arg":` + arg + `,"data":[{"coin":[{"coin":"USDT","available":"99","locked":"1","equity":"100","usdValue":"100"}]}]}`
	case "position":
		return `{"arg":` + arg + `,"data":[{"symbol":"` + symbol + `","posSide":"long","qty":"1","averageOpenPrice":"99","markPrice":"100","liquidationPrice":"50","leverage":"5","unrealisedPnl":"1"}]}`
	default:
		return `{"arg":` + arg + `,"data":[]}`
	}
}
