package account

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/shopspring/decimal"
)

func TestClassifyIsUniformAcrossVenues(t *testing.T) {
	for _, venue := range []exchange.Venue{exchange.VenueBinance, exchange.VenueOKX, exchange.VenueBybit, exchange.VenueBitget, exchange.VenueGate, exchange.VenueHyperliquid, exchange.VenueLighter} {
		cases := []struct {
			err  error
			want Category
		}{
			{exchange.NewError(exchange.KindTransport, exchange.ErrorDetails{Venue: venue}), CategoryNetwork},
			{exchange.NewError(exchange.KindDeadlineExceeded, exchange.ErrorDetails{Venue: venue}), CategoryNetwork},
			{exchange.NewError(exchange.KindMalformedResponse, exchange.ErrorDetails{Venue: venue, HTTPStatus: 503}), CategoryNetwork},
			{exchange.NewError(exchange.KindAuthentication, exchange.ErrorDetails{Venue: venue}), CategoryCredentials},
			{exchange.NewError(exchange.KindPermission, exchange.ErrorDetails{Venue: venue}), CategoryCredentials},
			{exchange.NewError(exchange.KindVenueRejected, exchange.ErrorDetails{Venue: venue}), CategoryRejected},
			{exchange.NewError(exchange.KindRateLimit, exchange.ErrorDetails{Venue: venue}), CategoryRateLimited},
		}
		for _, tc := range cases {
			if got := Classify(tc.err).Category; got != tc.want {
				t.Errorf("%s %v -> %s, want %s", venue, tc.err, got, tc.want)
			}
		}
	}
	if got := Classify(fmt.Errorf("wrapped: %w", context.DeadlineExceeded)).Category; got != CategoryNetwork {
		t.Errorf("deadline -> %s", got)
	}
	if got := Classify(&net.OpError{Op: "dial", Err: errors.New("refused")}).Category; got != CategoryNetwork {
		t.Errorf("net error -> %s", got)
	}
}

func TestNetworkFailureIsNotReportedAsCredentials(t *testing.T) {
	failure := Classify(exchange.NewError(exchange.KindTransport, exchange.ErrorDetails{Venue: exchange.VenueOKX, Operation: "Balances"}))
	if failure.Category != CategoryNetwork || !failure.Retryable {
		t.Fatalf("failure = %+v", failure)
	}
	message := failure.Message()
	if !strings.Contains(message, "Cannot reach OKX") || strings.Contains(strings.ToLower(message), "key") {
		t.Fatalf("message = %q", message)
	}
}

func TestRejectionShowsVenueText(t *testing.T) {
	request := exchange.PlaceOrderRequest{ClientOrderID: "42", Side: exchange.SideBuy, Type: exchange.OrderTypeMarket, Quantity: decimal.RequireFromString("0.01")}
	ack := exchange.OrderAcknowledgement{State: exchange.AckRejected, VenueCode: "-2019", VenueMessage: "Margin is insufficient."}
	err := exchange.NewError(exchange.KindVenueRejected, exchange.ErrorDetails{Venue: exchange.VenueBinance, Code: "-2019", VenueMessage: "Margin is insufficient."})
	result := classify("binance:perp:BTC-USDT", request, ack, err)
	if result.Status != OutcomeRejected || result.Category != CategoryRejected || result.Message != "Margin is insufficient." || result.VenueCode != "-2019" {
		t.Fatalf("result = %+v", result)
	}
}

func TestNetworkFailureDuringPlaceStaysUnknown(t *testing.T) {
	request := exchange.PlaceOrderRequest{ClientOrderID: "42", Side: exchange.SideBuy, Type: exchange.OrderTypeMarket, Quantity: decimal.RequireFromString("0.01")}
	result := classify("binance:perp:BTC-USDT", request, exchange.OrderAcknowledgement{}, exchange.NewError(exchange.KindTransport, exchange.ErrorDetails{Venue: exchange.VenueBinance}))
	if result.Status != OutcomeUnknown || result.Category != CategoryNetwork {
		t.Fatalf("result = %+v", result)
	}
}
