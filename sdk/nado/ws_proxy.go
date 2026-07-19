package nado

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	coder "github.com/coder/websocket"
	gorilla "github.com/gorilla/websocket"
)

func coderDialOptionsForURL(rawURL string) (*coder.DialOptions, error) {
	proxy, err := nadoProxyForURL(rawURL)
	if err != nil {
		return nil, err
	}
	if proxy == nil {
		return &coder.DialOptions{CompressionMode: coder.CompressionContextTakeover}, nil
	}
	return &coder.DialOptions{
		CompressionMode: coder.CompressionContextTakeover,
		HTTPClient: &http.Client{Transport: &http.Transport{
			Proxy: func(*http.Request) (*url.URL, error) { return proxy, nil },
		}},
	}, nil
}

func gorillaDialerForURL(rawURL string) (*gorilla.Dialer, error) {
	dialer := *gorilla.DefaultDialer
	proxy, err := nadoProxyForURL(rawURL)
	if err != nil {
		return nil, err
	}
	if proxy != nil {
		dialer.Proxy = func(*http.Request) (*url.URL, error) { return proxy, nil }
	}
	return &dialer, nil
}

func nadoProxyForURL(rawURL string) (*url.URL, error) {
	target, err := url.Parse(rawURL)
	if err != nil || target.Hostname() == "" {
		return nil, fmt.Errorf("nado websocket: invalid endpoint URL")
	}
	if nadoLoopbackHost(target.Hostname()) {
		return nil, nil
	}
	req := &http.Request{URL: &url.URL{Scheme: strings.Replace(target.Scheme, "ws", "http", 1), Host: target.Host}}
	if proxy, err := http.ProxyFromEnvironment(req); proxy != nil || err != nil {
		return proxy, err
	}
	raw := strings.TrimSpace(os.Getenv("PROXY"))
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("ALL_PROXY"))
	}
	if raw == "" {
		return nil, nil
	}
	proxy, err := url.Parse(raw)
	if err != nil || proxy.Host == "" {
		return nil, fmt.Errorf("nado websocket: invalid proxy URL")
	}
	switch strings.ToLower(proxy.Scheme) {
	case "http", "https", "socks5":
		return proxy, nil
	default:
		return nil, fmt.Errorf("nado websocket: unsupported proxy scheme %q", proxy.Scheme)
	}
}

func nadoLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
