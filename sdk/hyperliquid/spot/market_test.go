package spot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	hyperliquid "github.com/QuantProcessing/exchanges/sdk/hyperliquid"
	"github.com/stretchr/testify/require"
)

func TestClient_GetSpotMeta(t *testing.T) {
	meta, err := newLiveClient().GetSpotMeta(context.Background())
	if err != nil {
		t.Fatalf("GetSpotMeta: %v", err)
	}
	if len(meta.Tokens) == 0 || len(meta.Universe) == 0 {
		t.Fatalf("unexpected meta: %+v", meta)
	}
}

func TestClient_GetOutcomeMetaBuildsRequest(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/info", r.URL.Path)
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, "outcomeMeta", body["type"])
		_, _ = w.Write([]byte(`{"outcomes":[{"outcome":123,"name":"Recurring","description":"class:priceBinary|underlying:BTC","sideSpecs":[{"name":"Yes"},{"name":"No"}]}]}`))
	}))
	defer srv.Close()

	base := hyperliquid.NewClient()
	base.BaseURL = srv.URL
	client := NewClient(base)
	meta, err := client.GetOutcomeMeta(context.Background())
	require.NoError(t, err)
	require.Len(t, meta.Outcomes, 1)
	require.Equal(t, 123, meta.Outcomes[0].Outcome)
	require.Equal(t, "Yes", meta.Outcomes[0].SideSpecs[0].Name)
}

func TestClient_L2Book(t *testing.T) {
	coin := hyperliquidEnvOrDefault("HYPERLIQUID_SPOT_TEST_COIN", hyperliquidSpotCoin)
	book, err := newLiveClient().L2Book(context.Background(), coin)
	if err != nil {
		t.Fatalf("L2Book: %v", err)
	}
	if book.Coin != coin || len(book.Levels) == 0 {
		t.Fatalf("unexpected book: %+v", book)
	}
}

func TestClient_AllMids(t *testing.T) {
	mids, err := newLiveClient().AllMids(context.Background())
	if err != nil {
		t.Fatalf("AllMids: %v", err)
	}
	if len(mids) == 0 {
		t.Fatal("expected mids")
	}
}

func TestClient_CandleSnapshot(t *testing.T) {
	coin := hyperliquidEnvOrDefault("HYPERLIQUID_SPOT_TEST_COIN", hyperliquidSpotCoin)
	end := time.Now().UnixMilli()
	start := end - int64(time.Hour/time.Millisecond)

	candles, err := newLiveClient().CandleSnapshot(context.Background(), coin, "1m", start, end)
	if err != nil {
		t.Fatalf("CandleSnapshot: %v", err)
	}
	if len(candles) == 0 {
		t.Fatal("expected candles")
	}
}
