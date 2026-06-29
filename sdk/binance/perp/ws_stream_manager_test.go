package perp

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

func newPerpWSServer(t *testing.T, handler func(*websocket.Conn, *http.Request)) string {
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

func TestWsMarketClient_ConnectUsesCombinedStreamURLForQueuedPublicStreams(t *testing.T) {
	requests := make(chan string, 1)
	wsURL := newPerpWSServer(t, func(conn *websocket.Conn, r *http.Request) {
		defer conn.Close()
		requests <- r.URL.RequestURI()
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"stream":"btcusdt@bookTicker","data":{"e":"bookTicker","u":1,"s":"BTCUSDT","b":"1","B":"2","a":"3","A":"4"}}`))
		time.Sleep(50 * time.Millisecond)
	})

	client := NewWsMarketClient(context.Background())
	client.routeClient(binancePerpWSRoutePublic).URL = wsURL + "/public/ws"
	client.routeClient(binancePerpWSRouteMarket).URL = wsURL + "/market/ws"
	defer client.Close()

	done := make(chan struct{}, 1)
	if err := client.SubscribeBookTicker("btcusdt", func(event *WsBookTickerEvent) error {
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
		if uri != "/public/stream?streams=btcusdt@bookTicker" {
			t.Fatalf("expected combined public stream URL, got %s", uri)
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

func TestWsMarketClient_SubscribeAfterEmptyConnectOpensRouteStream(t *testing.T) {
	requests := make(chan string, 1)
	wsURL := newPerpWSServer(t, func(conn *websocket.Conn, r *http.Request) {
		defer conn.Close()
		requests <- r.URL.RequestURI()
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"stream":"btcusdt@markPrice@1s","data":{"e":"markPriceUpdate","E":7000,"s":"BTCUSDT","p":"200","i":"199","r":"0.0007","T":28800000}}`))
		time.Sleep(50 * time.Millisecond)
	})

	client := NewWsMarketClient(context.Background())
	client.routeClient(binancePerpWSRoutePublic).URL = wsURL + "/public/ws"
	client.routeClient(binancePerpWSRouteMarket).URL = wsURL + "/market/ws"
	defer client.Close()

	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	done := make(chan struct{}, 1)
	if err := client.SubscribeMarkPrice("btcusdt", "1s", func(event *WsMarkPriceEvent) error {
		if event.Symbol != "BTCUSDT" || event.FundingRate != "0.0007" {
			t.Errorf("unexpected mark price event: %+v", event)
		}
		done <- struct{}{}
		return nil
	}); err != nil {
		t.Fatalf("SubscribeMarkPrice after Connect: %v", err)
	}

	select {
	case uri := <-requests:
		if uri != "/market/stream?streams=btcusdt@markPrice@1s" {
			t.Fatalf("expected combined market stream URL, got %s", uri)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for websocket request")
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for mark price callback")
	}
}

func TestWsMarketClient_CombinedWrapperDispatchesExactStreamWithoutEventType(t *testing.T) {
	client := NewWsMarketClient(context.Background())
	var got *WsDepthEvent
	if err := client.SubscribeLimitOrderBook("btcusdt", 5, "100ms", func(event *WsDepthEvent) error {
		got = event
		return nil
	}); err != nil {
		t.Fatalf("SubscribeLimitOrderBook: %v", err)
	}

	client.handleMessage([]byte(`{"stream":"btcusdt@depth5@100ms","data":{"lastUpdateId":123,"E":1700000000000,"T":1700000000001,"bids":[["100.1","1.5"]],"asks":[["100.2","2.5"]]}}`))

	if got == nil {
		t.Fatal("expected partial depth callback")
	}
	if got.Symbol != "BTCUSDT" || got.FinalUpdateID != 123 {
		t.Fatalf("unexpected partial depth event: %+v", got)
	}
}

func TestWsMarketClient_SplitsStreamsByRouteAndCapacity(t *testing.T) {
	client := NewWsMarketClient(context.Background())
	client.setMaxSubscriptionsPerClientForTest(2)

	for _, symbol := range []string{"btcusdt", "ethusdt", "solusdt"} {
		if err := client.SubscribeBookTicker(symbol, func(*WsBookTickerEvent) error { return nil }); err != nil {
			t.Fatalf("SubscribeBookTicker(%s): %v", symbol, err)
		}
	}
	if publicClients := client.routeManager(binancePerpWSRoutePublic).clientCount(); publicClients != 2 {
		t.Fatalf("expected public streams split over 2 clients, got %d", publicClients)
	}
	if marketClients := client.routeManager(binancePerpWSRouteMarket).clientCount(); marketClients != 0 {
		t.Fatalf("expected no market clients, got %d", marketClients)
	}
}

func TestWsMarketClient_ConcurrentDuplicateSubscribeIsIdempotent(t *testing.T) {
	client := NewWsMarketClient(context.Background())
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := client.SubscribeBookTicker("btcusdt", func(*WsBookTickerEvent) error { return nil }); err != nil {
				t.Errorf("SubscribeBookTicker: %v", err)
			}
		}()
	}
	wg.Wait()

	manager := client.routeManager(binancePerpWSRoutePublic)
	if got := manager.streamCount(); got != 1 {
		t.Fatalf("expected one desired stream, got %d", got)
	}
}

func TestWsMarketClient_DuplicateConnectedSubscribePreservesReplayID(t *testing.T) {
	ready := make(chan struct{}, 1)
	wsURL := newPerpWSServer(t, func(conn *websocket.Conn, _ *http.Request) {
		defer conn.Close()
		ready <- struct{}{}
		time.Sleep(300 * time.Millisecond)
	})

	client := NewWsMarketClient(context.Background())
	client.routeClient(binancePerpWSRouteMarket).URL = wsURL + "/market/ws"
	defer client.Close()

	if err := client.SubscribeAggTrade("btcusdt", func(*WsAggTradeEvent) error { return nil }); err != nil {
		t.Fatalf("SubscribeAggTrade: %v", err)
	}
	if err := client.SubscribeKline("btcusdt", "1m", func(*WsKlineEvent) error { return nil }); err != nil {
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

	client.routeClient(binancePerpWSRouteMarket).Mu.RLock()
	before := client.routeClient(binancePerpWSRouteMarket).subs["btcusdt@kline_1m"].id
	client.routeClient(binancePerpWSRouteMarket).Mu.RUnlock()
	if before == 0 {
		t.Fatal("expected additional stream to have replay subscription id")
	}

	if err := client.SubscribeKline("btcusdt", "1m", func(*WsKlineEvent) error { return nil }); err != nil {
		t.Fatalf("duplicate SubscribeKline: %v", err)
	}

	client.routeClient(binancePerpWSRouteMarket).Mu.RLock()
	after := client.routeClient(binancePerpWSRouteMarket).subs["btcusdt@kline_1m"].id
	client.routeClient(binancePerpWSRouteMarket).Mu.RUnlock()
	if after != before {
		t.Fatalf("expected duplicate subscribe to preserve replay id %d, got %d", before, after)
	}
}
