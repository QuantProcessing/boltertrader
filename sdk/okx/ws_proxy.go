package okx

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

func okxWebSocketDialerFromEnvironment() *websocket.Dialer {
	dialer := &websocket.Dialer{
		ReadBufferSize:    65535,
		WriteBufferSize:   8192,
		HandshakeTimeout:  45 * time.Second,
		EnableCompression: true,
	}
	dialer.Proxy = websocketProxyFromEnvironment
	return dialer
}

func websocketProxyFromEnvironment(req *http.Request) (*url.URL, error) {
	proxyURL, err := http.ProxyFromEnvironment(req)
	if proxyURL != nil || err != nil {
		return proxyURL, err
	}
	raw := strings.TrimSpace(os.Getenv("PROXY"))
	if raw != "" {
		return parseOKXProxyURL(raw)
	}
	if req != nil && req.URL != nil && okxLoopbackHost(req.URL.Hostname()) {
		return nil, nil
	}
	raw = strings.TrimSpace(os.Getenv("ALL_PROXY"))
	if raw == "" {
		return nil, nil
	}
	return parseOKXProxyURL(raw)
}

func parseOKXProxyURL(raw string) (*url.URL, error) {
	proxyURL, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("invalid websocket proxy configuration")
	}
	return proxyURL, nil
}

func okxLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
