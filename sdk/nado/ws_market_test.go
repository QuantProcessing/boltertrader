package nado

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestSubscribeBookDepth(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping realtime websocket test under -short")
	}
	// Create a lifecycle context for the client
	ctx := context.Background()
	subscriptionClient := NewWsMarketClient(ctx)

	// Connect (internal 10s timeout)
	err := subscriptionClient.Connect()
	if err != nil {
		t.Fatal(err)
	}

	productID := int64(2)
	err = subscriptionClient.SubscribeOrderBook(productID, func(order *OrderBook) {
		fmt.Println(order)
	})
	if err != nil {
		t.Fatal(err)
	}

	timeout := time.NewTimer(10 * time.Second)

	<-timeout.C
}
