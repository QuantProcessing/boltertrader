package account

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
)

func TestResolveIdentityUsesExplicitAccountIDAndEffectiveAddress(t *testing.T) {
	id, err := ResolveIdentity(Source{
		ExplicitAccountID: "hl-test",
		AccountAddress:    " 0xABCDEF ",
		VaultAddress:      "0xvault",
		SignerAddress:     "0xsigner",
	})
	if err != nil {
		t.Fatalf("ResolveIdentity: %v", err)
	}
	if id.AccountID != "hl-test" {
		t.Fatalf("account id=%q, want explicit id", id.AccountID)
	}
	if id.QueryAddress != "0xABCDEF" {
		t.Fatalf("query address=%q, want trimmed account address", id.QueryAddress)
	}
}

func TestResolveIdentityUsesDefaultAccountIDForAccountAddress(t *testing.T) {
	id, err := ResolveIdentity(Source{AccountAddress: " 0xABCDEF "})
	if err != nil {
		t.Fatalf("ResolveIdentity: %v", err)
	}
	if id.AccountID != DefaultAccountID {
		t.Fatalf("account id=%q, want default logical id", id.AccountID)
	}
	if id.QueryAddress != "0xABCDEF" {
		t.Fatalf("query address=%q, want trimmed original address", id.QueryAddress)
	}
}

func TestResolveIdentityPrefersVaultOverSigner(t *testing.T) {
	id, err := ResolveIdentity(Source{
		VaultAddress:  "0xVAULT",
		SignerAddress: "0xSIGNER",
	})
	if err != nil {
		t.Fatalf("ResolveIdentity: %v", err)
	}
	if id.AccountID != DefaultAccountID || id.QueryAddress != "0xVAULT" {
		t.Fatalf("identity=%+v, want vault identity", id)
	}
}

func TestResolveIdentityFallsBackToSigner(t *testing.T) {
	id, err := ResolveIdentity(Source{SignerAddress: "0xSIGNER"})
	if err != nil {
		t.Fatalf("ResolveIdentity: %v", err)
	}
	if id.AccountID != DefaultAccountID || id.QueryAddress != "0xSIGNER" {
		t.Fatalf("identity=%+v, want signer identity", id)
	}
}

func TestResolveIdentityRequiresAddressWhenNoExplicitID(t *testing.T) {
	_, err := ResolveIdentity(Source{})
	if !errors.Is(err, ErrIdentityRequired) {
		t.Fatalf("err=%v, want ErrIdentityRequired", err)
	}
}

func TestResolveAPIAccountAddressRejectsAgentRoleWithoutHexOwner(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"role":"agent","data":{"user":"non-hex-owner"}}`))
	}))
	defer srv.Close()

	client := sdk.NewClient()
	client.BaseURL = srv.URL
	client.WithCredentials(strings.Repeat("01", 32), nil)

	_, err := ResolveAPIAccountAddress(context.Background(), client, "")
	if err == nil || !strings.Contains(err.Error(), "agent owner") {
		t.Fatalf("err=%v, want agent owner validation error", err)
	}
}

func TestResolveAPIAccountAddressRejectsNonHexConfiguredAddress(t *testing.T) {
	client := sdk.NewClient()
	client.WithCredentials(strings.Repeat("01", 32), nil)

	_, err := ResolveAPIAccountAddress(context.Background(), client, "non-hex-account-alias")
	if err == nil || !strings.Contains(err.Error(), "must be 0x address") {
		t.Fatalf("err=%v, want non-hex configured account rejection", err)
	}
}

func TestResolveAPIAccountAddressQueriesSignerBeforeConfiguredHexOwner(t *testing.T) {
	const owner = "0xabc0000000000000000000000000000000000000"
	client := sdk.NewClient()
	client.WithCredentials(strings.Repeat("01", 32), nil)
	signer := client.AccountAddr
	if signer == "" || strings.EqualFold(signer, owner) {
		t.Fatalf("unexpected signer=%q owner=%q", signer, owner)
	}

	var seenUser string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]string
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		seenUser = req["user"]
		_, _ = w.Write([]byte(`{"role":"agent","data":{"user":"` + owner + `"}}`))
	}))
	defer srv.Close()
	client.BaseURL = srv.URL

	got, err := ResolveAPIAccountAddress(context.Background(), client, owner)
	if err != nil {
		t.Fatalf("ResolveAPIAccountAddress: %v", err)
	}
	if seenUser != signer {
		t.Fatalf("userRole user=%q, want signer %q", seenUser, signer)
	}
	if got != owner || client.AccountAddr != owner {
		t.Fatalf("account=%q client=%q, want owner %q", got, client.AccountAddr, owner)
	}
}

func TestResolveAPIAccountAddressRejectsConfiguredHexOwnerMismatch(t *testing.T) {
	const owner = "0xabc0000000000000000000000000000000000000"
	const configured = "0xdef0000000000000000000000000000000000000"
	client := sdk.NewClient()
	client.WithCredentials(strings.Repeat("01", 32), nil)
	signer := client.AccountAddr

	var seenUser string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]string
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		seenUser = req["user"]
		_, _ = w.Write([]byte(`{"role":"agent","data":{"user":"` + owner + `"}}`))
	}))
	defer srv.Close()
	client.BaseURL = srv.URL

	_, err := ResolveAPIAccountAddress(context.Background(), client, configured)
	if err == nil || !strings.Contains(err.Error(), "does not match userRole owner") {
		t.Fatalf("err=%v, want configured owner mismatch", err)
	}
	if seenUser != signer {
		t.Fatalf("userRole user=%q, want signer %q", seenUser, signer)
	}
}

func TestResolveAPIAccountAddressUsesConfiguredHexWithoutPrivateKey(t *testing.T) {
	const configured = "0xabc0000000000000000000000000000000000000"
	client := sdk.NewClient()
	got, err := ResolveAPIAccountAddress(context.Background(), client, configured)
	if err != nil {
		t.Fatalf("ResolveAPIAccountAddress: %v", err)
	}
	if got != configured || client.AccountAddr != configured {
		t.Fatalf("account=%q client=%q, want configured %q", got, client.AccountAddr, configured)
	}
}

func TestResolveAPIAccountAddressRejectsUnsupportedUserRoles(t *testing.T) {
	for _, role := range []sdk.UserRoleType{sdk.UserRoleVault, sdk.UserRoleSubAccount, sdk.UserRoleUnknown} {
		t.Run(string(role), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"role":"` + string(role) + `","data":{"master":"0xabc0000000000000000000000000000000000000"}}`))
			}))
			defer srv.Close()

			client := sdk.NewClient()
			client.BaseURL = srv.URL
			client.WithCredentials(strings.Repeat("01", 32), nil)

			_, err := ResolveAPIAccountAddress(context.Background(), client, "")
			if err == nil || !strings.Contains(err.Error(), "userRole") {
				t.Fatalf("err=%v, want unsupported userRole rejection", err)
			}
		})
	}
}
