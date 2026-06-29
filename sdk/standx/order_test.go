package standx

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/QuantProcessing/exchanges/internal/testenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOrderIntegration(t *testing.T) {
	testenv.RequireFull(t, "STANDX_PRIVATE_KEY")
	privateKey := os.Getenv("STANDX_PRIVATE_KEY")

	client := NewClient()
	_, err := client.WithCredentials(privateKey)
	require.NoError(t, err)

	ctx := context.Background()
	err = client.Login(ctx)
	require.NoError(t, err)

	symbol := "BTC-USD"

	// 1. Change Leverage
	t.Run("ChangeLeverage", func(t *testing.T) {
		req := ChangeLeverageRequest{
			Symbol:   symbol,
			Leverage: 20,
		}
		resp, err := client.ChangeLeverage(ctx, req)
		require.NoError(t, err)
		assert.Equal(t, 0, resp.Code)
	})

	// 2. Change Margin Mode
	t.Run("ChangeMarginMode", func(t *testing.T) {
		req := ChangeMarginModeRequest{
			Symbol:     symbol,
			MarginMode: "cross",
		}
		resp, err := client.ChangeMarginMode(ctx, req)
		require.NoError(t, err)
		assert.Equal(t, 0, resp.Code)
	})

	// 3. Create Order
	t.Run("CreateOrder", func(t *testing.T) {
		price := "80000"
		qty := "0.01"

		req := CreateOrderRequest{
			Symbol:      symbol,
			Side:        SideSell,
			OrderType:   OrderTypeLimit,
			Qty:         qty,
			Price:       price,
			TimeInForce: TimeInForceGTC,
			ClientOrdID: fmt.Sprintf("test-%d", time.Now().UnixNano()),
		}

		resp, err := client.CreateOrder(ctx, req, nil)
		require.NoError(t, err)
		assert.Equal(t, 0, resp.Code)
		t.Logf("Order Response: %+v", resp)
	})

	// 4. Cancel Order (by ClOrdID)
	t.Run("CancelOrder", func(t *testing.T) {
		clID := fmt.Sprintf("cancel-%d", time.Now().UnixNano())

		// Create
		req := CreateOrderRequest{
			Symbol:      symbol,
			Side:        SideSell,
			OrderType:   OrderTypeLimit,
			Qty:         "0.001",
			Price:       "200000",
			TimeInForce: TimeInForceGTC,
			ClientOrdID: clID,
		}
		_, err := client.CreateOrder(ctx, req, nil)
		require.NoError(t, err)

		time.Sleep(200 * time.Millisecond)

		// Cancel
		cancelReq := CancelOrderRequest{
			ClOrdID: clID,
			Symbol:  symbol,
		}
		resp, err := client.CancelOrder(ctx, cancelReq)
		require.NoError(t, err)
		assert.Equal(t, 0, resp.Code)
	})

	// 5. Cancel Multiple Orders
	t.Run("CancelMultipleOrders", func(t *testing.T) {
		clID1 := fmt.Sprintf("bulk-%d-1", time.Now().UnixNano())
		clID2 := fmt.Sprintf("bulk-%d-2", time.Now().UnixNano())

		for _, id := range []string{clID1, clID2} {
			req := CreateOrderRequest{
				Symbol:      symbol,
				Side:        SideSell,
				OrderType:   OrderTypeLimit,
				Qty:         "0.001",
				Price:       "200000",
				TimeInForce: TimeInForceGTC,
				ClientOrdID: id,
			}
			_, err := client.CreateOrder(ctx, req, nil)
			require.NoError(t, err)
		}

		time.Sleep(200 * time.Millisecond)

		// Bulk Cancel
		cancelReq := CancelOrdersRequest{
			ClOrdIDs: []string{clID1, clID2},
			Symbol:   symbol,
		}
		resp, err := client.CancelMultipleOrders(ctx, cancelReq)
		require.NoError(t, err)
		assert.NotNil(t, resp)
	})
}
