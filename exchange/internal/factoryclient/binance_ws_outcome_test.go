package factoryclient

import (
	"errors"
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
	binanceperp "github.com/QuantProcessing/boltertrader/sdk/binance/perp"
	binancespot "github.com/QuantProcessing/boltertrader/sdk/binance/spot"
)

func TestBinanceSpotCommandAckMapsWSOutcomeUnknownToAmbiguous(t *testing.T) {
	ack, err := binanceSpotCommandAck("BTC/USDT", exchange.OrderOperationPlace, "PlaceOrder", "", "client-1", binancespot.ErrWSOutcomeUnknown)
	if !errors.Is(err, exchange.ErrAmbiguousOutcome) {
		t.Fatalf("error = %v, want ErrAmbiguousOutcome", err)
	}
	if ack.State != exchange.AckAmbiguous || ack.ClientOrderID != "client-1" || ack.VenueMessage != "transport outcome unknown" {
		t.Fatalf("ack = %+v", ack)
	}
}

func TestBinancePerpCommandErrorAckSeparatesWSUnknownFromPreSendTransport(t *testing.T) {
	base := binancePerpAck(exchange.OrderOperationPlace, "ETH/USDT:USDT", "", "client-1")

	ack, err := binancePerpCommandErrorAck("PlaceOrder", base, binanceperp.ErrWSOutcomeUnknown)
	if !errors.Is(err, exchange.ErrAmbiguousOutcome) || ack.State != exchange.AckAmbiguous {
		t.Fatalf("post-send ack=%+v err=%v, want ambiguous", ack, err)
	}

	ack, err = binancePerpCommandErrorAck("PlaceOrder", base, errors.New("websocket not connected"))
	if errors.Is(err, exchange.ErrAmbiguousOutcome) {
		t.Fatalf("pre-send error was ambiguous: ack=%+v err=%v", ack, err)
	}
	if ack.State != "" {
		t.Fatalf("pre-send ack=%+v, want no acknowledgement", ack)
	}
}

func TestBinanceCommandAckMapsMalformedMutationResponse(t *testing.T) {
	malformed := errors.New("failed to unmarshal response: invalid character")

	ack, err := binanceSpotCommandAck("BTC/USDT", exchange.OrderOperationPlace, "PlaceOrder", "", "client-1", malformed)
	if !errors.Is(err, exchange.ErrMalformedResponse) {
		t.Fatalf("spot malformed ack=%+v err=%v, want ErrMalformedResponse", ack, err)
	}
	if ack.State != "" {
		t.Fatalf("spot malformed ack=%+v, want no acknowledgement", ack)
	}

	ack, err = binancePerpCommandErrorAck("PlaceOrder", binancePerpAck(exchange.OrderOperationPlace, "ETH/USDT:USDT", "", "client-1"), malformed)
	if !errors.Is(err, exchange.ErrMalformedResponse) {
		t.Fatalf("perp malformed ack=%+v err=%v, want ErrMalformedResponse", ack, err)
	}
	if ack.State != "" {
		t.Fatalf("perp malformed ack=%+v, want no acknowledgement", ack)
	}
}
