package standx

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	sdkcore "github.com/QuantProcessing/boltertrader/sdk"
)

const (
	BaseURL     = "https://perps.standx.com"
	AuthBaseURL = "https://api.standx.com" // Auth endpoints use a different base URL
)

// Client handles Standx API communication
type Client struct {
	baseURL       string
	authBaseURL   string
	chain         string
	walletAddress string
	httpClient    *http.Client
	logger        *zap.SugaredLogger
	signer        *Signer

	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

// NewClient creates a new Standx API client
func NewClient() *Client {
	logger := zap.NewNop().Sugar().Named("standx-api")
	return &Client{
		baseURL:     BaseURL,
		authBaseURL: AuthBaseURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		logger: logger,
		chain:  "bsc",
	}
}

// WithCredentials sets the EVM private key for authentication
func (c *Client) WithCredentials(evmPrivateKeyHex string) (*Client, error) {
	signer, err := NewSigner(evmPrivateKeyHex)
	if err != nil {
		return nil, err
	}
	c.walletAddress = signer.evmAddress
	c.signer = signer
	return c, nil
}

// Login performs the full authentication flow
func (c *Client) Login(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.signer == nil {
		return fmt.Errorf("credentials not set")
	}

	// Use derived address if matched or default
	derived := c.signer.GetEVMAddress()
	if c.walletAddress != "" && !strings.EqualFold(c.walletAddress, derived) {
		c.logger.Warnf("Provided wallet address %s does not differ from credentials address %s", c.walletAddress, derived)
	}
	// Force use derived address to ensure signature validity
	useAddress := derived

	// 1. Prepare SignIn
	signedDataJWT, messageToSign, err := c.prepareSignIn(ctx, c.chain, useAddress)
	if err != nil {
		return fmt.Errorf("prepare sign-in failed: %w", err)
	}

	// Debug Log Message and Address
	c.logger.Debugf("SIWE Message for %s:\n%s", useAddress, messageToSign)

	// 2. Sign with Wallet (EVM Personal Sign)
	signature, err := c.signer.SignEVMPersonal(messageToSign)
	if err != nil {
		return fmt.Errorf("wallet signing failed: %w", err)
	}

	// 3. Login
	token, err := c.login(ctx, c.chain, signature, signedDataJWT)
	if err != nil {
		return fmt.Errorf("login failed: %w", err)
	}

	c.token = token
	c.logger.Info("Standx login successful")
	return nil
}

func (c *Client) GetSigner() *Signer {
	return c.signer
}

func (c *Client) GetToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	token := c.token
	expiresAt := c.expiresAt
	c.mu.Unlock()

	// if token is empty or expiring soon, login/refresh
	if token == "" || time.Now().Add(time.Hour*24).After(expiresAt) {
		if err := c.Login(ctx); err != nil {
			return "", err
		}

		// Return the new token after login
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.token, nil
	}

	return token, nil
}

// InvalidateToken clears the cached token, forcing a new login on next GetToken
func (c *Client) InvalidateToken() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token = ""
	c.expiresAt = time.Time{}
}

func (c *Client) prepareSignIn(ctx context.Context, chain, address string) (string, string, error) {
	reqBody := PrepareSignInRequest{
		Address:   address,
		RequestID: c.signer.requestID,
	}
	resp, err := c.doAuthRequest(ctx, http.MethodPost, "/v1/offchain/prepare-signin?chain="+chain, reqBody)
	if err != nil {
		return "", "", err
	}

	var result PrepareSignInResponse
	if err := json.Unmarshal(resp, &result); err != nil {
		return "", "", err
	}
	if !result.Success {
		return "", "", fmt.Errorf("prepare-signin returned unsuccessful")
	}

	// Parse JWT to extract the message to sign
	claims, err := parseJWT(result.SignedData)
	if err != nil {
		return "", "", fmt.Errorf("failed to parse signedData JWT: %w", err)
	}

	return result.SignedData, claims.Message, nil
}

