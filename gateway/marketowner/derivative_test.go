package marketowner

import (
	"context"
	"sync"
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/marketcapability"
	"github.com/QuantProcessing/boltertrader/gateway/markethub"
	"github.com/QuantProcessing/boltertrader/routeprofile"
)

func TestDerivativeOwnersMergeRoutesAtNativeFamilyBoundary(t *testing.T) {
	capabilities, err := marketcapability.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	factory := &recordingDerivativeFactory{}
	owners, err := NewDerivativeOwners(factory, routeprofile.DefaultRegistry(), capabilities, staticDerivativeResolver{}, markethub.NewFactStore(nil, nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owners.Close() })
	bybitOwners, bybitRoutes := 0, 0
	for _, owner := range owners.owners {
		if owner.key.Venue == exchange.VenueBybit {
			bybitOwners++
			if owner.key.NativeFamily != marketcapability.FamilyLinear {
				t.Fatalf("Bybit owner family=%q", owner.key.NativeFamily)
			}
			bybitRoutes += len(owner.profiles)
		}
	}
	if bybitOwners != 1 || bybitRoutes != 2 {
		t.Fatalf("Bybit owners=%d routes=%d, want one owner with two projections", bybitOwners, bybitRoutes)
	}
	bybitBuilds := 0
	for _, call := range factory.calls {
		if call.profiles[0].Venue == string(exchange.VenueBybit) {
			bybitBuilds++
			if len(call.profiles) != 2 {
				t.Fatalf("Bybit physical feed projections=%d", len(call.profiles))
			}
		}
		if len(call.capabilities) != 4 {
			t.Fatalf("%s capabilities=%d", call.profiles[0].ID, len(call.capabilities))
		}
		family := marketcapability.NativeFamily(exchange.Venue(call.profiles[0].Venue), exchange.ProductPerp, call.profiles[0].SettlementAsset)
		for _, capability := range call.capabilities {
			if capability.NativeMarketFamily != family {
				t.Fatalf("%s received foreign capability %+v", call.profiles[0].ID, capability)
			}
		}
	}
	if bybitBuilds != 1 {
		t.Fatalf("Bybit physical feed builds=%d, want 1", bybitBuilds)
	}
}

type derivativeFactoryCall struct {
	profiles     []routeprofile.Profile
	capabilities []exchange.MarketCapability
}

type recordingDerivativeFactory struct {
	mu    sync.Mutex
	calls []derivativeFactoryCall
}

func (factory *recordingDerivativeFactory) BuildDerivativeFeed(profiles []routeprofile.Profile, capabilities []exchange.MarketCapability, _ exchange.DerivativeInstrumentResolver) (exchange.DerivativeFeed, error) {
	factory.mu.Lock()
	factory.calls = append(factory.calls, derivativeFactoryCall{profiles: append([]routeprofile.Profile(nil), profiles...), capabilities: append([]exchange.MarketCapability(nil), capabilities...)})
	factory.mu.Unlock()
	return inertDerivativeFeed{}, nil
}

type inertDerivativeFeed struct{}

func (inertDerivativeFeed) Bootstrap(context.Context) (exchange.DerivativePatch, error) {
	return exchange.DerivativePatch{}, nil
}
func (inertDerivativeFeed) Stream(ctx context.Context, _ func(exchange.DerivativePatch)) error {
	<-ctx.Done()
	return ctx.Err()
}
func (inertDerivativeFeed) Close() error { return nil }

type staticDerivativeResolver struct{}

func (staticDerivativeResolver) ResolveDerivativeInstrument(context.Context, string, string, string) (exchange.DerivativeFact, bool) {
	return exchange.DerivativeFact{}, false
}
