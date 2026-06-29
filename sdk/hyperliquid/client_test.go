package hyperliquid

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/QuantProcessing/exchanges/internal/testenv"
)

func requireLiveCredentials(t *testing.T) {
	t.Helper()
	testenv.RequireLiveCredentials(t, "HYPERLIQUID_PRIVATE_KEY", "HYPERLIQUID_ACCOUNT_ADDR")
}

func hyperliquidEnv() (string, string, string) {
	return os.Getenv("HYPERLIQUID_PRIVATE_KEY"), os.Getenv("HYPERLIQUID_VAULT"), os.Getenv("HYPERLIQUID_ACCOUNT_ADDR")
}
func TestGetUserFees(t *testing.T) {
	requireLiveCredentials(t)
	privateKey, vault, accountAddr := hyperliquidEnv()
	client := NewClient().WithCredentials(privateKey, &vault).WithAccount(accountAddr)
	fees, err := client.GetUserFees(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	jsonData, err := json.Marshal(fees)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println(string(jsonData))
}
