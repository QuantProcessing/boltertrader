package nado

import "testing"

func TestProfileEndpoints(t *testing.T) {
	profile, err := NewProfile()
	if err != nil {
		t.Fatal(err)
	}
	if profile.ChainID() != 57073 {
		t.Fatalf("profile = %s chain=%d", profile, profile.ChainID())
	}
	for kind, expected := range map[EndpointKind]string{
		EndpointGatewayV1:       "https://gateway.prod.nado.xyz/v1",
		EndpointGatewayV2:       "https://gateway.prod.nado.xyz/v2",
		EndpointArchiveV1:       "https://archive.prod.nado.xyz/v1",
		EndpointArchiveV2:       "https://archive.prod.nado.xyz/v2",
		EndpointGatewayWS:       "wss://gateway.prod.nado.xyz/v1/ws",
		EndpointSubscriptionsWS: "wss://gateway.prod.nado.xyz/v1/subscribe",
		EndpointTrigger:         "https://trigger.prod.nado.xyz/v1",
	} {
		if got := profile.Endpoint(kind); got != expected {
			t.Errorf("%s = %q, want %q", kind, got, expected)
		}
		if err := profile.ValidateEndpoint(kind, expected); err != nil {
			t.Errorf("validate %s: %v", kind, err)
		}
	}
	if err := profile.Validate(); err != nil {
		t.Fatal(err)
	}
}

// The rejection half of this used to be phrased as testnet-refuses-production,
// which had two host sets to play one against the other. There is one host set
// now, but the property being guarded was never really "wrong environment" — it
// was that a profile refuses to be pointed anywhere but its own endpoints, which
// is what keeps an override from silently redirecting signed orders.
func TestProfileRejectsForeignEndpointAndUnknownKind(t *testing.T) {
	profile, err := NewProfile()
	if err != nil {
		t.Fatal(err)
	}
	for kind, endpoint := range map[EndpointKind]string{
		EndpointGatewayV1:       "https://gateway.example.com/v1",
		EndpointGatewayV2:       "https://gateway.example.com/v2",
		EndpointArchiveV1:       "https://archive.example.com/v1",
		EndpointArchiveV2:       "https://archive.example.com/v2",
		EndpointGatewayWS:       "wss://gateway.example.com/v1/ws",
		EndpointSubscriptionsWS: "wss://gateway.example.com/v1/subscribe",
		EndpointTrigger:         "https://trigger.example.com/v1",
	} {
		if err := profile.ValidateEndpoint(kind, endpoint); err == nil {
			t.Errorf("profile accepted foreign %s endpoint", kind)
		}
	}
	if err := profile.ValidateEndpoint(EndpointKind("unknown"), "https://gateway.prod.nado.xyz/v1"); err == nil {
		t.Fatal("unknown endpoint kind unexpectedly accepted")
	}
}
