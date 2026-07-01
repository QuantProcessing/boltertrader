package spot

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/model"
	sdkspot "github.com/QuantProcessing/boltertrader/sdk/binance/spot"
	"github.com/shopspring/decimal"
)

func demoD(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestSpotDemoE2ENormalizeSymbol(t *testing.T) {
	cases := map[string]string{
		"BTC-USDT": "BTCUSDT",
		"eth_usdt": "ETHUSDT",
		"sol/usdt": "SOLUSDT",
		"bnbusdt":  "BNBUSDT",
	}
	for input, want := range cases {
		if got := normalizeDemoE2ESymbol(input); got != want {
			t.Fatalf("normalizeDemoE2ESymbol(%q)=%q, want %q", input, got, want)
		}
	}
}

func TestSpotDemoE2ESymbolSpecFromExchangeInfo(t *testing.T) {
	info := &sdkspot.ExchangeInfoResponse{Symbols: []sdkspot.SymbolInfo{{
		Symbol:     "ETHUSDT",
		Status:     "TRADING",
		BaseAsset:  "ETH",
		QuoteAsset: "USDT",
		Filters: []map[string]any{
			{"filterType": "PRICE_FILTER", "tickSize": "0.01"},
			{"filterType": "LOT_SIZE", "stepSize": "0.0001", "minQty": "0.0001"},
			{"filterType": "MIN_NOTIONAL", "minNotional": "5"},
		},
	}}}

	spec, err := demoE2ESymbolSpecFromExchangeInfo(info, "eth-usdt")
	if err != nil {
		t.Fatalf("demoE2ESymbolSpecFromExchangeInfo: %v", err)
	}
	if spec.VenueSymbol != "ETHUSDT" || spec.BaseCurrency != "ETH" || spec.QuoteCurrency != "USDT" {
		t.Fatalf("unexpected symbol spec identity: %+v", spec)
	}
	if !spec.PriceTick.Equal(demoD("0.01")) || !spec.SizeStep.Equal(demoD("0.0001")) ||
		!spec.MinQty.Equal(demoD("0.0001")) || !spec.MinNotional.Equal(demoD("5")) {
		t.Fatalf("unexpected filters: %+v", spec)
	}
}

func TestSpotDemoE2ESelectOrderQuantityChoosesMinTradableStep(t *testing.T) {
	spec := demoE2ESymbolSpec{
		VenueSymbol: "ETHUSDT",
		SizeStep:    demoD("0.0001"),
		MinQty:      demoD("0.0001"),
		MinNotional: demoD("5"),
	}

	qty, err := selectDemoE2EOrderQuantity(spec, decimal.Zero, demoD("100"), demoD("3000"))
	if err != nil {
		t.Fatalf("selectDemoE2EOrderQuantity: %v", err)
	}
	if !qty.Equal(demoD("0.0017")) {
		t.Fatalf("qty=%s, want 0.0017", qty)
	}
}

func TestSpotDemoE2ESelectOrderQuantityRejectsOverMaxNotional(t *testing.T) {
	spec := demoE2ESymbolSpec{
		VenueSymbol: "BTCUSDT",
		SizeStep:    demoD("0.00001"),
		MinQty:      demoD("0.00001"),
		MinNotional: demoD("5"),
	}

	if _, err := selectDemoE2EOrderQuantity(spec, demoD("0.01"), demoD("100"), demoD("65000")); err == nil {
		t.Fatalf("expected over-max notional rejection")
	}
}

func TestSpotDemoE2EDefaultMaxNotionalIs100USDT(t *testing.T) {
	t.Setenv("BINANCE_DEMO_MAX_NOTIONAL_USDT", "")

	got := demoDecimalEnvOrDefault(t, "BINANCE_DEMO_MAX_NOTIONAL_USDT", demoDefaultMaxNotionalUSDT)
	if !got.Equal(demoD("100")) {
		t.Fatalf("default Demo max notional=%s, want 100", got)
	}
}

func TestSpotDemoE2ECleanupMetadataRemediation(t *testing.T) {
	meta := demoE2ECleanupMetadata{
		Symbol:         "ETHUSDT",
		Side:           "BUY",
		Quantity:       demoD("0.002"),
		VenueOrderIDs:  []string{"12345"},
		ClientOrderIDs: []string{"btds-demo-1"},
		BaseCurrency:   "ETH",
		QuoteCurrency:  "USDT",
		BaseDelta:      demoD("0.002"),
	}

	got := meta.Remediation()
	for _, want := range []string{"ETHUSDT", "BUY", "0.002", "12345", "btds-demo-1", "ETH", "USDT"} {
		if !strings.Contains(got, want) {
			t.Fatalf("remediation %q missing %q", got, want)
		}
	}
}

func TestSpotDemoClientOrderIDFitsBinanceLimit(t *testing.T) {
	for _, kind := range []string{"rest", "fill", "close"} {
		id := demoClientOrderID(kind)
		if len(id) >= 36 {
			t.Fatalf("client order id %q length=%d, want <36", id, len(id))
		}
		if !strings.Contains(id, kind) {
			t.Fatalf("client order id %q should include kind %q", id, kind)
		}
	}
}

func TestSpotDemoHTTPClientRejectsInvalidProxy(t *testing.T) {
	t.Setenv("PROXY", ":// bad proxy")

	if _, err := demoHTTPClient(time.Second); err == nil {
		t.Fatalf("expected invalid proxy error")
	}
}

func TestSpotDemoHTTPClientIgnoresInheritedProxyEnv(t *testing.T) {
	t.Setenv("PROXY", "")
	t.Setenv("ALL_PROXY", "http://127.0.0.1:65535")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:65535")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:65535")

	client, err := demoHTTPClient(time.Second)
	if err != nil {
		t.Fatalf("demoHTTPClient: %v", err)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport=%T, want *http.Transport", client.Transport)
	}
	if transport.Proxy != nil {
		t.Fatalf("demo HTTP client must ignore inherited proxy env unless PROXY is set")
	}
}

func TestWaitForDemoSpotBalanceObservationRequiresCurrencyDelta(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	events := make(chan contract.AccountEvent, 2)
	events <- contract.BalanceEvent{Balance: model.AccountBalance{Currency: "USDT", Total: demoD("10")}}
	events <- contract.BalanceEvent{Balance: model.AccountBalance{Currency: "ETH", Total: demoD("1.2")}}

	if err := waitForDemoSpotBalanceObservation(ctx, events, "ETH", demoD("1"), demoD("0.1")); err != nil {
		t.Fatalf("waitForDemoSpotBalanceObservation: %v", err)
	}
}

func TestWaitForDemoSpotBalanceObservationTimesOutWithoutDelta(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	events := make(chan contract.AccountEvent, 1)
	events <- contract.BalanceEvent{Balance: model.AccountBalance{Currency: "ETH", Total: demoD("1.01")}}

	err := waitForDemoSpotBalanceObservation(ctx, events, "ETH", demoD("1"), demoD("0.1"))
	if err == nil || !strings.Contains(err.Error(), "balance stream") {
		t.Fatalf("expected balance stream timeout, got %v", err)
	}
}
