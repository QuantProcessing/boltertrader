package routeprofile_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/routeprofile"
)

func TestRegistryDerivesNativeEnvironmentFromTrustedProfile(t *testing.T) {
	profile, err := routeprofile.DefaultRegistry().Resolve(routeprofile.ModeTest, "binance", "binance.perp.usdt.demo.v1")
	if err != nil {
		t.Fatal(err)
	}
	if profile.NativeEnvironment != routeprofile.EnvironmentDemo {
		t.Fatalf("environment=%s", profile.NativeEnvironment)
	}
	if _, err := routeprofile.DefaultRegistry().Resolve(routeprofile.ModeTest, "binance", "binance.perp.usdt.live.v1"); !errors.Is(err, routeprofile.ErrModeConflict) {
		t.Fatalf("err=%v", err)
	}
}

func TestRegistryCoversEveryLegacyGatewayProfile(t *testing.T) {
	tests := []struct {
		venue, product, settlement string
		environments               []routeprofile.NativeEnvironment
	}{
		{"binance", "spot", "", []routeprofile.NativeEnvironment{"live", "demo"}},
		{"binance", "perp", "USDT", []routeprofile.NativeEnvironment{"live", "demo"}},
		{"okx", "spot", "", []routeprofile.NativeEnvironment{"live", "demo"}},
		{"okx", "perp", "USDT", []routeprofile.NativeEnvironment{"live", "demo"}},
		{"lighter", "spot", "", []routeprofile.NativeEnvironment{"live", "testnet"}},
		{"lighter", "perp", "USDC", []routeprofile.NativeEnvironment{"live", "testnet"}},
		{"hyperliquid", "spot", "", []routeprofile.NativeEnvironment{"live", "testnet"}},
		{"hyperliquid", "perp", "USDC", []routeprofile.NativeEnvironment{"live", "testnet"}},
		{"bybit", "spot", "", []routeprofile.NativeEnvironment{"live", "demo", "testnet"}},
		{"bybit", "perp", "USDT", []routeprofile.NativeEnvironment{"live", "demo", "testnet"}},
		{"bybit", "perp", "USDC", []routeprofile.NativeEnvironment{"live", "demo", "testnet"}},
		{"bitget", "spot", "", []routeprofile.NativeEnvironment{"live", "demo"}},
		{"bitget", "perp", "USDT", []routeprofile.NativeEnvironment{"live", "demo"}},
		{"bitget", "perp", "USDC", []routeprofile.NativeEnvironment{"live", "demo"}},
		{"gate", "spot", "", []routeprofile.NativeEnvironment{"live", "testnet"}},
		{"gate", "perp", "USDT", []routeprofile.NativeEnvironment{"live", "testnet"}},
		{"aster", "spot", "", []routeprofile.NativeEnvironment{"live", "testnet"}},
		{"aster", "perp", "USDT", []routeprofile.NativeEnvironment{"live", "testnet"}},
		{"nado", "spot", "USDT0", []routeprofile.NativeEnvironment{"live", "testnet"}},
		{"nado", "perp", "USDT0", []routeprofile.NativeEnvironment{"live", "testnet"}},
	}
	registry := routeprofile.DefaultRegistry()
	for _, test := range tests {
		for _, environment := range test.environments {
			mode := routeprofile.ModeTest
			if environment == routeprofile.EnvironmentLive {
				mode = routeprofile.ModeLive
			}
			id := fmt.Sprintf("%s.%s", test.venue, test.product)
			if test.settlement != "" {
				id += "." + strings.ToLower(test.settlement)
			}
			id += "." + string(environment) + ".v1"
			profile, err := registry.Resolve(mode, test.venue, id)
			if err != nil {
				t.Fatalf("Resolve(%s,%s,%s): %v", mode, test.venue, id, err)
			}
			if string(profile.Product) != test.product || profile.SettlementAsset != test.settlement || profile.NativeEnvironment != environment {
				t.Fatalf("profile=%+v", profile)
			}
		}
	}
}

func TestRegistryRejectsUnknownAndCrossModeProfiles(t *testing.T) {
	registry := routeprofile.DefaultRegistry()
	if _, err := registry.Resolve(routeprofile.ModeLive, "binance", "binance.spot.demo.v1"); !errors.Is(err, routeprofile.ErrModeConflict) {
		t.Fatalf("live/demo err=%v", err)
	}
	if _, err := registry.Resolve(routeprofile.ModeTest, "unknown", "unknown.spot.demo.v1"); !errors.Is(err, routeprofile.ErrUnknownProfile) {
		t.Fatalf("unknown err=%v", err)
	}
}
