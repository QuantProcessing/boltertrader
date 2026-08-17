package account

import (
	"fmt"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/store"
	"github.com/QuantProcessing/boltertrader/routeprofile"
)

type ProfileRegistry struct {
	trusted *routeprofile.Registry
}

func NewProfileRegistry(trusted *routeprofile.Registry) *ProfileRegistry {
	if trusted == nil {
		trusted = routeprofile.DefaultRegistry()
	}
	return &ProfileRegistry{trusted: trusted}
}

// Validate checks a desired snapshot's routes against the trusted registry
// before anything is built from them.
//
// The snapshot arrives from the store, so every field in it was written by an
// earlier version of this binary and has to be re-proved rather than trusted:
// a route naming a profile the registry no longer carries, or carrying a
// product or settlement asset that profile does not, would otherwise be built
// into a runtime that then talks to the wrong market.
func (registry *ProfileRegistry) Validate(snapshot store.DesiredSnapshot) error {
	// The stored environment is re-proved for the same reason. Only live
	// endpoints exist; a row still asking for demo describes a runtime nothing
	// can dial, and it has to be refused here rather than fail later as an
	// unreachable venue.
	if snapshot.NativeEnvironment != string(exchange.EnvironmentLive) {
		return fmt.Errorf("runtime native environment is invalid")
	}
	for _, route := range snapshot.Routes {
		profile, err := registry.trusted.Resolve(snapshot.Venue, route.RouteProfileID)
		if err != nil {
			return fmt.Errorf("runtime route profile is invalid")
		}
		if string(profile.Product) != route.Product || profile.SettlementAsset != route.SettlementAsset {
			return fmt.Errorf("runtime route profile conflicts with desired route")
		}
	}
	return nil
}
