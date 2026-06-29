package spot

import (
	"context"
	"testing"
)

func TestClient_GetAccount(t *testing.T) {
	got, err := newLivePrivateClient(t).GetAccount(context.Background())
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if got.AccountType == "" {
		t.Fatalf("unexpected account response: %+v", got)
	}
}

func TestClient_StartUserDataStream(t *testing.T) {
	listenKey, err := requireBinanceSpotLiveWrite(t).StartUserDataStream(context.Background())
	if err != nil {
		t.Fatalf("StartUserDataStream: %v", err)
	}
	if listenKey == "" {
		t.Fatal("expected listen key")
	}
}

func TestClient_KeepAliveUserDataStream(t *testing.T) {
	client := requireBinanceSpotLiveWrite(t)
	listenKey, err := client.StartUserDataStream(context.Background())
	if err != nil {
		t.Fatalf("StartUserDataStream: %v", err)
	}
	t.Cleanup(func() {
		_ = client.CloseUserDataStream(context.Background(), listenKey)
	})
	if err := client.KeepAliveUserDataStream(context.Background(), listenKey); err != nil {
		t.Fatalf("KeepAliveUserDataStream: %v", err)
	}
}

func TestClient_CloseUserDataStream(t *testing.T) {
	client := requireBinanceSpotLiveWrite(t)
	listenKey, err := client.StartUserDataStream(context.Background())
	if err != nil {
		t.Fatalf("StartUserDataStream: %v", err)
	}
	if err := client.CloseUserDataStream(context.Background(), listenKey); err != nil {
		t.Fatalf("CloseUserDataStream: %v", err)
	}
}
