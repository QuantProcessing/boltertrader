package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	sdkcore "github.com/QuantProcessing/exchanges/sdk"
)

const (
	defaultBaseURL = "https://api.bitget.com"
	publicWSURL    = "wss://ws.bitget.com/v2/ws/public"
	privateWSURL   = "wss://ws.bitget.com/v3/ws/private"
	classicWSURL   = "wss://ws.bitget.com/v2/ws/private"
)

type Client struct {
	baseURL    string
	httpClient *http.Client
	apiKey     string
	secretKey  string
	passphrase string
}

func NewClient() *Client {
	return &Client{
		baseURL: defaultBaseURL,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

func (c *Client) WithCredentials(apiKey, secretKey, passphrase string) *Client {
	c.apiKey = apiKey
	c.secretKey = secretKey
	c.passphrase = passphrase
	return c
}

func (c *Client) WithBaseURL(baseURL string) *Client {
	c.baseURL = baseURL
	return c
}

func (c *Client) WithHTTPClient(httpClient *http.Client) *Client {
	c.httpClient = httpClient
	return c
}

func (c *Client) HasCredentials() bool {
	return c.apiKey != "" && c.secretKey != "" && c.passphrase != ""
}

type responseEnvelope[T any] struct {
	Code        string `json:"code"`
	Msg         string `json:"msg"`
	RequestTime int64  `json:"requestTime"`
	Data        T      `json:"data"`
}

func (c *Client) get(ctx context.Context, path string, query map[string]string, out any) error {
	return c.getInternal(ctx, path, query, false, out)
}

func (c *Client) getPrivate(ctx context.Context, path string, query map[string]string, out any) error {
	if !c.HasCredentials() {
		return fmt.Errorf("bitget sdk: credentials required")
	}
	return c.getInternal(ctx, path, query, true, out)
}

func (c *Client) getInternal(ctx context.Context, path string, query map[string]string, signed bool, out any) error {
	u, err := url.Parse(c.baseURL + path)
	if err != nil {
		return err
	}

	values := u.Query()
	for key, value := range query {
		if value != "" {
			values.Set(key, value)
		}
	}
	u.RawQuery = values.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	if signed {
		c.signHeaders(req, u.RawQuery, "")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("bitget sdk: GET %s returned %s: %s", path, resp.Status, string(bodyBytes))
	}

	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) postPrivate(ctx context.Context, path string, body any, out any) error {
	if !c.HasCredentials() {
		return fmt.Errorf("bitget sdk: credentials required")
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	c.signHeaders(req, "", string(payload))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("bitget sdk: POST %s returned %s: %s (request=%s)", path, resp.Status, string(bodyBytes), string(payload))
	}

	return json.NewDecoder(resp.Body).Decode(out)
}

// GetPrivateRaw executes an authenticated GET request for official Bitget
// endpoints that are SDK-covered by the raw fallback before a typed method is
// introduced.
func (c *Client) GetPrivateRaw(ctx context.Context, path string, query map[string]string, out any) error {
	return c.getPrivate(ctx, path, query, out)
}

// PostPrivateRaw executes an authenticated POST request for official Bitget
// endpoints that are SDK-covered by the raw fallback before a typed method is
// introduced.
func (c *Client) PostPrivateRaw(ctx context.Context, path string, body any, out any) error {
	return c.postPrivate(ctx, path, body, out)
}

func applySDKRequestOptsString(params map[string]string, opts sdkcore.RequestOpts) {
	if opts.RecvWindowMillis > 0 {
		params["recvWindow"] = fmt.Sprintf("%d", opts.RecvWindowMillis)
	}
	if opts.ClientRequestID != "" {
		params["clientOid"] = opts.ClientRequestID
	}
}
