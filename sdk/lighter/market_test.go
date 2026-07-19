package lighter

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestClientGetJSONHTTPErrorRedactsBody(t *testing.T) {
	const secret = "http-error-secret-canary"
	client := NewClient()
	client.BaseURL = "https://lighter.test"
	client.HTTPClient = &http.Client{Transport: lighterOrderRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Body:       io.NopCloser(strings.NewReader(`{"message":"http-error-secret-canary"}`)),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})}
	_, err := client.GetCandlesticks(context.Background(), 1, "1m", 1, 2, 1)
	if err == nil {
		t.Fatal("expected HTTP error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("HTTP error echoed the response body")
	}
}

func TestClient_GetFundingRateUsesLighterExchangeRow(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/funding-rates" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"code":200,"message":"success","funding_rates":[{"market_id":1,"exchange":"binance","symbol":"BTC","rate":0.0001},{"market_id":1,"exchange":"lighter","symbol":"BTC","rate":-0.000024}]}`))
	}))
	defer server.Close()

	client := NewClient()
	client.BaseURL = server.URL

	got, err := client.GetFundingRate(context.Background(), 1)
	if err != nil {
		t.Fatalf("GetFundingRate returned error: %v", err)
	}
	if got.Exchange != "lighter" || got.Rate != -0.000024 {
		t.Fatalf("expected lighter row, got %+v", got)
	}
}

func TestClient_GetAllFundingRatesFiltersLighterRows(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":200,"message":"success","funding_rates":[{"market_id":1,"exchange":"binance","symbol":"BTC","rate":0.0001},{"market_id":1,"exchange":"lighter","symbol":"BTC","rate":-0.000024},{"market_id":2,"exchange":"lighter","symbol":"ETH","rate":0.000003}]}`))
	}))
	defer server.Close()

	client := NewClient()
	client.BaseURL = server.URL

	got, err := client.GetAllFundingRates(context.Background())
	if err != nil {
		t.Fatalf("GetAllFundingRates returned error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected two lighter rows, got %+v", got)
	}
	for _, row := range got {
		if row.Exchange != "lighter" {
			t.Fatalf("expected only lighter rows, got %+v", got)
		}
	}
}

func lighterMarketID(t *testing.T) int {
	t.Helper()
	return lighterIntEnv(t, "LIGHTER_TEST_MARKET_ID", lighterTestMarketID)
}

func TestClient_GetAssetDetails(t *testing.T) {
	got, err := newLiveClient(t).GetAssetDetails(context.Background(), nil)
	if err != nil {
		t.Fatalf("GetAssetDetails: %v", err)
	}
	if got == nil {
		t.Fatal("expected asset details")
	}
}

func TestClient_GetOrderBookDetails(t *testing.T) {
	marketID := lighterMarketID(t)
	got, err := newLiveClient(t).GetOrderBookDetails(context.Background(), &marketID, nil)
	if err != nil {
		t.Fatalf("GetOrderBookDetails: %v", err)
	}
	if got == nil {
		t.Fatal("expected order book details")
	}
}

func TestClient_GetOrderBooks(t *testing.T) {
	marketID := lighterMarketID(t)
	got, err := newLiveClient(t).GetOrderBooks(context.Background(), &marketID)
	if err != nil {
		t.Fatalf("GetOrderBooks: %v", err)
	}
	if got == nil {
		t.Fatal("expected order books")
	}
}

func TestClient_GetRecentTrades(t *testing.T) {
	got, err := newLiveClient(t).GetRecentTrades(context.Background(), lighterMarketID(t), 10)
	if err != nil {
		t.Fatalf("GetRecentTrades: %v", err)
	}
	if got == nil {
		t.Fatal("expected recent trades")
	}
}

func TestClient_GetOrderBookOrders(t *testing.T) {
	got, err := newLiveClient(t).GetOrderBookOrders(context.Background(), lighterMarketID(t), 10)
	if err != nil {
		t.Fatalf("GetOrderBookOrders: %v", err)
	}
	if got == nil {
		t.Fatal("expected order book orders")
	}
}

func TestClient_GetFundingRates(t *testing.T) {
	got, err := newLiveClient(t).GetFundingRates(context.Background())
	if err != nil {
		t.Fatalf("GetFundingRates: %v", err)
	}
	if got == nil {
		t.Fatal("expected funding rates")
	}
}

