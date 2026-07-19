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
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/gorilla/websocket"
	"github.com/shopspring/decimal"
)

func TestNadoWebSocketFixtureFileCoversEveryMethod(t *testing.T) {
	TestNadoWebSocketExercisesEveryExposedMethod(t)
}

func TestNadoWebSocketExercisesEveryExposedMethod(t *testing.T) {
	fixture := newNadoWSFixture(t)
	settings := Settings{Environment: "testnet", Endpoint: fixture.restURL + "/v1", WebSocketEndpoint: fixture.wsURL}

	spot := NewNadoSpot(testAsterPrivateKey, "default", settings).WebSocket()
	exerciseNadoSpotWebSocketFixture(t, spot, fixture, "ETH-USDT0", int64(1))
	_ = spot.Close()

	perp := NewNadoUSDT0Perp(testAsterPrivateKey, "default", settings).WebSocket()
	exerciseNadoPerpWebSocketFixture(t, perp, fixture)
}

func TestNadoClientCachesAndClosesWebSocketFacet(t *testing.T) {
	client := NewNadoUSDT0Perp(testAsterPrivateKey, "default", Settings{Environment: "testnet"})
	if client.WebSocket() != client.WebSocket() {
		t.Fatal("Nado WebSocket returned different facets")
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
			t.Fatalf("Nado formatting leaked %q: %s", secret, rendered)
		}
	}
}

type nadoWSFixture struct {
	t        *testing.T
	restURL  string
	wsURL    string
	frames   chan nadoWSFrame
	writeMu  sync.Mutex
	upgrader websocket.Upgrader
	server   *httptest.Server
}

type nadoWSFrame struct {
	Method string
	Stream string
	ID     int64
}

