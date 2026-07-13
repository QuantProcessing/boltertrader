package spot

import (
	"context"
	"testing"

	"github.com/QuantProcessing/boltertrader/internal/testenv"
	astercommon "github.com/QuantProcessing/boltertrader/sdk/aster/common"
)

func TestPublicEndpoints(t *testing.T) {
	testenv.RequireLiveRead(t)
	profile, err := astercommon.NewProfile(astercommon.EnvironmentProduction, astercommon.ProductSpot)
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(profile, nil)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("ServerTime", func(t *testing.T) {
		// We don't have a direct ServerTime method exposed on Client yet,
		// but we can test connectivity via Ticker or Depth which are public.
		// Or we can add CheckServerTime to client.
	})

	t.Run("Depth", func(t *testing.T) {
		res, err := client.Depth(context.Background(), "BTCUSDT", 5)
		if err != nil {
			t.Fatalf("Depth failed: %v", err)
		}
		if len(res.Bids) == 0 || len(res.Asks) == 0 {
			t.Errorf("Depth returned empty bids/asks")
		}
	})

	t.Run("Ticker", func(t *testing.T) {
		res, err := client.Ticker(context.Background(), "BTCUSDT")
		if err != nil {
			t.Fatalf("Ticker failed: %v", err)
		}
		if res.Symbol != "BTCUSDT" {
			t.Errorf("Expected symbol BTCUSDT, got %s", res.Symbol)
		}
	})

	t.Run("Klines", func(t *testing.T) {
		res, err := client.Klines(context.Background(), "BTCUSDT", "1m", 5, 0, 0)
		if err != nil {
			t.Fatalf("Klines failed: %v", err)
		}
		if len(res) == 0 {
			t.Errorf("Klines returned empty list")
		}
	})
}
