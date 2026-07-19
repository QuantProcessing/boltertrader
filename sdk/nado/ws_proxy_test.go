package nado

import (
	"fmt"
	"strings"
	"testing"
)

func TestNadoWebSocketProxyUsesProjectProxyAndBypassesLoopback(t *testing.T) {
	t.Setenv("PROXY", "http://user:secret@proxy.example:8080")
	proxy, err := nadoProxyForURL("wss://gateway.test.nado.xyz/v1/ws")
	if err != nil {
		t.Fatalf("nadoProxyForURL: %v", err)
	}
	if proxy == nil || proxy.Host != "proxy.example:8080" {
		t.Fatalf("proxy = %v", proxy)
	}
	proxy, err = nadoProxyForURL("ws://127.0.0.1:8080/ws")
	if err != nil {
		t.Fatalf("loopback proxy check: %v", err)
	}
	if proxy != nil {
		t.Fatalf("loopback proxy = %v, want nil", proxy)
	}
}

func TestNadoWebSocketProxyErrorsDoNotLeakCredentials(t *testing.T) {
	t.Setenv("PROXY", "nado+bad://user:secret@proxy.example:8080")
	_, err := nadoProxyForURL("wss://gateway.test.nado.xyz/v1/ws")
	if err == nil {
		t.Fatal("expected unsupported proxy scheme")
	}
	rendered := fmt.Sprintf("%v", err)
	if strings.Contains(rendered, "secret") || strings.Contains(rendered, "user:") {
		t.Fatalf("proxy error leaked credentials: %s", rendered)
	}
}
