package factoryclient

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
)

func TestBinancePerpPositionsIgnoresUnsupportedZeroQuantityRows(t *testing.T) {
	client := newBinancePerpPositionsTestClient(`[
		{"symbol":"BTCUSDT","positionAmt":"0.01","entryPrice":"60000","markPrice":"61000","unRealizedProfit":"10","liquidationPrice":"10000","leverage":"5","isolatedMargin":"0"},
		{"symbol":"IDOL","positionAmt":"0","entryPrice":"0","markPrice":"0","unRealizedProfit":"0","liquidationPrice":"0","leverage":"20","isolatedMargin":"0"}
	]`)

	positions, err := client.Positions(context.Background(), exchange.PositionsRequest{})
	if err != nil {
		t.Fatalf("Positions: %v", err)
	}
	if len(positions) != 1 || positions[0].NativeSymbol != "BTCUSDT" {
		t.Fatalf("positions = %+v, want only BTCUSDT", positions)
	}
}

func TestBinancePerpPositionsRejectsUnsupportedNonzeroQuantityRows(t *testing.T) {
	client := newBinancePerpPositionsTestClient(`[
		{"symbol":"IDOL","positionAmt":"1","entryPrice":"1","markPrice":"1","unRealizedProfit":"0","liquidationPrice":"0","leverage":"20","isolatedMargin":"0"}
	]`)

	_, err := client.Positions(context.Background(), exchange.PositionsRequest{})
	if !errors.Is(err, exchange.ErrMalformedResponse) {
		t.Fatalf("Positions error = %v, want ErrMalformedResponse", err)
	}
}

func newBinancePerpPositionsTestClient(response string) *binancePerpClient {
	return NewBinanceUSDPerp("key", "secret", Settings{
		Endpoint: "https://openapi.invalid",
		HTTPClient: &http.Client{Transport: openAPIRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			return openAPIJSONResponse(response), nil
		})},
	}).(*binancePerpClient)
}
