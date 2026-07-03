package okx

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	sdkcore "github.com/QuantProcessing/boltertrader/sdk"
)

const (
	BaseURL = "https://www.okx.com"
)

type Client struct {
	ApiKey     string
	SecretKey  string
	Passphrase string

	HTTPClient *http.Client
	Signer     *Signer
	// Environment selects production or OKX simulated trading. The zero value
	// is treated as Production for compatibility with older tests.
	Environment Environment
	// DemoHostProfile selects the official Demo host family when Environment is
	// Simulated. The zero value is treated as DemoHostProfileGlobal.
	DemoHostProfile DemoHostProfile

	// BaseURL overrides the default OKX base URL. Used in tests to point at
	// an httptest server. Leave empty to use the package-level BaseURL constant.
	BaseURL string
}

func NewClient() *Client {
	httpClient := &http.Client{Timeout: 10 * time.Second}
	proxyEnv := os.Getenv("PROXY")
	if proxyEnv != "" {
		proxyURL, err := url.Parse(proxyEnv)
		if err != nil {
			fmt.Printf("Invalid PROXY URL: %s, error: %v\n", proxyEnv, err)
		} else {
			transport := &http.Transport{
				Proxy: http.ProxyURL(proxyURL),
			}
			httpClient.Transport = transport
		}
	}

	return &Client{
		HTTPClient:      httpClient,
		Environment:     Production,
		DemoHostProfile: DemoHostProfileGlobal,
	}
}

func (c *Client) WithCredentials(apiKey, secretKey, passphrase string) *Client {
	c.ApiKey = apiKey
	c.SecretKey = secretKey
	c.Passphrase = passphrase
	c.Signer = NewSigner(secretKey)
	return c
}

func (c *Client) WithHTTPClient(httpClient *http.Client) *Client {
	c.HTTPClient = httpClient
	return c
}

func (c *Client) WithEnvironment(env Environment) *Client {
	c.Environment = defaultEnvironment(env)
	return c
}

func (c *Client) WithDemoHostProfile(profile DemoHostProfile) *Client {
	c.DemoHostProfile = defaultDemoHostProfile(profile)
	return c
}

func (c *Client) WithBaseURL(baseURL string) *Client {
	c.BaseURL = baseURL
	return c
}

func (c *Client) restBaseURL() (string, error) {
	if c.BaseURL != "" {
		return c.BaseURL, nil
	}
	endpoints, err := DefaultEndpointURLs(c.Environment, c.DemoHostProfile)
	if err != nil {
		return "", err
	}
	return endpoints.REST, nil
}

// Do executes a generic HTTP request and returns the raw response body.
// It handles authentication signatures if auth is required.
func (c *Client) Do(ctx context.Context, method Method, path string, payload interface{}, auth bool) ([]byte, error) {
	var bodyReader io.Reader
	var bodyString string

	if payload != nil {
		jsonBytes, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal payload: %w", err)
		}
		bodyReader = bytes.NewBuffer(jsonBytes)
		bodyString = string(jsonBytes)
	}

	base, err := c.restBaseURL()
	if err != nil {
		return nil, err
	}
	fullURL := base + path
	req, err := http.NewRequestWithContext(ctx, string(method), fullURL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if defaultEnvironment(c.Environment) == Simulated {
		req.Header.Set("x-simulated-trading", "1")
	}

	if auth {
		if c.Signer == nil {
			return nil, fmt.Errorf("credentials required")
		}
		c.Signer.SignRequest(req, string(method), path, bodyString, c.ApiKey, c.Passphrase)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode >= 400 {
		if resp.StatusCode == http.StatusTooManyRequests {
			return nil, sdkcore.NewExchangeError("OKX", "429", strings.TrimSpace(string(data)), sdkcore.ErrRateLimited)
		}
		return nil, fmt.Errorf("http error %d: %s", resp.StatusCode, string(data))
	}

	return data, nil
}

// Request parses the response into a specific type T.
func Request[T any](c *Client, ctx context.Context, method Method, path string, payload interface{}, auth bool) ([]T, error) {
	data, err := c.Do(ctx, method, path, payload, auth)
	if err != nil {
		return nil, err
	}

	var baseResp BaseResponse[T]
	if err := json.Unmarshal(data, &baseResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	if baseResp.Code != "0" {
		if baseResp.Code == "50011" || baseResp.Code == "50061" {
			return nil, sdkcore.NewExchangeError("OKX", baseResp.Code, baseResp.Message, sdkcore.ErrRateLimited)
		}
		var details string
		if len(baseResp.Data) > 0 {
			if data, err := json.Marshal(baseResp.Data); err == nil {
				details = string(data)
			}
		}
		return nil, &APIError{
			Code:    baseResp.Code,
			Message: baseResp.Message,
			Details: details,
		}
	}

	return baseResp.Data, nil
}

func applySDKRequestOpts(payload map[string]any, opts sdkcore.RequestOpts) {
	if opts.RecvWindowMillis > 0 {
		payload["recvWindow"] = opts.RecvWindowMillis
	}
	if opts.ClientRequestID != "" {
		payload["clOrdId"] = opts.ClientRequestID
	}
}
