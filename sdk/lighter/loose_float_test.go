package lighter

import (
	"encoding/json"
	"testing"
)

func TestOrderBookDetailAcceptsMixedStatisticEncodings(t *testing.T) {
	payload := []byte(`{"code":200,
		"order_book_details":[{"symbol":"ETH","market_id":0,"market_type":"perp","open_interest":12.5,"last_trade_price":3000,"quote_multiplier":1}],
		"spot_order_book_details":[{"symbol":"ETH/USDC","market_id":2048,"market_type":"spot","open_interest":"0.000000","last_trade_price":3001.5,"quote_multiplier":null}]}`)
	var response OrderBookDetailsResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		t.Fatal(err)
	}
	if got := float64(response.OrderBookDetails[0].OpenInterest); got != 12.5 {
		t.Fatalf("perp open interest = %v", got)
	}
	if got := float64(response.SpotOrderBookDetails[0].OpenInterest); got != 0 {
		t.Fatalf("spot open interest = %v", got)
	}
	if response.SpotOrderBookDetails[0].QuoteMultiplier != nil {
		t.Fatal("null quote multiplier must decode as absent")
	}
}
