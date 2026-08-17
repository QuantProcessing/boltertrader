package common

import "testing"

func TestOfficialProfiles(t *testing.T) {
	tests := []struct {
		name     string
		product  Product
		rest     string
		publicWS string
		userWS   string
		chainID  int64
	}{
		{"spot", ProductSpot, "https://sapi.asterdex.com", "wss://sstream.asterdex.com", "wss://sstream.asterdex.com", 1666},
		{"perp", ProductPerp, "https://fapi.asterdex.com", "wss://fstream.asterdex.com", "wss://fstream.asterdex.com", 1666},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile, err := NewProfile(tt.product)
			if err != nil {
				t.Fatalf("NewProfile: %v", err)
			}
			if profile.Product() != tt.product {
				t.Fatalf("product = %s", profile.Product())
			}
			if profile.RESTURL() != tt.rest || profile.PublicWSURL() != tt.publicWS || profile.UserWSURL() != tt.userWS {
				t.Fatalf("endpoints = %q %q %q", profile.RESTURL(), profile.PublicWSURL(), profile.UserWSURL())
			}
			if profile.ChainID() != tt.chainID {
				t.Fatalf("chain id = %d, want %d", profile.ChainID(), tt.chainID)
			}
			if err := profile.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
		})
	}
}

// The endpoint check used to be phrased as testnet-refuses-production. What it
// was really asserting is that a profile refuses any host but its own, and that
// survives testnet: spot and perp sit on different hosts, so pointing one at the
// other is the same mistake with the same consequence.
func TestProfileRejectsForeignEndpoint(t *testing.T) {
	profile, err := NewProfile(ProductSpot)
	if err != nil {
		t.Fatal(err)
	}
	if err := profile.ValidateEndpoint(EndpointREST, "https://fapi.asterdex.com"); err == nil {
		t.Fatal("spot profile accepted the perp REST endpoint")
	}
	if err := profile.ValidateEndpoint(EndpointPublicWS, "wss://sstream.asterdex.com"); err != nil {
		t.Fatalf("official endpoint rejected: %v", err)
	}
}

func TestProfileRejectsUnknownProduct(t *testing.T) {
	if _, err := NewProfile(Product("option")); err == nil {
		t.Fatal("unknown product accepted")
	}
}
