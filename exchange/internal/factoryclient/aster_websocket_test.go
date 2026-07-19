package factoryclient

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/gorilla/websocket"
	"github.com/shopspring/decimal"
)

func TestAsterWebSocketFixtureFileCoversOrderBridge(t *testing.T) {
	TestAsterWebSocketOrderCommandsUseRESTBridge(t)
}

func TestAsterWebSocketExercisesEveryExposedMethod(t *testing.T) {
	fixture := newAsterWSFixture(t)
	settings := Settings{Environment: "testnet", Endpoint: fixture.restURL, WebSocketEndpoint: fixture.wsURL}

	spot := NewAsterSpot(testAsterUserAddress, testAsterPrivateKey, testAsterUserAddress, settings).WebSocket()
	exerciseAsterSpotWebSocketFixture(t, spot, fixture)

	perp := NewAsterUSDTPerp(testAsterUserAddress, testAsterPrivateKey, testAsterUserAddress, settings).WebSocket()
	exerciseAsterPerpWebSocketFixture(t, perp, fixture)
}

func TestAsterClientCachesAndClosesWebSocketFacet(t *testing.T) {
	client := NewAsterUSDTPerp(testAsterUserAddress, testAsterPrivateKey, testAsterUserAddress, Settings{Environment: "testnet"})
	if client.WebSocket() != client.WebSocket() {
		t.Fatal("Aster WebSocket returned different facets")
	}
	if err := client.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	rendered := fmt.Sprintf("%v %+v %#v", client, client.WebSocket(), client)
	for _, secret := range []string{testAsterPrivateKey, "ac0974", "signature=", "userinfo"} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("Aster formatting leaked %q: %s", secret, rendered)
		}
	}
}

type asterWSFixture struct {
	t        *testing.T
	restURL  string
	wsURL    string
	frames   chan asterWSFrame
	writeMu  sync.Mutex
	upgrader websocket.Upgrader
	server   *httptest.Server
}

type asterWSFrame struct {
	Method string
	Params []string
}