func newNadoWSFixture(t *testing.T) *nadoWSFixture {
	t.Helper()
	fixture := &nadoWSFixture{t: t, frames: make(chan nadoWSFrame, 128)}
	rest := newNadoOpenAPIRouter(t)
	fixture.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if websocket.IsWebSocketUpgrade(r) {
			fixture.serveWS(w, r)
			return
		}
		resp, err := rest.RoundTrip(r)
		if err != nil {
			t.Errorf("Nado REST fixture: %v", err)
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

func (fixture *nadoWSFixture) serveWS(w http.ResponseWriter, r *http.Request) {
	conn, err := fixture.upgrader.Upgrade(w, r, nil)
	if err != nil {
		fixture.t.Errorf("Nado WS upgrade: %v", err)
		return
	}
	defer conn.Close()
	for {
		var raw map[string]json.RawMessage
		if err := conn.ReadJSON(&raw); err != nil {
			return
		}
		if fixture.handleNadoControl(conn, raw) {
			continue
		}
		fixture.handleNadoGateway(conn, raw)
	}
}

func (fixture *nadoWSFixture) handleNadoControl(conn *websocket.Conn, raw map[string]json.RawMessage) bool {
	var method string
	if err := json.Unmarshal(raw["method"], &method); err != nil || method == "" {
		return false
	}
	var id int64
	_ = json.Unmarshal(raw["id"], &id)
	stream := struct {
		Type      string `json:"type"`
		ProductID *int64 `json:"product_id"`
	}{}
	_ = json.Unmarshal(raw["stream"], &stream)
	fixture.frames <- nadoWSFrame{Method: method, Stream: nadoStreamKey(stream.Type, stream.ProductID), ID: id}
	if method == "authenticate" {
		_ = fixture.writeJSON(conn, map[string]any{"id": id, "status": "success"})
		return true
	}
	if method == "subscribe" {
		if payload := nadoWSPayload(stream.Type, stream.ProductID); payload != "" {
			_ = fixture.writeJSON(conn, map[string]any{"id": id, "status": "success"})
			go fixture.writeJSONPayloadBurst(conn, nadoStreamKey(stream.Type, stream.ProductID), []byte(payload))
		}
		return true
	}
	_ = fixture.writeJSON(conn, map[string]any{"id": id, "status": "success"})
	return true
}

func (fixture *nadoWSFixture) writeJSON(conn *websocket.Conn, value any) error {
	fixture.writeMu.Lock()
	defer fixture.writeMu.Unlock()
	return conn.WriteJSON(value)
}

func (fixture *nadoWSFixture) writeJSONPayloadBurst(conn *websocket.Conn, stream string, payload []byte) {
	var object map[string]any
	if err := json.Unmarshal(payload, &object); err != nil {
		fixture.t.Errorf("invalid Nado WS payload: %v", err)
		return
	}
	for range 5 {
		time.Sleep(20 * time.Millisecond)
		if err := fixture.writeJSON(conn, object); err != nil {
			fixture.frames <- nadoWSFrame{Method: "write_error", Stream: stream + ": " + err.Error()}
			return
		}
		fixture.frames <- nadoWSFrame{Method: "write", Stream: stream}
	}
}

func (fixture *nadoWSFixture) handleNadoGateway(conn *websocket.Conn, raw map[string]json.RawMessage) {
	const digest = "0x1111111111111111111111111111111111111111111111111111111111111111"
	if payload, ok := raw["place_order"]; ok {
		id := nadoGatewayID(payload)
		fixture.frames <- nadoWSFrame{Method: "place_order", ID: id}
		_ = fixture.writeJSON(conn, map[string]any{"id": id, "status": "success", "request_type": "place_order", "data": map[string]any{"digest": digest}})
		return
	}
	if payload, ok := raw["cancel_orders"]; ok {
		id := nadoGatewayID(payload)
		fixture.frames <- nadoWSFrame{Method: "cancel_orders", ID: id}
		_ = fixture.writeJSON(conn, map[string]any{"id": id, "status": "success", "request_type": "cancel_orders", "data": map[string]any{"cancelled_orders": []map[string]any{{"digest": digest}}}})
		return
	}
	fixture.t.Errorf("unexpected Nado gateway frame: %s", raw)
}

func nadoGatewayID(payload json.RawMessage) int64 {
	var req struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(payload, &req)
	return req.ID
}

func nadoStreamKey(streamType string, productID *int64) string {
	if productID == nil {
		return streamType
	}
	return fmt.Sprintf("%s:%d", streamType, *productID)
}

func nadoWSPayload(streamType string, productID *int64) string {
	pid := int64(1)
	if productID != nil {
		pid = *productID
	}
	switch streamType {
	case "book_depth":
		return fmt.Sprintf(`{"type":"book_depth","product_id":%d,"min_timestamp":"1783641599","max_timestamp":"1783641600","last_max_timestamp":"1783641598","bids":[["99000000000000000000","1000000000000000000"]],"asks":[["101000000000000000000","2000000000000000000"]]}`, pid)
	case "best_bid_offer":
		return fmt.Sprintf(`{"type":"best_bid_offer","timestamp":"1783641600","product_id":%d,"bid_price":"99000000000000000000","bid_qty":"1000000000000000000","ask_price":"101000000000000000000","ask_qty":"2000000000000000000"}`, pid)
	case "trade":
		return fmt.Sprintf(`{"type":"trade","timestamp":"1783641600","product_id":%d,"price":"100000000000000000000","taker_qty":"1000000000000000000","maker_qty":"-1000000000000000000","is_taker_buyer":true}`, pid)
	case "latest_candlestick":
		return fmt.Sprintf(`{"type":"latest_candlestick","timestamp":"1783641600","product_id":%d,"granularity":60,"open_x18":"99000000000000000000","high_x18":"101000000000000000000","low_x18":"98000000000000000000","close_x18":"100000000000000000000","volume":"1000000000000000000"}`, pid)
	case "order_update":
		return fmt.Sprintf(`{"type":"order_update","timestamp":"1783641600","product_id":%d,"digest":"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","amount":"1000000000000000000","reason":"placed"}`, pid)
	case "fill":
		return fmt.Sprintf(`{"type":"fill","timestamp":"1783641600","product_id":%d,"subaccount":"default","order_digest":"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","filled_qty":"1000000000000000000","remaining_qty":"0","original_qty":"1000000000000000000","price":"100000000000000000000","is_taker":true,"is_bid":true,"fee":"1000000000000000","submission_idx":"42","appendix":"1"}`, pid)
	case "position_change":
		return fmt.Sprintf(`{"type":"position_change","timestamp":"1783641600","product_id":%d,"subaccount":"default","amount":"1000000000000000000","v_quote_amount":"-99000000000000000000","reason":"match_orders","isolated":false}`, pid)
	case "funding_rate":
		return fmt.Sprintf(`{"type":"funding_rate","timestamp":"1783641600","product_id":%d,"funding_rate_x18":"100000000000000","update_time":"1783641600"}`, pid)
	default:
		return ""
	}
}

func (fixture *nadoWSFixture) requireFrame(method, stream string) {
	fixture.t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case frame := <-fixture.frames:
			if frame.Method == "write_error" && (stream == "" || strings.HasPrefix(frame.Stream, stream)) {
				fixture.t.Fatalf("Nado fixture failed to write %s", frame.Stream)
			}
			if frame.Method == method && (stream == "" || frame.Stream == stream) {
				return
			}
		case <-deadline:
			fixture.t.Fatalf("timed out waiting for Nado %s %s", method, stream)
		}
	}
}

