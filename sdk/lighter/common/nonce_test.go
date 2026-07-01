package common

import (
	"context"
	"os"
	"strconv"
	"testing"

	"github.com/QuantProcessing/boltertrader/internal/testenv"
)

func TestNonceManager_Fetch(t *testing.T) {
	testenv.RequireLiveRead(t, "LIGHTER_ACCOUNT_INDEX", "LIGHTER_KEY_INDEX")
	accountIndex, err := strconv.ParseInt(os.Getenv("LIGHTER_ACCOUNT_INDEX"), 10, 64)
	if err != nil {
		t.Fatalf("parse LIGHTER_ACCOUNT_INDEX: %v", err)
	}
	keyIndex, err := strconv.ParseUint(os.Getenv("LIGHTER_KEY_INDEX"), 10, 8)
	if err != nil {
		t.Fatalf("parse LIGHTER_KEY_INDEX: %v", err)
	}

	nm, err := NewNonceManager("https://mainnet.zklighter.elliot.ai", accountIndex, uint8(keyIndex))
	if err != nil {
		t.Fatal(err)
	}

	nonce, err := nm.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if nonce < 0 {
		t.Fatalf("unexpected nonce: %d", nonce)
	}
}
