package sdk

import "testing"

// The profile a caller gets with no arguments must be gate's real one. This used
// to sit beside a testnet counterpart and the test's job was to keep the two
// apart; now there is one profile and the job is to keep it from drifting into a
// half-filled struct, since an empty WS URL fails as a connection error at
// subscribe time rather than at construction.
func TestMainnetEnvironmentProfileIsFullyPopulated(t *testing.T) {
	mainnet := MainnetEnvironmentProfile()
	if mainnet.RESTBaseURL != defaultRESTBaseURL {
		t.Fatalf("rest base URL = %q, want %q", mainnet.RESTBaseURL, defaultRESTBaseURL)
	}
	if mainnet.SpotWSURL != defaultSpotWSURL {
		t.Fatalf("spot WS URL = %q, want %q", mainnet.SpotWSURL, defaultSpotWSURL)
	}
	if mainnet.FuturesUSDTWSURL != defaultFuturesUSDTWSURL {
		t.Fatalf("futures WS URL = %q, want %q", mainnet.FuturesUSDTWSURL, defaultFuturesUSDTWSURL)
	}
}

func TestNewWSClientWithProfileSelectsURLByProduct(t *testing.T) {
	profile := EnvironmentProfile{
		RESTBaseURL:      "https://rest.test/api/v4",
		SpotWSURL:        "wss://spot.test/ws",
		FuturesUSDTWSURL: "wss://futures.test/ws",
	}
	spot, err := NewWSClientWithProfile(profile, ProductSpot)
	if err != nil {
		t.Fatal(err)
	}
	if spot.url != profile.SpotWSURL {
		t.Fatalf("spot url = %q, want %q", spot.url, profile.SpotWSURL)
	}
	futures, err := NewWSClientWithProfile(profile, ProductFuturesUSDT)
	if err != nil {
		t.Fatal(err)
	}
	if futures.url != profile.FuturesUSDTWSURL {
		t.Fatalf("futures url = %q, want %q", futures.url, profile.FuturesUSDTWSURL)
	}
}
