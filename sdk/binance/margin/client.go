package margin

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
	"os"
	"time"

	"go.uber.org/zap"
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
	Logger            *zap.SugaredLogger
}

func NewClient() *Client {
	timeout := 10 * time.Second
	httpClient := &http.Client{
		Timeout: timeout,
	}
	l := zap.NewNop().Sugar().Named("binance-margin")

	// Check for proxy in environment
	proxyURL := os.Getenv("PROXY")

	if proxyURL != "" {
		parsedURL, err := url.Parse(proxyURL)
		if err == nil {
			httpClient.Transport = &http.Transport{
				Proxy: http.ProxyURL(parsedURL),
			}
		} else {
			l.Warnw("Invalid proxy URL", "url", proxyURL, "error", err)
		}
	}

	return &Client{
		BaseURL:           BaseURL,
		ServerTimeBaseURL: ServerTimeURL,
		HTTPClient:        httpClient,
		Logger:            l,
	}
}

func (c *Client) WithCredentials(apiKey, secretKey string) *Client {
	c.APIKey = apiKey
	c.SecretKey = secretKey
	return c
}

func (c *Client) WithBaseURL(url string) *Client {
	c.BaseURL = url
	return c
}

func (c *Client) WithServerTimeBaseURL(url string) *Client {
	c.ServerTimeBaseURL = url
	return c
}

// GenerateSignature generates an HMAC SHA256 signature
func GenerateSignature(secretKey, data string) string {
	h := hmac.New(sha256.New, []byte(secretKey))
	h.Write([]byte(data))
	return hex.EncodeToString(h.Sum(nil))
}

// Timestamp returns the current timestamp in milliseconds
func Timestamp() int64 {
	return time.Now().UnixNano() / int64(time.Millisecond)
}

func (c *Client) call(ctx context.Context, method, endpoint string, params map[string]interface{}, signed bool, result interface{}) error {
	u, err := url.Parse(c.BaseURL + endpoint)
	if err != nil {
		return err
	}

	q := u.Query()
	for k, v := range params {
		q.Add(k, fmt.Sprintf("%v", v))
	}

	if signed {
		if q.Get("recvWindow") == "" {
			q.Add("recvWindow", "60000")
		}
		q.Add("timestamp", fmt.Sprintf("%d", c.timestamp(ctx)))
		queryString := q.Encode()
		sig := GenerateSignature(c.SecretKey, queryString)
		u.RawQuery = queryString + "&signature=" + sig
	} else {
		u.RawQuery = q.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, method, u.String(), nil)
	if err != nil {
		return err
	}

	if c.APIKey != "" {
		req.Header.Add("X-MBX-APIKEY", c.APIKey)
	}

	c.Logger.Debugw("Request", "method", method, "url", u.String())

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	c.Logger.Debugw("Response", "body", string(data))

	if resp.StatusCode >= 400 {
		var apiErr APIError
		if err := json.Unmarshal(data, &apiErr); err != nil {
			return fmt.Errorf("http error %d: %s", resp.StatusCode, string(data))
		}
		return &apiErr
	}

	if result != nil {
		if err := json.Unmarshal(data, result); err != nil {
			return fmt.Errorf("failed to unmarshal response: %w", err)
		}
	}

	return nil
}

func (c *Client) timestamp(ctx context.Context) int64 {
	serverTime, err := c.serverTime(ctx)
	if err != nil {
		return Timestamp()
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
	if resp.StatusCode >= 300 {
		return 0, fmt.Errorf("server time returned %s", resp.Status)
	}
	var out struct {
		ServerTime int64 `json:"serverTime"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, err
	}
	return out.ServerTime, nil
}

func (c *Client) Get(ctx context.Context, endpoint string, params map[string]interface{}, signed bool, result interface{}) error {
	return c.call(ctx, http.MethodGet, endpoint, params, signed, result)
}

func (c *Client) Post(ctx context.Context, endpoint string, params map[string]interface{}, signed bool, result interface{}) error {
	return c.call(ctx, http.MethodPost, endpoint, params, signed, result)
}

func (c *Client) Delete(ctx context.Context, endpoint string, params map[string]interface{}, signed bool, result interface{}) error {
	return c.call(ctx, http.MethodDelete, endpoint, params, signed, result)
}

type APIError struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("binance API error: code=%d, msg=%s", e.Code, e.Msg)
}
