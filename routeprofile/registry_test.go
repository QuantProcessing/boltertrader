package routeprofile_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/routeprofile"
)

// The table the gateway routes by, spelled out independently of the registry so
// that a row silently disappearing from `defaultRows` fails here rather than
// showing up as a venue that quietly stops being tradable.
func TestRegistryCoversEveryLegacyGatewayProfile(t *testing.T) {
	tests := []struct{ venue, product, settlement string }{
		{"binance", "spot", ""},
		{"binance", "perp", "USDT"},
		{"okx", "spot", ""},
		{"okx", "perp", "USDT"},
		{"lighter", "spot", ""},
		{"lighter", "perp", "USDC"},
		{"hyperliquid", "spot", ""},
		{"hyperliquid", "perp", "USDC"},
		{"bybit", "spot", ""},
		{"bybit", "perp", "USDT"},
		{"bybit", "perp", "USDC"},
		{"bitget", "spot", ""},
		{"bitget", "perp", "USDT"},
		{"bitget", "perp", "USDC"},
		{"gate", "spot", ""},
		{"gate", "perp", "USDT"},
		{"aster", "spot", ""},
		{"aster", "perp", "USDT"},
		{"nado", "spot", "USDT0"},
		{"nado", "perp", "USDT0"},
	}
	registry := routeprofile.DefaultRegistry()
	for _, test := range tests {
		id := fmt.Sprintf("%s.%s", test.venue, test.product)
		if test.settlement != "" {
			id += "." + strings.ToLower(test.settlement)
		}
		id += ".v1"

		profile, err := registry.Resolve(test.venue, id)
		if err != nil {
			t.Fatalf("Resolve(%s,%s): %v", test.venue, id, err)
		}
		if string(profile.Product) != test.product || profile.SettlementAsset != test.settlement {
			t.Fatalf("profile=%+v", profile)
		}

		// The same profile, reached from the parts a legacy caller carries.
		legacy, err := registry.ResolveLegacy(test.venue, test.product, test.settlement)
		if err != nil {
			t.Fatalf("ResolveLegacy(%s,%s,%s): %v", test.venue, test.product, test.settlement, err)
		}
		if legacy != profile {
			t.Fatalf("legacy=%+v profile=%+v", legacy, profile)
		}
	}
	if got := len(registry.Profiles()); got != len(tests) {
		t.Fatalf("registry holds %d profiles, table has %d", got, len(tests))
	}
}

func TestRegistryRejectsUnknownProfiles(t *testing.T) {
	registry := routeprofile.DefaultRegistry()
	for name, key := range map[string]routeprofile.Key{
		"unknown venue":        {Venue: "unknown", RouteProfileID: "unknown.spot.v1"},
		"unknown product":      {Venue: "binance", RouteProfileID: "binance.option.v1"},
		"wrong settlement":     {Venue: "binance", RouteProfileID: "binance.perp.usdc.v1"},
		"retired environment":  {Venue: "binance", RouteProfileID: "binance.spot.demo.v1"},
		"uppercased venue":     {Venue: "Binance", RouteProfileID: "binance.spot.v1"},
		"untrimmed profile id": {Venue: "binance", RouteProfileID: " binance.spot.v1"},
	} {
		if _, err := registry.Resolve(key.Venue, key.RouteProfileID); !errors.Is(err, routeprofile.ErrUnknownProfile) {
			t.Fatalf("%s: err=%v", name, err)
		}
	}
}
