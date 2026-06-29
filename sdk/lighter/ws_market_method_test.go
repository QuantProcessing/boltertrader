package lighter

import (
	"bytes"
	"os"
	"testing"
	"time"
)

func TestWebsocketClient_SubscribeOrderBook(t *testing.T) {
	client := newLiveWSClient(t)
	if err := client.SubscribeOrderBook(lighterMarketID(t), func([]byte) {}); err != nil {
		t.Fatalf("SubscribeOrderBook: %v", err)
	}
}

func TestWebsocketClient_SubscribeTicker(t *testing.T) {
	client := newLiveWSClient(t)
	if err := client.SubscribeTicker(lighterMarketID(t), func([]byte) {}); err != nil {
		t.Fatalf("SubscribeTicker: %v", err)
	}
}

func TestWebsocketClient_SubscribeMarketStats(t *testing.T) {
	client := newLiveWSClient(t)
	if err := client.SubscribeMarketStats(lighterMarketID(t), func([]byte) {}); err != nil {
		t.Fatalf("SubscribeMarketStats: %v", err)
	}
}

func TestWebsocketClient_SubscribeAllMarketStats(t *testing.T) {
	client := newLiveWSClient(t)
	if err := client.SubscribeAllMarketStats(func([]byte) {}); err != nil {
		t.Fatalf("SubscribeAllMarketStats: %v", err)
	}
}

func TestWebsocketClient_SubscribeAllMarketStatsReceivesFundingRate(t *testing.T) {
	if os.Getenv("LIGHTER_REALTIME_WS") != "1" {
		t.Skip("set LIGHTER_REALTIME_WS=1 to run real Lighter all-market funding smoke test")
	}
	client := newLiveWSClient(t)
	done := make(chan struct{}, 1)
	if err := client.SubscribeAllMarketStats(func(raw []byte) {
		if bytes.Contains(raw, []byte("funding_rate")) || bytes.Contains(raw, []byte("current_funding_rate")) {
			select {
			case done <- struct{}{}:
			default:
			}
		}
	}); err != nil {
		t.Fatalf("SubscribeAllMarketStats: %v", err)
	}
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("Timeout waiting for Lighter all-market stats funding payload")
	}
}

func TestWebsocketClient_SubscribeSpotMarketStats(t *testing.T) {
	client := newLiveWSClient(t)
	if err := client.SubscribeSpotMarketStats(lighterMarketID(t), func([]byte) {}); err != nil {
		t.Fatalf("SubscribeSpotMarketStats: %v", err)
	}
}

func TestWebsocketClient_SubscribeAllSpotMarketStats(t *testing.T) {
	client := newLiveWSClient(t)
	if err := client.SubscribeAllSpotMarketStats(func([]byte) {}); err != nil {
		t.Fatalf("SubscribeAllSpotMarketStats: %v", err)
	}
}

func TestWebsocketClient_SubscribeTrades(t *testing.T) {
	client := newLiveWSClient(t)
	if err := client.SubscribeTrades(lighterMarketID(t), func([]byte) {}); err != nil {
		t.Fatalf("SubscribeTrades: %v", err)
	}
}

func TestWebsocketClient_SubscribeHeight(t *testing.T) {
	client := newLiveWSClient(t)
	if err := client.SubscribeHeight(func([]byte) {}); err != nil {
		t.Fatalf("SubscribeHeight: %v", err)
	}
}

func TestWebsocketClient_UnsubscribeOrderBook(t *testing.T) {
	client := newLiveWSClient(t)
	_ = client.SubscribeOrderBook(lighterMarketID(t), func([]byte) {})
	if err := client.UnsubscribeOrderBook(lighterMarketID(t)); err != nil {
		t.Fatalf("UnsubscribeOrderBook: %v", err)
	}
}

func TestWebsocketClient_UnsubscribeTicker(t *testing.T) {
	client := newLiveWSClient(t)
	_ = client.SubscribeTicker(lighterMarketID(t), func([]byte) {})
	if err := client.UnsubscribeTicker(lighterMarketID(t)); err != nil {
		t.Fatalf("UnsubscribeTicker: %v", err)
	}
}

func TestWebsocketClient_UnsubscribeMarketStats(t *testing.T) {
	client := newLiveWSClient(t)
	_ = client.SubscribeMarketStats(lighterMarketID(t), func([]byte) {})
	if err := client.UnsubscribeMarketStats(lighterMarketID(t)); err != nil {
		t.Fatalf("UnsubscribeMarketStats: %v", err)
	}
}

func TestWebsocketClient_UnsubscribeSpotMarketStats(t *testing.T) {
	client := newLiveWSClient(t)
	_ = client.SubscribeSpotMarketStats(lighterMarketID(t), func([]byte) {})
	if err := client.UnsubscribeSpotMarketStats(lighterMarketID(t)); err != nil {
		t.Fatalf("UnsubscribeSpotMarketStats: %v", err)
	}
}

func TestWebsocketClient_UnsubscribeTrades(t *testing.T) {
	client := newLiveWSClient(t)
	_ = client.SubscribeTrades(lighterMarketID(t), func([]byte) {})
	if err := client.UnsubscribeTrades(lighterMarketID(t)); err != nil {
		t.Fatalf("UnsubscribeTrades: %v", err)
	}
}
