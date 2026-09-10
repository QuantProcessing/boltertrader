package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFillReadRejectsScopeMismatchAndPreservesInventoryFacts(t *testing.T) {
	for _, field := range []string{"none", "account_id", "instrument_id", "gateway_order_id"} {
		t.Run(field, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("instrument_id") != "BTC-USDT" || r.URL.Query().Get("gateway_order_id") != "go" || r.URL.Query().Get("account_id") != "a" {
					t.Errorf("query=%s", r.URL.RawQuery)
				}
				item := map[string]any{"gateway_order_id": "go", "account_id": "a", "instrument_id": "BTC-USDT", "product": "spot", "base_asset": "BTC", "settlement_asset": "USDT", "fill": map[string]any{"fill_id": "f", "side": "buy", "quantity": "1", "price": "100", "fee": "0.001", "fee_asset": "BTC", "time": "2026-09-10T00:00:00Z"}}
				if field != "none" {
					item[field] = "other"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{item}})
			}))
			defer server.Close()
			client, err := NewClient(server.URL, false, []byte(strings.Repeat("x", 32)), false, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			fills, err := client.fetchFills(context.Background(), "go", "a", "BTC-USDT")
			if field != "none" {
				if err == nil || len(fills) != 0 {
					t.Fatalf("out of scope facts accepted=%+v %v", fills, err)
				}
				return
			}
			if err != nil || len(fills) != 1 || fills[0].InstrumentProduct != "spot" || fills[0].BaseAsset != "BTC" || fills[0].Fee != "0.001" {
				t.Fatalf("facts=%+v err=%v", fills, err)
			}
		})
	}
}
