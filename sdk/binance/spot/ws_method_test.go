package spot

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func requireWSNotConnected(t *testing.T, err error) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "not connected") && !strings.Contains(err.Error(), "not established") {
		t.Fatalf("expected websocket not connected error, got %v", err)
	}
}

func TestCancelReplaceOrderResponse_MarshalJSON(t *testing.T) {
	data, err := json.Marshal(CancelReplaceOrderResponse{
		CancelResult:     "SUCCESS",
		NewOrderStatus:   "SUCCESS",
		CancelResponse:   &CancelOrderResponse{Symbol: "BTCUSDT", OrderID: 1},
		NewOrderResponse: &OrderResponse{Symbol: "BTCUSDT", OrderID: 2},
	})
	if err != nil {
		t.Fatalf("MarshalJSON returned error: %v", err)
	}
	if !strings.Contains(string(data), `"cancelResult":"SUCCESS"`) || !strings.Contains(string(data), `"newOrderResult":"SUCCESS"`) {
		t.Fatalf("unexpected marshal output: %s", data)
	}
}

func TestWSClient_Connect(t *testing.T) {
	client := NewWSClient(context.Background(), "wss://example.test/ws")
	client.Close()

	if err := client.Connect(); err == nil || !strings.Contains(err.Error(), "client is closed") {
		t.Fatalf("expected closed client error, got %v", err)
	}
}

func TestWSClient_WriteJSON(t *testing.T) {
	err := NewWSClient(context.Background(), "wss://example.test/ws").WriteJSON(map[string]string{"op": "ping"})
	requireWSNotConnected(t, err)
}

func TestWSClient_Close(t *testing.T) {
	client := NewWSClient(context.Background(), "wss://example.test/ws")
	client.Close()
	if !client.isClosed {
		t.Fatal("expected client to be marked closed")
	}
}

func TestWSClient_Subscribe(t *testing.T) {
	client := NewWSClient(context.Background(), "wss://example.test/ws")
	err := client.Subscribe("btcusdt@trade", func([]byte) error { return nil })
	requireWSNotConnected(t, err)
	if client.subs["btcusdt@trade"].callback == nil {
		t.Fatal("expected subscription callback to be registered before send")
	}
}

func TestWSClient_Unsubscribe(t *testing.T) {
	client := NewWSClient(context.Background(), "wss://example.test/ws")
	client.subs["btcusdt@trade"] = Subscription{id: 1, callback: func([]byte) error { return nil }}

	err := client.Unsubscribe("btcusdt@trade")
	requireWSNotConnected(t, err)
	if _, ok := client.subs["btcusdt@trade"]; ok {
		t.Fatal("expected subscription to be removed")
	}
}

func TestWSClient_SetHandler(t *testing.T) {
	client := NewWSClient(context.Background(), "wss://example.test/ws")
	client.SetHandler("executionReport", func([]byte) error { return nil })
	if client.subs["executionReport"].id != 0 || client.subs["executionReport"].callback == nil {
		t.Fatalf("unexpected handler registration: %+v", client.subs["executionReport"])
	}
}

func TestWSClient_SetPostReconnect(t *testing.T) {
	client := NewWSClient(context.Background(), "wss://example.test/ws")
	called := false
	client.SetPostReconnect(func() {
		called = true
	})
	if client.postReconnect == nil {
		t.Fatal("expected post reconnect hook")
	}
	client.postReconnect()
	if !called {
		t.Fatal("expected post reconnect hook to run")
	}
}

func TestWSClient_CallSubscription(t *testing.T) {
	client := NewWSClient(context.Background(), "wss://example.test/ws")
	called := false
	client.SetHandler("event", func(data []byte) error {
		called = string(data) == `{"ok":true}`
		return nil
	})

	client.CallSubscription("event", []byte(`{"ok":true}`))

	if !called {
		t.Fatal("expected subscription callback to be called")
	}
}

