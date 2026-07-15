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
	"strings"
	"time"
)

const defaultRESTBaseURL = "https://api.gateio.ws/api/v4"

type Client struct {
	baseURL    string
	httpClient *http.Client
	apiKey     string
	secretKey  string
	now        func() time.Time
}

func NewClient() *Client {
	return &Client{
		baseURL: defaultRESTBaseURL,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
		now: time.Now,
	}
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
	c.baseURL = strings.TrimRight(baseURL, "/")
	return c
}

func (c *Client) WithHTTPClient(httpClient *http.Client) *Client {
	c.httpClient = httpClient
	return c
}

func (c *Client) WithClock(now func() time.Time) *Client {
	if now != nil {
		c.now = now
	}
	return c
}

func (c *Client) get(ctx context.Context, path string, query map[string]string, out any) error {
	return c.do(ctx, http.MethodGet, path, query, nil, false, out)
}

func (c *Client) getPrivate(ctx context.Context, path string, query map[string]string, out any) error {
	if !c.HasCredentials() {
		return fmt.Errorf("gate sdk: credentials required")
	}
	return c.do(ctx, http.MethodGet, path, query, nil, true, out)
}

func (c *Client) postPrivate(ctx context.Context, path string, body any, out any) error {
	if !c.HasCredentials() {
		return fmt.Errorf("gate sdk: credentials required")
	}
	return c.do(ctx, http.MethodPost, path, nil, body, true, out)
}

func (c *Client) deletePrivate(ctx context.Context, path string, query map[string]string, out any) error {
	if !c.HasCredentials() {
		return fmt.Errorf("gate sdk: credentials required")
	}
	return c.do(ctx, http.MethodDelete, path, query, nil, true, out)
}

