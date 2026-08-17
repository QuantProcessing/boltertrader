package nado

import (
	"fmt"
	"net/url"
)

type EndpointKind string

const (
	EndpointGatewayV1       EndpointKind = "gateway_v1"
	EndpointGatewayV2       EndpointKind = "gateway_v2"
	EndpointArchiveV1       EndpointKind = "archive_v1"
	EndpointArchiveV2       EndpointKind = "archive_v2"
	EndpointGatewayWS       EndpointKind = "gateway_ws"
	EndpointSubscriptionsWS EndpointKind = "subscriptions_ws"
	EndpointTrigger         EndpointKind = "trigger"
)

var endpointKinds = []EndpointKind{
	EndpointGatewayV1,
	EndpointGatewayV2,
	EndpointArchiveV1,
	EndpointArchiveV2,
	EndpointGatewayWS,
	EndpointSubscriptionsWS,
	EndpointTrigger,
}

type Profile struct {
	chainID       int64
	endpoints     map[EndpointKind]string
	allowOverride bool
}

// NewProfile is nado's mainnet hosts.
//
// It used to take an environment. The testnet arm it selected carried its own
// chain ID (763373 against mainnet's 57073), which is why Validate still checks
// the chain ID: a profile reaching the wrong chain produces signatures the venue
// rejects as malformed rather than a transport error, so the check stays even
// though there is now only one chain to be on.
func NewProfile() (Profile, error) {
	profile := officialProfile()
	return profile, profile.Validate()
}

func (p Profile) ChainID() int64 { return p.chainID }

func (p Profile) Endpoint(kind EndpointKind) string { return p.endpoints[kind] }

func (p Profile) GatewayV1URL() string { return p.Endpoint(EndpointGatewayV1) }

func (p Profile) GatewayV2URL() string { return p.Endpoint(EndpointGatewayV2) }

func (p Profile) ArchiveV1URL() string { return p.Endpoint(EndpointArchiveV1) }

func (p Profile) ArchiveV2URL() string { return p.Endpoint(EndpointArchiveV2) }

func (p Profile) GatewayWSURL() string { return p.Endpoint(EndpointGatewayWS) }

func (p Profile) SubscriptionsWSURL() string { return p.Endpoint(EndpointSubscriptionsWS) }

func (p Profile) TriggerURL() string { return p.Endpoint(EndpointTrigger) }

func (p Profile) WithEndpointOverrides(overrides map[EndpointKind]string) (Profile, error) {
	if p.endpoints == nil {
		p.endpoints = make(map[EndpointKind]string)
	}
	cloned := make(map[EndpointKind]string, len(p.endpoints))
	for kind, endpoint := range p.endpoints {
		cloned[kind] = endpoint
	}
	for kind, endpoint := range overrides {
		if endpoint != "" {
			cloned[kind] = endpoint
		}
	}
	p.endpoints = cloned
	p.allowOverride = true
	for _, kind := range endpointKinds {
		if err := validateEndpointURL(kind, p.Endpoint(kind)); err != nil {
			return Profile{}, err
		}
	}
	return p, nil
}

func (p Profile) Validate() error {
	official := officialProfile()
	if p.chainID != official.chainID {
		return fmt.Errorf("nado profile: chain id %d does not match the official profile", p.chainID)
	}
	if p.allowOverride {
		for _, kind := range endpointKinds {
			if err := validateEndpointURL(kind, p.Endpoint(kind)); err != nil {
				return err
			}
		}
	} else {
		for _, kind := range endpointKinds {
			if err := official.ValidateEndpoint(kind, p.Endpoint(kind)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p Profile) ValidateEndpoint(kind EndpointKind, rawURL string) error {
	expected, exists := p.endpoints[kind]
	if !exists || expected == "" {
		return fmt.Errorf("nado profile: unsupported endpoint kind %q", kind)
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("nado profile: invalid %s endpoint", kind)
	}
	if rawURL != expected {
		return fmt.Errorf("nado profile: %s endpoint override is not allowed", kind)
	}
	return nil
}

func validateEndpointURL(kind EndpointKind, rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("nado profile: invalid %s endpoint", kind)
	}
	return nil
}

func (p Profile) String() string {
	return fmt.Sprintf("nado profile{chain=%d}", p.chainID)
}

func officialProfile() Profile {
	return Profile{
		chainID: 57073,
		endpoints: map[EndpointKind]string{
			EndpointGatewayV1:       "https://gateway.prod.nado.xyz/v1",
			EndpointGatewayV2:       "https://gateway.prod.nado.xyz/v2",
			EndpointArchiveV1:       "https://archive.prod.nado.xyz/v1",
			EndpointArchiveV2:       "https://archive.prod.nado.xyz/v2",
			EndpointGatewayWS:       "wss://gateway.prod.nado.xyz/v1/ws",
			EndpointSubscriptionsWS: "wss://gateway.prod.nado.xyz/v1/subscribe",
			EndpointTrigger:         "https://trigger.prod.nado.xyz/v1",
		},
	}
}
