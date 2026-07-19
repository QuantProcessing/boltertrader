package exchange

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

func TestOrderAcknowledgementStates(t *testing.T) {
	states := []OrderAckState{
		AckAcceptedPending,
		AckResting,
		AckPartiallyFilled,
		AckImmediatelyFilled,
		AckCanceled,
		AckRejected,
		AckAmbiguous,
	}
	seen := make(map[OrderAckState]struct{}, len(states))
	for _, state := range states {
		if state == "" {
			t.Fatal("ack state must not be empty")
		}
		if _, duplicate := seen[state]; duplicate {
			t.Fatalf("duplicate ack state %q", state)
		}
		seen[state] = struct{}{}
	}
}

func TestOrderAcknowledgementValidation(t *testing.T) {
	valid := []OrderAcknowledgement{
		{
			Venue: VenueLighter, Product: ProductSpot, Operation: OrderOperationPlace,
			State: AckAcceptedPending, Instrument: "BTC-USDC",
			ClientOrderID: "client-1", TransactionHash: "tx-1",
		},
		{
			Venue: VenueHyperliquid, Product: ProductPerp, Operation: OrderOperationPlace,
			State: AckResting, Instrument: "BTC", OrderID: "42",
		},
		{
			Venue: VenueHyperliquid, Product: ProductPerp, Operation: OrderOperationPlace,
			State: AckImmediatelyFilled, Instrument: "BTC", OrderID: "43",
		},
		{
			Venue: VenueBinance, Product: ProductSpot, Operation: OrderOperationPlace,
			State: AckPartiallyFilled, Instrument: "BTC-USDT", OrderID: "44",
			FilledQuantity: decimal.RequireFromString("0.001"),
		},
		{
			Venue: VenueBinance, Product: ProductSpot, Operation: OrderOperationCancel,
			State: AckCanceled, Instrument: "BTC-USDT", OrderID: "45",
		},
		{
			Venue: VenueOKX, Product: ProductSpot, Operation: OrderOperationPlace,
			State: AckRejected, Instrument: "BTC-USDT", VenueCode: "51008",
			VenueMessage: "insufficient balance",
		},
		{
			Venue: VenueBinance, Product: ProductSpot, Operation: OrderOperationCancel,
			State: AckAmbiguous, Instrument: "BTCUSDT", ClientOrderID: "client-2",
		},
	}
	for _, acknowledgement := range valid {
		if err := acknowledgement.Validate(); err != nil {
			t.Fatalf("%s acknowledgement rejected: %v", acknowledgement.State, err)
		}
	}

	invalid := OrderAcknowledgement{
		Venue: VenueHyperliquid, Product: ProductPerp, Operation: OrderOperationPlace,
		State: AckResting, Instrument: "BTC",
	}
	err := invalid.Validate()
	if !errors.Is(err, ErrMalformedResponse) {
		t.Fatalf("invalid acknowledgement error = %v, want ErrMalformedResponse", err)
	}

	marketResting := OrderAcknowledgement{
		Venue: VenueHyperliquid, Product: ProductPerp, Operation: OrderOperationPlace,
		State: AckResting, Instrument: "BTC", OrderID: "46", OrderType: OrderTypeMarket,
	}
	err = marketResting.Validate()
	if !errors.Is(err, ErrMalformedResponse) {
		t.Fatalf("market resting acknowledgement error = %v, want ErrMalformedResponse", err)
	}

	partialWithoutFill := OrderAcknowledgement{
		Venue: VenueBinance, Product: ProductSpot, Operation: OrderOperationPlace,
		State: AckPartiallyFilled, Instrument: "BTC-USDT", OrderID: "47",
	}
	err = partialWithoutFill.Validate()
	if !errors.Is(err, ErrMalformedResponse) {
		t.Fatalf("partial acknowledgement error = %v, want ErrMalformedResponse", err)
	}
}

func TestOrderAcknowledgementHasNoNativePayload(t *testing.T) {
	acknowledgement := OrderAcknowledgement{
		Venue: VenueOKX, Product: ProductSpot, Operation: OrderOperationPlace,
		State: AckRejected, Instrument: "BTC-USDT", VenueCode: "51008",
		VenueMessage: "insufficient balance",
	}
	data, err := json.Marshal(acknowledgement)
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(string(data))
	for _, forbidden := range []string{"sdk", "native", "raw", "signature", "secret", "token"} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("acknowledgement leaks %q: %s", forbidden, data)
		}
	}
}
