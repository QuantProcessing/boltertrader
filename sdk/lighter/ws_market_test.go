package lighter

import (
	"context"
	"testing"
)

func TestWSMarketCompanion_ChannelFormat(t *testing.T) {
	client := newLiveWSClient(t)
	if err := client.SubscribeHeight(func([]byte) {}); err != nil {
		t.Fatalf("SubscribeHeight: %v", err)
	}
	if client.Subscriptions["height"] == nil {
		t.Fatal("expected height subscription")
	}
}

func TestWebsocketClient_SubscribeCandleUsesDocumentedChannel(t *testing.T) {
	client := NewWebsocketClient(context.Background())
	conn := &recordingConn{}
	client.Mu.Lock()
	client.conn = conn
	client.Mu.Unlock()

	if err := client.SubscribeCandle(0, "1m", func([]byte) {}); err != nil {
		t.Fatal(err)
	}
	if client.Subscriptions["candle/0/1m"] == nil {
		t.Fatal("documented candle subscription was not retained")
	}
	if err := client.UnsubscribeCandle(0, "1m"); err != nil {
		t.Fatal(err)
	}
	if client.Subscriptions["candle/0/1m"] != nil {
		t.Fatal("candle subscription remained after unsubscribe")
	}
}
