package nado

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/adapter/internal/runtimeaccept"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/testenv"
	sdk "github.com/QuantProcessing/boltertrader/sdk/nado"
)

func TestNadoTestnetReferenceDataReadAcceptance(t *testing.T) {
	cfg := testenv.RequireNadoTestnetPublicRead(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	adapter, err := New(ctx, Config{Environment: sdk.EnvironmentTestnet, ProductKind: enums.KindPerp})
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Nado Testnet adapter initialization")
		t.Fatalf("new Nado Testnet adapter: %v", err)
	}
	defer adapter.Close()

	id := selectNadoReferenceInstrument(t, adapter.provider, cfg.PerpSymbol)
	if _, err := runtimeaccept.CheckReferenceDataReadOnly(ctx, adapter.Market, id, runtimeaccept.ReferenceDataReadOptions{
		Label: "Nado Testnet reference data",
	}); err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Nado Testnet reference data")
		t.Fatalf("Nado Testnet reference data: %v", err)
	}
}

func selectNadoReferenceInstrument(t *testing.T, provider *instrumentProvider, desired string) model.InstrumentID {
	t.Helper()
	desired = strings.TrimSpace(desired)
	for _, inst := range provider.All() {
		if inst == nil || inst.ID.Kind != enums.KindPerp {
			continue
		}
		if desired == "" || strings.EqualFold(inst.VenueSymbol, desired) || strings.EqualFold(inst.ID.Symbol, desired) {
			return inst.ID
		}
	}
	if desired == "" {
		t.Fatal("Nado Testnet returned no supported perp instruments")
	}
	t.Fatalf("Nado Testnet reference symbol %q was not loaded", desired)
	return model.InstrumentID{}
}