func exerciseNadoSpotWebSocketFixture(t *testing.T, ws exchange.SpotWebSocket, fixture *nadoWSFixture, instrument string, productID int64) {
	t.Helper()
	ctx := context.Background()

	book, err := ws.WatchOrderBook(ctx, exchange.WatchRequest{Instrument: instrument, Options: exchange.WatchOptions{Buffer: 8}})
	if err != nil {
		t.Fatalf("Nado spot WatchOrderBook: %v", err)
	}
	fixture.requireFrame("subscribe", fmt.Sprintf("book_depth:%d", productID))
	fixture.requireFrame("write", fmt.Sprintf("book_depth:%d", productID))
	requireSubscriptionEventOrError(t, book, "Nado spot book")
	_ = book.Close()
	fixture.requireFrame("unsubscribe", fmt.Sprintf("book_depth:%d", productID))

	bbo, err := ws.WatchBBO(ctx, exchange.WatchRequest{Instrument: instrument, Options: exchange.WatchOptions{Buffer: 8}})
	if err != nil {
		t.Fatalf("Nado spot WatchBBO: %v", err)
	}
	fixture.requireFrame("subscribe", fmt.Sprintf("best_bid_offer:%d", productID))
	requireSubscriptionEvent(t, bbo.Events(), "Nado spot bbo")
	_ = bbo.Close()
	fixture.requireFrame("unsubscribe", fmt.Sprintf("best_bid_offer:%d", productID))

	trades, err := ws.WatchPublicTrades(ctx, exchange.WatchRequest{Instrument: instrument, Options: exchange.WatchOptions{Buffer: 8}})
	if err != nil {
		t.Fatalf("Nado spot WatchPublicTrades: %v", err)
	}
	fixture.requireFrame("subscribe", fmt.Sprintf("trade:%d", productID))
	requireSubscriptionEvent(t, trades.Events(), "Nado spot trade")
	_ = trades.Close()
	fixture.requireFrame("unsubscribe", fmt.Sprintf("trade:%d", productID))

	candles, err := ws.WatchCandles(ctx, exchange.WatchCandlesRequest{Instrument: instrument, Interval: "1m", Options: exchange.WatchOptions{Buffer: 8}})
	if err != nil {
		t.Fatalf("Nado spot WatchCandles: %v", err)
	}
	fixture.requireFrame("subscribe", fmt.Sprintf("latest_candlestick:%d", productID))
	requireSubscriptionEvent(t, candles.Events(), "Nado spot candle")
	_ = candles.Close()
	fixture.requireFrame("unsubscribe", fmt.Sprintf("latest_candlestick:%d", productID))

	orders, err := ws.WatchOrders(ctx, exchange.WatchRequest{Instrument: instrument, Options: exchange.WatchOptions{Buffer: 8}})
	if err != nil {
		t.Fatalf("Nado spot WatchOrders: %v", err)
	}
	fixture.requireFrame("authenticate", "")
	fixture.requireFrame("subscribe", fmt.Sprintf("order_update:%d", productID))
	requireSubscriptionEvent(t, orders.Events(), "Nado spot order")
	_ = orders.Close()
	fixture.requireFrame("unsubscribe", fmt.Sprintf("order_update:%d", productID))

	fills, err := ws.WatchFills(ctx, exchange.WatchRequest{Instrument: instrument, Options: exchange.WatchOptions{Buffer: 8}})
	if err != nil {
		t.Fatalf("Nado spot WatchFills: %v", err)
	}
	fixture.requireFrame("subscribe", fmt.Sprintf("fill:%d", productID))
	requireSubscriptionEvent(t, fills.Events(), "Nado spot fill")
	_ = fills.Close()
	fixture.requireFrame("unsubscribe", fmt.Sprintf("fill:%d", productID))

	balances, err := ws.WatchBalances(ctx, exchange.WatchAccountRequest{Options: exchange.WatchOptions{Buffer: 8}})
	if err != nil {
		t.Fatalf("Nado spot WatchBalances: %v", err)
	}
	fixture.requireFrame("subscribe", "position_change")
	requireSubscriptionEventOrError(t, balances, "Nado spot balance")
	_ = balances.Close()
	fixture.requireFrame("unsubscribe", "position_change")

	ack, err := ws.PlaceOrder(ctx, exchange.PlaceOrderRequest{Instrument: instrument, ClientOrderID: "101", Side: exchange.SideBuy, Type: exchange.OrderTypeLimit, Quantity: decimal.NewFromInt(1), LimitPrice: decimal.NewFromInt(99), LimitPolicy: exchange.LimitPolicyResting})
	if err != nil || ack.OrderID == "" {
		t.Fatalf("Nado spot ws PlaceOrder: ack=%+v err=%v", ack, err)
	}
	fixture.requireFrame("place_order", "")
	ack, err = ws.CancelOrder(ctx, exchange.CancelOrderRequest{Instrument: instrument, OrderID: "0x1111111111111111111111111111111111111111111111111111111111111111"})
	if err != nil || ack.OrderID == "" {
		t.Fatalf("Nado spot ws CancelOrder: ack=%+v err=%v", ack, err)
	}
	fixture.requireFrame("cancel_orders", "")
}

