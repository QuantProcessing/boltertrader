package exchange

import (
	"testing"
	"time"
)

func TestAccountEventContractIsRouteScoped(t *testing.T) {
	route := AccountRoute{
		AccountID: "account-1", RouteID: "route-1", Venue: VenueBinance,
		Environment: EnvironmentLive, Product: ProductPerp, Settlement: "USDT", NativeFamily: "perpetual",
	}
	event := AccountEvent{Kind: AccountEventResyncRequired, Route: route, ObservedAt: time.Now().UTC(), Reason: "sequence-gap"}
	if err := event.Validate(); err != nil {
		t.Fatal(err)
	}
	if event.InstrumentID != "" {
		t.Fatal("account stream request unexpectedly requires an instrument")
	}
}

func TestMarketCapabilityContract(t *testing.T) {
	capability := MarketCapability{
		Venue: VenueBinance, NativeMarketFamily: "perpetual", Dataset: DatasetMark,
		Transport: TransportWebSocket, Granularity: GranularityAllMarket, NominalCadence: time.Second,
	}
	if err := capability.Validate(); err != nil {
		t.Fatal(err)
	}
}
