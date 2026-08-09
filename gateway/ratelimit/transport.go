package ratelimit

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
)

type RequestScope string

const (
	PublicScope  RequestScope = "public"
	PrivateScope RequestScope = "private"
)

type VectorProvider interface {
	Keys(*http.Request, RequestScope) []Key
}

type RequestVectorProvider struct {
	venueProfile Key
	principal    *Key
	endpoint     func(*http.Request) (Key, bool)
}

func NewRequestVectorProvider(venueProfile Key, principalIdentity []byte, endpoint func(*http.Request) (Key, bool)) (*RequestVectorProvider, error) {
	if venueProfile.Kind != VenueProfile || venueProfile.validate() != nil || endpoint == nil {
		return nil, ErrInvalidBudget
	}
	provider := &RequestVectorProvider{venueProfile: venueProfile, endpoint: endpoint}
	if len(principalIdentity) != 0 {
		digest := sha256.Sum256(principalIdentity)
		key := Key{Kind: AuthenticatedPrincipal, Value: strings.SplitN(venueProfile.Value, "/", 2)[0] + "/" + hex.EncodeToString(digest[:16])}
		provider.principal = &key
	}
	return provider, nil
}

func (provider *RequestVectorProvider) Keys(request *http.Request, scope RequestScope) []Key {
	if provider == nil || request == nil {
		return nil
	}
	keys := []Key{provider.venueProfile}
	if scope == PrivateScope && provider.principal != nil {
		keys = append(keys, *provider.principal)
	}
	if endpoint, ok := provider.endpoint(request); ok {
		keys = append(keys, endpoint)
	}
	return keys
}

type Transport struct {
	coordinator *Coordinator
	vectors     VectorProvider
	scope       RequestScope
	next        http.RoundTripper
}

func NewTransport(coordinator *Coordinator, vectors VectorProvider, scope RequestScope, next http.RoundTripper) (*Transport, error) {
	if coordinator == nil || vectors == nil || next == nil || (scope != PublicScope && scope != PrivateScope) {
		return nil, ErrInvalidBudget
	}
	return &Transport{coordinator: coordinator, vectors: vectors, scope: scope, next: next}, nil
}

func (transport *Transport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil {
		return nil, fmt.Errorf("rate-limit request is required")
	}
	if err := transport.coordinator.Acquire(request.Context(), transport.vectors.Keys(request, transport.scope)); err != nil {
		return nil, err
	}
	return transport.next.RoundTrip(request)
}