func TestWsMarketClient_SubscribeIncrementOrderBook(t *testing.T) {
	client := NewWsMarketClient(context.Background())
	if err := client.SubscribeIncrementOrderBook("btcusdt", "100ms", func(*WsDepthEvent) error { return nil }); err != nil {
		t.Fatalf("SubscribeIncrementOrderBook: %v", err)
	}
	if client.manager.handlers["btcusdt@depth@100ms"] == nil {
		t.Fatal("expected depth subscription")
	}
}

func TestWsMarketClient_SubscribeLimitOrderBook(t *testing.T) {
	client := NewWsMarketClient(context.Background())
	if err := client.SubscribeLimitOrderBook("BTCUSDT", 5, "", func(*DepthEvent) error { return nil }); err != nil {
		t.Fatalf("SubscribeLimitOrderBook: %v", err)
	}
	if client.manager.handlers["btcusdt@depth5@100ms"] == nil {
		t.Fatal("expected limit depth subscription")
	}
}

func TestWsMarketClient_SubscribeLimitOrderBookParsesPartialDepthPayload(t *testing.T) {
	client := NewWsMarketClient(context.Background())
	var got *DepthEvent
	if err := client.SubscribeLimitOrderBook("BTCUSDT", 5, "100ms", func(e *DepthEvent) error {
		got = e
		return nil
	}); err != nil {
		t.Fatalf("SubscribeLimitOrderBook: %v", err)
	}

	client.CallSubscription("btcusdt@depth5@100ms", []byte(`{"lastUpdateId":123,"bids":[["100.1","1.5"]],"asks":[["100.2","2.5"]]}`))

	if got == nil {
		t.Fatal("expected partial depth event")
	}
	if got.FinalUpdateID != 123 {
		t.Fatalf("expected final update id 123, got %d", got.FinalUpdateID)
	}
	if len(got.Bids) != 1 || got.Bids[0][0] != "100.1" || got.Bids[0][1] != "1.5" {
		t.Fatalf("unexpected bids: %#v", got.Bids)
	}
	if len(got.Asks) != 1 || got.Asks[0][0] != "100.2" || got.Asks[0][1] != "2.5" {
		t.Fatalf("unexpected asks: %#v", got.Asks)
	}
}

func TestWsMarketClient_SubscribeBookTicker(t *testing.T) {
	client := NewWsMarketClient(context.Background())
	if err := client.SubscribeBookTicker("BTCUSDT", func(*BookTickerEvent) error { return nil }); err != nil {
		t.Fatalf("SubscribeBookTicker: %v", err)
	}
	if client.manager.handlers["btcusdt@bookTicker"] == nil {
		t.Fatal("expected book ticker subscription")
	}
}

func TestWsMarketClient_SubscribeAggTrade(t *testing.T) {
	client := NewWsMarketClient(context.Background())
	if err := client.SubscribeAggTrade("BTCUSDT", func(*AggTradeEvent) error { return nil }); err != nil {
		t.Fatalf("SubscribeAggTrade: %v", err)
	}
	if client.manager.handlers["btcusdt@aggTrade"] == nil {
		t.Fatal("expected aggregate trade subscription")
	}
}

func TestWsMarketClient_SubscribeTrade(t *testing.T) {
	client := NewWsMarketClient(context.Background())
	if err := client.SubscribeTrade("BTCUSDT", func(*TradeEvent) error { return nil }); err != nil {
		t.Fatalf("SubscribeTrade: %v", err)
	}
	if client.manager.handlers["btcusdt@trade"] == nil {
		t.Fatal("expected trade subscription")
	}
}

func TestWsMarketClient_SubscribeKline(t *testing.T) {
	client := NewWsMarketClient(context.Background())
	if err := client.SubscribeKline("BTCUSDT", "1m", func(*KlineEvent) error { return nil }); err != nil {
		t.Fatalf("SubscribeKline: %v", err)
	}
	if client.manager.handlers["btcusdt@kline_1m"] == nil {
		t.Fatal("expected kline subscription")
	}
}

func TestWsMarketClient_SubscribeAllMiniTicker(t *testing.T) {
	client := NewWsMarketClient(context.Background())
	if err := client.SubscribeAllMiniTicker(func([]*WsMiniTickerEvent) error { return nil }); err != nil {
		t.Fatalf("SubscribeAllMiniTicker: %v", err)
	}
	if client.manager.handlers["!miniTicker@arr"] == nil {
		t.Fatal("expected all mini ticker subscription")
	}
}

