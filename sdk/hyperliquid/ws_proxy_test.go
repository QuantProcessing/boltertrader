package hyperliquid

import (
	"strings"
	"testing"
)

func TestWebsocketDialerBypassesProxyForLoopback(t *testing.T) {
	t.Setenv("PROXY", "socks5://proxy-user:proxy-secret@127.0.0.1:1080")
	dialer, err := websocketDialerForURL("ws://127.0.0.1:8080/ws")
	if err != nil {
		t.Fatalf("websocketDialerForURL: %v", err)
	}
	if dialer.Proxy != nil {
		t.Fatal("loopback websocket must bypass environment proxies")
	}
}

func TestProjectProxySupportsPROXYAndALLPROXYSocks5WithoutCredentialLeak(t *testing.T) {
	t.Setenv("PROXY", "socks5://proxy-user:proxy-secret@proxy.example:1080")
	t.Setenv("ALL_PROXY", "")
	proxyURL, err := projectProxyFromEnvironment()
	if err != nil {
		t.Fatalf("projectProxyFromEnvironment: %v", err)
	}
	if proxyURL == nil || proxyURL.Scheme != "socks5" || proxyURL.Host != "proxy.example:1080" {
		t.Fatalf("proxy=%v, want configured socks5 PROXY", proxyURL)
	}

	t.Setenv("PROXY", "")
	t.Setenv("ALL_PROXY", "http://all-proxy.example:8080")
	proxyURL, err = projectProxyFromEnvironment()
	if err != nil || proxyURL == nil || proxyURL.Host != "all-proxy.example:8080" {
		t.Fatalf("ALL_PROXY fallback proxy=%v err=%v", proxyURL, err)
	}

	t.Setenv("PROXY", "secret-scheme://proxy-user:proxy-secret@proxy.example:1080")
	_, err = projectProxyFromEnvironment()
	if err == nil || strings.Contains(err.Error(), "proxy-secret") || strings.Contains(err.Error(), "proxy-user") {
		t.Fatalf("invalid proxy err=%v, want redacted validation error", err)
	}
}
