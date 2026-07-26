package factoryclient

import (
	"errors"
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
)

func TestDecodedInstrumentDoesNotFabricateUnknownAssets(t *testing.T) {
	instrument, err := decodedInstrument(exchange.VenueAster, exchange.ProductSpot, "BTC-USDT")
	if !errors.Is(err, exchange.ErrMetadataResolution) {
		t.Fatalf("decodedInstrument err = %v, want ErrMetadataResolution", err)
	}
	if instrument != (exchange.Instrument{}) {
		t.Fatalf("decodedInstrument instrument = %+v, want zero value on metadata miss", instrument)
	}
}
