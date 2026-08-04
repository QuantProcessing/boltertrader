package okx

import (
	"net/http"
	"strings"
	"testing"
)

func TestWebSocketProxyFromEnvironmentUsesAllProxyFallback(t *testing.T) {
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("NO_PROXY", "")
	t.Setenv("PROXY", "")
	t.Setenv("ALL_PROXY", "http://proxy.example:10900")

	req, err := http.NewRequest(http.MethodGet, "wss://wspap.okx.com:8443/ws/v5/private", nil)
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

func TestWebSocketProxyFromEnvironmentBypassesAllProxyFallbackForLoopback(t *testing.T) {
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

func TestWebSocketProxyFromEnvironmentPrefersProjectProxyOverAllProxy(t *testing.T) {
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("NO_PROXY", "")
	t.Setenv("PROXY", "http://project-proxy.example:7897")
	t.Setenv("ALL_PROXY", "http://all-proxy.example:10900")

	req, err := http.NewRequest(http.MethodGet, "wss://wspap.okx.com:8443/ws/v5/private", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	proxyURL, err := websocketProxyFromEnvironment(req)
	if err != nil {
		t.Fatalf("websocketProxyFromEnvironment: %v", err)
	}
	if proxyURL == nil || proxyURL.Host != "project-proxy.example:7897" {
		t.Fatalf("proxy URL = %v, want project PROXY fallback", proxyURL)
	}
}

func TestWebSocketProxyFromEnvironmentRedactsMalformedFallbackCredentials(t *testing.T) {
	const secret = "proxy-super-secret"
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("NO_PROXY", "")
	t.Setenv("PROXY", "http://user:"+secret+"@%gh")
	t.Setenv("ALL_PROXY", "")

	req, err := http.NewRequest(http.MethodGet, "wss://wspap.okx.com:8443/ws/v5/private", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	_, err = websocketProxyFromEnvironment(req)
	if err == nil {
		t.Fatal("malformed fallback proxy unexpectedly accepted")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "user:") {
		t.Fatalf("proxy credential leaked in error: %v", err)
	}
}
