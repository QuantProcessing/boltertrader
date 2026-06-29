package subaccount

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const (
	BaseURL       = "https://api.binance.com"
	ServerTimeURL = "https://api.binance.com"
)

type Client struct {
	BaseURL           string
	ServerTimeBaseURL string
	APIKey            string
	SecretKey         string
	HTTPClient        *http.Client
}

func NewClient() *Client {
	return &Client{
		BaseURL:           BaseURL,
		ServerTimeBaseURL: ServerTimeURL,
		HTTPClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (c *Client) WithCredentials(apiKey, secretKey string) *Client {
	c.APIKey = apiKey
	c.SecretKey = secretKey
	return c
}

func (c *Client) WithBaseURL(baseURL string) *Client {
	c.BaseURL = baseURL
	return c
}

func (c *Client) WithServerTimeBaseURL(baseURL string) *Client {
	c.ServerTimeBaseURL = baseURL
	return c
}

func (c *Client) get(ctx context.Context, endpoint string, params map[string]string, signed bool, out any) error {
	return c.call(ctx, http.MethodGet, endpoint, params, signed, out)
}

func (c *Client) post(ctx context.Context, endpoint string, params map[string]string, signed bool, out any) error {
	return c.call(ctx, http.MethodPost, endpoint, params, signed, out)
}

func (c *Client) call(ctx context.Context, method, endpoint string, params map[string]string, signed bool, out any) error {
	u, err := url.Parse(c.BaseURL + endpoint)
	if err != nil {
		return err
	}

	q := u.Query()
	for key, value := range params {
		if value != "" {
			q.Set(key, value)
		}
	}
	if signed {
		if q.Get("recvWindow") == "" {
			q.Set("recvWindow", "60000")
		}
		q.Set("timestamp", fmt.Sprintf("%d", c.timestamp(ctx)))
		queryString := q.Encode()
		q.Set("signature", signature(c.SecretKey, queryString))
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, method, u.String(), nil)
	if err != nil {
		return err
	}
	if c.APIKey != "" {
		req.Header.Set("X-MBX-APIKEY", c.APIKey)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("binance subaccount sdk: %s %s returned %s: %s", method, endpoint, resp.Status, string(body))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode %s: %w", endpoint, err)
	}
	return nil
}

func (c *Client) timestamp(ctx context.Context) int64 {
	serverTime, err := c.serverTime(ctx)
	if err != nil {
		return time.Now().UnixMilli()
	}
	return serverTime
}

func (c *Client) serverTime(ctx context.Context) (int64, error) {
	baseURL := c.ServerTimeBaseURL
	if baseURL == "" {
		baseURL = ServerTimeURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/v3/time", nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	var out struct {
		ServerTime int64 `json:"serverTime"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, err
	}
	return out.ServerTime, nil
}

func signature(secretKey, payload string) string {
	mac := hmac.New(sha256.New, []byte(secretKey))
	_, _ = mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}
