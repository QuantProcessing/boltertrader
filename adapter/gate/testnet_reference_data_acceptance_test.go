package gate

import (
	"context"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/adapter/internal/runtimeaccept"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/internal/testenv"
	gatesdk "github.com/QuantProcessing/boltertrader/sdk/gate"
)

func TestGateTestnetReferenceDataReadAcceptance(t *testing.T) {
	cfg := testenv.RequireGateTestnetRead(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	adapter := newGateAcceptanceAdapter(t, ctx, cfg, []string{gatesdk.ProductFuturesUSDT})
	defer adapter.Close()

	id := requireGateAcceptanceInstrument(t, adapter, cfg.USDTPerpSymbol, enums.KindPerp, "USDT")
	if _, err := runtimeaccept.CheckReferenceDataReadOnly(ctx, adapter.Market, id, runtimeaccept.ReferenceDataReadOptions{
		Label: "Gate Testnet USDT Perp reference data",
	}); err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Gate Testnet reference data")
		t.Fatalf("Gate Testnet reference data: %v", err)
	}
}
