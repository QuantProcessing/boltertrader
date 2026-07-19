package lighter

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestClient_GetAccountActiveOrders(t *testing.T) {
	got, err := newLivePrivateClient(t).GetAccountActiveOrders(context.Background(), lighterMarketID(t))
	if err != nil {
		t.Fatalf("GetAccountActiveOrders: %v", err)
	}
	if got == nil {
		t.Fatal("expected active orders")
	}
}

func TestClientGetAccountActiveOrdersPreservesEnvelopeCodeWithoutMessage(t *testing.T) {
	const secret = "active-orders-secret-canary"
	client := NewClient().WithCredentials(strings.Repeat("01", 40), 66, 7)
	client.BaseURL = "https://lighter.test"
	client.HTTPClient = &http.Client{Transport: lighterOrderRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"code":429,"message":"active-orders-secret-canary"}`)),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})}

	_, err := client.GetAccountActiveOrders(context.Background(), 101)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want APIError", err)
	}
	if apiErr.Code != http.StatusTooManyRequests {
		t.Fatalf("code = %d, want %d", apiErr.Code, http.StatusTooManyRequests)
	}
	if apiErr.Message != "" || strings.Contains(err.Error(), secret) {
		t.Fatalf("envelope error exposed venue message: %v", err)
	}
}

func TestClient_GetNextNonce(t *testing.T) {
	nonce, err := newLivePrivateClient(t).GetNextNonce(context.Background())
	if err != nil {
		t.Fatalf("GetNextNonce: %v", err)
	}
	if nonce < 0 {
		t.Fatalf("unexpected nonce: %d", nonce)
	}
}

func TestClientGetNextNonceRequiresExplicitInRangeInteger(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "missing", body: `{"code":200}`},
		{name: "null", body: `{"code":200,"nonce":null}`},
		{name: "string", body: `{"code":200,"nonce":"1"}`},
		{name: "negative", body: `{"code":200,"nonce":-1}`},
		{name: "maximum", body: `{"code":200,"nonce":9223372036854775807}`},
		{name: "non-200 envelope", body: `{"code":0,"message":"nonce-secret-canary","nonce":1}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			client := NewClient()
			client.BaseURL = "https://lighter.test"
			client.HTTPClient = &http.Client{Transport: lighterOrderRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(test.body)),
					Header:     make(http.Header),
					Request:    req,
				}, nil
			})}
			_, err := client.GetNextNonce(context.Background())
			if !errors.Is(err, ErrMalformedResponse) {
				t.Fatalf("err = %v, want malformed response", err)
			}
			if strings.Contains(err.Error(), "nonce-secret-canary") {
				t.Fatal("malformed nonce error echoed the venue message")
			}
			if calls != 1 {
				t.Fatalf("HTTP calls = %d, want 1", calls)
			}
		})
	}
}

func TestClientGetNextNonceRejectsCachedMaxInt64(t *testing.T) {
	calls := 0
	client := NewClient()
	client.BaseURL = "https://lighter.test"
	client.HTTPClient = &http.Client{Transport: lighterOrderRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		body := `{"code":200,"nonce":9223372036854775806}`
		if calls == 2 {
			body = `{"code":200,"nonce":7}`
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})}
	first, err := client.GetNextNonce(context.Background())
	if err != nil {
		t.Fatalf("first GetNextNonce: %v", err)
	}
	_, err = client.GetNextNonce(context.Background())
	if !errors.Is(err, ErrMalformedResponse) {
		t.Fatalf("second err = %v, want malformed response", err)
	}
	third, err := client.GetNextNonce(context.Background())
	if err != nil {
		t.Fatalf("third GetNextNonce: %v", err)
	}
	if first != int64(^uint64(0)>>1)-1 || third != 7 || calls != 2 {
		t.Fatalf("nonces = %d, %d; HTTP calls = %d", first, third, calls)
	}
}

func TestClientGetNextNonceAcceptsExplicitZero(t *testing.T) {
	calls := 0
	client := NewClient()
	client.BaseURL = "https://lighter.test"
	client.HTTPClient = &http.Client{Transport: lighterOrderRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"code":200,"nonce":0}`)),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})}
	first, err := client.GetNextNonce(context.Background())
	if err != nil {
		t.Fatalf("first GetNextNonce: %v", err)
	}
	second, err := client.GetNextNonce(context.Background())
	if err != nil {
		t.Fatalf("second GetNextNonce: %v", err)
	}
	if first != 0 || second != 1 || calls != 1 {
		t.Fatalf("nonces = %d, %d; HTTP calls = %d", first, second, calls)
	}
}

func TestClient_GetAccount(t *testing.T) {
	got, err := newLivePrivateClient(t).GetAccount(context.Background())
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if got == nil {
		t.Fatal("expected account")
	}
}

func TestClient_GetInactiveOrders(t *testing.T) {
	marketID := lighterMarketID(t)
	got, err := newLivePrivateClient(t).GetInactiveOrders(context.Background(), &marketID, 10)
	if err != nil {
		t.Fatalf("GetInactiveOrders: %v", err)
	}
	if got == nil {
		t.Fatal("expected inactive orders")
	}
}

func TestClient_GetTradesUsesSignedTradesEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if r.URL.Path != "/api/v1/trades" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") == "" {
			t.Fatal("expected authorization header")
		}
		query := r.URL.Query()
		if query.Get("account_index") != "42" ||
			query.Get("market_id") != "7" ||
			query.Get("market_type") != "perp" ||
			query.Get("sort_by") != "trade_id" ||
			query.Get("sort_dir") != "desc" ||
			query.Get("type") != "all" ||
			query.Get("limit") != "50" {
			t.Fatalf("unexpected query: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"code":200,"trades":[{"trade_id":55,"trade_id_str":"55","market_id":7,"price":"100.25","size":"0.5","bid_id":100,"bid_id_str":"100","bid_client_id":42,"bid_client_id_str":"42","bid_account_id":42,"ask_id":101,"ask_id_str":"101","ask_account_id":77,"is_maker_ask":true,"taker_fee":12,"maker_fee":3,"transaction_time":1710000000001000}]}`))
	}))
	defer server.Close()

	marketID := 7
	accountIndex := int64(42)
	client := NewClient().WithCredentials(strings.Repeat("01", 40), accountIndex, 7)
	client.BaseURL = server.URL

	got, err := client.GetTrades(context.Background(), TradesRequest{
		MarketID:     &marketID,
		MarketType:   "perp",
		AccountIndex: &accountIndex,
		SortBy:       "trade_id",
		SortDir:      "desc",
		Type:         "all",
		Limit:        50,
	})
	if err != nil {
		t.Fatalf("GetTrades: %v", err)
	}
	if got == nil || len(got.Trades) != 1 {
		t.Fatalf("expected one trade, got %#v", got)
	}
	if got.Trades[0].TradeIdStr != "55" || got.Trades[0].BidAccountId != 42 {
		t.Fatalf("unexpected trade: %+v", got.Trades[0])
	}
}

