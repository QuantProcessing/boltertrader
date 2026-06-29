package spot

import (
	"context"
	"os"
	"testing"
	"time"
)

func requireSpotRealtimeWS(t *testing.T) {
	t.Helper()
	if os.Getenv("BINANCE_REALTIME_WS") != "1" {
		t.Skip("set BINANCE_REALTIME_WS=1 to run real Binance spot websocket smoke test")
	}
}

func Test_SubscribeOrderBook_Debug(t *testing.T) {
	requireSpotRealtimeWS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	client := NewWsMarketClient(ctx)

	// 开启 Debug 模式
	client.Debug = true

	// 首先连接
	t.Log("Connecting to WebSocket...")
	if err := client.Connect(); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer client.Close()
	t.Log("Connected successfully")

	// 等待一小段时间确保连接稳定
	time.Sleep(500 * time.Millisecond)

	// 订阅
	t.Log("Subscribing to order book...")
	count := 0
	err := client.SubscribeIncrementOrderBook("btcusdt", "100ms", func(e *WsDepthEvent) error {
		count++
		if count <= 3 {
			t.Logf("Received depth update #%d: EventType=%s, Symbol=%s, FirstUpdateID=%d, FinalUpdateID=%d, Bids=%d, Asks=%d",
				count, e.EventType, e.Symbol, e.FirstUpdateID, e.FinalUpdateID, len(e.Bids), len(e.Asks))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	t.Log("Subscription successful")

	// 等待接收数据
	time.Sleep(5 * time.Second)

	if count == 0 {
		t.Error("No depth updates received")
	} else {
		t.Logf("Test completed successfully, received %d updates", count)
	}
}

func Test_SubscribeOrderBook(t *testing.T) {
	requireSpotRealtimeWS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	client := NewWsMarketClient(ctx)

	// 首先连接
	t.Log("Connecting to WebSocket...")
	if err := client.Connect(); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer client.Close()
	t.Log("Connected successfully")

	// 等待一小段时间确保连接稳定
	time.Sleep(500 * time.Millisecond)

	// 订阅
	t.Log("Subscribing to order book...")
	count := 0
	err := client.SubscribeIncrementOrderBook("btcusdt", "100ms", func(e *WsDepthEvent) error {
		count++
		if count <= 3 {
			t.Logf("Received depth update #%d: FirstUpdateID=%d, FinalUpdateID=%d, Bids=%d, Asks=%d",
				count, e.FirstUpdateID, e.FinalUpdateID, len(e.Bids), len(e.Asks))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	t.Log("Subscription successful")

	// 等待接收数据
	time.Sleep(5 * time.Second)

	if count == 0 {
		t.Error("No depth updates received")
	} else {
		t.Logf("Test completed successfully, received %d updates", count)
	}
}
