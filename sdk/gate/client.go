package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultRESTBaseURL = "https://api.gateio.ws/api/v4"

type Client struct {
	baseURL    string
	httpClient *http.Client
	apiKey     string
	secretKey  string
	now        func() time.Time
}

func NewClient() *Client {
	return &Client{
		baseURL: defaultRESTBaseURL,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
		now: time.Now,
	}
}

func (c *Client) WithCredentials(apiKey, secretKey string) *Client {
	c.apiKey = apiKey
	c.secretKey = secretKey
	return c
}

func (c *Client) HasCredentials() bool {
	return c.apiKey != "" && c.secretKey != ""
}

func (c *Client) WithBaseURL(baseURL string) *Client {
	c.baseURL = strings.TrimRight(baseURL, "/")
	return c
}

func (c *Client) WithHTTPClient(httpClient *http.Client) *Client {
	c.httpClient = httpClient
	return c
}

func (c *Client) WithClock(now func() time.Time) *Client {
	if now != nil {
		c.now = now
	}
	return c
}

func (c *Client) get(ctx context.Context, path string, query map[string]string, out any) error {
	return c.do(ctx, http.MethodGet, path, query, nil, false, out)
}

func (c *Client) getPrivate(ctx context.Context, path string, query map[string]string, out any) error {
	if !c.HasCredentials() {
		return fmt.Errorf("gate sdk: credentials required")
	}
	return c.do(ctx, http.MethodGet, path, query, nil, true, out)
}

func (c *Client) postPrivate(ctx context.Context, path string, body any, out any) error {
	if !c.HasCredentials() {
		return fmt.Errorf("gate sdk: credentials required")
	}
	return c.do(ctx, http.MethodPost, path, nil, body, true, out)
}

func (c *Client) deletePrivate(ctx context.Context, path string, query map[string]string, out any) error {
	if !c.HasCredentials() {
		return fmt.Errorf("gate sdk: credentials required")
	}
	return c.do(ctx, http.MethodDelete, path, query, nil, true, out)
}

func (c *Client) do(ctx context.Context, method, path string, query map[string]string, body any, signed bool, out any) error {
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

	var bodyString string
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		bodyString = string(payload)
	}

	var reader io.Reader
	if bodyString != "" {
		reader = bytes.NewBufferString(bodyString)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
	if err != nil {
		return err
	}
	if signed {
		c.signHeaders(req, u.RawQuery, bodyString)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return newAPIError(resp.StatusCode, method, path, bodyBytes)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) GetPrivateRaw(ctx context.Context, path string, query map[string]string, out any) error {
	return c.getPrivate(ctx, path, query, out)
}

func (c *Client) PostPrivateRaw(ctx context.Context, path string, body any, out any) error {
	return c.postPrivate(ctx, path, body, out)
}

func (c *Client) DeletePrivateRaw(ctx context.Context, path string, query map[string]string, out any) error {
	return c.deletePrivate(ctx, path, query, out)
}

type APIError struct {
	StatusCode int
	Method     string
	Path       string
	Label      string
	Message    string
	Body       string
}

func (e *APIError) Error() string {
	if e.Label != "" || e.Message != "" {
		return fmt.Sprintf("gate sdk: %s %s returned %d: %s %s", e.Method, e.Path, e.StatusCode, e.Label, e.Message)
	}
	return fmt.Sprintf("gate sdk: %s %s returned %d: %s", e.Method, e.Path, e.StatusCode, e.Body)
}

func newAPIError(statusCode int, method, path string, body []byte) error {
	apiErr := &APIError{
		StatusCode: statusCode,
		Method:     method,
		Path:       path,
		Body:       string(body),
	}
	var payload struct {
		Label   string `json:"label"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &payload); err == nil {
		apiErr.Label = payload.Label
		apiErr.Message = payload.Message
	}
	return apiErr
}
