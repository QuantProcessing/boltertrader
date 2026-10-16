package factoryclient

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/internal/venuehttp"
)

// Strip native causes at the adapter boundary while retaining structured,
// non-secret transport evidence. The venue's own rejection text is kept in
// Details().VenueMessage; nothing else from the native error crosses.
func normalizeHTTPError(mapped, original error, venue exchange.Venue, product exchange.Product, operation string) error {
	if mapped == nil {
		return nil
	}
	var normalized *exchange.Error
	if !errors.As(mapped, &normalized) {
		return mapped
	}
	details, kind := normalized.Details(), normalized.Kind()
	var syntax *json.SyntaxError
	var unmarshal *json.UnmarshalTypeError
	if errors.Is(original, venuehttp.ErrMalformedResponse) || errors.As(original, &syntax) || errors.As(original, &unmarshal) {
		if kind != exchange.KindAmbiguousOutcome {
			kind = exchange.KindMalformedResponse
		}
		details.SafeMessage = "malformed venue response"
	}
	var httpErr *venuehttp.HTTPError
	if errors.As(original, &httpErr) {
		details.HTTPStatus = httpErr.StatusCode
		details.RetryAfter = max(details.RetryAfter, httpErr.RetryAfter)
		if details.Code == "" {
			details.Code = httpErr.Code
		}
		switch {
		case httpErr.StatusCode >= 500 || httpErr.StatusCode == 408:
			if kind != exchange.KindAmbiguousOutcome {
				kind = exchange.KindTransport
			}
			details.SafeMessage = "venue server or timeout response"
		case httpErr.StatusCode == 429 || httpErr.StatusCode == 418:
			if kind != exchange.KindAmbiguousOutcome {
				kind = exchange.KindRateLimit
			}
			details.SafeMessage = "venue rate limit"
		}
	}
	if errors.Is(original, venuehttp.ErrRateLimited) && kind != exchange.KindAmbiguousOutcome && details.HTTPStatus < 500 {
		kind = exchange.KindRateLimit
		details.SafeMessage = "request rate limited"
	}
	var local *venuehttp.LocalRateLimitError
	if errors.As(original, &local) {
		kind = exchange.KindRateLimit
		details.Code, details.SafeMessage = "LOCAL_RATE_LIMIT", "HTTP request was not sent: local budget exhausted"
		details.RetryAfter = local.RetryAfter
		// An order may issue a follow-up query. Its failure is not proof that the
		// earlier mutation was never sent.
		op := strings.ToLower(strings.ReplaceAll(operation, "_", ""))
		details.CommandNotSent = (local.Action == "place" && (op == "place" || op == "placeorder")) ||
			(local.Action == "cancel" && (op == "cancel" || op == "cancelorder"))
	}
	details.Venue, details.Product, details.Operation = venue, product, operation
	return withVenueMessage(exchange.NewError(kind, details), nil, original)
}

func normalizeHTTPCommand(ack *exchange.OrderAcknowledgement, mapped *error, original error, venue exchange.Venue, product exchange.Product, operation string) {
	var httpErr *venuehttp.HTTPError
	if errors.As(original, &httpErr) && (httpErr.StatusCode >= 500 || httpErr.StatusCode == 408 || httpErr.StatusCode == 429 || httpErr.StatusCode == 418) {
		// An error-looking payload returned by a proxy/server failure cannot prove
		// that the venue did not execute the command.
		ack.State = exchange.AckAmbiguous
		ack.VenueMessage = "order command outcome is unknown after possible send"
		*mapped = exchange.NewError(exchange.KindAmbiguousOutcome, exchange.ErrorDetails{Venue: venue, Product: product, Operation: operation, Code: ack.VenueCode, SafeMessage: "order command outcome is unknown after possible send"})
	}
	*mapped = normalizeHTTPError(*mapped, original, venue, product, operation)
	*mapped = withVenueMessage(*mapped, ack, original)
}
