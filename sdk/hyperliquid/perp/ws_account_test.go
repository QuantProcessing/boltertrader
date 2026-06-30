package perp

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/QuantProcessing/boltertrader/internal/testenv"
	"github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
)

func requireLiveWSCredentials(t *testing.T) {
	t.Helper()
	testenv.RequireLiveCredentials(t, "HYPERLIQUID_PRIVATE_KEY", "HYPERLIQUID_ACCOUNT_ADDR")
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
	err := wsClient.Connect()
	if err != nil {
		t.Fatal(err)
	}

	err = wsClient.SubscribeOrderUpdates(accountAddr, func(orderUpdates []hyperliquid.WsOrderUpdate) {
		fmt.Printf("%+v\n", orderUpdates)
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
	err := wsClient.Connect()
	if err != nil {
		t.Fatal(err)
	}

	err = wsClient.SubscribeWebData2(accountAddr, func(pos PerpPosition) {
		fmt.Printf("%+v\n", pos)
	})
	if err != nil {
		t.Fatal(err)
	}
}
