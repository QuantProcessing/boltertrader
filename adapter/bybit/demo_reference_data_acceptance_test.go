package bybit

import (
	"context"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/adapter/internal/runtimeaccept"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/testenv"
	bybitsdk "github.com/QuantProcessing/boltertrader/sdk/bybit"
)

func TestBybitDemoReferenceDataReadAcceptance(t *testing.T) {
	cfg := testenv.RequireBybitDemoRead(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	httpClient, err := testenv.BybitDemoHTTPClient(45 * time.Second)
	if err != nil {
		t.Fatalf("Bybit Demo HTTP client: %v", err)
	}
	adapter, err := New(ctx, Config{
		APIKey:      cfg.APIKey,
		APISecret:   cfg.APISecret,
		Environment: bybitSDKProfile(cfg.Profile),
		HTTPClient:  httpClient,
		Categories:  []string{"linear"},
	})
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Bybit Demo adapter initialization")
		t.Fatalf("new Bybit Demo adapter: %v", err)
	}
	defer adapter.Close()

	for _, tc := range []struct {
		label  string
		symbol string
		settle string
	}{
		{label: "USDT Perp", symbol: cfg.USDTPerpSymbol, settle: bybitsdk.SettleCoinUSDT},
		{label: "USDC Perp", symbol: cfg.USDCPerpSymbol, settle: bybitsdk.SettleCoinUSDC},
	} {
		t.Run(tc.label, func(t *testing.T) {
			id := requireBybitReferenceInstrument(t, adapter, tc.symbol, tc.settle)
			if _, err := runtimeaccept.CheckReferenceDataReadOnly(ctx, adapter.Market, id, runtimeaccept.ReferenceDataReadOptions{
				Label: "Bybit Demo " + tc.label + " reference data",
			}); err != nil {
				testenv.SkipIfTransientLiveNetworkError(t, err, "Bybit Demo "+tc.label+" reference data")
				t.Fatalf("Bybit Demo %s reference data: %v", tc.label, err)
			}
		})
	}
}

func requireBybitReferenceInstrument(t *testing.T, adapter *Adapter, desired, settle string) model.InstrumentID {
	t.Helper()
	if id, ok := adapter.provider.ResolveVenueInstrument(desired, enums.KindPerp, settle); ok {
		return id
	}
	for _, inst := range adapter.provider.All() {
		if inst != nil && inst.ID.Kind == enums.KindPerp && inst.Settle == settle {
			t.Logf("Bybit configured reference symbol %q is not a loaded %s perp; using %s", desired, settle, inst.VenueSymbol)
			return inst.ID
		}
	}
	t.Fatalf("Bybit reference data acceptance found no %s perp; configured symbol=%q", settle, desired)
	return model.InstrumentID{}
}
