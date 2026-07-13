package nado

import "testing"

func TestProfileEndpointMatrix(t *testing.T) {
	tests := []struct {
		environment Environment
		chainID     int64
		endpoints   map[EndpointKind]string
	}{
		{
			environment: EnvironmentMainnet,
			chainID:     57073,
			endpoints: map[EndpointKind]string{
				EndpointGatewayV1:       "https://gateway.prod.nado.xyz/v1",
				EndpointGatewayV2:       "https://gateway.prod.nado.xyz/v2",
				EndpointArchiveV1:       "https://archive.prod.nado.xyz/v1",
				EndpointArchiveV2:       "https://archive.prod.nado.xyz/v2",
				EndpointGatewayWS:       "wss://gateway.prod.nado.xyz/v1/ws",
				EndpointSubscriptionsWS: "wss://gateway.prod.nado.xyz/v1/subscribe",
				EndpointTrigger:         "https://trigger.prod.nado.xyz/v1",
			},
		},
		{
			environment: EnvironmentTestnet,
			chainID:     763373,
			endpoints: map[EndpointKind]string{
				EndpointGatewayV1:       "https://gateway.test.nado.xyz/v1",
				EndpointGatewayV2:       "https://gateway.test.nado.xyz/v2",
				EndpointArchiveV1:       "https://archive.test.nado.xyz/v1",
				EndpointArchiveV2:       "https://archive.test.nado.xyz/v2",
				EndpointGatewayWS:       "wss://gateway.test.nado.xyz/v1/ws",
				EndpointSubscriptionsWS: "wss://gateway.test.nado.xyz/v1/subscribe",
				EndpointTrigger:         "https://trigger.test.nado.xyz/v1",
			},
		},
	}
	for _, test := range tests {
		t.Run(string(test.environment), func(t *testing.T) {
			profile, err := NewProfile(test.environment)
			if err != nil {
				t.Fatal(err)
			}
			if profile.ChainID() != test.chainID || profile.Environment() != test.environment {
				t.Fatalf("profile = %s chain=%d", profile, profile.ChainID())
			}
			for kind, expected := range test.endpoints {
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
		})
	}
}

func TestProfileRejectsUnknownEnvironmentAndEndpointOverride(t *testing.T) {
	if _, err := NewProfile(Environment("staging")); err == nil {
		t.Fatal("unknown environment unexpectedly accepted")
	}
	profile, err := NewProfile(EnvironmentTestnet)
	if err != nil {
		t.Fatal(err)
	}
	for kind, endpoint := range map[EndpointKind]string{
		EndpointGatewayV1:       "https://gateway.prod.nado.xyz/v1",
		EndpointGatewayV2:       "https://gateway.prod.nado.xyz/v2",
		EndpointArchiveV1:       "https://archive.prod.nado.xyz/v1",
		EndpointArchiveV2:       "https://archive.prod.nado.xyz/v2",
		EndpointGatewayWS:       "wss://gateway.prod.nado.xyz/v1/ws",
		EndpointSubscriptionsWS: "wss://gateway.prod.nado.xyz/v1/subscribe",
		EndpointTrigger:         "https://trigger.prod.nado.xyz/v1",
	} {
		if err := profile.ValidateEndpoint(kind, endpoint); err == nil {
			t.Errorf("testnet accepted production %s endpoint", kind)
		}
	}
	if err := profile.ValidateEndpoint(EndpointKind("unknown"), "https://gateway.test.nado.xyz/v1"); err == nil {
		t.Fatal("unknown endpoint kind unexpectedly accepted")
	}
}