func TestWsMarketClient_UnsubscribeDepth(t *testing.T) {
	client := NewWsMarketClient(context.Background())
	if err := client.Subscribe("btcusdt@depth", func([]byte) error { return nil }); err != nil {
		t.Fatalf("Subscribe depth: %v", err)
	}
	if err := client.UnsubscribeDepth("BTCUSDT"); err != nil {
		t.Fatalf("UnsubscribeDepth: %v", err)
	}
}

func TestWsMarketClient_UnsubscribeIncrementOrderBook(t *testing.T) {
	client := NewWsMarketClient(context.Background())
	if err := client.SubscribeIncrementOrderBook("BTCUSDT", "100ms", func(*WsDepthEvent) error { return nil }); err != nil {
		t.Fatalf("SubscribeIncrementOrderBook: %v", err)
	}
	if err := client.UnsubscribeIncrementOrderBook("BTCUSDT", "100ms"); err != nil {
		t.Fatalf("UnsubscribeIncrementOrderBook: %v", err)
	}
}

func TestWsMarketClient_UnsubscribeLimitOrderBook(t *testing.T) {
	client := NewWsMarketClient(context.Background())
	if err := client.SubscribeLimitOrderBook("BTCUSDT", 5, "", func(*DepthEvent) error { return nil }); err != nil {
		t.Fatalf("SubscribeLimitOrderBook: %v", err)
	}
	if err := client.UnsubscribeLimitOrderBook("BTCUSDT", 5, ""); err != nil {
		t.Fatalf("UnsubscribeLimitOrderBook: %v", err)
	}
}

func TestWsMarketClient_UnsubscribeBookTicker(t *testing.T) {
	client := NewWsMarketClient(context.Background())
	if err := client.SubscribeBookTicker("BTCUSDT", func(*BookTickerEvent) error { return nil }); err != nil {
		t.Fatalf("SubscribeBookTicker: %v", err)
	}
	if err := client.UnsubscribeBookTicker("BTCUSDT"); err != nil {
		t.Fatalf("UnsubscribeBookTicker: %v", err)
	}
}

func TestWsMarketClient_UnsubscribeAggTrade(t *testing.T) {
	client := NewWsMarketClient(context.Background())
	if err := client.SubscribeAggTrade("BTCUSDT", func(*AggTradeEvent) error { return nil }); err != nil {
		t.Fatalf("SubscribeAggTrade: %v", err)
	}
	if err := client.UnsubscribeAggTrade("BTCUSDT"); err != nil {
		t.Fatalf("UnsubscribeAggTrade: %v", err)
	}
}

func TestWsMarketClient_UnsubscribeTrade(t *testing.T) {
	client := NewWsMarketClient(context.Background())
	if err := client.SubscribeTrade("BTCUSDT", func(*TradeEvent) error { return nil }); err != nil {
		t.Fatalf("SubscribeTrade: %v", err)
	}
	if err := client.UnsubscribeTrade("BTCUSDT"); err != nil {
		t.Fatalf("UnsubscribeTrade: %v", err)
	}
}

func TestWsMarketClient_UnsubscribeKline(t *testing.T) {
	client := NewWsMarketClient(context.Background())
	if err := client.SubscribeKline("BTCUSDT", "1m", func(*KlineEvent) error { return nil }); err != nil {
		t.Fatalf("SubscribeKline: %v", err)
	}
	if err := client.UnsubscribeKline("BTCUSDT", "1m"); err != nil {
		t.Fatalf("UnsubscribeKline: %v", err)
	}
}

func TestWsAccountClient_SubscribeExecutionReport(t *testing.T) {
	client := NewWsAccountClient(NewWsAPIClient(context.Background()), "api-key", "secret")
	client.SubscribeExecutionReport(func(*ExecutionReportEvent) {})
	if len(client.executionReportCallbacks) != 1 {
		t.Fatalf("expected callback registration, got %d", len(client.executionReportCallbacks))
	}
}

