package sdk

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/internal/testenv"
)

func TestPublicWSClient_Subscribe(t *testing.T) {
	testenv.RequireLiveRead(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	client := NewPublicWSClient("spot")
	defer client.Close()

	got := make(chan json.RawMessage, 1)
	err := client.Subscribe(ctx, "orderbook.50.BTCUSDT", func(payload json.RawMessage) {
		select {
		case got <- payload:
		default:
		}
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if client.handlers["orderbook.50.BTCUSDT"] == nil {
		t.Fatal("expected handler to be registered")
	}

	select {
	case <-got:
	case <-ctx.Done():
		t.Fatalf("expected order book payload: %v", ctx.Err())
	}
}

func TestPublicWSClient_Unsubscribe(t *testing.T) {
	testenv.RequireLiveRead(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	client := NewPublicWSClient("spot")
	defer client.Close()

	if err := client.Subscribe(ctx, "orderbook.50.BTCUSDT", func(json.RawMessage) {}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if err := client.Unsubscribe(ctx, "orderbook.50.BTCUSDT"); err != nil {
		t.Fatalf("Unsubscribe: %v", err)
	}
	if client.handlers["orderbook.50.BTCUSDT"] != nil {
		t.Fatal("expected handler to be removed")
	}
}
