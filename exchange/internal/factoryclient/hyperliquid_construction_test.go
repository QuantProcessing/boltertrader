package factoryclient

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

const (
	hyperliquidConstructionPrivateKey = "0000000000000000000000000000000000000000000000000000000000000001"
	hyperliquidConstructionOwner      = "0x1111111111111111111111111111111111111111"
)

func TestHyperliquidConstructionBindsExplicitOwnerToRESTAndPrivateWebSocket(t *testing.T) {
	seenUsers := make(map[string]string)
	transport := openAPIRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		requestType, _ := payload["type"].(string)
		user, _ := payload["user"].(string)
		seenUsers[requestType] = user
		switch requestType {
		case "spotClearinghouseState":
			return openAPIJSONResponse(`{"balances":[]}`), nil
		case "clearinghouseState":
			return openAPIJSONResponse(hyperliquidOpenAPIPerpStateJSON()), nil
		default:
			t.Fatalf("unexpected Hyperliquid account request: %#v", payload)
			return nil, nil
		}
	})
	settings := Settings{
		Endpoint:       "https://openapi.invalid",
		Environment:    "testnet",
		HTTPClient:     &http.Client{Transport: transport},
		AccountAddress: hyperliquidConstructionOwner,
	}

	spot := NewHyperliquidSpot(hyperliquidConstructionPrivateKey, settings).(*hyperliquidSpotClient)
	if _, err := spot.Balances(context.Background()); err != nil {
		t.Fatalf("spot Balances: %v", err)
	}
	assertHyperliquidSpotConstructionIdentity(t, spot, hyperliquidConstructionOwner)

	perp := NewHyperliquidPerp(hyperliquidConstructionPrivateKey, settings).(*hyperliquidPerpClient)
	if _, err := perp.PerpAccount(context.Background()); err != nil {
		t.Fatalf("perp PerpAccount: %v", err)
	}
	assertHyperliquidPerpConstructionIdentity(t, perp, hyperliquidConstructionOwner)

	if got := seenUsers["spotClearinghouseState"]; got != hyperliquidConstructionOwner {
		t.Fatalf("spot REST account user=%q, want explicit owner", got)
	}
	if got := seenUsers["clearinghouseState"]; got != hyperliquidConstructionOwner {
		t.Fatalf("perp REST account user=%q, want explicit owner", got)
	}
}

func TestHyperliquidConstructionDefaultsAccountIdentityToSigner(t *testing.T) {
	settings := Settings{Environment: "testnet"}
	spot := NewHyperliquidSpot(hyperliquidConstructionPrivateKey, settings).(*hyperliquidSpotClient)
	if spot.sdk.AccountAddr == "" || spot.sdk.AccountAddr == hyperliquidConstructionOwner {
		t.Fatalf("spot default account=%q, want derived signer", spot.sdk.AccountAddr)
	}
	assertHyperliquidSpotConstructionIdentity(t, spot, spot.sdk.AccountAddr)

	perp := NewHyperliquidPerp(hyperliquidConstructionPrivateKey, settings).(*hyperliquidPerpClient)
	if perp.sdk.AccountAddr == "" || perp.sdk.AccountAddr == hyperliquidConstructionOwner {
		t.Fatalf("perp default account=%q, want derived signer", perp.sdk.AccountAddr)
	}
	assertHyperliquidPerpConstructionIdentity(t, perp, perp.sdk.AccountAddr)
}

func assertHyperliquidSpotConstructionIdentity(t *testing.T, client *hyperliquidSpotClient, want string) {
	t.Helper()
	if client.sdk.AccountAddr != want {
		t.Fatalf("spot REST account=%q, want %q", client.sdk.AccountAddr, want)
	}
	socket := client.ws.(*spotWebSocket)
	backend := socket.private.backend.(*hyperliquidPrivateWSBackend)
	if backend.user != want || backend.base.AccountAddr != want {
		t.Fatalf("spot private WS identity user=%q base=%q, want %q", backend.user, backend.base.AccountAddr, want)
	}
	if backend.base.PrivateKey == nil {
		t.Fatal("spot private WS lost signer key while binding owner")
	}
}

func assertHyperliquidPerpConstructionIdentity(t *testing.T, client *hyperliquidPerpClient, want string) {
	t.Helper()
	if client.sdk.AccountAddr != want {
		t.Fatalf("perp REST account=%q, want %q", client.sdk.AccountAddr, want)
	}
	socket := client.ws.(*perpWebSocket)
	backend := socket.privateBackend.(*hyperliquidPrivateWSBackend)
	if backend.user != want || backend.base.AccountAddr != want {
		t.Fatalf("perp private WS identity user=%q base=%q, want %q", backend.user, backend.base.AccountAddr, want)
	}
	if backend.base.PrivateKey == nil {
		t.Fatal("perp private WS lost signer key while binding owner")
	}
}
