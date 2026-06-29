package spot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func newSpotWSServer(t *testing.T, handler func(*websocket.Conn, *http.Request)) string {
	t.Helper()
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		handler(conn, r)
	}))
	t.Cleanup(server.Close)
	return "ws" + strings.TrimPrefix(server.URL, "http")
}

func TestWsMarketClient_ConnectUsesCombinedStreamURLForQueuedStreams(t *testing.T) {
	requests := make(chan string, 1)
	wsURL := newSpotWSServer(t, func(conn *websocket.Conn, r *http.Request) {
		defer conn.Close()
		requests <- r.URL.RequestURI()
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"stream":"btcusdt@bookTicker","data":{"u":1,"s":"BTCUSDT","b":"1","B":"2","a":"3","A":"4"}}`))
		time.Sleep(50 * time.Millisecond)
	})

	client := NewWsMarketClient(context.Background())
	client.WsClient.URL = wsURL + "/ws"
	defer client.Close()

	done := make(chan struct{}, 1)
	if err := client.SubscribeBookTicker("BTCUSDT", func(event *BookTickerEvent) error {
		if event.Symbol != "BTCUSDT" {
			t.Errorf("unexpected symbol: %s", event.Symbol)
		}
		done <- struct{}{}
		return nil
	}); err != nil {
		t.Fatalf("SubscribeBookTicker before Connect: %v", err)
	}
	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	select {
	case uri := <-requests:
		if uri != "/stream?streams=btcusdt@bookTicker" {
			t.Fatalf("expected combined stream URL, got %s", uri)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for websocket request")
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for bookTicker callback")
	}
}

func TestWsMarketClient_CombinedWrapperDispatchesExactSpotStreamWithoutEventType(t *testing.T) {
	client := NewWsMarketClient(context.Background())
	var got *DepthEvent
	if err := client.SubscribeLimitOrderBook("BTCUSDT", 5, "100ms", func(event *DepthEvent) error {
		got = event
		return nil
	}); err != nil {
		t.Fatalf("SubscribeLimitOrderBook: %v", err)
	}

	client.handleMessage([]byte(`{"stream":"btcusdt@depth5@100ms","data":{"lastUpdateId":123,"bids":[["100.1","1.5"]],"asks":[["100.2","2.5"]]}}`))

	if got == nil {
		t.Fatal("expected partial depth callback")
	}
	if got.Symbol != "BTCUSDT" || got.FinalUpdateID != 123 {
		t.Fatalf("unexpected partial depth event: %+v", got)
	}
}

func TestWsMarketClient_SplitsSpotStreamsByCapacity(t *testing.T) {
	client := NewWsMarketClient(context.Background())
	client.setMaxSubscriptionsPerClientForTest(2)

	for _, symbol := range []string{"BTCUSDT", "ETHUSDT", "SOLUSDT"} {
		if err := client.SubscribeBookTicker(symbol, func(*BookTickerEvent) error { return nil }); err != nil {
			t.Fatalf("SubscribeBookTicker(%s): %v", symbol, err)
		}
	}
	if got := client.manager.clientCount(); got != 2 {
		t.Fatalf("expected streams split over 2 clients, got %d", got)
	}
}

func TestWsMarketClient_ConcurrentDuplicateSpotSubscribeIsIdempotent(t *testing.T) {
	client := NewWsMarketClient(context.Background())
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := client.SubscribeBookTicker("BTCUSDT", func(*BookTickerEvent) error { return nil }); err != nil {
				t.Errorf("SubscribeBookTicker: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := client.manager.streamCount(); got != 1 {
		t.Fatalf("expected one desired stream, got %d", got)
	}
}

func TestWsMarketClient_DuplicateConnectedSpotSubscribePreservesReplayID(t *testing.T) {
	ready := make(chan struct{}, 1)
	wsURL := newSpotWSServer(t, func(conn *websocket.Conn, _ *http.Request) {
		defer conn.Close()
		ready <- struct{}{}
		time.Sleep(300 * time.Millisecond)
	})

	client := NewWsMarketClient(context.Background())
	client.WsClient.URL = wsURL + "/ws"
	defer client.Close()

	if err := client.SubscribeAggTrade("BTCUSDT", func(*AggTradeEvent) error { return nil }); err != nil {
		t.Fatalf("SubscribeAggTrade: %v", err)
	}
	if err := client.SubscribeKline("BTCUSDT", "1m", func(*KlineEvent) error { return nil }); err != nil {
		t.Fatalf("SubscribeKline: %v", err)
	}
	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for websocket connection")
	}

	client.WsClient.Mu.RLock()
	before := client.WsClient.subs["btcusdt@kline_1m"].id
	client.WsClient.Mu.RUnlock()
	if before == 0 {
		t.Fatal("expected additional stream to have replay subscription id")
	}

	if err := client.SubscribeKline("BTCUSDT", "1m", func(*KlineEvent) error { return nil }); err != nil {
		t.Fatalf("duplicate SubscribeKline: %v", err)
	}

	client.WsClient.Mu.RLock()
	after := client.WsClient.subs["btcusdt@kline_1m"].id
	client.WsClient.Mu.RUnlock()
	if after != before {
		t.Fatalf("expected duplicate subscribe to preserve replay id %d, got %d", before, after)
	}
}
