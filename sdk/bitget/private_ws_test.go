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

	arg := WSArg{InstType: "UTA", Topic: "order"}
	if err := client.Subscribe(ctx, arg, func(json.RawMessage) {}); err != nil {
		skipIfBitgetPrivateWSUnavailable(t, err)
		t.Fatalf("Subscribe: %v", err)
	}
	if client.handlers[wsKey(arg)] == nil {
		t.Fatal("expected handler to be registered")
	}
}

func TestPrivateWSClient_Unsubscribe(t *testing.T) {
	client := newLivePrivateWSClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	arg := WSArg{InstType: "UTA", Topic: "order"}
	if err := client.Subscribe(ctx, arg, func(json.RawMessage) {}); err != nil {
		skipIfBitgetPrivateWSUnavailable(t, err)
		t.Fatalf("Subscribe: %v", err)
	}
	if err := client.Unsubscribe(ctx, arg); err != nil {
		t.Fatalf("Unsubscribe: %v", err)
	}
	if client.handlers[wsKey(arg)] != nil {
		t.Fatal("expected handler to be removed")
	}
}

func newLivePrivateWSClient(t *testing.T) *PrivateWSClient {
	t.Helper()
	testenv.RequireLiveRead(t, "BITGET_API_KEY", "BITGET_SECRET_KEY", "BITGET_PASSPHRASE")
	client := NewPrivateWSClient().WithCredentials(os.Getenv("BITGET_API_KEY"), os.Getenv("BITGET_SECRET_KEY"), os.Getenv("BITGET_PASSPHRASE"))
	t.Cleanup(func() {
		_ = client.Close()
	})
	return client
}
