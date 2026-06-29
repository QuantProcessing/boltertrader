package standx

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMarketEndpoints(t *testing.T) {
	// No auth needed for market data
	client := NewClient()
	ctx := context.Background()
	symbol := "BTC-USD"

	t.Run("QuerySymbolInfo", func(t *testing.T) {
		info, err := client.QuerySymbolInfo(ctx, symbol)
		require.NoError(t, err)
		assert.NotEmpty(t, info)
		t.Logf("SymbolInfo: %+v", info[0])
	})

	t.Run("QuerySymbolMarket", func(t *testing.T) {
		market, err := client.QuerySymbolMarket(ctx, symbol)
		require.NoError(t, err)
		assert.Equal(t, symbol, market.Symbol)
		t.Logf("Market Stats: %+v", market)
	})

	t.Run("QueryMarketOverview", func(t *testing.T) {
		overview, err := client.QueryMarketOverview(ctx)
		require.NoError(t, err)
		assert.Greater(t, overview.Summary.SymbolCount, 0)
		assert.NotEmpty(t, overview.Symbols)
		assert.NotEmpty(t, overview.Symbols[0].FundingRate)
		assert.NotEmpty(t, overview.Symbols[0].MarkPrice)
	})

	t.Run("QueryDepthBook", func(t *testing.T) {
		book, err := client.QueryDepthBook(ctx, symbol, 10)
		require.NoError(t, err)
		assert.Equal(t, symbol, book.Symbol)
		assert.NotEmpty(t, book.Bids)
		assert.NotEmpty(t, book.Asks)
	})

	t.Run("QuerySymbolPrice", func(t *testing.T) {
		price, err := client.QuerySymbolPrice(ctx, symbol)
		require.NoError(t, err)
		assert.Equal(t, symbol, price.Symbol)
		assert.NotEmpty(t, price.LastPrice)
		t.Logf("Price: %s", price.LastPrice)
	})

	t.Run("QueryRecentTrades", func(t *testing.T) {
		trades, err := client.QueryRecentTrades(ctx, symbol, 10)
		require.NoError(t, err)
		assert.NotEmpty(t, trades)
		t.Logf("Recent Trades Count: %d", len(trades))
	})

	t.Run("QueryFundingRates", func(t *testing.T) {
		end := time.Now().UnixMilli()
		start := end - 3600*1000*24 // 24h
		rates, err := client.QueryFundingRates(ctx, symbol, start, end)
		require.NoError(t, err)
		assert.NotEmpty(t, rates)
		t.Logf("Funding Rates Count: %d", len(rates))
	})
}
