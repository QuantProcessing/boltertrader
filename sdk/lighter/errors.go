package lighter

import "errors"

// Common error types
var (
	ErrInvalidSignature  = errors.New("invalid signature")
	ErrOrderNotFound     = errors.New("order not found")
	ErrOrderRejected     = errors.New("order rejected")
	ErrWSOutcomeUnknown  = errors.New("websocket transaction outcome is unknown")
	ErrMalformedResponse = errors.New("malformed response")
)
