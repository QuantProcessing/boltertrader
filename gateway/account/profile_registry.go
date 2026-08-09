package account

import (
	"fmt"

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

func (registry *ProfileRegistry) Validate(mode RuntimeMode, snapshot store.DesiredSnapshot) error {
	profileMode := routeprofile.ModeTest
	if mode == RuntimeModeLive {
		profileMode = routeprofile.ModeLive
	} else if mode != RuntimeModeTest {
		return fmt.Errorf("runtime profile mode is invalid")
	}
	for _, route := range snapshot.Routes {
		profile, err := registry.trusted.Resolve(profileMode, snapshot.Venue, route.RouteProfileID)
		if err != nil {
			return fmt.Errorf("runtime route profile is invalid")
		}
		if string(profile.NativeEnvironment) != snapshot.NativeEnvironment || string(profile.Product) != route.Product || profile.SettlementAsset != route.SettlementAsset {
			return fmt.Errorf("runtime route profile conflicts with desired route")
		}
	}
	return nil
}
