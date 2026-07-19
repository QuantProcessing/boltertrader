package exchange

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type ErrorKind string

const (
	KindInvalidConfig      ErrorKind = "invalid_config"
	KindInvalidRequest     ErrorKind = "invalid_request"
	KindAuthentication     ErrorKind = "authentication"
	KindPermission         ErrorKind = "permission"
	KindRateLimit          ErrorKind = "rate_limit"
	KindNotFound           ErrorKind = "not_found"
	KindVenueRejected      ErrorKind = "venue_rejected"
	KindTransport          ErrorKind = "transport"
	KindAmbiguousOutcome   ErrorKind = "ambiguous_outcome"
	KindMalformedResponse  ErrorKind = "malformed_response"
	KindCanceled           ErrorKind = "canceled"
	KindDeadlineExceeded   ErrorKind = "deadline_exceeded"
	KindSubscriptionGap    ErrorKind = "subscription_gap"
	KindSubscriptionClosed ErrorKind = "subscription_closed"
)

var (
	ErrInvalidConfig      = errorSentinel{kind: KindInvalidConfig}
	ErrInvalidRequest     = errorSentinel{kind: KindInvalidRequest}
	ErrAuthentication     = errorSentinel{kind: KindAuthentication}
	ErrPermission         = errorSentinel{kind: KindPermission}
	ErrRateLimit          = errorSentinel{kind: KindRateLimit}
	ErrNotFound           = errorSentinel{kind: KindNotFound}
	ErrVenueRejected      = errorSentinel{kind: KindVenueRejected}
	ErrTransport          = errorSentinel{kind: KindTransport}
	ErrAmbiguousOutcome   = errorSentinel{kind: KindAmbiguousOutcome}
	ErrMalformedResponse  = errorSentinel{kind: KindMalformedResponse}
	ErrCanceled           = errorSentinel{kind: KindCanceled}
	ErrDeadlineExceeded   = errorSentinel{kind: KindDeadlineExceeded}
	ErrSubscriptionGap    = errorSentinel{kind: KindSubscriptionGap}
	ErrSubscriptionClosed = errorSentinel{kind: KindSubscriptionClosed}
)

type errorSentinel struct {
	kind ErrorKind
}

func (sentinel errorSentinel) Error() string {
	return string(sentinel.kind)
}

type ErrorDetails struct {
	Venue       Venue         `json:"venue,omitempty"`
	Product     Product       `json:"product,omitempty"`
	Operation   string        `json:"operation,omitempty"`
	Code        string        `json:"code,omitempty"`
	SafeMessage string        `json:"safe_message,omitempty"`
	RetryAfter  time.Duration `json:"retry_after,omitempty"`
}

type Error struct {
	kind    ErrorKind
	details ErrorDetails
}

func NewError(kind ErrorKind, details ErrorDetails) *Error {
	return &Error{kind: kind, details: details}
}

func (err *Error) Kind() ErrorKind {
	if err == nil {
		return ""
	}
	return err.kind
}

func (err *Error) Details() ErrorDetails {
	if err == nil {
		return ErrorDetails{}
	}
	return err.details
}

func (err *Error) Error() string {
	if err == nil {
		return "<nil>"
	}
	parts := []string{string(err.kind)}
	details := err.details
	if details.Venue != "" {
		parts = append(parts, string(details.Venue))
	}
	if details.Product != "" {
		parts = append(parts, string(details.Product))
	}
	if details.Operation != "" {
		parts = append(parts, details.Operation)
	}
	if details.Code != "" {
		parts = append(parts, details.Code)
	}
	if details.SafeMessage != "" {
		parts = append(parts, details.SafeMessage)
	}
	if details.RetryAfter > 0 {
		parts = append(parts, fmt.Sprintf("retry_after=%s", details.RetryAfter))
	}
	return strings.Join(parts, ": ")
}

func (err *Error) Is(target error) bool {
	if err == nil || target == nil {
		return false
	}
	if sentinel, ok := target.(errorSentinel); ok {
		return err.kind == sentinel.kind
	}
	switch target {
	case context.Canceled:
		return err.kind == KindCanceled
	case context.DeadlineExceeded:
		return err.kind == KindDeadlineExceeded
	default:
		return false
	}
}
