package spot

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	sdkspot "github.com/QuantProcessing/boltertrader/sdk/binance/spot"
)

func TestNewDemoUsesSeparateCredentialsAndDemoEndpoints(t *testing.T) {
	const exchangeInfo = `{"timezone":"UTC","serverTime":1,"symbols":[{"symbol":"ETHUSDT","status":"TRADING","baseAsset":"ETH","quoteAsset":"USDT","baseAssetPrecision":8,"quotePrecision":8,"filters":[{"filterType":"PRICE_FILTER","tickSize":"0.01"},{"filterType":"LOT_SIZE","stepSize":"0.0001","minQty":"0.0001"},{"filterType":"MIN_NOTIONAL","minNotional":"5"}]}]}`

	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme+"://"+r.URL.Host != demoRESTBaseURL {
			t.Fatalf("adapter Demo REST must not use production host: %s", r.URL.String())
		}
		if r.URL.Path != "/api/v3/exchangeInfo" {
			t.Fatalf("unexpected exchange info path: %s", r.URL.Path)
		}
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(exchangeInfo)),
			Header:     make(http.Header),
		}, nil
	})}

	a, err := New(context.Background(), Config{
		APIKey:        "prod-key",
		APISecret:     "prod-secret",
		Demo:          true,
		DemoAPIKey:    "demo-key",
		DemoAPISecret: "demo-secret",
		HTTPClient:    httpClient,
	})
	if err != nil {
		t.Fatalf("New Demo adapter: %v", err)
	}
	defer a.Close()

	if a.rest.BaseURL != demoRESTBaseURL {
		t.Fatalf("expected Demo REST base URL %s, got %s", demoRESTBaseURL, a.rest.BaseURL)
	}
	if a.apiKey != "demo-key" || a.apiSecret != "demo-secret" {
		t.Fatalf("Demo adapter must use separate Demo credentials, got key=%q secret=%q", a.apiKey, a.apiSecret)
	}
	if a.wsMarket.WsClient.URL != demoWSBaseURL {
		t.Fatalf("unexpected Demo market ws URL: %s", a.wsMarket.WsClient.URL)
	}
	if a.wsAPI.URL != demoWSAPIBaseURL {
		t.Fatalf("unexpected Demo WS-API URL: %s", a.wsAPI.URL)
	}
}

func TestNewEnvironmentDemoUsesSeparateCredentialsAndDemoEndpoints(t *testing.T) {
	const exchangeInfo = `{"timezone":"UTC","serverTime":1,"symbols":[{"symbol":"ETHUSDT","status":"TRADING","baseAsset":"ETH","quoteAsset":"USDT","baseAssetPrecision":8,"quotePrecision":8,"filters":[{"filterType":"PRICE_FILTER","tickSize":"0.01"},{"filterType":"LOT_SIZE","stepSize":"0.0001","minQty":"0.0001"},{"filterType":"MIN_NOTIONAL","minNotional":"5"}]}]}`

	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme+"://"+r.URL.Host != sdkspot.DemoBaseURL {
			t.Fatalf("adapter Demo REST must not use production host: %s", r.URL.String())
		}
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(exchangeInfo)),
			Header:     make(http.Header),
		}, nil
	})}

	a, err := New(context.Background(), Config{
		APIKey:        "prod-key",
		APISecret:     "prod-secret",
		Environment:   sdkspot.EnvironmentDemo,
		DemoAPIKey:    "demo-key",
		DemoAPISecret: "demo-secret",
		HTTPClient:    httpClient,
	})
	if err != nil {
		t.Fatalf("New Demo adapter: %v", err)
	}
	defer a.Close()

	if a.rest.BaseURL != sdkspot.DemoBaseURL {
		t.Fatalf("expected Demo REST base URL %s, got %s", sdkspot.DemoBaseURL, a.rest.BaseURL)
	}
	if a.apiKey != "demo-key" || a.apiSecret != "demo-secret" {
		t.Fatalf("Demo adapter must use Demo credentials, got key=%q secret=%q", a.apiKey, a.apiSecret)
	}
	if a.wsMarket.WsClient.URL != sdkspot.DemoWSBaseURL {
		t.Fatalf("unexpected Demo market ws URL: %s", a.wsMarket.WsClient.URL)
	}
	if a.wsAPI.URL != sdkspot.DemoWSAPIBaseURL {
		t.Fatalf("unexpected Demo WS-API URL: %s", a.wsAPI.URL)
	}
}
