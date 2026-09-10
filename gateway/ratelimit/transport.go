package ratelimit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/QuantProcessing/boltertrader/internal/venuehttp"
)

// These are local operating ceilings, not a model of all venue quotas. They limit this
// Gateway process, not other processes or external callers sharing its IP/UID.
var localUnitsPerMinute = map[string]int{"binance": 1200, "aster": 1200, "bybit": 1200, "bitget": 600, "okx": 600, "gate": 600, "hyperliquid": 600, "lighter": 300, "nado": 300}

type Transport struct {
	coordinator *Coordinator
	next        http.RoundTripper
}

func NewTransport(coordinator *Coordinator, next http.RoundTripper) (*Transport, error) {
	if coordinator == nil || next == nil {
		return nil, ErrInvalidBudget
	}
	return &Transport{coordinator: coordinator, next: next}, nil
}

func (transport *Transport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil || request.URL == nil {
		return nil, fmt.Errorf("rate-limit request is required")
	}
	policy := venuehttp.PolicyForRequest(request)
	limit, known := localUnitsPerMinute[policy.Venue]
	if !known || policy.Endpoint == "" || policy.Cost <= 0 {
		return nil, fmt.Errorf("SDK request rate policy is required")
	}
	venueKey := Key{Kind: VenueBudget, Value: policy.Venue + "/egress"}
	principal, _ := request.Context().Value(principalKey{}).(string)
	endpointScope := policy.Venue
	if principal != "" {
		endpointScope += "/" + principal
	}
	endpointKey := Key{Kind: EndpointBudget, Value: endpointScope + "/" + policy.Endpoint}
	// Keep a fifth of the shared allowance available for cancellation. Venue
	// cooldowns still apply to every request; priority never bypasses an IP ban.
	reserve := limit / 5
	if policy.Action == "cancel" {
		reserve = 0
	}
	charges := []Charge{{Key: venueKey, Cost: policy.Cost, Reserve: reserve}}
	if err := transport.coordinator.Configure(venueKey, Quota{Limit: limit, Interval: time.Minute}); err != nil {
		return nil, err
	}

	// Endpoint accounting is in actual request units. Expensive snapshot weights
	// additionally consume the shared venue allowance above.
	endpointLimit := 10
	if policy.Venue == "bybit" {
		switch policy.Endpoint {
		case "POST /v5/order/cancel-all":
			endpointLimit = 1
		case "GET /v5/account/fee-rate", "POST /v5/account/set-margin-mode":
			endpointLimit = 5
		}
	}
	if policy.Venue == "nado" || policy.Venue == "lighter" {
		endpointLimit = 5
	}
	if err := transport.coordinator.Configure(endpointKey, Quota{Limit: endpointLimit, Interval: time.Second}); err != nil {
		return nil, err
	}
	charges = append(charges, Charge{Key: endpointKey, Cost: 1})
	delay, err := transport.coordinator.TryAcquire(request.Context(), charges)
	if err != nil {
		return nil, err
	}
	if delay > 0 {
		return nil, &venuehttp.LocalRateLimitError{RetryAfter: delay, Action: policy.Action}
	}
	response, err := transport.next.RoundTrip(request)
	if response != nil {
		delay := venuehttp.RetryAfter(response.Header, transport.coordinator.now())
		if response.StatusCode == 429 || response.StatusCode == 418 {
			if delay <= 0 {
				delay = time.Second
			}
			transport.coordinator.Block(venueKey, transport.coordinator.now().Add(delay))
		}
		// Bybit reports API quota exhaustion even with HTTP 200.
		if policy.Venue == "bybit" && response.Header.Get("X-Bapi-Limit-Status") == "0" {
			if millis, parseErr := strconv.ParseInt(response.Header.Get("X-Bapi-Limit-Reset-Timestamp"), 10, 64); parseErr == nil {
				transport.coordinator.Block(endpointKey, time.UnixMilli(millis))
			}
		}
	}
	return response, err
}

type principalKey struct{}
type principalTransport struct {
	principal string
	next      http.RoundTripper
}

func (transport principalTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport.next.RoundTrip(request.WithContext(context.WithValue(request.Context(), principalKey{}, transport.principal)))
}

// WithPrincipal keeps one local account budget across route rebuilds and key rotations.
func WithPrincipal(client *http.Client, accountID string) *http.Client {
	clone := *client
	next := clone.Transport
	if next == nil {
		next = http.DefaultTransport
	}
	hash := sha256.Sum256([]byte(accountID))
	clone.Transport = principalTransport{principal: hex.EncodeToString(hash[:16]), next: next}
	return &clone
}

func (*Transport) HasSharedHTTPBudget() bool { return true }
func (transport principalTransport) HasSharedHTTPBudget() bool {
	shared, ok := transport.next.(venuehttp.SharedHTTPBudget)
	return ok && shared.HasSharedHTTPBudget()
}
