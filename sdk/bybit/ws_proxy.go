package sdk

import (
	"errors"
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
	if raw == "" {
		return nil, nil
	}
	proxyURL, err = url.Parse(raw)
	if err != nil {
		return nil, errors.New("invalid websocket proxy configuration")
	}
	return proxyURL, nil
}
