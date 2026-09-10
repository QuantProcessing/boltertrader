package venuehttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/internal/errs"
)

// HTTPError retains response metadata without rendering headers, query strings or bodies.
// The native error remains available through errors.As for venue-specific decisions.
type HTTPError struct {
	StatusCode int
	Method     string
	Path       string
	Code       string
	RetryAfter time.Duration
	Err        error
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("venue HTTP failure (method=%s path=%s status=%d code=%s)", e.Method, e.Path, e.StatusCode, e.Code)
}
func (e *HTTPError) Unwrap() error { return e.Err }
func (e *HTTPError) Is(target error) bool {
	return target == ErrRateLimited && (e.StatusCode == http.StatusTooManyRequests || e.StatusCode == http.StatusTeapot)
}

func WithHTTPResponse(err error, response *http.Response) error {
	if err == nil || response == nil {
		return err
	}
	var existing *HTTPError
	if errors.As(err, &existing) {
		return err
	}
	failure := &HTTPError{StatusCode: response.StatusCode, RetryAfter: RetryAfter(response.Header, time.Now()), Err: err}
	if response.Request != nil {
		failure.Method = response.Request.Method
		if response.Request.URL != nil {
			failure.Path = response.Request.URL.EscapedPath()
		}
	}
	return failure
}

// RetryAfter accepts both RFC Retry-After forms. Venue-specific reset timestamps
// are interpreted by the corresponding SDK, not as generic HTTP headers.
func RetryAfter(header http.Header, now time.Time) time.Duration {
	value := strings.TrimSpace(header.Get("Retry-After"))
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds <= 0 {
			return 0
		}
		const maxSeconds = int64((1<<63 - 1) / time.Second)
		if seconds > maxSeconds {
			return time.Duration(1<<63 - 1)
		}
		return time.Duration(seconds) * time.Second
	}
	if until, err := http.ParseTime(value); err == nil && until.After(now) {
		return until.Sub(now)
	}
	return 0
}

type requestVenueKey struct{}

// DoHTTP carries the SDK's venue identity to an injected transport. It performs
// exactly one HTTP client call and does not retry an exchange operation.
func DoHTTP(client *http.Client, request *http.Request, venue string) (*http.Response, error) {
	if request == nil {
		return nil, errors.New("SDK HTTP request is required")
	}
	if client == nil {
		client = http.DefaultClient
	}
	ctx := context.WithValue(request.Context(), requestVenueKey{}, venue)
	request = request.WithContext(ctx)
	response, err := client.Do(request)
	if response != nil && response.Request == nil {
		response.Request = request
	}
	if err != nil {
		err = &httpTransportError{cause: err}
	}
	return response, err
}
func RequestVenue(request *http.Request) string {
	if request == nil {
		return ""
	}
	venue, _ := request.Context().Value(requestVenueKey{}).(string)
	return venue
}

// LocalRateLimitError means the HTTP transport did not send this request.
// Action describes that specific request; a blocked follow-up read must never
// be mistaken for proof that the preceding order was not sent.
type LocalRateLimitError struct {
	RetryAfter time.Duration
	Action     string
}

func (e *LocalRateLimitError) Error() string        { return "local request budget exhausted" }
func (e *LocalRateLimitError) Is(target error) bool { return target == ErrRateLimited }

func responseCode(body []byte) string {
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil {
		return ""
	}
	for _, key := range []string{"retCode", "code", "error_code", "label"} {
		raw := fields[key]
		if len(raw) == 0 {
			continue
		}
		var value string
		if json.Unmarshal(raw, &value) != nil {
			value = string(raw)
		}
		if len(value) == 0 || len(value) > 64 || value == "0" {
			continue
		}
		valid := true
		for _, r := range value {
			if !(r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r == '_' || r == '-') {
				valid = false
				break
			}
		}
		if valid {
			return value
		}
	}
	return ""
}

// CheckHTTPStatus is for endpoints without a native HTTP error decoder.
func CheckHTTPStatus(response *http.Response) error {
	if response.StatusCode < 400 {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	failure := WithHTTPResponse(errors.New("venue HTTP request failed"), response).(*HTTPError)
	failure.Code = responseCode(body)
	return failure
}

// SharedHTTPBudget identifies an injected process-wide limiter. Standalone SDK
// clients keep their own limiter unless their owner supplies this capability.
type SharedHTTPBudget interface {
	http.RoundTripper
	HasSharedHTTPBudget() bool
}

func UsesSharedHTTPBudget(client *http.Client) bool {
	if client == nil {
		return false
	}
	transport, ok := client.Transport.(SharedHTTPBudget)
	return ok && transport.HasSharedHTTPBudget()
}

type httpTransportError struct{ cause error }

func (*httpTransportError) Error() string   { return "venue HTTP transport failure" }
func (e *httpTransportError) Unwrap() error { return e.cause }

var ErrMalformedResponse = errors.New("malformed venue response")

var ErrRateLimited = errs.ErrRateLimited
