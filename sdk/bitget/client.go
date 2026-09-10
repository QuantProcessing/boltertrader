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
	"time"

	"github.com/QuantProcessing/boltertrader/internal/venuehttp"
	sdkcore "github.com/QuantProcessing/boltertrader/sdk"
)

const (
	defaultBaseURL = "https://api.bitget.com"
	publicWSURL    = "wss://ws.bitget.com/v3/ws/public"
	privateWSURL   = "wss://ws.bitget.com/v3/ws/private"
	classicWSURL   = "wss://ws.bitget.com/v2/ws/private"
)

type Client struct {
	baseURL    string
	httpClient *http.Client
	apiKey     string
	secretKey  string
	passphrase string
}

func NewClient() *Client {
	return &Client{
		baseURL: defaultBaseURL,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

func (c *Client) WithCredentials(apiKey, secretKey, passphrase string) *Client {
	c.apiKey = apiKey
	c.secretKey = secretKey
	c.passphrase = passphrase
	return c
}

func (c *Client) WithBaseURL(baseURL string) *Client {
	c.baseURL = baseURL
	return c
}

func (c *Client) WithHTTPClient(httpClient *http.Client) *Client {
	c.httpClient = httpClient
	return c
}

func (c *Client) HasCredentials() bool {
	return c.apiKey != "" && c.secretKey != "" && c.passphrase != ""
}

type responseEnvelope[T any] struct {
	Code        string `json:"code"`
	Msg         string `json:"msg"`
	RequestTime int64  `json:"requestTime"`
	Data        T      `json:"data"`
}

func (c *Client) get(ctx context.Context, path string, query map[string]string, out any) error {
	return c.getInternal(ctx, path, query, false, out)
}

func (c *Client) getPrivate(ctx context.Context, path string, query map[string]string, out any) error {
	if !c.HasCredentials() {
		return fmt.Errorf("bitget sdk: credentials required")
	}
	return c.getInternal(ctx, path, query, true, out)
}

func (c *Client) getInternal(ctx context.Context, path string, query map[string]string, signed bool, out any) (returnErr error) {
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
	if signed {
		c.signHeaders(req, u.RawQuery, "")
	}

	resp, err := venuehttp.DoHTTP(c.httpClient, req, "bitget")
	defer func() {
		returnErr = venuehttp.WithHTTPResponse(returnErr, resp)
	}()

	if err != nil {
		return newTransportError(http.MethodGet, u.Path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return venuehttp.WithHTTPResponse(newHTTPStatusErrorWithBody(http.MethodGet, u.EscapedPath(), resp.StatusCode, bodyBytes), resp)
	}

	return decodeHTTPResponse(resp, out)
}

func (c *Client) postPrivate(ctx context.Context, path string, body any, out any) (returnErr error) {
	if !c.HasCredentials() {
		return fmt.Errorf("bitget sdk: credentials required")
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	c.signHeaders(req, "", string(payload))

	resp, err := venuehttp.DoHTTP(c.httpClient, req, "bitget")
	defer func() {
		returnErr = venuehttp.WithHTTPResponse(returnErr, resp)
	}()

	if err != nil {
		return newTransportError(http.MethodPost, req.URL.Path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return venuehttp.WithHTTPResponse(newHTTPStatusErrorWithBody(http.MethodPost, req.URL.EscapedPath(), resp.StatusCode, bodyBytes), resp)
	}

	return decodeHTTPResponse(resp, out)
}

// GetPrivateRaw executes an authenticated GET request for official Bitget
// endpoints that are SDK-covered by the raw fallback before a typed method is
// introduced.
func (c *Client) GetPrivateRaw(ctx context.Context, path string, query map[string]string, out any) error {
	return c.getPrivate(ctx, path, query, out)
}

// PostPrivateRaw executes an authenticated POST request for official Bitget
// endpoints that are SDK-covered by the raw fallback before a typed method is
// introduced.
func (c *Client) PostPrivateRaw(ctx context.Context, path string, body any, out any) error {
	return c.postPrivate(ctx, path, body, out)
}

func newHTTPStatusError(method, path string, statusCode, responseBytes int) error {
	message := fmt.Sprintf("bitget sdk: %s %s returned %d (response_bytes=%d)", method, path, statusCode, responseBytes)
	if statusCode == http.StatusTooManyRequests {
		return fmt.Errorf("%s: %w", message, sdkcore.ErrRateLimited)
	}
	return errors.New(message)
}

func newHTTPStatusErrorWithBody(method, path string, statusCode int, body []byte) error {
	if statusCode == http.StatusTooManyRequests {
		return newHTTPStatusError(method, path, statusCode, len(body))
	}
	var envelope struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(body, &envelope) == nil && envelope.Code != "" {
		return &ResponseError{
			Operation: fmt.Sprintf("%s %s returned %d (response_bytes=%d)", method, path, statusCode, len(body)),
			Code:      envelope.Code,
			Message:   "venue rejected request",
		}
	}
	return newHTTPStatusError(method, path, statusCode, len(body))
}

func newTransportError(method, path string, err error) error {
	return fmt.Errorf("bitget sdk: %s %s transport failed: %w", method, path, transportCause(err))
}

type redactedTransportCause struct {
	cause error
}

func (e *redactedTransportCause) Error() string { return "transport failure" }

func (e *redactedTransportCause) Unwrap() error { return e.cause }

func transportCause(err error) error {
	if err == nil {
		return errors.New("transport failure")
	}
	return &redactedTransportCause{cause: err}
}

func applySDKRequestOptsString(params map[string]string, opts sdkcore.RequestOpts) {
	if opts.RecvWindowMillis > 0 {
		params["recvWindow"] = fmt.Sprintf("%d", opts.RecvWindowMillis)
	}
	if opts.ClientRequestID != "" {
		params["clientOid"] = opts.ClientRequestID
	}
}

func decodeHTTPResponse(response *http.Response, out any) (returnErr error) {
	defer func() {
		var failure *venuehttp.HTTPError
		var native *ResponseError
		if errors.As(returnErr, &failure) && errors.As(returnErr, &native) {
			failure.Code = native.Code
		}
	}()
	body, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return venuehttp.WithHTTPResponse(err, response)
	}
	var envelope struct {
		Code    string `json:"code"`
		Message string `json:"msg"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return venuehttp.WithHTTPResponse(fmt.Errorf("%w: invalid JSON", venuehttp.ErrMalformedResponse), response)
	}
	if envelope.Code == "" {
		return venuehttp.WithHTTPResponse(venuehttp.ErrMalformedResponse, response)
	}
	if envelope.Code != "00000" {
		return venuehttp.WithHTTPResponse(newResponseError("HTTP response", envelope.Code, envelope.Message), response)
	}
	if response.StatusCode >= 300 {
		return venuehttp.WithHTTPResponse(fmt.Errorf("venue HTTP request failed"), response)
	}
	return json.Unmarshal(body, out)
}