func newAsterWSFixture(t *testing.T) *asterWSFixture {
	t.Helper()
	fixture := &asterWSFixture{t: t, frames: make(chan asterWSFrame, 128)}
	rest := newAsterOpenAPIRouter()
	fixture.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if websocket.IsWebSocketUpgrade(r) {
			fixture.serveWS(w, r)
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v3/listenKey":
			_, _ = io.WriteString(w, `{"listenKey":"spot-listen-key"}`)
			return
		case (r.Method == http.MethodPut || r.Method == http.MethodDelete) && r.URL.Path == "/api/v3/listenKey":
			_, _ = io.WriteString(w, `{}`)
			return
		case r.Method == http.MethodPost && r.URL.Path == "/fapi/v3/listenKey":
			_, _ = io.WriteString(w, `{"listenKey":"perp-listen-key"}`)
			return
		case (r.Method == http.MethodPut || r.Method == http.MethodDelete) && r.URL.Path == "/fapi/v3/listenKey":
			_, _ = io.WriteString(w, `{}`)
			return
		}
		resp, err := rest.RoundTrip(r)
		if err != nil {
			t.Errorf("Aster REST fixture: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		defer resp.Body.Close()
		for k, values := range resp.Header {
			for _, value := range values {
				w.Header().Add(k, value)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	t.Cleanup(fixture.server.Close)
	fixture.restURL = fixture.server.URL
	fixture.wsURL = websocketURLFromHTTP(fixture.server.URL)
	return fixture
}

func (fixture *asterWSFixture) serveWS(w http.ResponseWriter, r *http.Request) {
	conn, err := fixture.upgrader.Upgrade(w, r, nil)
	if err != nil {
		fixture.t.Errorf("Aster WS upgrade: %v", err)
		return
	}
	if strings.Contains(r.URL.Path, "listen-key") {
		fixture.serveAsterAccountWS(conn, r.URL.Path)
		return
	}
	defer conn.Close()
	for {
		var req struct {
			Method string   `json:"method"`
			Params []string `json:"params"`
		}
		if err := conn.ReadJSON(&req); err != nil {
			return
		}
		fixture.frames <- asterWSFrame{Method: req.Method, Params: req.Params}
		if req.Method == "SUBSCRIBE" {
			for _, stream := range req.Params {
				if payload := asterMarketPayload(fixture.t, stream); payload != nil {
					go fixture.writePayloadBurst(conn, stream, payload)
				}
			}
		}
	}
}

func (fixture *asterWSFixture) writePayloadBurst(conn *websocket.Conn, stream string, payload []byte) {
	for range 5 {
		time.Sleep(20 * time.Millisecond)
		fixture.writeMu.Lock()
		if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
			fixture.writeMu.Unlock()
			fixture.frames <- asterWSFrame{Method: "WRITE_ERROR", Params: []string{stream, err.Error()}}
			return
		}
		fixture.writeMu.Unlock()
		fixture.frames <- asterWSFrame{Method: "WRITE", Params: []string{stream}}
	}
}

func (fixture *asterWSFixture) serveAsterAccountWS(conn *websocket.Conn, path string) {
	defer conn.Close()
	fixture.frames <- asterWSFrame{Method: "ACCOUNT", Params: []string{path}}
	payloads := [][]byte{
		asterReadFixture(fixture.t, "sdk/aster/spot/testdata/v3/execution_report.json"),
		asterReadFixture(fixture.t, "sdk/aster/spot/testdata/v3/account_update.json"),
	}
	if strings.Contains(path, "perp-listen-key") {
		payloads = [][]byte{
			asterReadFixture(fixture.t, "sdk/aster/perp/testdata/v3/order_update.json"),
			asterReadFixture(fixture.t, "sdk/aster/perp/testdata/v3/account_update.json"),
		}
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case <-ticker.C:
			for _, payload := range payloads {
				fixture.frames <- asterWSFrame{Method: "ACCOUNT_WRITE_ATTEMPT", Params: []string{path}}
				_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
				if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
					fixture.frames <- asterWSFrame{Method: "ACCOUNT_WRITE_ERROR", Params: []string{path, err.Error()}}
					return
				}
				fixture.frames <- asterWSFrame{Method: "ACCOUNT_WRITE", Params: []string{path}}
			}
		case <-timeout:
			return
		}
	}
}

func asterMarketPayload(t *testing.T, stream string) []byte {
	t.Helper()
	switch stream {
	case "asterusdt@depth":
		return asterReadFixture(t, "sdk/aster/spot/testdata/v3/depth_stream.json")
	case "asterusdt@bookTicker":
		return asterReadFixture(t, "sdk/aster/spot/testdata/v3/book_ticker_stream.json")
	case "asterusdt@aggTrade":
		return asterReadFixture(t, "sdk/aster/spot/testdata/v3/agg_trade_stream.json")
	case "asterusdt@kline_1m":
		return []byte(`{"e":"kline","E":1783641600250,"s":"ASTERUSDT","k":{"t":1783641540000,"T":1783641599999,"s":"ASTERUSDT","i":"1m","o":"1.2470","c":"1.2500","h":"1.2510","l":"1.2460","v":"1000.00","x":true}}`)
	case "asterusdt@markPrice@1s":
		return asterReadFixture(t, "sdk/aster/perp/testdata/v3/mark_price_stream.json")
	default:
		return nil
	}
}

func asterReadFixture(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		body, err = os.ReadFile("../../../" + path)
	}
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	return body
}

func websocketURLFromHTTP(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		panic(err)
	}
	parsed.Scheme = strings.Replace(parsed.Scheme, "http", "ws", 1)
	return parsed.String()
}

func (fixture *asterWSFixture) requireFrame(method, stream string) {
	fixture.t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case frame := <-fixture.frames:
			if frame.Method == "WRITE_ERROR" && len(frame.Params) >= 2 && frame.Params[0] == stream {
				fixture.t.Fatalf("Aster fixture write failed: %s", frame.Params[1])
			}
			if frame.Method == "ACCOUNT_WRITE_ERROR" && len(frame.Params) >= 2 && strings.Contains(frame.Params[0], stream) {
				fixture.t.Fatalf("Aster account fixture write failed: %s", frame.Params[1])
			}
			if frame.Method != method {
				continue
			}
			for _, param := range frame.Params {
				if param == stream || (strings.HasPrefix(frame.Method, "ACCOUNT") && strings.Contains(param, stream)) {
					return
				}
			}
		case <-deadline:
			fixture.t.Fatalf("timed out waiting for Aster %s %s", method, stream)
		}
	}
}

