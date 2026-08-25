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
		if capability.Venue != exchange.VenueBybit || capability.Granularity != exchange.GranularityAllMarket {
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
