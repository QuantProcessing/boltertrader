package sdk

import (
	"net/http"
	"testing"
)

func TestAuth_Sign(t *testing.T) {
	got := sign("secret", "payload")
	if got != "b82fcb791acec57859b989b430a826488ce2e479fdf92326bd0a2e8375a42ba4" {
		t.Fatalf("unexpected signature: %s", got)
	}
}

func TestClientConfiguredRecvWindowIsSignedAndSent(t *testing.T) {
	client := NewClient().
		WithCredentials("key", "secret").
		WithRecvWindowMillis(15000)
	req, err := http.NewRequest(http.MethodGet, "https://unit.test/v5/order/realtime?category=linear", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	client.signHeaders(req, req.URL.RawQuery, "")

	timestamp := req.Header.Get("X-BAPI-TIMESTAMP")
	if timestamp == "" {
		t.Fatal("missing timestamp header")
	}
	if got := req.Header.Get("X-BAPI-RECV-WINDOW"); got != "15000" {
		t.Fatalf("recv window header=%q, want 15000", got)
	}
	wantSignature := sign("secret", timestamp+"key"+"15000"+req.URL.RawQuery)
	if got := req.Header.Get("X-BAPI-SIGN"); got != wantSignature {
		t.Fatalf("signature=%q, want %q", got, wantSignature)
	}
}

func TestClientConfiguredRecvWindowSignsPostBody(t *testing.T) {
	client := NewClient().
		WithCredentials("key", "secret").
		WithRecvWindowMillis(15000)
	const body = `{"category":"linear","symbol":"BTCUSDT"}`
	req, err := http.NewRequest(http.MethodPost, "https://unit.test/v5/order/create", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	client.signHeaders(req, "", body)

	timestamp := req.Header.Get("X-BAPI-TIMESTAMP")
	wantSignature := sign("secret", timestamp+"key"+"15000"+body)
	if got := req.Header.Get("X-BAPI-RECV-WINDOW"); got != "15000" {
		t.Fatalf("recv window header=%q, want 15000", got)
	}
	if got := req.Header.Get("X-BAPI-SIGN"); got != wantSignature {
		t.Fatalf("signature=%q, want %q", got, wantSignature)
	}
}

func TestClientNonPositiveRecvWindowKeepsSecureDefault(t *testing.T) {
	client := NewClient().WithRecvWindowMillis(0)
	req, err := http.NewRequest(http.MethodGet, "https://unit.test/v5/account/info", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	client.signHeaders(req, "", "")

	if got := req.Header.Get("X-BAPI-RECV-WINDOW"); got != defaultRecvWindow {
		t.Fatalf("recv window header=%q, want default %s", got, defaultRecvWindow)
	}
	timestamp := req.Header.Get("X-BAPI-TIMESTAMP")
	wantSignature := sign("", timestamp+defaultRecvWindow)
	if got := req.Header.Get("X-BAPI-SIGN"); got != wantSignature {
		t.Fatalf("default signature=%q, want %q", got, wantSignature)
	}
}
