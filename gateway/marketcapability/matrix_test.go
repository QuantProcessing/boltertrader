package marketcapability

import (
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
)

func TestFreshnessWindowAllowsThreeMissedNativeObservations(t *testing.T) {
	for _, test := range []struct {
		cadence time.Duration
		want    time.Duration
	}{
		{cadence: 0, want: 5 * time.Second},
		{cadence: time.Second, want: 5 * time.Second},
		{cadence: 15 * time.Second, want: 45 * time.Second},
	} {
		if got := FreshnessWindow(exchange.MarketCapability{NominalCadence: test.cadence}); got != test.want {
			t.Fatalf("FreshnessWindow(%s) = %s, want %s", test.cadence, got, test.want)
		}
	}
}

func TestMatrixIsValidAndHasNoDuplicateNativePath(t *testing.T) {
	seen := make(map[[5]string]struct{})
	for _, capability := range exchange.MarketCapabilities() {
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
	for _, capability := range exchange.MarketCapabilities() {
		if capability.Venue != exchange.VenueBybit || capability.Granularity != exchange.GranularityAllMarket || capability.NativeMarketFamily == exchange.NativeFamilySpot {
			continue
		}
		if capability.NativeMarketFamily != exchange.NativeFamilyLinear {
			t.Fatalf("Bybit all-market capability split by route: %#v", capability)
		}
	}
}

func TestOKXHasNoAllMarketWebSocketFacts(t *testing.T) {
	for _, capability := range exchange.MarketCapabilities() {
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
		if resolved != capability {
			t.Fatalf("capability is shadowed by another runtime strategy: got %#v want %#v", resolved, capability)
		}
	}
}

func TestRegistryHotCapacityUsesNativeFamilyLimits(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if got := registry.HotCapacity(exchange.VenueBitget, exchange.NativeFamilyUSDTFutures); got != 50 {
		t.Fatalf("Bitget hot capacity=%d want=50", got)
	}
	if got := registry.HotCapacity(exchange.VenueLighter, exchange.NativeFamilyPerpetual); got < 1000 {
		t.Fatalf("broad-feed capacity=%d, want effectively unbounded", got)
	}
}

func TestMatrixDoesNotInventCatalogWideOIOrRESTJoinedDepth(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, venue := range []exchange.Venue{exchange.VenueBinance, exchange.VenueAster} {
		if capability, resolveErr := registry.Resolve(venue, exchange.NativeFamilyPerpetual, exchange.DatasetOpenInterest, false); resolveErr == nil {
			t.Fatalf("%s broad OI capability unexpectedly present: %+v", venue, capability)
		}
	}
	for _, family := range []string{exchange.NativeFamilyUSDTFutures, exchange.NativeFamilyUSDCFutures} {
		if capability, resolveErr := registry.Resolve(exchange.VenueBitget, family, exchange.DatasetOpenInterest, false); resolveErr == nil {
			t.Fatalf("Bitget %s broad OI capability unexpectedly present: %+v", family, capability)
		}
		capability, resolveErr := registry.Resolve(exchange.VenueBitget, family, exchange.DatasetOpenInterest, true)
		if resolveErr != nil || capability.Transport != exchange.TransportWebSocket || capability.Granularity != exchange.GranularitySingleInstrument {
			t.Fatalf("Bitget %s hot OI capability=%+v err=%v", family, capability, resolveErr)
		}
	}
	for _, venue := range []exchange.Venue{exchange.VenueHyperliquid, exchange.VenueLighter} {
		capability, resolveErr := registry.Resolve(venue, exchange.NativeFamilyPerpetual, exchange.DatasetOpenInterest, false)
		if resolveErr != nil || capability.Transport != exchange.TransportWebSocket || capability.Granularity != exchange.GranularityAllMarket {
			t.Fatalf("%s all-market OI capability=%+v err=%v", venue, capability, resolveErr)
		}
	}
	for _, family := range []string{exchange.NativeFamilySpot, exchange.NativeFamilyUSDT0Perpetual} {
		if capability, resolveErr := registry.Resolve(exchange.VenueNado, family, exchange.DatasetDepth, true); resolveErr == nil {
			t.Fatalf("Nado REST-joined depth capability unexpectedly present: %+v", capability)
		}
	}
}
