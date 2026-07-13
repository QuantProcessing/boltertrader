package nado

import (
	"context"
	"testing"
	"time"
)

func TestOrderUpdate(t *testing.T) {
	requireFullEnv(t)
	privateKey, subaccount := GetEnv()
	if subaccount == "" {
		subaccount = "default"
	}
	// Create a lifecycle context for the client
	ctx := context.Background()
	restClient, err := newNadoTestnetClient(t).WithCredentials(privateKey, subaccount)
	if err != nil {
		t.Fatal(err)
	}
	subscriptionClient, err := NewWsAccountClient(ctx, restClient)
	if err != nil {
		t.Fatal(err)
	}

	// Connect (internal 10s timeout)
	err = subscriptionClient.Connect()
	if err != nil {
		t.Fatal(err)
	}

	productID := int64(2)
	err = subscriptionClient.SubscribeOrders(&productID, func(order *OrderUpdate) {
		t.Logf("order update: %+v", order)
	})
	if err != nil {
		t.Fatal(err)
	}

	timeout := time.NewTimer(1 * time.Minute)

	<-timeout.C
}
