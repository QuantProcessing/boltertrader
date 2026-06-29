package perp

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/QuantProcessing/exchanges/sdk/hyperliquid"
)

func TestSubscribeL2Book(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping realtime websocket test under -short")
	}
	baseClient := hyperliquid.NewWebsocketClient(context.Background())
	wsClient := NewWebsocketClient(baseClient)
	err := wsClient.Connect()
	if err != nil {
		fmt.Printf("Connect error %v", err)
		return
	}
	wsClient.SubscribeL2Book("BTC", func(data hyperliquid.WsL2Book) {
		fmt.Println(data)
	})

	timeout := time.NewTimer(10 * time.Second)
	<-timeout.C
}

func TestSubscribeBbo(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping realtime websocket test under -short")
	}
	baseClient := hyperliquid.NewWebsocketClient(context.Background())
	wsClient := NewWebsocketClient(baseClient)
	err := wsClient.Connect()
	if err != nil {
		fmt.Printf("Connect error %v", err)
		return
	}
	wsClient.SubscribeBbo("BTC", func(data hyperliquid.WsBbo) {
		fmt.Println(data)
	})

	timeout := time.NewTimer(10 * time.Second)
	<-timeout.C
}
