package hyperliquid

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/gorilla/websocket"
)

func websocketDialerForURL(rawURL string) (*websocket.Dialer, error) {
	target, err := url.Parse(rawURL)
	if err != nil || target.Hostname() == "" {
		return nil, fmt.Errorf("hyperliquid websocket: invalid endpoint URL")
	}
	dialer := *websocket.DefaultDialer
	if isLoopbackHost(target.Hostname()) {
		dialer.Proxy = nil
		return &dialer, nil
	}
	dialer.Proxy = websocketProxyFromEnvironment
	return &dialer, nil
}

func websocketProxyFromEnvironment(req *http.Request) (*url.URL, error) {
	proxyURL, err := http.ProxyFromEnvironment(req)
	if proxyURL != nil || err != nil {
		return proxyURL, err
	}
	return projectProxyFromEnvironment()
}

func projectProxyFromEnvironment() (*url.URL, error) {
	raw := strings.TrimSpace(os.Getenv("PROXY"))
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("ALL_PROXY"))
	}
	if raw == "" {
		return nil, nil
	}
	proxyURL, err := url.Parse(raw)
	if err != nil || proxyURL.Host == "" {
		return nil, fmt.Errorf("hyperliquid websocket: invalid proxy URL")
	}
	switch strings.ToLower(proxyURL.Scheme) {
	case "http", "https", "socks5":
		return proxyURL, nil
	default:
		return nil, fmt.Errorf("hyperliquid websocket: unsupported proxy scheme %q", proxyURL.Scheme)
	}
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
