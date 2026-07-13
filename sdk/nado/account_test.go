package nado

import (
	"context"
	"encoding/json"
	"testing"
)

func TestFeeRates(t *testing.T) {
	requireFullEnv(t)
	client := newNadoCredentialClient(t)
	feeRates, err := client.GetFeeRates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("fee_rates=%+v", feeRates)
}

func TestGetAccount(t *testing.T) {
	requireFullEnv(t)
	client := newNadoCredentialClient(t)
	account, err := client.GetAccount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(account)
	t.Logf("account=%s", string(data))
}
