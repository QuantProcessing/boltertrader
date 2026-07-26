package exchange

import "context"

type ExactOrderLookup interface {
	LookupExactOrder(context.Context, ExactOrderLookupRequest) (ExactOrderLookupResult, error)
}

type ExactOrderLookupRequest struct {
	Instrument    *Instrument `json:"instrument,omitempty"`
	ClientOrderID string      `json:"client_order_id,omitempty"`
	VenueOrderID  string      `json:"venue_order_id,omitempty"`
}

func (request ExactOrderLookupRequest) Validate() error {
	identities := 0
	if request.ClientOrderID != "" {
		identities++
		if !isPortableClientOrderID(request.ClientOrderID) {
			return invalidRequest("client order id must be a positive decimal uint48")
		}
	}
	if request.VenueOrderID != "" {
		identities++
	}
	if identities != 1 {
		return invalidRequest("exact order lookup requires exactly one order identity")
	}
	if request.Instrument != nil {
		if err := request.Instrument.Validate(); err != nil {
			return err
		}
	}
	return nil
}

type ExactOrderLookupStatus string

const (
	ExactOrderFound       ExactOrderLookupStatus = "found"
	ExactOrderNotFound    ExactOrderLookupStatus = "not_found"
	ExactOrderUnconfirmed ExactOrderLookupStatus = "unconfirmed"
)

type ExactOrderLookupResult struct {
	Status   ExactOrderLookupStatus `json:"status"`
	Order    Order                  `json:"order,omitempty"`
	Evidence ExactOrderEvidence     `json:"evidence"`
}

func (result ExactOrderLookupResult) Validate() error {
	if result.Status != ExactOrderFound && result.Status != ExactOrderNotFound && result.Status != ExactOrderUnconfirmed {
		return invalidRequest("unknown exact order lookup status")
	}
	if err := result.Evidence.Validate(); err != nil {
		return err
	}
	if result.Status == ExactOrderFound || result.Status == ExactOrderNotFound {
		if !result.Evidence.Authoritative || !result.Evidence.Complete {
			return invalidRequest("exact found/not-found requires authoritative complete evidence")
		}
		if result.Evidence.Method == ExactOrderEvidenceByHistoryPage {
			return invalidRequest("history pagination is not exact order evidence")
		}
		if result.Evidence.ObservedClientOrderID == "" && result.Evidence.ObservedVenueOrderID == "" {
			return invalidRequest("exact found/not-found evidence has no checked order identity")
		}
	}
	if result.Status == ExactOrderFound {
		if result.Order.OrderID == "" && result.Order.ClientOrderID == "" {
			return invalidRequest("found exact order has no order identity")
		}
		if result.Evidence.ObservedClientOrderID != "" && result.Order.ClientOrderID != result.Evidence.ObservedClientOrderID {
			return invalidRequest("found exact order client identity does not match evidence")
		}
		if result.Evidence.ObservedVenueOrderID != "" && result.Order.OrderID != result.Evidence.ObservedVenueOrderID {
			return invalidRequest("found exact order venue identity does not match evidence")
		}
	}
	return nil
}

type ExactOrderEvidenceMethod string

const (
	ExactOrderEvidenceByClientOrderID ExactOrderEvidenceMethod = "client_order_id"
	ExactOrderEvidenceByVenueOrderID  ExactOrderEvidenceMethod = "venue_order_id"
	ExactOrderEvidenceByOpenOrders    ExactOrderEvidenceMethod = "open_orders"
	ExactOrderEvidenceByHistoryPage   ExactOrderEvidenceMethod = "history_page"
)

type ExactOrderScope struct {
	Venue      Venue       `json:"venue"`
	Product    Product     `json:"product"`
	Instrument *Instrument `json:"instrument,omitempty"`
}

func (scope ExactOrderScope) Validate() error {
	if scope.Venue == "" || scope.Product == "" {
		return invalidRequest("exact order evidence scope is incomplete")
	}
	if !knownVenue(scope.Venue) {
		return invalidRequest("exact order evidence venue is unknown")
	}
	if scope.Product != ProductSpot && scope.Product != ProductPerp {
		return invalidRequest("exact order evidence product is unknown")
	}
	if scope.Instrument != nil {
		if err := scope.Instrument.Validate(); err != nil {
			return err
		}
		if scope.Instrument.Identity.Venue != scope.Venue || scope.Instrument.Identity.Product != scope.Product {
			return invalidRequest("exact order evidence instrument scope mismatch")
		}
	}
	return nil
}

type ExactOrderEvidence struct {
	Scope                 ExactOrderScope          `json:"scope"`
	Method                ExactOrderEvidenceMethod `json:"method"`
	Authoritative         bool                     `json:"authoritative"`
	Complete              bool                     `json:"complete"`
	ObservedClientOrderID string                   `json:"observed_client_order_id,omitempty"`
	ObservedVenueOrderID  string                   `json:"observed_venue_order_id,omitempty"`
	NativeCursor          string                   `json:"native_cursor,omitempty"`
}

func (evidence ExactOrderEvidence) Validate() error {
	if err := evidence.Scope.Validate(); err != nil {
		return err
	}
	if evidence.ObservedClientOrderID != "" && !isPortableClientOrderID(evidence.ObservedClientOrderID) {
		return invalidRequest("exact order evidence client order id must be a positive decimal uint48")
	}
	switch evidence.Method {
	case ExactOrderEvidenceByClientOrderID:
		if evidence.ObservedClientOrderID == "" {
			return invalidRequest("client-order-id evidence requires a checked client order id")
		}
	case ExactOrderEvidenceByVenueOrderID:
		if evidence.ObservedVenueOrderID == "" {
			return invalidRequest("venue-order-id evidence requires a checked venue order id")
		}
	case ExactOrderEvidenceByOpenOrders, ExactOrderEvidenceByHistoryPage:
	default:
		return invalidRequest("exact order evidence method is unknown")
	}
	return nil
}
