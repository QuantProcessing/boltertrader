package model

import (
	"testing"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/shopspring/decimal"
)

func TestInstrumentID_String(t *testing.T) {
	id := InstrumentID{Venue: "BINANCE", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	if got, want := id.String(), "BINANCE:BTC-USDT:PERP"; got != want {
		t.Fatalf("String()=%q, want %q", got, want)
	}
}

// TestDecimalExact guards the core reason we use shopspring/decimal: 0.1+0.2
// must equal 0.3 exactly, which float64 cannot do. This is the property the
// whole PnL/risk layer rests on.
func TestDecimalExact(t *testing.T) {
	a := decimal.RequireFromString("0.1")
	b := decimal.RequireFromString("0.2")
	if !a.Add(b).Equal(decimal.RequireFromString("0.3")) {
		t.Fatal("decimal addition is not exact")
	}
	// A price * size product retains full precision.
	price := decimal.RequireFromString("64251.37")
	size := decimal.RequireFromString("0.001")
	if !price.Mul(size).Equal(decimal.RequireFromString("64.25137")) {
		t.Fatal("decimal multiplication lost precision")
	}
}

// TestSignedPositionQuantity documents the signed-quantity convention the
// model relies on to unify the three venues' position encodings.
func TestSignedPositionQuantity(t *testing.T) {
	long := Position{Quantity: decimal.RequireFromString("1.5"), Side: enums.PosLong}
	short := Position{Quantity: decimal.RequireFromString("-2.0"), Side: enums.PosShort}
	if !long.Quantity.IsPositive() || long.Side != enums.PosLong {
		t.Error("long position should have positive quantity and PosLong side")
	}
	if !short.Quantity.IsNegative() || short.Side != enums.PosShort {
		t.Error("short position should have negative quantity and PosShort side")
	}
}