func exerciseAsterSpotWebSocketFixture(t *testing.T, ws exchange.SpotWebSocket, fixture *asterWSFixture) {
	t.Helper()
	defer ws.Close()
	ctx := context.Background()

	book, err := ws.WatchOrderBook(ctx, exchange.WatchRequest{Instrument: "ASTER-USDT", Options: exchange.WatchOptions{Buffer: 8}})
	if err != nil {
		t.Fatalf("Aster spot WatchOrderBook: %v", err)
	}
	fixture.requireFrame("SUBSCRIBE", "asterusdt@depth")
	fixture.requireFrame("WRITE", "asterusdt@depth")
	requireSubscriptionEvent(t, book.Events(), "Aster spot book")
	_ = book.Close()

	bbo, err := ws.WatchBBO(ctx, exchange.WatchRequest{Instrument: "ASTER-USDT", Options: exchange.WatchOptions{Buffer: 8}})
	if err != nil {
		t.Fatalf("Aster spot WatchBBO: %v", err)
	}
	fixture.requireFrame("SUBSCRIBE", "asterusdt@bookTicker")
	requireSubscriptionEvent(t, bbo.Events(), "Aster spot bbo")
	_ = bbo.Close()

	trades, err := ws.WatchPublicTrades(ctx, exchange.WatchRequest{Instrument: "ASTER-USDT", Options: exchange.WatchOptions{Buffer: 8}})
	if err != nil {
		t.Fatalf("Aster spot WatchPublicTrades: %v", err)
	}
	fixture.requireFrame("SUBSCRIBE", "asterusdt@aggTrade")
	requireSubscriptionEvent(t, trades.Events(), "Aster spot trade")
	_ = trades.Close()

	candles, err := ws.WatchCandles(ctx, exchange.WatchCandlesRequest{Instrument: "ASTER-USDT", Interval: "1m", Options: exchange.WatchOptions{Buffer: 8}})
	if err != nil {
		t.Fatalf("Aster spot WatchCandles: %v", err)
	}
	fixture.requireFrame("SUBSCRIBE", "asterusdt@kline_1m")
	requireSubscriptionEvent(t, candles.Events(), "Aster spot candle")
	_ = candles.Close()

	orders, err := ws.WatchOrders(ctx, exchange.WatchRequest{Instrument: "ASTER-USDT", Options: exchange.WatchOptions{Buffer: 8}})
	if err != nil {
		t.Fatalf("Aster spot WatchOrders: %v", err)
	}
	fixture.requireFrame("ACCOUNT", "spot-listen-key")
	fixture.requireFrame("ACCOUNT_WRITE_ATTEMPT", "spot-listen-key")
	fixture.requireFrame("ACCOUNT_WRITE", "spot-listen-key")
	requireSubscriptionEventOrError(t, orders, "Aster spot order")
	_ = orders.Close()
	fills, err := ws.WatchFills(ctx, exchange.WatchRequest{Instrument: "ASTER-USDT", Options: exchange.WatchOptions{Buffer: 8}})
	if err != nil {
		t.Fatalf("Aster spot WatchFills: %v", err)
	}
	requireSubscriptionEvent(t, fills.Events(), "Aster spot fill")
	_ = fills.Close()
	balances, err := ws.WatchBalances(ctx, exchange.WatchAccountRequest{Options: exchange.WatchOptions{Buffer: 8}})
	if err != nil {
		t.Fatalf("Aster spot WatchBalances: %v", err)
	}
	requireSubscriptionEvent(t, balances.Events(), "Aster spot balance")
	_ = balances.Close()

	ack, err := ws.PlaceOrder(ctx, exchange.PlaceOrderRequest{Instrument: "BTC-USDT", ClientOrderID: "101", Side: exchange.SideBuy, Type: exchange.OrderTypeLimit, Quantity: decimal.NewFromInt(1), LimitPrice: decimal.NewFromInt(99), LimitPolicy: exchange.LimitPolicyResting})
	if err != nil || ack.OrderID == "" {
		t.Fatalf("Aster spot ws PlaceOrder REST bridge: ack=%+v err=%v", ack, err)
	}
}

