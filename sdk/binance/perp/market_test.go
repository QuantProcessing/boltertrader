package perp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClient_Depth(t *testing.T) {
	got, err := newLiveClient(t).Depth(context.Background(), binancePerpTestSymbol, 5)
	if err != nil {
		t.Fatalf("Depth: %v", err)
	}
	if got.LastUpdateID == 0 || len(got.Bids) == 0 || len(got.Asks) == 0 {
		t.Fatalf("unexpected depth response: %+v", got)
	}
}

func TestClient_Klines(t *testing.T) {
	got, err := newLiveClient(t).Klines(context.Background(), binancePerpTestSymbol, "1m", 1, 0, 0)
	if err != nil {
		t.Fatalf("Klines: %v", err)
	}
	if len(got) != 1 || len(got[0]) < 6 {
		t.Fatalf("unexpected klines response: %+v", got)
	}
}

func TestClient_ContinousKlines(t *testing.T) {
	got, err := newLiveClient(t).ContinousKlines(context.Background(), binancePerpTestSymbol, "PERPETUAL", "1m", 1, 0, 0)
	if err != nil {
		t.Fatalf("ContinousKlines: %v", err)
	}
	if len(got) != 1 || len(got[0]) < 6 {
		t.Fatalf("unexpected continuous klines response: %+v", got)
	}
}

func TestClient_Ticker(t *testing.T) {
	got, err := newLiveClient(t).Ticker(context.Background(), binancePerpTestSymbol)
	if err != nil {
		t.Fatalf("Ticker: %v", err)
	}
	if got.Symbol != binancePerpTestSymbol || got.LastPrice == "" {
		t.Fatalf("unexpected ticker response: %+v", got)
	}
}

func TestClient_TickerRequiresSymbol(t *testing.T) {
	if _, err := newLiveClient(t).Ticker(context.Background(), ""); err == nil {
		t.Fatal("expected missing symbol error")
	}
}

func TestClient_MarkPrice(t *testing.T) {
	got, err := newLiveClient(t).MarkPrice(context.Background(), binancePerpTestSymbol)
	if err != nil {
		t.Fatalf("MarkPrice: %v", err)
	}
	if got.Symbol != binancePerpTestSymbol || got.MarkPrice == "" {
		t.Fatalf("unexpected mark price response: %+v", got)
	}
}

func TestClient_ExchangeInfo(t *testing.T) {
	got, err := newLiveClient(t).ExchangeInfo(context.Background())
	if err != nil {
		t.Fatalf("ExchangeInfo: %v", err)
	}
	if len(got.Symbols) == 0 {
		t.Fatalf("unexpected exchange info response: %+v", got)
	}
}

func TestClient_GetAggTrades(t *testing.T) {
	got, err := newLiveClient(t).GetAggTrades(context.Background(), binancePerpTestSymbol, 1)
	if err != nil {
		t.Fatalf("GetAggTrades: %v", err)
	}
	if len(got) == 0 || got[0].Price == "" {
		t.Fatalf("unexpected aggregate trades response: %+v", got)
	}
}

func TestClient_GetAggTradesPaged(t *testing.T) {
	got, err := newLiveClient(t).GetAggTradesPaged(context.Background(), AggTradesQuery{Symbol: binancePerpTestSymbol, Limit: 1})
	if err != nil {
		t.Fatalf("GetAggTradesPaged: %v", err)
	}
	if len(got) == 0 || got[0].Price == "" {
		t.Fatalf("unexpected paged aggregate trades response: %+v", got)
	}
}

func TestClient_GetFundingInfo(t *testing.T) {
	got, err := newLiveClient(t).GetFundingInfo(context.Background())
	if err != nil {
		t.Fatalf("GetFundingInfo: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil funding info slice")
	}
}

func TestClient_GetFundingRate(t *testing.T) {
	got, err := newLiveClient(t).GetFundingRate(context.Background(), binancePerpTestSymbol)
	if err != nil {
		t.Fatalf("GetFundingRate: %v", err)
	}
	if got.Symbol != binancePerpTestSymbol || got.LastFundingRate == "" {
		t.Fatalf("unexpected funding rate response: %+v", got)
	}
}

func TestClient_GetFundingRatePreservesPremiumIndexResponse(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/fapi/v1/premiumIndex", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("symbol") != "BTCUSDT" {
			t.Fatalf("unexpected symbol query: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","markPrice":"43000.10","indexPrice":"42990.20","estimatedSettlePrice":"42995.00","lastFundingRate":"0.00080000","interestRate":"0.00010000","nextFundingTime":28800000,"time":123456789}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client := NewClient().WithBaseURL(server.URL)
	got, err := client.GetFundingRate(context.Background(), "BTCUSDT")
	if err != nil {
		t.Fatalf("GetFundingRate: %v", err)
	}
	if got.LastFundingRate != "0.00080000" {
		t.Fatalf("expected settlement-interval rate to be preserved, got %q", got.LastFundingRate)
	}
	if got.MarkPrice != "43000.10" || got.IndexPrice != "42990.20" {
		t.Fatalf("expected mark/index prices, got %+v", got)
	}
	if got.InterestRate != "0.00010000" || got.Time != 123456789 {
		t.Fatalf("expected official reference fields, got %+v", got)
	}
}

func TestClient_GetAllFundingRatesUsesOnlyPremiumIndex(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/fapi/v1/premiumIndex", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"symbol":"BTCUSDT","lastFundingRate":"not-a-number","nextFundingTime":28800000}]`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client := NewClient().WithBaseURL(server.URL)
	got, err := client.GetAllFundingRates(context.Background())
	if err != nil {
		t.Fatalf("GetAllFundingRates: %v", err)
	}
	if len(got) != 1 || got[0].LastFundingRate != "not-a-number" {
		t.Fatalf("expected raw premiumIndex row, got %+v", got)
	}
}

func TestClient_GetAllFundingRates(t *testing.T) {
	got, err := newLiveClient(t).GetAllFundingRates(context.Background())
	if err != nil {
		t.Fatalf("GetAllFundingRates: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected funding rates")
	}
}

func TestClient_GetOpenInterest(t *testing.T) {
	got, err := newLiveClient(t).GetOpenInterest(context.Background(), binancePerpTestSymbol)
	if err != nil {
		t.Fatalf("GetOpenInterest: %v", err)
	}
	if got.Symbol != binancePerpTestSymbol || got.OpenInterest == "" {
		t.Fatalf("unexpected open interest response: %+v", got)
	}
}

func TestClient_GetFundingRateHistory(t *testing.T) {
	got, err := newLiveClient(t).GetFundingRateHistory(context.Background(), binancePerpTestSymbol, 0, 0, 1)
	if err != nil {
		t.Fatalf("GetFundingRateHistory: %v", err)
	}
	if len(got) == 0 || got[0].Symbol != binancePerpTestSymbol {
		t.Fatalf("unexpected funding history response: %+v", got)
	}
}
