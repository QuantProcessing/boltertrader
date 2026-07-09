package perp

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/adapter/internal/runtimeaccept"
	"github.com/QuantProcessing/boltertrader/internal/testenv"
)

func TestBinanceDemoReferenceDataReadAcceptance(t *testing.T) {
	testenv.RequireBinanceDemoRead(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	httpClient, err := testenv.BinanceDemoHTTPClient(45 * time.Second)
	if err != nil {
		t.Fatalf("Binance Demo HTTP client: %v", err)
	}
	adapter, err := New(ctx, Config{
		Demo:          true,
		DemoAPIKey:    os.Getenv(testenv.BinanceDemoAPIKeyEnv),
		DemoAPISecret: os.Getenv(testenv.BinanceDemoAPISecretEnv),
		HTTPClient:    httpClient,
	})
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Binance Demo adapter initialization")
		t.Fatalf("new Binance Demo adapter: %v", err)
	}
	defer adapter.Close()

	info, err := adapter.rest.ExchangeInfo(ctx)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Binance Demo exchangeInfo")
		t.Fatalf("exchange info: %v", err)
	}
	spec, err := demoAcceptanceSymbolSpecFromExchangeInfo(info, demoEnvOrDefault("BINANCE_DEMO_SYMBOL", "ETH-USDT"))
	if err != nil {
		t.Fatalf("resolve Binance Demo symbol: %v", err)
	}
	id := adapter.provider.resolveVenueSymbol(spec.VenueSymbol)
	if _, err := runtimeaccept.CheckReferenceDataReadOnly(ctx, adapter.Market, id, runtimeaccept.ReferenceDataReadOptions{
		Label: "Binance Demo reference data",
	}); err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Binance Demo reference data")
		t.Fatalf("Binance Demo reference data: %v", err)
	}
}
