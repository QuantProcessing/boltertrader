package perp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	hyperliquid "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
	"github.com/stretchr/testify/require"
)

const hyperliquidPerpCoin = "BTC"

func newLiveClient() *Client {
	return NewClient(hyperliquid.NewClient())
}

func TestClient_GetMetaAndAssetCtxs(t *testing.T) {
	meta, err := newLiveClient().GetMetaAndAssetCtxs(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, meta.Meta.Universe)
	require.NotEmpty(t, meta.AssetCtxs)
}

func TestClient_GetFundingRate(t *testing.T) {
	fundingRate, err := newLiveClient().GetFundingRate(context.Background(), hyperliquidPerpCoin)
	require.NoError(t, err)
	require.Equal(t, hyperliquidPerpCoin, fundingRate.Coin)
	require.NotEmpty(t, fundingRate.Funding)
}

func TestClient_GetFundingRateIncludesReferencePrices(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/info", r.URL.Path)
		_, _ = w.Write([]byte(`[{"universe":[{"name":"BTC"}]},[{"funding":"0.0001","markPx":"43000.10","oraclePx":"42990.20","premium":"0.0002","openInterest":"10"}]]`))
	}))
	defer srv.Close()

	base := hyperliquid.NewClient()
	base.BaseURL = srv.URL
	client := NewClient(base)
	fundingRate, err := client.GetFundingRate(context.Background(), hyperliquidPerpCoin)
	require.NoError(t, err)
	require.Equal(t, "0.0001", fundingRate.Funding)
	require.Equal(t, "43000.10", fundingRate.MarkPx)
	require.Equal(t, "42990.20", fundingRate.OraclePx)
	require.Equal(t, "0.0002", fundingRate.Premium)

	raw, err := json.Marshal(fundingRate)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "fundingIntervalHours")
	require.NotContains(t, string(raw), "fundingTime")
	require.NotContains(t, string(raw), "nextFundingTime")
}

func TestClient_GetPerpDexsBuildsRequest(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/info", r.URL.Path)
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, "perpDexs", body["type"])
		_, _ = w.Write([]byte(`[null,{"name":"xyz","fullName":"trade xyz"}]`))
	}))
	defer srv.Close()

	base := hyperliquid.NewClient()
	base.BaseURL = srv.URL
	client := NewClient(base)
	dexs, err := client.GetPerpDexs(context.Background())
	require.NoError(t, err)
	require.Equal(t, []PerpDex{{Index: 1, Name: "xyz", FullName: "trade xyz"}}, dexs)
}

func TestClient_GetPrepMetaForDexBuildsDexRequest(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/info", r.URL.Path)
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, "meta", body["type"])
		require.Equal(t, "xyz", body["dex"])
		_, _ = w.Write([]byte(`{"universe":[{"name":"xyz:TSLA","szDecimals":3,"maxLeverage":10}]}`))
	}))
	defer srv.Close()

	base := hyperliquid.NewClient()
	base.BaseURL = srv.URL
	client := NewClient(base)
	meta, err := client.GetPrepMetaForDex(context.Background(), "xyz")
	require.NoError(t, err)
	require.Len(t, meta.Universe, 1)
	require.Equal(t, "xyz:TSLA", meta.Universe[0].Name)
}

func TestClient_GetMetaAndAssetCtxsForDexBuildsDexRequest(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/info", r.URL.Path)
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, "metaAndAssetCtxs", body["type"])
		require.Equal(t, "xyz", body["dex"])
		_, _ = w.Write([]byte(`[{"universe":[{"name":"xyz:TSLA"}]},[{"funding":"0.0001","markPx":"100","oraclePx":"99"}]]`))
	}))
	defer srv.Close()

	base := hyperliquid.NewClient()
	base.BaseURL = srv.URL
	client := NewClient(base)
	meta, err := client.GetMetaAndAssetCtxsForDex(context.Background(), "xyz")
	require.NoError(t, err)
	require.Equal(t, "xyz:TSLA", meta.Meta.Universe[0].Name)
	require.Equal(t, "0.0001", meta.AssetCtxs[0].Funding)
}

func TestClient_GetFundingRateHistoryForDexBuildsDexRequest(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/info", r.URL.Path)
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, "fundingHistory", body["type"])
		require.Equal(t, "xyz", body["dex"])
		require.Equal(t, "xyz:TSLA", body["coin"])
		_, _ = w.Write([]byte(`[{"coin":"xyz:TSLA","fundingRate":"0.0001","time":1710000000000}]`))
	}))
	defer srv.Close()

	base := hyperliquid.NewClient()
	base.BaseURL = srv.URL
	client := NewClient(base)
	rows, err := client.GetFundingRateHistoryForDex(context.Background(), "xyz", "xyz:TSLA", 1, 2)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "xyz:TSLA", rows[0].Coin)
}

func TestClient_GetFundingRate_InvalidCoin(t *testing.T) {
	_, err := newLiveClient().GetFundingRate(context.Background(), "INVALID_COIN_XYZ")
	require.Error(t, err)
	require.Contains(t, err.Error(), "funding rate not found")
}

func TestClient_GetAllFundingRates(t *testing.T) {
	rates, err := newLiveClient().GetAllFundingRates(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, rates)
}

func TestClient_L2Book(t *testing.T) {
	book, err := newLiveClient().L2Book(context.Background(), hyperliquidPerpCoin)
	require.NoError(t, err)
	require.Equal(t, hyperliquidPerpCoin, book.Coin)
	require.NotEmpty(t, book.Levels)
}

func TestClient_AllMids(t *testing.T) {
	mids, err := newLiveClient().AllMids(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, mids)
}

func TestClient_CandleSnapshot(t *testing.T) {
	end := time.Now().UnixMilli()
	start := end - int64(time.Hour/time.Millisecond)

	candles, err := newLiveClient().CandleSnapshot(context.Background(), hyperliquidPerpCoin, "1m", start, end)
	require.NoError(t, err)
	require.NotEmpty(t, candles)
}

func TestClient_GetPrepMeta(t *testing.T) {
	meta, err := newLiveClient().GetPrepMeta(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, meta.Universe)
}

func TestClient_GetFundingRateHistory(t *testing.T) {
	end := time.Now().UnixMilli()
	start := end - int64(24*time.Hour/time.Millisecond)

	hist, err := newLiveClient().GetFundingRateHistory(context.Background(), hyperliquidPerpCoin, start, end)
	require.NoError(t, err)
	require.NotEmpty(t, hist)
}
