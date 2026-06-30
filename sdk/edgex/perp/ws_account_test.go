package perp

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/internal/testenv"
)

func TestOrderUpdate(t *testing.T) {
	testenv.RequireSoak(t, "EDGEX_STARK_PRIVATE_KEY", "EDGEX_ACCOUNT_ID")
	starkPrivateKey, accountID := GetEnv()
	client := NewWsAccountClient(context.Background(), starkPrivateKey, accountID)
	client.Connect()

	client.SubscribeOrderUpdate(func(orders []Order) {
		fmt.Println(orders)
	})

	timeout := time.NewTimer(3 * time.Minute)

	<-timeout.C
	client.Close()
}

func TestPositionUpdate(t *testing.T) {
	testenv.RequireSoak(t, "EDGEX_STARK_PRIVATE_KEY", "EDGEX_ACCOUNT_ID")
	starkPrivateKey, accountID := GetEnv()
	client := NewWsAccountClient(context.Background(), starkPrivateKey, accountID)
	client.Connect()

	client.SubscribePositionUpdate(func(positions []PositionInfo) {
		fmt.Println(positions)
	})

	timeout := time.NewTimer(3 * time.Minute)
	<-timeout.C
	client.Close()

}
