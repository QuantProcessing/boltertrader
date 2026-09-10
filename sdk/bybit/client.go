package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/QuantProcessing/boltertrader/internal/venuehttp"
	sdkcore "github.com/QuantProcessing/boltertrader/sdk"
)

const defaultBaseURL = "https://api.bybit.com"

type Client struct {
	baseURL    string
	httpClient *http.Client
	apiKey     string
	secretKey  string
	recvWindow string
}

func NewClient() *Client {
	return &Client{
		baseURL:    defaultBaseURL,
		recvWindow: defaultRecvWindow,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// WithRecvWindowMillis configures the validity window signed into private REST
// requests. Non-positive values leave the current setting unchanged; a new
// client starts with Bybit's 5000 ms default.
func (c *Client) WithRecvWindowMillis(millis int64) *Client {
	if millis > 0 {
		c.recvWindow = strconv.FormatInt(millis, 10)
	}
	return c
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
	c.baseURL = baseURL
	return c
}

func (c *Client) WithHTTPClient(httpClient *http.Client) *Client {
	c.httpClient = httpClient
	return c
}

func (c *Client) get(ctx context.Context, path string, query map[string]string, out any) (returnErr error) {
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

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}

	resp, err := venuehttp.DoHTTP(c.httpClient, req, "bybit")
	defer func() {
		returnErr = venuehttp.WithHTTPResponse(returnErr, resp)
	}()

	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return decodeHTTPResponse(resp, out)
}

func (c *Client) getPrivate(ctx context.Context, path string, query map[string]string, out any) (returnErr error) {
	if !c.HasCredentials() {
		return fmt.Errorf("bybit sdk: credentials required")
	}

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

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	c.signHeaders(req, u.RawQuery, "")

	resp, err := venuehttp.DoHTTP(c.httpClient, req, "bybit")
	defer func() {
		returnErr = venuehttp.WithHTTPResponse(returnErr, resp)
	}()

	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return decodeHTTPResponse(resp, out)
}

func (c *Client) postPrivate(ctx context.Context, path string, body any, out any) (returnErr error) {
	if !c.HasCredentials() {
		return fmt.Errorf("bybit sdk: credentials required")
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c.signHeaders(req, "", string(payload))

	resp, err := venuehttp.DoHTTP(c.httpClient, req, "bybit")
	defer func() {
		returnErr = venuehttp.WithHTTPResponse(returnErr, resp)
	}()

	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return decodeHTTPResponse(resp, out)
}

// GetPrivateRaw executes an authenticated GET request for official Bybit V5
// endpoints that are SDK-covered by the raw fallback before a typed method is
// introduced.
func (c *Client) GetPrivateRaw(ctx context.Context, path string, query map[string]string, out any) error {
	return c.getPrivate(ctx, path, query, out)
}

// PostPrivateRaw executes an authenticated POST request for official Bybit V5
// endpoints that are SDK-covered by the raw fallback before a typed method is
// introduced.
func (c *Client) PostPrivateRaw(ctx context.Context, path string, body any, out any) error {
	return c.postPrivate(ctx, path, body, out)
}

func applySDKRequestOptsString(params map[string]string, opts sdkcore.RequestOpts) {
	if opts.RecvWindowMillis > 0 {
		params["recvWindow"] = fmt.Sprintf("%d", opts.RecvWindowMillis)
	}
	if opts.ClientRequestID != "" {
		params["orderLinkId"] = opts.ClientRequestID
	}
}

func decodeHTTPResponse(response *http.Response, out any) (returnErr error) {
	defer func() {
		var failure *venuehttp.HTTPError
		var native *ResponseError
		if errors.As(returnErr, &failure) && errors.As(returnErr, &native) {
			failure.Code = strconv.Itoa(native.Code)
			if native.Code == 10006 {
				if millis, err := strconv.ParseInt(response.Header.Get("X-Bapi-Limit-Reset-Timestamp"), 10, 64); err == nil {
					failure.RetryAfter = max(failure.RetryAfter, time.Until(time.UnixMilli(millis)))
				}
			}
		}
	}()
	body, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return venuehttp.WithHTTPResponse(err, response)
	}
	var envelope struct {
		Code    *int   `json:"retCode"`
		Message string `json:"retMsg"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return venuehttp.WithHTTPResponse(fmt.Errorf("%w: invalid JSON", venuehttp.ErrMalformedResponse), response)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return venuehttp.WithHTTPResponse(err, response)
	}
	if envelope.Code == nil {
		return venuehttp.WithHTTPResponse(fmt.Errorf("%w: missing retCode", venuehttp.ErrMalformedResponse), response)
	}
	if *envelope.Code != 0 {
		return venuehttp.WithHTTPResponse(newResponseError("HTTP response", *envelope.Code, envelope.Message), response)
	}
	if response.StatusCode >= 300 {
		return venuehttp.WithHTTPResponse(fmt.Errorf("venue HTTP request failed"), response)
	}
	return json.Unmarshal(body, out)
}
