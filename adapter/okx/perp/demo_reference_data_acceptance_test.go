package perp

import (
	"context"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/adapter/internal/runtimeaccept"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/testenv"
	"github.com/QuantProcessing/boltertrader/sdk/okx"
)

func TestOKXPerpDemoReferenceDataReadAcceptance(t *testing.T) {
	cfg := testenv.RequireOKXDemoRead(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	httpClient, err := testenv.OKXDemoHTTPClient(45 * time.Second)
	if err != nil {
		t.Fatalf("OKX Demo HTTP client: %v", err)
	}
	endpoints := okxDemoEndpoints(t, cfg)
	adapter, err := New(ctx, Config{
		APIKey:          cfg.APIKey,
		APISecret:       cfg.APISecret,
		Passphrase:      cfg.Passphrase,
		TdMode:          "cross",
		Environment:     okx.Simulated,
		DemoHostProfile: okx.DemoHostProfile(cfg.HostProfile),
		RESTBaseURL:     endpoints.REST,
		WSPublicURL:     endpoints.WSPublic,
		WSPrivateURL:    endpoints.WSPrivate,
		HTTPClient:      httpClient,
	})
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "OKX Perp Demo adapter initialization")
		t.Fatalf("new OKX Perp Demo adapter: %v", err)
	}
	defer adapter.Close()

	id := model.InstrumentID{Venue: venueName, Symbol: instIDToNeutral(cfg.PerpSymbol), Kind: enums.KindPerp}
	if _, ok := adapter.provider.Instrument(id); !ok {
		t.Fatalf("OKX Perp Demo symbol %s not loaded", cfg.PerpSymbol)
	}
	if _, err := runtimeaccept.CheckReferenceDataReadOnly(ctx, adapter.Market, id, runtimeaccept.ReferenceDataReadOptions{
		Label: "OKX Perp Demo reference data",
	}); err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "OKX Perp Demo reference data")
		t.Fatalf("OKX Perp Demo reference data: %v", err)
	}
}
