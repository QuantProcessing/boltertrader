package ratelimit

import (
	"fmt"
	"net/http"
)

type RequestScope string

const (
	PublicScope  RequestScope = "public"
	PrivateScope RequestScope = "private"
)

type VectorProvider interface {
	Keys(*http.Request, RequestScope) []Key
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
