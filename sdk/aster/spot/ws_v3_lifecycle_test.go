package spot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	astercommon "github.com/QuantProcessing/boltertrader/sdk/aster/common"
)

func TestDepthSequenceAcceptsNextUpdateAndRejectsDuplicateOrGap(t *testing.T) {
	var update WsDepthEvent
	if err := json.Unmarshal(readSpotFixture(t, "depth_stream.json"), &update); err != nil {
		t.Fatal(err)
	}
	sequence := NewDepthSequence(1027024)

	apply, err := sequence.Accept(update)
	if err != nil || !apply {
		t.Fatalf("first update apply=%t err=%v", apply, err)
	}
	if sequence.LastUpdateID() != 1027026 {
		t.Fatalf("last update id = %d", sequence.LastUpdateID())
	}

	apply, err = sequence.Accept(update)
	if err != nil || apply {
		t.Fatalf("duplicate update apply=%t err=%v", apply, err)
	}

	outOfOrder := update
	outOfOrder.FirstUpdateID = 1027025
	outOfOrder.FinalUpdateID = 1027025
	apply, err = sequence.Accept(outOfOrder)
	if err != nil || apply {
		t.Fatalf("out-of-order update apply=%t err=%v", apply, err)
	}

	var gap WsDepthEvent
	if err := json.Unmarshal(readSpotFixture(t, "depth_stream_gap.json"), &gap); err != nil {
		t.Fatal(err)
	}
	apply, err = sequence.Accept(gap)
	if apply {
		t.Fatal("gap update was accepted")
	}
	gapErr, ok := err.(*DepthSequenceGapError)
	if !ok || gapErr.Expected() != 1027027 || gapErr.FirstUpdateID() != 1027030 {
		t.Fatalf("gap error = %T %+v", err, err)
	}
	if sequence.LastUpdateID() != 1027026 {
		t.Fatalf("gap changed last update id to %d", sequence.LastUpdateID())
	}
}

func TestWsMarketRoutesAggregateTradeFixture(t *testing.T) {
	client := newTestWSMarketClient(t, context.Background())
	defer client.Close()
	received := make(chan *AggTradeEvent, 1)
	err := client.SubscribeAggTrade("ASTERUSDT", func(event *AggTradeEvent) error {
		received <- event
		return nil
	})
	if err == nil {
		t.Fatal("subscription without a connection unexpectedly succeeded")
	}
	subscription, ok := client.subs["asterusdt@aggTrade"]
	if !ok || subscription.callback == nil {
		t.Fatalf("aggregate trade subscription = %+v present=%t", subscription, ok)
	}
	payload := readSpotFixture(t, "agg_trade_stream.json")
	client.handleMessage(payload)
	select {
	case event := <-received:
		if event.AggTradeID != 30001 || event.Price != "1.2500" || event.Quantity != "4.00" {
			t.Fatalf("aggregate trade = %+v", event)
		}
	default:
		t.Fatal("aggregate trade fixture was not routed")
	}
}

func TestWsMarketDuplicateSubscribeSendsOnceAndRoutesBookTicker(t *testing.T) {
	var subscribeCount atomic.Int64
	server := newSpotWSServer(t, func(connection int, conn *websocket.Conn) {
		defer conn.Close()
		for {
			var request struct {
				Method string   `json:"method"`
				Params []string `json:"params"`
			}
			if err := conn.ReadJSON(&request); err != nil {
				return
			}
			if request.Method != "SUBSCRIBE" || len(request.Params) != 1 || request.Params[0] != "asterusdt@bookTicker" {
				t.Errorf("subscription request = %+v", request)
				continue
			}
			if subscribeCount.Add(1) == 1 {
				if err := conn.WriteMessage(websocket.TextMessage, readSpotFixture(t, "book_ticker_stream.json")); err != nil {
					t.Errorf("write fixture: %v", err)
					return
				}
			}
		}
	})
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := newTestWsMarketClientWithURL(ctx, websocketURL(server.URL))
	if err := client.Connect(); err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	received := make(chan *BookTickerEvent, 2)
	handler := func(event *BookTickerEvent) error {
		received <- event
		return nil
	}
	if err := client.SubscribeBookTicker("ASTERUSDT", handler); err != nil {
		t.Fatal(err)
	}
	if err := client.SubscribeBookTicker(" asterusdt ", handler); err != nil {
		t.Fatal(err)
	}

	select {
	case event := <-received:
		if event.Symbol != "ASTERUSDT" || event.UpdateID != 40001 {
			t.Fatalf("event = %+v", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("book ticker callback was not routed")
	}
	time.Sleep(100 * time.Millisecond)
	if subscribeCount.Load() != 1 {
		t.Fatalf("SUBSCRIBE messages = %d, want 1", subscribeCount.Load())
	}
	select {
	case event := <-received:
		t.Fatalf("duplicate callback = %+v", event)
	default:
	}
}

func TestWsMarketReconnectResubscribesExactlyOnce(t *testing.T) {
	t.Setenv("PROXY", "http://127.0.0.1:1")
	var connectionCount atomic.Int64
	var mu sync.Mutex
	subscribeByConnection := make(map[int]int)
	server := newSpotWSServer(t, func(connection int, conn *websocket.Conn) {
		defer conn.Close()
		var request struct {
			Method string   `json:"method"`
			Params []string `json:"params"`
		}
		if err := conn.ReadJSON(&request); err != nil {
			return
		}
		mu.Lock()
		subscribeByConnection[connection]++
		mu.Unlock()
		updateID := 40000 + connection
		payload := fmt.Sprintf(`{"u":%d,"s":"ASTERUSDT","b":"1.2499","B":"150","a":"1.2501","A":"125"}`, updateID)
		if err := conn.WriteMessage(websocket.TextMessage, []byte(payload)); err != nil {
			t.Errorf("write fixture: %v", err)
			return
		}
		connectionCount.Store(int64(connection))
		if connection == 1 {
			return
		}
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := newTestWsMarketClientWithURL(ctx, websocketURL(server.URL))
	client.ReconnectWait = 10 * time.Millisecond
	if err := client.Connect(); err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	received := make(chan int64, 4)
	if err := client.SubscribeBookTicker("ASTERUSDT", func(event *BookTickerEvent) error {
		received <- event.UpdateID
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	want := []int64{40001, 40002}
	for _, expected := range want {
		select {
		case got := <-received:
			if got != expected {
				t.Fatalf("update id = %d, want %d", got, expected)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out waiting for update %d; connections=%d", expected, connectionCount.Load())
		}
	}
	mu.Lock()
	first := subscribeByConnection[1]
	second := subscribeByConnection[2]
	mu.Unlock()
	if first != 1 || second != 1 {
		t.Fatalf("subscribe counts = first:%d second:%d", first, second)
	}
}

func newTestWsMarketClientWithURL(ctx context.Context, rawURL string) *WsMarketClient {
	profile, _ := astercommon.NewProfile(astercommon.EnvironmentTestnet, astercommon.ProductSpot)
	transport := newWSClient(ctx, rawURL)
	client := &WsMarketClient{WsClient: transport, profile: profile}
	transport.Handler = client.handleMessage
	return client
}

func newSpotWSServer(t *testing.T, handler func(connection int, conn *websocket.Conn)) *httptest.Server {
	t.Helper()
	var connections atomic.Int64
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		conn, err := upgrader.Upgrade(writer, request, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		handler(int(connections.Add(1)), conn)
	}))
}

func websocketURL(httpURL string) string {
	return "ws" + strings.TrimPrefix(httpURL, "http") + "/ws"
}
