package devsetup

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPublicMarketDiagnosticRequiresFreshQuoteNotOnlyHealthyHTTP(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenPath, []byte("offline-diagnostic-token"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, age := range []time.Duration{time.Second, time.Minute} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer offline-diagnostic-token" || r.URL.Path != "/v1/market/snapshot" {
				t.Error("wrong diagnostic request")
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"instrument_id":"binance:live:spot:BTC-USDT","snapshot":{"type":"snapshot","topic":"market.bbo","generation":1,"sequence":1,"observedAt":%q,"data":{"bid":{"price":"100","quantity":"1"},"ask":{"price":"101","quantity":"1"}}}}`, time.Now().UTC().Add(-age).Format(time.RFC3339Nano))
		}))
		parsed, _ := url.Parse(server.URL)
		result := checkPublicMarket(context.Background(), parsed.Port(), tokenPath, "binance:live:spot:BTC-USDT")
		server.Close()
		want := Pass
		if age > 5*time.Second {
			want = Fail
		}
		if result.Status != want {
			t.Fatalf("age=%s result=%+v", age, result)
		}
	}
}
