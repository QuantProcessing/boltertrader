package sdk

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/QuantProcessing/exchanges/internal/testenv"
)

func TestTradeWSClient_PlaceOrder(t *testing.T) {
	client := newLiveTradeWSClient(t, "BYBIT_TEST_ORDER_QTY", "BYBIT_TEST_ORDER_PRICE")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	err := client.PlaceOrder(ctx, PlaceOrderRequest{
		Category:    "linear",
		Symbol:      bybitEnvOrDefault("BYBIT_TEST_SYMBOL", bybitLinearSymbol),
		Side:        bybitEnvOrDefault("BYBIT_TEST_ORDER_SIDE", "Buy"),
		OrderType:   "Limit",
		Qty:         os.Getenv("BYBIT_TEST_ORDER_QTY"),
		Price:       os.Getenv("BYBIT_TEST_ORDER_PRICE"),
		TimeInForce: "GTC",
		OrderLinkID: bybitEnvOrDefault("BYBIT_TEST_ORDER_LINK_ID", ""),
	})
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
}

func TestTradeWSClient_CancelOrder(t *testing.T) {
	client := newLiveTradeWSClient(t, "BYBIT_TEST_ORDER_ID")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	err := client.CancelOrder(ctx, CancelOrderRequest{
		Category: "linear",
		Symbol:   bybitEnvOrDefault("BYBIT_TEST_SYMBOL", bybitLinearSymbol),
		OrderID:  os.Getenv("BYBIT_TEST_ORDER_ID"),
	})
	if err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}
}

func TestTradeWSClient_AmendOrder(t *testing.T) {
	client := newLiveTradeWSClient(t, "BYBIT_TEST_ORDER_ID", "BYBIT_TEST_ORDER_QTY", "BYBIT_TEST_ORDER_PRICE")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	err := client.AmendOrder(ctx, AmendOrderRequest{
		Category: "linear",
		Symbol:   bybitEnvOrDefault("BYBIT_TEST_SYMBOL", bybitLinearSymbol),
		OrderID:  os.Getenv("BYBIT_TEST_ORDER_ID"),
		Qty:      os.Getenv("BYBIT_TEST_ORDER_QTY"),
		Price:    os.Getenv("BYBIT_TEST_ORDER_PRICE"),
	})
	if err != nil {
		t.Fatalf("AmendOrder: %v", err)
	}
}

func TestTradeWSClient_BatchPlaceOrders(t *testing.T) {
	client := newLiveTradeWSClient(t, "BYBIT_TEST_ORDER_QTY", "BYBIT_TEST_ORDER_PRICE")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	err := client.BatchPlaceOrders(ctx, BatchPlaceOrdersRequest{
		Category: "linear",
		Request: []BatchPlaceOrderItem{{
			Symbol:      bybitEnvOrDefault("BYBIT_TEST_SYMBOL", bybitLinearSymbol),
			Side:        bybitEnvOrDefault("BYBIT_TEST_ORDER_SIDE", "Buy"),
			OrderType:   "Limit",
			Qty:         os.Getenv("BYBIT_TEST_ORDER_QTY"),
			Price:       os.Getenv("BYBIT_TEST_ORDER_PRICE"),
			TimeInForce: "GTC",
			OrderLinkID: bybitEnvOrDefault("BYBIT_TEST_ORDER_LINK_ID", ""),
		}},
	})
	if err != nil {
		t.Fatalf("BatchPlaceOrders: %v", err)
	}
}

func TestTradeWSClient_BatchAmendOrders(t *testing.T) {
	client := newLiveTradeWSClient(t, "BYBIT_TEST_ORDER_ID", "BYBIT_TEST_ORDER_QTY", "BYBIT_TEST_ORDER_PRICE")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	err := client.BatchAmendOrders(ctx, BatchAmendOrdersRequest{
		Category: "linear",
		Request: []BatchAmendOrderItem{{
			Symbol:  bybitEnvOrDefault("BYBIT_TEST_SYMBOL", bybitLinearSymbol),
			OrderID: os.Getenv("BYBIT_TEST_ORDER_ID"),
			Qty:     os.Getenv("BYBIT_TEST_ORDER_QTY"),
			Price:   os.Getenv("BYBIT_TEST_ORDER_PRICE"),
		}},
	})
	if err != nil {
		t.Fatalf("BatchAmendOrders: %v", err)
	}
}

func TestTradeWSClient_BatchCancelOrders(t *testing.T) {
	client := newLiveTradeWSClient(t, "BYBIT_TEST_ORDER_ID")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	err := client.BatchCancelOrders(ctx, BatchCancelOrdersRequest{
		Category: "linear",
		Request: []BatchCancelOrderItem{{
			Symbol:  bybitEnvOrDefault("BYBIT_TEST_SYMBOL", bybitLinearSymbol),
			OrderID: os.Getenv("BYBIT_TEST_ORDER_ID"),
		}},
	})
	if err != nil {
		t.Fatalf("BatchCancelOrders: %v", err)
	}
}

func newLiveTradeWSClient(t *testing.T, vars ...string) *TradeWSClient {
	t.Helper()
	required := append([]string{"BYBIT_API_KEY", "BYBIT_SECRET_KEY"}, vars...)
	testenv.RequireLiveWrite(t, bybitLiveWriteFlag, required...)
	client := NewTradeWSClient().WithCredentials(os.Getenv("BYBIT_API_KEY"), os.Getenv("BYBIT_SECRET_KEY"))
	t.Cleanup(func() {
		_ = client.Close()
	})
	return client
}
