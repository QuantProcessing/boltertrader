package margin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"

	"github.com/QuantProcessing/exchanges/internal/testenv"
)

func TestClient_GetAccount(t *testing.T) {
	got, err := newLivePrivateClient(t).GetAccount(context.Background())
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Binance margin account endpoint")
		t.Fatalf("GetAccount: %v", err)
	}
	if got == nil {
		t.Fatal("expected margin account")
	}
}

func TestClient_GetIsolatedAccount(t *testing.T) {
	got, err := newLivePrivateClient(t).GetIsolatedAccount(context.Background(), marginEnvOrDefault("BINANCE_MARGIN_TEST_SYMBOLS", "BTCUSDT"))
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Binance isolated margin account endpoint")
		t.Fatalf("GetIsolatedAccount: %v", err)
	}
	if got == nil {
		t.Fatal("expected isolated margin account")
	}
}

func TestClient_Borrow(t *testing.T) {
	client := requireBinanceMarginLiveWrite(t, "BINANCE_MARGIN_TEST_ASSET", "BINANCE_MARGIN_TEST_AMOUNT")
	amount, err := strconv.ParseFloat(os.Getenv("BINANCE_MARGIN_TEST_AMOUNT"), 64)
	if err != nil {
		t.Fatalf("parse BINANCE_MARGIN_TEST_AMOUNT: %v", err)
	}

	tranID, err := client.Borrow(
		context.Background(),
		os.Getenv("BINANCE_MARGIN_TEST_ASSET"),
		amount,
		os.Getenv("BINANCE_MARGIN_TEST_ISOLATED") == "1",
		os.Getenv("BINANCE_MARGIN_TEST_SYMBOL"),
	)
	if err != nil {
		t.Fatalf("Borrow: %v", err)
	}
	if tranID == 0 {
		t.Fatal("expected transaction id")
	}
}

func TestClient_BorrowBuildsMarginLoanRequest(t *testing.T) {
	t.Parallel()

	var sawLoan bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/time":
			_, _ = w.Write([]byte(`{"serverTime":1234567890}`))
		case "/sapi/v1/margin/loan":
			sawLoan = true
			if r.Method != http.MethodPost {
				t.Fatalf("unexpected method: %s", r.Method)
			}
			query := r.URL.Query()
			for key, want := range map[string]string{
				"asset":      "USDT",
				"amount":     "1.25",
				"isIsolated": "TRUE",
				"symbol":     "BTCUSDT",
				"timestamp":  "1234567890",
				"recvWindow": "60000",
			} {
				if got := query.Get(key); got != want {
					t.Fatalf("unexpected %s: got %q want %q (query=%s)", key, got, want, r.URL.RawQuery)
				}
			}
			if query.Get("signature") == "" {
				t.Fatalf("missing signature: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"tranId":123}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClient().
		WithCredentials("key", "secret").
		WithBaseURL(server.URL).
		WithServerTimeBaseURL(server.URL)
	tranID, err := client.Borrow(context.Background(), "USDT", 1.25, true, "BTCUSDT")
	if err != nil {
		t.Fatalf("Borrow: %v", err)
	}
	if tranID != 123 || !sawLoan {
		t.Fatalf("unexpected borrow result: tranID=%d sawLoan=%v", tranID, sawLoan)
	}
}

func TestClient_Repay(t *testing.T) {
	client := requireBinanceMarginLiveWrite(t, "BINANCE_MARGIN_TEST_ASSET", "BINANCE_MARGIN_TEST_AMOUNT")
	amount, err := strconv.ParseFloat(os.Getenv("BINANCE_MARGIN_TEST_AMOUNT"), 64)
	if err != nil {
		t.Fatalf("parse BINANCE_MARGIN_TEST_AMOUNT: %v", err)
	}

	tranID, err := client.Repay(
		context.Background(),
		os.Getenv("BINANCE_MARGIN_TEST_ASSET"),
		amount,
		os.Getenv("BINANCE_MARGIN_TEST_ISOLATED") == "1",
		os.Getenv("BINANCE_MARGIN_TEST_SYMBOL"),
	)
	if err != nil {
		t.Fatalf("Repay: %v", err)
	}
	if tranID == 0 {
		t.Fatal("expected transaction id")
	}
}

func TestClient_RepayBuildsMarginRepayRequest(t *testing.T) {
	t.Parallel()

	var sawRepay bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/time":
			_, _ = w.Write([]byte(`{"serverTime":1234567890}`))
		case "/sapi/v1/margin/repay":
			sawRepay = true
			if r.Method != http.MethodPost {
				t.Fatalf("unexpected method: %s", r.Method)
			}
			query := r.URL.Query()
			for key, want := range map[string]string{
				"asset":      "USDT",
				"amount":     "1.25",
				"timestamp":  "1234567890",
				"recvWindow": "60000",
			} {
				if got := query.Get(key); got != want {
					t.Fatalf("unexpected %s: got %q want %q (query=%s)", key, got, want, r.URL.RawQuery)
				}
			}
			if query.Get("isIsolated") != "" || query.Get("symbol") != "" {
				t.Fatalf("unexpected isolated repay params: %s", r.URL.RawQuery)
			}
			if query.Get("signature") == "" {
				t.Fatalf("missing signature: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"tranId":456}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClient().
		WithCredentials("key", "secret").
		WithBaseURL(server.URL).
		WithServerTimeBaseURL(server.URL)
	tranID, err := client.Repay(context.Background(), "USDT", 1.25, false, "")
	if err != nil {
		t.Fatalf("Repay: %v", err)
	}
	if tranID != 456 || !sawRepay {
		t.Fatalf("unexpected repay result: tranID=%d sawRepay=%v", tranID, sawRepay)
	}
}
