package common

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// WebSocketDialer returns an isolated dialer. Loopback endpoints always bypass
// proxies so fixture servers and local control planes stay deterministic.
func WebSocketDialer(rawURL, proxyURL string) (*websocket.Dialer, error) {
	target, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("aster websocket: invalid endpoint URL")
	}
	dialer := *websocket.DefaultDialer
	if isLoopbackHost(target.Hostname()) {
		dialer.Proxy = nil
		return &dialer, nil
	}
	if proxyURL == "" {
		return &dialer, nil
	}
	proxy, err := url.Parse(proxyURL)
	if err != nil || proxy.Scheme == "" || proxy.Host == "" {
		return nil, fmt.Errorf("aster websocket: invalid proxy URL")
	}
	dialer.Proxy = http.ProxyURL(proxy)
	dialer.HandshakeTimeout = 45 * time.Second
	return &dialer, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
