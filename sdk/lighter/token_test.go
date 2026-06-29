package lighter

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"
)

func TestClient_GetTokens(t *testing.T) {
	got, err := newLivePrivateClient(t).GetTokens(context.Background())
	if err != nil {
		t.Fatalf("GetTokens: %v", err)
	}
	if got == nil {
		t.Fatal("expected tokens slice")
	}
}

func TestClient_CreateToken(t *testing.T) {
	client := requireLighterLiveWrite(t)
	name := lighterEnvOrDefault("LIGHTER_TEST_TOKEN_NAME", "sdk-live-test")
	scope := lighterEnvOrDefault("LIGHTER_TEST_TOKEN_SCOPE", "read")
	expiry := lighterInt64Env(t, "LIGHTER_TEST_TOKEN_EXPIRY", time.Now().Add(24*time.Hour).Unix())

	token, err := client.CreateToken(context.Background(), name, scope, expiry)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	if token == "" {
		t.Fatal("expected token")
	}
}

func TestClient_RevokeToken(t *testing.T) {
	client := requireLighterLiveWrite(t, "LIGHTER_TEST_TOKEN_ID")
	tokenID, err := strconv.ParseInt(os.Getenv("LIGHTER_TEST_TOKEN_ID"), 10, 64)
	if err != nil {
		t.Fatalf("parse LIGHTER_TEST_TOKEN_ID: %v", err)
	}
	if err := client.RevokeToken(context.Background(), tokenID); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}
}
