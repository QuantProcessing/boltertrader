package factoryclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	hyperliquid "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
	hyperliquidperp "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/perp"
	hyperliquidspot "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/spot"
	"github.com/gorilla/websocket"
	"github.com/shopspring/decimal"
)

func TestHyperliquidPublicWebSocketsExerciseEveryExposedMethod(t *testing.T) {
	var connections atomic.Int32
	var unsubscribes atomic.Int32
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		conn, err := upgrader.Upgrade(w, request, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		connections.Add(1)
		for {
			var command struct {
				Method       string          `json:"method"`
				Subscription json.RawMessage `json:"subscription"`
			}
			if err := conn.ReadJSON(&command); err != nil {
				return
			}
			switch command.Method {
			case "subscribe":
				var requested map[string]string
				if err := json.Unmarshal(command.Subscription, &requested); err != nil {
					return
				}
				if err := conn.WriteJSON(map[string]any{
					"channel": "subscriptionResponse",
					"data":    json.RawMessage(command.Subscription),
				}); err != nil {
					return
				}
				coin := requested["coin"]
				switch requested["type"] {
				case "l2Book":
					if err := conn.WriteJSON(map[string]any{
						"channel": "l2Book",
						"data": map[string]any{
							"coin": coin,
							"time": int64(1_700_000_000_000),
							"levels": [][]map[string]any{
								{{"px": "10.1", "sz": "2", "n": 1}},
								{{"px": "10.2", "sz": "3", "n": 1}},
							},
						},
					}); err != nil {
						return
					}
				case "bbo":
					// Hyperliquid acknowledges native BBO subscriptions but
					// does not send an initial snapshot. The exchange BBO path
					// must use the snapshot-producing l2Book stream.
				case "trades":
					if err := conn.WriteJSON(map[string]any{
						"channel": "trades",
						"data": []map[string]any{{
							"coin": coin,
							"side": "B",
							"px":   "10.15",
							"sz":   "0.5",
							"time": int64(1_700_000_000_001),
							"tid":  int64(77),
						}},
					}); err != nil {
						return
					}
				case "candle":
					if err := conn.WriteJSON(map[string]any{
						"channel": "candle",
						"data": map[string]any{
							"t": int64(1_700_000_000_000),
							"T": int64(1_700_000_059_999),
							"s": coin,
							"i": "1m",
							"o": "10.0",
							"h": "10.5",
							"l": "9.9",
							"c": "10.2",
							"v": "12.5",
							"n": 7,
						},
					}); err != nil {
						return
					}
				case "activeAssetCtx":
					if err := conn.WriteJSON(map[string]any{
						"channel": "activeAssetCtx",
						"data": map[string]any{
							"coin": coin,
							"ctx": map[string]string{
								"markPx":  "100.25",
								"funding": "0.0001",
							},
						},
					}); err != nil {
						return
					}
				}
			case "unsubscribe":
				unsubscribes.Add(1)
			}
		}
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	base := hyperliquid.NewWebsocketClient(ctx).
		WithURL("ws" + strings.TrimPrefix(server.URL, "http"))
	base.SubscriptionAckTimeout = time.Second
	rest := &hyperliquidSpotClient{
		spotClient: &spotClient{meta: clientMeta{
			venue:   exchange.VenueHyperliquid,
			product: exchange.ProductSpot,
		}},
		metadata: map[string]hyperliquidMarketMeta{
			"@1": {
				instrument: exchange.Instrument{Symbol: "PURR-USDC", Product: exchange.ProductSpot},
				nativeCoin: "@1",
			},
		},
	}
	backend := newHyperliquidSpotWSBackendWithClient(
		rest,
		hyperliquidspot.NewWebsocketClient(base),
		base,
		cancel,
	)
	socket := newPublicWebSocket(rest.meta, backend)
	if connections.Load() != 0 {
		t.Fatal("backend construction connected eagerly")
	}

	watchCtx, watchCancel := context.WithTimeout(context.Background(), time.Second)
	defer watchCancel()
	subscription, err := socket.WatchBBO(watchCtx, exchange.WatchRequest{
		Instrument: "PURR-USDC",
	})
	if err != nil {
		t.Fatal(err)
	}
	if connections.Load() != 1 {
		t.Fatalf("connections = %d, want 1", connections.Load())
	}
	select {
	case event := <-subscription.Events():
		if event.Instrument != "PURR-USDC" ||
			!event.Bid.Price.Equal(decimal.RequireFromString("10.1")) ||
			!event.Ask.Quantity.Equal(decimal.RequireFromString("3")) {
			t.Fatalf("event = %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for BBO")
	}
	if err := subscription.Close(); err != nil {
		t.Fatal(err)
	}
	books, err := socket.WatchOrderBook(watchCtx, exchange.WatchRequest{Instrument: "PURR-USDC"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-books.Events():
		if event.Instrument != "PURR-USDC" ||
			!event.Bids[0].Price.Equal(decimal.RequireFromString("10.1")) ||
			!event.Asks[0].Quantity.Equal(decimal.RequireFromString("3")) {
			t.Fatalf("book event = %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for order book")
	}
	if err := books.Close(); err != nil {
		t.Fatal(err)
	}
	trades, err := socket.WatchPublicTrades(watchCtx, exchange.WatchRequest{Instrument: "PURR-USDC"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-trades.Events():
		if event.Instrument != "PURR-USDC" || event.TradeID != "77" ||
			event.Side != exchange.SideBuy ||
			!event.Quantity.Equal(decimal.RequireFromString("0.5")) {
			t.Fatalf("trade event = %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for public trade")
	}
	if err := trades.Close(); err != nil {
		t.Fatal(err)
	}
	candles, err := socket.WatchCandles(watchCtx, exchange.WatchCandlesRequest{
		Instrument: "PURR-USDC",
		Interval:   "1m",
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-candles.Events():
		if event.Instrument != "PURR-USDC" ||
			event.Interval != "1m" ||
			!event.Candle.Close.Equal(decimal.RequireFromString("10.2")) ||
			!event.Candle.Volume.Equal(decimal.RequireFromString("12.5")) ||
			!event.Candle.Complete {
			t.Fatalf("candle event = %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for candle")
	}
	if err := candles.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for unsubscribes.Load() != 4 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if unsubscribes.Load() != 4 {
		t.Fatalf("spot unsubscribes = %d, want 4", unsubscribes.Load())
	}
	if err := socket.Close(); err != nil {
		t.Fatal(err)
	}

	perpCtx, perpCancel := context.WithCancel(context.Background())
	t.Cleanup(perpCancel)
	perpBase := hyperliquid.NewWebsocketClient(perpCtx).
		WithURL("ws" + strings.TrimPrefix(server.URL, "http"))
	perpBase.SubscriptionAckTimeout = time.Second
	perpREST := &hyperliquidPerpClient{
		perpClient: &perpClient{meta: clientMeta{
			venue:   exchange.VenueHyperliquid,
			product: exchange.ProductPerp,
		}},
		metadata: map[string]hyperliquidMarketMeta{
			"BTC": {
				instrument: exchange.Instrument{Symbol: "BTC-USDC", Product: exchange.ProductPerp},
				nativeCoin: "BTC",
			},
		},
	}
	perpBackend := newHyperliquidPerpWSBackendWithClient(
		perpREST,
		hyperliquidperp.NewWebsocketClient(perpBase),
		perpBase,
		perpCancel,
	)
	perpSocket := newPerpWebSocket(perpREST.meta, perpBackend)
	if connections.Load() != 1 {
		t.Fatalf("perp backend construction connected eagerly; connections = %d", connections.Load())
	}

	perpBook, err := perpSocket.WatchOrderBook(watchCtx, exchange.WatchRequest{Instrument: "BTC-USDC"})
	if err != nil {
		t.Fatal(err)
	}
	if event := receiveWebSocketEvent(t, perpBook.Events()); event.Instrument != "BTC-USDC" {
		t.Fatalf("perp book event = %+v", event)
	}
	if err := perpBook.Close(); err != nil {
		t.Fatal(err)
	}
	perpBBO, err := perpSocket.WatchBBO(watchCtx, exchange.WatchRequest{Instrument: "BTC-USDC"})
	if err != nil {
		t.Fatal(err)
	}
	if event := receiveWebSocketEvent(t, perpBBO.Events()); event.Instrument != "BTC-USDC" {
		t.Fatalf("perp BBO event = %+v", event)
	}
	if err := perpBBO.Close(); err != nil {
		t.Fatal(err)
	}
	perpTrades, err := perpSocket.WatchPublicTrades(watchCtx, exchange.WatchRequest{Instrument: "BTC-USDC"})
	if err != nil {
		t.Fatal(err)
	}
	if event := receiveWebSocketEvent(t, perpTrades.Events()); event.TradeID != "77" {
		t.Fatalf("perp trade event = %+v", event)
	}
	if err := perpTrades.Close(); err != nil {
		t.Fatal(err)
	}
	perpCandles, err := perpSocket.WatchCandles(watchCtx, exchange.WatchCandlesRequest{Instrument: "BTC-USDC", Interval: "1m"})
	if err != nil {
		t.Fatal(err)
	}
	if event := receiveWebSocketEvent(t, perpCandles.Events()); event.Instrument != "BTC-USDC" {
		t.Fatalf("perp candle event = %+v", event)
	}
	if err := perpCandles.Close(); err != nil {
		t.Fatal(err)
	}
	marks, err := perpSocket.WatchMarkPrice(watchCtx, exchange.WatchRequest{Instrument: "BTC-USDC"})
	if err != nil {
		t.Fatal(err)
	}
	if event := receiveWebSocketEvent(t, marks.Events()); !event.Price.Equal(decimal.RequireFromString("100.25")) {
		t.Fatalf("mark event = %+v", event)
	}
	if err := marks.Close(); err != nil {
		t.Fatal(err)
	}
	funding, err := perpSocket.WatchFundingRate(watchCtx, exchange.WatchRequest{Instrument: "BTC-USDC"})
	if err != nil {
		t.Fatal(err)
	}
	if event := receiveWebSocketEvent(t, funding.Events()); !event.Rate.Equal(decimal.RequireFromString("0.0001")) {
		t.Fatalf("funding event = %+v", event)
	}
	if err := funding.Close(); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(time.Second)
	for unsubscribes.Load() != 10 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if unsubscribes.Load() != 10 {
		t.Fatalf("total unsubscribes = %d, want 10", unsubscribes.Load())
	}
	if connections.Load() != 2 {
		t.Fatalf("connections = %d, want one lazy connection per product", connections.Load())
	}
	if err := perpSocket.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestHyperliquidPublicWebSocketConnectErrorPreservesSDKCause(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	base := hyperliquid.NewWebsocketClient(ctx).WithURL("invalid-endpoint")
	backend := &hyperliquidPublicWSBackend{
		meta: clientMeta{
			venue:   exchange.VenueHyperliquid,
			product: exchange.ProductSpot,
		},
		base:   base,
		cancel: cancel,
	}

	err := backend.ensureConnected("WatchOrderBook")
	if err == nil || !strings.Contains(err.Error(), "invalid endpoint URL") {
		t.Fatalf("connect error = %v, want preserved SDK cause", err)
	}
}

func TestHyperliquidWatchCandlesSeedsLatestRESTSnapshotWhenNativeStreamIsIdle(t *testing.T) {
	var candleSnapshots atomic.Int32
	var candleSubscriptions atomic.Int32
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/ws" {
			conn, err := upgrader.Upgrade(w, request, nil)
			if err != nil {
				return
			}
			defer conn.Close()
			for {
				var command struct {
					Method       string          `json:"method"`
					Subscription json.RawMessage `json:"subscription"`
				}
				if err := conn.ReadJSON(&command); err != nil {
					return
				}
				if command.Method == "unsubscribe" {
					return
				}
				var subscription map[string]string
				if err := json.Unmarshal(command.Subscription, &subscription); err != nil {
					return
				}
				if subscription["type"] == "candle" {
					candleSubscriptions.Add(1)
				}
				if err := conn.WriteJSON(map[string]any{
					"channel": "subscriptionResponse",
					"data":    json.RawMessage(command.Subscription),
				}); err != nil {
					return
				}
			}
		}

		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			http.Error(w, "invalid payload", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch payload["type"] {
		case "spotMeta":
			_, _ = w.Write([]byte(`{"tokens":[{"name":"USDC","szDecimals":8,"weiDecimals":8,"index":0,"isCanonical":true},{"name":"PURR","szDecimals":0,"weiDecimals":5,"index":1,"isCanonical":true}],"universe":[{"name":"PURR/USDC","index":0,"tokens":[1,0],"isCanonical":true}]}`))
		case "candleSnapshot":
			candleSnapshots.Add(1)
			_, _ = w.Write([]byte(`[{"t":1700000000000,"T":1700000059999,"s":"PURR/USDC","i":"1m","o":"10.0","c":"10.2","h":"10.5","l":"9.9","v":"12.5","n":7}]`))
		default:
			http.Error(w, "unexpected info request", http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	client := NewHyperliquidSpot(openAPITestPrivateKey, Settings{
		Endpoint:          server.URL,
		WebSocketEndpoint: "ws" + strings.TrimPrefix(server.URL, "http") + "/ws",
		Environment:       "testnet",
		HTTPClient:        server.Client(),
	}).(*hyperliquidSpotClient)
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	subscription, err := client.WebSocket().WatchCandles(ctx, exchange.WatchCandlesRequest{
		Instrument: "PURR-USDC",
		Interval:   "1m",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()

	select {
	case event := <-subscription.Events():
		if event.Instrument != "PURR-USDC" ||
			event.Interval != "1m" ||
			!event.Candle.Close.Equal(decimal.RequireFromString("10.2")) {
			t.Fatalf("seed candle = %+v", event)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("idle native candle stream did not receive latest REST snapshot")
	}
	if candleSnapshots.Load() != 1 {
		t.Fatalf("candle snapshot requests = %d, want 1", candleSnapshots.Load())
	}
	if candleSubscriptions.Load() != 1 {
		t.Fatalf("native candle subscriptions = %d, want 1", candleSubscriptions.Load())
	}
}

func receiveWebSocketEvent[T any](t *testing.T, events <-chan T) T {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for WebSocket event")
		var zero T
		return zero
	}
}

func TestHyperliquidWebSocketConvertersRejectMalformedPayloads(t *testing.T) {
	meta := hyperliquidMarketMeta{
		instrument: exchange.Instrument{Symbol: "BTC-USDC", Product: exchange.ProductPerp},
		nativeCoin: "BTC",
	}
	if _, err := hyperliquidBookEvent(meta, hyperliquid.WsL2Book{
		Coin: "ETH",
		Time: 1,
	}); err == nil {
		t.Fatal("book identity mismatch was accepted")
	}
	if _, err := hyperliquidBBOEvent(meta, hyperliquid.WsBbo{
		Coin: "BTC",
		Time: 1,
		Bbo:  []hyperliquid.WsLevel{{Px: "bad", Sz: "1"}, {Px: "2", Sz: "1"}},
	}); err == nil {
		t.Fatal("malformed BBO decimal was accepted")
	}
	if _, err := hyperliquidReferenceEvent(meta, hyperliquid.WsActiveAssetCtx{
		Coin: "BTC",
		Ctx: hyperliquid.WsAssetCtx{
			MarkPx:  "100",
			Funding: "bad",
		},
	}, time.Now()); err == nil {
		t.Fatal("malformed funding rate was accepted")
	}
	if _, err := hyperliquidCandleEvent(meta, "1m", hyperliquid.WsCandle{
		T:      1_700_000_000_000,
		TClose: 1_700_000_059_999,
		S:      "BTC",
		I:      "1m",
		O:      "100",
		H:      "99",
		L:      "98",
		C:      "98.5",
		V:      "1",
	}, time.Now()); err == nil {
		t.Fatal("inconsistent candle prices were accepted")
	}
}
