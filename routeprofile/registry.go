package routeprofile

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

var ErrUnknownProfile = errors.New("unknown route profile")

type Registry struct {
	profiles map[Key]Profile
}

type profileRow struct {
	venue      string
	product    Product
	settlement string
}

// Every venue-product pair the platform routes to. Each row used to expand into
// one profile per environment the venue offered — a venue with a demo endpoint
// and a testnet produced three. The endpoints those extra profiles pointed at
// are gone, so a row is now exactly one profile.
var defaultRows = []profileRow{
	{"binance", ProductSpot, ""},
	{"binance", ProductPerp, "USDT"},
	{"okx", ProductSpot, ""},
	{"okx", ProductPerp, "USDT"},
	{"lighter", ProductSpot, ""},
	{"lighter", ProductPerp, "USDC"},
	{"hyperliquid", ProductSpot, ""},
	{"hyperliquid", ProductPerp, "USDC"},
	{"bybit", ProductSpot, ""},
	{"bybit", ProductPerp, "USDT"},
	{"bybit", ProductPerp, "USDC"},
	{"bitget", ProductSpot, ""},
	{"bitget", ProductPerp, "USDT"},
	{"bitget", ProductPerp, "USDC"},
	{"gate", ProductSpot, ""},
	{"gate", ProductPerp, "USDT"},
	{"aster", ProductSpot, ""},
	{"aster", ProductPerp, "USDT"},
	{"nado", ProductSpot, "USDT0"},
	{"nado", ProductPerp, "USDT0"},
}

func DefaultRegistry() *Registry {
	registry := &Registry{profiles: map[Key]Profile{}}
	for _, row := range defaultRows {
		id := profileID(row.venue, row.product, row.settlement)
		registry.profiles[Key{Venue: row.venue, RouteProfileID: id}] = Profile{
			ID:              id,
			Venue:           row.venue,
			Product:         row.product,
			SettlementAsset: row.settlement,
		}
	}
	return registry
}

// Resolve looks up a profile by the venue and ID a caller holds.
//
// The comparisons against the trimmed and lowercased forms are deliberate: an
// ID that only differs by case or whitespace is a caller bug, and normalising
// it here would let that bug reach the venue as a silently different route.
func (registry *Registry) Resolve(venue, routeProfileID string) (Profile, error) {
	if registry == nil || venue != strings.ToLower(strings.TrimSpace(venue)) || routeProfileID != strings.TrimSpace(routeProfileID) {
		return Profile{}, ErrUnknownProfile
	}
	if profile, ok := registry.profiles[Key{Venue: venue, RouteProfileID: routeProfileID}]; ok {
		return profile, nil
	}
	return Profile{}, ErrUnknownProfile
}

func (registry *Registry) Profiles() []Profile {
	if registry == nil {
		return nil
	}
	profiles := make([]Profile, 0, len(registry.profiles))
	for _, profile := range registry.profiles {
		profiles = append(profiles, profile)
	}
	sort.Slice(profiles, func(i, j int) bool {
		if profiles[i].Venue == profiles[j].Venue {
			return profiles[i].ID < profiles[j].ID
		}
		return profiles[i].Venue < profiles[j].Venue
	})
	return profiles
}

// ResolveLegacy resolves from the parts a profile ID is built out of, for
// callers that still carry product and settlement separately rather than the ID.
func (registry *Registry) ResolveLegacy(venue, product, settlement string) (Profile, error) {
	profile, err := registry.Resolve(venue, profileID(venue, Product(product), settlement))
	if err != nil {
		return Profile{}, err
	}
	if profile.Product != Product(product) || profile.SettlementAsset != settlement {
		return Profile{}, ErrUnknownProfile
	}
	return profile, nil
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

func profileID(venue string, product Product, settlement string) string {
	parts := []string{venue, string(product)}
	if settlement != "" {
		parts = append(parts, strings.ToLower(settlement))
	}
	return fmt.Sprintf("%s.v1", strings.Join(parts, "."))
}
