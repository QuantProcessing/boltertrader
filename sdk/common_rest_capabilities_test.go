package sdk_test

import (
	"testing"

	binanceperp "github.com/QuantProcessing/boltertrader/sdk/binance/perp"
	binancespot "github.com/QuantProcessing/boltertrader/sdk/binance/spot"
	hyperliquidperp "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/perp"
	hyperliquidspot "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/spot"
	"github.com/QuantProcessing/boltertrader/sdk/lighter"
	"github.com/QuantProcessing/boltertrader/sdk/okx"
)

func TestCommonRESTSDKCapabilitySurface(t *testing.T) {
	t.Helper()

	_ = (*binancespot.Client).GetTrades
	_ = (*binancespot.Client).PlaceOrder
	_ = (*binancespot.Client).AllOrders
	_ = (*binancespot.Client).MyTrades

	_ = (*binanceperp.Client).GetAggTrades
	_ = (*binanceperp.Client).PlaceOrder
	_ = (*binanceperp.Client).AllOrders
	_ = (*binanceperp.Client).MyTrades
	_ = (*binanceperp.Client).GetFundingRate
	_ = (*binanceperp.Client).GetFundingRateHistory
	_ = (*binanceperp.Client).ChangeLeverage

	_ = (*okx.Client).GetTrades
	_ = (*okx.Client).PlaceOrder
	_ = (*okx.Client).GetOrderHistory
	_ = (*okx.Client).GetFills
	_ = (*okx.Client).GetFundingRate
	_ = (*okx.Client).GetFundingRateHistory
	_ = (*okx.Client).SetLeverage

	_ = (*lighter.Client).GetRecentTrades
	_ = (*lighter.Client).PlaceOrder
	_ = (*lighter.Client).GetInactiveOrders
	_ = (*lighter.Client).GetTrades
	_ = (*lighter.Client).GetFundingRate
	_ = (*lighter.Client).GetFundingHistory
	_ = (*lighter.Client).UpdateLeverage

	_ = (*hyperliquidspot.Client).RecentTrades
	_ = (*hyperliquidspot.Client).PlaceMarketOrder
	_ = (*hyperliquidspot.Client).PlaceOrder
	_ = (*hyperliquidspot.Client).HistoricalOrders
	_ = (*hyperliquidspot.Client).UserFills

	_ = (*hyperliquidperp.Client).RecentTrades
	_ = (*hyperliquidperp.Client).PlaceMarketOrder
	_ = (*hyperliquidperp.Client).PlaceOrder
	_ = (*hyperliquidperp.Client).HistoricalOrders
	_ = (*hyperliquidperp.Client).UserFills
	_ = (*hyperliquidperp.Client).GetFundingRate
	_ = (*hyperliquidperp.Client).GetFundingRateHistory
	_ = (*hyperliquidperp.Client).UpdateLeverage
}
