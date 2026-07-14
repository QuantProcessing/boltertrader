package grvt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	sdkcore "github.com/QuantProcessing/boltertrader/sdk"
)

type Client struct {
	ApiKey       string
	SubAccountID string
	PrivateKey   string
	ChainID      string

	EdgeURL       string
	TradeDataURL  string
	MarketDataURL string

	HttpClient *http.Client

	// Auth State
	cookie    *http.Cookie
	accountID string // This is the Main Account ID returned from login
	mu        sync.RWMutex
}

func NewClient() *Client {
	c := &Client{
		HttpClient:    &http.Client{Timeout: 10 * time.Second},
		EdgeURL:       EdgeURL,
		TradeDataURL:  TradeDataURL,
		MarketDataURL: MarketDataURL,
		ChainID:       ChainID,
	}

	return c
}

func (c *Client) WithCredentials(apiKey, subAccountID, privateKey string) *Client {
	c.ApiKey = apiKey
	c.SubAccountID = subAccountID
	c.PrivateKey = privateKey
	return c
}

func (c *Client) Login(ctx context.Context) error {
	if c.ApiKey == "" {
		return fmt.Errorf("credentials required")
	}
	reqBody := LoginRequest{ApiKey: c.ApiKey}
	data, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.EdgeURL+"/auth/api_key/login", bytes.NewBuffer(data))
	if err != nil {
		return newGRVTRedactedError("build login request failed", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HttpClient.Do(req)
	if err != nil {
		return newGRVTRedactedError("login request transport failed", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		message := fmt.Sprintf("login failed: %s (response bytes: %d)", grvtHTTPStatus(resp.StatusCode), len(body))
		if resp.StatusCode == http.StatusTooManyRequests {
			return sdkcore.NewExchangeError("GRVT", "429", message, sdkcore.ErrRateLimited)
		}
		return errors.New(message)
	}

	// Extract Cookie
	cookies := resp.Cookies()
	for _, cookie := range cookies {
		if cookie.Name == "gravity" {
			c.mu.Lock()
			c.cookie = cookie
			c.mu.Unlock()
			break
		}
	}

	// Extract Account ID
	c.mu.Lock()
	c.accountID = resp.Header.Get("X-Grvt-Account-Id")
	c.mu.Unlock()

	if c.cookie == nil {
		return fmt.Errorf("login successful but no gravity cookie found")
	}

	return nil
}

func (c *Client) Post(ctx context.Context, url string, payload interface{}, signed bool) ([]byte, error) {
	// Auto-login if needed
	if signed {
		c.mu.RLock()
		cookie := c.cookie
		c.mu.RUnlock()

		// Simple check: if no cookie, login.
		// TODO: Check expiration
		if cookie == nil {
			if err := c.Login(ctx); err != nil {
				return nil, fmt.Errorf("auto-login failed: %w", err)
			}
		}
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(data))
	if err != nil {
		return nil, newGRVTRedactedError("build request failed", err)
	}
	req.Header.Set("Content-Type", "application/json")

	if signed {
		c.mu.RLock()
		if c.cookie != nil {
			req.AddCookie(c.cookie)
		}
		if c.accountID != "" {
			req.Header.Set("X-Grvt-Account-Id", c.accountID)
		}
		c.mu.RUnlock()
	}

	resp, err := c.HttpClient.Do(req)
	if err != nil {
		return nil, newGRVTRedactedError("request transport failed", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != 200 {
		message := fmt.Sprintf("request rejected: %s (response bytes: %d)", grvtHTTPStatus(resp.StatusCode), len(body))
		var grvtErr GrvtError
		if err := json.Unmarshal(body, &grvtErr); err == nil && grvtErr.Code != 0 {
			if resp.StatusCode == http.StatusTooManyRequests || grvtErr.Code == 1006 {
				return nil, sdkcore.NewExchangeError("GRVT", fmt.Sprintf("%d", grvtErr.Code), message, sdkcore.ErrRateLimited)
			}
			grvtErr.Message = message
			return nil, &grvtErr
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			return nil, sdkcore.NewExchangeError("GRVT", "429", message, sdkcore.ErrRateLimited)
		}
		return nil, errors.New(message)
	}

	return body, nil
}

func grvtHTTPStatus(statusCode int) string {
	if statusText := http.StatusText(statusCode); statusText != "" {
		return fmt.Sprintf("%d %s", statusCode, statusText)
	}
	return fmt.Sprintf("%d", statusCode)
}

type grvtRedactedError struct {
	message string
	cause   error
}

func (e *grvtRedactedError) Error() string {
	return e.message
}

func (e *grvtRedactedError) Unwrap() error {
	return e.cause
}

func newGRVTRedactedError(message string, cause error) error {
	return &grvtRedactedError{message: message, cause: cause}
}
