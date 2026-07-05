package spot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClient_GetAccount(t *testing.T) {
	got, err := newLivePrivateClient(t).GetAccount(context.Background())
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if got.AccountType == "" {
		t.Fatalf("unexpected account response: %+v", got)
	}
}

func TestClient_GetAccountPath(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/account", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method=%s, want GET", r.Method)
		}
		if r.URL.Query().Get("timestamp") == "" || r.URL.Query().Get("signature") == "" {
			t.Fatalf("signed account request missing timestamp/signature: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"accountType":"SPOT","balances":[{"asset":"USDT","free":"1","locked":"0"}]}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	got, err := NewClient().
		WithBaseURL(server.URL).
		WithCredentials("key", "secret").
		WithRateLimiter(nil).
		GetAccount(context.Background())
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if got.AccountType != "SPOT" || len(got.Balances) != 1 {
		t.Fatalf("unexpected account response: %+v", got)
	}
}

func TestClient_StartUserDataStream(t *testing.T) {
	listenKey, err := requireBinanceSpotLiveWrite(t).StartUserDataStream(context.Background())
	if err != nil {
		t.Fatalf("StartUserDataStream: %v", err)
	}
	if listenKey == "" {
		t.Fatal("expected listen key")
	}
}

func TestClient_KeepAliveUserDataStream(t *testing.T) {
	client := requireBinanceSpotLiveWrite(t)
	listenKey, err := client.StartUserDataStream(context.Background())
	if err != nil {
		t.Fatalf("StartUserDataStream: %v", err)
	}
	t.Cleanup(func() {
		_ = client.CloseUserDataStream(context.Background(), listenKey)
	})
	if err := client.KeepAliveUserDataStream(context.Background(), listenKey); err != nil {
		t.Fatalf("KeepAliveUserDataStream: %v", err)
	}
}

func TestClient_CloseUserDataStream(t *testing.T) {
	client := requireBinanceSpotLiveWrite(t)
	listenKey, err := client.StartUserDataStream(context.Background())
	if err != nil {
		t.Fatalf("StartUserDataStream: %v", err)
	}
	if err := client.CloseUserDataStream(context.Background(), listenKey); err != nil {
		t.Fatalf("CloseUserDataStream: %v", err)
	}
}
