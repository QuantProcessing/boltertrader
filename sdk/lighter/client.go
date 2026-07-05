package lighter

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	sdkcore "github.com/QuantProcessing/boltertrader/sdk"
	"github.com/QuantProcessing/boltertrader/sdk/lighter/common"
)

const (
	MainnetAPIURL  = "https://mainnet.zklighter.elliot.ai"
	TestnetAPIURL  = "https://testnet.zklighter.elliot.ai"
	ExplorerAPIURL = "https://explorer.elliot.ai/api"
)

type Environment string

const (
	EnvironmentMainnet Environment = "mainnet"
	EnvironmentTestnet Environment = "testnet"
)

type Client struct {
	BaseURL    string
	HTTPClient *http.Client
	Logger     *zap.SugaredLogger

	PrivateKey   string
	AccountIndex int64
	KeyIndex     uint8
	ChainId      uint32
	KeyManager   common.KeyManager

	nonce     int64
	nonceMu   sync.Mutex
	nonceInit bool
}

func NewClient() *Client {
	return &Client{
		BaseURL:    MainnetAPIURL,
		HTTPClient: http.DefaultClient,
		Logger:     zap.NewNop().Sugar().Named("lighter-rest"),
		ChainId:    MainnetChainID,
	}
}

func (c *Client) WithEnvironment(env Environment) *Client {
	switch env {
	case EnvironmentTestnet:
		c.BaseURL = TestnetAPIURL
		c.ChainId = TestnetChainID
	default:
		c.BaseURL = MainnetAPIURL
		c.ChainId = MainnetChainID
	}
	return c
}

func (c *Client) WithBaseURL(baseURL string) *Client {
	if baseURL != "" {
		c.BaseURL = baseURL
	}
	return c
}

func (c *Client) WithCredentials(privateKey string, accountIndex int64, keyIndex uint8) *Client {
	if len(privateKey) >= 2 && privateKey[:2] == "0x" {
		privateKey = privateKey[2:]
	}
	b, err := hex.DecodeString(privateKey)
	if err != nil {
		c.Logger.Errorw("invalid private key", "error", err)
		return c
	}
	keyManager, err := common.NewKeyManager(b)
	if err != nil {
		c.Logger.Errorw("invalid key manager", "error", err)
		return c
	}

	c.PrivateKey = privateKey
	c.AccountIndex = accountIndex
	c.KeyIndex = keyIndex
	c.KeyManager = keyManager
	return c
}

// InvalidateNonce clears the local nonce cache so the next write request
// refreshes from the exchange after a rejected transaction.
func (c *Client) InvalidateNonce() {
	c.nonceMu.Lock()
	defer c.nonceMu.Unlock()
	c.nonceInit = false
}

func (c *Client) Post(ctx context.Context, path string, payload any, auth bool) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		jsonData, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal payload: %w", err)
		}
		body = bytes.NewBuffer(jsonData)
		c.Logger.Debugw("Request Body", "body", string(jsonData))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	if auth {
		token, err := c.CreateAuthToken(time.Now().Add(time.Minute * 10))
		if err != nil {
			return nil, fmt.Errorf("failed to create auth token: %w", err)
		}
		req.Header.Set("Authorization", token)
	}

	c.Logger.Debugw("Request", "method", req.Method, "url", req.URL)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	c.Logger.Debugw("Response", "status", resp.Status, "body", string(data))

	if resp.StatusCode >= 400 {
		if resp.StatusCode == http.StatusTooManyRequests {
			return nil, sdkcore.NewExchangeError("LIGHTER", "429", strings.TrimSpace(string(data)), sdkcore.ErrRateLimited)
		}
		var apiErr APIError
		if err := json.Unmarshal(data, &apiErr); err == nil {
			return nil, &apiErr
		}
		return nil, fmt.Errorf("http error %d: %s", resp.StatusCode, data)
	}

	return data, nil
}

func (c *Client) CreateAuthToken(deadline time.Time) (string, error) {
	if c.KeyManager == nil {
		return "", fmt.Errorf("credentials required")
	}
	return c.KeyManager.CreateAuthToken(c.AccountIndex, c.KeyIndex, deadline)
}

// PostForm sends a multipart/form-data request
func (c *Client) PostForm(ctx context.Context, path string, params map[string]string, auth bool) ([]byte, error) {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	for key, val := range params {
		_ = writer.WriteField(key, val)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("failed to close multipart writer: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Accept", "application/json")

	if auth {
		token, err := c.CreateAuthToken(time.Now().Add(time.Minute * 10))
		if err != nil {
			return nil, fmt.Errorf("failed to create auth token: %w", err)
		}
		req.Header.Set("Authorization", token)
	}

	c.Logger.Debugw("Request", "method", req.Method, "url", req.URL)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	c.Logger.Debugw("Response", "status", resp.Status, "body", string(data))

	if resp.StatusCode >= 400 {
		var apiErr APIError
		if err := json.Unmarshal(data, &apiErr); err == nil {
			return nil, &apiErr
		}
		return nil, fmt.Errorf("http error %d: %s", resp.StatusCode, data)
	}

	return data, nil
}

func (c *Client) GetBlockHeight(ctx context.Context) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ExplorerAPIURL+"/blocks", nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return 0, fmt.Errorf("http error %d: %s", resp.StatusCode, data)
	}
	var res []struct {
		BlockHeight int64 `json:"block_height"`
	}
	if err := json.Unmarshal(data, &res); err != nil {
		return 0, err
	}
	if len(res) == 0 {
		return 0, fmt.Errorf("no blocks returned")
	}
	return res[0].BlockHeight, nil
}

func applySDKRequestOptsString(params map[string]string, opts sdkcore.RequestOpts) {
	if opts.RecvWindowMillis > 0 {
		params["recv_window"] = fmt.Sprintf("%d", opts.RecvWindowMillis)
	}
	if opts.ClientRequestID != "" {
		params["client_request_id"] = opts.ClientRequestID
	}
}
