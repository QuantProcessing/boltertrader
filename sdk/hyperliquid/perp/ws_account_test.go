package perp

import (
	"context"
	"os"
	"testing"

	"github.com/QuantProcessing/boltertrader/internal/testenv"
	"github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
)

func requireLiveWSCredentials(t *testing.T) {
	t.Helper()
	testenv.RequireLiveRead(t, "HYPERLIQUID_PRIVATE_KEY")
}

func hyperliquidWSEnv() (string, string, string) {
	privateKey := os.Getenv("HYPERLIQUID_PRIVATE_KEY")
	vault := os.Getenv("HYPERLIQUID_VAULT")
	accountAddr := os.Getenv("HYPERLIQUID_ACCOUNT_ADDR")
	return privateKey, vault, accountAddr
}

func TestSubscribeOrderUpdates(t *testing.T) {
	requireLiveWSCredentials(t)
	privateKey, _, accountAddr := hyperliquidWSEnv()
	baseClient := hyperliquid.NewWebsocketClient(context.Background())
	wsClient := NewWebsocketClient(baseClient).WithCredentials(privateKey, accountAddr)
	defer wsClient.Close()
	account := wsClient.AccountAddr
	err := wsClient.Connect()
	if err != nil {
		t.Fatal(err)
	}

	err = wsClient.SubscribeOrderUpdates(account, func(orderUpdates []hyperliquid.WsOrderUpdate) {
		t.Logf("order updates: %+v", orderUpdates)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSubscribeWebData2(t *testing.T) {
	requireLiveWSCredentials(t)
	privateKey, _, accountAddr := hyperliquidWSEnv()
	baseClient := hyperliquid.NewWebsocketClient(context.Background())
	wsClient := NewWebsocketClient(baseClient).WithCredentials(privateKey, accountAddr)
	defer wsClient.Close()
	account := wsClient.AccountAddr
	err := wsClient.Connect()
	if err != nil {
		t.Fatal(err)
	}

	err = wsClient.SubscribeWebData2(account, func(pos PerpPosition) {
		t.Logf("webData2 position: %+v", pos)
	})
	if err != nil {
		t.Fatal(err)
	}
}