func exerciseNadoPerpWebSocketFixture(t *testing.T, ws exchange.PerpWebSocket, fixture *nadoWSFixture) {
	t.Helper()
	exerciseNadoSpotWebSocketFixture(t, ws, fixture, "ETH-PERP-USDT0", 2)

	ctx := context.Background()
	positions, err := ws.WatchPositions(ctx, exchange.WatchRequest{Instrument: "ETH-PERP-USDT0", Options: exchange.WatchOptions{Buffer: 8}})
	if err != nil {
		t.Fatalf("Nado perp WatchPositions: %v", err)
	}
	fixture.requireFrame("subscribe", "position_change:2")
	requireSubscriptionEvent(t, positions.Events(), "Nado perp position")
	_ = positions.Close()
	fixture.requireFrame("unsubscribe", "position_change:2")

	funding, err := ws.WatchFundingRate(ctx, exchange.WatchRequest{Instrument: "ETH-PERP-USDT0", Options: exchange.WatchOptions{Buffer: 8}})
	if err != nil {
		t.Fatalf("Nado perp WatchFundingRate: %v", err)
	}
	fixture.requireFrame("subscribe", "funding_rate:2")
	requireSubscriptionEvent(t, funding.Events(), "Nado perp funding")
	_ = funding.Close()
	fixture.requireFrame("unsubscribe", "funding_rate:2")

	marks, err := ws.WatchMarkPrice(ctx, exchange.WatchRequest{Instrument: "ETH-PERP-USDT0", Options: exchange.WatchOptions{Buffer: 8}})
	if !errors.Is(err, exchange.ErrUnsupported) {
		t.Fatalf("Nado perp WatchMarkPrice error = %v, want ErrUnsupported", err)
	}
	if marks != nil {
		_ = marks.Close()
		t.Fatal("Nado WatchMarkPrice returned a subscription for an unsupported stream")
	}
	_ = ws.Close()
}

func requireSubscriptionEventOrError[T any](t *testing.T, sub exchange.Subscription[T], label string) T {
	t.Helper()
	select {
	case event := <-sub.Events():
		return event
	case err := <-sub.Errors():
		t.Fatalf("%s error: %v", label, err)
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
	var zero T
	return zero
}
