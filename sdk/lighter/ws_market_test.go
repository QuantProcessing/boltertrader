package lighter

import (
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
