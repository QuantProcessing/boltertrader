package lighter

import (
	"context"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/adapter/internal/runtimeaccept"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/internal/testenv"
)

func TestLighterTestnetReferenceDataReadAcceptance(t *testing.T) {
	cfg := testenv.RequireLighterTestnetRead(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	adapter := newLighterTestnetAdapter(t, ctx, cfg, false, 45*time.Second, "reference data")
	defer adapter.Close()

	inst := selectLighterTestnetInstrument(t, adapter, cfg.PerpSymbol, enums.KindPerp)
	if _, err := runtimeaccept.CheckReferenceDataReadOnly(ctx, adapter.Market, inst.ID, runtimeaccept.ReferenceDataReadOptions{
		Label: "Lighter Testnet reference data",
	}); err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Lighter Testnet reference data")
		t.Fatalf("Lighter Testnet reference data: %v", err)
	}
}
