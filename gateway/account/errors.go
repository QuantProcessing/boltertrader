package account

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/QuantProcessing/boltertrader/exchange"
)

// Category is the caller-facing class of a venue failure. It is the same for
// every venue, so a client can explain any failure without venue knowledge.
type Category string

const (
	// CategoryNetwork: the venue could not be reached or did not answer in
	// time. Nothing about the request itself is known to be wrong; retry later.
	CategoryNetwork Category = "network"
	// CategoryRejected: the venue received the request and refused it for a
	// business reason (margin, size, price, market state…).
	CategoryRejected Category = "rejected"
	// CategoryCredentials: the venue refused the key/signature or its permissions.
	CategoryCredentials Category = "credentials"
	// CategoryRateLimited: the venue or the local budget throttled the call.
	CategoryRateLimited Category = "rate_limited"
	// CategoryInvalid: the request is invalid before reaching the venue.
	CategoryInvalid Category = "invalid"
	// CategoryUnsupported: the venue or market does not offer the operation.
	CategoryUnsupported Category = "unsupported"
	// CategoryVenue: the venue answered with something unusable (malformed
	// response, unexpected state). Usually transient.
	CategoryVenue Category = "venue_error"
)

// VenueFailure is a classified venue error, safe to return to callers.
type VenueFailure struct {
	Category     Category
	Venue        string
	Code         string
	VenueMessage string // the venue's own text, when it provided one
	Retryable    bool
}

func (failure *VenueFailure) Error() string {
	text := failure.Message()
	if failure.Code != "" {
		text += " (" + failure.Code + ")"
	}
	return text
}

// Message is a short human explanation for display.
func (failure *VenueFailure) Message() string {
	venue := failure.Venue
	if venue == "" {
		venue = "the exchange"
	}
	switch failure.Category {
	case CategoryNetwork:
		return fmt.Sprintf("Cannot reach %s right now. Check the network or proxy and try again.", venue)
	case CategoryRejected:
		if failure.VenueMessage != "" {
			return failure.VenueMessage
		}
		return fmt.Sprintf("%s rejected the request.", venue)
	case CategoryCredentials:
		if failure.VenueMessage != "" {
			return fmt.Sprintf("%s rejected the API key: %s", venue, failure.VenueMessage)
		}
		return fmt.Sprintf("%s rejected the API key or its permissions.", venue)
	case CategoryRateLimited:
		return fmt.Sprintf("%s rate limit reached. Wait a moment and try again.", venue)
	case CategoryInvalid:
		if failure.VenueMessage != "" {
			return failure.VenueMessage
		}
		return "The request is invalid."
	case CategoryUnsupported:
		return fmt.Sprintf("%s does not support this operation.", venue)
	default:
		return fmt.Sprintf("%s returned an unexpected response. Try again.", venue)
	}
}

// Classify maps any error from an exchange client onto a VenueFailure.
// It returns nil for nil.
func Classify(err error) *VenueFailure {
	if err == nil {
		return nil
	}
	var existing *VenueFailure
	if errors.As(err, &existing) {
		return existing
	}
	failure := &VenueFailure{Category: CategoryVenue}
	var normalized *exchange.Error
	if errors.As(err, &normalized) {
		details := normalized.Details()
		failure.Venue, failure.Code, failure.VenueMessage = venueName(details.Venue), details.Code, details.VenueMessage
		switch normalized.Kind() {
		case exchange.KindTransport, exchange.KindDeadlineExceeded, exchange.KindCanceled,
			exchange.KindSubscriptionClosed, exchange.KindSubscriptionGap:
			failure.Category, failure.Retryable = CategoryNetwork, true
		case exchange.KindVenueRejected, exchange.KindNotFound:
			failure.Category = CategoryRejected
		case exchange.KindAuthentication, exchange.KindPermission:
			failure.Category = CategoryCredentials
		case exchange.KindRateLimit:
			failure.Category, failure.Retryable = CategoryRateLimited, true
		case exchange.KindInvalidRequest, exchange.KindInvalidConfig:
			failure.Category = CategoryInvalid
		case exchange.KindUnsupported:
			failure.Category = CategoryUnsupported
		default: // malformed response, metadata resolution, ambiguous outcome
			failure.Category, failure.Retryable = CategoryVenue, true
		}
		// A venue that answered 5xx/408 is a reachability problem for the user.
		if details.HTTPStatus >= 500 || details.HTTPStatus == 408 {
			failure.Category, failure.Retryable = CategoryNetwork, true
		}
		return failure
	}
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || errors.As(err, &netErr) {
		failure.Category, failure.Retryable = CategoryNetwork, true
	}
	return failure
}

var venueNames = map[exchange.Venue]string{
	exchange.VenueBinance: "Binance", exchange.VenueOKX: "OKX", exchange.VenueBybit: "Bybit",
	exchange.VenueBitget: "Bitget", exchange.VenueGate: "Gate", exchange.VenueHyperliquid: "Hyperliquid",
	exchange.VenueLighter: "Lighter", exchange.VenueAster: "Aster", exchange.VenueNado: "Nado",
}

func venueName(venue exchange.Venue) string {
	if name, ok := venueNames[venue]; ok {
		return name
	}
	return string(venue)
}
