package sdk

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestClient_GetInstruments(t *testing.T) {
	got, err := newLiveClient().GetInstruments(context.Background(), "linear")
	if err != nil {
		t.Fatalf("GetInstruments: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected at least one instrument")
	}
}

func TestClient_GetInstrumentsForBase(t *testing.T) {
	got, err := newLiveClient().GetInstrumentsForBase(context.Background(), "linear", "BTC")
	if err != nil {
		t.Fatalf("GetInstrumentsForBase: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected BTC linear instruments")
	}
}

func TestClient_GetTicker(t *testing.T) {
	got, err := newLiveClient().GetTicker(context.Background(), "spot", bybitSpotSymbol)
	if err != nil {
		t.Fatalf("GetTicker: %v", err)
	}
	if got.Symbol != bybitSpotSymbol {
		t.Fatalf("unexpected ticker symbol: %s", got.Symbol)
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
					`{"retCode":0,"retMsg":"OK","time":1710000000123,"result":{"category":"linear","list":[{"symbol":"BTCUSDT","fundingRate":"0.0001","markPrice":"65000","indexPrice":"64990","nextFundingTime":"1710003600000","fundingIntervalHour":"8"}]}}`,
				)),
				Header: make(http.Header),
			}, nil
		})})

	got, err := client.GetTickers(context.Background(), "linear")
	if err != nil {
		t.Fatalf("GetTickers returned error: %v", err)
	}
	if seenPath != "/v5/market/tickers" {
		t.Fatalf("unexpected path: %s", seenPath)
	}
	if strings.Contains(seenQuery, "symbol=") || !strings.Contains(seenQuery, "category=linear") {
		t.Fatalf("unexpected query: %s", seenQuery)
	}
	if len(got) != 1 || got[0].Symbol != "BTCUSDT" || got[0].FundingRate != "0.0001" || got[0].Time != "1710000000123" {
		t.Fatalf("unexpected ticker rows: %+v", got)
	}
}

func TestClient_GetOrderBook(t *testing.T) {
	got, err := newLiveClient().GetOrderBook(context.Background(), "linear", bybitLinearSymbol, 5)
	if err != nil {
		t.Fatalf("GetOrderBook: %v", err)
	}
	if len(got.Asks) == 0 || len(got.Bids) == 0 {
		t.Fatalf("expected non-empty order book: %+v", got)
	}
}

func TestClient_GetRecentTrades(t *testing.T) {
	got, err := newLiveClient().GetRecentTrades(context.Background(), "spot", bybitSpotSymbol, 10)
	if err != nil {
		t.Fatalf("GetRecentTrades: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected recent trades")
	}
}

func TestClient_GetKlines(t *testing.T) {
	got, err := newLiveClient().GetKlines(context.Background(), "linear", bybitLinearSymbol, "60", 0, 0, 10)
	if err != nil {
		t.Fatalf("GetKlines: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected klines")
	}
}

func TestClient_GetOpenInterest(t *testing.T) {
	got, err := newLiveClient().GetOpenInterest(context.Background(), "linear", bybitLinearSymbol, "5min", 0, 0, 50, "")
	if err != nil {
		t.Fatalf("GetOpenInterest: %v", err)
	}
	if len(got.List) == 0 {
		t.Fatal("expected open interest history")
	}
}

func TestClient_GetFundingHistory(t *testing.T) {
	got, err := newLiveClient().GetFundingHistory(context.Background(), "linear", bybitLinearSymbol, 0, 0, 2)
	if err != nil {
		t.Fatalf("GetFundingHistory: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected funding history")
	}
}
