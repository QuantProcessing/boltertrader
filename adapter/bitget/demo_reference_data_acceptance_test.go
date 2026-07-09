package bitget

import (
	"context"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/adapter/internal/runtimeaccept"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/internal/testenv"
	bitgetsdk "github.com/QuantProcessing/boltertrader/sdk/bitget"
)

func TestBitgetDemoReferenceDataReadAcceptance(t *testing.T) {
	cfg := testenv.RequireBitgetDemoRead(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	httpClient, err := testenv.BitgetDemoHTTPClient(45 * time.Second)
	if err != nil {
		t.Fatalf("Bitget Demo HTTP client: %v", err)
	}
	adapter, err := New(ctx, Config{
		APIKey:     cfg.APIKey,
		APISecret:  cfg.APISecret,
		Passphrase: cfg.Passphrase,
		Environment: bitgetsdk.EnvironmentProfile{
			RESTBaseURL:     cfg.Profile.RESTBaseURL,
			PublicWSURL:     cfg.Profile.PublicWSURL,
			PrivateWSURL:    cfg.Profile.PrivateWSURL,
			PAPTrading:      cfg.Profile.PAPTrading,
			OfficialTestnet: cfg.Profile.OfficialTestnet,
		},
		HTTPClient: httpClient,
		Categories: []string{bitgetsdk.ProductTypeUSDTFutures, bitgetsdk.ProductTypeUSDCFutures},
	})
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Bitget Demo adapter initialization")
		t.Fatalf("new Bitget Demo adapter: %v", err)
	}
	defer adapter.Close()

	for _, tc := range []struct {
		label  string
		symbol string
		settle string
	}{
		{label: "USDT Perp", symbol: cfg.USDTPerpSymbol, settle: "USDT"},
		{label: "USDC Perp", symbol: cfg.USDCPerpSymbol, settle: "USDC"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			id := requireBitgetAcceptanceInstrument(t, adapter, tc.symbol, enums.KindPerp, tc.settle)
			if _, err := runtimeaccept.CheckReferenceDataReadOnly(ctx, adapter.Market, id, runtimeaccept.ReferenceDataReadOptions{
				Label: "Bitget Demo " + tc.label + " reference data",
			}); err != nil {
				testenv.SkipIfTransientLiveNetworkError(t, err, "Bitget Demo "+tc.label+" reference data")
				t.Fatalf("Bitget Demo %s reference data: %v", tc.label, err)
			}
		})
	}
}
