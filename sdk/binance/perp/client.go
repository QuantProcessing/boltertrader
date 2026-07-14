package perp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	"go.uber.org/zap"

	"github.com/QuantProcessing/boltertrader/internal/mbx"
	sdkcore "github.com/QuantProcessing/boltertrader/sdk"
)

const (
	BaseURL      = "https://fapi.binance.com"
	CoinMBaseURL = "https://dapi.binance.com"
)

type Client struct {
	BaseURL        string
	EndpointPrefix string
	AccountVersion string
	APIKey         string
	SecretKey      string
	HTTPClient     *http.Client
	Logger         *zap.SugaredLogger

	UsedWeight  mbx.UsedWeight
	OrderCount  mbx.OrderCount
	RateLimiter *KeyedRateLimiter
}

func NewClient() *Client {
	httpClient := &http.Client{Timeout: 10 * time.Second}

	// proxy
	proxyURL := os.Getenv("PROXY")
	l := zap.NewNop().Sugar().Named("binance-perp")

	if proxyURL != "" {
		parsedURL, err := url.Parse(proxyURL)
		if err == nil {
			httpClient.Transport = &http.Transport{
				Proxy: http.ProxyURL(parsedURL),
			}
			l.Debugw("Using configured proxy")
		} else {
			l.Errorw("Invalid proxy URL")
		}
	}

	return &Client{
		BaseURL:        BaseURL,
		EndpointPrefix: "/fapi",
		AccountVersion: "v2",
		HTTPClient:     httpClient,
		Logger:         l,
		RateLimiter:    NewBinancePerpRateLimiter(),
	}
}

func NewCoinMClient() *Client {
	return NewClient().WithCoinM()
}

func NewDemoClient() *Client {
	return NewClient().WithUSDMMDemo()
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

func (c *Client) WithEndpointProfile(profile EndpointProfile) *Client {
	if profile.RESTBaseURL != "" {
		c.BaseURL = profile.RESTBaseURL
	}
	if profile.EndpointPrefix != "" {
		c.EndpointPrefix = profile.EndpointPrefix
	}
	if profile.AccountVersion != "" {
		c.AccountVersion = profile.AccountVersion
	}
	return c
}

func (c *Client) WithUSDMMDemo() *Client {
	return c.WithEndpointProfile(USDMMDemoEndpoints)
}

func (c *Client) WithCoinM() *Client {
	c.BaseURL = CoinMBaseURL
	c.EndpointPrefix = "/dapi"
	c.AccountVersion = "v1"
	return c
}

func (c *Client) WithHTTPClient(httpClient *http.Client) *Client {
	c.HTTPClient = httpClient
	return c
}

func (c *Client) WithRateLimiter(limiter *KeyedRateLimiter) *Client {
	c.RateLimiter = limiter
	return c
}

func (c *Client) rateLimitKeys(endpoint string) []string {
	return []string{binancePerpRateLimitKey(endpoint), binanceRateLimiterGlobalKey}
}

func (c *Client) apiV1(path string) string {
	return c.endpoint("v1", path)
}

func (c *Client) accountEndpoint(path string) string {
	version := c.AccountVersion
	if version == "" {
		version = "v2"
	}
	return c.endpoint(version, path)
}

func (c *Client) endpoint(version string, path string) string {
	prefix := c.EndpointPrefix
	if prefix == "" {
		prefix = "/fapi"
	}
	if path != "" && path[0] != '/' {
		path = "/" + path
	}
	return prefix + "/" + version + path
}

func (c *Client) call(ctx context.Context, method, endpoint string, params map[string]interface{}, signed bool, result interface{}) error {
	if c.RateLimiter != nil {
		if err := c.RateLimiter.Wait(ctx, c.rateLimitKeys(endpoint)); err != nil {
			return err
		}
	}

	u, err := url.Parse(c.BaseURL + endpoint)
	if err != nil {
		return err
	}

	q := u.Query()
	for k, v := range params {
		q.Add(k, fmt.Sprintf("%v", v))
	}

	if signed {
		q.Add("timestamp", fmt.Sprintf("%d", Timestamp()))
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

	c.Logger.Debugw("Request", "method", method, "path", u.Path)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("binance perp %s %s transport failed: %w", method, u.Path, mbx.TransportCause(err))
	}
	defer resp.Body.Close()

	c.UsedWeight.UpdateByHeader(resp.Header)
	c.OrderCount.UpdateByHeader(resp.Header)

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	c.Logger.Debugw("Response", "status", resp.StatusCode, "bytes", len(data))

	if resp.StatusCode >= 400 {
		if rlErr := mbx.MapAPIError("BINANCE", resp.StatusCode, data, func(d []byte) (int, string, error) {
			var apiErr APIError
			if err := json.Unmarshal(d, &apiErr); err != nil {
				return 0, "", err
			}
			return apiErr.Code, apiErr.Message, nil
		}); rlErr != nil {
			return rlErr
		}
		// Not a rate-limit error — return as generic APIError
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

func (c *Client) Get(ctx context.Context, endpoint string, params map[string]interface{}, signed bool, result interface{}) error {
	return c.call(ctx, http.MethodGet, endpoint, params, signed, result)
}

func (c *Client) Post(ctx context.Context, endpoint string, params map[string]interface{}, signed bool, result interface{}) error {
	return c.call(ctx, http.MethodPost, endpoint, params, signed, result)
}

func (c *Client) Delete(ctx context.Context, endpoint string, params map[string]interface{}, signed bool, result interface{}) error {
	return c.call(ctx, http.MethodDelete, endpoint, params, signed, result)
}

func (c *Client) Put(ctx context.Context, endpoint string, params map[string]interface{}, signed bool, result interface{}) error {
	return c.call(ctx, http.MethodPut, endpoint, params, signed, result)
}

func applySDKRequestOpts(params map[string]interface{}, opts sdkcore.RequestOpts) {
	if opts.RecvWindowMillis > 0 {
		params["recvWindow"] = opts.RecvWindowMillis
	}
	if opts.ClientRequestID != "" {
		params["newClientOrderId"] = opts.ClientRequestID
	}
}