func TestClient_GetFundingRate(t *testing.T) {
	got, err := newLiveClient(t).GetFundingRate(context.Background(), lighterMarketID(t))
	if err != nil {
		t.Fatalf("GetFundingRate: %v", err)
	}
	if got == nil {
		t.Fatal("expected funding rate")
	}
	if got.Exchange != "lighter" {
		t.Fatalf("expected lighter exchange funding rate, got %+v", got)
	}
}

func TestClient_GetAllFundingRates(t *testing.T) {
	got, err := newLiveClient(t).GetAllFundingRates(context.Background())
	if err != nil {
		t.Fatalf("GetAllFundingRates: %v", err)
	}
	if got == nil {
		t.Fatal("expected funding rates slice")
	}
	for _, row := range got {
		if row.Exchange != "lighter" {
			t.Fatalf("expected only lighter exchange funding rates, got %+v", row)
		}
	}
}

func TestClient_GetExchangeStats(t *testing.T) {
	got, err := newLiveClient(t).GetExchangeStats(context.Background())
	if err != nil {
		t.Fatalf("GetExchangeStats: %v", err)
	}
	if got == nil {
		t.Fatal("expected exchange stats")
	}
}

func TestClient_GetCandlesticks(t *testing.T) {
	end := time.Now().UnixMilli()
	start := end - int64(time.Hour/time.Millisecond)
	got, err := newLiveClient(t).GetCandlesticks(context.Background(), lighterMarketID(t), "1m", start, end, 10)
	if err != nil {
		t.Fatalf("GetCandlesticks: %v", err)
	}
	if got == nil || len(got.Candlesticks) == 0 {
		t.Fatal("expected candlesticks")
	}
}

func TestClient_GetFundingHistory(t *testing.T) {
	marketID := lighterMarketID(t)
	end := time.Now().UnixMilli()
	start := end - int64(24*time.Hour/time.Millisecond)
	got, err := newLiveClient(t).GetFundingHistory(context.Background(), marketID, "1h", start, end, 10)
	if err != nil {
		t.Fatalf("GetFundingHistory: %v", err)
	}
	if got == nil || len(got.Fundings) == 0 {
		t.Fatal("expected funding history")
	}
}

func TestClient_GetTransferFeeInfo(t *testing.T) {
	got, err := newLivePrivateClient(t).GetTransferFeeInfo(context.Background(), nil)
	if err != nil {
		t.Fatalf("GetTransferFeeInfo: %v", err)
	}
	if got == nil {
		t.Fatal("expected transfer fee info")
	}
}

func TestClient_GetWithdrawalDelay(t *testing.T) {
	got, err := newLiveClient(t).GetWithdrawalDelay(context.Background())
	if err != nil {
		t.Fatalf("GetWithdrawalDelay: %v", err)
	}
	if got == nil || got.Seconds <= 0 {
		t.Fatal("expected withdrawal delay")
	}
}

func TestClient_GetAnnouncements(t *testing.T) {
	got, err := newLiveClient(t).GetAnnouncements(context.Background())
	if err != nil {
		t.Fatalf("GetAnnouncements: %v", err)
	}
	if got == nil {
		t.Fatal("expected announcements")
	}
}

func TestClient_GetL1Metadata(t *testing.T) {
	client := newLivePrivateClient(t)
	l1Address := os.Getenv("LIGHTER_TEST_L1_ADDRESS")
	if l1Address == "" {
		t.Skip("LIGHTER_TEST_L1_ADDRESS is required for GetL1Metadata live test")
	}
	got, err := client.GetL1Metadata(context.Background(), l1Address)
	if err != nil {
		t.Fatalf("GetL1Metadata: %v", err)
	}
	if got == nil || got.L1Address == "" {
		t.Fatal("expected l1 metadata")
	}
}

func TestClient_GetPublicPoolsMetadata(t *testing.T) {
	got, err := newLiveClient(t).GetPublicPoolsMetadata(context.Background(), "all", 0, 10, nil)
	if err != nil {
		t.Fatalf("GetPublicPoolsMetadata: %v", err)
	}
	if got == nil {
		t.Fatal("expected public pools metadata")
	}
}
