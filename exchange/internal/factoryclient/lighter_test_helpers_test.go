package factoryclient

import (
	"io"
	"net/http"
	"strings"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/shopspring/decimal"
)

const lighterOpenAPILighterPrivateKey = "01010101010101010101010101010101010101010101010101010101010101010101010101010101"

type lighterOpenAPIRoundTripFunc func(*http.Request) (*http.Response, error)

func (function lighterOpenAPIRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func lighterOpenAPIJSONResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func lighterTestInstrument(product exchange.Product, symbol, nativeSymbol, nativeID string) exchange.Instrument {
	settle := ""
	if product == exchange.ProductPerp {
		settle = "USDC"
	}
	return exchange.Instrument{
		Identity: exchange.InstrumentIdentity{
			Venue:        exchange.VenueLighter,
			Product:      product,
			Symbol:       symbol,
			BaseAsset:    "ETH",
			QuoteAsset:   "USDC",
			SettleAsset:  settle,
			NativeSymbol: nativeSymbol,
			NativeID:     nativeID,
		},
		Rules: exchange.InstrumentRules{
			PriceIncrement:    decimal.RequireFromString("0.01"),
			QuantityIncrement: decimal.RequireFromString("0.1"),
			MinQuantity:       decimal.RequireFromString("0.1"),
			MinNotional:       exchange.OptionalDecimal{Value: decimal.NewFromInt(1), Valid: true},
		},
		Encoding: exchange.InstrumentEncoding{
			ContractMultiplier: exchange.OptionalDecimal{Value: decimal.NewFromInt(1), Valid: true},
			PriceScale:         exchange.OptionalInt{Value: 100, Valid: true},
			QuantityScale:      exchange.OptionalInt{Value: 10, Valid: true},
			QuoteScale:         exchange.OptionalInt{Value: 100, Valid: true},
		},
	}
}
