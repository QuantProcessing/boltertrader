package hyperliquid

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientGetUserAbstraction(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/info" {
			t.Fatalf("path=%s, want /info", r.URL.Path)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["type"] != "userAbstraction" || body["user"] != "0xabc" {
			t.Fatalf("unexpected body: %+v", body)
		}
		_, _ = w.Write([]byte(`"unifiedAccount"`))
	}))
	defer srv.Close()

	client := NewClient().WithAccount("0xabc")
	client.BaseURL = srv.URL
	got, err := client.GetUserAbstraction(context.Background(), "")
	if err != nil {
		t.Fatalf("GetUserAbstraction: %v", err)
	}
	if got != AccountAbstractionUnifiedAccount || !got.UsesSpotClearinghouseState() {
		t.Fatalf("mode=%q", got)
	}
}

func TestClientGetUserRoleAgentReturnsOwner(t *testing.T) {
	var seenBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = string(body)
		_, _ = w.Write([]byte(`{"role":"agent","data":{"user":"0xabc0000000000000000000000000000000000000"}}`))
	}))
	defer srv.Close()
	client := NewClient()
	client.BaseURL = srv.URL

	role, err := client.GetUserRole(context.Background(), "0xagent000000000000000000000000000000000000")
	if err != nil {
		t.Fatalf("GetUserRole: %v", err)
	}
	if role.Role != UserRoleAgent || role.Data.User != "0xabc0000000000000000000000000000000000000" {
		t.Fatalf("role=%+v, want agent owner user", role)
	}
	if !strings.Contains(seenBody, `"type":"userRole"`) || !strings.Contains(seenBody, `"user":"0xagent000000000000000000000000000000000000"`) {
		t.Fatalf("unexpected userRole body: %s", seenBody)
	}
}

func TestClientGetSpotClearinghouseState(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["type"] != "spotClearinghouseState" || body["user"] != "0xabc" {
			t.Fatalf("unexpected body: %+v", body)
		}
		_, _ = w.Write([]byte(`{"balances":[{"coin":"USDC","token":0,"hold":"1.5","total":"10","entryNtl":"0"}]}`))
	}))
	defer srv.Close()

	client := NewClient().WithAccount("0xabc")
	client.BaseURL = srv.URL
	got, err := client.GetSpotClearinghouseState(context.Background(), "")
	if err != nil {
		t.Fatalf("GetSpotClearinghouseState: %v", err)
	}
	if len(got.Balances) != 1 || got.Balances[0].Coin != "USDC" || got.Balances[0].Total != "10" {
		t.Fatalf("state=%+v", got)
	}
}
