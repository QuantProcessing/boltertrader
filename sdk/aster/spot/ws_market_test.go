package spot

import (
	"context"
	"testing"
	"time"
)

func Test_SubscribeIncrementOrderBook(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping realtime websocket test under -short")
	}
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

func TestWsMarketClientSubscribeAndUnsubscribeIncrementOrderBookRegistersStream(t *testing.T) {
	client := NewWsMarketClient(context.Background())
	t.Cleanup(client.Close)

	const stream = "btcusdt@depth@100ms"
	if err := client.SubscribeIncrementOrderBook("btcusdt", "100ms", func(*WsDepthEvent) error { return nil }); err == nil {
		t.Fatalf("SubscribeIncrementOrderBook without a websocket connection returned nil error")
	}
	if _, ok := client.subs[stream]; !ok {
		t.Fatalf("SubscribeIncrementOrderBook did not register stream %q", stream)
	}

	if err := client.UnsubscribeIncrementOrderBook("btcusdt", "100ms"); err == nil {
		t.Fatalf("UnsubscribeIncrementOrderBook without a websocket connection returned nil error")
	}
	if _, ok := client.subs[stream]; ok {
		t.Fatalf("UnsubscribeIncrementOrderBook did not remove stream %q", stream)
	}
}
