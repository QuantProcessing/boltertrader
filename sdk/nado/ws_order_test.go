package nado

import (
	"context"
	"testing"
	"time"
)

func connectWsAPI(t *testing.T, client *WsApiClient) {
	t.Helper()

	var err error
	for attempt := 0; attempt < 3; attempt++ {
		err = client.Connect()
		if err == nil {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("failed to connect ws api client: %v", err)
}

func TestWsPlaceOrder(t *testing.T) {
	requireWriteEnv(t)
	privateKey, subaccount := GetEnv()
	if subaccount == "" {
		subaccount = "default"
	}

	restClient, err := newNadoTestnetClient(t).WithCredentials(privateKey, subaccount)
	if err != nil {
		t.Fatal(err)
	}
	WsApiClient, err := NewWsApiClient(context.Background(), restClient)
	if err != nil {
		t.Fatal(err)
	}
	connectWsAPI(t, WsApiClient)

	order, err := WsApiClient.PlaceOrder(context.Background(), ClientOrderInput{
		ProductId:  2,
		Price:      "80000",
		Amount:     "0.01",
		Side:       OrderSideBuy,
		OrderType:  OrderTypeLimit,
		ReduceOnly: false,
		PostOnly:   false,
	})
	if err != nil {
		skipIfOrderTestEnvironmentIssue(t, err)
		t.Fatal(err)
	}
	t.Logf("order=%+v", order)

	time.Sleep(10 * time.Second)

	cancelResp, err := WsApiClient.CancelOrders(context.Background(), CancelOrdersInput{
		ProductIds: []int64{2},
		Digests:    []string{order.Digest},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("order canceled: %+v", cancelResp)
}

func TestWsCancelAndPlace(t *testing.T) {
	requireWriteEnv(t)
	privateKey, subaccount := GetEnv()
	if subaccount == "" {
		subaccount = "default"
	}

	restClient, err := newNadoTestnetClient(t).WithCredentials(privateKey, subaccount)
	if err != nil {
		t.Fatal(err)
	}
	WsApiClient, err := NewWsApiClient(context.Background(), restClient)
	if err != nil {
		t.Fatal(err)
	}
	connectWsAPI(t, WsApiClient)

	order, err := WsApiClient.PlaceOrder(context.Background(), ClientOrderInput{
		ProductId:  2,
		Price:      "80000",
		Amount:     "0.01",
		Side:       OrderSideBuy,
		OrderType:  OrderTypeLimit,
		ReduceOnly: false,
		PostOnly:   false,
	})
	if err != nil {
		skipIfOrderTestEnvironmentIssue(t, err)
		t.Fatal(err)
	}
	t.Logf("order=%+v", order)

	time.Sleep(10 * time.Second)

	cancelResp, err := WsApiClient.CancelAndPlace(context.Background(), CancelOrdersInput{
		ProductIds: []int64{2},
		Digests:    []string{order.Digest},
	}, ClientOrderInput{
		ProductId:  2,
		Price:      "80010",
		Amount:     "0.01",
		Side:       OrderSideBuy,
		OrderType:  OrderTypeLimit,
		ReduceOnly: false,
		PostOnly:   false,
	})
	if err != nil {
		skipIfOrderTestEnvironmentIssue(t, err)
		t.Fatal(err)
	}
	t.Logf("order canceled: %+v", cancelResp)
}
