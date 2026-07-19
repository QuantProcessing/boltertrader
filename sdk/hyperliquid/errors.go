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
	// ErrMarketReferenceUnavailable marks a failure to fetch the fresh
	// reference required before a protected market order can be signed.
	ErrMarketReferenceUnavailable = errors.New("hyperliquid: market reference unavailable")
	// ErrMarketReferenceMalformed marks missing, invalid, or unusable reference
	// data detected before an order action is signed.
	ErrMarketReferenceMalformed = errors.New("hyperliquid: malformed market reference")
	// ErrMutationOutcomeUnknown is reserved for failures after a signed action
	// may have reached the venue.
	ErrMutationOutcomeUnknown = errors.New("hyperliquid: mutation outcome unknown")
)

type MarketReferenceError struct {
	Kind  error
	Cause error
}

func NewMarketReferenceError(kind, cause error) error {
	if kind != ErrMarketReferenceUnavailable && kind != ErrMarketReferenceMalformed {
		kind = ErrMarketReferenceMalformed
	}
	return &MarketReferenceError{Kind: kind, Cause: cause}
}

func (e *MarketReferenceError) Error() string {
	if e == nil || e.Kind == nil {
		return ErrMarketReferenceMalformed.Error()
	}
	return e.Kind.Error()
}

func (e *MarketReferenceError) Unwrap() []error {
	if e == nil {
		return nil
	}
	errs := make([]error, 0, 2)
	if e.Kind != nil {
		errs = append(errs, e.Kind)
	}
	if e.Cause != nil {
		errs = append(errs, e.Cause)
	}
	return errs
}

type MutationOutcomeUnknownError struct {
	Cause error
}

func NewMutationOutcomeUnknown(cause error) error {
	return &MutationOutcomeUnknownError{Cause: cause}
}

func (e *MutationOutcomeUnknownError) Error() string {
	return ErrMutationOutcomeUnknown.Error()
}

func (e *MutationOutcomeUnknownError) Unwrap() []error {
	if e == nil {
		return nil
	}
	errs := []error{ErrMutationOutcomeUnknown}
	if e.Cause != nil {
		errs = append(errs, e.Cause)
	}
	return errs
}

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
