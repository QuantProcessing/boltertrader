// Package gateway is an application client for Gateway. It does not import
// Trading, venue adapters, SDKs, or Gateway runtime services.
package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	wire "github.com/QuantProcessing/boltertrader/api/exchange-gateway/v1"
)

const DefaultTimeout = 30 * time.Second
const DefaultResponseLimit int64 = 8 << 20

// Config uses the capability listener for normal requests. Accounts are still
// on the internal listener; InternalURL/Token are only needed for split service
// deployments. Neither credential is a venue key.
type Config struct {
	URL            string
	Token          string
	InternalURL    string
	InternalToken  string
	AllowPlaintext bool // Opt in only for an operator-controlled container network.
	HTTPClient     *http.Client
	Timeout        time.Duration
	ResponseLimit  int64
}
type Client struct {
	origin, internalOrigin string
	token, internalToken   string
	http                   *http.Client
	timeout                time.Duration
	limit                  int64
}

func New(config Config) (*Client, error) {
	origin, err := validateOrigin(config.URL, config.AllowPlaintext)
	if err != nil || !validToken(config.Token) {
		return nil, invalid("client configuration")
	}
	internalOrigin, internalToken := origin, config.Token
	if config.InternalURL != "" || config.InternalToken != "" {
		internalOrigin, err = validateOrigin(config.InternalURL, config.AllowPlaintext)
		if err != nil || !validToken(config.InternalToken) {
			return nil, invalid("internal client configuration")
		}
		internalToken = config.InternalToken
	}
	if config.Timeout < 0 || config.ResponseLimit < 0 {
		return nil, invalid("client limits")
	}
	if config.Timeout == 0 {
		config.Timeout = DefaultTimeout
	}
	if config.ResponseLimit == 0 {
		config.ResponseLimit = DefaultResponseLimit
	}
	transport := http.Client{Timeout: config.Timeout}
	if config.HTTPClient != nil {
		transport = *config.HTTPClient
	}
	// Never follow redirects carrying service credentials or converting writes.
	transport.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{origin: origin, internalOrigin: internalOrigin, token: config.Token, internalToken: internalToken, http: &transport, timeout: config.Timeout, limit: config.ResponseLimit}, nil
}
func validateOrigin(raw string, allow bool) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", invalid("Gateway origin")
	}
	ip := net.ParseIP(parsed.Hostname())
	if parsed.Scheme == "http" && !allow && parsed.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return "", invalid("plaintext Gateway origin")
	}
	return strings.TrimSuffix(parsed.String(), "/"), nil
}
func validToken(value string) bool { return len(value) > 0 && !strings.ContainsAny(value, "\r\n\t ") }

// WriteOutcome describes only the HTTP handoff, not the venue order's state.
// Unknown must be recovered by querying the same order/key, never by replacing
// the idempotency key. Retryable on a server error does not authorize a write.
type WriteOutcome string

const (
	NotAttempted WriteOutcome = "not_attempted"
	Rejected     WriteOutcome = "rejected"
	Unknown      WriteOutcome = "unknown"
)

type Error struct {
	Kind          string
	StatusCode    int
	Code          string
	Message       string
	CorrelationID string
	VenueCode     string
	Retryable     bool
	RetryAfter    time.Duration
	Outcome       WriteOutcome
	cause         error
}

func (e *Error) Error() string {
	return fmt.Sprintf("Gateway %s error (status %d)", e.Kind, e.StatusCode)
}
func (e *Error) GoString() string { return e.Error() }
func (e *Error) Unwrap() error    { return e.cause }
func invalid(field string) *Error {
	return &Error{Kind: "invalid_request", Message: field + " is invalid", Outcome: NotAttempted}
}
func protocol(field string) *Error {
	return &Error{Kind: "protocol", Message: field + " response is invalid", Outcome: Unknown}
}
func transportError(err error) *Error {
	var cause error
	if errors.Is(err, context.Canceled) {
		cause = context.Canceled
	} else if errors.Is(err, context.DeadlineExceeded) {
		cause = context.DeadlineExceeded
	} else {
		var timeout net.Error
		if errors.As(err, &timeout) && timeout.Timeout() {
			cause = context.DeadlineExceeded
		}
	}
	return &Error{Kind: "transport", Outcome: Unknown, cause: cause}
}

