package perp

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

	"github.com/gorilla/websocket"

	astercommon "github.com/QuantProcessing/boltertrader/sdk/aster/common"
)

func TestPerpPrivateStreamFixturesDecodeAndRoute(t *testing.T) {
	profile, _ := astercommon.NewProfile(astercommon.EnvironmentTestnet, astercommon.ProductPerp)
	client, err := NewWsAccountClient(context.Background(), profile, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	accountUpdates := make(chan *AccountUpdateEvent, 1)
	orderUpdates := make(chan *OrderUpdateEvent, 1)
	client.SubscribeAccountUpdate(func(event *AccountUpdateEvent) { accountUpdates <- event })
	client.SubscribeOrderUpdate(func(event *OrderUpdateEvent) { orderUpdates <- event })

	client.handleMessage(readPerpFixture(t, "account_update.json"))
	client.handleMessage(readPerpFixture(t, "order_update.json"))

	select {
	case event := <-accountUpdates:
		if event.EventType != "ACCOUNT_UPDATE" || len(event.UpdateData.Positions) != 1 {
			t.Fatalf("account update = %+v", event)
		}
		position := event.UpdateData.Positions[0]
		if position.Symbol != "ASTERUSDT" || position.PositionSide != "BOTH" || position.PositionAmount != "100.00" {
			t.Fatalf("account position = %+v", position)
		}
	default:
		t.Fatal("account update fixture was not routed")
	}
	select {
	case event := <-orderUpdates:
		if event.EventType != "ORDER_TRADE_UPDATE" || event.Order.PositionSide != "BOTH" {
			t.Fatalf("order update = %+v", event)
		}
		if !event.Order.IsReduceOnly || event.Order.ClosePosition || event.Order.RealizedProfit != "5.00000000" {
			t.Fatalf("order flags/profit = %+v", event.Order)
		}
	default:
		t.Fatal("order update fixture was not routed")
	}
}

func TestPerpPublicStreamFixturesDecodeAndRoute(t *testing.T) {
	client := newTestWSMarketClient(t, context.Background())
	defer client.Close()

	depths := make(chan *WsDepthEvent, 1)
	klines := make(chan *WsKlineEvent, 1)
	marks := make(chan *WsMarkPriceEvent, 1)
	if err := client.SubscribeIncrementOrderBook("ASTERUSDT", "500ms", func(event *WsDepthEvent) error {
		depths <- event
		return nil
	}); err == nil {
		t.Fatal("disconnected depth subscription unexpectedly succeeded")
	}
	if err := client.SubscribeKline("ASTERUSDT", "1m", func(event *WsKlineEvent) error {
		klines <- event
		return nil
	}); err == nil {
		t.Fatal("disconnected kline subscription unexpectedly succeeded")
	}
	if err := client.SubscribeMarkPrice("ASTERUSDT", "1s", func(event *WsMarkPriceEvent) error {
		marks <- event
		return nil
	}); err == nil {
		t.Fatal("disconnected mark subscription unexpectedly succeeded")
	}

	client.handleMessage(readPerpFixture(t, "depth_stream.json"))
	client.handleMessage(readPerpFixture(t, "kline_stream.json"))
	client.handleMessage(readPerpFixture(t, "mark_price_stream.json"))

	select {
	case event := <-depths:
		if event.FirstUpdateID != 2027025 || event.FinalUpdateID != 2027026 || event.FinalUpdateIDLast != 2027024 {
			t.Fatalf("depth = %+v", event)
		}
	default:
		t.Fatal("500ms depth fixture was not routed")
	}
	select {
	case event := <-klines:
		if event.Kline.NumberOfTrades != 100 || event.Kline.QuoteVolume != "1249.00" ||
			event.Kline.TakerBuyBaseVolume != "500.00" || event.Kline.TakerBuyQuoteVolume != "625.00" {
			t.Fatalf("kline = %+v", event.Kline)
		}
	default:
		t.Fatal("kline fixture was not routed")
	}
	select {
	case event := <-marks:
		if event.MarkPrice != "1.2500" || event.IndexPrice != "1.2480" || event.FundingRate != "0.00010000" {
			t.Fatalf("mark price = %+v", event)
		}
	default:
		t.Fatal("mark-price fixture was not routed")
	}
}

func TestPerpDepthStreamNamesAndConflicts(t *testing.T) {
	tests := []struct {
		name   string
		call   func(*WsMarketClient) error
		stream string
	}{
		{name: "diff default", stream: "asterusdt@depth", call: func(client *WsMarketClient) error {
			return client.SubscribeIncrementOrderBook("ASTERUSDT", "", func(*WsDepthEvent) error { return nil })
		}},
		{name: "diff 500ms", stream: "asterusdt@depth@500ms", call: func(client *WsMarketClient) error {
			return client.SubscribeIncrementOrderBook("ASTERUSDT", "500ms", func(*WsDepthEvent) error { return nil })
		}},
		{name: "partial default", stream: "asterusdt@depth5", call: func(client *WsMarketClient) error {
			return client.SubscribeLimitOrderBook("ASTERUSDT", 5, "", func(*WsDepthEvent) error { return nil })
		}},
		{name: "partial 100ms", stream: "asterusdt@depth10@100ms", call: func(client *WsMarketClient) error {
			return client.SubscribeLimitOrderBook("ASTERUSDT", 10, "100ms", func(*WsDepthEvent) error { return nil })
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newTestWSMarketClient(t, context.Background())
			defer client.Close()
			if err := test.call(client); err == nil {
				t.Fatal("disconnected subscription unexpectedly succeeded")
			}
			if _, exists := client.subs[test.stream]; !exists {
				t.Fatalf("subscriptions = %+v, want %q", client.subs, test.stream)
			}
		})
	}

	client := newTestWSMarketClient(t, context.Background())
	defer client.Close()
	_ = client.SubscribeIncrementOrderBook("ASTERUSDT", "", func(*WsDepthEvent) error { return nil })
	if err := client.SubscribeLimitOrderBook("ASTERUSDT", 5, "100ms", func(*WsDepthEvent) error { return nil }); err == nil {
		t.Fatal("ambiguous second depth stream unexpectedly registered")
	}
	if len(client.subs) != 1 {
		t.Fatalf("subscriptions after conflict = %+v", client.subs)
	}
}

func TestPerpDepthSequenceRejectsDuplicateAndGap(t *testing.T) {
	var update WsDepthEvent
	if err := json.Unmarshal(readPerpFixture(t, "depth_stream.json"), &update); err != nil {
		t.Fatal(err)
	}
	sequence := NewDepthSequence(2027024)
	apply, err := sequence.Accept(update)
	if err != nil || !apply || sequence.LastUpdateID() != 2027026 {
		t.Fatalf("first update apply=%t last=%d err=%v", apply, sequence.LastUpdateID(), err)
	}
	apply, err = sequence.Accept(update)
	if err != nil || apply {
		t.Fatalf("duplicate update apply=%t err=%v", apply, err)
	}
	var gap WsDepthEvent
	if err := json.Unmarshal(readPerpFixture(t, "depth_stream_gap.json"), &gap); err != nil {
		t.Fatal(err)
	}
	apply, err = sequence.Accept(gap)
	var gapErr *DepthSequenceGapError
	if apply || !errors.As(err, &gapErr) || gapErr.Expected() != 2027027 || sequence.LastUpdateID() != 2027026 {
		t.Fatalf("gap apply=%t last=%d err=%T %v", apply, sequence.LastUpdateID(), err, err)
	}
}

func TestPerpWsDuplicateSubscribeSendsOnceAndReconnectResubscribesOnce(t *testing.T) {
	t.Setenv("PROXY", "http://127.0.0.1:1")
	var mu sync.Mutex
	subscribeByConnection := make(map[int]int)
	server := newPerpWSServer(t, func(connection int, conn *websocket.Conn) {
		defer conn.Close()
		var request struct {
			Method string   `json:"method"`
			Params []string `json:"params"`
		}
		if err := conn.ReadJSON(&request); err != nil {
			return
		}
		if request.Method != "SUBSCRIBE" || len(request.Params) != 1 || request.Params[0] != "asterusdt@bookTicker" {
			t.Errorf("subscription request = %+v", request)
			return
		}
		mu.Lock()
		subscribeByConnection[connection]++
		mu.Unlock()
		payload := fmt.Sprintf(`{"u":%d,"s":"ASTERUSDT","b":"1.2499","B":"150","a":"1.2501","A":"125"}`, 50000+connection)
		if err := conn.WriteMessage(websocket.TextMessage, []byte(payload)); err != nil {
			t.Errorf("write fixture: %v", err)
			return
		}
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
	client := newTestPerpMarketClientWithURL(ctx, websocketURL(server.URL))
	client.ReconnectWait = 10 * time.Millisecond
	if err := client.Connect(); err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	received := make(chan int64, 4)
	handler := func(event *WsBookTickerEvent) error {
		received <- event.UpdateID
		return nil
	}
	if err := client.SubscribeBookTicker("ASTERUSDT", handler); err != nil {
		t.Fatal(err)
	}
	if err := client.SubscribeBookTicker(" asterusdt ", handler); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []int64{50001, 50002} {
		select {
		case got := <-received:
			if got != expected {
				t.Fatalf("update id = %d, want %d", got, expected)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out waiting for update %d", expected)
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

func TestPerpUserStreamManagerRenewsAfterKeepAliveFailureAndStopsOnce(t *testing.T) {
	var mu sync.Mutex
	postCount := 0
	putCount := 0
	deleteCount := 0
	client := newPerpFixtureClient(t, func(request *http.Request) (*http.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		switch request.Method {
		case http.MethodPost:
			postCount++
			return perpFixtureResponse(request, http.StatusOK, []byte(`{"listenKey":"key-`+fmt.Sprint(postCount)+`"}`)), nil
		case http.MethodPut:
			putCount++
			if request.URL.RawQuery == "" {
				t.Errorf("keepalive request unexpectedly unsigned")
			}
			if request.URL.Query().Get("listenKey") != "" {
				t.Errorf("keepalive included listenKey query: %s", request.URL.RawQuery)
			}
			if putCount == 1 {
				return perpFixtureResponse(request, http.StatusBadRequest, []byte(`{"code":-1125,"msg":"Invalid listen key."}`)), nil
			}
			return perpFixtureResponse(request, http.StatusOK, []byte(`{}`)), nil
		case http.MethodDelete:
			deleteCount++
			if request.URL.Query().Get("listenKey") != "" {
				t.Errorf("delete included listenKey query: %s", request.URL.RawQuery)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{}`)),
				Request:    request,
			}, nil
		default:
			t.Fatalf("unexpected method %s", request.Method)
			return nil, nil
		}
	})

	manager := NewPerpUserStreamManager(client)
	manager.KeepAliveInt = 10 * time.Millisecond
	renewed := make(chan string, 1)
	manager.SetRenewHandler(func(key string) { renewed <- key })

	key, err := manager.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if key != "key-1" {
		t.Fatalf("initial listen key = %q", key)
	}
	select {
	case key = <-renewed:
		if key != "key-2" || manager.ListenKey() != key {
			t.Fatalf("renewed key = %q manager=%q", key, manager.ListenKey())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("listen key was not renewed")
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	if err := manager.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if postCount != 2 || putCount < 1 || deleteCount != 1 {
		t.Fatalf("POST=%d PUT=%d DELETE=%d", postCount, putCount, deleteCount)
	}
}

func TestPerpWsAccountClientReconnectsOnListenKeyExpiredAndDeletesFinalKey(t *testing.T) {
	var wsConnections atomic.Int64
	paths := make(chan string, 2)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	wsServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		conn, err := upgrader.Upgrade(writer, request, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer conn.Close()
		connection := wsConnections.Add(1)
		paths <- request.URL.Path
		if connection == 1 {
			if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"e":"listenKeyExpired","E":1783641600100}`)); err != nil {
				t.Errorf("write listenKeyExpired: %v", err)
			}
			return
		}
		var payload map[string]any
		if err := json.Unmarshal(readPerpFixture(t, "account_update.json"), &payload); err != nil {
			t.Errorf("decode account fixture: %v", err)
			return
		}
		payload["E"] = float64(1783641600200 + connection)
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Errorf("encode account fixture: %v", err)
			return
		}
		if err := conn.WriteMessage(websocket.TextMessage, encoded); err != nil {
			t.Errorf("write account fixture: %v", err)
			return
		}
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer wsServer.Close()

	var restMu sync.Mutex
	postCount := 0
	deleteCount := 0
	profile, _ := astercommon.NewProfile(astercommon.EnvironmentTestnet, astercommon.ProductPerp)
	security := newTestSecurity(t)
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		restMu.Lock()
		defer restMu.Unlock()
		switch request.Method {
		case http.MethodPost:
			postCount++
			return perpFixtureResponse(request, http.StatusOK, []byte(`{"listenKey":"key-`+fmt.Sprint(postCount)+`"}`)), nil
		case http.MethodPut:
			return perpFixtureResponse(request, http.StatusOK, []byte(`{}`)), nil
		case http.MethodDelete:
			deleteCount++
			if request.URL.Query().Get("listenKey") != "" {
				t.Errorf("delete included listenKey query: %s", request.URL.RawQuery)
			}
			return perpFixtureResponse(request, http.StatusOK, []byte(`{}`)), nil
		default:
			return nil, fmt.Errorf("unexpected method %s", request.Method)
		}
	})}
	client, err := NewWsAccountClient(context.Background(), profile, security)
	if err != nil {
		t.Fatal(err)
	}
	client.Client.WithHTTPClient(httpClient)
	client.StreamMgr.Client = client.Client
	client.StreamMgr.KeepAliveInt = time.Hour
	client.ReconnectWait = 10 * time.Millisecond
	client.userStreamURL = func(key string) string {
		return websocketURL(wsServer.URL) + "/" + key
	}

	received := make(chan int64, 1)
	client.SubscribeAccountUpdate(func(event *AccountUpdateEvent) { received <- event.EventTime })
	if err := client.Connect(); err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	select {
	case got := <-received:
		if got != 1783641600202 {
			t.Fatalf("event time = %d", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for reconnected account event")
	}
	client.Close()
	first := <-paths
	second := <-paths
	if first != "/ws/key-1" || second != "/ws/key-2" {
		t.Fatalf("user stream paths = %q, %q", first, second)
	}
	restMu.Lock()
	defer restMu.Unlock()
	if postCount != 2 || deleteCount != 1 {
		t.Fatalf("POST=%d DELETE=%d", postCount, deleteCount)
	}
}

func newTestPerpMarketClientWithURL(ctx context.Context, rawURL string) *WsMarketClient {
	profile, _ := astercommon.NewProfile(astercommon.EnvironmentTestnet, astercommon.ProductPerp)
	transport := newWSClient(ctx, rawURL)
	client := &WsMarketClient{WsClient: transport, profile: profile}
	transport.Handler = client.handleMessage
	return client
}

func newPerpWSServer(t *testing.T, handler func(connection int, conn *websocket.Conn)) *httptest.Server {
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
