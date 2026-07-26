package exchange

import (
	"context"
	"errors"
	"testing"
)

func TestExactOrderLookupRequestRequiresExactlyOneIdentity(t *testing.T) {
	instrument := testInstrument(ProductSpot)
	valid := []ExactOrderLookupRequest{
		{ClientOrderID: "1001"},
		{VenueOrderID: "abc"},
		{ClientOrderID: "1001", Instrument: &instrument},
	}
	for _, request := range valid {
		if err := request.Validate(); err != nil {
			t.Fatalf("valid request rejected: %+v: %v", request, err)
		}
	}

	invalid := []ExactOrderLookupRequest{
		{},
		{ClientOrderID: "1001", VenueOrderID: "abc"},
		{ClientOrderID: "0"},
	}
	for _, request := range invalid {
		if err := request.Validate(); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("request %+v error = %v, want ErrInvalidRequest", request, err)
		}
	}
}

func TestExactOrderLookupResultStatusesAreFrozen(t *testing.T) {
	allowed := map[ExactOrderLookupStatus]bool{
		ExactOrderFound:       true,
		ExactOrderNotFound:    true,
		ExactOrderUnconfirmed: true,
	}
	for _, status := range []ExactOrderLookupStatus{ExactOrderFound, ExactOrderNotFound, ExactOrderUnconfirmed} {
		if !allowed[status] {
			t.Fatalf("status %q not in frozen set", status)
		}
	}
}

func TestExactOrderLookupResultRequiresScopedAuthoritativeEvidence(t *testing.T) {
	instrument := testInstrument(ProductSpot)
	valid := ExactOrderLookupResult{
		Status: ExactOrderFound,
		Order:  Order{NativeSymbol: instrument.Identity.NativeSymbol, ClientOrderID: "1001"},
		Evidence: ExactOrderEvidence{
			Scope:                 ExactOrderScope{Venue: VenueBinance, Product: ProductSpot, Instrument: &instrument},
			Method:                ExactOrderEvidenceByClientOrderID,
			Authoritative:         true,
			Complete:              true,
			ObservedClientOrderID: "1001",
		},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid exact lookup result rejected: %v", err)
	}
	invalid := valid
	invalid.Evidence.Complete = false
	if err := invalid.Validate(); err == nil {
		t.Fatal("found result accepted incomplete evidence")
	}
	invalid = valid
	invalid.Evidence.Method = ExactOrderEvidenceByHistoryPage
	if err := invalid.Validate(); err == nil {
		t.Fatal("found result accepted paginated history as exact evidence")
	}
	invalid = valid
	invalid.Evidence.ObservedClientOrderID = ""
	if err := invalid.Validate(); err == nil {
		t.Fatal("client-order-id lookup accepted evidence without checked client identity")
	}
	invalid = valid
	invalid.Evidence.ObservedClientOrderID = "1002"
	if err := invalid.Validate(); err == nil {
		t.Fatal("found result accepted order identity that disagrees with evidence")
	}
	venueFound := valid
	venueFound.Order = Order{OrderID: "venue-42"}
	venueFound.Evidence.Method = ExactOrderEvidenceByVenueOrderID
	venueFound.Evidence.ObservedClientOrderID = ""
	venueFound.Evidence.ObservedVenueOrderID = "venue-42"
	if err := venueFound.Validate(); err != nil {
		t.Fatalf("venue-order-id evidence rejected: %v", err)
	}
	invalid = venueFound
	invalid.Evidence.ObservedVenueOrderID = ""
	if err := invalid.Validate(); err == nil {
		t.Fatal("venue-order-id lookup accepted evidence without checked venue identity")
	}
	invalid = venueFound
	invalid.Order.OrderID = "venue-43"
	if err := invalid.Validate(); err == nil {
		t.Fatal("found venue order accepted identity that disagrees with evidence")
	}
	notFound := ExactOrderLookupResult{
		Status: ExactOrderNotFound,
		Evidence: ExactOrderEvidence{
			Scope:                 ExactOrderScope{Venue: VenueBinance, Product: ProductSpot},
			Method:                ExactOrderEvidenceByClientOrderID,
			Authoritative:         true,
			Complete:              true,
			ObservedClientOrderID: "1001",
		},
	}
	if err := notFound.Validate(); err != nil {
		t.Fatalf("exact not-found with checked identity rejected: %v", err)
	}
	invalid = notFound
	invalid.Evidence.ObservedClientOrderID = ""
	if err := invalid.Validate(); err == nil {
		t.Fatal("exact not-found accepted evidence without checked identity")
	}
	openOrdersNotFound := notFound
	openOrdersNotFound.Evidence.Method = ExactOrderEvidenceByOpenOrders
	openOrdersNotFound.Evidence.ObservedClientOrderID = ""
	if err := openOrdersNotFound.Validate(); err == nil {
		t.Fatal("open-orders not-found accepted evidence without checked identity")
	}
	openOrdersNotFound.Evidence.ObservedClientOrderID = "0"
	if err := openOrdersNotFound.Validate(); err == nil {
		t.Fatal("open-orders not-found accepted invalid checked identity")
	}
	unconfirmed := ExactOrderLookupResult{
		Status: ExactOrderUnconfirmed,
		Evidence: ExactOrderEvidence{
			Scope:  ExactOrderScope{Venue: VenueBinance, Product: ProductSpot},
			Method: ExactOrderEvidenceByOpenOrders,
		},
	}
	if err := unconfirmed.Validate(); err != nil {
		t.Fatalf("unconfirmed evidence rejected: %v", err)
	}
}

func TestExactOrderLookupInterfaceShape(t *testing.T) {
	var _ ExactOrderLookup = exactOrderLookupFunc(func(context.Context, ExactOrderLookupRequest) (ExactOrderLookupResult, error) {
		return ExactOrderLookupResult{Status: ExactOrderNotFound}, nil
	})
}

type exactOrderLookupFunc func(context.Context, ExactOrderLookupRequest) (ExactOrderLookupResult, error)

func (fn exactOrderLookupFunc) LookupExactOrder(ctx context.Context, request ExactOrderLookupRequest) (ExactOrderLookupResult, error) {
	return fn(ctx, request)
}
