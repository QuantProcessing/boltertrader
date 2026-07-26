package factoryclient

import (
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/shopspring/decimal"
)

func TestHyperliquidLimitPriceValidationUsesOfficialPrecisionWithoutRounding(t *testing.T) {
	tests := []struct {
		name          string
		side          exchange.Side
		price         string
		priceDecimals int
		valid         bool
	}{
		{name: "buy rejects excess significant figures", side: exchange.SideBuy, price: "101.23456", priceDecimals: 5},
		{name: "sell rejects excess significant figures", side: exchange.SideSell, price: "101.23456", priceDecimals: 5},
		{name: "buy rejects excess decimals", side: exchange.SideBuy, price: "0.001234567", priceDecimals: 6},
		{name: "sell rejects excess decimals", side: exchange.SideSell, price: "0.001234567", priceDecimals: 6},
		{name: "integer exception remains exact", side: exchange.SideBuy, price: "123456", priceDecimals: 3, valid: true},
		{name: "representable noninteger remains exact", side: exchange.SideSell, price: "101.23", priceDecimals: 5, valid: true},
		{name: "large noninteger is rejected", side: exchange.SideBuy, price: "123456.7", priceDecimals: 3},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requested := decimal.RequireFromString(test.price)
			got, err := hlNormalizeLimitPrice(
				test.side,
				requested,
				test.priceDecimals,
			)
			if !test.valid {
				if err == nil {
					t.Fatalf("invalid price silently became %s", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !got.Equal(requested) {
				t.Fatalf("validated price changed from %s to %s", requested, got)
			}
		})
	}
}