func (c *Client) login(ctx context.Context, chain, signature, signedData string) (string, error) {
	reqBody := LoginRequest{
		Signature:      signature,
		SignedData:     signedData,
		ExpiresSeconds: 604800, // 7 days
	}
	c.expiresAt = time.Now().Add(time.Hour * 24 * 7)
	resp, err := c.doAuthRequest(ctx, http.MethodPost, "/v1/offchain/login?chain="+chain, reqBody)
	if err != nil {
		return "", err
	}

	var result LoginResponse
	if err := json.Unmarshal(resp, &result); err != nil {
		return "", err
	}
	return result.Token, nil
}

// Helper to parse JWT payload without verifying signature (we trust the server's pre-sign response)
func parseJWT(tokenString string) (*SignedDataPayload, error) {
	parts := bytes.Split([]byte(tokenString), []byte("."))
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid jwt format")
	}
	// Add padding if needed for base64 decoding
	payloadBytes, err := base64Decode(parts[1])
	if err != nil {
		return nil, err
	}

	var claims SignedDataPayload
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return nil, err
	}
	return &claims, nil
}

// base64Decode handles standard and URL-safe base64
func base64Decode(data []byte) ([]byte, error) {
	l := len(data) % 4
	if l > 0 {
		data = append(data, bytes.Repeat([]byte("="), 4-l)...)
	}
	// Replace URL safe chars just in case
	data = bytes.ReplaceAll(data, []byte("-"), []byte("+"))
	data = bytes.ReplaceAll(data, []byte("_"), []byte("/"))

	dst := make([]byte, base64.StdEncoding.DecodedLen(len(data)))
	n, err := base64.StdEncoding.Decode(dst, data)
	if err != nil {
		return nil, err
	}
	return dst[:n], nil
}

// doAuthRequest handles requests to the Auth API (api.standx.com)
func (c *Client) doAuthRequest(ctx context.Context, method, endpoint string, payload interface{}) ([]byte, error) {
	return c.do(ctx, c.authBaseURL, method, endpoint, payload, false, false, nil)
}

// DoRequest handles requests to the Perps API (perps.standx.com)
func (c *Client) DoPublic(ctx context.Context, method, endpoint string, params url.Values, result interface{}) error {
	url := endpoint
	if len(params) > 0 {
		url += "?" + params.Encode()
	}
	resp, err := c.do(ctx, c.baseURL, method, url, nil, false, false, nil)
	if err != nil {
		return err
	}
	return json.Unmarshal(resp, result)
}

// DoPrivate handles authenticated requests
func (c *Client) DoPrivate(ctx context.Context, method, endpoint string, payload interface{}, result interface{}, signBody bool, extraHeaders map[string]string) error {
	resp, err := c.do(ctx, c.baseURL, method, endpoint, payload, true, signBody, extraHeaders)
	if err != nil {
		return err
	}
	return json.Unmarshal(resp, result)
}

func (c *Client) do(ctx context.Context, baseURL, method, endpoint string, payload interface{}, auth, signBody bool, extraHeaders map[string]string) ([]byte, error) {
	var body io.Reader
	var bodyStr string

	if payload != nil {
		jsonBytes, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewBuffer(jsonBytes)
		bodyStr = string(jsonBytes)
	}

	fullURL := baseURL + endpoint
	req, err := http.NewRequestWithContext(ctx, method, fullURL, body)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")

	if auth {
		if c.token == "" {
			return nil, fmt.Errorf("access token missing, please login first")
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	if signBody && c.signer != nil {
		// x-request-signature headers
		// Standx requires signing the body for trade requests
		timestamp := time.Now().UnixMilli()

		reqID := ""
		if v, ok := extraHeaders["x-request-id"]; ok {
			reqID = v
		}

		headers := c.signer.SignRequest(bodyStr, timestamp, reqID)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
	}

	// Inject Extra Headers
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= 400 {
		if resp.StatusCode == http.StatusTooManyRequests {
			return nil, sdkcore.NewExchangeError("STANDX", "429", strings.TrimSpace(string(respBytes)), sdkcore.ErrRateLimited)
		}
		return nil, fmt.Errorf("API error: status=%d body=%s", resp.StatusCode, string(respBytes))
	}

	// Check for "code" != 0 in JSON response if structure matches APIResponse
	// Some auth endpoints return different structures, so we only check if we expect APIResponse.
	// However, since we return []byte here, we defer parsing.

	return respBytes, nil
}
