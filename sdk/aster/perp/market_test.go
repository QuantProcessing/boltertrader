package perp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/internal/testenv"
	"github.com/stretchr/testify/require"
)

func TestGetKlines(t *testing.T) {
	testenv.RequireLiveRead(t)

	client := newTestClient(t)
	res, err := client.Klines(context.Background(), "BTCUSDT", "1m", 10, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println(res)
}

func TestClient_DefaultHTTPTimeout(t *testing.T) {
	client := newTestClient(t)
	require.Positive(t, client.HTTPClient.Timeout)
}

func TestClient_WithHTTPClient(t *testing.T) {
	httpClient := &http.Client{Timeout: 42 * time.Second}
	client := newTestClient(t).WithHTTPClient(httpClient)
	require.NotSame(t, httpClient, client.HTTPClient)
	require.Equal(t, httpClient.Timeout, client.HTTPClient.Timeout)
	require.NotNil(t, client.HTTPClient.CheckRedirect)
}

// TestGetFundingRate tests retrieving funding rate for a specific symbol
func TestGetFundingRate(t *testing.T) {
	testenv.RequireLiveRead(t)

	client := newTestClient(t)
	ctx := context.Background()

	// Test with BTCUSDT
	rate, err := client.GetFundingRate(ctx, "BTCUSDT")
	if err != nil {
		t.Fatalf("Failed to get funding rate: %v", err)
	}

	if rate == nil {
		t.Fatal("Expected funding rate, got nil")
	}

	if rate.Symbol != "BTCUSDT" {
		t.Errorf("Expected symbol BTCUSDT, got %s", rate.Symbol)
	}

	if rate.LastFundingRate == "" {
		t.Error("Expected non-empty funding rate")
	}

	t.Logf("BTCUSDT funding rate: %s", rate.LastFundingRate)
	t.Logf("Next funding time: %d", rate.NextFundingTime)
}

func TestGetFundingRatePreservesPremiumIndexResponse(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc("/fapi/v3/premiumIndex", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "BTCUSDT", r.URL.Query().Get("symbol"))
		_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","markPrice":"43000.10","indexPrice":"42990.20","estimatedSettlePrice":"42995.00","lastFundingRate":"0.00040000","interestRate":"0.00010000","nextFundingTime":14400000,"time":123456789}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newClientForServer(t, srv, nil)
	rate, err := c.GetFundingRate(context.Background(), "BTCUSDT")
	require.NoError(t, err)
	require.Equal(t, "0.00040000", rate.LastFundingRate)
	require.Equal(t, "43000.10", rate.MarkPrice)
	require.Equal(t, "42990.20", rate.IndexPrice)
	require.Equal(t, "0.00010000", rate.InterestRate)
	require.Equal(t, int64(123456789), rate.Time)
}

func TestMarkPricePreservesPremiumIndexResponse(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc("/fapi/v3/premiumIndex", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "BTCUSDT", r.URL.Query().Get("symbol"))
		_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","markPrice":"43000.10","indexPrice":"42990.20","estimatedSettlePrice":"42995.00","lastFundingRate":"0.00040000","nextFundingTime":14400000,"time":123456789}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newClientForServer(t, srv, nil)
	price, err := c.MarkPrice(context.Background(), "BTCUSDT")
	require.NoError(t, err)
	require.Equal(t, "BTCUSDT", price.Symbol)
	require.Equal(t, "43000.10", price.MarkPrice)
	require.Equal(t, "42990.20", price.IndexPrice)
	require.Equal(t, int64(123456789), price.Time)
}

// TestGetAllFundingRates tests retrieving all funding rates
func TestGetAllFundingRates(t *testing.T) {
	testenv.RequireLiveRead(t)

	client := newTestClient(t)
	ctx := context.Background()

	rates, err := client.GetAllFundingRates(ctx)
	if err != nil {
		t.Fatalf("Failed to get all funding rates: %v", err)
	}

	if len(rates) == 0 {
		t.Fatal("Expected at least one funding rate, got empty array")
	}

	t.Logf("Total symbols with funding rates: %d", len(rates))

	// Show first 3 rates
	for i, rate := range rates {
		if i >= 3 {
			break
		}
		t.Logf("%s: rate=%s", rate.Symbol, rate.LastFundingRate)
	}
}

func TestGetOpenInterestParses(t *testing.T) {
	t.Parallel()
	payload := `{"symbol":"BTCUSDT","openInterest":"12345.678","time":1700000000000}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/fapi/v3/openInterest", r.URL.Path)
		require.Equal(t, "BTCUSDT", r.URL.Query().Get("symbol"))
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()
	c := newClientForServer(t, srv, nil)
	oi, err := c.GetOpenInterest(context.Background(), "BTCUSDT")
	require.NoError(t, err)
	require.Equal(t, "BTCUSDT", oi.Symbol)
	require.Equal(t, "12345.678", oi.OpenInterest)
	require.Equal(t, int64(1700000000000), oi.Time)
}

func TestSymbolFilterMultiplierDecimalAcceptsStringOrNumber(t *testing.T) {
	for _, payload := range []string{
		`{"filterType":"PERCENT_PRICE","multiplierDecimal":"4"}`,
		`{"filterType":"PERCENT_PRICE","multiplierDecimal":4}`,
	} {
		var filter SymbolFilter
		require.NoError(t, json.Unmarshal([]byte(payload), &filter))
		require.NotNil(t, filter.MultiplierDecimal)
		require.Equal(t, "4", filter.MultiplierDecimal.String())
	}
}

func TestGetFundingRateHistoryParses(t *testing.T) {
	t.Parallel()
	payload := `[{"symbol":"BTCUSDT","fundingRate":"0.0001","fundingTime":1700000000000,"markPrice":"50000"}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/fapi/v3/fundingRate", r.URL.Path)
		require.Equal(t, "BTCUSDT", r.URL.Query().Get("symbol"))
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()
	c := newClientForServer(t, srv, nil)
	hist, err := c.GetFundingRateHistory(context.Background(), "BTCUSDT", 0, 0, 0)
	require.NoError(t, err)
	require.Len(t, hist, 1)
	require.Equal(t, "0.0001", hist[0].FundingRate)
}
