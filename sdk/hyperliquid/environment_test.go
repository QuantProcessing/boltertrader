package hyperliquid

import (
	"context"
	"strings"
	"testing"
)

type envSignAction struct {
	Type string `msgpack:"type"`
}

// Mainnet is the only endpoint pair left, so the constructor is the single
// place that can get it wrong.
func TestNewClientCarriesMainnetRESTEndpoint(t *testing.T) {
	client := NewClient()

	if client.BaseURL != MainnetAPIURL {
		t.Fatalf("BaseURL=%q, want %q", client.BaseURL, MainnetAPIURL)
	}
}

func TestNewWebsocketClientCarriesMainnetEndpoint(t *testing.T) {
	client := NewWebsocketClient(context.Background())

	if client.URL != MainnetWSURL {
		t.Fatalf("URL=%q, want %q", client.URL, MainnetWSURL)
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

// The client methods are thin delegations to the package-level signer, and the
// arguments they forward — vault and expiry — are hashed into the signature.
// These assert the delegation is faithful, which is what the environment
// comparison used to prove indirectly.
func TestClientSignL1ActionMatchesDirectSigning(t *testing.T) {
	action := envSignAction{Type: "noop"}
	nonce := int64(123456789)
	privateKey := strings.Repeat("01", 32)

	client := NewClient().WithCredentials(privateKey, nil)

	got, err := client.SignL1Action(action, nonce)
	if err != nil {
		t.Fatalf("SignL1Action: %v", err)
	}
	want, err := SignL1Action(client.PrivateKey, action, client.Vault, nonce, client.ExpiresAfter)
	if err != nil {
		t.Fatalf("direct SignL1Action: %v", err)
	}

	if got != want {
		t.Fatalf("client signature=%#v, want %#v", got, want)
	}
}

func TestWebsocketClientSignL1ActionMatchesDirectSigning(t *testing.T) {
	action := envSignAction{Type: "noop"}
	nonce := int64(123456789)
	privateKey := strings.Repeat("01", 32)

	client := NewWebsocketClient(context.Background()).WithCredentials(privateKey, nil)

	got, err := client.SignL1Action(action, nonce)
	if err != nil {
		t.Fatalf("SignL1Action: %v", err)
	}
	want, err := SignL1Action(client.PrivateKey, action, client.Vault, nonce, nil)
	if err != nil {
		t.Fatalf("direct SignL1Action: %v", err)
	}

	if got != want {
		t.Fatalf("websocket client signature=%#v, want %#v", got, want)
	}
}
