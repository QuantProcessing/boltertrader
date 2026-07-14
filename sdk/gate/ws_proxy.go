package sdk

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
		return nil, fmt.Errorf("gate websocket: invalid endpoint URL")
	}
	dialer := *websocket.DefaultDialer
	if gateLoopbackHost(target.Hostname()) {
		dialer.Proxy = nil
		return &dialer, nil
	}
	dialer.Proxy = gateWebSocketProxy
	return &dialer, nil
}

func gateWebSocketProxy(req *http.Request) (*url.URL, error) {
	raw := strings.TrimSpace(os.Getenv("PROXY"))
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("ALL_PROXY"))
	}
	if raw == "" {
		return http.ProxyFromEnvironment(req)
	}
	proxyURL, err := url.Parse(raw)
	if err != nil || proxyURL.Host == "" {
		return nil, fmt.Errorf("gate websocket: invalid proxy URL")
	}
	switch strings.ToLower(proxyURL.Scheme) {
	case "http", "https", "socks5":
		return proxyURL, nil
	default:
		return nil, fmt.Errorf("gate websocket: unsupported proxy scheme")
	}
}

func gateLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