func (c *Client) do(ctx context.Context, method, path string, query map[string]string, body any, signed bool, out any) error {
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

	var bodyString string
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		bodyString = string(payload)
	}

	var reader io.Reader
	if bodyString != "" {
		reader = bytes.NewBufferString(bodyString)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
	if err != nil {
		return err
	}
	if signed {
		c.signHeaders(req, u.RawQuery, bodyString)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return newAPIError(resp.StatusCode, method, path, bodyBytes)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) GetPrivateRaw(ctx context.Context, path string, query map[string]string, out any) error {
	return c.getPrivate(ctx, path, query, out)
}

func (c *Client) PostPrivateRaw(ctx context.Context, path string, body any, out any) error {
	return c.postPrivate(ctx, path, body, out)
}

func (c *Client) DeletePrivateRaw(ctx context.Context, path string, query map[string]string, out any) error {
	return c.deletePrivate(ctx, path, query, out)
}

type APIError struct {
	StatusCode int
	Method     string
	Path       string
	Label      string
	Message    string
	Body       string
}

func (e *APIError) Error() string {
	if e.Label != "" || e.Message != "" {
		return fmt.Sprintf("gate sdk: %s %s returned %d: %s %s", e.Method, e.Path, e.StatusCode, e.Label, e.Message)
	}
	return fmt.Sprintf("gate sdk: %s %s returned %d: %s", e.Method, e.Path, e.StatusCode, e.Body)
}

// IsDefinitiveCommandRejection reports whether err proves that Gate received a
// command and rejected it for a structured, non-retryable application reason.
// Transport failures, unstructured HTTP responses, throttling, timeouts, and
// server failures deliberately remain ambiguous.
func IsDefinitiveCommandRejection(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr == nil || apiErr.Label == "" {
		return false
	}
	if apiErr.StatusCode < http.StatusBadRequest || apiErr.StatusCode >= http.StatusInternalServerError {
		return false
	}
	switch apiErr.StatusCode {
	case http.StatusRequestTimeout, http.StatusTooEarly, http.StatusTooManyRequests:
		return false
	}
	return isDocumentedDefinitiveCommandRejectionLabel(strings.ToUpper(apiErr.Label))
}

// isDocumentedDefinitiveCommandRejectionLabel intentionally uses an
// allowlist. Gate can add new labels without changing the HTTP status; an
// unknown label therefore cannot prove that a submitted command was rejected.
// Duplicate-request labels are also omitted because they can indicate that an
// earlier attempt was accepted and still requires identity-based recovery.
func isDocumentedDefinitiveCommandRejectionLabel(label string) bool {
	switch label {
	case "INVALID_PARAM_VALUE",
		"INVALID_PROTOCOL",
		"INVALID_ARGUMENT",
		"INVALID_REQUEST_BODY",
		"MISSING_REQUIRED_PARAM",
		"BAD_REQUEST",
		"INVALID_CONTENT_TYPE",
		"NOT_ACCEPTABLE",
		"METHOD_NOT_ALLOWED",
		"NOT_FOUND",
		"INVALID_CREDENTIALS",
		"INVALID_KEY",
		"IP_FORBIDDEN",
		"READ_ONLY",
		"INVALID_SIGNATURE",
		"MISSING_REQUIRED_HEADER",
		"REQUEST_EXPIRED",
		"ACCOUNT_LOCKED",
		"FORBIDDEN",
		"INVALID_CLIENT_ORDER_ID",
		"INVALID_PRECISION",
		"INVALID_CURRENCY",
		"INVALID_CURRENCY_PAIR",
		"POC_FILL_IMMEDIATELY",
		"ORDER_NOT_FOUND",
		"ORDER_CLOSED",
		"ORDER_CANCELLED",
		"QUANTITY_NOT_ENOUGH",
		"BALANCE_NOT_ENOUGH",
		"MARGIN_NOT_SUPPORTED",
		"MARGIN_BALANCE_NOT_ENOUGH",
		"AMOUNT_TOO_LITTLE",
		"AMOUNT_TOO_MUCH",
		"TOO_MANY_CURRENCY_PAIRS",
		"TOO_MANY_ORDERS",
		"MIXED_ACCOUNT_TYPE",
		"AUTO_BORROW_TOO_MUCH",
		"TRADE_RESTRICTED",
		"FOK_NOT_FILL",
		"INITIAL_MARGIN_TOO_LOW",
		"ORDER_BOOK_NOT_FOUND",
		"USER_NOT_FOUND",
		"CONTRACT_NO_COUNTER",
		"CONTRACT_NOT_FOUND",
		"RISK_LIMIT_EXCEEDED",
		"INSUFFICIENT_AVAILABLE",
		"LIQUIDATE_IMMEDIATELY",
		"LEVERAGE_TOO_HIGH",
		"LEVERAGE_TOO_LOW",
		"ORDER_NOT_OWNED",
		"ORDER_FINISHED",
		"POSITION_CROSS_MARGIN",
		"POSITION_IN_LIQUIDATION",
		"POSITION_IN_CLOSE",
		"POSITION_EMPTY",
		"REMOVE_TOO_MUCH",
		"RISK_LIMIT_NOT_MULTIPLE",
		"RISK_LIMIT_TOO_HIGH",
		"RISK_LIMIT_TOO_LOW",
		"PRICE_TOO_DEVIATED",
		"SIZE_TOO_LARGE",
		"SIZE_TOO_SMALL",
		"PRICE_OVER_LIQUIDATION",
		"PRICE_OVER_BANKRUPT",
		"ORDER_POC_IMMEDIATE",
		"INCREASE_POSITION",
		"CONTRACT_IN_DELISTING",
		"POSITION_NOT_FOUND",
		"POSITION_DUAL_MODE",
		"ORDER_PENDING",
		"POSITION_HOLDING",
		"REDUCE_EXCEEDED",
		"NO_CHANGE",
		"AMEND_WITH_STOP",
		"ORDER_FOK":
		return true
	default:
		return false
	}
}

func newAPIError(statusCode int, method, path string, body []byte) error {
	apiErr := &APIError{
		StatusCode: statusCode,
		Method:     method,
		Path:       path,
		Body:       string(body),
	}
	var payload struct {
		Label   string `json:"label"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &payload); err == nil {
		apiErr.Label = payload.Label
		apiErr.Message = payload.Message
	}
	return apiErr
}
