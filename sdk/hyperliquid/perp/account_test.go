package perp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"

	hyperliquid "github.com/QuantProcessing/exchanges/sdk/hyperliquid"
	"github.com/stretchr/testify/require"
)

func TestClient_UserFills(t *testing.T) {
	account := os.Getenv("HYPERLIQUID_ACCOUNT_ADDR")
	client := newLivePrivateClient(t)

	fills, err := client.UserFills(context.Background(), account)
	require.NoError(t, err)
	require.NotNil(t, fills)
}

func TestClient_GetPerpPosition(t *testing.T) {
	position, err := newLivePrivateClient(t).GetPerpPosition(context.Background())
	require.NoError(t, err)
	require.NotNil(t, position)
}

func TestClient_GetBalance(t *testing.T) {
	balance, err := newLivePrivateClient(t).GetBalance(context.Background())
	require.NoError(t, err)
	require.NotNil(t, balance)
}

func TestClient_GetBalanceForDexBuildsDexRequest(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/info", r.URL.Path)
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, "clearinghouseState", body["type"])
		require.Equal(t, "0xabc", body["user"])
		require.Equal(t, "xyz", body["dex"])
		_, _ = w.Write([]byte(`{"withdrawable":"5","marginSummary":{"accountValue":"10"},"assetPositions":[]}`))
	}))
	defer srv.Close()

	base := hyperliquid.NewClient().WithAccount("0xabc")
	base.BaseURL = srv.URL
	client := NewClient(base)
	balance, err := client.GetBalanceForDex(context.Background(), "xyz")
	require.NoError(t, err)
	require.Equal(t, "5", balance.Withdrawable)
}

func TestClient_UpdateLeverage(t *testing.T) {
	client := requireHyperliquidLiveWrite(t, "HYPERLIQUID_PERP_TEST_ASSET_ID")
	err := client.UpdateLeverage(context.Background(), UpdateLeverageRequest{
		AssetID:  hyperliquidPerpAssetID(t),
		IsCross:  hyperliquidBoolEnv("HYPERLIQUID_TEST_IS_CROSS", true),
		Leverage: hyperliquidIntEnv("HYPERLIQUID_TEST_LEVERAGE", 2),
	})
	require.NoError(t, err)
}

func TestClient_UpdateIsolatedMargin(t *testing.T) {
	client := requireHyperliquidLiveWrite(t, "HYPERLIQUID_PERP_TEST_ASSET_ID", "HYPERLIQUID_TEST_MARGIN_AMOUNT")
	amount, err := strconv.ParseFloat(os.Getenv("HYPERLIQUID_TEST_MARGIN_AMOUNT"), 64)
	require.NoError(t, err)

	err = client.UpdateIsolatedMargin(context.Background(), UpdateIsolatedMarginRequest{
		AssetID: hyperliquidPerpAssetID(t),
		IsBuy:   hyperliquidBoolEnv("HYPERLIQUID_TEST_IS_BUY", true),
		Amount:  amount,
	})
	require.NoError(t, err)
}

func hyperliquidPerpAssetID(t *testing.T) int {
	t.Helper()
	value, err := strconv.Atoi(os.Getenv("HYPERLIQUID_PERP_TEST_ASSET_ID"))
	require.NoError(t, err)
	return value
}

func hyperliquidBoolEnv(key string, fallback bool) bool {
	if raw := os.Getenv(key); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err == nil {
			return value
		}
	}
	return fallback
}

func hyperliquidIntEnv(key string, fallback int) int {
	if raw := os.Getenv(key); raw != "" {
		value, err := strconv.Atoi(raw)
		if err == nil {
			return value
		}
	}
	return fallback
}
