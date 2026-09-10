package factory

import (
	"net/http"
	"testing"

	"github.com/QuantProcessing/boltertrader/credential"
	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/routeprofile"
)

func TestEveryRouteHasOneFactoryBindingCredentialSchemaAndMarketFamily(t *testing.T) {
	profiles := routeprofile.DefaultProfiles()
	if len(configProfiles) != len(profiles) {
		t.Fatal("factory bindings drifted from route catalog")
	}
	seen := map[string]bool{}
	for _, profile := range profiles {
		venue, product := exchange.Venue(profile.Venue), exchange.Product(profile.Product)
		name, ok := publicConnectorConfigName(venue, product, profile.SettlementAsset)
		if !ok || seen[name] {
			t.Fatalf("missing or duplicate binding: %+v", profile)
		}
		seen[name] = true
		v, p, s, ok := catalogIdentityForConfig(name)
		if !ok || v != venue || p != product || s != profile.SettlementAsset {
			t.Fatalf("binding did not round-trip: %s", name)
		}
		connector, err := NewPublicInstrumentConnector(venue, product, s, EnvironmentLive, &http.Client{})
		if err != nil {
			t.Fatal(err)
		}
		if err := connector.Close(); err != nil {
			t.Fatal(err)
		}
		schema, err := credential.DefaultRegistry().Schema(profile.Venue)
		if err != nil || len(schema.Fields) == 0 {
			t.Fatalf("missing credential schema: %s", profile.Venue)
		}
		family := exchange.NativeMarketFamily(venue, product, s)
		if family == "" || len(exchange.MarketCapabilitiesFor(venue, family)) == 0 {
			t.Fatalf("missing native market support: %s", profile.ID)
		}
	}
	if _, _, _, ok := catalogIdentityForConfig("unknown"); ok {
		t.Fatal("unknown factory accepted")
	}
	if exchange.NativeMarketFamily(exchange.VenueBinance, exchange.ProductPerp, "USDC") != "" {
		t.Fatal("unsupported settlement accepted")
	}
}
