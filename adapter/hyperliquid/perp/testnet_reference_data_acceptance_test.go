package perp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/adapter/hyperliquid/internal/instruments"
	"github.com/QuantProcessing/boltertrader/adapter/internal/runtimeaccept"
	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/testenv"
	sdk "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
	sdkperp "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/perp"
	sdkspot "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/spot"
)

func TestHyperliquidPerpTestnetReferenceDataReadAcceptance(t *testing.T) {
	cfg := testenv.RequireHyperliquidTestnetRead(t)

	t.Run("Standard", func(t *testing.T) {
		runHyperliquidReferenceDataReadAcceptance(t, cfg, "Hyperliquid Perp Testnet reference data", cfg.PerpSymbol, false, nil)
	})
	t.Run("HIP3", func(t *testing.T) {
		if cfg.HIP3Symbol == "" {
			t.Skipf("skipping Hyperliquid HIP-3 Testnet reference data: set %s", testenv.HyperliquidTestnetHIP3SymbolEnv)
		}
		dex, _, ok := strings.Cut(cfg.HIP3Symbol, ":")
		if !ok || dex == "" {
			t.Fatalf("%s must include a dex qualifier, got %q", testenv.HyperliquidTestnetHIP3SymbolEnv, cfg.HIP3Symbol)
		}
		runHyperliquidReferenceDataReadAcceptance(t, cfg, "Hyperliquid HIP-3 Testnet reference data", cfg.HIP3Symbol, true, []string{dex})
	})
}

func runHyperliquidReferenceDataReadAcceptance(t *testing.T, cfg testenv.HyperliquidTestnetConfig, label, symbol string, includeHIP3 bool, hip3Dexes []string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	httpClient, err := testenv.HyperliquidTestnetHTTPClient(45 * time.Second)
	if err != nil {
		t.Fatalf("Hyperliquid Testnet HTTP client: %v", err)
	}
	base := sdk.NewClient().WithEnvironment(sdk.EnvironmentTestnet)
	base.Http = httpClient
	rest := sdkperp.NewClient(base)
	insts, err := buildRegistryInstruments(ctx, rest, sdkspot.NewClient(base), Config{
		Environment: sdk.EnvironmentTestnet,
		IncludeHIP3: includeHIP3,
		HIP3Dexes:   hip3Dexes,
	})
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, label+" instrument registry")
		t.Fatalf("load %s instrument registry: %v", label, err)
	}
	provider := instruments.NewRegistry(insts...)
	market := newMarketDataClient(rest, nil, provider, clock.NewRealClock())
	defer market.Close()

	inst := selectReferenceTestnetInstrument(t, provider, symbol)
	if _, err := runtimeaccept.CheckReferenceDataReadOnly(ctx, market, inst.ID, runtimeaccept.ReferenceDataReadOptions{
		Label: label,
	}); err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, label)
		t.Fatalf("%s: %v", label, err)
	}
}

func selectReferenceTestnetInstrument(t *testing.T, provider model.InstrumentProvider, desired string) *model.Instrument {
	t.Helper()
	all := provider.All()
	if len(all) == 0 {
		t.Skip("Hyperliquid Testnet returned no perp instruments")
	}
	if desired != "" {
		for _, inst := range all {
			if matchesPerpTestnetSymbol(inst, desired) {
				return inst
			}
		}
		t.Fatalf("configured Hyperliquid Testnet symbol %q not loaded", desired)
	}
	return all[0]
}
