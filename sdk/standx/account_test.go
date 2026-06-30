package standx_test

import (
	"context"
	"os"
	"strconv"
	"testing"

	"github.com/QuantProcessing/boltertrader/internal/testenv"
	"github.com/QuantProcessing/boltertrader/sdk/standx"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAccountIntegration(t *testing.T) {
	testenv.RequireFull(t, "STANDX_PRIVATE_KEY")
	privateKey := os.Getenv("STANDX_PRIVATE_KEY")

	// Setup Client
	client := standx.NewClient()

	_, err := client.WithCredentials(privateKey)
	require.NoError(t, err)

	ctx := context.Background()

	// Login
	err = client.Login(ctx)
	require.NoError(t, err, "Login failed")

	// Test QueryBalances
	t.Run("QueryBalances", func(t *testing.T) {
		balance, err := client.QueryBalances(ctx)
		require.NoError(t, err)
		t.Logf("Balances: %+v", balance)

		assert.NotNil(t, balance)
		t.Logf("Total Balance: %s", balance.Balance)
	})

	// Test QueryPositions
	t.Run("QueryPositions", func(t *testing.T) {
		positions, err := client.QueryPositions(ctx, "")
		require.NoError(t, err)
		t.Logf("Positions: %+v", positions)
	})

	// Test QueryUserOrders
	t.Run("QueryUserOrders", func(t *testing.T) {
		orders, err := client.QueryUserOrders(ctx, "")
		require.NoError(t, err)
		t.Logf("Orders: %+v", orders)
	})

	// Test QueryUserAllOpenOrders
	t.Run("QueryUserAllOpenOrders", func(t *testing.T) {
		orders, err := client.QueryUserAllOpenOrders(ctx, "")
		require.NoError(t, err)
		t.Logf("Open Orders: %+v", orders)
	})

	// Test QueryUserTrades
	t.Run("QueryUserTrades", func(t *testing.T) {
		trades, err := client.QueryUserTrades(ctx, "", 0, 10)
		require.NoError(t, err)
		t.Logf("Trades: %+v", trades)
		if len(trades) > 0 {
			sym := trades[0].Symbol
			tradesBySym, err := client.QueryUserTrades(ctx, sym, 0, 10)
			require.NoError(t, err)
			t.Logf("Trades for %s: %+v", sym, tradesBySym)
		}
	})

	// Collect total fee for BTC-USD
	t.Run("TotalFee_BTC_USD", func(t *testing.T) {
		var totalFee float64
		var lastID int64
		limit := 50

		pageCount := 0
		for {
			pageCount++
			t.Logf("Fetching page %d (lastID=%d)...", pageCount, lastID)
			trades, err := client.QueryUserTrades(ctx, "BTC-USD", lastID, limit)
			require.NoError(t, err)

			if len(trades) == 0 {
				break
			}

			for _, tr := range trades {
				if tr.FeeQty != "" {
					f, err := strconv.ParseFloat(tr.FeeQty, 64)
					if err == nil {
						totalFee += f
					}
				}
			}

			if len(trades) < limit {
				break
			}
			lastID = int64(trades[len(trades)-1].ID)
		}

		t.Logf("Total Fee Qty for BTC-USD: %f", totalFee)
	})
}
