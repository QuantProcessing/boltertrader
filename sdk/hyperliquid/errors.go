package hyperliquid

import (
	"errors"
	"fmt"
	"strings"
)

//go:generate easyjson -all

type APIError struct {
	Code    int    `json:"code"`
	Message string `json:"msg"`
	Data    any    `json:"data,omitempty"`
}

func (e APIError) Error() string {
	return fmt.Sprintf("API error %d: %s", e.Code, e.Message)
}

type ValidationError struct {
	Field   string
	Message string
}

func (e ValidationError) Error() string {
	return fmt.Sprintf("validation error on field %s: %s", e.Field, e.Message)
}

// IsWalletDoesNotExistError checks if the error is a "wallet does not exist" error from the API.
func IsWalletDoesNotExistError(err error) bool {
	if err == nil {
		return false
	}
	errMsg := strings.ToLower(err.Error())
	return strings.Contains(errMsg, "does not exist") &&
		(strings.Contains(errMsg, "wallet") || strings.Contains(errMsg, "user"))
}

var (
	// ErrOrderNotFound marks an authoritative orderStatus "unknownOid" result.
	ErrOrderNotFound = errors.New("hyperliquid: order not found")
	// ErrOrderRejected marks an explicit per-order error returned by the exchange.
	ErrOrderRejected       = errors.New("hyperliquid: order rejected")
	ErrCredentialsRequired = errors.New("credentials required")
)

// OrderRejectedError preserves the exchange-provided rejection reason while
// remaining classifiable with errors.Is(err, ErrOrderRejected).
type OrderRejectedError struct {
	Reason string
}

func (e *OrderRejectedError) Error() string {
	if e == nil || e.Reason == "" {
		return ErrOrderRejected.Error()
	}
	return fmt.Sprintf("%s: %s", ErrOrderRejected, e.Reason)
}

func (e *OrderRejectedError) Unwrap() error { return ErrOrderRejected }
