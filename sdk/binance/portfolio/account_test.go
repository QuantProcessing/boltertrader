package portfolio

import (
	"context"
	"testing"

	"github.com/QuantProcessing/boltertrader/internal/testenv"
)

func TestClient_GetBalances(t *testing.T) {
	got, err := newLivePrivateClient(t).GetBalances(context.Background())
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Binance portfolio margin balance endpoint")
		t.Fatalf("GetBalances: %v", err)
	}
	if got == nil {
		t.Fatal("expected portfolio margin balances")
	}
}

func TestClient_GetAccount(t *testing.T) {
	got, err := newLivePrivateClient(t).GetAccount(context.Background())
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Binance portfolio margin account endpoint")
		t.Fatalf("GetAccount: %v", err)
	}
	if got == nil {
		t.Fatal("expected portfolio margin account")
	}
}
