package perp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/internal/testenv"
)

const (
	binancePerpLiveWriteFlag = "BINANCE_PERP_ENABLE_LIVE_WRITE_TESTS"
	binancePerpTestSymbol    = "BTCUSDT"
)

func newLiveClient(t *testing.T) *Client {
	t.Helper()
	testenv.RequireLiveRead(t)
	return NewClient()
}

func newLivePrivateClient(t *testing.T) *Client {
	t.Helper()
	testenv.RequireLiveRead(t, "BINANCE_API_KEY", "BINANCE_SECRET_KEY")
	return NewClient().WithCredentials(os.Getenv("BINANCE_API_KEY"), os.Getenv("BINANCE_SECRET_KEY"))
}

func requireBinancePerpLiveWrite(t *testing.T) *Client {
	t.Helper()
	testenv.RequireLiveWrite(t, binancePerpLiveWriteFlag, "BINANCE_API_KEY", "BINANCE_SECRET_KEY")
	return NewClient().WithCredentials(os.Getenv("BINANCE_API_KEY"), os.Getenv("BINANCE_SECRET_KEY"))
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func TestClient_WithCredentials(t *testing.T) {
	client := NewClient().WithCredentials("key", "secret")

	if client.APIKey != "key" || client.SecretKey != "secret" {
		t.Fatalf("unexpected credentials: %+v", client)
	}
}

func TestClient_WithBaseURL(t *testing.T) {
	client := NewClient().WithBaseURL("https://example.test")

	if client.BaseURL != "https://example.test" {
		t.Fatalf("unexpected base url: %+v", client)
	}
}

func TestClient_WithUSDMMDemoUsesDemoFAPI(t *testing.T) {
	client := NewClient().WithUSDMMDemo()

	if client.BaseURL != DemoBaseURL {
		t.Fatalf("expected Demo REST base URL %s, got %s", DemoBaseURL, client.BaseURL)
	}
	if client.EndpointPrefix != "/fapi" {
		t.Fatalf("expected USD-M endpoint prefix /fapi, got %s", client.EndpointPrefix)
	}
	if client.AccountVersion != "v2" {
		t.Fatalf("expected USD-M account version v2, got %s", client.AccountVersion)
	}
}

func TestClient_CoinMRoutesRESTMethodsToDAPI(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/dapi/v1/depth", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("symbol"); got != "BTCUSD_PERP" {
			t.Fatalf("unexpected depth symbol: %s", got)
		}
		_, _ = w.Write([]byte(`{"lastUpdateId":1,"E":2,"T":3,"bids":[["100","1"]],"asks":[["101","1"]]}`))
	})
	mux.HandleFunc("/dapi/v1/order", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Method; got != http.MethodPost {
			t.Fatalf("unexpected order method: %s", got)
		}
		if got := r.URL.Query().Get("symbol"); got != "BTCUSD_PERP" {
			t.Fatalf("unexpected order symbol: %s", got)
		}
		_, _ = w.Write([]byte(`{"symbol":"BTCUSD_PERP","orderId":12,"clientOrderId":"coin-client","status":"NEW","side":"BUY","type":"LIMIT","timeInForce":"GTC","origQty":"1","executedQty":"0","price":"100"}`))
	})
	mux.HandleFunc("/dapi/v1/account", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"feeTier":0,"canTrade":true,"assets":[],"positions":[]}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client := NewCoinMClient().WithBaseURL(server.URL).WithCredentials("key", "secret")
	if client.BaseURL != server.URL {
		t.Fatalf("WithBaseURL did not override Coin-M base url: %s", client.BaseURL)
	}

	if _, err := client.Depth(context.Background(), "BTCUSD_PERP", 5); err != nil {
		t.Fatalf("Depth: %v", err)
	}
	order, err := client.PlaceOrder(context.Background(), PlaceOrderParams{
		Symbol:           "BTCUSD_PERP",
		Side:             "BUY",
		Type:             "LIMIT",
		TimeInForce:      "GTC",
		Quantity:         "1",
		Price:            "100",
		NewClientOrderID: "coin-client",
	})
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	if order.OrderID != 12 {
		t.Fatalf("unexpected order response: %+v", order)
	}
	if _, err := client.GetAccount(context.Background()); err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
}

func TestClient_DefaultHTTPTimeout(t *testing.T) {
	client := NewClient()
	if client.HTTPClient.Timeout <= 0 {
		t.Fatal("expected default HTTP timeout")
	}
}

func TestClient_WithHTTPClient(t *testing.T) {
	httpClient := &http.Client{Timeout: 42 * time.Second}
	client := NewClient().WithHTTPClient(httpClient)
	if client.HTTPClient != httpClient {
		t.Fatal("WithHTTPClient did not install provided client")
	}
}

func TestClient_Get(t *testing.T) {
	var out struct {
		ServerTime int64 `json:"serverTime"`
	}
	if err := newLiveClient(t).Get(context.Background(), "/fapi/v1/time", nil, false, &out); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if out.ServerTime == 0 {
		t.Fatalf("unexpected server time response: %+v", out)
	}
}

func TestClient_Post(t *testing.T) {
	client := requireBinancePerpLiveWrite(t)
	if _, err := client.CreateListenKey(context.Background()); err != nil {
		t.Fatalf("Post via CreateListenKey: %v", err)
	}
}

func TestClient_Delete(t *testing.T) {
	client := requireBinancePerpLiveWrite(t)
	if err := client.CloseListenKey(context.Background()); err != nil {
		t.Fatalf("Delete via CloseListenKey: %v", err)
	}
}

func TestClient_Put(t *testing.T) {
	client := requireBinancePerpLiveWrite(t)
	if err := client.KeepAliveListenKey(context.Background()); err != nil {
		t.Fatalf("Put via KeepAliveListenKey: %v", err)
	}
}
