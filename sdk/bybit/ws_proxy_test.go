package sdk

import (
	"net/http"
	"strings"
	"testing"
)

func TestWebSocketProxyFromEnvironmentRedactsMalformedFallbackCredentials(t *testing.T) {
	const secret = "proxy-super-secret"
	t.Setenv("PROXY", "http://user:"+secret+"@%gh")

	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1/ws", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	_, err = websocketProxyFromEnvironment(req)
	if err == nil {
		t.Fatal("malformed fallback proxy unexpectedly accepted")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("proxy credential leaked in error: %v", err)
	}
}

func TestWebSocketProxyFromEnvironmentPreservesValidFallbackProxy(t *testing.T) {
	t.Setenv("PROXY", "http://user:pass@proxy.example:8080")

	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1/ws", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	proxyURL, err := websocketProxyFromEnvironment(req)
	if err != nil {
		t.Fatalf("websocketProxyFromEnvironment: %v", err)
	}
	if proxyURL == nil || proxyURL.Scheme != "http" || proxyURL.Host != "proxy.example:8080" {
		t.Fatalf("proxy URL = %v", proxyURL)
	}
	if proxyURL.User == nil {
		t.Fatal("valid proxy credentials were dropped")
	}
	password, ok := proxyURL.User.Password()
	if !ok || proxyURL.User.Username() != "user" || password != "pass" {
		t.Fatalf("proxy user info was not preserved")
	}
}
