package common

import "testing"

func TestOfficialProfiles(t *testing.T) {
	tests := []struct {
		name     string
		env      Environment
		product  Product
		rest     string
		publicWS string
		userWS   string
		chainID  int64
	}{
		{"spot production", EnvironmentProduction, ProductSpot, "https://sapi.asterdex.com", "wss://sstream.asterdex.com", "wss://sstream.asterdex.com", 1666},
		{"spot testnet", EnvironmentTestnet, ProductSpot, "https://sapi.asterdex-testnet.com", "wss://sstream.asterdex-testnet.com", "wss://sstream.asterdex-testnet.com", 714},
		{"perp production", EnvironmentProduction, ProductPerp, "https://fapi.asterdex.com", "wss://fstream.asterdex.com", "wss://fstream.asterdex.com", 1666},
		{"perp testnet", EnvironmentTestnet, ProductPerp, "https://fapi.asterdex-testnet.com", "wss://fstream5.asterdex-testnet.com", "wss://fstream.asterdex-testnet.com", 714},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile, err := NewProfile(tt.env, tt.product)
			if err != nil {
				t.Fatalf("NewProfile: %v", err)
			}
			if profile.Environment() != tt.env || profile.Product() != tt.product {
				t.Fatalf("identity = %s/%s", profile.Environment(), profile.Product())
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

func TestProfileRejectsCrossEnvironmentEndpoint(t *testing.T) {
	profile, err := NewProfile(EnvironmentTestnet, ProductSpot)
	if err != nil {
		t.Fatal(err)
	}
	if err := profile.ValidateEndpoint(EndpointREST, "https://sapi.asterdex.com"); err == nil {
		t.Fatal("testnet profile accepted production REST endpoint")
	}
	if err := profile.ValidateEndpoint(EndpointPublicWS, "wss://sstream.asterdex-testnet.com"); err != nil {
		t.Fatalf("official testnet endpoint rejected: %v", err)
	}
}

func TestProfileRejectsUnknownValues(t *testing.T) {
	if _, err := NewProfile(Environment("staging"), ProductSpot); err == nil {
		t.Fatal("unknown environment accepted")
	}
	if _, err := NewProfile(EnvironmentTestnet, Product("option")); err == nil {
		t.Fatal("unknown product accepted")
	}
}
