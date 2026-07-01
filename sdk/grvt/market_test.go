package grvt

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientGetTradeIncludesLimit(t *testing.T) {
	var seen GetTradeRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/lite/v1/trade" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&seen); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_, _ = w.Write([]byte(`{"r":[]}`))
	}))
	defer server.Close()

	client := NewClient()
	client.MarketDataURL = server.URL

	_, err := client.GetTrade(context.Background(), "BTC_USDT_Perp", 50)
	if err != nil {
		t.Fatalf("GetTrade: %v", err)
	}
	if seen.Instrument != "BTC_USDT_Perp" {
		t.Fatalf("instrument = %q", seen.Instrument)
	}
	if seen.Limit != 50 {
		t.Fatalf("limit = %d, want 50", seen.Limit)
	}
}

// TestGetTickerFundingFields tests retrieving raw current funding fields from ticker.
func TestGetTickerFundingFields(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	client := newLiveClient(t)
	ctx := context.Background()

	var ticker *GetTickerResponse
	retryGRVTLive(t, "GetTicker", func() error {
		var err error
		ticker, err = client.GetTicker(ctx, "BTC_USDT_Perp")
		return err
	})

	if ticker == nil {
		t.Fatal("Expected ticker, got nil")
	}

	if ticker.Result.Instrument == "" {
		t.Error("Expected non-empty instrument")
	}

	if ticker.Result.FundingRate == "" {
		t.Error("Expected non-empty funding rate")
	}

	t.Logf("Instrument: %s", ticker.Result.Instrument)
	t.Logf("Funding rate: %s", ticker.Result.FundingRate)
	t.Logf("Next funding time: %s", ticker.Result.NextFundingTime)
}
