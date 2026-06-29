package spot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClient_GetTradesUsesPublicTradesEndpoint(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/trades", func(w http.ResponseWriter, r *http.Request) {
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

	got, err := NewClient("", "").WithBaseURL(server.URL).GetTrades(context.Background(), "BTCUSDT", 2)
	if err != nil {
		t.Fatalf("GetTrades: %v", err)
	}
	if len(got) != 1 || got[0].ID != 1 || got[0].Price != "100.5" || got[0].Time != 1710000000000 {
		t.Fatalf("unexpected trades response: %+v", got)
	}
}

func TestClient_GetAggTradesPagedUsesPublicAggTradesEndpoint(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/aggTrades", func(w http.ResponseWriter, r *http.Request) {
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

	got, err := NewClient("", "").WithBaseURL(server.URL).GetAggTradesPaged(context.Background(), AggTradesQuery{
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
