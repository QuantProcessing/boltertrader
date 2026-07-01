package sdk

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/internal/testenv"
)

func TestPublicWSCompanion_WSKey(t *testing.T) {
	key := wsKey(WSArg{InstType: "SPOT", Topic: "books", Symbol: "BTCUSDT", Channel: "ticker", InstID: "BTCUSDT"})
	if key != "SPOT|books|BTCUSDT|ticker|BTCUSDT" {
		t.Fatalf("unexpected ws key: %s", key)
	}
}

func TestPublicWSClient_Subscribe(t *testing.T) {
	testenv.RequireLiveRead(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	client := NewPublicWSClient()
	defer client.Close()
	arg := WSArg{InstType: "SPOT", Channel: "ticker", InstID: bitgetSpotSymbol}
	got := make(chan json.RawMessage, 1)

	if err := client.Subscribe(ctx, arg, func(payload json.RawMessage) {
		select {
		case got <- payload:
		default:
		}
	}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if client.handlers[wsKey(arg)] == nil {
		t.Fatal("expected handler to be registered")
	}

	select {
	case <-got:
	case <-ctx.Done():
		t.Fatalf("expected ticker payload: %v", ctx.Err())
	}
}

func TestPublicWSClient_Unsubscribe(t *testing.T) {
	testenv.RequireLiveRead(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	client := NewPublicWSClient()
	defer client.Close()
	arg := WSArg{InstType: "SPOT", Channel: "ticker", InstID: bitgetSpotSymbol}
	if err := client.Subscribe(ctx, arg, func(json.RawMessage) {}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if err := client.Unsubscribe(ctx, arg); err != nil {
		t.Fatalf("Unsubscribe: %v", err)
	}
	if client.handlers[wsKey(arg)] != nil {
		t.Fatal("expected handler to be removed")
	}
}
