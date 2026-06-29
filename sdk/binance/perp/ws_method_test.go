package perp

import (
	"context"
	"strings"
	"testing"
)

func requireWSNotConnected(t *testing.T, err error) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "not connected") && !strings.Contains(err.Error(), "not established") {
		t.Fatalf("expected websocket not connected error, got %v", err)
	}
}

func TestWSClient_Connect(t *testing.T) {
	client := NewWSClient(context.Background(), "wss://example.test/ws")
	client.Close()

	if err := client.Connect(); err == nil || !strings.Contains(err.Error(), "client is closed") {
		t.Fatalf("expected closed client error, got %v", err)
	}
}

func TestWSClient_IsConnected(t *testing.T) {
	client := NewWSClient(context.Background(), "wss://example.test/ws")
	if client.IsConnected() {
		t.Fatal("expected new client to be disconnected")
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
	client.SetHandler("ORDER_TRADE_UPDATE", func([]byte) error { return nil })
	if client.subs["ORDER_TRADE_UPDATE"].id != 0 || client.subs["ORDER_TRADE_UPDATE"].callback == nil {
		t.Fatalf("unexpected handler registration: %+v", client.subs["ORDER_TRADE_UPDATE"])
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

func TestWsMarketClientUsesRoutedEndpoints(t *testing.T) {
	client := NewWsMarketClient(context.Background())
	t.Cleanup(client.Close)

	if WSPublicBaseURL != "wss://fstream.binance.com/public/ws" {
		t.Fatalf("unexpected public websocket constant: %s", WSPublicBaseURL)
	}
	if WSBaseURL != WSPublicBaseURL {
		t.Fatalf("unexpected compatibility websocket constant: %s", WSBaseURL)
	}
	if client.WsClient.URL != WSPublicBaseURL {
		t.Fatalf("unexpected public websocket URL: %s", client.WsClient.URL)
	}
	if WSMarketBaseURL != "wss://fstream.binance.com/market/ws" {
		t.Fatalf("unexpected market websocket constant: %s", WSMarketBaseURL)
	}
	if WSPrivateBaseURL != "wss://fstream.binance.com/private/ws" {
		t.Fatalf("unexpected private websocket constant: %s", WSPrivateBaseURL)
	}
	if WSMarketFallbackBaseURL != "wss://fstream.binancefuture.com/ws" {
		t.Fatalf("unexpected market websocket fallback constant: %s", WSMarketFallbackBaseURL)
	}
	if client.routeClient(binancePerpWSRouteMarket).URL != WSMarketBaseURL {
		t.Fatalf("unexpected market websocket URL: %s", client.routeClient(binancePerpWSRouteMarket).URL)
	}
}

func TestCoinMWsMarketClientUsesDstreamEndpoints(t *testing.T) {
	client := NewCoinMWsMarketClient(context.Background())
	t.Cleanup(client.Close)

	if CoinMWSPublicBaseURL != "wss://dstream.binance.com/ws" {
		t.Fatalf("unexpected COIN-M public websocket constant: %s", CoinMWSPublicBaseURL)
	}
	if CoinMWSMarketBaseURL != "wss://dstream.binance.com/ws" {
		t.Fatalf("unexpected COIN-M market websocket constant: %s", CoinMWSMarketBaseURL)
	}
	if client.WsClient.URL != CoinMWSPublicBaseURL {
		t.Fatalf("unexpected COIN-M public websocket URL: %s", client.WsClient.URL)
	}
	if client.routeClient(binancePerpWSRouteMarket).URL != CoinMWSMarketBaseURL {
		t.Fatalf("unexpected COIN-M market websocket URL: %s", client.routeClient(binancePerpWSRouteMarket).URL)
	}

	candidates := client.routeManager(binancePerpWSRouteMarket).combinedURLCandidates("btcusd_perp@kline_1m")
	wantPrimary := "wss://dstream.binance.com/stream?streams=btcusd_perp@kline_1m"
	wantFallback := "wss://dstream.binancefuture.com/stream?streams=btcusd_perp@kline_1m"
	if len(candidates) != 2 || candidates[0] != wantPrimary || candidates[1] != wantFallback {
		t.Fatalf("unexpected COIN-M combined URL candidates: %#v", candidates)
	}
	for _, candidate := range candidates {
		if strings.Contains(candidate, "fstream") {
			t.Fatalf("COIN-M candidate must not use USD-M fstream host: %s", candidate)
		}
	}
}

func TestWsMarketClient_SubscribeMarkPrice(t *testing.T) {
	client := NewWsMarketClient(context.Background())
	if err := client.SubscribeMarkPrice("btcusdt", "1s", func(*WsMarkPriceEvent) error { return nil }); err != nil {
		t.Fatalf("SubscribeMarkPrice: %v", err)
	}
	if client.routeManager(binancePerpWSRouteMarket).handlers["btcusdt@markPrice@1s"] == nil {
		t.Fatal("expected mark price subscription")
	}
}

func TestWsMarketClient_SubscribeIncrementOrderBook(t *testing.T) {
	client := NewWsMarketClient(context.Background())
	if err := client.SubscribeIncrementOrderBook("btcusdt", "100ms", func(*WsDepthEvent) error { return nil }); err != nil {
		t.Fatalf("SubscribeIncrementOrderBook: %v", err)
	}
	if client.routeManager(binancePerpWSRoutePublic).handlers["btcusdt@depth@100ms"] == nil {
		t.Fatal("expected depth subscription")
	}
}

func TestWsMarketClient_SubscribeLimitOrderBook(t *testing.T) {
	client := NewWsMarketClient(context.Background())
	if err := client.SubscribeLimitOrderBook("btcusdt", 5, "100ms", func(*WsDepthEvent) error { return nil }); err != nil {
		t.Fatalf("SubscribeLimitOrderBook: %v", err)
	}
	if client.routeManager(binancePerpWSRoutePublic).handlers["btcusdt@depth5@100ms"] == nil {
		t.Fatal("expected limit depth subscription")
	}
}

func TestWsMarketClient_SubscribeLimitOrderBookParsesPartialDepthPayload(t *testing.T) {
	client := NewWsMarketClient(context.Background())
	var got *WsDepthEvent
	if err := client.SubscribeLimitOrderBook("btcusdt", 5, "100ms", func(e *WsDepthEvent) error {
		got = e
		return nil
	}); err != nil {
		t.Fatalf("SubscribeLimitOrderBook: %v", err)
	}

	client.CallSubscription("btcusdt@depth5@100ms", []byte(`{"lastUpdateId":123,"E":1700000000000,"T":1700000000001,"bids":[["100.1","1.5"]],"asks":[["100.2","2.5"]]}`))

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
	if err := client.SubscribeBookTicker("btcusdt", func(*WsBookTickerEvent) error { return nil }); err != nil {
		t.Fatalf("SubscribeBookTicker: %v", err)
	}
	if client.routeManager(binancePerpWSRoutePublic).handlers["btcusdt@bookTicker"] == nil {
		t.Fatal("expected book ticker subscription")
	}
}

func TestWsMarketClient_SubscribeAggTrade(t *testing.T) {
	client := NewWsMarketClient(context.Background())
	if err := client.SubscribeAggTrade("btcusdt", func(*WsAggTradeEvent) error { return nil }); err != nil {
		t.Fatalf("SubscribeAggTrade: %v", err)
	}
	if client.routeManager(binancePerpWSRouteMarket).handlers["btcusdt@aggTrade"] == nil {
		t.Fatal("expected aggregate trade subscription")
	}
}

func TestWsMarketClient_SubscribeKline(t *testing.T) {
	client := NewWsMarketClient(context.Background())
	if err := client.SubscribeKline("btcusdt", "1m", func(*WsKlineEvent) error { return nil }); err != nil {
		t.Fatalf("SubscribeKline: %v", err)
	}
	if client.routeManager(binancePerpWSRouteMarket).handlers["btcusdt@kline_1m"] == nil {
		t.Fatal("expected kline subscription")
	}
}

func TestWsMarketClient_SubscribeAllMiniTicker(t *testing.T) {
	client := NewWsMarketClient(context.Background())
	if err := client.SubscribeAllMiniTicker(func([]*WsMiniTickerEvent) error { return nil }); err != nil {
		t.Fatalf("SubscribeAllMiniTicker: %v", err)
	}
	if client.routeManager(binancePerpWSRouteMarket).handlers["!miniTicker@arr"] == nil {
		t.Fatal("expected all mini ticker subscription")
	}
}

func TestWsMarketClient_RawSubscribeRoutesByStream(t *testing.T) {
	client := NewWsMarketClient(context.Background())
	if err := client.Subscribe("btcusdt@kline_1m", func([]byte) error { return nil }); err != nil {
		t.Fatalf("Subscribe kline: %v", err)
	}
	if err := client.Subscribe("btcusdt@depth5@100ms", func([]byte) error { return nil }); err != nil {
		t.Fatalf("Subscribe depth: %v", err)
	}

	if client.routeManager(binancePerpWSRouteMarket).handlers["btcusdt@kline_1m"] == nil {
		t.Fatal("expected raw kline subscription on market route")
	}
	if client.routeManager(binancePerpWSRoutePublic).handlers["btcusdt@depth5@100ms"] == nil {
		t.Fatal("expected raw depth subscription on public route")
	}
}

func TestWsMarketClient_StreamPathURLs(t *testing.T) {
	urls := binancePerpWSStreamPathURLs(binancePerpWSRouteMarket, "btcusdt@kline_1m", false)
	if len(urls) != 2 || urls[0] != WSMarketBaseURL+"/btcusdt@kline_1m" || urls[1] != WSMarketFallbackBaseURL+"/btcusdt@kline_1m" {
		t.Fatalf("unexpected market path urls: %#v", urls)
	}
	urls = binancePerpWSStreamPathURLs(binancePerpWSRoutePublic, "btcusdt@depth@100ms", true)
	if len(urls) != 2 || urls[0] != WSMarketFallbackBaseURL+"/btcusdt@depth@100ms" || urls[1] != WSPublicBaseURL+"/btcusdt@depth@100ms" {
		t.Fatalf("unexpected public fallback path urls: %#v", urls)
	}
}

func TestWsMarketClient_SubscribeAllMarkPrice(t *testing.T) {
	client := NewWsMarketClient(context.Background())
	var got []*WsMarkPriceEvent
	if err := client.SubscribeAllMarkPrice("1s", func(events []*WsMarkPriceEvent) error {
		got = events
		return nil
	}); err != nil {
		t.Fatalf("SubscribeAllMarkPrice: %v", err)
	}
	if client.routeManager(binancePerpWSRouteMarket).handlers["!markPrice@arr@1s"] == nil {
		t.Fatal("expected all mark price subscription")
	}
	client.CallSubscription("!markPrice@arr@1s", []byte(`[{"e":"markPriceUpdate","E":7000,"s":"BTCUSDT","p":"200","i":"199","r":"0.0007","T":28800000}]`))
	if len(got) != 1 || got[0].Symbol != "BTCUSDT" || got[0].FundingRate != "0.0007" {
		t.Fatalf("unexpected mark price array: %#v", got)
	}
}

func TestWsMarketClient_RoutesThreeSecondMarkPriceStreams(t *testing.T) {
	client := NewWsMarketClient(context.Background())
	var single *WsMarkPriceEvent
	if err := client.SubscribeMarkPrice("btcusdt", "3s", func(event *WsMarkPriceEvent) error {
		single = event
		return nil
	}); err != nil {
		t.Fatalf("SubscribeMarkPrice: %v", err)
	}
	client.handleMessage([]byte(`{"e":"markPriceUpdate","E":7000,"s":"BTCUSDT","p":"200","i":"199","r":"0.0007","T":28800000}`))
	if single == nil || single.Symbol != "BTCUSDT" || single.FundingRate != "0.0007" {
		t.Fatalf("expected 3s single mark price route, got %#v", single)
	}

	var all []*WsMarkPriceEvent
	if err := client.SubscribeAllMarkPrice("3s", func(events []*WsMarkPriceEvent) error {
		all = events
		return nil
	}); err != nil {
		t.Fatalf("SubscribeAllMarkPrice: %v", err)
	}
	client.handleMessage([]byte(`[{"e":"markPriceUpdate","E":7000,"s":"BTCUSDT","p":"200","i":"199","r":"0.0007","T":28800000}]`))
	if len(all) != 1 || all[0].Symbol != "BTCUSDT" || all[0].FundingRate != "0.0007" {
		t.Fatalf("expected 3s all-market mark price route, got %#v", all)
	}
}

func TestWsMarketClient_UnsubscribeMarkPrice(t *testing.T) {
	client := NewWsMarketClient(context.Background())
	if err := client.SubscribeMarkPrice("btcusdt", "1s", func(*WsMarkPriceEvent) error { return nil }); err != nil {
		t.Fatalf("SubscribeMarkPrice: %v", err)
	}
	if err := client.UnsubscribeMarkPrice("btcusdt", "1s"); err != nil {
		t.Fatalf("UnsubscribeMarkPrice: %v", err)
	}
	if client.routeManager(binancePerpWSRouteMarket).handlers["btcusdt@markPrice@1s"] != nil {
		t.Fatal("expected mark price subscription to be removed")
	}
}

func TestWsMarketClient_UnsubscribeIncrementOrderBook(t *testing.T) {
	client := NewWsMarketClient(context.Background())
	if err := client.SubscribeIncrementOrderBook("btcusdt", "100ms", func(*WsDepthEvent) error { return nil }); err != nil {
		t.Fatalf("SubscribeIncrementOrderBook: %v", err)
	}
	if err := client.UnsubscribeIncrementOrderBook("btcusdt", "100ms"); err != nil {
		t.Fatalf("UnsubscribeIncrementOrderBook: %v", err)
	}
}

func TestWsMarketClient_UnsubscribeLimitOrderBook(t *testing.T) {
	client := NewWsMarketClient(context.Background())
	if err := client.SubscribeLimitOrderBook("btcusdt", 5, "100ms", func(*WsDepthEvent) error { return nil }); err != nil {
		t.Fatalf("SubscribeLimitOrderBook: %v", err)
	}
	if err := client.UnsubscribeLimitOrderBook("btcusdt", 5, "100ms"); err != nil {
		t.Fatalf("UnsubscribeLimitOrderBook: %v", err)
	}
}

func TestWsMarketClient_UnsubscribeBookTicker(t *testing.T) {
	client := NewWsMarketClient(context.Background())
	if err := client.SubscribeBookTicker("btcusdt", func(*WsBookTickerEvent) error { return nil }); err != nil {
		t.Fatalf("SubscribeBookTicker: %v", err)
	}
	if err := client.UnsubscribeBookTicker("btcusdt"); err != nil {
		t.Fatalf("UnsubscribeBookTicker: %v", err)
	}
}

func TestWsMarketClient_UnsubscribeAggTrade(t *testing.T) {
	client := NewWsMarketClient(context.Background())
	if err := client.SubscribeAggTrade("btcusdt", func(*WsAggTradeEvent) error { return nil }); err != nil {
		t.Fatalf("SubscribeAggTrade: %v", err)
	}
	if err := client.UnsubscribeAggTrade("btcusdt"); err != nil {
		t.Fatalf("UnsubscribeAggTrade: %v", err)
	}
}

func TestWsMarketClient_UnsubscribeKline(t *testing.T) {
	client := NewWsMarketClient(context.Background())
	if err := client.SubscribeKline("btcusdt", "1m", func(*WsKlineEvent) error { return nil }); err != nil {
		t.Fatalf("SubscribeKline: %v", err)
	}
	if err := client.UnsubscribeKline("btcusdt", "1m"); err != nil {
		t.Fatalf("UnsubscribeKline: %v", err)
	}
}

func TestWsAccountClient_WithURL(t *testing.T) {
	client := NewWsAccountClient(context.Background(), "api-key", "secret")
	got := client.WithURL("wss://example.test/account")
	if got != client || client.WsClient.URL != "wss://example.test/account" {
		t.Fatalf("unexpected WithURL result: %v %s", got == client, client.WsClient.URL)
	}
}

func TestWsAccountClient_SubscribeAccountUpdate(t *testing.T) {
	client := NewWsAccountClient(context.Background(), "api-key", "secret")
	client.SubscribeAccountUpdate(func(*AccountUpdateEvent) {})
	if len(client.accountUpdateCallbacks) != 1 {
		t.Fatalf("expected callback registration, got %d", len(client.accountUpdateCallbacks))
	}
}

func TestWsAccountClient_SubscribeOrderUpdate(t *testing.T) {
	client := NewWsAccountClient(context.Background(), "api-key", "secret")
	client.SubscribeOrderUpdate(func(*OrderUpdateEvent) {})
	if len(client.orderUpdateCallbacks) != 1 {
		t.Fatalf("expected callback registration, got %d", len(client.orderUpdateCallbacks))
	}
}

func TestWsAccountClient_SubscribeAccountConfigUpdate(t *testing.T) {
	client := NewWsAccountClient(context.Background(), "api-key", "secret")
	client.SubscribeAccountConfigUpdate(func(*AccountConfigUpdateEvent) {})
	if len(client.accountConfigUpdateCallbacks) != 1 {
		t.Fatalf("expected callback registration, got %d", len(client.accountConfigUpdateCallbacks))
	}
}

func TestWsAccountClient_Connect(t *testing.T) {
	requireBinancePerpLiveWrite(t)
	client := NewWsAccountClient(context.Background(), envOrDefault("BINANCE_API_KEY", ""), envOrDefault("BINANCE_SECRET_KEY", ""))
	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	client.Close()
}

func TestWsAccountClient_Close(t *testing.T) {
	client := NewWsAccountClient(context.Background(), "api-key", "secret")
	client.Close()
	if !client.WsClient.isClosed {
		t.Fatal("expected embedded websocket client to close")
	}
}

func TestWsAPIClient_WithURL(t *testing.T) {
	client := NewWsAPIClient(context.Background())
	if client.WithURL("wss://example.test/api") != client || client.URL != "wss://example.test/api" {
		t.Fatalf("unexpected URL: %s", client.URL)
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
	_, err := NewWsAPIClient(context.Background()).ModifyOrderWS("api-key", "secret", ModifyOrderParams{
		Symbol: "BTCUSDT", Side: "BUY", OrderID: 1, Quantity: "1", Price: "101",
	}, "req-1")
	requireWSNotConnected(t, err)
}

func TestWsAPIClient_CancelOrderWS(t *testing.T) {
	_, err := NewWsAPIClient(context.Background()).CancelOrderWS("api-key", "secret", CancelOrderParams{
		Symbol: "BTCUSDT", OrderID: "1",
	}, "req-1")
	requireWSNotConnected(t, err)
}

func TestWsAPIClient_CancelAllOrdersWS(t *testing.T) {
	err := NewWsAPIClient(context.Background()).CancelAllOrdersWS("api-key", "secret", CancelAllOrdersParams{Symbol: "BTCUSDT"}, "req-1")
	requireWSNotConnected(t, err)
}

func TestClient_CreateListenKey(t *testing.T) {
	listenKey, err := requireBinancePerpLiveWrite(t).CreateListenKey(context.Background())
	if err != nil {
		t.Fatalf("CreateListenKey: %v", err)
	}
	if listenKey == "" {
		t.Fatal("expected listen key")
	}
}

func TestClient_KeepAliveListenKey(t *testing.T) {
	client := requireBinancePerpLiveWrite(t)
	if err := client.KeepAliveListenKey(context.Background()); err != nil {
		t.Fatalf("KeepAliveListenKey: %v", err)
	}
}

func TestClient_CloseListenKey(t *testing.T) {
	client := requireBinancePerpLiveWrite(t)
	if err := client.CloseListenKey(context.Background()); err != nil {
		t.Fatalf("CloseListenKey: %v", err)
	}
}
