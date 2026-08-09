package routeprofile

import (
	"errors"
	"fmt"
	"strings"
)

var (
	ErrUnknownProfile = errors.New("unknown route profile")
	ErrModeConflict   = errors.New("route profile mode conflict")
)

type Registry struct {
	profiles map[Key]Profile
	byID     map[string]Profile
}

type profileRow struct {
	venue        string
	product      Product
	settlement   string
	environments []NativeEnvironment
}

var defaultRows = []profileRow{
	{"binance", ProductSpot, "", []NativeEnvironment{EnvironmentLive, EnvironmentDemo}},
	{"binance", ProductPerp, "USDT", []NativeEnvironment{EnvironmentLive, EnvironmentDemo}},
	{"okx", ProductSpot, "", []NativeEnvironment{EnvironmentLive, EnvironmentDemo}},
	{"okx", ProductPerp, "USDT", []NativeEnvironment{EnvironmentLive, EnvironmentDemo}},
	{"lighter", ProductSpot, "", []NativeEnvironment{EnvironmentLive, EnvironmentTestnet}},
	{"lighter", ProductPerp, "USDC", []NativeEnvironment{EnvironmentLive, EnvironmentTestnet}},
	{"hyperliquid", ProductSpot, "", []NativeEnvironment{EnvironmentLive, EnvironmentTestnet}},
	{"hyperliquid", ProductPerp, "USDC", []NativeEnvironment{EnvironmentLive, EnvironmentTestnet}},
	{"bybit", ProductSpot, "", []NativeEnvironment{EnvironmentLive, EnvironmentDemo, EnvironmentTestnet}},
	{"bybit", ProductPerp, "USDT", []NativeEnvironment{EnvironmentLive, EnvironmentDemo, EnvironmentTestnet}},
	{"bybit", ProductPerp, "USDC", []NativeEnvironment{EnvironmentLive, EnvironmentDemo, EnvironmentTestnet}},
	{"bitget", ProductSpot, "", []NativeEnvironment{EnvironmentLive, EnvironmentDemo}},
	{"bitget", ProductPerp, "USDT", []NativeEnvironment{EnvironmentLive, EnvironmentDemo}},
	{"bitget", ProductPerp, "USDC", []NativeEnvironment{EnvironmentLive, EnvironmentDemo}},
	{"gate", ProductSpot, "", []NativeEnvironment{EnvironmentLive, EnvironmentTestnet}},
	{"gate", ProductPerp, "USDT", []NativeEnvironment{EnvironmentLive, EnvironmentTestnet}},
	{"aster", ProductSpot, "", []NativeEnvironment{EnvironmentLive, EnvironmentTestnet}},
	{"aster", ProductPerp, "USDT", []NativeEnvironment{EnvironmentLive, EnvironmentTestnet}},
	{"nado", ProductSpot, "USDT0", []NativeEnvironment{EnvironmentLive, EnvironmentTestnet}},
	{"nado", ProductPerp, "USDT0", []NativeEnvironment{EnvironmentLive, EnvironmentTestnet}},
}

func DefaultRegistry() *Registry {
	registry := &Registry{profiles: map[Key]Profile{}, byID: map[string]Profile{}}
	for _, row := range defaultRows {
		for _, environment := range row.environments {
			mode := ModeTest
			if environment == EnvironmentLive {
				mode = ModeLive
			}
			id := profileID(row.venue, row.product, row.settlement, environment)
			profile := Profile{ID: id, Venue: row.venue, Mode: mode, NativeEnvironment: environment, Product: row.product, SettlementAsset: row.settlement}
			registry.profiles[Key{Venue: row.venue, Mode: mode, RouteProfileID: id}] = profile
			registry.byID[row.venue+"\x00"+id] = profile
		}
	}
	return registry
}

func (registry *Registry) Resolve(mode Mode, venue, routeProfileID string) (Profile, error) {
	if registry == nil || (mode != ModeTest && mode != ModeLive) || venue != strings.ToLower(strings.TrimSpace(venue)) || routeProfileID != strings.TrimSpace(routeProfileID) {
		return Profile{}, ErrUnknownProfile
	}
	if profile, ok := registry.profiles[Key{Venue: venue, Mode: mode, RouteProfileID: routeProfileID}]; ok {
		return profile, nil
	}
	if _, ok := registry.byID[venue+"\x00"+routeProfileID]; ok {
		return Profile{}, ErrModeConflict
	}
	return Profile{}, ErrUnknownProfile
}

func (registry *Registry) ResolveLegacy(mode Mode, venue, product, settlement string, environment NativeEnvironment) (Profile, error) {
	id := profileID(venue, Product(product), settlement, environment)
	profile, err := registry.Resolve(mode, venue, id)
	if err != nil {
		return Profile{}, err
	}
	if profile.Product != Product(product) || profile.SettlementAsset != settlement || profile.NativeEnvironment != environment {
		return Profile{}, ErrUnknownProfile
	}
	return profile, nil
}

func (registry *Registry) SupportsEnvironment(mode Mode, venue string, environment NativeEnvironment) bool {
	if registry == nil {
		return false
	}
	for key, profile := range registry.profiles {
		if key.Mode == mode && key.Venue == venue && profile.NativeEnvironment == environment {
			return true
		}
	}
	return false
}

func (registry *Registry) KnowsVenue(venue string) bool {
	if registry == nil {
		return false
	}
	for key := range registry.profiles {
		if key.Venue == venue {
			return true
		}
	}
	return false
}

func profileID(venue string, product Product, settlement string, environment NativeEnvironment) string {
	parts := []string{venue, string(product)}
	if settlement != "" {
		parts = append(parts, strings.ToLower(settlement))
	}
	return fmt.Sprintf("%s.%s.v1", strings.Join(parts, "."), environment)
}
