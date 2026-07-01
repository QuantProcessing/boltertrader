package grvt

import (
	"context"
	"testing"
	"time"
)

type websocketConnector interface {
	Connect() error
	Close()
}

func connectWithRetry(t *testing.T, wsClient websocketConnector) {
	t.Helper()

	var err error
	for attempt := 0; attempt < 3; attempt++ {
		err = wsClient.Connect()
		if err == nil {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("failed to connect to websocket: %v", err)
}

func TestSubscribeOrderbookDelta(t *testing.T) {
	requireFullEnv(t)
	apiKey, subaccount, privateKey := GetEnv()
	client := newLiveClient(t).WithCredentials(apiKey, subaccount, privateKey)
	wsClient := NewMarketWebsocketClient(context.Background(), client)
	connectWithRetry(t, wsClient)
	defer wsClient.Close()

	received := make(chan struct{}, 1)
	err := wsClient.SubscribeOrderbookDelta("BTC_USDT_Perp", OrderBookDeltaRate100, func(data WsFeeData[OrderBook]) error {
		if data.Feed.Instrument == "" {
			t.Error("expected order book instrument in websocket payload")
		}
		select {
		case received <- struct{}{}:
		default:
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	select {
	case <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for GRVT order book delta")
	}
}
