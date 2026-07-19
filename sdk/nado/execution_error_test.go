package nado

import (
	"errors"
	"testing"
)

func TestGatewayApplicationErrorClassifiesOnlyConclusiveExecutionCodes(t *testing.T) {
	for _, requestType := range []string{
		"place_order", "cancel_orders", "cancel_product_orders", "cancel_and_place",
		"execute_place_order", "execute_cancel_orders", "execute_cancel_and_place",
	} {
		for _, code := range []int{2000, 2001, 2094} {
			rejected := NewGatewayApplicationError(code, "command validation failed", requestType)
			if !errors.Is(rejected, ErrExecutionRejected) {
				t.Errorf("request_type=%q code=%d err=%v, want ErrExecutionRejected", requestType, code, rejected)
			}
		}
	}
	for _, err := range []error{
		NewGatewayApplicationError(0, "malformed failure", "place_order"),
		NewGatewayApplicationError(2500, "unproven application failure", "place_order"),
		NewGatewayApplicationError(5001, "internal error", "place_order"),
		NewGatewayApplicationError(2001, "product is not active", "query_max_order_size"),
		NewGatewayApplicationError(2500, "unproven application failure", "execute_place_order"),
		NewGatewayApplicationError(2500, "unproven application failure", "execute_cancel_orders"),
		NewGatewayApplicationError(2500, "unproven application failure", "execute_cancel_and_place"),
	} {
		if errors.Is(err, ErrExecutionRejected) {
			t.Fatalf("ambiguous/non-command err=%v classified as execution rejection", err)
		}
	}
}
