package factory

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/gorilla/websocket"
	"github.com/shopspring/decimal"
)

func TestPublicMarketSocketKeepsOptionalCapabilitiesOptional(t *testing.T) {
	if publicMarketSocket(nil) != nil {
		t.Fatal("nil socket became a usable capability")
	}
	// An adapter with the basic perpetual surface must not gain a fabricated
	// rich-reference capability from an embedded nil optional interface.
	basic := struct{ exchange.PerpMarketWebSocket }{}
	socket := publicMarketSocket(basic)
	if _, ok := socket.(exchange.PerpMarketWebSocket); !ok {
		t.Fatal("public perpetual capability was lost")
	}
	if _, ok := socket.(exchange.DerivativeReferenceWebSocket); ok {
		t.Fatal("unsupported rich-reference capability was advertised")
	}
}

func TestPublicMarketSocketPreservesNativeSharingReplayAndLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ready := make(chan struct{})
	closed := make(chan struct{}, 8)
	var subscriptions atomic.Int64
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer func() { _ = conn.Close(); closed <- struct{}{} }()
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		for {
			var request struct {
				ID   string   `json:"req_id"`
				Op   string   `json:"op"`
				Args []string `json:"args"`
			}
			if err := conn.ReadJSON(&request); err != nil {
				return
			}
			if request.Op == "unsubscribe" {
				continue
			}
			if request.Op != "subscribe" || len(request.Args) != 1 {
				t.Errorf("public socket sent unexpected operation %q", request.Op)
				return
			}
			subscriptions.Add(1)
			if err := conn.WriteJSON(map[string]any{"op": "subscribe", "req_id": request.ID, "success": true}); err != nil {
				return
			}
			var payload string
			switch topic := request.Args[0]; topic {
			case "orderbook.50.BTCUSDT":
				payload = `{"topic":"orderbook.50.BTCUSDT","type":"snapshot","ts":1720000000000,"data":{"s":"BTCUSDT","b":[["99","1"]],"a":[["101","2"]],"u":1,"seq":1}}`
			case "tickers.BTCUSDT":
				// Wait for all three logical consumers to join the same native topic.
				select {
				case <-ready:
				case <-ctx.Done():
					return
				}
				payload = `{"topic":"tickers.BTCUSDT","ts":1720000000000,"data":{"symbol":"BTCUSDT","markPrice":"100","indexPrice":"99.5","fundingRate":"0.0001","openInterest":"123","nextFundingTime":"1720003600000","time":"1720000000000"}}`
			default:
				t.Errorf("unexpected public topic %q", topic)
				return
			}
			if err := conn.WriteMessage(websocket.TextMessage, []byte(payload)); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	transport := new(countingPublicMarketTransport)
	settings := newSettings([]Option{
		WithEnvironment(EnvironmentLive), WithHTTPClient(&http.Client{Transport: transport}),
		WithWebSocketEndpoint("ws" + strings.TrimPrefix(server.URL, "http")),
	})
	_, _, _, socket, closeClient := publicMarketClientForConfig("bybit-usdt-perp", settings)
	if socket == nil || closeClient == nil {
		t.Fatal("public client construction failed")
	}
	defer closeClient()
	instrument := exchange.Instrument{Identity: exchange.InstrumentIdentity{
		Venue: exchange.VenueBybit, Product: exchange.ProductPerp, Symbol: "BTC-USDT",
		NativeSymbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT", SettleAsset: "USDT",
	}, Rules: exchange.InstrumentRules{
		PriceIncrement: decimal.RequireFromString("0.1"), QuantityIncrement: decimal.RequireFromString("0.001"),
		MinQuantity: decimal.RequireFromString("0.001"),
	}}
	request := exchange.WatchRequest{Instrument: instrument}
	book, err := socket.WatchOrderBook(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if event := receivePublicEvent(t, ctx, book.Events()); len(event.Bids) != 1 || event.Bids[0].Price.String() != "99" {
		t.Fatalf("book snapshot = %+v", event)
	}
	for receivePublicEvent(t, ctx, book.Status()).State != exchange.SubscriptionActive {
	}
	lateBook, err := socket.WatchOrderBook(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	replay, ok := lateBook.(exchange.ReplaySubscription[exchange.BookEvent])
	if !ok {
		t.Fatal("late subscriber lost replay capability")
	}
	if event, valid := replay.Replay(); !valid || len(event.Asks) != 1 || event.Asks[0].Price.String() != "101" {
		t.Fatalf("replayed book = %+v, valid = %v", event, valid)
	}
	perp := socket.(exchange.PerpMarketWebSocket)
	mark, err := perp.WatchMarkPrice(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	funding, err := perp.WatchFundingRate(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	reference, err := socket.(exchange.DerivativeReferenceWebSocket).WatchDerivativeReference(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	close(ready)
	if event := receivePublicEvent(t, ctx, mark.Events()); event.Price.String() != "100" {
		t.Fatalf("mark = %+v", event)
	}
	if event := receivePublicEvent(t, ctx, funding.Events()); event.Rate.String() != "0.0001" {
		t.Fatalf("funding = %+v", event)
	}
	if event := receivePublicEvent(t, ctx, reference.Events()); !event.IndexPrice.Valid || event.IndexPrice.Value.String() != "99.5" || !event.OpenInterest.Valid || event.OpenInterest.Value.String() != "123" {
		t.Fatalf("rich reference fields were lost: %+v", event)
	}
	if got := subscriptions.Load(); got != 2 {
		t.Fatalf("%d native subscriptions, want one book and one shared reference", got)
	}
	if got := transport.requests.Load(); got != 0 {
		t.Fatalf("public streams performed %d unexpected HTTP requests", got)
	}
	if err := book.Close(); err != nil {
		t.Fatal(err)
	}
	if _, valid := replay.Replay(); !valid {
		t.Fatal("closing first consumer destroyed the late consumer's book")
	}
	if err := socket.Close(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		receivePublicEvent(t, ctx, closed)
	}
	select {
	case _, open := <-reference.Events():
		if open {
			t.Fatal("socket close left reference subscription open")
		}
	case <-ctx.Done():
		t.Fatal("socket close did not close the reference subscription")
	}
}

func receivePublicEvent[T any](t *testing.T, ctx context.Context, events <-chan T) T {
	t.Helper()
	select {
	case event, open := <-events:
		if !open {
			t.Fatal("public stream closed before the expected event")
		}
		return event
	case <-ctx.Done():
		t.Fatal("timed out waiting for public stream event")
		var zero T
		return zero
	}
}
