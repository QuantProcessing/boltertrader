package sdk

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/gorilla/websocket"
)

func websocketDialerFromEnvironment() *websocket.Dialer {
	dialer := *websocket.DefaultDialer
	dialer.Proxy = websocketProxyFromEnvironment
	return &dialer
}

func websocketProxyFromEnvironment(req *http.Request) (*url.URL, error) {
	proxyURL, err := http.ProxyFromEnvironment(req)
	if proxyURL != nil || err != nil {
		return proxyURL, err
	}
	raw := strings.TrimSpace(os.Getenv("PROXY"))
	if raw != "" {
		return parseBybitProxyURL(raw)
	}
	if req != nil && req.URL != nil && bybitLoopbackHost(req.URL.Hostname()) {
		return nil, nil
	}
	raw = strings.TrimSpace(os.Getenv("ALL_PROXY"))
	if raw == "" {
		return nil, nil
	}
	return parseBybitProxyURL(raw)
}

func parseBybitProxyURL(raw string) (*url.URL, error) {
	proxyURL, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("invalid websocket proxy configuration")
	}
	return proxyURL, nil
}

func bybitLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
