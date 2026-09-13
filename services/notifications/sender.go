package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Sender struct{ client *http.Client }

func NewSender() *Sender {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Resolve and validate at connection time to prevent DNS rebinding to local
	// services. A configured Bark server must have a public HTTPS address.
	transport.Proxy = nil
	transport.DialContext = publicDial
	return &Sender{client: &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func validateServerURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 512 || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.Trim(u.Path, "/") != "" || (u.Port() != "" && u.Port() != "443") {
		return ErrInvalid
	}
	if strings.EqualFold(u.Hostname(), "localhost") {
		return ErrInvalid
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil && !publicIP(ip) {
		return ErrInvalid
	}
	return nil
}

func publicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !netip.MustParsePrefix("100.64.0.0/10").Contains(ip) && !netip.MustParsePrefix("0.0.0.0/8").Contains(ip)
}

func publicDial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, ErrDelivery
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(ips) == 0 {
		return nil, ErrDelivery
	}
	for _, ip := range ips {
		if !publicIP(ip) {
			return nil, ErrDelivery
		}
	}
	dialer := net.Dialer{Timeout: 5 * time.Second}
	for _, ip := range ips {
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
	}
	return nil, ErrDelivery
}

func (s *Sender) Send(ctx context.Context, provider Provider, input Input, message Message) error {
	if err := validate(provider, &input); err != nil {
		return err
	}
	var endpoint string
	var payload map[string]any
	switch provider {
	case Telegram:
		endpoint = "https://api.telegram.org/bot" + input.Secret + "/sendMessage"
		payload = map[string]any{"chat_id": input.ChatID, "text": message.Title + "\n" + message.Body, "disable_notification": false}
	case Bark:
		endpoint = input.ServerURL + "/push"
		level := "active"
		if input.Critical {
			level = "critical"
		}
		payload = map[string]any{"device_key": input.Secret, "title": message.Title, "body": message.Body, "group": "Bolter", "level": level}
		if input.Critical {
			payload["volume"] = strconv.Itoa(input.Volume)
		}
	}
	body, _ := json.Marshal(payload)
	defer clear(body)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return ErrDelivery
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	// Transport errors contain Telegram's token-bearing URL; never return them.
	if err != nil {
		return ErrDelivery
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%w (HTTP %d)", ErrDelivery, resp.StatusCode)
	}
	var result struct {
		OK   bool `json:"ok"`
		Code int  `json:"code"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 16*1024)).Decode(&result) != nil {
		return ErrDelivery
	}
	if (provider == Telegram && !result.OK) || (provider == Bark && result.Code != 200) {
		return ErrDelivery
	}
	return nil
}
