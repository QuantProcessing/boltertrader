package nado

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestOrderUpdate(t *testing.T) {
	requireFullEnv(t)
	privateKey, _ := GetEnv()
	// Create a lifecycle context for the client
	ctx := context.Background()
	subscriptionClient := NewWsAccountClient(ctx).WithCredentials(privateKey)

	// Connect (internal 10s timeout)
	err := subscriptionClient.Connect()
	if err != nil {
		fmt.Println(err)
		return
	}

	productID := int64(2)
	err = subscriptionClient.SubscribeOrders(&productID, func(order *OrderUpdate) {
		fmt.Println(order)
	})
	if err != nil {
		fmt.Println(err)
		return
	}

	timeout := time.NewTimer(1 * time.Minute)

	<-timeout.C
}
