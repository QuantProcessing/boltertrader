package account

import (
	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/market"
	"github.com/shopspring/decimal"
)

// testInstrument is a valid Binance USDT perpetual with permissive rules.
func testInstrument() market.Instrument {
	return market.NewTestInstrument(exchange.Instrument{
		Identity: exchange.InstrumentIdentity{Venue: exchange.VenueBinance, Product: exchange.ProductPerp, Symbol: "BTC-USDT",
			BaseAsset: "BTC", QuoteAsset: "USDT", SettleAsset: "USDT", NativeSymbol: "BTCUSDT"},
		Rules: exchange.InstrumentRules{PriceIncrement: decimal.RequireFromString("0.1"), QuantityIncrement: decimal.RequireFromString("0.001"), MinQuantity: decimal.RequireFromString("0.001")},
	})
}
