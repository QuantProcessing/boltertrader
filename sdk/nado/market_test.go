package nado

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/internal/testenv"
	"github.com/stretchr/testify/require"
)

func requireFullEnv(t *testing.T) {
	t.Helper()
	testenv.RequireFull(t, "NADO_PRIVATE_KEY", "NADO_SUBACCOUNT_NAME")
}

func GetEnv() (string, string) {
	pk := os.Getenv("NADO_PRIVATE_KEY")
	subaccount := os.Getenv("NADO_SUBACCOUNT_NAME")
	return pk, subaccount
}

func retryNadoPublic[T any](t *testing.T, op string, fn func() (T, error)) T {
	t.Helper()

	var zero T
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		value, err := fn()
		if err == nil {
			return value
		}
		lastErr = err
		lower := strings.ToLower(err.Error())
		if !strings.Contains(lower, "eof") && !strings.Contains(lower, "timeout") {
			t.Fatalf("%s failed: %v", op, err)
		}
		time.Sleep(500 * time.Millisecond)
	}

	t.Fatalf("%s failed after retries: %v", op, lastErr)
	return zero
}

func TestGetNonces(t *testing.T) {
	requireFullEnv(t)
	privateKey, subaccount := GetEnv()
	client, err := NewClient().WithCredentials(privateKey, subaccount)
	if err != nil {
		t.Fatal(err)
	}
	nonces, err := client.GetNonces(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println(nonces)
}

func TestGetCandlesticks(t *testing.T) {
	requireFullEnv(t)
	privateKey, subaccount := GetEnv()
	client, err := NewClient().WithCredentials(privateKey, subaccount)
	if err != nil {
		t.Fatal(err)
	}
	candlesticks := retryNadoPublic(t, "GetCandlesticks", func() ([]ArchiveCandlestick, error) {
		return client.GetCandlesticks(context.Background(), CandlestickRequest{
			Candlesticks: Candlesticks{
				ProductID:   1,
				Granularity: 60,
				Limit:       10,
			},
		})
	})
	fmt.Println(candlesticks)
}

func TestGetContracts(t *testing.T) {
	client := NewClient()
	contracts, err := client.GetContracts(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println(contracts)
}

func TestGetTickers(t *testing.T) {
	client := NewClient()
	tickers, err := client.GetTickers(context.Background(), MarketTypePerp, nil)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println(tickers)
}

// TestGetFundingRate tests the GetFundingRate method
func TestGetFundingRate(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	client := NewClient()
	ctx := context.Background()

	// Test with product ID 36 (commonly LIT-PERP in Nado)
	rate, err := client.GetFundingRate(ctx, 36)
	if err != nil {
		t.Fatalf("Failed to get funding rate: %v", err)
	}

	if rate == nil {
		t.Fatal("Expected funding rate, got nil")
	}

	if rate.ProductID != 36 {
		t.Errorf("Expected ProductID 36, got %d", rate.ProductID)
	}

	if rate.FundingRateX18 == "" {
		t.Error("Expected raw funding_rate_x18")
	}

	t.Logf("Product ID: %d", rate.ProductID)
	t.Logf("Funding rate x18: %s", rate.FundingRateX18)
	t.Logf("Update time: %s", rate.UpdateTime)
}

// TestGetAllFundingRates tests the GetAllFundingRates method
func TestGetAllFundingRates(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	client := NewClient()
	ctx := context.Background()

	rates, err := client.GetAllFundingRates(ctx)
	if err != nil {
		t.Fatalf("Failed to get all funding rates: %v", err)
	}

	if len(rates) == 0 {
		t.Fatal("Expected at least one funding rate, got empty array")
	}

	t.Logf("Total products with funding rates: %d", len(rates))

	// Show first 3 rates
	i := 0
	for productID, rate := range rates {
		if i >= 3 {
			break
		}
		t.Logf("Product %s: rate_x18=%s, update_time=%s", productID, rate.FundingRateX18, rate.UpdateTime)
		i++
	}

	for productID, rate := range rates {
		if rate.FundingRateX18 == "" {
			t.Errorf("Expected raw funding_rate_x18 for product %s", productID)
		}
	}
}

// TestGetFundingRateHistoryParses verifies the SDK method parses the archive
// response envelope correctly and forwards query parameters to the server.
func TestGetFundingRateHistoryParses(t *testing.T) {
	t.Parallel()

	entries := []FundingRateArchiveEntry{
		{ProductID: 1, FundingRateX18: "100000000000000000", Timestamp: 1700000000000},
		{ProductID: 1, FundingRateX18: "200000000000000000", Timestamp: 1700003600000},
	}
	payload, err := json.Marshal(entries)
	require.NoError(t, err)

	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	c := NewClient().WithArchiveV1URL(srv.URL)
	hist, err := c.GetFundingRateHistory(context.Background(), 1, 1700000000000, 1700007200000, 10)
	require.NoError(t, err)
	require.Len(t, hist, 2)
	require.Equal(t, int64(1), hist[0].ProductID)
	require.Equal(t, "100000000000000000", hist[0].FundingRateX18)
	require.Equal(t, int64(1700000000000), hist[0].Timestamp)
	require.Equal(t, int64(1700003600000), hist[1].Timestamp)

	// Verify the request body included our query parameters.
	var reqBody FundingRateHistoryRequest
	require.NoError(t, json.Unmarshal(capturedBody, &reqBody))
	require.Equal(t, int64(1), reqBody.FundingRateHistory.ProductID)
	require.Equal(t, int64(1700000000000), reqBody.FundingRateHistory.StartTime)
	require.Equal(t, int64(1700007200000), reqBody.FundingRateHistory.EndTime)
	require.Equal(t, 10, reqBody.FundingRateHistory.Limit)
}

// TestGetFundingRateHistoryEmpty verifies that an empty array response is valid.
func TestGetFundingRateHistoryEmpty(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
	}))
	defer srv.Close()

	c := NewClient().WithArchiveV1URL(srv.URL)
	hist, err := c.GetFundingRateHistory(context.Background(), 99, 0, 0, 0)
	require.NoError(t, err)
	require.Empty(t, hist)
}