func exerciseAsterPerpWebSocketFixture(t *testing.T, ws exchange.PerpWebSocket, fixture *asterWSFixture) {
	t.Helper()
	defer ws.Close()
	ctx := context.Background()

	book, err := ws.WatchOrderBook(ctx, exchange.WatchRequest{Instrument: "ASTER-USDT", Options: exchange.WatchOptions{Buffer: 8}})
	if err != nil {
		t.Fatalf("Aster perp WatchOrderBook: %v", err)
	}
	fixture.requireFrame("SUBSCRIBE", "asterusdt@depth")
	requireSubscriptionEvent(t, book.Events(), "Aster perp book")
	_ = book.Close()

	bbo, err := ws.WatchBBO(ctx, exchange.WatchRequest{Instrument: "ASTER-USDT", Options: exchange.WatchOptions{Buffer: 8}})
	if err != nil {
		t.Fatalf("Aster perp WatchBBO: %v", err)
	}
	fixture.requireFrame("SUBSCRIBE", "asterusdt@bookTicker")
	requireSubscriptionEvent(t, bbo.Events(), "Aster perp bbo")
	_ = bbo.Close()

	trades, err := ws.WatchPublicTrades(ctx, exchange.WatchRequest{Instrument: "ASTER-USDT", Options: exchange.WatchOptions{Buffer: 8}})
	if err != nil {
		t.Fatalf("Aster perp WatchPublicTrades: %v", err)
	}
	fixture.requireFrame("SUBSCRIBE", "asterusdt@aggTrade")
	requireSubscriptionEvent(t, trades.Events(), "Aster perp trade")
	_ = trades.Close()

	candles, err := ws.WatchCandles(ctx, exchange.WatchCandlesRequest{Instrument: "ASTER-USDT", Interval: "1m", Options: exchange.WatchOptions{Buffer: 8}})
	if err != nil {
		t.Fatalf("Aster perp WatchCandles: %v", err)
	}
	fixture.requireFrame("SUBSCRIBE", "asterusdt@kline_1m")
	requireSubscriptionEvent(t, candles.Events(), "Aster perp candle")
	_ = candles.Close()

	orders, err := ws.WatchOrders(ctx, exchange.WatchRequest{Instrument: "ASTER-USDT", Options: exchange.WatchOptions{Buffer: 8}})
	if err != nil {
		t.Fatalf("Aster perp WatchOrders: %v", err)
	}
	requireSubscriptionEvent(t, orders.Events(), "Aster perp order")
	_ = orders.Close()
	fills, err := ws.WatchFills(ctx, exchange.WatchRequest{Instrument: "ASTER-USDT", Options: exchange.WatchOptions{Buffer: 8}})
	if err != nil {
		t.Fatalf("Aster perp WatchFills: %v", err)
	}
	requireSubscriptionEvent(t, fills.Events(), "Aster perp fill")
	_ = fills.Close()
	balances, err := ws.WatchBalances(ctx, exchange.WatchAccountRequest{Options: exchange.WatchOptions{Buffer: 8}})
	if err != nil {
		t.Fatalf("Aster perp WatchBalances: %v", err)
	}
	requireSubscriptionEvent(t, balances.Events(), "Aster perp balance")
	_ = balances.Close()
	positions, err := ws.WatchPositions(ctx, exchange.WatchRequest{Instrument: "ASTER-USDT", Options: exchange.WatchOptions{Buffer: 8}})
	if err != nil {
		t.Fatalf("Aster perp WatchPositions: %v", err)
	}
	requireSubscriptionEvent(t, positions.Events(), "Aster perp position")
	_ = positions.Close()

	marks, err := ws.WatchMarkPrice(ctx, exchange.WatchRequest{Instrument: "ASTER-USDT", Options: exchange.WatchOptions{Buffer: 8}})
	if err != nil {
		t.Fatalf("Aster perp WatchMarkPrice: %v", err)
	}
	fixture.requireFrame("SUBSCRIBE", "asterusdt@markPrice@1s")
	requireSubscriptionEvent(t, marks.Events(), "Aster perp mark")
	_ = marks.Close()
	funding, err := ws.WatchFundingRate(ctx, exchange.WatchRequest{Instrument: "ASTER-USDT", Options: exchange.WatchOptions{Buffer: 8}})
	if err != nil {
		t.Fatalf("Aster perp WatchFundingRate: %v", err)
	}
	fixture.requireFrame("SUBSCRIBE", "asterusdt@markPrice@1s")
	requireSubscriptionEvent(t, funding.Events(), "Aster perp funding")
	_ = funding.Close()

	ack, err := ws.CancelOrder(ctx, exchange.CancelOrderRequest{Instrument: "BTC-USDT", OrderID: "21"})
	if err != nil || ack.OrderID == "" {
		t.Fatalf("Aster perp ws CancelOrder REST bridge: ack=%+v err=%v", ack, err)
	}
}

func requireSubscriptionEvent[T any](t *testing.T, ch <-chan T, label string) T {
	t.Helper()
	select {
	case event := <-ch:
		return event
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
	var zero T
	return zero
}
