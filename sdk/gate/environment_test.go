package sdk

import "testing"

func TestEnvironmentProfiles(t *testing.T) {
	mainnet := MainnetEnvironmentProfile()
	if mainnet.RESTBaseURL != defaultRESTBaseURL || mainnet.OfficialTestnet {
		t.Fatalf("unexpected mainnet profile: %+v", mainnet)
	}

	testnet := TestnetEnvironmentProfile()
	if testnet.RESTBaseURL != defaultTestnetRESTBaseURL || !testnet.OfficialTestnet {
		t.Fatalf("unexpected testnet profile: %+v", testnet)
	}

	custom, err := NewTestnetEnvironmentProfile("https://rest.test/api/v4", "wss://spot.test/ws", "wss://futures.test/ws")
	if err != nil {
		t.Fatal(err)
	}
	if custom.RESTBaseURL != "https://rest.test/api/v4" || custom.SpotWSURL != "wss://spot.test/ws" {
		t.Fatalf("unexpected custom profile: %+v", custom)
	}
}

func TestEnvironmentProfileRejectsInvalidURLs(t *testing.T) {
	if _, err := NewTestnetEnvironmentProfile("", "wss://spot.test/ws", "wss://futures.test/ws"); err == nil {
		t.Fatal("expected missing rest URL error")
	}
	if _, err := NewTestnetEnvironmentProfile("https://rest.test/api/v4", ":", "wss://futures.test/ws"); err == nil {
		t.Fatal("expected invalid spot URL error")
	}
}
