package nado

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

func TestFeeRates(t *testing.T) {
	requireFullEnv(t)
	privateKey, subaccount := GetEnv()
	client, err := NewClient().WithCredentials(privateKey, subaccount)
	if err != nil {
		t.Fatal(err)
	}
	feeRates, err := client.GetFeeRates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("%+v\n", feeRates)
}

func TestGetAccount(t *testing.T) {
	requireFullEnv(t)
	privateKey, subaccount := GetEnv()
	client, err := NewClient().WithCredentials(privateKey, subaccount)
	if err != nil {
		t.Fatal(err)
	}
	account, err := client.GetAccount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(account)
	fmt.Printf("%+v\n", string(data))
}
