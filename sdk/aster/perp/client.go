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
	astercommon "github.com/QuantProcessing/boltertrader/sdk/aster/common"
)

type Client struct {
	HTTPClient *http.Client
	Logger     *zap.SugaredLogger
	Debug      bool

	UsedWeight mbx.UsedWeight
	OrderCount mbx.OrderCount

	profile  astercommon.Profile
	security *astercommon.SecurityContext
}

func NewClient(profile astercommon.Profile, security *astercommon.SecurityContext) (*Client, error) {
	if profile.Product() != astercommon.ProductPerp {
		return nil, fmt.Errorf("aster perp client: profile product is %q", profile.Product())
	}
	if err := profile.Validate(); err != nil {
		return nil, err
	}
	httpClient := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: rejectPerpRedirect,
	}
	l := zap.NewNop().Sugar().Named("aster-perp")

	// Check for proxy in environment
	proxyURL := os.Getenv("PROXY")

	if proxyURL != "" {
		parsedURL, err := url.Parse(proxyURL)
		if err == nil {
			httpClient.Transport = &http.Transport{
				Proxy: http.ProxyURL(parsedURL),
			}
		} else {
			l.Warn("Invalid proxy URL")
		}
	}

	return &Client{
		HTTPClient: httpClient,
		Logger:     l,
		Debug:      os.Getenv("DEBUG") == "true" || os.Getenv("DEBUG") == "1",
		profile:    profile,
		security:   security,
	}, nil
}

func (c *Client) WithHTTPClient(httpClient *http.Client) *Client {
	if httpClient != nil {
		clone := *httpClient
		clone.CheckRedirect = rejectPerpRedirect
		c.HTTPClient = &clone
	}
	return c
}

func (c *Client) Profile() astercommon.Profile { return c.profile }

func rejectPerpRedirect(_ *http.Request, _ []*http.Request) error {
	return fmt.Errorf("aster perp client: redirects are disabled")
}

func (c *Client) WithDebug(debug bool) *Client {
	c.Debug = debug
	return c
}

func (c *Client) call(ctx context.Context, method, endpoint string, params map[string]interface{}, signed bool, result interface{}) error {
	u, err := url.Parse(c.profile.RESTURL() + endpoint)
	if err != nil {
		return err
	}

	q := u.Query()
	for k, v := range params {
		if k == "symbol" {
			symbol, ok := v.(string)
			if !ok {
				return fmt.Errorf("aster perp client: symbol must be a string")
			}
			normalized, err := astercommon.NormalizeSymbol(c.profile, symbol)
			if err != nil {
				return err
			}
			q.Add(k, normalized)
			continue
		}
		q.Add(k, fmt.Sprintf("%v", v))
	}

	if signed {
		signedParams, err := c.security.Sign(c.profile, q)
		if err != nil {
			return err
		}
		u.RawQuery = signedParams.Encode()
	} else {
		u.RawQuery = q.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, method, u.String(), nil)
	if err != nil {
		return astercommon.NewTransportError(method, endpoint, err)
	}

	if c.Debug {
		c.Logger.Debugw("Request", "method", method, "url", astercommon.RedactURL(u.String()))
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return astercommon.NewTransportError(method, endpoint, err)
	}
	defer resp.Body.Close()

	c.UsedWeight.UpdateByHeader(resp.Header)
	c.OrderCount.UpdateByHeader(resp.Header)

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if c.Debug {
		c.Logger.Debugw("Response", "status", resp.StatusCode, "bytes", len(data))
	}

	if resp.StatusCode >= 400 {
		var apiErr APIError
		if err := json.Unmarshal(data, &apiErr); err != nil {
			return astercommon.NewVenueError(resp.StatusCode, method, endpoint, 0, http.StatusText(resp.StatusCode))
		}
		message := astercommon.SanitizeVenueMessage(apiErr.Message)
		if rlErr := mbx.MapAPIError("ASTER", resp.StatusCode, nil, func([]byte) (int, string, error) {
			return apiErr.Code, message, nil
		}); rlErr != nil {
			return rlErr
		}
		return astercommon.NewVenueError(resp.StatusCode, method, endpoint, apiErr.Code, message)
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
