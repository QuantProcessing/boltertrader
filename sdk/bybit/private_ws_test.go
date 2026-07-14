package sdk

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/internal/testenv"
)

func TestPrivateWSClient_Subscribe(t *testing.T) {
	client := newLivePrivateWSClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	err := client.Subscribe(ctx, "order", func(json.RawMessage) {})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if client.handlers["order"] == nil {
		t.Fatal("expected handler to be registered")
	}
}

func TestPrivateWSClient_Unsubscribe(t *testing.T) {
	client := newLivePrivateWSClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := client.Subscribe(ctx, "order", func(json.RawMessage) {}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if err := client.Unsubscribe(ctx, "order"); err != nil {
		t.Fatalf("Unsubscribe: %v", err)
	}
	if client.handlers["order"] != nil {
		t.Fatal("expected handler to be removed")
	}
}

func TestDecodeWalletMessage(t *testing.T) {
	msg, err := DecodeWalletMessage([]byte(`{"topic":"wallet","data":[{"accountType":"UNIFIED","coin":[{"coin":"USDT","walletBalance":"100","availableToWithdraw":"90"}]}]}`))
	if err != nil {
		t.Fatalf("DecodeWalletMessage: %v", err)
	}
	if msg.Topic != "wallet" || len(msg.Data) != 1 || msg.Data[0].AccountType != "UNIFIED" {
		t.Fatalf("unexpected wallet message: %+v", msg)
	}
	if len(msg.Data[0].Coins) != 1 || msg.Data[0].Coins[0].Coin != "USDT" {
		t.Fatalf("unexpected wallet coins: %+v", msg.Data[0].Coins)
	}
}

func TestDecodePrivateTradingMessagesPreservesRoutingFields(t *testing.T) {
	order, err := DecodeOrderMessage([]byte(`{"topic":"order","data":[{"category":"linear","symbol":"BTCUSDT","positionIdx":1}]}`))
	if err != nil {
		t.Fatalf("DecodeOrderMessage: %v", err)
	}
	if len(order.Data) != 1 || order.Data[0].Category != "linear" || order.Data[0].PositionIdx != 1 {
		t.Fatalf("decoded order routing fields=%+v, want category linear and positionIdx 1", order.Data)
	}

	execution, err := DecodeExecutionMessage([]byte(`{"topic":"execution","data":[{"category":"linear","execType":"Trade","symbol":"BTCUSDT"}]}`))
	if err != nil {
		t.Fatalf("DecodeExecutionMessage: %v", err)
	}
	if len(execution.Data) != 1 || execution.Data[0].Category != "linear" || execution.Data[0].ExecType != "Trade" {
		t.Fatalf("decoded execution routing fields=%+v, want category linear and execType Trade", execution.Data)
	}

	position, err := DecodePositionMessage([]byte(`{"topic":"position","data":[{"category":"linear","symbol":"BTCUSDT","positionIdx":2}]}`))
	if err != nil {
		t.Fatalf("DecodePositionMessage: %v", err)
	}
	if len(position.Data) != 1 || position.Data[0].Category != "linear" || position.Data[0].PositionIdx != 2 {
		t.Fatalf("decoded position routing fields=%+v, want category linear and positionIdx 2", position.Data)
	}
}

func newLivePrivateWSClient(t *testing.T) *PrivateWSClient {
	t.Helper()
	testenv.RequireLiveRead(t, "BYBIT_API_KEY", "BYBIT_SECRET_KEY")
	client := NewPrivateWSClient().WithCredentials(os.Getenv("BYBIT_API_KEY"), os.Getenv("BYBIT_SECRET_KEY"))
	t.Cleanup(func() {
		_ = client.Close()
	})
	return client
}
