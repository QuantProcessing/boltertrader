package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/account"
	"github.com/QuantProcessing/boltertrader/gateway/hub"
	"github.com/QuantProcessing/boltertrader/gateway/market"
	"github.com/QuantProcessing/boltertrader/gateway/store"
	"github.com/QuantProcessing/boltertrader/secretstore"
	"github.com/shopspring/decimal"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	cipher, _ := secretstore.NewCipher(bytes.Repeat([]byte{7}, 32))
	accounts, err := store.Open(filepath.Join(t.TempDir(), "accounts.enc"), cipher)
	if err != nil {
		t.Fatal(err)
	}
	catalog := market.NewStaticCatalog([]exchange.Instrument{{
		Identity: exchange.InstrumentIdentity{Venue: exchange.VenueBinance, Product: exchange.ProductPerp, Symbol: "BTC-USDT",
			BaseAsset: "BTC", QuoteAsset: "USDT", SettleAsset: "USDT", NativeSymbol: "BTCUSDT"},
		Rules: exchange.InstrumentRules{PriceIncrement: decimal.RequireFromString("0.1"), QuantityIncrement: decimal.RequireFromString("0.001"), MinQuantity: decimal.RequireFromString("0.001")},
	}})
	manager := account.NewManager(accounts, catalog, http.DefaultClient)
	feeds := market.NewFeeds(catalog)
	public := hub.New(feeds.Source, time.Second)
	private := hub.New(manager.Source, time.Second)
	t.Cleanup(func() { public.Close(); private.Close() })
	return NewServer([]byte("test-token-0123456789abcdef0123456789"), catalog, manager, feeds, public, private)
}

func do(server *Server, method, path, token, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	return recorder
}

const token = "test-token-0123456789abcdef0123456789"

func TestAuthentication(t *testing.T) {
	server := newTestServer(t)
	if got := do(server, "GET", "/v1/health", "", "").Code; got != 200 {
		t.Fatalf("health = %d", got)
	}
	for _, bad := range []string{"", "wrong"} {
		if got := do(server, "GET", "/v1/instruments", bad, "").Code; got != 401 {
			t.Fatalf("token %q = %d", bad, got)
		}
	}
	response := do(server, "GET", "/v1/instruments", token, "")
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"id":"binance:perp:BTC-USDT"`) {
		t.Fatalf("instruments = %d %s", response.Code, response.Body)
	}
}

func TestAccountErrors(t *testing.T) {
	server := newTestServer(t)
	cases := []struct {
		method, path, body string
		want               int
	}{
		{"GET", "/v1/accounts/acc_missing/positions", "", 404},
		{"POST", "/v1/accounts/acc_missing/orders", `{"inst":"binance:perp:BTC-USDT","side":"buy","type":"market","qty":"1","clientOrderId":"1"}`, 404},
		{"POST", "/v1/accounts", `{"venue":"nope","credentials":{}}`, 400},
		{"POST", "/v1/accounts", `{"venue":"binance","credentials":{"apiKey":"k"}}`, 400},
		{"POST", "/v1/accounts", `{"venue":"binance","credentials":{"apiKey":"k","apiSecret":"s","extra":"x"}}`, 400},
		{"POST", "/v1/accounts", `{"venue":"binance","unexpected":1}`, 400},
		{"DELETE", "/v1/accounts/acc_missing", "", 404},
	}
	for _, tc := range cases {
		response := do(server, tc.method, tc.path, token, tc.body)
		if response.Code != tc.want {
			t.Errorf("%s %s = %d %s, want %d", tc.method, tc.path, response.Code, response.Body, tc.want)
		}
		if strings.Contains(response.Body.String(), `"s"`) && strings.Contains(tc.body, "apiSecret") {
			t.Errorf("response echoes credential material: %s", response.Body)
		}
	}
}
