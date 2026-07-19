package common

import (
	"fmt"
	"net/url"
)

type Environment string

const (
	EnvironmentProduction Environment = "production"
	EnvironmentTestnet    Environment = "testnet"
)

type Product string

const (
	ProductSpot Product = "spot"
	ProductPerp Product = "perp"
)

type EndpointKind string

const (
	EndpointREST     EndpointKind = "rest"
	EndpointPublicWS EndpointKind = "public_ws"
	EndpointUserWS   EndpointKind = "user_ws"
)

type Profile struct {
	environment   Environment
	product       Product
	restURL       string
	publicWSURL   string
	userWSURL     string
	chainID       int64
	allowOverride bool
}

func NewProfile(environment Environment, product Product) (Profile, error) {
	switch environment {
	case EnvironmentProduction:
		switch product {
		case ProductSpot:
			return Profile{
				environment: environment,
				product:     product,
				restURL:     "https://sapi.asterdex.com",
				publicWSURL: "wss://sstream.asterdex.com",
				userWSURL:   "wss://sstream.asterdex.com",
				chainID:     1666,
			}, nil
		case ProductPerp:
			return Profile{
				environment: environment,
				product:     product,
				restURL:     "https://fapi.asterdex.com",
				publicWSURL: "wss://fstream.asterdex.com",
				userWSURL:   "wss://fstream.asterdex.com",
				chainID:     1666,
			}, nil
		default:
			return Profile{}, fmt.Errorf("aster profile: unsupported product %q", product)
		}
	case EnvironmentTestnet:
		switch product {
		case ProductSpot:
			return Profile{
				environment: environment,
				product:     product,
				restURL:     "https://sapi.asterdex-testnet.com",
				publicWSURL: "wss://sstream.asterdex-testnet.com",
				userWSURL:   "wss://sstream.asterdex-testnet.com",
				chainID:     714,
			}, nil
		case ProductPerp:
			return Profile{
				environment: environment,
				product:     product,
				restURL:     "https://fapi.asterdex-testnet.com",
				publicWSURL: "wss://fstream5.asterdex-testnet.com",
				userWSURL:   "wss://fstream.asterdex-testnet.com",
				chainID:     714,
			}, nil
		default:
			return Profile{}, fmt.Errorf("aster profile: unsupported product %q", product)
		}
	default:
		return Profile{}, fmt.Errorf("aster profile: unsupported environment %q", environment)
	}
}

func (p Profile) Environment() Environment { return p.environment }

func (p Profile) Product() Product { return p.product }

func (p Profile) RESTURL() string { return p.restURL }

func (p Profile) PublicWSURL() string { return p.publicWSURL }

func (p Profile) UserWSURL() string { return p.userWSURL }

func (p Profile) ChainID() int64 { return p.chainID }

func (p Profile) WithEndpointOverrides(restURL, publicWSURL, userWSURL string) (Profile, error) {
	if restURL != "" {
		p.restURL = restURL
	}
	if publicWSURL != "" {
		p.publicWSURL = publicWSURL
	}
	if userWSURL != "" {
		p.userWSURL = userWSURL
	}
	p.allowOverride = true
	for kind, endpoint := range map[EndpointKind]string{
		EndpointREST:     p.restURL,
		EndpointPublicWS: p.publicWSURL,
		EndpointUserWS:   p.userWSURL,
	} {
		if err := validateEndpointURL(kind, endpoint); err != nil {
			return Profile{}, err
		}
	}
	return p, nil
}

func (p Profile) Validate() error {
	official, err := NewProfile(p.environment, p.product)
	if err != nil {
		return err
	}
	if p.allowOverride {
		for kind, endpoint := range map[EndpointKind]string{
			EndpointREST:     p.restURL,
			EndpointPublicWS: p.publicWSURL,
			EndpointUserWS:   p.userWSURL,
		} {
			if err := validateEndpointURL(kind, endpoint); err != nil {
				return err
			}
		}
	} else {
		for kind, endpoint := range map[EndpointKind]string{
			EndpointREST:     p.restURL,
			EndpointPublicWS: p.publicWSURL,
			EndpointUserWS:   p.userWSURL,
		} {
			if err := official.ValidateEndpoint(kind, endpoint); err != nil {
				return err
			}
		}
	}
	if p.chainID != official.chainID {
		return fmt.Errorf("aster profile: chain id %d does not match %s", p.chainID, p.environment)
	}
	return nil
}

func (p Profile) ValidateEndpoint(kind EndpointKind, rawURL string) error {
	var expected string
	switch kind {
	case EndpointREST:
		expected = p.restURL
	case EndpointPublicWS:
		expected = p.publicWSURL
	case EndpointUserWS:
		expected = p.userWSURL
	default:
		return fmt.Errorf("aster profile: unsupported endpoint kind %q", kind)
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("aster profile: invalid %s endpoint", kind)
	}
	if rawURL != expected {
		return fmt.Errorf("aster profile: %s endpoint override is not allowed for %s/%s", kind, p.environment, p.product)
	}
	return nil
}

func validateEndpointURL(kind EndpointKind, rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("aster profile: invalid %s endpoint", kind)
	}
	return nil
}

func (p Profile) String() string {
	return fmt.Sprintf("aster profile{%s/%s}", p.environment, p.product)
}
