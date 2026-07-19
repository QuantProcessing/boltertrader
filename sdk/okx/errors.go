package okx

import (
	"errors"
	"fmt"
)

var (
	// ErrWSPreSend means an OKX private WebSocket command failed before the
	// financial mutation was written to the selected ready socket.
	ErrWSPreSend = errors.New("okx ws command failed before send")
	// ErrWSOutcomeUnknown means an OKX private WebSocket command was written, or
	// may have been partially written, but no authoritative mutation outcome was
	// observed.
	ErrWSOutcomeUnknown = errors.New("okx ws command outcome unknown after send")
	// ErrWSMalformedResponse means an OKX private WebSocket command response did
	// not match the documented command envelope.
	ErrWSMalformedResponse = errors.New("okx ws malformed command response")
)

type wsCommandError struct {
	sentinel error
	cause    error
	message  string
}

func (e *wsCommandError) Error() string {
	if e == nil {
		return ""
	}
	if e.message != "" {
		return e.message
	}
	if e.cause != nil {
		return fmt.Sprintf("%v: %v", e.sentinel, e.cause)
	}
	return e.sentinel.Error()
}

func (e *wsCommandError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *wsCommandError) Is(target error) bool {
	return e != nil && target == e.sentinel
}

func newWSPreSendError(cause error) error {
	return &wsCommandError{sentinel: ErrWSPreSend, cause: cause}
}

func newWSOutcomeUnknownError(cause error) error {
	return &wsCommandError{sentinel: ErrWSOutcomeUnknown, cause: cause}
}

func newWSMalformedResponseError(operation, reason string) error {
	return &wsCommandError{sentinel: ErrWSMalformedResponse, message: fmt.Sprintf("okx ws %s malformed response: %s", operation, reason)}
}

// APIError represents an error returned by the OKX API.
type APIError struct {
	Code    string `json:"code"`
	Message string `json:"msg"`
	Details string `json:"details,omitempty"`
}

func (e *APIError) Error() string {
	if e.Details != "" {
		return fmt.Sprintf("okx api error: code=%s, msg=%s, details=%s", e.Code, e.Message, e.Details)
	}
	return fmt.Sprintf("okx api error: code=%s, msg=%s", e.Code, e.Message)
}
