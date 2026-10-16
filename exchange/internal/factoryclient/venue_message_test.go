package factoryclient

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
	binanceperp "github.com/QuantProcessing/boltertrader/sdk/binance/perp"
)

func TestBinanceRejectionCarriesVenueText(t *testing.T) {
	ack, err := binancePerpCommandErrorAck(
		"PlaceOrder",
		binancePerpAck(exchange.OrderOperationPlace, websocketTestInstrument(exchange.VenueBinance, exchange.ProductPerp, "BTC-USDT"), "", "42"),
		&binanceperp.APIError{Code: -2019, Message: "Margin is insufficient.", HTTPStatus: http.StatusBadRequest},
	)
	var normalized *exchange.Error
	if !errors.As(err, &normalized) || normalized.Kind() != exchange.KindVenueRejected {
		t.Fatalf("err = %v", err)
	}
	if got := normalized.Details().VenueMessage; got != "Margin is insufficient." {
		t.Fatalf("VenueMessage = %q", got)
	}
	if ack.State != exchange.AckRejected || ack.VenueCode != "-2019" || ack.VenueMessage != "Margin is insufficient." {
		t.Fatalf("ack = %+v", ack)
	}
}

func TestVenueTextNeverComesFromTransportOrServerErrors(t *testing.T) {
	for _, err := range []error{
		errors.New("dial tcp: secret host"),
		&binanceperp.APIError{Code: -1001, Message: "proxy page", HTTPStatus: http.StatusBadGateway},
	} {
		mapped := withVenueMessage(exchange.NewError(exchange.KindTransport, exchange.ErrorDetails{HTTPStatus: 502}), nil, err)
		var normalized *exchange.Error
		if errors.As(mapped, &normalized) && normalized.Details().VenueMessage != "" {
			t.Fatalf("transport error carried venue text: %+v", normalized.Details())
		}
	}
}

func TestVenueTextIsBoundedAndSingleLine(t *testing.T) {
	long := strings.Repeat("x", 500) + "\nline\ttwo"
	got := cleanVenueMessage(long)
	if len(got) > maxVenueMessage+len("…") || strings.ContainsAny(got, "\n\t") {
		t.Fatalf("cleaned = %q", got)
	}
	if cleanVenueMessage("  Margin   is\ninsufficient. ") != "Margin is insufficient." {
		t.Fatal("whitespace not normalized")
	}
}