func TestWsAccountClient_SubscribeAccountPosition(t *testing.T) {
	client := NewWsAccountClient(NewWsAPIClient(context.Background()), "api-key", "secret")
	client.SubscribeAccountPosition(func(*AccountPositionEvent) {})
	if len(client.accountPositionCallbacks) != 1 {
		t.Fatalf("expected callback registration, got %d", len(client.accountPositionCallbacks))
	}
}

func TestWsAccountClient_IsConnected(t *testing.T) {
	client := NewWsAccountClient(NewWsAPIClient(context.Background()), "api-key", "secret")
	if client.IsConnected() {
		t.Fatal("expected new account client to be disconnected")
	}
}

func TestWsAccountClient_Connect(t *testing.T) {
	api := NewWsAPIClient(context.Background()).WithURL(":// bad url")
	client := NewWsAccountClient(api, "api-key", "secret")
	if err := client.Connect(); err == nil {
		t.Fatal("expected WS API connect to fail")
	}
}

func TestWsAccountClient_Close(t *testing.T) {
	client := NewWsAccountClient(NewWsAPIClient(context.Background()), "api-key", "secret")
	client.Close()
}

func TestWsAPIClient_WithURL(t *testing.T) {
	client := NewWsAPIClient(context.Background())
	if client.WithURL("wss://example.test/api") != client || client.URL != "wss://example.test/api" {
		t.Fatalf("unexpected URL: %s", client.URL)
	}
}

func TestWsAPIClient_SetEventHandler(t *testing.T) {
	client := NewWsAPIClient(context.Background())
	called := false
	client.SetEventHandler(func(data []byte) { called = string(data) == `{"e":"executionReport"}` })
	client.handleMessage([]byte(`{"e":"executionReport"}`))
	if !called {
		t.Fatal("expected event handler to receive pushed message")
	}
}

func TestWsAPIClient_Connect(t *testing.T) {
	client := NewWsAPIClient(context.Background()).WithURL(":// bad url")
	if err := client.Connect(); err == nil {
		t.Fatal("expected invalid URL to fail")
	}
}

func TestWsAPIClient_IsConnected(t *testing.T) {
	if NewWsAPIClient(context.Background()).IsConnected() {
		t.Fatal("expected new WS API client to be disconnected")
	}
}

func TestWsAPIClient_SendRequest(t *testing.T) {
	client := NewWsAPIClient(context.Background())
	_, err := client.SendRequest("req-1", map[string]string{"method": "ping"})
	requireWSNotConnected(t, err)
	if _, ok := client.PendingRequests["req-1"]; ok {
		t.Fatal("expected pending request to be removed after send failure")
	}
}

func TestWsAPIClient_Close(t *testing.T) {
	client := NewWsAPIClient(context.Background())
	client.Close()
	if !client.isClosed {
		t.Fatal("expected WS API client to be closed")
	}
}

func TestWsAPIClient_PlaceOrderWS(t *testing.T) {
	_, err := NewWsAPIClient(context.Background()).PlaceOrderWS("api-key", "secret", PlaceOrderParams{
		Symbol: "BTCUSDT", Side: "BUY", Type: "LIMIT", TimeInForce: "GTC", Quantity: "1", Price: "100",
	}, "req-1")
	requireWSNotConnected(t, err)
}

func TestWsAPIClient_ModifyOrderWS(t *testing.T) {
	_, err := NewWsAPIClient(context.Background()).ModifyOrderWS("api-key", "secret", CancelReplaceOrderParams{
		Symbol: "BTCUSDT", Side: "BUY", Type: "LIMIT", CancelReplaceMode: "STOP_ON_FAILURE", Quantity: "1", Price: "101",
	}, "req-1")
	requireWSNotConnected(t, err)
}

func TestWsAPIClient_CancelOrderWS(t *testing.T) {
	_, err := NewWsAPIClient(context.Background()).CancelOrderWS("api-key", "secret", "BTCUSDT", 1, "", "req-1")
	requireWSNotConnected(t, err)
}