func TestClient_GetTrades(t *testing.T) {
	marketID := lighterMarketID(t)
	client := newLivePrivateClient(t)
	accountIndex := client.AccountIndex
	got, err := client.GetTrades(context.Background(), TradesRequest{
		MarketID:     &marketID,
		MarketType:   "perp",
		AccountIndex: &accountIndex,
		SortBy:       "trade_id",
		SortDir:      "desc",
		Type:         "all",
		Limit:        10,
	})
	if err != nil {
		t.Fatalf("GetTrades: %v", err)
	}
	if got == nil {
		t.Fatal("expected trades")
	}
}

func TestClient_GetAccountTxs(t *testing.T) {
	got, err := newLivePrivateClient(t).GetAccountTxs(context.Background(), 10)
	if err != nil {
		t.Fatalf("GetAccountTxs: %v", err)
	}
	if got == nil {
		t.Fatal("expected account txs")
	}
}

func TestClient_GetPnL(t *testing.T) {
	end := time.Now().UnixMilli()
	start := end - int64(24*time.Hour/time.Millisecond)
	got, err := newLivePrivateClient(t).GetPnL(context.Background(), start, end)
	if err != nil {
		t.Fatalf("GetPnL: %v", err)
	}
	if got == nil {
		t.Fatal("expected pnl")
	}
}

func TestClient_GetAccountLimits(t *testing.T) {
	got, err := newLivePrivateClient(t).GetAccountLimits(context.Background())
	if err != nil {
		t.Fatalf("GetAccountLimits: %v", err)
	}
	if got == nil {
		t.Fatal("expected account limits")
	}
}

func TestClient_GetAccountMetadata(t *testing.T) {
	got, err := newLivePrivateClient(t).GetAccountMetadata(context.Background())
	if err != nil {
		t.Fatalf("GetAccountMetadata: %v", err)
	}
	if got == nil {
		t.Fatal("expected account metadata")
	}
}

func TestClient_ChangeAccountTier(t *testing.T) {
	client := requireLighterLiveWrite(t, "LIGHTER_TEST_ACCOUNT_TIER")
	got, err := client.ChangeAccountTier(context.Background(), os.Getenv("LIGHTER_TEST_ACCOUNT_TIER"))
	if err != nil {
		t.Fatalf("ChangeAccountTier: %v", err)
	}
	if got == nil {
		t.Fatal("expected account tier response")
	}
}

func TestClient_GetPositionFunding(t *testing.T) {
	marketID := lighterMarketID(t)
	got, err := newLivePrivateClient(t).GetPositionFunding(context.Background(), &marketID, 10, nil)
	if err != nil {
		t.Fatalf("GetPositionFunding: %v", err)
	}
	if got == nil {
		t.Fatal("expected position funding")
	}
}

func TestClient_GetApiKeys(t *testing.T) {
	got, err := newLivePrivateClient(t).GetApiKeys(context.Background())
	if err != nil {
		t.Fatalf("GetApiKeys: %v", err)
	}
	if got == nil {
		t.Fatal("expected api keys")
	}
}

func TestClient_GetReferralPoints(t *testing.T) {
	got, err := newLivePrivateClient(t).GetReferralPoints(context.Background())
	if err != nil {
		t.Fatalf("GetReferralPoints: %v", err)
	}
	if got == nil {
		t.Fatal("expected referral points")
	}
}

func TestClient_GetAccountsByL1Address(t *testing.T) {
	address := os.Getenv("LIGHTER_TEST_L1_ADDRESS")
	if address == "" {
		t.Skip("LIGHTER_TEST_L1_ADDRESS is required for GetAccountsByL1Address live test")
	}
	got, err := newLiveClient(t).GetAccountsByL1Address(context.Background(), address)
	if err != nil {
		t.Fatalf("GetAccountsByL1Address: %v", err)
	}
	if got == nil {
		t.Fatal("expected accounts by L1 address")
	}
}

func TestClient_UpdateLeverage(t *testing.T) {
	client := requireLighterLiveWrite(t)
	got, err := client.UpdateLeverage(
		context.Background(),
		lighterMarketID(t),
		uint16(lighterIntEnv(t, "LIGHTER_TEST_LEVERAGE", 2)),
		uint8(lighterIntEnv(t, "LIGHTER_TEST_MARGIN_MODE", 0)),
	)
	if err != nil {
		t.Fatalf("UpdateLeverage: %v", err)
	}
	if got == nil {
		t.Fatal("expected update leverage response")
	}
}
