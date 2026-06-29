package sdk

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

const defaultBaseURL = "https://api.backpack.exchange"

type Client struct {
	baseURL    string
	httpClient *http.Client
	apiKey     string
	privateKey string
}

func NewClient() *Client {
	return &Client{
		baseURL: defaultBaseURL,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

func (c *Client) WithCredentials(apiKey, privateKey string) *Client {
	c.apiKey = apiKey
	c.privateKey = privateKey
	return c
}

func (c *Client) get(ctx context.Context, path string, query map[string]string, out any) error {
	u, err := url.Parse(c.baseURL + path)
	if err != nil {
		return err
	}
	q := u.Query()
	for key, value := range query {
		if value != "" {
			q.Set(key, value)
		}
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("backpack sdk: GET %s returned %s", path, resp.Status)
	}

	return json.NewDecoder(resp.Body).Decode(out)
}
