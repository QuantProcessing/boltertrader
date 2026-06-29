package sdk

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestClient_GetCurrentFundRateBuildsCurrentFundingRequest(t *testing.T) {
	t.Parallel()

	var seenPath string
	var seenQuery string
	client := NewClient().
		WithBaseURL("https://example.test").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			seenPath = req.URL.Path
			seenQuery = req.URL.RawQuery
			return &http.Response{
				StatusCode: http.StatusOK,
				Body: io.NopCloser(strings.NewReader(
					`{"code":"00000","msg":"success","requestTime":1710000000123,"data":[{"symbol":"BTCUSDT","fundingRate":"0.0001","fundingRateInterval":"8","nextUpdate":"1710003600000","minFundingRate":"-0.003","maxFundingRate":"0.003"}]}`,
				)),
				Header: make(http.Header),
			}, nil
		})})

	got, err := client.GetCurrentFundRate(context.Background(), "BTCUSDT", "USDT-FUTURES")
	if err != nil {
		t.Fatalf("GetCurrentFundRate returned error: %v", err)
	}
	if seenPath != "/api/v2/mix/market/current-fund-rate" {
		t.Fatalf("unexpected path: %s", seenPath)
	}
	if !strings.Contains(seenQuery, "symbol=BTCUSDT") || !strings.Contains(seenQuery, "productType=USDT-FUTURES") {
		t.Fatalf("unexpected query: %s", seenQuery)
	}
	if len(got) != 1 {
		t.Fatalf("expected one funding row, got %d", len(got))
	}
	if got[0].FundingRate != "0.0001" || got[0].FundingRateInterval != "8" || got[0].RequestTime != 1710000000123 {
		t.Fatalf("unexpected funding row: %+v", got[0])
	}
}

func TestClient_GetTickersBuildsAllTickersRequest(t *testing.T) {
	t.Parallel()

	var seenPath string
	var seenQuery string
	client := NewClient().
		WithBaseURL("https://example.test").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			seenPath = req.URL.Path
			seenQuery = req.URL.RawQuery
			return &http.Response{
				StatusCode: http.StatusOK,
				Body: io.NopCloser(strings.NewReader(
					`{"code":"00000","msg":"success","requestTime":1710000000123,"data":[{"category":"USDT-FUTURES","symbol":"BTCUSDT","fundingRate":"0.0001","markPrice":"65000","indexPrice":"64990","ts":"1710000000123"}]}`,
				)),
				Header: make(http.Header),
			}, nil
		})})

	got, err := client.GetTickers(context.Background(), "USDT-FUTURES")
	if err != nil {
		t.Fatalf("GetTickers returned error: %v", err)
	}
	if seenPath != "/api/v3/market/tickers" {
		t.Fatalf("unexpected path: %s", seenPath)
	}
	if strings.Contains(seenQuery, "symbol=") || !strings.Contains(seenQuery, "category=USDT-FUTURES") {
		t.Fatalf("unexpected query: %s", seenQuery)
	}
	if len(got) != 1 || got[0].Symbol != "BTCUSDT" || got[0].FundingRate != "0.0001" {
		t.Fatalf("unexpected ticker rows: %+v", got)
	}
}

func TestClient_GetInstruments(t *testing.T) {
	got, err := newLiveClient().GetInstruments(context.Background(), bitgetSpotCategory, "")
	if err != nil {
		t.Fatalf("GetInstruments: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected spot instruments")
	}
}

func TestClient_GetTicker(t *testing.T) {
	got, err := newLiveClient().GetTicker(context.Background(), bitgetPerpCategory, bitgetPerpSymbol)
	if err != nil {
		t.Fatalf("GetTicker: %v", err)
	}
	if got.Symbol != bitgetPerpSymbol {
		t.Fatalf("unexpected ticker symbol: %s", got.Symbol)
	}
}

func TestClient_GetOrderBook(t *testing.T) {
	got, err := newLiveClient().GetOrderBook(context.Background(), bitgetSpotCategory, bitgetSpotSymbol, 5)
	if err != nil {
		t.Fatalf("GetOrderBook: %v", err)
	}
	if len(got.Asks) == 0 || len(got.Bids) == 0 {
		t.Fatalf("expected non-empty order book: %+v", got)
	}
}

func TestClient_GetRecentFills(t *testing.T) {
	got, err := newLiveClient().GetRecentFills(context.Background(), bitgetSpotCategory, bitgetSpotSymbol, 10)
	if err != nil {
		t.Fatalf("GetRecentFills: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected recent fills")
	}
}

func TestClient_GetOpenInterest(t *testing.T) {
	got, err := newLiveClient().GetOpenInterest(context.Background(), bitgetPerpSymbol, bitgetPerpCategory)
	if err != nil {
		t.Fatalf("GetOpenInterest: %v", err)
	}
	if len(got.List) == 0 || got.List[0].Symbol == "" || got.TS == "" {
		t.Fatalf("expected open interest symbol: %+v", got)
	}
}

func TestClient_GetHistoryFundRate(t *testing.T) {
	got, err := newLiveClient().GetHistoryFundRate(context.Background(), bitgetPerpSymbol, bitgetPerpCategory, 2, 1)
	if err != nil {
		t.Fatalf("GetHistoryFundRate: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected funding history")
	}
}

func TestClient_GetCandles(t *testing.T) {
	got, err := newLiveClient().GetCandles(context.Background(), bitgetPerpCategory, bitgetPerpSymbol, "1m", "market", 0, 0, 2)
	if err != nil {
		t.Fatalf("GetCandles: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected candles")
	}
}
