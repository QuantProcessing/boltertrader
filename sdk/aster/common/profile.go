package common

import (
	"fmt"
	"net/url"
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
	product       Product
	restURL       string
	publicWSURL   string
	userWSURL     string
	chainID       int64
	allowOverride bool
}

// NewProfile is aster's production hosts for one product.
//
// It used to take an environment as well. The testnet arm it selected carried
// its own chain ID (714 against production's 1666), which is the reason the
// chain ID is still checked in Validate: an override that reaches the wrong
// chain produces signatures the venue silently rejects rather than a transport
// error, so the check stays even though there is now only one chain to be on.
func NewProfile(product Product) (Profile, error) {
	switch product {
	case ProductSpot:
		return Profile{
			product:     product,
			restURL:     "https://sapi.asterdex.com",
			publicWSURL: "wss://sstream.asterdex.com",
			userWSURL:   "wss://sstream.asterdex.com",
			chainID:     1666,
		}, nil
	case ProductPerp:
		return Profile{
			product:     product,
			restURL:     "https://fapi.asterdex.com",
			publicWSURL: "wss://fstream.asterdex.com",
			userWSURL:   "wss://fstream.asterdex.com",
			chainID:     1666,
		}, nil
	default:
		return Profile{}, fmt.Errorf("aster profile: unsupported product %q", product)
	}
}

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
	official, err := NewProfile(p.product)
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
		return fmt.Errorf("aster profile: chain id %d does not match %s", p.chainID, p.product)
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
		return fmt.Errorf("aster profile: %s endpoint override is not allowed for %s", kind, p.product)
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
	return fmt.Sprintf("aster profile{%s}", p.product)
}
