package spot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

const binanceSpotTestSymbol = "BTCUSDT"

func TestClient_Depth(t *testing.T) {
	got, err := newLiveClient().Depth(context.Background(), binanceSpotTestSymbol, 5)
	if err != nil {
		t.Fatalf("Depth: %v", err)
	}
	if got.LastUpdateID == 0 || len(got.Bids) == 0 || len(got.Asks) == 0 {
		t.Fatalf("unexpected depth response: %+v", got)
	}
}

func TestClient_Klines(t *testing.T) {
	got, err := newLiveClient().Klines(context.Background(), binanceSpotTestSymbol, "1m", 1, 0, 0)
	if err != nil {
		t.Fatalf("Klines: %v", err)
	}
	if len(got) != 1 || len(got[0]) < 6 {
		t.Fatalf("unexpected klines response: %+v", got)
	}
}

func TestClient_GetTradesUsesPublicTradesEndpoint(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/trades", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("symbol"); got != "BTCUSDT" {
			t.Fatalf("unexpected symbol query: %s", r.URL.RawQuery)
		}
		if got := r.URL.Query().Get("limit"); got != "2" {
			t.Fatalf("unexpected limit query: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`[{"id":1,"price":"100.5","qty":"0.3","quoteQty":"30.15","time":1710000000000,"isBuyerMaker":false,"isBestMatch":true}]`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	got, err := NewClient().WithBaseURL(server.URL).GetTrades(context.Background(), "BTCUSDT", 2)
	if err != nil {
		t.Fatalf("GetTrades: %v", err)
	}
	if len(got) != 1 || got[0].ID != 1 || got[0].Price != "100.5" || got[0].Time != 1710000000000 {
		t.Fatalf("unexpected trades response: %+v", got)
	}
}

func TestClient_GetAggTradesPagedUsesPublicAggTradesEndpoint(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/aggTrades", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("symbol"); got != "BTCUSDT" {
			t.Fatalf("unexpected symbol query: %s", r.URL.RawQuery)
		}
		if got := r.URL.Query().Get("startTime"); got != "1710000000000" {
			t.Fatalf("unexpected startTime query: %s", r.URL.RawQuery)
		}
		if got := r.URL.Query().Get("endTime"); got != "1710000180000" {
			t.Fatalf("unexpected endTime query: %s", r.URL.RawQuery)
		}
		if got := r.URL.Query().Get("limit"); got != "2" {
			t.Fatalf("unexpected limit query: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`[{"a":7,"p":"100.5","q":"0.3","f":1,"l":2,"T":1710000060000,"m":false,"M":true}]`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	got, err := NewClient().WithBaseURL(server.URL).GetAggTradesPaged(context.Background(), AggTradesQuery{
		Symbol:    "BTCUSDT",
		StartTime: 1710000000000,
		EndTime:   1710000180000,
		Limit:     2,
	})
	if err != nil {
		t.Fatalf("GetAggTradesPaged: %v", err)
	}
	if len(got) != 1 || got[0].ID != 7 || got[0].Price != "100.5" || got[0].Timestamp != 1710000060000 {
		t.Fatalf("unexpected agg trades response: %+v", got)
	}
}

func TestClient_Ticker(t *testing.T) {
	got, err := newLiveClient().Ticker(context.Background(), binanceSpotTestSymbol)
	if err != nil {
		t.Fatalf("Ticker: %v", err)
	}
	if got.Symbol != binanceSpotTestSymbol || got.LastPrice == "" {
		t.Fatalf("unexpected ticker response: %+v", got)
	}
}

func TestClient_TickerRequiresSymbol(t *testing.T) {
	if _, err := newLiveClient().Ticker(context.Background(), ""); err == nil {
		t.Fatal("expected missing symbol error")
	}
}

func TestClient_BookTicker(t *testing.T) {
	got, err := newLiveClient().BookTicker(context.Background(), binanceSpotTestSymbol)
	if err != nil {
		t.Fatalf("BookTicker: %v", err)
	}
	if got.Symbol != binanceSpotTestSymbol || got.BidPrice == "" || got.AskPrice == "" {
		t.Fatalf("unexpected book ticker response: %+v", got)
	}
}

func TestClient_BookTickerRequiresSymbol(t *testing.T) {
	if _, err := newLiveClient().BookTicker(context.Background(), ""); err == nil {
		t.Fatal("expected missing symbol error")
	}
}

func TestClient_ExchangeInfo(t *testing.T) {
	got, err := newLiveClient().ExchangeInfo(context.Background())
	if err != nil {
		t.Fatalf("ExchangeInfo: %v", err)
	}
	if len(got.Symbols) == 0 {
		t.Fatalf("unexpected exchange info response: %+v", got)
	}
}
