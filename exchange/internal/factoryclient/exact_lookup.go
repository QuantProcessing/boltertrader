package factoryclient

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/QuantProcessing/boltertrader/exchange"
)

const exactLookupOperation = "LookupExactOrder"

func exactLookupInstrument(
	ctx context.Context,
	request exchange.ExactOrderLookupRequest,
	venue exchange.Venue,
	product exchange.Product,
) (exchange.Instrument, error) {
	if ctx == nil {
		return exchange.Instrument{}, exactLookupError(venue, product, exchange.KindInvalidRequest, "context is required")
	}
	if err := ctx.Err(); err != nil {
		kind := exchange.KindCanceled
		if err == context.DeadlineExceeded {
			kind = exchange.KindDeadlineExceeded
		}
		return exchange.Instrument{}, exactLookupError(venue, product, kind, "context is not active")
	}
	if err := request.Validate(); err != nil {
		return exchange.Instrument{}, exactLookupError(venue, product, exchange.KindInvalidRequest, "invalid exact order lookup request")
	}
	if request.Instrument == nil {
		return exchange.Instrument{}, exactLookupError(venue, product, exchange.KindInvalidRequest, "instrument is required for exact order lookup")
	}
	instrument := *request.Instrument
	if instrument.Identity.Venue != venue || instrument.Identity.Product != product {
		return exchange.Instrument{}, exactLookupError(venue, product, exchange.KindInvalidRequest, "instrument does not match exact lookup client")
	}
	return instrument, nil
}

func exactLookupPositiveInt64(
	value string,
	venue exchange.Venue,
	product exchange.Product,
	name string,
) (int64, error) {
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 {
		return 0, exactLookupError(venue, product, exchange.KindInvalidRequest, name+" must be a positive decimal int64")
	}
	return parsed, nil
}

func exactLookupFound(
	request exchange.ExactOrderLookupRequest,
	venue exchange.Venue,
	product exchange.Product,
	instrument exchange.Instrument,
	order exchange.Order,
) (exchange.ExactOrderLookupResult, error) {
	evidence := exactLookupEvidence(request, venue, product, instrument, true, true)
	result := exchange.ExactOrderLookupResult{
		Status:   exchange.ExactOrderFound,
		Order:    order,
		Evidence: evidence,
	}
	if err := result.Validate(); err != nil {
		return exchange.ExactOrderLookupResult{}, exactLookupError(venue, product, exchange.KindMalformedResponse, "exact order response identity is inconsistent")
	}
	return result, nil
}

func exactLookupNotFound(
	request exchange.ExactOrderLookupRequest,
	venue exchange.Venue,
	product exchange.Product,
	instrument exchange.Instrument,
) (exchange.ExactOrderLookupResult, error) {
	result := exchange.ExactOrderLookupResult{
		Status:   exchange.ExactOrderNotFound,
		Evidence: exactLookupEvidence(request, venue, product, instrument, true, true),
	}
	if err := result.Validate(); err != nil {
		return exchange.ExactOrderLookupResult{}, exactLookupError(venue, product, exchange.KindMalformedResponse, "exact not-found evidence is inconsistent")
	}
	return result, nil
}

func exactLookupUnconfirmed(
	request exchange.ExactOrderLookupRequest,
	venue exchange.Venue,
	product exchange.Product,
	instrument exchange.Instrument,
	cursor string,
) (exchange.ExactOrderLookupResult, error) {
	evidence := exactLookupEvidence(request, venue, product, instrument, false, false)
	evidence.Method = exchange.ExactOrderEvidenceByOpenOrders
	evidence.NativeCursor = cursor
	result := exchange.ExactOrderLookupResult{
		Status:   exchange.ExactOrderUnconfirmed,
		Evidence: evidence,
	}
	if err := result.Validate(); err != nil {
		return exchange.ExactOrderLookupResult{}, exactLookupError(venue, product, exchange.KindMalformedResponse, "unconfirmed exact order evidence is inconsistent")
	}
	return result, nil
}

func exactLookupEvidence(
	request exchange.ExactOrderLookupRequest,
	venue exchange.Venue,
	product exchange.Product,
	instrument exchange.Instrument,
	authoritative bool,
	complete bool,
) exchange.ExactOrderEvidence {
	scopeInstrument := instrument
	evidence := exchange.ExactOrderEvidence{
		Scope: exchange.ExactOrderScope{
			Venue:      venue,
			Product:    product,
			Instrument: &scopeInstrument,
		},
		Authoritative: authoritative,
		Complete:      complete,
	}
	if request.ClientOrderID != "" {
		evidence.Method = exchange.ExactOrderEvidenceByClientOrderID
		evidence.ObservedClientOrderID = request.ClientOrderID
	} else {
		evidence.Method = exchange.ExactOrderEvidenceByVenueOrderID
		evidence.ObservedVenueOrderID = request.VenueOrderID
	}
	return evidence
}

func exactPortableOrderStatus(raw string) (string, error) {
	status := strings.ToLower(strings.TrimSpace(raw))
	status = strings.ReplaceAll(status, "-", "_")
	status = strings.ReplaceAll(status, " ", "_")
	switch status {
	case "accepted", "accepted_pending", "pending", "in_progress":
		return "pending", nil
	case "new", "live", "open", "resting":
		return "open", nil
	case "partially_filled", "partial":
		return "partially_filled", nil
	case "filled":
		return "filled", nil
	case "canceled", "cancelled", "expired", "mmp_canceled":
		return "canceled", nil
	case "rejected":
		return "rejected", nil
	}
	if strings.Contains(status, "cancel") {
		return "canceled", nil
	}
	if strings.HasSuffix(status, "rejected") {
		return "rejected", nil
	}
	return "", fmt.Errorf("unsupported exact order status")
}

func exactLookupError(
	venue exchange.Venue,
	product exchange.Product,
	kind exchange.ErrorKind,
	message string,
) error {
	return exchange.NewError(kind, exchange.ErrorDetails{
		Venue:       venue,
		Product:     product,
		Operation:   exactLookupOperation,
		SafeMessage: message,
	})
}
