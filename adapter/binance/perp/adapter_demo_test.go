package perp

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	sdkperp "github.com/QuantProcessing/boltertrader/sdk/binance/perp"
)

func TestNewDemoUsesSeparateCredentialsAndNoProductionFallback(t *testing.T) {
	const exchangeInfo = `{"timezone":"UTC","serverTime":1,"symbols":[{"symbol":"BTCUSDT","contractType":"PERPETUAL","baseAsset":"BTC","quoteAsset":"USDT","marginAsset":"USDT","pricePrecision":2,"quantityPrecision":3,"filters":[{"filterType":"PRICE_FILTER","tickSize":"0.10"},{"filterType":"LOT_SIZE","stepSize":"0.001","minQty":"0.001"},{"filterType":"MIN_NOTIONAL","notional":"5"}]}]}`

	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme+"://"+r.URL.Host != sdkperp.DemoBaseURL {
			t.Fatalf("adapter Demo REST must not use production host: %s", r.URL.String())
		}
		if r.URL.Path != "/fapi/v1/exchangeInfo" {
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

	if a.rest.BaseURL != sdkperp.DemoBaseURL {
		t.Fatalf("expected Demo REST base URL %s, got %s", sdkperp.DemoBaseURL, a.rest.BaseURL)
	}
	if a.apiKey != "demo-key" || a.apiSecret != "demo-secret" {
		t.Fatalf("Demo adapter must use separate Demo credentials, got key=%q secret=%q", a.apiKey, a.apiSecret)
	}

	market, ok := a.Market.(*marketDataClient)
	if !ok {
		t.Fatalf("unexpected market client type %T", a.Market)
	}
	if market.ws.WsClient.URL != sdkperp.DemoWSPublicBaseURL {
		t.Fatalf("unexpected Demo public ws URL: %s", market.ws.WsClient.URL)
	}

	accountWS := a.newWsAccountClient(context.Background())
	if accountWS.Client.APIKey != "demo-key" || accountWS.Client.SecretKey != "demo-secret" {
		t.Fatalf("Demo account websocket must not fall back to production credentials: key=%q secret=%q", accountWS.Client.APIKey, accountWS.Client.SecretKey)
	}
	if accountWS.BaseURL != sdkperp.DemoWSPrivateBaseURL {
		t.Fatalf("unexpected Demo account websocket URL: %s", accountWS.BaseURL)
	}
}

func TestNewEnvironmentDemoUsesSeparateCredentialsAndNoProductionFallback(t *testing.T) {
	const exchangeInfo = `{"timezone":"UTC","serverTime":1,"symbols":[{"symbol":"BTCUSDT","contractType":"PERPETUAL","baseAsset":"BTC","quoteAsset":"USDT","marginAsset":"USDT","pricePrecision":2,"quantityPrecision":3,"filters":[{"filterType":"PRICE_FILTER","tickSize":"0.10"},{"filterType":"LOT_SIZE","stepSize":"0.001","minQty":"0.001"},{"filterType":"MIN_NOTIONAL","notional":"5"}]}]}`

	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme+"://"+r.URL.Host != sdkperp.DemoBaseURL {
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
		Environment:   sdkperp.EnvironmentDemo,
		DemoAPIKey:    "demo-key",
		DemoAPISecret: "demo-secret",
		HTTPClient:    httpClient,
	})
	if err != nil {
		t.Fatalf("New Demo adapter: %v", err)
	}
	defer a.Close()

	if a.rest.BaseURL != sdkperp.DemoBaseURL {
		t.Fatalf("expected Demo REST base URL %s, got %s", sdkperp.DemoBaseURL, a.rest.BaseURL)
	}
	if a.apiKey != "demo-key" || a.apiSecret != "demo-secret" {
		t.Fatalf("Demo adapter must use Demo credentials, got key=%q secret=%q", a.apiKey, a.apiSecret)
	}
	if a.profile.RESTBaseURL != sdkperp.DemoBaseURL {
		t.Fatalf("profile=%+v, want Demo profile", a.profile)
	}
}
