package nado

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/internal/testenv"
	"github.com/stretchr/testify/require"
)

func requireFullEnv(t *testing.T) {
	t.Helper()
	testenv.RequireLiveRead(t, "NADO_TESTNET_PRIVATE_KEY")
}

func requireWriteEnv(t *testing.T) {
	t.Helper()
	testenv.RequireLiveWrite(t, "BOLTER_ENABLE_NADO_UNSAFE_RAW_SDK_WRITES", "NADO_TESTNET_PRIVATE_KEY")
}

func GetEnv() (string, string) {
	pk := os.Getenv("NADO_TESTNET_PRIVATE_KEY")
	subaccount := os.Getenv("NADO_TESTNET_SUBACCOUNT_NAME")
	if strings.TrimSpace(subaccount) == "" {
		subaccount = "default"
	}
	return pk, subaccount
}

func newNadoTestnetClient(t *testing.T) *Client {
	t.Helper()
	profile, err := NewProfile(EnvironmentTestnet)
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(profile)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func newNadoCredentialClient(t *testing.T) *Client {
	t.Helper()
	privateKey, subaccount := GetEnv()
	client, err := newNadoTestnetClient(t).WithCredentials(privateKey, subaccount)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func newNadoClientForServer(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	client := newNadoTestnetClient(t)
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := server.Client().Transport
	client.WithHTTPClient(&http.Client{Transport: nadoRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		clone := request.Clone(request.Context())
		clone.URL.Scheme = target.Scheme
		clone.URL.Host = target.Host
		clone.Host = target.Host
		return transport.RoundTrip(clone)
	})})
	return client
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
	client := newNadoCredentialClient(t)
	nonces, err := client.GetNonces(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("nonces=%+v", nonces)
}

func TestGetCandlesticks(t *testing.T) {
	requireFullEnv(t)
	client := newNadoCredentialClient(t)
	candlesticks := retryNadoPublic(t, "GetCandlesticks", func() ([]ArchiveCandlestick, error) {
		return client.GetCandlesticks(context.Background(), CandlestickRequest{
			Candlesticks: Candlesticks{
				ProductID:   1,
				Granularity: 60,
				Limit:       10,
			},
		})
	})
	t.Logf("candlesticks=%+v", candlesticks)
}

func TestGetContracts(t *testing.T) {
	testenv.RequireLiveRead(t)
	client := newNadoTestnetClient(t)
	contracts, err := client.GetContracts(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("contracts=%d", len(contracts))
}

func TestGetTickers(t *testing.T) {
	testenv.RequireLiveRead(t)
	client := newNadoTestnetClient(t)
	tickers, err := client.GetTickers(context.Background(), MarketTypePerp, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("tickers=%d", len(tickers))
}

// TestGetFundingRate tests the GetFundingRate method
func TestGetFundingRate(t *testing.T) {
	testenv.RequireLiveRead(t)

	client := newNadoTestnetClient(t)
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
	testenv.RequireLiveRead(t)

	client := newNadoTestnetClient(t)
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

func TestGetPerpPricePreservesX18AndSourceTime(t *testing.T) {
	t.Parallel()

	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&captured))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"product_id": 2,
			"index_price_x18": "31483202055051853950444",
			"mark_price_x18": "31514830401018841708801",
			"update_time": "1689281222"
		}`)
	}))
	defer srv.Close()

	client := newNadoClientForServer(t, srv)
	price, err := client.GetPerpPrice(context.Background(), 2)
	require.NoError(t, err)
	require.Equal(t, int64(2), price.ProductID)
	require.Equal(t, "31483202055051853950444", price.IndexPriceX18)
	require.Equal(t, "31514830401018841708801", price.MarkPriceX18)
	require.Equal(t, "1689281222", price.UpdateTime)
	require.Equal(t, map[string]any{"price": map[string]any{"product_id": float64(2)}}, captured)
}

func TestGetOraclePricesPreservesX18AndPerProductSourceTime(t *testing.T) {
	t.Parallel()

	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&captured))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"prices": [
				{"product_id": 1, "oracle_price_x18": "29464023750000000000000", "update_time": "1683315718"},
				{"product_id": 2, "oracle_price_x18": "29430225194712740000000", "update_time": "1683315721"}
			]
		}`)
	}))
	defer srv.Close()

	client := newNadoClientForServer(t, srv)
	prices, err := client.GetOraclePrices(context.Background(), []int64{1, 2})
	require.NoError(t, err)
	require.Len(t, prices, 2)
	require.Equal(t, OraclePriceResponse{
		ProductID:      2,
		OraclePriceX18: "29430225194712740000000",
		UpdateTime:     "1683315721",
	}, prices[1])
	require.Equal(t, map[string]any{
		"oracle_price": map[string]any{"product_ids": []any{float64(1), float64(2)}},
	}, captured)
}
