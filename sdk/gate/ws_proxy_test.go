package sdk

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestGateWebSocketDialerUsesProjectProxy(t *testing.T) {
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("NO_PROXY", "")
	t.Setenv("PROXY", "http://proxy-user:proxy-pass@127.0.0.1:7890")
	t.Setenv("ALL_PROXY", "")

	dialer, err := websocketDialerForURL("wss://fx-ws-testnet.gateio.ws/v4/ws/usdt")
	if err != nil {
		t.Fatalf("websocketDialerForURL: %v", err)
	}
	proxyURL, err := dialer.Proxy(&http.Request{URL: mustGateProxyTestURL(t, "https://fx-ws-testnet.gateio.ws")})
	if err != nil {
		t.Fatalf("proxy: %v", err)
	}
	if proxyURL == nil || proxyURL.Host != "127.0.0.1:7890" || proxyURL.User == nil {
		t.Fatalf("proxy URL=%v, want configured project proxy", proxyURL)
	}
}

func TestGateWebSocketDialerBypassesProxyForLoopback(t *testing.T) {
	t.Setenv("PROXY", "http://127.0.0.1:7890")
	dialer, err := websocketDialerForURL("ws://127.0.0.1:8080/ws")
	if err != nil {
		t.Fatalf("websocketDialerForURL: %v", err)
	}
	if dialer.Proxy != nil {
		t.Fatal("loopback websocket must bypass environment proxy")
	}
}

func TestGateWebSocketDialerRedactsInvalidProxyCredentials(t *testing.T) {
	const secret = "gate-proxy-secret"
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("NO_PROXY", "")
	t.Setenv("PROXY", "http://proxy-user:"+secret+"@%gh")
	t.Setenv("ALL_PROXY", "")

	dialer, err := websocketDialerForURL("wss://fx-ws-testnet.gateio.ws/v4/ws/usdt")
	if err != nil {
		t.Fatalf("websocketDialerForURL: %v", err)
	}
	_, err = dialer.Proxy(&http.Request{URL: mustGateProxyTestURL(t, "https://fx-ws-testnet.gateio.ws")})
	if err == nil {
		t.Fatal("malformed proxy unexpectedly accepted")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("proxy credential leaked in error: %v", err)
	}
}

func mustGateProxyTestURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse URL: %v", err)
	}
	return parsed
}
