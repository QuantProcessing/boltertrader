package factoryclient

import (
	"errors"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/sdk/okx"
)

func TestOKXCommandTransportAckDistinguishesWSOutcomeBoundaries(t *testing.T) {
	ack, err := okxCommandTransportAck(exchange.ProductSpot, exchange.OrderOperationPlace, "BTC-USDT", "", "cid", okx.ErrWSPreSend)
	if !errors.Is(err, exchange.ErrTransport) || ack.State != "" {
		t.Fatalf("pre-send ack=%+v err=%v, want transport without ack", ack, err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("pre-send error leaked unsafe detail: %v", err)
	}

	ack, err = okxCommandTransportAck(exchange.ProductSpot, exchange.OrderOperationCancel, "BTC-USDT", "1", "", okx.ErrWSOutcomeUnknown)
	if !errors.Is(err, exchange.ErrAmbiguousOutcome) || ack.State != exchange.AckAmbiguous || ack.OrderID != "1" {
		t.Fatalf("post-send ack=%+v err=%v, want ambiguous ack", ack, err)
	}

	for _, op := range []exchange.OrderOperation{exchange.OrderOperationPlace, exchange.OrderOperationCancel} {
		ack, err = okxCommandTransportAck(exchange.ProductSpot, op, "BTC-USDT", "1", "cid", okx.ErrWSMalformedResponse)
		if !errors.Is(err, exchange.ErrMalformedResponse) || ack.State != "" {
			t.Fatalf("malformed %s ack=%+v err=%v, want malformed without ack", op, ack, err)
		}
	}
}

func TestOKXCommandTransportAckMapsWSAPIErrorSafely(t *testing.T) {
	ack, err := okxCommandTransportAck(exchange.ProductPerp, exchange.OrderOperationPlace, "BTC-USDT-SWAP", "", "cid", &okx.APIError{
		Code:    "51000",
		Message: "secret venue rejection detail",
	})
	if !errors.Is(err, exchange.ErrVenueRejected) || ack.State != exchange.AckRejected || ack.VenueCode != "51000" {
		t.Fatalf("rejected ack=%+v err=%v", ack, err)
	}
	if strings.Contains(err.Error(), "secret") || strings.Contains(ack.VenueMessage, "secret") {
		t.Fatalf("unsafe venue detail leaked: ack=%+v err=%v", ack, err)
	}

	ack, err = okxCommandTransportAck(exchange.ProductPerp, exchange.OrderOperationCancel, "BTC-USDT-SWAP", "1", "", &okx.APIError{
		Code:    "50113",
		Message: "secret auth detail",
	})
	if !errors.Is(err, exchange.ErrAuthentication) || ack.State != "" {
		t.Fatalf("auth ack=%+v err=%v, want auth without ack", ack, err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("auth error leaked unsafe detail: %v", err)
	}
}
