package nado

import (
	"errors"
	"fmt"
	"strings"
)

var (
	ErrInvalidSymbol       = errors.New("nado: invalid symbol")
	ErrInvalidSide         = errors.New("nado: invalid side")
	ErrInvalidOrderType    = errors.New("nado: invalid order type")
	ErrNotAuthenticated    = errors.New("nado: not authenticated")
	ErrTimeout             = errors.New("nado: timeout")
	ErrCredentialsRequired = errors.New("nado: credentials required")
	// ErrExecutionRejected marks a structured gateway business response that
	// proves the execution command was rejected before acceptance.
	ErrExecutionRejected = errors.New("nado: execution rejected")
)

// GatewayApplicationError preserves structured gateway response semantics.
// It is not automatically an execution rejection: query failures, missing
// codes, and temporary/internal code families remain ambiguous.
type GatewayApplicationError struct {
	Code        int
	Message     string
	RequestType string
}

func NewGatewayApplicationError(code int, message, requestType string) error {
	return &GatewayApplicationError{Code: code, Message: strings.TrimSpace(message), RequestType: strings.TrimSpace(requestType)}
}

func (e *GatewayApplicationError) Error() string {
	if e == nil {
		return "nado gateway application error"
	}
	return fmt.Sprintf("nado gateway application error (%d) for %s: %s", e.Code, e.RequestType, e.Message)
}

func (e *GatewayApplicationError) Unwrap() error {
	if e != nil && isExecutionRequestType(e.RequestType) && isConclusiveExecutionRejectionCode(e.Code) {
		return ErrExecutionRejected
	}
	return nil
}

func isExecutionRequestType(requestType string) bool {
	switch strings.TrimSpace(requestType) {
	case "place_order", "cancel_orders", "cancel_product_orders", "cancel_and_place",
		"execute_place_order", "execute_cancel_orders", "execute_cancel_and_place":
		return true
	default:
		return false
	}
}

func isConclusiveExecutionRejectionCode(code int) bool {
	// These validation codes have been observed from the official gateway
	// before command acceptance: invalid price grid (2000), inactive product
	// (2001), and below-minimum order size (2094).
	switch code {
	case 2000, 2001, 2094:
		return true
	default:
		return false
	}
}
