package marketcapability

import (
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
)

func TestMatrixIsValidAndHasNoDuplicateNativePath(t *testing.T) {
	seen := make(map[[5]string]struct{})
	for _, capability := range Matrix() {
		if err := capability.Validate(); err != nil {
			t.Fatalf("invalid capability %#v: %v", capability, err)
		}
		key := [5]string{string(capability.Venue), capability.NativeMarketFamily, string(capability.Dataset), string(capability.Transport), string(capability.Granularity)}
		if _, duplicate := seen[key]; duplicate {
			t.Fatalf("duplicate capability: %#v", key)
		}
		seen[key] = struct{}{}
	}
}

func TestBybitUsesOneLinearAllMarketOwner(t *testing.T) {
	for _, capability := range Matrix() {
		if capability.Venue != exchange.VenueBybit || capability.Granularity != exchange.GranularityAllMarket || capability.NativeMarketFamily == FamilySpot {
			continue
		}
		if capability.NativeMarketFamily != FamilyLinear {
			t.Fatalf("Bybit all-market capability split by route: %#v", capability)
		}
	}
}

func TestOKXHasNoAllMarketWebSocketFacts(t *testing.T) {
	for _, capability := range Matrix() {
		if capability.Venue == exchange.VenueOKX && capability.Transport == exchange.TransportWebSocket && capability.Granularity != exchange.GranularitySingleInstrument {
			t.Fatalf("OKX broad websocket capability reintroduced: %#v", capability)
		}
	}
}

func TestRuntimeRegistryConsumesEveryCapabilityOwnerAndDataset(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, capability := range registry.All() {
		instrumentScoped := capability.Granularity == exchange.GranularitySingleInstrument
		resolved, resolveErr := registry.Resolve(capability.Venue, capability.NativeMarketFamily, capability.Dataset, instrumentScoped)
		if resolveErr != nil {
			t.Fatalf("unconsumed capability %#v: %v", capability, resolveErr)
		}
		if resolved.Venue != capability.Venue || resolved.NativeMarketFamily != capability.NativeMarketFamily || resolved.Dataset != capability.Dataset {
			t.Fatalf("capability resolved to a different native owner: got %#v want %#v", resolved, capability)
		}
	}
}
