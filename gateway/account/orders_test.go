package account

import (
	"errors"
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/shopspring/decimal"
)

func TestClassifyNeverReportsRejectedWithoutProof(t *testing.T) {
	request := exchange.PlaceOrderRequest{ClientOrderID: "42", Side: exchange.SideBuy, Type: exchange.OrderTypeMarket, Quantity: decimal.NewFromInt(1)}
	cases := []struct {
		name string
		ack  exchange.OrderAcknowledgement
		err  error
		want string
	}{
		{"transport after send", exchange.OrderAcknowledgement{}, exchange.NewError(exchange.KindTransport, exchange.ErrorDetails{HTTPStatus: 502}), OutcomeUnknown},
		{"ambiguous", exchange.OrderAcknowledgement{State: exchange.AckAmbiguous}, exchange.NewError(exchange.KindAmbiguousOutcome, exchange.ErrorDetails{}), OutcomeUnknown},
		{"malformed response", exchange.OrderAcknowledgement{}, exchange.NewError(exchange.KindMalformedResponse, exchange.ErrorDetails{}), OutcomeUnknown},
		{"deadline", exchange.OrderAcknowledgement{}, exchange.NewError(exchange.KindDeadlineExceeded, exchange.ErrorDetails{}), OutcomeUnknown},
		{"plain error", exchange.OrderAcknowledgement{}, errors.New("boom"), OutcomeUnknown},
		{"venue rejection", exchange.OrderAcknowledgement{State: exchange.AckRejected, VenueCode: "-2019", VenueMessage: "Margin is insufficient."}, exchange.NewError(exchange.KindVenueRejected, exchange.ErrorDetails{Code: "-2019"}), OutcomeRejected},
		{"not sent", exchange.OrderAcknowledgement{}, exchange.NewError(exchange.KindTransport, exchange.ErrorDetails{CommandNotSent: true}), OutcomeRejected},
		{"local validation", exchange.OrderAcknowledgement{}, exchange.NewError(exchange.KindInvalidRequest, exchange.ErrorDetails{}), OutcomeRejected},
		{"auth", exchange.OrderAcknowledgement{}, exchange.NewError(exchange.KindAuthentication, exchange.ErrorDetails{HTTPStatus: 401}), OutcomeRejected},
		{"filled", exchange.OrderAcknowledgement{State: exchange.AckImmediatelyFilled, OrderID: "9", FilledQuantity: decimal.NewFromInt(1)}, nil, OutcomeAccepted},
		{"pending", exchange.OrderAcknowledgement{State: exchange.AckAcceptedPending, OrderID: "9"}, nil, OutcomeAccepted},
		{"unknown ack state", exchange.OrderAcknowledgement{State: "weird"}, nil, OutcomeUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classify("binance:perp:BTC-USDT", request, tc.ack, tc.err)
			if got.Status != tc.want {
				t.Fatalf("status = %s, want %s (%+v)", got.Status, tc.want, got)
			}
		})
	}
}

func TestClassifyFilledCarriesFillFacts(t *testing.T) {
	request := exchange.PlaceOrderRequest{ClientOrderID: "42", Side: exchange.SideSell, Type: exchange.OrderTypeMarket, Quantity: decimal.RequireFromString("0.01")}
	ack := exchange.OrderAcknowledgement{State: exchange.AckImmediatelyFilled, OrderID: "77", FilledQuantity: decimal.RequireFromString("0.01"),
		AverageFillPrice: exchange.OptionalDecimal{Value: decimal.RequireFromString("65000.1"), Valid: true}}
	got := classify("binance:perp:BTC-USDT", request, ack, nil)
	if got.Order == nil || got.Order.Status != "filled" || got.Order.AvgPrice != "65000.1" || got.Order.Filled != "0.01" || got.Order.OrderID != "77" || got.Order.ClientOrderID != "42" {
		t.Fatalf("order = %+v", got.Order)
	}
}

func TestPlaceRequestValidation(t *testing.T) {
	valid := PlaceRequest{Side: "buy", Type: "market", Quantity: "1", ClientOrderID: "123"}
	bad := []PlaceRequest{
		{Side: "buy", Type: "market", Quantity: "1", ClientOrderID: "0"},
		{Side: "buy", Type: "market", Quantity: "1", ClientOrderID: "0123"},
		{Side: "buy", Type: "market", Quantity: "1", ClientOrderID: "281474976710656"},
		{Side: "buy", Type: "market", Quantity: "1", ClientOrderID: "abc"},
		{Side: "hold", Type: "market", Quantity: "1", ClientOrderID: "1"},
		{Side: "buy", Type: "market", Quantity: "-1", ClientOrderID: "1"},
		{Side: "buy", Type: "market", Quantity: "1", Price: "10", ClientOrderID: "1"},
		{Side: "buy", Type: "limit", Quantity: "1", ClientOrderID: "1"},
		{Side: "buy", Type: "limit", Quantity: "1", Price: "10", TimeInForce: "fok", ClientOrderID: "1"},
	}
	_ = valid
	for _, request := range bad {
		if _, err := request.native(testInstrument()); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("%+v accepted: %v", request, err)
		}
	}
}

func TestProjectPositionNormalizesSide(t *testing.T) {
	cases := []struct {
		position exchange.Position
		side     string
		size     string
	}{
		{exchange.Position{PositionSlot: exchange.PositionSlotNet, Quantity: decimal.RequireFromString("-0.5")}, "short", "0.5"},
		{exchange.Position{PositionSlot: exchange.PositionSlotNet, Side: exchange.SideSell, Quantity: decimal.RequireFromString("0.5")}, "short", "0.5"},
		{exchange.Position{PositionSlot: exchange.PositionSlotNet, Side: exchange.SideBuy, Quantity: decimal.RequireFromString("2")}, "long", "2"},
		{exchange.Position{PositionSlot: exchange.PositionSlotShort, Quantity: decimal.RequireFromString("3")}, "short", "3"},
	}
	for _, tc := range cases {
		got, ok := projectPosition("x", tc.position)
		if !ok || got.Side != tc.side || got.Size != tc.size {
			t.Errorf("%+v -> %+v", tc.position, got)
		}
	}
	if _, ok := projectPosition("x", exchange.Position{Quantity: decimal.Zero}); ok {
		t.Error("zero position must be dropped")
	}
}

func TestNormalizeStatus(t *testing.T) {
	for raw, want := range map[string]string{
		"NEW": "open", "live": "open", "PARTIALLY_FILLED": "partially_filled", "FILLED": "filled",
		"CANCELED": "canceled", "cancelled": "canceled", "marginCanceled": "canceled", "EXPIRED": "canceled",
		"REJECTED": "rejected", "accepted_pending": "pending",
	} {
		if got := NormalizeStatus(raw); got != want {
			t.Errorf("%s -> %s, want %s", raw, got, want)
		}
	}
}