func (c *Client) request(ctx context.Context, method, path string, query url.Values, body any, key string, target any, status int) error {
	if err := ctx.Err(); err != nil {
		return &Error{Kind: "transport", Outcome: NotAttempted, cause: err}
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	origin, token := c.origin, c.token
	if strings.HasPrefix(path, "/internal/") {
		origin, token = c.internalOrigin, c.internalToken
	}
	rawURL := origin + path
	if len(query) > 0 {
		rawURL += "?" + query.Encode()
	}
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			return invalid("request body")
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, bytes.NewReader(data))
	if err != nil {
		return invalid("request")
	}
	// net/http may retry a POST with an Idempotency-Key on a reused connection
	// when GetBody is set. Only explicit application recovery may replay a write.
	req.GetBody = nil
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return transportError(err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, c.limit+1))
	if err != nil {
		return transportError(err)
	}
	if int64(len(raw)) > c.limit {
		return protocol("oversized")
	}
	if res.StatusCode != status {
		result := &Error{Kind: "http", StatusCode: res.StatusCode, Outcome: Unknown}
		if res.StatusCode >= 400 && res.StatusCode < 500 && res.StatusCode != 408 {
			result.Outcome = Rejected
		}
		var envelope wire.ErrorEnvelope
		if decode(raw, &envelope) == nil {
			result.Code = envelope.Error.Code
			result.Message = envelope.Error.Message
			result.CorrelationID = envelope.Error.CorrelationID
			if result.CorrelationID == "" {
				result.CorrelationID = envelope.Error.RequestID
			}
			result.VenueCode = envelope.Error.VenueCode
			result.Retryable = envelope.Error.Retryable
		}
		// Even a misconfigured proxy must not echo our own credentials to callers.
		result.Code = strings.ReplaceAll(result.Code, token, "[redacted]")
		result.Message = strings.ReplaceAll(result.Message, token, "[redacted]")
		result.CorrelationID = strings.ReplaceAll(result.CorrelationID, token, "[redacted]")
		result.VenueCode = strings.ReplaceAll(result.VenueCode, token, "[redacted]")
		if seconds, err := strconv.ParseInt(res.Header.Get("Retry-After"), 10, 32); err == nil && seconds >= 0 {
			result.RetryAfter = time.Duration(seconds) * time.Second
		}
		return result
	}
	if target != nil && decode(raw, target) != nil {
		return protocol("JSON")
	}
	return nil
}

// Unknown fields are additive. Null, trailing values, and duplicate field names
// (including case aliases accepted by encoding/json) are not compatible data.
func decode(raw []byte, target any) error {
	if !utf8.Valid(raw) || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return errors.New("invalid JSON")
	}
	scanner := json.NewDecoder(bytes.NewReader(raw))
	scanner.UseNumber()
	if err := scanValue(scanner); err != nil {
		return err
	}
	if _, err := scanner.Token(); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return json.Unmarshal(raw, target)
}
func scanValue(d *json.Decoder) error {
	token, err := d.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok {
				return errors.New("invalid key")
			}
			name = strings.ToLower(name)
			if seen[name] {
				return errors.New("duplicate key")
			}
			seen[name] = true
			if err := scanValue(d); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := scanValue(d); err != nil {
				return err
			}
		}
	default:
		return errors.New("invalid delimiter")
	}
	_, err = d.Token()
	return err
}

func (c *Client) String() string   { return "Gateway client (credentials redacted)" }
func (c *Client) GoString() string { return c.String() }
func (c Config) String() string    { return "Gateway client configuration (credentials redacted)" }
func (c Config) GoString() string  { return c.String() }
