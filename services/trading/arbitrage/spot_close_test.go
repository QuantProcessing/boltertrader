package arbitrage

import (
	"testing"
)

func TestSpotCashLegClosesWithoutReduceOnly(t *testing.T) {
	payload, err := EncodeIntentWithClose(
		LegLong, "binance:spot:BTC-USDT", ReduceSide(LegLong), "1", "market", "", "IOC", "spot",
		"account-perp", "binance:perp:BTC-USDT", false, true, "runtime-a",
	)
	if err != nil {
		t.Fatalf("EncodeIntentWithClose: %v", err)
	}
	intent, err := decodePlaceIntent(payload)
	if err != nil {
		t.Fatalf("decodePlaceIntent: %v", err)
	}
	if !intent.Closing || intent.ReduceOnly || intent.Side != "sell" {
		t.Fatalf("spot close intent = %+v", intent)
	}
}

func TestClassifyPairRecognizesNonReduceOnlySpotClose(t *testing.T) {
	legs := []LegView{
		{Leg: LegLong, State: "filled"},
		{Leg: LegShort, State: "filled"},
		{Leg: LegLong, Closing: true, ReduceOnly: false, State: "filled"},
		{Leg: LegShort, Closing: true, ReduceOnly: true, State: "filled"},
	}
	if got := ClassifyPair(legs); got != PairStateClosed {
		t.Fatalf("ClassifyPair = %s, want %s", got, PairStateClosed)
	}
}
