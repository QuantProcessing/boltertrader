package okx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestWSClientConstructorCompatibility(t *testing.T) {
	ctx := context.Background()

	modern := NewWSClient(ctx)
	legacy := NewWsClient(ctx)

	var modernFromLegacy *WSClient = legacy
	var legacyFromModern *WsClient = modern

	if modernFromLegacy != legacy {
		t.Fatalf("legacy constructor returned incompatible type")
	}
	if legacyFromModern != modern {
		t.Fatalf("modern constructor returned incompatible alias type")
	}
	if modern.URL != legacy.URL {
		t.Fatalf("constructors should initialize the same URL: got %q and %q", modern.URL, legacy.URL)
	}
	if modern.Subs == nil || legacy.PendingReqs == nil {
		t.Fatalf("constructors should initialize websocket client maps")
	}
}

func TestWSClient_SubscribeTicker(t *testing.T) {
	client := newLivePublicOKXWSClient(t)

	if err := client.SubscribeTicker(okxSpotInstID, func(*Ticker) {}); err != nil {
		t.Fatalf("SubscribeTicker: %v", err)
	}
	if client.Subs[WsSubscribeArgs{Channel: "tickers", InstId: okxSpotInstID}] == nil {
		t.Fatalf("expected ticker subscription to be registered")
	}
}

func TestWSClient_SubscribeOrderBook(t *testing.T) {
	client := newLivePublicOKXWSClient(t)

	err := client.SubscribeOrderBook(okxSpotInstID, func(*OrderBook, string) {})
	if err != nil {
		t.Fatalf("SubscribeOrderBook failed: %v", err)
	}
	if client.Subs[WsSubscribeArgs{Channel: "books", InstId: okxSpotInstID}] == nil {
		t.Fatalf("expected order book subscription to be registered")
	}
}

func TestWSClient_SubscribeOrderBookDepthSelectsOKXChannel(t *testing.T) {
	client := newLocalPublicOKXWSClient(t)

	if err := client.SubscribeOrderBookDepth(okxSpotInstID, 50, func(*OrderBook, string) {}); err != nil {
		t.Fatalf("SubscribeOrderBookDepth(50): %v", err)
	}
	if client.Subs[WsSubscribeArgs{Channel: "books50-l2-tbt", InstId: okxSpotInstID}] == nil {
		t.Fatalf("expected books50-l2-tbt subscription to be registered")
	}
	if err := client.SubscribeOrderBookDepth(okxSpotInstID, 400, func(*OrderBook, string) {}); err != nil {
		t.Fatalf("SubscribeOrderBookDepth(400): %v", err)
	}
	if client.Subs[WsSubscribeArgs{Channel: "books-l2-tbt", InstId: okxSpotInstID}] == nil {
		t.Fatalf("expected books-l2-tbt subscription to be registered")
	}
	if _, err := OrderBookChannel(25); err == nil {
		t.Fatalf("expected unsupported depth to error")
	}
}

func TestWSClient_SubscribeTrades(t *testing.T) {
	client := newLivePublicOKXWSClient(t)

	if err := client.SubscribeTrades(okxSpotInstID, func(*PublicTrade) {}); err != nil {
		t.Fatalf("SubscribeTrades: %v", err)
	}
	if client.Subs[WsSubscribeArgs{Channel: "trades", InstId: okxSpotInstID}] == nil {
		t.Fatalf("expected trades subscription to be registered")
	}
}

func TestWSClient_SubscribeCandles(t *testing.T) {
	client := newLivePublicOKXWSClient(t)

	if err := client.SubscribeCandles(okxSpotInstID, "candle1m", func(Candle) {}); err != nil {
		t.Fatalf("SubscribeCandles: %v", err)
	}
	if client.Subs[WsSubscribeArgs{Channel: "candle1m", InstId: okxSpotInstID}] == nil {
		t.Fatalf("expected candles subscription to be registered")
	}
}

func TestWSClient_SubscribeMarkPrice(t *testing.T) {
	client := newLocalPublicOKXWSClient(t)

	if err := client.SubscribeMarkPrice(okxSwapInstID, func(*MarkPrice) {}); err != nil {
		t.Fatalf("SubscribeMarkPrice: %v", err)
	}
	if client.Subs[WsSubscribeArgs{Channel: "mark-price", InstId: okxSwapInstID}] == nil {
		t.Fatalf("expected mark-price subscription to be registered")
	}
}

func TestWSClient_SubscribeIndexTicker(t *testing.T) {
	client := newLocalPublicOKXWSClient(t)

	if err := client.SubscribeIndexTicker(okxSpotInstID, func(*IndexTicker) {}); err != nil {
		t.Fatalf("SubscribeIndexTicker: %v", err)
	}
	if client.Subs[WsSubscribeArgs{Channel: "index-tickers", InstId: okxSpotInstID}] == nil {
		t.Fatalf("expected index-tickers subscription to be registered")
	}
}

func TestWSClient_SubscribeOptionSummary(t *testing.T) {
	client := newLocalPublicOKXWSClient(t)

	if err := client.SubscribeOptionSummary("BTC-USD", func(*OptionSummary) {}); err != nil {
		t.Fatalf("SubscribeOptionSummary: %v", err)
	}
	if client.Subs[WsSubscribeArgs{Channel: "opt-summary", InstFamily: "BTC-USD"}] == nil {
		t.Fatalf("expected opt-summary subscription to be registered")
	}
}

func newLocalPublicOKXWSClient(t *testing.T) *WSClient {
	t.Helper()
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		go func() {
			defer conn.Close()
			for {
				var req struct {
					ID   int64             `json:"id"`
					Op   string            `json:"op"`
					Args []WsSubscribeArgs `json:"args"`
				}
				if err := conn.ReadJSON(&req); err != nil {
					return
				}
				if req.Op != "subscribe" || len(req.Args) == 0 {
					return
				}
				id := strconv.FormatInt(req.ID, 10)
				event := "subscribe"
				code := "0"
				resp := WsSubscribeRes{ID: &id, Event: &event, Arg: &req.Args[0], Code: &code}
				if err := conn.WriteJSON(resp); err != nil {
					return
				}
			}
		}()
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(context.Background())
	client := NewWSClient(ctx)
	client.URL = "ws" + strings.TrimPrefix(server.URL, "http")
	if err := client.Connect(); err != nil {
		cancel()
		t.Fatalf("Connect local websocket: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		if client.Conn != nil {
			_ = client.Conn.Close()
		}
	})
	return client
}
