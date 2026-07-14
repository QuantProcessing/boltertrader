// Package mbx provides shared rate-limit tracking and error mapping for
// Binance-family exchanges (Binance, Aster) that use the X-Mbx-* header
// convention for communicating request weight and order count usage.
package mbx

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"

	exchanges "github.com/QuantProcessing/boltertrader/internal/errs"
)

type redactedTransportCause struct {
	cause error
}

func (e *redactedTransportCause) Error() string { return "transport failure" }

func (e *redactedTransportCause) Unwrap() error { return e.cause }

// TransportCause returns a safe-rendering wrapper around the complete
// transport error tree. The fixed Error text prevents arbitrary cause strings
// (including signed URLs) from being rendered, while Unwrap preserves the
// original tree for errors.Is and errors.As classification.
func TransportCause(err error) error {
	if err == nil {
		return nil
	}
	return &redactedTransportCause{cause: err}
}

// UsedWeight tracks the cumulative request weight as reported by the
// exchange via X-Mbx-Used-Weight and X-Mbx-Used-Weight-1m headers.
type UsedWeight struct {
	Used   int64
	Used1M int64
}

// UpdateByHeader reads X-Mbx-Used-Weight headers and atomically stores them.
func (u *UsedWeight) UpdateByHeader(header http.Header) {
	if value := header.Get("X-Mbx-Used-Weight"); value != "" {
		if used, err := strconv.ParseInt(value, 10, 64); err == nil {
			atomic.StoreInt64(&u.Used, used)
		}
	}
	if value := header.Get("X-Mbx-Used-Weight-1m"); value != "" {
		if used, err := strconv.ParseInt(value, 10, 64); err == nil {
			atomic.StoreInt64(&u.Used1M, used)
		}
	}
}

// OrderCount tracks the order rate as reported by the exchange via
// X-Mbx-Order-Count-10s and X-Mbx-Order-Count-1d headers.
type OrderCount struct {
	Count10s int64
	Count1d  int64
}

// UpdateByHeader reads X-Mbx-Order-Count headers and atomically stores them.
func (o *OrderCount) UpdateByHeader(header http.Header) {
	if value := header.Get("X-Mbx-Order-Count-10s"); value != "" {
		if count, err := strconv.ParseInt(value, 10, 64); err == nil {
			atomic.StoreInt64(&o.Count10s, count)
		}
	}
	if value := header.Get("X-Mbx-Order-Count-1d"); value != "" {
		if count, err := strconv.ParseInt(value, 10, 64); err == nil {
			atomic.StoreInt64(&o.Count1d, count)
		}
	}
}

// MapAPIError parses a Binance-family API error response and wraps rate-limit
// errors as exchanges.ErrRateLimited. The apiErr parameter should be a pointer
// to a struct with Code (int) and Message (string) fields populated from the
// JSON response body. Returns nil if no error mapping was needed — caller
// should fall through to returning apiErr directly.
func MapAPIError(exchange string, statusCode int, data []byte, unmarshalErr func([]byte) (int, string, error)) error {
	code, message, err := unmarshalErr(data)
	if err != nil {
		return fmt.Errorf("http error %d: %s", statusCode, string(data))
	}
	if statusCode == http.StatusTooManyRequests || statusCode == http.StatusTeapot || IsRateLimitMessage(message) || code == -1003 || code == -1015 {
		return exchanges.NewExchangeError(exchange, fmt.Sprintf("%d", code), message, exchanges.ErrRateLimited)
	}
	return nil // Caller should return the original apiErr
}

// IsRateLimitMessage checks if the error message suggests rate limiting.
func IsRateLimitMessage(message string) bool {
	lower := strings.ToLower(message)
	return strings.Contains(lower, "request weight") ||
		strings.Contains(lower, "too many requests") ||
		strings.Contains(lower, "banned until")
}

// APIErrorFields is a helper that extracts Code and Message from a standard
// Binance-family JSON error response: {"code": -1003, "msg": "..."}.
func UnmarshalAPIError(data []byte) (code int, message string, err error) {
	var raw struct {
		Code    int    `json:"code"`
		Message string `json:"msg"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return 0, "", err
	}
	return raw.Code, raw.Message, nil
}
