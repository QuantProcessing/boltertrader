package factoryclient

import (
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/shopspring/decimal"
)

func TestHyperliquidLimitPriceNormalizationUsesOfficialPrecisionAndConservativeDirection(t *testing.T) {
	tests := []struct {
		name          string
		side          exchange.Side
		price         string
		priceDecimals int
		want          string
	}{
		{name: "buy rounds down at five significant figures", side: exchange.SideBuy, price: "101.23456", priceDecimals: 5, want: "101.23"},
		{name: "sell rounds up at five significant figures", side: exchange.SideSell, price: "101.23456", priceDecimals: 5, want: "101.24"},
		{name: "buy respects decimal limit", side: exchange.SideBuy, price: "0.001234567", priceDecimals: 6, want: "0.001234"},
		{name: "sell respects decimal limit", side: exchange.SideSell, price: "0.001234567", priceDecimals: 6, want: "0.001235"},
		{name: "integer exception remains exact", side: exchange.SideBuy, price: "123456", priceDecimals: 3, want: "123456"},
		{name: "large noninteger buy becomes lower integer", side: exchange.SideBuy, price: "123456.7", priceDecimals: 3, want: "123456"},
		{name: "large noninteger sell becomes higher integer", side: exchange.SideSell, price: "123456.7", priceDecimals: 3, want: "123457"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := hlNormalizeLimitPrice(
				test.side,
				decimal.RequireFromString(test.price),
				test.priceDecimals,
			)
			if err != nil {
				t.Fatal(err)
			}
			if got.String() != test.want {
				t.Fatalf("normalized price = %s, want %s", got, test.want)
			}
			requested := decimal.RequireFromString(test.price)
			if test.side == exchange.SideBuy && got.GreaterThan(requested) {
				t.Fatalf("buy price %s exceeds requested %s", got, requested)
			}
			if test.side == exchange.SideSell && got.LessThan(requested) {
				t.Fatalf("sell price %s is below requested %s", got, requested)
			}
			isInteger := got.Equal(got.Truncate(0))
			if decimalPlaces(got) > test.priceDecimals || (!isInteger && hlSignificantDigits(got) > 5) {
				t.Fatalf("normalized price %s still violates Hyperliquid precision", got)
			}
		})
	}
}
