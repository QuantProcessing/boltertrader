package nado

import (
	"context"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/internal/testenv"
)

func TestSubscribeBookDepth(t *testing.T) {
	testenv.RequireLiveRead(t)

	// Create a lifecycle context for the client
	ctx := context.Background()
	profile, err := NewProfile(EnvironmentTestnet)
	if err != nil {
		t.Fatal(err)
	}
	subscriptionClient, err := NewWsMarketClient(ctx, profile)
	if err != nil {
		t.Fatal(err)
	}

	// Connect (internal 10s timeout)
	err = subscriptionClient.Connect()
	if err != nil {
		t.Fatal(err)
	}

	productID := int64(2)
	err = subscriptionClient.SubscribeOrderBook(productID, func(order *OrderBook) {
		t.Logf("order book: %+v", order)
	})
	if err != nil {
		t.Fatal(err)
	}

	timeout := time.NewTimer(10 * time.Second)

	<-timeout.C
}
