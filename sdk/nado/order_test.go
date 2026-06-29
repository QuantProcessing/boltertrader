package nado

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"
)

func skipIfOrderTestEnvironmentIssue(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "insufficient") || strings.Contains(lower, "account health") {
		t.Skipf("skipping: account cannot support nado order integration test: %v", err)
	}
}

func TestPlaceOrder(t *testing.T) {
	requireFullEnv(t)
	privateKey, subaccount := GetEnv()
	client, err := NewClient().WithCredentials(privateKey, subaccount)
	if err != nil {
		t.Fatal(err)
	}

	input := ClientOrderInput{
		ProductId:  2,
		Price:      "80000",
		Amount:     "0.01",
		Side:       OrderSideBuy,
		OrderType:  OrderTypeLimit,
		ReduceOnly: false,
		PostOnly:   false,
	}

	order, err := client.PlaceOrder(context.Background(), input)
	if err != nil {
		skipIfOrderTestEnvironmentIssue(t, err)
		t.Fatalf("PlaceOrder request failed: %v", err)
	} else {
		fmt.Printf("PlaceOrder success: %+v\n", order)
	}
}

func TestPlaceMarketOrder(t *testing.T) {
	requireFullEnv(t)
	privateKey, subaccount := GetEnv()
	client, err := NewClient().WithCredentials(privateKey, subaccount)
	if err != nil {
		t.Fatal(err)
	}

	input := ClientOrderInput{
		ProductId:  2,
		Price:      "80000",
		Amount:     "0.01",
		Side:       OrderSideBuy,
		OrderType:  OrderTypeMarket,
		ReduceOnly: false,
		PostOnly:   false,
	}

	order, err := client.PlaceOrder(context.Background(), input)
	if err != nil {
		skipIfOrderTestEnvironmentIssue(t, err)
		t.Fatalf("PlaceOrder request failed: %v", err)
	} else {
		fmt.Printf("PlaceOrder success: %+v\n", order)
	}
}

func TestCancelOrders(t *testing.T) {
	requireFullEnv(t)
	privateKey, subaccount := GetEnv()
	client, err := NewClient().WithCredentials(privateKey, subaccount)
	if err != nil {
		t.Fatal(err)
	}

	// cancel order
	cancelInput := CancelOrdersInput{
		ProductIds: []int64{1},
		Digests:    []string{"0xa06904abd84ce65aedf6f8629ae43b55cd90949910e32b1f4f3e41de53637672"},
	}
	cancelResp, err := client.CancelOrders(context.Background(), cancelInput)
	if err != nil {
		t.Fatalf("CancelOrders request failed (as expected for test creds): %v", err)
	} else {
		fmt.Printf("CancelOrders success: %+v\n", cancelResp)
	}
}

func TestPlaceOrderAndCancelOrder(t *testing.T) {
	requireFullEnv(t)
	privateKey, subaccount := GetEnv()
	client, err := NewClient().WithCredentials(privateKey, subaccount)
	if err != nil {
		t.Fatal(err)
	}

	input := ClientOrderInput{
		ProductId:  2,
		Price:      "80000",
		Amount:     "0.01",
		Side:       OrderSideBuy,
		OrderType:  OrderTypeLimit,
		ReduceOnly: false,
		PostOnly:   false,
	}

	order, err := client.PlaceOrder(context.Background(), input)
	if err != nil {
		skipIfOrderTestEnvironmentIssue(t, err)
		t.Fatalf("PlaceOrder request failed: %v", err)
	}
	fmt.Printf("PlaceOrder success: %+v\n", order)

	time.Sleep(3 * time.Second)
	// cancel order
	cancelInput := CancelOrdersInput{
		ProductIds: []int64{2},
		Digests:    []string{order.Digest},
	}
	cancelResp, err := client.CancelOrders(context.Background(), cancelInput)
	if err != nil {
		t.Fatalf("CancelOrders request failed (as expected for test creds): %v", err)
	} else {
		fmt.Printf("CancelOrders success: %+v\n", cancelResp)
	}
}

func TestBuildAppendix(t *testing.T) {
	// 1. Default Limit Order
	// Version=1 (bit 0)
	// OrderType=0 (Default)
	// Expect: 1
	input1 := ClientOrderInput{OrderType: OrderTypeLimit}
	app1 := BuildAppendix(input1)
	if app1 != "1" {
		t.Errorf("Case 1 (Limit): Expected 1, got %s", app1)
	}

	// 2. Market Order
	// Version=1
	// OrderType=IOC (1) << 9 = 512
	// Expect: 1 | 512 = 513
	input2 := ClientOrderInput{OrderType: OrderTypeMarket}
	app2 := BuildAppendix(input2)
	if app2 != "513" {
		t.Errorf("Case 2 (Market): Expected 513, got %s", app2)
	}

	// 3. Post Only
	// Version=1
	// PostOnly=True -> OrderType=PostOnly (3) << 9 = 1536
	// Expect: 1 | 1536 = 1537
	input3 := ClientOrderInput{OrderType: OrderTypeLimit, PostOnly: true}
	app3 := BuildAppendix(input3)
	if app3 != "1537" {
		t.Errorf("Case 3 (PostOnly): Expected 1537, got %s", app3)
	}

	// 4. Isolated Margin
	// Version=1
	// Isolated=True (1) << 8 = 256
	// IsolatedMargin=10.0 -> 10,000,000 (x6) << 64
	// Expect: 1 | 256 | (10000000 << 64)
	input4 := ClientOrderInput{
		OrderType:      OrderTypeLimit,
		Isolated:       true,
		IsolatedMargin: 10.0,
	}
	app4 := BuildAppendix(input4)

	// manual calc
	expectedBig := new(big.Int).SetInt64(1)
	expectedBig.Or(expectedBig, big.NewInt(256))
	val := big.NewInt(10_000_000)
	val.Lsh(val, 64)
	expectedBig.Or(expectedBig, val)

	if app4 != expectedBig.String() {
		t.Errorf("Case 4 (Isolated): Expected %s, got %s", expectedBig.String(), app4)
	}
}
