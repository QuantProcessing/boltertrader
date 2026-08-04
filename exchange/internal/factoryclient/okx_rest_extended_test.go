package factoryclient

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/shopspring/decimal"
)

func TestOKXPerpMarkPriceRESTNormalizesPublicMarkPrice(t *testing.T) {
	transport := okxCatalogRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/v5/public/mark-price" {
			t.Fatalf("path=%s, want /api/v5/public/mark-price", r.URL.Path)
		}
		if r.URL.Query().Get("instType") != "SWAP" || r.URL.Query().Get("instId") != "BTC-USDT-SWAP" {
			t.Fatalf("query=%s, want SWAP BTC-USDT-SWAP", r.URL.RawQuery)
		}
		return okxJSONResponse(`{"code":"0","msg":"","data":[{"instType":"SWAP","instId":"BTC-USDT-SWAP","markPx":"43125.5","ts":"1700000000000"}]}`), nil
	})

	client := NewOKXUSDTPerp("", "", "", Settings{
		Endpoint:   "https://okx.invalid",
		HTTPClient: &http.Client{Transport: transport},
	}).(exchange.PerpMarkPriceREST)
	instrument := okxCatalogTestInstrument(exchange.ProductPerp, "BTC", "USDT", "2001", decimal.RequireFromString("0.01"))
	event, err := client.MarkPrice(context.Background(), instrument)
	if err != nil {
		t.Fatalf("MarkPrice: %v", err)
	}
	if event.Instrument.Identity != instrument.Identity ||
		!event.Price.Equal(decimal.RequireFromString("43125.5")) ||
		event.Time.UnixMilli() != 1700000000000 {
		t.Fatalf("event=%+v", event)
	}
}

func TestOKXPerpMarkPriceRESTRejectsMalformedRows(t *testing.T) {
	tests := []struct {
		name string
		row  string
	}{
		{name: "instrument mismatch", row: `{"instType":"SWAP","instId":"ETH-USDT-SWAP","markPx":"43125.5","ts":"1700000000000"}`},
		{name: "zero price", row: `{"instType":"SWAP","instId":"BTC-USDT-SWAP","markPx":"0","ts":"1700000000000"}`},
		{name: "bad timestamp", row: `{"instType":"SWAP","instId":"BTC-USDT-SWAP","markPx":"43125.5","ts":"bad"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := okxCatalogRoundTripper(func(*http.Request) (*http.Response, error) {
				return okxJSONResponse(`{"code":"0","msg":"","data":[` + test.row + `]}`), nil
			})

			client := NewOKXUSDTPerp("", "", "", Settings{
				Endpoint:   "https://okx.invalid",
				HTTPClient: &http.Client{Transport: transport},
			}).(exchange.PerpMarkPriceREST)
			instrument := okxCatalogTestInstrument(exchange.ProductPerp, "BTC", "USDT", "2001", decimal.RequireFromString("0.01"))
			_, err := client.MarkPrice(context.Background(), instrument)
			if !errors.Is(err, exchange.ErrMalformedResponse) {
				t.Fatalf("MarkPrice error=%v, want ErrMalformedResponse", err)
			}
		})
	}
}
