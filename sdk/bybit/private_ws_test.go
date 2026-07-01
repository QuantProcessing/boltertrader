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

func newLivePrivateWSClient(t *testing.T) *PrivateWSClient {
	t.Helper()
	testenv.RequireLiveRead(t, "BYBIT_API_KEY", "BYBIT_SECRET_KEY")
	client := NewPrivateWSClient().WithCredentials(os.Getenv("BYBIT_API_KEY"), os.Getenv("BYBIT_SECRET_KEY"))
	t.Cleanup(func() {
		_ = client.Close()
	})
	return client
}
