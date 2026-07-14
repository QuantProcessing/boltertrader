package hyperliquid

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	sdkcore "github.com/QuantProcessing/boltertrader/sdk"
	"github.com/ethereum/go-ethereum/crypto"
)

const (
	httpErrorStatusCode = 400
)

type Client struct {
	Logger  *zap.SugaredLogger
	Debug   bool
	BaseURL string
	Env     Environment
	Http    *http.Client

	PrivateKey  *ecdsa.PrivateKey
	Vault       string
	AccountAddr string

	LastNonce    atomic.Int64
	ExpiresAfter *int64
}

func NewClient() *Client {
	env := EnvironmentMainnet
	return &Client{
		BaseURL: restURLForEnvironment(env),
		Env:     env,
		Http:    http.DefaultClient,
		Logger:  zap.NewNop().Sugar().Named("hyperliquid-rest"),
	}
}

func (c *Client) WithEnvironment(env Environment) *Client {
	c.Env = normalizeEnvironment(env)
	c.BaseURL = restURLForEnvironment(c.Env)
	return c
}

func (c *Client) IsMainnet() bool {
	return normalizeEnvironment(c.Env) == EnvironmentMainnet
}

func (c *Client) SignL1Action(action any, nonce int64) (SignatureResult, error) {
	return SignL1Action(c.PrivateKey, action, c.Vault, nonce, c.ExpiresAfter, c.IsMainnet())
}

func parsePrivateKey(privateKey string) (*ecdsa.PrivateKey, error) {
	return crypto.HexToECDSA(strings.TrimPrefix(strings.TrimSpace(privateKey), "0x"))
}

func (c *Client) WithCredentials(privateKey string, vault *string) *Client {
	if privateKey != "" {
		pk, err := parsePrivateKey(privateKey)
		if err == nil {
			c.PrivateKey = pk
			// If account address is not set, derive it from private key
			if c.AccountAddr == "" {
				c.AccountAddr = crypto.PubkeyToAddress(c.PrivateKey.PublicKey).Hex()
			}
		} else {
			// Log error or ignore? Ideally return error but signature is fluent.
			// Just safe guard against nil dereference.
			if c.Logger != nil {
				c.Logger.Errorw("Invalid private key", "error", err)
			}
		}
	}
	if vault != nil {
		c.Vault = *vault
	}
	return c
}

func (c *Client) WithAccount(accountAddr string) *Client {
	c.AccountAddr = accountAddr
	return c
}

func (c *Client) GetNextNonce() int64 {
	for {
		last := c.LastNonce.Load()
		candidate := time.Now().UnixMilli()

		if candidate <= last {
			candidate = last + 1
		}
		if c.LastNonce.CompareAndSwap(last, candidate) {
			return candidate
		}
	}
}

func (c *Client) PostAction(ctx context.Context, action any, sig SignatureResult, nonce int64) ([]byte, error) {
	payload := map[string]any{
		"action":    action,
		"nonce":     nonce,
		"signature": sig,
	}
	if c.Vault != "" {
		if actionMap, ok := action.(map[string]any); ok {
			if actionMap["type"] == "usdClassTransfer" {
				actionMap["vaultAddress"] = c.Vault
			} else {
				payload["vaultAddress"] = nil
			}
		} else {
			payload["vaultAddress"] = c.Vault
		}
	}

	if c.ExpiresAfter != nil {
		payload["expiresAfter"] = *c.ExpiresAfter
	}

	return c.Post(ctx, "/exchange", payload)
}

func (c *Client) Post(ctx context.Context, path string, payload any) ([]byte, error) {
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal payload %w", err)
	}

	url := c.BaseURL + path
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		url,
		bytes.NewBuffer(jsonData),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	c.Logger.Debugw("request", "method", req.Method, "path", req.URL.Path, "body_bytes", len(jsonData))

	resp, err := c.Http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	// c.Logger.Debugw("response", "status", resp.Status, "body", string(data))

	if resp.StatusCode >= httpErrorStatusCode {
		if !json.Valid(data) {
			return nil, fmt.Errorf("invalid json response: %s", string(data))
		}
		var apiErr APIError
		if err := json.Unmarshal(data, &apiErr); err != nil {
			return nil, fmt.Errorf("failed to unmarshal error response: %w", err)
		}
		if isRateLimitError(apiErr) {
			return nil, sdkcore.NewExchangeError("HYPERLIQUID", fmt.Sprintf("%d", apiErr.Code), apiErr.Message, sdkcore.ErrRateLimited)
		}
		return nil, &apiErr
	}

	return data, nil
}

func isRateLimitError(err APIError) bool {
	if err.Code == http.StatusTooManyRequests {
		return true
	}
	return strings.Contains(strings.ToLower(err.Message), "rate limit")
}

func (c *Client) GetUserFees(ctx context.Context) (*UserFees, error) {
	data, err := c.Post(ctx, "/info", map[string]string{
		"type": "userFees",
		"user": c.AccountAddr,
	})
	if err != nil {
		return nil, err
	}
	var res UserFees
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

func applySDKRequestOpts(payload map[string]any, opts sdkcore.RequestOpts) {
	if opts.RecvWindowMillis > 0 {
		payload["expiresAfter"] = time.Now().Add(time.Duration(opts.RecvWindowMillis) * time.Millisecond).UnixMilli()
	}
	if opts.ClientRequestID != "" {
		payload["clientRequestId"] = opts.ClientRequestID
	}
}
