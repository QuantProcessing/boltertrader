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

func TestWebSocketProxyFromEnvironmentUsesAllProxyFallback(t *testing.T) {
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("NO_PROXY", "")
	t.Setenv("PROXY", "")
	t.Setenv("ALL_PROXY", "http://proxy.example:10900")

	req, err := http.NewRequest(http.MethodGet, "wss://stream-demo.bybit.com/v5/private", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	proxyURL, err := websocketProxyFromEnvironment(req)
	if err != nil {
		t.Fatalf("websocketProxyFromEnvironment: %v", err)
	}
	if proxyURL == nil || proxyURL.Scheme != "http" || proxyURL.Host != "proxy.example:10900" {
		t.Fatalf("proxy URL = %v", proxyURL)
	}
}

func TestWebSocketProxyFromEnvironmentBypassesFallbackProxyForLoopback(t *testing.T) {
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("NO_PROXY", "")
	t.Setenv("PROXY", "")
	t.Setenv("ALL_PROXY", "http://proxy.example:10900")

	req, err := http.NewRequest(http.MethodGet, "ws://127.0.0.1:8080/ws", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	proxyURL, err := websocketProxyFromEnvironment(req)
	if err != nil {
		t.Fatalf("websocketProxyFromEnvironment: %v", err)
	}
	if proxyURL != nil {
		t.Fatalf("loopback proxy URL = %v, want nil", proxyURL)
	}
}
