package nado

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

	sdkcore "github.com/QuantProcessing/boltertrader/sdk"
)

const (
	GatewayV1URL = "https://gateway.prod.nado.xyz/v1"
	GatewayV2URL = "https://gateway.prod.nado.xyz/v2"
	ArchiveV1URL = "https://archive.prod.nado.xyz/v1"
	ArchiveV2URL = "https://archive.prod.nado.xyz/v2"
)

type Client struct {
	gatewayV1URL string
	gatewayV2URL string
	archiveV1URL string
	archiveV2URL string
	client       *http.Client
	privateKey   string
	address      string
	subaccount   string
	Signer       *Signer
}

func NewClient() *Client {
	return &Client{
		gatewayV1URL: GatewayV1URL,
		gatewayV2URL: GatewayV2URL,
		archiveV1URL: ArchiveV1URL,
		archiveV2URL: ArchiveV2URL,
		client:       &http.Client{Timeout: 10 * time.Second},
	}
}

// WithArchiveV1URL overrides the archive V1 base URL. Intended for tests only.
func (c *Client) WithArchiveV1URL(u string) *Client {
	c.archiveV1URL = u
	return c
}

func (c *Client) WithCredentials(privateKey, subaccount string) (*Client, error) {
	signer, err := NewSigner(privateKey)
	if err != nil {
		return nil, err
	}
	c.privateKey = privateKey
	c.subaccount = subaccount
	c.Signer = signer
	c.address = signer.GetAddress().String()
	return c, nil
}

// Execute sends a POST request (V1) for execution/transaction endpoints.
func (c *Client) Execute(ctx context.Context, reqBody interface{}) ([]byte, error) {
	if c.privateKey == "" {
		return nil, ErrCredentialsRequired
	}
	var bodyReader io.Reader
	if reqBody != nil {
		jsonBytes, err := json.Marshal(reqBody)
		if err != nil {
			return nil, fmt.Errorf("marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(jsonBytes)
	}

	u := c.gatewayV1URL + "/execute"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("perform request: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	if resp.StatusCode >= 400 {
		if resp.StatusCode == http.StatusTooManyRequests {
			return nil, sdkcore.NewExchangeError("NADO", "429", strings.TrimSpace(string(respBytes)), sdkcore.ErrRateLimited)
		}
		return nil, fmt.Errorf("api error (status %d): %s", resp.StatusCode, string(respBytes))
	}

	var apiResp ApiV1Response
	if err := json.Unmarshal(respBytes, &apiResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	if apiResp.Status != "success" || apiResp.Error != "" {
		return nil, fmt.Errorf("api v1 error: %d %s", apiResp.ErrorCode, apiResp.Error)
	}

	return apiResp.Data, nil
}

// QueryV1 v1 endpoints api: support get and post method
func (c *Client) QueryGateWayV1(ctx context.Context, method string, req map[string]interface{}) ([]byte, error) {
	var (
		data []byte
	)
	switch method {
	case http.MethodGet:
		// get request: url.Values
		u, err := url.Parse(c.gatewayV1URL + "/query")
		if err != nil {
			return nil, err
		}
		q := url.Values{}
		for k, v := range req {
			q.Set(k, fmt.Sprintf("%v", v))
		}
		u.RawQuery = q.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, err
		}
		resp, err := c.client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		data, err = io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode >= 400 {
			if resp.StatusCode == http.StatusTooManyRequests {
				return nil, sdkcore.NewExchangeError("NADO", "429", strings.TrimSpace(string(data)), sdkcore.ErrRateLimited)
			}
			return nil, fmt.Errorf("api v1 error (status %d): %s", resp.StatusCode, string(data))
		}
	case http.MethodPost:
		// post request: json
		jsonBytes, err := json.Marshal(req)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.gatewayV1URL+"/query", bytes.NewReader(jsonBytes))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		data, err = io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode >= 400 {
			if resp.StatusCode == http.StatusTooManyRequests {
				return nil, sdkcore.NewExchangeError("NADO", "429", strings.TrimSpace(string(data)), sdkcore.ErrRateLimited)
			}
			return nil, fmt.Errorf("api v1 error (status %d): %s", resp.StatusCode, string(data))
		}
	default:
		return nil, fmt.Errorf("unsupported method: %s", method)
	}

	var apiResp ApiV1Response
	if err := json.Unmarshal(data, &apiResp); err != nil {
		return nil, fmt.Errorf("unmarshal response v1: %w", err)
	}

	if apiResp.Status != "success" || apiResp.Error != "" {
		return nil, fmt.Errorf("api v1 error: %d %s", apiResp.ErrorCode, apiResp.Error)
	}

	return apiResp.Data, nil
}

// QueryGatewayV2 v2 endpoints api: only support get method
func (c *Client) QueryGatewayV2(ctx context.Context, path string, params url.Values, dest interface{}) error {
	u, err := url.Parse(c.gatewayV2URL + path)
	if err != nil {
		return err
	}
	if params != nil {
		u.RawQuery = params.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response v2: %w", err)
	}

	if resp.StatusCode >= 400 {
		if resp.StatusCode == http.StatusTooManyRequests {
			return fmt.Errorf("rate limited: %w", sdkcore.NewExchangeError("NADO", "429", strings.TrimSpace(string(respBytes)), sdkcore.ErrRateLimited))
		}
		return fmt.Errorf("api v2 error (status %d): %s", resp.StatusCode, string(respBytes))
	}

	if err := json.Unmarshal(respBytes, dest); err != nil {
		return fmt.Errorf("unmarshal response v2: %w", err)
	}
	return nil
}

// QueryArchiveV1 v1 endpoints api: only support get method
func (c *Client) QueryArchiveV1(ctx context.Context, params interface{}) (data []byte, err error) {
	jsonBytes, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.archiveV1URL, bytes.NewReader(jsonBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		if resp.StatusCode == http.StatusTooManyRequests {
			return nil, sdkcore.NewExchangeError("NADO", "429", strings.TrimSpace(string(data)), sdkcore.ErrRateLimited)
		}
		return nil, fmt.Errorf("api v1 error (status %d): %s", resp.StatusCode, string(data))
	}
	return data, nil
}

// QueryArchiveV2 v2 endpoints api: only support get method
func (c *Client) QueryArchiveV2(ctx context.Context, path string, params url.Values, dest interface{}) error {
	u, err := url.Parse(c.archiveV2URL + path)
	if err != nil {
		return err
	}
	if params != nil {
		u.RawQuery = params.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response v2: %w", err)
	}

	if resp.StatusCode >= 400 {
		if resp.StatusCode == http.StatusTooManyRequests {
			return fmt.Errorf("rate limited: %w", sdkcore.NewExchangeError("NADO", "429", strings.TrimSpace(string(respBytes)), sdkcore.ErrRateLimited))
		}
		return fmt.Errorf("api v2 error (status %d): %s", resp.StatusCode, string(respBytes))
	}

	if err := json.Unmarshal(respBytes, dest); err != nil {
		return fmt.Errorf("unmarshal response v2: %w", err)
	}
	return nil
}
