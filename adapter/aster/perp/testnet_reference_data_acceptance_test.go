package perp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/adapter/internal/runtimeaccept"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/testenv"
	astercommon "github.com/QuantProcessing/boltertrader/sdk/aster/common"
)

func TestAsterPerpTestnetReferenceDataReadAcceptance(t *testing.T) {
	cfg := testenv.RequireAsterTestnetPublicRead(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	profile, err := astercommon.NewProfile(astercommon.EnvironmentTestnet, astercommon.ProductPerp)
	if err != nil {
		t.Fatal(err)
	}

	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		adapter, err := New(ctx, Config{Profile: profile})
		if err == nil {
			id := selectAsterReferenceInstrument(t, adapter.provider, cfg.PerpSymbol)
			_, err = runtimeaccept.CheckReferenceDataReadOnly(ctx, adapter.Market, id, runtimeaccept.ReferenceDataReadOptions{
				Label: "Aster Perp Testnet reference data",
			})
			_ = adapter.Close()
		}
		if err == nil {
			return
		}
		lastErr = err
		if !testenv.IsTransientLiveNetworkError(err) {
			t.Fatalf("Aster Perp Testnet reference data: %v", err)
		}
		t.Logf("Aster Testnet transient attempt %d/3: %v", attempt, err)
		if attempt < 3 {
			time.Sleep(time.Duration(attempt) * time.Second)
		}
	}
	testenv.SkipIfTransientLiveNetworkError(t, lastErr, "Aster Perp Testnet reference data after 3 attempts")
	t.Fatalf("Aster Perp Testnet reference data: %v", lastErr)
}

func selectAsterReferenceInstrument(t *testing.T, provider *instrumentProvider, desired string) model.InstrumentID {
	t.Helper()
	desired = strings.TrimSpace(desired)
	for _, inst := range provider.All() {
		if inst != nil && (desired == "" || strings.EqualFold(inst.VenueSymbol, desired) || strings.EqualFold(inst.ID.Symbol, desired)) {
			return inst.ID
		}
	}
	if desired == "" {
		t.Fatal("Aster Testnet returned no supported perp instruments")
	}
	t.Fatalf("Aster Testnet reference symbol %q was not loaded", desired)
	return model.InstrumentID{}
}
