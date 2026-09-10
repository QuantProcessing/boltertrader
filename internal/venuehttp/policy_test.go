package venuehttp

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type policyRoundTrip func(*http.Request) (*http.Response, error)

func (fn policyRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }
func TestWirePoliciesWeightParametersAndPreserveBodies(t *testing.T) {
	tests := []struct {
		venue, method, path, body, action string
		cost                              int
	}{
		{"binance", "GET", "/api/v3/depth?limit=100", "", "read", 5},
		{"binance", "GET", "/api/v3/depth?limit=5000", "", "read", 250},
		{"binance", "GET", "/fapi/v1/depth", "", "read", 10},
		{"binance", "GET", "/fapi/v1/klines?limit=1500", "", "read", 10},
		{"binance", "GET", "/api/v3/ticker/24hr", "", "read", 80},
		{"binance", "GET", "/api/v3/ticker/24hr?symbol=BTCUSDT", "", "read", 2},
		{"binance", "DELETE", "/api/v3/order?signature=SECRET", "", "cancel", 1},
		{"bybit", "POST", "/v5/order/create-batch", `{"request":[{},{}]}`, "place", 2},
		{"hyperliquid", "POST", "/info", `{"type":"allMids"}`, "read", 2},
		{"hyperliquid", "POST", "/info", `{"type":"userFillsByTime"}`, "read", 120},
		{"hyperliquid", "POST", "/info", `{"type":"metaAndAssetCtxs"}`, "read", 20},
		{"hyperliquid", "POST", "/exchange", `{"action":{"type":"cancel","cancels":[{}]}}`, "cancel", 1},
		{"nado", "POST", "/v1/execute", `{"place_order":{}}`, "place", 1},
		{"lighter", "POST", "/api/v1/sendTx", `{}`, "", 1},
	}
	for _, tt := range tests {
		t.Run(tt.venue+tt.path, func(t *testing.T) {
			client := &http.Client{Transport: policyRoundTrip(func(req *http.Request) (*http.Response, error) {
				p := PolicyForRequest(req)
				if p.Venue != tt.venue || p.Cost != tt.cost || p.Action != tt.action || strings.Contains(p.Endpoint, "SECRET") {
					t.Fatalf("policy=%+v", p)
				}
				body, _ := io.ReadAll(req.Body)
				if string(body) != tt.body {
					t.Fatalf("body=%s", body)
				}
				return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
			})}
			req, _ := http.NewRequest(tt.method, "https://example.invalid"+tt.path, strings.NewReader(tt.body))
			resp, err := DoHTTP(client, req, tt.venue)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
		})
	}
}
func TestSDKTransportFailureKeepsClassificationWithoutRenderingSignedURL(t *testing.T) {
	native := &LocalRateLimitError{Action: "place"}
	client := &http.Client{Transport: policyRoundTrip(func(*http.Request) (*http.Response, error) { return nil, native })}
	req, _ := http.NewRequest("POST", "https://example.invalid/order?signature=SECRET", nil)
	_, err := DoHTTP(client, req, "binance")
	var local *LocalRateLimitError
	if !errors.As(err, &local) || local != native || !errors.Is(err, ErrRateLimited) || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("err=%v", err)
	}
}
