package hyperliquid

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
