package spot

import (
	"context"
	"os"
	"testing"

	"github.com/QuantProcessing/exchanges/internal/testenv"
)

const binanceSpotLiveWriteFlag = "BINANCE_ENABLE_LIVE_WRITE_TESTS"

func newLiveClient() *Client {
	return NewClient()
}

func newLivePrivateClient(t *testing.T) *Client {
	t.Helper()
	testenv.RequireLiveCredentials(t, "BINANCE_API_KEY", "BINANCE_SECRET_KEY")
	return NewClient().WithCredentials(
		os.Getenv("BINANCE_API_KEY"),
		os.Getenv("BINANCE_SECRET_KEY"),
	)
}

func requireBinanceSpotLiveWrite(t *testing.T) *Client {
	t.Helper()
	testenv.RequireLiveWrite(t, binanceSpotLiveWriteFlag, "BINANCE_API_KEY", "BINANCE_SECRET_KEY")
	return newLivePrivateClient(t)
}

func TestClient_WithCredentials(t *testing.T) {
	client := NewClient().WithCredentials("key", "secret")

	if client.APIKey != "key" || client.SecretKey != "secret" {
		t.Fatalf("unexpected credentials: %+v", client)
	}
}

func TestClient_WithBaseURL(t *testing.T) {
	client := NewClient().WithBaseURL("https://unit.test")

	if client.BaseURL != "https://unit.test" {
		t.Fatalf("unexpected baseURL: %s", client.BaseURL)
	}
}

func TestClient_Get(t *testing.T) {
	var out struct {
		ServerTime int64 `json:"serverTime"`
	}
	if err := newLiveClient().Get(context.Background(), "/api/v3/time", nil, false, &out); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if out.ServerTime == 0 {
		t.Fatalf("unexpected server time response: %+v", out)
	}
}

func TestClient_Post(t *testing.T) {
	client := requireBinanceSpotLiveWrite(t)
	if _, err := client.StartUserDataStream(context.Background()); err != nil {
		t.Fatalf("Post via StartUserDataStream: %v", err)
	}
}

func TestClient_Delete(t *testing.T) {
	client := requireBinanceSpotLiveWrite(t)
	listenKey, err := client.StartUserDataStream(context.Background())
	if err != nil {
		t.Fatalf("StartUserDataStream: %v", err)
	}
	if err := client.CloseUserDataStream(context.Background(), listenKey); err != nil {
		t.Fatalf("Delete via CloseUserDataStream: %v", err)
	}
}

func TestClient_Put(t *testing.T) {
	client := requireBinanceSpotLiveWrite(t)
	listenKey, err := client.StartUserDataStream(context.Background())
	if err != nil {
		t.Fatalf("StartUserDataStream: %v", err)
	}
	t.Cleanup(func() {
		_ = client.CloseUserDataStream(context.Background(), listenKey)
	})
	if err := client.KeepAliveUserDataStream(context.Background(), listenKey); err != nil {
		t.Fatalf("Put via KeepAliveUserDataStream: %v", err)
	}
}
