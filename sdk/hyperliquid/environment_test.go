package hyperliquid

import (
	"context"
	"strings"
	"testing"
)

type envSignAction struct {
	Type string `msgpack:"type"`
}

func TestClientWithEnvironmentSelectsTestnetRESTEndpoint(t *testing.T) {
	client := NewClient().WithEnvironment(EnvironmentTestnet)

	if client.BaseURL != TestnetAPIURL {
		t.Fatalf("BaseURL=%q, want %q", client.BaseURL, TestnetAPIURL)
	}
	if client.IsMainnet() {
		t.Fatal("testnet client must not sign as mainnet")
	}
}

func TestWebsocketClientWithEnvironmentSelectsTestnetEndpoint(t *testing.T) {
	client := NewWebsocketClient(context.Background()).WithEnvironment(EnvironmentTestnet)

	if client.URL != TestnetWSURL {
		t.Fatalf("URL=%q, want %q", client.URL, TestnetWSURL)
	}
	if client.IsMainnet() {
		t.Fatal("testnet websocket client must not sign as mainnet")
	}
}

func TestWebsocketClientWithCredentialsDerivesAccountAddress(t *testing.T) {
	privateKey := strings.Repeat("01", 32)
	rest := NewClient().WithCredentials(privateKey, nil)
	ws := NewWebsocketClient(context.Background()).WithCredentials(privateKey, nil)

	if ws.AccountAddr == "" {
		t.Fatal("websocket client did not derive account address from private key")
	}
	if ws.AccountAddr != rest.AccountAddr {
		t.Fatalf("websocket account address=%q, want REST-derived %q", ws.AccountAddr, rest.AccountAddr)
	}
}

func TestClientsWithCredentialsAcceptHexPrefixedPrivateKey(t *testing.T) {
	privateKey := "0x" + strings.Repeat("01", 32)
	rest := NewClient().WithCredentials(privateKey, nil)
	ws := NewWebsocketClient(context.Background()).WithCredentials(privateKey, nil)

	if rest.PrivateKey == nil || rest.AccountAddr == "" {
		t.Fatalf("REST client did not parse 0x-prefixed private key; account=%q", rest.AccountAddr)
	}
	if ws.PrivateKey == nil || ws.AccountAddr == "" {
		t.Fatalf("websocket client did not parse 0x-prefixed private key; account=%q", ws.AccountAddr)
	}
	if ws.AccountAddr != rest.AccountAddr {
		t.Fatalf("websocket account address=%q, want REST-derived %q", ws.AccountAddr, rest.AccountAddr)
	}
}

func TestClientSignL1ActionUsesEnvironmentSource(t *testing.T) {
	action := envSignAction{Type: "noop"}
	nonce := int64(123456789)
	privateKey := strings.Repeat("01", 32)

	client := NewClient().
		WithEnvironment(EnvironmentTestnet).
		WithCredentials(privateKey, nil)

	got, err := client.SignL1Action(action, nonce)
	if err != nil {
		t.Fatalf("SignL1Action: %v", err)
	}
	wantTestnet, err := SignL1Action(client.PrivateKey, action, "", nonce, nil, false)
	if err != nil {
		t.Fatalf("direct testnet SignL1Action: %v", err)
	}
	wantMainnet, err := SignL1Action(client.PrivateKey, action, "", nonce, nil, true)
	if err != nil {
		t.Fatalf("direct mainnet SignL1Action: %v", err)
	}

	if got != wantTestnet {
		t.Fatalf("client signature=%#v, want testnet signature %#v", got, wantTestnet)
	}
	if got == wantMainnet {
		t.Fatal("testnet client signature matched mainnet source")
	}
}

func TestWebsocketClientSignL1ActionUsesEnvironmentSource(t *testing.T) {
	action := envSignAction{Type: "noop"}
	nonce := int64(123456789)
	privateKey := strings.Repeat("01", 32)

	client := NewWebsocketClient(context.Background()).
		WithEnvironment(EnvironmentTestnet).
		WithCredentials(privateKey, nil)

	got, err := client.SignL1Action(action, nonce)
	if err != nil {
		t.Fatalf("SignL1Action: %v", err)
	}
	wantTestnet, err := SignL1Action(client.PrivateKey, action, "", nonce, nil, false)
	if err != nil {
		t.Fatalf("direct testnet SignL1Action: %v", err)
	}
	wantMainnet, err := SignL1Action(client.PrivateKey, action, "", nonce, nil, true)
	if err != nil {
		t.Fatalf("direct mainnet SignL1Action: %v", err)
	}

	if got != wantTestnet {
		t.Fatalf("client signature=%#v, want testnet signature %#v", got, wantTestnet)
	}
	if got == wantMainnet {
		t.Fatal("testnet websocket client signature matched mainnet source")
	}
}
